package voice_test

// End-to-end runtime scenarios on fakes, in synctest's fake time (tokio's start_paused).
// Port of the 25 #[tokio::test]s in ai-mascot crates/voice-core/src/runtime_tests.rs, same
// names in CamelCase. Differences from Rust, all from the Go runtime's shape:
//   - Swift pushes the mic (PushMic) and pulls playback (PullPlayback), so the rig drives a
//     voicetest.FakeMic (which, like a real mic, sends silence while on) and a
//     voicetest.Player; "PlayEvent" comes from the playback queue's enqueue/flush hook.
//   - The mascot sink is the UI event stream (voicetest.Sink).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
	vt "github.com/sihekuang/deskpixie/core/voice/voicetest"
)

const (
	idle       = voice.StateIdle
	listening  = voice.StateListening
	thinking   = voice.StateThinking
	delegating = voice.StateDelegating
	errState   = voice.StateError
)

type playEvent struct {
	n     int
	flush bool
}

func play(n int) playEvent { return playEvent{n: n} }

var flushEv = playEvent{flush: true}

type playLog struct {
	mu  sync.Mutex
	evs []playEvent
}

func (l *playLog) hook(n int, flush bool) {
	l.mu.Lock()
	l.evs = append(l.evs, playEvent{n, flush})
	l.mu.Unlock()
}

func (l *playLog) log() []playEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.evs)
}

// slowTool takes one second to answer.
type slowTool struct{}

func (slowTool) Name() string                { return "slow" }
func (slowTool) Description() string         { return "Slow." }
func (slowTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (slowTool) Invoke(ctx context.Context, _ json.RawMessage) voice.ToolOutcome {
	select {
	case <-time.After(time.Second):
	case <-ctx.Done():
	}
	return voice.ToolOutcome{Output: json.RawMessage(`{"slow":true}`)}
}

type fixedSecret struct{ key string }

func (f fixedSecret) APIKey(string) (voice.Secret, bool) { return voice.NewSecret(f.key), f.key != "" }
func (fixedSecret) KeyHint(string) string                { return "" }

type rig struct {
	r        *voice.Runtime
	model    *vt.ScriptedModel
	mic      *vt.FakeMic
	playback *playLog
	sink     *vt.Sink
}

type rigOpts struct {
	secrets   voice.SecretSource
	mic       vt.MicMode
	micErr    voice.AudioError
	noChimes  bool
	facts     voice.SessionFacts
	recordDir string
	cleaner   audio.Cleaner
}

func newRig(t *testing.T, model *vt.ScriptedModel, o rigOpts) *rig {
	t.Helper()
	if o.secrets == nil {
		o.secrets = fixedSecret{"test-key"}
	}
	tools := voice.DefaultTools()
	tools.Register(slowTool{})
	pl := &playLog{}
	sink := &vt.Sink{}
	cfg := voice.WithPlayHook(voice.RuntimeConfig{
		Session: voice.SessionConfig{Model: "test-model", Voice: "marin", Instructions: "Be brief.",
			TurnDetection: voice.TurnSemantic},
		Secrets:      o.secrets,
		Tools:        tools,
		Facts:        o.facts,
		Chimes:       !o.noChimes,
		RecordDir:    o.recordDir,
		MouthLevels:  4,
		Conversation: voice.ConversationConfig{IdleTimeout: 7 * time.Second, HoldTimeout: 180 * time.Second},
	}, pl.hook)
	r := voice.NewRuntime(model, o.cleaner, cfg, sink.Emit)
	mic := vt.NewFakeMic(o.mic)
	mic.FailWith = o.micErr
	mic.Attach(r, sink)
	player := vt.StartPlayer(r)
	t.Cleanup(func() {
		mic.Stop()
		player.Stop()
		r.Close()
	})
	return &rig{r: r, model: model, mic: mic, playback: pl, sink: sink}
}

func wait(ms int) { time.Sleep(time.Duration(ms) * time.Millisecond) }

func chimeLen(k voice.Chime) int { return len(voice.ChimeSamples(k)) }

func eqStates(t *testing.T, got []voice.MascotState, want ...voice.MascotState) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("states %v, want %v", got, want)
	}
}

func last[T any](s []T) (T, bool) {
	var zero T
	if len(s) == 0 {
		return zero, false
	}
	return s[len(s)-1], true
}

func lastState(r *rig) voice.MascotState { s, _ := last(r.sink.States()); return s }

func hasStatus(r *rig, s string) bool { return slices.Contains(r.sink.Statuses(), s) }

func cmdKinds(cs []voice.Command) []voice.CommandKind {
	var out []voice.CommandKind
	for _, c := range cs {
		out = append(out, c.Kind)
	}
	return out
}

func heardOf(t *testing.T, cs []voice.Command) time.Duration {
	t.Helper()
	for _, c := range cs {
		if c.Kind == voice.CmdCancelResponse {
			return time.Duration(c.HeardMs) * time.Millisecond
		}
	}
	t.Fatal("CancelResponse not sent")
	return 0
}

// run wraps a scenario in a synctest bubble.
func run(t *testing.T, f func(t *testing.T)) {
	synctest.Test(t, f)
}

func TestANormalTurn(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.Audio(5)), rigOpts{})
		r.r.Hotkey()
		wait(100)
		eqStates(t, r.sink.States(), thinking, listening)
		if !r.mic.IsOpen() {
			t.Fatal("mic not open")
		}
		if got := r.playback.log()[0]; got != play(chimeLen(voice.ChimeStart)) {
			t.Fatalf("first play %v", got)
		}
		r.mic.Say(500)
		wait(1500)
		eqStates(t, r.sink.States(), thinking, listening, thinking, idle, listening)
		mouths := r.sink.Mouths()
		if !slices.ContainsFunc(mouths, func(m int) bool { return m > 0 }) {
			t.Fatalf("mouth never moved: %v", mouths)
		}
		if m, _ := last(mouths); m != 0 {
			t.Fatalf("mouth left open: %v", mouths)
		}
		if !slices.ContainsFunc(r.model.Commands(), func(c voice.Command) bool { return c.Kind == voice.CmdAudio }) {
			t.Fatal("no audio reached the model")
		}
	})
}

func TestSilenceTimeoutEndsWithEndChime(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{})
		r.r.Hotkey()
		wait(100)
		wait(7000)
		eqStates(t, r.sink.States(), thinking, listening, idle)
		if l, _ := last(r.playback.log()); l != play(chimeLen(voice.ChimeEnd)) {
			t.Fatalf("last play %v", l)
		}
		if k := cmdKinds(r.model.NonAudio()); !slices.Equal(k, []voice.CommandKind{voice.CmdClose}) {
			t.Fatalf("commands %v", k)
		}
		if r.mic.IsOpen() {
			t.Fatal("mic still open")
		}
	})
}

func TestTalkingOverBitCancelsWithWhatWasHeard(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.Audio(30)), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.mic.Say(500)
		wait(400)
		r.mic.Say(300)
		wait(500)
		heard := heardOf(t, r.model.NonAudio())
		if heard < 300*time.Millisecond || heard > 700*time.Millisecond {
			t.Fatalf("heard %v", heard)
		}
		if !slices.Contains(r.playback.log(), flushEv) {
			t.Fatal("no flush")
		}
		st := r.sink.States()
		if len(st) < 5 {
			t.Fatalf("states %v", st)
		}
		eqStates(t, st[:5], thinking, listening, thinking, idle, listening)
	})
}

func TestGoodbyeEndsAfterItPlays(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.AudioThenTool(3, "end_conversation")), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.mic.Say(500)
		wait(1500)
		if lastState(r) != idle {
			t.Fatalf("states %v", r.sink.States())
		}
		if l, _ := last(r.playback.log()); l != play(chimeLen(voice.ChimeEnd)) {
			t.Fatalf("last play %v", l)
		}
		if slices.Contains(r.playback.log(), flushEv) {
			t.Fatal("the goodbye was cut off")
		}
		cs := r.model.NonAudio()
		if len(cs) != 2 || cs[0].Kind != voice.CmdToolResult || cs[0].CallID != "call_1" ||
			string(cs[0].Output) != `{"ok":true}` || cs[0].Respond || cs[1].Kind != voice.CmdClose {
			t.Fatalf("commands %+v", cs)
		}
	})
}

func TestHotkeyEndsMidReply(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.Audio(30)), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.mic.Say(500)
		wait(300)
		r.r.Hotkey()
		wait(100)
		log := r.playback.log()
		if n := len(log); n < 2 || log[n-2] != flushEv || log[n-1] != play(chimeLen(voice.ChimeEnd)) {
			t.Fatalf("log tail %v", log)
		}
		if lastState(r) != idle {
			t.Fatalf("states %v", r.sink.States())
		}
		if c, _ := last(r.model.NonAudio()); c.Kind != voice.CmdClose {
			t.Fatalf("last command %+v", c)
		}
	})
}

func TestMissingKeyShowsErrorThenIdle(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{secrets: fixedSecret{""}})
		r.r.Hotkey()
		wait(100)
		eqStates(t, r.sink.States(), thinking, errState)
		if !hasStatus(r, "Voice: no API key") {
			t.Fatalf("no hint from fixedSecret: %v", r.sink.Statuses())
		}
		if r.model.Connects() != 0 {
			t.Fatal("connected without a key")
		}
		wait(2100)
		if lastState(r) != idle {
			t.Fatalf("states %v", r.sink.States())
		}
	})
}

// noKeyHinting has no key but knows which variable would hold it.
type noKeyHinting string

func (noKeyHinting) APIKey(string) (voice.Secret, bool) { return voice.Secret{}, false }
func (n noKeyHinting) KeyHint(provider string) string {
	if provider == "scripted" {
		return string(n)
	}
	return ""
}

func TestMissingKeyStatusNamesTheProvidersVariable(t *testing.T) {
	for _, v := range []string{"OPENAI_API_KEY", "GEMINI_API_KEY"} {
		run(t, func(t *testing.T) {
			r := newRig(t, vt.NewScriptedModel(), rigOpts{secrets: noKeyHinting(v)})
			r.r.Hotkey()
			wait(100)
			eqStates(t, r.sink.States(), thinking, errState)
			if want := fmt.Sprintf("Voice: no API key (set %s)", v); !hasStatus(r, want) {
				t.Fatalf("%v", r.sink.Statuses())
			}
			if r.model.Connects() != 0 {
				t.Fatal("connected without a key")
			}
		})
	}
}

func TestConnectFailureShowsConnectionLost(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.FailingModel(), rigOpts{})
		r.r.Hotkey()
		wait(100)
		eqStates(t, r.sink.States(), thinking, errState)
		if !hasStatus(r, "Voice: connection lost") {
			t.Fatalf("%v", r.sink.Statuses())
		}
	})
}

func TestServerHangupShowsError(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.model.HangUp()
		wait(100)
		if lastState(r) != errState || !hasStatus(r, "Voice: connection lost") {
			t.Fatalf("states %v statuses %v", r.sink.States(), r.sink.Statuses())
		}
		if r.mic.IsOpen() {
			t.Fatal("mic still open")
		}
	})
}

func TestMicFailureMidConversationShowsError(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.mic.Fail(voice.AudioError{Kind: voice.AudioDeviceChanged})
		wait(100)
		if lastState(r) != errState || !hasStatus(r, "Voice: audio device changed") {
			t.Fatalf("states %v statuses %v", r.sink.States(), r.sink.Statuses())
		}
		if c, _ := last(r.model.NonAudio()); c.Kind != voice.CmdClose {
			t.Fatalf("last command %+v", c)
		}
	})
}

func TestMicPermissionDeniedOnStart(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{secrets: fixedSecret{"k"}, mic: vt.MicFailing,
			micErr: voice.AudioError{Kind: voice.AudioPermissionDenied}})
		r.r.Hotkey()
		wait(100)
		if !hasStatus(r, "Voice: microphone access denied") || lastState(r) != errState {
			t.Fatalf("states %v statuses %v", r.sink.States(), r.sink.Statuses())
		}
	})
}

func TestLateSessionAfterCancelIsClosed(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel().WithConnectDelay(500*time.Millisecond), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.r.Hotkey() // cancel while connecting
		wait(1000)
		eqStates(t, r.sink.States(), thinking, idle)
		if r.model.Connects() != 1 {
			t.Fatalf("connects %d", r.model.Connects())
		}
		if k := cmdKinds(r.model.NonAudio()); !slices.Equal(k, []voice.CommandKind{voice.CmdClose}) {
			t.Fatalf("the late session was not closed: %v", k)
		}
		r.r.Hotkey()
		wait(700)
		if lastState(r) != listening {
			t.Fatalf("a fresh press still works: %v", r.sink.States())
		}
	})
}

func TestChimesCanBeTurnedOff(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{secrets: fixedSecret{"k"}, noChimes: true})
		r.r.Hotkey()
		wait(100)
		r.r.Hotkey()
		wait(100)
		for _, e := range r.playback.log() {
			if e != flushEv {
				t.Fatalf("no chimes: %v", r.playback.log())
			}
		}
	})
}

func TestStaleToolResultFromPreviousConversationIsDropped(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.Tool("slow")), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.mic.Say(500) // the reply asks for the slow tool just before this returns
		wait(100)
		r.r.Hotkey() // end the conversation while the tool is still running
		wait(100)
		r.r.Hotkey() // a new conversation
		wait(2000)   // the old tool finishes meanwhile
		if r.model.Connects() != 2 {
			t.Fatalf("connects %d", r.model.Connects())
		}
		for _, c := range r.model.NonAudio() {
			if c.Kind == voice.CmdToolResult {
				t.Fatalf("stale result reached a session: %+v", c)
			}
		}
		if lastState(r) != listening {
			t.Fatalf("the new conversation is affected: %v", r.sink.States())
		}
	})
}

func TestHeardCountsOnlyTheFollowUpResponseAfterATool(t *testing.T) {
	run(t, func(t *testing.T) {
		// Reply 1: 0.5 s of audio then a slow tool (1 s); reply 2 (follow-up): 3 s of audio.
		r := newRig(t, vt.NewScriptedModel(vt.AudioThenTool(5, "slow"), vt.Audio(30)), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.mic.Say(500)
		wait(1800) // reply 1 played, tool done, follow-up started ~0.3 s ago
		if lastState(r) != idle {
			t.Fatalf("speaking the follow-up: %v", r.sink.States())
		}
		r.mic.Say(300)
		wait(500)
		if heard := heardOf(t, r.model.NonAudio()); heard >= 800*time.Millisecond {
			t.Fatalf("heard %v must not include the first response's 500 ms", heard)
		}
	})
}

// ---------- slice 4: inject, hold-open, signals ----------

func TestInjectIsSentOnceQuietAndBitAnswers(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.Audio(3)), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.r.Inject(voice.Notice{Text: "Agent reply: the tests pass."})
		wait(1000)
		var injected []voice.Command
		for _, c := range r.model.NonAudio() {
			if c.Kind == voice.CmdInject {
				injected = append(injected, c)
			}
		}
		if len(injected) != 1 || injected[0].Text != "Agent reply: the tests pass." || !injected[0].Respond {
			t.Fatalf("injected %+v", injected)
		}
		plays := 0
		for _, e := range r.playback.log() {
			if !e.flush {
				plays++
			}
		}
		if plays <= 1 {
			t.Fatal("Bit did not speak the reply")
		}
		if lastState(r) != listening {
			t.Fatalf("states %v", r.sink.States())
		}
	})
}

func recvSignal(t *testing.T, ch <-chan voice.VoiceSignal) voice.VoiceSignal {
	t.Helper()
	select {
	case s := <-ch:
		return s
	default:
		t.Fatal("no signal")
	}
	return voice.VoiceSignal{}
}

func TestSignalsReportOpenAndCloseWithUndeliveredNotices(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{})
		signals := r.r.Subscribe()
		r.r.Hotkey()
		wait(100)
		if s := recvSignal(t, signals); s.Kind != voice.SigOpened {
			t.Fatalf("got %+v", s)
		}
		r.r.Hotkey()
		wait(100)
		if s := recvSignal(t, signals); s.Kind != voice.SigClosed || len(s.Undelivered) != 0 {
			t.Fatalf("got %+v", s)
		}
		r.r.Inject(voice.Notice{Text: "too late"})
		wait(100)
		if s := recvSignal(t, signals); s.Kind != voice.SigClosed ||
			!slices.Equal(s.Undelivered, []voice.Notice{{Text: "too late"}}) {
			t.Fatalf("got %+v", s)
		}
	})
}

func TestHoldOpenKeepsTheConversationPastSevenSeconds(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{})
		r.r.HoldOpen(true)
		r.r.Hotkey()
		wait(100)
		wait(10_000)
		if !r.mic.IsOpen() {
			t.Fatal("not listening after 10 s")
		}
		r.r.HoldOpen(false)
		wait(7100)
		if r.mic.IsOpen() {
			t.Fatal("still open 7 s after release")
		}
		if lastState(r) != idle {
			t.Fatalf("states %v", r.sink.States())
		}
	})
}

func TestHoldOpenWhileClosedRestsInDelegating(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{})
		r.r.HoldOpen(true)
		wait(100)
		eqStates(t, r.sink.States(), delegating)
		r.r.HoldOpen(false)
		wait(100)
		eqStates(t, r.sink.States(), delegating, idle)
	})
}

func TestAnUnansweredMicPromptGivesUpAfterAMinute(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{mic: vt.MicAwaitingPermission})
		r.r.Hotkey()
		wait(59_000)
		eqStates(t, r.sink.States(), thinking, listening)
		wait(2000)
		if !hasStatus(r, "Voice: no answer to the microphone prompt") || lastState(r) != errState {
			t.Fatalf("states %v statuses %v", r.sink.States(), r.sink.Statuses())
		}
		if r.r.IsOpen() {
			t.Fatal("still open")
		}
	})
}

func TestASlowAnswerToTheMicPromptStillOpensTheConversation(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.Audio(5)), rigOpts{mic: vt.MicAwaitingPermission})
		r.r.Hotkey()
		wait(10_000) // the user reads the prompt for 10 s
		eqStates(t, r.sink.States(), thinking, listening)
		r.mic.Grant()
		wait(100)
		r.mic.Say(500)
		wait(1500)
		eqStates(t, r.sink.States(), thinking, listening, thinking, idle, listening)
		wait(7000)
		if lastState(r) != idle {
			t.Fatalf("the silence timeout no longer applies: %v", r.sink.States())
		}
		if r.mic.IsOpen() {
			t.Fatal("mic still open")
		}
	})
}

func TestTheHandleKnowsWhetherAConversationIsOpen(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{})
		if r.r.IsOpen() {
			t.Fatal("open before the hotkey")
		}
		r.r.Hotkey()
		wait(100)
		if !r.r.IsOpen() {
			t.Fatal("not open")
		}
		r.r.Hotkey()
		wait(100)
		if r.r.IsOpen() {
			t.Fatal("still open")
		}
	})
}

func TestAPackSwitchChangesTheMouthRange(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.Audio(5), vt.Audio(5)), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.mic.Say(500)
		wait(1500)
		if m := slices.Max(r.sink.Mouths()); m != 3 {
			t.Fatalf("a 4-level pack opens fully: max %d", m)
		}
		r.r.SetMouthLevels(2)
		before := len(r.sink.Mouths())
		r.mic.Say(500)
		wait(1500)
		after := r.sink.Mouths()[before:]
		if !slices.ContainsFunc(after, func(m int) bool { return m > 0 }) {
			t.Fatalf("mouth did not move: %v", after)
		}
		if slices.Max(after) > 1 {
			t.Fatalf("a 2-level pack went past level 1: %v", after)
		}
	})
}

// countingFacts counts how often it was asked, so each connect visibly gets fresh facts.
type countingFacts struct{ n atomic.Int32 }

func (c *countingFacts) Facts() string { return fmt.Sprintf("Facts #%d.", c.n.Add(1)) }

func TestEachConnectAppendsFreshFactsToTheInstructions(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{secrets: fixedSecret{"k"}, facts: &countingFacts{}})
		r.r.Hotkey()
		wait(100)
		r.r.Hotkey()
		wait(3000)
		r.r.Hotkey()
		wait(100)
		want := []string{"Be brief.\n\nFacts #1.", "Be brief.\n\nFacts #2."}
		if got := r.model.Instructions(); !slices.Equal(got, want) {
			t.Fatalf("instructions %q", got)
		}
	})
}

type blankFacts struct{}

func (blankFacts) Facts() string { return "  \n" }

func TestBlankFactsLeaveTheInstructionsAlone(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{secrets: fixedSecret{"k"}, facts: blankFacts{}})
		r.r.Hotkey()
		wait(100)
		if got := r.model.Instructions(); !slices.Equal(got, []string{"Be brief."}) {
			t.Fatalf("instructions %q", got)
		}
	})
}

// ---------- Go additions ----------

// The registry's specs, sorted, are what the session is configured with.
func TestTheSessionGetsTheRegisteredToolSpecs(t *testing.T) {
	run(t, func(t *testing.T) {
		m := &specModel{ScriptedModel: vt.NewScriptedModel()}
		tools := voice.DefaultTools()
		tools.Register(slowTool{})
		rt := voice.NewRuntime(m, nil, voice.RuntimeConfig{
			Session: voice.SessionConfig{Model: "x", Tools: []voice.ToolSpec{{Name: "ignored"}}},
			Secrets: fixedSecret{"k"}, Tools: tools, MouthLevels: 4,
			Conversation: voice.DefaultConversationConfig(),
		}, nil)
		defer rt.Close()
		rt.Hotkey()
		wait(100)
		m.mu.Lock()
		got := m.names
		m.mu.Unlock()
		if !slices.Equal(got, []string{"end_conversation", "slow"}) {
			t.Fatalf("tools %v", got)
		}
		rt.Hotkey()
		wait(100)
	})
}

type specModel struct {
	*vt.ScriptedModel
	mu    sync.Mutex
	names []string
}

func (m *specModel) Connect(ctx context.Context, cfg voice.SessionConfig, key string) (voice.Session, error) {
	m.mu.Lock()
	m.names = nil
	for _, s := range cfg.Tools {
		m.names = append(m.names, s.Name)
	}
	m.mu.Unlock()
	return m.ScriptedModel.Connect(ctx, cfg, key)
}

// An unknown tool gets an error result and the model is asked to go on.
func TestAnUnknownToolGetsAnErrorResult(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.Tool("nope"), vt.Audio(2)), rigOpts{})
		r.r.Hotkey()
		wait(100)
		r.mic.Say(500)
		wait(1000)
		var res []voice.Command
		for _, c := range r.model.NonAudio() {
			if c.Kind == voice.CmdToolResult {
				res = append(res, c)
			}
		}
		if len(res) != 1 || string(res[0].Output) != `{"error":"unknown tool nope"}` || !res[0].Respond {
			t.Fatalf("results %+v", res)
		}
	})
}

// Subscribers' channels close with the runtime.
func TestSubscribersCloseWithTheRuntime(t *testing.T) {
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{})
		ch := r.r.Subscribe()
		r.r.Close()
		if _, ok := <-ch; ok {
			t.Fatal("signal after close")
		}
		if _, ok := <-r.r.Subscribe(); ok {
			t.Fatal("subscribe after close is open")
		}
	})
}

// halver is a stand-in noise filter: the model gets its output, the recording both.
type halver struct{}

func (halver) Process(in []int16) []int16 {
	out := make([]int16, len(in))
	for i, v := range in {
		out[i] = v / 2
	}
	return out
}
func (halver) Close() error { return nil }

func wavFiles(dir string) []string {
	m, _ := filepath.Glob(filepath.Join(dir, "*.wav"))
	slices.Sort(m)
	return m
}

// Rust recording.rs frames_reach_the_runtime_and_the_file +
// the_unfiltered_mic_goes_to_a_matching_file_and_not_to_the_model, at the runtime.
func TestRecordingFramesReachTheModelAndTheFile(t *testing.T) {
	dir := t.TempDir()
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(), rigOpts{recordDir: dir, cleaner: halver{}})
		r.r.Hotkey()
		wait(300)
		r.r.Hotkey() // end: the take finishes its files
		wait(100)
		sent := 0
		for _, c := range r.model.Commands() {
			if c.Kind == voice.CmdAudio {
				sent++
			}
		}
		if sent == 0 {
			t.Fatal("no audio reached the model")
		}
	})
	files := wavFiles(dir)
	if len(files) != 2 {
		t.Fatalf("files %v", files)
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if len(b) <= 44 {
			t.Fatalf("%s is empty", f)
		}
	}
}

// Rust recording.rs an_unwritable_folder_still_passes_the_mic_through, at the runtime.
func TestRecordingUnwritableFolderStillPassesTheMicThrough(t *testing.T) {
	base := filepath.Join(t.TempDir(), "blocked")
	_ = os.WriteFile(base, []byte("a file, not a folder"), 0o600)
	run(t, func(t *testing.T) {
		r := newRig(t, vt.NewScriptedModel(vt.Audio(2)), rigOpts{recordDir: filepath.Join(base, "sub")})
		r.r.Hotkey()
		wait(100)
		r.mic.Say(500)
		wait(1000)
		eqStates(t, r.sink.States(), thinking, listening, thinking, idle, listening)
	})
}
