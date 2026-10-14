// Package smoketest is the shared setup for the opt-in live smokes.
package smoketest

// Shared setup for the opt-in live smokes (`go test -tags live`): one dedicated Herdr session,
// SmokeSession, never the app's "deskpixie" or Herdr's "default". The smokes in different
// packages run as separate test binaries, possibly at once, so a file lock serializes them.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/herdr"
)

// SmokeSession is the only Herdr session the live smokes use (and stop and delete).
const SmokeSession = "deskpixie-smoke"

// LiveSmoke is a locked, clean SmokeSession environment.
type LiveSmoke struct {
	// Bin is the herdr binary; Env its exact server environment (ServerEnv: scrubbed, with
	// HERDR_SESSION=SmokeSession); Home the user's home (Claude Code's login lives there).
	Bin  string
	Env  []agent.EnvVar
	Home string
	// Environ is the parent environment as KEY=value lines (for agentapp.Config.Environ).
	Environ []string
	Runner  agent.CommandRunner
}

// Smoke skips the test unless herdr and claude are installed, takes the cross-package smoke
// lock, removes any SmokeSession left by an aborted run, and stops and deletes the session at
// the end (pass, fail or panic), unless SMOKE_KEEP is set. extraParent is added to the parent
// environment before ServerEnv scrubs it.
func Smoke(t testing.TB, extraParent ...agent.EnvVar) *LiveSmoke {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	pathVar := os.Getenv("PATH")
	bin, ok := herdr.FindHerdr("", pathVar, home, isFile)
	if !ok {
		t.Skip("herdr not found")
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
	env, err := herdr.ServerEnv(parent, SmokeSession, home)
	if err != nil {
		t.Fatalf("server env: %v", err)
	}
	s := &LiveSmoke{Bin: bin, Env: env, Home: home, Environ: os.Environ(), Runner: herdr.ProcessRunner{}}

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
	s.removeSession()
	t.Cleanup(func() {
		if os.Getenv("SMOKE_KEEP") != "" {
			t.Logf("SMOKE_KEEP: leaving session %s. Clean up with:\n  %s session stop %s\n  %s session delete %s",
				SmokeSession, bin, SmokeSession, bin, SmokeSession)
			return
		}
		s.removeSession()
	})
	return s
}

// removeSession stops and deletes SmokeSession (only ever that one); errors are ignored (it
// may not exist).
func (s *LiveSmoke) removeSession() {
	for _, args := range [][]string{{"session", "stop", SmokeSession, "--json"}, {"session", "delete", SmokeSession}} {
		_, _ = s.Runner.Run(append([]string{s.Bin}, args...), s.Env)
	}
}

// Supervisor is a Supervisor for SmokeSession.
func (s *LiveSmoke) Supervisor(t testing.TB) *herdr.Supervisor {
	t.Helper()
	sup, err := herdr.NewSupervisor(s.Bin, s.Env, s.Runner)
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	return sup
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
