package agent

import (
	"reflect"
	"testing"
)

// Ported from crates/agent-core/src/inbox.rs (12 tests).

func job(n uint64) JobID { return JobIDFromBits(n) }

// ready: would a queued message go out now?
func ready(i *Inbox) bool {
	probe := i.Clone()
	probe.Push(MessageForJob(job(99), "probe"))
	_, ok := probe.TakePrompt()
	return ok
}

func takeRefs(t *testing.T, i *Inbox) []JobID {
	t.Helper()
	p, ok := i.TakePrompt()
	if !ok {
		t.Fatal("no prompt")
	}
	return p.Refs
}

func wantRefs(t *testing.T, got []JobID, want ...JobID) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestAFinishedTurnAloneDoesNotMakeItReady(t *testing.T) {
	i := NewInbox()
	i.OnStatus(PaneIdle)
	i.Push(MessageForJob(job(1), "a"))
	i.TakePrompt()
	i.FinishTurn()
	if ready(i) {
		t.Fatal("the controller decides when (an Idle, or a short settle)")
	}
	i.MarkReady()
	if !ready(i) {
		t.Fatal("ready")
	}
}

func TestAFailedPromptGoesBackInFrontAndDroppedJobsLeaveFlight(t *testing.T) {
	i := NewInbox()
	i.OnStatus(PaneIdle)
	i.Push(MessageForJob(job(1), "a"))
	i.Push(MessageForJob(job(2), "b"))
	i.TakePrompt()
	i.Push(MessageForJob(job(3), "c"))
	i.RequeueInFlight()
	if i.Carries(job(1)) {
		t.Fatal("carries")
	}
	i.MarkReady()
	wantRefs(t, takeRefs(t, i), job(1), job(2), job(3))
	i.DropInFlight(job(2))
	wantRefs(t, i.InFlightRefs(), job(1), job(3))
}

func TestNothingIsSentUntilTheCoordinatorIsIdle(t *testing.T) {
	i := NewInbox()
	i.Push(MessageForJob(job(1), "New request from the user: a"))
	if _, ok := i.TakePrompt(); ok {
		t.Fatal("sent")
	}
	i.OnStatus(PaneWorking)
	if _, ok := i.TakePrompt(); ok {
		t.Fatal("sent while working")
	}
	i.OnStatus(PaneIdle)
	p, ok := i.TakePrompt()
	if !ok || p.Text != "New request from the user: a\n[ref job_000000000001]" {
		t.Fatalf("%q", p.Text)
	}
	wantRefs(t, p.Refs, job(1))
}

func TestQueuedMessagesGoAsOnePromptEachKeepingItsRef(t *testing.T) {
	i := NewInbox()
	i.Push(MessageForJob(job(1), "New request from the user: a"))
	i.Push(MessageForJob(job(2), "New request from the user: b"))
	i.OnStatus(PaneDone)
	p, _ := i.TakePrompt()
	if p.Text != "New request from the user: a\n[ref job_000000000001]\n\nNew request from the user: b\n[ref job_000000000002]" {
		t.Fatalf("%q", p.Text)
	}
	wantRefs(t, p.Refs, job(1), job(2))
}

func TestAfterSendingItWaitsForTheNextIdle(t *testing.T) {
	i := NewInbox()
	i.OnStatus(PaneIdle)
	i.Push(MessageForJob(job(1), "a"))
	if _, ok := i.TakePrompt(); !ok {
		t.Fatal("first")
	}
	i.Push(MessageForJob(job(2), "b"))
	if _, ok := i.TakePrompt(); ok {
		t.Fatal("busy with the first prompt")
	}
	i.OnStatus(PaneWorking)
	i.OnStatus(PaneIdle)
	wantRefs(t, takeRefs(t, i), job(2))
}

func TestInFlightRefsAreRememberedUntilTheTurnEnds(t *testing.T) {
	i := NewInbox()
	i.OnStatus(PaneIdle)
	i.Push(MessageForJob(job(1), "a"))
	i.TakePrompt()
	if !i.Carries(job(1)) {
		t.Fatal("carries")
	}
	wantRefs(t, i.InFlightRefs(), job(1))
	i.FinishTurn()
	if i.Carries(job(1)) {
		t.Fatal("still carries")
	}
}

func TestALostCoordinatorGetsItsInFlightWorkAgainFirst(t *testing.T) {
	i := NewInbox()
	i.OnStatus(PaneIdle)
	i.Push(MessageForJob(job(1), "a"))
	i.TakePrompt()
	i.Push(MessageForJob(job(2), "b"))
	i.CoordinatorLost()
	if ready(i) {
		t.Fatal("ready")
	}
	i.OnStatus(PaneIdle)
	wantRefs(t, takeRefs(t, i), job(1), job(2))
}

func TestASecondPromptWithoutAFinishedTurnKeepsBothBatchesInFlight(t *testing.T) {
	i := NewInbox()
	i.OnStatus(PaneIdle)
	i.Push(MessageForJob(job(1), "a"))
	i.TakePrompt()
	i.OnStatus(PaneWorking)
	i.OnStatus(PaneIdle)
	i.Push(MessageForJob(job(2), "b"))
	p, _ := i.TakePrompt()
	wantRefs(t, p.Refs, job(2))
	if !i.Carries(job(1)) || !i.Carries(job(2)) {
		t.Fatal("both in flight")
	}
	i.CoordinatorLost()
	i.OnStatus(PaneIdle)
	wantRefs(t, takeRefs(t, i), job(1), job(2))
	if _, ok := i.TakePrompt(); ok {
		t.Fatal("each exactly once")
	}
}

func TestAnInterruptedTurnSendsTheOtherJobsAgainBehindTheQueue(t *testing.T) {
	i := NewInbox()
	i.OnStatus(PaneIdle)
	i.Push(MessageForJob(job(1), "a"))
	i.Push(MessageForJob(job(2), "b"))
	i.TakePrompt()
	i.Push(MessageForJob(job(1), "cancel"))
	i.RequeueInFlightExcept(job(1))
	if !i.Carries(job(1)) || i.Carries(job(2)) {
		t.Fatal("carries")
	}
	i.OnStatus(PaneIdle)
	wantRefs(t, takeRefs(t, i), job(1), job(2))
}

func TestBlockedAndUnknownDoNotArmReady(t *testing.T) {
	i := NewInbox()
	i.OnStatus(PaneBlocked)
	if ready(i) {
		t.Fatal("blocked")
	}
	i.OnStatus(PaneUnknown)
	if ready(i) {
		t.Fatal("unknown")
	}
	i.OnStatus(PaneIdle)
	i.OnStatus(PaneUnknown)
	if !ready(i) {
		t.Fatal("unknown leaves it as it was")
	}
	i.OnStatus(PaneBlocked)
	if ready(i) {
		t.Fatal("blocked again")
	}
}

func TestALostCoordinatorAfterAFinishedTurnResendsNothing(t *testing.T) {
	i := NewInbox()
	i.OnStatus(PaneIdle)
	i.Push(MessageForJob(job(1), "a"))
	i.TakePrompt()
	i.FinishTurn()
	i.CoordinatorLost()
	i.OnStatus(PaneIdle)
	if _, ok := i.TakePrompt(); ok {
		t.Fatal("resent")
	}
}

func TestQueuedMessagesForAJobCanBeDropped(t *testing.T) {
	i := NewInbox()
	i.Push(MessageForJob(job(1), "a"))
	i.Push(MessageForJob(job(2), "b"))
	if !i.DropQueued(job(1)) {
		t.Fatal("drop")
	}
	if i.DropQueued(job(1)) {
		t.Fatal("drop twice")
	}
	i.OnStatus(PaneIdle)
	wantRefs(t, takeRefs(t, i), job(2))
}
