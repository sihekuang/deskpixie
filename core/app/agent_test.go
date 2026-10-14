package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// The app's side of the agent wiring (Rust lib.rs/actions.rs/tray.rs/voice/sink.rs agent
// touchpoints), against a fake app.Agent.

type fakeAgent struct {
	mu       sync.Mutex
	menu     []string
	opened   int
	mouths   []int
	attached AgentVoice
	shut     int
	tray     AgentTray
	hooks    AgentHooks
	cfg      settings.Config
}

func (f *fakeAgent) Tools() []voice.Tool { return []voice.Tool{namedTool("delegate")} }
func (f *fakeAgent) Persona() string     { return "Delegating:\n- hand it off" }
func (f *fakeAgent) Tray() AgentTray {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tray
}
func (f *fakeAgent) Menu(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.menu = append(f.menu, id)
	return id != "agent:bogus"
}
func (f *fakeAgent) OpenTerminal() { f.mu.Lock(); f.opened++; f.mu.Unlock() }
func (f *fakeAgent) AttachVoice(v AgentVoice) {
	f.mu.Lock()
	f.attached = v
	f.mu.Unlock()
}
func (f *fakeAgent) OnMouth(level int) {
	f.mu.Lock()
	f.mouths = append(f.mouths, level)
	f.mu.Unlock()
}
func (f *fakeAgent) Shutdown() { f.mu.Lock(); f.shut++; f.mu.Unlock() }

type namedTool string

func (n namedTool) Name() string              { return string(n) }
func (namedTool) Description() string         { return "x" }
func (namedTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (namedTool) Invoke(context.Context, json.RawMessage) voice.ToolOutcome {
	return voice.ToolOutcome{Output: json.RawMessage(`{}`)}
}

func withAgent(f *fakeAgent) harnessOpt {
	return func(d *Deps, _ *harness) {
		d.Agent = func(cfg settings.Config, _ string, hooks AgentHooks) Agent {
			f.cfg, f.hooks = cfg, hooks
			return f
		}
	}
}

func TestTheAgentGetsTheVoiceLoopAndAddsItsToolsAndPersona(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	if f.attached == nil || f.attached != AgentVoice(h.app.Runtime()) {
		t.Fatal("the voice loop was not attached")
	}
	if !f.cfg.Agent.Enabled {
		t.Fatal("the factory gets the settings")
	}
	tools, persona := h.app.voiceExtras()
	if len(tools) != 1 || tools[0].Name() != "delegate" || persona != "Delegating:\n- hand it off" {
		t.Fatal(tools, persona)
	}
	if got := Instructions("You are Bit.", persona); got != "You are Bit.\n\nDelegating:\n- hand it off" {
		t.Fatal(got)
	}
}

func TestTheAgentTrayRowsAndStatusLineReachAppState(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true, Status: "", Hint: IntegrationHint,
		Jobs: []JobRow{{ID: "job_000000000001", Status: "working", Task: strings.Repeat("x", 50), AgeSecs: 125}}}}
	h := newHarness(t, withAgent(f))
	s := h.state()
	if !s.Agent.Enabled || len(s.Agent.Jobs) != 1 || s.Agent.Jobs[0].ID != "job_000000000001" {
		t.Fatalf("%+v", s.Agent)
	}
	if want := strings.Repeat("x", 39) + "… — working, 2 min"; s.Agent.Jobs[0].Label != want {
		t.Fatalf("%q", s.Agent.Jobs[0].Label)
	}
	if !slices.Contains(s.StatusLines, IntegrationHint) {
		t.Fatal(s.StatusLines)
	}
	f.mu.Lock()
	f.tray.Status = "Agent: herdr not found"
	f.mu.Unlock()
	if s := h.state(); !slices.Contains(s.StatusLines, "Agent: herdr not found") || slices.Contains(s.StatusLines, IntegrationHint) {
		t.Fatal("the runtime status wins over the hint:", s.StatusLines)
	}
}

func TestAgentMenuIdsClickAndMouthReachTheAgent(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.action("agent:cancel:job_000000000001")
	h.action("agent:open")
	h.action("agent:restart")
	if _, err := h.callErr("app.action", map[string]string{"id": "agent:bogus"}); err == nil {
		t.Fatal("an id the agent does not know is an error")
	}
	h.call("mascot.click", nil)
	h.app.onVoiceEvent(voice.UIEvent{Kind: "mouth", Level: 3})
	h.app.onVoiceEvent(voice.UIEvent{Kind: "mouth", Level: 0})
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.menu, []string{"agent:cancel:job_000000000001", "agent:open", "agent:restart", "agent:bogus"}) {
		t.Fatal(f.menu)
	}
	if f.opened != 1 || !slices.Equal(f.mouths, []int{3, 0}) {
		t.Fatal(f.opened, f.mouths)
	}
}

func TestAgentHooksCelebrateFlashRefreshAndNotify(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	h.take()
	f.hooks.FlashError()
	h.waitFor("state", func(e event) bool { return e["state"] == "error" }, time.Second)
	f.hooks.RefreshTray()
	h.waitFor("tray", nil, time.Second)
	f.hooks.Notify("Bit", "Agent reply: done.")
	n := h.waitFor("notify", nil, time.Second)
	if n["title"] != "Bit" || n["body"] != "Agent reply: done." {
		t.Fatal(n)
	}
	if _, ok := n["sound"]; ok {
		t.Fatal("the agent chimes on its own; the notification is silent")
	}
}

func TestClosingTheAppShutsTheAgentDownFirst(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	h.app.Close()
	h.app.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.shut != 1 {
		t.Fatal(f.shut)
	}
}

func TestANilAgentLeavesTheMenuHidden(t *testing.T) {
	h := newHarness(t, func(d *Deps, _ *harness) {
		d.Agent = func(settings.Config, string, AgentHooks) Agent { return nil }
	})
	if h.state().Agent.Enabled {
		t.Fatal("shown")
	}
	h.action("agent:open") // a logged no-op
	h.call("mascot.click", nil)
}

// rememberingAgent is an agent whose coordinator side gives each voice session a context block.
type rememberingAgent struct {
	fakeAgent
	facts string
}

func (r *rememberingAgent) Facts() string { return r.facts }

func TestEachVoiceSessionGetsTheAgentsRecentWork(t *testing.T) {
	a := &App{agent: &rememberingAgent{facts: "Recent work with the agent:\n- [9:00 AM] done: count issues."}}
	got := a.sessionFacts("Leeds").Facts()
	local := strings.Index(got, "Leeds")
	recent := strings.Index(got, "\n\nRecent work with the agent:\n- [9:00 AM] done: count issues.")
	if local < 0 || recent < local {
		t.Fatal(got)
	}
	// Nothing to remember adds nothing; no agent, only the local facts.
	a.agent.(*rememberingAgent).facts = "  "
	if got := a.sessionFacts("Leeds").Facts(); strings.Contains(got, "Recent work") || strings.HasSuffix(got, "\n") {
		t.Fatal(got)
	}
	if got := (&App{}).sessionFacts("Leeds").Facts(); !strings.Contains(got, "Leeds") || strings.Contains(got, "Recent work") {
		t.Fatal(got)
	}
	if got := (&App{agent: &fakeAgent{}}).sessionFacts("Leeds").Facts(); strings.Contains(got, "Recent work") {
		t.Fatal(got)
	}
}
