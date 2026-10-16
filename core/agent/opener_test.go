package agent

import (
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

// agent.openConversationForUpdates (opener.go).

func openingCtl() *AgentController {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true })
	delegate(c, 1, "summarise the issues")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	return c
}

func opens(fx []Effect) int {
	n := 0
	for _, e := range fx {
		if e.Kind == EfOpenConversation {
			n++
		}
	}
	return n
}

const doneStop = `<spoken ref="job_000000000001" status="done">There are 12 open issues.</spoken>`

func TestFlagOffADoneLineWhileClosedIsExactlyTodays(t *testing.T) {
	c := workingOnJob1()
	fx := visible(stop(c, doneStop, T0))
	wantEffects(t, fx, eChime, eNotify("There are 12 open issues."), eCue(CueHappy), eHold(false))
	for at := T0 + 1000; at < T0+5*NotifyWindowMs; at += 1000 {
		if n := opens(c.Step(TickInput(), at)); n != 0 {
			t.Fatal("opened with the flag off")
		}
	}
}

func TestADoneLineWhileClosedOpensAConversationAndIsSaidOnce(t *testing.T) {
	c := openingCtl()
	fx := visible(stop(c, doneStop, T0))
	// Today's chime and notification, then the open.
	wantEffects(t, fx, eChime, eNotify("There are 12 open issues."), eOpen, eCue(CueHappy), eHold(false))
	fx = visible(c.Step(voiceOpened(), T0+800))
	wantEffects(t, fx, eInject("Coordinator replies while you were away:\n- Coordinator reply: There are 12 open issues."))
	if n := len(c.outbox.Undelivered()); n != 0 {
		t.Fatalf("%d lines still undelivered", n)
	}
	// Closed by the silence timeout: nothing reopens, nothing is said again.
	c.Step(voiceClosed(), T0+20_000)
	for at := T0 + 21_000; at < T0+2*NotifyWindowMs; at += 1000 {
		if fx := visible(c.Step(TickInput(), at)); anyKind(fx, EfOpenConversation, EfInject) {
			t.Fatal(fmtEffects(fx))
		}
	}
	wantNone(t, visible(c.Step(voiceOpened(), T0+3*NotifyWindowMs)), "said once")
}

func TestFailedAndQuestionLinesOpenAConversation(t *testing.T) {
	for _, status := range []string{"failed", "question"} {
		c := openingCtl()
		fx := stop(c, `<spoken ref="job_000000000001" status="`+status+`">Which repo?</spoken>`, T0)
		if opens(fx) != 1 {
			t.Fatalf("%s: %s", status, fmtEffects(visible(fx)))
		}
	}
}

func TestAPermissionQuestionOpensAConversation(t *testing.T) {
	c := openingCtl()
	fx := visible(askPush(c, 7, T0))
	wantContains(t, fx, eOpen)
	fx = visible(c.Step(voiceOpened(), T0+500))
	wantEffects(t, fx, eInject("Coordinator replies while you were away:\n- Coordinator question: Run git push in coordinator? Say confirm hawk."))
}

func TestProgressAndUnsolicitedLinesDoNotOpenAConversation(t *testing.T) {
	c := openingCtl()
	fx := stop(c, `<spoken ref="job_000000000001" status="working">Halfway through.</spoken>`, T0)
	fx = append(fx, stop(c, `<spoken ref="job_000000000099">Something else.</spoken>`, T0+10)...)
	if opens(fx) != 0 {
		t.Fatal(fmtEffects(visible(fx)))
	}
	for at := T0 + 1000; at < T0+10_000; at += 1000 {
		if opens(c.Step(TickInput(), at)) != 0 {
			t.Fatal("a tick opened for a progress line")
		}
	}
}

// #113: with the flag on, a chime means a conversation follows. A progress reply opens none, so
// it doesn't chime or notify either: it waits in the outbox for the next conversation.
func TestAProgressReplyWhileClosedNeitherChimesNorNotifies(t *testing.T) {
	c := openingCtl()
	fx := visible(stop(c, `<spoken ref="job_000000000001" status="working">Halfway through.</spoken>`, T0))
	if anyKind(fx, EfChime, EfNotify, EfOpenConversation) {
		t.Fatal(fmtEffects(fx))
	}
	fx = visible(stop(c, doneStop, T0+5000))
	wantContains(t, fx, eChime)
	wantContains(t, fx, eOpen)
	fx = visible(c.Step(voiceOpened(), T0+5800))
	wantEffects(t, fx, eInject("Coordinator replies while you were away:\n- Coordinator reply: Halfway through.\n- Coordinator reply: There are 12 open issues."))
}

func TestAnOpenConversationGetsTheLineInjectedAsToday(t *testing.T) {
	c := openingCtl()
	openVoice(c)
	fx := visible(stop(c, doneStop, T0))
	wantEffects(t, fx, eInject("Coordinator reply: There are 12 open issues."), eCue(CueHappy), eHold(false))
}

func TestABurstOpensOneConversation(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true })
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	delegate(c, 3, "c")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	fx := stop(c, `<spoken ref="job_000000000001" status="done">A done.</spoken>`, T0)
	fx = append(fx, stop(c, `<spoken ref="job_000000000002" status="done">B done.</spoken>`, T0+300)...)
	fx = append(fx, c.Step(TickInput(), T0+1000)...)
	fx = append(fx, stop(c, `<spoken ref="job_000000000003" status="question">Which repo?</spoken>`, T0+1200)...)
	if n := opens(fx); n != 1 {
		t.Fatalf("%d opens: %s", n, fmtEffects(visible(fx)))
	}
	fx = visible(c.Step(voiceOpened(), T0+2000))
	wantEffects(t, fx, eInject("Coordinator replies while you were away:\n- Coordinator reply: A done.\n- Coordinator reply: B done.\n- Coordinator question: Which repo?"))
}

func TestOpensAreCappedPerMinuteAndResumeWhenASlotFrees(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.NotifyPerMinute = 2 })
	for n := uint64(1); n <= 3; n++ {
		delegate(c, n, "task")
	}
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	total := 0
	for n, at := range []Millis{T0, T0 + 10_000, T0 + 20_000} {
		ref := []string{"job_000000000001", "job_000000000002", "job_000000000003"}[n]
		total += opens(stop(c, `<spoken ref="`+ref+`" status="question">Which?</spoken>`, at))
		// Each conversation fails to connect (no key, say): closed without Opened.
		c.Step(voiceClosed(), at+500)
	}
	if total != 2 {
		t.Fatalf("%d opens in a minute, want 2", total)
	}
	got := 0
	for at := T0 + 21_000; at < T0+NotifyWindowMs; at += 1000 {
		got += opens(c.Step(TickInput(), at))
	}
	if got != 0 {
		t.Fatal("opened again within the minute")
	}
	// The slot frees: the line still unsaid gets its conversation.
	for at := T0 + NotifyWindowMs; at < T0+NotifyWindowMs+3000; at += 1000 {
		got += opens(c.Step(TickInput(), at))
	}
	if got != 1 {
		t.Fatalf("%d opens once a slot freed, want 1", got)
	}
}

func TestARefusedOpenIsRetriedOnTheNextTick(t *testing.T) {
	c := openingCtl()
	if opens(stop(c, doneStop, T0)) != 1 {
		t.Fatal("no open")
	}
	// The voice loop was busy (a conversation closing, the mic in use).
	wantNone(t, visible(c.Step(OpenRefusedInput(), T0)))
	if opens(c.Step(TickInput(), T0+1000)) != 1 {
		t.Fatal("not retried")
	}
	c.Step(OpenRefusedInput(), T0+1000)
	if opens(c.Step(TickInput(), T0+2000)) != 1 {
		t.Fatal("refusals must not use up the cap")
	}
	fx := visible(c.Step(voiceOpened(), T0+3000))
	wantEffects(t, fx, eInject("Coordinator replies while you were away:\n- Coordinator reply: There are 12 open issues."))
	if opens(c.Step(TickInput(), T0+4000)) != 0 {
		t.Fatal("opened again after Opened")
	}
}

func TestAConversationTheUserOpenedMeanwhileTakesTheUpdate(t *testing.T) {
	c := openingCtl()
	c.Step(OpenRefusedInput(), T0) // nothing waiting: ignored
	stop(c, doneStop, T0)
	c.Step(OpenRefusedInput(), T0) // the user was already starting one
	fx := visible(c.Step(voiceOpened(), T0+500))
	wantContains(t, fx, eInject("Coordinator replies while you were away:\n- Coordinator reply: There are 12 open issues."))
	if opens(c.Step(TickInput(), T0+1000)) != 0 {
		t.Fatal("nothing is left to say")
	}
}

func TestAnOpenThatNeverReportsIsGivenUp(t *testing.T) {
	c := openingCtl()
	stop(c, doneStop, T0)
	got := 0
	for at := T0 + 1000; at < T0+OpenWaitMs+2000; at += 1000 {
		got += opens(c.Step(TickInput(), at))
	}
	if got != 1 {
		t.Fatalf("%d re-opens after the wait, want 1", got)
	}
}

func TestAWorkerReportOpensAConversation(t *testing.T) {
	s := workerSettings()
	s.SpeakWorkerReports = true
	s.OpenConversation = true
	c := wctlWith(s)
	running(t, c)
	call := McpCall{Kind: CallReportResult, Outcome: OutcomeDone, Summary: "Fixed the flaky test", Evidence: "x"}
	// Debounced first (debounce.go): the open comes with the line, once the job is quiet.
	_, _, fx := mcp(t, c, workerName, call, T0)
	if opens(fx) != 0 {
		t.Fatal("opened before the report was let out: " + fmtEffects(visible(fx)))
	}
	if fx = c.Step(TickInput(), T0+UpdateQuietMs); opens(fx) != 1 {
		t.Fatal(fmtEffects(visible(fx)))
	}
	fx = visible(c.Step(VoiceInput(VoiceSignal{Kind: voice.SigOpened}), T0+UpdateQuietMs+500))
	wantContains(t, fx, eInject("Coordinator replies while you were away:\n- The upload worker in focus says it's done: Fixed the flaky test."))
}

func TestABurstOfWorkerReportsIsOneLineAndOneConversation(t *testing.T) {
	s := workerSettings()
	s.SpeakWorkerReports = true
	s.OpenConversation = true
	c := wctlWith(s)
	running(t, c)
	admit(t, c)
	up(c, workerName+"-2", PaneIdle)
	c.Step(paneStatus(workerName+"-2", PaneWorking), T0)
	var fx []Effect
	for _, r := range []struct {
		worker, summary string
		at              Millis
	}{
		{workerName, "Fixed the flaky test", T0},
		{workerName, "Fixed the flaky test", T0 + 1_000}, // a repeat: dropped
		{workerName + "-2", "Added a retry", T0 + 3_000},
		{workerName, "Fixed the flaky test and the lint", T0 + 5_000}, // the latest per worker wins
	} {
		_, _, more := mcp(t, c, r.worker, done(r.summary), r.at)
		fx = append(fx, more...)
	}
	for at := T0 + 6_000; at < T0+5_000+UpdateQuietMs; at += 1000 {
		fx = append(fx, c.Step(TickInput(), at)...)
	}
	if opens(fx) != 0 || anyKind(visible(fx), EfNotify, EfChime) {
		t.Fatal("said or opened before the job was quiet: " + fmtEffects(visible(fx)))
	}
	fx = visible(c.Step(TickInput(), T0+5_000+UpdateQuietMs))
	line := "2 worker updates: The upload worker in focus says it's done: Fixed the flaky test and the lint. The upload 2 worker in focus says it's done: Added a retry."
	wantContains(t, fx, eNotify(line))
	if n := opens(fx); n != 1 {
		t.Fatalf("%d opens for the coalesced line, want 1", n)
	}
	fx = visible(c.Step(voiceOpened(), T0+5_000+UpdateQuietMs+500))
	wantEffects(t, fx, eInject("Coordinator replies while you were away:\n- "+line))
	c.Step(voiceClosed(), T0+5_000+UpdateQuietMs+20_000)
	for at := T0 + 5_000 + UpdateQuietMs + 21_000; at < T0+3*NotifyWindowMs; at += 1000 {
		if fx := visible(c.Step(TickInput(), at)); anyKind(fx, EfOpenConversation, EfInject, EfNotify) {
			t.Fatal(fmtEffects(fx))
		}
	}
}

func TestAWorkerReportWithSpeakResultsOffOpensNothing(t *testing.T) {
	s := workerSettings()
	s.OpenConversation = true
	c := wctlWith(s)
	running(t, c)
	call := McpCall{Kind: CallReportResult, Outcome: OutcomeDone, Summary: "Fixed the flaky test", Evidence: "x"}
	_, _, fx := mcp(t, c, workerName, call, T0)
	if opens(fx) != 0 {
		t.Fatal(fmtEffects(visible(fx)))
	}
}

func TestAFailedConnectIsNotRetriedUntilTheNextUpdate(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true })
	delegate(c, 1, "a")
	delegate(c, 2, "b")
	delegate(c, 3, "c")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	stop(c, `<spoken ref="job_000000000001" status="done">A done.</spoken>`, T0)
	stop(c, `<spoken ref="job_000000000002" status="done">B done.</spoken>`, T0+100) // while connecting
	c.Step(voiceClosed(), T0+500)                                                    // no API key, say
	for at := T0 + 1000; at < T0+10_000; at += 1000 {
		if opens(c.Step(TickInput(), at)) != 0 {
			t.Fatal("retried a failed connect")
		}
	}
	if opens(stop(c, `<spoken ref="job_000000000003" status="done">C done.</spoken>`, T0+10_000)) != 1 {
		t.Fatal("the next update must try again")
	}
}

func TestARefusedOpenWaitsOutTheClosingConversation(t *testing.T) {
	c := openingCtl()
	stop(c, doneStop, T0)
	c.Step(OpenRefusedInput(), T0) // Pixie is still closing an earlier conversation
	c.Step(voiceClosed(), T0+200)  // which now ends
	if opens(c.Step(TickInput(), T0+1000)) != 1 {
		t.Fatal("the refused open must be retried once Pixie is at rest")
	}
}
