package pix

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// Prompts a session waits on (#105). pix tells DeskPixie what is asked (Claude's hooks, or
// Codex's screen), and types your answer from Pixie only while the prompt is still waiting:
// on screen, and with no key of yours pressed since it showed (then you are answering it).

// Screen markers of the prompts, as each agent shows them.
const (
	claudeMenu       = "Enter to select"
	codexMenu        = "enter to submit answer"
	claudePermission = "Do you want to" // "… proceed?", "… make this edit to x?", "… create x?"
)

// claudeAnswerable: the tools whose permission dialog's first option is a plain Yes, so "1"
// answers what Pixie asked (others, e.g. ExitPlanMode's "Yes, and auto-accept edits", are only
// announced).
func claudeAnswerable(tool string) bool {
	switch tool {
	case "Bash", "Edit", "Write", "MultiEdit", "NotebookEdit", "WebFetch":
		return true
	}
	return strings.HasPrefix(tool, "mcp__")
}

// claudeDetail is text of this request that its dialog shows (the command, the file's name,
// the URL's host, the MCP tool's name): with the marker, it tells this dialog from another's.
func claudeDetail(tool string, input any) string {
	m, _ := input.(map[string]any)
	str := func(k string) string { v, _ := m[k].(string); return v }
	switch tool {
	case "Bash":
		return firstRunes(str("command"), 24)
	case "Edit", "Write", "MultiEdit":
		return filepath.Base(str("file_path"))
	case "NotebookEdit":
		return filepath.Base(str("notebook_path"))
	case "WebFetch":
		if u, err := url.Parse(str("url")); err == nil {
			return u.Host
		}
		return ""
	}
	if i := strings.LastIndex(tool, "__"); i >= 0 {
		return tool[i+2:]
	}
	return ""
}

func firstRunes(s string, n int) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > n {
		return string(r[:n])
	}
	return strings.TrimSpace(s)
}

// codexApproval is what a Codex OSC 9 notification asks to do, if it is an approval prompt
// (its messages: "Approval requested: <command>", "Codex wants to edit <file>", "Approval
// requested by <server>"); "" for any other notification.
func codexApproval(msg string) string {
	switch {
	case strings.HasPrefix(msg, "Approval requested: "):
		return "wants to run " + strings.TrimPrefix(msg, "Approval requested: ")
	case strings.HasPrefix(msg, "Codex wants to edit "):
		return "wants to edit " + strings.TrimPrefix(msg, "Codex wants to edit ")
	case strings.HasPrefix(msg, "Approval requested by "):
		return "asks for approval for " + strings.TrimPrefix(msg, "Approval requested by ")
	}
	return ""
}

// osc9Watch collects OSC 9 notifications (ESC ] 9 ; message BEL or ST) from the agent's
// output, across chunks (PTY reader goroutine only).
type osc9Watch struct{ partial string }

func (w *osc9Watch) messages(out string) []string {
	text := w.partial + out
	w.partial = ""
	var msgs []string
	for {
		at := strings.Index(text, "\x1b]9;")
		if at < 0 {
			return msgs
		}
		rest := text[at+4:]
		end := strings.IndexAny(rest, "\x07\x1b")
		if end < 0 {
			if len(rest) < 512 {
				w.partial = text[at:]
			}
			return msgs
		}
		msgs = append(msgs, rest[:end])
		text = rest[end:]
	}
}

// question is the prompt waiting on you that Pixie may answer.
type question struct {
	id      string
	kind    string // "permission" or "menu"
	options int    // menu: how many options a digit picks from (0: answered in the terminal)
	marker  string // shown while it waits ("" when it was seen on screen to begin with)
	detail  string // shown with it: this request's own text, so another dialog doesn't count
	seen    bool   // its marker and detail showed since it was asked
	shown   bool   // and both were on the screen at once (a tall dialog's top can scroll off)
	// markerAt: when its marker was last on the screen, once shown (a smaller pane can scroll
	// its text off while its options and marker stay).
	markerAt time.Time
	// read reads its dialog off the screen; held: its event, not sent yet: it waits for its
	// dialog to be read (or dialogWait), so DeskPixie gets the dialog as shown. optKeys: what
	// answers each of the dialog's options (nil: its number).
	read    func([]string) (dialog, bool)
	held    *wire.Msg
	optKeys []string
	// agent: the subagent whose hook asked it ("": the session's own, #344); tool: the tool it
	// asks about; use: a permission's tool call (its name and input), as its PostToolUse names
	// it again.
	agent, tool, use string
	// told: its event as DeskPixie last had it (nil: not told yet), to ask it again (#347).
	told *wire.Msg
	// why: a menu Pixie shows no buttons for, why (for the log, #363).
	why string
	// part: the menu's question it is, next the ones after it (#364), and said what the ones
	// before it were answered, for its review.
	part menuPart
	next []menuPart
	said []string
}

// toolUse is the tool call a hook's payload is about: its name and input, as JSON (map keys
// sorted, so the same call reads the same in every hook).
func toolUse(p map[string]any) string {
	b, _ := json.Marshal([]any{p["tool_name"], p["tool_input"]})
	return string(b)
}

// endsWith: the PostToolUse hook p is the end of the very tool call q waits on (#344, #347): a
// permission's call (whichever agent's hook it came in, as a subagent's dialog may come
// without its agent_id), or the menu tool of the agent that asked it. Any other tool that
// finishes meanwhile leaves q waiting: a background subagent's, or one the agent ran beside it.
func (q *question) endsWith(p map[string]any) bool {
	if q.use != "" {
		return q.use == toolUse(p)
	}
	name, _ := p["tool_name"].(string)
	return q.kind == "menu" && q.tool != "" && q.tool == name && q.agent == agentOf(p)
}

// menuTool is an agent's question tool (#268): its name in the hook's payload, the marker its
// menu shows on screen, and the keys that answer its menu of n options as you would in its
// terminal (#359): with your own words, or saying you want to talk it over first (chat; nil:
// it can't be). An option is its digit, which picks it in either agent's menu. Another
// agent's is another entry.
//
// A menu of several questions asks them in turn (#364), each answered as its agent goes on to
// the next. picks: the keys that pick several of a question's options (nil: it takes no such
// question, answered in the terminal); review: the agent ends a menu of tabs (several
// questions, or one of several picks) with a review whose first option sends the answers.
// chatEnds: talking a question over ends the menu (Claude's "Chat about this"; Codex's is a note
// that answers it, and goes on to the next). last: what the last of several questions shows in
// place of marker ("" marker); tab: what the
// screen shows of question k (from 0) of several, by their headers, and only while it waits:
// its text may show elsewhere too (your prompt), and the marker stays for the next one; ticked:
// what it shows in its place once you ticked one of its several picks.
type menuTool struct {
	name     string
	marker   string
	own      func(n int, text string) []step
	chat     func(n int) []step
	picks    func(labels []string, picks []int) []step
	review   bool
	chatEnds bool
	last     string
	tab      func(headers []string, k int) string
	ticked   func(headers []string, k int) string
}

var menuTools = map[string]menuTool{
	"claude": {name: "AskUserQuestion", marker: claudeMenu, own: claudeOwn, chat: claudeChat, picks: claudePicks, review: true, chatEnds: true,
		tab: claudeTab, ticked: claudeTicked},
	"codex": {name: "request_user_input", marker: codexMenu, own: codexOwn, chat: codexChat, last: "enter to submit all",
		tab: func(headers []string, k int) string { return fmt.Sprintf("Question %d/%d", k+1, len(headers)) }},
}

// menuPart is one question of a menu as Pixie asks it: what it asks, as its menu shows it, and
// title as Pixie shows it (which one of how many); its options and their descriptions; multi:
// you pick several. marker and detail: what its page shows in place of its tool's marker and
// its text ("" those); ticked: what it shows in place of detail once one of its boxes is ticked.
type menuPart struct {
	text, title     string
	labels, details []string
	multi, review   bool
	marker, detail  string
	ticked          string
}

// claudeTab: Claude Code 2.1.311's tabs, "←  ☒ Color  ☐ Size  ✔ Submit  →", up to question k's
// while it waits: those before it answered (☒), its own not yet (☐). "" when one has no header.
func claudeTab(headers []string, k int) string {
	if slices.Contains(headers[:k+1], "") {
		return ""
	}
	tab := "←"
	for _, h := range headers[:k] {
		tab += "☒" + h
	}
	return tab + "☐" + headers[k]
}

// claudeTicked: Claude marks a tab of several picks answered (☒) as soon as one is ticked.
func claudeTicked(headers []string, k int) string {
	tab := claudeTab(headers, k)
	if tab == "" {
		return ""
	}
	return strings.TrimSuffix(tab, "☐"+headers[k]) + "☒" + headers[k]
}

// claudeReview is Claude Code 2.1.311's page after a menu's tabs: "Submit answers" sends them,
// "Cancel" drops the menu.
var claudeReview = menuPart{text: "Ready to submit your answers?", title: "Ready to submit your answers?",
	labels: []string{"Submit answers", "Cancel"}, details: []string{"", ""}, review: true, marker: "Review your answers",
	detail: noSpace("Ready to submit your answers?")}

// answer is choice as the part's review shows it: the option, the options, or your words.
func (p menuPart) answer(choice string) string {
	if typed, ok := strings.CutPrefix(choice, "text:"); ok {
		return strings.Join(strings.Fields(typed), " ")
	}
	if picks, ok := strings.CutPrefix(choice, "picks:"); ok {
		ns, _ := pickList(picks, len(p.labels))
		names := make([]string, len(ns))
		for i, n := range ns {
			names[i] = p.labels[n-1]
		}
		return strings.Join(names, ", ")
	}
	if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= len(p.labels) {
		return p.labels[n-1]
	}
	return choice
}

// pickList reads "1,3" as the options picked, of n: each once, in order, at least one.
func pickList(s string, n int) ([]int, bool) {
	var ns []int
	for _, f := range strings.Split(s, ",") {
		k, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || k < 1 || k > n || slices.Contains(ns, k) {
			return nil, false
		}
		ns = append(ns, k)
	}
	slices.Sort(ns)
	return ns, len(ns) > 0
}

// claudePicks: in Claude's question of several picks a digit ticks its option, and Tab goes on
// to the next question (or the review).
func claudePicks(labels []string, picks []int) []step {
	var steps []step
	for _, n := range picks {
		digit := strconv.Itoa(n)
		steps = append(steps, step{[]byte(digit), noSpace(digit+". [✔] ") + drawn(labels[n-1])})
	}
	return append(steps, step{[]byte("\t"), ""})
}

// step is keys typed into a question, and what the screen (without spaces) shows once the
// agent took them ("": nothing to wait for), before the next step is typed.
type step struct {
	keys  []byte
	shows string
}

// claudeOwn: Claude Code 2.1.310 numbers "Type something." after a menu's options: its digit
// moves there and opens it for your words, drawn in its place, which Enter sends.
func claudeOwn(n int, text string) []step {
	digit := strconv.Itoa(n + 1)
	return []step{{[]byte(digit), noSpace("❯ " + digit + ". Type something.")},
		{[]byte(text), noSpace("❯ "+digit+". ") + drawn(text)}, {[]byte("\r"), ""}}
}

// claudeChat: "Chat about this" comes after "Type something."; its digit picks it. A digit
// goes to 9 only.
func claudeChat(n int) []step {
	if n+2 > 9 {
		return nil
	}
	return []step{{[]byte(strconv.Itoa(n + 2)), ""}}
}

// codexOwn: Codex 0.164 adds "None of the above" after a menu's options, its digit picking it
// as it is: Down moves there from the first option, Tab opens its notes for your words, and
// Enter sends both.
func codexOwn(n int, text string) []step {
	other := strconv.Itoa(n + 1)
	return []step{{[]byte(strings.Repeat("\x1b[B", n)), noSpace("› " + other + ". None of the above")},
		{[]byte("\t"), noSpace("› Add notes")}, {[]byte(text), "›" + drawn(text)}, {[]byte("\r"), ""}}
}

// drawn is how the screen (without spaces) shows the start of text: the runes it draws (none
// of width 0, such as combining marks and joiners), as far as a line surely holds them.
func drawn(text string) string {
	shown := strings.Map(func(r rune) rune {
		if runeWidth(r) == 0 {
			return -1
		}
		return r
	}, text)
	return noSpace(firstRunes(shown, 24))
}

// codexChat: Codex's menu has no way to talk it over, so its note says you want to.
func codexChat(n int) []step {
	return codexOwn(n, "I'd like to talk this over before answering. Ask me what to clarify; don't ask the question again until I have.")
}

// dialogWait: how long a Claude permission waits for its dialog before it is asked without it.
const dialogWait = 1500 * time.Millisecond

// ask makes q the prompt waiting on you and tells DeskPixie about it with ev. A prompt still
// waiting is never replaced (its dialog could be the one on screen): the new one is not asked.
// One that showed and is off the screen since went (#347): the new one takes its place.
func (s *Session) ask(q *question, ev wire.Msg) bool {
	s.mu.Lock()
	if s.q != nil && !s.offScreen(s.q) {
		s.mu.Unlock()
		return false
	}
	if old := s.q; old != nil {
		s.mu.Unlock()
		s.dropQuestion(old, "off screen")
		s.mu.Lock()
		if s.q != nil {
			s.mu.Unlock()
			return false
		}
	}
	s.qseq++
	q.id = "q" + strconv.Itoa(s.qseq)
	s.q = q
	// Claude draws its dialog as it asks, so the dialog can be on the screen before its hook
	// comes, and isn't drawn again after it (#355). Its own text there (its detail) says it is
	// this one; a prompt with no detail counts only once shown after it was asked.
	if q.marker != "" && q.detail != "" && shows(s.screen, q.marker, q.detail) {
		q.seen = true
		s.sawQuestion(q, s.screen)
	}
	ev.Kind, ev.Event, ev.ID = wire.KindEvent, "question", q.id
	if q.read != nil {
		q.held = &ev
		s.mu.Unlock()
		time.AfterFunc(dialogWait, func() { s.sendHeld(q, nil) })
		return true
	}
	told := ev
	q.told = &told
	s.mu.Unlock()
	s.logTold(q, ev)
	s.client.Send(ev)
	return true
}

// offScreen: q showed on screen whole, and no prompt of its kind (its marker) has been there
// for offScreenWait, so it was answered or went (#347). A redraw takes it off for a moment
// only, and a pane made smaller keeps its options and marker. Under mu.
func (s *Session) offScreen(q *question) bool {
	return q.shown && q.marker != "" && !strings.Contains(s.screen, noSpace(q.marker)) &&
		s.now().Sub(q.markerAt) >= offScreenWait
}

// sawQuestion notes what screen (without spaces) shows of q, once seen. Under mu.
func (s *Session) sawQuestion(q *question, screen string) {
	if q == nil || !q.seen || q.marker == "" {
		return
	}
	q.shown = q.shown || shows(screen, q.marker, q.detail)
	if q.shown && strings.Contains(screen, noSpace(q.marker)) {
		q.markerAt = s.now()
	}
}

// offScreenWait: how long a question's marker is off the screen before pix takes it for gone,
// and how far apart its two looks are after another tool ends (a redraw may be under way).
var offScreenWait = time.Second

// retell asks DeskPixie question id again as it is now, if it still waits (#347): its answer
// couldn't be typed (its card comes back), or what answers it changed.
func (s *Session) retell(id string) {
	s.mu.Lock()
	q := s.q
	if q == nil || q.id != id || q.told == nil {
		s.mu.Unlock()
		return
	}
	ev := *q.told
	s.mu.Unlock()
	s.o.log.Printf("question %s: asked again", id)
	s.client.Send(ev)
}

// sendHeld asks q's held event, with its dialog d as shown when it was read (nil: not read):
// Claude's summary, the box's text, the question and every option, a number answering each.
func (s *Session) sendHeld(q *question, d *dialog) {
	s.mu.Lock()
	if s.q != q || q.held == nil {
		s.mu.Unlock()
		return
	}
	ev := *q.held
	q.held = nil
	if d != nil {
		ev.Text, ev.Body, ev.Prompt, ev.Keys = d.Summary, d.Body, d.Question, d.Options
		q.options, q.optKeys = len(d.Options), d.Keys
	}
	told := ev
	q.told = &told
	s.mu.Unlock()
	s.logTold(q, ev)
	s.client.Send(ev)
}

// logTold notes question q as DeskPixie is told it with ev (#363): what it asks, and how
// Pixie can answer it.
func (s *Session) logTold(q *question, ev wire.Msg) {
	// A menu's question, but only a tool's name: its input (a command, a file) can hold a key,
	// and this log never holds what's sent to the agent.
	what := ev.ToolName
	if q.kind == "menu" {
		what = ev.Text
	}
	how := fmt.Sprintf("%d options", len(ev.Keys))
	switch {
	case len(ev.Keys) == 0 && ev.Source == "permission":
		how = "Allow or Deny" // its dialog not read off the screen
	case len(ev.Keys) == 0 && q.why != "":
		how = "no buttons (" + q.why + "): answered in its terminal"
	case len(ev.Keys) == 0:
		how = "no buttons: answered in its terminal"
	case ev.Multi:
		how += ", several picks"
	case ev.Typed:
		how += ", or your own words"
	}
	s.o.log.Printf("question %s asked: %s %q, %s", q.id, ev.Source, firstRunes(what, 60), how)
}

func (s *Session) currentQuestion() *question {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.q
}

// clearQuestion: the prompt was answered (here, or by you) or the session moved on; why says
// which (the hook or key that showed it), for the log.
func (s *Session) clearQuestion(why string) { s.dropQuestion(nil, why) }

// dropQuestion is clearQuestion of want only, if it still waits (nil: whichever does).
func (s *Session) dropQuestion(want *question, why string) {
	s.mu.Lock()
	q := s.q
	if want != nil && q != want {
		s.mu.Unlock()
		return
	}
	s.q = nil
	told := q != nil && q.held == nil // a held one was never told
	s.mu.Unlock()
	if q == nil {
		return
	}
	s.o.log.Printf("question %s gone: %s", q.id, why)
	if told {
		s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: q.id})
	}
}

// answer types choice ("yes", "no", or a menu option's number) into prompt id. When it can't
// and the prompt still waits, it is asked again: Pixie took it as answered (#347).
func (s *Session) answer(id, choice string) wire.Msg {
	r := s.answerOnce(id, choice)
	if r.Err != "" {
		s.retell(id)
	}
	return r
}

func (s *Session) answerOnce(id, choice string) wire.Msg {
	s.mu.Lock()
	q := s.q
	switch {
	case q == nil || q.id != id:
		s.mu.Unlock()
		return wire.Msg{Err: "The question is no longer waiting."}
	case !q.seen:
		s.mu.Unlock()
		return wire.Msg{Err: "The question isn't on screen yet."}
	case !shows(s.screen, q.marker, q.detail):
		// On screen now too, so its leaving the screen tells that the answer was taken (#268).
		s.mu.Unlock()
		return wire.Msg{Err: "The question isn't on screen."}
	}
	steps, ok := q.steps(s.o.tool, choice)
	if !ok {
		s.mu.Unlock()
		return wire.Msg{Err: "That isn't one of its answers."}
	}
	s.q = nil
	if s.landing != nil {
		// An answer still checked whose prompt this one followed on screen: it left, so it landed.
		close(s.landing.done)
	}
	l := &landing{marker: q.marker, detail: q.detail, done: make(chan struct{})}
	next := q.nextPart(s.o.tool, choice)
	if next != nil && next.part.detail != "" {
		l.nextMarker, l.nextDetail = next.part.shownAs(next.marker)
	}
	s.landing = l
	keys := s.yourKeys
	s.mu.Unlock()
	// still: the agent didn't take it, and it still waits (its card comes back, #347). After
	// some of its steps the menu isn't as it was, so it is finished in the terminal.
	still := func(typed int) wire.Msg {
		s.dropLanding(l)
		if len(steps) > 1 && typed > 0 {
			return s.leftHalfway(q, keys)
		}
		s.mu.Lock()
		if s.q == nil {
			s.q = q
		}
		s.mu.Unlock()
		return wire.Msg{Err: "The session didn't take the answer."}
	}
	for i, st := range steps {
		// Each step after the first goes into the menu as the last one left it: still on the
		// screen, and with no key of yours since (you'd be answering it yourself).
		if i > 0 && !s.untouched(q, keys) {
			return still(i)
		}
		if string(st.keys) == "\x1b" {
			s.stopKey() // Claude's No shows its interrupt and fires no Stop (#322)
		}
		if err := s.runner.WriteToAgent(st.keys); err != nil {
			s.dropLanding(l)
			return wire.Msg{Err: err.Error()}
		}
		if st.shows != "" && !s.screenShows(st.shows, stepWait) {
			return still(i + 1)
		}
	}
	// It counts once the prompt left the screen: keys the agent didn't take leave it there (#268).
	t := time.NewTimer(answerWait)
	defer t.Stop()
	select {
	case <-l.done:
	case <-t.C:
		return still(len(steps))
	}
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: id})
	if next != nil {
		s.askPart(next) // its agent shows it now (#364)
	}
	return wire.Msg{}
}

// untouched: question q is on the screen and you pressed no key since keys were counted.
func (s *Session) untouched(q *question, keys int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.yourKeys == keys && (shows(s.screen, q.marker, q.detail) || q.part.ticked != "" && shows(s.screen, q.marker, q.part.ticked))
}

// leftHalfway: an answer typed in steps stopped part-way, so q's menu is no longer as Pixie
// asked it (its field open, its pick moved). A key of yours meanwhile: you are answering it.
// Else it still waits, asked again without its buttons: you finish it in its terminal.
func (s *Session) leftHalfway(q *question, keys int) wire.Msg {
	s.mu.Lock()
	yours := s.yourKeys != keys
	if !yours && s.q == nil {
		s.q = q
		q.options, q.optKeys = 0, nil
		if t := q.told; t != nil {
			t.Keys, t.Details, t.Typed = nil, nil, false
		}
	}
	s.mu.Unlock()
	if yours {
		s.o.log.Printf("question %s gone: a key of yours, while Pixie answered", q.id)
		s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: q.id})
		return wire.Msg{Err: "You're answering it in its terminal."}
	}
	return wire.Msg{Err: "The session didn't take all of the answer: finish it in its terminal."}
}

// stepWait: how long one step of an answer has to show on the screen.
var stepWait = time.Second

// screenShows waits up to d for the screen (without spaces) to show text.
func (s *Session) screenShows(text string, d time.Duration) bool {
	for end := s.now().Add(d); ; time.Sleep(20 * time.Millisecond) {
		s.mu.Lock()
		shown := strings.Contains(s.screen, text)
		s.mu.Unlock()
		if shown {
			return true
		}
		if s.now().After(end) {
			return false
		}
	}
}

// answerWait: how long an answer typed from Pixie has to take its prompt off the screen.
var answerWait = 3 * time.Second

// landing is an answer typed from Pixie: done closes when its prompt is off the screen, or for
// a menu's question with one after it whose page tells it, when that page shows (#364): a tab
// may look answered while you still tick its boxes, and its mark stays on the next.
type landing struct {
	marker, detail         string
	nextMarker, nextDetail string
	done                   chan struct{}
}

func (l *landing) landed(screen string) bool {
	if l.nextDetail != "" {
		return shows(screen, l.nextMarker, l.nextDetail)
	}
	return !shows(screen, l.marker, l.detail)
}

// shows: screen (without spaces) has the prompt with marker and detail on it. The detail tells
// it from the next prompt with the same marker (another command's dialog right after it).
func shows(screen, marker, detail string) bool {
	return strings.Contains(screen, noSpace(marker)) && strings.Contains(screen, noSpace(detail))
}

// noteScreen keeps what vt shows and the menu on it (#306), and closes the typed answer's done
// once its prompt is no longer on it (PTY reader goroutine only).
func (s *Session) noteScreen(vt *screen) {
	lines := vt.Lines()
	text := noSpace(strings.Join(lines, ""))
	prompt := promptOf(s.o.tool, lines)
	s.mu.Lock()
	s.screen, s.prompt = text, prompt
	s.sawQuestion(s.q, text)
	if l := s.landing; l != nil && l.landed(text) {
		s.landing = nil
		close(l.done)
	}
	s.mu.Unlock()
	s.noteMenu(lines, text)
}

func (s *Session) dropLanding(l *landing) {
	s.mu.Lock()
	if s.landing == l {
		s.landing = nil
	}
	s.mu.Unlock()
}

// keys are what answers q with choice: a permission's Yes is Claude's first option (1) or
// Codex's y, its No is Escape (both); a menu's option, or a Claude dialog's read off the
// screen, is its digit (Claude picks on it).
func (q *question) keys(tool, choice string) ([]byte, bool) {
	switch {
	case q.kind == "permission" && choice == "no":
		return []byte{0x1b}, true
	case q.kind == "permission" && choice == "yes" && len(q.optKeys) > 0:
		return []byte(q.optKeys[0]), true
	case q.kind == "permission" && choice == "yes" && tool == "codex":
		return []byte("y"), true
	case q.kind == "permission" && choice == "yes":
		return []byte("1"), true
	case q.kind == "menu" || q.kind == "permission":
		if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= q.options {
			if n <= len(q.optKeys) {
				return []byte(q.optKeys[n-1]), true
			}
			return []byte(strconv.Itoa(n)), true
		}
	}
	return nil, false
}

// steps are what answers q with choice as you would in its terminal (#359): its keys, or a
// menu's answer in your own words ("text:…", on one line) or "chat", as its agent's menu takes
// them.
func (q *question) steps(tool, choice string) ([]step, bool) {
	if q.kind == "menu" && q.options > 0 {
		t := menuTools[tool]
		if q.part.multi {
			// Several picks, "picks:1,3" (#364): a single digit only ticks one.
			list, ok := strings.CutPrefix(choice, "picks:")
			picks, ok2 := pickList(list, q.options)
			if !ok || !ok2 || t.picks == nil {
				return nil, false
			}
			return t.picks(q.part.labels, picks), true
		}
		if q.part.review && !digits(choice) {
			return nil, false
		}
		if typed, ok := strings.CutPrefix(choice, "text:"); ok {
			typed = strings.Join(strings.Fields(typed), " ")
			return t.own(q.options, typed), typed != ""
		}
		if choice == "chat" {
			chat := t.chat(q.options)
			return chat, chat != nil
		}
	}
	keys, ok := q.keys(tool, choice)
	return []step{{keys: keys}}, ok
}

// digits: choice is an option's number, not your words or "chat".
func digits(choice string) bool {
	_, err := strconv.Atoi(choice)
	return err == nil
}

// askPart asks q, the menu question q.part (#364): what DeskPixie shows for it, and, on its
// review, what the questions before it were answered.
func (s *Session) askPart(q *question) {
	p := q.part
	q.options = len(p.labels)
	q.marker, q.detail = p.shownAs(q.marker)
	title := p.title
	if p.review && len(q.said) > 0 {
		title += " " + strings.Join(q.said, " · ")
	}
	s.ask(q, wire.Msg{Source: "menu", Text: title, Keys: p.labels, Details: p.details,
		Typed: len(p.labels) > 0 && !p.multi && !p.review, Multi: p.multi})
}

// shownAs: what the screen shows while part p waits, its menu's marker being marker.
func (p menuPart) shownAs(marker string) (string, string) {
	detail := firstRunes(p.text, 24)
	if p.detail != "" {
		detail = p.detail
	}
	if p.marker != "" {
		marker = p.marker
	}
	return marker, detail
}

// nextPart: the menu's next question once q was answered with choice in tool's menu, as it went
// on to it (nil: none, or the answer ended the menu: talking it over first, in Claude's).
func (q *question) nextPart(tool, choice string) *question {
	if q.kind != "menu" || len(q.next) == 0 || choice == "chat" && menuTools[tool].chatEnds {
		return nil
	}
	said := append(slices.Clone(q.said), q.part.text+" → "+q.part.answer(choice))
	return &question{kind: "menu", marker: q.marker, agent: q.agent, tool: q.tool, part: q.next[0], next: q.next[1:], said: said}
}

// questionWatch notes when the waiting prompt shows on screen: its marker and its detail, both
// shown after it was asked, or on the screen whole (PTY reader goroutine only).
type questionWatch struct {
	tail string
	for_ *question
}

func (w *questionWatch) watch(s *Session, out string, vt *screen) {
	q := s.currentQuestion()
	if q == nil || q.marker == "" {
		return
	}
	if w.for_ != q { // a new prompt: only what is shown after it was asked counts
		w.for_, w.tail = q, ""
	}
	s.mu.Lock()
	seen, held := q.seen, q.held != nil
	s.mu.Unlock()
	if !seen {
		w.tail += noSpace(ansi.ReplaceAllString(out, ""))
		if len(w.tail) > 8192 {
			w.tail = w.tail[len(w.tail)-8192:]
		}
		// Claude redraws only the cells that change, so a dialog drawn over a row that showed
		// most of its text may never be written whole: the screen shows it whole. As when it is
		// asked, only by its own text there (its detail).
		s.mu.Lock()
		onScreen := q.detail != "" && shows(s.screen, q.marker, q.detail)
		s.mu.Unlock()
		if !onScreen && (!strings.Contains(w.tail, noSpace(q.marker)) || !strings.Contains(w.tail, noSpace(q.detail))) {
			return
		}
		s.mu.Lock()
		if s.q == q {
			q.seen = true
			s.sawQuestion(q, s.screen)
		}
		s.mu.Unlock()
	}
	if held && q.read != nil { // shown: read the dialog as it is drawn now
		if d, ok := q.read(vt.Lines()); ok {
			s.sendHeld(q, &d)
		}
	}
}

// menuParts are the questions of a menu tool's input t (AskUserQuestion, request_user_input),
// asked in turn with their options when digits answer them (#364): one pick, or several where
// the agent takes them, of at most 8 options (Claude adds "Type something" after them), then
// the agent's review. Else one, the first question, without options, and why (for the log,
// #363): you answer it in the terminal.
func menuParts(t menuTool, input any) ([]menuPart, string) {
	m, _ := input.(map[string]any)
	qs, _ := m["questions"].([]any)
	if len(qs) == 0 {
		return nil, "no question"
	}
	var parts []menuPart
	var headers []string
	tabs := len(qs) > 1
	for i, x := range qs {
		qm, _ := x.(map[string]any)
		text, _ := qm["question"].(string)
		header, _ := qm["header"].(string)
		headers = append(headers, header)
		multi, _ := qm["multiSelect"].(bool)
		secret, _ := qm["isSecret"].(bool) // Codex: typed in its terminal only
		p := menuPart{text: text, title: text, multi: multi}
		if len(qs) > 1 {
			p.title = fmt.Sprintf("%s (%d of %d)", text, i+1, len(qs))
		}
		why := ""
		switch {
		case multi && t.picks == nil:
			why = "several picks"
		case secret:
			why = "a secret"
		}
		opts, _ := qm["options"].([]any)
		for _, o := range opts {
			om, _ := o.(map[string]any)
			label, _ := om["label"].(string)
			if label == "" && why == "" {
				why = "an option without a label"
			}
			detail, _ := om["description"].(string)
			p.labels, p.details = append(p.labels, label), append(p.details, detail)
		}
		switch {
		case why != "":
		case len(p.labels) > 8:
			why = fmt.Sprintf("%d options", len(p.labels))
		case len(p.labels) == 0:
			why = "no options"
		}
		if why != "" {
			first, _ := qs[0].(map[string]any)
			text, _ := first["question"].(string)
			if len(qs) > 1 {
				text = fmt.Sprintf("%s (and %d more)", text, len(qs)-1)
			}
			return []menuPart{{text: text, title: text}}, why
		}
		tabs = tabs || multi
		parts = append(parts, p)
	}
	if len(parts) > 1 {
		parts[len(parts)-1].marker = t.last
	}
	for k := range parts {
		if tabs && t.tab != nil {
			parts[k].detail = noSpace(t.tab(headers, k))
		}
		if parts[k].multi && t.ticked != nil {
			parts[k].ticked = noSpace(t.ticked(headers, k))
		}
	}
	if tabs && t.review {
		parts = append(parts, claudeReview)
	}
	return parts, ""
}

// onlyReports: b is the terminal reporting focus or the mouse, not a key of yours.
func onlyReports(b []byte) bool { return len(withoutReports(b)) == 0 }

// screenFor is vt, or a fresh screen when there is none or the terminal was resized (the agent
// then draws it all again). PTY reader goroutine only.
func (s *Session) screenFor(vt *screen) *screen {
	if rows, cols := s.runner.Size(); vt == nil || vt.rows != rows || vt.cols != cols {
		if vt != nil {
			s.mu.Lock()
			s.freshAt = s.now() // a menu shown before is drawn again (#306)
			s.mu.Unlock()
		}
		return newScreen(rows, cols)
	}
	return vt
}
