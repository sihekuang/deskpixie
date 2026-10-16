//go:build live

package agentapp_test

// Opt-in live checks that a request handed to the coordinator reaches it (#127, #128):
//
//	go test -tags live -run LiveLanding -v -timeout 15m ./app/agentapp/
//
// Both run the real coordinator (haiku) in the Herdr smoke session ("deskpixie-smoke"), warmed
// up by one job first (so it has reported a prompt it took), then hand it a second job:
//  1. The host reports that job's first prompt typed but never types it, as one was lost at a
//     Stop on 2026-10-16: the app must notice and send it again (#128).
//  2. Herdr is told the coordinator is working while it sits at its idle prompt, as it reads a
//     waiting background agent's spinner: the job must still go out (#127).

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
)

// droppingHost is the real host, except the first prompt to the coordinator containing drop
// is reported typed and never typed.
type droppingHost struct {
	agent.SessionHost
	drop string

	mu      sync.Mutex
	dropped int
	sent    int // prompts containing drop that were typed
}

func (h *droppingHost) Prompt(ctx context.Context, name, text string) error {
	if name == agent.Coordinator && strings.Contains(text, h.drop) {
		h.mu.Lock()
		first := h.dropped == 0
		if first {
			h.dropped++
		} else {
			h.sent++
		}
		h.mu.Unlock()
		if first {
			return nil
		}
	}
	return h.SessionHost.Prompt(ctx, name, text)
}

func (h *droppingHost) counts() (dropped, sent int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dropped, h.sent
}

// Focus and Relabel: what the real host does besides SessionHost.
func (h *droppingHost) Focus(ctx context.Context, name string) error {
	if f, ok := h.SessionHost.(agent.Focuser); ok {
		return f.Focus(ctx, name)
	}
	return nil
}

func (h *droppingHost) Relabel(ctx context.Context, name string, tab agent.TabLabel) error {
	if l, ok := h.SessionHost.(agent.Labeler); ok {
		return l.Relabel(ctx, name, tab)
	}
	return nil
}

// reply waits for a coordinator line after from that contains word, and returns it.
func reply(t *testing.T, v *holdVoice, from int, word string, within time.Duration) string {
	t.Helper()
	find := func() int {
		for i, e := range v.all() {
			if i >= from && strings.HasPrefix(e, "inject: Coordinator") && strings.Contains(strings.ToLower(e), word) {
				return i
			}
		}
		return -1
	}
	v.waitFor(t, "the coordinator's reply with "+word, within, func() bool { return find() >= 0 })
	return v.all()[find()]
}

// warmUp hands the coordinator a first job and waits for its answer.
func warmUp(t *testing.T, a *agentapp.Agent, v *holdVoice) {
	t.Helper()
	tool(t, a, "delegate", `{"task":"Reply with only the word fig."}`)
	t.Logf("ok   warm-up: %s", reply(t, v, 0, "fig", 3*time.Minute))
}

func TestLiveLandingALostPromptIsSentAgain(t *testing.T) {
	var host *droppingHost
	a, v, _, _ := holdApp(t, "", func(c *agentapp.Config) {
		real := agentapp.SessionHosts()["herdr"]
		c.Hosts = map[string]agentapp.HostFactory{"herdr": func(d agentapp.HostDeps) (agent.SessionHost, error) {
			inner, err := real(d)
			if err != nil {
				return nil, err
			}
			host = &droppingHost{SessionHost: inner, drop: "plum"}
			return host, nil
		}}
	})
	warmUp(t, a, v)

	mark := len(v.all())
	began := time.Now()
	tool(t, a, "delegate", `{"task":"Reply with only the word plum."}`)
	got := reply(t, v, mark, "plum", 2*time.Minute)
	dropped, sent := host.counts()
	if dropped != 1 || sent != 1 {
		t.Fatalf("dropped %d, typed %d: want the lost prompt sent once more", dropped, sent)
	}
	t.Logf("ok   the lost prompt was sent again and answered in %v: %s", time.Since(began).Round(time.Second), got)
}

func TestLiveLandingAWorkingReportAtAnIdlePromptDoesNotHoldRequests(t *testing.T) {
	a, v, sm, tmp := holdApp(t, "")
	host := herdr.NewHost(smoketest.SmokeSession, sm.Home, sm.Supervisor(t))
	t.Cleanup(host.Shutdown)
	coordinator := func() (string, agent.PaneStatus) {
		list, _ := host.List(context.Background())
		if i := slices.IndexFunc(list, func(a agent.AgentInfo) bool { return a.Name == agent.Coordinator }); i >= 0 {
			return list[i].Pane, list[i].Status
		}
		return "", agent.PaneUnknown
	}
	warmUp(t, a, v)
	v.waitFor(t, "the coordinator idle", time.Minute, func() bool { _, st := coordinator(); return st == agent.PaneIdle })

	// What Herdr said on 2026-10-15 while the coordinator waited on its background agent: it
	// reads a spinner in the terminal title (Claude Code keeps one while a background agent
	// runs). The same title, written to the idle coordinator's terminal.
	out, _ := exec.Command("/usr/bin/pgrep", "-f", filepath.Join(tmp, "data", "agent", "coordinator-settings.json")).Output()
	ttyName := ""
	for _, pid := range strings.Fields(string(out)) {
		if name, err := exec.Command("/bin/ps", "-o", "tty=", "-p", pid).Output(); err == nil && !strings.Contains(string(name), "?") {
			ttyName = strings.TrimSpace(string(name))
		}
	}
	if ttyName == "" {
		t.Fatalf("no terminal for the coordinator (pids %q)", out)
	}
	tty, err := os.OpenFile("/dev/"+ttyName, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open its tty: %v", err)
	}
	defer tty.Close()
	if _, err := tty.WriteString("\x1b]0;\u25d0 Waiting on a background agent\x07"); err != nil {
		t.Fatal(err)
	}
	v.waitFor(t, "Herdr reporting the idle coordinator working", 10*time.Second, func() bool { _, st := coordinator(); return st == agent.PaneWorking })
	time.Sleep(3 * time.Second) // the app has seen it
	t.Log("ok   Herdr reports the coordinator working at its idle prompt")

	mark := len(v.all())
	began := time.Now()
	tool(t, a, "delegate", `{"task":"Reply with only the word plum."}`)
	got := reply(t, v, mark, "plum", 90*time.Second)
	t.Logf("ok   answered in %v while Herdr reports it working: %s", time.Since(began).Round(time.Second), got)
}
