package agent

import (
	"strings"
	"testing"
)

// #330: a session's menu Pixie relayed last is answered by what you say back (answer_job, from
// voice or the chat window): the option it names, else your words typed in when the menu takes
// them. One that pushes, merges, publishes or deploys is still picked in the bubble.

var namesMenu = SessionQuestion{ID: "q7", Label: "deskpixie-landing", App: "dev.warp.Warp-Stable", Kind: "menu",
	Text: "Which should the new section do?", Options: []string{"Don't name them", "Name them in the bubble"}, Typed: true}

var dbChoice = SessionQuestion{ID: "q8", Label: "api", App: "dev.warp.Warp-Stable", Kind: "menu",
	Text: "Which database?", Options: []string{"Postgres", "SQLite", "MySQL"}}

func menuResult(fx []Effect) Effect {
	if r := effectsOfKind(fx, EfAnswerResult); len(r) == 1 {
		return r[0]
	}
	return Effect{}
}

func TestASpokenAnswerPicksTheMenusOption(t *testing.T) {
	for _, tc := range []struct{ said, want string }{
		{"SQLite", "2"},
		{"sqlite.", "2"},
		{"The second one.", "2"},
		{"Option 3", "3"},
		{"the last one", "3"},
		{"Let's go with Postgres, please.", "1"},
		{"Postgres sounds right for this", "1"},
	} {
		c := ctl()
		askSession(c, dbChoice, T0)
		fx := c.Step(AnswerInput("", tc.said), T0+1)
		a := answers(fx)
		if len(a) != 1 || a[0].Name != sessionName || a[0].Text != "q8" || a[0].Answer != tc.want {
			t.Errorf("%q: %+v", tc.said, a)
		}
		if r := menuResult(fx); r.Err != "" || r.Name != sessionName {
			t.Errorf("%q: the answer went to the session: %+v", tc.said, r)
		}
		if c.confirms.IsPendingToken("hawk") {
			t.Errorf("%q: the menu is answered", tc.said)
		}
	}
}

// The #330 menu, with what was said that day.
func TestTheLandingMenuIsAnsweredByVoice(t *testing.T) {
	for _, tc := range []struct{ said, want string }{
		{"Name them in the bubble.", "2"},
		{"Don't name them.", "1"},
		// Naming neither one alone: typed in, for the session to read.
		{"Just name them.", "text:Just name them."},
		{"Name them. Name them in a bubble.", "text:Name them. Name them in a bubble."},
	} {
		c := ctl()
		askSession(c, namesMenu, T0)
		a := answers(c.Step(AnswerInput("", tc.said), T0+1))
		if len(a) != 1 || a[0].Answer != tc.want {
			t.Errorf("%q: %+v", tc.said, a)
		}
	}
}

func TestAnAnswerThatPicksNoOptionIsAskedAgain(t *testing.T) {
	for _, said := range []string{"Not Postgres.", "Postgres or SQLite, I don't mind", "the fifth one", "MariaDB"} {
		c := ctl()
		askSession(c, dbChoice, T0)
		fx := c.Step(AnswerInput("", said), T0+1)
		if len(answers(fx)) != 0 {
			t.Errorf("%q: nothing is picked: %+v", said, answers(fx))
		}
		if r := menuResult(fx); !strings.Contains(r.Err, "Postgres, SQLite or MySQL") || !strings.Contains(r.Err, "in the bubble") {
			t.Errorf("%q: %+v", said, r)
		}
		// Still waiting, and still the question asked last.
		if !c.confirms.IsPendingToken("hawk") || len(answers(c.Step(AnswerInput("", "MySQL"), T0+2))) != 1 {
			t.Errorf("%q: the menu still waits for an answer", said)
		}
	}
}

func TestASaidNoIsTypedWhenTheMenuTakesWords(t *testing.T) {
	c := ctl()
	askSession(c, namesMenu, T0)
	if a := answers(c.Step(AnswerInput("", "not the bubble one"), T0+1)); len(a) != 1 || a[0].Answer != "text:not the bubble one" {
		t.Fatalf("%+v", a)
	}
}

func TestAMenuOptionThatPushesIsStillPickedInTheBubble(t *testing.T) {
	c := ctl()
	askSession(c, pushMenu, T0)
	fx := c.Step(AnswerInput("", "Push"), T0+1)
	if len(answers(fx)) != 0 {
		t.Fatalf("a spoken push isn't picked: %+v", answers(fx))
	}
	if r := menuResult(fx); r.Err != "That one asks it to push: pick it in the bubble." {
		t.Fatalf("%+v", r)
	}
	if a := answers(c.Step(AnswerInput("", "Don't push"), T0+2)); len(a) != 1 || a[0].Answer != "2" {
		t.Fatalf("the other option is said: %+v", a)
	}
}

func TestATypedAnswerThatPushesIsStillPickedInTheBubble(t *testing.T) {
	c := ctl()
	askSession(c, namesMenu, T0)
	fx := c.Step(AnswerInput("", "Name them and push it"), T0+1)
	if len(answers(fx)) != 0 || menuResult(fx).Err != "That one asks it to push: pick it in the bubble." {
		t.Fatalf("%+v", fx)
	}
}

// Only the question relayed last: a job's question after the menu takes the answer.
func TestAnAnswerGoesToTheQuestionAskedLast(t *testing.T) {
	c := ctl()
	askSession(c, dbChoice, T0)
	c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: "web-claude", Turn: &SessionTurn{Label: "web",
		Reply: "Done. Should I also update the docs?", Duration: 45_000, FromPixie: true}}), T0+1)
	fx := c.Step(AnswerInput("", "SQLite"), T0+2)
	if len(answers(fx)) != 0 || !c.confirms.IsPendingToken("hawk") {
		t.Fatalf("the menu wasn't asked last: %+v", answers(fx))
	}
}

func TestAMenuAnsweredInTheBubbleTakesNoSpokenAnswer(t *testing.T) {
	c := ctl()
	token := bubbleOf(askSession(c, dbChoice, T0))[0].Token
	c.Step(ChooseInput(token, 1), T0+1)
	if a := answers(c.Step(AnswerInput("", "SQLite"), T0+2)); len(a) != 0 {
		t.Fatalf("%+v", a)
	}
}

// #330 review: a menu answered by a tap leaves nothing to answer, even once its confirm word
// comes round again for a permission prompt: the menu is known by its request id.
func TestATappedMenusWordReusedLaterIsntAnsweredByVoice(t *testing.T) {
	c := ctl()
	token := bubbleOf(askSession(c, dbChoice, T0))[0].Token
	c.Step(ChooseInput(token, 1), T0+1)
	for k := range len(Tokens) - 1 {
		askPush(c, uint64(100+k), T0+2) // the other eleven words
	}
	codex := SessionQuestion{ID: "q9", Label: "web", App: "dev.warp.Warp-Stable", Kind: "permission",
		Text: "run make release", Options: []string{"Yes", "No"}, Prompt: "Allow?"}
	b := bubbleOf(askSession(c, codex, T0+3))
	if b[len(b)-1].Token != token {
		t.Fatalf("the word came round: %+v", b[len(b)-1])
	}
	fx := c.Step(AnswerInput("", "No"), T0+4)
	if len(answers(fx)) != 0 {
		t.Fatalf("a permission dialog is never answered this way: %+v", answers(fx))
	}
}

func TestAMenuChoiceReadsOptionsAsSaid(t *testing.T) {
	for _, tc := range []struct {
		options []string
		said    string
		want    int
	}{
		{[]string{"Yes", "No"}, "Yes", 1},
		{[]string{"Yes", "No"}, "Yes please", 1},
		{[]string{"Option A", "Option B"}, "Option B", 2},
		{[]string{"Option A", "Option B"}, "B", 2},
		{[]string{"Yes, proceed", "No, stop"}, "Yes, proceed", 1},
		{[]string{"Go ahead", "Wait"}, "Go ahead", 1},
		{[]string{"The first approach", "The second approach"}, "The second approach", 2},
		{[]string{"Postgres", "SQLite", "MySQL"}, "option one", 1},
		{[]string{"Postgres", "SQLite", "MySQL"}, "number two", 2},
		{[]string{"Yes", "Yes please"}, "yes please", 2},
		// Options with numbers: a number names one, never a place.
		{[]string{"2", "4", "8"}, "two", 1},
		{[]string{"2 workers", "4 workers", "8 workers"}, "2", 1},
		{[]string{"3 retries", "5 retries", "1 retry"}, "3", 1},
		{[]string{"3 retries", "5 retries", "1 retry"}, "one", 3},
		{[]string{"3 retries", "5 retries", "1 retry"}, "the second one", 2},
		{[]string{"2 workers", "4 workers"}, "6", 0},
		// Ruled out: nothing is picked.
		{[]string{"Postgres", "SQLite", "MySQL"}, "anything but SQLite", 0},
		{[]string{"Postgres", "SQLite", "MySQL"}, "avoid MySQL", 0},
		{[]string{"Postgres", "SQLite", "MySQL"}, "other than Postgres", 0},
		{[]string{"Postgres", "SQLite", "MySQL"}, "skip Postgres", 0},
		{[]string{"Postgres", "SQLite", "MySQL"}, "Postgres, except on CI", 0},
	} {
		got, ok := MenuChoice(tc.options, tc.said)
		if !ok {
			got = 0
		}
		if got != tc.want {
			t.Errorf("MenuChoice(%q, %q) = %d, want %d", tc.options, tc.said, got, tc.want)
		}
	}
}

func TestAMenuAndAJobAskedTogetherAreNoOnesAnswer(t *testing.T) {
	c := ctl()
	c.askedBy(asker{job: "job_000000000001", at: T0})
	c.askedBy(asker{menu: 7, at: T0})
	if !c.asked.tie {
		t.Fatal("job then menu", c.asked)
	}
	c.asked = asker{}
	c.askedBy(asker{menu: 7, at: T0})
	c.askedBy(asker{job: "job_000000000001", at: T0})
	if !c.asked.tie {
		t.Fatal("menu then job", c.asked)
	}
}

func TestOrListOfNothingIsEmpty(t *testing.T) {
	if got := orList(nil); got != "" {
		t.Fatal(got)
	}
}
