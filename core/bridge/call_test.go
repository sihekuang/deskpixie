package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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

type codedErr struct{}

func (codedErr) Error() string { return "no" }
func (codedErr) Code() string  { return "refused" }

func TestCallErrorCarriesItsCode(t *testing.T) {
	e := newEngine(t, Options{})
	e.Register("t.refuse", func(json.RawMessage) (any, error) { return nil, fmt.Errorf("wrapped: %w", codedErr{}) })
	if got := string(e.Call("t.refuse", nil)); got != `{"error":"wrapped: no","code":"refused"}` {
		t.Fatalf("got %s", got)
	}
	e.Register("t.fail", func(json.RawMessage) (any, error) { return nil, errors.New("bad") })
	if got := string(e.Call("t.fail", nil)); got != `{"error":"bad"}` {
		t.Fatalf("no code: got %s", got)
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

func TestAppMethodsNeedConfigure(t *testing.T) {
	e := newEngine(t, Options{})
	if got := string(e.Call("app.state", nil)); got != `{"error":"unknown method app.state"}` {
		t.Fatalf("got %s", got)
	}
}

// Every method of the shared contract (and P1's and P14a's additions) is served once
// configured.
func TestConfigureRegistersTheContractMethods(t *testing.T) {
	e := newEngine(t, Options{})
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{
		"settings.snapshot", "settings.set", "settings.reload", "settings.secrets", "settings.choices",
		"settings.openFile", "settings.restart", "settings.hotkeyFailed",
		"app.state", "app.action", "pixie.click", "pixie.moved",
		"canvas.resolve", "canvas.geometry", "audio.deviceChanged", "audio.failed",
		"setup.programs",
	} {
		if strings.HasPrefix(string(e.Call(m, []byte(`{"bogus":`))), `{"error":"unknown method`) {
			t.Errorf("%s not registered", m)
		}
	}
}

func TestAppStateAfterConfigure(t *testing.T) {
	e := newEngine(t, Options{})
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	m := callJSON(t, e.Call("app.state", nil))
	var s struct {
		State, Pack string
		Scale       int
		Packs       []struct{ ID, Name string }
		StatusLines []string `json:"statusLines"`
	}
	if err := json.Unmarshal(m["ok"], &s); err != nil {
		t.Fatalf("app.state %s: %v", m["ok"], err)
	}
	if s.State != "idle" || s.Pack != "bit" || s.Scale != 8 || len(s.Packs) == 0 || s.StatusLines == nil {
		t.Fatalf("got %+v", s)
	}
}

func TestTrayEventOnConfigure(t *testing.T) {
	e := newEngine(t, Options{})
	drainKinds(t, e)
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	k := drainKinds(t, e)
	if count(k, "tray") == 0 || count(k, "devices") != 1 {
		t.Fatalf("got %v", k)
	}
}

func TestAppActionThroughDpCall(t *testing.T) {
	e := newEngine(t, Options{})
	if err := e.Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	drainKinds(t, e)
	if got := string(e.Call("app.action", []byte(`{"id":"state:thinking"}`))); got != `{"ok":{}}` {
		t.Fatalf("got %s", got)
	}
	if k := drainKinds(t, e); count(k, "state") != 1 || count(k, "tray") == 0 {
		t.Fatalf("got %v", k)
	}
	if _, ok := callJSON(t, e.Call("app.action", []byte(`{"id":"state:nope"}`)))["error"]; !ok {
		t.Fatal("unknown state must fail")
	}
	if got := string(e.Call("app.action", []byte(`{"id":"reload"}`))); got != `{"ok":{}}` {
		t.Fatalf("reload: %s", got)
	}
}

func TestGlobalCall(t *testing.T) {
	Destroy()
	if got := string(Call("app.state", nil)); got != `{"error":"unknown method app.state"}` {
		t.Fatalf("before Init: %s", got)
	}
	defaultOptions = Options{MetricsLogPath: filepath.Join(t.TempDir(), "metrics.log"), Secrets: noSecrets}
	t.Cleanup(func() { defaultOptions = Options{} })
	Init()
	t.Cleanup(Destroy)
	if err := Configure(cfgJSON(t, "")); err != nil {
		t.Fatal(err)
	}
	if _, ok := callJSON(t, Call("app.state", nil))["ok"]; !ok {
		t.Fatal("app.state after Configure")
	}
}
