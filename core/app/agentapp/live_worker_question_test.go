//go:build live

package agentapp_test

// #422: a worker's question, relayed by a real coordinator, answered as the chat window's
// Reply… answers it (AnswerJob), reaches the worker:
//
//	go test -tags live -run LiveWorkerQuestion -v -timeout 20m ./app/agentapp/
//
// The smoke herdr session, a free hook port, temp app data and projects root, as
// TestLiveDelegationEndToEnd; claude workers on.

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/chat"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
)

// liveThread records what the chat thread is given: the open requests now, and every line.
type liveThread struct {
	mu       sync.Mutex
	requests []chat.Entry
	lines    []string
}

func (l *liveThread) setRequests(es []chat.Entry) {
	l.mu.Lock()
	l.requests = append([]chat.Entry(nil), es...)
	l.mu.Unlock()
}

func (l *liveThread) add(e chat.Entry) {
	l.mu.Lock()
	l.lines = append(l.lines, e.Text)
	l.mu.Unlock()
}

// asking: the open question whose text has words, with its job.
func (l *liveThread) asking(words string) (chat.Entry, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.requests {
		if e.Request != nil && e.Request.Kind == "question" && strings.Contains(strings.ToLower(e.Text), words) {
			return e, true
		}
	}
	return chat.Entry{}, false
}

func (l *liveThread) said(words string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, t := range l.lines {
		if strings.Contains(strings.ToLower(t), words) {
			return true
		}
	}
	return false
}

func (l *liveThread) dump() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for _, e := range l.requests {
		b.WriteString("request: " + e.Text + "\n")
	}
	for _, t := range l.lines {
		b.WriteString("line: " + t + "\n")
	}
	return b.String()
}

func TestLiveWorkerQuestionAnsweredFromTheWindowReachesTheWorker(t *testing.T) {
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
	s.Workers.Enabled = true
	s.Workers.Kinds = []string{"claude"}
	s.Warm = false
	s.KeepPanesOnQuit = false
	// A projects folder outside home isn't used: workers get DeskPixie's own (data/agent/projects).
	repo := filepath.Join(tmp, "data", "agent", "projects", "colors")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("Colors.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."},
		{"-c", "user.name=live", "-c", "user.email=live@example.com", "commit", "-q", "-m", "start"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v %s", args[0], err, out)
		}
	}

	th := &liveThread{}
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: filepath.Join(tmp, "data"), Home: sm.Home, Environ: sm.Environ,
		Hooks: app.AgentHooks{
			ThreadRequests: th.setRequests,
			ThreadLine:     th.add,
			ThreadAnswer:   th.add,
		},
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	t.Cleanup(a.Shutdown)

	if _, err := a.WindowSend("Start one claude worker in the colors project. Its whole task: call ask_coordinator " +
		"with the question \"Red or blue?\", wait for the answer, then call report_result with outcome done and the " +
		"summary \"The user picked <the answer>.\" It must not edit any file. Pass the worker's question on to me."); err != nil {
		t.Fatal(err)
	}

	var q chat.Entry
	deadline := time.Now().Add(8 * time.Minute)
	for {
		var ok bool
		if q, ok = th.asking("red or blue"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker's question never came:\n%s", th.dump())
		}
		time.Sleep(time.Second)
	}
	t.Logf("ok   question from %s %q, job %q, token %q", q.Who.Kind, q.Text, q.Request.Job, q.Request.Token)
	if q.Request.Job == "" || q.Request.Token != "" {
		t.Fatalf("the question has no job to answer, or a token: %+v", q.Request)
	}

	sent, ok, err := a.AnswerJob(q.Request.Job, "blue")
	if err != nil || !ok || !sent.Answered || sent.Job != q.Request.Job {
		t.Fatalf("AnswerJob = %+v %v %v", sent, ok, err)
	}
	t.Logf("ok   answered job %s", sent.Job)

	deadline = time.Now().Add(6 * time.Minute)
	for !th.said("picked blue") {
		if time.Now().After(deadline) {
			t.Fatalf("the worker never reported the answer:\n%s", th.dump())
		}
		time.Sleep(time.Second)
	}
	t.Logf("ok   the answer came back:\n%s", th.dump())
}
