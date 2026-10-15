package pix

import (
	"bytes"
	"os"
	"strings"
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
