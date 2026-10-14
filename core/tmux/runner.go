// Package tmux is agent.SessionHost over a private tmux server (`tmux -L <session>`): one
// window per agent, named by the agent's name, with status from the agents' own hooks (Claude),
// their screens (Codex) and tmux's pane state (exits). Spec:
// docs/specs/2026-10-14-tmux-session-host.md (#64).
package tmux

import (
	"context"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/proc"
)

// Output is one finished tmux command: its exit code and output.
type Output struct {
	Code   int
	Stdout string
	Stderr string
}

// Runner runs one tmux command (args after the program; the host puts `-L <socket>` first).
// The error is for a tmux that could not be run (missing: HostNotFound; too slow: HostTimeout);
// a command tmux refused is an Output with a non-zero Code.
type Runner interface {
	Run(ctx context.Context, args ...string) (Output, error)
}

// DefaultTimeout bounds a command whose ctx has no deadline.
const DefaultTimeout = 10 * time.Second

// ExecRunner runs the tmux program with exactly Env (proc.ServerEnv: scrubbed) through Exec.
type ExecRunner struct {
	Bin  string
	Env  []agent.EnvVar
	Exec agent.CommandRunner
}

var _ Runner = ExecRunner{}

func (r ExecRunner) Run(ctx context.Context, args ...string) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DefaultTimeout)
	}
	out, err := r.Exec.RunBy(append([]string{r.Bin}, args...), r.Env, deadline)
	if err != nil {
		return Output{}, runErrorToHost(err)
	}
	return Output{Code: int(out.Status), Stdout: out.Stdout, Stderr: out.Stderr}, nil
}

// runErrorToHost: only a program that isn't there (ENOENT) is NotFound (which turns
// delegation off); a timeout is Timeout; anything else is Other.
func runErrorToHost(err error) error {
	switch {
	case proc.IsNotFound(err):
		return agent.HostError{Kind: agent.HostNotFound, Host: Name}
	case strings.Contains(err.Error(), "timed out"):
		return agent.HostError{Kind: agent.HostTimeout, Detail: "tmux did not answer"}
	}
	return agent.HostError{Kind: agent.HostOther, Detail: "tmux could not be run"}
}
