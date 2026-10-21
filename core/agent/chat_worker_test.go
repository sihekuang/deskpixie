package agent

import "testing"

// A worker's own report on a job typed in the text box goes to the box (EfChat), by the same
// route as the coordinator's reply, not to the voice or the updates bubble.
func TestATypedJobsWorkerResultGoesToTheCard(t *testing.T) {
	s := workerSettings()
	s.SpeakWorkerReports = true
	c := NewAgentController(s)
	c.Step(TypedDelegateInput(job(1), "fix the upload test in focus", ""), T0)
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	running(t, c)
	call := McpCall{Kind: CallReportResult, Outcome: OutcomeDone, Summary: "Fixed the flaky test", Evidence: "go test passed"}
	mcp(t, c, workerName, call, T0)
	fx := c.Step(TickInput(), T0+UpdateQuietMs)
	got := chats(fx)
	if len(got) != 1 || got[0].Job != job(1) || got[0].Text != "The upload worker in focus says it's done: Fixed the flaky test." {
		t.Fatalf("chat lines %v (%s)", got, fmtEffects(fx))
	}
	noUsualRoute(t, fx)
}
