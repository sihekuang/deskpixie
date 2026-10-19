package agent_test

import (
	"strings"
	"testing"

	. "github.com/sihekuang/deskpixie/core/agent"
)

// #153: under a new request, your sessions, then Pixie's live workers with their job's task,
// then the order to hand it on in.
func TestRequestNoteListsSessionsThenWorkersThenTheOrder(t *testing.T) {
	sessions := []SessionRow{{Name: "focus-claude", Description: "login bug", Repo: "focus", Status: "idle", LastTurn: "Fixed the redirect."}}
	workers := []WorkerRow{
		{Name: "focus-upload", Repo: "focus", JobID: "job_1", Status: WorkerRunning},
		{Name: "focus-old", Repo: "focus", JobID: "job_2", Status: WorkerStopped},
	}
	task := func(id JobID) string {
		return map[JobID]string{"job_1": "fix the upload test", "job_2": "old work"}[id]
	}
	got := RequestNote(sessions, workers, task)
	want := "Your open pix sessions:\n" +
		`- focus-claude: "login bug", focus repo, idle, last turn "Fixed the redirect."` + "\n" +
		"Pixie's workers:\n" +
		`- focus-upload: focus repo, running, on "fix the upload test"` + "\n" +
		"Hand this on in this order: a session on the same thing (message_session), else a worker on it (message_worker), else do it yourself."
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRequestNoteWithOnlyWorkersOrNothing(t *testing.T) {
	workers := []WorkerRow{{Name: "focus-upload", Repo: "focus", JobID: "job_1", Status: WorkerStarting}}
	got := RequestNote(nil, workers, func(JobID) string { return "fix it" })
	if strings.Contains(got, "pix sessions") || !strings.Contains(got, `- focus-upload: focus repo, starting, on "fix it"`) {
		t.Fatal(got)
	}
	if got := RequestNote(nil, []WorkerRow{{Name: "x", Status: WorkerFailed}}, func(JobID) string { return "" }); got != "" {
		t.Fatalf("nothing open, nothing said: %q", got)
	}
}
