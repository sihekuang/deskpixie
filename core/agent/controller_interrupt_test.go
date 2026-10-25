package agent

import "testing"

// #309: the stop button by the working coordinator.

func TestInterruptingTheTurnCancelsTheTypedJobItCarries(t *testing.T) {
	c := ctl()
	c.Step(TypedDelegateInput(job(1), "a", ""), T0)
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	c.Step(coordStatus(PaneWorking), T0)
	fx := visible(c.Step(InterruptTurnInput(), T0))
	wantEffects(t, fx, eInterrupt)
	wantJobStatus(t, c, 1, JobCancelled)
	// As Stop in the box: the spoken request in the same turn goes out again.
	fx = visible(c.Step(coordStatus(PaneIdle), T0))
	wantEffects(t, fx, ePrompt("The user cancelled job_000000000001. Stop work on it and confirm.\n[ref job_000000000001]\n\nNew request from the user: b\n[ref job_000000000002]"))
	wantJobStatus(t, c, 2, JobWorking)
}

func TestInterruptingATurnWithNoTypedJobEndsItAndKeepsItsJobs(t *testing.T) {
	c := workingOnJob1()
	c.Step(coordStatus(PaneWorking), T0)
	delegate(c, 2, "b") // queued behind the turn
	fx := visible(c.Step(InterruptTurnInput(), T0))
	wantEffects(t, fx, eInterrupt)
	wantJobStatus(t, c, 1, JobWorking)
	// The interrupt fires no Stop: the Idle it brings sends what waits, at once.
	fx = visible(c.Step(coordStatus(PaneIdle), T0))
	wantEffects(t, fx, ePrompt("New request from the user: b\n[ref job_000000000002]"))
}

func TestInterruptingWithNoCoordinatorUpDoesNothing(t *testing.T) {
	c := ctl()
	if fx := visible(c.Step(InterruptTurnInput(), T0)); len(fx) != 0 {
		t.Fatal(fmtEffects(fx))
	}
}
