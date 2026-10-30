//go:build live

package agentapp_test

// Opt-in live check of #419: a request that repeats a message still waiting for a busy pix
// session isn't taken for delivered (2026-10-29 23:06, "Same request as before… it already has
// it" while it waited). The repeat means the person wants it in now: the coordinator sends
// what waits in (flush_session), stopping the busy turn, and doesn't send it a second time:
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
)

const waitingAsk = "Tell my quince crate session: label the crate with the word medlar."

func TestLiveWaitingARepeatOfAWaitingMessageSendsItInNow(t *testing.T) {
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
	defer ptmx.Close()
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
	io.WriteString(ptmx, "Run this exact shell command in the foreground, not in the background, and wait for it to finish: python3 -c \"import time; time.sleep(150)\" Then say done. Do nothing else.")
	time.Sleep(time.Second)
	io.WriteString(ptmx, "\r")
	time.Sleep(10 * time.Second) // the sleep is running: a message to it waits
	t.Log("ok   pix claude busy")

	replyAfter := func(mark int) string {
		var r string
		v.waitFor(t, "the coordinator's reply", 4*time.Minute, func() bool {
			for _, e := range v.all()[mark:] {
				if strings.HasPrefix(e, "inject: Coordinator reply:") {
					r = e
					return true
				}
			}
			return false
		})
		return r
	}
	mark := len(v.all())
	tool(t, a, "delegate", `{"task":"`+waitingAsk+`"}`)
	first := replyAfter(mark)
	t.Logf("ok   first: %q", first)
	if strings.Contains(first, "Passed on") { // typed in at once: the session wasn't busy
		t.Fatalf("the first send didn't wait: %q", first)
	}

	// The repeat sends the waiting message in now, stopping the busy turn: well before its wait
	// ends (150 s), and once (counted below).
	tool(t, a, "delegate", `{"task":"`+waitingAsk+`"}`)
	repeated := time.Now()
	// The message goes in once: the repeat wasn't sent again.
	medlars := func() int {
		n := 0
		// Claude keeps this session's transcript under its folder, named after it.
		dir := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(work, "-")
		files, _ := filepath.Glob(filepath.Join(sm.Home, ".claude", "projects", dir, "*.jsonl"))
		for _, f := range files {
			b, _ := os.ReadFile(f)
			for _, line := range strings.Split(string(b), "\n") {
				// Typed as a prompt, or passed in with a tool (#407): every copy, two in one list too.
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
				n += strings.Count(strings.ToLower(in), "with the word medlar")
			}
		}
		return n
	}
	v.waitFor(t, "the message sent in on the repeat", time.Minute, func() bool { return medlars() > 0 })
	t.Logf("ok   sent in %s after the repeat", time.Since(repeated).Round(time.Second))
	time.Sleep(15 * time.Second)
	if n := medlars(); n != 1 {
		t.Fatalf("the message went in %d times", n)
	}
	t.Log("ok   it went in once")
}
