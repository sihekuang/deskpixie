package settings

// A model per voice provider, from the provider's list when it loaded (#477).

import (
	"encoding/json"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"
)

var modelPaths = map[string]string{
	ProviderOpenAI: "voice.openaiModel", ProviderGemini: "voice.geminiModel", ProviderOpenAILive: "voice.liveModel",
}

func TestEachProviderHasItsOwnModelField(t *testing.T) {
	if FieldByPath("voice.model") != nil {
		t.Fatal("voice.model is gone")
	}
	for provider, path := range modelPaths {
		f := FieldByPath(path)
		if f == nil || f.Kind.Type != KindChoice || !f.Kind.Choices.Open || f.Kind.Choices.Source != SourceVoiceModels ||
			f.Kind.Choices.Provider != provider || f.Apply != ApplyRestart || f.Kind.Choices.Nullable() {
			t.Fatal(path, f)
		}
		want := []Condition{{Key: "voice.provider", In: []string{provider}}}
		if !reflect.DeepEqual(f.VisibleWhen, want) {
			t.Fatal(path, f.VisibleWhen)
		}
	}
	d := Default().Voice
	if d.OpenAIModel != ProviderDefaults[ProviderOpenAI].Model || d.GeminiModel != ProviderDefaults[ProviderGemini].Model ||
		d.LiveModel != ProviderDefaults[ProviderOpenAILive].Model {
		t.Fatal(d)
	}
}

func TestModelIsTheCurrentProvidersField(t *testing.T) {
	v := VoiceSettings{OpenAIModel: "o", GeminiModel: "g", LiveModel: "l"}
	for provider, want := range map[string]string{ProviderOpenAI: "o", ProviderGemini: "g", ProviderOpenAILive: "l", "other": ""} {
		v.Provider = provider
		if got := v.Model(); got != want {
			t.Fatal(provider, got)
		}
	}
}

func TestTheOldModelKeyIsNotWritten(t *testing.T) {
	b, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	var top struct{ Voice map[string]any }
	json.Unmarshal(b, &top)
	if _, ok := top.Voice["model"]; ok {
		t.Fatal(top.Voice)
	}
	for _, k := range []string{"openaiModel", "geminiModel", "liveModel"} {
		if _, ok := top.Voice[k]; !ok {
			t.Fatal(k, top.Voice)
		}
	}
}

func TestAnOldVoiceModelMovesToTheFieldOfItsFamily(t *testing.T) {
	d := Default().Voice
	for _, tc := range []struct {
		provider, model string
		want            VoiceSettings
	}{
		{ProviderOpenAI, "gpt-realtime-mini", VoiceSettings{OpenAIModel: "gpt-realtime-mini", GeminiModel: d.GeminiModel, LiveModel: d.LiveModel}},
		// #473: GPT-Live with a Realtime model left behind keeps GPT-Live's default.
		{ProviderOpenAILive, "gpt-realtime", VoiceSettings{OpenAIModel: "gpt-realtime", GeminiModel: d.GeminiModel, LiveModel: d.LiveModel}},
		{ProviderOpenAI, "gpt-live-1.1", VoiceSettings{OpenAIModel: d.OpenAIModel, GeminiModel: d.GeminiModel, LiveModel: "gpt-live-1.1"}},
		{ProviderOpenAI, "gemini-3.1-flash-live-preview", VoiceSettings{OpenAIModel: d.OpenAIModel, GeminiModel: "gemini-3.1-flash-live-preview", LiveModel: d.LiveModel}},
		{ProviderGemini, "models/gemini-3.8-live", VoiceSettings{OpenAIModel: d.OpenAIModel, GeminiModel: "models/gemini-3.8-live", LiveModel: d.LiveModel}},
		// Not any family's: the current provider's field.
		{ProviderGemini, "my-proxy-model", VoiceSettings{OpenAIModel: d.OpenAIModel, GeminiModel: "my-proxy-model", LiveModel: d.LiveModel}},
		{ProviderOpenAILive, "custom", VoiceSettings{OpenAIModel: d.OpenAIModel, GeminiModel: d.GeminiModel, LiveModel: "custom"}},
	} {
		c, err := ParseConfig([]byte(`{"voice":{"provider":"` + tc.provider + `","model":"` + tc.model + `"}}`))
		if err != nil {
			t.Fatal(err)
		}
		got := VoiceSettings{OpenAIModel: c.Voice.OpenAIModel, GeminiModel: c.Voice.GeminiModel, LiveModel: c.Voice.LiveModel}
		if got != tc.want {
			t.Errorf("%s %s: %+v", tc.provider, tc.model, got)
		}
	}
}

func TestAMigratedFileIsLeftAlone(t *testing.T) {
	c, err := ParseConfig([]byte(`{"voice":{"provider":"openai-realtime","model":"gpt-realtime-mini","geminiModel":"gemini-x-live"}}`))
	if err != nil {
		t.Fatal(err)
	}
	d := Default().Voice
	if c.Voice.OpenAIModel != d.OpenAIModel || c.Voice.GeminiModel != "gemini-x-live" || c.Voice.LiveModel != d.LiveModel {
		t.Fatal("a file with one of the new keys is already migrated:", c.Voice)
	}
	c, err = ParseConfig([]byte(`{"voice":{"provider":"gemini-live","voice":"Kore"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Voice.OpenAIModel != d.OpenAIModel || c.Voice.GeminiModel != d.GeminiModel || c.Voice.LiveModel != d.LiveModel {
		t.Fatal("no voice.model: the defaults", c.Voice)
	}
}

func TestModelOptionsAreTheFieldsProviders(t *testing.T) {
	for provider, path := range modelPaths {
		ch := FieldByPath(path).Kind.Choices
		// The current provider doesn't matter: each field lists its own provider's models.
		got := Options(ch, OptionSources{VoiceProvider: "other"})
		want := static(map[string][]Opt{ProviderOpenAI: OpenAIModels, ProviderGemini: GeminiModels, ProviderOpenAILive: OpenAILiveModels}[provider])
		if !reflect.DeepEqual(got, want) {
			t.Fatal(path, got)
		}
	}
	if got := values(Options(FieldByPath("voice.openaiModel").Kind.Choices, OptionSources{})); !reflect.DeepEqual(got,
		[]string{"gpt-realtime", "gpt-realtime-2", "gpt-realtime-2.1", "gpt-realtime-mini", "gpt-realtime-2.1-mini"}) {
		t.Fatal(got)
	}
}

func TestModelOptionsAreTheLoadedListWithTheSavedOne(t *testing.T) {
	ch := FieldByPath("voice.liveModel").Kind.Choices
	src := OptionSources{VoiceModels: map[string][]string{ProviderOpenAILive: {"gpt-live-1", "gpt-live-2"}}}
	got := Options(ch, src)
	// A known id keeps its built-in label; an unknown one is labelled with the id.
	if !reflect.DeepEqual(pairsOf(got), [][2]string{{"gpt-live-1", "GPT-Live 1"}, {"gpt-live-2", "gpt-live-2"}}) {
		t.Fatal(pairsOf(got))
	}
	src.VoiceModelsSaved = map[string]string{ProviderOpenAILive: "gpt-live-0"}
	if got := values(Options(ch, src)); !reflect.DeepEqual(got, []string{"gpt-live-1", "gpt-live-2", "gpt-live-0"}) {
		t.Fatal(got)
	}
	src.VoiceModelsSaved[ProviderOpenAILive] = "gpt-live-2"
	if got := values(Options(ch, src)); !reflect.DeepEqual(got, []string{"gpt-live-1", "gpt-live-2"}) {
		t.Fatal("listed once:", got)
	}
	// Without a loaded list: the built-in one, plus the saved model.
	src = OptionSources{VoiceModelsSaved: map[string]string{ProviderOpenAILive: "gpt-live-0"}}
	if got := values(Options(ch, src)); !reflect.DeepEqual(got, []string{"gpt-live-1", "gpt-live-1.1", "gpt-live-0"}) {
		t.Fatal(got)
	}
}

func TestAFailedLoadIsNotedInTheModelsHelp(t *testing.T) {
	src := OptionSources{VoiceModelsNote: map[string]string{ProviderOpenAI: "openai models: http 401: bad key", ProviderGemini: strings.Repeat("x", 300)}}
	views := Views(src)
	openaiHelp := viewAt(views, "voice.openaiModel").Help
	if !strings.HasSuffix(openaiHelp, "\nCouldn't load the list from OpenAI (openai models: http 401: bad key); showing the built-in list.") ||
		!strings.HasPrefix(openaiHelp, FieldByPath("voice.openaiModel").Help) {
		t.Fatal(openaiHelp)
	}
	g := viewAt(views, "voice.geminiModel").Help
	if !strings.Contains(g, "Couldn't load the list from Google (xxx") || !strings.Contains(g, "…); showing the built-in list.") || len(g) > 300 {
		t.Fatal("a long reason is clipped:", g)
	}
	if h := viewAt(views, "voice.liveModel").Help; h != FieldByPath("voice.liveModel").Help {
		t.Fatal("no note, no line:", h)
	}
	// A later failure over a list that loaded before: that list is what shows.
	src.VoiceModels = map[string][]string{ProviderOpenAI: {"gpt-realtime"}}
	if h := viewAt(Views(src), "voice.openaiModel").Help; !strings.HasSuffix(h, "(openai models: http 401: bad key); showing the last loaded list.") {
		t.Fatal(h)
	}
}

// The snapshot fills the saved models from the config, so each field lists its own value.
func TestTheSnapshotListsEachSavedModel(t *testing.T) {
	env := testEnv(t)
	c := Default()
	c.Voice.GeminiModel = "gemini-old-live"
	svc := NewService(newMemStore(c), &recorder{})
	if got := values(viewAt(svc.Snapshot(env).Fields, "voice.geminiModel").Kind.Options); !reflect.DeepEqual(got,
		[]string{"gemini-3.8-live", "gemini-3.1-flash-live-preview", "gemini-old-live"}) {
		t.Fatal(got)
	}
}

// Search finds the current provider's model field only.
func TestSearchFindsTheCurrentProvidersModel(t *testing.T) {
	c := Default()
	c.Voice.Provider = ProviderGemini
	var found []string
	for _, h := range Search("gemini live model", Macos, c) {
		if strings.HasPrefix(h.Path, "voice.") && strings.HasSuffix(h.Path, "Model") {
			found = append(found, h.Path)
		}
	}
	if !reflect.DeepEqual(found, []string{"voice.geminiModel"}) {
		t.Fatal(found)
	}
}

// A voice.model that names no family, with a provider this version doesn't know, has no field
// to go to: it is dropped, and the log says so.
func TestAnOldModelWithNowhereToGoIsLogged(t *testing.T) {
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	c, err := ParseConfig([]byte(`{"voice":{"provider":"future-voice","model":"x-1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	d := Default().Voice
	if c.Voice.OpenAIModel != d.OpenAIModel || c.Voice.GeminiModel != d.GeminiModel || c.Voice.LiveModel != d.LiveModel {
		t.Fatal(c.Voice)
	}
	if !strings.Contains(buf.String(), `voice.model "x-1"`) {
		t.Fatal(buf.String())
	}
}
