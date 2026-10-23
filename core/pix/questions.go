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

func (s *Session) setHold(d time.Duration) {
	s.mu.Lock()
	s.hold = d
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
func (s *Session) ask(q *question, ev wire.Msg) bool {
	s.mu.Lock()
	if s.q != nil {
		s.mu.Unlock()
		return false
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
	s.mu.Unlock()
	s.client.Send(ev)
	return true
}

// await is the reply of held question q's hook: Pixie's answer, or "" once wait passed (its
// bubble goes and the terminal shows it) or it was answered or withdrawn elsewhere.
func (s *Session) await(q *question, wait time.Duration) string {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r := <-q.hold.reply:
		return r
	case <-t.C:
		s.withdraw(q)
		return <-q.hold.reply // "", or an answer given just before
	}
}

// withdraw takes q back if it still waits, lets its hook go, and tells DeskPixie it's gone.
func (s *Session) withdraw(q *question) {
	s.mu.Lock()
	mine := s.q == q
	if mine {
		s.q = nil
	}
	told := mine && q.held == nil
	s.mu.Unlock()
	q.hold.give("")
	if told {
		s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: q.id})
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
	s.mu.Unlock()
	s.client.Send(ev)
}

func (s *Session) currentQuestion() *question {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.q
}

// clearQuestion: the prompt was answered (here, or by you) or the session moved on.
func (s *Session) clearQuestion() {
	s.mu.Lock()
	q := s.q
	s.q = nil
	told := q != nil && q.held == nil // a held one was never told
	s.mu.Unlock()
	if q != nil && q.hold != nil {
		q.hold.give("") // answered in the terminal, or the session moved on: its hook lets go
	}
	if told {
		s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: q.id})
	}
}

// answer types choice ("yes", "no", or a menu option's number) into prompt id.
func (s *Session) answer(id, choice string) wire.Msg {
	s.mu.Lock()
	q := s.q
	if q != nil && q.id == id && q.hold != nil {
		if r, ok := q.hookAnswer(choice); ok {
			s.q = nil
			s.mu.Unlock()
			if !q.hold.give(r) {
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
	}
	if s.landing != nil {
		// An answer still checked whose prompt this one followed on screen: it left, so it landed.
		close(s.landing.done)
	}
	l := &landing{marker: q.marker, detail: q.detail, done: make(chan struct{})}
	s.landing = l
	s.mu.Unlock()
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
			return menuReply(q.input, strings.TrimSpace(typed), "")
		}
		if choice == "chat" { // as its terminal's "Chat about this": an answer, not a refusal
			return menuReply(q.input, "Chat about this", "The user wants to talk this over before answering. Ask them what to clarify.")
		}
		if _, err := strconv.Atoi(choice); err != nil {
			return "", false
		}
		return menuReply(q.input, label, "")
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

// noteScreen keeps what vt shows and closes the typed answer's done once its prompt is no
// longer on it (PTY reader goroutine only).
func (s *Session) noteScreen(vt *screen) {
	text := noSpace(strings.Join(vt.Lines(), ""))
	s.mu.Lock()
	defer s.mu.Unlock()
	s.screen = text
	if l := s.landing; l != nil && !shows(text, l.marker, l.detail) {
		s.landing = nil
		close(l.done)
	}
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
	if multi, _ := first["multiSelect"].(bool); multi {
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
		return newScreen(rows, cols)
	}
	return vt
}
