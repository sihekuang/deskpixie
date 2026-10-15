package pix

import (
	"bytes"
	"context"
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
