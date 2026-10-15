package httpserver

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/tools/toolstest"
)

// #78: Pixie's local tools (timer, get_weather, media, get_time, calculate) are served to the
// coordinator only, as the very tools the voice loop holds.

type rang struct {
	mu    sync.Mutex
	fired []string
}

func (r *rang) Fire(label, spoken string) {
	r.mu.Lock()
	r.fired = append(r.fired, label+" ("+spoken+")")
	r.mu.Unlock()
}

func (r *rang) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.fired...)
}

type localRig struct {
	mcpRig
	clock  *toolstest.TimerClock
	alarm  *rang
	player *toolstest.Player
	local  []tools.Tool
}

func paris() tools.Place {
	return tools.Place{Name: "Paris", Country: "France", CountryCode: "FR", Latitude: 48.85, Longitude: 2.35, Timezone: "Europe/Paris"}
}

func localTools(clock *toolstest.TimerClock, alarm tools.Alarm, player tools.MediaPlayer) []tools.Tool {
	atlas := toolstest.Atlas{Places: []tools.Place{paris()}}
	chance := 60
	sky := toolstest.Sky{Today: tools.Forecast{Temperature: 18, Code: 61, Wind: 12, High: 21, Low: 12, PrecipitationChance: &chance}}
	return []tools.Tool{
		tools.TimerTool{Timers: tools.NewTimersWithClock(alarm, clock)},
		tools.GetWeather{Places: atlas, Sky: sky, Home: "Paris"},
		tools.Media{Player: player},
		tools.GetTime{Clock: tools.FixedClock{NowTime: time.Date(2026, 10, 14, 15, 4, 0, 0, time.UTC), Location: time.UTC}, Places: atlas},
		tools.Calculate{},
	}
}

func lrig(workersOff bool) localRig {
	tokens := NewTokens()
	coordinator := tokens.Issue("coordinator")
	worker := tokens.Issue("focus-fix")
	rt := newFakeRuntime()
	clock := toolstest.NewTimerClock()
	alarm := &rang{}
	player := &toolstest.Player{Said: "Paused."}
	local := localTools(clock, alarm, player)
	var served []LocalTool
	for _, t := range local {
		served = append(served, t)
	}
	srv := NewServerWith(tokens, rt, Options{CoordinatorTools: served, NoWorkerTools: workersOff})
	return localRig{mcpRig: mcpRig{app: srv, coordinator: coordinator, worker: worker, rt: rt}, clock: clock, alarm: alarm, player: player, local: local}
}

var toolsList = map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}}

func listed(t *testing.T, r localRig, bearer string) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	all, _ := get(rpc(t, r.app, bearer, toolsList), "result", "tools").([]any)
	for _, tl := range all {
		m := tl.(map[string]any)
		out[m["name"].(string)] = m
	}
	return out
}

func names(v map[string]map[string]any) []string {
	var out []string
	for k := range v {
		out = append(out, k)
	}
	return sortStrings(out)
}

func sortStrings(s []string) []string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}

var localNames = []string{"calculate", "get_time", "get_weather", "media", "timer"}

func TestTheCoordinatorAlsoListsPixiesLocalToolsWithTheirOwnSchemas(t *testing.T) {
	r := lrig(false)
	got := listed(t, r, r.coordinator)
	want := sortStrings(append(append([]string{}, localNames...), "list_workers", "message_worker", "start_worker", "stop_worker"))
	if !reflect.DeepEqual(names(got), want) {
		t.Fatal(names(got))
	}
	for _, lt := range r.local {
		entry := got[lt.Name()]
		var schema any
		if err := json.Unmarshal(lt.Parameters(), &schema); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(entry["inputSchema"], schema) {
			t.Errorf("%s: the schema is not the voice tool's: %v", lt.Name(), entry["inputSchema"])
		}
		if entry["description"] != lt.Description() {
			t.Errorf("%s: description %v", lt.Name(), entry["description"])
		}
	}
}

func TestAWorkerNeverSeesTheLocalTools(t *testing.T) {
	r := lrig(false)
	if got := names(listed(t, r, r.worker)); !reflect.DeepEqual(got, []string{"ask_coordinator", "report_blocked", "report_progress", "report_result"}) {
		t.Fatal(got)
	}
}

func TestAWorkerCannotCallTheLocalTools(t *testing.T) {
	r := lrig(false)
	for _, name := range localNames {
		v := rpc(t, r.app, r.worker, toolCall(name, map[string]any{"action": "start", "duration_seconds": 60, "expression": "1+1"}))
		if get(v, "error", "code") != -32602.0 || !strings.HasPrefix(get(v, "error", "message").(string), "Unknown tool: ") {
			t.Fatalf("%s: %s", name, toJSON(v))
		}
	}
	if _, ok := r.rt.tryRecv(); ok {
		t.Fatal("a refused call reached the app")
	}
	if got := r.alarm.got(); len(got) != 0 {
		t.Fatal(got)
	}
	r.clock.Advance(time.Hour)
	if got := r.alarm.got(); len(got) != 0 {
		t.Fatal("a worker's timer was set:", got)
	}
}

// callOK is a successful tools/call's text, decoded.
func callOK(t *testing.T, r localRig, name string, args any) map[string]any {
	t.Helper()
	v := rpc(t, r.app, r.coordinator, toolCall(name, args))
	if get(v, "result", "isError") != false {
		t.Fatalf("%s: %s", name, toJSON(v))
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(get(v, "result", "content", 0, "text").(string)), &out); err != nil {
		t.Fatalf("%s: %s", name, toJSON(v))
	}
	return out
}

func TestEachLocalToolAnswersTheCoordinatorOverMcp(t *testing.T) {
	r := lrig(false)
	if got := callOK(t, r, "calculate", map[string]any{"expression": "12*(3+4)"}); got["result"] != "84" {
		t.Fatal(got)
	}
	if got := callOK(t, r, "get_time", map[string]any{}); !strings.Contains(toJSON(got), "3:04") {
		t.Fatal(got)
	}
	if got := callOK(t, r, "get_weather", map[string]any{}); !strings.HasPrefix(got["summary"].(string), "Paris, France: 18°C") {
		t.Fatal(got)
	}
	if got := callOK(t, r, "media", map[string]any{"action": "pause"}); got["done"] != "Paused." {
		t.Fatal(got)
	}
	if acts := r.player.Acts(); len(acts) != 1 || acts[0].Kind != tools.Pause {
		t.Fatal(acts)
	}
	if got := callOK(t, r, "timer", map[string]any{"action": "start", "duration_seconds": 300, "label": "tea"}); got["label"] != "tea" || got["started"] != "5 minutes" {
		t.Fatal(got)
	}
	if got := callOK(t, r, "timer", map[string]any{"action": "list"}); !strings.Contains(toJSON(got), `"label":"tea"`) {
		t.Fatal(got)
	}
	if _, ok := r.rt.tryRecv(); ok {
		t.Fatal("a local tool went through the agent runtime")
	}
}

func TestATimerTheCoordinatorSetsRingsThroughTheAlarm(t *testing.T) {
	r := lrig(false)
	callOK(t, r, "timer", map[string]any{"action": "start", "duration_seconds": 90, "label": "pasta"})
	r.clock.Advance(89 * time.Second)
	if got := r.alarm.got(); len(got) != 0 {
		t.Fatal("early:", got)
	}
	r.clock.Advance(time.Second)
	if got := r.alarm.got(); !reflect.DeepEqual(got, []string{"pasta (1 minute 30 seconds)"}) {
		t.Fatal(got)
	}
}

func TestALocalToolsInBandErrorIsAToolError(t *testing.T) {
	r := lrig(false)
	v := rpc(t, r.app, r.coordinator, toolCall("timer", map[string]any{"action": "cancel"}))
	if get(v, "result", "isError") != true || !strings.Contains(get(v, "result", "content", 0, "text").(string), "no timers are running") {
		t.Fatal(toJSON(v))
	}
}

func TestWithWorkersOffTheCoordinatorHasOnlyTheLocalTools(t *testing.T) {
	r := lrig(true)
	if got := names(listed(t, r, r.coordinator)); !reflect.DeepEqual(got, localNames) {
		t.Fatal(got)
	}
	v := rpc(t, r.app, r.coordinator, toolCall("list_workers", map[string]any{}))
	if get(v, "error", "code") != -32602.0 {
		t.Fatal(toJSON(v))
	}
	if _, ok := r.rt.tryRecv(); ok {
		t.Fatal("a hidden tool reached the app")
	}
	if got := callOK(t, r, "calculate", map[string]any{"expression": "2+2"}); got["result"] != "4" {
		t.Fatal(got)
	}
}
