//go:build live

package agentapp_test

// #288, live: Pixie hands "where do I change the microphone?" to the coordinator, which opens
// Settings at the microphone with its open_settings tool (a fake window here, recording what it
// was asked) and answers which page it is on. Same setup as TestLiveDelegationEndToEnd: the
// herdr smoke session, a free port, temp app data. Run:
//
//	go test -count=1 -tags live -run TestLiveCoordinatorOpensSettings -v -timeout 10m ./app/agentapp/

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
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/voice"
)

// liveWindow is the Settings window: it finds the microphone for any query naming it, and
// records each search and each open.
type liveWindow struct {
	mu      sync.Mutex
	queries []string
	opened  []string
}

func (w *liveWindow) Find(q string) []tools.SettingMatch {
	w.mu.Lock()
	w.queries = append(w.queries, q)
	w.mu.Unlock()
	if strings.Contains(strings.ToLower(q), "mic") {
		return []tools.SettingMatch{{Path: "voice.inputDevice", Label: "Microphone", Page: "Voice"}}
	}
	return nil
}

func (w *liveWindow) Open(m *tools.SettingMatch) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if m == nil {
		w.opened = append(w.opened, "")
	} else {
		w.opened = append(w.opened, m.Path)
	}
}

func (w *liveWindow) seen() ([]string, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.queries...), append([]string(nil), w.opened...)
}

func TestLiveCoordinatorOpensSettings(t *testing.T) {
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
	s.KeepPanesOnQuit = false
	if err := os.MkdirAll(s.ProjectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	win := &liveWindow{}
	a := agentapp.Build(agentapp.Config{
		Settings: s, AppSupport: data, Home: sm.Home, Environ: sm.Environ,
		Hooks:  app.AgentHooks{LocalTools: []tools.Tool{tools.OpenSettings{Window: win}}},
		Listen: func(uint16) (net.Listener, error) { return ln, nil },
	})
	if a == nil {
		t.Fatal("Build returned nil")
	}
	t.Cleanup(a.Shutdown)

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
	// What Pixie hands off: the user's words as they said them.
	out := delegate.Invoke(context.Background(), json.RawMessage(`{"task":"Where do I change the microphone?"}`))
	t.Logf("delegate → %s", out.Output)

	deadline := time.Now().Add(4 * time.Minute)
	reply := ""
	for reply == "" && time.Now().Before(deadline) {
		for _, text := range v.all() {
			if body, ok := strings.CutPrefix(text, "Coordinator reply:"); ok {
				reply = strings.TrimSpace(body)
			}
		}
		time.Sleep(time.Second)
	}
	queries, opened := win.seen()
	t.Logf("open_settings searched %q, opened %q; reply %q", queries, opened, reply)
	if reply == "" {
		t.Fatalf("no Coordinator reply within 4 minutes; injected %q", v.all())
	}
	if len(opened) == 0 || opened[len(opened)-1] != "voice.inputDevice" {
		t.Errorf("want Settings opened at the microphone: searched %q, opened %q", queries, opened)
	}
	if !strings.Contains(strings.ToLower(reply), "voice") {
		t.Errorf("want the reply to name the Voice page: %q", reply)
	}
}
