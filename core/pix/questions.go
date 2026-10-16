package pix

import (
	"bytes"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

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
}

// ask makes q the prompt waiting on you and tells DeskPixie about it with ev. A prompt still
// waiting is never replaced (its dialog could be the one on screen): the new one is not asked.
func (s *Session) ask(q *question, ev wire.Msg) {
	s.mu.Lock()
	if s.q != nil {
		s.mu.Unlock()
		return
	}
	s.qseq++
	q.id = "q" + strconv.Itoa(s.qseq)
	s.q = q
	s.mu.Unlock()
	ev.Kind, ev.Event, ev.ID = wire.KindEvent, "question", q.id
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
	s.mu.Unlock()
	if q != nil {
		s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: q.id})
	}
}

// answer types choice ("yes", "no", or a menu option's number) into prompt id.
func (s *Session) answer(id, choice string) wire.Msg {
	s.mu.Lock()
	q := s.q
	switch {
	case q == nil || q.id != id:
		s.mu.Unlock()
		return wire.Msg{Err: "The question is no longer waiting."}
	case !q.seen:
		s.mu.Unlock()
		return wire.Msg{Err: "The question isn't on screen yet."}
	}
	keys, ok := q.keys(s.o.tool, choice)
	if !ok {
		s.mu.Unlock()
		return wire.Msg{Err: "That isn't one of its answers."}
	}
	s.q = nil
	s.mu.Unlock()
	if err := s.runner.WriteToAgent(keys); err != nil {
		return wire.Msg{Err: err.Error()}
	}
	s.client.Send(wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: id})
	return wire.Msg{}
}

// keys are what answers q with choice: a permission's Yes is Claude's first option (1) or
// Codex's y, its No is Escape (both); a menu's option is its digit (Claude picks and submits
// on it).
func (q *question) keys(tool, choice string) ([]byte, bool) {
	switch {
	case q.kind == "permission" && choice == "no":
		return []byte{0x1b}, true
	case q.kind == "permission" && choice == "yes" && tool == "codex":
		return []byte("y"), true
	case q.kind == "permission" && choice == "yes":
		return []byte("1"), true
	case q.kind == "menu":
		if n, err := strconv.Atoi(choice); err == nil && n >= 1 && n <= q.options {
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

func (w *questionWatch) watch(s *Session, out string) {
	q := s.currentQuestion()
	if q == nil || q.marker == "" || q.seen {
		return
	}
	if w.for_ != q { // a new prompt: only what is shown after it was asked counts
		w.for_, w.tail = q, ""
	}
	w.tail += noSpace(ansi.ReplaceAllString(out, ""))
	if len(w.tail) > 8192 {
		w.tail = w.tail[len(w.tail)-8192:]
	}
	if strings.Contains(w.tail, noSpace(q.marker)) && strings.Contains(w.tail, noSpace(q.detail)) {
		s.mu.Lock()
		if s.q == q {
			q.seen = true
		}
		s.mu.Unlock()
	}
}

// menuOf is the question of an AskUserQuestion input and its options when a digit answers it:
// one question, one pick, at most 8 options (Claude adds "Type something" after them). Else
// no options: you answer it in the terminal.
func menuOf(input any) (string, []string) {
	m, _ := input.(map[string]any)
	qs, _ := m["questions"].([]any)
	if len(qs) == 0 {
		return "", nil
	}
	first, _ := qs[0].(map[string]any)
	text, _ := first["question"].(string)
	if len(qs) > 1 {
		return fmt.Sprintf("%s (and %d more)", text, len(qs)-1), nil
	}
	if multi, _ := first["multiSelect"].(bool); multi {
		return text, nil
	}
	opts, _ := first["options"].([]any)
	var labels []string
	for _, o := range opts {
		om, _ := o.(map[string]any)
		label, _ := om["label"].(string)
		if label == "" {
			return text, nil
		}
		labels = append(labels, label)
	}
	if len(labels) > 8 {
		return text, nil
	}
	return text, labels
}

// onlyFocusReports: b is the terminal saying it gained or lost focus, not a key of yours.
func onlyFocusReports(b []byte) bool {
	b = bytes.ReplaceAll(bytes.ReplaceAll(b, []byte("\x1b[I"), nil), []byte("\x1b[O"), nil)
	return len(b) == 0
}
