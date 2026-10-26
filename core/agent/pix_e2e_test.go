package agent_test

// End to end through the runtime: the real pix binary in a terminal (a PTY), the real wrapper
// host, the agent runtime on top, and testdata/fakeclaude standing in for Claude Code. No
// network, no API key, nothing of the real user's: a temporary home and app data folder, and
// sockets of its own.

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/pixhost"
)

// pixSession is one `pix claude` running fakeclaude in a terminal, registered with host.
type pixSession struct {
	t    *testing.T
	dir  string // fakeclaude's FAKE_DIR
	host *pixhost.Host
	addr string // its name, as the coordinator's session tools know it
	keys *os.File
	ctl  *os.File
	done int
}

// startPixSession builds pix and fakeclaude, and runs `pix claude` in a PTY against a host of
// its own.
func startPixSession(t *testing.T) *pixSession {
	t.Helper()
	if testing.Short() {
		t.Skip("builds pix and runs it in a terminal")
	}
	bin, err := os.MkdirTemp("/tmp", "pa") // short: a unix socket's path has a small limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(bin) })
	for out, pkg := range map[string]string{"pix": "../cmd/pix", "claude": "./testdata/fakeclaude"} {
		if b, err := exec.Command("go", "build", "-o", filepath.Join(bin, out), pkg).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", pkg, err, b)
		}
	}
	fake, home, support := filepath.Join(bin, "f"), filepath.Join(bin, "home"), filepath.Join(bin, "support")
	for _, d := range []string{fake, home, support} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := syscall.Mkfifo(filepath.Join(fake, "ctl"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(bin, "d.sock")
	host := pixhost.New(sock, func() []string { return nil })
	if err := host.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Shutdown() })

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(bin, "pix"), "claude")
	cmd.Dir = bin
	cmd.Env = pixEnv("PATH="+bin+":/usr/bin:/bin", "HOME="+home, "DESKPIXIE_SUPPORT_DIR="+support,
		"DESKPIXIE_PIX_SOCK="+sock, "FAKE_DIR="+fake, "TERM=xterm-256color",
		"TMPDIR="+bin) // pix's own folder goes here, short enough for its socket, and goes with bin
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stopPix(cmd) })
	tty.Close()
	go func() { // what the terminal shows; read so the agent never blocks writing it
		s := bufio.NewScanner(ptmx)
		for s.Scan() {
		}
	}()
	t.Cleanup(func() { ptmx.Close() })
	ctl, err := os.OpenFile(filepath.Join(fake, "ctl"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	p := &pixSession{t: t, dir: fake, host: host, keys: ptmx, ctl: ctl}
	p.waitFor("registered", func() bool {
		l, err := host.List(context.Background())
		if err != nil || len(l) != 1 || !l[0].User.Reachable {
			return false
		}
		p.addr = l[0].Name
		return true
	})
	return p
}

// stopPix ends pix as a closed terminal tab would (SIGTERM), so it removes its own folder; only
// a pix that doesn't end by then is killed.
func stopPix(cmd *exec.Cmd) {
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-done
	}
}

// pixEnv is the test's environment without anything that would make pix think it is nested,
// report to the real app, use the real home, or leave its folder in the real temp folder.
func pixEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "PIX_SESSION", "PIX_SOCK", "PIXIE_AGENT_TOKEN", "PATH", "HOME", "DESKPIXIE_SUPPORT_DIR", "DESKPIXIE_PIX_SOCK",
			"TERMINAL_EMULATOR", "TERM_PROGRAM", "__CFBundleIdentifier", "TERM", "TMPDIR":
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func (p *pixSession) waitFor(what string, f func() bool) {
	p.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if f() {
			return
		}
	}
	p.t.Fatalf("timed out: %s", what)
}

// step has fakeclaude take one step ("work", "tool", "stop") and waits until it has.
func (p *pixSession) step(s string) {
	p.t.Helper()
	if _, err := p.ctl.WriteString(s + "\n"); err != nil {
		p.t.Fatal(err)
	}
	p.done++
	n := p.done
	p.waitFor("step "+s, func() bool { return strings.Count(p.read("done"), "ok ") >= n })
}

// read is one of fakeclaude's files ("" before it exists).
func (p *pixSession) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(p.dir, name))
	return string(b)
}

// prompts are the prompts pix typed and sent, in order.
func (p *pixSession) prompts() []string {
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(p.read("prompts")), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// status is the session's status as the host lists it.
func (p *pixSession) status() PaneStatus {
	l, _ := p.host.List(context.Background())
	if len(l) != 1 {
		return PaneUnknown
	}
	return l[0].Status
}

// type is you typing in its terminal.
func (p *pixSession) typeKeys(s string) {
	p.t.Helper()
	if _, err := p.keys.WriteString(s); err != nil {
		p.t.Fatal(err)
	}
}

// pixRuntime is the agent runtime with the coordinator on a fake host and your sessions on
// p's real host; voice is the voice link (nil: a recording one).
func pixRuntime(t *testing.T, p *pixSession, cfg RuntimeConfig, voice VoiceLink) (*AgentHandle, *agenttest.RecordingVoiceLink) {
	t.Helper()
	rec := &agenttest.RecordingVoiceLink{}
	if voice == nil {
		voice = rec
	}
	coord := agenttest.NewFakeSessionHost().ManualTurns()
	clock := agenttest.ClockAt(1_000_000)
	alerts := &agenttest.RecordingNotifier{}
	deps := Deps{
		Host: NewAgentHost(NewCompositeHost(coord, p.host), clock, CoordinatorRecipe{Cwd: t.TempDir()}), Voice: voice,
		Notifier: alerts, Pixie: alerts, Status: &agenttest.RecordingStatus{}, Planner: &agenttest.FakePlanner{},
		Focus: &agenttest.FakeFocusSink{}, TerminalDir: t.TempDir(), Store: &agenttest.MemStore{}, Clock: clock,
		IDs: &agenttest.SeqIds{}, Updates: &agenttest.RecordingUpdates{}, Chat: &recChat{}, Attachments: &fakeAttach{},
	}
	h := NewRuntime(deps, cfg)
	go h.Run(t.Context())
	return h, rec
}

// sessionCall is the coordinator calling one of the session tools on p's session.
func sessionCall(t *testing.T, h *AgentHandle, call McpCall) map[string]any {
	t.Helper()
	reply := make(chan McpReply, 1)
	h.Deliver(HookEvent{Kind: HookMcp, Agent: Coordinator, Call: call, McpReply: reply})
	select {
	case r := <-reply:
		if r.Err != nil {
			t.Fatalf("%v: %v", call.Kind, r.Err)
		}
		v, _ := r.Value.(map[string]any)
		return v
	case <-time.After(15 * time.Second):
		t.Fatalf("%v: no reply", call.Kind)
	}
	return nil
}
