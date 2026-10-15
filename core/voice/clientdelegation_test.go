package voice

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Client delegation (#70): the conversation builds the task; the router picks the agent tool.

func newClientConv() *Conversation {
	c := NewConversation(ConversationConfig{IdleTimeout: testTimeout, HoldTimeout: testTimeout,
		ProviderTurns: true, HandOff: true, CheckClaims: true, ClaimTools: []string{DelegateTool}, ClientDelegation: true})
	c.Step(HotkeyInput(), t0())
	model(c, evReady, t0())
	c.Step(MicLiveInput(), t0())
	return c
}

func clientCall(c *Conversation, id string, at time.Time) (Effect, ClientDelegationArgs) {
	for _, e := range model(c, Event{Kind: EvToolCall, CallID: id, ToolName: ClientDelegationTool, Args: json.RawMessage(`{}`)}, at) {
		if e.Kind == EfInvokeTool {
			var a ClientDelegationArgs
			_ = json.Unmarshal(e.Args, &a)
			return e, a
		}
	}
	return Effect{}, ClientDelegationArgs{}
}

func TestClientDelegationTaskIsTheUsersWordsWithEarlierTurnsAsContext(t *testing.T) {
	c := newClientConv()
	userTurn(c, "the tmux tests were flaky yesterday", ms(t0(), 100))
	reply(c, "Oh no, that's annoying.", ms(t0(), 600))
	userTurn(c, "can you look into it?", ms(t0(), 3000))
	e, a := clientCall(c, "item_1", ms(t0(), 3100))
	if e.ToolName != ClientDelegationTool || e.CallID != "item_1" || e.Canned != nil {
		t.Fatalf("%+v", e)
	}
	if a.Task != "can you look into it?" || a.Answer {
		t.Fatalf("args %+v", a)
	}
	want := "Earlier in this voice conversation:\nUser: the tmux tests were flaky yesterday\nBit: Oh no, that's annoying."
	if a.Context != want {
		t.Fatalf("context %q", a.Context)
	}
	// It counts as the turn's delegate call: no hand-off or claim correction follows.
	fx := reply(c, "I'll hand that to the agent.", ms(t0(), 3200))
	fx = append(fx, ticks(c, ms(t0(), 3200), 3*time.Second)...)
	if len(handOffs(fx)) != 0 || len(injects(fx)) != 0 {
		t.Fatalf("hand-off %v injects %v", handOffs(fx), injects(fx))
	}
}

func TestClientDelegationOfAQuestionIsACheck(t *testing.T) {
	c := newClientConv()
	userTurn(c, "What's the weather like in Paris?", ms(t0(), 100))
	if _, a := clientCall(c, "item_1", ms(t0(), 200)); a.Task != "Check: What's the weather like in Paris?" {
		t.Fatalf("%+v", a)
	}
}

func TestClientDelegationAfterAnAgentQuestionIsAnAnswer(t *testing.T) {
	c := newClientConv()
	fx := c.Step(InjectInput(Notice{Text: "Agent question: which branch?"}), ms(t0(), 100))
	if len(injects(fx)) != 1 {
		t.Fatalf("%v", fx)
	}
	reply(c, "Which branch should it use?", ms(t0(), 400))
	userTurn(c, "main", ms(t0(), 2000))
	if _, a := clientCall(c, "item_1", ms(t0(), 2100)); !a.Answer || a.Task != "main" {
		t.Fatalf("%+v", a)
	}
	// Only the first turn after the question answers it.
	userTurn(c, "and what's the time in Tokyo?", ms(t0(), 6000))
	if _, a := clientCall(c, "item_2", ms(t0(), 6100)); a.Answer {
		t.Fatalf("%+v", a)
	}
	// A confirm prompt is never answered through answer_job.
	c.Step(InjectInput(Notice{Text: "Agent question: may I delete it? Say confirm hawk."}), ms(t0(), 9000))
	userTurn(c, "yes do it", ms(t0(), 12000))
	if _, a := clientCall(c, "item_3", ms(t0(), 12100)); a.Answer {
		t.Fatalf("%+v", a)
	}
}

// A turn the app already handed off (#71) isn't handed off twice.
func TestClientDelegationAfterTheAppsHandOffIsCanned(t *testing.T) {
	c := newClientConv()
	userTurn(c, "What's the price of bitcoin right now?", ms(t0(), 100))
	reply(c, "I'm not sure, I can't check live prices.", ms(t0(), 500))
	if h := handOffs(ticks(c, ms(t0(), 500), 3*time.Second)); len(h) != 1 {
		t.Fatalf("hand-offs %v", h)
	}
	if e, _ := clientCall(c, "item_1", ms(t0(), 4000)); e.Canned == nil {
		t.Fatalf("%+v", e)
	}
}

// Work ends the conversation once acknowledged, but not before GPT-Live had ClientAckGrace to
// say it handed it off.
func TestClientDelegationEndsAfterTheAcknowledgement(t *testing.T) {
	c := newClientConv()
	userTurn(c, "fix the failing test", ms(t0(), 100))
	clientCall(c, "item_1", ms(t0(), 200))
	c.Step(ToolDoneAfterReplyInput("item_1"), ms(t0(), 210))
	reply(c, "Checking now.", ms(t0(), 900)) // finished before the grace
	fx := ticks(c, ms(t0(), 900), time.Second)
	if c.Phase() == PhaseIdle {
		t.Fatalf("ended before the grace: %v", fx)
	}
	ticks(c, ms(t0(), 2000), 2*time.Second)
	if c.Phase() != PhaseIdle {
		t.Fatalf("phase %v", c.Phase())
	}
}

type recTool struct {
	name, out string
	got       []string
}

func (r *recTool) Name() string                { return r.name }
func (r *recTool) Description() string         { return r.name }
func (r *recTool) Parameters() json.RawMessage { return json.RawMessage(`{}`) }
func (r *recTool) Invoke(_ context.Context, raw json.RawMessage) ToolOutcome {
	r.got = append(r.got, string(raw))
	return ToolOutcome{Output: json.RawMessage(r.out), EndAfterReply: r.name == DelegateTool}
}

func route(t *testing.T, reg *ToolRegistry, a ClientDelegationArgs) (ClientDelegationResult, ToolOutcome) {
	t.Helper()
	raw, _ := json.Marshal(a)
	out := clientDelegation{reg: reg}.Invoke(context.Background(), raw)
	var r ClientDelegationResult
	if err := json.Unmarshal(out.Output, &r); err != nil {
		t.Fatal(err)
	}
	return r, out
}

func TestClientDelegationRouting(t *testing.T) {
	newReg := func(answerOut string) (*ToolRegistry, *recTool, *recTool) {
		reg := NewToolRegistry()
		del := &recTool{name: DelegateTool, out: `{"job_id":"job_1","status":"delegated"}`}
		ans := &recTool{name: AnswerJobTool, out: answerOut}
		reg.Register(del)
		reg.Register(ans)
		return reg, del, ans
	}

	reg, del, ans := newReg(`{"job_id":"job_2","status":"answered"}`)
	r, out := route(t, reg, ClientDelegationArgs{Task: "fix the test", Context: "Earlier: x"})
	if r.Routed != "delegate" || r.JobID != "job_1" || r.Check || !out.EndAfterReply || len(ans.got) != 0 ||
		del.got[0] != `{"context":"Earlier: x","task":"fix the test"}` {
		t.Fatalf("%+v %v %v", r, del.got, ans.got)
	}
	if r, _ := route(t, reg, ClientDelegationArgs{Task: "Check: is it raining?"}); !r.Check {
		t.Fatalf("%+v", r)
	}

	reg, del, ans = newReg(`{"job_id":"job_2","status":"answered"}`)
	if r, _ := route(t, reg, ClientDelegationArgs{Task: "main", Answer: true}); r.Routed != "answer_job" || r.JobID != "job_2" ||
		ans.got[0] != `{"answer":"main"}` || len(del.got) != 0 {
		t.Fatalf("%+v %v", r, ans.got)
	}

	reg, del, _ = newReg(`{"error":"No job is waiting for an answer."}`)
	if r, _ := route(t, reg, ClientDelegationArgs{Task: "main", Answer: true}); r.Routed != "delegate" || len(del.got) != 1 {
		t.Fatalf("no job waiting should start one: %+v", r)
	}

	reg, del, _ = newReg(`{"error":"That job is waiting for a confirm word."}`)
	if r, _ := route(t, reg, ClientDelegationArgs{Task: "sure", Answer: true}); r.Routed != "none" || len(del.got) != 0 {
		t.Fatalf("%+v", r)
	}

	reg, del, ans = newReg(`{}`)
	if r, _ := route(t, reg, ClientDelegationArgs{Task: "confirm hawk"}); r.Routed != "none" || len(del.got)+len(ans.got) != 0 {
		t.Fatalf("a confirm word was sent on: %+v", r)
	}

	if r, _ := route(t, NewToolRegistry(), ClientDelegationArgs{Task: "fix it"}); r.Error != DelegationOffNoAgent {
		t.Fatalf("%+v", r)
	}
	off := NewToolRegistry()
	off.Register(&recTool{name: DelegateTool, out: `{"error":"Delegation is off (Agent: herdr not found)."}`})
	if r, _ := route(t, off, ClientDelegationArgs{Task: "fix it"}); !strings.HasPrefix(r.Error, "Delegation is off (Agent: herdr") {
		t.Fatalf("%+v", r)
	}
}

// The runtime declares the router to no model and keeps the claim check to delegate.
func TestRuntimeRegistersTheRouterOnlyForClientDelegation(t *testing.T) {
	reg := DefaultTools()
	reg.Register(&recTool{name: DelegateTool, out: `{}`})
	reg.Register(&recTool{name: TimerToolName, out: `{}`})
	rt := NewRuntime(&nopModel{}, nil, RuntimeConfig{Session: SessionConfig{ClientDelegation: true}, Tools: reg}, nil)
	defer rt.Close()
	if _, ok := rt.tools.Get(ClientDelegationTool); !ok {
		t.Fatal("no router")
	}
	for _, s := range rt.cfg.Session.Tools {
		if s.Name == ClientDelegationTool {
			t.Fatal("the router is declared to the model")
		}
	}
	if _, ok := reg.Get(ClientDelegationTool); ok {
		t.Fatal("the caller's registry was changed")
	}
	if got := rt.cfg.Conversation.ClaimTools; len(got) != 1 || got[0] != DelegateTool {
		t.Fatalf("claim tools %v", got)
	}
	plain := NewRuntime(&nopModel{}, nil, RuntimeConfig{Tools: reg}, nil)
	defer plain.Close()
	if _, ok := plain.tools.Get(ClientDelegationTool); ok {
		t.Fatal("router without client delegation")
	}
}

type nopModel struct{}

func (nopModel) Name() string { return "nop" }
func (nopModel) Connect(context.Context, SessionConfig, string) (Session, error) {
	return nil, context.Canceled
}

func TestTranscriptMatchers(t *testing.T) {
	for _, tc := range []struct {
		said                   string
		bye, question, confirm bool
	}{
		{"Thanks, that's all. Bye!", true, false, false},
		{"bye", true, false, false},
		{"ok talk to you later", true, false, false},
		{"goodbye for now?", false, true, false},
		{"can you say bye to Sam", false, false, false},
		{"What time is it?", false, true, false},
		{"what's the status of my jobs", false, true, false},
		{"check why the build failed", false, false, false},
		{"can you look into it?", false, false, false},
		{"is it going to rain", false, true, false},
		{"confirm hawk", false, false, true},
		{"ok confirm hawk", false, false, true},
		{"confirm that the build is green please", false, false, false},
	} {
		if IsGoodbye(tc.said) != tc.bye || IsQuestion(tc.said) != tc.question || IsConfirmWords(tc.said) != tc.confirm {
			t.Errorf("%q: bye=%v question=%v confirm=%v", tc.said, IsGoodbye(tc.said), IsQuestion(tc.said), IsConfirmWords(tc.said))
		}
	}
}
