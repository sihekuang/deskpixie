package pixhost

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/pix/wire"
)

type fakePix struct {
	c   net.Conn
	dec *wire.Decoder
}

func dialPix(t *testing.T, sock string, hello wire.Msg) (*fakePix, wire.Msg) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	hello.Kind = wire.KindRegister
	wire.Encode(c, hello)
	p := &fakePix{c: c, dec: wire.NewDecoder(c)}
	r, err := p.dec.Next()
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

// answer replies to the next command with err ("" for success) and returns the command.
func (p *fakePix) answer(err string) wire.Msg {
	m, _ := p.dec.Next()
	wire.Encode(p.c, wire.Msg{Kind: wire.KindResult, ID: m.ID, Err: err})
	return m
}

func newHost(t *testing.T, taken ...string) (*Host, string) {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "ph")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	sock := filepath.Join(d, "agent", "pix.sock")
	h := New(sock, func() []string { return taken })
	if err := h.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Shutdown)
	return h, sock
}

func listed(t *testing.T, h *Host) []agent.AgentInfo {
	t.Helper()
	l, err := h.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// eventually retries f until it passes or a second has gone by.
func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatal(what)
}

func TestSocketIsPrivate(t *testing.T) {
	_, sock := newHost(t)
	fi, err := os.Stat(sock)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi.Mode(), err)
	}
}

func TestRegisterListsSession(t *testing.T) {
	h, sock := newHost(t, "focus-claude")
	_, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/focus", Cwd: "/x/focus/src", Where: "WebStorm", Description: "login bug"})
	if r.Kind != wire.KindRegistered || r.Address != "focus-claude-2" {
		t.Fatalf("%+v", r)
	}
	l := listed(t, h)
	if len(l) != 1 || l[0].Name != "focus-claude-2" || l[0].Origin != agent.OriginUser || l[0].Cwd != "/x/focus/src" ||
		l[0].User == nil || l[0].User.Description != "login bug" || l[0].User.Where != "WebStorm" || l[0].User.Repo != "focus" ||
		!l[0].User.Reachable || !h.Owns("focus-claude-2") {
		t.Fatalf("%+v %+v", l, l[0].User)
	}
}

func TestTwoSessionsGetDistinctAddresses(t *testing.T) {
	_, sock := newHost(t)
	_, a := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	_, b := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	if a.Address == b.Address {
		t.Fatalf("both %s", a.Address)
	}
}

func TestPromptRoundTrip(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "codex", GitRoot: "/x/api"})
	got := make(chan wire.Msg, 1)
	go func() { got <- p.answer("") }()
	if err := h.Prompt(context.Background(), r.Address, "hi"); err != nil {
		t.Fatal(err)
	}
	if m := <-got; m.Command != "prompt" || m.Text != "hi" {
		t.Fatalf("%+v", m)
	}
}

func TestSendKeysAndClose(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "codex", GitRoot: "/x/api"})
	got := make(chan wire.Msg, 2)
	go func() { got <- p.answer(""); got <- p.answer("") }()
	if err := h.SendKeys(context.Background(), r.Address, []string{"Escape"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(context.Background(), r.Address); err != nil {
		t.Fatal(err)
	}
	if k, c := <-got, <-got; k.Command != "keys" || k.Keys[0] != "Escape" || c.Command != "close" {
		t.Fatalf("%+v %+v", k, c)
	}
}

func TestPromptErrorFromPix(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "codex", GitRoot: "/x/api"})
	go p.answer("busy: 20 messages waiting")
	err := h.Prompt(context.Background(), r.Address, "hi")
	if err == nil || err.Error() != "busy: 20 messages waiting" {
		t.Fatalf("%v", err)
	}
}

func TestMissingAgentIsNoAgent(t *testing.T) {
	h, _ := newHost(t)
	if err := h.Prompt(context.Background(), "nope", "x"); !agent.IsHostError(err, agent.HostNoAgent) {
		t.Fatal(err)
	}
	if err := h.CloseExact(context.Background(), "nope", "p"); err != nil {
		t.Fatalf("CloseExact of a gone session is fine: %v", err)
	}
}

func TestPromptWhilePixDroppedIsNoAgent(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	p.c.Close()
	eventually(t, "session not marked unreachable", func() bool { l := listed(t, h); return len(l) == 1 && !l[0].User.Reachable })
	if err := h.Prompt(context.Background(), r.Address, "x"); !agent.IsHostError(err, agent.HostNoAgent) {
		t.Fatal(err)
	}
}

func TestReRegisterKeepsAddress(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1"})
	p.c.Close()
	eventually(t, "drop not noticed", func() bool { l := listed(t, h); return len(l) == 1 && !l[0].User.Reachable })
	// pix asks for the address it held (Client keeps it), as the real client does.
	_, r2 := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1", Address: r.Address})
	if r2.Address != r.Address {
		t.Fatalf("%s != %s", r2.Address, r.Address)
	}
	if l := listed(t, h); len(l) != 1 || !l[0].User.Reachable {
		t.Fatalf("one reachable session expected: %+v", l)
	}
}

func TestSessionIDLaterStillMatches(t *testing.T) {
	// Claude's session id is only known after the first hook; re-registration uses it.
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "session", SessionID: "s-2"})
	time.Sleep(50 * time.Millisecond)
	p.c.Close()
	eventually(t, "drop not noticed", func() bool { l := listed(t, h); return len(l) == 1 && !l[0].User.Reachable })
	_, r2 := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-2", Address: r.Address})
	if r2.Address != r.Address {
		t.Fatalf("%s != %s", r2.Address, r.Address)
	}
}

func TestDuplicateSessionRefused(t *testing.T) {
	_, sock := newHost(t)
	dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1"})
	_, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1"})
	if r.Kind != wire.KindRefused || r.Reason == "" {
		t.Fatalf("%+v", r)
	}
}

func TestStatusAndExitEvents(t *testing.T) {
	h, sock := newHost(t)
	events := h.Subscribe()
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "status", Status: "working"})
	if ev := <-events; ev.Agent != r.Address || ev.Kind != agent.PaneEventStatus || ev.Status != agent.PaneWorking {
		t.Fatalf("%+v", ev)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "exited"})
	if ev := <-events; ev.Kind != agent.PaneEventExited || ev.Agent != r.Address {
		t.Fatalf("%+v", ev)
	}
	if len(listed(t, h)) != 0 {
		t.Fatal("exited session still listed")
	}
}

func TestDescriptionSources(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: "fix the flaky upload test"})
	eventually(t, "auto description", func() bool { return listed(t, h)[0].User.Description == "fix the flaky upload test" })
	if err := h.SetDescription(r.Address, "uploads"); err != nil {
		t.Fatal(err)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: "agent text"})
	time.Sleep(50 * time.Millisecond)
	u := listed(t, h)[0].User
	if u.Description != "uploads" || len(u.PastDescriptions) != 1 || u.PastDescriptions[0] != "fix the flaky upload test" {
		t.Fatalf("user must win, auto kept as past: %+v", u)
	}
}

func TestDropAfterUnreachable(t *testing.T) {
	h, sock := newHost(t)
	h.unreachableFor = 100 * time.Millisecond
	events := h.Subscribe()
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	p.c.Close()
	select {
	case ev := <-events:
		if ev.Kind != agent.PaneEventExited || ev.Agent != r.Address {
			t.Fatalf("%+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no Exited after the session stayed unreachable")
	}
	if len(listed(t, h)) != 0 {
		t.Fatal("unreachable session should be dropped")
	}
}

func TestVersionRefused(t *testing.T) {
	_, sock := newHost(t)
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte(`{"v":99,"kind":"register"}` + "\n"))
	if r, _ := wire.NewDecoder(c).Next(); r.Kind != wire.KindRefused {
		t.Fatalf("%+v", r)
	}
}

func TestUnsupportedMethods(t *testing.T) {
	h, _ := newHost(t)
	if _, err := h.StartAgent(context.Background(), agent.AgentSpec{Name: "x"}); err == nil {
		t.Fatal("StartAgent: want unsupported")
	}
	if _, err := h.Read(context.Background(), "x", 10); err == nil {
		t.Fatal("Read: want unsupported")
	}
	if h.AttachCommand() != nil || h.EnsureSession(context.Background()) != nil {
		t.Fatal("no attach command; EnsureSession is a no-op")
	}
}

func TestShutdownLeavesNoSocket(t *testing.T) {
	h, sock := newHost(t)
	h.Shutdown()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("socket left behind")
	}
}

func TestRegisterPrefersPreviousAddress(t *testing.T) {
	// After an app restart the new daemon knows nothing; pix says which address it had.
	_, sock := newHost(t)
	_, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Address: "api-claude-2"})
	if r.Address != "api-claude-2" {
		t.Fatalf("%+v", r)
	}
}

func TestPreviousAddressNotTakenFromOthers(t *testing.T) {
	h, sock := newHost(t, "api-claude-2") // a worker has it now
	_, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Address: "api-claude-2"})
	if r.Address == "api-claude-2" {
		t.Fatal("must not take a name in use")
	}
	_, r2 := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Address: r.Address})
	if r2.Address == r.Address {
		t.Fatal("must not take another live session's address")
	}
	_, r3 := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Address: "Not Valid!"})
	if r3.Kind != wire.KindRegistered || r3.Address == "Not Valid!" {
		t.Fatalf("%+v", r3)
	}
	_ = h
}

func TestASessionIDArrivingLaterIsCheckedForDuplicates(t *testing.T) {
	// pix registers before any hook has told it the session id; `pix claude --resume X` in a
	// second tab learns X only at its first hook.
	h, sock := newHost(t)
	a, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	wire.Encode(a.c, wire.Msg{Kind: wire.KindEvent, Event: "session", SessionID: "s-1"})
	b, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	eventually(t, "both listed", func() bool { return len(listed(t, h)) == 2 })
	wire.Encode(b.c, wire.Msg{Kind: wire.KindEvent, Event: "session", SessionID: "s-1"})
	b.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if m, err := b.dec.Next(); err != nil || m.Kind != wire.KindRefused || m.Reason == "" {
		t.Fatalf("the second pix must be refused: %+v %v", m, err)
	}
	eventually(t, "only the first listed", func() bool { return len(listed(t, h)) == 1 })
}

func TestAnUnreachableSessionIsNotTakenOverByAnotherPix(t *testing.T) {
	h, sock := newHost(t)
	a, ra := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1"})
	a.c.Close() // unreachable, kept for a while
	eventually(t, "unreachable", func() bool { l := listed(t, h); return len(l) == 1 && !l[0].User.Reachable })
	// Another pix on the same conversation, not asking for a's address.
	_, rb := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1"})
	if rb.Kind != wire.KindRefused {
		t.Fatalf("must not take over %s: %+v", ra.Address, rb)
	}
	// a itself comes back with its address.
	_, ra2 := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", SessionID: "s-1", Address: ra.Address})
	if ra2.Kind != wire.KindRegistered || ra2.Address != ra.Address {
		t.Fatalf("%+v", ra2)
	}
}

// #148: the agent's pid travels with the session, so a click can find the tab it runs in.
func TestRegisterCarriesTheAgentPid(t *testing.T) {
	h, sock := newHost(t)
	dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Pid: 4242})
	if l := listed(t, h); len(l) != 1 || l[0].User.Pid != 4242 {
		t.Fatalf("%+v", l[0].User)
	}
}

func TestRegisterCarriesTheAppBundleID(t *testing.T) {
	h, sock := newHost(t)
	dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Where: "iTerm", App: "com.googlecode.iterm2"})
	if l := listed(t, h); len(l) != 1 || l[0].User.App != "com.googlecode.iterm2" {
		t.Fatalf("%+v", l[0].User)
	}
}

// changedSoon waits for a change signal.
func changedSoon(t *testing.T, h *Host, what string) {
	t.Helper()
	select {
	case <-h.Changes():
	case <-time.After(time.Second):
		t.Fatal("no change signal: " + what)
	}
}

func TestChangesOnRegisterDescriptionDropAndExit(t *testing.T) {
	h, sock := newHost(t)
	h.unreachableFor = 100 * time.Millisecond
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	changedSoon(t, h, "register")
	if err := h.SetDescription(r.Address, "uploads"); err != nil {
		t.Fatal(err)
	}
	changedSoon(t, h, "description")
	p.c.Close()
	changedSoon(t, h, "unreachable")
	changedSoon(t, h, "dropped")
}

func TestNoDescriptionYetSaysWhatItIs(t *testing.T) {
	h, sock := newHost(t)
	dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/focus", Cwd: "/x/focus"})
	dialPix(t, sock, wire.Msg{Tool: "codex", Cwd: "/x/scratch"})
	got := map[string]string{}
	for _, a := range listed(t, h) {
		got[a.User.Tool] = a.User.Description
	}
	if got["claude"] != "Claude Code in focus, no task yet" || got["codex"] != "Codex in scratch, no task yet" {
		t.Fatalf("%v", got)
	}
}

func TestListShowsStartAndLastActivity(t *testing.T) {
	h, sock := newHost(t)
	before := time.Now()
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/focus"})
	u := listed(t, h)[0].User
	if u.Started.Before(before) || u.LastActive.Before(u.Started) {
		t.Fatalf("started %v last %v", u.Started, u.LastActive)
	}
	first := u.LastActive
	time.Sleep(5 * time.Millisecond)
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "status", Status: "working"})
	eventually(t, "activity", func() bool { return listed(t, h)[0].User.LastActive.After(first) })
}

func TestJunkDescriptionsFromPixAreIgnored(t *testing.T) {
	h, sock := newHost(t)
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/focus"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: "fix the upload test"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto",
		Description: "Another Claude session sent a message:\n<agent-message from=\"a9\">\n[Subagent hand-back] x"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "status", Status: "working"})
	eventually(t, "status", func() bool { return listed(t, h)[0].Status == agent.PaneWorking })
	if d := listed(t, h)[0].User.Description; d != "fix the upload test" {
		t.Fatalf("%q", d)
	}
}

// #105: a prompt a session waits on reaches the controller, and your answer goes back to pix.
func TestAQuestionBecomesAPaneEventAndItsAnswerAPixCommand(t *testing.T) {
	h, sock := newHost(t)
	events := h.Subscribe()
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", App: "com.googlecode.iterm2"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "question", ID: "q1", Source: "permission",
		ToolName: "Bash", Input: `{"command":"npm test"}`})
	ev := <-events
	if ev.Kind != agent.PaneEventQuestion || ev.Agent != r.Address || ev.Question == nil {
		t.Fatalf("%+v", ev)
	}
	q := *ev.Question
	if q.ID != "q1" || q.Label != "api" || q.App != "com.googlecode.iterm2" || q.Kind != "permission" ||
		q.Tool != "Bash" || q.Input != `{"command":"npm test"}` {
		t.Fatalf("%+v", q)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "question", ID: "q2", Source: "menu",
		Text: "Push main?", Keys: []string{"Push", "Don't push"}})
	if ev := <-events; ev.Question == nil || ev.Question.Text != "Push main?" || len(ev.Question.Options) != 2 {
		t.Fatalf("%+v", ev.Question)
	}
	done := make(chan error, 1)
	go func() { done <- h.AnswerQuestion(context.Background(), r.Address, "q2", "2") }()
	if m := p.answer(""); m.Command != "answer" || m.Text != "q2" || m.Source != "2" {
		t.Fatalf("%+v", m)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	go func() { done <- h.AnswerQuestion(context.Background(), r.Address, "q2", "1") }()
	p.answer("The question is no longer on screen.")
	if err := <-done; err == nil || !strings.Contains(err.Error(), "no longer on screen") {
		t.Fatal(err)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "question-gone", ID: "q2"})
	if ev := <-events; ev.Kind != agent.PaneEventQuestionGone || ev.Question == nil || ev.Question.ID != "q2" {
		t.Fatalf("%+v", ev)
	}
}

// #114: a finished turn reaches the controller with what to call the session and where it runs.
func TestATurnBecomesAPaneEventWithTheSessionsLabel(t *testing.T) {
	h, sock := newHost(t)
	events := h.Subscribe()
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Cwd: "/x/api/web", App: "com.jetbrains.WebStorm"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn", Text: "All tests pass.", DurationMs: 42_000, Source: "you"})
	ev := <-events
	if ev.Kind != agent.PaneEventTurn || ev.Agent != r.Address || ev.Turn == nil {
		t.Fatalf("%+v", ev)
	}
	if want := (agent.SessionTurn{Label: "api", Reply: "All tests pass.", App: "com.jetbrains.WebStorm", Duration: 42_000}); *ev.Turn != want {
		t.Fatalf("%+v", *ev.Turn)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: "fix the flaky upload test"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn", Source: "pixie"})
	ev = <-events
	if ev.Turn == nil || ev.Turn.Label != "fix the flaky upload test" || !ev.Turn.FromPixie {
		t.Fatalf("%+v", ev.Turn)
	}
	if len(listed(t, h)) != 1 {
		t.Fatal("a turn is not an exit")
	}
}

func TestATurnLabelFallsBackToTheFolder(t *testing.T) {
	h, sock := newHost(t)
	events := h.Subscribe()
	p, _ := dialPix(t, sock, wire.Msg{Tool: "codex", Cwd: "/x/scratch"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn"})
	if ev := <-events; ev.Turn == nil || ev.Turn.Label != "scratch" {
		t.Fatalf("%+v", ev)
	}
	p2, _ := dialPix(t, sock, wire.Msg{Tool: "codex"})
	wire.Encode(p2.c, wire.Msg{Kind: wire.KindEvent, Event: "turn"})
	if ev := <-events; ev.Turn == nil || ev.Turn.Label != "codex" {
		t.Fatalf("%+v", ev)
	}
}

// e2e of #114: a description pix guessed from your prompt is no name to say ("What is 17 times
// 23? Reply in one short sentence with the is done: …"); the folder is. The agent's is.
func TestASpokenLabelIsNeverAGuessFromThePrompt(t *testing.T) {
	h, sock := newHost(t)
	events := h.Subscribe()
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", Cwd: "/x/scratch"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: "What is 17 times 23? Reply in one"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn"})
	if ev := nextTurn(t, events); ev.Turn.Label != "scratch" {
		t.Fatalf("%+v", ev.Turn)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: "scratch: multiply numbers"})
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn"})
	if ev := nextTurn(t, events); ev.Turn.Label != "scratch: multiply numbers" {
		t.Fatalf("%+v", ev.Turn)
	}
}

func nextTurn(t *testing.T, events <-chan agent.PaneEvent) agent.PaneEvent {
	t.Helper()
	for {
		select {
		case ev := <-events:
			if ev.Turn != nil {
				return ev
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no turn")
		}
	}
}

// TestVerbatimPerSession (#143): each session is verbatim until it's turned off, and the
// setting is its own: another session keeps the default.
func TestVerbatimPerSession(t *testing.T) {
	h, sock := newHost(t)
	_, a := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	_, b := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/web"})
	var _ agent.VerbatimSetter = h
	if on, err := h.Verbatim(a.Address); err != nil || !on {
		t.Fatalf("on by default: %v %v", on, err)
	}
	if err := h.SetVerbatim(a.Address, false); err != nil {
		t.Fatal(err)
	}
	if on, _ := h.Verbatim(a.Address); on {
		t.Fatal("turned off")
	}
	if on, _ := h.Verbatim(b.Address); !on {
		t.Fatal("another session keeps its own setting")
	}
	for _, s := range listed(t, h) {
		if want := s.Name != a.Address; s.User.Verbatim != want {
			t.Errorf("%s listed verbatim %v, want %v", s.Name, s.User.Verbatim, want)
		}
	}
	if _, err := h.Verbatim("nobody"); err == nil {
		t.Fatal("an unknown session is an error")
	}
	if err := h.SetVerbatim("nobody", true); err == nil {
		t.Fatal("an unknown session is an error")
	}
}

// #146: list_sessions shows what each session's last turn came to, so the coordinator can tell
// sessions in one repo apart by what they just did.
func TestListShowsTheLastTurnsGist(t *testing.T) {
	h, sock := newHost(t)
	events := h.Subscribe()
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/focus"})
	if u := listed(t, h)[0].User; u.LastTurn != "" || u.LastTurnAsks {
		t.Fatalf("no turn yet: %+v", u)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn", Text: "I fixed the upload retry.\n\nAll 12 tests pass."})
	<-events
	if u := listed(t, h)[0].User; u.LastTurn != "All 12 tests pass." || u.LastTurnAsks {
		t.Fatalf("%+v", u)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "turn", Text: "Done. Should I open the PR?"})
	<-events
	if u := listed(t, h)[0].User; u.LastTurn != "Should I open the PR?" || !u.LastTurnAsks {
		t.Fatalf("%+v", u)
	}
}

// #183: pix's prompt result says typed or waiting (and why); the host counts the waiting ones
// until pix reports them delivered.
func TestPromptDeliverySaysTypedOrWaitingAndCountsTheWaiting(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	reply := func(res wire.Msg) agent.Delivery {
		t.Helper()
		go func() {
			m, _ := p.dec.Next()
			res.Kind, res.ID = wire.KindResult, m.ID
			wire.Encode(p.c, res)
		}()
		d, err := h.PromptDelivery(context.Background(), r.Address, "hi")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	waiting := func() int {
		l, _ := h.List(context.Background())
		return l[0].User.Waiting
	}
	if d := reply(wire.Msg{Status: "typed"}); d.Status != "typed" || waiting() != 0 {
		t.Fatalf("%+v %d", d, waiting())
	}
	if d := reply(wire.Msg{Status: "waiting", Reason: "working"}); d != (agent.Delivery{Status: "waiting", Reason: "working"}) {
		t.Fatalf("%+v", d)
	}
	reply(wire.Msg{Status: "waiting", Reason: "working"})
	if waiting() != 2 {
		t.Fatalf("waiting %d", waiting())
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "delivered"})
	for deadline := time.Now().Add(2 * time.Second); waiting() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("still waiting %d after delivered", waiting())
		}
	}
}

// #187: Flush asks pix to type what waits; an older pix that doesn't know flush says so plainly.
func TestFlushSaysTypedOrWhyItStillWaits(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	flush := func(res wire.Msg) (agent.Delivery, error) {
		t.Helper()
		go func() {
			m, _ := p.dec.Next()
			if m.Command != "flush" {
				t.Errorf("command %q", m.Command)
			}
			res.Kind, res.ID = wire.KindResult, m.ID
			wire.Encode(p.c, res)
		}()
		return h.Flush(context.Background(), r.Address)
	}
	if d, err := flush(wire.Msg{Status: "typed"}); err != nil || d.Status != "typed" {
		t.Fatalf("%+v %v", d, err)
	}
	if d, err := flush(wire.Msg{Status: "waiting", Reason: "working"}); err != nil || d != (agent.Delivery{Status: "waiting", Reason: "working"}) {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := flush(wire.Msg{Err: "unknown command flush"}); err == nil || !strings.Contains(err.Error(), "restart it") {
		t.Fatalf("old pix: %v", err)
	}
}

// #189: pix says which build it is when it registers. One older than the pix the app ships (or
// from before builds, with none) is registered all the same, marked outdated: in its reply, in
// List, and in what a message to it returns. With no build of its own, the app marks none.
func TestAnOlderPixIsMarkedOutdated(t *testing.T) {
	h, sock := newHost(t)
	h.SetBuild("new")
	_, current := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/a", Build: "new"})
	_, older := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/b", Build: "old"})
	po, unknown := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/c"})
	if current.Kind != wire.KindRegistered || current.Outdated || !older.Outdated || !unknown.Outdated {
		t.Fatalf("%+v %+v %+v", current, older, unknown)
	}
	out := map[string]bool{}
	for _, s := range listed(t, h) {
		out[s.Name] = s.User.Outdated
	}
	if out[current.Address] || !out[older.Address] || !out[unknown.Address] {
		t.Fatal(out)
	}
	go po.answer("")
	d, err := h.PromptDelivery(context.Background(), unknown.Address, "hi")
	if err != nil || !d.Outdated {
		t.Fatalf("%+v %v", d, err)
	}

	h2, sock2 := newHost(t)
	_, r := dialPix(t, sock2, wire.Msg{Tool: "claude", GitRoot: "/x/a", Build: "old"})
	if r.Outdated || listed(t, h2)[0].User.Outdated {
		t.Fatal("marked outdated by an app that doesn't know its own pix")
	}
}
