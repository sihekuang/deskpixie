package agent_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// Port of the tests in ai-mascot crates/agent-core/src/host.rs (13). Rust's tokio paused time
// is testing/synctest here.

var bg = context.Background()

func hostRig() (*AgentHost, *agenttest.FakeSessionHost, *agenttest.ManualClock) {
	fake := agenttest.NewFakeSessionHost()
	clock := agenttest.ClockAt(1_000_000)
	recipe := CoordinatorRecipe{
		Cwd:  "/data/agent/workspace",
		Args: []string{"--permission-mode", "auto"},
		Env:  []EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: "t"}},
	}
	return NewAgentHost(fake, clock, recipe), fake, clock
}

func started(resumed bool) Ensured { return Ensured{Started: true, Status: PaneIdle, Resumed: resumed} }

func wantEnsured(t *testing.T, h *AgentHost, want Ensured) {
	t.Helper()
	got, err := h.EnsureCoordinator(bg)
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func wantHostErr(t *testing.T, h *AgentHost, want HostError) {
	t.Helper()
	_, err := h.EnsureCoordinator(bg)
	var he HostError
	if !errors.As(err, &he) || he != want {
		t.Fatalf("got %v; want %v", err, want)
	}
}

func lastArg(spec AgentSpec) string {
	if len(spec.Args) == 0 {
		return ""
	}
	return spec.Args[len(spec.Args)-1]
}

func TestFirstStartLaunchesTheCoordinatorFresh(t *testing.T) {
	h, fake, _ := hostRig()
	wantEnsured(t, h, started(false))
	spec := fake.Starts()[0]
	if spec.Name != "coordinator" || spec.Kind != Claude || spec.Cwd != "/data/agent/workspace" {
		t.Fatalf("%+v", spec)
	}
	if !slices.Equal(spec.Args, []string{"--permission-mode", "auto"}) {
		t.Fatal(spec.Args)
	}
	if !slices.Equal(spec.Env, []EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: "t"}}) {
		t.Fatal(spec.Env)
	}
	if fake.Ensures() != 1 {
		t.Fatal(fake.Ensures())
	}
}

func TestARunningCoordinatorIsLeftAlone(t *testing.T) {
	h, fake, _ := hostRig()
	h.EnsureCoordinator(bg)
	fake.SetStatus(Coordinator, PaneWorking)
	wantEnsured(t, h, Ensured{Status: PaneWorking})
	if len(fake.Starts()) != 1 {
		t.Fatal(fake.Starts())
	}
}

func TestAMissingPaneIsReCreatedWithContinue(t *testing.T) {
	h, fake, _ := hostRig()
	h.EnsureCoordinator(bg)
	fake.Kill(Coordinator)
	wantEnsured(t, h, started(true))
	if lastArg(fake.Starts()[1]) != "--continue" {
		t.Fatal(fake.Starts()[1].Args)
	}
}

func TestThreeReCreationsInFiveMinutesThenItGivesUpUntilReset(t *testing.T) {
	h, fake, clock := hostRig()
	h.EnsureCoordinator(bg)
	for range 3 {
		fake.Kill(Coordinator)
		clock.Advance(60_000)
		if e, err := h.EnsureCoordinator(bg); err != nil || !e.Started || !e.Resumed {
			t.Fatal(e, err)
		}
	}
	fake.Kill(Coordinator)
	clock.Advance(60_000)
	wantHostErr(t, h, HostError{Kind: HostGaveUp})
	wantHostErr(t, h, HostError{Kind: HostGaveUp}) // stays off
	h.Reset()
	if _, err := h.EnsureCoordinator(bg); err != nil {
		t.Fatal(err)
	}
}

func TestCrashesSpreadOverMoreThanFiveMinutesAreFine(t *testing.T) {
	h, fake, clock := hostRig()
	h.EnsureCoordinator(bg)
	for range 6 {
		fake.Kill(Coordinator)
		clock.Advance(2 * 60_000)
		if _, err := h.EnsureCoordinator(bg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStartFailuresAreReported(t *testing.T) {
	h, fake, _ := hostRig()
	fake.FailStart(HostError{Kind: HostTimeout, Detail: "claude did not start"})
	wantHostErr(t, h, HostError{Kind: HostTimeout, Detail: "claude did not start"})
	fake.FailStart(nil)
	wantEnsured(t, h, started(false))
	if slices.Contains(fake.Starts()[0].Args, "--continue") {
		t.Fatal("a failed first start leaves no history")
	}
	fake.FailStart(HostError{Kind: HostOther, Detail: "x"})
	fake.FailEnsure(HostError{Kind: HostNotFound})
	wantHostErr(t, h, HostError{Kind: HostNotFound})
}

func recreateAt(h *AgentHost, fake *agenttest.FakeSessionHost, clock *agenttest.ManualClock, advance Millis) (Ensured, error) {
	fake.Kill(Coordinator)
	clock.Advance(advance)
	return h.EnsureCoordinator(bg)
}

func TestACrashExactlyFiveMinutesAfterTheOldestReCreationIsAllowed(t *testing.T) {
	h, fake, clock := hostRig()
	h.EnsureCoordinator(bg)
	recreateAt(h, fake, clock, 1) // t = T
	recreateAt(h, fake, clock, 1_000)
	recreateAt(h, fake, clock, 1_000)
	if _, err := recreateAt(h, fake, clock, RecreationWindowMs-2_000); err != nil {
		t.Fatal(err)
	}
}

func TestACrashOneMsInsideTheWindowGivesUpAndRecordsNothing(t *testing.T) {
	h, fake, clock := hostRig()
	h.EnsureCoordinator(bg)
	recreateAt(h, fake, clock, 1)
	recreateAt(h, fake, clock, 1_000)
	recreateAt(h, fake, clock, 1_000)
	if _, err := recreateAt(h, fake, clock, RecreationWindowMs-2_001); !IsHostError(err, HostGaveUp) {
		t.Fatal(err)
	}
	if n := h.RecreationsForTest(); n != 3 {
		t.Fatalf("a refused attempt records no timestamp: %d", n)
	}
}

func TestConcurrentEnsuresStartOneCoordinator(t *testing.T) {
	h, fake, _ := hostRig()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = h.EnsureCoordinator(bg)
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatal(errs)
	}
	if len(fake.Starts()) != 1 {
		t.Fatal(fake.Starts())
	}
}

// A coordinator left by an earlier run carries that run's token: the first ensure of this
// process replaces it (closed, then started with --continue: same conversation, new token).
func TestACoordinatorLeftByAnEarlierRunIsReplacedWithContinue(t *testing.T) {
	h, fake, _ := hostRig()
	fake.StartAgent(bg, AgentSpec{Name: Coordinator, Kind: Claude, Cwd: "/old", Env: []EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: "old"}}})
	wantEnsured(t, h, started(true))
	if !slices.Equal(fake.Closed(), []string{"coordinator"}) {
		t.Fatal(fake.Closed())
	}
	spec := fake.Starts()[len(fake.Starts())-1]
	if lastArg(spec) != "--continue" {
		t.Fatal(spec.Args)
	}
	if !slices.Equal(spec.Env, []EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: "t"}}) {
		t.Fatal("this run's token", spec.Env)
	}
	if h.RecreationsForTest() != 0 {
		t.Fatal("not a crash")
	}
	// Later ensures in the same process leave the (now current) coordinator alone.
	wantEnsured(t, h, Ensured{Status: PaneIdle})
	if len(fake.Closed()) != 1 || len(fake.Starts()) != 2 {
		t.Fatal(fake.Closed(), fake.Starts())
	}
}

// A coordinator kept over a quit whose token this run restored (AdoptEarlier) is adopted as it
// is: no replacement, no --continue, and later ensures leave it alone.
func TestAKeptCoordinatorIsAdoptedWhenItsTokenWasRestored(t *testing.T) {
	fake := agenttest.NewFakeSessionHost()
	recipe := CoordinatorRecipe{Cwd: "/data/agent/workspace", Env: []EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: "kept"}}, AdoptEarlier: true}
	h := NewAgentHost(fake, agenttest.ClockAt(1_000_000), recipe)
	fake.StartAgent(bg, AgentSpec{Name: Coordinator, Kind: Claude, Cwd: "/data/agent/workspace", Env: recipe.Env})
	wantEnsured(t, h, Ensured{Status: PaneIdle})
	wantEnsured(t, h, Ensured{Status: PaneIdle})
	if len(fake.Closed()) != 0 || len(fake.Starts()) != 1 {
		t.Fatal(fake.Closed(), fake.Starts())
	}
	if h.Token() != "kept" {
		t.Fatal(h.Token())
	}
}

// With AdoptEarlier but no coordinator running (it exited while the app was down), the start
// is today's first start: fresh.
func TestAKeptCoordinatorThatIsGoneStartsFresh(t *testing.T) {
	fake := agenttest.NewFakeSessionHost()
	recipe := CoordinatorRecipe{Cwd: "/w", Env: []EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: "kept"}}, AdoptEarlier: true}
	h := NewAgentHost(fake, agenttest.ClockAt(1_000_000), recipe)
	wantEnsured(t, h, started(false))
}

// Any failed start with --continue (a timeout included) gives up on resuming: the next
// attempt starts fresh. Only the "exited while starting" failure is retried at once.
func TestAFailedReplacementStartsFreshNextTime(t *testing.T) {
	for _, e := range []HostError{{Kind: HostTimeout, Detail: "claude did not start"}, {Kind: HostOther, Detail: "agent_pane_busy: not a shell"}} {
		h, fake, _ := hostRig()
		fake.StartAgent(bg, AgentSpec{Name: Coordinator, Kind: Claude, Cwd: "/old"})
		before := fake.StartAttempts()
		fake.FailStart(e)
		wantHostErr(t, h, e)
		if fake.StartAttempts()-before != 1 {
			t.Fatalf("%v: not retried in the same op", e)
		}
		fake.FailStart(nil)
		wantEnsured(t, h, started(false))
		if slices.Contains(fake.Starts()[len(fake.Starts())-1].Args, "--continue") {
			t.Fatal(e)
		}
	}
}

// `claude --continue` with no conversation to continue exits at once. An adopted pane that
// never had a prompt leaves exactly that: the start is retried once without --continue.
func TestAContinueWithNothingToContinueStartsFreshInstead(t *testing.T) {
	h, fake, clock := hostRig()
	fake.StartAgent(bg, AgentSpec{Name: Coordinator, Kind: Claude, Cwd: "/old"})
	fake.FailContinue(true)
	wantEnsured(t, h, started(false))
	spec := fake.Starts()[len(fake.Starts())-1]
	if slices.Contains(spec.Args, "--continue") {
		t.Fatal(spec.Args)
	}
	if h.ResumeEarlierRunForTest() {
		t.Fatal("no earlier conversation to resume")
	}
	// A later crash resumes the conversation this process started.
	fake.FailContinue(false)
	fake.Kill(Coordinator)
	clock.Advance(1)
	wantEnsured(t, h, started(true))
}

func TestAFailedFreshStartIsNotRetried(t *testing.T) {
	h, fake, _ := hostRig()
	fake.FailStart(HostError{Kind: HostOther, Detail: "x"})
	if _, err := h.EnsureCoordinator(bg); err == nil {
		t.Fatal("want an error")
	}
	if fake.StartAttempts() != 1 {
		t.Fatal(fake.StartAttempts())
	}
}

func TestStoppingSendsEscapeThenCloses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, fake, _ := hostRig()
		h.EnsureCoordinator(bg)
		if err := h.StopCoordinator(bg); err != nil {
			t.Fatal(err)
		}
		keys := fake.Keys()
		if len(keys) != 1 || keys[0].Agent != "coordinator" || !slices.Equal(keys[0].Keys, []string{"Escape"}) {
			t.Fatal(keys)
		}
		if !slices.Equal(fake.Closed(), []string{"coordinator"}) {
			t.Fatal(fake.Closed())
		}
		if err := h.StopCoordinator(bg); err != nil {
			t.Fatal("already gone is fine", err)
		}
	})
}

// ResumeLast: with no coordinator running, the first start resumes the workspace's last
// conversation (--continue); with nothing to continue it starts fresh in the same attempt.
func TestResumeLastResumesTheLastConversationOnTheFirstStart(t *testing.T) {
	recipe := CoordinatorRecipe{Cwd: "/data/agent/workspace", ResumeLast: true}
	fake := agenttest.NewFakeSessionHost()
	h := NewAgentHost(fake, agenttest.ClockAt(1_000_000), recipe)
	wantEnsured(t, h, started(true))
	if lastArg(fake.Starts()[0]) != "--continue" {
		t.Fatal(fake.Starts()[0].Args)
	}

	fake = agenttest.NewFakeSessionHost()
	fake.FailContinue(true)
	h = NewAgentHost(fake, agenttest.ClockAt(1_000_000), recipe)
	wantEnsured(t, h, started(false))
	if spec := fake.Starts()[len(fake.Starts())-1]; slices.Contains(spec.Args, "--continue") {
		t.Fatal(spec.Args)
	}
}
