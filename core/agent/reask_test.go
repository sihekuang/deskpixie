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
	if n := effectsOfKind(fx, EfInject); len(n) == 0 || !strings.HasPrefix(n[len(n)-1].Notice.Text, "Updates:\n- ") || len(n[len(n)-1].Notice.Sessions) != 1 {
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

// #443, #495: with agent.listenAfterUpdates "never" the mic stays off for a question. A later
// conversation opened only to speak doesn't say it again, however many there are; the next one
// you can answer in does, once, and your answer goes to it.
func TestAQuestionSaidWithTheMicOffIsSaidAgainOnlyOnceYouCanAnswer(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation, s.SpeakUpdatesOnly = true, true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	if fx := askSession(c, storiesAsk, T0+1); opens(fx) != 1 {
		t.Fatalf("the question opened no conversation: %+v", fx)
	}
	if fx := c.Step(voiceOpenedToSpeak(), T0+800); !asksAgain(fx) {
		t.Fatalf("not said: %q", injectsOf(fx))
	}
	c.Step(voiceClosed(), T0+5_000) // said with the mic off: nobody could answer

	// Later updates open conversations only to speak: the question doesn't go with them.
	for i := Millis(1); i <= 5; i++ {
		at := T0 + i*70_000
		var fx []Effect
		c.deliverOpening(NewLine(LineReply, "another update"), at, &fx)
		if opens(fx) != 1 {
			t.Fatalf("update %d opened no conversation: %+v", i, fx)
		}
		said := c.Step(voiceOpenedToSpeak(), at+800)
		if asksAgain(said) || !strings.Contains(strings.Join(injectsOf(said), "\n"), "another update") {
			t.Fatalf("update %d: %q", i, injectsOf(said))
		}
		c.Step(voiceClosed(), at+5_000)
	}

	// You press the hotkey: this one you can answer in, and it says the question.
	if fx := c.Step(voiceOpened(), T0+500_000); !asksAgain(fx) {
		t.Fatalf("not said where you can answer: %q", injectsOf(fx))
	}
	if a := answers(c.Step(AnswerInput("", "create both, assign to me"), T0+501_000)); len(a) != 1 || a[0].Answer != "2" {
		t.Fatalf("%+v", a)
	}
}

// Answered while only conversations opened to speak came, it isn't said in the next one (#495).
func TestAQuestionSaidWithTheMicOffThenAnsweredIsNotSaidAgain(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation, s.SpeakUpdatesOnly = true, true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	askSession(c, storiesAsk, T0+1)
	c.Step(voiceOpenedToSpeak(), T0+800)
	c.Step(voiceClosed(), T0+5_000)
	var fx []Effect
	c.deliverOpening(NewLine(LineReply, "another update"), T0+70_000, &fx)
	if opens(fx) != 1 {
		t.Fatalf("the update opened no conversation: %+v", fx)
	}
	c.Step(voiceOpenedToSpeak(), T0+70_800)
	c.Step(voiceClosed(), T0+75_000)
	c.Step(ChooseInput("hawk", 1), T0+80_000)
	if fx := c.Step(voiceOpened(), T0+90_000); asksAgain(fx) {
		t.Fatalf("said again: %q", injectsOf(fx))
	}
}

// Once said where you could answer, it is said no more, as without the setting.
func TestAQuestionSaidAgainWithTheMicOnIsSaidOnlyOnce(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation, s.SpeakUpdatesOnly = true, true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	askSession(c, storiesAsk, T0+1)
	c.Step(voiceOpenedToSpeak(), T0+800)
	c.Step(voiceClosed(), T0+5_000)
	if fx := c.Step(voiceOpened(), T0+20_000); !asksAgain(fx) { // the hotkey
		t.Fatalf("not said again: %q", injectsOf(fx))
	}
	c.Step(voiceClosed(), T0+30_000)
	if fx := c.Step(voiceOpened(), T0+60_000); asksAgain(fx) {
		t.Fatalf("said a third time: %q", injectsOf(fx))
	}
}

// #445: with agent.listenAfterUpdates "questions", a question said where Pixie opened only to speak
// turned the mic on, so you could answer there: it is said again once, as without the setting.
func TestAQuestionSaidWhereYouCouldAnswerIsSaidAgainOnlyOnce(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation, s.SpeakUpdatesOnly, s.ListenAfterQuestion = true, true, true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	askSession(c, storiesAsk, T0+1)
	c.Step(voiceOpenedToSpeak(), T0+800)
	c.Step(voiceClosed(), T0+5_000) // you didn't answer
	delegate(c, 1, "summarise the issues")
	stop(c, doneStop, T0+70_000)
	if fx := c.Step(voiceOpenedToSpeak(), T0+70_800); !asksAgain(fx) { // opened to speak; it listens after it
		t.Fatalf("not said again: %q", injectsOf(fx))
	}
	c.Step(voiceClosed(), T0+75_000)
	if fx := c.Step(voiceOpened(), T0+90_000); asksAgain(fx) {
		t.Fatalf("said a third time: %q", injectsOf(fx))
	}
}

// #495: the hotkey while Pixie says another update turns the mic on: the question is said there,
// once, and your answer goes to it.
func TestAQuestionIsSaidAgainWhenYouTurnTheMicOnDuringAnUpdate(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation, s.SpeakUpdatesOnly = true, true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	askSession(c, storiesAsk, T0+1)
	c.Step(voiceOpenedToSpeak(), T0+800)
	if fx := c.Step(voiceListening(), T0+900); asksAgain(fx) { // it is being said already
		t.Fatalf("said twice in one conversation: %q", injectsOf(fx))
	}
	c.Step(voiceClosed(), T0+5_000)
	var fx []Effect
	c.deliverOpening(NewLine(LineReply, "another update"), T0+70_000, &fx)
	if fx := c.Step(voiceOpenedToSpeak(), T0+70_800); asksAgain(fx) {
		t.Fatalf("said with the mic off: %q", injectsOf(fx))
	}
	if fx := c.Step(voiceListening(), T0+72_000); !asksAgain(fx) {
		t.Fatalf("not said once you could answer: %q", injectsOf(fx))
	}
	c.Step(voiceClosed(), T0+80_000)
	if fx := c.Step(voiceOpened(), T0+90_000); asksAgain(fx) {
		t.Fatalf("said a third time: %q", injectsOf(fx))
	}
	if a := answers(c.Step(AnswerInput("", "create both, assign to me"), T0+91_000)); len(a) != 1 || a[0].Answer != "2" {
		t.Fatalf("%+v", a)
	}
}

// #495: the hotkey while the conversation asked for only to speak connects: it opens with the mic
// on, so the question is said there.
func TestAQuestionIsSaidAgainWhenTheHotkeyBeatsTheOpenToSpeak(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation, s.SpeakUpdatesOnly = true, true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	askSession(c, storiesAsk, T0+1)
	c.Step(voiceOpenedToSpeak(), T0+800)
	c.Step(voiceClosed(), T0+5_000)
	var fx []Effect
	c.deliverOpening(NewLine(LineReply, "another update"), T0+70_000, &fx)
	if opens(fx) != 1 {
		t.Fatalf("the update opened no conversation: %+v", fx)
	}
	if fx := c.Step(voiceOpened(), T0+70_800); !asksAgain(fx) {
		t.Fatalf("not said where you can answer: %q", injectsOf(fx))
	}
}

// #495 as it happened: a session's turn that ends asking, with updates after it.
func TestASessionsQuestionSaidWithTheMicOffIsNotRepeated(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation, s.SpeakUpdatesOnly = true, true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	asks := func(fx []Effect) bool { return strings.Contains(strings.Join(injectsOf(fx), "\n"), "update the docs") }
	c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: sessionName, Turn: &SessionTurn{Label: "api",
		Reply: "Should I also update the docs?", FromPixie: true}}), T0+1)
	if fx := c.Step(voiceOpenedToSpeak(), T0+800); !asks(fx) {
		t.Fatalf("not said: %q", injectsOf(fx))
	}
	c.Step(voiceClosed(), T0+5_000)
	for i := Millis(1); i <= 3; i++ {
		var fx []Effect
		c.deliverOpening(NewLine(LineReply, "another update"), T0+i*70_000, &fx)
		if fx := c.Step(voiceOpenedToSpeak(), T0+i*70_000+800); asks(fx) {
			t.Fatalf("update %d: said again: %q", i, injectsOf(fx))
		}
		c.Step(voiceClosed(), T0+i*70_000+5_000)
	}
	if fx := c.Step(voiceOpened(), T0+400_000); !asks(fx) {
		t.Fatalf("not said where you can answer: %q", injectsOf(fx))
	}
}

// #496: said again but the conversation closed before Pixie said it (a second press, the mic
// failing): it isn't used up, whether the notice comes back with the close or after it.
func TestAQuestionSaidAgainButLeftUnsaidIsStillDue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		open  []Input
		close func(Notice) []Input
	}{
		{"mic turned on during an update", []Input{voiceOpenedToSpeak(), voiceListening()},
			func(n Notice) []Input { return []Input{voiceClosed(n)} }},
		{"a usual conversation", []Input{voiceOpened()},
			func(n Notice) []Input { return []Input{voiceClosed(n)} }},
		{"back after the close", []Input{voiceOpened()},
			func(n Notice) []Input { return []Input{voiceClosed(), voiceClosed(n)} }},
		{"bounced after the close", []Input{voiceOpened()},
			func(n Notice) []Input { return []Input{voiceClosed(), {Kind: InInjectBounced, Notice: n}} }},
	} {
		c := ctlWith(func(s *Settings) { s.OpenConversation, s.SpeakUpdatesOnly = true, true })
		c.Step(CoordinatorUpInput(PaneIdle), T0)
		askSession(c, storiesAsk, T0+1)
		c.Step(voiceOpenedToSpeak(), T0+800)
		c.Step(voiceClosed(), T0+5_000)
		var unsaid Notice
		for _, in := range tc.open {
			for _, e := range effectsOfKind(c.Step(in, T0+70_000), EfInject) {
				if strings.Contains(e.Notice.Text, "Create these 2 stories?") {
					unsaid = e.Notice
				}
			}
		}
		if unsaid.Text == "" {
			t.Fatalf("%s: not said again", tc.name)
		}
		for _, in := range tc.close(unsaid) {
			c.Step(in, T0+75_000)
		}
		n := strings.Count(strings.Join(injectsOf(c.Step(voiceOpened(), T0+90_000)), "\n"), "Create these 2 stories?")
		if n != 1 {
			t.Errorf("%s: said %d times in the next", tc.name, n)
		}
		c.Step(voiceClosed(), T0+95_000)
		if fx := c.Step(voiceOpened(), T0+120_000); asksAgain(fx) {
			t.Errorf("%s: said once more after that: %q", tc.name, injectsOf(fx))
		}
	}
}

// #497: its first notice comes back unsaid after the conversation closed: the outbox's replay
// says it, and it isn't also said again.
func TestAQuestionThatBouncedAfterTheCloseIsSaidOnceInTheNext(t *testing.T) {
	c := ctl()
	c.Step(voiceOpened(), T0)
	var said Notice
	for _, e := range effectsOfKind(askSession(c, storiesAsk, T0+1), EfInject) {
		said = e.Notice
	}
	c.Step(voiceClosed(), T0+2)
	c.Step(voiceClosed(said), T0+3)
	if n := strings.Count(strings.Join(injectsOf(c.Step(voiceOpened(), T0+20_000)), "\n"), "Create these 2 stories?"); n != 1 {
		t.Fatalf("said %d times", n)
	}
}

// With "questions" a conversation opened to speak listens after a question: turning its mic on
// doesn't say the question a second time there (#495).
func TestListeningAfterAQuestionDoesNotSayItAgain(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation, s.SpeakUpdatesOnly, s.ListenAfterQuestion = true, true, true })
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	askSession(c, storiesAsk, T0+1)
	c.Step(voiceOpenedToSpeak(), T0+800)
	c.Step(voiceClosed(), T0+5_000)
	if fx := c.Step(voiceOpenedToSpeak(), T0+70_800); !asksAgain(fx) {
		t.Fatalf("not said again: %q", injectsOf(fx))
	}
	if fx := c.Step(voiceListening(), T0+70_900); asksAgain(fx) {
		t.Fatalf("said twice: %q", injectsOf(fx))
	}
}
