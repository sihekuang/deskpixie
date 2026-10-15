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
