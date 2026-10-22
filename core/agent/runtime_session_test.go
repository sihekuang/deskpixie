package agent_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/voice"
)

func TestRuntimeRoutesSessionToolsEvenWithWorkersOff(t *testing.T) {
	r := wRigWith(t, false)
	got := call(r, Coordinator, McpCall{Kind: CallListSessions})
	if got.Err != nil {
		t.Fatalf("list_sessions must not depend on workers: %v", got.Err)
	}
	if rows, _ := got.Value.(map[string]any)["sessions"].([]SessionRow); rows == nil {
		t.Fatalf("%+v", got.Value)
	}
}

func TestRuntimeSessionToolsAreTheCoordinators(t *testing.T) {
	r := wRigWith(t, true)
	wantErr(t, call(r, "focus-fix", McpCall{Kind: CallListSessions}), "This tool is not available to you.")
}

// pixRig is a runtime (on real time: the composite host's event pump outlives a synctest bubble) whose session host also has one session you started, focus-claude.
func pixRig(t *testing.T) (*rtRig, *userFake) {
	user := &userFake{FakeSessionHost: agenttest.NewFakeSessionHost(), described: map[string]string{}}
	user.FakeSessionHost.StartAgent(context.Background(), AgentSpec{Name: "focus-claude"})
	r := rigOn(t, &agenttest.MemStore{}, false, func(h *agenttest.FakeSessionHost) SessionHost { return NewCompositeHost(h, user) })
	r.voice.SetOpen(true)
	r.h.Voice(VoiceSignal{Kind: voice.SigOpened})
	return r, user
}

func closeCall(r *rtRig) chan McpReply {
	reply := make(chan McpReply, 1)
	r.h.Deliver(HookEvent{Kind: HookMcp, Agent: Coordinator, Call: McpCall{Kind: CallCloseSession, Name: "focus-claude"}, McpReply: reply})
	return reply
}

func TestClosingYourSessionAsksFirst(t *testing.T) {
	// The app asks, whichever agent the coordinator is (a Codex coordinator approves every
	// pixie tool itself).
	{
		r, user := pixRig(t)
		reply := closeCall(r)
		asked := agenttest.InjectCall("Coordinator question: Close your session login bug? Say confirm hawk.")
		if !eventually(func() bool { return hasCall(r.voice.Calls(), asked) }) {
			t.Fatal(r.voice.Calls())
		}
		if _, ok := user.StatusOf("focus-claude"); !ok {
			t.Fatal("closed before you confirmed")
		}
		r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"})
		if got := <-reply; got.Err != nil {
			t.Fatal(got.Err)
		}
		if _, ok := user.StatusOf("focus-claude"); ok {
			t.Fatal("not closed after you confirmed")
		}
	}
}

func TestNoLeavesYourSessionRunning(t *testing.T) {
	{
		r, user := pixRig(t)
		reply := closeCall(r)
		settle()
		r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "no hawk"})
		settle()
		got := <-reply
		if got.Err == nil || !strings.Contains(got.Err.Error(), "said no") {
			t.Fatalf("%+v", got)
		}
		if _, ok := user.StatusOf("focus-claude"); !ok {
			t.Fatal("closed although you said no")
		}
	}
}

func messageCall(r *rtRig, text string) chan McpReply {
	reply := make(chan McpReply, 1)
	r.h.Deliver(HookEvent{Kind: HookMcp, Agent: Coordinator, Call: McpCall{Kind: CallMessageSession, Name: "focus-claude", Text: text}, McpReply: reply})
	return reply
}

// #141: a message that has your session push or merge goes through the same spoken confirm
// word as the coordinator's own git push; message_session itself is never asked about.
func TestAMessageThatPushesOrMergesAsksFirst(t *testing.T) {
	r, user := pixRig(t)
	reply := messageCall(r, "Commit, push, open one PR covering #137 and #138, and merge it.")
	settle()
	if !hasCall(r.voice.Calls(), agenttest.InjectCall("Coordinator question: Message your session login bug, asking it to push and merge? Say confirm hawk.")) {
		t.Fatal(r.voice.Calls())
	}
	if len(user.Prompts()) != 0 {
		t.Fatal("sent before you confirmed")
	}
	r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"})
	settle()
	if got := <-reply; got.Err != nil {
		t.Fatal(got.Err)
	}
	if p := user.Prompts(); len(p) != 1 || !strings.Contains(p[0].Text, "merge it") {
		t.Fatalf("%+v", p)
	}
}

// #246: the coordinator's tool call gives up after about a minute, so a confirm that takes longer
// is answered at once (not sent yet, don't send again) and the message goes out when you confirm.
func TestAMessageWaitingOnYourConfirmIsAnsweredInTimeAndSentWhenYouConfirm(t *testing.T) {
	r, user := pixRig(t)
	SetConfirmWaitForTest(r.h, 100*time.Millisecond)
	reply := messageCall(r, "Go ahead and git push.")
	got := <-reply
	v, _ := got.Value.(map[string]any)
	if got.Err != nil || v["status"] != "awaiting_confirmation" || !strings.Contains(fmt.Sprint(v["note"]), "Don't call it again") {
		t.Fatalf("%+v", got)
	}
	if len(user.Prompts()) != 0 {
		t.Fatal("sent before you confirmed")
	}
	r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"})
	if !eventually(func() bool { return len(user.Prompts()) == 1 }) {
		t.Fatalf("not sent once you confirmed: %+v", user.Prompts())
	}
}

func TestClosingWaitsOnALateConfirmAndClosesOnceYouConfirm(t *testing.T) {
	r, user := pixRig(t)
	SetConfirmWaitForTest(r.h, 100*time.Millisecond)
	got := <-closeCall(r)
	if v, _ := got.Value.(map[string]any); got.Err != nil || v["status"] != "awaiting_confirmation" {
		t.Fatalf("%+v", got)
	}
	if _, ok := user.StatusOf("focus-claude"); !ok {
		t.Fatal("closed before you confirmed")
	}
	r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"})
	if !eventually(func() bool { _, ok := user.StatusOf("focus-claude"); return !ok }) {
		t.Fatal("not closed once you confirmed")
	}
}

// A late send is told on the request that asked for it, or on its own once that request is over.
func TestALateSendIsToldEvenAfterTheRequestEnded(t *testing.T) {
	r, user := pixRig(t)
	SetConfirmWaitForTest(r.h, 100*time.Millisecond)
	<-messageCall(r, "Go ahead and git push.")
	r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"})
	if !eventually(func() bool { return len(user.Prompts()) == 1 }) {
		t.Fatal("not sent")
	}
	if !eventually(func() bool { return strings.Contains(toldText(r), "Passed on to your session") }) {
		t.Fatal(toldText(r))
	}
}

// Calling again while the first waits on your confirm asks nothing more, and sends once.
func TestAgainWhileAwaitingConfirmAsksOnceAndSendsOnce(t *testing.T) {
	r, user := pixRig(t)
	SetConfirmWaitForTest(r.h, 100*time.Millisecond)
	<-messageCall(r, "Go ahead and git push.")
	got := <-messageCall(r, "Go ahead and git push.")
	if v, _ := got.Value.(map[string]any); got.Err != nil || v["status"] != "awaiting_confirmation" {
		t.Fatalf("%+v", got)
	}
	r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"})
	settle()
	settle()
	if p := user.Prompts(); len(p) != 1 {
		t.Fatalf("%+v", p)
	}
	if n := strings.Count(fmt.Sprint(r.voice.Calls()), "Coordinator question:"); n != 1 {
		t.Fatalf("asked %d times: %v", n, r.voice.Calls())
	}
}

// #249: the coordinator ends its request once told the confirm waits; the confirm outlives it and
// the message goes when you confirm (closing the request used to deny it, silently).
func TestALateConfirmOutlivesTheRequestThatAskedIt(t *testing.T) {
	r, user := pixRig(t)
	SetConfirmWaitForTest(r.h, 100*time.Millisecond)
	if _, err := r.h.Delegate("tell my login session to push", ""); err != nil {
		t.Fatal(err)
	}
	settle()
	got := <-messageCall(r, "Go ahead and git push.")
	if v, _ := got.Value.(map[string]any); v["status"] != "awaiting_confirmation" {
		t.Fatalf("%+v", got)
	}
	stopHook(r, `<spoken ref="job_000000000001" status="done">It waits for you to confirm the push.</spoken>`)
	settle()
	if len(user.Prompts()) != 0 {
		t.Fatal("sent before you confirmed")
	}
	if len(r.h.Updates()) == 0 || r.h.Updates()[len(r.h.Updates())-1].Token != "hawk" {
		t.Fatalf("the confirm left the bubble: %+v", r.h.Updates())
	}
	r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"})
	if !eventually(func() bool { return len(user.Prompts()) == 1 }) {
		t.Fatalf("not sent once you confirmed: %+v", user.Prompts())
	}
}

func TestNoToALateConfirmKeepsTheMessageUnsent(t *testing.T) {
	r, user := pixRig(t)
	SetConfirmWaitForTest(r.h, 100*time.Millisecond)
	<-messageCall(r, "Go ahead and git push.")
	r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "no hawk"})
	settle()
	settle()
	if len(user.Prompts()) != 0 {
		t.Fatal("sent although you said no")
	}
}

func TestNoKeepsAPushMessageUnsent(t *testing.T) {
	r, user := pixRig(t)
	reply := messageCall(r, "Go ahead and git push.")
	settle()
	r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "no hawk"})
	settle()
	if got := <-reply; got.Err == nil || !strings.Contains(got.Err.Error(), "said no") {
		t.Fatalf("%+v", got)
	}
	if len(user.Prompts()) != 0 {
		t.Fatal("sent although you said no")
	}
}

// #152: a message that only forbids a push, as a careful coordinator writes it, is not one.
func TestAMessageThatForbidsPushingIsSentWithoutAsking(t *testing.T) {
	r, user := pixRig(t)
	if got := <-messageCall(r, "Is there anything committed on this branch? Please don't push anything or change anything, just tell me what's committed."); got.Err != nil {
		t.Fatal(got.Err)
	}
	if p := user.Prompts(); len(p) != 1 || !strings.Contains(p[0].Text, "don't push") {
		t.Fatalf("%+v", p)
	}
	if len(r.voice.Calls()) != 0 {
		t.Fatalf("asked although nothing is pushed: %v", r.voice.Calls())
	}
}

func TestAnOrdinaryMessageIsSentWithoutAsking(t *testing.T) {
	r, user := pixRig(t)
	if got := <-messageCall(r, "Run the tests and tell me what fails."); got.Err != nil {
		t.Fatal(got.Err)
	}
	if len(user.Prompts()) != 1 {
		t.Fatalf("%+v", user.Prompts())
	}
}

// coordinatorText is everything typed into the coordinator so far.
func coordinatorText(r *rtRig) string {
	var b strings.Builder
	for _, p := range r.host.Prompts() {
		b.WriteString(p.Text + "\n")
	}
	return b.String()
}

// #153: a new request carries the sessions you have open, so the coordinator sees the one
// already working on it without having to remember to look.
func TestANewRequestListsYourOpenSessions(t *testing.T) {
	r, user := pixRig(t)
	user.setTurn("Fixed the redirect after sign-in.", false)
	delegate(t, r, "is the login fix pushed?")
	settle()
	got := coordinatorText(r)
	for _, want := range []string{"is the login fix pushed?", "Your open pix sessions", `focus-claude: "login bug"`, "focus repo", "idle", `last turn "Fixed the redirect after sign-in."`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestANewRequestSaysNothingAboutSessionsWhenNoneAreOpen(t *testing.T) {
	r := rigWith(t, &agenttest.MemStore{}, false)
	delegate(t, r, "summarise the issues")
	settle()
	if got := coordinatorText(r); !strings.Contains(got, "summarise the issues") || strings.Contains(got, "pix sessions") {
		t.Fatal(got)
	}
}

// Recall asks what the coordinator remembers: no work, so no sessions to hand it to.
// #161: Pixie relays a session's question; the user's answer, through answer_job without a job
// id, is typed into that session.
func TestAnAnswerToASessionsQuestionIsTypedIntoIt(t *testing.T) {
	user := &userFake{FakeSessionHost: agenttest.NewFakeSessionHost(), described: map[string]string{}}
	user.FakeSessionHost.StartAgent(context.Background(), AgentSpec{Name: "focus-claude"})
	cfg := config(false)
	cfg.Settings.SessionTurns = true
	r := build(t, &agenttest.MemStore{}, cfg, func(h *agenttest.FakeSessionHost) SessionHost { return NewCompositeHost(h, user) }, nil)
	r.voice.SetOpen(true)
	r.h.Voice(VoiceSignal{Kind: voice.SigOpened})
	r.h.Input(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: "focus-claude", Turn: &SessionTurn{Label: "focus: release",
		Reply: "The build is ready.\n\nShould I open the PR?", FromPixie: true}}))
	settle()
	got := invoke(t, AnswerJob{H: r.h}, `{"answer":"Yes, open it."}`)
	if !reflect.DeepEqual(got, jsonOf(`{"to":"the session that asked","status":"answered"}`)) {
		t.Fatalf("%v", got)
	}
	settle()
	if p := user.Prompts(); len(p) != 1 || p[0].Agent != "focus-claude" || p[0].Text != "Yes, open it." {
		t.Fatalf("typed into the session: %+v", p)
	}
}

// #205: with agent.alwaysForceSend on, the coordinator's message to your session is sent in at
// once (a waiting one is flushed) though it didn't ask to force it; off, it waits.
func TestAlwaysForceSendFlushesEveryMessage(t *testing.T) {
	for _, on := range []bool{false, true} {
		user := &userFake{FakeSessionHost: agenttest.NewFakeSessionHost(), described: map[string]string{}}
		user.FakeSessionHost.StartAgent(context.Background(), AgentSpec{Name: "focus-claude"})
		d := &deliveringFake{userFake: user, delivery: Delivery{Status: "waiting", Reason: "typing"}, flushResult: Delivery{Status: "typed"}}
		cfg := config(false)
		cfg.Settings.AlwaysForceSend = on
		r := build(t, &agenttest.MemStore{}, cfg, func(h *agenttest.FakeSessionHost) SessionHost { return NewCompositeHost(h, d) }, nil)
		got := <-messageCall(r, "keep going")
		if got.Err != nil {
			t.Fatal(got.Err)
		}
		want, status := 0, "waiting"
		if on {
			want, status = 1, "sent"
		}
		if v, _ := got.Value.(map[string]any); len(d.flushed) != want || v["status"] != status {
			t.Fatalf("on=%v: flushed %v, %v", on, d.flushed, got.Value)
		}
	}
}

// toldText is everything said or shown to you so far: spoken lines and the updates bubble.
func toldText(r *rtRig) string {
	return fmt.Sprint(r.voice.Calls()) + fmt.Sprintf("%+v", r.h.Updates())
}
