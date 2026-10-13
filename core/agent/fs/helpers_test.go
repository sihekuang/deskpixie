package agentfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// tempDir is a canonical (symlinks resolved) temp folder; nothing here touches the real HOME.
func tempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o666); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o777); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func mode(t *testing.T, p string) os.FileMode {
	t.Helper()
	m, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return m.Mode().Perm()
}

func count(t *testing.T, dir string) int {
	t.Helper()
	e, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(e)
}

// procRunner is the real agent.CommandRunner for tests (Rust: session_herdr::ProcessRunner /
// PatientRunner): exactly the given env, its own process group, killed whole on timeout.
type procRunner struct{ timeout time.Duration } // 0 = 10 s (Rust DEFAULT_TIMEOUT)

func (p procRunner) limit() time.Duration {
	if p.timeout == 0 {
		return 10 * time.Second
	}
	return p.timeout
}

func envList(env []agent.EnvVar) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		out = append(out, e.Key+"="+e.Value)
	}
	return out
}

func (p procRunner) runWithTimeout(argv []string, env []agent.EnvVar, timeout time.Duration) (agent.CommandOutput, error) {
	if len(argv) == 0 {
		return agent.CommandOutput{}, errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = envList(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 200 * time.Millisecond
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return agent.CommandOutput{}, fmt.Errorf("%s: timed out after %v", argv[0], timeout)
	}
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		return agent.CommandOutput{}, fmt.Errorf("%s: %v", argv[0], err)
	}
	return agent.CommandOutput{Status: int32(cmd.ProcessState.ExitCode()), Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

func (p procRunner) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	return p.runWithTimeout(argv, env, p.limit())
}

func (p procRunner) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	left := time.Until(deadline)
	if left <= 0 {
		return agent.CommandOutput{}, fmt.Errorf("%s: timed out (out of time)", argv[0])
	}
	return p.runWithTimeout(argv, env, min(p.limit(), left))
}

func (p procRunner) SpawnDetached(argv []string, env []agent.EnvVar) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = envList(env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// recordingTrust is agent-core's fakes::RecordingTrust.
type recordingTrust struct{ granted []string }

func (r *recordingTrust) Grant(dir string) ([]string, error) {
	r.granted = append(r.granted, dir)
	return []string{dir}, nil
}
