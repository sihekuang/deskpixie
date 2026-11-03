//go:build live

package agentapp_test

// Opt-in live check of #428: two messages typed to your session in the chat window, the same
// words one right after the other, both reach it: the coordinator never folds them into one
// (pix takes each, sent or waiting).
//
//	go test -tags live -run LiveEachMessage -v -timeout 15m ./app/agentapp/
//
// Throwaway like LiveMenuAnswered: the Herdr smoke session, a free hook port, a private pix
// socket and log folder, and a real `pix claude` (haiku) in its own terminal.

import (
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

func TestLiveEachMessageToASessionGoesIn(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	logs := &syncLog{}
	log.SetOutput(io.MultiWriter(os.Stderr, logs))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	short, err := os.MkdirTemp("/tmp", "pxe")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	sock := filepath.Join(short, "pix.sock")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	if port == 48217 {
		t.Fatal("the smoke must never use the app's port 48217")
	}
	s := settings.Default().Agent
	s.Session = smoketest.SmokeSession
	s.Port = port
	s.ProjectsRoot = filepath.Join(tmp, "projects")
	s.Workers.Enabled = false
	s.Warm = false
	s.KeepPanesOnQuit = false
	model := "sonnet"
	if m := os.Getenv("SMOKE_MODEL"); m != "" {
		model = m
	}
	s.Model = &model
	os.MkdirAll(s.ProjectsRoot, 0o755)
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: filepath.Join(tmp, "data"), Home: sm.Home, Environ: sm.Environ, PixSocket: sock,
		Hooks:  app.AgentHooks{Notify: func(string, string) {}},
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	t.Cleanup(a.Shutdown)
	v := &liveVoice{sig: make(chan voice.VoiceSignal, 8)}
	a.AttachVoice(v)
	v.sig <- voice.VoiceSignal{Kind: voice.SigOpened}

	// Your session, its pix log in a folder of the test's own.
	bin := filepath.Join(short, "bin")
	work := filepath.Join(tmp, "greeter")
	pixData := filepath.Join(tmp, "pixdata")
	os.MkdirAll(bin, 0o755)
	os.MkdirAll(work, 0o755)
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "pix"), "../../cmd/pix").CombinedOutput(); err != nil {
		t.Fatalf("build pix: %v\n%s", err, out)
	}
	if _, err := agentfs.NewClaudeTrustStore(filepath.Join(sm.Home, ".claude.json"), []string{tmp}).Grant(work); err != nil {
		t.Fatalf("trust: %v", err)
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120})
	screen := &lockedBuffer{}
	go io.Copy(screen, ptmx)
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PIX_SESSION" || k == "PIX_SOCK" || k == "__CFBundleIdentifier" || k == "PIXIE_AGENT_TOKEN" || k == "CLAUDECODE" || k == "DESKPIXIE_SUPPORT_DIR" || strings.HasPrefix(k, "CLAUDE_CODE_") || strings.HasPrefix(k, "HERDR_") {
			continue
		}
		env = append(env, kv)
	}
	pix := exec.Command(filepath.Join(bin, "pix"), "--description", "greeter", "claude", "--model", "haiku", "--permission-mode", "default")
	pix.Dir = work
	pix.Env = append(env, "PATH="+bin+":"+os.Getenv("PATH"), "DESKPIXIE_PIX_SOCK="+sock, "DESKPIXIE_SUPPORT_DIR="+pixData, "__CFBundleIdentifier=com.googlecode.iterm2")
	pix.Stdin, pix.Stdout, pix.Stderr = tty, tty, tty
	if err := pix.Start(); err != nil {
		t.Fatal(err)
	}
	tty.Close()
	// prompts: the messages pix took for the session (sent, or waiting to go in); a refused one
	// logs "command prompt: <why>".
	prompts := func() int {
		b, _ := os.ReadFile(filepath.Join(pixData, "logs", "pix.log"))
		n := 0
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasSuffix(l, " command prompt") {
				n++
			}
		}
		return n
	}
	t.Cleanup(func() {
		pix.Process.Signal(syscall.SIGKILL)
		if t.Failed() {
			b, _ := os.ReadFile(filepath.Join(pixData, "logs", "pix.log"))
			t.Logf("pix log:\n%s\nheard:\n%s\napp log:\n%s", tail(string(b), 3000), strings.Join(v.all(), "\n"), tail(logs.String(), 3000))
		}
	})
	for deadline := time.Now().Add(30 * time.Second); !strings.Contains(screen.String(), "greeter"); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("pix never registered (no tab title)")
		}
	}
	time.Sleep(5 * time.Second) // Claude's TUI comes up
	t.Log("ok   pix claude registered")

	// The same words twice, as the chat window sends them, one right after the other.
	for i := 0; i < 2; i++ {
		if _, err := a.ChatDelegate("Tell the greeter session: hi"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	for deadline := time.Now().Add(5 * time.Minute); prompts() < 2; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("pix took %d of the 2 messages", prompts())
		}
	}
	time.Sleep(20 * time.Second) // nothing more goes in
	if n := prompts(); n != 2 {
		t.Fatalf("pix took %d messages, want 2", n)
	}
	t.Log("ok   pix took both messages")
}
