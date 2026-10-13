package bridge

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/voice"
)

const mascots = "../../mascots"

func absMascots(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(mascots)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func cfgJSON(t *testing.T, extra string) []byte {
	t.Helper()
	b, _ := json.Marshal(absMascots(t))
	s := `{"packRoots":[` + string(b) + `]`
	if extra != "" {
		s += "," + extra
	}
	return []byte(s + "}")
}

func newEngine(t *testing.T, o Options) *Engine {
	t.Helper()
	if o.MetricsLogPath == "" {
		o.MetricsLogPath = filepath.Join(t.TempDir(), "logs", "metrics.log")
	}
	e := New(o)
	t.Cleanup(e.Close)
	return e
}

func decode(t *testing.T, b []byte) voice.UIEvent {
	t.Helper()
	var ev voice.UIEvent
	if err := json.Unmarshal(b, &ev); err != nil {
		t.Fatalf("bad event %q: %v", b, err)
	}
	return ev
}

// waitEvent polls until match returns true or 3 s pass; it returns every event seen.
func waitEvent(t *testing.T, e *Engine, match func(voice.UIEvent) bool) []voice.UIEvent {
	t.Helper()
	var seen []voice.UIEvent
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		b := e.PollEvent()
		if b == nil {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		ev := decode(t, b)
		seen = append(seen, ev)
		if match(ev) {
			return seen
		}
	}
	t.Fatalf("event not seen; got %+v", seen)
	return nil
}

func TestBridgeConfigureWithoutKey(t *testing.T) {
	e := newEngine(t, Options{})
	if err := e.Configure(cfgJSON(t, `"apiKey":""`)); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	e.Hotkey()
	seen := waitEvent(t, e, func(ev voice.UIEvent) bool { return ev.Kind == "state" && ev.State == "error" })
	var sawKeyError bool
	for _, ev := range seen {
		if ev.Kind == "error" && strings.Contains(ev.Text, "OPENAI_API_KEY") {
			sawKeyError = true
		}
	}
	if !sawKeyError {
		t.Errorf("no OPENAI_API_KEY error event in %+v", seen)
	}
}

func TestBridgeConfigureBadJSON(t *testing.T) {
	e := newEngine(t, Options{})
	err := e.Configure([]byte(`{"model":`))
	if !errors.Is(err, ErrBadJSON) {
		t.Fatalf("err = %v, want ErrBadJSON", err)
	}
	if e.LastError() == "" {
		t.Error("LastError empty")
	}
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if e.LastError() != "" {
		t.Errorf("LastError not cleared: %q", e.LastError())
	}
}

func TestBridgeConfigureNoPackIsBuildError(t *testing.T) {
	e := newEngine(t, Options{})
	err := e.Configure([]byte(`{"packRoots":["` + t.TempDir() + `"]}`))
	if err == nil || errors.Is(err, ErrBadJSON) || errors.Is(err, ErrNotInit) {
		t.Fatalf("err = %v, want a build error", err)
	}
	if e.LastError() == "" {
		t.Error("LastError empty")
	}
}

func TestBridgeConfigDefaults(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"apiKey":"k","personaPath":"/p","idleTimeoutSecs":0,"packRoots":["a"],"noisyRoom":true}`), &c); err != nil {
		t.Fatal(err)
	}
	c.applyDefaults()
	if c.Model != "gpt-realtime" || c.Voice != "marin" || c.IdleTimeoutSecs != 7 || c.Pack != "bit" {
		t.Errorf("defaults = %+v", c)
	}
	if c.APIKey != "k" || c.PersonaPath != "/p" || !c.NoisyRoom || len(c.PackRoots) != 1 {
		t.Errorf("camelCase keys not decoded: %+v", c)
	}
}

func TestBridgeReconfigureReplacesRuntime(t *testing.T) {
	e := newEngine(t, Options{})
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	first := e.rt.Load()
	if err := e.Configure(cfgJSON(t, `"pack":"dino"`)); err != nil {
		t.Fatal(err)
	}
	if e.rt.Load() == first || e.rt.Load() == nil {
		t.Error("runtime not replaced")
	}
	var v struct {
		Pack struct{ Name string } `json:"pack"`
	}
	if err := json.Unmarshal(e.PackJSON(), &v); err != nil || v.Pack.Name != "Dino" {
		t.Errorf("pack = %q, %v", v.Pack.Name, err)
	}
	// The replaced runtime still works.
	e.Hotkey()
	waitEvent(t, e, func(ev voice.UIEvent) bool { return ev.Kind == "state" && ev.State == "error" })
}

func TestBridgePushAudioNeverBlocks(t *testing.T) {
	e := newEngine(t, Options{})
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	frame := make([]int16, 960)
	start := time.Now()
	dropped := 0
	for i := 0; i < 10000; i++ {
		if !e.PushAudio(frame, time.Now().UnixNano()) {
			dropped++
		}
	}
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Fatalf("10 000 pushes took %v", el)
	}
	if got := e.rec.Snapshot().MicDrops; got != uint64(dropped) {
		t.Errorf("recorded drops = %d, want %d", got, dropped)
	}
}

func TestBridgeWithoutRuntimeIsSafe(t *testing.T) {
	e := newEngine(t, Options{})
	if e.PushAudio(make([]int16, 960), 0) {
		t.Error("push without a runtime reported success")
	}
	if n, flush := e.PullAudio(make([]int16, 10)); n != 0 || flush {
		t.Errorf("pull = %d %v", n, flush)
	}
	e.Hotkey()
	e.ReportPlayback(1, 2, 0.5, 3)
	e.MicFailed("denied")
	if e.PackJSON() != nil {
		t.Error("PackJSON before Configure")
	}
	if e.rec.Snapshot().Underruns != 3 {
		t.Error("underruns not recorded")
	}
}

func TestBridgePackJSON(t *testing.T) {
	e := newEngine(t, Options{})
	if err := e.Configure(cfgJSON(t, `"pack":"nope"`)); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Pack      map[string]any `json:"pack"`
		SheetPath string         `json:"sheetPath"`
	}
	if err := json.Unmarshal(e.PackJSON(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Pack["name"] != "Bit" {
		t.Errorf("name = %v", v.Pack["name"])
	}
	if !filepath.IsAbs(v.SheetPath) {
		t.Errorf("sheetPath %q not absolute", v.SheetPath)
	}
	if _, err := os.Stat(v.SheetPath); err != nil {
		t.Errorf("sheetPath: %v", err)
	}
	states := v.Pack["states"].(map[string]any)
	for _, s := range []string{"idle", "listening", "thinking", "delegating", "happy", "error"} {
		st, ok := states[s].(map[string]any)
		if !ok {
			t.Fatalf("state %s missing", s)
		}
		for _, k := range []string{"row", "frames", "loop", "bob", "faceDy"} {
			if _, ok := st[k]; !ok {
				t.Errorf("state %s missing %s", s, k)
			}
		}
		if len(st["faceDy"].([]any)) != int(st["frames"].(float64)) {
			t.Errorf("state %s faceDy length", s)
		}
	}
}

func TestBridgeEventQueueDropsOldestMouthFirst(t *testing.T) {
	e := newEngine(t, Options{})
	e.emit(voice.UIEvent{Kind: "state", State: "listening"})
	e.emit(voice.UIEvent{Kind: "mouth", Level: 1})
	for i := 0; i < eventQueueCap-2; i++ {
		e.emit(voice.UIEvent{Kind: "status", Text: "s"})
	}
	// Full. The next push drops the only mouth event, the next one the oldest (state).
	e.emit(voice.UIEvent{Kind: "transcript", Text: "a"})
	e.emit(voice.UIEvent{Kind: "transcript", Text: "b"})
	var all []voice.UIEvent
	for b := e.PollEvent(); b != nil; b = e.PollEvent() {
		all = append(all, decode(t, b))
	}
	if len(all) != eventQueueCap {
		t.Fatalf("len = %d", len(all))
	}
	for _, ev := range all {
		if ev.Kind == "mouth" || ev.Kind == "state" {
			t.Errorf("%s event should have been dropped", ev.Kind)
		}
	}
	if all[len(all)-2].Text != "a" || all[len(all)-1].Text != "b" {
		t.Errorf("tail = %+v", all[len(all)-2:])
	}
	if e.PollEvent() != nil {
		t.Error("queue not empty")
	}
}

func TestBridgeEmitNeverBlocks(t *testing.T) {
	e := newEngine(t, Options{})
	start := time.Now()
	for i := 0; i < 100000; i++ {
		e.emit(voice.UIEvent{Kind: "mouth", Level: i % 4})
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("emit took %v", el)
	}
}

func TestBridgeMicSentRecordsLatency(t *testing.T) {
	e := newEngine(t, Options{})
	e.onMicSent(time.Now().Add(-5 * time.Millisecond))
	s := e.rec.Snapshot()
	if s.MicSamples != 1 || s.MicToSendP50Ms < 5 {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestBridgeMetricsEventAndLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nested", "DeskPixie", "metrics.log")
	e := newEngine(t, Options{MetricsInterval: 20 * time.Millisecond, MetricsLogPath: logPath})
	seen := waitEvent(t, e, func(ev voice.UIEvent) bool { return ev.Kind == "metrics" })
	var snap map[string]any
	if err := json.Unmarshal([]byte(seen[len(seen)-1].Text), &snap); err != nil {
		t.Fatalf("metrics text not JSON: %v", err)
	}
	if _, ok := snap["maxGcPauseMs"]; !ok {
		t.Errorf("snapshot = %v", snap)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		f, err := os.Open(logPath)
		if err == nil {
			sc := bufio.NewScanner(f)
			ok := sc.Scan() && strings.Contains(sc.Text(), "micToSendP50Ms")
			f.Close()
			if ok {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no metrics line in %s: %v", logPath, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestGlobalAPI(t *testing.T) {
	defaultOptions = Options{MetricsLogPath: filepath.Join(t.TempDir(), "metrics.log")}
	Destroy()
	if err := Configure(cfgJSON(t, "")); !errors.Is(err, ErrNotInit) {
		t.Fatalf("Configure before Init: %v", err)
	}
	if LastError() == "" {
		t.Error("LastError empty before Init")
	}
	Init()
	Init() // idempotent
	defer Destroy()
	if err := Configure([]byte("nope")); !errors.Is(err, ErrBadJSON) {
		t.Fatalf("bad JSON: %v", err)
	}
	if err := Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	if LastError() != "" {
		t.Errorf("LastError = %q", LastError())
	}
	if PackJSON() == nil {
		t.Error("PackJSON nil")
	}
	PushAudio(make([]int16, 960), 0)
	PullAudio(make([]int16, 960))
	ReportPlayback(0, 0, 0, 0)
	Hotkey()
	waitGlobal(t, func(ev voice.UIEvent) bool { return ev.Kind == "state" && ev.State == "error" })
	MicFailed("denied")
	Destroy()
	Destroy() // idempotent
	if PollEvent() != nil || PackJSON() != nil {
		t.Error("API live after Destroy")
	}
	if PushAudio(make([]int16, 1), 0) {
		t.Error("push after Destroy succeeded")
	}
}

func waitGlobal(t *testing.T, match func(voice.UIEvent) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b := PollEvent(); b != nil {
			if match(decode(t, b)) {
				return
			}
			continue
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("event not seen")
}
