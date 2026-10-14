package settings

// Model and voice pickers (#45). Not ported from Rust: there voice.model, voice.voice and
// agent.model are free Text fields. Here they are open choices — suggested options, while any
// text that was valid before still validates and is shown "(not found)" by the window.

import (
	"reflect"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice/gemini"
	"github.com/sihekuang/deskpixie/core/voice/openailive"
)

func values(opts []OptionView) []string {
	out := []string{}
	for _, o := range opts {
		out = append(out, o.Value)
	}
	return out
}

func viewAt(views []FieldView, path string) FieldView {
	for _, v := range views {
		if v.Path == path {
			return v
		}
	}
	return FieldView{}
}

func TestModelVoiceAndAgentModelAreOpenChoices(t *testing.T) {
	for path, src := range map[string]ChoiceSource{
		"voice.model": SourceVoiceModels, "voice.voice": SourceVoiceVoices, "agent.model": SourceStatic,
	} {
		f := FieldByPath(path)
		if f.Kind.Type != KindChoice || !f.Kind.Choices.Open || f.Kind.Choices.Source != src || f.Apply != ApplyRestart {
			t.Fatal(path, f.Kind)
		}
	}
	if !FieldByPath("agent.model").Kind.Choices.Nullable() || FieldByPath("voice.model").Kind.Choices.Nullable() {
		t.Fatal("only agent.model stores null")
	}
	agent := Options(FieldByPath("agent.model").Kind.Choices, OptionSources{})
	want := []string{"", "opus", "sonnet", "haiku", "fable", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-1"}
	if !reflect.DeepEqual(values(agent), want) || agent[0].Label != "Default (Claude Code decides)" {
		t.Fatal(pairsOf(agent))
	}
}

func TestModelAndVoiceOptionsFollowTheProvider(t *testing.T) {
	model, voiceC := FieldByPath("voice.model").Kind.Choices, FieldByPath("voice.voice").Kind.Choices
	openai := OptionSources{VoiceProvider: ProviderOpenAI}
	if got := values(Options(model, openai)); !reflect.DeepEqual(got, []string{"gpt-realtime", "gpt-realtime-mini"}) {
		t.Fatal(got)
	}
	voices := values(Options(voiceC, openai))
	if !reflect.DeepEqual(voices, []string{"alloy", "ash", "ballad", "cedar", "coral", "echo", "marin", "sage", "shimmer", "verse"}) {
		t.Fatal(voices)
	}
	g := OptionSources{VoiceProvider: ProviderGemini}
	if got := values(Options(model, g)); got[0] != gemini.DefaultModel {
		t.Fatal(got)
	}
	if got := values(Options(voiceC, g)); !reflect.DeepEqual(got, gemini.Voices) {
		t.Fatal(got)
	}
	for _, o := range Options(model, g) {
		if id, ok := gemini.GeminiModel(o.Value); !ok || id != o.Value {
			t.Fatal("not a model the Gemini adapter keeps:", o.Value)
		}
	}
	if n := len(Options(model, OptionSources{VoiceProvider: "other"})); n != 0 {
		t.Fatal("an unknown provider suggests nothing", n)
	}
}

func TestProviderDefaultsMatchTheAdaptersAndTheDefaultConfig(t *testing.T) {
	d := Default()
	if (ProviderDefaults[d.Voice.Provider] != ProviderDefault{d.Voice.Model, d.Voice.Voice}) {
		t.Fatal(ProviderDefaults[d.Voice.Provider], d.Voice)
	}
	if (ProviderDefaults[ProviderGemini] != ProviderDefault{gemini.DefaultModel, gemini.DefaultVoice}) {
		t.Fatal(ProviderDefaults[ProviderGemini])
	}
}

func TestTheSnapshotsChoicesFollowTheCurrentProvider(t *testing.T) {
	env := testEnv(t)
	svc := NewService(newMemStore(Default()), &recorder{})
	if got := values(viewAt(svc.Snapshot(env).Fields, "voice.model").Kind.Options); got[0] != "gpt-realtime" {
		t.Fatal(got)
	}
	if err := svc.SetField(env, "voice.provider", raw(`"gemini-live"`)); err != nil {
		t.Fatal(err)
	}
	snap := svc.Snapshot(env)
	if got := values(viewAt(snap.Fields, "voice.voice").Kind.Options); !reflect.DeepEqual(got, gemini.Voices) {
		t.Fatal(got)
	}
	if got := values(viewAt(snap.Fields, "voice.model").Kind.Options); got[0] != gemini.DefaultModel {
		t.Fatal(got)
	}
}

func TestACustomModelOrVoiceIsStillValid(t *testing.T) {
	env := testEnv(t)
	store := newMemStore(Default())
	svc := NewService(store, &recorder{})
	for path, v := range map[string]string{
		"voice.model": `"gpt-realtime-2025-08-28"`, "voice.voice": `"Kore"`, "agent.model": `"claude-opus-4-1"`,
	} {
		if err := svc.SetField(env, path, raw(v)); err != nil {
			t.Fatal(path, err)
		}
	}
	c := store.Current()
	if c.Voice.Model != "gpt-realtime-2025-08-28" || c.Voice.Voice != "Kore" || *c.Agent.Model != "claude-opus-4-1" {
		t.Fatal(c.Voice, *c.Agent.Model)
	}
	if p := problems(c, env); len(p) != 0 {
		t.Fatal(p)
	}
	if e := errorsOf(t, svc.SetField(env, "voice.voice", raw(`" "`))); !reflect.DeepEqual(e, one("voice.voice", Empty)) {
		t.Fatal(e)
	}
	if err := svc.SetField(env, "agent.model", raw(`""`)); err != nil || store.Current().Agent.Model != nil {
		t.Fatal(err, store.Current().Agent.Model)
	}
}

func TestSwitchingProviderSwapsTheOtherProvidersDefaults(t *testing.T) {
	env := testEnv(t)
	store := newMemStore(Default())
	svc := NewService(store, &recorder{})
	set := func(path, v string) {
		t.Helper()
		if err := svc.SetField(env, path, raw(v)); err != nil {
			t.Fatal(path, err)
		}
	}
	set("voice.provider", `"gemini-live"`)
	if v := store.Current().Voice; v.Model != "gemini-3.8-live" || v.Voice != "Puck" {
		t.Fatal(v)
	}
	set("voice.provider", `"openai-realtime"`)
	if v := store.Current().Voice; v.Model != "gpt-realtime" || v.Voice != "marin" {
		t.Fatal(v)
	}
	// A value that is not the other provider's default is left for the adapter to substitute.
	set("voice.model", `"gpt-realtime-mini"`)
	set("voice.voice", `"Kore"`)
	set("voice.provider", `"gemini-live"`)
	if v := store.Current().Voice; v.Model != "gpt-realtime-mini" || v.Voice != "Kore" {
		t.Fatal(v)
	}
	if got := svc.RestartPending(); !reflect.DeepEqual(got, []string{"voice.provider", "voice.model", "voice.voice"}) {
		t.Fatal(got)
	}
}

func TestGPTLiveListsMatchTheAdapter(t *testing.T) {
	model, voiceC := FieldByPath("voice.model").Kind.Choices, FieldByPath("voice.voice").Kind.Choices
	live := OptionSources{VoiceProvider: ProviderOpenAILive}
	if got := values(Options(model, live)); !reflect.DeepEqual(got, []string{openailive.DefaultModel}) {
		t.Fatal(got)
	}
	if got := values(Options(voiceC, live)); !reflect.DeepEqual(got, openailive.Voices) {
		t.Fatal(got)
	}
	if (ProviderDefaults[ProviderOpenAILive] != ProviderDefault{openailive.DefaultModel, openailive.DefaultVoice}) {
		t.Fatal(ProviderDefaults[ProviderOpenAILive])
	}
	if Default().Voice.LiveBackendModel != openailive.DefaultBackendModel {
		t.Fatal(Default().Voice.LiveBackendModel)
	}
	if got := values(Options(FieldByPath("voice.liveBackendModel").Kind.Choices, live)); got[0] != openailive.DefaultBackendModel {
		t.Fatal(got)
	}
}
