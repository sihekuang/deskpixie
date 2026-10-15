package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/voice"
)

// Pixie's memory comes from the coordinator side: the job table as a session block, and recall.

func memJob(n uint64, task string, status JobStatus, updated Millis) Job {
	return Job{ID: JobIDFromBits(n), Task: task, Status: status, Created: updated, Updated: updated}
}

func strp(s string) *string { return &s }

// noon on a Tuesday, in UTC.
var memNow = Millis(time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC).UnixMilli())

func TestMemoryFactsListsJobsWithTheirLastReply(t *testing.T) {
	done := memJob(1, "summarise the open issues", JobDone, memNow-3_600_000)
	done.Reply = strp("There are 12 open issues; 3 are bugs.")
	failed := memJob(2, "deploy the site", JobFailed, memNow-30*3_600_000)
	failed.Reason = strp("the coordinator stopped")
	working := memJob(3, "draft the newsletter", JobWorking, memNow-60_000)
	working.LastProgress = strp("Writing the intro.")
	got := MemoryFacts([]Job{done, failed, working}, nil, memNow, time.UTC)
	want := []string{
		"- [11:00 AM] done: summarise the open issues. Reply: There are 12 open issues; 3 are bugs.",
		"- [Mon 6:00 AM] failed: deploy the site. Why: the coordinator stopped",
		"- [11:59 AM] working: draft the newsletter. Latest: Writing the intro.",
	}
	if !strings.HasPrefix(got, "Recent work with the coordinator") || !strings.HasSuffix(got, strings.Join(want, "\n")) {
		t.Fatal(got)
	}
	if MemoryFacts(nil, nil, memNow, time.UTC) != "" {
		t.Fatal("no jobs, no block")
	}
}

func TestMemoryFactsKeepsOpenJobsAndTheNewestWithinTheBudget(t *testing.T) {
	var jobs []Job
	open := memJob(1, "the oldest job, still open", JobWorking, memNow-10*3_600_000)
	jobs = append(jobs, open)
	for n := uint64(2); n <= 40; n++ {
		j := memJob(n, fmt.Sprintf("task %d %s", n, strings.Repeat("x", 100)), JobDone, memNow-Millis(40-n)*60_000)
		j.Reply = strp(strings.Repeat("long reply ", 40))
		jobs = append(jobs, j)
	}
	jobs = append(jobs, memJob(41, "Recall: what did we decide about the logo?", JobDone, memNow))
	got := MemoryFacts(jobs, nil, memNow, time.UTC)
	if len([]rune(got)) > MemoryBudgetChars {
		t.Fatalf("%d chars", len([]rune(got)))
	}
	lines := strings.Split(got, "\n")[1:]
	if len(lines) > MemoryMaxJobs || !strings.Contains(lines[0], "the oldest job, still open") {
		t.Fatal(got)
	}
	if !strings.Contains(lines[len(lines)-1], "task 40 ") || strings.Contains(got, "task 2 ") {
		t.Fatal("the newest closed jobs are kept, oldest dropped:\n" + got)
	}
	if strings.Contains(got, "Recall:") {
		t.Fatal("recall questions are not work")
	}
	for _, l := range lines {
		if len([]rune(l)) > MemoryEntryChars {
			t.Fatal(l)
		}
	}
}

func TestAJobsReplyIsKeptAndSurvivesARestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &agenttest.MemStore{}
		r := rigWith(t, store, false)
		delegate(t, r, "count issues")
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="done">There are 12.</spoken>`)
		settle()
		if f := r.h.Facts(); !strings.Contains(f, "done: count issues. Reply: There are 12.") {
			t.Fatal(f)
		}
		again := rigWith(t, store, false)
		settle()
		if f := again.h.Facts(); !strings.Contains(f, "done: count issues. Reply: There are 12.") {
			t.Fatal(f)
		}
	})
}

func TestAnOldJobLineWithoutAReplyStillLoads(t *testing.T) {
	var j Job
	line := `{"id":"job_000000000001","task":"a","status":"done","created":1,"updated":2,"last_progress":null,"reason":null,"workers":[]}`
	if err := json.Unmarshal([]byte(line), &j); err != nil || j.Reply != nil {
		t.Fatal(err, j)
	}
	j.Reply = strp("ok")
	b, _ := json.Marshal(j)
	var back Job
	if err := json.Unmarshal(b, &back); err != nil || back.Reply == nil || *back.Reply != "ok" {
		t.Fatal(string(b))
	}
}

func TestRecallAsksTheCoordinatorAndKeepsListening(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		out := Recall{H: r.h}.Invoke(context.Background(), json.RawMessage(`{"question":"what did we decide about the logo?"}`))
		wantJSON(t, jsonOf(string(out.Output)), `{"job_id":"job_000000000001","status":"asked"}`)
		if out.EndAfterReply || out.EndConversation {
			t.Fatal("recall keeps the conversation open for the answer")
		}
		settle()
		prompts := r.host.Prompts()
		if len(prompts) != 1 || prompts[0].Agent != Coordinator ||
			!strings.HasPrefix(prompts[0].Text, "New request from the user: Recall: what did we decide about the logo?\n") ||
			!strings.Contains(prompts[0].Text, "not a task") || !strings.HasSuffix(prompts[0].Text, "[ref job_000000000001]") {
			t.Fatal(prompts)
		}
		r.voice.SetOpen(true)
		r.h.Voice(VoiceSignal{Kind: voice.SigOpened})
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="done">We picked the blue one.</spoken>`)
		settle()
		if !slices.Contains(r.voice.Calls(), agenttest.InjectCall("Coordinator reply: We picked the blue one.")) {
			t.Fatal(r.voice.Calls())
		}
		if strings.Contains(r.h.Facts(), "Recall:") {
			t.Fatal(r.h.Facts())
		}
		if bad := invoke(t, Recall{H: r.h}, `{"question":"  "}`); bad.(map[string]any)["error"] == nil {
			t.Fatal(bad)
		}
	})
}
