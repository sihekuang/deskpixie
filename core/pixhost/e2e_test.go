package pixhost_test

// End to end (#89): the real pix binary, the real wrapper host, and a fake `claude` on PATH
// that calls `pix hook` the way Claude Code's hooks do. No network, no API key.

import (
	"bufio"
	"context"
	"github.com/creack/pty"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/pixhost"
)

// fakeClaude: reports a session id and idle, then for each line it reads, echoes it, reports
// a prompt (working) and idle again; "quit" exits 7.
const fakeClaude = `#!/bin/sh
echo '{"session_id":"e2e-1"}' | pix hook Stop >/dev/null
while IFS= read -r line; do
  echo "got:$line"
  echo '{"prompt":"fix the upload test","session_id":"e2e-1"}' | pix hook UserPromptSubmit >/dev/null
  case "$line" in *quit*) exit 7;; esac
  echo '{}' | pix hook Stop >/dev/null
done
`

func buildPix(t *testing.T, dir string) {
	t.Helper()
	out, err := exec.Command("go", "build", "-o", filepath.Join(dir, "pix"), "../cmd/pix").CombinedOutput()
	if err != nil {
		t.Fatalf("build pix: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
}

// cleanEnv is the test's environment without anything that would make pix think it is nested.
func cleanEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k == "PIX_SESSION" || k == "PIXIE_AGENT_TOKEN" || k == "PIX_SOCK" || k == "PATH" || k == "HOME" {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func waitFor(t *testing.T, what string, limit time.Duration, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out: %s", what)
}

func only(t *testing.T, h *pixhost.Host) (agent.AgentInfo, bool) {
	l, err := h.List(context.Background())
	if err != nil || len(l) != 1 {
		return agent.AgentInfo{}, false
	}
	return l[0], true
}

func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the pix binary")
	}
	bin, err := os.MkdirTemp("/tmp", "pe")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(bin) })
	buildPix(t, bin)
	sock := filepath.Join(bin, "d", "pix.sock")
	host := pixhost.New(sock, func() []string { return []string{"repo-claude"} }) // a worker already has it
	if err := host.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Shutdown() })
	events := host.Subscribe()

	// A real terminal, as you have: with pipes pix just runs the tool (Passthrough).
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	stdin, stdout, toPix, fromPix := tty, tty, ptmx, ptmx
	cmd := exec.Command(filepath.Join(bin, "pix"), "--description", "e2e session", "claude")
	cmd.Dir = bin // not a git repo: the address is repo-…
	cmd.Env = cleanEnv("PATH="+bin+":/usr/bin:/bin", "HOME="+bin, "DESKPIXIE_PIX_SOCK="+sock)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Signal(syscall.SIGKILL) })
	tty.Close()
	lines := make(chan string, 100)
	go func() {
		s := bufio.NewScanner(fromPix)
		for s.Scan() {
			lines <- strings.TrimRight(s.Text(), "\r")
		}
		close(lines)
	}()
	defer toPix.Close()

	// 1. Registers, with its description, an address clear of the worker's, and idle status.
	var info agent.AgentInfo
	waitFor(t, "registered and idle", 5*time.Second, func() bool {
		var ok bool
		info, ok = only(t, host)
		return ok && info.Status == agent.PaneIdle && info.User.Reachable
	})
	if info.Name != "repo-claude-2" || info.Origin != agent.OriginUser || info.User.Description != "e2e session" || info.User.Tool != "claude" {
		t.Fatalf("%+v %+v", info, info.User)
	}
	addr := info.Name

	// 2. A prompt reaches the agent (typed in: no Claude inbox socket here), status follows its hooks.
	ctx := context.Background()
	if err := host.Prompt(ctx, addr, "hello from pixie"); err != nil {
		t.Fatal(err)
	}
	got := ""
	waitFor(t, "agent saw the prompt", 10*time.Second, func() bool {
		select {
		case l := <-lines:
			if strings.Contains(l, "got:") && strings.Contains(l, "hello from pixie") {
				got = l
			}
		default:
		}
		return got != ""
	})
	sawWorking := false
	waitFor(t, "working, then idle", 5*time.Second, func() bool {
		select {
		case ev := <-events:
			if ev.Agent == addr && ev.Status == agent.PaneWorking {
				sawWorking = true
			}
			return sawWorking && ev.Agent == addr && ev.Status == agent.PaneIdle
		default:
			return false
		}
	})

	// 3. Escape is delivered.
	if err := host.SendKeys(ctx, addr, []string{"Escape"}); err != nil {
		t.Fatal(err)
	}

	// 4. DeskPixie restarts: pix reconnects and keeps its address.
	host.Shutdown()
	host2 := pixhost.New(sock, func() []string { return nil })
	if err := host2.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host2.Shutdown() })
	events2 := host2.Subscribe()
	waitFor(t, "re-registered with the same address", 10*time.Second, func() bool {
		i, ok := only(t, host2)
		return ok && i.Name == addr && i.User.Reachable
	})

	// 5. The agent exits 7: pix exits 7 too, and the session is gone.
	if err := host2.Prompt(ctx, addr, "quit"); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
		t.Fatalf("pix exit: %v", err)
	}
	waitFor(t, "exited event", 5*time.Second, func() bool {
		select {
		case ev := <-events2:
			return ev.Agent == addr && ev.Kind == agent.PaneEventExited
		default:
			return false
		}
	})
	if l, _ := host2.List(ctx); len(l) != 0 {
		t.Fatalf("still listed: %+v", l)
	}
}
