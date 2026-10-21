package agent

import "testing"

const workerDone = "The upload worker in focus says it's done: Fixed the flaky test."

// typedWorkerJob: job 1 typed in the text box and handed to a worker that is now running.
func typedWorkerJob(t *testing.T, textMode bool) *AgentController {
	t.Helper()
	s := workerSettings()
	s.SpeakWorkerReports = true
	c := NewAgentController(s)
	c.Step(TextModeInput(textMode), T0)
	c.Step(TypedDelegateInput(job(1), "fix the upload test in focus", ""), T0)
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	running(t, c)
	return c
}

// workerReports: the worker reports job 1 done, and the held update is flushed.
func workerReports(t *testing.T, c *AgentController) []Effect {
	t.Helper()
	call := McpCall{Kind: CallReportResult, Outcome: OutcomeDone, Summary: "Fixed the flaky test", Evidence: "go test passed"}
	mcp(t, c, workerName, call, T0)
	return c.Step(TickInput(), T0+UpdateQuietMs)
}

// usualRoute: the report went out as a spoken update would (outbox or notification), not to the box.
func usualRoute(t *testing.T, fx []Effect) {
	t.Helper()
	if got := chats(fx); len(got) != 0 {
		t.Fatalf("went to the box: %v", got)
	}
	for _, e := range fx {
		if e.Kind == EfNotify || e.Kind == EfInject || e.Kind == EfAppend && e.File == OutboxFile {
			return
		}
	}
	t.Fatalf("not delivered at all: %s", fmtEffects(fx))
}

// A worker's own report on a job typed in the text box goes to the box (EfChat), by the same
// route as the coordinator's reply, not to the voice or the updates bubble.
func TestATypedJobsWorkerResultGoesToTheCard(t *testing.T) {
	c := typedWorkerJob(t, true)
	fx := workerReports(t, c)
	got := chats(fx)
	if len(got) != 1 || got[0].Job != job(1) || got[0].Text != workerDone {
		t.Fatalf("chat lines %v (%s)", got, fmtEffects(fx))
	}
	noUsualRoute(t, fx)
}

// Back in voice mode the box is gone: the report takes the usual route, so it isn't lost.
func TestAWorkerReportInVoiceModeTakesTheUsualRoute(t *testing.T) {
	c := typedWorkerJob(t, true)
	c.Step(TextModeInput(false), T0)
	usualRoute(t, workerReports(t, c))
}

// The box shows only the newest typed job: an older job's report takes the usual route.
func TestAnOlderTypedJobsWorkerReportTakesTheUsualRoute(t *testing.T) {
	c := typedWorkerJob(t, true)
	c.Step(TypedDelegateInput(job(2), "what's running?", ""), T0)
	usualRoute(t, workerReports(t, c))
}

// While the coordinator waits on its question in the box, a worker's report doesn't replace it
// (the next thing typed answers that question): the report takes the usual route.
func TestAWorkerReportDoesNotHideTheCoordinatorsQuestion(t *testing.T) {
	c := typedWorkerJob(t, true)
	fx := stop(c, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`, T0)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "question", Text: "Which repo?"})
	usualRoute(t, workerReports(t, c))
}
