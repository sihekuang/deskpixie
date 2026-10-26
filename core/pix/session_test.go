package pix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// fakeDaemon accepts pix connections, answers register with an address (or refuses), and
// records every message it gets.
type fakeDaemon struct {
	conn   chan net.Conn
	events chan wire.Msg
	// outdated: registered replies say this pix is older than the app's (#189).
	outdated atomic.Bool
	// holdMs: registered replies say how long a question waits for Pixie (#268).
	holdMs atomic.Int64
	// down: DeskPixie is gone; a connection is closed at once.
	down atomic.Bool
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
			if d.down.Load() {
				c.Close()
				continue
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
						wire.Encode(c, wire.Msg{Kind: wire.KindRegistered, Address: "repo-claude", Outdated: d.outdated.Load(), DurationMs: d.holdMs.Load()})
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
		OnRegistered: func(m wire.Msg) { got <- m.Address }, OnCommand: func(wire.Msg) wire.Msg { return wire.Msg{} }}
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
	return newLoggedHarness(t, tool, agent, nil)
}

func newLoggedHarness(t *testing.T, tool string, agent Launch, log *Log) *harness {
	t.Helper()
	d, sock := newFakeDaemon(t, "")
	inR, inW, outR, outW := pipes(t)
	s := newSession(sessionOpts{tool: tool, daemonSock: sock, home: t.TempDir(), stdin: inR, stdout: outW, agent: agent, where: "terminal", log: log})
	s.runner.Tap = func(b []byte) {
		tapMu.Lock()
		tapped[s] = append(tapped[s], b...)
		writes[s] = append(writes[s], agentWrite{string(b), time.Now()})
		tapMu.Unlock()
	}
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

// TestSessionTypesPromptAfterWindowSwitch (#138): you switch away from an idle session (the
// terminal sends a focus-out report), then a message arrives. pix must type it, not hold it.
func TestSessionTypesPromptAfterWindowSwitch(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo seen:$x"}})
	h.stdinW.WriteString("\x1b[I\x1b[O")
	time.Sleep(100 * time.Millisecond)
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "while you were away"})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.ID == "1" && m.Err == "" })
	done := make(chan int, 1)
	go func() { done <- h.s.finish() }()
	select {
	case code := <-done:
		var buf bytes.Buffer
		h.s.o.stdout.Close()
		buf.ReadFrom(h.out)
		if code != 0 || !strings.Contains(buf.String(), "seen:") || !strings.Contains(buf.String(), "while you were away") {
			t.Fatalf("code %d out %q", code, buf.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the message was never typed: %d still queued", h.s.queue.Len())
	}
}

// TestSessionTypesPromptWhileMouseMoves (#138): fullscreen Claude Code has the terminal report
// every mouse move, so your mouse over the window must not hold a message back.
func TestSessionTypesPromptWhileMouseMoves(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo seen:$x"}})
	h.stdinW.WriteString(mouseReports)
	time.Sleep(100 * time.Millisecond)
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "while the mouse moves"})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.ID == "1" && m.Err == "" })
	h.stdinW.WriteString(mouseReports)
	done := make(chan int, 1)
	go func() { done <- h.s.finish() }()
	select {
	case code := <-done:
		var buf bytes.Buffer
		h.s.o.stdout.Close()
		buf.ReadFrom(h.out)
		if code != 0 || !strings.Contains(buf.String(), "while the mouse moves") {
			t.Fatalf("code %d out %q", code, buf.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the message was never typed: %d still queued", h.s.queue.Len())
	}
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

// #336: a subagent's tool (its hook carries agent_id) doesn't take what waits: the context
// would reach the subagent, not the session. The session's own next tool still does.
func TestSubagentToolLeavesQueuedForTheSession(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "check the tests"})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.ID == "1" && m.Reason == "working" })
	if reply := h.s.Hook("PostToolUse", map[string]any{"tool_name": "Bash", "agent_id": "a0d84cff17c3490ba", "agent_type": "general-purpose"}); reply != "" || h.s.queue.Len() != 1 {
		t.Fatalf("a subagent's tool took it: reply %q, queue %d", reply, h.s.queue.Len())
	}
	if reply := h.s.Hook("PostToolUse", map[string]any{"tool_name": "Bash", "agent_id": ""}); !strings.Contains(reply, "check the tests") || h.s.queue.Len() != 0 {
		t.Fatalf("the session's own tool: reply %q, queue %d", reply, h.s.queue.Len())
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// #336: after a subagent's tool, stop-and-send still has the message to send: it stops the
// turn and types it, rather than finding nothing waits and saying it was sent.
func TestStopAfterASubagentToolStillTypesWhatWaits(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", stopsOnEsc}})
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "waiting: working", func(m wire.Msg) bool { return m.ID == "1" && m.Reason == "working" })
	h.s.Hook("PostToolUse", map[string]any{"tool_name": "Bash", "agent_id": "sub1"})
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "stop"})
	waitEvent(t, h.d.events, "typed", func(m wire.Msg) bool { return m.ID == "2" && m.Status == "typed" })
	if got := h.s.typedForTest(); !strings.HasPrefix(got, "\x1b") || !strings.Contains(got, "\x1b[200~from pixie\x1b[201~") || h.s.queue.Len() != 0 {
		t.Fatalf("typed %q, %d still queued", got, h.s.queue.Len())
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// claudeInbox fakes the ~/.claude/sessions/<pid>.json record and inbox socket Claude Code
// itself would leave for s's wrapped process, so a prompt command's choice to use it (or not)
// is actually exercised rather than failing open because no record exists. got receives every
// {"type":"user",…} message posted to it.
func claudeInbox(t *testing.T, s *Session) (got chan map[string]any) {
	t.Helper()
	pid := s.runner.Pid()
	start, err := ProcStart(pid)
	if err != nil {
		t.Fatal(err)
	}
	sessDir := filepath.Join(s.o.home, ".claude", "sessions")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(sockDir(t), "inbox.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	b, _ := json.Marshal(map[string]any{
		"pid": pid, "sessionId": "s-1", "procStart": start, "peerProtocol": 1, "messagingSocketPath": sock,
	})
	if err := os.WriteFile(filepath.Join(sessDir, strconv.Itoa(pid)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	got = make(chan map[string]any, 4)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var m map[string]any
					if json.Unmarshal(sc.Bytes(), &m) == nil && m["type"] == "user" {
						got <- m
					}
				}
			}(c)
		}
	}()
	return got
}

// TestSessionPromptNeverUsesClaudeInbox (#129, extended): a "prompt" command is always typed,
// same as Codex, never relayed over Claude's inbox socket — even when a usable record for the
// session is sitting right there. Claude Code labels anything arriving over that socket as
// coming from a peer session, refuses to treat it as the user's own approval however it's
// worded, and wraps even a routine coordinator instruction in a generic "another Claude
// session" notice; typing it directly avoids both. (The "Coordinator says: " label for a
// non-verbatim message is added a layer up, in core/agent — this package only ever sees the
// final text to type.)
func TestSessionPromptNeverUsesClaudeInbox(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo seen:$x"}})
	got := claudeInbox(t, h.s)

	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "Coordinator says: go ahead"})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.Kind == wire.KindResult && m.ID == "1" && m.Err == "" })

	select {
	case m := <-got:
		t.Fatalf("a prompt must never reach Claude's inbox socket: %v", m)
	case <-time.After(300 * time.Millisecond):
	}

	code := h.s.finish()
	var buf bytes.Buffer
	h.s.o.stdout.Close()
	buf.ReadFrom(h.out)
	if code != 0 || !strings.Contains(buf.String(), "seen:") || !strings.Contains(buf.String(), "go ahead") {
		t.Fatalf("text must be typed, same as Codex: code %d out %q", code, buf.String())
	}
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
	// message arrives in two chunks, as PTY reads can split it. Your Esc asked for it (#322).
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c",
		`read x; printf '\033[1m⎿  Interrupted · What should Clau'; sleep 0.2; printf 'de do instead?\033[0m\r\n────\r\n❯ \r\n────\r\n'; sleep 30`}})
	h.s.setStatus("working")
	waitEvent(t, h.d.events, "working", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "working" })
	pressEsc(h)
	waitEvent(t, h.d.events, "idle after the interrupt", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "idle" })
	h.s.runner.Hangup()
	h.s.finish()
}

// #322: the interrupt counts only once the input box shows below it with nothing between.
// A prompt you sent (❯) or a reply (⏺) there means a turn since; the box's draft doesn't; with
// neither drawn yet (a redraw cut between reads) it isn't decided.
func TestInterruptOnScreenIsOnlyTheLatest(t *testing.T) {
	box := []string{"────────", "❯ my draft", "────────", "  ⏵⏵ auto mode on"}
	const line = "  ⎿  Interrupted · What should Claude do instead?"
	cases := []struct {
		name          string
		lines         []string
		at            int
		latest, stale bool
	}{
		{"just stopped", append([]string{"  the story so far", line}, box...), 1, true, false},
		{"box not drawn yet", []string{line, "", ""}, 0, false, false},
		{"a turn since", append([]string{line, "❯ Write a story", "⏺ Once upon a time"}, box...), 0, false, true},
		{"a reply since", append([]string{line, "⏺ PLUM"}, box...), 0, false, true},
		{"stopped twice", append([]string{line, "❯ Write a story", line}, box...), 2, true, false},
		{"none", box, -1, false, false},
		{"wrapped in a narrow pane", append([]string{"  ⎿  Interrupted · What", "should Claude do instead?"}, box...), 1, true, false},
	}
	for _, c := range cases {
		if at, latest, stale := interruptOnScreen(c.lines); at != c.at || latest != c.latest || stale != c.stale {
			t.Errorf("%s: %d %t %t", c.name, at, latest, stale)
		}
	}
}

// #322: Claude Code redraws an earlier interrupt above the turn now running, the redraw cut
// between two reads right after that line: still working. Your Esc then stops it; and when
// the next prompt begins, Claude draws that interrupt again over an empty input box before it
// shows the prompt: still working, as no Esc asked for it.
func TestARedrawnInterruptDoesNotEndTheTurn(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c",
		`read x; printf '\033[2J\033[H  ⎿  Interrupted · What should Claude do instead?\r\n'; sleep 0.3; ` +
			`printf '❯ Write a story\r\n⏺ Once upon a time\r\n────\r\n❯ \r\n────\r\n'; ` +
			`read y; printf '\033[2J\033[H⏺ Once upon a time\r\n  ⎿  Interrupted · What should Claude do instead?\r\n────\r\n❯ \r\n────\r\n'; ` +
			`read z; printf '\033[2J\033[H⏺ Once upon a time\r\n  ⎿  Interrupted · What should Claude do instead?\r\n────\r\n❯ \r\n────\r\n'; ` +
			`printf 'Next turn\r\n'; sleep 30`}})
	h.s.setStatus("working")
	waitEvent(t, h.d.events, "working", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "working" })
	h.stdinW.WriteString("go\n")
	waitScreen(t, h, "Onceuponatime") // the turn drawn below the old line
	if st := h.s.currentStatus(); st != "working" {
		t.Fatalf("redraw made it %s", st)
	}
	pressEsc(h)
	waitEvent(t, h.d.events, "idle", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "idle" })
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "next"})
	h.stdinW.WriteString("next\n")
	waitScreen(t, h, "Nextturn")
	if st := h.s.currentStatus(); st != "working" {
		t.Fatalf("the redraw at the next prompt made it %s", st)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// pressEsc types Esc on its own, then a line, as a test agent's `read` wants one.
func pressEsc(h *harness) {
	h.stdinW.WriteString("\x1b")
	time.Sleep(50 * time.Millisecond)
	h.stdinW.WriteString("\n")
}

// waitScreen waits until the screen pix reads shows text (spaces taken out).
func waitScreen(t *testing.T, h *harness, text string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		h.s.mu.Lock()
		shown := strings.Contains(h.s.screen, text)
		h.s.mu.Unlock()
		if shown {
			return
		}
		if time.Now().After(end) {
			t.Fatalf("the screen never showed %q", text)
		}
	}
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

// keyHarness runs an agent that prints, in hex, the first byte typed into it on a cleared
// screen, as an agent taking an answer redraws without its prompt.
func keyHarness(t *testing.T, tool string) *harness {
	t.Helper()
	return agentHarness(t, tool, `stty raw -echo; b=$(head -c 1 | od -An -tx1); printf '\033[2J\033[H%s\r\n' "$b"; sleep 30`)
}

func agentHarness(t *testing.T, tool, script string) *harness {
	t.Helper()
	h := newHarness(t, tool, Launch{Path: "/bin/sh", Args: []string{"-c", script}})
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

// #322: Pixie's No is an Esc pix types itself: Claude's interrupt then ends the blocked turn.
func TestNoAtAClaudePermissionEndsTheTurn(t *testing.T) {
	// An agent that draws nothing: the test alone feeds the screen, as only the PTY reader may.
	h := agentHarness(t, "claude", `stty raw -echo; cat >/dev/null`)
	h.s.setStatus("working")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": "/x/a.go"}})
	q := askedQuestion(t, h)
	h.s.runner.OnOutput([]byte("Edit file a.go\r\nDo you want to make this edit to a.go?"))
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "no", Command: "answer", Text: q.ID, Source: "no"})
	for end := time.Now().Add(5 * time.Second); !strings.Contains(h.s.typedForTest(), "\x1b"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("pix never typed its Esc")
		}
	}
	// What Claude shows then: the dialog gone, its interrupt over the input box.
	h.s.runner.OnOutput([]byte("\x1b[2J\x1b[H" + claudeInterrupted + "\r\n────\r\n"))
	if r := waitEvent(t, h.d.events, "answered", func(m wire.Msg) bool { return m.ID == "no" }); r.Err != "" {
		t.Fatal(r.Err)
	}
	if st := h.s.currentStatus(); st != "idle" {
		t.Fatalf("after No: %s", st)
	}
}

// #322: an Esc at idle (closing a menu, clearing a draft) or one before Enter stops nothing:
// the interrupt Claude draws again as the next prompt starts leaves it working.
func TestAnEscThatStoppedNothingDoesNotCount(t *testing.T) {
	redraw := []byte("\x1b[2J\x1b[H" + claudeInterrupted + "\r\n────\r\n❯ \r\n────\r\n")
	h := keyHarness(t, "claude")
	h.s.setStatus("idle")
	h.s.runner.OnInput([]byte{0x1b}) // at idle
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "next"})
	h.s.runner.OnOutput(redraw)
	if st := h.s.currentStatus(); st != "working" {
		t.Fatalf("an Esc at idle: %s", st)
	}
	h.s.runner.OnInput([]byte{0x1b}) // working, but Enter follows: a prompt, not a stop
	h.s.runner.OnInput([]byte("\r"))
	h.s.runner.OnOutput(redraw)
	if st := h.s.currentStatus(); st != "working" {
		t.Fatalf("an Esc before Enter: %s", st)
	}
	h.s.runner.OnInput([]byte{0x1b}) // a real stop
	h.s.runner.OnOutput(redraw)
	if st := h.s.currentStatus(); st != "idle" {
		t.Fatalf("the stop: %s", st)
	}
}

// #322: what counts as a stop key in a read.
func TestIsStopKey(t *testing.T) {
	for in, want := range map[string]bool{
		"\x1b": true, "\x1b\x1b\x1b": true, "\x1bq": true, "\x03": true, "x\x03": true,
		"\x1b[27u": true, "\x1b[27;1:1u\x1b[27;1:3u": true, "\x1b[99;5u": true,
		"\x1b[A": false, "\x1bOA": false, "\x1b[200~text\x1b[201~": false, "\x1b[27;1:3u": false,
		"\x1b[I": false, "a": false, "\r": false,
	} {
		if got := isStopKey([]byte(in)); got != want {
			t.Errorf("%q: %t", in, got)
		}
	}
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

// screensHarness runs an agent that draws first a moment after it starts, takes one key, then
// draws after (if any) once wait passed: it draws everything itself, so nothing writes to the
// screen beside pix's PTY reader.
func screensHarness(t *testing.T, first, after string, wait time.Duration) *harness {
	t.Helper()
	dir := t.TempDir()
	write := func(name, text string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return agentHarness(t, "claude", fmt.Sprintf("stty raw -echo; sleep 0.3; cat %q; head -c 1 >/dev/null; sleep %.2f; cat %q; sleep 30",
		write("first", first), wait.Seconds(), write("after", after)))
}

func askBash(t *testing.T, h *harness, command string) wire.Msg {
	t.Helper()
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}})
	return askedQuestion(t, h)
}

// #268: an answer counts once its prompt left the screen; one the agent didn't take (the
// prompt stays) is refused, so Pixie says to answer it in the terminal.
func TestAnAnswerTheAgentDoesntTakeIsRefused(t *testing.T) {
	defer func(w time.Duration) { answerWait = w }(answerWait)
	answerWait = 300 * time.Millisecond
	h := screensHarness(t, bashDialog, "", 0) // takes the key, draws nothing
	q := askBash(t, h, "npm test")
	if err := answer(t, h, q.ID, "yes"); err != "The session didn't take the answer." {
		t.Fatalf("%q", err)
	}
	if err := answer(t, h, q.ID, "yes"); !strings.Contains(err, "no longer waiting") {
		t.Fatalf("typed once: %q", err)
	}
}

func TestAnAnswerCountsOnceThePromptLeftTheScreen(t *testing.T) {
	h := screensHarness(t, bashDialog, "\x1b[2J\x1b[H* Bash(npm test)\r\n", 500*time.Millisecond)
	q := askBash(t, h, "npm test")
	start := time.Now()
	if err := answer(t, h, q.ID, "yes"); err != "" {
		t.Fatal(err)
	}
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("answered after %v, before the prompt left the screen", d)
	}
	if err := answer(t, h, q.ID, "yes"); !strings.Contains(err, "no longer waiting") {
		t.Fatalf("answered once: %q", err)
	}
}

// Claude's next dialog with the same question right after it ("Do you want to proceed?" for the
// next command): the first one's answer still landed (review of #268).
func TestTheNextDialogWithTheSameMarkerStillCountsAsTaken(t *testing.T) {
	next := "\x1b[2J\x1b[H" + strings.ReplaceAll(bashDialog, "npm test", "npm run build")
	h := screensHarness(t, bashDialog, next, 0)
	q := askBash(t, h, "npm test")
	if err := answer(t, h, q.ID, "yes"); err != "" {
		t.Fatal(err)
	}
}

// A prompt that showed but is no longer drawn (the screen was cleared or redrawn without it) is
// not typed into: its leaving couldn't tell whether the answer was taken.
func TestAPromptNoLongerDrawnIsNotTypedInto(t *testing.T) {
	h := screensHarness(t, bashDialog+"\x1b[2J\x1b[H", "", 0)
	q := askBash(t, h, "npm test")
	if err := answer(t, h, q.ID, "yes"); err != "The question isn't on screen." {
		t.Fatalf("%q", err)
	}
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

// Review #3: a Codex approval is its own notification (OSC 9), which text on screen can't fake;
// with its prompt not drawn (yet), it is asked plainly and nothing is typed.
func TestACodexApprovalNotificationAloneIsAskedButNotAnswered(t *testing.T) {
	h := keyHarness(t, "codex")
	h.s.runner.OnOutput([]byte("\x1b]9;Approval requ"))
	h.s.runner.OnOutput([]byte("ested: npm test\x07"))
	q := askedQuestion(t, h)
	if q.Source != "permission" || q.Text != "wants to run npm test" || len(q.Keys) != 0 {
		t.Fatalf("%+v", q)
	}
	if err := answer(t, h, q.ID, "yes"); !strings.Contains(err, "isn't on screen") {
		t.Fatalf("%q", err)
	}
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
	h.s.runner.OnInput([]byte{0x1b})                                // your Esc (#322)
	h.s.runner.OnOutput([]byte(claudeInterrupted + "\r\n────\r\n")) // the input box below it
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

// turnHarness is newHarness once pix is connected (it re-sends its status on registering):
// an event sent before that would be dropped.
func turnHarness(t *testing.T, tool string, agent Launch) *harness {
	t.Helper()
	h := newHarness(t, tool, agent)
	waitEvent(t, h.d.events, "connected", func(m wire.Msg) bool { return m.Event == "status" })
	return h
}

// setClock makes s read the time from *at (under s.mu: the PTY goroutines read it too).
func setClock(s *Session, at *time.Time) {
	s.mu.Lock()
	s.now = func() time.Time { return *at }
	s.mu.Unlock()
}

func turnEvent(t *testing.T, h *harness) wire.Msg {
	t.Helper()
	return waitEvent(t, h.d.events, "turn", func(m wire.Msg) bool { return m.Event == "turn" })
}

// #114: a finished turn goes to DeskPixie with its reply, how long it ran and who started it.
func TestAClaudeTurnReportsItsReplyAndDuration(t *testing.T) {
	h := turnHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	at := time.Unix(1_000, 0)
	setClock(h.s, &at)
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "fix the flaky upload test"})
	at = at.Add(42 * time.Second)
	h.s.Hook("Stop", map[string]any{"last_assistant_message": "Fixed: the upload test is stable now."})
	m := turnEvent(t, h)
	if m.Text != "Fixed: the upload test is stable now." || m.DurationMs != 42_000 || m.Source != "you" {
		t.Fatalf("%+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestATurnPixieStartedSaysSo(t *testing.T) {
	h := turnHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	task := "Task from the coordinator: run the upload tests again."
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "p", Command: "prompt", Text: task})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.ID == "p" })
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": task + "\n"})
	h.s.Hook("Stop", map[string]any{"last_assistant_message": "They pass."})
	if m := turnEvent(t, h); m.Source != "pixie" {
		t.Fatalf("%+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestACodexTurnReportsItsLastMessage(t *testing.T) {
	h := turnHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.Hook("Stop", map[string]any{"type": "agent-turn-complete", "input-messages": []any{"rename foo to bar"},
		"last-assistant-message": "Rename complete and verified."})
	m := turnEvent(t, h)
	if m.Text != "Rename complete and verified." || m.DurationMs != 0 || m.Source != "you" {
		t.Fatalf("no working seen: duration 0; %+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestAnInterruptedTurnDoesNotCountTowardTheNext(t *testing.T) {
	h := turnHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	at := time.Unix(1_000, 0)
	setClock(h.s, &at)
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "a long job"})
	at = at.Add(10 * time.Minute)
	h.s.interrupted() // what the screen marker does
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "something quick"})
	at = at.Add(3 * time.Second)
	h.s.Hook("Stop", map[string]any{"last_assistant_message": "Done."})
	if m := turnEvent(t, h); m.DurationMs != 3_000 {
		t.Fatalf("%+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

func TestTheReplyIsCutOnACharacterBoundary(t *testing.T) {
	got := capUTF8(strings.Repeat("é", 3_000), MaxTurnReplyBytes)
	if len(got) > MaxTurnReplyBytes || !utf8.ValidString(got) || len(got) < MaxTurnReplyBytes-1 {
		t.Fatalf("len %d valid %v", len(got), utf8.ValidString(got))
	}
	if capUTF8("short", MaxTurnReplyBytes) != "short" {
		t.Fatal("short replies are kept")
	}
}

// Review #1: Esc at a permission prompt (status blocked, no Stop) ends that turn too.
func TestAnInterruptAtAPromptDoesNotCountTowardTheNextTurn(t *testing.T) {
	h := turnHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	at := time.Unix(1_000, 0)
	setClock(h.s, &at)
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "a long job"})
	h.s.Hook("Notification", map[string]any{})
	at = at.Add(10 * time.Minute)
	h.s.runner.OnOutput([]byte(claudeInterrupted + "\r\n────\r\n")) // the input box below it (#322)
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "something quick"})
	at = at.Add(3 * time.Second)
	h.s.Hook("Stop", map[string]any{"last_assistant_message": "Done."})
	if m := turnEvent(t, h); m.DurationMs != 3_000 {
		t.Fatalf("%+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// Review #1: a new prompt after a turn that ended without a Stop (an API error) starts afresh.
func TestAPromptAfterAnUnfinishedTurnStartsANewOne(t *testing.T) {
	h := turnHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	at := time.Unix(1_000, 0)
	setClock(h.s, &at)
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "a long job"})
	h.s.Hook("Notification", map[string]any{}) // waiting on you; you answer by typing anew
	at = at.Add(10 * time.Minute)
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "never mind, something quick"})
	at = at.Add(3 * time.Second)
	h.s.Hook("Stop", map[string]any{"last_assistant_message": "Done."})
	if m := turnEvent(t, h); m.DurationMs != 3_000 {
		t.Fatalf("%+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// Review #1: an aborted Codex turn sends no notify; its banner ends the turn.
func TestAnInterruptedCodexTurnDoesNotCountTowardTheNext(t *testing.T) {
	h := turnHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	at := time.Unix(1_000, 0)
	setClock(h.s, &at)
	h.s.runner.OnOutput([]byte(codexBusy))
	at = at.Add(10 * time.Minute)
	h.s.runner.OnOutput([]byte("■ Conversation interrupted - use /feedback if something went wrong"))
	if st := h.s.currentStatus(); st != "idle" {
		t.Fatalf("an interrupted Codex is idle: %s", st)
	}
	h.s.runner.OnOutput([]byte(codexBusy))
	at = at.Add(3 * time.Second)
	h.s.Hook("Stop", map[string]any{"last-assistant-message": "Done."})
	if m := turnEvent(t, h); m.DurationMs != 3_000 {
		t.Fatalf("%+v", m)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// #105: a Claude dialog is relayed as drawn: Claude's summary, the command, its question and
// every option, answered with the option's number.
const bashDialog = "────────────────────────────────────────\r\n Bash command\r\n Run the tests\r\n" +
	"╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌\r\n npm test\r\n╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌╌\r\n Do you want to proceed?\r\n" +
	" ❯ 1. Yes\r\n   2. Yes, and don't ask again for: npm test\r\n   3. No\r\n\r\n Esc to cancel"

func TestAClaudeDialogIsRelayedAsShownAndAnsweredByNumber(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm test"}})
	h.s.runner.OnOutput([]byte(bashDialog))
	q := askedQuestion(t, h)
	if q.Text != "Run the tests" || q.Prompt != "Do you want to proceed?" || q.Body != "npm test" ||
		strings.Join(q.Keys, "|") != "Yes|Yes, and don't ask again for: npm test|No" || q.ToolName != "Bash" {
		t.Fatalf("%+v", q)
	}
	if err := answer(t, h, q.ID, "4"); !strings.Contains(err, "isn't one of its answers") {
		t.Fatalf("%q", err)
	}
	if err := answer(t, h, q.ID, "2"); err != "" {
		t.Fatal(err)
	}
	typed(t, h, '2')
}

func TestADialogThatIsntReadIsAskedWithoutOptionsSoon(t *testing.T) {
	h := keyHarness(t, "claude")
	start := time.Now()
	h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "npm test"}})
	q := askedQuestion(t, h)
	if len(q.Keys) != 0 || q.Prompt != "" || time.Since(start) > 3*time.Second {
		t.Fatalf("%+v after %v", q, time.Since(start))
	}
	h.s.runner.OnOutput([]byte(bashDialog))
	if err := answer(t, h, q.ID, "yes"); err != "" {
		t.Fatal(err)
	}
	typed(t, h, '1')
}

// #105: a Codex approval is relayed as shown; each option is answered with the key Codex shows.
const codexPrompt = "\x1b]9;Approval requested: touch hi.txt\x07" +
	"\r\n  Would you like to run the following command?\r\n\r\n  Reason: Create hi.txt?\r\n\r\n  $ touch hi.txt\r\n\r\n" +
	"› 1. Yes, proceed (y)\r\n  2. Yes, and don't ask again for this (p)\r\n  3. No, and tell Codex what to do differently (esc)\r\n\r\n" +
	"  Press enter to confirm or esc to cancel"

func TestACodexApprovalIsRelayedAsShownAndAnsweredByItsKey(t *testing.T) {
	h := keyHarness(t, "codex")
	h.s.runner.OnOutput([]byte(codexPrompt))
	q := askedQuestion(t, h)
	if q.Prompt != "Would you like to run the following command?" || q.Text != "Create hi.txt?" || q.Body != "touch hi.txt" ||
		strings.Join(q.Keys, "|") != "Yes, proceed|Yes, and don't ask again for this|No, and tell Codex what to do differently" {
		t.Fatalf("%+v", q)
	}
	if err := answer(t, h, q.ID, "2"); err != "" {
		t.Fatal(err)
	}
	typed(t, h, 'p')
}

func TestACodexApprovalsLastOptionIsEscape(t *testing.T) {
	h := keyHarness(t, "codex")
	h.s.runner.OnOutput([]byte(codexPrompt))
	q := askedQuestion(t, h)
	if err := answer(t, h, q.ID, "3"); err != "" {
		t.Fatal(err)
	}
	typed(t, h, 0x1b)
}

// TestSessionTypesPromptAfterIdleNotice (#144): Claude Code notifies "waiting for your input"
// (idle_prompt) after a minute idle. That isn't a prompt waiting on you: the session is still
// idle, and a message sent then must be typed, not held until your next turn.
func TestSessionTypesPromptAfterIdleNotice(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo seen:$x"}})
	h.s.Hook("Notification", map[string]any{"notification_type": "idle_prompt", "message": "Claude is waiting for your input"})
	if st := h.s.currentStatus(); st != "idle" {
		t.Fatalf("an idle notice must leave the session idle, got %q", st)
	}
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "after a minute idle"})
	waitEvent(t, h.d.events, "prompt result", func(m wire.Msg) bool { return m.ID == "1" && m.Err == "" })
	done := make(chan int, 1)
	go func() { done <- h.s.finish() }()
	select {
	case code := <-done:
		var buf bytes.Buffer
		h.s.o.stdout.Close()
		buf.ReadFrom(h.out)
		if code != 0 || !strings.Contains(buf.String(), "after a minute idle") {
			t.Fatalf("code %d out %q", code, buf.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the message was never typed: %d still queued", h.s.queue.Len())
	}
}

// TestPermissionNoticeStillBlocks (#144): a prompt that does wait on you still marks it blocked.
func TestPermissionNoticeStillBlocks(t *testing.T) {
	for _, p := range []map[string]any{{"notification_type": "permission_prompt"}, {"notification_type": "elicitation_dialog"}, {}} {
		h := keyHarness(t, "claude")
		h.s.Hook("Notification", p)
		if st := h.s.currentStatus(); st != "blocked" {
			t.Errorf("%v: got %q, want blocked", p, st)
		}
	}
}

// #165: once the prompt it waited on is answered and a tool finishes, it is working again; it
// stayed "blocked" until the turn ended, ten minutes later.
func TestAToolFinishingAfterAPromptMeansItIsWorkingAgain(t *testing.T) {
	h := keyHarness(t, "claude")
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "fix it"})
	h.s.Hook("Notification", map[string]any{"notification_type": "elicitation_dialog"})
	h.s.Hook("PostToolUse", map[string]any{"tool_name": "AskUserQuestion"})
	if st := h.s.currentStatus(); st != "working" {
		t.Fatalf("got %q, want working", st)
	}
}

// Pixie's message, typed at once or waiting (#183): the prompt result says which, and why it
// waits; a message that waited reports when it reaches the agent.
func TestAPromptResultSaysTypedOrWaitingAndWhy(t *testing.T) {
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo seen:$x; sleep 30"}})
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "right away"})
	waitEvent(t, h.d.events, "typed", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "typed" })

	h.stdinW.WriteString("half a sent") // no Enter: you're typing
	time.Sleep(100 * time.Millisecond)
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "waiting: typing", func(m wire.Msg) bool {
		return m.ID == "2" && m.Status == "waiting" && m.Reason == "typing"
	})
	h.stdinW.WriteString(strings.Repeat("\x7f", len("half a sent"))) // you delete it: it goes in after the quiet time
	waitEvent(t, h.d.events, "delivered", func(m wire.Msg) bool { return m.Kind == wire.KindEvent && m.Event == "delivered" })
	h.s.runner.Hangup()
	h.s.finish()
}

func TestAMessageForAWorkingAgentWaitsAndIsDeliveredWithItsNextTool(t *testing.T) {
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "check the tests"})
	waitEvent(t, h.d.events, "waiting: working", func(m wire.Msg) bool {
		return m.ID == "1" && m.Status == "waiting" && m.Reason == "working"
	})
	h.s.Hook("PostToolUse", map[string]any{})
	waitEvent(t, h.d.events, "delivered", func(m wire.Msg) bool { return m.Kind == wire.KindEvent && m.Event == "delivered" })
	h.s.runner.Hangup()
	h.s.finish()
}

// #185: Claude Code taking a prompt you typed means your line was sent, however your terminal
// sent the Enter (Warp with the kitty keyboard protocol: ESC[13u, which the gate can't read).
// The gate kept counting that text as unsent, and Pixie's messages waited ("you're typing").
func TestAPromptYouSubmittedOpensTheGateHoweverEnterWasSent(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.stdinW.WriteString("redesign the card\x1b[13;1u")
	time.Sleep(100 * time.Millisecond)
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "redesign the card"})
	h.s.Hook("Stop", map[string]any{})
	h.s.gate.mu.Lock()
	h.s.gate.last = time.Now().Add(-TypingQuiet) // past the quiet time
	h.s.gate.mu.Unlock()
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "typed", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "typed" })
	h.s.runner.Hangup()
	h.s.finish()
}

// "Send it anyway" (#187): flush types what waits even while the gate thinks you're typing; a
// working agent still gets it with its next tool, and a question on screen is never typed into.
func TestFlushTypesWhatWaitsDespiteTheGate(t *testing.T) {
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo seen:$x; sleep 30"}})
	h.stdinW.WriteString("half a sent") // the gate holds the message
	time.Sleep(100 * time.Millisecond)
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "waiting", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "waiting" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "flush"})
	waitEvent(t, h.d.events, "flushed", func(m wire.Msg) bool { return m.ID == "2" && m.Status == "typed" })
	if h.s.queue.Len() != 0 {
		t.Fatalf("still queued: %d", h.s.queue.Len())
	}
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "3", Command: "prompt", Text: "later"})
	waitEvent(t, h.d.events, "waiting: working", func(m wire.Msg) bool { return m.ID == "3" && m.Reason == "working" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "4", Command: "flush"})
	waitEvent(t, h.d.events, "still waiting: working", func(m wire.Msg) bool { return m.ID == "4" && m.Status == "waiting" && m.Reason == "working" })
	h.s.runner.Hangup()
	h.s.finish()
}

// stopsOnEsc is an agent that reads until an Esc, then shows Claude Code's interrupt over its
// input box (#320, #322).
const stopsOnEsc = `stty raw -echo; while [ "$(dd bs=1 count=1 2>/dev/null)" != "$(printf '\033')" ]; do :; done; ` +
	`printf '\033[1m⎿  Interrupted · What should Claude do instead?\033[0m\r\n────\r\n❯ \r\n────\r\n'; sleep 30`

// #320: "stop it and send it now": stop presses Esc in a busy agent, waits for it to show it
// stopped, and types what waits.
func TestStopEndsABusyTurnThenTypesWhatWaits(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", stopsOnEsc}})
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "waiting: working", func(m wire.Msg) bool { return m.ID == "1" && m.Reason == "working" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "stop"})
	waitEvent(t, h.d.events, "typed", func(m wire.Msg) bool { return m.ID == "2" && m.Status == "typed" })
	got := h.s.typedForTest()
	if !strings.HasPrefix(got, "\x1b") || !strings.Contains(got, "\x1b[200~from pixie\x1b[201~") || h.s.queue.Len() != 0 {
		t.Fatalf("typed %q, %d still queued", got, h.s.queue.Len())
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// #320: after Pixie's stop button (#309) held what waits, stop-and-send lets it go; and a
// stop button pressed just before doesn't make this stop hold it.
func TestStopSendsWhatPixiesStopHeld(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", stopsOnEsc}})
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "waiting: working", func(m wire.Msg) bool { return m.ID == "1" && m.Reason == "working" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "keys", Keys: []string{"Escape"}}) // the stop button
	waitEvent(t, h.d.events, "idle", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "idle" })
	if !h.s.held() || h.s.queue.Len() != 1 {
		t.Fatalf("not held: %t %d", h.s.held(), h.s.queue.Len())
	}
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "3", Command: "stop"})
	waitEvent(t, h.d.events, "typed", func(m wire.Msg) bool { return m.ID == "3" && m.Status == "typed" })
	if got := h.s.typedForTest(); !strings.HasSuffix(got, "\x1b[200~from pixie\x1b[201~\r") || h.s.held() {
		t.Fatalf("typed %q, held %t", got, h.s.held())
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// #320: a question on screen while pix still says working (Codex never says blocked; Claude's
// dialog shows before its hook): stop doesn't press Esc, which would answer it.
func TestStopNeverAnswersAQuestion(t *testing.T) {
	for _, tool := range []string{"claude", "codex"} {
		h := newHarness(t, tool, Launch{Path: "/bin/sh", Args: []string{"-c", stopsOnEsc}})
		h.s.Hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
		askedQuestion(t, h)
		h.s.setStatus("working")
		wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "from pixie"})
		waitEvent(t, h.d.events, "waiting", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "waiting" })
		wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "stop"})
		waitEvent(t, h.d.events, "waiting: blocked", func(m wire.Msg) bool { return m.ID == "2" && m.Status == "waiting" && m.Reason == "blocked" })
		if got := h.s.typedForTest(); strings.Contains(got, "\x1b") || h.s.queue.Len() != 1 {
			t.Fatalf("%s: typed %q, %d queued", tool, got, h.s.queue.Len())
		}
		h.s.runner.Hangup()
		h.s.finish()
	}
}

// #320: stopping isn't forcing: while you type there the message waits for you, your draft is
// left alone, and the turn isn't cut for a message that can't go in yet.
func TestStopNeverTypesOverYourDraft(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", stopsOnEsc}})
	h.s.setStatus("working")
	h.stdinW.WriteString("my draft")
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if d, _ := h.s.gate.Draft(); d == "my draft" {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the gate never saw the draft")
		}
	}
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "waiting: working", func(m wire.Msg) bool { return m.ID == "1" && m.Reason == "working" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "stop"})
	waitEvent(t, h.d.events, "waiting: typing", func(m wire.Msg) bool { return m.ID == "2" && m.Status == "waiting" && m.Reason == "typing" })
	if got := h.s.typedForTest(); strings.Contains(got, "\x1b") || strings.Contains(got, "from pixie") || h.s.queue.Len() != 1 || h.s.currentStatus() != "working" {
		t.Fatalf("typed %q, %d queued", got, h.s.queue.Len())
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// #320: an agent that doesn't stop in time keeps the message, and says it's busy; with
// nothing waiting there's nothing to stop.
func TestStopWaitsWhenTheAgentKeepsWorking(t *testing.T) {
	defer func(w time.Duration) { InterruptWait = w }(InterruptWait)
	InterruptWait = 200 * time.Millisecond
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "stop"})
	waitEvent(t, h.d.events, "nothing waits", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "typed" })
	if got := h.s.typedForTest(); got != "" {
		t.Fatalf("stopped with nothing waiting: %q", got)
	}
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "waiting: working", func(m wire.Msg) bool { return m.ID == "2" && m.Reason == "working" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "3", Command: "stop"})
	waitEvent(t, h.d.events, "still stopping", func(m wire.Msg) bool { return m.ID == "3" && m.Status == "waiting" && m.Reason == "stopping" })
	if got := h.s.typedForTest(); got != "\x1b" || h.s.queue.Len() != 1 {
		t.Fatalf("typed %q, %d queued", got, h.s.queue.Len())
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// #187: a forced message doesn't take your draft with it. Codex: pix deletes what you typed,
// sends the message, and types your draft back.
func TestFlushSetsYourDraftAsideAndPutsItBack(t *testing.T) {
	h := newHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c", "stty raw -echo; od -An -c; sleep 30"}})
	h.stdinW.WriteString("my draft")
	time.Sleep(100 * time.Millisecond)
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "from pixie"})
	waitEvent(t, h.d.events, "waiting", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "waiting" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "flush"})
	waitEvent(t, h.d.events, "flushed", func(m wire.Msg) bool { return m.ID == "2" && m.Status == "typed" })
	want := strings.Repeat("\x7f", 8) + "\x1b[200~from pixie\x1b[201~\r\x1b[200~my draft\x1b[201~"
	if got := h.s.typedForTest(); !strings.HasSuffix(got, want) {
		t.Fatalf("typed %q\nwant suffix %q", got, want)
	}
	if d, ok := h.s.gate.Draft(); !ok || d != "my draft" {
		t.Fatalf("gate after: %q %t", d, ok)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// Claude Code stashes the draft itself (Ctrl-S) and restores it once the message is sent: pix
// needn't know the text, so it works even after keys pix couldn't follow.
//
// The agents in these flush tests draw nothing themselves: pix's wait for the agent to take a
// paste (#305) settles on the terminal's echo of it.
func TestFlushStashesTheDraftInClaudeCode(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}})
	h.stdinW.WriteString("draft\x1b[Dx") // an arrow: pix can't follow
	time.Sleep(100 * time.Millisecond)
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "one"})
	waitEvent(t, h.d.events, "waiting", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "waiting" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "prompt", Text: "two"})
	waitEvent(t, h.d.events, "waiting 2", func(m wire.Msg) bool { return m.ID == "2" && m.Status == "waiting" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "3", Command: "flush"})
	waitEvent(t, h.d.events, "flushed", func(m wire.Msg) bool { return m.ID == "3" && m.Status == "typed" })
	want := "\x13\x1b[200~one\n\ntwo\x1b[201~\r" // both in one prompt: the restored draft must not join the second
	if got := h.s.typedForTest(); !strings.HasSuffix(got, want) {
		t.Fatalf("typed %q\nwant suffix %q", got, want)
	}
	h.s.runner.Hangup()
	h.s.finish()
}

// typedForTest is everything pix itself typed into the agent (not your keys), recorded by Tap.
func (s *Session) typedForTest() string {
	tapMu.Lock()
	defer tapMu.Unlock()
	return string(tapped[s])
}

// writesForTest is each write pix made to the agent, and when.
func (s *Session) writesForTest() []agentWrite {
	tapMu.Lock()
	defer tapMu.Unlock()
	return append([]agentWrite(nil), writes[s]...)
}

type agentWrite struct {
	b  string
	at time.Time
}

var (
	tapMu  sync.Mutex
	tapped = map[*Session][]byte{}
	writes = map[*Session][]agentWrite{}
)

// #305: Claude Code drops an Enter that comes with a pasted image path, so pix presses Enter on
// its own once the agent has taken the paste: here, an agent that redraws 300 ms after it.
func TestEnterWaitsUntilTheAgentHasTakenThePaste(t *testing.T) {
	h := agentHarness(t, "claude", `stty raw -echo; echo ready; head -c 1 >/dev/null; sleep 0.3; printf '[Image #1]'; sleep 30`)
	agentShown(t, h, "ready")
	msg := "look\nAttached: /tmp/x.png"
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: msg})
	waitEvent(t, h.d.events, "typed", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "typed" })
	w := h.s.writesForTest()
	if len(w) != 2 || w[0].b != string(Paste(msg)) || w[1].b != "\r" {
		t.Fatalf("writes %+v", w)
	}
	if gap := w[1].at.Sub(w[0].at); gap < 300*time.Millisecond || gap >= pasteLimit {
		t.Fatalf("Enter %v after the paste: not after the agent took it", gap)
	}
}

// agentShown waits for the agent to print text.
func agentShown(t *testing.T, h *harness, text string) {
	t.Helper()
	var all []byte
	buf := make([]byte, 256)
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(string(all), text); {
		if time.Now().After(deadline) {
			t.Fatalf("the agent didn't show %q: %q", text, all)
		}
		n, err := h.out.Read(buf)
		all = append(all, buf[:n]...)
		if err != nil {
			t.Fatalf("the agent didn't show %q: %v", text, err)
		}
	}
}

// Keys you type while pix waits for the agent to take its paste go in after its Enter, not
// inside the message it sends.
func TestYourKeysWaitUntilPixiesMessageIsSent(t *testing.T) {
	h := agentHarness(t, "claude", `stty raw -echo; echo ready; head -c 1 >/dev/null; sleep 0.3; printf '[Image #1]'; sleep 30`)
	agentShown(t, h, "ready")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "look"})
	eventually(t, "the paste", func() bool { return len(h.s.writesForTest()) > 0 })
	h.stdinW.WriteString("mine")
	waitEvent(t, h.d.events, "typed", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "typed" })
	eventually(t, "your keys", func() bool { return len(h.s.writesForTest()) == 3 })
	w := h.s.writesForTest()
	if w[0].b != string(Paste("look")) || w[1].b != "\r" || w[2].b != "mine" {
		t.Fatalf("writes %+v", w)
	}
}

// Messages that waited go in as one prompt with one wait (#305): a wait per message could
// outlast the command's time limit.
func TestMessagesThatWaitedGoInAsOnePrompt(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 30"}}) // settles on the echo
	t.Cleanup(func() { h.s.runner.Hangup(); h.s.finish() })
	h.s.setStatus("working")
	for _, id := range []string{"1", "2"} {
		wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: id, Command: "prompt", Text: "message " + id})
		waitEvent(t, h.d.events, "waiting "+id, func(m wire.Msg) bool { return m.ID == id && m.Status == "waiting" })
	}
	h.s.setStatus("idle")
	if !h.s.flushNow() {
		t.Fatal("not typed")
	}
	w := h.s.writesForTest()
	if len(w) != 2 || w[0].b != string(Paste("message 1\n\nmessage 2")) || w[1].b != "\r" {
		t.Fatalf("writes %+v", w)
	}
}

// An agent that never shows the paste still gets its Enter, after pasteLimit, and pix logs why.
func TestEnterGoesAnywayWhenTheAgentShowsNothing(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "pix.log")
	h := newLoggedHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "stty raw -echo; echo ready; sleep 30"}}, openLog(logPath, 2<<20, "claude"))
	waitEvent(t, h.d.events, "connected", func(m wire.Msg) bool { return m.Event == "status" })
	t.Cleanup(func() { h.s.runner.Hangup(); h.s.finish() })
	agentShown(t, h, "ready")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "hello"})
	waitEvent(t, h.d.events, "typed", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "typed" })
	w := h.s.writesForTest()
	if len(w) != 2 || w[1].b != "\r" || w[1].at.Sub(w[0].at) < pasteLimit {
		t.Fatalf("writes %+v", w)
	}
	if b, _ := os.ReadFile(logPath); !strings.Contains(string(b), "pressing Enter after a paste anyway: the agent showed nothing") {
		t.Fatalf("log %q", b)
	}
}

// #189: pix tells the app which build it is; told it's older than the app's, it shows that on
// its tab title until it reconnects to an app that says otherwise.
func TestSessionSendsItsBuildAndShowsWhenOutdated(t *testing.T) {
	h := newHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "sleep 5"}})
	hello := waitEvent(t, h.d.events, "register", func(m wire.Msg) bool { return m.Kind == wire.KindRegister })
	if hello.Build == "" || hello.Build != wire.BuildOf(os.Args[0]) {
		t.Fatalf("build %q", hello.Build)
	}
	if strings.Contains(h.s.titleText(), "restart") {
		t.Fatal(h.s.titleText())
	}
	h.d.outdated.Store(true)
	h.conn.Close()
	eventually(t, "the outdated title", func() bool { return strings.Contains(h.s.titleText(), "restart pix") })
	h.d.outdated.Store(false)
	(<-h.d.conn).Close()
	eventually(t, "the title back", func() bool { return !strings.Contains(h.s.titleText(), "restart") })
}

// eventually retries f until it passes or 5 s have gone by.
func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatal("timed out waiting for " + what)
}

// #309: Pixie's stop button. A message waiting for a working session is typed when its turn
// ends; after Pixie's stop it is held instead, still counted, until you type your next prompt
// there or Pixie sends a new message. Your own Esc is any other end of a turn.

// escAgent is a Claude stand-in that shows Claude's interrupt once it has read n keys (Esc).
func escAgent(n int) Launch {
	return Launch{Path: "/bin/sh", Args: []string{"-c", "stty raw -echo; " + strings.Repeat("head -c 1 >/dev/null; ", n) +
		"printf '\\033[1m⎿  Interrupted · What should Claude do instead?\\033[0m\\r\\n────\\r\\n❯ \\r\\n────\\r\\n'; sleep 30"}}
}

// stoppedWithAWaitingMessage is a working Claude session with one message waiting for it,
// whose turn an Esc then interrupted: Pixie's (pixie) or yours.
func stoppedWithAWaitingMessage(t *testing.T, pixie bool) *harness {
	t.Helper()
	h := turnHarness(t, "claude", escAgent(1))
	t.Cleanup(func() { h.s.runner.Hangup(); h.s.finish() })
	time.Sleep(200 * time.Millisecond) // its stty
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "a long job"})
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "check the tests"})
	waitEvent(t, h.d.events, "waiting", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "waiting" })
	if pixie {
		wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "k", Command: "keys", Keys: []string{"Escape"}})
	} else {
		h.stdinW.Write([]byte{0x1b})
	}
	waitEvent(t, h.d.events, "idle after the interrupt", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "idle" })
	return h
}

func waitQueue(t *testing.T, s *Session, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.queue.Len() != want {
		if time.Now().After(deadline) {
			t.Fatalf("queue %d, want %d", s.queue.Len(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPixiesStopHoldsWhatWaitsUntilYourNextPrompt(t *testing.T) {
	h := stoppedWithAWaitingMessage(t, true)
	time.Sleep(1200 * time.Millisecond) // past two of flushSoon's checks
	if h.s.queue.Len() != 1 || !strings.HasPrefix(h.s.titleText(), "📨 1 ") {
		t.Fatalf("held and still counted: queue %d, title %q", h.s.queue.Len(), h.s.titleText())
	}
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "now do this instead"})
	h.s.Hook("Stop", map[string]any{"last_assistant_message": "Done."})
	waitQueue(t, h.s, 0)
}

func TestPixiesStopHoldsWhatWaitsUntilANewMessage(t *testing.T) {
	h := stoppedWithAWaitingMessage(t, true)
	time.Sleep(700 * time.Millisecond)
	if h.s.queue.Len() != 1 {
		t.Fatal("held", h.s.queue.Len())
	}
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "2", Command: "prompt", Text: "and this"})
	waitEvent(t, h.d.events, "typed", func(m wire.Msg) bool { return m.ID == "2" && m.Status == "typed" })
	waitQueue(t, h.s, 0)
}

func TestYourOwnEscTypesWhatWaitsAsBefore(t *testing.T) {
	h := stoppedWithAWaitingMessage(t, false)
	waitQueue(t, h.s, 0)
}

// pixieStopAfter: a working Claude session with a message waiting, Pixie's Esc sent (which
// the stand-in takes without showing anything), then after (on pix's clock) the interrupt shows.
func pixieStopAfter(t *testing.T, after time.Duration) *harness {
	t.Helper()
	h := turnHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "stty raw -echo; sleep 30"}})
	t.Cleanup(func() { h.s.runner.Hangup(); h.s.finish() })
	at := time.Unix(1_000, 0)
	setClock(h.s, &at)
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "check the tests"})
	waitEvent(t, h.d.events, "waiting", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "waiting" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "k", Command: "keys", Keys: []string{"Escape"}})
	waitEvent(t, h.d.events, "keys result", func(m wire.Msg) bool { return m.ID == "k" && m.Err == "" })
	h.s.mu.Lock()
	at = at.Add(after)
	h.s.mu.Unlock()
	h.s.interrupted() // what the screen marker does
	return h
}

func TestPixiesEscStillCountsWhenTheInterruptIsSlowToShow(t *testing.T) {
	h := pixieStopAfter(t, pixieEscWindow-time.Second) // a slow tool giving way
	time.Sleep(1200 * time.Millisecond)
	if h.s.queue.Len() != 1 {
		t.Fatal("held", h.s.queue.Len())
	}
}

func TestAnEscFromPixieLongAgoDoesNotMakeAnInterruptItsStop(t *testing.T) {
	h := pixieStopAfter(t, pixieEscWindow)
	waitQueue(t, h.s, 0)
}

func TestYourKeyAfterPixiesEscMakesTheInterruptYours(t *testing.T) {
	// Pixie's Esc stopped nothing (the stand-in takes the first key and shows nothing); you
	// press Esc yourself afterwards.
	h := turnHarness(t, "claude", escAgent(2))
	t.Cleanup(func() { h.s.runner.Hangup(); h.s.finish() })
	time.Sleep(200 * time.Millisecond)
	h.s.setStatus("working")
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "k", Command: "keys", Keys: []string{"Escape"}})
	waitEvent(t, h.d.events, "keys result", func(m wire.Msg) bool { return m.ID == "k" && m.Err == "" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "check the tests"})
	waitEvent(t, h.d.events, "waiting", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "waiting" })
	h.stdinW.Write([]byte{0x1b}) // yours
	waitEvent(t, h.d.events, "idle after the interrupt", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "idle" })
	waitQueue(t, h.s, 0)
}

func TestAPromptAfterPixiesEscMakesALaterInterruptNotIts(t *testing.T) {
	h := turnHarness(t, "claude", Launch{Path: "/bin/sh", Args: []string{"-c", "stty raw -echo; sleep 30"}})
	t.Cleanup(func() { h.s.runner.Hangup(); h.s.finish() })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "k", Command: "keys", Keys: []string{"Escape"}})
	waitEvent(t, h.d.events, "keys result", func(m wire.Msg) bool { return m.ID == "k" && m.Err == "" })
	h.s.Hook("UserPromptSubmit", map[string]any{"prompt": "a long job"})
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "check the tests"})
	waitEvent(t, h.d.events, "waiting", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "waiting" })
	h.s.interrupted()
	waitQueue(t, h.s, 0)
}

// The frame Codex draws for an interrupt can still hold the busy footer of the turn it ended.
// Built by hand from the two strings pix watches for (no recording of a real Codex frame).
func TestCodexsInterruptFrameWithItsOldBusyFooterKeepsPixiesHold(t *testing.T) {
	frame := "\033[2K\033[1G• Working (12s • esc to interrupt)\r\n" +
		"\033[2K■ Conversation interrupted - tell the model what to do differently.\r\n"
	h := turnHarness(t, "codex", Launch{Path: "/bin/sh", Args: []string{"-c",
		"stty raw -echo; printf '• Working (1s • esc to interrupt)\\r\\n'; head -c 1 >/dev/null; printf '" + frame + "'; sleep 30"}})
	t.Cleanup(func() { h.s.runner.Hangup(); h.s.finish() })
	// The status, not an event: the busy line may come before turnHarness's first status
	// event, which it then takes.
	eventually(t, "working", func() bool { return h.s.currentStatus() == "working" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "1", Command: "prompt", Text: "check the tests"})
	waitEvent(t, h.d.events, "waiting", func(m wire.Msg) bool { return m.ID == "1" && m.Status == "waiting" })
	wire.Encode(h.conn, wire.Msg{Kind: wire.KindCommand, ID: "k", Command: "keys", Keys: []string{"Escape"}})
	waitEvent(t, h.d.events, "idle after the interrupt", func(m wire.Msg) bool { return m.Event == "status" && m.Status == "idle" })
	time.Sleep(1200 * time.Millisecond)
	if h.s.queue.Len() != 1 || h.s.currentStatus() != "idle" {
		t.Fatalf("held, and idle: queue %d, status %s", h.s.queue.Len(), h.s.currentStatus())
	}
}
