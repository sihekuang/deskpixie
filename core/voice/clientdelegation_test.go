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
	want := "Earlier in this voice conversation:\nUser: the tmux tests were flaky yesterday\nPixie: Oh no, that's annoying."
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
	// The older "Agent question:" lead-in still counts.
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
	c.Step(InjectInput(Notice{Text: "Coordinator question: may I delete it? Say confirm hawk."}), ms(t0(), 9000))
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
	off.Register(&recTool{name: DelegateTool, out: `{"error":"Delegation is off (Coordinator: herdr not found)."}`})
	if r, _ := route(t, off, ClientDelegationArgs{Task: "fix it"}); !strings.HasPrefix(r.Error, "Delegation is off (Coordinator: herdr") {
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
		// #93: the usual ways of signing off.
		{"Thank you.", true, false, false},
		{"Thanks!", true, false, false},
		{"Okay, thank you.", true, false, false},
		{"Perfect, thanks.", true, false, false},
		{"That's all, thanks.", true, false, false},
		{"That's it for now.", true, false, false},
		{"Thanks Pixie, that's all for now.", true, false, false},
		{"Bye Pixie.", true, false, false},
		{"Goodbye Pixie.", true, false, false},
		{"I'm good, thanks.", true, false, false},
		{"Nope, I'm good.", true, false, false},
		{"Nothing else.", true, false, false},
		{"We're done here.", true, true, false}, // "were" opens a question; harmless here
		{"Never mind.", true, false, false},
		{"Okay, I'm all set.", true, false, false},
		// A thanks after a request, or before one, is not a goodbye.
		{"set a timer for ten minutes, thanks", false, false, false},
		{"thanks, now make it blue", false, false, false},
		{"thank you, can you also check the build", false, false, false},
		{"okay", false, false, false},
		{"good", false, false, false},
		{"the build is all good", false, false, false},
	} {
		if IsGoodbye(tc.said) != tc.bye || IsQuestion(tc.said) != tc.question || IsConfirmWords(tc.said) != tc.confirm {
			t.Errorf("%q: bye=%v question=%v confirm=%v", tc.said, IsGoodbye(tc.said), IsQuestion(tc.said), IsConfirmWords(tc.said))
		}
	}
}

// #75 in client mode: Bit claims it passed the user's answer on without a delegation; the
// app's hand-off carries Answer, so the router gives it to answer_job, not a new job.
func TestClientModeClaimedAnswerIsHandedOffAsAnAnswer(t *testing.T) {
	c := newClientConv()
	c.Step(InjectInput(Notice{Text: "Coordinator question: what colour should the chart be?"}), ms(t0(), 100))
	reply(c, "What colour should the chart be?", ms(t0(), 400))
	userTurn(c, "make it blue", ms(t0(), 2000))
	reply(c, "Got it, I've passed that on to the agent.", ms(t0(), 2500))
	h := handOffs(ticks(c, ms(t0(), 2500), 3*time.Second))
	if len(h) != 1 || !h[0].Answer || h[0].Text != "make it blue" {
		t.Fatalf("%+v", h)
	}

	reg := NewToolRegistry()
	del := &recTool{name: DelegateTool, out: `{"job_id":"job_new"}`}
	ans := &recTool{name: AnswerJobTool, out: `{"job_id":"job_3","status":"answered"}`}
	reg.Register(del)
	reg.Register(ans)
	raw, _ := json.Marshal(ClientDelegationArgs{Task: "make it blue", Answer: true})
	out := routedHandOff{clientDelegation{reg: reg}}.Invoke(context.Background(), raw)
	if string(out.Output) != `{"job_id":"job_3"}` || len(del.got) != 0 {
		t.Fatalf("%s %v", out.Output, del.got)
	}
	if n, _ := HandOffNotice("make it blue", out.Output); !strings.HasPrefix(n.Text, HandedOffPrefix) {
		t.Fatalf("%+v", n)
	}
}

// A pause splits one request into two transcribed turns, or a piece comes while Bit talks: the
// task is all of the user's words since Bit last spoke (bench: "The login tests were flaky" |
// "yesterday").
func TestClientDelegationJoinsASplitUtterance(t *testing.T) {
	c := newClientConv()
	reply(c, "Hi!", ms(t0(), 50))
	userTurn(c, "The login tests were flaky", ms(t0(), 100))
	userTurn(c, "yesterday", ms(t0(), 1200))
	if _, a := clientCall(c, "item_1", ms(t0(), 1300)); a.Task != "The login tests were flaky yesterday" ||
		a.Context != "Earlier in this voice conversation:\nPixie: Hi!" {
		t.Fatalf("%+v", a)
	}
	c2 := newClientConv()
	model(c2, evSpeechStarted, ms(t0(), 100))
	model(c2, said("user", "I'm working on the desk pixie repo"), ms(t0(), 200))
	model(c2, said("user", "today"), ms(t0(), 900))
	if _, a := clientCall(c2, "item_1", ms(t0(), 1000)); a.Task != "I'm working on the desk pixie repo today" {
		t.Fatalf("%+v", a)
	}
}

// #95: GPT-Live says the usual acknowledgement without delegating; the claim check hands the
// request off.
func TestClientModeOnItWithoutAHandOffIsHandedOff(t *testing.T) {
	c := newClientConv()
	userTurn(c, "I want you to build a Next.js application", ms(t0(), 1000))
	reply(c, "Sure, I'm on it. I'll let you know when it's done.", ms(t0(), 2500))
	h := handOffs(ticks(c, ms(t0(), 2500), 3*time.Second))
	if len(h) != 1 || h[0].Text != "I want you to build a Next.js application" {
		t.Fatalf("%+v", h)
	}
}

// A question in the "while you were away" replay (how a conversation opened for an update
// starts) asks too: the next turn answers it, unless the question is a confirm prompt.
func TestClientDelegationAfterAReplayedQuestionIsAnAnswer(t *testing.T) {
	c := newClientConv()
	c.Step(InjectInput(Notice{Text: "Coordinator replies while you were away:\n- Coordinator reply: Found it.\n- Coordinator question: Which repo should I push?"}), ms(t0(), 100))
	reply(c, "Which repo should I push?", ms(t0(), 400))
	userTurn(c, "the spacex one", ms(t0(), 2000))
	if _, a := clientCall(c, "item_1", ms(t0(), 2100)); !a.Answer {
		t.Fatalf("%+v", a)
	}
	c.Step(InjectInput(Notice{Text: "Coordinator replies while you were away:\n- Coordinator question: Run git push? Say confirm hawk."}), ms(t0(), 9000))
	userTurn(c, "yes do it", ms(t0(), 12000))
	if _, a := clientCall(c, "item_2", ms(t0(), 12100)); a.Answer {
		t.Fatalf("%+v", a)
	}
}

// #161: an answer the app typed into the session that asked is answered, not a new request;
// a session's turn that asks is a question to answer, its prompts in the bubble are not.
func TestClientDelegationAnswersASessionsQuestion(t *testing.T) {
	reg := NewToolRegistry()
	del := &recTool{name: DelegateTool, out: `{"job_id":"job_1","status":"delegated"}`}
	reg.Register(del)
	reg.Register(&recTool{name: AnswerJobTool, out: `{"to":"the session that asked","status":"answered"}`})
	if r, _ := route(t, reg, ClientDelegationArgs{Task: "yes, do it", Answer: true}); r.Routed != "answer_job" || len(del.got) != 0 {
		t.Fatalf("%+v %v", r, del.got)
	}
	for text, want := range map[string]bool{
		"Session update: deskpixie: hand-off is asking: should I rebuild it and open the PR?":     true,
		"Coordinator replies while you were away:\n- Session update: api is asking: ship it?":     true,
		"Session update: deskpixie is asking: Which file? Pick 1 or 2 in the bubble.":             false,
		"Session update: deskpixie: hand-off is done: the suite passed.":                          false,
		"Session update: deskpixie: Run make release. Say confirm hawk, or choose in the bubble.": false,
	} {
		if got := asksQuestion(text); got != want {
			t.Errorf("asksQuestion(%q) = %v", text, got)
		}
	}
}
