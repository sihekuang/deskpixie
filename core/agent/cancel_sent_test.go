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
