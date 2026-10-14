//go:build live

package agentapp_test

// Opt-in live end-to-end check of delegation (D-497..D-513 "E2E needs a live run"):
//
//	go test -tags live -run Live -v -timeout 15m ./app/agentapp/
//
// agentapp.Build with the real herdr host on the dedicated smoke session ("deskpixie-smoke",
// never the app's "deskpixie"), the hook server on a free loopback port (never 48217: the
// running app holds it), temp app data and projects root. A trivial task is delegated through
// the voice tool; the coordinator's Stop-hook reply must reach the voice loop as an injected
// "Agent reply: Pong." (any case or punctuation); any other injection mentioning pong (the
// task echoed back in a failure) fails. Shutdown then denies anything pending, closes the panes
// and deletes the MCP configs. The smoke session is stopped and deleted at the end.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// liveVoice is the voice loop as the agent sees it: always open, recording injections.
type liveVoice struct {
	mu       sync.Mutex
	injected []string
	sig      chan voice.VoiceSignal
}

func (v *liveVoice) Inject(n voice.Notice) {
	v.mu.Lock()
	v.injected = append(v.injected, n.Text)
	v.mu.Unlock()
}
func (v *liveVoice) HoldOpen(bool)                       {}
func (v *liveVoice) Open() bool                          { return false }
func (v *liveVoice) Subscribe() <-chan voice.VoiceSignal { return v.sig }
func (v *liveVoice) all() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.injected...)
}

// isPong: the reply is the word pong (any case, surrounding spaces and punctuation).
func isPong(body string) bool {
	return strings.ToLower(strings.Trim(body, " \t\n.!\"'")) == "pong"
}

func TestLiveDelegationEndToEnd(t *testing.T) {
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	data := filepath.Join(tmp, "data")

	// The hook server's listener, bound here on a free port so the settings files carry it.
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
	haiku := "haiku"
	s.Model = &haiku
	if err := os.MkdirAll(s.ProjectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	var notes []string
	var notesMu sync.Mutex
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: data, Home: sm.Home, Environ: sm.Environ,
		Hooks: app.AgentHooks{Notify: func(title, body string) {
			notesMu.Lock()
			notes = append(notes, title+": "+body)
			notesMu.Unlock()
		}},
		Listen: func(p uint16) (net.Listener, error) {
			if p != port {
				t.Errorf("Build listens on %d, settings say %d", p, port)
			}
			return ln, nil
		},
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	shut := false
	t.Cleanup(func() {
		if !shut {
			a.Shutdown()
		}
	})
	if a.Addr() != ln.Addr().String() {
		t.Fatalf("hook server on %q, want %q", a.Addr(), ln.Addr())
	}
	if tray := a.Tray(); tray.Status != "" {
		t.Logf("tray status before delegating: %q", tray.Status)
	}

	v := &liveVoice{sig: make(chan voice.VoiceSignal, 8)}
	a.AttachVoice(v)
	v.sig <- voice.VoiceSignal{Kind: voice.SigOpened}

	var delegate voice.Tool
	for _, tool := range a.Tools() {
		if tool.Name() == "delegate" {
			delegate = tool
		}
	}
	if delegate == nil {
		t.Fatal("no delegate tool")
	}
	out := delegate.Invoke(context.Background(), json.RawMessage(`{"task":"Reply with the word pong and nothing else."}`))
	t.Logf("delegate → %s", out.Output)
	var res struct {
		JobID string `json:"job_id"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(out.Output, &res); err != nil || res.JobID == "" {
		t.Fatalf("delegate failed: %s", out.Output)
	}

	// Only the coordinator's own answer counts: an "Agent reply:" whose text is just "pong".
	// Anything else that mentions pong (e.g. "The agent stopped before finishing: Reply with the
	// word pong…" when the coordinator never started) fails the test.
	deadline := time.Now().Add(4 * time.Minute)
	reply := ""
	for reply == "" {
		for _, text := range v.all() {
			if body, ok := strings.CutPrefix(text, "Agent reply:"); ok && isPong(body) {
				reply = text
				continue
			}
			if strings.Contains(strings.ToLower(text), "pong") {
				notesMu.Lock()
				t.Fatalf("the voice loop got %q, not an Agent reply of pong; injected %q; notifications %q; tray %+v", text, v.all(), notes, a.Tray())
			}
		}
		if reply != "" {
			break
		}
		if time.Now().After(deadline) {
			notesMu.Lock()
			t.Fatalf("no Agent reply with pong within 4 minutes; injected %q; notifications %q; tray %+v", v.all(), notes, a.Tray())
		}
		time.Sleep(time.Second)
	}
	t.Logf("ok   voice Inject: %q", reply)

	// Shutdown: deny pending permissions, close the panes, delete the MCP configs.
	start := time.Now()
	a.Shutdown()
	shut = true
	t.Logf("ok   shutdown took %v", time.Since(start).Round(time.Millisecond))
	mcp, _ := filepath.Glob(filepath.Join(data, "agent", "mcp", "*.json"))
	if len(mcp) != 0 {
		t.Errorf("MCP configs left: %v", mcp)
	}
	sup, err := herdr.NewSupervisor(sm.Bin, sm.Env, sm.Runner)
	if err != nil {
		t.Fatal(err)
	}
	host := herdr.NewHost(smoketest.SmokeSession, sm.Home, sup)
	defer host.Shutdown()
	list, err := host.List(context.Background())
	if err != nil {
		t.Logf("agent.list after shutdown: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("panes left after shutdown: %+v", list)
	} else {
		t.Log("ok   no agent panes left after shutdown")
	}
}
