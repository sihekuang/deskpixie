package pix

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func pipes(t *testing.T) (inR, inW, outR, outW *os.File) {
	inR, inW, _ = os.Pipe()
	outR, outW, _ = os.Pipe()
	t.Cleanup(func() { inR.Close(); inW.Close(); outR.Close(); outW.Close() })
	return
}

func readAll(f *os.File) string {
	var buf bytes.Buffer
	buf.ReadFrom(f)
	return buf.String()
}

func TestPassThroughAndExitCode(t *testing.T) {
	inR, inW, outR, outW := pipes(t)
	r := &Runner{Launch: Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo got:$x; exit 3"}}, Stdin: inR, Stdout: outW}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	inW.WriteString("hello\n")
	code := r.Wait()
	outW.Close()
	if out := readAll(outR); code != 3 || !strings.Contains(out, "got:hello") {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestInjectReachesChild(t *testing.T) {
	inR, _, outR, outW := pipes(t)
	r := &Runner{Launch: Launch{Path: "/bin/sh", Args: []string{"-c", "read x; echo inj:$x"}}, Stdin: inR, Stdout: outW}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	r.WriteToAgent([]byte("pixie\n"))
	r.Wait()
	outW.Close()
	if out := readAll(outR); !strings.Contains(out, "inj:pixie") {
		t.Fatalf("%q", out)
	}
}

func TestInputTapSeesKeys(t *testing.T) {
	inR, inW, _, outW := pipes(t)
	var seen bytes.Buffer
	done := make(chan struct{})
	r := &Runner{Launch: Launch{Path: "/bin/sh", Args: []string{"-c", "read x"}}, Stdin: inR, Stdout: outW,
		OnInput: func(b []byte) { seen.Write(b); close(done) }}
	r.Start()
	inW.WriteString("k\n")
	<-done
	r.Wait()
	if seen.String() != "k\n" {
		t.Fatalf("%q", seen.String())
	}
}

func TestHangupEndsChild(t *testing.T) {
	inR, _, _, outW := pipes(t)
	// Ignores SIGHUP, so the grace kill must end it.
	r := &Runner{Launch: Launch{Path: "/bin/sh", Args: []string{"-c", "trap '' HUP; sleep 30"}}, Stdin: inR, Stdout: outW}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // let the trap install
	start := time.Now()
	r.Hangup()
	r.Wait()
	if d := time.Since(start); d > HangupGrace+time.Second {
		t.Fatalf("took %v", d)
	}
	if err := syscall.Kill(r.Pid(), 0); err == nil {
		t.Fatal("child still alive")
	}
}

// sockDir is a short temp folder: Unix socket paths must stay under 104 bytes.
func sockDir(t *testing.T) string {
	d, err := os.MkdirTemp("/tmp", "pt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestWaitReturnsWhenADescendantKeepsTheTerminal(t *testing.T) {
	// A leftover child in its own session (out of the agent's process group) holds the PTY
	// open, so its output never ends; pix must still finish when the agent does.
	// (On macOS the agent's exit revokes the terminal, so this guards against a regression.)
	inR, _, _, outW := pipes(t)
	pidFile := filepath.Join(t.TempDir(), "orphan")
	r := &Runner{Launch: Launch{Path: "/bin/sh", Args: []string{"-c",
		// The shell exits once the leftover child has written its pid (not after a fixed sleep:
		// on a slow runner perl hadn't written it yet, and the test read pid 0).
		`perl -MPOSIX -e 'exit 0 if fork; POSIX::setsid(); open(F, ">` + pidFile + `.tmp"); print F $$; close F; rename("` + pidFile + `.tmp", "` + pidFile + `"); sleep 4' & ` +
			`i=0; while [ ! -s ` + pidFile + ` ] && [ $i -lt 250 ]; do sleep 0.02; i=$((i+1)); done; exit 5`}}, Stdin: inR, Stdout: outW}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- r.Wait() }()
	select {
	case code := <-done:
		if code != 5 {
			t.Fatalf("code %d", code)
		}
		b, _ := os.ReadFile(pidFile)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
		if pid == 0 || syscall.Kill(pid, 0) != nil {
			t.Fatal("the leftover child should still be running")
		}
		syscall.Kill(pid, syscall.SIGKILL)
	case <-time.After(2 * time.Second):
		t.Fatal("Wait hangs while a descendant holds the terminal")
	}
}

// #305: Settle waits for the agent to show something after a write and then go quiet.
func TestSettleWaitsForTheAgentToShowAndGoQuiet(t *testing.T) {
	r := &Runner{}
	mark := r.Outputs()
	const quiet = 200 * time.Millisecond
	var last atomic.Int64
	go func() {
		for i := 0; i < 3; i++ {
			time.Sleep(5 * time.Millisecond)
			now := time.Now()
			last.Store(now.UnixNano())
			r.sawOutput(now)
		}
	}()
	if err := r.Settle(mark, quiet, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if n := r.Outputs() - mark; n != 3 {
		t.Fatalf("settled after %d of 3 outputs", n)
	}
	if since := time.Since(time.Unix(0, last.Load())); since < quiet {
		t.Fatalf("settled %v after the last output, before %v of quiet", since, quiet)
	}
}

func TestSettleFailsWhenTheAgentShowsNothing(t *testing.T) {
	r := &Runner{}
	r.sawOutput(time.Now()) // before the write: not an answer to it
	const limit = 100 * time.Millisecond
	start := time.Now()
	err := r.Settle(r.Outputs(), 5*time.Millisecond, limit)
	if err == nil || !strings.Contains(err.Error(), "showed nothing") || time.Since(start) < limit {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}

func TestSettleFailsWhenTheAgentNeverStops(t *testing.T) {
	r := &Runner{}
	mark := r.Outputs()
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(5 * time.Millisecond):
				r.sawOutput(time.Now())
			}
		}
	}()
	if err := r.Settle(mark, 200*time.Millisecond, 500*time.Millisecond); err == nil || !strings.Contains(err.Error(), "didn't settle") {
		t.Fatalf("err %v", err)
	}
}
