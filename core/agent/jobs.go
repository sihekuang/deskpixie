package agent

import (
	"encoding/json"
	"slices"
)

// The job table (port of jobs.rs): the single source of truth for delegated jobs.
// Pure: callers pass now; persistence is jsonl lines the caller writes through Store.

// RetentionMs: closed jobs older than this are dropped when the file is compacted at start.
const RetentionMs Millis = 14 * 24 * 60 * 60 * 1000

// JobsFile is the file name under `<app_data>/agent/`.
const JobsFile = "jobs.jsonl"

// JobErrorKind tells JobError variants apart.
type JobErrorKind int

const (
	JobErrUnknown JobErrorKind = iota
	JobErrClosed
)

// JobError is a refused job change.
type JobError struct {
	Kind JobErrorKind
	ID   JobID
}

func (e JobError) Error() string {
	if e.Kind == JobErrClosed {
		return "job " + string(e.ID) + " is already closed"
	}
	return "no job " + string(e.ID)
}

// JobView is what `jobs` status returns per job.
type JobView struct {
	JobID        string      `json:"job_id"`
	Status       JobStatus   `json:"status"`
	Task         string      `json:"task"`
	AgeSecs      uint64      `json:"age_secs"`
	LastProgress *string     `json:"last_progress,omitempty"`
	Workers      []WorkerRow `json:"workers,omitempty"`
}

// JobTable holds every job, in creation order.
type JobTable struct {
	jobs []Job
}

func NewJobTable() *JobTable { return &JobTable{} }

func (t *JobTable) find(id JobID) *Job {
	for k := range t.jobs {
		if t.jobs[k].ID == id {
			return &t.jobs[k]
		}
	}
	return nil
}

// Create adds a Delegated job and returns it.
func (t *JobTable) Create(id JobID, task string, now Millis) Job {
	t.jobs = append(t.jobs, Job{ID: id, Task: task, Status: JobDelegated, Created: now, Updated: now, Workers: []WorkerRow{}})
	return cloneJob(t.jobs[len(t.jobs)-1])
}

func cloneJob(j Job) Job {
	j.Workers = slices.Clone(j.Workers)
	return j
}

func (t *JobTable) Get(id JobID) (Job, bool) {
	if j := t.find(id); j != nil {
		return cloneJob(*j), true
	}
	return Job{}, false
}

// Open returns the open jobs, oldest first.
func (t *JobTable) Open() []Job {
	var out []Job
	for _, j := range t.jobs {
		if j.Status.IsOpen() {
			out = append(out, cloneJob(j))
		}
	}
	return out
}

func (t *JobTable) AnyOpen() bool {
	return slices.ContainsFunc(t.jobs, func(j Job) bool { return j.Status.IsOpen() })
}

func (t *JobTable) IsOpen(id JobID) bool {
	j := t.find(id)
	return j != nil && j.Status.IsOpen()
}

// Transition moves an open job to `to`. Closed jobs never change again, and nothing goes back
// to Delegated.
func (t *JobTable) Transition(id JobID, to JobStatus, now Millis) (Job, error) {
	j := t.find(id)
	if j == nil {
		return Job{}, JobError{JobErrUnknown, id}
	}
	if !j.Status.IsOpen() || to == JobDelegated {
		return Job{}, JobError{JobErrClosed, id}
	}
	j.Status = to
	j.Updated = now
	return cloneJob(*j), nil
}

// Fail moves an open job to Failed with a reason.
func (t *JobTable) Fail(id JobID, reason string, now Millis) (Job, error) {
	if _, err := t.Transition(id, JobFailed, now); err != nil {
		return Job{}, err
	}
	j := t.find(id)
	j.Reason = &reason
	return cloneJob(*j), nil
}

// SetProgress records a progress phrase on an open job.
func (t *JobTable) SetProgress(id JobID, phrase string, now Millis) {
	if j := t.find(id); j != nil && j.Status.IsOpen() {
		j.LastProgress = &phrase
		j.Updated = now
	}
}

// Snapshot is `jobs` status: open jobs first (oldest first), then the 5 most recently closed.
func (t *JobTable) Snapshot(now Millis) []JobView {
	view := func(j Job) JobView {
		return JobView{JobID: string(j.ID), Status: j.Status, Task: j.Task, AgeSecs: satSub(now, j.Created) / 1000,
			LastProgress: j.LastProgress, Workers: slices.Clone(j.Workers)}
	}
	var out []JobView
	var closed []Job
	for _, j := range t.jobs {
		if j.Status.IsOpen() {
			out = append(out, view(j))
		} else {
			closed = append(closed, j)
		}
	}
	// Stable, newest update first (Rust sort_by_key with Reverse is stable too).
	slices.SortStableFunc(closed, func(a, b Job) int {
		switch {
		case a.Updated > b.Updated:
			return -1
		case a.Updated < b.Updated:
			return 1
		}
		return 0
	})
	for _, j := range closed[:min(len(closed), 5)] {
		out = append(out, view(j))
	}
	return out
}

// AttachWorker records a new worker on its (open) job.
func (t *JobTable) AttachWorker(row WorkerRow, now Millis) error {
	j := t.find(row.JobID)
	if j == nil {
		return JobError{JobErrUnknown, row.JobID}
	}
	if !j.Status.IsOpen() {
		return JobError{JobErrClosed, row.JobID}
	}
	j.Workers = append(j.Workers, row)
	j.Updated = now
	return nil
}

// SetWorkerStatus sets the status of the worker row named name (the live one, else the
// newest); returns its job if the status changed. Rows on closed jobs still change (a stop
// after a job ends).
func (t *JobTable) SetWorkerStatus(name string, status WorkerStatus, now Millis) (JobID, bool) {
	live := false
	for _, j := range t.jobs {
		for _, w := range j.Workers {
			if w.Name == name && w.Status.IsLive() {
				live = true
			}
		}
	}
	for k := len(t.jobs) - 1; k >= 0; k-- {
		j := &t.jobs[k]
		for r := len(j.Workers) - 1; r >= 0; r-- {
			w := &j.Workers[r]
			if w.Name != name || live && !w.Status.IsLive() {
				continue
			}
			if w.Status == status {
				return "", false
			}
			w.Status = status
			if j.Status.IsOpen() {
				j.Updated = now
			}
			return j.ID, true
		}
	}
	return "", false
}

// WorkerRows is `list_workers`: every row of an open job, plus live rows of closed ones;
// oldest job first.
func (t *JobTable) WorkerRows() []WorkerRow {
	var out []WorkerRow
	for _, j := range t.jobs {
		for _, w := range j.Workers {
			if j.Status.IsOpen() || w.Status.IsLive() {
				out = append(out, w)
			}
		}
	}
	return out
}

func jobLine(j Job) string {
	b, err := json.Marshal(j)
	if err != nil {
		panic("job serializes: " + err.Error())
	}
	return string(b)
}

// Line is the job as one jsonl line (append it after every change).
func (t *JobTable) Line(id JobID) (string, bool) {
	if j := t.find(id); j != nil {
		return jobLine(*j), true
	}
	return "", false
}

// Lines is every job as jsonl (the compacted file).
func (t *JobTable) Lines() []string {
	out := make([]string, len(t.jobs))
	for k, j := range t.jobs {
		out[k] = jobLine(j)
	}
	return out
}

// RestoreJobTable rebuilds the table from jobs.jsonl at start. The last line per id wins;
// unreadable lines are skipped. Jobs still open become Failed("app restarted") (returned so
// the caller can outbox a line for each) unless keepOpen (the coordinator was kept running over
// the quit and still has them), and closed jobs older than 14 days are dropped.
func RestoreJobTable(lines []string, now Millis, keepOpen bool) (*JobTable, []JobID) {
	var order []JobID
	latest := map[JobID]Job{}
	for _, line := range lines {
		var j Job
		if err := json.Unmarshal([]byte(line), &j); err != nil {
			continue
		}
		if _, seen := latest[j.ID]; !seen {
			order = append(order, j.ID)
		}
		latest[j.ID] = j
	}
	table := NewJobTable()
	var restarted []JobID
	for _, id := range order {
		j := latest[id]
		if j.Workers == nil {
			j.Workers = []WorkerRow{}
		}
		// No worker of an earlier run is tracked any more (its token died with that run); a kept
		// one's row is set running again when it is adopted.
		for k := range j.Workers {
			if j.Workers[k].Status.IsLive() {
				j.Workers[k].Status = WorkerStopped
			}
		}
		if j.Status.IsOpen() && !keepOpen {
			reason := "app restarted"
			j.Status = JobFailed
			j.Reason = &reason
			j.Updated = now
			restarted = append(restarted, j.ID)
		} else if !j.Status.IsOpen() && satSub(now, j.Updated) > RetentionMs {
			continue
		}
		table.jobs = append(table.jobs, j)
	}
	return table, restarted
}
