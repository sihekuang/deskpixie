package voice

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

// The claim check in the conversation (#72).

func newClaimConv(handOff, checkClaims bool) *Conversation {
	c := NewConversation(ConversationConfig{
		IdleTimeout: testTimeout, HandOff: handOff, CheckClaims: checkClaims, ClaimTools: allClaimTools,
	})
	c.Step(HotkeyInput(), t0())
	model(c, evReady, t0())
	c.Step(MicLiveInput(), t0())
	return c
}

const timerFix = "Coordinator: correction: no timer was set or changed. Call timer now, or ask the user how long."

func TestClaimATimerClaimWithNoTimerCallIsCorrectedOnce(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Set a timer for ten minutes.", ms(t0(), 100))
	fx := reply(c, "Sure, I've set a timer for ten minutes.", ms(t0(), 500))
	if len(injects(fx)) != 0 {
		t.Fatal("corrected before the grace period")
	}
	fx = ticks(c, ms(t0(), 500), 5*time.Second)
	if got := injects(fx); len(got) != 1 || got[0] != timerFix {
		t.Fatalf("injects: %q", got)
	}
	if len(handOffs(fx)) != 0 {
		t.Fatal("a timer claim is not handed off")
	}
	// Pixie's reply to the correction is not checked again.
	fx = reply(c, "Oops, I've set a timer now.", ms(t0(), 6000))
	fx = append(fx, ticks(c, ms(t0(), 6000), 5*time.Second)...)
	if got := injects(fx); len(got) != 0 {
		t.Fatalf("a second correction: %q", got)
	}
}

func TestClaimBackedByTheToolIsLeftAlone(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Set a timer for ten minutes.", ms(t0(), 100))
	model(c, evResponseCreated, ms(t0(), 200))
	model(c, toolCall(TimerToolName), ms(t0(), 200))
	model(c, evResponseDone, ms(t0(), 200))
	c.Step(ToolDoneInput("c1", false), ms(t0(), 300))
	fx := reply(c, "I've set a timer for ten minutes.", ms(t0(), 400))
	fx = append(fx, ticks(c, ms(t0(), 400), 5*time.Second)...)
	if len(injects(fx)) != 0 || len(handOffs(fx)) != 0 {
		t.Fatalf("a backed claim was corrected: %+v", fx)
	}
}

func TestClaimNotWhenTheSettingIsOff(t *testing.T) {
	c := newClaimConv(true, false)
	userTurn(c, "Set a timer for ten minutes.", ms(t0(), 100))
	fx := reply(c, "I've set a timer for ten minutes.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	if len(injects(fx)) != 0 {
		t.Fatal("corrected with voice.checkClaims off")
	}
}

func TestClaimWithoutAUserTranscriptStillCorrects(t *testing.T) {
	c := newClaimConv(true, true)
	model(c, evSpeechStarted, ms(t0(), 100))
	model(c, evSpeechStopped, ms(t0(), 100))
	fx := reply(c, "I've paused the music.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	want := "Coordinator: correction: the music wasn't changed. Call media now, or tell the user it didn't happen."
	if got := injects(fx); len(got) != 1 || got[0] != want {
		t.Fatalf("injects: %q", got)
	}
}

func TestClaimADelegateClaimDelegatesQuietly(t *testing.T) {
	c := newClaimConv(false, true) // the hand-off setting is not needed
	userTurn(c, "Can you fix the failing test in deskpixie?", ms(t0(), 100))
	fx := reply(c, "On it, let me hand that off.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	h := handOffs(fx)
	if len(h) != 1 || h[0].Text != "Can you fix the failing test in deskpixie?" || !h[0].Claim {
		t.Fatalf("hand-offs: %+v", h)
	}
	// Pixie already said it: a successful hand-off says nothing more.
	fx = c.Step(HandOffDoneInput(Notice{}), ms(t0(), 6000))
	fx = append(fx, ticks(c, ms(t0(), 6000), time.Second)...)
	if len(injects(fx)) != 0 || len(handOffs(fx)) != 0 {
		t.Fatalf("%+v", fx)
	}
}

func TestClaimLetMeCheckDelegatesAFactCheck(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Who won the game last night?", ms(t0(), 100))
	fx := reply(c, "Let me check that.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	if h := handOffs(fx); len(h) != 1 || h[0].Text != "Check: Who won the game last night?" {
		t.Fatalf("hand-offs: %+v", h)
	}
}

func TestClaimADelegateClaimToSmallTalkIsLeftAlone(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Great, thanks!", ms(t0(), 100))
	fx := reply(c, "You're welcome, I've handed it to the agent.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	if len(handOffs(fx)) != 0 || len(injects(fx)) != 0 {
		t.Fatalf("%+v", fx)
	}
}

// #71's hand-off and the claim check share the once-per-turn guard.
func TestClaimAndHandOffActOnceBetweenThem(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	fx := reply(c, "I don't know, let me hand that off.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	h := handOffs(fx)
	if len(h) != 1 || h[0].Claim {
		t.Fatalf("want one #71 hand-off: %+v", h)
	}
	fx = c.Step(HandOffDoneInput(Notice{Text: HandedOffPrefix + "x"}), ms(t0(), 6000))
	fx = append(fx, ticks(c, ms(t0(), 6000), time.Second)...)
	fx = append(fx, reply(c, "I've handed that to the agent; I'll let you know.", ms(t0(), 7200))...)
	fx = append(fx, ticks(c, ms(t0(), 7200), 5*time.Second)...)
	if len(handOffs(fx)) != 0 || len(injects(fx)) != 1 {
		t.Fatalf("a second action in one turn: %+v", fx)
	}
}

func TestClaimNeverWhileEnding(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Set a timer and then bye.", ms(t0(), 100))
	model(c, evResponseCreated, ms(t0(), 200))
	model(c, said("assistant", "I've set a timer. Bye!"), ms(t0(), 200))
	model(c, toolCall(EndConversationTool), ms(t0(), 200))
	model(c, evResponseDone, ms(t0(), 200))
	fx := c.Step(ToolDoneInput("c1", true), ms(t0(), 300))
	fx = append(fx, ticks(c, ms(t0(), 300), 5*time.Second)...)
	if len(injects(fx)) != 0 || len(handOffs(fx)) != 0 {
		t.Fatal("corrected while ending")
	}
}

func TestClaimNotForBitsReplyToANotice(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Thanks.", ms(t0(), 100))
	model(c, evResponseCreated, ms(t0(), 200))
	model(c, evResponseDone, ms(t0(), 200))
	fx := c.Step(InjectInput(notice("Timer done: tea (5 minutes).")), ms(t0(), 300))
	fx = append(fx, tick(c, true, ms(t0(), 400))...)
	if got := injects(fx); len(got) != 1 {
		t.Fatalf("the notice was not delivered: %q", got)
	}
	fx = reply(c, "Your tea timer is done, so I've stopped the timer.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	if len(injects(fx)) != 0 {
		t.Fatal("corrected Pixie's reply to a notice")
	}
}

// GPT-Live's backend can call the tool after the grace period: a correction still waiting for a
// quiet moment is withdrawn.
func TestClaimALateToolCallWithdrawsTheWaitingCorrection(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Set a timer for ten minutes.", ms(t0(), 100))
	reply(c, "Setting a timer for ten minutes.", ms(t0(), 500))
	var fx []Effect
	for at := ms(t0(), 500); at.Before(ms(t0(), 3000)); at = at.Add(100 * time.Millisecond) {
		fx = append(fx, tick(c, false, at)...) // Pixie's words are still playing
	}
	if len(injects(fx)) != 0 {
		t.Fatal("injected over playback")
	}
	model(c, evToolCall("late", TimerToolName), ms(t0(), 3100))
	c.Step(ToolDoneInput("late", false), ms(t0(), 3200))
	fx = reply(c, "Done, ten minutes.", ms(t0(), 3300))
	fx = append(fx, ticks(c, ms(t0(), 3300), 5*time.Second)...)
	if got := injects(fx); len(got) != 0 {
		t.Fatalf("the withdrawn correction was said: %q", got)
	}
}

// #71 on GPT-Live: the safety net delegated, then the provider's own delegate call for the
// same turn arrives late. It is answered without starting a second job.
func TestALateDelegateCallAfterTheHandOffIsShortCircuited(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	reply(c, "I'm not sure.", ms(t0(), 500))
	fx := ticks(c, ms(t0(), 500), 3*time.Second)
	if len(handOffs(fx)) != 1 {
		t.Fatal("no hand-off")
	}
	fx = model(c, evToolCall("late", DelegateTool), ms(t0(), 3600))
	var inv []Effect
	for _, e := range fx {
		if e.Kind == EfInvokeTool {
			inv = append(inv, e)
		}
	}
	if len(inv) != 1 || inv[0].CallID != "late" || len(inv[0].Canned) == 0 {
		t.Fatalf("want a canned result for the late call: %+v", inv)
	}
	if !strings.Contains(string(inv[0].Canned), "already") {
		t.Fatalf("%s", inv[0].Canned)
	}
	// A new turn delegates for real again.
	userTurn(c, "Now check the weather in Paris.", ms(t0(), 9000))
	fx = model(c, evToolCall("next", DelegateTool), ms(t0(), 9100))
	for _, e := range fx {
		if e.Kind == EfInvokeTool && len(e.Canned) != 0 {
			t.Fatal("a new turn's delegate call was short-circuited")
		}
	}
}

// ---- through the runtime ----

func TestRuntimeKeepsTheClaimToolsItHas(t *testing.T) {
	m := &fakeModel{sess: newFakeSession()}
	reg := DefaultTools()
	reg.Register(&fakeDelegate{})
	cc := DefaultConversationConfig()
	cc.CheckClaims = true
	r := NewRuntime(m, audio.Passthrough{}, RuntimeConfig{Session: testSessionCfg, APIKey: "k", Tools: reg, Conversation: cc}, nil)
	defer r.Close()
	if got := r.conv.claimTools; len(got) != 1 || got[0] != DelegateTool {
		t.Fatalf("claim tools: %v", got)
	}
}

func TestRuntimeDelegateClaimDelegatesWithoutANotice(t *testing.T) {
	d := &fakeDelegate{}
	m := &fakeModel{sess: newFakeSession()}
	ui := &uiLog{}
	reg := DefaultTools()
	reg.Register(d)
	cc := DefaultConversationConfig()
	cc.CheckClaims = true
	r := NewRuntime(m, audio.Passthrough{}, RuntimeConfig{
		Session: testSessionCfg, APIKey: "k", MouthLevels: 4, Tools: reg, Conversation: cc,
	}, ui.emit)
	t.Cleanup(r.Close)
	startListening(t, r, m, ui)
	s := m.sess
	for _, ev := range []Event{
		evSpeechStarted, evSpeechStopped, said("user", "Can you fix the failing test?"),
		evResponseCreated, said("assistant", "Sure, I'll hand that off."), evResponseDone,
	} {
		s.events <- ev
	}
	waitLong(t, "the delegation", func() bool { return len(d.jobs()) == 1 })
	if d.jobs()[0]["task"] != "Can you fix the failing test?" {
		t.Fatalf("%v", d.jobs())
	}
	time.Sleep(300 * time.Millisecond)
	if n := len(s.commands(CmdInject)); n != 0 {
		t.Fatalf("Pixie already said it; %d injects", n)
	}
}

func TestRuntimeALateDelegateCallStartsNoSecondJob(t *testing.T) {
	d := &fakeDelegate{}
	_, s := runHandOff(t, d, true)
	waitLong(t, "the hand-off notice", func() bool { return len(s.commands(CmdInject)) == 1 })
	s.events <- evToolCall("late", DelegateTool)
	waitLong(t, "the late call's result", func() bool { return len(s.commands(CmdToolResult)) == 1 })
	if n := len(d.jobs()); n != 1 {
		t.Fatalf("%d jobs", n)
	}
	got := s.commands(CmdToolResult)[0]
	var out map[string]any
	if err := json.Unmarshal(got.Output, &out); err != nil || got.CallID != "late" || out["status"] != "already handed off" {
		t.Fatalf("%+v", got)
	}
}

// #133: the request came in pieces, Pixie's "Mm-hmm." between, and its last piece alone
// ("agent") is no request: the claimed hand-off takes the whole of it.
func TestClaimAHandOffClaimTakesARequestSplitAcrossTurns(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "I- I I actually want to have a, I actually want to have a sub session Herder", ms(t0(), 100))
	reply(c, "Mm-hmm.", ms(t0(), 300))
	userTurn(c, "agent", ms(t0(), 600))
	fx := reply(c, "Okay, I'll hand that off and let you know.", ms(t0(), 900))
	fx = append(fx, ticks(c, ms(t0(), 900), 5*time.Second)...)
	h := handOffs(fx)
	if len(h) != 1 || h[0].Text != "I- I I actually want to have a, I actually want to have a sub session Herder agent" || !h[0].Claim {
		t.Fatalf("hand-offs: %+v", h)
	}
}

// What Pixie said in earnest ends the request: an earlier one it answered is not handed off again.
func TestClaimAHandOffClaimDoesNotReachPastPixiesAnswer(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Can you check the weather in Bothell?", ms(t0(), 100))
	reply(c, "It's sunny in Bothell, seventy-four degrees.", ms(t0(), 300))
	ticks(c, ms(t0(), 300), 3*time.Second)
	userTurn(c, "Great, thanks!", ms(t0(), 4000))
	fx := reply(c, "Sure, I'll hand that off.", ms(t0(), 4300))
	fx = append(fx, ticks(c, ms(t0(), 4300), 5*time.Second)...)
	if h := handOffs(fx); len(h) != 0 {
		t.Fatalf("hand-offs: %+v", h)
	}
}

// #133 as it happened on 2026-10-16 02:20: the request in three transcripts, Pixie's "Mm-hmm."
// interrupted between, then a claimed hand-off with no delegate call.
func TestRuntimeAHandOffClaimAfterASplitRequestDelegatesIt(t *testing.T) {
	d := &fakeDelegate{}
	m := &fakeModel{sess: newFakeSession()}
	ui := &uiLog{}
	reg := DefaultTools()
	reg.Register(d)
	cc := DefaultConversationConfig()
	cc.CheckClaims = true
	r := NewRuntime(m, audio.Passthrough{}, RuntimeConfig{
		Session: testSessionCfg, APIKey: "k", MouthLevels: 4, Tools: reg, Conversation: cc,
	}, ui.emit)
	t.Cleanup(r.Close)
	startListening(t, r, m, ui)
	s := m.sess
	for _, ev := range []Event{
		evSpeechStarted, evSpeechStopped, said("user", "I- I I actually want to"),
		evSpeechStarted, evSpeechStopped, said("user", "have a, I actually want to have a sub session Herder"),
		evResponseCreated, said("assistant", "Mm-hmm."),
		evSpeechStarted, evResponseDone, evSpeechStopped, said("user", "agent"),
		evResponseCreated, said("assistant", "Okay, I'll hand that off and let you know."), evResponseDone,
	} {
		s.events <- ev
	}
	waitLong(t, "the delegation", func() bool { return len(d.jobs()) == 1 })
	task := d.jobs()[0]["task"]
	if !strings.Contains(task, "sub session Herder agent") {
		t.Fatalf("task %q", task)
	}
	t.Logf("handed off: %q", task)
}

// Oct 18, 21:03: Pixie said "On it" to a statement that is no request and nothing was handed
// off. A promise to hand it on is made true for anything said that isn't small talk.
func TestClaimADelegateClaimToAStatementHandsItOn(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "The coordinator should not answer me questions like that.", ms(t0(), 100))
	fx := reply(c, "On it, I'll let you know when it's done.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	if h := handOffs(fx); len(h) != 1 || h[0].Text != "The coordinator should not answer me questions like that." {
		t.Fatalf("hand-offs: %+v", h)
	}
}

// Oct 18, 22:09: Pixie added "Say confirm hawk." to a question that had no confirm word. A
// confirm phrase no notice in this conversation gave is corrected.
func TestClaimAMadeUpConfirmWordIsCorrected(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "What's happening with the build?", ms(t0(), 100))
	model(c, evResponseCreated, ms(t0(), 200))
	model(c, toolCall(DelegateTool), ms(t0(), 200))
	model(c, evResponseDone, ms(t0(), 200))
	c.Step(ToolDoneInput("c1", false), ms(t0(), 300))
	fx := reply(c, "Should I check the repo's git status? Say confirm hawk.", ms(t0(), 400))
	fx = append(fx, ticks(c, ms(t0(), 400), 5*time.Second)...)
	if got := injects(fx); len(got) != 1 || !strings.HasPrefix(got[0], CorrectionPrefix+"there is no confirm word") {
		t.Fatalf("injects: %q", got)
	}
}

// A confirm word the app gave is said as given.
func TestClaimARealConfirmWordIsLeftAlone(t *testing.T) {
	c := newClaimConv(true, true)
	c.Step(InjectInput(Notice{Text: "Coordinator question: Run git push in the coordinator? Say confirm hawk."}), ms(t0(), 50))
	fx := reply(c, "The coordinator wants to run git push. Say confirm hawk.", ms(t0(), 400))
	fx = append(fx, ticks(c, ms(t0(), 400), 5*time.Second)...)
	if got := injects(fx); len(got) != 0 {
		t.Fatalf("injects: %q", got)
	}
}

func closes(fx []Effect) bool {
	for _, e := range fx {
		if e.Kind == EfSignal && e.Signal.Kind == SigClosed {
			return true
		}
	}
	return false
}

// Voice log, Oct 18 (20:38, 22:09): with a 3 s silence timeout a conversation closed 3 s after
// Pixie said "Say confirm …". After a confirm question it listens at least QuestionListenMin.
func TestAConfirmQuestionGetsTimeForTheAnswer(t *testing.T) {
	c := NewConversation(ConversationConfig{IdleTimeout: 3 * time.Second})
	c.Step(HotkeyInput(), t0())
	model(c, evReady, t0())
	c.Step(MicLiveInput(), t0())
	c.Step(InjectInput(Notice{Text: "Coordinator question: Run git push in the coordinator? Say confirm hawk."}), t0())
	reply(c, "The coordinator wants to run git push. Say confirm hawk.", ms(t0(), 500))
	if fx := ticks(c, ms(t0(), 500), 5*time.Second); closes(fx) {
		t.Fatal("closed before the user could say the confirm word")
	}
	if fx := ticks(c, ms(t0(), 5_600), QuestionListenMin); !closes(fx) {
		t.Fatal("never closed")
	}
}

// #337: any question Pixie relays gets the same time to answer: a long one with choices
// ("Pick A or B") can't be answered in a 5 s silence timeout.
func TestARelayedQuestionGetsTimeForTheAnswer(t *testing.T) {
	for _, notice := range []string{
		"Coordinator question: The landing session asks: name the other products, or not? Which should it do?",
		"Session update: deskpixie: hand-off is asking: should I rebuild it and open the PR?",
	} {
		c := NewConversation(ConversationConfig{IdleTimeout: 5 * time.Second})
		c.Step(HotkeyInput(), t0())
		model(c, evReady, t0())
		c.Step(MicLiveInput(), t0())
		c.Step(InjectInput(Notice{Text: notice}), t0())
		reply(c, "The session is asking which to do: name them, or not?", ms(t0(), 500))
		if fx := ticks(c, ms(t0(), 500), 8*time.Second); closes(fx) {
			t.Fatalf("%q: closed before the user could answer", notice)
		}
		if fx := ticks(c, ms(t0(), 8_600), QuestionListenMin); !closes(fx) {
			t.Fatalf("%q: never closed", notice)
		}
	}
}

// #337 review: a question that came in while Pixie was talking, and the user cut in before she
// relayed it, still gets the time: their words before it don't answer it.
func TestAQuestionQueuedBeforeTheUserCutInStillGetsTheTime(t *testing.T) {
	c := NewConversation(ConversationConfig{IdleTimeout: 5 * time.Second})
	c.Step(HotkeyInput(), t0())
	model(c, evReady, t0())
	c.Step(MicLiveInput(), t0())
	model(c, evResponseCreated, t0())
	model(c, evAudio(frame()), t0())
	c.Step(InjectInput(Notice{Text: "Coordinator question: The landing session asks: A or B?"}), ms(t0(), 100))
	model(c, evSpeechStarted, ms(t0(), 200)) // "thanks"
	model(c, evSpeechStopped, ms(t0(), 400))
	reply(c, "You're welcome. The landing session asks: A or B?", ms(t0(), 1_200))
	if fx := ticks(c, ms(t0(), 1_200), 8*time.Second); closes(fx) {
		t.Fatal("closed before the user could answer")
	}
}

// Without a confirm question the user's own silence timeout decides.
func TestAPlainNoticeKeepsTheSilenceTimeout(t *testing.T) {
	c := NewConversation(ConversationConfig{IdleTimeout: 3 * time.Second})
	c.Step(HotkeyInput(), t0())
	model(c, evReady, t0())
	c.Step(MicLiveInput(), t0())
	c.Step(InjectInput(Notice{Text: "Coordinator reply: The build passed."}), t0())
	reply(c, "The build passed.", ms(t0(), 500))
	if fx := ticks(c, ms(t0(), 500), 5*time.Second); !closes(fx) {
		t.Fatal("still open after the 3 s timeout")
	}
}

// A confirm word from an earlier conversation doesn't make one Pixie says now real.
func TestClaimAConfirmWordFromAnEarlierConversationDoesNotCount(t *testing.T) {
	c := newClaimConv(true, true)
	c.Step(InjectInput(Notice{Text: "Coordinator question: Run git push in the coordinator? Say confirm hawk."}), ms(t0(), 50))
	c.Step(HotkeyInput(), ms(t0(), 100)) // closes it
	ticks(c, ms(t0(), 100), 3*time.Second)
	c.Step(HotkeyInput(), ms(t0(), 4000))
	model(c, evReady, ms(t0(), 4000))
	c.Step(MicLiveInput(), ms(t0(), 4000))
	userTurn(c, "What's going on with the push?", ms(t0(), 4100))
	fx := reply(c, "Should I check the repo? Say confirm hawk.", ms(t0(), 4500))
	fx = append(fx, ticks(c, ms(t0(), 4500), 5*time.Second)...)
	if got := injects(fx); len(got) != 1 || got[0] != MadeUpConfirmFix {
		t.Fatalf("injects: %q", got)
	}
}

// The real case, Oct 18 22:09: relaying a question that had no confirm word, Pixie added one.
func TestClaimAMadeUpConfirmWordInAReplyToANoticeIsCorrected(t *testing.T) {
	c := newClaimConv(true, true)
	c.Step(InjectInput(Notice{Text: "Coordinator question: The deskpixie session is busy. Want me to check the repo's git status myself?"}), ms(t0(), 50))
	fx := reply(c, "Should I check the repo's git status for you? Say confirm hawk.", ms(t0(), 400))
	fx = append(fx, ticks(c, ms(t0(), 400), 5*time.Second)...)
	if got := injects(fx); len(got) != 1 || got[0] != MadeUpConfirmFix {
		t.Fatalf("injects: %q", got)
	}
}

// Voice log, Oct 19 01:17: the user said "Confirm lark" and Pixie hung up, so "Coordinator:
// allowed" waited for the next conversation. A confirm phrase is no goodbye.
func TestSayingTheConfirmWordDoesNotEndTheConversation(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Confirm lark.", ms(t0(), 100))
	model(c, evResponseCreated, ms(t0(), 200))
	model(c, evToolCall("c1", EndConversationTool), ms(t0(), 200))
	model(c, evResponseDone, ms(t0(), 200))
	c.Step(ToolDoneInput("c1", true), ms(t0(), 300))
	fx := c.Step(InjectInput(Notice{Text: "Coordinator: allowed"}), ms(t0(), 400))
	fx = append(fx, ticks(c, ms(t0(), 400), time.Second)...)
	if closes(fx) {
		t.Fatal("hung up on the confirm word")
	}
	if got := injects(fx); len(got) != 1 || got[0] != "Coordinator: allowed" {
		t.Fatalf("injects: %q", got)
	}
}

// A real goodbye still ends it.
func TestAGoodbyeStillEndsTheConversation(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "Thanks, bye.", ms(t0(), 100))
	model(c, evResponseCreated, ms(t0(), 200))
	model(c, evToolCall("c1", EndConversationTool), ms(t0(), 200))
	model(c, evResponseDone, ms(t0(), 200))
	fx := c.Step(ToolDoneInput("c1", true), ms(t0(), 300))
	fx = append(fx, ticks(c, ms(t0(), 300), time.Second)...)
	if !closes(fx) {
		t.Fatal("a goodbye didn't end it")
	}
}

// #380: "I'll tell them to …" after the user answers a relayed question, with no hand-off, is
// handed off as the answer; the same words reading back a "Coordinator reply:" are not.
func TestClaimTellingTheSessionIsHandedOffAsTheAnswer(t *testing.T) {
	c := newClaimConv(true, true)
	c.Step(InjectInput(Notice{Text: "Coordinator question: jira is asking: Create both, unassigned, or change something first?"}), ms(t0(), 100))
	reply(c, "Should it create both unassigned, or change something first?", ms(t0(), 400))
	userTurn(c, "Create both unassigned", ms(t0(), 2000))
	fx := reply(c, "Sure thing, I'll tell them to create both unassigned.", ms(t0(), 2500))
	fx = append(fx, ticks(c, ms(t0(), 2500), 5*time.Second)...)
	if h := handOffs(fx); len(h) != 1 || h[0].Text != "Create both unassigned" {
		t.Fatalf("hand-offs: %+v", h)
	}

	c = newClaimConv(true, true)
	c.Step(InjectInput(Notice{Text: "Coordinator reply: Passed on to your session on jira."}), ms(t0(), 100))
	fx = reply(c, "I passed that on to your session, and I'll let them know.", ms(t0(), 400))
	fx = append(fx, ticks(c, ms(t0(), 400), 5*time.Second)...)
	if h := handOffs(fx); len(h) != 0 {
		t.Fatalf("reading back a reply was handed off: %+v", h)
	}
}
