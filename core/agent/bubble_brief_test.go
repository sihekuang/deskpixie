package agent

import "testing"

// #197: the bubble shows a short form of the lines Pixie builds: who on a title line, the news
// below. What is said doesn't change.

func TestASessionTurnIsBriefInTheBubble(t *testing.T) {
	items, _ := lastUpdates(t, sessionTurn(turnCtl(nil), longTurn, T0))
	if len(items) != 1 || items[0].Title != "deskpixie: fix login redirect" ||
		items[0].Brief != "The redirect loop is fixed and all 412 tests pass." || items[0].Text != longTurnLine || items[0].Asking {
		t.Fatalf("%+v", items)
	}
	asks := longTurn
	asks.Reply = "Fixed the loop.\n\nShould I also update the docs?"
	items, _ = lastUpdates(t, sessionTurn(turnCtl(nil), asks, T0))
	if len(items) != 1 || items[0].Brief != "Should I also update the docs?" || !items[0].Asking {
		t.Fatalf("%+v", items)
	}
}

func TestAnOlderPixIsBriefInTheBubble(t *testing.T) {
	ev := PaneEvent{Kind: PaneEventOutdated, Agent: "api-claude", Outdated: &SessionOutdated{Label: "landing", Tool: "codex"}}
	items, _ := lastUpdates(t, ctl().Step(PaneInput(ev), T0))
	if len(items) != 1 || items[0].Title != "landing" || items[0].Brief != "Older pix: restart with pix codex resume." {
		t.Fatalf("%+v", items)
	}
}

func TestAHandOffIsBriefInTheBubble(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, c.Step(SessionSentInput("repo-claude", landing), T0))
	if len(items) != 1 || items[0].Title != landing || items[0].Brief != "Message passed on." {
		t.Fatalf("%+v", items)
	}
	c = workingOnJob1()
	items, _ = lastUpdates(t, c.Step(SessionWaitingInput("repo-claude", landing, "typing"), T0))
	if len(items) != 1 || items[0].Brief != "Message held: you're typing there." {
		t.Fatalf("%+v", items)
	}
}

func TestACoordinatorQuestionsBriefLeavesOutHowToAnswerIt(t *testing.T) {
	for text, want := range map[string]string{
		"The coordinator wants to run ls. Say confirm hawk.":                     "The coordinator wants to run ls.",
		"Run the tests? Say confirm hawk, or choose in the bubble.":              "Run the tests?",
		"The coordinator wants to edit main.go. Allow or deny it in the bubble.": "The coordinator wants to edit main.go.",
		"Which one?": "",
	} {
		if got := QuestionBrief(text); got != want {
			t.Errorf("%q → %q, want %q", text, got, want)
		}
	}
}

// A session's question: its label is the title, the brief its question as asked, even when the
// question itself says "Pick".
func TestASessionQuestionsBriefIsItsQuestion(t *testing.T) {
	menu := SessionQuestion{ID: "q9", Label: "api", Kind: "menu", Text: "Pick a database to use.", Options: []string{"Postgres", "MySQL"}}
	items := bubbleOf(askSession(ctl(), menu, T0))
	if len(items) != 1 || items[0].Title != "api" || items[0].Brief != "Pick a database to use." {
		t.Fatalf("%+v", items)
	}
	items = bubbleOf(askSession(ctl(), pushMenu, T0))
	if len(items) != 1 || items[0].Brief != "Push main to origin?" {
		t.Fatalf("%+v", items)
	}
	codex := SessionQuestion{ID: "q8", Label: "api", Kind: "permission", Text: "is this safe to run?", Prompt: "Run it?", Options: []string{"Yes", "No"}}
	items = bubbleOf(askSession(ctl(), codex, T0))
	if len(items) != 1 || items[0].Brief != "Is this safe to run?" {
		t.Fatalf("%+v", items)
	}
}

// #194 review: told in a conversation, an older pix still counts as told for the next run.
func TestAnOlderPixSaidInAConversationIsNotToldAgainAfterARestart(t *testing.T) {
	ev := PaneEvent{Kind: PaneEventOutdated, Agent: "api-claude", Outdated: &SessionOutdated{Label: "landing", Tool: "claude"}}
	first := ctl()
	openVoice(first)
	wantContains(t, first.Step(PaneInput(ev), T0), eInject("Session update: landing runs an older pix than the app: restart it with pix claude --resume to update it."))
	c := ctl()
	c.outbox = RestoreOutbox(first.outbox.Lines(), T0+60_000)
	if fx := c.Step(PaneInput(ev), T0+60_000); anyKind(fx, EfNotify, EfInject) {
		t.Fatal(fmtEffects(fx))
	}
}

// Lines alike about different jobs are both kept: each one's click goes somewhere else.
func TestRepeatsAreOnlyAboutTheSameTarget(t *testing.T) {
	o := NewOutbox()
	o.Push(NewJobLine(LineReply, "Passed on to your session.", job(1)), T0)
	if recs := o.Repeats(NewJobLine(LineReply, "Passed on to your session.", job(2))); len(recs) != 0 {
		t.Fatal(recs)
	}
	if recs := o.Repeats(NewJobLine(LineReply, "Passed on to your session.", job(1))); len(recs) != 1 {
		t.Fatal(recs)
	}
}

func TestTheShortFormIsKeptInTheOutbox(t *testing.T) {
	rec := NewOutbox().Push(Line{Kind: LineSession, Text: "long", Title: "api", Brief: "Done.", Asking: true}, T0)
	var back OutboxLine
	if err := back.UnmarshalJSON([]byte(rec)); err != nil || back.Title != "api" || back.Brief != "Done." || !back.Asking {
		t.Fatalf("%s → %+v %v", rec, back, err)
	}
}
