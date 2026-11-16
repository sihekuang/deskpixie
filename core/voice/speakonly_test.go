package voice

import (
	"testing"
	"time"
)

// #433: a conversation opened only to speak (agent.listenAfterUpdates).

// openedToSpeak is a speak-only conversation, ready at t0+20ms.
func openedToSpeak(t *testing.T) *Conversation {
	t.Helper()
	c := newConv()
	eq(t, c.Step(OpenToSpeakInput(), t0()), ef(EfConnect), pixie(StateThinking))
	eq(t, model(c, evReady, ms(t0(), 20)),
		ef(EfStatus), pixie(StateIdle), signalFx(VoiceSignal{Kind: SigOpened}))
	return c
}

// sayIt plays Pixie's whole reply to an injected notice, starting at at; the effects of the
// tick that finds playback drained.
func sayIt(t *testing.T, c *Conversation, at time.Time) []Effect {
	t.Helper()
	model(c, evResponseCreated, at)
	model(c, evAudio(frame()), ms(at, 10))
	model(c, evResponseDone, ms(at, 20))
	return tick(c, true, ms(at, 30)) // playback drained: the turn ends
}

func TestSpeakOnlyOpensWithNoMicAndNoChime(t *testing.T) {
	c := openedToSpeak(t)
	phase(t, c, PhaseListening)
	// No mic was asked for: the mic permission wait never fails it.
	if fx := tick(c, true, ms(t0(), 20+int64(MicPermissionWait/time.Millisecond)+10)); hasKind(fx, EfStatus) {
		t.Fatalf("failed waiting for a mic it never asked for: %+v", fx)
	}
}

func TestSpeakOnlySaysItsNoticesThenEnds(t *testing.T) {
	c := openedToSpeak(t)
	fx := c.Step(InjectInput(notice("Coordinator reply: the build passed.")), ms(t0(), 30))
	eq(t, fx, injectFx("Coordinator reply: the build passed."+speakOnlyNote), pixie(StateThinking))
	sayIt(t, c, ms(t0(), 40))
	eq(t, tick(c, true, ms(t0(), 100)),
		ef(EfStopMic), ef(EfClose), pixie(StateIdle), closed()) // no end chime
	phase(t, c, PhaseIdle)
	if c.EndReason() != "said its updates" {
		t.Fatalf("end reason %q", c.EndReason())
	}
}

func TestSpeakOnlyNeverShowsListening(t *testing.T) {
	c := openedToSpeak(t)
	c.Step(InjectInput(notice("Coordinator reply: done.")), ms(t0(), 30))
	model(c, evResponseCreated, ms(t0(), 40))
	model(c, evAudio(frame()), ms(t0(), 50))
	model(c, evResponseDone, ms(t0(), 60))
	if fx := tick(c, true, ms(t0(), 70)); contains(fx, pixie(StateListening)) {
		t.Fatalf("showed listening with no mic: %+v", fx)
	}
}

func TestSpeakOnlySaysEveryNoticeBeforeEnding(t *testing.T) {
	c := openedToSpeak(t)
	c.Step(InjectInput(notice("Coordinator reply: one.")), ms(t0(), 30))
	c.Step(InjectInput(notice("Coordinator progress: editing two.go")), ms(t0(), 35))
	fx := sayIt(t, c, ms(t0(), 40))
	if !contains(fx, injectFx("Coordinator progress: editing two.go"+speakOnlyNote)) || hasKind(fx, EfClose) {
		t.Fatalf("second notice: %+v", fx)
	}
	sayIt(t, c, ms(t0(), 110))
	if fx := tick(c, true, ms(t0(), 200)); !hasKind(fx, EfClose) {
		t.Fatalf("didn't end once all was said: %+v", fx)
	}
}

func TestSpeakOnlyWaitsForItsNoticesAWhile(t *testing.T) {
	c := openedToSpeak(t)
	if fx := tick(c, true, ms(t0(), 20+int64(SpeakOnlyWait/time.Millisecond)-10)); hasKind(fx, EfClose) {
		t.Fatalf("ended before its notices could come: %+v", fx)
	}
	eq(t, tick(c, true, ms(t0(), 20+int64(SpeakOnlyWait/time.Millisecond))),
		ef(EfStopMic), ef(EfClose), pixie(StateIdle), closed())
}

// #443: a question is said with the mic off too, and the conversation ends once it is said.
func TestSpeakOnlyKeepsTheMicOffForAQuestion(t *testing.T) {
	c := openedToSpeak(t)
	q := "Coordinator question: deploy now or wait?"
	eq(t, c.Step(InjectInput(notice(q)), ms(t0(), 30)), injectFx(q+speakOnlyQuestionNote), pixie(StateThinking))
	if fx := sayIt(t, c, ms(t0(), 40)); hasKind(fx, EfStartMic) || contains(fx, pixie(StateListening)) {
		t.Fatalf("listened after a question: %+v", fx)
	}
	eq(t, tick(c, true, ms(t0(), 100)),
		ef(EfStopMic), ef(EfClose), pixie(StateIdle), closed()) // no end chime
	if c.EndReason() != "said its updates" {
		t.Fatalf("end reason %q", c.EndReason())
	}
}

func TestSpeakOnlyKeepsTheMicOffForAConfirmWord(t *testing.T) {
	c := openedToSpeak(t)
	n := "Coordinator wants to run rm -rf build. Say confirm hawk."
	eq(t, c.Step(InjectInput(notice(n)), ms(t0(), 30)), injectFx(n+speakOnlyQuestionNote), pixie(StateThinking))
	sayIt(t, c, ms(t0(), 40))
	if fx := tick(c, true, ms(t0(), 100)); !hasKind(fx, EfClose) || hasKind(fx, EfStartMic) {
		t.Fatalf("after a confirm word: %+v", fx)
	}
}

// A question said, the hotkey still turns the mic on to answer it, and it waits for the answer.
func TestSpeakOnlyHotkeyAfterAQuestionListensForTheAnswer(t *testing.T) {
	c := openedToSpeak(t)
	c.Step(InjectInput(notice("Coordinator question: deploy now or wait?")), ms(t0(), 30))
	model(c, evResponseCreated, ms(t0(), 40))
	model(c, evAudio(frame()), ms(t0(), 50))
	eq(t, c.Step(HotkeyInput(), ms(t0(), 60)), ef(EfStartMic))
	model(c, evResponseDone, ms(t0(), 70))
	tick(c, true, ms(t0(), 80))
	c.Step(MicLiveInput(), ms(t0(), 90))
	if fx := tick(c, true, ms(t0(), 1000)); hasKind(fx, EfClose) {
		t.Fatalf("ended without waiting for the answer: %+v", fx)
	}
	if fx := tick(c, true, ms(t0(), 90+int64(QuestionListenMin/time.Millisecond))); !contains(fx, chime(ChimeEnd)) {
		t.Fatalf("silence end: %+v", fx)
	}
}

func TestSpeakOnlyHotkeyTurnsTheMicOn(t *testing.T) {
	c := openedToSpeak(t)
	c.Step(InjectInput(notice("Coordinator reply: done.")), ms(t0(), 30))
	model(c, evResponseCreated, ms(t0(), 40))
	model(c, evAudio(frame()), ms(t0(), 50))
	// Mid-sentence: the mic comes on and Pixie keeps talking, with no chime cutting it in two.
	eq(t, c.Step(HotkeyInput(), ms(t0(), 60)), ef(EfStartMic))
	phase(t, c, PhaseSpeaking)
	model(c, evResponseDone, ms(t0(), 70))
	if fx := tick(c, true, ms(t0(), 80)); !contains(fx, pixie(StateListening)) || hasKind(fx, EfClose) {
		t.Fatalf("after the hotkey it listens: %+v", fx)
	}
	// A second press ends it as usual.
	if fx := c.Step(HotkeyInput(), ms(t0(), 90)); !hasKind(fx, EfClose) {
		t.Fatalf("second press: %+v", fx)
	}
}

func TestSpeakOnlyHotkeyWhileQuietListensAtOnce(t *testing.T) {
	c := openedToSpeak(t)
	eq(t, c.Step(HotkeyInput(), ms(t0(), 30)), chime(ChimeStart), ef(EfStartMic), pixie(StateListening))
	if fx := tick(c, true, ms(t0(), 20+int64(SpeakOnlyWait/time.Millisecond))); hasKind(fx, EfClose) {
		t.Fatalf("ended as speak-only after the hotkey: %+v", fx)
	}
}

func TestSpeakOnlyHotkeyWhileConnectingOpensTheMicWhenReady(t *testing.T) {
	c := newConv()
	c.Step(OpenToSpeakInput(), t0())
	empty(t, c.Step(HotkeyInput(), ms(t0(), 10)), "a press while connecting to speak")
	phase(t, c, PhaseConnecting)
	if fx := model(c, evReady, ms(t0(), 20)); !hasKind(fx, EfStartMic) || !contains(fx, chime(ChimeStart)) {
		t.Fatalf("ready after the hotkey: %+v", fx)
	}
}

func TestSpeakOnlyFailureEndsLikeAnyOther(t *testing.T) {
	c := openedToSpeak(t)
	fx := c.Step(FailedInput(Failure{Kind: FailConnectionLost}), ms(t0(), 30))
	phase(t, c, PhaseCooldown)
	if !hasKind(fx, EfClose) || contains(fx, chime(ChimeEnd)) {
		t.Fatalf("failure (no end chime with no start chime): %+v", fx)
	}
	// The next conversation is a usual one.
	tick(c, true, ms(t0(), 30+int64(ErrorCooldown/time.Millisecond)))
	c.Step(HotkeyInput(), ms(t0(), 30+int64(ErrorCooldown/time.Millisecond)+10))
	if fx := model(c, evReady, ms(t0(), 30+int64(ErrorCooldown/time.Millisecond)+20)); !hasKind(fx, EfStartMic) {
		t.Fatalf("next conversation has no mic: %+v", fx)
	}
}

func TestOpenAfterSpeakOnlyIsAUsualConversation(t *testing.T) {
	c := openedToSpeak(t)
	tick(c, true, ms(t0(), 20+int64(SpeakOnlyWait/time.Millisecond)))
	phase(t, c, PhaseIdle)
	c.Step(OpenInput(), ms(t0(), 6000))
	if fx := model(c, evReady, ms(t0(), 6010)); !hasKind(fx, EfStartMic) {
		t.Fatalf("open after a speak-only one: %+v", fx)
	}
}

func TestRuntimeOpenToSpeakStartsNoMic(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	if !r.OpenToSpeak() {
		t.Fatal("refused at rest")
	}
	eventually(t, "connect", func() bool { return len(m.connects()) == 1 })
	if r.OpenToSpeak() || r.Open() {
		t.Fatal("accepted while connecting")
	}
	m.sess.events <- Event{Kind: EvReady}
	eventually(t, "open", r.IsOpen)
	time.Sleep(20 * time.Millisecond)
	if ui.has(UIEvent{Kind: "mic", Text: "start"}) {
		t.Fatal("started the mic")
	}
}

func TestRuntimeOpenToSpeakAfterCloseIsRefused(t *testing.T) {
	r, _, _ := newTestRuntime(t, "sk-test")
	r.Close()
	if r.OpenToSpeak() {
		t.Fatal("accepted after Close")
	}
}

// The hotkey turns the mic on to answer: with the mic unusable the answer would be lost, so it
// doesn't open (the update stays in the bubble, as with Open).
func TestRuntimeOpenToSpeakRefusesWhileTheMicIsUnusable(t *testing.T) {
	r, m, _ := newTestRuntime(t, "sk-test")
	r.MicFailed("Microphone access denied")
	eventually(t, "mic failure recorded", func() bool { return r.micUnavailable.Load() != nil })
	if r.OpenToSpeak() {
		t.Fatal("accepted with the mic unusable")
	}
	time.Sleep(20 * time.Millisecond)
	if len(m.connects()) != 0 {
		t.Fatalf("connected %d times", len(m.connects()))
	}
}

// While a job holds the conversation open, Pixie rests delegating, not idle, and it still ends
// once said.
func TestSpeakOnlyWhileHeldRestsDelegatingAndEnds(t *testing.T) {
	c := newConv()
	c.Step(HoldOpenInput(true), t0())
	c.Step(OpenToSpeakInput(), ms(t0(), 1))
	eq(t, model(c, evReady, ms(t0(), 20)),
		ef(EfStatus), pixie(StateDelegating), signalFx(VoiceSignal{Kind: SigOpened}))
	c.Step(InjectInput(notice("Coordinator reply: done.")), ms(t0(), 30))
	if fx := sayIt(t, c, ms(t0(), 40)); contains(fx, pixie(StateIdle)) || contains(fx, pixie(StateListening)) {
		t.Fatalf("after its reply: %+v", fx)
	}
	if fx := tick(c, true, ms(t0(), 100)); !hasKind(fx, EfClose) || !contains(fx, pixie(StateDelegating)) {
		t.Fatalf("held, it didn't end once said: %+v", fx)
	}
}

// A silence timeout shorter than SpeakOnlyWait doesn't end it before its notices come.
func TestSpeakOnlyIgnoresAShortSilenceTimeout(t *testing.T) {
	cfg := DefaultConversationConfig()
	cfg.IdleTimeout = time.Second
	c := NewConversation(cfg)
	c.Step(OpenToSpeakInput(), t0())
	model(c, evReady, ms(t0(), 20))
	if fx := tick(c, true, ms(t0(), 3000)); hasKind(fx, EfClose) {
		t.Fatalf("ended on silence before its notices came: %+v", fx)
	}
}

// #436: nobody can answer, so Pixie is told to say the update and nothing more; a question is
// told to be said in full for later (#443); a usual conversation is given the notice as it is.
func TestSpeakOnlyTellsPixieToSayOnlyTheUpdate(t *testing.T) {
	c := openedToSpeak(t)
	fx := c.Step(InjectInput(notice("Session update: orchard finished its turn.")), ms(t0(), 30))
	if !contains(fx, injectFx("Session update: orchard finished its turn."+speakOnlyNote)) {
		t.Fatalf("speak-only notice: %+v", fx)
	}

	q := "Coordinator question: deploy now or wait?"
	if fx := openedToSpeak(t).Step(InjectInput(notice(q)), ms(t0(), 30)); !contains(fx, injectFx(q+speakOnlyQuestionNote)) {
		t.Fatalf("a question: %+v", fx)
	}

	// After the hotkey the mic is on: the next notice is a usual one.
	c = openedToSpeak(t)
	c.Step(HotkeyInput(), ms(t0(), 30))
	if fx := c.Step(InjectInput(notice("Coordinator reply: done.")), ms(t0(), 40)); !contains(fx, injectFx("Coordinator reply: done.")) {
		t.Fatalf("after the hotkey: %+v", fx)
	}
}

// Each notice gets its own note: a plain update said after a question is told it's an update.
func TestSpeakOnlyAnUpdateAfterAQuestionGetsTheUpdateNote(t *testing.T) {
	c := openedToSpeak(t)
	c.Step(InjectInput(notice("Coordinator question: deploy now or wait?")), ms(t0(), 30))
	c.Step(InjectInput(notice("Session update: orchard finished its turn.")), ms(t0(), 35))
	if fx := sayIt(t, c, ms(t0(), 40)); !contains(fx, injectFx("Session update: orchard finished its turn."+speakOnlyNote)) {
		t.Fatalf("the update after a question: %+v", fx)
	}
}

// #445: with SpeakListens (agent.listenAfterUpdates "questions") a question it says turns
// the mic on to listen for the answer, as before #443.

// openedToSpeakListening is a speak-only conversation that listens after a question.
func openedToSpeakListening(t *testing.T) *Conversation {
	t.Helper()
	c := NewConversation(ConversationConfig{IdleTimeout: testTimeout, SpeakListens: true})
	c.Step(OpenToSpeakInput(), t0())
	eq(t, model(c, evReady, ms(t0(), 20)),
		ef(EfStatus), pixie(StateIdle), signalFx(VoiceSignal{Kind: SigOpened}))
	return c
}

func TestSpeakListensAfterAQuestion(t *testing.T) {
	c := openedToSpeakListening(t)
	q := "Coordinator question: deploy now or wait?"
	eq(t, c.Step(InjectInput(notice(q)), ms(t0(), 30)), injectFx(q), pixie(StateThinking), chime(ChimeStart), ef(EfStartMic))
	sayIt(t, c, ms(t0(), 40))
	c.Step(MicLiveInput(), ms(t0(), 80))
	// A usual conversation now: it waits for the answer, then ends on silence with the chime.
	if fx := tick(c, true, ms(t0(), 1000)); hasKind(fx, EfClose) {
		t.Fatalf("ended without waiting for the answer: %+v", fx)
	}
	if fx := tick(c, true, ms(t0(), 80+int64(QuestionListenMin/time.Millisecond))); !contains(fx, chime(ChimeEnd)) {
		t.Fatalf("silence end: %+v", fx)
	}
}

func TestSpeakListensAfterAConfirmWord(t *testing.T) {
	c := openedToSpeakListening(t)
	n := "Coordinator wants to run rm -rf build. Say confirm hawk."
	if fx := c.Step(InjectInput(notice(n)), ms(t0(), 30)); !hasKind(fx, EfStartMic) || !contains(fx, injectFx(n)) {
		t.Fatalf("mic not started for a confirm word: %+v", fx)
	}
}

// A plain update is still only said, with the update note, and it ends once said.
func TestSpeakListensSaysAPlainUpdateOnly(t *testing.T) {
	c := openedToSpeakListening(t)
	fx := c.Step(InjectInput(notice("Coordinator reply: the build passed.")), ms(t0(), 30))
	eq(t, fx, injectFx("Coordinator reply: the build passed."+speakOnlyNote), pixie(StateThinking))
	sayIt(t, c, ms(t0(), 40))
	if fx := tick(c, true, ms(t0(), 100)); !hasKind(fx, EfClose) || contains(fx, chime(ChimeEnd)) {
		t.Fatalf("didn't end once said: %+v", fx)
	}
}

// A plain update first, then a question: the update gets its note, the question turns the mic
// on, and an update after that is a usual one, with no note.
func TestSpeakListensAnUpdateThenAQuestionThenAnUpdate(t *testing.T) {
	c := openedToSpeakListening(t)
	c.Step(InjectInput(notice("Coordinator reply: one.")), ms(t0(), 30))
	c.Step(InjectInput(notice("Coordinator question: deploy now or wait?")), ms(t0(), 35))
	c.Step(InjectInput(notice("Coordinator reply: two.")), ms(t0(), 36))
	fx := sayIt(t, c, ms(t0(), 40))
	if !contains(fx, injectFx("Coordinator question: deploy now or wait?")) || !hasKind(fx, EfStartMic) {
		t.Fatalf("the question: %+v", fx)
	}
	if fx := sayIt(t, c, ms(t0(), 110)); !contains(fx, injectFx("Coordinator reply: two.")) || hasKind(fx, EfClose) {
		t.Fatalf("the update after it: %+v", fx)
	}
}
