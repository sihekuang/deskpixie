package agent_test

import (
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

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
		// #189: with the stamp of what it was started with.
		if len(saved) != 1 || saved[0].CoordinatorToken != "tok-coordinator" || saved[0].CoordinatorStamp != "stamp-1" || len(saved[0].Workers) != 1 {
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
		// The reconnects are typed; the coordinator's and the worker's prompts wait for their
		// results (#168, #172).
		host.SetScreen(Coordinator, "❯ "+McpReconnect+"\n  ⎿  Successfully reconnected to pixie")
		host.SetScreen(wName, "❯ "+McpReconnect+"\n  ⎿  Successfully reconnected to pixie")
		time.Sleep(time.Second) // the screen is polled, not watched
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
		// Its pixie tools come back before it's asked (#168).
		var coord []string
		for _, p := range ps {
			if p.Agent == Coordinator {
				coord = append(coord, p.Text)
			}
		}
		if len(coord) < 2 || coord[0] != McpReconnect || !strings.Contains(coord[1], CoordinatorRestartNote) {
			t.Fatal("not reconnected first", coord)
		}
		// So do the worker's, before it's told to report again (#172).
		if worker := promptsTo(ps, wName); len(worker) < 2 || worker[0] != McpReconnect || !strings.Contains(worker[1], WorkerRestartNote) {
			t.Fatal("worker not reconnected first", worker)
		}
	})
}

func promptsTo(ps []agenttest.Prompt, agent string) (out []string) {
	for _, p := range ps {
		if p.Agent == agent {
			out = append(out, p.Text)
		}
	}
	return out
}

// A kept Codex worker takes the pixie server by -c overrides: it's told the restart note as is.
func TestAKeptCodexWorkerIsNotToldToReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := agenttest.NewFakeSessionHost().ManualTurns()
		host.StartAgent(t.Context(), AgentSpec{Name: Coordinator, Kind: Claude, Cwd: "/data/agent/workspace"})
		host.StartAgent(t.Context(), AgentSpec{Name: wName, Kind: Codex, Cwd: "/projects/focus/.worktrees/upload"})
		kw := keptWorker()
		kw.Plan.Kind = Codex
		rigBuild(t, true, true, openJobStore(), agenttest.FakeWorktreesWithRepo("focus"), host, KeptAgents{CoordinatorToken: "kept-c", Workers: []KeptWorker{kw}})
		settle()
		host.SetScreen(Coordinator, "❯ "+McpReconnect+"\n  ⎿  Successfully reconnected to pixie")
		time.Sleep(time.Second)
		if worker := promptsTo(host.Prompts(), wName); len(worker) == 0 || slices.Contains(worker, McpReconnect) || !strings.Contains(worker[0], WorkerRestartNote) {
			t.Fatal(worker)
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
