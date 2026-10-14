package agent_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/voice"
)

// End-to-end delegation scenarios on fakes. Port of ai-mascot
// crates/agent-core/src/runtime_tests.rs (37 tests). Rust runs them in tokio's paused time;
// here each runs in a testing/synctest bubble, where time is fake and moves only when every
// goroutine is blocked: `settle` (sleep 50 ms) lets everything that can run, run.

type rtRig struct {
	h        *AgentHandle
	host     *agenttest.FakeSessionHost
	voice    *agenttest.RecordingVoiceLink
	alerts   *agenttest.RecordingNotifier
	status   *agenttest.RecordingStatus
	launcher *agenttest.FakeLauncher
	store    *agenttest.MemStore
	clock    *agenttest.ManualClock
}

func config(warm bool) RuntimeConfig {
	return RuntimeConfig{Settings: DefaultSettings(), Warm: warm}
}

func build(t *testing.T, store *agenttest.MemStore, cfg RuntimeConfig, wrap func(*agenttest.FakeSessionHost) SessionHost, launch TerminalLauncher) *rtRig {
	host := agenttest.NewFakeSessionHost().ManualTurns()
	clock := agenttest.ClockAt(1_000_000)
	recipe := CoordinatorRecipe{Cwd: "/data/agent/workspace"}
	r := &rtRig{host: host, voice: &agenttest.RecordingVoiceLink{}, alerts: &agenttest.RecordingNotifier{},
		status: &agenttest.RecordingStatus{}, launcher: &agenttest.FakeLauncher{}, store: store, clock: clock}
	var session SessionHost = host
	if wrap != nil {
		session = wrap(host)
	}
	if launch == nil {
		launch = r.launcher
	}
	deps := Deps{
		Host: NewAgentHost(session, clock, recipe), Voice: r.voice, Notifier: r.alerts, Mascot: r.alerts,
		Status: r.status, Launcher: launch, TerminalDir: "/home/me/Projects", Store: store, Clock: clock, IDs: &agenttest.SeqIds{},
	}
	r.h = NewRuntime(deps, cfg)
	go r.h.Run(t.Context())
	return r
}

func rigWith(t *testing.T, store *agenttest.MemStore, warm bool) *rtRig {
	return build(t, store, config(warm), nil, nil)
}

func rigOn(t *testing.T, store *agenttest.MemStore, warm bool, wrap func(*agenttest.FakeSessionHost) SessionHost) *rtRig {
	return build(t, store, config(warm), wrap, nil)
}

func newRig(t *testing.T) *rtRig { return rigWith(t, &agenttest.MemStore{}, false) }

func settle() { time.Sleep(50 * time.Millisecond) }

func stopHook(r *rtRig, message string) {
	r.h.Deliver(HookEvent{Kind: HookStop, Agent: Coordinator, Message: message})
}

// invoke runs a voice tool and decodes its output.
func invoke(t *testing.T, tool voice.Tool, args string) any {
	t.Helper()
	out := tool.Invoke(context.Background(), json.RawMessage(args))
	if out.EndConversation {
		t.Fatalf("%s ended the conversation", tool.Name())
	}
	var v any
	if err := json.Unmarshal(out.Output, &v); err != nil {
		t.Fatalf("%s: %v", out.Output, err)
	}
	return v
}

// jsonOf decodes a JSON literal for comparisons.
func jsonOf(s string) any {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(err)
	}
	return v
}

// asJSON round-trips v through JSON (a tool result with Go types → generic JSON values).
func asJSON(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return jsonOf(string(b))
}

func wantJSON(t *testing.T, got any, want string) {
	t.Helper()
	if !reflect.DeepEqual(asJSON(got), jsonOf(want)) {
		t.Fatalf("got %v; want %s", asJSON(got), want)
	}
}

func delegate(t *testing.T, r *rtRig, task string) any {
	return invoke(t, Delegate{H: r.h}, `{"task":`+quote(task)+`}`)
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func hasCall(calls []agenttest.VoiceCall, c agenttest.VoiceCall) bool {
	return slices.Contains(calls, c)
}

func hasAlert(alerts []agenttest.Alert, a agenttest.Alert) bool { return slices.Contains(alerts, a) }

func TestDelegateStartsTheCoordinatorAndPromptsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		out := Delegate{H: r.h}.Invoke(context.Background(), json.RawMessage(`{"task":"summarise the open issues"}`))
		wantJSON(t, jsonOf(string(out.Output)), `{"job_id":"job_000000000001","status":"delegated"}`)
		if out.EndConversation {
			t.Fatal("end_conversation")
		}
		settle()
		if len(r.host.Starts()) != 1 {
			t.Fatal(r.host.Starts())
		}
		want := []agenttest.Prompt{{Agent: Coordinator, Text: "New request from the user: summarise the open issues\n[ref job_000000000001]"}}
		if !slices.Equal(r.host.Prompts(), want) {
			t.Fatal(r.host.Prompts())
		}
		if !slices.Equal(r.voice.Calls(), []agenttest.VoiceCall{agenttest.HoldOpenCall(true)}) {
			t.Fatal(r.voice.Calls())
		}
		if n := len(r.store.Lines(JobsFile)); n != 2 {
			t.Fatalf("created, then working: %d", n)
		}
	})
}

func TestADoneReplyWithTheConversationClosedChimesAndNotifies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		delegate(t, r, "count issues")
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="done">There are 12.</spoken>`)
		settle()
		want := []agenttest.Alert{agenttest.ChimeAlert(), agenttest.NotifyAlert("Bit", "There are 12."), agenttest.CueAlert(CueHappy)}
		if !slices.Equal(r.alerts.Alerts(), want) {
			t.Fatal(r.alerts.Alerts())
		}
		calls := r.voice.Calls()
		if calls[len(calls)-1] != agenttest.HoldOpenCall(false) {
			t.Fatal(calls)
		}
	})
}

func TestAReplyWhileOpenIsInjected(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.voice.SetOpen(true)
		r.h.Voice(VoiceSignal{Kind: voice.SigOpened})
		delegate(t, r, "count issues")
		settle()
		stopHook(r, `<spoken ref="job_000000000001">There are 12.</spoken>`)
		settle()
		if !hasCall(r.voice.Calls(), agenttest.InjectCall("Agent reply: There are 12.")) {
			t.Fatal(r.voice.Calls())
		}
	})
}

func TestAnInjectThatFindsTheConversationClosedFallsBackToANotification(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.h.Voice(VoiceSignal{Kind: voice.SigOpened}) // the controller thinks it is open, the link says closed
		delegate(t, r, "count issues")
		settle()
		stopHook(r, `<spoken ref="job_000000000001">There are 12.</spoken>`)
		settle()
		if !hasAlert(r.alerts.Alerts(), agenttest.NotifyAlert("Bit", "There are 12.")) {
			t.Fatal(r.alerts.Alerts())
		}
	})
}

func TestJobsStatusAnswersFromTheTable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		delegate(t, r, "a")
		settle()
		out := invoke(t, Jobs{H: r.h}, `{"action":"status"}`).(map[string]any)
		job := out["jobs"].([]any)[0].(map[string]any)
		if job["job_id"] != "job_000000000001" || job["status"] != "working" {
			t.Fatal(out)
		}
		if len(r.host.Prompts()) != 1 {
			t.Fatal("no agent turn for status")
		}
	})
}

func TestJobsCancelInterruptsTheCoordinator(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		delegate(t, r, "a")
		settle()
		wantJSON(t, invoke(t, Jobs{H: r.h}, `{"action":"cancel"}`), `{"job_id":"job_000000000001","status":"cancelled"}`)
		settle()
		keys := r.host.Keys()
		if len(keys) != 1 || keys[0].Agent != Coordinator || !slices.Equal(keys[0].Keys, []string{"Escape"}) {
			t.Fatal(keys)
		}
		wantJSON(t, invoke(t, Jobs{H: r.h}, `{"action":"cancel"}`), `{"error":"No job is open."}`)
		wantJSON(t, invoke(t, Jobs{H: r.h}, `{}`), `{"error":"action must be status or cancel"}`)
	})
}

func permissionEvent(command string) (HookEvent, chan Decision) {
	rx := make(chan Decision, 1)
	return HookEvent{Kind: HookPermission, Agent: Coordinator, ToolName: "Bash", ToolInput: map[string]any{"command": command}, Reply: rx}, rx
}

func TestAPermissionHookIsAnsweredByASpokenConfirm(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.voice.SetOpen(true)
		r.h.Voice(VoiceSignal{Kind: voice.SigOpened})
		ev, rx := permissionEvent("git push")
		r.h.Deliver(ev)
		settle()
		if !hasCall(r.voice.Calls(), agenttest.InjectCall("Agent question: Run git push in coordinator? Say confirm hawk.")) {
			t.Fatal(r.voice.Calls())
		}
		r.h.Voice(VoiceSignal{Kind: voice.SigUserSaid, Text: "confirm hawk"})
		settle()
		if d := <-rx; d != Allow() {
			t.Fatal(d)
		}
	})
}

func TestQuittingDeniesAWaitingPermissionHook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		ev, rx := permissionEvent("rm -rf build")
		r.h.Deliver(ev)
		settle()
		r.h.Shutdown()
		if d := <-rx; d != Deny("The app quit.") {
			t.Fatal(d)
		}
	})
}

func TestOpenTerminalEnsuresTheSessionStartsTheCoordinatorAndAttaches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		if err := r.h.OpenTerminal(context.Background()); err != nil {
			t.Fatal(err)
		}
		settle()
		if r.host.Ensures() != 2 {
			t.Fatalf("once to attach, once to start the coordinator: %d", r.host.Ensures())
		}
		if len(r.host.Starts()) != 1 {
			t.Fatal(r.host.Starts())
		}
		want := []Attach{{Argv: []string{"fake-herdr", "session", "attach", "test"}, Dir: "/home/me/Projects"}}
		if !reflect.DeepEqual(r.launcher.Attaches(), want) {
			t.Fatal(r.launcher.Attaches())
		}
	})
}

func TestOpenTerminalReportsHerdrMissing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.host.FailEnsure(HostError{Kind: HostNotFound})
		if err := r.h.OpenTerminal(context.Background()); err == nil || err.Error() != "herdr not found" {
			t.Fatal(err)
		}
		if len(r.launcher.Opened()) != 0 {
			t.Fatal(r.launcher.Opened())
		}
	})
}

func TestHerdrMissingTurnsDelegationOffAndDelegateSaysSo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.host.FailEnsure(HostError{Kind: HostNotFound})
		delegate(t, r, "a")
		settle()
		if !slices.Equal(r.status.Lines(), []string{"Agent: herdr not found"}) {
			t.Fatal(r.status.Lines())
		}
		wantJSON(t, delegate(t, r, "b"), `{"error":"Delegation is off (Agent: herdr not found)."}`)
	})
}

func TestWarmStartLaunchesTheCoordinatorAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rigWith(t, &agenttest.MemStore{}, true)
		settle()
		if len(r.host.Starts()) != 1 {
			t.Fatal(r.host.Starts())
		}
	})
}

func TestShutdownClosesTheCoordinatorPane(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		delegate(t, r, "a")
		settle()
		r.h.Shutdown()
		if !slices.Equal(r.host.Closed(), []string{Coordinator}) {
			t.Fatal(r.host.Closed())
		}
	})
}

func TestJobsLeftOpenByAPreviousRunFailOnRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &agenttest.MemStore{}
		{
			r := rigWith(t, store, false)
			delegate(t, r, "a")
			settle()
		}
		r := rigWith(t, store, false)
		settle()
		if r.h.JobStatus()[0].Status != JobFailed {
			t.Fatal(r.h.JobStatus())
		}
	})
}

func TestDelegateRequiresATaskAndThePersonaNamesEveryTool(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		wantJSON(t, invoke(t, Delegate{H: r.h}, `{"task":"  "}`), `{"error":"task is required"}`)
	})
	for _, name := range []string{"delegate", "jobs", "answer_job", "Agent reply:", "Agent question:", "confirm"} {
		if !strings.Contains(DelegationPersona, name) {
			t.Fatal(name)
		}
	}
}

func TestThePersonaSendsUncertainFactsToTheAgent(t *testing.T) {
	if !strings.Contains(DelegationPersona, "Check: ") || !strings.Contains(DelegationPersona, "Let me check that.") {
		t.Fatal(DelegationPersona)
	}
}

func TestThePersonaSendsLiveInformationToTheAgent(t *testing.T) {
	if !strings.Contains(DelegationPersona, "news, prices, scores") || !strings.Contains(DelegationPersona, "Never tell the user to check") {
		t.Fatal(DelegationPersona)
	}
	if !strings.Contains(Delegate{}.Description(), "live information (news, prices, scores)") {
		t.Fatal(Delegate{}.Description())
	}
}

func TestTheTimeWeatherAndMathGoToTheLocalToolsNotTheAgent(t *testing.T) {
	if !strings.Contains(DelegationPersona, "call get_time, get_weather or calculate") {
		t.Fatal(DelegationPersona)
	}
	if d := (Delegate{}).Description(); !strings.Contains(d, "Not for the time, the weather or arithmetic") {
		t.Fatal(d)
	}
}

func TestThePersonaOffloadsWorkToTheAgentByDefault(t *testing.T) {
	for _, phrase := range []string{
		"You are the voice; it does the work.",
		"If you are unsure whether something is work, delegate it.",
		"Keep for yourself only:",
		"The agent does not hear this conversation.",
		"One delegate call per independent task.",
		"If you think you can't do it yourself, that is the signal to delegate",
	} {
		if !strings.Contains(DelegationPersona, phrase) {
			t.Fatal(phrase)
		}
	}
	if d := (Delegate{}).Description(); !strings.Contains(d, "the task and context must stand on their own") {
		t.Fatal(d)
	}
}

func TestAnswerJobSendsTheUsersAnswerToTheWaitingJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		delegate(t, r, "pick a branch")
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="question">Which branch?</spoken>`)
		settle()
		r.host.SetStatus(Coordinator, PaneIdle)
		settle()
		wantJSON(t, invoke(t, AnswerJob{H: r.h}, `{"answer":"  main "}`), `{"job_id":"job_000000000001","status":"answered"}`)
		settle()
		prompts := r.host.Prompts()
		if !strings.Contains(prompts[len(prompts)-1].Text, "The user answered: main") {
			t.Fatal(prompts)
		}
		if r.h.JobStatus()[0].Status != JobWorking {
			t.Fatal(r.h.JobStatus())
		}
	})
}

func TestAnswerJobErrorsAreReturnedNotRaised(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		tool := AnswerJob{H: r.h}
		wantJSON(t, invoke(t, tool, `{}`), `{"error":"answer is required"}`)
		wantJSON(t, invoke(t, tool, `{"answer":"x"}`), `{"error":"No job is waiting for an answer."}`)
		wantJSON(t, invoke(t, tool, `{"answer":"x","job_id":"nope"}`), `{"error":"No open job nope."}`)
		if len(r.host.Prompts()) != 0 {
			t.Fatal(r.host.Prompts())
		}
		if tool.Name() != "answer_job" {
			t.Fatal(tool.Name())
		}
		params := jsonOf(string(tool.Parameters())).(map[string]any)
		if !reflect.DeepEqual(params["required"], jsonOf(`["answer"]`)) {
			t.Fatal(params)
		}
	})
}

func TestTheRuntimeTicksSoAnUnansweredPermissionExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		ev, rx := permissionEvent("git push")
		r.h.Deliver(ev)
		settle()
		// The controller reads the injected clock, so move it past the 120 s deadline and let a tick land.
		r.clock.Advance(121_000)
		time.Sleep(2 * time.Second)
		select {
		case d := <-rx:
			if d != Deny("No answer from the user.") {
				t.Fatal(d)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a tick expired the request")
		}
	})
}

func TestOpeningTheConversationReachesTheController(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Once Opened is forwarded to the controller a reply is injected, not notified.
		r := newRig(t)
		r.voice.SetOpen(true)
		r.h.Voice(VoiceSignal{Kind: voice.SigOpened})
		delegate(t, r, "a")
		settle()
		stopHook(r, `<spoken ref="job_000000000001">Done.</spoken>`)
		settle()
		if !hasCall(r.voice.Calls(), agenttest.InjectCall("Agent reply: Done.")) {
			t.Fatal(r.voice.Calls())
		}
		if slices.ContainsFunc(r.alerts.Alerts(), agenttest.Alert.IsNotify) {
			t.Fatal(r.alerts.Alerts())
		}
	})
}

// probe is a session host that can be slow to start an agent, never finish closing one, and
// logs the order of prompts and key presses. Hangs end with their context (Rust drops the
// future at its timeout).
type probe struct {
	*agenttest.FakeSessionHost
	startDelay  time.Duration
	hangClose   bool
	hangPrompt  bool
	mu          sync.Mutex
	failPrompts int // this many prompts fail (with Other) before prompts work again
	log         []string
}

func (p *probe) record(s string) {
	p.mu.Lock()
	p.log = append(p.log, s)
	p.mu.Unlock()
}

func (p *probe) Log() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.log)
}

func (p *probe) Name() string { return "probe" }

func (p *probe) StartAgent(ctx context.Context, spec AgentSpec) (AgentInfo, error) {
	select {
	case <-time.After(p.startDelay):
	case <-ctx.Done():
		return AgentInfo{}, ctx.Err()
	}
	return p.FakeSessionHost.StartAgent(ctx, spec)
}

func (p *probe) ReplaceAgent(ctx context.Context, spec AgentSpec) (AgentInfo, error) {
	return DefaultReplaceAgent(ctx, p, spec)
}

func (p *probe) Prompt(ctx context.Context, agent, text string) error {
	p.record("prompt")
	if p.hangPrompt {
		<-ctx.Done()
		return ctx.Err()
	}
	p.mu.Lock()
	if p.failPrompts > 0 {
		p.failPrompts--
		p.mu.Unlock()
		return HostError{Kind: HostOther, Detail: "prompt refused"}
	}
	p.mu.Unlock()
	return p.FakeSessionHost.Prompt(ctx, agent, text)
}

func (p *probe) SendKeys(ctx context.Context, agent string, keys []string) error {
	p.record("keys:" + strings.Join(keys, "+"))
	return p.FakeSessionHost.SendKeys(ctx, agent, keys)
}

func (p *probe) Close(ctx context.Context, agent string) error {
	p.record("close")
	if p.hangClose {
		<-ctx.Done()
		return ctx.Err()
	}
	return p.FakeSessionHost.Close(ctx, agent)
}

func (p *probe) CloseExact(ctx context.Context, agent, pane string) error {
	return DefaultCloseExact(ctx, p, agent, pane)
}

func probeRig(t *testing.T, startDelay time.Duration, hangClose, hangPrompt bool) (*rtRig, *probe) {
	var p *probe
	r := rigOn(t, &agenttest.MemStore{}, false, func(inner *agenttest.FakeSessionHost) SessionHost {
		p = &probe{FakeSessionHost: inner, startDelay: startDelay, hangClose: hangClose, hangPrompt: hangPrompt}
		return p
	})
	return r, p
}

func permission(r *rtRig) chan Decision {
	ev, rx := permissionEvent("git push")
	r.h.Deliver(ev)
	return rx
}

func TestShutdownDuringASlowStartLeavesNoRunningPane(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := probeRig(t, 2*time.Second, false, false)
		delegate(t, r, "a")
		time.Sleep(500 * time.Millisecond) // the start is in flight
		if len(r.host.Starts()) != 0 {
			t.Fatal(r.host.Starts())
		}
		r.h.Shutdown()
		time.Sleep(5 * time.Second)
		if len(r.host.Starts()) != 1 {
			t.Fatal("the in-flight start finished", r.host.Starts())
		}
		if !slices.Equal(r.host.Closed(), []string{Coordinator}) {
			t.Fatal("and was closed afterwards", r.host.Closed())
		}
		if _, ok := r.host.StatusOf(Coordinator); ok {
			t.Fatal("still running")
		}
		if len(r.host.Prompts()) != 0 {
			t.Fatal("nothing was prompted after quitting")
		}
	})
}

func TestNothingStartsAfterShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.h.Shutdown()
		r.h.Input(EnsureCoordinatorInput())
		if _, err := r.h.Delegate("a", ""); err == nil {
			t.Fatal("delegate after shutdown")
		}
		if _, err := r.h.Answer("", "x"); err == nil {
			t.Fatal("answer after shutdown")
		}
		rx := permission(r)
		settle()
		if d := <-rx; d != Deny("The app quit.") {
			t.Fatal(d)
		}
		if len(r.host.Starts()) != 0 {
			t.Fatal(r.host.Starts())
		}
	})
}

func TestAnInterruptReachesTheHostBeforeThePromptThatFollowsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, p := probeRig(t, 0, false, false)
		delegate(t, r, "a")
		settle()
		invoke(t, Jobs{H: r.h}, `{"action":"cancel"}`)
		delegate(t, r, "b")
		settle()
		if !slices.Equal(p.Log(), []string{"prompt", "keys:Escape", "prompt"}) {
			t.Fatal(p.Log())
		}
	})
}

func TestAHangingStopCannotHoldUpShutdownAndPermissionsAreDenied(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := probeRig(t, 0, true, false)
		delegate(t, r, "a")
		settle()
		rx := permission(r)
		settle()
		before := time.Now()
		r.h.Shutdown()
		if e := time.Since(before); e < ShutdownLimit || e >= ShutdownLimit+time.Second {
			t.Fatal(e)
		}
		if d := <-rx; d != Deny("The app quit.") {
			t.Fatal(d)
		}
	})
}

func TestAHungPromptDoesNotBlockTheShutdownStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := probeRig(t, 0, false, true)
		delegate(t, r, "a")
		settle() // the worker is now stuck inside `prompt`
		before := time.Now()
		r.h.Shutdown()
		if e := time.Since(before); e >= ShutdownLimit {
			t.Fatal("stop ran directly", e)
		}
		if !slices.Equal(r.host.Closed(), []string{Coordinator}) {
			t.Fatal(r.host.Closed())
		}
	})
}

func TestAHungHostCallTimesOutAndTheNextOneRuns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, p := probeRig(t, 0, false, true)
		delegate(t, r, "a")
		settle()
		invoke(t, Jobs{H: r.h}, `{"action":"cancel"}`)
		time.Sleep(OpLimit + time.Second)
		if !slices.Contains(p.Log(), "keys:Escape") {
			t.Fatal(p.Log())
		}
	})
}

func TestAPermissionRacingTheQuitIsDenied(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		// The hook is queued when the app quits; the runtime sees it after the shutdown.
		rx := permission(r)
		r.h.Shutdown()
		select {
		case d := <-rx:
			if d != Deny("The app quit.") {
				t.Fatal(d)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("decided")
		}
	})
}

// ---------- final review fixes ----------

func TestAStartThatOutlastsItsLimitFailsTheJobOutLoud(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, p := probeRig(t, time.Hour, false, false)
		delegate(t, r, "a")
		time.Sleep(StartOpLimit + 2*time.Second)
		if r.h.JobStatus()[0].Status != JobFailed {
			t.Fatal("the controller left Starting", r.h.JobStatus())
		}
		if !hasAlert(r.alerts.Alerts(), agenttest.NotifyAlert("Bit", "The agent stopped before finishing: a")) {
			t.Fatal(r.alerts.Alerts())
		}
		if !hasAlert(r.alerts.Alerts(), agenttest.CueAlert(CueError)) {
			t.Fatal(r.alerts.Alerts())
		}
		if !slices.Contains(p.Log(), "close") {
			t.Fatal("a half-started pane is closed", p.Log())
		}
		// Delegation stays on: the next job starts the coordinator again.
		if _, err := r.h.Delegate("b", ""); err != nil {
			t.Fatal(err)
		}
	})
}

func TestAStartSlowerThanTheOldOpLimitStillSucceeds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := probeRig(t, OpLimit+5*time.Second, false, false)
		delegate(t, r, "a")
		time.Sleep(OpLimit + 10*time.Second)
		if len(r.host.Prompts()) != 1 {
			t.Fatal(r.host.Prompts())
		}
		if r.h.JobStatus()[0].Status != JobWorking {
			t.Fatal(r.h.JobStatus())
		}
	})
}

func failFirstPrompts(t *testing.T, n int) (*rtRig, *probe) {
	var p *probe
	r := rigOn(t, &agenttest.MemStore{}, false, func(inner *agenttest.FakeSessionHost) SessionHost {
		p = &probe{FakeSessionHost: inner, failPrompts: n}
		return p
	})
	return r, p
}

func TestAFailedPromptIsSentAgainOnTheNextIdle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, p := failFirstPrompts(t, 1)
		delegate(t, r, "a")
		settle()
		if len(r.host.Prompts()) != 0 {
			t.Fatal("the first prompt failed")
		}
		r.host.SetStatus(Coordinator, PaneIdle)
		settle()
		if len(r.host.Prompts()) != 1 {
			t.Fatal(p.Log())
		}
		if !strings.Contains(r.host.Prompts()[0].Text, "[ref job_000000000001]") {
			t.Fatal(r.host.Prompts())
		}
	})
}

func TestAWarmStartWithASetupProblemStartsNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := RuntimeConfig{Settings: DefaultSettings(), Warm: true, SetupProblem: "Agent: hook port busy"}
		r := build(t, &agenttest.MemStore{}, cfg, nil, nil)
		settle()
		if len(r.host.Starts()) != 0 || r.host.Ensures() != 0 {
			t.Fatal(r.host.Starts(), r.host.Ensures())
		}
		if !slices.Equal(r.status.Lines(), []string{"Agent: hook port busy"}) {
			t.Fatal(r.status.Lines())
		}
		if _, err := r.h.Delegate("a", ""); err == nil || err.Error() != "Delegation is off (Agent: hook port busy)." {
			t.Fatal(err)
		}
	})
}

// blockingLauncher blocks in Open until released.
type blockingLauncher struct {
	entered chan struct{}
	release chan struct{}
}

func (*blockingLauncher) Name() string { return "blocking" }

func (l *blockingLauncher) Open(Attach) error {
	close(l.entered)
	<-l.release
	return nil
}

// Rust checks that the (blocking) launch runs off the async runtime's thread. Go has no such
// thread: the equivalent property is that a launcher blocked in Open (osascript, open) never
// stalls the runtime, which keeps carrying out effects meanwhile.
func TestOpenTerminalLaunchesOffTheAsyncThread(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := &blockingLauncher{entered: make(chan struct{}), release: make(chan struct{})}
		r := build(t, &agenttest.MemStore{}, config(false), nil, l)
		done := make(chan error, 1)
		go func() { done <- r.h.OpenTerminal(context.Background()) }()
		<-l.entered
		settle()
		if len(r.host.Starts()) != 1 {
			t.Fatal("the runtime started the coordinator while the launch was blocked", r.host.Starts())
		}
		close(l.release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

// ---------- fix round 2 ----------

func TestAFailedPromptToAnIdleCoordinatorIsRetriedWithoutAnyStatusEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, p := failFirstPrompts(t, 1)
		delegate(t, r, "a")
		settle()
		if len(r.host.Prompts()) != 0 {
			t.Fatal("the first prompt failed")
		}
		// No status event at all: only time passes.
		r.clock.Advance(PromptRetryMs)
		time.Sleep(2 * time.Second)
		if len(r.host.Prompts()) != 1 {
			t.Fatal(p.Log())
		}
	})
}

func TestATimedOutPromptWithNoSignOfLifeFailsTheJobOutLoud(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, _ := probeRig(t, 0, false, true)
		delegate(t, r, "a")
		time.Sleep(OpLimit + time.Second)
		if r.h.JobStatus()[0].Status != JobWorking {
			t.Fatal("not yet", r.h.JobStatus())
		}
		r.clock.Advance(PromptWatchdogMs)
		time.Sleep(2 * time.Second)
		if r.h.JobStatus()[0].Status != JobFailed {
			t.Fatal(r.h.JobStatus())
		}
		if !hasAlert(r.alerts.Alerts(), agenttest.NotifyAlert("Bit", "I couldn't reach the agent: a")) {
			t.Fatal(r.alerts.Alerts())
		}
	})
}

func TestAfterAStopTheNextPromptWaitsForTheSettle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		delegate(t, r, "a")
		settle()
		delegate(t, r, "b")
		settle()
		stopHook(r, `<spoken ref="job_000000000001">Done.</spoken>`)
		time.Sleep(time.Second)
		if len(r.host.Prompts()) != 1 {
			t.Fatal("not right at the Stop", r.host.Prompts())
		}
		r.clock.Advance(StopSettleMs)
		time.Sleep(2 * time.Second)
		if len(r.host.Prompts()) != 2 {
			t.Fatal(r.host.Prompts())
		}
	})
}
