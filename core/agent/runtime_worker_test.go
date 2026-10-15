package agent_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// Worker scenarios end to end on fakes (spec §5.5): the MCP calls as the hook server delivers
// them, the worker start, its ops, and quitting. Port of ai-mascot
// crates/agent-core/src/runtime_worker_tests.rs (38 tests), in testing/synctest bubbles.
//
// One difference from tokio's paused clock: tokio holds its clock while a blocking call
// (spawn_blocking) runs, while a synctest bubble lets fake time pass while a fake's gate (a
// channel) blocks. The held-gate tests use synctest.Wait (run until everything is blocked,
// without moving time) where Rust spins on real time.

const (
	wName = "focus-upload"
	wJob  = "job_000000000001"
)

type wRig struct {
	h         *AgentHandle
	host      *agenttest.FakeSessionHost
	worktrees *agenttest.FakeWorktrees
	tokens    *agenttest.FakeTokens
	recipes   *agenttest.FakeRecipes
	clock     *agenttest.ManualClock
	kept      *agenttest.FakeKept
	roster    *agenttest.RecordingRoster
}

func wRigWith(t *testing.T, workers bool) *wRig { return rigKeeping(t, workers, false) }

func rigKeeping(t *testing.T, workers, keep bool) *wRig {
	return rigFrom(t, workers, keep, &agenttest.MemStore{})
}

func rigFrom(t *testing.T, workers, keep bool, store *agenttest.MemStore) *wRig {
	return rigFull(t, workers, keep, store, agenttest.FakeWorktreesWithRepo("focus"))
}

func rigWithWorktrees(t *testing.T, store *agenttest.MemStore, wt *agenttest.FakeWorktrees) *wRig {
	return rigFull(t, true, false, store, wt)
}

func rigFull(t *testing.T, workers, keep bool, store *agenttest.MemStore, wt *agenttest.FakeWorktrees) *wRig {
	return rigBuild(t, workers, keep, store, wt, agenttest.NewFakeSessionHost().ManualTurns(), KeptAgents{})
}

// rigBuild: host may already run panes (agents kept from an earlier run); kept is what that
// run saved (its coordinator token, when set, is this run's).
func rigBuild(t *testing.T, workers, keep bool, store *agenttest.MemStore, wt *agenttest.FakeWorktrees, host *agenttest.FakeSessionHost, kept KeptAgents) *wRig {
	clock := agenttest.ClockAt(1_000_000)
	token := "tok-coordinator"
	if kept.CoordinatorToken != "" {
		token = kept.CoordinatorToken
	}
	recipe := CoordinatorRecipe{Cwd: "/data/agent/workspace", Env: []EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: token}}, AdoptEarlier: kept.CoordinatorToken != ""}
	notifier := &agenttest.RecordingNotifier{}
	deps := Deps{
		Host: NewAgentHost(host, clock, recipe), Voice: &agenttest.RecordingVoiceLink{}, Notifier: notifier,
		Pixie: &agenttest.RecordingNotifier{}, Status: &agenttest.RecordingStatus{}, Launcher: &agenttest.FakeLauncher{},
		Store: store, Clock: clock, IDs: &agenttest.SeqIds{}, Kept: &agenttest.FakeKept{},
		Roster: &agenttest.RecordingRoster{},
	}
	r := &wRig{host: host, worktrees: wt, tokens: &agenttest.FakeTokens{}, recipes: &agenttest.FakeRecipes{}, clock: clock, kept: deps.Kept.(*agenttest.FakeKept), roster: deps.Roster.(*agenttest.RecordingRoster)}
	settings := DefaultSettings()
	settings.Workers = WorkerPolicy{Enabled: true, Max: 4, Kinds: []AgentKind{Claude, Codex}}
	settings.KeepPanesOnQuit = keep
	cfg := RuntimeConfig{Settings: settings, Kept: kept}
	var wd *WorkerDeps
	if workers {
		wd = &WorkerDeps{Worktrees: wt, Tokens: r.tokens, Recipes: r.recipes}
	}
	r.h = NewRuntimeWithWorkers(deps, cfg, wd)
	go r.h.Run(t.Context())
	return r
}

// withJob is a delegated job whose prompt is with the (manual-turn) coordinator.
func withJob(t *testing.T, workers bool) *wRig {
	r := wRigWith(t, workers)
	invoke(t, Delegate{H: r.h}, `{"task":"fix the upload test in focus"}`)
	settle()
	return r
}

// send delivers an MCP call; the reply arrives on the returned channel.
func send(r *wRig, agent string, call McpCall) chan McpReply {
	reply := make(chan McpReply, 1)
	r.h.Deliver(HookEvent{Kind: HookMcp, Agent: agent, Call: call, McpReply: reply})
	return reply
}

func call(r *wRig, agent string, c McpCall) McpReply { return <-send(r, agent, c) }

func startCall(repo string) McpCall {
	return McpCall{Kind: CallStartWorker, Worker: WorkerRequest{Kind: Claude, Repo: repo, Task: "Fix the flaky upload test.", JobID: wJob, Name: "upload"}}
}

func wantErr(t *testing.T, got McpReply, want string) {
	t.Helper()
	if got.Err == nil || got.Err.Error() != want {
		t.Fatalf("got %+v; want error %q", got, want)
	}
}

func okValue(t *testing.T, got McpReply) map[string]any {
	t.Helper()
	if got.Err != nil {
		t.Fatalf("got error %v", got.Err)
	}
	return asJSON(got.Value).(map[string]any)
}

func workerName(t *testing.T, got McpReply) string {
	t.Helper()
	return okValue(t, got)["name"].(string)
}

func pressed(keys []agenttest.Keys, agent string, k ...string) bool {
	return slices.ContainsFunc(keys, func(x agenttest.Keys) bool { return x.Agent == agent && slices.Equal(x.Keys, k) })
}

// typedInto: any key press other than Escape reached agent.
func typedInto(keys []agenttest.Keys, agent string) bool {
	return slices.ContainsFunc(keys, func(x agenttest.Keys) bool { return x.Agent == agent && !slices.Equal(x.Keys, []string{"Escape"}) })
}

func promptedTo(prompts []agenttest.Prompt, agent string) bool {
	return slices.ContainsFunc(prompts, func(p agenttest.Prompt) bool { return p.Agent == agent })
}

func TestStartWorkerMakesAWorktreeStartsThePaneAndSendsTheBrief(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		wantJSON(t, okValue(t, call(r, Coordinator, startCall("focus"))), `{"name":"focus-upload","worktree":"/projects/focus/.worktrees/upload"}`)
		if !slices.Equal(r.worktrees.Ensured(), []agenttest.RepoSlug{{Repo: "/projects/focus", Slug: "upload"}}) {
			t.Fatal(r.worktrees.Ensured())
		}
		i := slices.IndexFunc(r.host.Starts(), func(s AgentSpec) bool { return s.Name == wName })
		if i < 0 {
			t.Fatal("the worker's pane")
		}
		spec := r.host.Starts()[i]
		if spec.Cwd != "/projects/focus/.worktrees/upload" {
			t.Fatal(spec.Cwd)
		}
		if spec.Tab.Label != wName+" · job_000000000001 · Fix the flaky upload test." || spec.Tab.Meta["branch"] != "pixie/upload" {
			t.Fatalf("%+v", spec.Tab)
		}
		if !slices.Equal(spec.Env, []EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: "tok-focus-upload"}}) {
			t.Fatal(spec.Env)
		}
		if r.recipes.Prepared()[0].Token != "tok-focus-upload" {
			t.Fatal(r.recipes.Prepared())
		}
		settle()
		var brief string
		for _, p := range r.host.Prompts() {
			if p.Agent == wName {
				brief = p.Text
			}
		}
		if !strings.HasSuffix(brief, "Your task:\nFix the flaky upload test.") {
			t.Fatal(brief)
		}
		job := r.h.JobStatus()[0]
		if job.Workers[0].Name != wName || job.Workers[0].Status != WorkerRunning {
			t.Fatal(job.Workers)
		}
	})
}

func TestOnlyTheCoordinatorStartsWorkersAndOnlyWhenTheyAreOn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		wantErr(t, call(r, "focus-other", startCall("focus")), "Only the coordinator can start workers.")
		off := withJob(t, false)
		wantErr(t, call(off, Coordinator, startCall("focus")), "Workers are turned off (agent.workers.enabled).")
		if len(off.host.Starts()) != 1 {
			t.Fatal("the coordinator only", off.host.Starts())
		}
	})
}

func TestAnUnknownRepoIsRefusedBeforeAnythingIsMade(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		wantErr(t, call(r, Coordinator, startCall("nope")), "No repository nope under the projects root.")
		if len(r.worktrees.Ensured()) != 0 || len(r.h.JobStatus()[0].Workers) != 0 {
			t.Fatal("nothing admitted")
		}
	})
}

func TestAFailedStartIsAnErrorAndForgetsTheWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.host.FailStart(HostError{Kind: HostTimeout, Detail: "claude did not start"})
		wantErr(t, call(r, Coordinator, startCall("focus")), "timed out: claude did not start")
		settle()
		if !slices.Equal(r.tokens.Revoked(), []string{wName}) || !slices.Equal(r.recipes.Discarded(), []string{wName}) {
			t.Fatal(r.tokens.Revoked(), r.recipes.Discarded())
		}
		if r.h.JobStatus()[0].Workers[0].Status != WorkerFailed {
			t.Fatal(r.h.JobStatus())
		}
	})
}

func TestAFailedRecipeIsAnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.recipes.SetFail("codex workers are not available yet")
		wantErr(t, call(r, Coordinator, startCall("focus")), "codex workers are not available yet")
		if len(r.host.Starts()) != 1 {
			t.Fatal("no worker pane", r.host.Starts())
		}
	})
}

func TestReportsAreAnsweredAtOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		okValue(t, call(r, Coordinator, startCall("focus")))
		reply := call(r, wName, McpCall{Kind: CallReportResult, Outcome: OutcomeDone, Summary: "Fixed", Evidence: "tests pass"})
		wantJSON(t, okValue(t, reply), `{"ok":true}`)
		listed := okValue(t, call(r, Coordinator, McpCall{Kind: CallListWorkers}))
		if listed["workers"].([]any)[0].(map[string]any)["name"] != wName {
			t.Fatal(listed)
		}
	})
}

func TestStopWorkerSendsEscapeThenClosesAfterFiveSeconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		okValue(t, call(r, Coordinator, startCall("focus")))
		wantJSON(t, okValue(t, call(r, Coordinator, McpCall{Kind: CallStopWorker, Name: wName})), `{"name":"focus-upload","status":"stopping"}`)
		settle()
		if !pressed(r.host.Keys(), wName, "Escape") || slices.Contains(r.host.Closed(), wName) {
			t.Fatal(r.host.Keys(), r.host.Closed())
		}
		time.Sleep(WorkerStopGrace)
		settle()
		if !slices.Contains(r.host.Closed(), wName) {
			t.Fatal(r.host.Closed())
		}
		if !slices.Equal(r.recipes.Discarded(), []string{wName}) {
			t.Fatal(r.recipes.Discarded())
		}
		if r.h.JobStatus()[0].Workers[0].Status != WorkerStopped {
			t.Fatal(r.h.JobStatus())
		}
	})
}

func auditedUpload() []agenttest.RepoSlug {
	return []agenttest.RepoSlug{{Repo: "/projects/focus", Slug: "upload"}}
}

func TestAForgottenWorkerHasItsGitLinksRechecked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		okValue(t, call(r, Coordinator, startCall("focus")))
		if len(r.worktrees.Audited()) != 0 {
			t.Fatal("not while it runs")
		}
		okValue(t, call(r, Coordinator, McpCall{Kind: CallStopWorker, Name: wName}))
		settle()
		if slices.Contains(r.host.Closed(), wName) || len(r.worktrees.Audited()) != 0 {
			t.Fatal("not before its pane closed")
		}
		time.Sleep(WorkerStopGrace + time.Second)
		if !slices.Contains(r.host.Closed(), wName) {
			t.Fatal(r.host.Closed())
		}
		if !slices.Equal(r.worktrees.Audited(), auditedUpload()) {
			t.Fatal(r.worktrees.Audited())
		}
	})
}

func TestAFailedStartHasItsGitLinksRechecked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.host.FailStart(HostError{Kind: HostTimeout, Detail: "claude did not start"})
		if call(r, Coordinator, startCall("focus")).Err == nil {
			t.Fatal("want an error")
		}
		settle()
		if !slices.Equal(r.worktrees.Audited(), auditedUpload()) {
			t.Fatal(r.worktrees.Audited())
		}
	})
}

func TestQuittingRechecksEveryKnownWorkersGitLinks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		okValue(t, call(r, Coordinator, startCall("focus")))
		began := time.Now()
		r.h.Shutdown()
		if time.Since(began) > ShutdownLimit {
			t.Fatal(time.Since(began))
		}
		if !slices.Equal(r.worktrees.Audited(), auditedUpload()) {
			t.Fatal(r.worktrees.Audited())
		}
	})
}

func TestPanesKeptRunningOnQuitAreNotRecheckedThen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rigKeeping(t, true, true)
		invoke(t, Delegate{H: r.h}, `{"task":"fix the upload test in focus"}`)
		settle()
		okValue(t, call(r, Coordinator, startCall("focus")))
		r.h.Shutdown()
		settle()
		if slices.Contains(r.host.Closed(), wName) {
			t.Fatal("the pane is kept")
		}
		if len(r.worktrees.Audited()) != 0 {
			t.Fatal("it could rewrite them again; its next reuse re-checks")
		}
	})
}

func TestATrustPromptFailsTheStartAndNothingIsTyped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		why := "Codex asked whether to trust this folder; I won't trust it for you."
		r.recipes.SetRefusal(&StartRefusal{Screens: [][]string{{"Do you trust the contents of this directory"}}, Reason: why})
		r.host.SetScreen(wName, "> Do you trust the contents of this directory?\n  1. Yes, continue\n  2. No, quit")
		wantErr(t, call(r, Coordinator, startCall("focus")), why)
		settle()
		if typedInto(r.host.Keys(), wName) {
			t.Fatal("no answer is typed", r.host.Keys())
		}
		if promptedTo(r.host.Prompts(), wName) {
			t.Fatal("no brief is typed into the prompt")
		}
		if r.h.JobStatus()[0].Workers[0].Status != WorkerFailed {
			t.Fatal(r.h.JobStatus())
		}
		if !slices.Contains(r.host.Closed(), wName) {
			t.Fatal("its pane is closed")
		}
	})
}

func TestNoTrustPromptMeansANormalStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.recipes.SetRefusal(&StartRefusal{Screens: [][]string{{"Do you trust"}}, Reason: "no"})
		okValue(t, call(r, Coordinator, startCall("focus")))
		if slices.ContainsFunc(r.host.Keys(), func(k agenttest.Keys) bool { return k.Agent == wName }) {
			t.Fatal(r.host.Keys())
		}
	})
}

// codexLike: refused on any trust marker, up only once the input box shows.
func codexLike(r *wRig) {
	r.recipes.SetRefusal(&StartRefusal{Screens: CodexTrustScreensOwned(), Reason: "trust it yourself"})
	r.recipes.SetReady(&StartReady{Marker: CodexIdleMarker, Reason: CodexNotReady("/projects/focus"), Settle: CodexReadySettle})
}

func TestTheStartWaitsForTheInputBoxBeforeTheBrief(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		codexLike(r)
		r.host.SetScreen(wName, "Booting MCP server: pixie")
		host := r.host
		bad := make(chan string, 1)
		go func() {
			time.Sleep(5 * time.Second)
			if promptedTo(host.Prompts(), wName) {
				bad <- "nothing is sent before the input box shows"
			}
			host.SetScreen(wName, "› Ask Codex to do anything\n\n  ? for shortcuts")
		}()
		began := time.Now()
		okValue(t, call(r, Coordinator, startCall("focus")))
		// Up once the input box has shown for CodexReadySettle.
		if e := time.Since(began); e < 5*time.Second+CodexReadySettle || e >= 6*time.Second+CodexReadySettle {
			t.Fatal(e)
		}
		select {
		case msg := <-bad:
			t.Fatal(msg)
		default:
		}
		settle()
		if !slices.ContainsFunc(r.host.Prompts(), func(p agenttest.Prompt) bool {
			return p.Agent == wName && strings.Contains(p.Text, "Fix the flaky upload test.")
		}) {
			t.Fatal("then the brief goes", r.host.Prompts())
		}
	})
}

func TestAStartWhoseInputBoxNeverShowsFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		codexLike(r)
		r.host.SetScreen(wName, "Starting MCP servers (0/1): pixie\n\n  \x1b[2m⠋ Booting\x07 the pixie MCP server, which takes a long while to come up, longer than it should\n\n")
		began := time.Now()
		why := "Codex didn't become ready (its screen ends with \"[2m⠋ Booting the pixie MCP server, which takes a long while to come up, longer t\"); open Codex in /projects/focus to see why."
		// The last line, control characters dropped, 80 chars.
		wantErr(t, call(r, Coordinator, startCall("focus")), why)
		// Inside the start budget: until ReadyWindow after the start began (the fake starts at once).
		if e := time.Since(began); e < ReadyWindow || e >= ReadyWindow+time.Second {
			t.Fatal(e)
		}
		settle()
		if promptedTo(r.host.Prompts(), wName) || typedInto(r.host.Keys(), wName) {
			t.Fatal(r.host.Prompts(), r.host.Keys())
		}
	})
}

func TestCodexSNewTrustScreenOnAnIdlePaneFailsTheStartAndNothingIsTyped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		codexLike(r)
		// Herdr reports the pane idle while the trust screen shows; it shows a moment after the start.
		r.host.SetScreen(wName, "")
		host := r.host
		go func() {
			time.Sleep(2 * time.Second)
			host.SetScreen(wName, CodexTrustScreenForTest)
		}()
		wantErr(t, call(r, Coordinator, startCall("focus")), "trust it yourself")
		settle()
		if typedInto(r.host.Keys(), wName) {
			t.Fatal("no answer is typed", r.host.Keys())
		}
		if promptedTo(r.host.Prompts(), wName) {
			t.Fatal("no brief is typed into it")
		}
		if !slices.Contains(r.host.Closed(), wName) {
			t.Fatal("its pane is closed")
		}
	})
}

func TestCodexSTrustScreenDrawnOverItsInputBoxFailsTheStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		codexLike(r)
		// codex 0.162 draws its input box first, then its trust screen over it.
		r.host.SetScreen(wName, "› Ask Codex to do anything\n\n  ? for shortcuts")
		host := r.host
		go func() {
			time.Sleep(time.Second)
			host.SetScreen(wName, CodexTrustScreenForTest)
		}()
		wantErr(t, call(r, Coordinator, startCall("focus")), "trust it yourself")
		settle()
		if typedInto(r.host.Keys(), wName) || promptedTo(r.host.Prompts(), wName) {
			t.Fatal("nothing is typed", r.host.Keys(), r.host.Prompts())
		}
		if !slices.Contains(r.host.Closed(), wName) {
			t.Fatal("its pane is closed")
		}
	})
}

func TestTheInputBoxMustShowOnEveryReadForTheSettle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		codexLike(r)
		box := "› Ask Codex to do anything\n\n  ? for shortcuts"
		r.host.SetScreen(wName, box)
		host := r.host
		go func() {
			time.Sleep(2 * time.Second)
			host.SetScreen(wName, "Booting MCP server: pixie")
			time.Sleep(time.Second)
			host.SetScreen(wName, box)
		}()
		began := time.Now()
		okValue(t, call(r, Coordinator, startCall("focus")))
		// The settle starts again when the box shows again (at 3 s).
		if e := time.Since(began); e < 3*time.Second+CodexReadySettle || e >= 4*time.Second+CodexReadySettle {
			t.Fatal(e)
		}
	})
}

func TestASettleBegunNearTheDeadlineRunsToItsEnd(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		codexLike(r)
		r.host.SetScreen(wName, "Booting MCP server: pixie")
		host := r.host
		go func() {
			time.Sleep(ReadyWindow - time.Second)
			host.SetScreen(wName, "› Ask Codex to do anything")
		}()
		okValue(t, call(r, Coordinator, startCall("focus")))
	})
}

func TestQuittingClosesWorkerPanesAndForgetsThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		okValue(t, call(r, Coordinator, startCall("focus")))
		r.h.Shutdown()
		if !slices.Contains(r.host.Closed(), wName) || !slices.Contains(r.recipes.Discarded(), wName) || !slices.Contains(r.tokens.Revoked(), wName) {
			t.Fatal(r.host.Closed(), r.recipes.Discarded(), r.tokens.Revoked())
		}
		wantErr(t, call(r, Coordinator, McpCall{Kind: CallListWorkers}), "The app is quitting.")
	})
}

func TestCancellingTheJobStopsItsWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		okValue(t, call(r, Coordinator, startCall("focus")))
		if _, err := r.h.Cancel(wJob); err != nil {
			t.Fatal(err)
		}
		time.Sleep(WorkerStopGrace + time.Second)
		if !slices.Contains(r.host.Closed(), wName) {
			t.Fatal(r.host.Closed())
		}
	})
}

func TestAHungStartIsAnsweredAtTheLimitAndFreesItsSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.host.HangStart(true)
		began := time.Now()
		wantErr(t, call(r, Coordinator, startCall("focus")), "claude did not start in time")
		if e := time.Since(began); e < WorkerStartLimit || e >= WorkerStartLimit+20*time.Second {
			t.Fatal(e)
		}
		settle()
		if r.h.JobStatus()[0].Workers[0].Status != WorkerFailed {
			t.Fatal(r.h.JobStatus())
		}
		if !slices.Equal(r.tokens.Revoked(), []string{wName}) || !slices.Equal(r.recipes.Discarded(), []string{wName}) {
			t.Fatal(r.tokens.Revoked(), r.recipes.Discarded())
		}
		// The slot is free again: the same worker can be started.
		r.host.HangStart(false)
		if n := workerName(t, call(r, Coordinator, startCall("focus"))); n != wName {
			t.Fatal(n)
		}
	})
}

func TestACrashingStartIsAnsweredAndForgetsTheWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.host.PanicStart(true)
		if reply := call(r, Coordinator, startCall("focus")); reply.Err == nil {
			t.Fatal(reply)
		}
		settle()
		if r.h.JobStatus()[0].Workers[0].Status != WorkerFailed {
			t.Fatal(r.h.JobStatus())
		}
		if !slices.Equal(r.tokens.Revoked(), []string{wName}) {
			t.Fatal(r.tokens.Revoked())
		}
		r.host.PanicStart(false)
		if reply := call(r, Coordinator, startCall("focus")); reply.Err != nil {
			t.Fatal("the slot was freed", reply.Err)
		}
	})
}

func TestAClientThatHangsUpLeavesTheWorkerStartedOnceAndTracked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		send(r, Coordinator, startCall("focus")) // nobody waits for the reply
		settle()
		n := 0
		for _, s := range r.host.Starts() {
			if s.Name == wName {
				n++
			}
		}
		if n != 1 {
			t.Fatal(r.host.Starts())
		}
		job := r.h.JobStatus()[0]
		if len(job.Workers) != 1 || job.Workers[0].Status != WorkerRunning {
			t.Fatal(job.Workers)
		}
		if !promptedTo(r.host.Prompts(), wName) {
			t.Fatal("its brief went out")
		}
		// The client's retry does not find a duplicate pane under the same name.
		if again := workerName(t, call(r, Coordinator, startCall("focus"))); again == wName {
			t.Fatal(again)
		}
	})
}

func TestQuittingStopsAllWorkerPanesInParallelWithinTheLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		var names []string
		for _, n := range []string{"one", "two", "three"} {
			req := WorkerRequest{Kind: Claude, Repo: "focus", Task: "Do it.", JobID: wJob, Name: n}
			names = append(names, workerName(t, call(r, Coordinator, McpCall{Kind: CallStartWorker, Worker: req})))
		}
		began := time.Now()
		r.h.Shutdown()
		if e := time.Since(began); e >= 2*StopGrace {
			t.Fatal(e)
		}
		for _, n := range names {
			if !pressed(r.host.Keys(), n, "Escape") || !slices.Contains(r.host.Closed(), n) || !slices.Contains(r.tokens.Revoked(), n) {
				t.Fatal(n, r.host.Keys(), r.host.Closed(), r.tokens.Revoked())
			}
		}
	})
}

func TestQuittingDuringAStartAnswersItAndLeavesNothingBehind(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.host.HangStart(true)
		wait := send(r, Coordinator, startCall("focus"))
		settle()
		r.h.Shutdown()
		if !slices.Contains(r.tokens.Revoked(), wName) || !slices.Contains(r.recipes.Discarded(), wName) {
			t.Fatal(r.tokens.Revoked(), r.recipes.Discarded())
		}
		if reply := <-wait; reply.Err == nil {
			t.Fatal("the pending call is answered", reply)
		}
	})
}

func TestAQuitDuringTheWorktreeStepLaunchesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		attempts := r.host.StartAttempts()
		r.worktrees.HoldEnsure()
		wait := send(r, Coordinator, startCall("focus"))
		synctest.Wait()
		if !r.worktrees.EnsureIsHeld() {
			t.Fatal("the start never reached ensure")
		}
		// Released a little later, while the shutdown runs.
		gate := r.worktrees
		go func() {
			time.Sleep(200 * time.Millisecond)
			gate.ReleaseEnsure()
		}()
		r.h.Shutdown()
		settle()
		wantErr(t, <-wait, "The app is quitting.")
		if r.host.StartAttempts() != attempts {
			t.Fatal("no agent was started")
		}
		if len(r.tokens.Issued()) != 0 {
			t.Fatal("no token was issued")
		}
		if len(r.recipes.Prepared()) != 0 {
			t.Fatal("no launch files were written")
		}
		if !slices.Contains(r.tokens.Revoked(), wName) {
			t.Fatal(r.tokens.Revoked())
		}
	})
}

func TestAPaneThatDiedDuringTheStartIsNotReportedUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.host.DieOnStart(true)
		wantErr(t, call(r, Coordinator, startCall("focus")), "claude exited at start")
		settle()
		if r.h.JobStatus()[0].Workers[0].Status != WorkerFailed {
			t.Fatal(r.h.JobStatus())
		}
		if promptedTo(r.host.Prompts(), wName) {
			t.Fatal("no brief was sent")
		}
	})
}

func TestABriefLeftInTheInputIsSubmittedWithEnter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.host.SwallowPrompts(true)
		okValue(t, call(r, Coordinator, startCall("focus")))
		settle()
		r.host.SetScreen(wName, "› [Pasted Content 900 chars]")
		r.clock.Advance(WorkerDeliveryMs)
		time.Sleep(2 * time.Second)
		settle()
		if !pressed(r.host.Keys(), wName, "Enter") {
			t.Fatal(r.host.Keys())
		}
	})
}

func TestAListFailureBeforeAdmissionRefusesTheStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.host.FailList(true)
		wantErr(t, call(r, Coordinator, startCall("focus")), "Couldn't check the running workers; try again.")
		if len(r.h.JobStatus()[0].Workers) != 0 || len(r.worktrees.Ensured()) != 0 {
			t.Fatal("nothing admitted")
		}
	})
}

func TestAFailedStartsNameStaysTakenAndItsLinksWaitUntilItsPaneIsClosed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.host.HangStart(true)
		r.host.HangClose(true)
		wantErr(t, call(r, Coordinator, startCall("focus")), "claude did not start in time")
		settle()
		if len(r.worktrees.Audited()) != 0 {
			t.Fatal("not while its pane may still be closing")
		}
		r.host.HangStart(false)
		r.host.HangClose(false)
		if n := workerName(t, call(r, Coordinator, startCall("focus"))); n != "focus-upload-2" {
			t.Fatal("the name is still being closed", n)
		}
		time.Sleep(OpLimit + time.Second)
		if !slices.Equal(r.worktrees.Audited(), auditedUpload()) {
			t.Fatal("after the close", r.worktrees.Audited())
		}
		req := WorkerRequest{Kind: Claude, Repo: "focus", Task: "Fix it.", JobID: wJob, Name: "upload"}
		if n := workerName(t, call(r, Coordinator, McpCall{Kind: CallStartWorker, Worker: req})); n != wName {
			t.Fatal("free again once closed", n)
		}
	})
}

func TestAStartThatTimesOutInGitKeepsItsNameAndLinksUntilGitReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.worktrees.HoldEnsure()
		defer r.worktrees.ReleaseEnsure() // however the test ends
		wait := send(r, Coordinator, startCall("focus"))
		synctest.Wait()
		if !r.worktrees.EnsureIsHeld() {
			t.Fatal("the start never reached ensure")
		}
		time.Sleep(WorkerStartLimit + time.Second)
		wantErr(t, <-wait, "claude did not start in time")
		time.Sleep(OpLimit + time.Second)
		if r.h.JobStatus()[0].Workers[0].Status != WorkerFailed {
			t.Fatal(r.h.JobStatus())
		}
		if len(r.worktrees.Audited()) != 0 {
			t.Fatal("not while git may still be making it")
		}
		// Its name is still taken: a new start of the same worker gets another one.
		second := send(r, Coordinator, startCall("focus"))
		synctest.Wait()
		if ws := r.h.JobStatus()[0].Workers; len(ws) != 2 || ws[1].Name != "focus-upload-2" {
			t.Fatal("the second start was never admitted", ws)
		}
		r.worktrees.ReleaseEnsure()
		synctest.Wait()
		if !slices.Equal(r.worktrees.Audited(), auditedUpload()) {
			t.Fatal("its links were re-checked", r.worktrees.Audited())
		}
		if n := workerName(t, <-second); n != "focus-upload-2" {
			t.Fatal(n)
		}
	})
}

func TestClosingAStartedPaneByItsIdLeavesANewerAgentOfTheSameNameAlone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := agenttest.NewFakeSessionHost().ManualTurns()
		spec := AgentSpec{Name: wName, Kind: Claude, Cwd: "/w"}
		old, _ := host.StartAgent(bg, spec)
		host.Close(bg, wName)
		fresh, _ := host.StartAgent(bg, spec)
		if old.Pane == fresh.Pane {
			t.Fatal(old.Pane)
		}
		CloseStartedForTest(host, wName, old.Pane)
		if st, ok := host.StatusOf(wName); !ok || st != PaneIdle {
			t.Fatal("the newer agent still runs")
		}
		CloseStartedForTest(host, wName, fresh.Pane)
		if _, ok := host.StatusOf(wName); ok {
			t.Fatal("closed")
		}
	})
}

func TestAWorkerStartFitsInOneMcpCall(t *testing.T) {
	if !(RepoLimit+ListLimit+WorkerStartLimit < McpCallLimit) {
		t.Fatal("start_worker must answer within McpCallLimit")
	}
}

func TestAWorktreesFolderEntryThatIsNotAWorkersIsNeverGivenToOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		r.worktrees.AddForeign("upload")
		if n := workerName(t, call(r, Coordinator, startCall("focus"))); n != "focus-upload-2" {
			t.Fatal(n)
		}
		if !slices.Equal(r.worktrees.Ensured(), []agenttest.RepoSlug{{Repo: "/projects/focus", Slug: "upload-2"}}) {
			t.Fatal(r.worktrees.Ensured())
		}
	})
}

func TestLiveWorktreesAreRememberedUntilTheirWorkerIsForgotten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &agenttest.MemStore{}
		r := rigFrom(t, true, false, store)
		invoke(t, Delegate{H: r.h}, `{"task":"fix the upload test in focus"}`)
		settle()
		okValue(t, call(r, Coordinator, startCall("focus")))
		wantJSON(t, jsonOf(store.Lines(LiveWorktreesFile)[0]), `{"repo":"/projects/focus","slug":"upload"}`)
		okValue(t, call(r, Coordinator, McpCall{Kind: CallStopWorker, Name: wName}))
		time.Sleep(WorkerStopGrace + time.Second)
		if len(store.Lines(LiveWorktreesFile)) != 0 {
			t.Fatal(store.Lines(LiveWorktreesFile))
		}
	})
}

func liveLine(repo, slug string) string {
	b, _ := json.Marshal(map[string]string{"repo": repo, "slug": slug})
	return string(b)
}

func TestWorktreesLeftByACrashAreRecheckedAtStartAndForgotten(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &agenttest.MemStore{}
		store.Rewrite(LiveWorktreesFile, []string{
			liveLine("/projects/focus", "upload"),
			"not json",
			// Not a repository under the projects root (any more): never audited.
			liveLine("/etc", "upload"),
		})
		r := rigFrom(t, true, false, store)
		settle()
		if !slices.Equal(r.worktrees.Audited(), auditedUpload()) {
			t.Fatal(r.worktrees.Audited())
		}
		if len(store.Lines(LiveWorktreesFile)) != 0 {
			t.Fatal(store.Lines(LiveWorktreesFile))
		}
	})
}

func TestNoWorkerIsAdmittedBeforeTheCrashRecheckIsDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &agenttest.MemStore{}
		store.Rewrite(LiveWorktreesFile, []string{liveLine("/projects/focus", "upload")})
		wt := agenttest.FakeWorktreesWithRepo("focus")
		wt.HoldAudit()
		defer wt.ReleaseAudit()
		r := rigWithWorktrees(t, store, wt)
		invoke(t, Delegate{H: r.h}, `{"task":"fix the upload test in focus"}`)
		wait := send(r, Coordinator, startCall("focus"))
		synctest.Wait() // everything runs until blocked; no time passes
		if len(r.h.JobStatus()[0].Workers) != 0 || len(r.worktrees.Ensured()) != 0 {
			t.Fatal("not admitted while the recheck runs")
		}
		r.worktrees.ReleaseAudit()
		if n := workerName(t, <-wait); n != wName {
			t.Fatal(n)
		}
		if r.worktrees.Audited()[0] != auditedUpload()[0] {
			t.Fatal("the recheck came first", r.worktrees.Audited())
		}
	})
}

func TestAgentsThisRunDidNotStartAreNamed(t *testing.T) {
	listed := []string{"coordinator", "focus-old", "focus-upload", "ai-x"}
	if got := UnknownAgentsForTest(listed, []string{"focus-upload"}); !slices.Equal(got, []string{"ai-x", "focus-old"}) {
		t.Fatal(got)
	}
	if got := UnknownAgentsForTest([]string{"coordinator"}, nil); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestQuittingTouchesTheLiveWorktreesFileOnlyWhenWorkersChangedIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, workers := range []bool{false, true} {
			empty := &agenttest.MemStore{}
			rigFrom(t, workers, false, empty).h.Shutdown()
			if empty.Has(LiveWorktreesFile) {
				t.Fatalf("workers %v: no file is made", workers)
			}
		}
		// Workers off: a crash list from an earlier run stays for the next run with workers.
		kept := &agenttest.MemStore{}
		crash := []string{liveLine("/projects/focus", "upload")}
		kept.Rewrite(LiveWorktreesFile, crash)
		rigFrom(t, false, false, kept).h.Shutdown()
		if !slices.Equal(kept.Lines(LiveWorktreesFile), crash) {
			t.Fatal(kept.Lines(LiveWorktreesFile))
		}
	})
}

func TestFocusJobFocusesTheJobsLiveWorker(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		if name := workerName(t, call(r, Coordinator, startCall("focus"))); name != wName {
			t.Fatal(name)
		}
		settle()
		if err := r.h.FocusJob(t.Context(), JobIDFromBits(1), ""); err != nil {
			t.Fatal(err)
		}
		if got := r.host.Focused(); len(got) == 0 || got[len(got)-1] != wName {
			t.Fatal(got)
		}
	})
}
