package agent

import "testing"

// A prompt's landing (#128) and a Working with no turn behind it (#127): both rest on the
// coordinator's UserPromptSubmit hook, which reports every prompt it takes.

const firstPrompt = "New request from the user: summarise the issues\n[ref job_000000000001]"

func submit(c *AgentController, text string, at Millis) []Effect {
	return c.Step(SubmitInput(Coordinator, text), at)
}

// T1 is when landingCtl's coordinator can take a prompt.
const T1 = T0 + StopSettleMs

// landingCtl: a coordinator that is up, has reported a submitted prompt (the user's "hello")
// and finished that turn, and is idle.
func landingCtl() *AgentController {
	c := ctl()
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	submit(c, "hello", T0)
	stop(c, "Hello.", T0)
	tick(c, T1)
	return c
}

// sentJob1: landingCtl with job 1's prompt typed into it at T1.
func sentJob1(t *testing.T) *AgentController {
	t.Helper()
	c := landingCtl()
	wantPrompts(t, c.Step(DelegateInput(job(1), "summarise the issues", ""), T1), firstPrompt)
	return c
}

func TestAPromptThatNeverLandsIsSentAgain(t *testing.T) {
	c := sentJob1(t)
	wantPrompts(t, tick(c, T1+PromptLandMs-1))
	fx := tick(c, T1+PromptLandMs)
	wantContains(t, fx, eLog("prompt not taken by the coordinator: sending it again"))
	wantJobStatus(t, c, 1, JobWorking)
	wantPrompts(t, tick(c, T1+PromptLandMs+PromptRetryMs), firstPrompt)
}

func TestALandedPromptIsNotSentAgain(t *testing.T) {
	c := sentJob1(t)
	submit(c, firstPrompt, T1+100)
	wantPrompts(t, tick(c, T1+PromptLandMs))
	wantPrompts(t, tick(c, T1+5*PromptLandMs))
	stop(c, `<spoken ref="job_000000000001">Done.</spoken>`, T1+6*PromptLandMs)
	wantJobStatus(t, c, 1, JobDone)
}

func TestAPromptThatNeverLandsThreeTimesFailsItsJobs(t *testing.T) {
	c := sentJob1(t)
	at := T1
	for i := 0; i < int(MaxPromptFailures); i++ {
		at += PromptLandMs
		tick(c, at)
		if i < int(MaxPromptFailures)-1 {
			at += PromptRetryMs << i
			wantPrompts(t, tick(c, at), firstPrompt)
		}
	}
	wantJobStatus(t, c, 1, JobFailed)
}

func TestAStopBeforeThePromptLandedIsAnotherTurns(t *testing.T) {
	c := sentJob1(t)
	// A background agent's notification ends a turn of its own just as job 1 was typed.
	openVoice(c)
	fx := stop(c, "<spoken>The cloud agent stopped.</spoken>", T1+100)
	wantContains(t, fx, eInject("Coordinator says: The cloud agent stopped."))
	wantJobStatus(t, c, 1, JobWorking, "that turn did not answer job 1")
	// Job 1's prompt still has to land, counted from that Stop.
	wantPrompts(t, tick(c, T1+100+PromptLandMs-1))
	tick(c, T1+100+PromptLandMs)
	wantPrompts(t, tick(c, T1+100+PromptLandMs+PromptRetryMs), firstPrompt)
}

func TestAnotherTurnRunningHoldsTheLandingClock(t *testing.T) {
	c := sentJob1(t)
	// Claude Code queued job 1 behind a background agent's notification turn.
	submit(c, "<task-notification>agent finished</task-notification>", T1+100)
	wantPrompts(t, tick(c, T1+10*PromptLandMs)) // the notification turn is still running
	stop(c, "<spoken>The agent finished.</spoken>", T1+10*PromptLandMs)
	submit(c, firstPrompt, T1+10*PromptLandMs+500)
	wantPrompts(t, tick(c, T1+20*PromptLandMs))
	wantJobStatus(t, c, 1, JobWorking)
}

func TestAWorkingWithNoSubmitDoesNotHoldTheInbox(t *testing.T) {
	// #127: a coordinator waiting on its background agent sits at its prompt while Herdr,
	// reading its title's spinner, reports it working.
	c := sentJob1(t)
	submit(c, firstPrompt, T1+100)
	c.Step(coordStatus(PaneWorking), T1+200)
	stop(c, `<spoken ref="job_000000000001" status="working">A background agent is on it.</spoken>`, T1+300)
	c.Step(coordStatus(PaneWorking), T1+400)
	wantPrompts(t, c.Step(DelegateInput(job(2), "second", ""), T1+500))
	wantPrompts(t, tick(c, T1+300+StopSettleMs), secondPrompt)
}

func TestASubmitAfterAStopIsANewTurn(t *testing.T) {
	c := sentJob1(t)
	submit(c, firstPrompt, T1+100)
	stop(c, `<spoken ref="job_000000000001">Done.</spoken>`, T1+200)
	c.Step(DelegateInput(job(2), "second", ""), T1+300)
	submit(c, "the user typed this", T1+400)
	wantPrompts(t, tick(c, T1+200+StopSettleMs)) // busy with the user's turn
	stop(c, "<spoken>Sure.</spoken>", T1+5_000)
	wantPrompts(t, tick(c, T1+5_000+StopSettleMs), secondPrompt)
}

func TestWithoutSubmitReportsAPromptIsNotChecked(t *testing.T) {
	// A coordinator with no UserPromptSubmit hook (a Codex one, or kept from an older app):
	// a prompt is trusted to land, as before.
	c := workingOnJob1()
	wantPrompts(t, tick(c, T0+10*PromptLandMs))
	wantJobStatus(t, c, 1, JobWorking)
}

func TestALostStopOfAnotherTurnStillLeavesThePromptToLand(t *testing.T) {
	c := sentJob1(t)
	submit(c, "<task-notification>agent finished</task-notification>", T1+100)
	c.Step(DelegateInput(job(2), "second", ""), T1+150)
	idle(c) // that turn's Stop is lost: only Herdr's Idle comes
	at := T0 + IdleWithoutStopMs
	wantPrompts(t, tick(c, at)) // job 1 has not landed: nothing else goes out
	wantPrompts(t, tick(c, at+PromptLandMs-1))
	tick(c, at+PromptLandMs)
	wantPrompts(t, tick(c, at+PromptLandMs+PromptRetryMs), firstPrompt+"\n\n"+secondPrompt)
}
