//go:build live

package agentapp_test

// Opt-in live check of #431: a session you turn Pixie off for runs on, but its turns don't reach
// Pixie, and a message you type to it in the chat window doesn't reach it (the coordinator
// doesn't see it). Turned back on, a message goes in again.
//
//	go test -tags live -run LiveSessionOff -v -timeout 15m ./app/agentapp/
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
	"github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

func TestLiveSessionOffIsLeftAlone(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	logs := &syncLog{}
	log.SetOutput(io.MultiWriter(os.Stderr, logs))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	short, err := os.MkdirTemp("/tmp", "pxo")
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

	v0 := a.Roster().(agent.RosterView)
	if len(v0.Yours) != 1 {
		t.Fatalf("one session of yours: %+v", v0)
	}
	name := v0.Yours[0].Name
	if err := a.SetSessionOff(name, true); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if v := a.Roster().(agent.RosterView); len(v.Yours) == 1 && v.Yours[0].Off {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the roster never showed it off")
		}
	}
	t.Log("ok   turned off")

	// It runs on: you type to it in its terminal, and it answers there, but Pixie hears nothing.
	ptmx.Write([]byte("Reply with only the word pineapple in capital letters."))
	time.Sleep(time.Second)
	ptmx.Write([]byte("\r"))
	for deadline := time.Now().Add(2 * time.Minute); !strings.Contains(screen.String(), "PINEAPPLE"); time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatal("the session never answered in its terminal")
		}
	}
	time.Sleep(10 * time.Second)
	for _, l := range v.all() {
		if strings.Contains(strings.ToLower(l), "pineapple") || strings.Contains(l, "greeter") {
			t.Fatalf("Pixie heard from a session that is off: %q", l)
		}
	}
	t.Log("ok   its turn didn't reach Pixie")

	// A message typed to it in the chat window doesn't go in.
	heard := len(v.all())
	if _, err := a.ChatDelegate("Tell the greeter session: hi"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(4 * time.Minute); len(v.all()) == heard; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatal("the coordinator never answered")
		}
	}
	time.Sleep(15 * time.Second)
	if n := prompts(); n != 0 {
		t.Fatalf("pix took %d messages for a session that is off", n)
	}
	t.Logf("ok   nothing went in; heard: %q", v.all()[heard:])

	if err := a.SetSessionOff(name, false); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ChatDelegate("Tell the greeter session: hello again"); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(4 * time.Minute); prompts() < 1; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatal("back on, the message never went in")
		}
	}
	t.Log("ok   back on, the message went in")
}
