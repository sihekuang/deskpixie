//go:build live

package agentapp_test

// Opt-in live check of #384: a spoken answer that reaches the coordinator as a new request
// (2026-10-27 14:44, after the voice model lost track of the question) picks the option of the
// menu your session waits on, not a message that only waits behind it or goes to another
// session. Worded so the app's own matcher can't pick it (#383 answers "create both and assign
// to me" before the coordinator sees it), so the coordinator's answer_session_menu does:
//
//	go test -tags live -run LiveMenuAnswered -v -timeout 15m ./app/agentapp/
//
// Throwaway like LivePix: the Herdr smoke session, a free hook port, a private pix socket, and
// a real `pix claude` (haiku) in its own terminal; the coordinator runs on SMOKE_MODEL (sonnet).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

// syncLog is the app's log, for what it did to the session.
type syncLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

const storiesAsk = "Use your question tool once to ask me exactly 'Create these 2 stories under OVA-23377?' with the three options 'Create both, unassigned', 'Create both, assign to me' and 'Change something first' (one question, single select). After I answer, reply with only my answer, verbatim, and nothing else."

func TestLiveMenuAnsweredThroughTheCoordinator(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	logs := &syncLog{}
	log.SetOutput(io.MultiWriter(os.Stderr, logs))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	data := filepath.Join(tmp, "data")
	short, err := os.MkdirTemp("/tmp", "pxm")
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
		Settings: s, AppSupport: data, Home: sm.Home, Environ: sm.Environ, PixSocket: sock,
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
	heard := func() string { return strings.Join(v.all(), "\n") }

	// Your session: pix claude in its own terminal, in a folder Claude trusts.
	bin := filepath.Join(short, "bin")
	work := filepath.Join(tmp, "stories")
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
		if k == "PIX_SESSION" || k == "PIX_SOCK" || k == "__CFBundleIdentifier" || k == "PIXIE_AGENT_TOKEN" || k == "CLAUDECODE" || strings.HasPrefix(k, "CLAUDE_CODE_") || strings.HasPrefix(k, "HERDR_") {
			continue
		}
		env = append(env, kv)
	}
	pix := exec.Command(filepath.Join(bin, "pix"), "--description", "jira stories", "claude", "--model", "haiku", "--permission-mode", "default")
	pix.Dir = work
	pix.Env = append(env, "PATH="+bin+":"+os.Getenv("PATH"), "DESKPIXIE_PIX_SOCK="+sock, "__CFBundleIdentifier=com.googlecode.iterm2")
	pix.Stdin, pix.Stdout, pix.Stderr = tty, tty, tty
	if err := pix.Start(); err != nil {
		t.Fatal(err)
	}
	tty.Close()
	t.Cleanup(func() {
		pix.Process.Signal(syscall.SIGKILL)
		if t.Failed() {
			out := screen.String()
			if len(out) > 3000 {
				out = out[len(out)-3000:]
			}
			t.Logf("pix screen (tail):\n%q\nheard:\n%s", out, heard())
		}
	})
	for deadline := time.Now().Add(30 * time.Second); !strings.Contains(screen.String(), "jira stories"); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("pix never registered (no tab title)")
		}
	}
	time.Sleep(5 * time.Second) // Claude's TUI comes up
	t.Log("ok   pix claude registered")

	// The session asks its menu, typed in its terminal as you would.
	ptmx.Write([]byte(storiesAsk))
	time.Sleep(time.Second)
	ptmx.Write([]byte("\r"))
	for deadline := time.Now().Add(3 * time.Minute); !strings.Contains(heard(), "Create both, assign to me"); time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatal("the session's menu never reached Pixie")
		}
	}
	t.Log("ok   the menu reached Pixie: " + heard())

	// The 14:44 delegate call, with the words only the coordinator can match to an option.
	var delegate voice.Tool
	for _, tool := range a.Tools() {
		if tool.Name() == "delegate" {
			delegate = tool
		}
	}
	out := delegate.Invoke(context.Background(), json.RawMessage(`{"task":"yes, make both of them, and put me down as the assignee.","context":"The user wants 'both' created and assigned to them. Refer to earlier context in the chat history with the coordinator regarding what 'both' refers to."}`))
	var res struct {
		JobID string `json:"job_id"`
	}
	if json.Unmarshal(out.Output, &res); res.JobID == "" {
		t.Fatalf("delegate: %s", out.Output)
	}
	for deadline := time.Now().Add(5 * time.Minute); !strings.Contains(logs.String(), "question q1 with 2"); time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatalf("the menu was never answered with option 2; log:\n%s", tail(logs.String(), 3000))
		}
	}
	t.Log("ok   the coordinator picked option 2 of the session's menu")
	time.Sleep(15 * time.Second) // anything else the coordinator sends it lands
	if strings.Contains(strings.ToLower(noSpaces(screen.String())), "makebothofthem") {
		t.Error("the words were also typed into the session as a message")
	}
	t.Logf("heard: %s", heard())
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
