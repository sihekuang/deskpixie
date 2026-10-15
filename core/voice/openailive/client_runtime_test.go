package openailive

// The client delegation flow end to end (#70): a real voice.Runtime with this adapter, talking
// to the fake GPT-Live (conformance_test.go) in client delegation mode, a fake mic and speaker,
// and fake agent tools that record what they get.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/voice"
	vt "github.com/sihekuang/deskpixie/core/voice/voicetest"
)

// fakeAgentTool records its arguments and answers with out (EndAfterReply as the real
// delegate tool sets it: work ends the conversation once acknowledged, a "Check:" doesn't).
type fakeAgentTool struct {
	name string
	out  string
	mu   sync.Mutex
	args []map[string]any
}

func (f *fakeAgentTool) Name() string                { return f.name }
func (f *fakeAgentTool) Description() string         { return f.name }
func (f *fakeAgentTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeAgentTool) Invoke(_ context.Context, raw json.RawMessage) voice.ToolOutcome {
	var a map[string]any
	_ = json.Unmarshal(raw, &a)
	f.mu.Lock()
	f.args = append(f.args, a)
	f.mu.Unlock()
	task, _ := a["task"].(string)
	return voice.ToolOutcome{Output: json.RawMessage(f.out), EndAfterReply: f.name == voice.DelegateTool && !strings.HasPrefix(task, "Check:")}
}

func (f *fakeAgentTool) calls() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.args...)
}

type clientRig struct {
	t      *testing.T
	fs     *fakeLive
	rt     *voice.Runtime
	mic    *vt.FakeMic
	player *vt.Player
}

func newClientRig(t *testing.T, fs *fakeLive, agentTools ...voice.Tool) *clientRig {
	srv := httptest.NewServer(http.HandlerFunc(fs.serve))
	t.Cleanup(srv.Close)
	reg := voice.DefaultTools()
	for _, tool := range agentTools {
		reg.Register(tool)
	}
	sink := &vt.Sink{}
	rt := voice.NewRuntime(New("ws"+strings.TrimPrefix(srv.URL, "http")), nil, voice.RuntimeConfig{
		Session: voice.SessionConfig{Instructions: "be Bit", ClientDelegation: true},
		APIKey:  "k", Tools: reg, Conversation: voice.DefaultConversationConfig(),
	}, sink.Emit)
	r := &clientRig{t: t, fs: fs, rt: rt, mic: vt.NewFakeMic(vt.MicLiveAtOnce)}
	r.mic.Attach(rt, sink)
	r.player = vt.StartPlayer(rt)
	t.Cleanup(func() { r.mic.Stop(); r.player.Stop(); rt.Close() })
	rt.Hotkey()
	r.wait("the conversation to open", rt.IsOpen)
	return r
}

func (r *clientRig) wait(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s; client events %v", what, r.fs.clientTypes())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (s *fakeLive) clientTypes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.clients...)
}

// withDelegation returns the client events of type typ that carry delegation id.
func (s *fakeLive) withDelegation(typ, id string) []map[string]any {
	var out []map[string]any
	for _, m := range s.sent(typ) {
		if m["delegation_id"] == id {
			out = append(out, m)
		}
	}
	return out
}

// A work request after some talk: the task is the user's words, the earlier talk its context,
// GPT-Live is told it was handed off, and the conversation ends once Bit has said so.
func TestClientDelegationHandsTheTurnToTheAgent(t *testing.T) {
	del := &fakeAgentTool{name: voice.DelegateTool, out: `{"job_id":"job_1","status":"delegated"}`}
	fs := &fakeLive{client: true, script: []confReply{{frames: 3}, {frames: 3, tool: voice.ClientDelegationTool}},
		said: []string{"the tmux tests were flaky yesterday", "can you look into it"}}
	r := newClientRig(t, fs, del, &fakeAgentTool{name: voice.AnswerJobTool, out: `{"error":"No job is waiting for an answer."}`})
	r.mic.Say(300)
	time.Sleep(1200 * time.Millisecond) // Bit's first reply
	r.mic.Say(300)
	r.wait("the delegate call", func() bool { return len(del.calls()) == 1 })
	got := del.calls()[0]
	if got["task"] != "can you look into it" {
		t.Fatalf("task %q", got["task"])
	}
	if ctx, _ := got["context"].(string); !strings.Contains(ctx, "User: the tmux tests were flaky yesterday") {
		t.Fatalf("context %q", ctx)
	}
	r.wait("the hand-off commentary", func() bool { return len(fs.withDelegation("session.commentary.append", "item_1")) == 1 })
	if c := fs.withDelegation("session.commentary.append", "item_1")[0]["content"].(string); !strings.Contains(c, "job_1") {
		t.Fatalf("commentary %q", c)
	}
	r.wait("the conversation to end after Bit says so", func() bool { return !r.rt.IsOpen() })
}

// A question is a "Check:" that stays open; the agent's reply later goes in as commentary for
// the whole session (no delegation id).
func TestClientDelegationQuestionThenAgentReply(t *testing.T) {
	del := &fakeAgentTool{name: voice.DelegateTool, out: `{"job_id":"job_2","status":"delegated"}`}
	fs := &fakeLive{client: true, script: []confReply{{frames: 3, tool: voice.ClientDelegationTool}},
		said: []string{"what's the weather in Paris?"}}
	r := newClientRig(t, fs, del)
	r.mic.Say(300)
	r.wait("the delegate call", func() bool { return len(del.calls()) == 1 })
	if task := del.calls()[0]["task"]; task != "Check: what's the weather in Paris?" {
		t.Fatalf("task %q", task)
	}
	r.wait("the quiet acknowledgement", func() bool { return len(fs.withDelegation("session.thinking.append", "item_1")) == 1 })
	r.rt.Inject(voice.Notice{Text: "Agent reply: 17 degrees and light rain in Paris."})
	r.wait("the reply as commentary", func() bool {
		for _, m := range fs.sent("session.commentary.append") {
			if m["delegation_id"] == nil && strings.Contains(m["content"].(string), "17 degrees") {
				return true
			}
		}
		return false
	})
	if !r.rt.IsOpen() {
		t.Fatal("a fact check closed the conversation")
	}
}

// After an "Agent question:", the user's answer goes to answer_job, not a new job (#75).
func TestClientDelegationAnswersAWaitingQuestion(t *testing.T) {
	del := &fakeAgentTool{name: voice.DelegateTool, out: `{"job_id":"job_9","status":"delegated"}`}
	ans := &fakeAgentTool{name: voice.AnswerJobTool, out: `{"job_id":"job_3","status":"answered"}`}
	fs := &fakeLive{client: true, script: []confReply{{frames: 3, tool: voice.ClientDelegationTool}},
		said: []string{"make it blue"}}
	r := newClientRig(t, fs, del, ans)
	r.rt.Inject(voice.Notice{Text: "Agent question: what colour should the chart be?"})
	r.wait("the question as commentary", func() bool { return len(fs.sent("session.commentary.append")) == 1 })
	time.Sleep(600 * time.Millisecond) // Bit asks it
	r.mic.Say(300)
	r.wait("answer_job", func() bool { return len(ans.calls()) == 1 })
	if a := ans.calls()[0]["answer"]; a != "make it blue" {
		t.Fatalf("answer %q", a)
	}
	if len(del.calls()) != 0 {
		t.Fatalf("a new job was started: %v", del.calls())
	}
	r.wait("the answer acknowledgement", func() bool { return len(fs.withDelegation("session.commentary.append", "item_1")) == 1 })
}

// Without an agent, GPT-Live is told why it can't hand the turn off.
func TestClientDelegationWithoutAnAgentSaysWhy(t *testing.T) {
	fs := &fakeLive{client: true, script: []confReply{{frames: 3, tool: voice.ClientDelegationTool}},
		said: []string{"can you fix the failing test"}}
	r := newClientRig(t, fs)
	r.mic.Say(300)
	r.wait("the commentary", func() bool { return len(fs.withDelegation("session.commentary.append", "item_1")) == 1 })
	c := fs.withDelegation("session.commentary.append", "item_1")[0]["content"].(string)
	if !strings.Contains(c, "can't be handed off") || !strings.Contains(c, "turned off in Settings") {
		t.Fatalf("commentary %q", c)
	}
}
