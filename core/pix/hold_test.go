package pix

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// #268: a question is held in its hook while Pixie may answer it, and answered through the
// hook's reply: nothing is typed, so no option is lost to the keys or the screen.

// holdHarness is keyHarness with questions held for wait.
func holdHarness(t *testing.T, wait time.Duration) *harness {
	t.Helper()
	h := keyHarness(t, "claude")
	h.s.setHold(wait)
	return h
}

// hookAsync runs a hook the way Claude Code does, waiting on its reply.
func hookAsync(h *harness, event string, p map[string]any) chan string {
	reply := make(chan string, 1)
	go func() { reply <- h.s.Hook(event, p) }()
	return reply
}

// pending waits until the hook made its question the one waiting (before its dialog is drawn).
func pending(t *testing.T, h *harness) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for h.s.currentQuestion() == nil {
		if time.Now().After(deadline) {
			t.Fatal("no question waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func hookReply(t *testing.T, reply chan string, within time.Duration) map[string]any {
	t.Helper()
	select {
	case r := <-reply:
		if r == "" {
			return nil
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(r), &out); err != nil {
			t.Fatalf("hook reply %q: %v", r, err)
		}
		return out
	case <-time.After(within):
		t.Fatalf("the hook didn't return within %v", within)
		return nil
	}
}

var pushMenu = map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
	map[string]any{"question": "Push main?", "header": "Push", "multiSelect": false,
		"options": []any{map[string]any{"label": "Push", "description": "Send it"}, map[string]any{"label": "Don't push"}}}}}}

func TestAHeldMenuIsAnsweredThroughItsHook(t *testing.T) {
	h := holdHarness(t, 5*time.Second)
	reply := hookAsync(h, "PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	if q.Source != "menu" || strings.Join(q.Keys, "|") != "Push|Don't push" {
		t.Fatalf("%+v", q)
	}
	if err := answer(t, h, q.ID, "3"); !strings.Contains(err, "isn't one of its answers") {
		t.Fatalf("%q", err)
	}
	if err := answer(t, h, q.ID, "2"); err != "" { // nothing is on screen: the hook answers it
		t.Fatal(err)
	}
	out := hookReply(t, reply, time.Second)
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	in, _ := hso["updatedInput"].(map[string]any)
	answers, _ := in["answers"].(map[string]any)
	if hso["hookEventName"] != "PreToolUse" || hso["permissionDecision"] != "allow" || answers["Push main?"] != "Don't push" {
		t.Fatalf("%v", out)
	}
	if qs, _ := in["questions"].([]any); len(qs) != 1 { // the questions go back as they came
		t.Fatalf("%v", in)
	}
	if err := answer(t, h, q.ID, "1"); !strings.Contains(err, "no longer waiting") {
		t.Fatalf("answered once: %q", err)
	}
}

// Nobody answers in Pixie: the hook lets go, so the terminal shows the menu, and Pixie's bubble
// goes away.
func TestAHeldMenuNobodyAnswersGoesToTheTerminal(t *testing.T) {
	h := holdHarness(t, 300*time.Millisecond)
	start := time.Now()
	reply := hookAsync(h, "PreToolUse", pushMenu)
	q := askedQuestion(t, h)
	if out := hookReply(t, reply, 2*time.Second); out != nil || time.Since(start) < 300*time.Millisecond {
		t.Fatalf("%v after %v", out, time.Since(start))
	}
	waitEvent(t, h.d.events, "question gone", func(m wire.Msg) bool { return m.Event == "question-gone" && m.ID == q.ID })
	if err := answer(t, h, q.ID, "1"); !strings.Contains(err, "no longer waiting") {
		t.Fatalf("%q", err)
	}
}

func TestWithoutAHoldTheHookReturnsAtOnce(t *testing.T) {
	h := holdHarness(t, 0)
	if out := hookReply(t, hookAsync(h, "PreToolUse", pushMenu), time.Second); out != nil {
		t.Fatalf("%v", out)
	}
	askedQuestion(t, h) // still asked: answered with its digit, as before
}

// A question the bubble can't show yet (several at once) isn't held: you answer it in the terminal.
func TestAMenuPixieCantShowIsNotHeld(t *testing.T) {
	h := holdHarness(t, 5*time.Second)
	one := map[string]any{"question": "Which?", "options": []any{map[string]any{"label": "A"}}}
	p := map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{one, one}}}
	if out := hookReply(t, hookAsync(h, "PreToolUse", p), time.Second); out != nil {
		t.Fatalf("%v", out)
	}
}

func heldBash(t *testing.T, h *harness) (chan string, wire.Msg) {
	t.Helper()
	reply := hookAsync(h, "PermissionRequest", map[string]any{"tool_name": "Bash",
		"tool_input": map[string]any{"command": "npm test"},
		"permission_suggestions": []any{map[string]any{"type": "addRules", "behavior": "allow", "destination": "localSettings",
			"rules": []any{map[string]any{"toolName": "Bash", "ruleContent": "npm test"}}}}})
	pending(t, h)
	h.s.runner.OnOutput([]byte(bashDialog))
	return reply, askedQuestion(t, h)
}

func decision(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	hso, _ := out["hookSpecificOutput"].(map[string]any)
	d, _ := hso["decision"].(map[string]any)
	if hso["hookEventName"] != "PermissionRequest" || d == nil {
		t.Fatalf("%v", out)
	}
	return d
}

func TestAHeldPermissionIsAnsweredThroughItsHook(t *testing.T) {
	for _, c := range []struct {
		choice, behavior string
		remember         bool
	}{{"yes", "allow", false}, {"1", "allow", false}, {"2", "allow", true}, {"3", "deny", false}, {"no", "deny", false}} {
		t.Run(c.choice, func(t *testing.T) {
			h := holdHarness(t, 5*time.Second)
			reply, q := heldBash(t, h)
			if err := answer(t, h, q.ID, c.choice); err != "" {
				t.Fatal(err)
			}
			d := decision(t, hookReply(t, reply, time.Second))
			rules, _ := d["updatedPermissions"].([]any)
			if d["behavior"] != c.behavior || (len(rules) == 1) != c.remember {
				t.Fatalf("%v", d)
			}
			if err := answer(t, h, q.ID, c.choice); !strings.Contains(err, "no longer waiting") {
				t.Fatalf("answered once: %q", err)
			}
		})
	}
}

// An option the hook can't say ("Yes, allow all edits during this session" and the like) is
// typed into the dialog, which shows the whole time, and the hook lets go.
func TestAPermissionOptionTheHookCantSayIsTyped(t *testing.T) {
	h := holdHarness(t, 5*time.Second)
	reply := hookAsync(h, "PermissionRequest", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": "/x/a.go"}})
	pending(t, h)
	h.s.runner.OnOutput([]byte(editDialog))
	q := askedQuestion(t, h)
	errs := make(chan string, 1)
	go func() { errs <- answer(t, h, q.ID, "2") }()
	typed(t, h, '2')
	if err := <-errs; err != "" {
		t.Fatal(err)
	}
	if out := hookReply(t, reply, time.Second); out != nil {
		t.Fatalf("%v", out)
	}
}

const editDialog = "────────────────────────────────────────\r\n Edit file\r\n a.go\r\n" +
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌\r\n - old\r\n + new\r\n╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌\r\n Do you want to make this edit to a.go?\r\n" +
	" ❯ 1. Yes\r\n   2. Yes, allow all edits during this session (shift+tab)\r\n   3. No\r\n\r\n Esc to cancel"

// You answer the dialog in the terminal first: the hook lets go and the bubble goes away.
func TestAPermissionAnsweredInTheTerminalLetsItsHookGo(t *testing.T) {
	h := holdHarness(t, 5*time.Second)
	reply, q := heldBash(t, h)
	h.stdinW.Write([]byte("1"))
	if out := hookReply(t, reply, time.Second); out != nil {
		t.Fatalf("%v", out)
	}
	waitEvent(t, h.d.events, "question gone", func(m wire.Msg) bool { return m.Event == "question-gone" && m.ID == q.ID })
}

// Losing DeskPixie lets a held question go at once: nobody is left to answer it in Pixie.
func TestLosingDeskPixieLetsAHeldQuestionGo(t *testing.T) {
	h := holdHarness(t, 5*time.Second)
	reply := hookAsync(h, "PreToolUse", pushMenu)
	askedQuestion(t, h)
	h.conn.Close()
	if out := hookReply(t, reply, 2*time.Second); out != nil {
		t.Fatalf("%v", out)
	}
}

// DeskPixie says how long to hold when pix registers.
func TestTheHoldComesFromDeskPixie(t *testing.T) {
	d, sock := newFakeDaemon(t, "")
	d.holdMs.Store(1500)
	inR, _, _, outW := pipes(t)
	s := newSession(sessionOpts{tool: "claude", daemonSock: sock, home: t.TempDir(), stdin: inR, stdout: outW,
		agent: Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := s.start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.runner.Hangup(); s.finish() })
	<-d.conn
	deadline := time.Now().Add(3 * time.Second)
	for s.holdFor() != 1500*time.Millisecond {
		if time.Now().After(deadline) {
			t.Fatalf("hold %v", s.holdFor())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
