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
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
)

// extraDirHost starts the coordinator with one more --add-dir: a folder Claude Code asks about.
type extraDirHost struct {
	agent.SessionHost
	dir string
}

func (h extraDirHost) withDir(spec agent.AgentSpec) agent.AgentSpec {
	if spec.Name == agent.Coordinator {
		spec.Args = append(append([]string{}, spec.Args...), "--add-dir", h.dir)
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
	time.Sleep(watchPause)
	t.Logf("     after the second request: %q", v.all()[mark:])

	t.Log("step 3: answer the prompt as the user would: Yes, add it")
	if err := host.SendKeys(context.Background(), agent.Coordinator, []string{"Down"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := host.SendKeys(context.Background(), agent.Coordinator, []string{"Enter"}); err != nil {
		t.Fatal(err)
	}

	t.Log("step 4: the requests go through, the user hears so, and the coordinator answers")
	v.waitFor(t, "went through", 2*time.Minute, func() bool { return hasEvent(v.all()[mark:], "your request went through") })
	t.Log("ok   heard: the coordinator is past its prompt; your request went through")
	v.waitFor(t, "the coordinator's answer", 4*time.Minute, func() bool { return hasEvent(v.all()[mark:], "PINEAPPLE") })
	t.Log("ok   the coordinator answered PINEAPPLE")
	time.Sleep(watchPause)
}

func hasEvent(events []string, text string) bool {
	for _, e := range events {
		if strings.Contains(e, text) {
			return true
		}
	}
	return false
}
