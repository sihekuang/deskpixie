package agent

import (
	"fmt"
	"testing"
)

// #237: a worker stays open when its job ends; the coordinator closes it (stop_worker) once
// the person is done with it. Only a cancel or the quit closes workers by itself.

func TestAJobThatEndsKeepsItsWorkersOpen(t *testing.T) {
	for _, status := range []string{"done", "failed"} {
		t.Run(status, func(t *testing.T) {
			c := wctl()
			running(t, c)
			fx := c.Step(StopInput(Coordinator, fmt.Sprintf(`<spoken ref="%s" status="%s">Fixed.</spoken>`, job(1), status)), T0)
			if anyKind(fx, EfStopWorker) {
				t.Fatalf("closed with its job: %s", fmtEffects(fx))
			}
			if w := c.workers.Get(workerName); w == nil || w.StopRequested {
				t.Fatal("not kept")
			}
			wantRows(t, c, rowView{workerName, WorkerRunning})
		})
	}
}

func TestAJobFailedByTheAppKeepsItsWorkersOpen(t *testing.T) {
	c := wctl()
	running(t, c)
	fx := c.Step(CoordinatorFailedInput(HostError{Kind: HostGaveUp}), T0)
	wantJobStatus(t, c, 1, JobFailed)
	if anyKind(fx, EfStopWorker) {
		t.Fatalf("closed with its job: %s", fmtEffects(fx))
	}
}

func TestAKeptWorkerIsClosedWhenTheCoordinatorStopsIt(t *testing.T) {
	c := wctl()
	running(t, c)
	c.Step(StopInput(Coordinator, fmt.Sprintf(`<spoken ref="%s" status="done">Fixed.</spoken>`, job(1))), T0)
	_, err, fx := mcp(t, c, Coordinator, McpCall{Kind: CallStopWorker, Name: workerName}, T0+1)
	if err != "" {
		t.Fatal(err)
	}
	wantContains(t, fx, eStopWorker(workerName))
	fx = c.Step(WorkerClosedInput(workerName), T0+6_000)
	wantContains(t, fx, eForgetWorker(workerName))
	wantPrompts(t, fx) // a stop it asked for is not news for the coordinator
}

func TestCancelStillClosesTheJobsWorkers(t *testing.T) {
	c := wctl()
	running(t, c)
	wantContains(t, c.Step(CancelInput(job(1)), T0), eStopWorker(workerName))
}

// newRequestInFlight: job 1 ended (its worker kept), job 2 is the coordinator's turn now.
func newRequestInFlight(t *testing.T) *AgentController {
	t.Helper()
	c := wctl()
	running(t, c)
	c.Step(StopInput(Coordinator, fmt.Sprintf(`<spoken ref="%s" status="done">Fixed.</spoken>`, job(1))), T0)
	c.Step(DelegateInput(job(2), "also fix the docs test", ""), T0+1)
	c.Step(TickInput(), T0+StopSettleMs)
	if refs := c.inbox.InFlightRefs(); len(refs) != 1 || refs[0] != job(2) {
		t.Fatalf("in flight %v", refs)
	}
	return c
}

func TestAKeptWorkerMessagedForANewRequestWorksForIt(t *testing.T) {
	c := newRequestInFlight(t)
	v, err, _ := mcp(t, c, Coordinator, McpCall{Kind: CallMessageWorker, Name: workerName, Text: "Now the docs test."}, T0+StopSettleMs+1)
	wantToolOK(t, v, err, map[string]any{"name": workerName, "status": "queued"}) // it is still busy
	if w := c.workers.Get(workerName); w == nil || w.Plan.Job != job(2) {
		t.Fatalf("still on its old job: %+v", w)
	}
	j2, _ := c.jobs.Get(job(2))
	j1, _ := c.jobs.Get(job(1))
	if len(j2.Workers) != 1 || j2.Workers[0].Name != workerName || j2.Workers[0].JobID != job(2) || len(j1.Workers) != 0 {
		t.Fatalf("rows: job 1 %+v, job 2 %+v", j1.Workers, j2.Workers)
	}
	// Its reports and permission requests count again, for job 2.
	_, err, _ = mcp(t, c, workerName, McpCall{Kind: CallReportProgress, Summary: "on the docs test"}, T0+StopSettleMs+2)
	if err != "" {
		t.Fatal(err)
	}
	fx := c.Step(PermissionInput(9, workerName, "Bash", map[string]any{"command": "go test ./..."}), T0+StopSettleMs+3)
	if len(decisions(fx)) != 0 {
		t.Fatalf("denied at once: %s", fmtEffects(fx))
	}
}

// With no one request for it to join, its replies would be refused: nothing is sent.
func TestAKeptWorkerMessagedOutsideARequestIsRefused(t *testing.T) {
	c := wctl()
	running(t, c)
	c.Step(StopInput(Coordinator, fmt.Sprintf(`<spoken ref="%s" status="done">Fixed.</spoken>`, job(1))), T0)
	_, err, fx := mcp(t, c, Coordinator, McpCall{Kind: CallMessageWorker, Name: workerName, Text: "Thanks."}, T0+1)
	wantToolErr(t, err, "focus-upload's request has ended, and no single request is in progress for it to join, so its replies would be lost.")
	if anyKind(fx, EfPromptWorker) {
		t.Fatal(fmtEffects(fx))
	}
	if w := c.workers.Get(workerName); w == nil || w.Plan.Job != job(1) {
		t.Fatalf("%+v", w)
	}
}

// A kept worker that reported keeps no job open: a reply with no status ends it.
func TestAWorkerThatReportedDoesNotKeepAReplyWithoutAStatusOpen(t *testing.T) {
	c := newRequestInFlight(t)
	mcp(t, c, Coordinator, McpCall{Kind: CallMessageWorker, Name: workerName, Text: "Now the docs test."}, T0+StopSettleMs+1)
	c.Step(StopInput(workerName, "ok"), T0+StopSettleMs+2)
	c.Step(StopInput(Coordinator, fmt.Sprintf(`<spoken ref="%s">Asked it.</spoken>`, job(2))), T0+StopSettleMs+3)
	wantJobStatus(t, c, 2, JobWorking, "still on the docs test")
	_, err, _ := mcp(t, c, workerName, McpCall{Kind: CallReportResult, Outcome: OutcomeDone, Summary: "docs test fixed", Evidence: "x"}, T0+StopSettleMs+4)
	if err != "" {
		t.Fatal(err)
	}
	c.Step(StopInput(Coordinator, fmt.Sprintf(`<spoken ref="%s">The docs test is fixed.</spoken>`, job(2))), T0+StopSettleMs+5)
	wantJobStatus(t, c, 2, JobDone, "its worker reported and is only kept open")
}

// A worker whose job the restart itself failed was mid-task: it is closed, as before.
func TestAWorkerWhoseJobTheRestartFailedIsClosed(t *testing.T) {
	first := wctl()
	running(t, first)
	line, _ := first.jobs.Line(job(1))
	c := NewAgentController(workerSettings())
	c.Step(RestoredInput([]string{line}, nil, false), T0+10)
	wantJobStatus(t, c, 1, JobFailed)
	fx := c.Step(AdoptWorkerInput(keptPlan(), "/projects/focus/.worktrees/upload", PaneIdle), T0+20)
	wantContains(t, fx, eStopWorker(workerName))
	if c.workers.Get(workerName) != nil {
		t.Fatal("adopted")
	}
}

func TestAKeptWorkerWhoseJobFinishedIsAdoptedQuietlyAfterARestart(t *testing.T) {
	c := wctl()
	_ = c.jobs.AttachWorker(WorkerRow{Name: workerName, Kind: Claude, Repo: "focus", JobID: job(1), Status: WorkerStopped}, T0)
	c.Step(StopInput(Coordinator, fmt.Sprintf(`<spoken ref="%s" status="done">Fixed.</spoken>`, job(1))), T0)
	fx := c.Step(AdoptWorkerInput(keptPlan(), "/projects/focus/.worktrees/upload", PaneIdle), T0+1)
	if anyKind(fx, EfStopWorker) {
		t.Fatalf("closed at the restart: %s", fmtEffects(fx))
	}
	if w := c.workers.Get(workerName); w == nil || !w.IsUp() {
		t.Fatal("not adopted")
	}
	for _, e := range fx {
		if e.Kind == EfAdmitted && e.Err != "" {
			t.Fatal(e.Err)
		}
	}
	// No restart note: its job is over, so there is nothing to report again.
	if anyKind(c.Step(TickInput(), T0+WorkerStartReadyMs+1), EfPromptWorker) {
		t.Fatal("told to report again")
	}
	wantJobStatus(t, c, 1, JobDone)
	wantRows(t, c, rowView{workerName, WorkerRunning})
}
