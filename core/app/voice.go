package app

// Composition of the voice loop: the ONLY app code that names concrete voice providers and
// tools. Port of ai-mascot src-tauri/src/voice/{mod.rs, providers.rs}. Audio is Swift's (VPIO),
// so there is no audio registry here, only its settings name.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/audio/denoise"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/voice"
	"github.com/sihekuang/deskpixie/core/voice/gemini"
	"github.com/sihekuang/deskpixie/core/voice/openai"
)

// ModelFactory builds a voice model adapter.
type ModelFactory func() voice.Model

// Providers is the voice provider registry: settings `voice.provider` name → adapter. The names
// are also the secrets' provider names ("gemini-live" → GEMINI_API_KEY).
func Providers() map[string]ModelFactory {
	return map[string]ModelFactory{
		openai.Name: func() voice.Model { return openai.New("") },
		gemini.Name: func() voice.Model { return gemini.New("") },
	}
}

// ProviderNames are the registered providers, sorted (Settings ▸ Voice choices).
func ProviderNames() []string {
	var out []string
	for name := range Providers() {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// AudioBackends are the audio backend names Swift implements (Settings ▸ Voice ▸ Audio).
func AudioBackends() []string { return []string{"macos-vpio"} }

// PickModel builds the model named by s.Provider; an unknown name is a setup failure.
func PickModel(reg map[string]ModelFactory, s settings.VoiceSettings) (voice.Model, *voice.Failure) {
	f, ok := reg[s.Provider]
	if !ok {
		fail := voice.SetupFailure(fmt.Sprintf("unknown provider %q", s.Provider))
		return nil, &fail
	}
	return f(), nil
}

// SessionConfig is the session from settings and the instructions.
func SessionConfig(s settings.VoiceSettings, instructions string) voice.SessionConfig {
	return voice.SessionConfig{
		Model:         s.Model,
		Voice:         s.Voice,
		Instructions:  instructions,
		TurnDetection: voice.TurnSemantic,
		NoisyRoom:     s.NoisyRoom,
		// A noisy room asks the provider to reduce noise only while the mic is not cleaned here.
		ProviderNoiseReduction: s.NoisyRoom && !s.NoiseFilter,
	}
}

// Instructions is the persona plus an optional extra section (delegation), separated by a blank
// line.
func Instructions(persona, extra string) string {
	e := strings.TrimSpace(extra)
	if e == "" {
		return persona
	}
	return persona + "\n\n" + e
}

// MouthLevels is the voice loop's mouth level count for a pack's mouth.levels (no pack: 2).
func MouthLevels(packLevels uint32, ok bool) int {
	if !ok {
		return 2
	}
	return int(min(max(packLevels, 2), 255))
}

// Timeouts are the conversation's silence timeouts: idle ≥ 1 s; hold = max(holdOpenSecs, idle, 1).
func Timeouts(c settings.Config) (idle, hold time.Duration) {
	idleSecs := max(c.Voice.IdleTimeoutSecs, 1)
	holdSecs := max(c.Agent.HoldOpenSecs, c.Voice.IdleTimeoutSecs, 1)
	return time.Duration(idleSecs) * time.Second, time.Duration(holdSecs) * time.Second
}

// NoiseModel is the settings' noise model; an unknown name (a hand edit) falls back to Hush,
// logged.
func NoiseModel(name string) denoise.Model {
	m, ok := denoise.ParseModel(strings.ToLower(strings.TrimSpace(name)))
	if !ok {
		log.Printf("[voice] unknown noiseModel %q, using hush", name)
		return denoise.Hush
	}
	return m
}

// AdaptTool makes a local tool a voice tool. A Go error (local tools report problems in-band,
// so this is rare) becomes {"error": "..."} for the model.
func AdaptTool(t tools.Tool) voice.Tool { return toolAdapter{t} }

type toolAdapter struct{ t tools.Tool }

func (a toolAdapter) Name() string                { return a.t.Name() }
func (a toolAdapter) Description() string         { return a.t.Description() }
func (a toolAdapter) Parameters() json.RawMessage { return a.t.Parameters() }
func (a toolAdapter) Invoke(ctx context.Context, args json.RawMessage) voice.ToolOutcome {
	out, end, err := a.t.Invoke(ctx, args)
	if err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return voice.ToolOutcome{Output: b}
	}
	return voice.ToolOutcome{Output: out, EndConversation: end}
}

// LocalTools are the tools every conversation has: end_conversation plus the local ones, which
// answer the time, the weather, arithmetic, timers and media at once.
func LocalTools(homeCity string, alarm tools.Alarm) *voice.ToolRegistry {
	om := tools.NewOpenMeteo()
	reg := voice.DefaultTools()
	reg.Register(AdaptTool(tools.GetTime{Clock: tools.SystemClock{}, Places: om}))
	reg.Register(AdaptTool(tools.GetWeather{Places: om, Sky: om, Home: homeCity}))
	reg.Register(AdaptTool(tools.Calculate{}))
	reg.Register(AdaptTool(tools.TimerTool{Timers: tools.NewTimers(alarm)}))
	reg.Register(AdaptTool(tools.Media{Player: tools.NewMacPlayers()}))
	return reg
}

// VoiceLink is what the timer alarm needs of the voice loop (*voice.Runtime).
type VoiceLink interface {
	IsOpen() bool
	Inject(voice.Notice)
	Subscribe() <-chan voice.VoiceSignal
}

// TimerAlarm: during a conversation Bit says it; otherwise, or if the conversation closes
// before Bit could, a chime and a macOS notification. Fire is goroutine-safe (it only reads its
// fields and starts a goroutine).
type TimerAlarm struct {
	// Voice is the running voice loop, or nil.
	Voice func() VoiceLink
	// Notify shows the notification with the Glass sound.
	Notify func(title, body string)
}

func timerNotice(label, spoken string) string {
	return fmt.Sprintf("Timer done: %s (%s).", label, spoken)
}

// Fire implements tools.Alarm.
func (t *TimerAlarm) Fire(label, spoken string) {
	body := fmt.Sprintf("%s (%s)", label, spoken)
	notify := func() { t.Notify("Timer done", body) }
	var v VoiceLink
	if t.Voice != nil {
		v = t.Voice()
	}
	if v == nil || !v.IsOpen() {
		notify()
		return
	}
	text := timerNotice(label, spoken)
	signals := v.Subscribe()
	v.Inject(voice.Notice{Text: text})
	go func() {
		if bounced(signals, text) {
			notify()
		}
	}()
}

// bounced: the conversation closed with text still unsaid (it waits for the next close). A
// closed channel (the voice loop is gone) counts as unsaid: a rare duplicate beats a lost
// timer. Go channels drop silently when a subscriber lags, so a missed close is not detected
// (Rust's Lagged); the runtime's 64-signal buffer makes that unlikely for this short wait.
func bounced(signals <-chan voice.VoiceSignal, text string) bool {
	for s := range signals {
		if s.Kind != voice.SigClosed {
			continue
		}
		for _, n := range s.Undelivered {
			if n.Text == text {
				return true
			}
		}
		return false
	}
	return true
}
