package pix

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// HangupGrace is how long the agent gets after a hangup before it is killed.
const HangupGrace = 2 * time.Second

// Runner runs the agent in a PTY and copies bytes both ways, untouched.
type Runner struct {
	Launch Launch
	Stdin  *os.File // your terminal
	Stdout *os.File
	// OnOutput sees the agent's output after it is written; OnInput sees your keys before
	// they are forwarded.
	OnOutput func([]byte)
	OnInput  func([]byte)
	Tap      func([]byte) // sees what is written to the agent (tests)

	cmd     *exec.Cmd
	ptmx    *os.File
	mu      sync.Mutex // one writer to the agent at a time (your keys and injections)
	done    chan struct{}
	code    int
	restore func()

	outMu sync.Mutex
	outN  uint64        // reads of the agent's output so far
	outAt time.Time     // the last one
	outCh chan struct{} // closed and replaced on each read
}

// Start starts the agent. Raw mode and resize forwarding only when Stdin is a terminal.
func (r *Runner) Start() error {
	r.cmd = exec.Command(r.Launch.Path, r.Launch.Args...)
	r.cmd.Env = append(os.Environ(), r.Launch.Env...)
	tty := term.IsTerminal(int(r.Stdin.Fd()))
	var size *pty.Winsize
	if tty {
		size, _ = pty.GetsizeFull(r.Stdin)
	}
	ptmx, err := pty.StartWithSize(r.cmd, size)
	if err != nil {
		return err
	}
	r.ptmx = ptmx
	r.done = make(chan struct{})
	if tty {
		if old, err := term.MakeRaw(int(r.Stdin.Fd())); err == nil {
			r.restore = func() { term.Restore(int(r.Stdin.Fd()), old) }
		}
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		go func() {
			defer signal.Stop(winch)
			for {
				select {
				case <-winch:
					pty.InheritSize(r.Stdin, r.ptmx)
				case <-r.done:
					return
				}
			}
		}()
	}
	go r.copyIn()
	outDone := make(chan struct{})
	go func() { r.copyOut(); close(outDone) }()
	go func() {
		err := r.cmd.Wait()
		<-outDone // the agent's last output reaches the terminal first
		r.code = exitCode(err)
		if r.restore != nil {
			r.restore()
		}
		close(r.done)
	}()
	return nil
}

func (r *Runner) copyIn() {
	buf := make([]byte, 4096)
	for {
		n, err := r.Stdin.Read(buf)
		if n > 0 {
			if r.OnInput != nil {
				r.OnInput(buf[:n])
			}
			if r.WriteToAgent(buf[:n]) != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (r *Runner) copyOut() {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.ptmx.Read(buf)
		if n > 0 {
			r.sawOutput(time.Now()) // counted before anyone sees it, so a write after it isn't answered by it
			r.Stdout.Write(buf[:n])
			if r.OnOutput != nil {
				r.OnOutput(buf[:n])
			}
		}
		if err != nil { // EIO once the agent's side closes
			return
		}
	}
}

// WriteToAgent writes b to the agent as if typed.
func (r *Runner) WriteToAgent(b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Tap != nil {
		r.Tap(b)
	}
	_, err := r.ptmx.Write(b)
	return err
}

func (r *Runner) sawOutput(now time.Time) {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	r.outN++
	r.outAt = now
	if r.outCh != nil {
		close(r.outCh)
	}
	r.outCh = make(chan struct{})
}

// Outputs counts the agent's output so far, to wait for what it shows after a write (Settle).
func (r *Runner) Outputs() uint64 {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	return r.outN
}

// Settle waits until the agent has shown something since Outputs was mark and then shown
// nothing for quiet: it has taken what was written and redrawn. An error after limit when it
// showed nothing, or never stopped.
func (r *Runner) Settle(mark uint64, quiet, limit time.Duration) error {
	deadline := time.After(limit)
	for {
		r.outMu.Lock()
		if r.outCh == nil {
			r.outCh = make(chan struct{})
		}
		n, at, changed := r.outN, r.outAt, r.outCh
		r.outMu.Unlock()
		var settled <-chan time.Time
		if n > mark {
			left := quiet - time.Since(at)
			if left <= 0 {
				return nil
			}
			settled = time.After(left)
		}
		select {
		case <-changed:
		case <-settled:
		case <-deadline:
			if n == mark {
				return fmt.Errorf("the agent showed nothing in %v", limit)
			}
			return fmt.Errorf("the agent's screen didn't settle in %v", limit)
		}
	}
}

// Pid is the agent's process id.
func (r *Runner) Pid() int { return r.cmd.Process.Pid }

// Size is the agent's terminal size (rows, cols); 24x80 when it has none.
func (r *Runner) Size() (int, int) {
	if r.ptmx != nil {
		if ws, err := pty.GetsizeFull(r.ptmx); err == nil && ws.Rows > 0 && ws.Cols > 0 {
			return int(ws.Rows), int(ws.Cols)
		}
	}
	return 24, 80
}

// Hangup sends SIGHUP to the agent's process group, then SIGKILL after HangupGrace.
func (r *Runner) Hangup() {
	pgid := r.cmd.Process.Pid // the PTY start makes the agent a session and group leader
	syscall.Kill(-pgid, syscall.SIGHUP)
	select {
	case <-r.done:
	case <-time.After(HangupGrace):
		syscall.Kill(-pgid, syscall.SIGKILL)
	}
}

// Wait waits for the agent and returns its exit code (128+signal when killed).
func (r *Runner) Wait() int {
	<-r.done
	r.ptmx.Close()
	return r.code
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}
