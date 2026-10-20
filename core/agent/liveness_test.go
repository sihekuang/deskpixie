package agent

import (
	"strings"
	"testing"
)

func promptWith(fx []Effect, text string) bool {
	for _, e := range fx {
		if e.Kind == EfPrompt && strings.Contains(e.Text, text) {
			return true
		}
	}
	return false
}

func spoke(fx []Effect, text string) bool {
	for _, e := range fx {
		if (e.Kind == EfInject && strings.Contains(e.Notice.Text, text)) ||
			(e.Kind == EfNotify && strings.Contains(e.Text, text)) {
			return true
		}
	}
	return false
}

// unbackedJob1: the coordinator said job 1 is still being worked on, then went idle, and no
// worker is running for it (e.g. the worker's pane went away).
func unbackedJob1(t *testing.T) *AgentController {
	t.Helper()
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="working">A worker is on it.</spoken>`, T0)
	c.Step(coordStatus(PaneIdle), T0)
	wantJobStatus(t, c, 1, JobWorking)
	return c
}

func TestAWorkingJobWithNothingBehindItIsNudgedThenFailed(t *testing.T) {
	c := unbackedJob1(t)
	at := T0 + StopSettleMs + 10
	tick(c, at) // first seen unbacked
	if fx := tick(c, at+UnbackedNudgeMs-1); promptWith(fx, unbackedNudge) {
		t.Fatal("nudged early")
	}
	fx := tick(c, at+UnbackedNudgeMs)
	if !promptWith(fx, unbackedNudge) || !promptWith(fx, "[ref job_000000000001]") {
		t.Fatalf("no nudge: %+v", fx)
	}
	// The coordinator's turn ends without a word about the job.
	t2 := at + UnbackedNudgeMs + 1000
	stop(c, `<spoken ref="job_000000000099">Something else.</spoken>`, t2)
	c.Step(coordStatus(PaneIdle), t2)
	tick(c, t2+StopSettleMs+10)
	wantJobStatus(t, c, 1, JobWorking)
	fx = tick(c, t2+StopSettleMs+10+UnbackedFailMs)
	wantJobStatus(t, c, 1, JobFailed)
	wantContains(t, fx, eCue(CueError))
	wantContains(t, fx, eHold(false))
	if !spoke(visible(fx), "Coordinator: a job stopped without finishing: summarise the issues") {
		t.Fatalf("not said: %+v", fx)
	}
}

func TestAProgressReplyAfterTheNudgeResetsTheWatch(t *testing.T) {
	c := unbackedJob1(t)
	at := T0 + StopSettleMs + 10
	tick(c, at)
	tick(c, at+UnbackedNudgeMs)
	t2 := at + UnbackedNudgeMs + 1000
	stop(c, `<spoken ref="job_000000000001" status="working">Still at it.</spoken>`, t2)
	c.Step(coordStatus(PaneIdle), t2)
	t3 := t2 + StopSettleMs + 10
	tick(c, t3)
	// A fresh watch: at the old failure time it is nudged again, not failed.
	fx := tick(c, t3+UnbackedNudgeMs)
	wantJobStatus(t, c, 1, JobWorking)
	if !promptWith(fx, unbackedNudge) {
		t.Fatalf("no second nudge: %+v", fx)
	}
}

// Today's bug: a live worker keeps its job alive; once its pane is gone, nothing does.
func TestAJobIsBackedByItsWorkerUntilTheWorkerGoes(t *testing.T) {
	c := wctl()
	running(t, c)
	coordinatorTurnEnds(c, T0)
	c.Step(coordStatus(PaneIdle), T0+StopSettleMs)
	start := T0 + StopSettleMs + 10
	for at := start; at < start+2*UnbackedNudgeMs; at += 10_000 {
		if fx := tick(c, at); promptWith(fx, unbackedNudge) {
			t.Fatal("nudged while the worker runs")
		}
	}
	gone := start + 2*UnbackedNudgeMs
	c.Step(WorkerClosedInput(workerName), gone)
	// The coordinator is told the worker exited; its turn ends without a word about the job.
	stop(c, `<spoken ref="job_000000000099">Something else.</spoken>`, gone+1000)
	c.Step(coordStatus(PaneIdle), gone+1000)
	t2 := gone + 1000 + StopSettleMs + 10
	tick(c, t2)
	if fx := tick(c, t2+UnbackedNudgeMs); !promptWith(fx, unbackedNudge) {
		t.Fatalf("no nudge after the worker went: %+v", fx)
	}
}

func TestABusyCoordinatorBacksItsJobs(t *testing.T) {
	c := unbackedJob1(t)
	c.Step(coordStatus(PaneWorking), T0+1)
	for at := T0 + 1000; at < T0+3*(UnbackedNudgeMs+UnbackedFailMs); at += 30_000 {
		if fx := tick(c, at); promptWith(fx, unbackedNudge) {
			t.Fatal("nudged while the coordinator works")
		}
	}
	wantJobStatus(t, c, 1, JobWorking)
}

func TestAJobInFlightIsBacked(t *testing.T) {
	c := workingOnJob1() // the prompt is out, its Stop hasn't come
	for at := T0 + 1000; at < T0+3*(UnbackedNudgeMs+UnbackedFailMs); at += 30_000 {
		tick(c, at)
	}
	wantJobStatus(t, c, 1, JobWorking)
}

func TestAnUnansweredQuestionExpiresAfterThirtyMinutes(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="question">Merge it yourself, or allow merges?</spoken>`, T0)
	c.Step(coordStatus(PaneIdle), T0)
	wantJobStatus(t, c, 1, JobQuestion)
	tick(c, T0+QuestionExpiryMs-1)
	wantJobStatus(t, c, 1, JobQuestion)
	fx := tick(c, T0+QuestionExpiryMs)
	wantJobStatus(t, c, 1, JobFailed)
	if !spoke(visible(fx), "Coordinator: closed a question you didn't answer: summarise the issues") {
		t.Fatalf("not said: %+v", fx)
	}
	if !promptWith(fx, questionExpiredNote) {
		t.Fatalf("coordinator not told: %+v", fx)
	}
	// The hold went when the question was asked (#119).
	wantNotContains(t, fx, eHold(true))
}

func TestAnAnsweredQuestionDoesNotExpire(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`, T0)
	c.Step(coordStatus(PaneIdle), T0)
	c.Step(AnswerInput(job(1), "deskpixie"), T0+60_000)
	wantJobStatus(t, c, 1, JobWorking)
	tick(c, T0+QuestionExpiryMs+1)
	if s := c.Snapshot(T0)[0].Status; s == JobFailed {
		t.Fatal("expired after an answer")
	}
}

// #119: a job that only waits on the user's answer doesn't hold Pixie in delegating; answering
// it puts the job back to work, and the hold with it.
func TestAQuestionReleasesTheHoldAndAnAnswerTakesItBack(t *testing.T) {
	c := workingOnJob1()
	fx := stop(c, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`, T0)
	wantJobStatus(t, c, 1, JobQuestion)
	wantContains(t, fx, eHold(false))
	fx = c.Step(AnswerInput(job(1), "deskpixie"), T0+60_000)
	wantJobStatus(t, c, 1, JobWorking)
	wantContains(t, fx, eHold(true))
}

// Another job still running keeps the hold while one waits on a question.
func TestARunningJobKeepsTheHoldBesideAQuestion(t *testing.T) {
	c := workingOnJob1()
	delegate(c, 2, "b")
	fx := stop(c, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`, T0)
	wantNotContains(t, fx, eHold(false))
}

// #165: a job handed to a session has nothing behind it the app can see; the nudge tells the
// coordinator to close it rather than ask the user about the session.
func TestTheNudgeClosesAJobHandedToASession(t *testing.T) {
	if !strings.Contains(unbackedNudge, "If you handed it to a session, reply done") {
		t.Fatal(unbackedNudge)
	}
}
