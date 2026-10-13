package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// Ported from crates/agent-core/src/jobs.rs (16 tests).

func id(n uint64) JobID { return JobIDFromBits(n) }

func tableWith(n uint64) *JobTable {
	t := NewJobTable()
	t.Create(id(n), "summarise the issues", 1_000)
	return t
}

func mustTransition(t *testing.T, tbl *JobTable, j JobID, to JobStatus, now Millis) Job {
	t.Helper()
	got, err := tbl.Transition(j, to, now)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func wantJobErr(t *testing.T, err error, want JobError) {
	t.Helper()
	var je JobError
	if !errors.As(err, &je) || je != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestDelegateCreatesADelegatedJob(t *testing.T) {
	tbl := tableWith(1)
	j, _ := tbl.Get(id(1))
	if j.Status != JobDelegated || j.Task != "summarise the issues" || j.Created != 1_000 {
		t.Fatalf("%+v", j)
	}
	if !tbl.AnyOpen() {
		t.Fatal("open")
	}
}

func TestPickupMovesDelegatedToWorking(t *testing.T) {
	tbl := tableWith(1)
	if mustTransition(t, tbl, id(1), JobWorking, 2_000).Status != JobWorking {
		t.Fatal("working")
	}
}

func TestWorkingProgressStaysWorking(t *testing.T) {
	tbl := tableWith(1)
	mustTransition(t, tbl, id(1), JobWorking, 2_000)
	if mustTransition(t, tbl, id(1), JobWorking, 3_000).Updated != 3_000 {
		t.Fatal("updated")
	}
}

func TestAnyOpenJobCanBecomeAQuestionAndBack(t *testing.T) {
	for _, from := range []JobStatus{JobDelegated, JobWorking, JobQuestion} {
		tbl := tableWith(1)
		if from != JobDelegated {
			mustTransition(t, tbl, id(1), from, 2_000)
		}
		if mustTransition(t, tbl, id(1), JobQuestion, 3_000).Status != JobQuestion {
			t.Fatal("question")
		}
		if mustTransition(t, tbl, id(1), JobWorking, 4_000).Status != JobWorking {
			t.Fatal("working")
		}
	}
}

func TestAnyOpenJobCanFinishFailOrBeCancelled(t *testing.T) {
	for _, to := range []JobStatus{JobDone, JobFailed, JobCancelled} {
		for _, from := range []JobStatus{JobDelegated, JobWorking, JobQuestion} {
			tbl := tableWith(1)
			if from != JobDelegated {
				mustTransition(t, tbl, id(1), from, 2_000)
			}
			if mustTransition(t, tbl, id(1), to, 3_000).Status != to {
				t.Fatal(to)
			}
			if tbl.AnyOpen() {
				t.Fatal("open")
			}
		}
	}
}

func TestClosedJobsNeverChangeAndNothingReturnsToDelegated(t *testing.T) {
	tbl := tableWith(1)
	mustTransition(t, tbl, id(1), JobDone, 2_000)
	_, err := tbl.Transition(id(1), JobWorking, 3_000)
	wantJobErr(t, err, JobError{Kind: JobErrClosed, ID: id(1)})
	tbl = tableWith(2)
	_, err = tbl.Transition(id(2), JobDelegated, 3_000)
	wantJobErr(t, err, JobError{Kind: JobErrClosed, ID: id(2)})
	_, err = tbl.Transition(id(9), JobDone, 3_000)
	wantJobErr(t, err, JobError{Kind: JobErrUnknown, ID: id(9)})
}

func TestDelegationOffFailsWithAReason(t *testing.T) {
	tbl := tableWith(1)
	j, err := tbl.Fail(id(1), "the agent stopped", 2_000)
	if err != nil || j.Status != JobFailed || j.Reason == nil || *j.Reason != "the agent stopped" {
		t.Fatalf("%v %+v", err, j)
	}
}

func TestProgressIsRecordedOnOpenJobsOnly(t *testing.T) {
	tbl := tableWith(1)
	tbl.SetProgress(id(1), "running the tests", 2_000)
	j, _ := tbl.Get(id(1))
	if j.LastProgress == nil || *j.LastProgress != "running the tests" {
		t.Fatal("progress")
	}
	mustTransition(t, tbl, id(1), JobDone, 3_000)
	tbl.SetProgress(id(1), "late", 4_000)
	j, _ = tbl.Get(id(1))
	if *j.LastProgress != "running the tests" {
		t.Fatal("late progress recorded")
	}
}

func TestSnapshotListsOpenJobsWithAgeThenRecentClosedOnes(t *testing.T) {
	tbl := NewJobTable()
	for n := uint64(1); n <= 8; n++ {
		tbl.Create(id(n), fmt.Sprintf("task %d", n), n*1_000)
	}
	for n := uint64(1); n <= 7; n++ {
		mustTransition(t, tbl, id(n), JobDone, 10_000+n)
	}
	s := tbl.Snapshot(20_000)
	if s[0].JobID != id(8).String() || s[0].Status != JobDelegated || s[0].AgeSecs != 12 {
		t.Fatalf("%+v", s[0])
	}
	var closed []string
	for _, v := range s[1:] {
		closed = append(closed, v.JobID)
	}
	want := []string{id(7).String(), id(6).String(), id(5).String(), id(4).String(), id(3).String()}
	if !reflect.DeepEqual(closed, want) {
		t.Fatal(closed)
	}
	b, _ := json.Marshal(s[0])
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["status"] != "delegated" {
		t.Fatal(string(b))
	}
	if _, ok := m["last_progress"]; ok {
		t.Fatal(string(b))
	}
}

func TestLinesRoundTripAndTheLastLinePerJobWins(t *testing.T) {
	tbl := tableWith(1)
	l, _ := tbl.Line(id(1))
	file := []string{l}
	mustTransition(t, tbl, id(1), JobDone, 2_000)
	l, _ = tbl.Line(id(1))
	file = append(file, l, "not json")
	back, restarted := RestoreJobTable(file, 3_000)
	if len(restarted) != 0 {
		t.Fatal(restarted)
	}
	if j, _ := back.Get(id(1)); j.Status != JobDone {
		t.Fatal(j.Status)
	}
	if len(back.Lines()) != 1 {
		t.Fatal(back.Lines())
	}
}

func TestOpenJobsFailOnRestart(t *testing.T) {
	tbl := tableWith(1)
	back, restarted := RestoreJobTable(tbl.Lines(), 5_000)
	if !reflect.DeepEqual(restarted, []JobID{id(1)}) {
		t.Fatal(restarted)
	}
	j, _ := back.Get(id(1))
	if j.Status != JobFailed || j.Reason == nil || *j.Reason != "app restarted" || j.Updated != 5_000 {
		t.Fatalf("%+v", j)
	}
}

func row(name string, j uint64, status WorkerStatus) WorkerRow {
	return WorkerRow{Name: name, Kind: Claude, Repo: "focus", JobID: id(j), Status: status}
}

func TestWorkersAttachToOpenJobsOnlyAndShowInTheSnapshot(t *testing.T) {
	tbl := tableWith(1)
	if err := tbl.AttachWorker(row("focus-fix", 1, WorkerStarting), 2_000); err != nil {
		t.Fatal(err)
	}
	if j, _ := tbl.Get(id(1)); len(j.Workers) != 1 {
		t.Fatal("workers")
	}
	b, _ := json.Marshal(tbl.Snapshot(3_000)[0])
	var m struct {
		Workers []map[string]any `json:"workers"`
	}
	_ = json.Unmarshal(b, &m)
	if m.Workers[0]["name"] != "focus-fix" || m.Workers[0]["status"] != "starting" {
		t.Fatal(string(b))
	}
	mustTransition(t, tbl, id(1), JobDone, 4_000)
	wantJobErr(t, tbl.AttachWorker(row("focus-b", 1, WorkerStarting), 5_000), JobError{Kind: JobErrClosed, ID: id(1)})
	wantJobErr(t, tbl.AttachWorker(row("focus-c", 9, WorkerStarting), 5_000), JobError{Kind: JobErrUnknown, ID: id(9)})
}

func TestWorkerStatusChangesTheLiveRowAndReportsItsJob(t *testing.T) {
	tbl := tableWith(1)
	tbl.Create(id(2), "second", 1_000)
	_ = tbl.AttachWorker(row("focus-fix", 1, WorkerStopped), 1_000)
	_ = tbl.AttachWorker(row("focus-fix", 2, WorkerStarting), 1_000)
	if j, ok := tbl.SetWorkerStatus("focus-fix", WorkerRunning, 2_000); !ok || j != id(2) {
		t.Fatal("the live row, not the old one")
	}
	if _, ok := tbl.SetWorkerStatus("focus-fix", WorkerRunning, 3_000); ok {
		t.Fatal("unchanged")
	}
	if _, ok := tbl.SetWorkerStatus("nobody", WorkerStopped, 3_000); ok {
		t.Fatal("nobody")
	}
	mustTransition(t, tbl, id(2), JobDone, 4_000)
	if j, ok := tbl.SetWorkerStatus("focus-fix", WorkerStopped, 5_000); !ok || j != id(2) {
		t.Fatal("a closed job's worker can still stop")
	}
}

func TestWorkerRowsListOpenJobsAndLiveWorkersOfClosedOnes(t *testing.T) {
	tbl := tableWith(1)
	tbl.Create(id(2), "second", 1_000)
	_ = tbl.AttachWorker(row("a", 1, WorkerRunning), 1_000)
	_ = tbl.AttachWorker(row("b", 2, WorkerRunning), 1_000)
	_ = tbl.AttachWorker(row("c", 2, WorkerStopped), 1_000)
	mustTransition(t, tbl, id(2), JobDone, 2_000)
	var names []string
	for _, w := range tbl.WorkerRows() {
		names = append(names, w.Name)
	}
	if !reflect.DeepEqual(names, []string{"a", "b"}) {
		t.Fatal(names)
	}
}

func TestRestoredWorkersAreStopped(t *testing.T) {
	tbl := tableWith(1)
	_ = tbl.AttachWorker(row("a", 1, WorkerRunning), 1_000)
	back, _ := RestoreJobTable(tbl.Lines(), 2_000)
	if j, _ := back.Get(id(1)); j.Workers[0].Status != WorkerStopped {
		t.Fatal(j.Workers[0].Status)
	}
}

func TestClosedJobsOlderThan14DaysAreDropped(t *testing.T) {
	tbl := tableWith(1)
	tbl.Create(id(2), "newer", 1_000)
	mustTransition(t, tbl, id(1), JobDone, 1_000)
	mustTransition(t, tbl, id(2), JobDone, 2_000)
	back, _ := RestoreJobTable(tbl.Lines(), 1_001+RetentionMs)
	if _, ok := back.Get(id(1)); ok {
		t.Fatal("1 kept")
	}
	if _, ok := back.Get(id(2)); !ok {
		t.Fatal("2 dropped")
	}
}
