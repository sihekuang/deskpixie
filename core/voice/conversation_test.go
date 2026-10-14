package voice

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// One-for-one port of the 52 #[test]s in ai-mascot crates/voice-core/src/conversation.rs.
// Each Rust test is one case in TestConversation, under the same (snake_case) name.

const (
	testTimeout = 7 * time.Second
	testHold    = 180 * time.Second
)

var testT0 = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func t0() time.Time { return testT0 }

func ms(base time.Time, n int64) time.Time { return base.Add(time.Duration(n) * time.Millisecond) }

func frame() []int16 {
	f := make([]int16, 2400)
	for i := range f {
		f[i] = 1000
	}
	return f
}

// ---- inputs ----

var (
	evReady           = Event{Kind: EvReady}
	evSpeechStarted   = Event{Kind: EvSpeechStarted}
	evSpeechStopped   = Event{Kind: EvSpeechStopped}
	evResponseCreated = Event{Kind: EvResponseCreated}
	evResponseDone    = Event{Kind: EvResponseDone}
)

func evAudio(f []int16) Event { return Event{Kind: EvAudioDelta, Audio: f} }

func evToolCall(id, name string) Event {
	return Event{Kind: EvToolCall, CallID: id, ToolName: name, Args: json.RawMessage(`{}`)}
}

// toolCall mirrors the Rust helper: call id "c1".
func toolCall(name string) Event { return evToolCall("c1", name) }

func model(c *Conversation, ev Event, now time.Time) []Effect { return c.Step(ModelInput(ev), now) }

func tick(c *Conversation, idle bool, now time.Time) []Effect { return c.Step(TickInput(idle), now) }

func notice(text string) Notice { return Notice{Text: text} }

// ---- effects ----

func ef(k EffectKind) Effect         { return Effect{Kind: k} }
func pixie(s PixieState) Effect      { return Effect{Kind: EfPixie, State: s} }
func status(s string) Effect         { return Effect{Kind: EfStatus, Status: s} }
func chime(ch Chime) Effect          { return Effect{Kind: EfPlayChime, Chime: ch} }
func play(f []int16) Effect          { return Effect{Kind: EfPlay, Audio: f} }
func injectFx(text string) Effect    { return Effect{Kind: EfInject, Text: text} }
func signalFx(s VoiceSignal) Effect  { return Effect{Kind: EfSignal, Signal: s} }
func closedWith(ns ...Notice) Effect { return signalFx(VoiceSignal{Kind: SigClosed, Undelivered: ns}) }
func closed() Effect                 { return closedWith() }
func invoke(id, name string, args string) Effect {
	return Effect{Kind: EfInvokeTool, CallID: id, ToolName: name, Args: json.RawMessage(args)}
}

func ended() []Effect {
	return []Effect{ef(EfStopMic), ef(EfClose), chime(ChimeEnd), pixie(StateIdle), closed()}
}

// ---- assertions ----

func effectsEqual(a, b []Effect) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if len(x.Signal.Undelivered) == 0 && len(y.Signal.Undelivered) == 0 {
			x.Signal.Undelivered, y.Signal.Undelivered = nil, nil
		}
		if !reflect.DeepEqual(x, y) {
			return false
		}
	}
	return true
}

func eq(t *testing.T, got []Effect, want ...Effect) {
	t.Helper()
	if !effectsEqual(got, want) {
		t.Fatalf("effects:\n got  %+v\n want %+v", got, want)
	}
}

func empty(t *testing.T, got []Effect, why ...string) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("want no effects %v, got %+v", why, got)
	}
}

func contains(fx []Effect, e Effect) bool {
	for _, x := range fx {
		if effectsEqual([]Effect{x}, []Effect{e}) {
			return true
		}
	}
	return false
}

func hasKind(fx []Effect, k EffectKind) bool {
	for _, x := range fx {
		if x.Kind == k {
			return true
		}
	}
	return false
}

func phase(t *testing.T, c *Conversation, want Phase) {
	t.Helper()
	if c.Phase() != want {
		t.Fatalf("phase: got %v, want %v", c.Phase(), want)
	}
}

// listeningArmed asserts Phase::Listening { silence_since: Some(at) }.
func listeningArmed(t *testing.T, c *Conversation, at time.Time) {
	t.Helper()
	since, armed := c.SilenceSince()
	if c.Phase() != PhaseListening || !armed || !since.Equal(at) {
		t.Fatalf("want Listening armed at %v, got %v armed=%v since=%v", at, c.Phase(), armed, since)
	}
}

// listeningTalking asserts Phase::Listening { silence_since: None }.
func listeningTalking(t *testing.T, c *Conversation) {
	t.Helper()
	if _, armed := c.SilenceSince(); c.Phase() != PhaseListening || armed {
		t.Fatalf("want Listening with timer off, got %v armed=%v", c.Phase(), armed)
	}
}

// ---- fixtures ----

func newConv() *Conversation { return NewConversation(ConversationConfig{IdleTimeout: testTimeout}) }

func newHeldConv() *Conversation {
	return NewConversation(ConversationConfig{IdleTimeout: testTimeout, HoldTimeout: testHold})
}

// listening: Hotkey + Ready + mic on, silence timer armed at now.
func listening(now time.Time) *Conversation {
	c := newConv()
	c.Step(HotkeyInput(), now)
	model(c, evReady, now)
	c.Step(MicLiveInput(), now)
	return c
}

// thinking: ... then the user spoke and stopped.
func thinking(now time.Time) *Conversation {
	c := listening(now)
	model(c, evSpeechStarted, now)
	model(c, evSpeechStopped, now)
	return c
}

// speaking: ... then the first reply audio arrived.
func speaking(now time.Time) *Conversation {
	c := thinking(now)
	model(c, evResponseCreated, now)
	model(c, evAudio(frame()), now)
	return c
}

func TestConversation(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"hotkey_from_idle_connects", func(t *testing.T) {
			c := newConv()
			eq(t, c.Step(HotkeyInput(), t0()), ef(EfConnect), pixie(StateThinking))
			phase(t, c, PhaseConnecting)
			if !c.IsActive() {
				t.Fatal("active")
			}
		}},
		{"ready_plays_start_chime_and_listens", func(t *testing.T) {
			now := t0()
			c := newConv()
			c.Step(HotkeyInput(), now)
			eq(t, model(c, evReady, now),
				status(""), chime(ChimeStart), ef(EfStartMic), pixie(StateListening), signalFx(VoiceSignal{Kind: SigOpened}))
			listeningArmed(t, c, now)
		}},
		{"hotkey_while_connecting_cancels_quietly", func(t *testing.T) {
			c := newConv()
			c.Step(HotkeyInput(), t0())
			eq(t, c.Step(HotkeyInput(), t0()), ef(EfClose), pixie(StateIdle), closed())
			phase(t, c, PhaseIdle)
			empty(t, model(c, evReady, t0()), "a late Ready is ignored")
		}},
		{"speech_disarms_timer_and_stopping_thinks", func(t *testing.T) {
			now := t0()
			c := listening(now)
			empty(t, model(c, evSpeechStarted, now))
			listeningTalking(t, c)
			eq(t, model(c, evSpeechStopped, now), pixie(StateThinking))
			phase(t, c, PhaseThinking)
		}},
		{"first_audio_starts_speaking", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			eq(t, model(c, evAudio(frame()), now), ef(EfResponseStarted), pixie(StateIdle), play(frame()))
			phase(t, c, PhaseSpeaking)
			eq(t, model(c, evAudio(frame()), now), play(frame()))
		}},
		{"reply_waits_for_playback_then_listens_with_timer", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			empty(t, model(c, evResponseDone, now))
			empty(t, tick(c, false, ms(now, 500)), "still playing")
			eq(t, tick(c, true, ms(now, 900)), pixie(StateListening))
			listeningArmed(t, c, ms(now, 900))
		}},
		{"reply_without_audio_returns_to_listening", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			model(c, evResponseCreated, now)
			eq(t, model(c, evResponseDone, now), pixie(StateListening))
			listeningArmed(t, c, now)
		}},
		{"talking_over_bit_interrupts", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			eq(t, model(c, evSpeechStarted, now), ef(EfInterrupt), pixie(StateListening))
			listeningTalking(t, c)
		}},
		{"trailing_audio_after_interrupt_is_dropped", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			model(c, evSpeechStarted, now)
			empty(t, model(c, evAudio(frame()), now))
			empty(t, model(c, evResponseDone, now), "the cancelled reply's done changes nothing")
			listeningTalking(t, c)
		}},
		{"unprompted_reply_while_quietly_listening_is_played", func(t *testing.T) {
			now := t0()
			c := listening(now)
			eq(t, model(c, evAudio(frame()), now), ef(EfResponseStarted), pixie(StateIdle), play(frame()))
		}},
		{"speech_while_thinking_goes_back_to_listening", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			eq(t, model(c, evSpeechStarted, now), pixie(StateListening))
			listeningTalking(t, c)
		}},
		{"silence_timeout_ends_after_seven_seconds", func(t *testing.T) {
			now := t0()
			c := listening(now)
			empty(t, tick(c, true, ms(now, 6_999)))
			eq(t, tick(c, true, ms(now, 7_000)), ended()...)
			phase(t, c, PhaseIdle)
		}},
		{"talking_resets_the_silence_timer", func(t *testing.T) {
			now := t0()
			c := listening(now)
			model(c, evSpeechStarted, ms(now, 5_000))
			empty(t, tick(c, true, ms(now, 20_000)), "timer is off while the user talks")
		}},
		{"end_conversation_waits_for_the_goodbye_to_finish", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			eq(t, model(c, evToolCall("c1", "end_conversation"), now), invoke("c1", "end_conversation", `{}`))
			empty(t, model(c, evResponseDone, now))
			empty(t, c.Step(ToolDoneInput("c1", true), now), "goodbye still playing")
			empty(t, tick(c, false, ms(now, 300)))
			eq(t, tick(c, true, ms(now, 600)), ended()...)
		}},
		{"end_conversation_without_audio_ends_at_once", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			model(c, evResponseCreated, now)
			model(c, evToolCall("c1", "end_conversation"), now)
			empty(t, model(c, evResponseDone, now), "waits for the tool")
			eq(t, c.Step(ToolDoneInput("c1", true), now), ended()...)
		}},
		{"other_tools_wait_for_the_follow_up_reply", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			model(c, evResponseCreated, now)
			model(c, evToolCall("c1", "lookup"), now)
			empty(t, model(c, evResponseDone, now))
			phase(t, c, PhaseThinking) // tool pending: not done yet
			empty(t, c.Step(ToolDoneInput("c1", false), now))
			phase(t, c, PhaseThinking) // waiting for the reply that uses the result
			model(c, evResponseCreated, now)
			model(c, evAudio(frame()), now)
			phase(t, c, PhaseSpeaking)
		}},
		{"hotkey_during_conversation_ends_with_flush", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			eq(t, c.Step(HotkeyInput(), now), append([]Effect{ef(EfFlushPlayback)}, ended()...)...)
			phase(t, c, PhaseIdle)
		}},
		{"failure_shows_error_then_idle_after_two_seconds", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			eq(t, c.Step(FailedInput(Failure{Kind: FailConnectionLost}), now),
				ef(EfFlushPlayback), ef(EfStopMic), ef(EfClose), pixie(StateError), chime(ChimeEnd),
				status("Voice: connection lost"), closed())
			if c.IsActive() {
				t.Fatal("still active")
			}
			empty(t, tick(c, true, ms(now, 1_999)))
			eq(t, tick(c, true, ms(now, 2_000)), pixie(StateIdle))
			phase(t, c, PhaseIdle)
		}},
		{"failure_while_connecting_also_shows_error", func(t *testing.T) {
			c := newConv()
			c.Step(HotkeyInput(), t0())
			fx := c.Step(FailedInput(NoAPIKey("OPENAI_API_KEY")), t0())
			if !contains(fx, pixie(StateError)) {
				t.Fatalf("%+v", fx)
			}
			if !contains(fx, status("Voice: no API key (set OPENAI_API_KEY)")) {
				t.Fatalf("%+v", fx)
			}
		}},
		{"failures_and_events_when_idle_are_ignored", func(t *testing.T) {
			c := newConv()
			empty(t, c.Step(FailedInput(Failure{Kind: FailConnectionLost}), t0()))
			empty(t, model(c, evSpeechStarted, t0()))
			empty(t, c.Step(ToolDoneInput("c1", true), t0()))
			phase(t, c, PhaseIdle)
		}},
		{"fatal_model_errors_fail_and_others_are_ignored", func(t *testing.T) {
			now := t0()
			c := listening(now)
			empty(t, model(c, Event{Kind: EvError, Fatal: false, Message: "meh"}, now))
			fx := model(c, Event{Kind: EvError, Fatal: true, Message: "bad"}, now)
			if !contains(fx, status("Voice: model error")) {
				t.Fatalf("%+v", fx)
			}
		}},
		{"hotkey_during_cooldown_starts_a_new_conversation", func(t *testing.T) {
			now := t0()
			c := listening(now)
			c.Step(FailedInput(Failure{Kind: FailConnectionLost}), now)
			eq(t, c.Step(HotkeyInput(), ms(now, 500)), ef(EfConnect), pixie(StateThinking))
			phase(t, c, PhaseConnecting)
		}},
		{"user_transcripts_are_signalled_and_change_nothing_else", func(t *testing.T) {
			now := t0()
			c := listening(now)
			eq(t, model(c, Event{Kind: EvTranscript, Role: "user", Text: "hi"}, now),
				signalFx(VoiceSignal{Kind: SigUserSaid, Text: "hi"}))
			empty(t, model(c, Event{Kind: EvTranscript, Role: "assistant", Text: "hello"}, now))
			listeningArmed(t, c, now)
		}},

		// ---------- slice 4: Inject and HoldOpen ----------

		{"inject_while_quietly_listening_is_sent_at_once", func(t *testing.T) {
			now := t0()
			c := listening(now)
			eq(t, c.Step(InjectInput(notice("Agent reply: done")), now), injectFx("Agent reply: done"), pixie(StateThinking))
			phase(t, c, PhaseThinking)
		}},
		{"inject_waits_while_the_user_talks", func(t *testing.T) {
			now := t0()
			c := listening(now)
			model(c, evSpeechStarted, now)
			empty(t, c.Step(InjectInput(notice("Agent reply: x")), now))
			empty(t, tick(c, true, ms(now, 100)), "timer disarmed: someone is talking")
			model(c, evSpeechStopped, ms(now, 200))
			model(c, evResponseCreated, ms(now, 300))
			model(c, evResponseDone, ms(now, 400))
			// Back to listening with the timer armed: the notice goes out on the next tick.
			eq(t, tick(c, true, ms(now, 500)), injectFx("Agent reply: x"), pixie(StateThinking))
		}},
		{"inject_is_never_sent_mid_speech", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			empty(t, c.Step(InjectInput(notice("Agent reply: y")), now))
			empty(t, tick(c, false, ms(now, 100)), "still playing")
			model(c, evResponseDone, ms(now, 200))
			if contains(tick(c, false, ms(now, 300)), injectFx("Agent reply: y")) {
				t.Fatal("injected mid speech")
			}
			// Playback drains: Speaking -> Listening, and the notice goes out in the same tick.
			eq(t, tick(c, true, ms(now, 400)), pixie(StateListening), injectFx("Agent reply: y"), pixie(StateThinking))
		}},
		{"inject_waits_for_running_tools", func(t *testing.T) {
			now := t0()
			c := listening(now)
			model(c, toolCall("lookup"), now)
			empty(t, c.Step(InjectInput(notice("n")), now))
			empty(t, tick(c, true, ms(now, 100)))
		}},
		{"notices_go_out_one_per_quiet_moment_in_order", func(t *testing.T) {
			now := t0()
			c := listening(now)
			c.Step(InjectInput(notice("first")), now)
			empty(t, c.Step(InjectInput(notice("second")), now), "the first is being answered")
			model(c, evResponseCreated, ms(now, 100))
			model(c, evResponseDone, ms(now, 200))
			eq(t, tick(c, true, ms(now, 300)), injectFx("second"), pixie(StateThinking))
		}},
		{"inject_when_closed_bounces_as_undelivered", func(t *testing.T) {
			c := newConv()
			eq(t, c.Step(InjectInput(notice("late")), t0()), closedWith(notice("late")))
		}},
		{"ending_returns_queued_notices_as_undelivered", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			c.Step(InjectInput(notice("queued")), now)
			fx := c.Step(HotkeyInput(), now)
			eq(t, fx[len(fx)-1:], closedWith(notice("queued")))
		}},
		{"cancelling_while_connecting_returns_queued_notices", func(t *testing.T) {
			now := t0()
			c := newConv()
			c.Step(HotkeyInput(), now)
			empty(t, c.Step(InjectInput(notice("n")), now), "queued while connecting")
			fx := c.Step(HotkeyInput(), now)
			eq(t, fx[len(fx)-1:], closedWith(notice("n")))
			// The next conversation must not replay the stale notice.
			c.Step(HotkeyInput(), now)
			if fx := model(c, evReady, now); hasKind(fx, EfInject) {
				t.Fatalf("%+v", fx)
			}
			if fx := tick(c, true, ms(now, 100)); hasKind(fx, EfInject) {
				t.Fatalf("%+v", fx)
			}
		}},
		{"hold_open_swaps_the_silence_timeout", func(t *testing.T) {
			now := t0()
			c := newHeldConv()
			c.Step(HotkeyInput(), now)
			model(c, evReady, now)
			c.Step(MicLiveInput(), now)
			c.Step(HoldOpenInput(true), now)
			empty(t, tick(c, true, ms(now, 7_000)), "held: 7 s is not enough")
			empty(t, tick(c, true, ms(now, 179_999)))
			fx := tick(c, true, ms(now, 180_000))
			if !contains(fx, ef(EfClose)) {
				t.Fatalf("%+v", fx)
			}
			if !contains(fx, pixie(StateDelegating)) {
				t.Fatalf("rests in delegating while held: %+v", fx)
			}
		}},
		{"releasing_the_hold_restores_seven_seconds", func(t *testing.T) {
			now := t0()
			c := newHeldConv()
			c.Step(HoldOpenInput(true), now)
			c.Step(HotkeyInput(), now)
			model(c, evReady, now)
			c.Step(MicLiveInput(), now)
			c.Step(HoldOpenInput(false), ms(now, 1_000))
			eq(t, tick(c, true, ms(now, 7_000)), ended()...)
		}},
		{"hold_open_while_idle_sets_the_rest_state", func(t *testing.T) {
			c := newHeldConv()
			eq(t, c.Step(HoldOpenInput(true), t0()), pixie(StateDelegating))
			eq(t, c.Step(HoldOpenInput(false), t0()), pixie(StateIdle))
			now := t0()
			c = listening(now)
			empty(t, c.Step(HoldOpenInput(true), now), "mid-conversation the voice loop owns the pixie")
		}},
		{"barge_in_with_a_hold_only_interrupts_speech", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			c.Step(HoldOpenInput(true), now)
			eq(t, model(c, evSpeechStarted, now), ef(EfInterrupt), pixie(StateListening))
			if !c.IsActive() {
				t.Fatal("ended")
			}
		}},
		{"stale_response_done_after_new_turn_does_not_end_it", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			model(c, evSpeechStarted, now)
			model(c, evSpeechStopped, now)
			empty(t, model(c, evResponseDone, now), "stale done of the cancelled reply")
			phase(t, c, PhaseThinking)
			model(c, evResponseCreated, now)
			model(c, evAudio(frame()), now)
			model(c, evResponseDone, now)
			eq(t, tick(c, true, ms(now, 500)), pixie(StateListening))
			listeningArmed(t, c, ms(now, 500))
		}},
		{"tool_done_before_response_done_waits_for_follow_up", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			model(c, evResponseCreated, now)
			model(c, toolCall("lookup"), now)
			c.Step(ToolDoneInput("c1", false), now)
			empty(t, model(c, evResponseDone, now))
			phase(t, c, PhaseThinking)
			model(c, evResponseCreated, now)
			model(c, evAudio(frame()), now)
			phase(t, c, PhaseSpeaking)
		}},
		{"follow_up_response_re_marks_playback", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			model(c, toolCall("lookup"), now)
			model(c, evResponseDone, now)
			c.Step(ToolDoneInput("c1", false), now)
			phase(t, c, PhaseSpeaking)
			model(c, evResponseCreated, now)
			eq(t, model(c, evAudio(frame()), now), ef(EfResponseStarted), play(frame()))
			eq(t, model(c, evAudio(frame()), now), play(frame()))
		}},
		{"thinking_watchdog_returns_to_listening", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			empty(t, tick(c, true, ms(now, 14_900)))
			phase(t, c, PhaseThinking)
			eq(t, tick(c, true, ms(now, 15_000)), pixie(StateListening))
			listeningArmed(t, c, ms(now, 15_000))
		}},
		{"goodbye_interrupted_by_user_does_not_end", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			model(c, toolCall("end_conversation"), now)
			model(c, evResponseDone, now)
			model(c, evSpeechStarted, now)
			empty(t, c.Step(ToolDoneInput("c1", true), now))
			listeningTalking(t, c)
		}},
		{"silence_timeout_waits_for_pending_tools", func(t *testing.T) {
			now := t0()
			c := listening(now)
			model(c, toolCall("lookup"), now)
			empty(t, tick(c, true, ms(now, 8_000)))
			phase(t, c, PhaseListening)
			c.Step(ToolDoneInput("c1", false), ms(now, 8_000))
			// The tool result re-armed the timer at +8 s.
			eq(t, tick(c, true, ms(now, 15_000)), ended()...)
		}},
		{"watchdog_restarts_after_tool_result", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			model(c, evResponseCreated, now)
			model(c, toolCall("lookup"), now)
			model(c, evResponseDone, now)
			c.Step(ToolDoneInput("c1", false), ms(now, 16_000))
			empty(t, tick(c, true, ms(now, 16_100)))
			phase(t, c, PhaseThinking)
			eq(t, tick(c, true, ms(now, 31_000)), pixie(StateListening))
			phase(t, c, PhaseListening)
		}},
		{"speaking_with_no_follow_up_recovers", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			model(c, toolCall("lookup"), now)
			model(c, evResponseDone, now)
			c.Step(ToolDoneInput("c1", false), now)
			phase(t, c, PhaseSpeaking)
			empty(t, tick(c, false, ms(now, 1_000)), "still playing")
			empty(t, tick(c, true, ms(now, 2_000)))
			empty(t, tick(c, true, ms(now, 16_900)))
			eq(t, tick(c, true, ms(now, 17_000)), pixie(StateListening))
			listeningArmed(t, c, ms(now, 17_000))
		}},
		{"new_question_cancels_pending_goodbye", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			model(c, evResponseCreated, now)
			model(c, toolCall("end_conversation"), now)
			model(c, evSpeechStarted, now)
			c.Step(ToolDoneInput("c1", true), now)
			model(c, evSpeechStopped, now)
			model(c, evResponseDone, now)
			model(c, evResponseCreated, now)
			model(c, evAudio(frame()), now)
			model(c, evResponseDone, now)
			fx := tick(c, true, ms(now, 500))
			if contains(fx, ef(EfClose)) {
				t.Fatalf("%+v", fx)
			}
			phase(t, c, PhaseListening)
		}},
		{"silence_timer_restarts_after_tool_result", func(t *testing.T) {
			now := t0()
			c := listening(now)
			model(c, toolCall("lookup"), now)
			c.Step(ToolDoneInput("c1", false), ms(now, 6_000))
			empty(t, tick(c, true, ms(now, 12_999)), "7 s count from the tool result")
			eq(t, tick(c, true, ms(now, 13_000)), ended()...)
		}},
		{"goodbye_from_an_earlier_turn_is_ignored", func(t *testing.T) {
			now := t0()
			c := speaking(now)
			model(c, toolCall("end_conversation"), now)
			model(c, evResponseDone, now)
			// The user talks over the goodbye and finishes a new question before the tool returns.
			model(c, evSpeechStarted, now)
			model(c, evSpeechStopped, now)
			empty(t, c.Step(ToolDoneInput("c1", true), now))
			model(c, evResponseCreated, now)
			model(c, evAudio(frame()), now)
			model(c, evResponseDone, now)
			// The new answer must not end the conversation.
			eq(t, tick(c, true, ms(now, 500)), pixie(StateListening))
		}},
		{"goodbye_from_the_current_turn_still_ends_after_a_late_result", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			model(c, evResponseCreated, now)
			model(c, toolCall("end_conversation"), now)
			model(c, evResponseDone, now)
			eq(t, c.Step(ToolDoneInput("c1", true), ms(now, 900)), ended()...)
		}},
		{"an_unknown_call_cannot_end_the_conversation", func(t *testing.T) {
			now := t0()
			c := thinking(now)
			model(c, evResponseCreated, now)
			model(c, toolCall("end_conversation"), now)
			model(c, evResponseDone, now)
			fx := c.Step(ToolDoneInput("other", true), now)
			if contains(fx, ef(EfClose)) {
				t.Fatalf("%+v", fx)
			}
		}},
		{"the_silence_timer_waits_for_the_mic", func(t *testing.T) {
			// First run: macOS shows the mic prompt and the user takes 10 s to answer.
			now := t0()
			c := newConv()
			c.Step(HotkeyInput(), now)
			model(c, evReady, now)
			empty(t, tick(c, true, ms(now, 10_000)), "no timeout while the prompt shows")
			empty(t, c.Step(MicLiveInput(), ms(now, 10_000)))
			listeningArmed(t, c, ms(now, 10_000)) // the timer starts with the mic
			empty(t, tick(c, true, ms(now, 16_999)))
			eq(t, tick(c, true, ms(now, 17_000)), ended()...)
		}},
		{"an_unanswered_mic_prompt_ends_the_conversation_after_a_minute", func(t *testing.T) {
			now := t0()
			c := newConv()
			c.Step(HotkeyInput(), now)
			model(c, evReady, now)
			empty(t, tick(c, true, now.Add(MicPermissionWait-time.Millisecond)))
			fx := tick(c, true, now.Add(MicPermissionWait))
			if !contains(fx, status("Voice: no answer to the microphone prompt")) {
				t.Fatalf("%+v", fx)
			}
			if !contains(fx, ef(EfStopMic)) || !contains(fx, ef(EfClose)) {
				t.Fatalf("%+v", fx)
			}
			phase(t, c, PhaseCooldown)
		}},
		{"a_mic_that_went_on_is_never_timed_out_by_the_prompt_limit", func(t *testing.T) {
			now := t0()
			c := listening(now)
			model(c, evSpeechStarted, now)
			empty(t, tick(c, true, now.Add(MicPermissionWait*2)), "talking for two minutes is fine")
		}},
		{"the_mic_goes_live_again_in_the_next_conversation_only_when_told", func(t *testing.T) {
			now := t0()
			c := listening(now)
			eq(t, tick(c, true, ms(now, 7_000)), ended()...)
			c.Step(HotkeyInput(), ms(now, 8_000))
			model(c, evReady, ms(now, 8_000))
			empty(t, tick(c, true, ms(now, 20_000)), "a new conversation waits for its own mic")
		}},
	}
	// conversation.rs has 52 #[test]s; keep this in step with it.
	if len(cases) != 52 {
		t.Fatalf("want 52 ported cases, have %d", len(cases))
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		if seen[tc.name] {
			t.Fatalf("duplicate case %s", tc.name)
		}
		seen[tc.name] = true
		t.Run(tc.name, tc.run)
	}
}

// Brief-mandated defaults (context.md): idle 7 s, hold-open 180 s, and an unset hold timeout
// falls back to the idle timeout as in Rust's Conversation::new.
func TestConversationConfigDefaults(t *testing.T) {
	d := DefaultConversationConfig()
	if d.IdleTimeout != 7*time.Second || d.HoldTimeout != 180*time.Second {
		t.Fatalf("%+v", d)
	}
	if ReplyWatchdog != 15*time.Second || MicPermissionWait != 60*time.Second || ErrorCooldown != 2*time.Second {
		t.Fatal(fmt.Sprint(ReplyWatchdog, MicPermissionWait, ErrorCooldown))
	}
	// HoldTimeout zero => same as idle: a hold does not stretch the 7 s timeout.
	now := t0()
	c := listening(now)
	c.Step(HoldOpenInput(true), now)
	fx := tick(c, true, ms(now, 7_000))
	if !contains(fx, ef(EfClose)) || !contains(fx, pixie(StateDelegating)) {
		t.Fatalf("%+v", fx)
	}
}

// A tool that ends after the reply (delegate): the acknowledgement plays, then the conversation
// ends, unless the user starts talking first. Not a ported case.
func TestEndAfterReply(t *testing.T) {
	t.Run("end_after_reply_plays_the_acknowledgement_then_ends", func(t *testing.T) {
		now := t0()
		c := thinking(now)
		model(c, evResponseCreated, now)
		model(c, evToolCall("c1", "delegate"), now)
		empty(t, model(c, evResponseDone, now))
		empty(t, c.Step(ToolDoneAfterReplyInput("c1"), now), "waits for the acknowledgement")
		phase(t, c, PhaseThinking)
		model(c, evResponseCreated, now)
		model(c, evAudio(frame()), now)
		empty(t, model(c, evResponseDone, now))
		empty(t, tick(c, false, ms(now, 300)), "acknowledgement still playing")
		eq(t, tick(c, true, ms(now, 600)), ended()...)
	})
	t.Run("end_after_reply_is_cancelled_when_the_user_talks", func(t *testing.T) {
		now := t0()
		c := thinking(now)
		model(c, evResponseCreated, now)
		model(c, evToolCall("c1", "delegate"), now)
		model(c, evResponseDone, now)
		c.Step(ToolDoneAfterReplyInput("c1"), now)
		model(c, evResponseCreated, now)
		model(c, evAudio(frame()), now)
		model(c, evSpeechStarted, now)
		model(c, evResponseDone, now)
		tick(c, true, ms(now, 600))
		phase(t, c, PhaseListening)
	})
}
