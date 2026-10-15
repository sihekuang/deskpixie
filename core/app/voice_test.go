package app

// Port of ai-mascot src-tauri/src/voice/mod.rs tests (8 of 8) and voice/providers.rs tests
// (8: 5 ported, 3 adapted, see below).
//
// providers.rs omissions/adaptations (audio is Swift's, the noise filter is Go's denoise):
//   - default_audio_backend_is_registered_on_macos: there is no Go audio registry; VPIO lives in
//     Swift. AudioBackends() lists its settings name so validation accepts it (tested below).
//   - the_mac_backend_shares_the_noise_filter_switch: adapted as
//     TestTheNoiseFilterSettingReachesTheCleaner (app_test.go): the Applier flips the denoise
//     Cleaner the runtime uses.
//   - the_mac_backend_shares_the_device_choices: adapted as
//     TestDeviceChoicesReachSwiftAsADevicesEvent (app_test.go).
//   - the_noise_model_name_is_forgiving_and_falls_back_to_hush: ported against denoise.Model.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/audio/denoise"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

type silentAlarm struct{}

func (silentAlarm) Fire(string, string) {}

// --- voice/mod.rs ---

func TestEveryConversationHasTheLocalTools(t *testing.T) {
	var names []string
	for _, s := range LocalTools("", silentAlarm{}).Specs() {
		names = append(names, s.Name)
	}
	want := []string{"calculate", "end_conversation", "get_time", "get_weather", "media", "timer"}
	if !slices.Equal(names, want) {
		t.Fatalf("got %v, want %v", names, want)
	}
}

func TestATimerNoticeLeftUnsaidAtCloseFallsBackToANotification(t *testing.T) {
	text := timerNotice("pasta", "5 minutes")
	if text != "Timer done: pasta (5 minutes)." {
		t.Fatal(text)
	}
	ch := make(chan voice.VoiceSignal, 8)
	ch <- voice.VoiceSignal{Kind: voice.SigUserSaid, Text: "thanks"}
	ch <- voice.VoiceSignal{Kind: voice.SigClosed, Undelivered: []voice.Notice{{Text: text}}}
	if !bounced(ch, text, alwaysOpen, time.Hour) {
		t.Error("closed with it unsaid")
	}

	ch = make(chan voice.VoiceSignal, 8)
	ch <- voice.VoiceSignal{Kind: voice.SigClosed, Undelivered: []voice.Notice{{Text: "Coordinator reply: x"}}}
	if bounced(ch, text, alwaysOpen, time.Hour) {
		t.Error("said before the close")
	}

	ch = make(chan voice.VoiceSignal)
	close(ch)
	if !bounced(ch, text, alwaysOpen, time.Hour) {
		t.Error("the voice loop is gone: notify rather than lose it")
	}
}

func TestRegistryNamesAreListedForTheSettingsPage(t *testing.T) {
	if !slices.Contains(ProviderNames(), "openai-realtime") {
		t.Error(ProviderNames())
	}
	if !slices.Contains(AudioBackends(), "macos-vpio") {
		t.Error(AudioBackends())
	}
}

func TestSessionConfigComesFromSettingsAndPersona(t *testing.T) {
	s := settings.Default().Voice
	s.Model, s.Voice = "gpt-realtime-mini", "cedar"
	c := SessionConfig(s, "You are Pixie.")
	if c.Model != "gpt-realtime-mini" || c.Voice != "cedar" || c.Instructions != "You are Pixie." {
		t.Fatalf("%+v", c)
	}
	if c.TurnDetection != voice.TurnSemantic {
		t.Error("turn detection")
	}
	if c.NoisyRoom {
		t.Error("noisy room")
	}
	noisy := s
	noisy.NoisyRoom = true
	if !SessionConfig(noisy, "").NoisyRoom {
		t.Error("noisy room not passed")
	}
	if c.ProviderNoiseReduction {
		t.Error("provider noise reduction")
	}
	// A noisy room asks the provider to reduce noise only while the mic is not cleaned here.
	if !SessionConfig(noisy, "").ProviderNoiseReduction {
		t.Error("noisy without filter")
	}
	filtered := noisy
	filtered.NoiseFilter = true
	if SessionConfig(filtered, "").ProviderNoiseReduction {
		t.Error("noisy with filter")
	}
	if len(c.Tools) != 0 {
		t.Error("the runtime fills tools from the registry")
	}
}

func TestTheDelegationSectionFollowsThePersona(t *testing.T) {
	if got := Instructions("You are Pixie.", ""); got != "You are Pixie." {
		t.Fatal(got)
	}
	if got := Instructions("You are Pixie.", "  "); got != "You are Pixie." {
		t.Fatal(got)
	}
	if got := Instructions("You are Pixie.", "Delegating:\n- x"); got != "You are Pixie.\n\nDelegating:\n- x" {
		t.Fatal(got)
	}
}

func TestMouthLevelsFollowThePackWithinTheMeterRange(t *testing.T) {
	cases := []struct {
		levels uint32
		ok     bool
		want   int
	}{{4, true, 4}, {1, true, 2}, {1000, true, 255}, {0, false, 2}}
	for _, c := range cases {
		if got := MouthLevels(c.levels, c.ok); got != c.want {
			t.Errorf("MouthLevels(%d, %v) = %d, want %d", c.levels, c.ok, got, c.want)
		}
	}
}

// dotenv_search_order: the Rust debug-only workspace .env has no Go equivalent (P2 dropped it).
func TestDotenvSearchOrder(t *testing.T) {
	paths := voice.DotenvPaths("/data")
	if paths[0] != ".env" || paths[len(paths)-1] != filepath.Join("/data", ".env") || len(paths) != 2 {
		t.Fatal(paths)
	}
	if len(voice.DotenvPaths("")) != len(paths)-1 {
		t.Fatal(voice.DotenvPaths(""))
	}
}

func TestGeminiLiveCanBePickedWithTheOpenAIModelDefaultsLeftIn(t *testing.T) {
	s := settings.Default().Voice
	s.Provider = "gemini-live"
	m, fail := PickModel(Providers(), s)
	if fail != nil {
		t.Fatal(fail.Status())
	}
	if m.Name() != "gemini-live" {
		t.Fatal(m.Name())
	}
}

func TestUnknownProviderIsASetupFailure(t *testing.T) {
	s := settings.Default().Voice
	s.Provider = "nope"
	_, fail := PickModel(Providers(), s)
	if fail == nil || fail.Status() != `Voice: unknown provider "nope"` {
		t.Fatalf("%v", fail)
	}
	// The audio half (unknown audio backend) is Swift's: Go never builds the audio unit.
}

// --- voice/providers.rs ---

func TestDefaultProviderIsRegistered(t *testing.T) {
	if _, ok := Providers()["openai-realtime"]; !ok {
		t.Fatal("openai-realtime missing")
	}
}

func TestUnknownNamesAreAbsent(t *testing.T) {
	if _, ok := Providers()["nope"]; ok {
		t.Fatal("nope registered")
	}
	if slices.Contains(AudioBackends(), "nope") {
		t.Fatal("nope audio")
	}
}

func TestFactoriesBuildModelsWithMatchingNames(t *testing.T) {
	for _, name := range []string{"openai-realtime", "gemini-live"} {
		if got := Providers()[name]().Name(); got != name {
			t.Errorf("%s builds %s", name, got)
		}
	}
}

func TestGeminiLiveIsRegistered(t *testing.T) {
	if _, ok := Providers()["gemini-live"]; !ok {
		t.Fatal("gemini-live missing")
	}
}

func TestTheNoiseModelNameIsForgivingAndFallsBackToHush(t *testing.T) {
	cases := map[string]denoise.Model{"dfn3": denoise.DFN3, " DFN3 ": denoise.DFN3, "hush": denoise.Hush, "df3": denoise.Hush}
	for in, want := range cases {
		if got := NoiseModel(in); got != want {
			t.Errorf("NoiseModel(%q) = %v, want %v", in, got, want)
		}
	}
}

// --- Go-only ---

func TestRuntimeConfigTimeoutsFollowRust(t *testing.T) {
	c := settings.Default()
	c.Voice.IdleTimeoutSecs = 0
	c.Agent.HoldOpenSecs = 0
	idle, hold := Timeouts(c)
	if idle.Seconds() != 1 || hold.Seconds() != 1 {
		t.Fatalf("idle %v hold %v", idle, hold)
	}
	c.Voice.IdleTimeoutSecs = 30
	c.Agent.HoldOpenSecs = 10
	idle, hold = Timeouts(c)
	if idle.Seconds() != 30 || hold.Seconds() != 30 {
		t.Fatalf("hold is at least the idle timeout: %v %v", idle, hold)
	}
	c.Agent.HoldOpenSecs = 180
	if _, hold = Timeouts(c); hold.Seconds() != 180 {
		t.Fatal(hold)
	}
}

var errBoom = errors.New("boom")

type failingTool struct{}

func (failingTool) Name() string                { return "boom" }
func (failingTool) Description() string         { return "" }
func (failingTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (failingTool) Invoke(context.Context, json.RawMessage) (json.RawMessage, bool, error) {
	return nil, false, errBoom
}

func TestAToolErrorIsReportedToTheModelInBand(t *testing.T) {
	out := AdaptTool(failingTool{}).Invoke(context.Background(), json.RawMessage(`{}`))
	if string(out.Output) != `{"error":"boom"}` || out.EndConversation {
		t.Fatalf("%s %v", out.Output, out.EndConversation)
	}
}

func alwaysOpen() bool { return true }

// #57: every local tool is named in what Pixie keeps and what delegate is not for, so nothing
// else Pixie could reach for competes with delegating.
func TestEveryLocalToolIsOnTheKeepListAndOffTheDelegateList(t *testing.T) {
	desc := agent.Delegate{}.Description()
	for _, s := range LocalTools("", silentAlarm{}).Specs() {
		if s.Name == "end_conversation" {
			continue
		}
		if !strings.Contains(agent.DelegationPersona, s.Name) || !strings.Contains(desc, s.Name) {
			t.Errorf("%s is missing from the delegation rules", s.Name)
		}
	}
}

// voice.liveDelegation (#70): the default keeps GPT-Live's backend; "agent" is client delegation.
func TestLiveDelegationSetting(t *testing.T) {
	s := settings.Default().Voice
	if s.LiveDelegation != settings.LiveDelegationBackend || SessionConfig(s, "").ClientDelegation {
		t.Fatalf("default %q", s.LiveDelegation)
	}
	s.LiveDelegation = settings.LiveDelegationAgent
	if !SessionConfig(s, "").ClientDelegation {
		t.Fatal("agent mode not passed")
	}
	if f := settings.FieldByPath("voice.liveDelegation"); f == nil || f.Section != settings.SectionVoice {
		t.Fatalf("field %+v", f)
	}
}
