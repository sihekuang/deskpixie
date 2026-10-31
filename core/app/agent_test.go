package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/attach"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tools/toolstest"
	"github.com/sihekuang/deskpixie/core/voice"
)

// The app's side of the agent wiring (Rust lib.rs/actions.rs/tray.rs/voice/sink.rs agent
// touchpoints), against a fake app.Agent.

type fakeAgent struct {
	mu              sync.Mutex
	fromWindow      []string
	jobAnswers      []string // AnswerJob calls that answered: "job: text"
	answerJobErr    error
	menu            []string
	opened          int
	mouths          []int
	attached        AgentVoice
	shut            int
	tray            AgentTray
	hooks           AgentHooks
	cfg             settings.Config
	seen            [][]uint64
	dismissed       [][]uint64
	decided         []string
	away            []bool
	focused         [][2]string
	updates         any
	roster          any
	focusedAgents   []string
	focusedSessions []string
	focusOutcomes   []string
	sent            []string
	jobs            int    // jobs started by the box
	delegated       int    // ChatDelegate calls
	asking          string // the job whose question the box waits on ("" = none)
	askingSession   string // and the session that asked it, when a session did
	chatErr         error
	stops           int
	interrupted     []string
	interruptErr    error
	textModes       []bool
	markForced      []bool // SetMarkForced calls (#340)
	attach          *attach.Store
	places          map[string][2]string // session → folder, app
	placeCalls      int
	shown           [][]uint64
	replies         any
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
func (f *fakeAgent) UpdatesSeen(ids []uint64) {
	f.mu.Lock()
	f.seen = append(f.seen, ids)
	f.mu.Unlock()
}
func (f *fakeAgent) RepliesShown(ids []uint64) {
	f.mu.Lock()
	f.shown = append(f.shown, ids)
	f.mu.Unlock()
}
func (f *fakeAgent) PendingReplies() any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replies == nil {
		return []any{} // never nil, as the real one
	}
	return f.replies
}
func (f *fakeAgent) shownReplies() []uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var all []uint64
	for _, ids := range f.shown {
		all = append(all, ids...)
	}
	return all
}
func (f *fakeAgent) UpdatesDismissed(ids []uint64) {
	f.mu.Lock()
	f.dismissed = append(f.dismissed, ids)
	f.mu.Unlock()
}
func (f *fakeAgent) Choose(token string, option int) {
	f.mu.Lock()
	f.decided = append(f.decided, fmt.Sprintf("%s:option %d", token, option))
	f.mu.Unlock()
}
func (f *fakeAgent) TypedAnswer(token, text string) {
	f.mu.Lock()
	f.decided = append(f.decided, fmt.Sprintf("%s:typed %s", token, text))
	f.mu.Unlock()
}
func (f *fakeAgent) Pick(token string, options []int) {
	f.mu.Lock()
	f.decided = append(f.decided, fmt.Sprintf("%s:picks %v", token, options))
	f.mu.Unlock()
}
func (f *fakeAgent) ChatAbout(token string) {
	f.mu.Lock()
	f.decided = append(f.decided, token+":chat")
	f.mu.Unlock()
}
func (f *fakeAgent) Dismiss(id uint64) {
	f.mu.Lock()
	f.decided = append(f.decided, fmt.Sprintf("%d:dismiss", id))
	f.mu.Unlock()
}
func (f *fakeAgent) Decide(token string, allow bool) {
	f.mu.Lock()
	f.decided = append(f.decided, fmt.Sprintf("%s:%v", token, allow))
	f.mu.Unlock()
}
func (f *fakeAgent) Updates() any { f.mu.Lock(); defer f.mu.Unlock(); return f.updates }
func (f *fakeAgent) Roster() any  { f.mu.Lock(); defer f.mu.Unlock(); return f.roster }
func (f *fakeAgent) FocusAgent(name string) {
	f.mu.Lock()
	f.focusedAgents = append(f.focusedAgents, name)
	f.mu.Unlock()
}
func (f *fakeAgent) FocusSession(name string) {
	f.mu.Lock()
	f.focusedSessions = append(f.focusedSessions, name)
	f.mu.Unlock()
}
func (f *fakeAgent) SessionPlaces(names []string) []SessionPlace {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.placeCalls++
	var out []SessionPlace
	for _, n := range names {
		if p, ok := f.places[n]; ok {
			out = append(out, SessionPlace{Folder: p[0], App: p[1]})
		}
	}
	return out
}
func (f *fakeAgent) ReportFocusOutcome(success bool, detail string) {
	f.mu.Lock()
	f.focusOutcomes = append(f.focusOutcomes, fmt.Sprintf("%v:%s", success, detail))
	f.mu.Unlock()
}
func (f *fakeAgent) SetAway(away bool) { f.mu.Lock(); f.away = append(f.away, away); f.mu.Unlock() }
func (f *fakeAgent) FocusJob(job, worker string) {
	f.mu.Lock()
	f.focused = append(f.focused, [2]string{job, worker})
	f.mu.Unlock()
}

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
	if got := Instructions("You are Pixie.", persona); got != "You are Pixie.\n\nDelegating:\n- hand it off" {
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
	f.tray.Status = "Coordinator: herdr not found"
	f.mu.Unlock()
	if s := h.state(); !slices.Contains(s.StatusLines, "Coordinator: herdr not found") || slices.Contains(s.StatusLines, IntegrationHint) {
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
	h.call("pixie.click", nil)
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
	f.hooks.Notify("Pixie", "Coordinator reply: done.")
	n := h.waitFor("notify", nil, time.Second)
	if n["title"] != "Pixie" || n["body"] != "Coordinator reply: done." {
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
	h.call("pixie.click", nil)
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

func TestTheUpdatesBubbleCallsReachTheAgent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	h.call("agent.updatesSeen", map[string]any{"ids": []uint64{1, 2}})
	h.call("agent.updatesSeen", map[string]any{"ids": []uint64{3}, "clicked": true})
	h.call("presence.set", map[string]any{"away": true})
	h.call("presence.set", map[string]any{"away": false})
	h.call("agent.focusJob", map[string]any{"job": "job_000000000001", "worker": "focus-upload"})
	h.call("agent.focusJob", map[string]any{"job": "job_000000000002"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != 1 || !slices.Equal(f.seen[0], []uint64{1, 2}) {
		t.Fatal(f.seen)
	}
	if len(f.dismissed) != 1 || !slices.Equal(f.dismissed[0], []uint64{3}) {
		t.Fatal(f.dismissed, "a click (#199) dismisses")
	}
	if !slices.Equal(f.away, []bool{true, false}) {
		t.Fatal(f.away)
	}
	if !slices.Equal(f.focused, [][2]string{{"job_000000000001", "focus-upload"}, {"job_000000000002", ""}}) {
		t.Fatal(f.focused)
	}
}

func TestTheUpdatesHookEmitsAnUpdatesEvent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	type item struct {
		ID   uint64 `json:"id"`
		Text string `json:"text"`
	}
	f.hooks.Updates([]item{{ID: 3, Text: "There are 12."}})
	e := h.waitFor("updates", nil, time.Second)
	if b, _ := json.Marshal(e); string(b) != `{"kind":"updates","lines":[{"id":3,"text":"There are 12."}]}` {
		t.Fatal(string(b))
	}
	f.hooks.Updates([]item{})
	e = h.waitFor("updates", nil, time.Second)
	if b, _ := json.Marshal(e); string(b) != `{"kind":"updates","lines":[]}` {
		t.Fatal("an empty list is [], not null:", string(b))
	}
}

func TestAgentUpdatesReturnsTheCurrentList(t *testing.T) {
	type item struct {
		ID   uint64 `json:"id"`
		Text string `json:"text"`
	}
	f := &fakeAgent{updates: []item{{ID: 3, Text: "There are 12."}}}
	h := newHarness(t, withAgent(f))
	if out := h.call("agent.updates", nil); string(out) != `{"lines":[{"id":3,"text":"There are 12."}]}` {
		t.Fatal(string(out))
	}
}

func TestAgentUpdatesIsAnEmptyListWithoutAnAgent(t *testing.T) {
	h := newHarness(t, func(d *Deps, _ *harness) {
		d.Agent = func(settings.Config, string, AgentHooks) Agent { return nil }
	})
	if out := h.call("agent.updates", nil); string(out) != `{"lines":[]}` {
		t.Fatal(string(out))
	}
}

func TestTheBubbleDecideCallReachesTheAgent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	h.call("agent.decide", map[string]any{"token": "hawk", "allow": true})
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.decided, []string{"hawk:true"}) {
		t.Fatal(f.decided)
	}
	if _, err := h.callErr("agent.decide", "not an object"); err == nil {
		t.Fatal("a malformed body was accepted")
	}
}

// #105: a tap on a session menu's option reaches the agent.
func TestTheBubbleChooseCallReachesTheAgent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	h.call("agent.choose", map[string]any{"token": "hawk", "option": 2})
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.decided, []string{"hawk:option 2"}) {
		t.Fatal(f.decided)
	}
}

// #268: a typed answer and "Chat about this" from the bubble reach the agent, as do picks (#364).
func TestTheBubbleTypedAnswerAndChatReachTheAgent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	h.call("agent.typedAnswer", map[string]any{"token": "hawk", "text": "MariaDB"})
	h.call("agent.chatAbout", map[string]any{"token": "owl"})
	h.call("agent.pick", map[string]any{"token": "lark", "options": []int{1, 3}}) // #364
	h.call("agent.dismiss", map[string]any{"id": 42})                             // #388
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.decided, []string{"hawk:typed MariaDB", "owl:chat", "lark:picks [1 3]", "42:dismiss"}) {
		t.Fatal(f.decided)
	}
}

func TestRepliesCallsReachTheAgent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	h.call("agent.repliesShown", map[string]any{"ids": []uint64{3, 4}})
	if got := f.shownReplies(); !slices.Equal(got, []uint64{3, 4}) {
		t.Fatal(got)
	}
	if out := h.call("agent.replies", map[string]any{}); !strings.HasPrefix(string(out), `{"cards":`) {
		t.Fatal(string(out))
	}
}

func TestTheReplyHookEmitsAReplyCardEvent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	f.hooks.Reply(map[string]any{"id": 7, "text": "Done."})
	e := h.waitFor("reply.card", nil, time.Second)
	if b, _ := json.Marshal(e); string(b) != `{"id":7,"kind":"reply.card","text":"Done."}` {
		t.Fatal(string(b))
	}
}

func TestTheRepliesCallsAreNoOpsWithoutAnAgent(t *testing.T) {
	h := newHarness(t, func(d *Deps, _ *harness) {
		d.Agent = func(settings.Config, string, AgentHooks) Agent { return nil }
	})
	if out := h.call("agent.repliesShown", map[string]any{"ids": []uint64{1}}); string(out) != "{}" {
		t.Fatal(string(out))
	}
	if out := h.call("agent.replies", map[string]any{}); string(out) != `{"cards":[]}` {
		t.Fatal(string(out))
	}
}

func TestTheUpdatesBubbleCallsAreNoOpsWithoutAnAgent(t *testing.T) {
	h := newHarness(t, func(d *Deps, _ *harness) {
		d.Agent = func(settings.Config, string, AgentHooks) Agent { return nil }
	})
	for method, args := range map[string]any{
		"agent.updatesSeen": map[string]any{"ids": []uint64{1}},
		"agent.decide":      map[string]any{"token": "hawk", "allow": true},
		"agent.choose":      map[string]any{"token": "hawk", "option": 1},
		"agent.typedAnswer": map[string]any{"token": "hawk", "text": "x"},
		"agent.chatAbout":   map[string]any{"token": "hawk"},
		"agent.dismiss":     map[string]any{"id": 1},
		"agent.pick":        map[string]any{"token": "hawk", "options": []int{1}},
		"agent.focusJob":    map[string]any{"job": "job_000000000001"},
		"presence.set":      map[string]any{"away": true},
	} {
		if out := h.call(method, args); string(out) != "{}" {
			t.Fatal(method, string(out))
		}
	}
}

// #78: the local tools are built once, before the agent, and the agent gets the very instances
// the voice loop holds (a timer set through one is a timer of the other).
func TestTheAgentGetsTheSameLocalToolsAsTheVoiceLoop(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	var got []string
	for _, lt := range f.hooks.LocalTools {
		got = append(got, lt.Name())
	}
	if !slices.Equal(got, []string{"timer", "get_weather", "media", "get_time", "calculate", "open_settings"}) {
		t.Fatal(got)
	}
	timer := f.hooks.LocalTools[0]
	if _, _, err := timer.Invoke(context.Background(), json.RawMessage(`{"action":"start","duration_seconds":600,"label":"tea"}`)); err != nil {
		t.Fatal(err)
	}
	vt, ok := h.app.voiceTools.Get("timer")
	if !ok {
		t.Fatal("the voice loop has no timer")
	}
	out := vt.Invoke(context.Background(), json.RawMessage(`{"action":"list"}`))
	if !strings.Contains(string(out.Output), `"label":"tea"`) {
		t.Fatalf("the voice loop's timer is another one: %s", out.Output)
	}
	_ = vt.Invoke(context.Background(), json.RawMessage(`{"action":"cancel"}`))
}

// #78: a timer the coordinator sets rings through the app's own alarm (here: no voice loop at
// all, so a chime and a notification), like one Pixie sets.
func TestACoordinatorTimerRingsThroughTheAppEvenWithoutAVoiceLoop(t *testing.T) {
	f := &fakeAgent{}
	clock := toolstest.NewTimerClock()
	h := newHarness(t, withAgent(f), func(d *Deps, _ *harness) {
		d.Providers = map[string]ModelFactory{} // the voice provider is unknown: no voice loop
		d.LocalTools = LocalToolDeps{TimerClock: clock}
	})
	if h.app.Runtime() != nil {
		t.Fatal("expected no voice loop")
	}
	if len(f.hooks.LocalTools) != 6 {
		t.Fatal("the agent gets the local tools even without a voice loop:", len(f.hooks.LocalTools))
	}
	if _, _, err := f.hooks.LocalTools[0].Invoke(context.Background(), json.RawMessage(`{"action":"start","duration_seconds":120,"label":"pasta"}`)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	ev := h.waitFor("notify", func(e event) bool { return e["title"] == "Timer done" }, 2*time.Second)
	if ev["body"] != "pasta (2 minutes)" || ev["sound"] != "glass" {
		t.Fatal(ev)
	}
}

func TestTheRosterHookEmitsAnAgentsEvent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	f.hooks.Roster(map[string]any{"agent_count": 1})
	e := h.waitFor("agents", nil, time.Second)
	if b, _ := json.Marshal(e); string(b) != `{"kind":"agents","roster":{"agent_count":1}}` {
		t.Fatal(string(b))
	}
}

func TestAgentRosterAndFocusAgentReachTheAgent(t *testing.T) {
	f := &fakeAgent{roster: map[string]any{"your_count": 2}}
	h := newHarness(t, withAgent(f))
	if out := h.call("agent.roster", nil); string(out) != `{"roster":{"your_count":2}}` {
		t.Fatal(string(out))
	}
	h.call("agent.focusAgent", map[string]any{"name": "deskpixie-claude"})
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.focusedAgents, []string{"deskpixie-claude"}) {
		t.Fatal(f.focusedAgents)
	}
}

// #148: a click on one of your sessions reaches the agent by name.
func TestAgentFocusSessionReachesTheAgent(t *testing.T) {
	f := &fakeAgent{}
	h := newHarness(t, withAgent(f))
	h.call("agent.focusSession", map[string]any{"name": "api-claude"})
	h.call("agent.focusSession", map[string]any{"name": ""})
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(f.focusedSessions, []string{"api-claude"}) {
		t.Fatal(f.focusedSessions)
	}
}

func TestAgentRosterIsEmptyWithoutAnAgent(t *testing.T) {
	h := newHarness(t, func(d *Deps, _ *harness) {
		d.Agent = func(settings.Config, string, AgentHooks) Agent { return nil }
	})
	if out := h.call("agent.roster", nil); string(out) != `{"roster":{"agents":[],"yours":[],"agent_count":0,"your_count":0,"busy":false}}` {
		t.Fatal(string(out))
	}
	if out := h.call("agent.focusAgent", map[string]any{"name": "x"}); string(out) != "{}" {
		t.Fatal(string(out))
	}
}

func TestTheManualModeLineAndFixReachAppState(t *testing.T) {
	line := "Coordinator is in Manual mode: its model is too old for auto mode"
	f := &fakeAgent{tray: AgentTray{Enabled: true, Mode: line, ModeFix: &TrayFix{ID: "agent:fix-model", Title: "Use a supported model"}}}
	h := newHarness(t, withAgent(f))
	s := h.state()
	if !slices.Contains(s.StatusLines, line) {
		t.Fatal(s.StatusLines)
	}
	b, _ := json.Marshal(s)
	if !strings.Contains(string(b), `"agentModeFix":{"id":"agent:fix-model","title":"Use a supported model"}`) {
		t.Fatal(string(b))
	}
}

func TestTheClearModelHookSavesNullAgentModel(t *testing.T) {
	f := &fakeAgent{tray: AgentTray{Enabled: true}}
	h := newHarness(t, withAgent(f))
	m := "claude-sonnet-4-5"
	h.app.save(func(c *settings.Config) { c.Agent.Model = &m })
	f.hooks.ClearModel()
	if h.app.store.Current().Agent.Model != nil {
		t.Fatal("model not cleared")
	}
}
