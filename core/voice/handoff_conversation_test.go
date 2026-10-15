package voice

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

// The hand-off safety net in the conversation (#71).

func newHandOffConv(on bool) *Conversation {
	c := NewConversation(ConversationConfig{IdleTimeout: testTimeout, HandOff: on})
	c.Step(HotkeyInput(), t0())
	model(c, evReady, t0())
	c.Step(MicLiveInput(), t0())
	return c
}

func said(role, text string) Event { return Event{Kind: EvTranscript, Role: role, Text: text} }

// userTurn: the user speaks (with their words transcribed) at `at`.
func userTurn(c *Conversation, words string, at time.Time) {
	model(c, evSpeechStarted, at)
	model(c, evSpeechStopped, at)
	model(c, said("user", words), at)
}

// reply: one spoken response from Pixie with its transcript, finished at `at`.
func reply(c *Conversation, words string, at time.Time) []Effect {
	var fx []Effect
	fx = append(fx, model(c, evResponseCreated, at)...)
	fx = append(fx, model(c, evAudio(frame()), at)...)
	fx = append(fx, model(c, said("assistant", words), at)...)
	return append(fx, model(c, evResponseDone, at)...)
}

func handOffs(fx []Effect) []Effect {
	var out []Effect
	for _, e := range fx {
		if e.Kind == EfHandOff {
			out = append(out, e)
		}
	}
	return out
}

func injects(fx []Effect) []string {
	var out []string
	for _, e := range fx {
		if e.Kind == EfInject {
			out = append(out, e.Text)
		}
	}
	return out
}

// ticks steps the clock from `from` for d in 100 ms ticks, playback idle.
func ticks(c *Conversation, from time.Time, d time.Duration) []Effect {
	var fx []Effect
	for at := from; !at.After(from.Add(d)); at = at.Add(100 * time.Millisecond) {
		fx = append(fx, tick(c, true, at)...)
	}
	return fx
}

func TestHandOffANonAnswerWithNoToolDelegatesOnceAndSaysSo(t *testing.T) {
	c := newHandOffConv(true)
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	fx := reply(c, "I'm not sure, I can't check live prices.", ms(t0(), 500))
	if len(handOffs(fx)) != 0 {
		t.Fatal("handed off before the grace period")
	}
	fx = ticks(c, ms(t0(), 500), 5*time.Second)
	h := handOffs(fx)
	if len(h) != 1 || h[0].Text != "What's the price of bitcoin right now?" || h[0].Context != "" {
		t.Fatalf("hand-offs: %+v", h)
	}
	if len(injects(fx)) != 0 {
		t.Fatal("injected before the hand-off finished")
	}
	fx = c.Step(HandOffDoneInput(Notice{Text: HandedOffPrefix + "What's the price of bitcoin right now?"}), ms(t0(), 6000))
	fx = append(fx, ticks(c, ms(t0(), 6000), time.Second)...)
	if got := injects(fx); len(got) != 1 || got[0] != HandedOffPrefix+"What's the price of bitcoin right now?" {
		t.Fatalf("injects: %q", got)
	}
	if len(handOffs(fx)) != 0 {
		t.Fatal("handed off twice")
	}
}

func TestHandOffWaitsForTheHandOffBeforeTheSilenceTimeout(t *testing.T) {
	c := newHandOffConv(true)
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	reply(c, "I don't know.", ms(t0(), 500))
	fx := ticks(c, ms(t0(), 500), 30*time.Second)
	if len(handOffs(fx)) != 1 || hasKind(fx, EfClose) {
		t.Fatalf("a running hand-off keeps the conversation open: %+v", fx)
	}
}

func TestHandOffNotWhenAToolWasCalled(t *testing.T) {
	c := newHandOffConv(true)
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	model(c, evResponseCreated, ms(t0(), 200))
	model(c, said("assistant", "I don't know."), ms(t0(), 200))
	model(c, toolCall("get_time"), ms(t0(), 200))
	model(c, evResponseDone, ms(t0(), 200))
	c.Step(ToolDoneInput("c1", false), ms(t0(), 300))
	fx := reply(c, "I'm not sure.", ms(t0(), 400))
	fx = append(fx, ticks(c, ms(t0(), 400), 5*time.Second)...)
	if len(handOffs(fx)) != 0 {
		t.Fatal("handed off a turn that already called a tool")
	}
}

func TestHandOffNotWhenTheSettingIsOff(t *testing.T) {
	c := newHandOffConv(false)
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	fx := reply(c, "I don't know.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	if len(handOffs(fx)) != 0 {
		t.Fatal("handed off with handOffWhenUnsure off")
	}
}

func TestHandOffAtMostOncePerTurn(t *testing.T) {
	c := newHandOffConv(true)
	userTurn(c, "Who won the game last night?", ms(t0(), 100))
	fx := reply(c, "I don't know.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 3*time.Second)...)
	c.Step(HandOffDoneInput(Notice{}), ms(t0(), 3600))
	// Pixie says another non-answer in the same turn (unprompted).
	fx = append(fx, reply(c, "Sorry, I can't check that.", ms(t0(), 3700))...)
	fx = append(fx, ticks(c, ms(t0(), 3700), 5*time.Second)...)
	if n := len(handOffs(fx)); n != 1 {
		t.Fatalf("%d hand-offs in one turn", n)
	}
	// A new turn may hand off again, with the earlier turn as context.
	userTurn(c, "Who is playing tonight?", ms(t0(), 10000))
	fx = reply(c, "I'm not sure.", ms(t0(), 10200))
	fx = append(fx, ticks(c, ms(t0(), 10200), 3*time.Second)...)
	h := handOffs(fx)
	if len(h) != 1 || h[0].Text != "Who is playing tonight?" {
		t.Fatalf("second turn: %+v", h)
	}
	if !strings.Contains(h[0].Context, "User: Who won the game last night?") ||
		!strings.Contains(h[0].Context, "Pixie: I don't know. Sorry, I can't check that.") {
		t.Fatalf("context: %q", h[0].Context)
	}
}

func TestHandOffNotForSmallTalkOrGoodbyes(t *testing.T) {
	for _, tc := range []struct{ user, bot string }{
		{"What's your favorite food?", "I don't know, maybe pizza?"},
		{"What should we have for dinner?", "I don't know."},
		{"Thanks, bye!", "I don't know."},
		{"Who won the game last night?", "The Giants won, five to three."},
	} {
		c := newHandOffConv(true)
		userTurn(c, tc.user, ms(t0(), 100))
		fx := reply(c, tc.bot, ms(t0(), 500))
		fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
		if len(handOffs(fx)) != 0 {
			t.Errorf("handed off %q → %q", tc.user, tc.bot)
		}
	}
}

func TestHandOffNeverWhileEnding(t *testing.T) {
	c := newHandOffConv(true)
	userTurn(c, "Can you check the news?", ms(t0(), 100))
	model(c, evResponseCreated, ms(t0(), 200))
	model(c, said("assistant", "I don't know. Bye!"), ms(t0(), 200))
	model(c, toolCall(EndConversationTool), ms(t0(), 200))
	model(c, evResponseDone, ms(t0(), 200))
	fx := c.Step(ToolDoneInput("c1", true), ms(t0(), 300))
	fx = append(fx, ticks(c, ms(t0(), 300), 5*time.Second)...)
	if len(handOffs(fx)) != 0 {
		t.Fatal("handed off while ending")
	}
}

func TestHandOffNotWhenTheUserSpeaksAgainFirst(t *testing.T) {
	c := newHandOffConv(true)
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	reply(c, "I don't know.", ms(t0(), 500))
	model(c, evSpeechStarted, ms(t0(), 900)) // before the grace period ends
	fx := ticks(c, ms(t0(), 900), 5*time.Second)
	if len(handOffs(fx)) != 0 {
		t.Fatal("handed off a turn the user moved on from")
	}
}

func TestHandOffALateUserTranscriptStillCounts(t *testing.T) {
	c := newHandOffConv(true)
	model(c, evSpeechStarted, ms(t0(), 100))
	model(c, evSpeechStopped, ms(t0(), 100))
	fx := reply(c, "I'm not sure.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 2*time.Second)...)
	// openai-realtime's transcription can land after the reply.
	fx = append(fx, model(c, said("user", "What's the weather on Mars today?"), ms(t0(), 2600))...)
	fx = append(fx, ticks(c, ms(t0(), 2600), time.Second)...)
	if h := handOffs(fx); len(h) != 1 || h[0].Text != "What's the weather on Mars today?" {
		t.Fatalf("hand-offs: %+v", h)
	}
}

func TestHandOffNotForBitsReplyToANotice(t *testing.T) {
	c := newHandOffConv(true)
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	model(c, evResponseCreated, ms(t0(), 200)) // a reply with no words
	model(c, evResponseDone, ms(t0(), 200))
	fx := c.Step(InjectInput(notice("Timer done: tea (5 minutes).")), ms(t0(), 300))
	fx = append(fx, tick(c, true, ms(t0(), 400))...)
	if got := injects(fx); len(got) != 1 {
		t.Fatalf("the notice was not delivered: %q", got)
	}
	fx = reply(c, "I'm not sure what the timer was for.", ms(t0(), 500))
	fx = append(fx, ticks(c, ms(t0(), 500), 5*time.Second)...)
	if len(handOffs(fx)) != 0 {
		t.Fatal("handed off Pixie's reply to a notice")
	}
}

func TestHandOffNoticeFromTheDelegateResult(t *testing.T) {
	n, _ := HandOffNotice("what's on my calendar", json.RawMessage(`{"job_id":"job_0123456789ab","status":"delegated"}`))
	if n.Text != "Coordinator: handed off: what's on my calendar" {
		t.Fatalf("%q", n.Text)
	}
	n, why := HandOffNotice("x", json.RawMessage(`{"error":"Delegation is off (Coordinator: herdr not found)."}`))
	if n.Text != "Coordinator: can't hand off: delegation is off (herdr not found)." || why != "delegation is off (herdr not found)" {
		t.Fatalf("%q / %q", n.Text, why)
	}
	if n, _ := HandOffNotice("x", json.RawMessage(`{"error":"The app is quitting."}`)); n.Text != "" {
		t.Fatalf("another failure says nothing: %q", n.Text)
	}
	if n, _ := HandOffNotice("x", nil); n.Text != "" {
		t.Fatalf("no output says nothing: %q", n.Text)
	}
}

// ---- through the runtime, with a fake delegate tool ----

type fakeDelegate struct {
	off string // non-empty: delegation is off with this status line

	mu    sync.Mutex
	calls []map[string]string
}

func (*fakeDelegate) Name() string                { return DelegateTool }
func (*fakeDelegate) Description() string         { return "delegate" }
func (*fakeDelegate) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (d *fakeDelegate) Invoke(_ context.Context, raw json.RawMessage) ToolOutcome {
	var args map[string]string
	_ = json.Unmarshal(raw, &args)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.off != "" {
		return ToolOutcome{Output: json.RawMessage(`{"error":"Delegation is off (` + d.off + `)."}`)}
	}
	d.calls = append(d.calls, args)
	return ToolOutcome{Output: json.RawMessage(`{"job_id":"job_0000000000aa","status":"delegated"}`), EndAfterReply: true}
}

func (d *fakeDelegate) jobs() []map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]map[string]string(nil), d.calls...)
}

func runHandOff(t *testing.T, d *fakeDelegate, on bool) (*fakeModel, *fakeSession) {
	t.Helper()
	m := &fakeModel{sess: newFakeSession()}
	ui := &uiLog{}
	reg := DefaultTools()
	reg.Register(d)
	cc := DefaultConversationConfig()
	cc.HandOff = on
	r := NewRuntime(m, audio.Passthrough{}, RuntimeConfig{
		Session: testSessionCfg, APIKey: "k", MouthLevels: 4, Tools: reg, Conversation: cc,
	}, ui.emit)
	t.Cleanup(r.Close)
	startListening(t, r, m, ui)
	s := m.sess
	for _, ev := range []Event{
		evSpeechStarted, evSpeechStopped, said("user", "What's the price of bitcoin right now?"),
		evResponseCreated, said("assistant", "I don't know."), evResponseDone,
	} {
		s.events <- ev
	}
	return m, s
}

func waitLong(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRuntimeHandOffDelegatesAndInjects(t *testing.T) {
	d := &fakeDelegate{}
	_, s := runHandOff(t, d, true)
	waitLong(t, "the hand-off notice", func() bool { return len(s.commands(CmdInject)) == 1 })
	if got := s.commands(CmdInject)[0]; got.Text != "Coordinator: handed off: What's the price of bitcoin right now?" || !got.Respond {
		t.Fatalf("%+v", got)
	}
	jobs := d.jobs()
	if len(jobs) != 1 || jobs[0]["task"] != "What's the price of bitcoin right now?" {
		t.Fatalf("delegated: %v", jobs)
	}
	if len(s.commands(CmdToolResult)) != 0 {
		t.Fatal("the hand-off is not a tool result for the model")
	}
	// The delegate call started background work: once Pixie has said so, the conversation
	// ends (well before the 7 s idle timeout).
	for _, ev := range []Event{evResponseCreated, said("assistant", "I've handed that to the coordinator."), evResponseDone} {
		s.events <- ev
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the conversation stayed open after the hand-off was said")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRuntimeHandOffWithDelegationOffSaysWhy(t *testing.T) {
	d := &fakeDelegate{off: "Coordinator: herdr not found"}
	_, s := runHandOff(t, d, true)
	waitLong(t, "the reason notice", func() bool { return len(s.commands(CmdInject)) == 1 })
	if got := s.commands(CmdInject)[0].Text; got != "Coordinator: can't hand off: delegation is off (herdr not found)." {
		t.Fatalf("%q", got)
	}
	if len(d.jobs()) != 0 {
		t.Fatal("a job was started with delegation off")
	}
}

func TestRuntimeHandOffOffDoesNothing(t *testing.T) {
	d := &fakeDelegate{}
	_, s := runHandOff(t, d, false)
	time.Sleep(HandOffGrace + 500*time.Millisecond)
	if len(s.commands(CmdInject)) != 0 || len(d.jobs()) != 0 {
		t.Fatal("handed off with the setting off")
	}
}

func TestRuntimeHandOffNeedsADelegateTool(t *testing.T) {
	m := &fakeModel{sess: newFakeSession()}
	cc := DefaultConversationConfig()
	cc.HandOff = true
	r := NewRuntime(m, audio.Passthrough{}, RuntimeConfig{Session: testSessionCfg, APIKey: "k", Conversation: cc}, nil)
	defer r.Close()
	if r.conv.handOff {
		t.Fatal("hand-off armed without a delegate tool")
	}
}

// A hand-off the app made (the claim check or the safety net) ends the conversation once
// Pixie's acknowledgement has played, like a delegate call does: the work runs in the
// background and its result comes back on its own.
func TestAClaimedHandOffEndsTheConversationOnceItIsTrue(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "build me a small dashboard app", ms(t0(), 100))
	reply(c, "I've handed that off to the coordinator.", ms(t0(), 500))
	if h := handOffs(ticks(c, ms(t0(), 500), 3*time.Second)); len(h) != 1 || !h[0].Claim {
		t.Fatalf("%+v", h)
	}
	c.Step(HandOffDoneAfterReplyInput(Notice{}), ms(t0(), 3600))
	ticks(c, ms(t0(), 3600), time.Second)
	if c.IsActive() {
		t.Fatal("still open after the claimed hand-off came true")
	}
}

func TestASafetyNetHandOffEndsAfterPixieSaysSo(t *testing.T) {
	c := newHandOffConv(true)
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	reply(c, "I'm not sure, I can't check live prices.", ms(t0(), 500))
	if len(handOffs(ticks(c, ms(t0(), 500), 3*time.Second))) != 1 {
		t.Fatal("no hand-off")
	}
	fx := c.Step(HandOffDoneAfterReplyInput(Notice{Text: HandedOffPrefix + "What's the price of bitcoin right now?"}), ms(t0(), 3600))
	if len(injects(fx)) != 1 || !c.IsActive() {
		t.Fatalf("injects %v active %v: the notice must be said before the end", injects(fx), c.IsActive())
	}
	reply(c, "I've handed that to the coordinator; I'll let you know.", ms(t0(), 4000))
	ticks(c, ms(t0(), 4000), time.Second)
	if c.IsActive() {
		t.Fatal("still open after Pixie said it handed off")
	}
}

func TestAHandOffThatDoesntEndKeepsListening(t *testing.T) {
	// A "Check: " fact (or an answer to a coordinator question) stays open for its answer.
	c := newHandOffConv(true)
	userTurn(c, "Who won the game last night?", ms(t0(), 100))
	reply(c, "I don't know.", ms(t0(), 500))
	ticks(c, ms(t0(), 500), 3*time.Second)
	c.Step(HandOffDoneInput(Notice{}), ms(t0(), 3600))
	ticks(c, ms(t0(), 3600), 2*time.Second)
	if !c.IsActive() {
		t.Fatal("ended without being asked to")
	}
}

func TestTheUserTalkingCancelsTheEndAfterAHandOff(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "build me a small dashboard app", ms(t0(), 100))
	reply(c, "I've handed that off to the coordinator.", ms(t0(), 500))
	ticks(c, ms(t0(), 500), 3*time.Second)
	userTurn(c, "and make it dark", ms(t0(), 3550))
	c.Step(HandOffDoneAfterReplyInput(Notice{}), ms(t0(), 3600))
	ticks(c, ms(t0(), 3600), time.Second)
	if !c.IsActive() {
		t.Fatal("ended while the user was saying more")
	}
}
