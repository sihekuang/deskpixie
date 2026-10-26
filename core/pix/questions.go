package pix

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	// read reads its dialog off the screen; held: its event, not sent yet: it waits for its
	// dialog to be read (or dialogWait), so DeskPixie gets the dialog as shown. optKeys: what
	// answers each of the dialog's options (nil: its number).
	read    func([]string) (dialog, bool)
	held    *wire.Msg
	optKeys []string
	// hold: its hook waits for Pixie's answer, sent as the hook's reply (#268). labels: the
	// options Pixie shows; input: the menu's tool input; suggestions: a permission's rules.
	hold        *hold
	labels      []string
	input       map[string]any
	suggestions any
	// replyMenu: a held menu's hook reply for an answer, as its agent takes it.
	replyMenu func(input map[string]any, a menuAnswer) (string, bool)
	// agent: the subagent whose hook asked it ("": the session's own, #344); tool: the tool it
	// asks about; use: a permission's tool call (its name and input), as its PostToolUse names
	// it again.
	agent, tool, use string
	// told: its event as DeskPixie last had it (nil: not told yet), to ask it again (#347).
	told *wire.Msg
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

// menuAnswer is Pixie's answer to a held menu: an option's label, your own words (own), or
// that you want to talk it over first (chat).
type menuAnswer struct {
	label     string
	own, chat bool
}

// menuTool is an agent's question tool, as pix holds it (#268): its name in the hook's payload,
// the marker its menu shows on screen, whether pix can answer that menu by keys when it isn't
// held, and the hook reply that answers it. Another agent's is another entry.
type menuTool struct {
	name   string
	marker string
	keys   bool
	reply  func(input map[string]any, a menuAnswer) (string, bool)
}

var menuTools = map[string]menuTool{
	"claude": {name: "AskUserQuestion", marker: claudeMenu, keys: true, reply: claudeMenuReply},
	"codex":  {name: "request_user_input", marker: codexMenu, reply: codexMenuReply},
}

// hold is a question held in its hook (#268): the hook returns its reply, "" to let it go.
type hold struct {
	once  sync.Once
	reply chan string
}

func newHold() *hold { return &hold{reply: make(chan string, 1)} }

// give hands the hook its reply; false when it already had one.
func (h *hold) give(r string) (given bool) {
	h.once.Do(func() { h.reply <- r; given = true })
	return given
}

// MaxHold is the longest a question waits in its hook for Pixie (#268); DeskPixie's setting
// stays within it, and the hooks that hold are given longer.
const MaxHold = 5 * time.Minute

// holds: the hooks whose question may wait for Pixie's answer.
func holds(event string) bool { return event == "PreToolUse" || event == "PermissionRequest" }

// setHold: how long a question waits in its hook for Pixie's answer, never past MaxHold (the
// hook's own deadlines are set from it).
func (s *Session) setHold(d time.Duration) {
	s.mu.Lock()
	s.hold = min(max(d, 0), MaxHold)
	s.mu.Unlock()
}

func (s *Session) holdFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hold
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
	s.client.Send(ev)
	return true
}

// offScreen: q showed on screen, whole, and is no longer there, so it was answered or went (#347).
// Under mu.
func (s *Session) offScreen(q *question) bool {
	return q.shown && q.marker != "" && !shows(s.screen, q.marker, q.detail)
}

// offScreenWait: how long after another tool ends pix looks again whether a question it
// leaves is still on screen (a redraw may be under way when the tool ends).
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

// await is the reply of held question q's hook h: Pixie's answer, or "" once wait passed (the
// terminal shows it) or it was answered or withdrawn elsewhere.
func (s *Session) await(q *question, h *hold, wait time.Duration) string {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r := <-h.reply:
		return r
	case <-t.C:
		s.unhold(q)
		return <-h.reply // "", or an answer given just before
	}
}

// unhold lets held question q's hook go once its wait passed: the terminal shows it now, and
// it still waits on you (#347). Pixie keeps it until it is answered or gone from the session:
// with its buttons where its keys answer it (as without a hold), else as asked in the terminal.
func (s *Session) unhold(q *question) {
	s.mu.Lock()
	h := q.hold
	mine := s.q == q
	keys := q.kind == "permission" || menuTools[s.o.tool].keys
	changed := false
	if mine {
		q.hold = nil
		if t := q.told; t != nil { // what answers it now: keys only, so no typed answer or chat
			changed = t.Typed || !keys
			t.Typed = false
			if !keys { // asked again without its buttons: answered in its terminal
				q.options, q.labels = 0, nil
				t.Keys, t.Details = nil, nil
			}
		}
	}
	s.mu.Unlock()
	h.give("")
	if mine {
		s.o.log.Printf("question %s: in the terminal now, still waiting", q.id)
	}
	if changed {
		s.retell(q.id)
	}
}

// letHeldGo lets a held question's hook go at once: DeskPixie is gone, so nobody answers it there.
func (s *Session) letHeldGo() {
	s.mu.Lock()
	q := s.q
	if q == nil || q.hold == nil {
		s.mu.Unlock()
		return
	}
	s.q = nil
	s.mu.Unlock()
	q.hold.give("")
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
		q.options, q.optKeys, q.labels = len(d.Options), d.Keys, d.Options
	}
	told := ev
	q.told = &told
	s.mu.Unlock()
	s.client.Send(ev)
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
	var h *hold
	if q != nil {
		h = q.hold
	}
	told := q != nil && q.held == nil // a held one was never told
	s.mu.Unlock()
	if q == nil {
		return
	}
	s.o.log.Printf("question %s gone: %s", q.id, why)
	if h != nil {
		h.give("") // answered in the terminal, or the session moved on: its hook lets go
	}
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
	if q != nil && q.id == id && q.hold != nil {
		if r, ok := q.hookAnswer(choice); ok {
			s.q = nil
			h := q.hold
			s.mu.Unlock()
			if !h.give(r) {
				return wire.Msg{Err: "The question is no longer waiting."}
			}
			s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: id})
			return wire.Msg{}
		}
		if q.kind == "menu" {
			s.mu.Unlock()
			return wire.Msg{Err: "That isn't one of its answers."}
		}
		// A permission option its hook can't say: typed into its dialog, which shows meanwhile.
	}
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
	keys, ok := q.keys(s.o.tool, choice)
	if !ok {
		s.mu.Unlock()
		return wire.Msg{Err: "That isn't one of its answers."}
	}
	s.q = nil
	if q.hold != nil {
		q.hold.give("") // the keys answer it
		q.hold = nil    // and its hook has its reply: tried again, keys answer it again
	}
	if s.landing != nil {
		// An answer still checked whose prompt this one followed on screen: it left, so it landed.
		close(s.landing.done)
	}
	l := &landing{marker: q.marker, detail: q.detail, done: make(chan struct{})}
	s.landing = l
	s.mu.Unlock()
	if string(keys) == "\x1b" {
		s.stopKey() // Claude's No shows its interrupt and fires no Stop (#322)
	}
	if err := s.runner.WriteToAgent(keys); err != nil {
		s.dropLanding(l)
		return wire.Msg{Err: err.Error()}
	}
	// It counts once the prompt left the screen: keys the agent didn't take leave it there (#268).
	t := time.NewTimer(answerWait)
	defer t.Stop()
	select {
	case <-l.done:
	case <-t.C:
		s.dropLanding(l)
		s.mu.Lock()
		if s.q == nil { // still on screen: it still waits
			s.q = q
		}
		s.mu.Unlock()
		return wire.Msg{Err: "The session didn't take the answer."}
	}
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: id})
	return wire.Msg{}
}

// hookAnswer is the reply of held question q's hook that answers it with choice: a menu's
// option, a typed answer ("text:…") or "chat" goes back in its tool input's answers; a
// permission's Yes, No, and "don't ask again" (with the rules Claude suggested) are its
// decision. False for anything else.
func (q *question) hookAnswer(choice string) (string, bool) {
	label := choice
	if n, err := strconv.Atoi(choice); err == nil {
		if n < 1 || n > len(q.labels) {
			return "", false
		}
		label = q.labels[n-1]
	}
	if q.kind == "menu" {
		if typed, ok := strings.CutPrefix(choice, "text:"); ok && strings.TrimSpace(typed) != "" {
			return q.replyMenu(q.input, menuAnswer{label: strings.TrimSpace(typed), own: true})
		}
		if choice == "chat" {
			return q.replyMenu(q.input, menuAnswer{chat: true})
		}
		if _, err := strconv.Atoi(choice); err != nil {
			return "", false
		}
		return q.replyMenu(q.input, menuAnswer{label: label})
	}
	switch {
	case label == "yes" || label == "Yes":
		return permissionReply(map[string]any{"behavior": "allow"})
	case label == "no" || strings.HasPrefix(label, "No"):
		return permissionReply(map[string]any{"behavior": "deny", "message": "The user said no in Pixie."})
	case strings.Contains(label, "don't ask again") && q.suggestions != nil:
		return permissionReply(map[string]any{"behavior": "allow", "updatedPermissions": q.suggestions})
	}
	return "", false
}

// claudeMenuReply answers AskUserQuestion in its input's answers; "Chat about this" as its
// terminal's: an answer with a note, not a refusal (a deny reads as a hook error).
func claudeMenuReply(input map[string]any, a menuAnswer) (string, bool) {
	if a.chat {
		return menuReply(input, "Chat about this", "The user wants to talk this over before answering. Ask them what to clarify.")
	}
	return menuReply(input, a.label, "")
}

// codexMenuReply answers request_user_input: Codex's hook can't change the tool's input, so the
// answer is its refusal's reason, which Codex reads as the tool's result.
func codexMenuReply(input map[string]any, a menuAnswer) (string, bool) {
	text, _, _ := menuOf(input)
	var reason string
	switch {
	case a.chat:
		reason = fmt.Sprintf("The user wants to talk this over before answering %q. Ask them what to clarify; don't ask the question again until they have.", text)
	case a.own:
		reason = fmt.Sprintf("The user answered in Pixie: %q -> %q (their own answer, not one of the options). Continue with that answer; don't ask it again.", text, a.label)
	default:
		reason = fmt.Sprintf("The user answered in Pixie: %q -> %q. Continue with that answer; don't ask it again.", text, a.label)
	}
	return hookOutput(map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason})
}

// menuReply answers AskUserQuestion's one question with label (and notes, if any): its input as
// it came, plus answers.
func menuReply(input map[string]any, label, notes string) (string, bool) {
	qs, _ := input["questions"].([]any)
	if len(qs) == 0 {
		return "", false
	}
	first, _ := qs[0].(map[string]any)
	text, _ := first["question"].(string)
	in := map[string]any{}
	for k, v := range input {
		in[k] = v
	}
	in["answers"] = map[string]any{text: label}
	if notes != "" {
		in["annotations"] = map[string]any{text: map[string]any{"notes": notes}}
	}
	return hookOutput(map[string]any{"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": in})
}

func permissionReply(decision map[string]any) (string, bool) {
	return hookOutput(map[string]any{"hookEventName": "PermissionRequest", "decision": decision})
}

func hookOutput(specific map[string]any) (string, bool) {
	b, err := json.Marshal(map[string]any{"hookSpecificOutput": specific})
	return string(b), err == nil
}

// answerWait: how long an answer typed from Pixie has to take its prompt off the screen.
var answerWait = 3 * time.Second

// landing is an answer typed from Pixie: done closes when its prompt is off the screen.
type landing struct {
	marker, detail string
	done           chan struct{}
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
	if q := s.q; q != nil && q.seen && q.marker != "" && shows(text, q.marker, q.detail) {
		q.shown = true
	}
	if l := s.landing; l != nil && !shows(text, l.marker, l.detail) {
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

// questionWatch notes when the waiting prompt shows on screen: its marker and its detail, both
// shown after it was asked (PTY reader goroutine only).
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
		if !strings.Contains(w.tail, noSpace(q.marker)) || !strings.Contains(w.tail, noSpace(q.detail)) {
			return
		}
		s.mu.Lock()
		if s.q == q {
			q.seen = true
			q.shown = shows(s.screen, q.marker, q.detail)
		}
		s.mu.Unlock()
	}
	if held && q.read != nil { // shown: read the dialog as it is drawn now
		if d, ok := q.read(vt.Lines()); ok {
			s.sendHeld(q, &d)
		}
	}
}

// menuOf is the question of an AskUserQuestion input and its options when a digit answers it:
// one question, one pick, at most 8 options (Claude adds "Type something" after them). Else
// no options: you answer it in the terminal.
func menuOf(input any) (string, []string, []string) {
	m, _ := input.(map[string]any)
	qs, _ := m["questions"].([]any)
	if len(qs) == 0 {
		return "", nil, nil
	}
	first, _ := qs[0].(map[string]any)
	text, _ := first["question"].(string)
	if len(qs) > 1 {
		return fmt.Sprintf("%s (and %d more)", text, len(qs)-1), nil, nil
	}
	multi, _ := first["multiSelect"].(bool)
	secret, _ := first["isSecret"].(bool) // Codex: typed in its terminal only
	if multi || secret {
		return text, nil, nil
	}
	opts, _ := first["options"].([]any)
	var labels, details []string
	for _, o := range opts {
		om, _ := o.(map[string]any)
		label, _ := om["label"].(string)
		if label == "" {
			return text, nil, nil
		}
		detail, _ := om["description"].(string)
		labels, details = append(labels, label), append(details, detail)
	}
	if len(labels) > 8 {
		return text, nil, nil
	}
	return text, labels, details
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
