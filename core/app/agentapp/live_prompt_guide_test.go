//go:build live

package agentapp_test

// Opt-in live check of #459 (and #456): a real Claude Code coordinator stops at a prompt in its
// terminal on start. The app reads its screen and says which prompt it is and what to pick; a
// request made meanwhile says it waits; once the user answers, the request goes through and
// the user hears so, and the coordinator answers it.
//
//	go test -count=1 -tags live -run LivePromptGuide -v -timeout 10m ./app/agentapp/
//
// The prompt is Claude Code's "Adding a new directory" for a fresh, empty folder the test adds
// with --add-dir (the canvas is pre-trusted since #457). Like LiveHold: the real coordinator
// (haiku) in the Herdr smoke session (watch it: `herdr session attach deskpixie-smoke`), a
// free hook port. The test answers the prompt as the user would, after a pause to see it.

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/voice"
)

// extraDirHost starts the coordinator with one more --add-dir: a folder Claude Code asks about.
// Without --continue: this fresh workspace has no conversation, and Claude Code would exit
// once the prompt is answered (a real coordinator has one).
type extraDirHost struct {
	agent.SessionHost
	dir string
}

func (h extraDirHost) withDir(spec agent.AgentSpec) agent.AgentSpec {
	if spec.Name == agent.Coordinator {
		var args []string
		for _, a := range spec.Args {
			if a != "--continue" {
				args = append(args, a)
			}
		}
		spec.Args = append(args, "--add-dir", h.dir)
	}
	return spec
}

func (h extraDirHost) StartAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return h.SessionHost.StartAgent(ctx, h.withDir(spec))
}

func (h extraDirHost) ReplaceAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return h.SessionHost.ReplaceAgent(ctx, h.withDir(spec))
}

// watchPause keeps each step on screen long enough for a person watching the session.
const watchPause = 6 * time.Second

func TestLivePromptGuideClaude(t *testing.T) {
	short, err := os.MkdirTemp("/tmp", "pxg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	extra := filepath.Join(short, "untrusted")
	if err := os.MkdirAll(extra, 0o755); err != nil {
		t.Fatal(err)
	}
	var host *extraDirHost
	a, v, _, _ := holdApp(t, filepath.Join(short, "pix.sock"), func(c *agentapp.Config) {
		herdr := agentapp.SessionHosts()["herdr"]
		c.Hosts = map[string]agentapp.HostFactory{"herdr": func(d agentapp.HostDeps) (agent.SessionHost, error) {
			s, err := herdr(d)
			if err != nil {
				return nil, err
			}
			host = &extraDirHost{SessionHost: s, dir: extra}
			return host, nil
		}}
	})

	t.Log("step 1: ask something; the coordinator starts and stops at Claude Code's folder prompt")
	if out := tool(t, a, "delegate", `{"task":"Reply with exactly the one word PINEAPPLE and nothing else."}`); strings.Contains(string(out), "error") {
		t.Fatalf("delegate failed: %s", out)
	}
	v.waitFor(t, "the guidance", 2*time.Minute, func() bool {
		for _, e := range v.all() {
			if strings.Contains(e, `choose "Yes, add it"`) {
				return true
			}
		}
		return false
	})
	for _, e := range v.all() {
		if strings.Contains(e, `choose "Yes, add it"`) {
			t.Logf("ok   told: %s", e)
			if !strings.Contains(e, "Your request goes through once you've answered.") {
				t.Fatalf("the waiting request isn't mentioned: %s", e)
			}
		}
	}

	t.Log("step 2: a second request while it waits says it waits")
	mark := len(v.all())
	tool(t, a, "delegate", `{"task":"Then also reply with exactly the one word MANGO."}`)
	v.waitFor(t, "the second request says it waits", 10*time.Second, func() bool {
		return hasEvent(v.all()[mark:], "Your request is waiting: the coordinator is asking you something in its terminal.")
	})
	t.Log("ok   the second request says it waits")
	time.Sleep(watchPause)

	t.Log("step 3: answer the prompt as the user would: Yes, add it")
	screen := func(when string) string {
		text, _ := host.Read(context.Background(), agent.Coordinator, 40)
		t.Logf("     screen %s:\n%s", when, agent.ScreenBottom(text, 12))
		return text
	}
	screen("before")
	if err := host.SendKeys(context.Background(), agent.Coordinator, []string{"Down"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if !strings.Contains(screen("after Down"), "❯ Yes, add it") {
		t.Fatal("Down didn't move to \"Yes, add it\"")
	}
	if err := host.SendKeys(context.Background(), agent.Coordinator, []string{"Enter"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	screen("after Enter")

	t.Log("step 4: the requests go through, the user hears so, and the coordinator answers")
	v.waitFor(t, "went through", 2*time.Minute, func() bool { return hasEvent(v.all()[mark:], "your request went through") })
	t.Log("ok   heard: the coordinator is past its prompt; your request went through")
	v.waitFor(t, "the coordinator's answer", 4*time.Minute, func() bool { return hasEvent(v.all()[mark:], "PINEAPPLE") })
	t.Log("ok   the coordinator answered PINEAPPLE")
	time.Sleep(watchPause)
}

// freshFolderHost starts the coordinator in a brand-new folder Claude Code has never seen: its
// first-run "Quick safety check" asks whether to trust it. Without --continue, as above.
type freshFolderHost struct {
	agent.SessionHost
	dir string
}

func (h freshFolderHost) inFolder(spec agent.AgentSpec) agent.AgentSpec {
	if spec.Name == agent.Coordinator {
		var args []string
		for _, a := range spec.Args {
			if a != "--continue" {
				args = append(args, a)
			}
		}
		spec.Args, spec.Cwd = args, h.dir
	}
	return spec
}

func (h freshFolderHost) StartAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return h.SessionHost.StartAgent(ctx, h.inFolder(spec))
}

func (h freshFolderHost) ReplaceAgent(ctx context.Context, spec agent.AgentSpec) (agent.AgentInfo, error) {
	return h.SessionHost.ReplaceAgent(ctx, h.inFolder(spec))
}

// TestLivePromptGuideClaudeFirstRun: the first-time setup case. The coordinator opens in a
// folder Claude Code has never trusted; the app says which prompt it is; the user answers
// "Yes, I trust this folder"; the request goes through. Then a permission check: the
// coordinator is asked to write a file, and its permission request reaches the user as before
// (the screen guidance stays out of its way). Answering the trust prompt records the scratch
// folder as trusted in ~/.claude.json, as Claude Code always does.
func TestLivePromptGuideClaudeFirstRun(t *testing.T) {
	short, err := os.MkdirTemp("/tmp", "pxf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	fresh := filepath.Join(short, "first-run")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	var host *freshFolderHost
	a, v, _, _ := holdApp(t, filepath.Join(short, "pix.sock"), func(c *agentapp.Config) {
		herdr := agentapp.SessionHosts()["herdr"]
		c.Hosts = map[string]agentapp.HostFactory{"herdr": func(d agentapp.HostDeps) (agent.SessionHost, error) {
			s, err := herdr(d)
			if err != nil {
				return nil, err
			}
			host = &freshFolderHost{SessionHost: s, dir: fresh}
			return host, nil
		}}
	})
	screen := func(when string) string {
		text, _ := host.Read(context.Background(), agent.Coordinator, 40)
		t.Logf("     screen %s:\n%s", when, agent.ScreenBottom(text, 14))
		return text
	}

	t.Logf("step 1: ask something; the coordinator opens in %s and asks to trust it", fresh)
	tool(t, a, "delegate", `{"task":"Reply with exactly the one word PINEAPPLE and nothing else."}`)
	v.waitFor(t, "the guidance", 2*time.Minute, func() bool { return hasEvent(v.all(), `choose "Yes, I trust this folder"`) })
	for _, e := range v.all() {
		if strings.Contains(e, "trust") {
			t.Logf("ok   told: %s", e)
		}
	}
	time.Sleep(watchPause)

	t.Log("step 2: answer it as the user would: Yes, I trust this folder")
	mark := len(v.all())
	screen("before")
	// "No, exit" comes first and is selected: down to the yes.
	if err := host.SendKeys(context.Background(), agent.Coordinator, []string{"Down"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if !strings.Contains(screen("after Down"), "❯ Yes, I trust this folder") {
		t.Fatal("Down didn't move to \"Yes, I trust this folder\"")
	}
	if err := host.SendKeys(context.Background(), agent.Coordinator, []string{"Enter"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	screen("after Enter")
	v.waitFor(t, "went through", 2*time.Minute, func() bool { return hasEvent(v.all()[mark:], "your request went through") })
	t.Log("ok   heard: your request went through")
	v.waitFor(t, "the coordinator's answer", 4*time.Minute, func() bool { return hasEvent(v.all()[mark:], "PINEAPPLE") })
	t.Log("ok   the coordinator answered PINEAPPLE")
	time.Sleep(watchPause)

	t.Log("step 3: a permission check: rm -rf always asks (the coordinator's settings)")
	mark = len(v.all())
	scratch := filepath.Join(fresh, "scratch")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	tool(t, a, "delegate", `{"task":"Yourself, with your Bash tool (no worker, no session): run rm -rf `+scratch+` and then reply DONE."}`)
	// Asked aloud with a confirm word, or, for a command that can't be read out, refused with a
	// pointer to the terminal: either way the hook's request reached the user.
	askWord := regexp.MustCompile(`Say confirm (\w+)\.`)
	var word string
	v.waitFor(t, "the permission request", 3*time.Minute, func() bool {
		for _, e := range v.all()[mark:] {
			if m := askWord.FindStringSubmatch(e); m != nil {
				word = m[1]
				return true
			}
			if strings.Contains(e, "asked to run") {
				return true
			}
		}
		return false
	})
	screen("at the permission request")
	if _, err := os.Stat(scratch); err != nil {
		t.Fatal("it ran rm -rf without asking")
	}
	t.Log("ok   the permission request reached the user; nothing ran")
	if word != "" {
		time.Sleep(watchPause)
		t.Logf("     the user says: deny %s", word)
		v.sig <- voice.VoiceSignal{Kind: voice.SigUserSaid, Text: "deny " + word}
	}
	// Past the screen guidance's own wait: it must not follow with a stale "asking permission".
	time.Sleep(time.Duration(agent.PermissionGuideWaitMs)*time.Millisecond + 5*time.Second)
	for _, e := range v.all()[mark:] {
		t.Logf("     heard: %s", e)
	}
	if hasEvent(v.all()[mark:], CoordinatorAsksPermissionGuide) {
		t.Fatal("the screen guidance spoke over the permission request")
	}
	if _, err := os.Stat(scratch); err != nil {
		t.Fatal("rm -rf ran though it was denied")
	}
	t.Log("ok   denied, nothing ran, and no second \"asking permission\" from the screen")
}

// CoordinatorAsksPermissionGuide is the screen guidance's own permission line, which must not
// repeat a hook's request.
const CoordinatorAsksPermissionGuide = "Claude Code is asking permission for something."

func hasEvent(events []string, text string) bool {
	for _, e := range events {
		if strings.Contains(e, text) {
			return true
		}
	}
	return false
}
