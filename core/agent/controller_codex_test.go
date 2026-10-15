package agent

import "testing"

// #87: the tray names the coordinator's agent when it can't start, and says why a start screen
// refused it.

func TestACodexStartFailureNamesCodex(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.CoordinatorKind = Codex })
	delegate(c, 1, "a")
	fx := visible(c.Step(CoordinatorFailedInput(HostError{Kind: HostTimeout, Detail: "codex"}), T0))
	wantEffects(t, fx[:1], eStatus("Coordinator: codex did not start"))
	wantJobStatus(t, c, 1, JobFailed)
	wantDisabled(t, c, "")
}

func TestARefusedStartSaysTheReasonAndFailsTheWaitingJobs(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.CoordinatorKind = Codex })
	delegate(c, 1, "a")
	fx := visible(c.Step(CoordinatorFailedInput(HostError{Kind: HostRefused, Detail: CodexHooksReviewText}), T0))
	wantEffects(t, fx[:1], eStatus("Coordinator: "+CodexHooksReviewText))
	wantJobStatus(t, c, 1, JobFailed)
	// Not a sticky disable: the next request tries again (after the user dealt with it).
	wantDisabled(t, c, "")
	wantEffects(t, visible(delegate(c, 2, "b")), eHold(true), eStart)
}
