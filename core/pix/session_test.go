package pix

import (
	"bytes"
	"context"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// fakeDaemon accepts pix connections, answers register with an address (or refuses), and
// records every message it gets.
type fakeDaemon struct {
	conn   chan net.Conn
	events chan wire.Msg
}

func newFakeDaemon(t *testing.T, refuse string) (*fakeDaemon, string) {
	sock := filepath.Join(sockDir(t), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{conn: make(chan net.Conn, 4), events: make(chan wire.Msg, 256)}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				dec := wire.NewDecoder(c)
				for {
					m, err := dec.Next()
					if err != nil {
						return
					}
					if m.Kind == wire.KindRegister {
						if refuse != "" {
							wire.Encode(c, wire.Msg{Kind: wire.KindRefused, Reason: refuse})
							continue
						}
						wire.Encode(c, wire.Msg{Kind: wire.KindRegistered, Address: "repo-claude"})
						d.conn <- c
					}
					d.events <- m
				}
			}()
		}
	}()
	t.Cleanup(func() { l.Close() })
	return d, sock
}

func waitEvent(t *testing.T, ch chan wire.Msg, what string, pred func(wire.Msg) bool) wire.Msg {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-ch:
			if pred(m) {
				return m
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestClientRegistersAndReconnects(t *testing.T) {
	d, sock := newFakeDaemon(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan string, 2)
	c := &Client{Sock: sock, Hello: wire.Msg{Kind: wire.KindRegister, Tool: "claude", SessionID: "s1"},
		OnRegistered: func(a string) { got <- a }, OnCommand: func(wire.Msg) wire.Msg { return wire.Msg{} }}
	go c.Run(ctx)
	if a := <-got; a != "repo-claude" {
		t.Fatal(a)
	}
	(<-d.conn).Close() // the daemon goes away
	select {
	case a := <-got:
		if a != "repo-claude" {
			t.Fatal(a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no re-register after the connection dropped")
	}
}

func TestClientResendsStateOnRegister(t *testing.T) {
	d, sock := newFakeDaemon(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := &Client{Sock: sock, Hello: wire.Msg{Kind: wire.KindRegister},
		OnCommand: func(wire.Msg) wire.Msg { return wire.Msg{} },
		Resend:    func() []wire.Msg { return []wire.Msg{{Kind: wire.KindEvent, Event: "status", Status: "working"}} }}
	go c.Run(ctx)
	waitEvent(t, d.events, "resent status", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "working" })
}

func TestRefusedRunsStandalone(t *testing.T) {
	_, sock := newFakeDaemon(t, "already connected")
	refused := make(chan string, 1)
	c := &Client{Sock: sock, Hello: wire.Msg{Kind: wire.KindRegister}, OnRefused: func(r string) { refused <- r },
		OnCommand: func(wire.Msg) wire.Msg { return wire.Msg{} }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	if r := <-refused; r != "already connected" {
		t.Fatal(r)
	}
}

type harness struct {
	s      *Session
	d      *fakeDaemon
	conn   net.Conn
	out    *os.File
	stdinW *os.File
}

func newHarness(t *testing.T, tool string, agent Launch) *harness {
	t.Helper()
	d, sock := newFakeDaemon(t, "")
	inR, inW, outR, outW := pipes(t)
	s := newSession(sessionOpts{tool: tool, daemonSock: sock, home: t.TempDir(), stdin: inR, stdout: outW, agent: agent, where: "terminal"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := s.start(ctx); err != nil {
		t.Fatal(err)
	}
	h := &harness{s: s, d: d, out: outR, stdinW: inW}
	select {
	case h.conn = <-d.conn:
	case <-time.After(5 * time.Second):
		t.Fatal("pix did not register")
	}
	return h
}

func TestSessionPromptIsPastedWhenIdle(t *testing.T) {
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo seen:$x"}})
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "hello pix"})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.Kind == wire.KindResult && m.ID == "1" && m.Err == "" })
	code := h.s.finish()
	var buf bytes.Buffer
	h.s.o.stdout.Close()
	buf.ReadFrom(h.out)
	if code != 0 || !strings.Contains(buf.String(), "seen:") || !strings.Contains(buf.String(), "hello pix") {
		t.Fatalf("code %d out %q", code, buf.String())
	}
	waitEvent(t, h.d.events, "exited", func(m wire.Msg) bool { return m.Event == "exited" })
}

func TestSessionHoldsPromptWhileYouType(t *testing.T) {
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.stdinW.WriteString("half a sent") // no Enter
	time.Sleep(100 * time.Millisecond)
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.ID == "1" })
	time.Sleep(2 * time.Second)
	if h.s.queue.Len() != 1 {
		t.Fatalf("message must wait while you have unsent text; queue %d", h.s.queue.Len())
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestPostToolUseDeliversQueued(t *testing.T) {
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "check the tests"})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.ID == "1" })
	reply := h.s.Hook("PostToolUse", map[string]any{})
	if !strings.Contains(reply, "check the tests") || !strings.Contains(reply, "additionalContext") || h.s.queue.Len() != 0 {
		t.Fatalf("reply %q queue %d", reply, h.s.queue.Len())
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestHooksReportStatusAndAutoDescription(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "fix the flaky upload test", "session_id": "s-9"})
	// The three events may arrive in any order.
	seen := map[string]bool{}
	waitEvent(t, h.d.events, "working, auto description and session id", func(m wire.Msg) bool {
		switch {
		case m.Event == "status" && m.Status == "working":
			seen["working"] = true
		case m.Event == "description" && m.Source == "auto" && m.Description == "fix the flaky upload test":
			seen["auto"] = true
		case m.Event == "session" && m.SessionID == "s-9":
			seen["session"] = true
		case m.Event == "description" && m.Description == "and also this":
			t.Fatal("only the first prompt of new work sets the auto description")
		}
		return len(seen) == 3
	})
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "and also this"})
	h.s.Hook("Stop", map[string]any{})
	waitEvent(t, h.d.events, "idle", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "idle" })
	h.s.runner.Hangup()
	h.s.finish()
}

func TestKeysAndClose(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "k", Command: "keys", Keys: []string{"Escape"}})
	waitEvent(t, h.d.events, "keys result", func(m wire.Msg) bool { return m.ID == "k" && m.Err == "" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "b", Command: "keys", Keys: []string{"F13"}})
	waitEvent(t, h.d.events, "bad key", func(m wire.Msg) bool { return m.ID == "b" && m.Err != "" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "c", Command: "close"})
	done := make(chan int, 1)
	go func() { done <- h.s.finish() }()
	select {
	case <-done:
	case <-time.After(HangupGrace + 3*time.Second):
		t.Fatal("close did not end the agent")
	}
}

func TestReRegisterSendsAddressAndSessionID(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.Hook("Stop", map[string]any{"session_id": "s-7"})
	waitEvent(t, h.d.events, "session id", func(m wire.Msg) bool { return m.Event == "session" })
	h.conn.Close() // the daemon restarts
	hello := waitEvent(t, h.d.events, "re-register", func(m wire.Msg) bool { return m.Kind == wire.KindRegister })
	if hello.Address != "repo-claude" || hello.SessionID != "s-7" {
		t.Fatalf("%+v", hello)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestClaudeInterruptOnScreenMeansIdle(t *testing.T) {
	// Claude fires no Stop hook when a turn is interrupted; its screen says so instead. The
	// message arrives in two chunks, as PTY reads can split it.
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c",
		`read x; printf '\033[1m⎿  Interrupted · What should Clau'; sleep 0.2; printf 'de do instead?\033[0m\n'; sleep 30`}})
	h.s.setStatus("working")
	waitEvent(t, h.d.events, "working", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "working" })
	h.stdinW.WriteString("go\n")
	waitEvent(t, h.d.events, "idle after the interrupt", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "idle" })
	h.s.runner.Hangup()
	h.s.finish()
}

func TestScreenMarkersDontFlipStatusTheWrongWay(t *testing.T) {
	var w screenWatch
	if w.saw("Interrupted · What should Claude do instead?", claudeInterrupted) != true {
		t.Fatal("marker in one chunk")
	}
	if w.saw("nothing here", claudeInterrupted) {
		t.Fatal("no marker, no match")
	}
}

func TestRefusedAfterRegisteringStopsReconnecting(t *testing.T) {
	d, sock := newFakeDaemon(t, "")
	refused := make(chan string, 1)
	c := &Client{Sock: sock, Hello: wire.Msg{Kind: wire.KindRegister}, OnRefused: func(r string) { refused <- r },
		OnCommand: func(wire.Msg) wire.Msg { return wire.Msg{} }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { c.Run(ctx); close(done) }()
	conn := <-d.conn
	wire.Encode(conn, wire.Msg{Kind: wire.KindRefused, Reason: "this conversation is already connected"})
	select {
	case r := <-refused:
		if r != "this conversation is already connected" {
			t.Fatal(r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refusal not reported")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("still reconnecting after a refusal")
	}
}

func TestScreenMarkerFoundWhenCursorMovesReplaceSpaces(t *testing.T) {
	// Captured from Claude Code 2.1.300 in a real terminal.
	var w screenWatch
	chunk := "\x1b[38;2;153;153;153m  ⎿  Interrupted · What\x1b[25Gshould Claude\x1b[39Gdo instead?\r\x1b[3B\x1b[39m❯"
	if !w.saw(chunk, claudeInterrupted) {
		t.Fatal("marker with cursor moves for spaces")
	}
}

// A session whose first message is Pixie's ("Task from the coordinator, …", seen 2026-10-15
// as the description "Task from coordinator,") must not be described by its opening words;
// pix asks the agent for a description instead, and the agent's is what shows.
func TestPixieMessageAsksForADescriptionInsteadOfBeingOne(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	task := "Task from the coordinator, on the user's request: fix how Pixie's pix sessions get described."
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "p", Command: "prompt", Text: task})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.ID == "p" })
	reply := h.s.Hook("UserPromptSubmit", map[string]any{"prompt": task + "\n"})
	if !strings.Contains(reply, "set_description") || !strings.Contains(reply, "UserPromptSubmit") {
		t.Fatalf("the agent is asked to describe its task: %q", reply)
	}
	if err := h.s.SetDescription("deskpixie: fix pix session descriptions"); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, h.d.events, "agent description", func(m wire.Msg) bool {
		if m.Event == "description" && m.Source == "auto" {
			t.Fatalf("Pixie's message became the description: %q", m.Description)
		}
		return m.Event == "description" && m.Source == "agent" && m.Description == "deskpixie: fix pix session descriptions"
	})
	if d := h.s.description(); d != "deskpixie: fix pix session descriptions" {
		t.Fatalf("%q", d)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestMessagesYouDidNotTypeAreNotDescriptions(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "Another Claude session sent a message:\n<agent-message from=\"a91706fc445c4c179\">\n[Subagent hand-back] report"})
	h.s.Hook("Stop", map[string]any{})
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "<task-notification>\n<task-id>a5</task-id>"})
	h.s.Hook("Stop", map[string]any{})
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "Please fix the flaky upload test\nit fails on CI"})
	waitEvent(t, h.d.events, "auto description", func(m wire.Msg) bool {
		if m.Event == "description" && m.Description != "fix the flaky upload test" {
			t.Fatalf("got %q", m.Description)
		}
		return m.Event == "description"
	})
	h.s.runner.Hangup()
	h.s.finish()
}

func TestAgentDescriptionMustBeWords(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	if err := h.s.SetDescription(`<agent-message from="a9"> [Subagent`); err == nil {
		t.Fatal("a message fragment is refused")
	}
	if err := h.s.SetDescription("  "); err == nil {
		t.Fatal("an empty description is refused")
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// Codex has no prompt hook; its turn-end notify carries what you typed.
func TestCodexDescriptionFromItsTurn(t *testing.T) {
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.Hook("Stop", map[string]any{"type": "agent-turn-complete", "input-messages": []any{"add retries to the uploader", "and log them"}})
	waitEvent(t, h.d.events, "codex auto description", func(m wire.Msg) bool {
		return m.Event == "description" && m.Source == "auto" && m.Description == "add retries to the uploader"
	})
	h.s.runner.Hangup()
	h.s.finish()
}

// #105: prompts a session waits on, answered from Pixie.

// keyHarness runs an agent that prints, in hex, the first byte typed into it.
func keyHarness(t *testing.T, tool string) *harness {
	t.Helper()
	h := newHarness(t, tool, Launch{Path: "/bin/sh", Args: []string{"-c", "stty raw -echo; head -c 1 | od -An -tx1; sleep 30"}})
	waitEvent(t, h.d.events, "connected", func(m wire.Msg) bool { return m.Event == "status" })
	t.Cleanup(func() { h.s.runner.Hangup(); h.s.finish() })
	return h
}

// typed waits for the agent to print the byte it got.
func typed(t *testing.T, h *harness, want byte) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		var all []byte
		buf := make([]byte, 256)
		for {
			n, err := h.out.Read(buf)
			all = append(all, buf[:n]...)
			if strings.Contains(string(all), hex.EncodeToString([]byte{want})) || err != nil {
				got <- string(all)
				return
			}
		}
	}()
	select {
	case s := <-got:
		if !strings.Contains(s, hex.EncodeToString([]byte{want})) {
			t.Fatalf("typed %q, want %x", s, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("nothing typed (want %x)", want)
	}
}

func askedQuestion(t *testing.T, h *harness) wire.Msg {
	t.Helper()
	return waitEvent(t, h.d.events, "question", func(m wire.Msg) bool { return m.Event == "question" })
}

func answer(t *testing.T, h *harness, id, choice string) string {
	t.Helper()
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "a-" + id + choice, Command: "answer", Text: id, Source: choice})
	return waitEvent(t, h.d.events, "answer result", func(m wire.Msg) bool { return m.ID == "a-"+id+choice }).Err
}

func TestAClaudePermissionIsAskedAndAnsweredOnceOnScreen(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm test"}})
	q := askedQuestion(t, h)
	if q.Source != "permission" || q.ToolName != "Bash" || q.Input != `{"command":"npm test"}` || q.ID == "" {
		t.Fatalf("%+v", q)
	}
	if err := answer(t, h, q.ID, "yes"); !strings.Contains(err, "isn't on screen") {
		t.Fatalf("not typed before the dialog shows: %q", err)
	}
	h.s.runner.OnOutput([]byte("Bash command\r\n npm test\r\nDo you want to proceed?\r\n❯ 1. Yes"))
	if err := answer(t, h, q.ID, "yes"); err != "" {
		t.Fatal(err)
	}
	typed(t, h, '1')
	if err := answer(t, h, q.ID, "yes"); !strings.Contains(err, "no longer waiting") {
		t.Fatalf("answered once: %q", err)
	}
}

func TestNoAtAClaudePermissionIsEscape(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": "/x/a.go"}})
	q := askedQuestion(t, h)
	h.s.runner.OnOutput([]byte("Edit file a.go\r\nDo you want to make this edit to a.go?"))
	if err := answer(t, h, q.ID, "no"); err != "" {
		t.Fatal(err)
	}
	typed(t, h, 0x1b)
}

func TestAClaudeMenuIsAnsweredWithItsDigit(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{
		map[string]any{"question": "Push main?", "header": "Push", "multiSelect": false,
			"options": []any{map[string]any{"label": "Push"}, map[string]any{"label": "Don't push"}}}}}})
	q := askedQuestion(t, h)
	if q.Source != "menu" || q.Text != "Push main?" || strings.Join(q.Keys, "|") != "Push|Don't push" {
		t.Fatalf("%+v", q)
	}
	h.s.runner.OnOutput([]byte("Push main?\r\n❯ 1. Push\r\n  2. Don't push\r\nEnter to select · ↑/↓ to navigate"))
	if err := answer(t, h, q.ID, "3"); !strings.Contains(err, "isn't one of its answers") {
		t.Fatalf("%q", err)
	}
	if err := answer(t, h, q.ID, "2"); err != "" {
		t.Fatal(err)
	}
	typed(t, h, '2')
}

func TestAMenuOfSeveralQuestionsIsOnlyAnnounced(t *testing.T) {
	h := keyHarness(t, "claude")
	one := map[string]any{"question": "Which?", "options": []any{map[string]any{"label": "A"}}}
	h.s.Hook("PreToolUse", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{"questions": []any{one, one}}})
	if q := askedQuestion(t, h); q.Text != "Which? (and 1 more)" || len(q.Keys) != 0 {
		t.Fatalf("%+v", q)
	}
}

func TestAKeyYouPressWithdrawsTheQuestion(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	q := askedQuestion(t, h)
	h.s.runner.OnOutput([]byte("$ ls\r\nDo you want to proceed?"))
	h.stdinW.Write([]byte("\x1b[I")) // the terminal saying it got focus is not you answering
	time.Sleep(200 * time.Millisecond)
	h.stdinW.Write([]byte("1"))
	waitEvent(t, h.d.events, "question gone", func(m wire.Msg) bool { return m.Event == "question-gone" && m.ID == q.ID })
	if err := answer(t, h, q.ID, "yes"); !strings.Contains(err, "no longer waiting") {
		t.Fatalf("%q", err)
	}
}

func TestTheSessionMovingOnWithdrawsTheQuestion(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	q := askedQuestion(t, h)
	h.s.Hook("PostToolUse", map[string]any{"tool_name": "Bash"})
	waitEvent(t, h.d.events, "question gone", func(m wire.Msg) bool { return m.Event == "question-gone" && m.ID == q.ID })
}

func TestAPermissionRequestForTheMenuIsNotASecondQuestion(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "AskUserQuestion", "tool_input": map[string]any{}})
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	if q := askedQuestion(t, h); q.ToolName != "Bash" {
		t.Fatalf("%+v", q)
	}
}

// Review #3: a Codex approval is its own notification (OSC 9), which text on screen can't fake.
func TestACodexApprovalNotificationIsAskedAndAnsweredWithY(t *testing.T) {
	h := keyHarness(t, "codex")
	h.s.runner.OnOutput([]byte("\x1b]9;Approval requ"))
	h.s.runner.OnOutput([]byte("ested: npm test\x07"))
	q := askedQuestion(t, h)
	if q.Source != "permission" || q.Text != "wants to run npm test" || q.ToolName != "" {
		t.Fatalf("%+v", q)
	}
	h.s.runner.OnOutput([]byte("\x1b]9;Codex wants to edit a.go\x07")) // a second one asks nothing while the first waits
	if err := answer(t, h, q.ID, "yes"); err != "" {
		t.Fatal(err)
	}
	typed(t, h, 'y')
}

func TestCodexApprovalTextOnScreenAsksNothing(t *testing.T) {
	h := keyHarness(t, "codex")
	h.s.runner.OnOutput([]byte("Would you like to run the following command?\r\nYes, proceed (y)"))
	h.s.runner.OnOutput([]byte("\x1b]9;Agent turn complete\x07"))
	if q := h.s.currentQuestion(); q != nil {
		t.Fatalf("asked from screen text: %+v", q)
	}
}

func TestCodexApprovalMessages(t *testing.T) {
	for msg, want := range map[string]string{
		"Approval requested: npm test":   "wants to run npm test",
		"Codex wants to edit 3 files":    "wants to edit 3 files",
		"Approval requested by github":   "asks for approval for github",
		"Agent turn complete":            "",
		"Plan mode prompt: Implement it": "",
	} {
		if got := codexApproval(msg); got != want {
			t.Errorf("%q: got %q, want %q", msg, got, want)
		}
	}
}

// Review #2: an Escape sent through Pixie (interrupt_session) dismisses the prompt.
func TestAnEscapeFromPixieWithdrawsTheQuestion(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	q := askedQuestion(t, h)
	h.s.runner.OnOutput([]byte("$ ls\r\nDo you want to proceed?"))
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "k", Command: "keys", Keys: []string{"Escape"}})
	waitEvent(t, h.d.events, "question gone", func(m wire.Msg) bool { return m.Event == "question-gone" && m.ID == q.ID })
}

// Review #2: Claude's interrupt shows at a permission prompt too (status blocked there).
func TestClaudesInterruptAtAPromptWithdrawsTheQuestion(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("Notification", map[string]any{})
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	q := askedQuestion(t, h)
	h.s.runner.OnOutput([]byte(claudeInterrupted))
	waitEvent(t, h.d.events, "question gone", func(m wire.Msg) bool { return m.Event == "question-gone" && m.ID == q.ID })
}

// Review #4: a second prompt never takes the place of one still waiting.
func TestAQuestionIsNotReplacedWhileItWaits(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm test"}})
	q := askedQuestion(t, h)
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf build"}})
	if cur := h.s.currentQuestion(); cur == nil || cur.id != q.ID {
		t.Fatalf("replaced: %+v", cur)
	}
}

// Review #4: another tool's dialog on screen doesn't count as this one shown.
func TestAnotherToolsDialogDoesNotCountAsShown(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm test"}})
	q := askedQuestion(t, h)
	h.s.runner.OnOutput([]byte("$ rm -rf build\r\nDo you want to proceed?"))
	if err := answer(t, h, q.ID, "yes"); !strings.Contains(err, "isn't on screen") {
		t.Fatalf("%q", err)
	}
}

// Review #5: only dialogs whose first option is a plain Yes are answered from Pixie.
func TestAPlanApprovalIsOnlyAnnounced(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "ExitPlanMode", "tool_input": map[string]any{"plan": "Do you want to ship it?"}})
	q := askedQuestion(t, h)
	if q.Source != "notice" || q.ToolName != "ExitPlanMode" {
		t.Fatalf("%+v", q)
	}
	h.s.runner.OnOutput([]byte("ExitPlanMode Do you want to ship it?"))
	if err := answer(t, h, q.ID, "yes"); err == "" {
		t.Fatal("a plan approval must not be answered from Pixie")
	}
}
