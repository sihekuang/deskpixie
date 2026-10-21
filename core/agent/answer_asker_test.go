package agent

import (
	"strings"
	"testing"
)

// #161: answer_job without a job id answers the question Pixie relayed last: a session's turn
// that asks gets the answer typed in, a job's question gets it as before.

var askingTurn = SessionTurn{Label: "deskpixie: hand-off tests",
	Reply: "The suite passed.\n\nShould I rebuild it and open the PR?", Duration: 45_000}

// twoJobsAsking leaves jobs 1 and 2 waiting on answers, asked in one Stop.
func twoJobsAsking(c *AgentController) {
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	c.Step(StopInput(Coordinator, `<spoken ref="job_000000000001" status="question">A?</spoken><spoken ref="job_000000000002" status="question">B?</spoken>`), T0)
}

func answerResult(t *testing.T, fx []Effect) Effect {
	t.Helper()
	rs := effectsOfKind(fx, EfAnswerResult)
	if len(rs) != 1 {
		t.Fatalf("want one answer result: %s", fmtEffects(fx))
	}
	return rs[0]
}

func TestAnAnswerAfterASessionsQuestionIsTypedIntoThatSession(t *testing.T) {
	c := turnCtl(nil)
	twoJobsAsking(c)
	sessionTurn(c, askingTurn, T0+1_000)
	fx := c.Step(AnswerInput("", "yes, let's do that"), T0+2_000)
	if r := answerResult(t, fx); r.Err != "" || r.Name != "api-claude" || r.Job != "" {
		t.Fatalf("answered %+v", r)
	}
	tells := effectsOfKind(fx, EfTellSession)
	if len(tells) != 1 || tells[0].Name != "api-claude" || tells[0].Text != "yes, let's do that" {
		t.Fatalf("the answer is typed into the session that asked: %s", fmtEffects(fx))
	}
	wantJobStatus(t, c, 1, JobQuestion)
	wantJobStatus(t, c, 2, JobQuestion)
	// Answered once: the next answer goes to the jobs still waiting, not the session again.
	fx = c.Step(AnswerInput("", "again"), T0+3_000)
	if r := answerResult(t, fx); r.Text != "coordinator" || anyKind(fx, EfTellSession) {
		t.Fatalf("a second answer went to %s", fmtEffects(fx))
	}
}

func TestAnAnswerAfterAJobsQuestionGoesToThatJob(t *testing.T) {
	c := turnCtl(nil)
	sessionTurn(c, askingTurn, T0)
	delegate(c, 1, "a")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	c.Step(StopInput(Coordinator, `<spoken ref="job_000000000001" status="question">A?</spoken>`), T0+1_000)
	delegate(c, 2, "b")
	c.Step(StopInput(Coordinator, `<spoken ref="job_000000000002" status="question">B?</spoken>`), T0+2_000)
	fx := c.Step(AnswerInput("", "the blue one"), T0+3_000)
	if r := answerResult(t, fx); r.Err != "" || r.Job != job(2) {
		t.Fatalf("answered %+v", r)
	}
	if anyKind(fx, EfTellSession) {
		t.Fatalf("a later job's question takes the answer, not the session: %s", fmtEffects(fx))
	}
	wantJobStatus(t, c, 1, JobQuestion)
	wantJobStatus(t, c, 2, JobWorking)
}

func TestASessionThatMovedOnNoLongerTakesTheAnswer(t *testing.T) {
	c := turnCtl(nil)
	sessionTurn(c, askingTurn, T0)
	sessionTurn(c, longTurn, T0+1_000) // its next turn asks nothing
	fx := c.Step(AnswerInput("", "yes"), T0+2_000)
	if r := answerResult(t, fx); r.Err != "No job is waiting for an answer." || anyKind(fx, EfTellSession) {
		t.Fatalf("%s", fmtEffects(fx))
	}
}

// Which one two questions asked together answer is the coordinator's to work out: it gets the
// answer with both questions, and asks the user only if it can't tell.
func TestAnAnswerToTwoQuestionsAskedTogetherGoesToTheCoordinatorToSort(t *testing.T) {
	c := turnCtl(nil)
	twoJobsAsking(c)
	fx := c.Step(AnswerInput("", "the blue one"), T0+1_000)
	if r := answerResult(t, fx); r.Err != "" || r.Text != "coordinator" {
		t.Fatalf("answered %+v", r)
	}
	fx = append(fx, c.Step(coordStatus(PaneIdle), T0+2_000)...)
	var sent string
	for _, e := range effectsOfKind(fx, EfPrompt) {
		sent += e.Text
	}
	for _, want := range []string{`The user answered: "the blue one"`, `job_000000000001 asked: "A?"`, `job_000000000002 asked: "B?"`,
		"work out which question it answers", "ask the user only if you can't tell", "[ref job_000000000001]", "[ref job_000000000002]"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the coordinator's prompt lacks %q: %q", want, sent)
		}
	}
}

// Voice models copy example ids or make them up (job_0123456789ab, Oct 18): an id that isn't a
// waiting job is ignored, and the answer goes to whoever asked.
func TestAnAnswerWithAMadeUpJobIDGoesToWhoeverAsked(t *testing.T) {
	c := turnCtl(nil)
	sessionTurn(c, askingTurn, T0)
	fx := c.Step(AnswerInput("job_0123456789ab", "yes"), T0+1_000)
	if r := answerResult(t, fx); r.Err != "" || r.Name != "api-claude" {
		t.Fatalf("answered %+v", r)
	}
	delegate(c, 1, "a")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	c.Step(StopInput(Coordinator, `<spoken ref="job_000000000001" status="question">A?</spoken>`), T0+2_000)
	fx = c.Step(AnswerInput("job_f876543210ab", "the blue one"), T0+3_000)
	if r := answerResult(t, fx); r.Err != "" || r.Job != job(1) {
		t.Fatalf("answered %+v", r)
	}
}

// #229: a turn of yours that isn't announced (the setting off, or too short) still means the
// session moved on: an answer meant for something else isn't typed into it.
func TestASessionThatMovedOnQuietlyNoLongerTakesTheAnswer(t *testing.T) {
	for name, edit := range map[string]func(*Settings){
		"setting off": settingOff,
		"short turn":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			c := turnCtl(edit)
			asked := askingTurn
			asked.FromPixie = true
			sessionTurn(c, asked, T0)
			mine := longTurn
			mine.Duration = 5_000 // under the minimum
			sessionTurn(c, mine, T0+1_000)
			fx := c.Step(AnswerInput("", "yes"), T0+2_000)
			if r := answerResult(t, fx); r.Err != "No job is waiting for an answer." || anyKind(fx, EfTellSession) {
				t.Fatalf("%s", fmtEffects(fx))
			}
		})
	}
}
