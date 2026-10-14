package agent

import (
	"strings"
	"testing"
)

// Agents kept running over a quit (agent.keepPanesOnQuit): the next run keeps their jobs open
// and adopts the workers whose panes are still there.

func TestRestoreWithAKeptCoordinatorKeepsJobsOpenAndAsksForTheirResultsAgain(t *testing.T) {
	first := ctl()
	var jobs []string
	for _, e := range delegate(first, 1, "summarise the issues") {
		if e.Kind == EfAppend && e.File == JobsFile {
			jobs = append(jobs, e.Line)
		}
	}
	c := ctl()
	c.Step(RestoredInput(jobs, nil, true), T0+10)
	wantJobStatus(t, c, 1, JobDelegated)
	wantContains(t, c.Step(EnsureCoordinatorInput(), T0+20), eStart)
	ps := prompts(c.Step(CoordinatorUpInput(PaneIdle), T0+30))
	if len(ps) != 1 || !strings.Contains(ps[0], CoordinatorRestartNote) || !strings.Contains(ps[0], "[ref "+job(1).String()+"]") {
		t.Fatalf("%q", ps)
	}
	// Nothing about a stopped job is outboxed.
	wantNone(t, visible(c.Step(voiceOpened(), T0+40)))
}

func keptPlan() WorkerPlan {
	return WorkerPlan{Name: workerName, Slug: "upload", Kind: Claude, Repo: "/projects/focus", RepoName: "focus", Job: job(1), Task: "Fix the flaky upload test."}
}

func TestAnAdoptedWorkerIsUpRunningAndToldToReportAgain(t *testing.T) {
	c := wctl()
	// The row restored from jobs.jsonl reads stopped until the worker is adopted.
	_ = c.jobs.AttachWorker(WorkerRow{Name: workerName, Kind: Claude, Repo: "focus", JobID: job(1), Status: WorkerStopped}, T0)
	fx := c.Step(AdoptWorkerInput(keptPlan(), "/projects/focus/.worktrees/upload", PaneIdle), T0)
	var admitted *Effect
	for _, e := range fx {
		if e.Kind == EfAdmitted {
			admitted = &e
		}
	}
	if admitted == nil || admitted.Err != "" || admitted.Plan.Name != workerName {
		t.Fatalf("%s", fmtEffects(fx))
	}
	wantContains(t, fx, ePromptWorker(workerName, WorkerRestartNote))
	if w := c.workers.Get(workerName); w == nil || !w.IsUp() {
		t.Fatal("not live and up")
	}
	j, _ := c.jobs.Get(job(1))
	if j.Workers[0].Status != WorkerRunning {
		t.Fatalf("%+v", j.Workers)
	}
	wantJobStatus(t, c, 1, JobWorking)
}

func TestAKeptWorkerWhoseJobEndedIsStopped(t *testing.T) {
	c := wctl()
	c.Step(CancelInput(job(1)), T0)
	fx := c.Step(AdoptWorkerInput(keptPlan(), "/projects/focus/.worktrees/upload", PaneIdle), T0)
	wantContains(t, fx, eStopWorker(workerName))
	if c.workers.Get(workerName) != nil {
		t.Fatal("adopted on a closed job")
	}
}

func TestTheLiveWorkersAreWhatAQuitKeeps(t *testing.T) {
	c := wctl()
	plan := admit(t, c)
	up(c, workerName, PaneIdle)
	kept := c.LiveWorkers()
	if len(kept) != 1 || kept[0].Plan != plan || kept[0].Worktree != "/projects/focus/.worktrees/upload" {
		t.Fatalf("%+v", kept)
	}
	// A worker being stopped is not kept.
	mcp(t, c, Coordinator, McpCall{Kind: CallStopWorker, Name: workerName}, T0)
	if kept := c.LiveWorkers(); len(kept) != 0 {
		t.Fatalf("%+v", kept)
	}
}
