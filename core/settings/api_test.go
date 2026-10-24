package settings

// Ported from ai-mascot src-tauri/src/settings/commands.rs (6 tests: 2 ported), mod.rs (2 tests:
// 1 ported) and view.rs (5 tests: 4 ported as API tests).
//
// Omitted, with reasons:
//   - commands.rs only_the_settings_window_may_call_settings_commands: Tauri window labels;
//     dp_call has no per-window caller identity (only the app process calls it).
//   - commands.rs secrets_use_the_providers_env_secrets_knows: checks voice_core's provider →
//     env var map, which P2 ports in core/voice (env secrets).
//   - commands.rs settings_commands_are_declared_and_granted_to_the_settings_window_only and
//     the_mascot_and_canvas_windows_get_nothing_new: Tauri capability files; no equivalent.
//   - mod.rs the_page_listens_for_the_event_rust_emits: the web page's event name; the Swift
//     window (S4) reads snapshots from dp_call instead.
//   - view.rs notify_does_nothing_while_the_window_is_closed: Go has no view; P14 emits
//     `settings.changed` and Swift decides whether to fetch.

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
)

// keys is where each provider's API key comes from, by provider registry name.
type keys map[string]string

func (k keys) Source(provider string) string { return k[provider] }

func TestSecretsReportFoundOrMissingAndTheSource(t *testing.T) {
	want := Secrets{OpenAI: KeyFound, Gemini: KeyMissing, Sources: KeySources{OpenAI: "keychain"}}
	if got := SecretStatus(keys{"openai-realtime": "keychain"}); got != want {
		t.Fatal(got)
	}
	want = Secrets{OpenAI: KeyMissing, Gemini: KeyFound, Sources: KeySources{Gemini: "dotenv"}}
	if got := SecretStatus(keys{"gemini-live": "dotenv"}); got != want {
		t.Fatal(got)
	}
	want0 := map[string]any{"openai": "missing", "gemini": "missing", "sources": map[string]any{"openai": "", "gemini": ""}}
	if got := mustJSON(t, SecretStatus(keys{})); !reflect.DeepEqual(got, want0) {
		t.Fatal(got)
	}
	b, _ := json.Marshal(SecretStatus(keys{"openai-realtime": "env", "gemini-live": "keychain"}))
	if string(b) != `{"openai":"found","gemini":"found","sources":{"openai":"env","gemini":"keychain"}}` {
		t.Fatal(string(b))
	}
	if SecretStatus(nil) != (Secrets{OpenAI: KeyMissing, Gemini: KeyMissing}) {
		t.Fatal("nil source")
	}
}

func TestTheSettingsFileIsRevealedThePlatformWay(t *testing.T) {
	file := "/data/settings.json"
	for _, tc := range []struct {
		p    Platform
		prog string
		args []string
	}{
		{Macos, "open", []string{"-R", "/data/settings.json"}},
		{Windows, "explorer", []string{"/select,/data/settings.json"}},
		{Linux, "xdg-open", []string{"/data"}},
	} {
		prog, args := RevealCommand(tc.p, file)
		if prog != tc.prog || !reflect.DeepEqual(args, tc.args) {
			t.Fatal(tc.p, prog, args)
		}
	}
}

// fakeRegistrar is the bridge's registry, in memory.
type fakeRegistrar map[string]func(json.RawMessage) (any, error)

func (f fakeRegistrar) Register(method string, fn func(json.RawMessage) (any, error)) { f[method] = fn }

func (f fakeRegistrar) call(t *testing.T, method, args string) map[string]any {
	t.Helper()
	fn, ok := f[method]
	if !ok {
		t.Fatalf("%s not registered", method)
	}
	out, err := fn(json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return mustJSON(t, out)
}

type apiFixture struct {
	api      *API
	store    *memStore
	rec      *recorder
	restarts int
	ran      [][]string
}

func newAPIFixture(t *testing.T) *apiFixture {
	t.Helper()
	f := &apiFixture{store: newMemStore(Default()), rec: &recorder{}}
	base := testEnv(t)
	base.Sources.Inputs, base.Sources.Outputs = nil, nil
	f.api = NewAPI(APIDeps{
		Store:        f.store,
		Applier:      f.rec,
		Env:          func() Env { return base },
		Secrets:      keys{"openai-realtime": "env"},
		SettingsPath: "/data/settings.json",
		Platform:     Macos,
		Restart:      func() { f.restarts++ },
		Run: func(program string, args []string) error {
			f.ran = append(f.ran, append([]string{program}, args...))
			return nil
		},
	})
	return f
}

func TestDeviceListsAreReadOnEachCallWithTheSavedDeviceKept(t *testing.T) {
	f := newAPIFixture(t)
	gone := "gone"
	f.store.now.Voice.OutputDevice = &gone
	f.api.SetChoices("voice.inputDevice", []Pair{{"uid-Yeti", "Yeti"}})
	f.api.SetChoices("voice.outputDevice", []Pair{{"uid-MacBook Pro Speakers", "MacBook Pro Speakers"}})
	src := f.api.Env().Sources
	if !reflect.DeepEqual(src.Inputs, []Pair{{"uid-Yeti", "Yeti"}}) {
		t.Fatal(src.Inputs)
	}
	if !reflect.DeepEqual(src.Outputs, []Pair{{"uid-MacBook Pro Speakers", "MacBook Pro Speakers"}, {"gone", MissingSpeaker}}) {
		t.Fatal(src.Outputs)
	}
	f.api.SetChoices("voice.inputDevice", nil)
	f.api.SetChoices("voice.outputDevice", nil)
	yeti := "yeti"
	f.store.now.Voice.InputDevice = &yeti
	if got := f.api.Env().Sources.Inputs; !reflect.DeepEqual(got, []Pair{{"yeti", MissingMic}}) {
		t.Fatal(got)
	}
	if err := f.api.SetChoices("pack", nil); err == nil {
		t.Fatal("only device fields take choices")
	}
}

func TestReloadShowsAHandEditOnceAndNothingWhenUnchanged(t *testing.T) {
	f := newAPIFixture(t)
	if _, changed := f.api.Reload(); changed {
		t.Fatal("nothing changed on disk")
	}
	edited := Default()
	edited.Scale = 6
	f.store.setFile(edited)
	snap, changed := f.api.Reload()
	if !changed || snap.Values["scale"] != json.Number("6") {
		t.Fatal(changed, snap.Values["scale"])
	}
	if _, changed := f.api.Reload(); changed {
		t.Fatal("a second focus with no edit is not news")
	}
}

func TestAGoodSetReturnsTheNewSnapshot(t *testing.T) {
	f := newAPIFixture(t)
	before := f.api.Snapshot().Seq
	snap := f.api.Set("scale", raw(`4`))
	if snap.SetError != nil || snap.Values["scale"] != json.Number("4") || snap.Seq <= before {
		t.Fatalf("%+v", snap)
	}
}

func TestARefusedSetCarriesItsErrorsAndAMergedHandEdit(t *testing.T) {
	f := newAPIFixture(t)
	snap := f.api.Set("scale", raw(`999`))
	if snap.SetError == nil || snap.SetError.Kind != SetInvalid || snap.SetError.Errors[0].Path != "scale" {
		t.Fatalf("%+v", snap.SetError)
	}
	edited := Default()
	edited.Scale = 2
	f.store.setFile(edited)
	snap = f.api.Set("scale", raw(`999`))
	if snap.SetError == nil || snap.Values["scale"] != json.Number("2") {
		t.Fatal("the merged hand edit is shown", snap.Values["scale"])
	}
}

func TestSnapshotPicksUpHandEdits(t *testing.T) {
	f := newAPIFixture(t)
	edited := Default()
	edited.Pack = "dino"
	f.store.setFile(edited)
	if got := f.api.Snapshot().Values["pack"]; got != "dino" {
		t.Fatal(got)
	}
}

func TestRegisteredMethodsSpeakTheContract(t *testing.T) {
	f := newAPIFixture(t)
	r := fakeRegistrar{}
	f.api.Register(r)
	want := []string{"settings.choices", "settings.openFile", "settings.reload", "settings.restart", "settings.search", "settings.secrets", "settings.set", "settings.snapshot"}
	var got []string
	for m := range r {
		got = append(got, m)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}

	snap := r.call(t, "settings.snapshot", `{}`)
	for _, k := range []string{"platform", "values", "fields", "restartPending", "fileError", "warning", "seq"} {
		if _, ok := snap[k]; !ok {
			t.Fatal("missing", k)
		}
	}
	if snap["platform"] != "macos" {
		t.Fatal(snap["platform"])
	}
	if _, ok := snap["setError"]; ok {
		t.Fatal("setError only after a refused set")
	}

	set := r.call(t, "settings.set", `{"field":"agent.workers.enabled","value":true}`)
	if !reflect.DeepEqual(set["restartPending"], []any{"agent.workers.enabled"}) {
		t.Fatal(set["restartPending"])
	}
	bad := r.call(t, "settings.set", `{"field":"scale","value":0}`)
	se := bad["setError"].(map[string]any)
	if se["kind"] != "invalid" || se["errors"].([]any)[0].(map[string]any)["message"] != RangeMessage(1, 16) {
		t.Fatal(se)
	}
	if _, err := r["settings.set"](json.RawMessage(`{"value":1}`)); err == nil {
		t.Fatal("a set without a field is an error")
	}

	if reload := r.call(t, "settings.reload", `{}`); reload["seq"] == nil {
		t.Fatal(reload)
	}

	sec := r.call(t, "settings.secrets", `{}`)
	wantSec := map[string]any{"openai": "found", "gemini": "missing", "sources": map[string]any{"openai": "env", "gemini": ""}}
	if !reflect.DeepEqual(sec, wantSec) {
		t.Fatal(sec)
	}

	r.call(t, "settings.choices", `{"field":"voice.inputDevice","choices":[{"value":"USB-1","label":"Yeti"}]}`)
	snap = r.call(t, "settings.snapshot", `{}`)
	var mic map[string]any
	for _, fv := range snap["fields"].([]any) {
		if fv.(map[string]any)["path"] == "voice.inputDevice" {
			mic = fv.(map[string]any)
		}
	}
	opts := mic["kind"].(map[string]any)["options"].([]any)
	if len(opts) != 2 || opts[1].(map[string]any)["label"] != "Yeti" {
		t.Fatal(opts)
	}
	if _, err := r["settings.choices"](json.RawMessage(`{"field":"pack","choices":[]}`)); err == nil {
		t.Fatal("pack choices come from the packs")
	}

	r.call(t, "settings.openFile", `{}`)
	if !reflect.DeepEqual(f.ran, [][]string{{"open", "-R", "/data/settings.json"}}) {
		t.Fatal(f.ran)
	}
	r.call(t, "settings.restart", `{}`)
	if f.restarts != 1 {
		t.Fatal(f.restarts)
	}
}

func TestOpenFileFailureIsAPlainMessage(t *testing.T) {
	f := newAPIFixture(t)
	f.api.deps.Run = func(string, []string) error { return errors.New("exec: not found") }
	if err := f.api.OpenFile(); err == nil || err.Error() != "Couldn't open the settings file" {
		t.Fatal(err)
	}
}

func TestTheAPIIsSafeForConcurrentCalls(t *testing.T) {
	f := newAPIFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			f.api.Set("scale", raw([]string{`4`, `6`}[i%2]))
			f.api.Snapshot()
			f.api.SetChoices("voice.inputDevice", []Pair{{"a", "A"}})
		}(i)
	}
	wg.Wait()
}
