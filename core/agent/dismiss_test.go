package agent

import "testing"

// #388: a session's question with no buttons (its turn ended asking, often a closing "Want me
// to …?") stayed on the badge until that session took another turn. Dismiss takes it off; the
// session is left as it is, and what you say next isn't taken as its answer.

func sessionAsks(c *AgentController, reply string, now Millis) []Effect {
	return c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: sessionName, Turn: &SessionTurn{Label: "api",
		Reply: reply, FromPixie: true}}), now)
}

func TestASessionsQuestionCardCanBeDismissed(t *testing.T) {
	c := ctl()
	sessionAsks(c, "Want me to file that and open the PR?", T0)
	b := c.Updates()
	if len(b) != 1 || !b[0].Dismissible || b[0].Session != sessionName {
		t.Fatalf("%+v", b)
	}
	fx := c.Step(DismissInput(b[0].ID), T0+1)
	if len(c.Updates()) != 0 {
		t.Fatalf("still on the badge: %+v", c.Updates())
	}
	if b := bubbleOf(fx); b == nil || len(b) != 0 {
		t.Fatalf("the bubble wasn't told: %+v", b)
	}
	// "yes" now is no answer to it: nothing is typed into the session.
	fx = c.Step(AnswerInput("", "yes"), T0+2)
	if len(effectsOfKind(fx, EfTellSession)) != 0 {
		t.Fatal("the dismissed question was answered")
	}
	// Its next question comes up as usual.
	sessionAsks(c, "Should I also update the docs?", T0+3)
	if b := c.Updates(); len(b) != 1 || !b[0].Dismissible {
		t.Fatalf("%+v", b)
	}
}

// Cards with buttons, and the coordinator's own questions, wait on work: they aren't
// dismissible, and Dismiss with their id does nothing.
func TestOnlyASessionsOwnQuestionIsDismissible(t *testing.T) {
	for _, tc := range []struct {
		name string
		ask  func(c *AgentController)
	}{
		{"a permission", func(c *AgentController) { askSession(c, bashAsk, T0) }},
		{"a menu", func(c *AgentController) { askSession(c, dbChoice, T0) }},
		{"the coordinator's question", func(c *AgentController) {
			c.Step(DelegateInput(job(1), "deploy it", ""), T0)
			stop(c, `<spoken ref="job_000000000001" status="question">Which environment?</spoken>`, T0+1)
		}},
	} {
		c := ctl()
		tc.ask(c)
		b := c.Updates()
		if len(b) != 1 || b[0].Dismissible {
			t.Errorf("%s: %+v", tc.name, b)
			continue
		}
		c.Step(DismissInput(b[0].ID), T0+5)
		if len(c.Updates()) != 1 {
			t.Errorf("%s: dismissed", tc.name)
		}
	}
	// An id that isn't on the badge: nothing.
	c := ctl()
	sessionAsks(c, "Want me to push it?", T0)
	c.Step(DismissInput(999), T0+1)
	if len(c.Updates()) != 1 {
		t.Fatal("the wrong card went")
	}
}
