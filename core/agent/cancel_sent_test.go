package agent

import "testing"

// #332: "stop that" just after the coordinator typed a request into one of your sessions: the
// job is closed, so a cancel means that session's turn.

// sentAndClosed: job 1 was typed into repo-claude and closed.
func sentAndClosed(t *testing.T) *AgentController {
	t.Helper()
	c := typedOnJob1()
	c.Step(SessionSentInput("repo-claude", landing), T0+5)
	stop(c, `<spoken ref="job_000000000001" status="done">Passed it on.</spoken>`, T0+10)
	wantJobStatus(t, c, 1, JobDone)
	return c
}

func TestACancelOfAJobTypedIntoASessionMeansThatSession(t *testing.T) {
	c := sentAndClosed(t)
	for _, raw := range []string{"job_000000000001", " job_000000000001 ", ""} {
		if name, ok := c.SentTo(raw); !ok || name != "repo-claude" {
			t.Errorf("%q: %q %v", raw, name, ok)
		}
	}
	for _, raw := range []string{"job_000000000002", "job_banana"} {
		if name, ok := c.SentTo(raw); ok {
			t.Errorf("%q is another job: %q", raw, name)
		}
	}
}

func TestOnceItsTurnEndedThereIsNothingToStop(t *testing.T) {
	c := sentAndClosed(t)
	c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: "repo-claude", Turn: &SessionTurn{Label: "repo",
		Reply: "Done.", Duration: 45_000, FromPixie: true}}), T0+20)
	if name, ok := c.SentTo(""); ok {
		t.Fatalf("its next turn is someone else's: %q", name)
	}
}

func TestAMessageHeldForABusySessionIsntItsTurn(t *testing.T) {
	c := typedOnJob1()
	c.Step(SessionSentInput("repo-claude", landing), T0+4) // an earlier one went in
	c.Step(SessionWaitingInput("repo-claude", landing, "working"), T0+5)
	stop(c, `<spoken ref="job_000000000001" status="done">It's busy; it gets it next.</spoken>`, T0+10)
	if name, ok := c.SentTo(""); ok {
		t.Fatalf("the turn it's on isn't Pixie's: %q", name)
	}
}

func TestAnOpenJobIsCancelledAsBefore(t *testing.T) {
	c := typedOnJob1()
	c.Step(SessionSentInput("repo-claude", landing), T0+5)
	if name, ok := c.SentTo("job_000000000001"); ok {
		t.Fatalf("still open: the cancel stops it, not the session: %q", name)
	}
}

// #332 review: a stop (or Esc in its terminal) ends the turn with no turn reported: seen idle
// after working, it's over, and the user's own next turn is never stopped for it.
func TestAStoppedTurnIsNoLongerPixies(t *testing.T) {
	c := sentAndClosed(t)
	c.Step(paneStatus("repo-claude", PaneWorking), T0+11)
	c.Step(paneStatus("repo-claude", PaneIdle), T0+12)
	c.Step(paneStatus("repo-claude", PaneWorking), T0+13) // the user's own request
	if name, ok := c.SentTo(""); ok {
		t.Fatalf("%q", name)
	}
}

func TestIdleJustAfterTheRequestWentInIsntTheEnd(t *testing.T) {
	c := sentAndClosed(t)
	c.Step(paneStatus("repo-claude", PaneIdle), T0+6) // before it started on it
	if _, ok := c.SentTo(""); !ok {
		t.Fatal("not started yet is still its turn")
	}
	c.Step(paneStatus("repo-claude", PaneIdle), T0+5+SentIdleGraceMs)
	if _, ok := c.SentTo(""); ok {
		t.Fatal("idle since: over")
	}
}

func TestAClosedSessionsNameIsntStoppedLater(t *testing.T) {
	c := sentAndClosed(t)
	c.Step(PaneInput(PaneEvent{Kind: PaneEventExited, Agent: "repo-claude"}), T0+11)
	if name, ok := c.SentTo(""); ok {
		t.Fatalf("a new session may take its name: %q", name)
	}
}

func TestWithJobsOpenACancelWithoutAnIDStopsNoSession(t *testing.T) {
	c := sentAndClosed(t)
	c.Step(DelegateInput(job(2), "another", ""), T0+11)
	if name, ok := c.SentTo(""); ok {
		t.Fatalf("the open job is the one to cancel, or ask which: %q", name)
	}
	if name, ok := c.SentTo("job_000000000001"); !ok || name != "repo-claude" {
		t.Fatal("named, it still means its session", name, ok)
	}
}

// #332 review: its turn was seen working before the hand-off was reported (Claude's prompt hook,
// pix's 300 ms settle): an idle within the grace is still its stop.
func TestAStopRightAfterAHandOffReportedLateEndsIt(t *testing.T) {
	c := typedOnJob1()
	c.Step(paneStatus("repo-claude", PaneWorking), T0+4)
	c.Step(SessionSentInput("repo-claude", landing), T0+5)
	stop(c, `<spoken ref="job_000000000001" status="done">Passed it on.</spoken>`, T0+10)
	c.Step(paneStatus("repo-claude", PaneIdle), T0+2000) // Esc in its terminal
	c.Step(paneStatus("repo-claude", PaneWorking), T0+60_000)
	if name, ok := c.SentTo(""); ok {
		t.Fatalf("the user's own turn: %q", name)
	}
}

func TestAStopOfItLeavesNothingToStop(t *testing.T) {
	c := sentAndClosed(t)
	c.SentStopped("repo-claude")
	if name, ok := c.SentTo(""); ok {
		t.Fatalf("%q", name)
	}
}
