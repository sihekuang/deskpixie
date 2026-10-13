package bridge

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

func callJSON(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("bad envelope %q: %v", b, err)
	}
	if len(m) != 1 {
		t.Fatalf("envelope must have exactly one key: %s", b)
	}
	return m
}

func TestCallUnknownMethod(t *testing.T) {
	e := newEngine(t, Options{})
	got := string(e.Call("nope.nothing", []byte(`{}`)))
	if got != `{"error":"unknown method nope.nothing"}` {
		t.Fatalf("got %s", got)
	}
}

func TestCallOkWrapsResult(t *testing.T) {
	e := newEngine(t, Options{})
	e.Register("t.echo", func(args json.RawMessage) (any, error) {
		var in struct{ N int }
		if err := json.Unmarshal(args, &in); err != nil {
			return nil, err
		}
		return map[string]int{"n": in.N + 1}, nil
	})
	if got := string(e.Call("t.echo", []byte(`{"N":41}`))); got != `{"ok":{"n":42}}` {
		t.Fatalf("got %s", got)
	}
}

func TestCallErrorWrapsMessage(t *testing.T) {
	e := newEngine(t, Options{})
	e.Register("t.fail", func(json.RawMessage) (any, error) { return nil, errors.New(`bad "field"`) })
	m := callJSON(t, e.Call("t.fail", nil))
	var msg string
	if err := json.Unmarshal(m["error"], &msg); err != nil || msg != `bad "field"` {
		t.Fatalf("error = %s (%v)", m["error"], err)
	}
}

func TestCallNilResultIsEmptyObject(t *testing.T) {
	e := newEngine(t, Options{})
	e.Register("t.nil", func(json.RawMessage) (any, error) { return nil, nil })
	if got := string(e.Call("t.nil", nil)); got != `{"ok":{}}` {
		t.Fatalf("got %s", got)
	}
}

func TestCallEmptyArgsBecomeEmptyObject(t *testing.T) {
	e := newEngine(t, Options{})
	var seen string
	e.Register("t.args", func(a json.RawMessage) (any, error) { seen = string(a); return nil, nil })
	e.Call("t.args", nil)
	if seen != `{}` {
		t.Fatalf("args = %q", seen)
	}
	e.Call("t.args", []byte("  "))
	if seen != `{}` {
		t.Fatalf("blank args = %q", seen)
	}
}

func TestCallInvalidArgsJSON(t *testing.T) {
	e := newEngine(t, Options{})
	called := false
	e.Register("t.x", func(json.RawMessage) (any, error) { called = true; return nil, nil })
	m := callJSON(t, e.Call("t.x", []byte(`{not json`)))
	if called {
		t.Fatal("handler ran on invalid JSON")
	}
	if !strings.Contains(string(m["error"]), "invalid args JSON") {
		t.Fatalf("got %s", m["error"])
	}
}

func TestCallHandlerPanicBecomesError(t *testing.T) {
	e := newEngine(t, Options{})
	e.Register("t.panic", func(json.RawMessage) (any, error) { panic("boom") })
	m := callJSON(t, e.Call("t.panic", nil))
	if !strings.Contains(string(m["error"]), "boom") {
		t.Fatalf("got %s", m["error"])
	}
}

func TestCallUnencodableResultIsError(t *testing.T) {
	e := newEngine(t, Options{})
	e.Register("t.chan", func(json.RawMessage) (any, error) { return make(chan int), nil })
	if _, ok := callJSON(t, e.Call("t.chan", nil))["error"]; !ok {
		t.Fatal("want error")
	}
}

func TestRegisterReplacesExisting(t *testing.T) {
	e := newEngine(t, Options{})
	e.Register("app.state", func(json.RawMessage) (any, error) { return "mine", nil })
	if got := string(e.Call("app.state", nil)); got != `{"ok":"mine"}` {
		t.Fatalf("got %s", got)
	}
}

func TestPackageRegisterReachesEveryEngine(t *testing.T) {
	Register("t.pkg", func(json.RawMessage) (any, error) { return 1, nil })
	t.Cleanup(func() { unregisterPackage("t.pkg") })
	e := newEngine(t, Options{})
	if got := string(e.Call("t.pkg", nil)); got != `{"ok":1}` {
		t.Fatalf("got %s", got)
	}
	// Engine registrations win over package ones.
	e.Register("t.pkg", func(json.RawMessage) (any, error) { return 2, nil })
	if got := string(e.Call("t.pkg", nil)); got != `{"ok":2}` {
		t.Fatalf("got %s", got)
	}
}

func TestCallConcurrentWithRegister(t *testing.T) {
	e := newEngine(t, Options{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); e.Register("t.c", func(json.RawMessage) (any, error) { return nil, nil }) }()
		go func() { defer wg.Done(); e.Call("t.c", nil) }()
	}
	wg.Wait()
}

func appState(t *testing.T, e *Engine) AppState {
	t.Helper()
	m := callJSON(t, e.Call("app.state", nil))
	var s AppState
	if err := json.Unmarshal(m["ok"], &s); err != nil {
		t.Fatalf("app.state %s: %v", m["ok"], err)
	}
	return s
}

func TestAppStateBeforeConfigure(t *testing.T) {
	e := newEngine(t, Options{})
	s := appState(t, e)
	if s.State != "idle" || s.Scale != DefaultScale || s.Pack != "" || len(s.StatusLines) != 0 {
		t.Fatalf("got %+v", s)
	}
	if s.Packs == nil || s.StatusLines == nil || s.CanvasRecent == nil || s.Agent.Jobs == nil {
		t.Fatalf("lists must encode as [] not null: %+v", s)
	}
}

func TestAppStateReportsPacksStateAndStatus(t *testing.T) {
	e := newEngine(t, Options{})
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	s := appState(t, e)
	if s.Pack != "bit" {
		t.Fatalf("pack = %q", s.Pack)
	}
	found := false
	for _, p := range s.Packs {
		if p.ID == "bit" && p.Name != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("packs = %+v", s.Packs)
	}

	e.emit(voice.UIEvent{Kind: "state", State: "thinking"})
	e.emit(voice.UIEvent{Kind: "status", Text: "Voice: connection lost"})
	s = appState(t, e)
	if s.State != "thinking" || len(s.StatusLines) != 1 || s.StatusLines[0] != "Voice: connection lost" {
		t.Fatalf("got %+v", s)
	}
	e.emit(voice.UIEvent{Kind: "error", Text: "connect: refused"})
	if s = appState(t, e); len(s.StatusLines) != 1 || s.StatusLines[0] != "connect: refused" {
		t.Fatalf("got %+v", s.StatusLines)
	}
	// An empty status clears the line (Rust: set_status(None)).
	e.emit(voice.UIEvent{Kind: "status", Text: ""})
	if s = appState(t, e); len(s.StatusLines) != 0 {
		t.Fatalf("got %+v", s.StatusLines)
	}
}

// drainKinds pops every queued event and returns their kinds in order.
func drainKinds(t *testing.T, e *Engine) []string {
	t.Helper()
	var kinds []string
	for b := e.PollEvent(); b != nil; b = e.PollEvent() {
		kinds = append(kinds, decode(t, b).Kind)
	}
	return kinds
}

func count(kinds []string, k string) int {
	n := 0
	for _, x := range kinds {
		if x == k {
			n++
		}
	}
	return n
}

func TestTrayEventOnStateAndStatusChange(t *testing.T) {
	e := newEngine(t, Options{})
	drainKinds(t, e)
	e.emit(voice.UIEvent{Kind: "state", State: "listening"})
	if k := drainKinds(t, e); strings.Join(k, ",") != "state,tray" {
		t.Fatalf("got %v", k)
	}
	// Same state again: no tray refresh needed.
	e.emit(voice.UIEvent{Kind: "state", State: "listening"})
	if k := drainKinds(t, e); count(k, "tray") != 0 {
		t.Fatalf("got %v", k)
	}
	e.emit(voice.UIEvent{Kind: "status", Text: "Voice: model error"})
	if k := drainKinds(t, e); strings.Join(k, ",") != "status,tray" {
		t.Fatalf("got %v", k)
	}
	e.emit(voice.UIEvent{Kind: "status", Text: "Voice: model error"})
	if k := drainKinds(t, e); count(k, "tray") != 0 {
		t.Fatalf("got %v", k)
	}
	e.emit(voice.UIEvent{Kind: "error", Text: "boom"})
	if k := drainKinds(t, e); strings.Join(k, ",") != "error,tray" {
		t.Fatalf("got %v", k)
	}
	// Mouth and transcript never refresh the tray.
	e.emit(voice.UIEvent{Kind: "mouth", Level: 2})
	e.emit(voice.UIEvent{Kind: "transcript", Role: "user", Text: "hi"})
	if k := drainKinds(t, e); count(k, "tray") != 0 {
		t.Fatalf("got %v", k)
	}
}

func TestTrayEventOnConfigure(t *testing.T) {
	e := newEngine(t, Options{})
	drainKinds(t, e)
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	if k := drainKinds(t, e); count(k, "tray") == 0 {
		t.Fatalf("got %v", k)
	}
}

func TestTempAppActionSetsState(t *testing.T) {
	e := newEngine(t, Options{})
	drainKinds(t, e)
	if got := string(e.Call("app.action", []byte(`{"id":"state:happy"}`))); got != `{"ok":{}}` {
		t.Fatalf("got %s", got)
	}
	if k := drainKinds(t, e); strings.Join(k, ",") != "state,tray" {
		t.Fatalf("got %v", k)
	}
	if s := appState(t, e); s.State != "happy" {
		t.Fatalf("state = %q", s.State)
	}
	if _, ok := callJSON(t, e.Call("app.action", []byte(`{"id":"state:nope"}`)))["error"]; !ok {
		t.Fatal("unknown state must fail")
	}
	if _, ok := callJSON(t, e.Call("app.action", []byte(`{"id":"reload"}`)))["error"]; !ok {
		t.Fatal("unported action must fail until P14")
	}
}

func TestGlobalCall(t *testing.T) {
	Destroy()
	if got := string(Call("app.state", nil)); got != `{"error":"unknown method app.state"}` {
		t.Fatalf("before Init: %s", got)
	}
	Init()
	t.Cleanup(Destroy)
	if _, ok := callJSON(t, Call("app.state", nil))["ok"]; !ok {
		t.Fatal("app.state after Init")
	}
}
