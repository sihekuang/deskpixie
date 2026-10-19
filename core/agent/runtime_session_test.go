package agent_test

import (
	"context"
	"strings"
	"testing"

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
		settle()
		if !hasCall(r.voice.Calls(), agenttest.InjectCall("Coordinator question: Close your session login bug? Say confirm hawk.")) {
			t.Fatal(r.voice.Calls())
		}
		if _, ok := user.StatusOf("focus-claude"); !ok {
			t.Fatal("closed before you confirmed")
		}
		r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"})
		settle()
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
