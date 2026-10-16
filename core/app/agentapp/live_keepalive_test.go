//go:build live

package agentapp_test

// Opt-in live check that the coordinator keeps running while the app runs (#118):
//
//	go test -tags live -run LiveKeepAlive -v -timeout 10m ./app/agentapp/
//
// agentapp.Build with the real herdr host on the smoke session ("deskpixie-smoke"), not warmed:
// nothing starts until a conversation opens, which starts the coordinator. Its pane is then
// closed from outside (as a crash or a closed tab would) twice, and must come back each time.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

func TestLiveKeepAliveTheCoordinatorComesBack(t *testing.T) {
	sm := smoketest.Smoke(t)
	tmp := smoketest.CanonicalTemp(t)
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
	s.KeepPanesOnQuit = false
	haiku := "haiku"
	s.Model = &haiku
	if err := os.MkdirAll(s.ProjectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: filepath.Join(tmp, "data"), Home: sm.Home, Environ: sm.Environ,
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	t.Cleanup(a.Shutdown)
	v := &liveVoice{sig: make(chan voice.VoiceSignal, 8)}
	a.AttachVoice(v)

	host := herdr.NewHost(smoketest.SmokeSession, sm.Home, sm.Supervisor(t))
	t.Cleanup(host.Shutdown)
	ctx := context.Background()
	// coordinator is the coordinator's pane ("" if none) and its status.
	coordinator := func() (string, agent.PaneStatus) {
		list, _ := host.List(ctx)
		if i := slices.IndexFunc(list, func(a agent.AgentInfo) bool { return a.Name == agent.Coordinator }); i >= 0 {
			return list[i].Pane, list[i].Status
		}
		return "", 0
	}
	pane := func() string { p, _ := coordinator(); return p }
	// idle: the coordinator is up and waiting (its start finished), with a clear tray status.
	idle := func() bool { p, st := coordinator(); return p != "" && st == agent.PaneIdle && a.Tray().Status == "" }
	waitFor := func(what string, within time.Duration, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(within); !ok(); time.Sleep(500 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: not within %v; tray %+v", what, within, a.Tray())
			}
		}
	}

	time.Sleep(3 * time.Second)
	if p := pane(); p != "" {
		t.Fatalf("a coordinator (%s) started before any conversation", p)
	}
	v.sig <- voice.VoiceSignal{Kind: voice.SigOpened}
	waitFor("the coordinator started by opening a conversation", time.Minute, idle)
	was := pane()
	t.Logf("ok   opening a conversation started the coordinator (%s)", was)

	for i := 1; i <= 2; i++ {
		began := time.Now()
		if err := host.Close(ctx, agent.Coordinator); err != nil {
			t.Fatalf("close the coordinator: %v", err)
		}
		waitFor("the closed coordinator's pane gone", 10*time.Second, func() bool { return pane() != was })
		waitFor("the coordinator back and idle", time.Minute, func() bool { return pane() != was && idle() })
		back := time.Since(began)
		if back < time.Duration(agent.CoordinatorRestartMs)*time.Millisecond {
			t.Fatalf("back in %v, before the restart pause", back)
		}
		was = pane()
		t.Logf("ok   closed %d: back and idle in %v (%s)", i, back.Round(100*time.Millisecond), was)
	}
}
