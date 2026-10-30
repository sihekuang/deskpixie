//go:build live

package agentapp_test

// Opt-in live checks of #419: a request that repeats a message still waiting for a busy pix
// session isn't taken for delivered (2026-10-29 23:06, "Same request as before… it already has
// it" while it waited). The repeat means the person wants it in now: the coordinator sends
// what waits in (flush_session), stopping the busy turn, and doesn't send it a second time.
// A repeat of one that went in already doesn't force in another that waits:
//
//	go test -count=1 -tags live -run LiveWaiting -v -timeout 20m ./app/agentapp/
//
// Like LiveHold: the real coordinator (haiku) in the Herdr smoke session, a free hook port, a
// private pix socket, and a real `pix claude` (haiku) kept busy by a long wait (Claude Code
// refuses a bare sleep).

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
)

const (
	medlarAsk = "Tell my quince crate session: label the crate with the word medlar."
	tealAsk   = "Tell my quince crate session: paint the crate with the color teal."
)

// quinceSession is the app with a real `pix claude` session, "quince crate", registered with it.
type quinceSession struct {
	a    *agentapp.Agent
	v    *holdVoice
	ptmx *os.File
	// transcripts: where Claude keeps this session's transcript.
	transcripts string
}

func startQuinceSession(t *testing.T) *quinceSession {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	short, err := os.MkdirTemp("/tmp", "pxw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	sock := filepath.Join(short, "pix.sock")
	a, v, sm, tmp := holdApp(t, sock)

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
	t.Cleanup(func() { ptmx.Close() })
	pty.Setsize(ptmx, &pty.Winsize{Rows: 50, Cols: 120})
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
	pix := exec.Command(filepath.Join(bin, "pix"), "--description", "quince crate", "claude", "--model", "haiku", "--allowedTools", "Bash(python3:*)")
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
	// Claude keeps the session's transcript under a folder named after its own.
	dir := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(work, "-")
	return &quinceSession{a: a, v: v, ptmx: ptmx, transcripts: filepath.Join(sm.Home, ".claude", "projects", dir)}
}

// busy types a turn that keeps the session working for 150 s: a message to it then waits.
func (q *quinceSession) busy(t *testing.T) {
	t.Helper()
	io.WriteString(q.ptmx, "Run this exact shell command in the foreground, not in the background, and wait for it to finish: python3 -c \"import time; time.sleep(150)\" Then say done. Do nothing else.")
	time.Sleep(time.Second)
	io.WriteString(q.ptmx, "\r")
	time.Sleep(10 * time.Second)
	t.Log("ok   pix claude busy")
}

// ask hands the coordinator a request and returns its reply: the app's own words on what
// became of a message ("Passed on…", or why it waits).
func (q *quinceSession) ask(t *testing.T, task string) string {
	t.Helper()
	mark := len(q.v.all())
	tool(t, q.a, "delegate", `{"task":"`+task+`"}`)
	var r string
	q.v.waitFor(t, "the coordinator's reply", 4*time.Minute, func() bool {
		for _, e := range q.v.all()[mark:] {
			if strings.HasPrefix(e, "inject: Coordinator reply:") {
				r = e
				return true
			}
		}
		return false
	})
	t.Logf("ok   %q", r)
	return r
}

// received counts how often phrase reached the session: typed as a prompt, or passed in with a
// tool (#407), every copy, two in one list too.
func (q *quinceSession) received(phrase string) int {
	n := 0
	files, _ := filepath.Glob(filepath.Join(q.transcripts, "*.jsonl"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(b), "\n") {
			var e struct {
				Type    string `json:"type"`
				Message struct {
					Content any `json:"content"`
				} `json:"message"`
				Attachment struct {
					Type    string   `json:"type"`
					Content []string `json:"content"`
				} `json:"attachment"`
			}
			if json.Unmarshal([]byte(line), &e) != nil {
				continue
			}
			in := ""
			switch {
			case e.Type == "user":
				in, _ = e.Message.Content.(string)
			case e.Attachment.Type == "hook_additional_context":
				in = strings.Join(e.Attachment.Content, "\n")
			}
			n += strings.Count(strings.ToLower(in), phrase)
		}
	}
	return n
}

func TestLiveWaitingARepeatOfAWaitingMessageSendsItInNow(t *testing.T) {
	q := startQuinceSession(t)
	q.busy(t)
	if first := q.ask(t, medlarAsk); strings.Contains(first, "Passed on") { // typed in at once
		t.Fatalf("the first send didn't wait: %q", first)
	}
	// The repeat sends the waiting message in now, stopping the busy turn: well before its wait
	// ends (150 s), and once.
	tool(t, q.a, "delegate", `{"task":"`+medlarAsk+`"}`)
	repeated := time.Now()
	q.v.waitFor(t, "the message sent in on the repeat", time.Minute, func() bool { return q.received("word medlar") > 0 })
	t.Logf("ok   sent in %s after the repeat", time.Since(repeated).Round(time.Second))
	time.Sleep(15 * time.Second)
	if n := q.received("word medlar"); n != 1 {
		t.Fatalf("the message went in %d times", n)
	}
	t.Log("ok   it went in once")
}

// A repeat of a message that went in already isn't one that waits: another message waiting
// for the session isn't forced in for it.
func TestLiveWaitingARepeatOfADeliveredMessageForcesNothing(t *testing.T) {
	q := startQuinceSession(t)
	if first := q.ask(t, medlarAsk); !strings.Contains(first, "Passed on") {
		t.Fatalf("the first send waited: %q", first)
	}
	q.v.waitFor(t, "the first message taken in", time.Minute, func() bool { return q.received("word medlar") > 0 })
	time.Sleep(20 * time.Second) // its turn ends
	q.busy(t)
	if second := q.ask(t, tealAsk); strings.Contains(second, "Passed on") {
		t.Fatalf("the second send didn't wait: %q", second)
	}
	q.ask(t, medlarAsk)
	time.Sleep(30 * time.Second) // still well inside the busy turn
	if n := q.received("color teal"); n != 0 {
		t.Fatalf("the waiting message was forced in for a repeat of another (%d)", n)
	}
	t.Log("ok   the waiting message still waits")
}
