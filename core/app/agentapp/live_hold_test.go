//go:build live

package agentapp_test

// Opt-in live checks of what holds Pixie in delegating (#119):
//
//	go test -tags live -run LiveHold -v -timeout 20m ./app/agentapp/
//
// HoldOpen(true) is what puts Pixie in delegating (voice.InHoldOpen). Both checks run the real
// coordinator in the Herdr smoke session ("deskpixie-smoke") with the hook server on a free port.
// 1. A job whose coordinator asks the user a question releases the hold; the answer takes it back.
// 2. A pix session waiting on a permission prompt never takes the hold.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// holdVoice is the voice loop as the agent sees it, recording injections, holds and
// notifications in order ("inject: …", "hold: true", "notify: …").
type holdVoice struct {
	mu     sync.Mutex
	events []string
	sig    chan voice.VoiceSignal
}

func (v *holdVoice) add(e string) {
	v.mu.Lock()
	v.events = append(v.events, e)
	v.mu.Unlock()
}
func (v *holdVoice) Inject(n voice.Notice)               { v.add("inject: " + n.Text) }
func (v *holdVoice) HoldOpen(on bool)                    { v.add(fmt.Sprintf("hold: %v", on)) }
func (v *holdVoice) Open() bool                          { return false }
func (v *holdVoice) Subscribe() <-chan voice.VoiceSignal { return v.sig }
func (v *holdVoice) all() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.events...)
}

// since is the index of the first event with prefix at or after from, -1 if none.
func (v *holdVoice) since(from int, prefix string) int {
	for i, e := range v.all() {
		if i >= from && strings.HasPrefix(e, prefix) {
			return i
		}
	}
	return -1
}

// held is the latest hold, false if none yet.
func (v *holdVoice) held() bool {
	ev := v.all()
	for i := len(ev) - 1; i >= 0; i-- {
		if h, ok := strings.CutPrefix(ev[i], "hold: "); ok {
			return h == "true"
		}
	}
	return false
}

// waitFor polls until ok or the deadline, then fails with the events so far.
func (v *holdVoice) waitFor(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !ok(); time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %v; events:\n%s", what, within, strings.Join(v.all(), "\n"))
		}
	}
}

// holdApp builds the agent app on the smoke session with a haiku coordinator and no workers,
// with holdVoice attached and a conversation open. pixSock, if set, is pix's socket.
func holdApp(t *testing.T, pixSock string, edit ...func(*agentapp.Config)) (*agentapp.Agent, *holdVoice, *smoketest.LiveSmoke, string) {
	t.Helper()
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
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
	haiku := "haiku"
	s.Model = &haiku
	if err := os.MkdirAll(s.ProjectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	v := &holdVoice{sig: make(chan voice.VoiceSignal, 8)}
	cfg := agentapp.Config{
		Settings: s, AppSupport: filepath.Join(tmp, "data"), Home: sm.Home, Environ: sm.Environ, PixSocket: pixSock,
		Hooks:  app.AgentHooks{Notify: func(_, body string) { v.add("notify: " + body) }},
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	}
	for _, e := range edit {
		e(&cfg)
	}
	a := agentapp.Build(cfg)
	if a == nil {
		t.Fatal("Build returned nil")
	}
	t.Cleanup(a.Shutdown)
	a.AttachVoice(v)
	v.sig <- voice.VoiceSignal{Kind: voice.SigOpened}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("events:\n%s", strings.Join(v.all(), "\n"))
		}
	})
	return a, v, sm, tmp
}

func tool(t *testing.T, a *agentapp.Agent, name, args string) json.RawMessage {
	t.Helper()
	for _, tl := range a.Tools() {
		if tl.Name() == name {
			out := tl.Invoke(context.Background(), json.RawMessage(args))
			t.Logf("%s → %s", name, out.Output)
			return out.Output
		}
	}
	t.Fatalf("no %s tool", name)
	return nil
}

func TestLiveHoldAQuestionReleasesItAndTheAnswerTakesItBack(t *testing.T) {
	a, v, _, _ := holdApp(t, "")
	out := tool(t, a, "delegate", `{"task":"Before doing anything else, ask me which color I want, as a question for me, and wait for my answer. Once I answer, reply with only that color."}`)
	if !strings.Contains(string(out), "job_") {
		t.Fatalf("delegate failed: %s", out)
	}
	v.waitFor(t, "held while the job runs", 30*time.Second, v.held)
	t.Log("ok   held while the job runs")

	v.waitFor(t, "the coordinator's question", 4*time.Minute, func() bool { return v.since(0, "inject: Coordinator question:") >= 0 })
	q := v.since(0, "inject: Coordinator question:")
	t.Logf("ok   %s", v.all()[q])
	v.waitFor(t, "the hold released while the question waits", 5*time.Second, func() bool { return !v.held() })
	t.Log("ok   released while the question waits")

	mark := len(v.all())
	tool(t, a, "answer_job", `{"answer":"blue"}`)
	v.waitFor(t, "held again after the answer", 10*time.Second, func() bool { return v.since(mark, "hold: true") >= 0 })
	t.Log("ok   held again after the answer")

	v.waitFor(t, "the coordinator's reply", 4*time.Minute, func() bool { return v.since(mark, "inject: Coordinator reply:") >= 0 })
	if r := v.all()[v.since(mark, "inject: Coordinator reply:")]; !strings.Contains(strings.ToLower(r), "blue") {
		t.Fatalf("reply %q, want blue", r)
	}
	v.waitFor(t, "released once the job is done", 10*time.Second, func() bool { return !v.held() })
	t.Log("ok   replied blue; released once done")
}

func TestLiveHoldASessionPromptNeverTakesIt(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	short, err := os.MkdirTemp("/tmp", "pxh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	sock := filepath.Join(short, "pix.sock")
	_, v, sm, tmp := holdApp(t, sock)

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
	pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 120})
	screen := &lockedBuffer{}
	go io.Copy(screen, ptmx)
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PIX_SESSION" || k == "__CFBundleIdentifier" || k == "PIXIE_AGENT_TOKEN" || k == "CLAUDECODE" || strings.HasPrefix(k, "CLAUDE_CODE_") || strings.HasPrefix(k, "HERDR_") {
			continue
		}
		env = append(env, kv)
	}
	pix := exec.Command(filepath.Join(bin, "pix"), "--description", "quince crate", "claude", "--model", "haiku")
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
			t.Logf("pix screen (tail):\n%q", out)
		}
	})
	v.waitFor(t, "pix registered", 30*time.Second, func() bool { return strings.Contains(screen.String(), "quince crate") })
	time.Sleep(5 * time.Second) // Claude's TUI comes up
	t.Log("ok   pix claude registered")

	io.WriteString(ptmx, "Use the AskUserQuestion tool to ask me one question, whether to plant a quince tree, with the options Plant and Skip. Do nothing else.")
	time.Sleep(time.Second)
	io.WriteString(ptmx, "\r")

	// The app relays the session's menu (as a notification: no conversation is open).
	v.waitFor(t, "the session's prompt reached Pixie", 3*time.Minute, func() bool {
		return strings.Contains(strings.ToLower(strings.Join(v.all(), "\n")), "quince")
	})
	t.Logf("ok   relayed: %q", v.all())
	time.Sleep(3 * time.Second) // any hold would follow at once
	if i := v.since(0, "hold: true"); i >= 0 {
		t.Fatalf("a session prompt held Pixie in delegating (event %d)", i)
	}
	t.Log("ok   never held while the session waits on its prompt")
}
