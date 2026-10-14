package agent_test

import (
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// Agents kept running over a quit (agent.keepPanesOnQuit) on fakes: what the quit saves, and
// the next run adopting them.

func TestAQuitThatKeepsPanesSavesTheCoordinatorAndItsLiveWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rigKeeping(t, true, true)
		invoke(t, Delegate{H: r.h}, `{"task":"fix the upload test in focus"}`)
		settle()
		okValue(t, call(r, Coordinator, startCall("focus")))
		settle()
		r.h.Shutdown()
		saved := r.kept.Saved()
		if len(saved) != 1 || saved[0].CoordinatorToken != "tok-coordinator" || len(saved[0].Workers) != 1 {
			t.Fatalf("%+v", saved)
		}
		w := saved[0].Workers[0]
		if w.Plan.Name != wName || w.Plan.Job.String() != wJob || w.Worktree == "" || w.Token != "tok-"+wName {
			t.Fatalf("%+v", w)
		}
	})
}

func TestAQuitThatClosesPanesSavesNothing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rigKeeping(t, true, false)
		invoke(t, Delegate{H: r.h}, `{"task":"fix the upload test in focus"}`)
		settle()
		r.h.Shutdown()
		if saved := r.kept.Saved(); len(saved) != 0 {
			t.Fatalf("%+v", saved)
		}
	})
}

// A coordinator that never started this run is not saved: a pane from an older run under that
// name holds another token.
func TestACoordinatorThatNeverStartedIsNotKept(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rigKeeping(t, true, true)
		r.h.Shutdown()
		if saved := r.kept.Saved(); len(saved) != 1 || saved[0].CoordinatorToken != "" {
			t.Fatalf("%+v", saved)
		}
	})
}

// openJobStore holds job 1, still open, with its worker running at the quit, as jobs.jsonl.
func openJobStore() *agenttest.MemStore {
	tbl := NewJobTable()
	tbl.Create(JobIDFromBits(1), "fix the upload test in focus", 1_000)
	_ = tbl.AttachWorker(WorkerRow{Name: wName, Kind: Claude, Repo: "focus", JobID: JobIDFromBits(1), Status: WorkerRunning}, 1_000)
	store := &agenttest.MemStore{}
	store.Rewrite(JobsFile, tbl.Lines())
	return store
}

func keptWorker() KeptWorker {
	plan := WorkerPlan{Name: wName, Slug: "upload", Kind: Claude, Repo: "/projects/focus", RepoName: "focus", Job: JobIDFromBits(1), Task: "Fix the flaky upload test."}
	return KeptWorker{Plan: plan, Worktree: "/projects/focus/.worktrees/upload", Token: "kept-w"}
}

func TestTheNextRunAdoptsTheKeptCoordinatorAndWorkers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := agenttest.NewFakeSessionHost().ManualTurns()
		host.StartAgent(t.Context(), AgentSpec{Name: Coordinator, Kind: Claude, Cwd: "/data/agent/workspace"})
		host.StartAgent(t.Context(), AgentSpec{Name: wName, Kind: Claude, Cwd: "/projects/focus/.worktrees/upload"})
		kept := KeptAgents{CoordinatorToken: "kept-c", Workers: []KeptWorker{keptWorker()}}
		r := rigBuild(t, true, true, openJobStore(), agenttest.FakeWorktreesWithRepo("focus"), host, kept)
		settle()
		if r.tokens.Restored()[wName] != "kept-w" {
			t.Fatal(r.tokens.Restored())
		}
		if n := len(host.Starts()); n != 2 || len(host.Closed()) != 0 {
			t.Fatal("adopted, not replaced:", host.Starts(), host.Closed())
		}
		jobs := r.h.JobStatus()
		if len(jobs) != 1 || !jobs[0].Status.IsOpen() || len(jobs[0].Workers) != 1 || jobs[0].Workers[0].Status != WorkerRunning {
			t.Fatalf("%+v", jobs)
		}
		ps := host.Prompts()
		if !slices.ContainsFunc(ps, func(p agenttest.Prompt) bool { return p.Agent == wName && strings.Contains(p.Text, WorkerRestartNote) }) {
			t.Fatal("worker not told", ps)
		}
		if !slices.ContainsFunc(ps, func(p agenttest.Prompt) bool {
			return p.Agent == Coordinator && strings.Contains(p.Text, CoordinatorRestartNote)
		}) {
			t.Fatal("coordinator not asked again", ps)
		}
	})
}

func TestAKeptWorkerWhosePaneIsGoneIsNotAdopted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := agenttest.NewFakeSessionHost().ManualTurns()
		host.StartAgent(t.Context(), AgentSpec{Name: Coordinator, Kind: Claude, Cwd: "/data/agent/workspace"})
		kept := KeptAgents{CoordinatorToken: "kept-c", Workers: []KeptWorker{keptWorker()}}
		r := rigBuild(t, true, true, openJobStore(), agenttest.FakeWorktreesWithRepo("focus"), host, kept)
		settle()
		if _, ok := r.tokens.Restored()[wName]; ok {
			t.Fatal("restored a gone worker's token")
		}
		if jobs := r.h.JobStatus(); len(jobs) != 1 || len(jobs[0].Workers) != 1 || jobs[0].Workers[0].Status != WorkerStopped {
			t.Fatalf("%+v", jobs)
		}
	})
}
