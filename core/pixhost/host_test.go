package pixhost

import (
	"context"
	"net"
	"os"
	"path/filepath"
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
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "auto", Description: "Please fix the flaky upload test"})
	eventually(t, "auto description", func() bool { return listed(t, h)[0].User.Description == "fix flaky upload" })
	if err := h.SetDescription(r.Address, "uploads"); err != nil {
		t.Fatal(err)
	}
	wire.Encode(p.c, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: "agent text"})
	time.Sleep(50 * time.Millisecond)
	u := listed(t, h)[0].User
	if u.Description != "uploads" || len(u.PastDescriptions) != 1 || u.PastDescriptions[0] != "fix flaky upload" {
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

func TestRegisterCarriesTheAppBundleID(t *testing.T) {
	h, sock := newHost(t)
	dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Where: "iTerm", App: "com.googlecode.iterm2"})
	if l := listed(t, h); len(l) != 1 || l[0].User.App != "com.googlecode.iterm2" {
		t.Fatalf("%+v", l[0].User)
	}
}
