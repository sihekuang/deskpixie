package voice

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

// speakingWith is speaking(now) with the given ProviderTurns.
func speakingWith(providerTurns bool, now time.Time) *Conversation {
	c := NewConversation(ConversationConfig{IdleTimeout: testTimeout, ProviderTurns: providerTurns})
	c.Step(HotkeyInput(), now)
	model(c, evReady, now)
	c.Step(MicLiveInput(), now)
	model(c, evSpeechStarted, now)
	model(c, evSpeechStopped, now)
	model(c, evResponseCreated, now)
	model(c, evAudio(frame()), now)
	return c
}

// A tool result the provider chooses not to speak about: with ProviderTurns the turn ends as
// soon as playback is idle; without it, the turn waits for a follow-up reply.
func TestProviderTurnsToolDoneDoesNotWaitForReply(t *testing.T) {
	for _, pt := range []bool{false, true} {
		now := t0()
		c := speakingWith(pt, now)
		model(c, toolCall("get_time"), now)
		model(c, evResponseDone, now)
		tick(c, true, ms(now, 40))
		c.Step(ToolDoneInput("c1", false), ms(now, 80))
		tick(c, true, ms(now, 120))
		if pt {
			phase(t, c, PhaseListening)
		} else {
			phase(t, c, PhaseSpeaking)
		}
	}
}

// With ProviderTurns a reply that does follow still plays normally.
func TestProviderTurnsFollowUpReplyStillPlays(t *testing.T) {
	now := t0()
	c := speakingWith(true, now)
	model(c, toolCall("get_time"), now)
	c.Step(ToolDoneInput("c1", false), ms(now, 40))
	model(c, evResponseDone, ms(now, 60))
	model(c, evResponseCreated, ms(now, 70))
	fx := model(c, evAudio(frame()), ms(now, 80))
	if !contains(fx, play(frame())) {
		t.Fatalf("follow-up audio not played: %+v", fx)
	}
	phase(t, c, PhaseSpeaking)
}

func TestFatalErrorCarriesItsFailure(t *testing.T) {
	now := t0()
	c := speakingWith(false, now)
	f := Failure{Kind: FailModelError, Msg: "GPT-Live session expired"}
	fx := model(c, Event{Kind: EvError, Fatal: true, Failure: &f}, now)
	if !contains(fx, status("Voice: GPT-Live session expired")) {
		t.Fatalf("status not from Event.Failure: %+v", fx)
	}
}

func TestNewFailureStatusLines(t *testing.T) {
	if got := (Failure{Kind: FailNoAccess, Msg: "GPT-Live"}).Status(); got != "Voice: this API key has no GPT-Live access" {
		t.Fatal(got)
	}
	if got := (Failure{Kind: FailModelError, Msg: "ended by the safety filter"}).Status(); got != "Voice: ended by the safety filter" {
		t.Fatal(got)
	}
}

type failErr struct{ f Failure }

func (e failErr) Error() string    { return "nope" }
func (e failErr) Failure() Failure { return e.f }

type failingModel struct{ err error }

func (m failingModel) Name() string { return "fake" }
func (m failingModel) Connect(context.Context, SessionConfig, string) (Session, error) {
	return nil, m.err
}

// A Connect error that is a FailureError shows its own status line; others stay "connection lost".
func TestRuntimeConnectFailureErrorStatus(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{failErr{Failure{Kind: FailNoAccess, Msg: "GPT-Live"}}, "Voice: this API key has no GPT-Live access"},
		{errors.New("dial: boom"), "Voice: connection lost"},
	} {
		ui := &uiLog{}
		r := NewRuntime(failingModel{c.err}, audio.Passthrough{}, RuntimeConfig{
			Session: testSessionCfg, APIKey: "sk-test", Conversation: DefaultConversationConfig(),
		}, ui.emit)
		r.Hotkey()
		eventually(t, "status "+c.want, func() bool { return ui.has(UIEvent{Kind: "status", Text: c.want}) })
		if !slices.Contains(ui.states(), "error") {
			t.Fatalf("no error state: %+v", ui.all())
		}
		r.Close()
	}
}

type turnOwningModel struct{ failingModel }

func (turnOwningModel) Capabilities() Capabilities { return Capabilities{OwnsTurnTaking: true} }

func TestCapabilitiesOf(t *testing.T) {
	if got := CapabilitiesOf(failingModel{}); got != (Capabilities{Truncate: true}) {
		t.Fatalf("default: %+v", got)
	}
	if got := CapabilitiesOf(turnOwningModel{}); !got.OwnsTurnTaking || got.Truncate {
		t.Fatalf("reported: %+v", got)
	}
	r := NewRuntime(turnOwningModel{}, audio.Passthrough{}, RuntimeConfig{APIKey: "k"}, nil)
	defer r.Close()
	if !r.conv.providerTurns {
		t.Fatal("runtime did not pass ProviderTurns")
	}
}
