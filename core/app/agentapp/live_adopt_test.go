//go:build live

package agentapp_test

// Opt-in live check of #168: a coordinator kept over a quit gets its pixie tools back when the
// next run adopts it.
//
//	go test -tags live -run LiveAnAdopted -v -timeout 15m ./app/agentapp/
//
// agentapp.Build with the real herdr host on the smoke session ("deskpixie-smoke"), panes kept on
// quit. The first run starts the coordinator (a pong round trip) and quits. While the app is
// down, the coordinator is put in the state Oct 19 left it in: on that night Claude Code gave up
// on the pixie server after half an hour of refused connections; `/mcp reconnect pixie` with the
// server down fails the same way at once, and leaves the server failed and its tools gone. The
// second run, on the same port and data, adopts that coordinator and asks it to use
// list_sessions: it must have the tool.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

func TestLiveAnAdoptedCoordinatorGetsItsPixieToolsBack(t *testing.T) {
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
	s.Session = smoketest.SmokeSession
	s.Port = port
	s.ProjectsRoot = filepath.Join(tmp, "projects")
	s.Workers.Enabled = false
	s.Warm = false
	s.KeepPanesOnQuit = true
	haiku := "haiku"
	s.Model = &haiku
	os.MkdirAll(s.ProjectsRoot, 0o755)

	host := herdr.NewHost(smoketest.SmokeSession, sm.Home, sm.Supervisor(t))
	t.Cleanup(host.Shutdown)
	ctx := context.Background()
	// The kept coordinator outlives both runs: close it at the end.
	t.Cleanup(func() { host.Close(ctx, agent.Coordinator) })

	// run is one app run: it hands the coordinator task and returns the first Coordinator reply.
	run := func(name string, ln net.Listener, task string) (*agentapp.Agent, string) {
		t.Helper()
		a := agentapp.Build(agentapp.Config{
			Settings: s, AppSupport: data, Home: sm.Home, Environ: sm.Environ,
			Listen: func(uint16) (net.Listener, error) { return ln, nil },
		})
		if a == nil {
			t.Fatalf("%s: Build returned nil", name)
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
		out := delegate.Invoke(ctx, json.RawMessage(`{"task":`+quoteJSON(task)+`}`))
		var res struct {
			JobID string `json:"job_id"`
		}
		if json.Unmarshal(out.Output, &res); res.JobID == "" {
			a.Shutdown()
			t.Fatalf("%s: delegate: %s", name, out.Output)
		}
		for deadline := time.Now().Add(4 * time.Minute); ; time.Sleep(time.Second) {
			for _, text := range v.all() {
				if body, ok := strings.CutPrefix(text, "Coordinator reply:"); ok {
					return a, strings.TrimSpace(body)
				}
			}
			if time.Now().After(deadline) {
				screen, _ := host.Read(ctx, agent.Coordinator, 40)
				a.Shutdown()
				t.Fatalf("%s: no Coordinator reply in 4 minutes; injected %q; tray %+v; screen:\n%s", name, v.all(), a.Tray(), screen)
			}
		}
	}

	// 1. The first run starts the coordinator, then quits keeping it.
	a, reply := run("first run", ln, "Reply with the word pong and nothing else.")
	if !isPong(reply) {
		a.Shutdown()
		t.Fatalf("first run: %q", reply)
	}
	a.Shutdown()
	list, _ := host.List(ctx)
	if !slices.ContainsFunc(list, func(i agent.AgentInfo) bool { return i.Name == agent.Coordinator }) {
		t.Fatalf("the coordinator wasn't kept over the quit: %+v", list)
	}
	t.Log("ok   first run: pong, and the coordinator kept over the quit")

	// 2. With the app down, the coordinator gives up on its pixie server.
	if err := host.Prompt(ctx, agent.Coordinator, agent.McpReconnect); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(500 * time.Millisecond) {
		screen, _ := host.Read(ctx, agent.Coordinator, 40)
		if strings.Contains(strings.Join(strings.Fields(screen), " "), "Failed to reconnect to "+agent.McpServer) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the reconnect with the app down didn't fail:\n%s", screen)
		}
	}
	t.Log("ok   app down: the coordinator's pixie server failed")

	// 3. The next run adopts it; its pixie tools must work.
	ln2, err := net.Listen("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	a2, reply := run("second run", ln2, "Call your list_sessions tool. If the call works, reply with exactly the word tools-ok. "+
		"If you have no list_sessions tool or it fails, reply with exactly the word no-tools.")
	t.Cleanup(a2.Shutdown)
	if got := strings.ToLower(strings.Trim(reply, " .!\"'")); got != "tools-ok" {
		screen, _ := host.Read(ctx, agent.Coordinator, 30)
		t.Fatalf("second run: the adopted coordinator replied %q, not tools-ok; its screen:\n%s", reply, screen)
	}
	t.Log("ok   second run: the adopted coordinator used list_sessions")
}
