//go:build live

package agentapp_test

// Opt-in live check of #170: Pixie's focus_session shows the session you named, among your
// real pix sessions:
//
//	go test -tags live -run LiveFocus -v -timeout 5m ./app/agentapp/
//
// agentapp.Build runs with no coordinator (nothing asks for one) and pix's socket in a private
// folder (never the app's). Two `pix claude` sessions run in their own terminals, each posing
// as its own Warp tab and iTerm session (the planner reads whichever belongs to the terminal
// this test runs in). Pixie's memory must list both; focus_session on each name must send the
// app a plan that shows that session's tab, and a wrong name must send nothing. Nothing is shown on screen: the plans go to a test hook, not the macOS app.

import (
	"context"
	"encoding/json"
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
	"github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

func TestLiveFocusSessionShowsTheSessionYouNamed(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not installed")
	}
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	short, err := os.MkdirTemp("/tmp", "pxf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	sock := filepath.Join(short, "pix.sock")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := settings.Default().Agent
	s.Session = smoketest.SmokeSession
	s.Port = uint16(ln.Addr().(*net.TCPAddr).Port)
	s.ProjectsRoot = filepath.Join(tmp, "projects")
	s.Workers.Enabled = false
	s.Warm = false
	s.KeepPanesOnQuit = false
	os.MkdirAll(s.ProjectsRoot, 0o755)

	var plans []agent.FocusPlan
	var plansMu sync.Mutex
	sent := func() []agent.FocusPlan {
		plansMu.Lock()
		defer plansMu.Unlock()
		return append([]agent.FocusPlan(nil), plans...)
	}
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: filepath.Join(tmp, "data"), Home: sm.Home, Environ: sm.Environ, PixSocket: sock,
		Hooks: app.AgentHooks{Focus: func(plan any) {
			plansMu.Lock()
			plans = append(plans, plan.(agent.FocusPlan))
			plansMu.Unlock()
		}},
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	t.Cleanup(a.Shutdown)

	bin := filepath.Join(short, "bin")
	os.MkdirAll(bin, 0o755)
	if out, err := exec.Command("go", "build", "-o", filepath.Join(bin, "pix"), "../../cmd/pix").CombinedOutput(); err != nil {
		t.Fatalf("build pix: %v\n%s", err, out)
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PIX_SESSION" || k == "__CFBundleIdentifier" || k == "PIXIE_AGENT_TOKEN" || k == "CLAUDECODE" || k == "ITERM_SESSION_ID" ||
			k == "WARP_FOCUS_URL" || strings.HasPrefix(k, "CLAUDE_CODE_") || strings.HasPrefix(k, "HERDR_") {
			continue
		}
		env = append(env, kv)
	}

	// Your sessions: each in its own folder and its own (pretend) Warp tab and iTerm session.
	sessions := []struct{ description, tab string }{
		{"marmalade jar", "11111111-2222-3333-4444-555555555555"},
		{"quince tart", "66666666-7777-8888-9999-aaaaaaaaaaaa"},
	}
	for i, sess := range sessions {
		work := filepath.Join(tmp, []string{"jam", "tart"}[i])
		os.MkdirAll(work, 0o755)
		if _, err := agentfs.NewClaudeTrustStore(filepath.Join(sm.Home, ".claude.json"), []string{tmp}).Grant(work); err != nil {
			t.Fatalf("trust: %v", err)
		}
		ptmx, tty, err := pty.Open()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ptmx.Close() })
		pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120})
		go io.Copy(io.Discard, ptmx)
		pix := exec.Command(filepath.Join(bin, "pix"), "--description", sess.description, "claude", "--model", "haiku")
		pix.Dir = work
		pix.Env = append(env, "PATH="+bin+":"+os.Getenv("PATH"), "DESKPIXIE_PIX_SOCK="+sock,
			"WARP_FOCUS_URL=warp://session/"+sess.tab, "ITERM_SESSION_ID=w0t"+string(rune('0'+i))+"p0:"+sess.tab)
		pix.Stdin, pix.Stdout, pix.Stderr = tty, tty, tty
		if err := pix.Start(); err != nil {
			t.Fatal(err)
		}
		tty.Close()
		t.Cleanup(func() { pix.Process.Signal(syscall.SIGKILL); pix.Wait() })
	}

	// Pixie's memory names both, by the descriptions you gave them.
	names := map[string]string{}
	var facts string
	for deadline := time.Now().Add(60 * time.Second); len(names) < 2; time.Sleep(500 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("Pixie's memory doesn't list both sessions:\n%s", facts)
		}
		facts = a.Facts()
		for _, line := range strings.Split(facts, "\n") {
			for _, sess := range sessions {
				if name, rest, ok := strings.Cut(strings.TrimPrefix(line, "- "), ": "); ok && strings.HasPrefix(line, "- ") && strings.HasPrefix(rest, sess.description) {
					names[sess.description] = name
				}
			}
		}
	}
	t.Logf("ok   Pixie's memory lists them:\n%s", facts)

	var focus voice.Tool
	for _, tool := range a.Tools() {
		if tool.Name() == "focus_session" {
			focus = tool
		}
	}
	if focus == nil {
		t.Fatal("no focus_session tool")
	}
	call := func(name string) map[string]any {
		out := focus.Invoke(context.Background(), json.RawMessage(`{"name":`+quoteJSON(name)+`}`))
		var v map[string]any
		if err := json.Unmarshal(out.Output, &v); err != nil {
			t.Fatalf("%s: %v", out.Output, err)
		}
		return v
	}

	// Each name shows its own session, not the other one.
	for i, sess := range sessions {
		name := names[sess.description]
		if got := call(name); got["status"] != "shown" {
			t.Fatalf("%s: %v", name, got)
		}
		p := sent()
		if len(p) != i+1 {
			t.Fatalf("%s: %d plans sent: %+v", name, len(p), p)
		}
		shows := strings.Join(append(p[i].FocusURLs, p[i].ItermSelect...), "\n")
		if !strings.Contains(shows, sess.tab) || strings.Contains(shows, sessions[1-i].tab) {
			t.Fatalf("%s: the plan doesn't show its own tab %s: %+v", name, sess.tab, p[i])
		}
		t.Logf("ok   %s (%s) shows its own tab: %+v", name, sess.description, p[i])
	}

	// A name that isn't one shows nothing and lists your sessions.
	got := call("nobody-claude")
	if got["error"] == nil || len(got["your_sessions"].([]any)) != 2 || len(sent()) != 2 {
		t.Fatalf("a wrong name: %v; plans %d", got, len(sent()))
	}
	t.Logf("ok   a wrong name lists: %v", got["your_sessions"])
}
