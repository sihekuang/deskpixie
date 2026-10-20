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
//
// The second test (#188) puts it in the state an app restart on Oct 20 left it in: herdr's own
// session restore relaunched the pane as plain `claude --resume <id>`, without the app's
// --settings and --mcp-config, so it has no pixie server at all. The second run must replace it
// with one of its own and still get tools-ok.
//
// The third (#189): between the runs the coordinator's model changes (from haiku to sonnet),
// which Claude Code takes only at its start. The second run must replace the kept coordinator
// with one started on sonnet. (A change to its instructions alone needs no restart: Claude Code
// rereads a CLAUDE.md that changed, which a run of this test with the stamp check off showed.)

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
	liveAdopt(t, toolsTask, toolsWorked, func(ctx context.Context, host *herdr.Host, _ *settings.AgentSettings) {
		// With the app down, the coordinator gives up on its pixie server.
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
	})
}

func TestLiveAnAdoptedCoordinatorHerdrRestoredWithoutItsServerIsReplaced(t *testing.T) {
	liveAdopt(t, toolsTask, toolsWorked, func(ctx context.Context, host *herdr.Host, _ *settings.AgentSettings) {
		list, _ := host.List(ctx)
		i := slices.IndexFunc(list, func(i agent.AgentInfo) bool { return i.Name == agent.Coordinator })
		// Same pane name, cwd and conversation, none of the app's arguments.
		if _, err := host.ReplaceAgent(ctx, agent.AgentSpec{Name: agent.Coordinator, Kind: agent.Claude, Cwd: list[i].Cwd, Args: []string{"--continue"}}); err != nil {
			t.Fatal(err)
		}
		for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(time.Second) {
			screen, _ := host.Read(ctx, agent.Coordinator, 40)
			if strings.Contains(screen, "❯") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the bare coordinator never showed its prompt:\n%s", screen)
			}
		}
		t.Log("ok   app down: the coordinator relaunched without the app's arguments")
	})
}

func TestLiveAKeptCoordinatorStartedOnAnotherModelIsReplaced(t *testing.T) {
	liveAdopt(t, "Which model family are you: Haiku, Sonnet or Opus? Reply with only the one word.",
		func(reply string) bool { return strings.Contains(strings.ToLower(reply), "sonnet") },
		func(_ context.Context, _ *herdr.Host, s *settings.AgentSettings) {
			sonnet := "sonnet"
			s.Model = &sonnet
			t.Log("ok   app down: the coordinator's model changed to sonnet")
		})
}

const toolsTask = "Call your list_sessions tool. If the call works, reply with exactly the word tools-ok. " +
	"If you have no list_sessions tool or it fails, reply with exactly the word no-tools."

// liveAdopt: a first run starts the coordinator and quits keeping it; breakIt changes something
// while the app is down; a second run, given task, must reply what ok accepts.
func liveAdopt(t *testing.T, task string, ok func(string) bool, breakIt func(context.Context, *herdr.Host, *settings.AgentSettings)) {
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

	// 2. While the app is down.
	breakIt(ctx, host, &s)

	// 3. The next run adopts it; its pixie tools must work.
	ln2, err := net.Listen("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	a2, reply := run("second run", ln2, task)
	t.Cleanup(a2.Shutdown)
	if !ok(reply) {
		screen, _ := host.Read(ctx, agent.Coordinator, 30)
		t.Fatalf("second run: the coordinator replied %q; its screen:\n%s", reply, screen)
	}
	t.Log("ok   second run: the coordinator replied", reply)
}

// toolsWorked: the reply says the tool call worked. Haiku often says it in words, as the
// coordinator's spoken rules ask ("Tools are working.", "The session list call worked").
func toolsWorked(reply string) bool {
	got := strings.ToLower(reply)
	for _, bad := range []string{"no-tools", "not work", "fail", "don't have", "unavailable", "down"} {
		if strings.Contains(got, bad) {
			return false
		}
	}
	return strings.Contains(got, "tools-ok") || strings.Contains(got, "work")
}
