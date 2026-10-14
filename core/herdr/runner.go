// Package herdr is agent.SessionHost over Herdr's Unix-socket API (SupportedVersions: Herdr
// 0.7.5, protocol 17, or 0.9.x, protocol 22), plus
// the real agent.CommandRunner (port of ai-mascot crates/session-herdr). Mirrors
// command-center's daemon/src/herdr (read-only reference).
package herdr

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// DefaultTimeout is how long ProcessRunner.Run waits before killing the child.
const DefaultTimeout = 10 * time.Second

// ProcessRunner is the real agent.CommandRunner: os/exec with an exact environment, stdin
// /dev/null, and its own process group (killed whole at the deadline).
type ProcessRunner struct{}

// PatientRunner is a ProcessRunner whose Run waits up to its own timeout instead of
// DefaultTimeout (e.g. `git worktree add` checking out a large repository: 45 s; the cleanup:
// 60 s).
type PatientRunner time.Duration

var (
	_ agent.CommandRunner = ProcessRunner{}
	_ agent.CommandRunner = PatientRunner(0)
)

// runError formats a start failure like Rust's io::Error: an OS error carries "(os error N)",
// which the supervisor keys on (ENOENT is "(os error 2)").
func runError(program string, err error) error {
	var errno syscall.Errno
	if errors.Is(err, exec.ErrNotFound) {
		errno = syscall.ENOENT // not on PATH: Rust's spawn reports ENOENT
	}
	if errno != 0 || errors.As(err, &errno) {
		return fmt.Errorf("%s: %s (os error %d): %w", program, errno.Error(), int(errno), err)
	}
	return fmt.Errorf("%s: %w", program, err)
}

// command builds the child: exactly env (never the parent's), program looked up on env's
// PATH when it has no slash (as Rust's Command does with an explicit PATH).
func command(argv []string, env []agent.EnvVar) (*exec.Cmd, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty command")
	}
	list := make([]string, 0, len(env)) // non-nil: an empty env, not the parent's
	path := ""
	for _, e := range env {
		list = append(list, e.Key+"="+e.Value)
		if e.Key == "PATH" {
			path = e.Value
		}
	}
	program := argv[0]
	if !strings.Contains(program, "/") {
		if found := lookPath(program, path); found != "" {
			program = found
		}
	}
	c := exec.Command(program, argv[1:]...)
	c.Args[0] = argv[0]
	c.Env = list
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c, nil
}

func lookPath(name, path string) string {
	if path == "" {
		p, err := exec.LookPath(name)
		if err != nil {
			return ""
		}
		return p
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// RunWithTimeout runs to completion with exactly env; kills the child's whole process group
// and errors once timeout elapses.
func (ProcessRunner) RunWithTimeout(argv []string, env []agent.EnvVar, timeout time.Duration) (agent.CommandOutput, error) {
	c, err := command(argv, env)
	if err != nil {
		return agent.CommandOutput{}, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return agent.CommandOutput{}, runError(argv[0], err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		outR.Close()
		outW.Close()
		return agent.CommandOutput{}, runError(argv[0], err)
	}
	c.Stdout, c.Stderr = outW, errW
	if err := c.Start(); err != nil {
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return agent.CommandOutput{}, runError(argv[0], err)
	}
	outW.Close()
	errW.Close()
	drain := func(r *os.File) <-chan []byte {
		ch := make(chan []byte, 1)
		go func() {
			b, _ := io.ReadAll(r)
			r.Close()
			ch <- b
		}()
		return ch
	}
	out, errOut := drain(outR), drain(errR)
	deadline := time.Now().Add(timeout)
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		// The whole group: the child may have started helpers that hold the pipes open.
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		_ = c.Process.Kill()
		<-done
		return agent.CommandOutput{}, fmt.Errorf("%s: timed out after %v", argv[0], timeout)
	}
	// Bounded by what is left of the deadline (a helper holding a pipe must not hang us).
	text := func(ch <-chan []byte) string {
		left := max(time.Until(deadline), 200*time.Millisecond)
		select {
		case b := <-ch:
			return strings.ToValidUTF8(string(b), "�")
		case <-time.After(left):
			return ""
		}
	}
	return agent.CommandOutput{Status: int32(c.ProcessState.ExitCode()), Stdout: text(out), Stderr: text(errOut)}, nil
}

// runUntil is RunWithTimeout with timeout, cut short at deadline (an error, without running
// anything, once it has passed).
func (r ProcessRunner) runUntil(argv []string, env []agent.EnvVar, timeout time.Duration, deadline time.Time) (agent.CommandOutput, error) {
	left := time.Until(deadline)
	if left <= 0 {
		first := ""
		if len(argv) > 0 {
			first = argv[0]
		}
		return agent.CommandOutput{}, fmt.Errorf("%s: timed out (out of time)", first)
	}
	return r.RunWithTimeout(argv, env, min(timeout, left))
}

func (r ProcessRunner) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	return r.RunWithTimeout(argv, env, DefaultTimeout)
}

func (r ProcessRunner) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	return r.runUntil(argv, env, DefaultTimeout, deadline)
}

// SpawnDetached starts argv with exactly env in its own process group and does not wait; the
// child is reaped if it exits while we run (it outlives the app otherwise).
func (ProcessRunner) SpawnDetached(argv []string, env []agent.EnvVar) error {
	c, err := command(argv, env)
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return runError(argv[0], err)
	}
	go func() { _ = c.Wait() }()
	return nil
}

func (p PatientRunner) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	return ProcessRunner{}.RunWithTimeout(argv, env, time.Duration(p))
}

func (p PatientRunner) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	return ProcessRunner{}.runUntil(argv, env, time.Duration(p), deadline)
}

func (PatientRunner) SpawnDetached(argv []string, env []agent.EnvVar) error {
	return ProcessRunner{}.SpawnDetached(argv, env)
}
