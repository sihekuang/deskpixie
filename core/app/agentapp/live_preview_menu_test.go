//go:build live

package agentapp_test

// Opt-in live check of #425: your session's menu whose options have previews (Claude draws it
// side by side, with no "Type something." and an unnumbered "Chat about this"), answered as
// the chat window answers it: "Chat about this", then a message to the session through the
// coordinator, which must reach it rather than wait behind the menu; then your own words:
//
//	go test -tags live -run LivePreviewMenu -v -timeout 20m ./app/agentapp/
//
// Throwaway like LiveMenuAnswered: the Herdr smoke session, a free hook port, a private pix
// socket, and a real `pix claude` (haiku) in its own terminal; the coordinator on SMOKE_MODEL.

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
	"github.com/sihekuang/deskpixie/core/chat"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

const previewAsk = "Use your question tool once to ask me exactly 'Where should the opt-in live?' with the three options " +
	"'Settings file', 'Dashboard toggle' and 'Both' (one question, single select), giving each option a preview: " +
	"'autoReplenish: true', 'A switch on the book page' and 'settings default plus dashboard override'. Then do nothing else."

func TestLivePreviewMenuChatAndOwnWordsReachTheSession(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	logs := &syncLog{}
	log.SetOutput(io.MultiWriter(os.Stderr, logs))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	data := filepath.Join(tmp, "data")
	short, err := os.MkdirTemp("/tmp", "pxp")
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
	th := &liveThread{}
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: data, Home: sm.Home, Environ: sm.Environ, PixSocket: sock,
		Hooks: app.AgentHooks{Notify: func(string, string) {}, ThreadRequests: th.setRequests,
			ThreadLine: th.add, ThreadAnswer: th.add},
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
	work := filepath.Join(tmp, "optin")
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
	pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 140})
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
	pix := exec.Command(filepath.Join(bin, "pix"), "--description", "opt-in design", "claude", "--model", "haiku", "--permission-mode", "default")
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
			t.Logf("pix screen (tail):\n%q\nheard:\n%s\nthread:\n%s\nlog:\n%s", tail(screen.String(), 3000), heard(), th.dump(), tail(logs.String(), 3000))
		}
	})
	for deadline := time.Now().Add(30 * time.Second); !strings.Contains(screen.String(), "opt-in design"); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("pix never registered (no tab title)")
		}
	}
	time.Sleep(5 * time.Second) // Claude's TUI comes up
	t.Log("ok   pix claude registered")

	// ask has the session ask its menu, typed in its terminal as you would, and waits for it
	// in the chat thread: a session's question, with the token its buttons answer.
	ask := func(not string) chat.Entry {
		ptmx.Write([]byte(previewAsk))
		time.Sleep(time.Second)
		ptmx.Write([]byte("\r"))
		for deadline := time.Now().Add(3 * time.Minute); ; time.Sleep(time.Second) {
			if q, ok := th.asking("opt-in live"); ok && q.Request.Token != "" && q.Request.Token != not {
				if !q.Request.Typed {
					t.Fatalf("the menu takes no typed answer or chat: %+v", q.Request)
				}
				return q
			}
			if time.Now().After(deadline) {
				t.Fatal("the session's menu never reached the chat thread")
			}
		}
	}
	waitLog := func(what string, d time.Duration) {
		for deadline := time.Now().Add(d); !strings.Contains(logs.String(), what); time.Sleep(500 * time.Millisecond) {
			if strings.Contains(logs.String(), "The session didn't take") {
				t.Fatalf("the session refused the answer")
			}
			if time.Now().After(deadline) {
				t.Fatalf("never logged %q", what)
			}
		}
	}

	// "Chat about this" on its card.
	q := ask("")
	t.Logf("ok   the menu reached the thread: %q", q.Text)
	a.ChatAbout(q.Request.Token)
	waitLog("question q1 with chat", time.Minute)
	t.Log("ok   Chat about this was taken")

	// Then a message to the session from the chat window, told through the coordinator: the
	// menu is gone, so it lands, and isn't held as waiting behind a question.
	if _, err := a.ChatDelegate("Tell the opt-in design session: reply with only the word bananas in capital letters."); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Minute); !strings.Contains(noSpaces(screen.String()), "BANANAS") || !strings.Contains(heard(), "opt-in design is done"); time.Sleep(time.Second) {
		if strings.Contains(heard(), "hasn't got your message") {
			t.Fatalf("the coordinator says the message waits: %s", heard())
		}
		if time.Now().After(deadline) {
			t.Fatal("the message never reached the session")
		}
	}
	t.Log("ok   the coordinator's message reached the session")

	// Your own words, typed on its card.
	time.Sleep(10 * time.Second) // the session's turn ends
	q2 := ask(q.Request.Token)
	a.TypedAnswer(q2.Request.Token, "per-book setting please")
	waitLog("with text:per-book setting please", time.Minute)
	for deadline := time.Now().Add(time.Minute); !strings.Contains(noSpaces(screen.String()), "(notesonly)"); time.Sleep(time.Second) {
		if time.Now().After(deadline) {
			t.Fatal("the session never got the words as the answer")
		}
	}
	t.Log("ok   your own words answered it")
	if strings.Contains(heard(), "hasn't got your message") {
		t.Errorf("heard: %s", heard())
	}
}
