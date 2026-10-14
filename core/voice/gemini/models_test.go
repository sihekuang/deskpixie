package gemini

// Port of ai-mascot crates/voice-gemini/src/models.rs tests (3/3).

import (
	"reflect"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

func modelCfg(model, v string) voice.SessionConfig {
	return voice.SessionConfig{Model: model, Voice: v, Instructions: "i", TurnDetection: voice.TurnSemantic}
}

func TestOpenaiDefaultsBecomeGeminiDefaults(t *testing.T) {
	c, subs := Resolve(modelCfg("gpt-realtime", "marin"))
	if c.Model != DefaultModel || c.Voice != DefaultVoice {
		t.Fatalf("got %q %q", c.Model, c.Voice)
	}
	want := []Substitution{
		{Kind: SubModel, Requested: "gpt-realtime", Used: DefaultModel},
		{Kind: SubVoice, Requested: "marin", Used: DefaultVoice},
	}
	if !reflect.DeepEqual(subs, want) {
		t.Fatalf("subs %+v", subs)
	}
	if got := subs[0].Describe(); got != `voice.model "gpt-realtime" is not a Gemini model; using gemini-3.8-live` {
		t.Fatal(got)
	}
	if got := subs[1].Describe(); got != `voice.voice "marin" is not a Gemini voice; using Puck` {
		t.Fatal(got)
	}
}

func TestGeminiSettingsPassThrough(t *testing.T) {
	c, subs := Resolve(modelCfg("gemini-3.1-flash-live-preview", "kore"))
	if c.Model != "gemini-3.1-flash-live-preview" {
		t.Fatal(c.Model)
	}
	if c.Voice != "Kore" {
		t.Fatalf("voice names are matched case-insensitively: %q", c.Voice)
	}
	if len(subs) != 0 {
		t.Fatalf("subs %+v", subs)
	}
	c, _ = Resolve(modelCfg("models/gemini-3.8-live", "Puck"))
	if c.Model != "gemini-3.8-live" {
		t.Fatalf("a models/ prefix is accepted and dropped: %q", c.Model)
	}
}

func TestOtherVendorsIdsAreNeverUsed(t *testing.T) {
	for _, m := range []string{"gpt-realtime-mini", "", "claude-x", "models/gpt-4o"} {
		if c, _ := Resolve(modelCfg(m, "Puck")); c.Model != DefaultModel {
			t.Fatalf("%q -> %q", m, c.Model)
		}
	}
	for _, v := range []string{"cedar", "alloy", ""} {
		if c, _ := Resolve(modelCfg("gemini-3.8-live", v)); c.Voice != DefaultVoice {
			t.Fatalf("%q -> %q", v, c.Voice)
		}
	}
	if len(Voices) != 30 {
		t.Fatal(len(Voices))
	}
	if _, ok := GeminiVoice(DefaultVoice); !ok {
		t.Fatal("default voice unknown")
	}
}
