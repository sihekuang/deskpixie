//go:build live

package pixhost_test

// Opt-in live checks of #359: the real pix around the installed Claude Code and Codex answers
// their questions from Pixie by keys, as you would in the terminal. Run with:
//
//	go test -tags live -run LiveAnswers -v -timeout 15m ./pixhost/
//
// Throwaway: a temp workspace, a private pix socket (never the app's), SMOKE_MODEL for Claude
// (default haiku). Codex runs with a CODEX_HOME of its own, holding a copy of your login only,
// so trusting pix's hook there (its path is new each run) leaves your own Codex as it was.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/pixhost"
)

// livePix is the real pix running tool in a terminal of the test's own, against a host.
type livePix struct {
	host   *pixhost.Host
	events <-chan agent.PaneEvent
	addr   string
	term   *os.File // the terminal you would type in
	work   string
	screen string // the screen log
}

func startLivePix(t *testing.T, tool string, args ...string) *livePix {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Skip(tool + " not installed")
	}
	root, err := os.MkdirTemp("/tmp", "pixanswers")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	codexHome := ""
	t.Cleanup(func() { os.RemoveAll(root) })
	bin, work := filepath.Join(root, "bin"), filepath.Join(root, "work")
	os.MkdirAll(bin, 0o755)
	os.MkdirAll(work, 0o755)
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "pix"), "../cmd/pix").CombinedOutput(); err != nil {
		t.Fatalf("build pix: %v\n%s", err, out)
	}
	if tool == "claude" {
		home, _ := os.UserHomeDir()
		if _, err := agentfs.NewClaudeTrustStore(filepath.Join(home, ".claude.json"), []string{root}).Grant(work); err != nil {
			t.Fatalf("trust: %v", err)
		}
	} else {
		args = append([]string{"-c", fmt.Sprintf("projects.%q.trust_level=\"trusted\"", work)}, args...)
		home, _ := os.UserHomeDir()
		auth, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
		if err != nil {
			t.Skip("codex not logged in")
		}
		codexHome = filepath.Join(root, "codex-home")
		os.MkdirAll(codexHome, 0o700)
		if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), auth, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(root, "pix.sock")
	host := pixhost.New(sock, func() []string { return nil })
	if err := host.Listen(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(host.Shutdown)
	p := &livePix{host: host, events: host.Subscribe(), work: work}

	screen, _ := os.Create(filepath.Join(root, "screen.log"))
	p.screen = screen.Name()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	pty.Setsize(ptmx, &pty.Winsize{Rows: 45, Cols: 110})
	p.term = ptmx
	go io.Copy(screen, ptmx)
	cmd := exec.Command(filepath.Join(bin, "pix"), append([]string{tool}, args...)...)
	cmd.Dir = work
	support := filepath.Join(root, "support") // pix's log goes here, not in the app's folder
	os.MkdirAll(support, 0o700)
	env := []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "DESKPIXIE_PIX_SOCK=" + sock, "DESKPIXIE_SUPPORT_DIR=" + support}
	if codexHome != "" {
		env = append(env, "CODEX_HOME="+codexHome)
	}
	cmd.Env = liveEnv(env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	tty.Close()
	t.Cleanup(func() {
		stopPix(cmd)
		ptmx.Close()
		if t.Failed() {
			b, _ := os.ReadFile(screen.Name())
			if len(b) > 6000 {
				b = b[len(b)-6000:]
			}
			t.Logf("pix screen (tail):\n%q", b)
			if l, err := os.ReadFile(filepath.Join(support, "logs", "pix.log")); err == nil {
				t.Logf("pix.log:\n%s", l)
			}
		}
	})
	for deadline := time.Now().Add(30 * time.Second); p.addr == "" && time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if l, _ := host.List(context.Background()); len(l) == 1 {
			p.addr = l[0].Name
		}
	}
	if p.addr == "" {
		t.Fatal("pix did not register")
	}
	time.Sleep(8 * time.Second) // the agent's TUI comes up
	if codexHome != "" {
		// You trust pix's hook in Codex's own menu (only you answer its startup menus, #306).
		if !screenShows(screen.Name(), "Trustallandcontinue", 20*time.Second) {
			t.Fatal("Codex didn't ask to trust pix's hook")
		}
		ptmx.Write([]byte("2")) // its digit moves to it, and Enter picks it
		time.Sleep(500 * time.Millisecond)
		ptmx.Write([]byte("\r"))
		time.Sleep(3 * time.Second)
	}
	return p
}

var csi = regexp.MustCompile(`\x1b\[[0-9;?<>]*[A-Za-z]`)

// screenShows waits for the screen log (without its escapes and spaces) to show text.
func screenShows(log, text string, d time.Duration) bool {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		b, _ := os.ReadFile(log)
		if strings.Contains(strings.Join(strings.Fields(csi.ReplaceAllString(string(b), "")), ""), text) {
			return true
		}
	}
	return false
}

// asked prompts the session with prompt, from Pixie or typed in its terminal by hand, and
// waits for the question it asks.
func (p *livePix) asked(t *testing.T, prompt string, byHand bool) *agent.SessionQuestion {
	t.Helper()
	if byHand {
		p.term.Write([]byte(prompt))
		time.Sleep(time.Second)
		p.term.Write([]byte("\r"))
	} else if err := p.host.Prompt(context.Background(), p.addr, prompt); err != nil {
		t.Fatal(err)
	}
	var q *agent.SessionQuestion
	waitEvent(t, p.events, p.addr, "a question", 120*time.Second, func(ev agent.PaneEvent) bool {
		if ev.Kind == agent.PaneEventQuestion && ev.Question != nil {
			q = ev.Question
			return true
		}
		return false
	})
	t.Logf("  asked %s %q %q (typed %v)", q.Kind, q.Text, q.Options, q.Typed)
	return q
}

// answer answers q with choice once its question is drawn, as Pixie's tap would, and waits
// for the turn it goes on with to end.
func (p *livePix) answer(t *testing.T, q *agent.SessionQuestion, choice string) {
	t.Helper()
	var err error
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		err = p.host.AnswerQuestion(context.Background(), p.addr, q.ID, choice)
		if err == nil || !strings.Contains(err.Error(), "isn't on screen yet") || time.Now().After(end) {
			break
		}
	}
	if err != nil {
		t.Fatalf("answer %q: %v", choice, err)
	}
	t.Logf("✓ answered %q", choice)
	waitEvent(t, p.events, p.addr, "the question gone", 30*time.Second, func(ev agent.PaneEvent) bool {
		return ev.Kind == agent.PaneEventQuestionGone && ev.Question != nil && ev.Question.ID == q.ID
	})
	waitEvent(t, p.events, p.addr, "the turn it went on with ends", 120*time.Second, func(ev agent.PaneEvent) bool { return ev.Kind == agent.PaneEventTurn })
}

// answerPart answers q, a question of a menu of several, with choice, and waits for the menu's
// next question (#364).
func (p *livePix) answerPart(t *testing.T, q *agent.SessionQuestion, choice string) *agent.SessionQuestion {
	t.Helper()
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		err := p.host.AnswerQuestion(context.Background(), p.addr, q.ID, choice)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "isn't on screen yet") || time.Now().After(end) {
			t.Fatalf("answer %q: %v", choice, err)
		}
	}
	t.Logf("✓ answered %q", choice)
	var next *agent.SessionQuestion
	waitEvent(t, p.events, p.addr, "the next question", 30*time.Second, func(ev agent.PaneEvent) bool {
		if ev.Kind == agent.PaneEventQuestion && ev.Question != nil && ev.Question.ID != q.ID {
			next = ev.Question
			return true
		}
		return false
	})
	t.Logf("  asked %s %q %q (typed %v, multi %v)", next.Kind, next.Text, next.Options, next.Typed, next.Multi)
	return next
}

// lastReply answers q with choice and hands back the reply of the turn it goes on with.
func (p *livePix) lastReply(t *testing.T, q *agent.SessionQuestion, choice string) string {
	t.Helper()
	reply := ""
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		err := p.host.AnswerQuestion(context.Background(), p.addr, q.ID, choice)
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "isn't on screen yet") || time.Now().After(end) {
			t.Fatalf("answer %q: %v", choice, err)
		}
	}
	t.Logf("✓ answered %q", choice)
	waitEvent(t, p.events, p.addr, "the turn it went on with ends", 120*time.Second, func(ev agent.PaneEvent) bool {
		if ev.Kind == agent.PaneEventTurn && ev.Turn != nil {
			reply = ev.Turn.Reply
			return true
		}
		return false
	})
	t.Logf("  replied %q", reply)
	return reply
}

// wants: the reply names every one of words, and none of not.
func wants(t *testing.T, reply string, words, not []string) {
	t.Helper()
	for _, w := range words {
		if !strings.Contains(strings.ToLower(reply), strings.ToLower(w)) {
			t.Errorf("the reply %q doesn't name %q", reply, w)
		}
	}
	for _, w := range not {
		if strings.Contains(strings.ToLower(reply), strings.ToLower(w)) {
			t.Errorf("the reply %q names %q", reply, w)
		}
	}
}

const twoAsk = "Use your question tool once with exactly two questions, in this order: 'Which color?' with the options Red and Blue (single select), and 'Which toppings?' with the options Cheese, Ham and Olives (multiSelect: true, several picks). After I answer, reply with only my answers, comma-separated, and nothing else."

const codexTwoAsk = "Use your question tool once with exactly two questions, in this order: 'Which color?' with the options Red and Blue, and 'Which size?' with the options Small and Large. After I answer, reply with only my answers, comma-separated, and nothing else."

const colorAsk = "Use your question tool once to ask me exactly 'Which color?' with the two options Red and Blue (one question, single select). After I answer, reply with only my answer, verbatim, and nothing else."

func TestLiveAnswersClaude(t *testing.T) {
	model := os.Getenv("SMOKE_MODEL")
	if model == "" {
		model = "haiku"
	}
	p := startLivePix(t, "claude", "--model", model, "--permission-mode", "default")
	for _, choice := range []string{"2", "text:Green please", "chat"} {
		q := p.asked(t, colorAsk, false)
		if q.Kind != "menu" || len(q.Options) != 2 || !q.Typed {
			t.Fatalf("%+v", q)
		}
		p.answer(t, q, choice)
	}
	q := p.asked(t, "Run this exact bash command and nothing else: touch made-by-pix.txt", false)
	if q.Kind != "permission" {
		t.Fatalf("%+v", q)
	}
	p.answer(t, q, "yes")
	if _, err := os.Stat(filepath.Join(p.work, "made-by-pix.txt")); err != nil {
		t.Fatalf("allowed, but: %v", err)
	}
	t.Log("✓ the allowed command ran")
}

// #364: Claude's menu of two questions, the second of several picks: each answered on its
// tab, own words on one, then its review's Submit answers.
func TestLiveAnswersClaudeSeveralQuestions(t *testing.T) {
	model := os.Getenv("SMOKE_MODEL")
	if model == "" {
		model = "haiku"
	}
	p := startLivePix(t, "claude", "--model", model, "--permission-mode", "default")
	for _, c := range []struct {
		color string
		want  []string
	}{{"2", []string{"Blue"}}, {"text:Purple please", []string{"Purple"}}} {
		q := p.asked(t, twoAsk, false)
		if q.Kind != "menu" || q.Text != "Which color? (1 of 2)" || len(q.Options) != 2 || !q.Typed || q.Multi {
			t.Fatalf("%+v", q)
		}
		q2 := p.answerPart(t, q, c.color)
		if q2.Text != "Which toppings? (2 of 2)" || len(q2.Options) != 3 || !q2.Multi || q2.Typed {
			t.Fatalf("%+v", q2)
		}
		review := p.answerPart(t, q2, "picks:1,3")
		if strings.Join(review.Options, "|") != "Submit answers|Cancel" || !strings.Contains(review.Text, "Cheese, Olives") {
			t.Fatalf("%+v", review)
		}
		wants(t, p.lastReply(t, review, "1"), append(c.want, "Cheese", "Olives"), []string{"Ham"})
	}
}

func TestLiveAnswersCodex(t *testing.T) {
	p := startLivePix(t, "codex")
	// Shift-Tab: Plan mode, where Codex has its question tool. pix then takes your keys for a
	// draft until a turn starts, so the first prompt is yours, typed in its terminal.
	p.term.Write([]byte("\x1b[Z"))
	time.Sleep(time.Second)
	for i, choice := range []string{"2", "text:Green please", "chat"} {
		q := p.asked(t, colorAsk, i == 0)
		if q.Kind != "menu" || len(q.Options) < 2 || !q.Typed {
			t.Fatalf("%+v", q)
		}
		p.answer(t, q, choice)
	}
}

// #364: Codex asks its questions in turn too; answering the last sends them all.
func TestLiveAnswersCodexSeveralQuestions(t *testing.T) {
	p := startLivePix(t, "codex")
	p.term.Write([]byte("\x1b[Z"))
	time.Sleep(time.Second)
	for i, c := range []struct {
		color string
		want  string
	}{{"2", "Blue"}, {"text:Purple please", "Purple"}} {
		q := p.asked(t, codexTwoAsk, i == 0)
		if q.Kind != "menu" || q.Text != "Which color? (1 of 2)" || len(q.Options) < 2 || !q.Typed {
			t.Fatalf("%+v", q)
		}
		q2 := p.answerPart(t, q, c.color)
		if q2.Text != "Which size? (2 of 2)" || len(q2.Options) < 2 {
			t.Fatalf("%+v", q2)
		}
		wants(t, p.lastReply(t, q2, "2"), []string{c.want, "Large"}, nil)
	}
}
