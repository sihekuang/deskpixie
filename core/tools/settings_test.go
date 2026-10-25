package tools

import (
	"encoding/json"
	"testing"
)

// fakeWindow answers Find from found and records each Open (nil: Settings without a setting).
type fakeWindow struct {
	found   []SettingMatch
	queries []string
	opened  []*SettingMatch
}

func (f *fakeWindow) Find(q string) []SettingMatch {
	f.queries = append(f.queries, q)
	return f.found
}

func (f *fakeWindow) Open(m *SettingMatch) { f.opened = append(f.opened, m) }

func TestOpenSettingsShowsTheBestMatchAndNamesTheOthers(t *testing.T) {
	w := &fakeWindow{found: []SettingMatch{
		{"voice.inputDevice", "Microphone", "Voice"},
		{"voice.audio", "Audio", "Voice"},
		{"voice.noiseFilter", "Noise filter", "Voice"},
		{"voice.outputDevice", "Speaker", "Voice"},
		{"x", "Fifth", "Advanced"},
	}}
	got := call(t, OpenSettings{w}, `{"query":"  mic "}`)
	eq(t, got, `{"opened":true,"found":true,"setting":"Microphone","page":"Voice",
		"others":["Audio (Voice)","Noise filter (Voice)","Speaker (Voice)"]}`)
	if len(w.queries) != 1 || w.queries[0] != "mic" || len(w.opened) != 1 || w.opened[0].Path != "voice.inputDevice" {
		t.Fatal(w.queries, w.opened)
	}
}

func TestOpenSettingsWithOneMatchNamesNoOthers(t *testing.T) {
	w := &fakeWindow{found: []SettingMatch{{"extra:key.gemini", "Gemini key", "Voice"}}}
	eq(t, call(t, OpenSettings{w}, `{"query":"gemini key"}`), `{"opened":true,"found":true,"setting":"Gemini key","page":"Voice"}`)
}

func TestOpenSettingsWithNoMatchStillOpensIt(t *testing.T) {
	w := &fakeWindow{}
	got := call(t, OpenSettings{w}, `{"query":"zebra"}`)
	eq(t, got, `{"opened":true,"found":false,"note":"No setting matches; Settings is open."}`)
	if len(w.opened) != 1 || w.opened[0] != nil {
		t.Fatal(w.opened)
	}
}

func TestOpenSettingsWithoutAQueryJustOpensIt(t *testing.T) {
	for _, args := range []string{`{}`, `{"query":"  "}`, ``, `not json`, `{"query":5}`} {
		w := &fakeWindow{}
		eq(t, call(t, OpenSettings{w}, args), `{"opened":true}`)
		if len(w.queries) != 0 || len(w.opened) != 1 || w.opened[0] != nil {
			t.Fatal(args, w.queries, w.opened)
		}
	}
}

func TestOpenSettingsDescribesItself(t *testing.T) {
	o := OpenSettings{}
	if o.Name() != "open_settings" || o.Description() == "" || !validJSON(o.Parameters()) {
		t.Fatal(o.Name(), o.Description(), string(o.Parameters()))
	}
}

func validJSON(b []byte) bool {
	var v map[string]any
	return json.Unmarshal(b, &v) == nil
}
