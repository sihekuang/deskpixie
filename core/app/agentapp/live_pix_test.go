//go:build live

package agentapp_test

// Opt-in live check that the coordinator's session tools reach a session you started with pix
// (#89), with Herdr as the app's session host:
//
//	go test -tags live -run LivePix -v -timeout 15m ./app/agentapp/
//
// agentapp.Build runs the real coordinator in the Herdr smoke session ("deskpixie-smoke"), the
// hook server on a free port and pix's socket in a private folder (never the app's). A real
// `pix claude` runs in its own terminal. The coordinator is asked to find it with list_sessions
// and message it (the wrapped Claude must show the message), then to close it: the app must
// ask with a confirm word first, and pix must exit once it is said. Everything is throwaway;
// Claude Code records folder trust for the temp folders in ~/.claude.json.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// lockedBuffer is pix's terminal output.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var confirmWord = regexp.MustCompile(`Close your session [^?]*\? Say confirm (\w+)\.`)

func TestLivePixSessionThroughTheHerdrCoordinator(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	data := filepath.Join(tmp, "data")
	short, err := os.MkdirTemp("/tmp", "pxl")
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

	var notes []string
	var notesMu sync.Mutex
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: data, Home: sm.Home, Environ: sm.Environ, PixSocket: sock,
		Hooks: app.AgentHooks{Notify: func(title, body string) {
			notesMu.Lock()
			notes = append(notes, title+": "+body)
			notesMu.Unlock()
		}},
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	t.Cleanup(a.Shutdown)
	v := &liveVoice{sig: make(chan voice.VoiceSignal, 8)}
	a.AttachVoice(v)
	v.sig <- voice.VoiceSignal{Kind: voice.SigOpened}
	heard := func() string {
		notesMu.Lock()
		defer notesMu.Unlock()
		return strings.Join(append(v.all(), notes...), "\n")
	}

	// Your session: pix claude in its own terminal, in a folder Claude trusts.
	bin := filepath.Join(short, "bin")
	work := filepath.Join(tmp, "mine")
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
		if k == "PIX_SESSION" || k == "PIXIE_AGENT_TOKEN" || k == "CLAUDECODE" || strings.HasPrefix(k, "CLAUDE_CODE_") || strings.HasPrefix(k, "HERDR_") {
			continue
		}
		env = append(env, kv)
	}
	pix := exec.Command(filepath.Join(bin, "pix"), "--description", "marmalade jar", "claude", "--model", "haiku")
	pix.Dir = work
	pix.Env = append(env, "PATH="+bin+":"+os.Getenv("PATH"), "DESKPIXIE_PIX_SOCK="+sock)
	pix.Stdin, pix.Stdout, pix.Stderr = tty, tty, tty
	if err := pix.Start(); err != nil {
		t.Fatal(err)
	}
	tty.Close()
	exited := make(chan error, 1)
	go func() { exited <- pix.Wait() }()
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
	for deadline := time.Now().Add(30 * time.Second); !strings.Contains(screen.String(), "marmalade jar"); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("pix never registered (no tab title)")
		}
	}
	time.Sleep(5 * time.Second) // Claude's TUI and inbox come up
	t.Log("ok   pix claude registered")

	var delegate voice.Tool
	for _, tool := range a.Tools() {
		if tool.Name() == "delegate" {
			delegate = tool
		}
	}
	if delegate == nil {
		t.Fatal("no delegate tool")
	}
	ask := func(task string) {
		out := delegate.Invoke(context.Background(), json.RawMessage(`{"task":`+quoteJSON(task)+`}`))
		var res struct {
			JobID string `json:"job_id"`
		}
		if json.Unmarshal(out.Output, &res); res.JobID == "" {
			t.Fatalf("delegate: %s", out.Output)
		}
	}
	replies := func() int { return strings.Count(heard(), "Coordinator reply:") }

	// 1. list_sessions + message_session from the Herdr coordinator reach the wrapped Claude.
	ask("Use the list_sessions tool to find my session described as \"marmalade jar\", then use message_session to send it exactly this text: Say the word quince. Then reply to me with the single word done.")
	for deadline := time.Now().Add(5 * time.Minute); !strings.Contains(noSpaces(screen.String()), "Saythewordquince"); time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatal("the message never reached the wrapped Claude")
		}
	}
	t.Log("ok   message_session reached the pix session")
	for deadline := time.Now().Add(3 * time.Minute); replies() < 1; time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatal("no coordinator reply after messaging")
		}
	}
	t.Log("ok   coordinator replied")

	// 2. close_session asks first; pix ends only after the confirm word.
	ask("Use the close_session tool to close my session described as \"marmalade jar\". Then reply with the single word closed.")
	var word string
	for deadline := time.Now().Add(5 * time.Minute); word == ""; time.Sleep(time.Second) {
		if m := confirmWord.FindStringSubmatch(heard()); m != nil {
			word = m[1]
		}
		select {
		case err := <-exited:
			t.Fatalf("pix ended before you confirmed (%v)", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the app never asked to confirm closing")
		}
	}
	t.Logf("ok   asked: say confirm %s", word)
	v.sig <- voice.VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm " + word}
	select {
	case err := <-exited:
		t.Logf("ok   pix exited after the confirm (%v)", err)
	case <-time.After(60 * time.Second):
		t.Fatal("pix still running after the confirm")
	}
}

func quoteJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

func noSpaces(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == ' ' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*\x07`).ReplaceAllString(s, ""))
}
