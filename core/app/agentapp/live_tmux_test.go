//go:build live

package agentapp_test

// Opt-in live end-to-end check of delegation on the tmux session host:
//
//	go test -count=1 -tags live -run LiveTmux -v -timeout 20m ./app/agentapp/
//
// As TestLiveDelegationEndToEnd, with agent.sessionHost = tmux on the smoke server
// (`tmux -L deskpixie-smoke`, never the app's "deskpixie" nor the default socket): Build installs
// the Claude settings with the state hooks, the coordinator's Stop-hook reply must reach the
// voice loop as "Coordinator reply: pong", and Shutdown closes its window. The server is killed at
// the end.

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
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tmux"
	"github.com/sihekuang/deskpixie/core/tmux/smoketest"
	"github.com/sihekuang/deskpixie/core/voice"
)

func TestLiveTmuxDelegationEndToEnd(t *testing.T) {
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
	data := filepath.Join(tmp, "data")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(ln.Addr().(*net.TCPAddr).Port)
	if port == 48217 {
		t.Fatal("the smoke must never use the app's port 48217")
	}
	s := settings.Default().Agent
	s.SessionHost = tmux.Name
	s.Session = smoketest.SmokeSession
	s.Port = port
	s.ProjectsRoot = filepath.Join(tmp, "projects")
	s.Workers.Enabled = false
	s.Warm = false
	s.KeepPanesOnQuit = false
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
	if tray := a.Tray(); tray.Status != "" {
		t.Logf("tray status before delegating: %q", tray.Status)
	}
	// Build installed the coordinator's settings with the state hooks (tmux).
	coordinatorSettings, err := os.ReadFile(agentapp.NewPaths(data).Settings)
	if err != nil {
		matches, _ := filepath.Glob(filepath.Join(data, "*", "*settings*.json"))
		t.Fatalf("coordinator settings: %v (found %v)", err, matches)
	}
	if err := agentapp.ValidateStateHooks(string(coordinatorSettings), port); err != nil {
		t.Fatalf("the coordinator settings lack the state hooks: %v", err)
	}
	t.Log("ok   the coordinator settings carry the state hooks")

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

	// Only the coordinator's own answer counts: an "Coordinator reply:" whose text is just "pong".
	deadline := time.Now().Add(4 * time.Minute)
	reply := ""
	for reply == "" {
		for _, text := range v.all() {
			body, ok := strings.CutPrefix(text, "Coordinator reply:")
			if !ok {
				continue
			}
			if !isPong(body) {
				notesMu.Lock()
				t.Fatalf("the voice loop got %q, not a Coordinator reply of pong; injected %q; notifications %q; tray %+v", text, v.all(), notes, a.Tray())
			}
			reply = text
		}
		if reply != "" {
			break
		}
		if time.Now().After(deadline) {
			notesMu.Lock()
			t.Fatalf("no Coordinator reply with pong within 4 minutes; injected %q; notifications %q; tray %+v", v.all(), notes, a.Tray())
		}
		time.Sleep(time.Second)
	}
	t.Logf("ok   voice Inject: %q", reply)

	start := time.Now()
	a.Shutdown()
	shut = true
	t.Logf("ok   shutdown took %v", time.Since(start).Round(time.Millisecond))
	mcp, _ := filepath.Glob(filepath.Join(data, "agent", "mcp", "*.json"))
	if len(mcp) != 0 {
		t.Errorf("MCP configs left: %v", mcp)
	}
	host := sm.Host()
	defer host.Shutdown()
	list, err := host.List(context.Background())
	if err != nil {
		t.Errorf("list after shutdown: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("agent windows left after shutdown: %+v", list)
	} else {
		t.Log("ok   no agent windows left after shutdown")
	}
}
