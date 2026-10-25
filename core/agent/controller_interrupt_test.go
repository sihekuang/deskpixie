package agent

import (
	"fmt"
	"strings"
	"testing"
)

// #309: the stop button by the working coordinator, and by one of Pixie's workers.

// interruptTurn steps InterruptTurnInput: whether a turn was stopped, and the other effects.
func interruptTurn(t *testing.T, c *AgentController, now Millis) (bool, []Effect) {
	t.Helper()
	var rest []Effect
	var stopped, answered bool
	for _, e := range c.Step(InterruptTurnInput(), now) {
		if e.Kind == EfToolResult {
			stopped, answered = e.Value.(bool), true
			continue
		}
		rest = append(rest, e)
	}
	if !answered {
		t.Fatal("no ToolResult")
	}
	return stopped, rest
}

func cancelNote(n uint64) string {
	return fmt.Sprintf("The user cancelled %s. Stop work on it and confirm.\n[ref %s]", job(n), job(n))
}

func TestInterruptingTheTurnCancelsEveryJobWhoseRequestItCarries(t *testing.T) {
	c := ctl()
	c.Step(TypedDelegateInput(job(1), "a", ""), T0)
	delegate(c, 2, "b") // spoken
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	c.Step(coordStatus(PaneWorking), T0)
	stopped, fx := interruptTurn(t, c, T0)
	if !stopped {
		t.Fatal("a turn was running")
	}
	wantEffects(t, visible(fx), eInterrupt, eHold(false))
	wantJobStatus(t, c, 1, JobCancelled)
	wantJobStatus(t, c, 2, JobCancelled, "a spoken request is stopped too")
	// One Esc ends the turn; the Idle it brings sends the notes.
	fx = visible(c.Step(coordStatus(PaneIdle), T0))
	wantEffects(t, fx, ePrompt(cancelNote(1)+"\n\n"+cancelNote(2)))
}

func TestStoppingAWorkersReportKeepsItsJobAndTellsTheCoordinatorNextTurn(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="working">On it.</spoken>`, T0)
	tick(c, T0+StopSettleMs)
	report := MessageForJob(job(1), "Worker focus-upload reports done: \"Fixed it\"")
	c.inbox.Push(report)
	var fx []Effect
	c.flush(T0+StopSettleMs, &fx)
	wantPrompts(t, fx, report.Text)
	c.Step(coordStatus(PaneWorking), T0+StopSettleMs)
	stopped, fx := interruptTurn(t, c, T0+StopSettleMs)
	if !stopped {
		t.Fatal("its turn was running")
	}
	wantEffects(t, visible(fx), eInterrupt)
	wantJobStatus(t, c, 1, JobWorking, "a worker's report isn't cancelled")
	// The note makes no turn of its own: the coordinator stays stopped.
	wantPrompts(t, c.Step(coordStatus(PaneIdle), T0+StopSettleMs+1))
	wantPrompts(t, tick(c, T0+StopSettleMs+2_000))
	// It rides with the next one.
	wantPrompts(t, delegate(c, 2, "b"), "The user stopped your turn; don't continue unless asked.\n[ref job_000000000001]\n\nNew request from the user: b\n[ref job_000000000002]")
}

func TestAStoppedSpokenJobIsNotRevivedByTheLivenessCheck(t *testing.T) {
	c := workingOnJob1() // spoken, in flight
	openVoice(c)
	c.Step(coordStatus(PaneWorking), T0)
	if stopped, _ := interruptTurn(t, c, T0); !stopped {
		t.Fatal("its turn was running")
	}
	wantJobStatus(t, c, 1, JobCancelled)
	if c.held {
		t.Fatal("the conversation held open for it is let go")
	}
	at := T0 + 1
	wantPrompts(t, c.Step(coordStatus(PaneIdle), at), cancelNote(1))
	stop(c, "Stopped.", at)
	for at < T0+UnbackedNudgeMs+UnbackedFailMs+60_000 {
		at += 1_000
		for _, p := range prompts(tick(c, at)) {
			t.Fatalf("nothing more for a stopped job: %q", p)
		}
	}
	wantJobStatus(t, c, 1, JobCancelled)
}

func TestInterruptingWithNoCoordinatorUpOrAnIdleOneDoesNothing(t *testing.T) {
	c := ctl()
	if stopped, fx := interruptTurn(t, c, T0); stopped || len(visible(fx)) != 0 {
		t.Fatal(stopped, fmtEffects(fx))
	}
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	if stopped, fx := interruptTurn(t, c, T0); stopped || len(visible(fx)) != 0 {
		t.Fatal("nothing running", stopped, fmtEffects(fx))
	}
}

func TestAStopWhileThePromptLandsWaitsForItToLand(t *testing.T) {
	c := sentJob1(t)
	stopped, fx := interruptTurn(t, c, T1+10)
	if !stopped || anyKind(fx, EfInterrupt) {
		t.Fatal("no Esc into the prompt being typed", stopped, fmtEffects(fx))
	}
	wantJobStatus(t, c, 1, JobWorking)
	fx = submit(c, firstPrompt, T1+20)
	wantContains(t, fx, eInterrupt, "landed: its turn is stopped")
	wantJobStatus(t, c, 1, JobCancelled)
}

func TestAStopWhileAPromptNeverLandsCancelsItsRequestWithoutAnEsc(t *testing.T) {
	c := sentJob1(t)
	interruptTurn(t, c, T1+10)
	fx := tick(c, T1+PromptLandMs)
	if anyKind(fx, EfInterrupt) {
		t.Fatal(fmtEffects(fx))
	}
	wantJobStatus(t, c, 1, JobCancelled)
	wantPrompts(t, tick(c, T1+PromptLandMs+PromptRetryMs)) // nothing sent again for it
}

// interruptWorker steps WorkerInterruptedInput: whether it was Pixie's worker, and the effects.
func interruptWorker(t *testing.T, c *AgentController, name string, now Millis) (bool, []Effect) {
	t.Helper()
	var rest []Effect
	var ok bool
	for _, e := range c.Step(WorkerInterruptedInput(name), now) {
		if e.Kind == EfToolResult {
			ok = e.Value.(bool)
			continue
		}
		rest = append(rest, e)
	}
	return ok, rest
}

func stoppedWorkerNote() string {
	return fmt.Sprintf("The user stopped %s's turn. Don't restart it unless they ask.\n[ref %s]", workerName, job(1))
}

func TestStoppingAWorkerTellsTheCoordinatorAtOnceAndKeepsItsJob(t *testing.T) {
	c := wctl()
	running(t, c)
	coordinatorTurnEnds(c, T0)
	if ok, _ := interruptWorker(t, c, "nobody", T0+StopSettleMs); ok {
		t.Fatal("not a worker")
	}
	ok, fx := interruptWorker(t, c, workerName, T0+StopSettleMs)
	if !ok {
		t.Fatal("its worker")
	}
	wantPrompts(t, fx, stoppedWorkerNote())
	wantJobStatus(t, c, 1, JobWorking, "the coordinator decides what next")
	wantRows(t, c, rowView{workerName, WorkerRunning})
	// Its Esc fires no Stop: the turn is over, with nothing forwarded late.
	at := T0 + StopSettleMs + 1
	c.Step(paneStatus(workerName, PaneIdle), at)
	for ; at < T0+ReportGraceMs+WorkerIdleWithoutStopMs+10_000; at += 1_000 {
		if fx := tick(c, at); anyKind(fx, EfReadWorker) || anyKind(fx, EfPrompt) {
			t.Fatalf("no silent-turn report after a stop: %s", fmtEffects(fx))
		}
	}
}

func TestAStoppedWorkersMessagesWaitUntilTheCoordinatorSendsMore(t *testing.T) {
	c := wctl()
	running(t, c)
	_, _, _ = mcp(t, c, Coordinator, McpCall{Kind: CallMessageWorker, Name: workerName, Text: "Use the main branch."}, T0)
	interruptWorker(t, c, workerName, T0)
	fx := c.Step(paneStatus(workerName, PaneIdle), T0+1)
	fx = append(fx, tick(c, T0+WorkerIdleWithoutStopMs+1)...)
	if anyKind(fx, EfPromptWorker) {
		t.Fatalf("held after the stop: %s", fmtEffects(fx))
	}
	_, _, fx = mcp(t, c, Coordinator, McpCall{Kind: CallMessageWorker, Name: workerName, Text: "Carry on."}, T0+WorkerIdleWithoutStopMs+2)
	wantContains(t, fx, ePromptWorker(workerName, "Coordinator says: Use the main branch.\n\nCoordinator says: Carry on."))
}

func TestAStoppedWorkersMessagesGoOnceTheUserSetsItWorkingAgain(t *testing.T) {
	c := wctl()
	running(t, c)
	_, _, _ = mcp(t, c, Coordinator, McpCall{Kind: CallMessageWorker, Name: workerName, Text: "Use the main branch."}, T0)
	interruptWorker(t, c, workerName, T0)
	c.Step(paneStatus(workerName, PaneIdle), T0+1)
	// You type in its terminal: a new turn, and its Stop.
	c.Step(paneStatus(workerName, PaneWorking), T0+2)
	fx := c.Step(StopInput(workerName, "Done what you asked."), T0+3)
	var sent []string
	for _, e := range fx {
		if e.Kind == EfPromptWorker {
			sent = append(sent, e.Text)
		}
	}
	if len(sent) != 1 || !strings.Contains(sent[0], "Use the main branch.") {
		t.Fatal(fmtEffects(fx))
	}
}

func TestALateWorkingAfterAWorkersStopDoesNotReopenIt(t *testing.T) {
	c := wctl()
	running(t, c)
	coordinatorTurnEnds(c, T0)
	_, _, _ = mcp(t, c, Coordinator, McpCall{Kind: CallMessageWorker, Name: workerName, Text: "Use the main branch."}, T0)
	at := T0 + StopSettleMs
	interruptWorker(t, c, workerName, at)
	// A Working queued before the Esc took, then the Idle the Esc brought.
	c.Step(paneStatus(workerName, PaneWorking), at+1)
	c.Step(paneStatus(workerName, PaneIdle), at+2)
	for at += 3; at < T0+ReportGraceMs+WorkerIdleWithoutStopMs+10_000; at += 1_000 {
		if fx := tick(c, at); anyKind(fx, EfReadWorker, EfPromptWorker) {
			t.Fatalf("still stopped: %s", fmtEffects(fx))
		}
	}
	if w := c.workers.Get(workerName); !w.stopped {
		t.Fatal("its messages still wait")
	}
	// Now you type in its terminal: that is a new turn.
	c.Step(paneStatus(workerName, PaneWorking), at)
	if w := c.workers.Get(workerName); w.stopped {
		t.Fatal("a new turn lets them go")
	}
}

func TestTheNudgeForAStoppedTurnsJobSaysItOnce(t *testing.T) {
	c := workingOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="working">On it.</spoken>`, T0)
	tick(c, T0+StopSettleMs)
	var fx []Effect
	c.inbox.Push(MessageForJob(job(1), "Worker focus-upload reports done: \"Fixed it\""))
	c.flush(T0+StopSettleMs, &fx)
	c.Step(coordStatus(PaneWorking), T0+StopSettleMs)
	interruptTurn(t, c, T0+StopSettleMs)
	c.Step(coordStatus(PaneIdle), T0+StopSettleMs+1)
	var got []string
	for at := T0 + StopSettleMs + 1_000; at <= T0+StopSettleMs+UnbackedNudgeMs+2_000; at += 1_000 {
		got = append(got, prompts(tick(c, at))...)
	}
	if len(got) != 1 || got[0] != "The user stopped your turn while you were on this; reply with what you have, or that it stopped.\n[ref job_000000000001]" {
		t.Fatalf("one sentence, not the rider and the nudge: %q", got)
	}
}
