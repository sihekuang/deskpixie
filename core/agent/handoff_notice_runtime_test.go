package agent_test

import (
	"testing"
	"testing/synctest"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/voice"
)

// #177: the coordinator's message_session reaching your session is said at once, with the
// session's description, before the coordinator's own reply.
func TestTheRuntimeSaysAHandOffAsSoonAsTheSessionHasIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, false)
		r.user.about = map[string][2]string{"repo-claude": {"repo", "deskpixie-landing: SEO branch"}}
		r.user.connect(t, "repo-claude")
		time.Sleep(time.Second)
		r.voice.SetOpen(true)
		r.h.Voice(VoiceSignal{Kind: voice.SigOpened})
		invoke(t, Delegate{H: r.h}, `{"task":"keep going on the SEO branch"}`)
		time.Sleep(time.Second)
		reply := make(chan McpReply, 1)
		r.h.Deliver(HookEvent{Kind: HookMcp, Agent: Coordinator, Call: McpCall{Kind: CallMessageSession, Name: "repo-claude", Text: "keep going on the SEO branch"}, McpReply: reply})
		if got := <-reply; got.Err != nil {
			t.Fatal(got.Err)
		}
		time.Sleep(time.Second)
		if want := agenttest.InjectCall("Coordinator reply: Passed on to your session on deskpixie-landing: SEO branch."); !hasCall(r.voice.Calls(), want) {
			t.Fatalf("%v", r.voice.Calls())
		}
	})
}
