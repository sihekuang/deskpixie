// Package smoketest is the shared setup for the opt-in live smokes on the tmux session host.
package smoketest

// One dedicated tmux server, `tmux -L deskpixie-smoke` (socket and session SmokeSession), never
// the app's "deskpixie" and never the default socket. The smokes (these and Herdr's) run as
// separate test binaries, possibly at once, so they share Herdr's lock file.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/proc"
	"github.com/sihekuang/deskpixie/core/tmux"
)

// SmokeSession is the only tmux server (socket) and session the live smokes use, and kill.
const SmokeSession = "deskpixie-smoke"

// LiveSmoke is a locked, clean SmokeSession environment.
type LiveSmoke struct {
	// Bin is tmux; Env its exact server environment (tmux.ServerEnv: scrubbed); Home the
	// user's home (Claude Code's login lives there).
	Bin  string
	Env  []agent.EnvVar
	Home string
	// Environ is the parent environment as KEY=value lines (for agentapp.Config.Environ).
	Environ []string
	Runner  agent.CommandRunner
}

// Smoke skips the test unless tmux and claude are installed, takes the smoke lock, kills any
// SmokeSession server left by an aborted run, and kills it again at the end (pass, fail or
// panic), unless SMOKE_KEEP is set. extraParent is added to the parent environment before
// ServerEnv scrubs it.
func Smoke(t testing.TB, extraParent ...agent.EnvVar) *LiveSmoke {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	bin, ok := tmux.FindTmux("", os.Getenv("PATH"), isFile)
	if !ok {
		t.Skip("tmux not found")
	}
	if !isFile(filepath.Join(home, ".local/bin/claude")) {
		if _, err := exec.LookPath("claude"); err != nil {
			t.Skip("claude not found")
		}
	}
	var parent []agent.EnvVar
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		parent = append(parent, agent.EnvVar{Key: k, Value: v})
	}
	parent = append(parent, extraParent...)
	env, err := tmux.ServerEnv(parent, SmokeSession, home)
	if err != nil {
		t.Fatalf("server env: %v", err)
	}
	s := &LiveSmoke{Bin: bin, Env: env, Home: home, Environ: os.Environ(), Runner: proc.ProcessRunner{}}

	// The same lock file as Herdr's smokes (core/herdr/smoketest).
	lock, err := os.OpenFile(filepath.Join(os.TempDir(), "deskpixie-smoke.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("smoke lock: %v", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("smoke lock: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	})
	s.KillServer()
	t.Cleanup(func() {
		if os.Getenv("SMOKE_KEEP") != "" {
			t.Logf("SMOKE_KEEP: leaving the tmux server. Look with:\n  %s -L %s attach\nClean up with:\n  %s -L %s kill-server",
				bin, SmokeSession, bin, SmokeSession)
			return
		}
		s.KillServer()
	})
	return s
}

// KillServer ends the SmokeSession server (only ever that one); errors are ignored (it may not
// run).
func (s *LiveSmoke) KillServer() {
	_, _ = s.Runner.Run([]string{s.Bin, "-L", SmokeSession, "kill-server"}, s.Env)
}

// Host is a tmux host on SmokeSession.
func (s *LiveSmoke) Host() *tmux.Host {
	return tmux.NewHost(s.Bin, SmokeSession, s.Home, tmux.ExecRunner{Bin: s.Bin, Env: s.Env, Exec: s.Runner})
}

// CanonicalTemp is a fresh temp dir by its real path (macOS's temp dir is under the /var
// symlink; the worktree adapter refuses symlinks and trust is keyed by real path).
func CanonicalTemp(t testing.TB) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
