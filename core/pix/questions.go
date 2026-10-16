package pix

import (
	"bytes"
	"fmt"
	"strconv"

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

// codexApprovals are Codex's approval prompts and what each asks to do.
var codexApprovals = []struct{ marker, asks string }{
	{"Would you like to run the following command?", "wants to run a command"},
	{"Would you like to make the following edits?", "wants to make edits"},
	{"Allow this request?", "asks to allow a request"},
}

// question is the prompt waiting on you that Pixie may answer.
type question struct {
	id      string
	kind    string // "permission" or "menu"
	options int    // menu: how many options a digit picks from (0: answered in the terminal)
	marker  string // shown while it waits ("" when it was seen on screen to begin with)
	seen    bool   // its marker showed since it was asked
}

// ask makes q the prompt waiting on you (a prompt asked before it is replaced) and tells
// DeskPixie about it with ev.
func (s *Session) ask(q *question, ev wire.Msg) {
	s.mu.Lock()
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

// questionWatch notes when the waiting prompt shows on screen (PTY reader goroutine only).
type questionWatch struct {
	screen screenWatch
	for_   *question
}

func (w *questionWatch) watch(s *Session, out string) {
	q := s.currentQuestion()
	if q == nil || q.marker == "" {
		return
	}
	if w.for_ != q { // a new prompt: only what is shown after it was asked counts
		w.for_, w.screen = q, screenWatch{}
	}
	if w.screen.saw(out, q.marker) {
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
