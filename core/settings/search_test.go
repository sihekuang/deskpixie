package settings

import (
	"encoding/json"
	"strings"
	"testing"
)

func paths(hits []Hit) []string {
	out := []string{}
	for _, h := range hits {
		out = append(out, h.Path)
	}
	return out
}

func TestSearchFindsASettingByTheStartOfAWordInItsName(t *testing.T) {
	hits := Search("mic", Macos, Default())
	if len(hits) == 0 || hits[0].Path != "voice.inputDevice" {
		t.Fatal(paths(hits))
	}
	if hits[0].Section != SectionVoice || hits[0].Label != "Microphone" || hits[0].Help == "" {
		t.Fatal(hits[0])
	}
}

func TestSearchIgnoresCaseAndTheWordsOfAQuestion(t *testing.T) {
	for _, q := range []string{"Noise Filter", "where do I change the noise filter?", "NOISE"} {
		hits := Search(q, Macos, Default())
		if len(hits) == 0 || hits[0].Path != "voice.noiseFilter" {
			t.Fatal(q, paths(hits))
		}
	}
}

func TestSearchNeedsEveryWordAndRanksTheNameAboveTheHelp(t *testing.T) {
	// "chime" is only in voice.chimes' name; no setting has "chime" and "zebra".
	if hits := Search("chime zebra", Macos, Default()); len(hits) != 0 {
		t.Fatal(paths(hits))
	}
	// "fans" is only help text (the noise filter's); a name match comes first.
	hits := Search("speaker", Macos, Default())
	if len(hits) == 0 || hits[0].Path != "voice.outputDevice" {
		t.Fatal(paths(hits))
	}
	if hits := Search("fans", Macos, Default()); len(hits) != 1 || hits[0].Path != "voice.noiseFilter" {
		t.Fatal(paths(hits))
	}
}

func TestSearchFindsTheWindowsOtherRows(t *testing.T) {
	for q, want := range map[string]string{
		"gemini key":          "extra:key.gemini",
		"api key":             "extra:key.openai",
		"export logs":         "extra:exportLogs",
		"restart coordinator": "extra:restartCoordinator",
		"pix":                 "extra:pix",
		"reset position":      "extra:resetPosition",
	} {
		hits := Search(q, Macos, Default())
		found := false
		for _, h := range hits {
			found = found || h.Path == want
		}
		if !found {
			t.Fatal(q, paths(hits))
		}
	}
}

func TestEveryExtraIsOnAPageAndNamed(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range Extras {
		if e.ID == "" || e.Label == "" || e.Help == "" || seen[e.ID] || len(e.Platforms) == 0 {
			t.Fatal(e)
		}
		seen[e.ID] = true
		found := false
		for _, f := range Fields {
			found = found || f.Section == e.Section
		}
		if !found {
			t.Fatal("no page", e)
		}
	}
}

func TestSearchLeavesOutHiddenOtherPlatformAndAppWrittenFields(t *testing.T) {
	// GPT-Live's fields show only for GPT-Live.
	c := Default()
	c.Voice.Provider = ProviderGemini
	if strings.Contains(strings.Join(paths(Search("gpt-live delegation", Macos, c)), " "), "voice.liveDelegation") {
		t.Fatal("hidden field found")
	}
	c.Voice.Provider = ProviderOpenAILive
	if hits := Search("gpt-live delegation", Macos, c); len(hits) == 0 || hits[0].Path != "voice.liveDelegation" {
		t.Fatal(paths(hits))
	}
	// The noise filter is macOS only.
	for _, h := range Search("noise filter", Windows, Default()) {
		if h.Path == "voice.noiseFilter" {
			t.Fatal("mac-only field on Windows")
		}
	}
	// position is written by the app.
	for _, h := range Search("position", Macos, Default()) {
		if h.Path == "position" {
			t.Fatal("managed field found")
		}
	}
}

func TestAnEmptyQueryFindsNothing(t *testing.T) {
	for _, q := range []string{"", "   ", "?!"} {
		if hits := Search(q, Macos, Default()); hits == nil || len(hits) != 0 {
			t.Fatal(q, hits)
		}
	}
}

func TestVisibleComparesValuesAsTheWindowShowsThem(t *testing.T) {
	values := map[string]any{"a": true, "n": json.Number("3"), "s": "x", "z": nil}
	cases := []struct {
		cond Condition
		want bool
	}{
		{Condition{"a", []string{"true"}}, true},
		{Condition{"a", []string{"false"}}, false},
		{Condition{"n", []string{"3"}}, true},
		{Condition{"s", []string{"y", "x"}}, true},
		{Condition{"z", []string{""}}, true},
		{Condition{"missing", []string{""}}, true},
		{Condition{"missing", []string{"x"}}, false},
	}
	for _, tc := range cases {
		if got := Visible(FieldSpec{VisibleWhen: []Condition{tc.cond}}, values); got != tc.want {
			t.Fatal(tc.cond, got)
		}
	}
}

func TestTheSearchCallAnswersForTheCurrentValues(t *testing.T) {
	f := newAPIFixture(t)
	reg := fakeRegistrar{}
	f.api.Register(reg)
	out, err := reg["settings.search"](json.RawMessage(`{"query":"backend model"}`))
	if err != nil || len(out.([]Hit)) != 0 {
		t.Fatal(out, err)
	}
	f.store.now.Voice.Provider = ProviderOpenAILive
	f.store.now.Voice.LiveDelegation = LiveDelegationBackend
	out, err = reg["settings.search"](json.RawMessage(`{"query":"backend model"}`))
	if err != nil || len(out.([]Hit)) == 0 || out.([]Hit)[0].Path != "voice.liveBackendModel" {
		t.Fatal(out, err)
	}
	if _, err := reg["settings.search"](json.RawMessage(`{`)); err == nil {
		t.Fatal("bad args accepted")
	}
}
