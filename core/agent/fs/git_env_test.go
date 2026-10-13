package agentfs

import (
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// recordingRunner runs git for real and records every argv and env it was given.
type recordingRunner struct {
	procRunner
	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	argv []string
	env  []agent.EnvVar
}

func (r *recordingRunner) record(argv []string, env []agent.EnvVar) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, recordedCall{append([]string(nil), argv...), append([]agent.EnvVar(nil), env...)})
}

func (r *recordingRunner) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	r.record(argv, env)
	return r.procRunner.Run(argv, env)
}

func (r *recordingRunner) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	r.record(argv, env)
	return r.procRunner.RunBy(argv, env, deadline)
}

// D-317/D-318/D-322: every git call (ResolveRepo, Ensure, Audit) is
// `git -C <dir> -c core.hooksPath=/dev/null -c core.fsmonitor=false …` with exactly PATH, HOME,
// GIT_TERMINAL_PROMPT=0, LC_ALL=C and GIT_CEILING_DIRECTORIES=<parent of the projects root>.
func TestEveryGitCallHasTheFixedArgvPrefixAndExactEnv(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	rec := &recordingRunner{}
	g := f.fsWith(rec)
	if _, err := g.ResolveRepo("focus"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Ensure(r, "fix"); err != nil {
		t.Fatal(err)
	}
	if err := g.Audit(r, "fix"); err != nil {
		t.Fatal(err)
	}
	canonRoot, err := canonicalize(f.root)
	if err != nil {
		t.Fatal(err)
	}
	wantEnv := []agent.EnvVar{
		{Key: "PATH", Value: "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"},
		{Key: "HOME", Value: f.home},
		{Key: "GIT_TERMINAL_PROMPT", Value: "0"},
		{Key: "LC_ALL", Value: "C"},
		{Key: "GIT_CEILING_DIRECTORIES", Value: filepath.Dir(canonRoot)},
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.calls) < 3 {
		t.Fatalf("only %d git calls", len(rec.calls))
	}
	for _, c := range rec.calls {
		if len(c.argv) < 7 || c.argv[0] != "git" || c.argv[1] != "-C" || c.argv[2] == "" ||
			!reflect.DeepEqual(c.argv[3:7], []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}) {
			t.Fatalf("argv prefix: %q", c.argv)
		}
		if !reflect.DeepEqual(c.env, wantEnv) {
			t.Fatalf("env for %q:\n got %v\nwant %v", c.argv, c.env, wantEnv)
		}
	}
}
