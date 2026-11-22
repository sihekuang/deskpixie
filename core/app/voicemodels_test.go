package app

// The voice model lists in the app (#477): loaded into <app data>/voice/models.json from the
// provider's API in the background, shown in Settings.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// fakeLists stands in for the providers' APIs: each provider answers with its models (or err).
type fakeLists struct {
	mu     sync.Mutex
	models map[string][]string
	err    error
	calls  []string // provider per call
	keys   []string
}

type fakeList struct {
	f        *fakeLists
	provider string
}

func (l fakeList) ListModels(_ context.Context, key string) ([]string, error) {
	l.f.mu.Lock()
	defer l.f.mu.Unlock()
	l.f.calls = append(l.f.calls, l.provider)
	l.f.keys = append(l.f.keys, key)
	return l.f.models[l.provider], l.f.err
}

func (f *fakeLists) catalog(provider string) voice.ModelCatalog { return fakeList{f, provider} }

func (f *fakeLists) callsFor(provider string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.calls {
		if p == provider {
			n++
		}
	}
	return n
}

func withLists(f *fakeLists) harnessOpt {
	return func(d *Deps, h *harness) { d.ModelCatalog = f.catalog }
}

func withKeys(values map[string]string) harnessOpt {
	return func(d *Deps, h *harness) {
		d.Secrets = voice.NewEnvSecretsWithLookup(nil, func(k string) (string, bool) { v, ok := values[k]; return v, ok })
	}
}

// fieldView is one field of a snapshot, as far as these tests read it.
type fieldView struct {
	Path string `json:"path"`
	Help string `json:"help"`
	Kind struct {
		Options []struct{ Value, Label string } `json:"options"`
	} `json:"kind"`
}

func (h *harness) field(method, path string) fieldView {
	h.t.Helper()
	var s struct {
		Fields []fieldView `json:"fields"`
	}
	if err := json.Unmarshal(h.call(method, nil), &s); err != nil {
		h.t.Fatal(err)
	}
	for _, f := range s.Fields {
		if f.Path == path {
			return f
		}
	}
	h.t.Fatalf("no field %s", path)
	return fieldView{}
}

func optionValues(f fieldView) []string {
	var out []string
	for _, o := range f.Kind.Options {
		out = append(out, o.Value)
	}
	return out
}

func (h *harness) settingsChanged() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, e := range h.events {
		if e["kind"] == "settings.changed" {
			n++
		}
	}
	return n
}

// A snapshot refreshes the current provider's list; when it lands, settings.changed tells the
// window, and the next snapshot shows it and keeps it in <app data>/voice/models.json.
func TestASnapshotLoadsTheCurrentProvidersModels(t *testing.T) {
	f := &fakeLists{models: map[string][]string{settings.ProviderOpenAI: {"gpt-realtime", "gpt-realtime-3"}}}
	h := newHarness(t, withLists(f), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	h.call("settings.snapshot", nil)
	waitFor(t, "settings.changed", func() bool { return h.settingsChanged() > 0 })
	got := h.field("settings.snapshot", "voice.openaiModel")
	if !slices.Equal(optionValues(got), []string{"gpt-realtime", "gpt-realtime-3"}) || got.Kind.Options[0].Label != "GPT Realtime" {
		t.Fatal(got.Kind.Options)
	}
	if f.callsFor(settings.ProviderOpenAI) != 1 || f.callsFor(settings.ProviderGemini) != 0 || f.keys[0] != "sk-test" {
		t.Fatal(f.calls)
	}
	b, err := os.ReadFile(filepath.Join(h.dir, "voice", "models.json"))
	if err != nil || !strings.Contains(string(b), "gpt-realtime-3") || strings.Contains(string(b), "sk-test") {
		t.Fatal(string(b), err)
	}
}

func TestAProviderChangeLoadsTheNewProvidersModels(t *testing.T) {
	f := &fakeLists{models: map[string][]string{settings.ProviderGemini: {"gemini-3.8-live", "gemini-4-live"}}}
	h := newHarness(t, withLists(f), withKeys(map[string]string{"GEMINI_API_KEY": "g"}))
	h.call("settings.set", map[string]any{"field": "voice.provider", "value": settings.ProviderGemini})
	waitFor(t, "the Gemini list", func() bool { return f.callsFor(settings.ProviderGemini) == 1 })
	waitFor(t, "the list in the snapshot", func() bool {
		return slices.Contains(optionValues(h.field("settings.snapshot", "voice.geminiModel")), "gemini-4-live")
	})
}

// A failed load says so in the model's help: the reason, and which list shows.
func TestAFailedLoadIsNotedInTheModelsHelp(t *testing.T) {
	f := &fakeLists{err: errors.New("openai models: http 401: bad key")}
	h := newHarness(t, withLists(f), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	h.call("settings.snapshot", nil)
	waitFor(t, "the note", func() bool {
		return strings.HasSuffix(h.field("settings.snapshot", "voice.openaiModel").Help,
			"Couldn't load the list from OpenAI (openai models: http 401: bad key); showing the built-in list.")
	})
}

func TestWithoutAKeyNothingIsFetchedAndTheHelpSaysWhy(t *testing.T) {
	f := &fakeLists{}
	h := newHarness(t, withLists(f))
	h.call("settings.snapshot", nil)
	if help := h.field("settings.snapshot", "voice.openaiModel").Help; !strings.Contains(help, "(no API key); showing the built-in list.") {
		t.Fatal(help)
	}
	if len(f.calls) != 0 {
		t.Fatal(f.calls)
	}
}

// A key saved in Settings loads the list at once, whatever the last load said.
func TestASavedKeyLoadsTheListAgain(t *testing.T) {
	f := &fakeLists{models: map[string][]string{settings.ProviderOpenAI: {"gpt-realtime"}, settings.ProviderOpenAILive: {"gpt-live-1"}}}
	h := newHarness(t, withLists(f))
	h.call("settings.snapshot", nil) // no key yet: nothing fetched
	h.call("secrets.keychain", map[string]any{"values": map[string]string{"OPENAI_API_KEY": "sk-new"}})
	waitFor(t, "both OpenAI lists", func() bool {
		return f.callsFor(settings.ProviderOpenAI) == 1 && f.callsFor(settings.ProviderOpenAILive) == 1
	})
	if f.callsFor(settings.ProviderGemini) != 0 {
		t.Fatal("Gemini's key didn't change:", f.calls)
	}
	h.call("secrets.keychain", map[string]any{"values": map[string]string{"OPENAI_API_KEY": "sk-new"}})
	h.app.models.Wait()
	if f.callsFor(settings.ProviderOpenAI) != 1 {
		t.Fatal("the same key again loads nothing:", f.calls)
	}
}

func TestTheRealCatalogsAreTheAdapters(t *testing.T) {
	a := &App{deps: Deps{Providers: Providers()}}
	for _, p := range ProviderNames() {
		if a.catalog(p) == nil {
			t.Fatal(p, "lists no models")
		}
	}
	if a.catalog("nope") != nil {
		t.Fatal("an unknown provider lists nothing")
	}
}
