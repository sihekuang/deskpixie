package agent

import (
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

// #382: 2026-10-27 14:43, Pixie read out a session's menu and the conversation dropped two
// seconds later ("audio device changed"); the next one didn't know it, so the answer went out
// as a new request. A question said in a conversation that closed before you said anything is
// said again, once, in the next, while it still waits.

var storiesAsk = SessionQuestion{ID: "q1", Label: "jira", App: "dev.warp.Warp-Stable", Kind: "menu",
	Text:    "Create these 2 stories?",
	Options: []string{"Create both, unassigned", "Create both, assign to me", "Change something first"}}

// injectsOf: the texts of the notices fx injects.
func injectsOf(fx []Effect) []string {
	var out []string
	for _, e := range effectsOfKind(fx, EfInject) {
		out = append(out, e.Notice.Text)
	}
	return out
}

func asksAgain(fx []Effect) bool {
	for _, s := range injectsOf(fx) {
		if strings.Contains(s, "Coordinator question: jira is asking: Create these 2 stories?") {
			return true
		}
	}
	return false
}

func TestAMenuSaidBeforeTheConversationDroppedIsSaidAgainOnce(t *testing.T) {
	c := ctl()
	c.Step(voiceOpened(), T0)
	if fx := askSession(c, storiesAsk, T0+1); !asksAgain(fx) {
		t.Fatalf("not said at first: %q", injectsOf(fx))
	}
	c.Step(voiceClosed(), T0+2) // dropped before you answered
	fx := c.Step(voiceOpened(), T0+20_000)
	if !asksAgain(fx) {
		t.Fatalf("not said again: %q", injectsOf(fx))
	}
	if n := effectsOfKind(fx, EfInject); len(n) == 0 || !strings.HasPrefix(n[len(n)-1].Notice.Text, "Coordinator replies while you were away:\n- ") || len(n[len(n)-1].Notice.Sessions) != 1 {
		t.Fatalf("%+v", n)
	}
	// Only once: quiet again, it is left to the badge.
	c.Step(voiceClosed(), T0+30_000)
	if fx := c.Step(voiceOpened(), T0+60_000); asksAgain(fx) {
		t.Fatalf("said a third time: %q", injectsOf(fx))
	}
	// Your answer still goes to it, as answer_job without a job id.
	if a := answers(c.Step(AnswerInput("", "create both, assign to me"), T0+61_000)); len(a) != 1 || a[0].Answer != "2" {
		t.Fatalf("%+v", a)
	}
}

func TestAQuestionIsNotSaidAgainWhen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		between func(c *AgentController)
	}{
		{"you said something after it", func(c *AgentController) {
			c.Step(VoiceInput(VoiceSignal{Kind: voice.SigUserSaid, Text: "hang on"}), T0+2)
		}},
		{"you tapped an option", func(c *AgentController) { c.Step(ChooseInput("hawk", 1), T0+2) }},
		{"it went away", func(c *AgentController) { sessionGone(c, "q1", T0+2) }},
		{"another question came after", func(c *AgentController) {
			c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: "web-claude", Turn: &SessionTurn{Label: "web",
				Reply: "Which port should it use?", FromPixie: true}}), T0+2)
		}},
	} {
		c := ctl()
		c.Step(voiceOpened(), T0)
		askSession(c, storiesAsk, T0+1)
		tc.between(c)
		c.Step(voiceClosed(), T0+3)
		if fx := c.Step(voiceOpened(), T0+20_000); asksAgain(fx) {
			t.Errorf("%s: said again: %q", tc.name, injectsOf(fx))
		}
	}
}

// Never said (no conversation open, so it waits in the outbox): the next conversation's replay
// says it, once; it isn't said again on top of that.
func TestAQuestionThatWaitedInTheOutboxIsSaidOnce(t *testing.T) {
	c := ctl()
	askSession(c, storiesAsk, T0)
	fx := c.Step(voiceOpened(), T0+10_000)
	n := 0
	for _, s := range injectsOf(fx) {
		n += strings.Count(s, "Create these 2 stories?")
	}
	if n != 1 {
		t.Fatalf("said %d times: %q", n, injectsOf(fx))
	}
}

// A session's turn that ends asking is said again too, while it hasn't moved on.
func TestASessionsQuestionIsSaidAgain(t *testing.T) {
	c := ctl()
	c.Step(voiceOpened(), T0)
	c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: sessionName, Turn: &SessionTurn{Label: "api",
		Reply: "Should I also update the docs?", FromPixie: true}}), T0+1)
	c.Step(voiceClosed(), T0+2)
	fx := c.Step(voiceOpened(), T0+20_000)
	found := false
	for _, s := range injectsOf(fx) {
		found = found || strings.Contains(s, "update the docs")
	}
	if !found {
		t.Fatalf("%q", injectsOf(fx))
	}
}

// Review of #385: answered in its terminal meanwhile (the session went to work), it isn't
// said again, and what you say next isn't typed into the busy session as its answer.
func TestASessionsQuestionAnsweredInItsTerminalIsNotSaidAgain(t *testing.T) {
	c := ctl()
	c.Step(voiceOpened(), T0)
	c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: sessionName, Turn: &SessionTurn{Label: "api",
		Reply: "Should I also update the docs?", FromPixie: true}}), T0+1)
	c.Step(PaneInput(PaneEvent{Kind: PaneEventStatus, Agent: sessionName, Status: PaneIdle}), T0+1)
	c.Step(voiceClosed(), T0+2)
	c.Step(PaneInput(PaneEvent{Kind: PaneEventStatus, Agent: sessionName, Status: PaneWorking}), T0+5_000)
	for _, s := range injectsOf(c.Step(voiceOpened(), T0+20_000)) {
		if strings.Contains(s, "update the docs") {
			t.Fatalf("said again: %q", s)
		}
	}
}

// Dropped before Pixie said it (the notice comes back unsaid): the outbox's replay says it in
// the next conversation, once; it isn't also said again.
func TestAQuestionThatBouncedIsSaidOnceInTheNext(t *testing.T) {
	c := ctl()
	c.Step(voiceOpened(), T0)
	fx := askSession(c, storiesAsk, T0+1)
	var said Notice
	for _, e := range effectsOfKind(fx, EfInject) {
		said = e.Notice
	}
	c.Step(voiceClosed(said), T0+2)
	n := 0
	for _, s := range injectsOf(c.Step(voiceOpened(), T0+20_000)) {
		n += strings.Count(s, "Create these 2 stories?")
	}
	if n != 1 {
		t.Fatalf("said %d times", n)
	}
}
