package agentapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/tools/toolstest"
)

// #78: the coordinator gets Pixie's local tools over the pixie MCP server.

var localToolNames = []string{"timer", "get_weather", "media", "get_time", "calculate"}

type notes struct {
	mu  sync.Mutex
	got [][2]string
}

func (n *notes) add(title, body string) {
	n.mu.Lock()
	n.got = append(n.got, [2]string{title, body})
	n.mu.Unlock()
}
func (n *notes) list() [][2]string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.got)
}

// localFixture is the agent built with the app's local tools: the real tools and the app's own
// TimerAlarm (no voice loop: a chime and a notification), over a fake clock, atlas, sky and
// player.
func localFixture(t *testing.T, change func(*Config)) (*fixture, *toolstest.TimerClock, *notes) {
	t.Helper()
	clock := toolstest.NewTimerClock()
	rung := &notes{}
	chance := 10
	local := app.NewLocalTools("Paris", &app.TimerAlarm{Notify: rung.add}, app.LocalToolDeps{
		Clock:      tools.FixedClock{NowTime: time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC), Location: time.UTC},
		TimerClock: clock,
		Places:     toolstest.Atlas{Places: []tools.Place{{Name: "Paris", Country: "France", CountryCode: "FR", Timezone: "Europe/Paris"}}},
		Sky:        toolstest.Sky{Today: tools.Forecast{Temperature: 18, Code: 0, Wind: 5, High: 20, Low: 11, PrecipitationChance: &chance}},
		Player:     &toolstest.Player{Said: "Skipped to the next song."},
	})
	f := newFixture(t, func(c *Config) {
		c.Hooks.LocalTools = local
		if change != nil {
			change(c)
		}
	})
	return f, clock, rung
}

func (f *fixture) mcp(t *testing.T, token string, method string, params any) map[string]any {
	t.Helper()
	status, body := f.post(t, "/mcp", token, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if status != 200 {
		t.Fatalf("%s: %d %s", method, status, body)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		t.Fatal(body)
	}
	return v
}

func (f *fixture) listedTools(t *testing.T, token string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	result, _ := f.mcp(t, token, "tools/list", map[string]any{})["result"].(map[string]any)
	all, _ := result["tools"].([]any)
	for _, tl := range all {
		m := tl.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

func (f *fixture) callTool(t *testing.T, token, name string, args any) map[string]any {
	t.Helper()
	v := f.mcp(t, token, "tools/call", map[string]any{"name": name, "arguments": args})
	result, _ := v["result"].(map[string]any)
	if result == nil || result["isError"] != false {
		t.Fatalf("%s: %v", name, v)
	}
	content := result["content"].([]any)[0].(map[string]any)
	var out map[string]any
	if err := json.Unmarshal([]byte(content["text"].(string)), &out); err != nil {
		t.Fatal(content)
	}
	return out
}

func (f *fixture) startCoordinator(t *testing.T) string {
	t.Helper()
	f.agent.Menu("agent:open")
	waitFor(t, "the coordinator", func() bool { return len(f.host.Starts()) == 1 })
	return f.coordinatorToken(t)
}

func TestTheCoordinatorAlwaysGetsThePixieMcpServer(t *testing.T) {
	f, _, _ := localFixture(t, nil) // the default settings: workers off
	f.startCoordinator(t)
	args := f.host.Starts()[0].Args
	i := slices.Index(args, "--mcp-config")
	if i < 0 || args[i+1] != NewPaths(f.data).CoordinatorMcp() {
		t.Fatal("workers off, the coordinator still gets the pixie MCP server:", args)
	}
	text := readFile(t, NewPaths(f.data).CoordinatorMcp())
	if !strings.Contains(text, `"`+agent.McpServer+`"`) || agent.McpServer != "pixie" {
		t.Fatal(text)
	}
}

func TestWithWorkersOffTheCoordinatorListsTheLocalAndSessionToolsWordedForIt(t *testing.T) {
	f, _, _ := localFixture(t, nil)
	token := f.startCoordinator(t)
	got := f.listedTools(t, token)
	var names []string
	for k := range got {
		names = append(names, k)
	}
	slices.Sort(names)
	want := append(slices.Clone(localToolNames), agent.SessionTools[:]...) // sessions don't need workers (#89)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatal(names)
	}
	for _, name := range localToolNames {
		d, _ := got[name]["description"].(string)
		if d == "" || strings.Contains(d, "delegate") {
			t.Errorf("%s: a coordinator description must not speak of delegate: %q", name, d)
		}
	}
	if !strings.Contains(got["timer"]["description"].(string), "sleep") {
		t.Error("the timer description warns against sleep")
	}
}

func TestWithWorkersOnTheCoordinatorHasBothItsWorkerToolsAndTheLocalOnes(t *testing.T) {
	f, _, _ := localFixture(t, func(c *Config) {
		c.Settings.Workers = settings.WorkerSettings{Enabled: true, Max: 2, Kinds: []string{"claude"}}
		c.Settings.ProjectsRoot = "~/code"
		_ = os.MkdirAll(filepath.Join(c.Home, "code"), 0o755)
	})
	got := f.listedTools(t, f.startCoordinator(t))
	for _, name := range append(slices.Clone(localToolNames), agent.CoordinatorTools[:]...) {
		if got[name] == nil {
			t.Errorf("%s is missing", name)
		}
	}
}

func TestEachLocalToolAnswersTheRealCoordinatorEndpoint(t *testing.T) {
	f, _, _ := localFixture(t, nil)
	token := f.startCoordinator(t)
	if got := f.callTool(t, token, "calculate", map[string]any{"value": 100, "from": "celsius", "to": "fahrenheit"}); !strings.Contains(got["result"].(string), "212") {
		t.Fatal(got)
	}
	if got := f.callTool(t, token, "get_time", map[string]any{}); got["time"] != "9:30 AM" {
		t.Fatal(got)
	}
	if got := f.callTool(t, token, "get_weather", map[string]any{}); !strings.HasPrefix(got["summary"].(string), "Paris, France") {
		t.Fatal("the home city is the default place:", got)
	}
	if got := f.callTool(t, token, "media", map[string]any{"action": "next"}); got["done"] != "Skipped to the next song." {
		t.Fatal(got)
	}
}

// The timer is the app's: it rings through the app's TimerAlarm (here a chime and a
// notification), and the coordinator going away does not touch it.
func TestATimerSetOverMcpRingsThroughTheAppsAlarmAfterTheCoordinatorRestarts(t *testing.T) {
	f, clock, rung := localFixture(t, nil)
	token := f.startCoordinator(t)
	if got := f.callTool(t, token, "timer", map[string]any{"action": "start", "duration_seconds": 300, "label": "tea"}); got["label"] != "tea" {
		t.Fatal(got)
	}
	f.agent.Menu("agent:restart")
	waitFor(t, "the coordinator stopped", func() bool { return slices.Contains(f.host.Closed(), agent.Coordinator) })
	clock.Advance(5 * time.Minute)
	if got := rung.list(); !slices.Equal(got, [][2]string{{"Timer done", "tea (5 minutes)"}}) {
		t.Fatal(got)
	}
}

func TestTheCoordinatorPromptSendsTimersWeatherMusicTimeAndMathToTheTools(t *testing.T) {
	for _, md := range []string{RenderClaudeMd("/data/canvas", ""), RenderClaudeMd("/data/canvas", "/Users/x/code")} {
		for _, name := range localToolNames {
			if !strings.Contains(md, "`"+name+"`") {
				t.Errorf("coordinator.md does not name %s", name)
			}
		}
		for _, rule := range []string{"web search", "sleep"} {
			if !strings.Contains(md, rule) {
				t.Errorf("coordinator.md does not mention %q", rule)
			}
		}
	}
}

// #153: with workers on or off, work a pix session is doing, and questions about it, go to
// that session before the coordinator does anything itself.
func TestTheCoordinatorPromptHandsASessionsWorkToThatSession(t *testing.T) {
	for _, md := range []string{RenderClaudeMd("/data/canvas", ""), RenderClaudeMd("/data/canvas", "/Users/x/code")} {
		handOff := strings.Index(md, "hand it to that session")
		yourself := strings.Index(md, "do them yourself")
		if handOff < 0 || yourself < 0 || handOff > yourself {
			t.Errorf("the hand-off rule must come before doing things yourself:\n%s", md)
		}
		for _, rule := range []string{"committed, pushed or built", "without warnings they didn't give"} {
			if !strings.Contains(md, rule) {
				t.Errorf("coordinator.md does not mention %q", rule)
			}
		}
	}
	if solo := RenderClaudeMd("/data/canvas", ""); !strings.Contains(solo, "If no session fits, say so in your reply") {
		t.Errorf("with workers off, code changes go to a session first:\n%s", solo)
	}
}

// mcp__pixie in permissions.allow allows every tool of the pixie server, the local ones too.
func TestThePermissionAllowListCoversTheLocalTools(t *testing.T) {
	var v map[string]any
	if err := json.Unmarshal([]byte(RenderSettings(48217)), &v); err != nil {
		t.Fatal(err)
	}
	allow, _ := at(v, "permissions", "allow").([]any)
	if !slices.Contains(allow, any("mcp__"+agent.McpServer)) {
		t.Fatal(allow)
	}
	for _, rule := range allow {
		if s, _ := rule.(string); strings.HasPrefix(s, "mcp__"+agent.McpServer+"__") {
			t.Fatal("a per-tool rule would leave the others unallowed:", s)
		}
	}
}
