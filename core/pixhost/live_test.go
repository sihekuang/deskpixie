//go:build live

package pixhost_test

// Opt-in live smokes (#89): the real pix binary wrapping the installed Claude Code and Codex, driven
// through the real wrapper host. Run with:
//
//	go test -tags live -run Live -v -timeout 10m ./pixhost/
//
// Throwaway: a temp workspace (Claude Code records its folder trust in ~/.claude.json, as the
// Herdr smokes do), a private pix socket (never the app's), and SMOKE_MODEL (default haiku).
// Claude's permission prompts stay on; the prompts ask for words only.

import (
	"context"
	"fmt"
	"github.com/creack/pty"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/pixhost"
)

func liveEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PIX_SESSION" || k == "PIXIE_AGENT_TOKEN" || k == "PIX_SOCK" || strings.HasPrefix(k, "CLAUDE_CODE_") || k == "CLAUDECODE" {
			continue // not nested in this (or any) Claude session
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// waitEvent waits for an event about addr that pred accepts.
func waitEvent(t *testing.T, events <-chan agent.PaneEvent, addr, what string, limit time.Duration, pred func(agent.PaneEvent) bool) {
	t.Helper()
	deadline := time.After(limit)
	for {
		select {
		case ev := <-events:
			if ev.Agent == addr && pred(ev) {
				t.Logf("✓ %s", what)
				return
			}
		case <-deadline:
			t.Fatalf("timed out: %s", what)
		}
	}
}

func TestLivePixWrapsClaude(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	model := os.Getenv("SMOKE_MODEL")
	if model == "" {
		model = "haiku"
	}
	home, _ := os.UserHomeDir()
	root, err := os.MkdirTemp("/tmp", "pixlive")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	bin := filepath.Join(root, "bin")
	work := filepath.Join(root, "work")
	os.MkdirAll(bin, 0o755)
	os.MkdirAll(work, 0o755)
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "pix"), "../cmd/pix").CombinedOutput(); err != nil {
		t.Fatalf("build pix: %v\n%s", err, out)
	}
	if _, err := agentfs.NewClaudeTrustStore(filepath.Join(home, ".claude.json"), []string{root}).Grant(work); err != nil {
		t.Fatalf("trust: %v", err)
	}

	sock := filepath.Join(root, "pix.sock")
	host := pixhost.New(sock, func() []string { return nil })
	if err := host.Listen(); err != nil {
		t.Fatal(err)
	}
	events := host.Subscribe()

	screen, _ := os.Create(filepath.Join(root, "screen.log"))
	// A real terminal, as you have: with pipes pix just runs the tool (Passthrough).
	ptmx, stdin, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	go io.Copy(screen, ptmx)
	cmd := exec.Command(filepath.Join(bin, "pix"), "claude", "--model", model)
	cmd.Dir = work
	cmd.Env = liveEnv("PATH="+bin+":"+os.Getenv("PATH"), "DESKPIXIE_PIX_SOCK="+sock)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdin, stdin
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGKILL)
		if t.Failed() {
			b, _ := os.ReadFile(screen.Name())
			if len(b) > 4000 {
				b = b[len(b)-4000:]
			}
			t.Logf("pix screen (tail):\n%q", b)
		}
	})
	stdin.Close()

	// 1. Registers.
	var addr string
	for deadline := time.Now().Add(30 * time.Second); addr == "" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if l, _ := host.List(context.Background()); len(l) == 1 {
			addr = l[0].Name
		}
	}
	if addr == "" {
		t.Fatal("pix did not register")
	}
	t.Logf("✓ registered as %s", addr)
	time.Sleep(5 * time.Second) // Claude's TUI and session record come up

	// 2. A prompt through Claude's inbox socket starts a turn; hooks report working then idle.
	ctx := context.Background()
	if err := host.Prompt(ctx, addr, "Call the set_description tool with the text \"live smoke test\", then reply with the single word PONG."); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, addr, "prompt → working", 60*time.Second, func(ev agent.PaneEvent) bool { return ev.Status == agent.PaneWorking })
	waitEvent(t, events, addr, "turn → idle", 120*time.Second, func(ev agent.PaneEvent) bool { return ev.Status == agent.PaneIdle })
	// The agent's set_description (via pix mcp) beats the automatic one.
	if l, _ := host.List(ctx); len(l) != 1 || l[0].User.Description != "live smoke test" {
		t.Fatalf("description: %+v", l[0].User)
	} else {
		t.Logf("✓ set_description → %q (past %v)", l[0].User.Description, l[0].User.PastDescriptions)
	}

	// 3. Escape interrupts a long turn.
	if err := host.Prompt(ctx, addr, "Write the numbers from 1 to 400, one per line, with no other text."); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, addr, "long prompt → working", 60*time.Second, func(ev agent.PaneEvent) bool { return ev.Status == agent.PaneWorking })
	time.Sleep(2 * time.Second)
	if err := host.SendKeys(ctx, addr, []string{"Escape"}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, addr, "Escape → idle", 30*time.Second, func(ev agent.PaneEvent) bool { return ev.Status.IsReady() })

	// 4. DeskPixie restarts: pix reconnects with the same address.
	host.Shutdown()
	host2 := pixhost.New(sock, func() []string { return nil })
	if err := host2.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host2.Shutdown)
	events2 := host2.Subscribe()
	ok := false
	for deadline := time.Now().Add(40 * time.Second); !ok && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		l, _ := host2.List(ctx)
		ok = len(l) == 1 && l[0].Name == addr && l[0].User.Reachable
	}
	if !ok {
		t.Fatal("pix did not come back with the same address")
	}
	t.Logf("✓ same address after a restart")

	// 5. Close ends Claude; pix exits and reports it.
	if err := host2.Close(ctx, addr); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events2, addr, "close → exited", 15*time.Second, func(ev agent.PaneEvent) bool { return ev.Kind == agent.PaneEventExited })
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		t.Logf("✓ pix exited (%v)", err)
	case <-time.After(10 * time.Second):
		t.Fatal("pix still running after close")
	}
}

func TestLivePixWrapsCodex(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("codex not installed")
	}
	root, err := os.MkdirTemp("/tmp", "pixcodex")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() { os.RemoveAll(root) })
	bin, work := filepath.Join(root, "bin"), filepath.Join(root, "work")
	os.MkdirAll(bin, 0o755)
	os.MkdirAll(work, 0o755)
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "pix"), "../cmd/pix").CombinedOutput(); err != nil {
		t.Fatalf("build pix: %v\n%s", err, out)
	}
	sock := filepath.Join(root, "pix.sock")
	host := pixhost.New(sock, func() []string { return nil })
	if err := host.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Shutdown)
	events := host.Subscribe()
	screen, _ := os.Create(filepath.Join(root, "screen.log"))
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120})
	go io.Copy(screen, ptmx)
	args := []string{"codex", "-c", fmt.Sprintf("projects.%q.trust_level=\"trusted\"", work)}
	if m := os.Getenv("SMOKE_CODEX_MODEL"); m != "" {
		args = append(args, "-m", m)
	}
	cmd := exec.Command(filepath.Join(bin, "pix"), args...)
	cmd.Dir = work
	cmd.Env = liveEnv("PATH="+bin+":"+os.Getenv("PATH"), "DESKPIXIE_PIX_SOCK="+sock)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tty.Close()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGKILL)
		if t.Failed() {
			b, _ := os.ReadFile(screen.Name())
			if len(b) > 3000 {
				b = b[len(b)-3000:]
			}
			t.Logf("pix screen (tail):\n%q", b)
		}
	})
	var addr string
	for deadline := time.Now().Add(30 * time.Second); addr == "" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if l, _ := host.List(context.Background()); len(l) == 1 {
			addr = l[0].Name
		}
	}
	if addr == "" {
		t.Fatal("pix did not register")
	}
	t.Logf("✓ registered as %s", addr)
	time.Sleep(8 * time.Second) // Codex's TUI comes up
	ctx := context.Background()
	if err := host.Prompt(ctx, addr, "Reply with the single word PONG and nothing else."); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, addr, "typed prompt → working (busy marker)", 90*time.Second, func(ev agent.PaneEvent) bool { return ev.Status == agent.PaneWorking })
	waitEvent(t, events, addr, "turn end → idle (notify)", 180*time.Second, func(ev agent.PaneEvent) bool { return ev.Status == agent.PaneIdle })
	if err := host.Close(ctx, addr); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, addr, "close → exited", 15*time.Second, func(ev agent.PaneEvent) bool { return ev.Kind == agent.PaneEventExited })
}
