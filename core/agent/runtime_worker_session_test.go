package agent_test

import (
	"slices"
	"strings"
	"testing"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// workerStarted reports whether the worker's own pane (never the coordinator's) was started.
func workerStarted(r *pixWorkerRig) bool {
	return slices.ContainsFunc(r.primary.Starts(), func(s AgentSpec) bool { return s.Name == "focus-upload" })
}

// Issue #126 end to end: start_worker itself notices a pix session already open on the repo
// and prefers it, instead of relying on the coordinator's prompt to remember to check.
//
// On real time, not a synctest bubble: a CompositeHost's merged event pump (composite.go's
// Subscribe) outlives a bubble, as pixRig (runtime_session_test.go) already notes.

type pixWorkerRig struct {
	h         *AgentHandle
	primary   *agenttest.FakeSessionHost
	worktrees *agenttest.FakeWorktrees
}

// pixWorkerRig builds a worker-enabled runtime whose session host also lists the given pix
// sessions (all on repo "focus" unless given otherwise).
func newPixWorkerRig(t *testing.T, sessions ...AgentInfo) *pixWorkerRig {
	t.Helper()
	primary := agenttest.NewFakeSessionHost()
	sec := fixedSecondary{FakeSessionHost: agenttest.NewFakeSessionHost(), sessions: sessions}
	host := NewCompositeHost(primary, sec)
	clock := agenttest.ClockAt(1_000_000)
	recipe := CoordinatorRecipe{Cwd: "/data/agent/workspace", Env: []EnvVar{{Key: "PIXIE_AGENT_TOKEN", Value: "tok-coordinator"}}}
	wt := agenttest.FakeWorktreesWithRepo("focus")
	deps := Deps{
		Host: NewAgentHost(host, clock, recipe), Voice: &agenttest.RecordingVoiceLink{}, Notifier: &agenttest.RecordingNotifier{},
		Pixie: &agenttest.RecordingNotifier{}, Status: &agenttest.RecordingStatus{}, Launcher: &agenttest.FakeLauncher{},
		Store: &agenttest.MemStore{}, Clock: clock, IDs: &agenttest.SeqIds{}, Kept: &agenttest.FakeKept{},
	}
	settings := DefaultSettings()
	settings.Workers = WorkerPolicy{Enabled: true, Max: 4, Kinds: []AgentKind{Claude, Codex}}
	cfg := RuntimeConfig{Settings: settings}
	wd := &WorkerDeps{Worktrees: wt, Tokens: &agenttest.FakeTokens{}, Recipes: &agenttest.FakeRecipes{}}
	r := &pixWorkerRig{primary: primary, worktrees: wt}
	r.h = NewRuntimeWithWorkers(deps, cfg, wd)
	go r.h.Run(t.Context())
	return r
}

// delegateAndStart runs a request through the coordinator (so a job is open), then calls
// start_worker on repo "focus" for it, with override as given.
func delegateAndStart(t *testing.T, r *pixWorkerRig, override bool) McpReply {
	t.Helper()
	invoke(t, Delegate{H: r.h}, `{"task":"fix the upload test in focus"}`)
	settle()
	reply := make(chan McpReply, 1)
	req := WorkerRequest{Kind: Claude, Repo: "focus", Task: "Fix the flaky upload test.", JobID: "job_000000000001", Name: "upload", OverrideExistingSession: override}
	r.h.Deliver(HookEvent{Kind: HookMcp, Agent: Coordinator, Call: McpCall{Kind: CallStartWorker, Worker: req}, McpReply: reply})
	return <-reply
}

func TestStartWorkerRefusesWhenAMatchingPixSessionExists(t *testing.T) {
	existing := AgentInfo{Name: "focus-claude", Origin: OriginUser, User: &UserSession{Repo: "focus", Description: "login bug", Where: "WebStorm"}}
	r := newPixWorkerRig(t, existing)

	got := delegateAndStart(t, r, false)

	if got.Err == nil {
		t.Fatal("start_worker must refuse when a pix session already matches the repo")
	}
	for _, want := range []string{"login bug", "message_session", "override_existing_session"} {
		if !contains(got.Err.Error(), want) {
			t.Fatalf("missing %q in %q", want, got.Err.Error())
		}
	}
	if workerStarted(r) {
		t.Fatal("no worker should have started")
	}
}

func TestStartWorkerOverrideProceedsDespiteAMatchingSession(t *testing.T) {
	existing := AgentInfo{Name: "focus-claude", Origin: OriginUser, User: &UserSession{Repo: "focus", Description: "login bug", Where: "WebStorm"}}
	r := newPixWorkerRig(t, existing)

	got := delegateAndStart(t, r, true)

	if got.Err != nil {
		t.Fatalf("override_existing_session must let the worker start: %v", got.Err)
	}
	if !workerStarted(r) {
		t.Fatal("the worker should have started")
	}
}

func TestStartWorkerIgnoresASessionOnADifferentRepo(t *testing.T) {
	elsewhere := AgentInfo{Name: "other-claude", Origin: OriginUser, User: &UserSession{Repo: "other", Description: "unrelated"}}
	r := newPixWorkerRig(t, elsewhere)

	got := delegateAndStart(t, r, false)

	if got.Err != nil {
		t.Fatalf("a session on a different repo must not block the worker: %v", got.Err)
	}
	if !workerStarted(r) {
		t.Fatal("the worker should have started")
	}
}

func TestStartWorkerRefusalListsSeveralMatchingSessions(t *testing.T) {
	a := AgentInfo{Name: "focus-claude", Origin: OriginUser, User: &UserSession{Repo: "focus", Description: "login bug"}}
	b := AgentInfo{Name: "focus-codex", Origin: OriginUser, User: &UserSession{Repo: "focus", Description: "upload fix"}}
	r := newPixWorkerRig(t, a, b)

	got := delegateAndStart(t, r, false)

	if got.Err == nil {
		t.Fatal("start_worker must refuse when several pix sessions match the repo")
	}
	for _, want := range []string{"login bug", "upload fix", "Ask the person which"} {
		if !contains(got.Err.Error(), want) {
			t.Fatalf("missing %q in %q", want, got.Err.Error())
		}
	}
	if workerStarted(r) {
		t.Fatal("no worker should have started")
	}
}

func contains(s, substr string) bool { return strings.Contains(s, substr) }
