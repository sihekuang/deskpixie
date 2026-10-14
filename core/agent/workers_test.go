package agent

import (
	"reflect"
	"testing"
)

// Ported from crates/agent-core/src/workers.rs (32 tests).

const T0 Millis = 1_000_000

const ff = "focus-fix"

func plan(name string, kind AgentKind) WorkerPlan {
	return WorkerPlan{
		Name:     name,
		Slug:     "fix",
		Kind:     kind,
		Repo:     "/projects/focus",
		RepoName: "focus",
		Job:      JobIDFromBits(1),
		Task:     "fix it",
	}
}

func upWorkers(kind AgentKind) *Workers {
	w := NewWorkers()
	w.Admit(plan(ff, kind))
	w.Up(ff, "/projects/focus/.worktrees/fix", PaneIdle, "brief", T0)
	return w
}

// sendIs asserts take_send's result: want == "" means None.
func sendIs(t *testing.T, w *Workers, now Millis, want string, why ...string) {
	t.Helper()
	got, ok := w.TakeSend(ff, now)
	if want == "" {
		if ok {
			t.Fatalf("take_send = %q, want none %v", got, why)
		}
		return
	}
	if !ok || got != want {
		t.Fatalf("take_send = %q (%v), want %q %v", got, ok, want, why)
	}
}

func mustSend(t *testing.T, w *Workers, now Millis) string {
	t.Helper()
	got, ok := w.TakeSend(ff, now)
	if !ok {
		t.Fatal("take_send = none")
	}
	return got
}

func eqv(t *testing.T, got, want any, why ...string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v %v", got, want, why)
	}
}

func names(n ...string) []string { return n }

func readD(n string) []WorkerDelivery { return []WorkerDelivery{{Kind: DeliveryRead, Name: n}} }

func screenIs(t *testing.T, w *Workers, screen string, now Millis, want Nudge, why ...string) {
	t.Helper()
	got, ok := w.OnDeliveryScreen(ff, screen, now)
	if !ok || got != want {
		t.Fatalf("on_delivery_screen = %+v (%v), want %+v %v", got, ok, want, why)
	}
}

func readScreen(n string) []Silent { return []Silent{{Kind: SilentReadScreen, Name: n}} }

func said(n, text string) []Silent { return []Silent{{Kind: SilentSaid, Name: n, Text: text}} }

func emptySilent(t *testing.T, s []Silent, why ...string) {
	t.Helper()
	if len(s) != 0 {
		t.Fatalf("due_silent = %+v %v", s, why)
	}
}

func TestNothingIsSentBeforeThePaneIsUpThenTheBriefGoesFirst(t *testing.T) {
	w := NewWorkers()
	w.Admit(plan(ff, Claude))
	w.Push(ff, "Coordinator says: hi")
	sendIs(t, w, T0, "")
	w.Up(ff, "/wt", PaneIdle, "brief", T0)
	sendIs(t, w, T0, "brief\n\nCoordinator says: hi")
	sendIs(t, w, T0, "", "busy until its turn ends")
}

func TestAStartWhoseIdleNeverComesIsTreatedAsIdleAfterTheWatchdog(t *testing.T) {
	for _, kind := range []AgentKind{Claude, Codex} {
		w := NewWorkers()
		w.Admit(plan(ff, kind))
		// A stale status at start: no Idle ever follows.
		w.Up(ff, "/wt", PaneWorking, "brief", T0)
		sendIs(t, w, T0, "")
		if len(w.DueRetries(T0+WorkerStartReadyMs-1)) != 0 {
			t.Fatal(kind)
		}
		eqv(t, w.DueRetries(T0+WorkerStartReadyMs), names(ff), kind.String())
		at := T0 + WorkerStartReadyMs
		sendIs(t, w, at, "brief", kind.String())
		// Checked by the delivery logic like any prompt.
		eqv(t, w.DueDeliveries(at+WorkerDeliveryMs), readD(ff), kind.String())
		if len(w.DueRetries(at+1)) != 0 {
			t.Fatal("once")
		}
	}
}

func TestTheStartWatchdogLeavesAWorkerThatBecameReadyAlone(t *testing.T) {
	w := upWorkers(Codex)
	sendIs(t, w, T0, "brief")
	if len(w.DueRetries(T0+WorkerStartReadyMs)) != 0 {
		t.Fatal("its brief already went out")
	}
}

func TestABlockedStartWaitsForIdle(t *testing.T) {
	w := NewWorkers()
	w.Admit(plan(ff, Codex))
	w.Up(ff, "/wt", PaneBlocked, "brief", T0)
	sendIs(t, w, T0, "")
	if !w.Get(ff).IsBlocked() {
		t.Fatal("blocked")
	}
	w.OnStatus(ff, PaneIdle, T0+1)
	sendIs(t, w, T0, "brief")
}

func TestMessagesWaitWhileTheWorkerWorksAndGoOutOnIdle(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneWorking, T0)
	w.Push(ff, "Coordinator says: use main")
	sendIs(t, w, T0, "")
	w.OnStatus(ff, PaneIdle, T0+5)
	sendIs(t, w, T0, "", "an Idle that beats the Stop is not ready yet")
	w.OnStop(ff, "done for now", T0+6)
	sendIs(t, w, T0, "Coordinator says: use main")
}

func TestATurnEndedByTheIdleFallbackReadsTheScreenNotEmptyWords(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneWorking, T0)
	w.OnStatus(ff, PaneIdle, T0+1)
	w.DueRetries(T0 + 1 + WorkerIdleWithoutStopMs)
	if n := len(w.TakeNotes()); n != 1 {
		t.Fatal(n)
	}
	at := T0 + 1 + WorkerIdleWithoutStopMs + ReportGraceMs
	eqv(t, w.DueSilent(at), readScreen(ff))
	if !w.TakeScreenPending(ff) {
		t.Fatal("screen pending")
	}
	w.OnStop(ff, "late", at)
	if n := len(w.TakeNotes()); n != 1 {
		t.Fatal("the stale Stop is noted")
	}
}

func TestAnIdleWithNoStopIsReadyAfterTenSeconds(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneWorking, T0)
	w.Push(ff, "next")
	w.OnStatus(ff, PaneIdle, T0+5)
	if len(w.DueRetries(T0+5+WorkerIdleWithoutStopMs-1)) != 0 {
		t.Fatal("early")
	}
	eqv(t, w.DueRetries(T0+5+WorkerIdleWithoutStopMs), names(ff))
	sendIs(t, w, T0, "next")
}

func TestALateStopAfterANewSendDoesNotArmAGrace(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneIdle, T0)
	w.DueRetries(T0 + WorkerIdleWithoutStopMs)
	w.OnReport(ff, T0)
	w.Push(ff, "next")
	// The lost Stop shows up after the Idle fallback but before the next send: ignored.
	w.OnStop(ff, "old words", T0+WorkerIdleWithoutStopMs+1)
	emptySilent(t, w.DueSilent(T0+WorkerIdleWithoutStopMs+1+ReportGraceMs))
	mustSend(t, w, T0)
	emptySilent(t, w.DueSilent(T0+10*ReportGraceMs), "no stale grace")
}

func TestAClaudeTurnWithoutAReportIsForwardedAfterTheGrace(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStop(ff, "I fixed it.", T0)
	emptySilent(t, w.DueSilent(T0+ReportGraceMs-1))
	eqv(t, w.DueSilent(T0+ReportGraceMs), said(ff, "I fixed it."))
	emptySilent(t, w.DueSilent(T0+2*ReportGraceMs), "once")
}

func TestAReportOrANewTurnCancelsTheGrace(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnReport(ff, T0)
	w.OnStop(ff, "done", T0)
	emptySilent(t, w.DueSilent(T0+ReportGraceMs), "reported during the turn")
	w = upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStop(ff, "thinking", T0)
	w.OnStatus(ff, PaneWorking, T0+10)
	emptySilent(t, w.DueSilent(T0+ReportGraceMs), "it went on working")
}

func TestACodexTurnEndsAtIdleAfterWorkingAndIsReadFromTheScreen(t *testing.T) {
	w := upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneIdle, T0)
	emptySilent(t, w.DueSilent(T0+ReportGraceMs), "an Idle before any Working is not the end")
	w.OnStatus(ff, PaneWorking, T0+1)
	w.OnStatus(ff, PaneDone, T0+2)
	eqv(t, w.DueSilent(T0+2+ReportGraceMs), readScreen(ff))
}

func TestAFailedSendIsRetriedThenDropped(t *testing.T) {
	w := upWorkers(Claude)
	text := mustSend(t, w, T0)
	if w.SendFailed(ff, text, T0) {
		t.Fatal("first")
	}
	if len(w.DueRetries(T0+WorkerRetryMs-1)) != 0 {
		t.Fatal("early")
	}
	eqv(t, w.DueRetries(T0+WorkerRetryMs), names(ff))
	text = mustSend(t, w, T0)
	if text != "brief" {
		t.Fatal(text)
	}
	if w.SendFailed(ff, text, T0) {
		t.Fatal("second")
	}
	w.DueRetries(T0 + 10*WorkerRetryMs)
	text = mustSend(t, w, T0)
	if !w.SendFailed(ff, text, T0) {
		t.Fatal("the third failure gives up")
	}
	w.DueRetries(T0 + 100*WorkerRetryMs)
	sendIs(t, w, T0, "", "queue dropped")
}

func TestALongBlockWithoutAPermissionHintsOnce(t *testing.T) {
	yes := func(string) bool { return true }
	no := func(string) bool { return false }
	w := upWorkers(Codex)
	w.OnStatus(ff, PaneBlocked, T0)
	if len(w.DueHints(T0+WorkerBlockedGraceMs, yes)) != 0 {
		t.Fatal("a pending permission explains it")
	}
	eqv(t, w.DueHints(T0+WorkerBlockedGraceMs, no), names(ff))
	if len(w.DueHints(T0+10*WorkerBlockedGraceMs, no)) != 0 {
		t.Fatal("once")
	}
	w.OnStatus(ff, PaneWorking, T0+1)
	w.OnStatus(ff, PaneBlocked, T0+2)
	if len(w.DueHints(T0+2+WorkerBlockedGraceMs, no)) != 1 {
		t.Fatal("a new block hints again")
	}
}

func TestAResumedClaudeTurnIsForwardedWhenItEndsAgain(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStop(ff, "first", T0)
	w.OnStatus(ff, PaneWorking, T0+1)
	w.OnStop(ff, "second", T0+2)
	eqv(t, w.DueSilent(T0+2+ReportGraceMs), said(ff, "second"))
}

func TestAResumedCodexTurnIsReadWhenItEndsAgain(t *testing.T) {
	w := upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneWorking, T0)
	w.OnStatus(ff, PaneDone, T0+1)
	w.OnStatus(ff, PaneWorking, T0+2)
	w.OnStatus(ff, PaneIdle, T0+3)
	eqv(t, w.DueSilent(T0+3+ReportGraceMs), readScreen(ff))
}

func TestGivingUpLeavesTheWorkerReadyForLaterMessages(t *testing.T) {
	w := upWorkers(Claude)
	for i := 0; i < 3; i++ {
		text := mustSend(t, w, T0)
		w.SendFailed(ff, text, T0)
		w.DueRetries(T0 + 100*WorkerRetryMs)
	}
	sendIs(t, w, T0, "")
	w.Push(ff, "later")
	sendIs(t, w, T0, "later")
}

func TestARetryDoesNotOverrideABusyPane(t *testing.T) {
	w := upWorkers(Claude)
	text := mustSend(t, w, T0)
	w.SendFailed(ff, text, T0)
	w.OnStatus(ff, PaneWorking, T0+1)
	if len(w.DueRetries(T0+10*WorkerRetryMs)) != 0 {
		t.Fatal("retry")
	}
	sendIs(t, w, T0, "")
	w.OnStatus(ff, PaneIdle, T0+20*WorkerRetryMs)
	sendIs(t, w, T0, "brief")
}

func TestAnIdleFlapDoesNotResetTheFailureCap(t *testing.T) {
	w := upWorkers(Claude)
	for i := Millis(0); i < 3; i++ {
		text := mustSend(t, w, T0)
		if got := w.SendFailed(ff, text, T0); got != (i == 2) {
			t.Fatal(i)
		}
		w.OnStatus(ff, PaneIdle, T0+i)
	}
}

func TestAdmitRefusesALiveNameAndUpQueuesOneBrief(t *testing.T) {
	w := upWorkers(Claude)
	if w.Admit(plan(ff, Codex)) {
		t.Fatal("admitted twice")
	}
	if w.Get(ff).Plan.Kind != Claude {
		t.Fatal("kind")
	}
	w.Up(ff, "/other", PaneIdle, "brief", T0)
	sendIs(t, w, T0, "brief")
	sendIs(t, w, T0, "")
}

func TestAReportedFollowUpTurnIsNotForwardedAgain(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStop(ff, "turn one", T0)
	w.Push(ff, "Coordinator says: go on")
	w.TakeSend(ff, T0)
	w.OnReport(ff, T0)
	w.OnStop(ff, "turn two", T0+10)
	emptySilent(t, w.DueSilent(T0+10+ReportGraceMs), "the report covered it")
	// A late Working after the report is only a new, still-open turn.
	w.OnStatus(ff, PaneWorking, T0+20)
	emptySilent(t, w.DueSilent(T0+20+ReportGraceMs))
}

func TestADuplicateStopAfterTheGraceFiredIsIgnored(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStop(ff, "done", T0)
	if len(w.DueSilent(T0+ReportGraceMs)) != 1 {
		t.Fatal("grace")
	}
	w.OnStop(ff, "done", T0+ReportGraceMs+1)
	emptySilent(t, w.DueSilent(T0+3*ReportGraceMs))
}

func TestTheInputBoxIsReadFromTheScreen(t *testing.T) {
	sent := "Rules: be good.\n\nYour task:\nFix the flaky upload test."
	codexEmpty := "╭──╮\n│ › Ask Codex to do anything │\n╰──╯"
	for _, c := range []struct {
		kind   AgentKind
		screen string
		sent   string
		want   InputState
	}{
		{Codex, codexEmpty, sent, InputEmpty},
		{Claude, codexEmpty, sent, InputUnknown}, // Codex's marker only
		{Codex, "› [Pasted Content 1234 chars]", sent, InputTyped},
		{Claude, "> [Pasted text #1 +4 lines]", sent, InputTyped},
		{Claude, "old output\n│ > Fix the flaky upload test. │", sent, InputTyped},
		{Codex, "• Working (3s)", sent, InputUnknown},
		{Codex, "", "", InputUnknown},
	} {
		if got := InputStateOf(c.kind, c.screen, c.sent); got != c.want {
			t.Errorf("%v %q: got %v want %v", c.kind, c.screen, got, c.want)
		}
	}
}

func TestAPromptWithNoSignOfLifeIsCheckedOnItsScreenThenGivenUp(t *testing.T) {
	w := upWorkers(Codex)
	brief := mustSend(t, w, T0)
	if len(w.DueDeliveries(T0+WorkerDeliveryMs-1)) != 0 {
		t.Fatal("early")
	}
	eqv(t, w.DueDeliveries(T0+WorkerDeliveryMs), readD(ff))
	if len(w.DueDeliveries(T0+10*WorkerDeliveryMs)) != 0 {
		t.Fatal("once, until the screen comes back")
	}
	at := T0 + WorkerDeliveryMs + 1
	screenIs(t, w, "› Ask Codex to do anything", at, Nudge{Kind: NudgeResend, Text: brief})
	if _, ok := w.OnDeliveryScreen(ff, "anything", at); ok {
		t.Fatal("only a read it asked for")
	}
	eqv(t, w.DueDeliveries(at+WorkerDeliveryMs), readD(ff))
	screenIs(t, w, "› [Pasted Content 900 chars]", at+WorkerDeliveryMs, Nudge{Kind: NudgeEnter})
	eqv(t, w.DueDeliveries(at+3*WorkerDeliveryMs), []WorkerDelivery{{Kind: DeliveryGaveUp, Name: ff}})
	if len(w.DueDeliveries(at+10*WorkerDeliveryMs)) != 0 {
		t.Fatal("once")
	}
	if MaxDeliveryNudges != 2 {
		t.Fatal(MaxDeliveryNudges)
	}
}

func TestABusyPaneCountsAsDeliveredEvenWithThePlaceholder(t *testing.T) {
	sent := "Your task:\nFix it."
	codex := "› Ask Codex to do anything\n• Working (5s • esc to interrupt)"
	if InputStateOf(Codex, codex, sent) != InputBusy {
		t.Fatal("codex")
	}
	if InputStateOf(Claude, "✻ Thinking… (3s · ↑ 1k tokens · esc to interrupt)\n> ", sent) != InputBusy {
		t.Fatal("thinking")
	}
	if InputStateOf(Claude, "✶ Pondering… (12s · esc)\n> Fix it.", sent) != InputBusy {
		t.Fatal("pondering")
	}
	w := upWorkers(Codex)
	w.TakeSend(ff, T0)
	if len(w.DueDeliveries(T0+WorkerDeliveryMs)) != 1 {
		t.Fatal("due")
	}
	screenIs(t, w, codex, T0+WorkerDeliveryMs, Nudge{Kind: NudgeBusy}, "no resend")
	if len(w.DueDeliveries(T0+10*WorkerDeliveryMs)) != 0 {
		t.Fatal("delivered: no more checks")
	}
}

func TestWorkingAStopOrAReportConfirmsDelivery(t *testing.T) {
	w := upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneWorking, T0+1)
	if len(w.DueDeliveries(T0+10*WorkerDeliveryMs)) != 0 {
		t.Fatal("working")
	}
	w = upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStop(ff, "done", T0+1)
	if len(w.DueDeliveries(T0+10*WorkerDeliveryMs)) != 0 {
		t.Fatal("a Stop")
	}
	w = upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnReport(ff, T0)
	if len(w.DueDeliveries(T0+10*WorkerDeliveryMs)) != 0 {
		t.Fatal("a report")
	}
	w = upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneIdle, T0+1)
	if len(w.DueDeliveries(T0+WorkerDeliveryMs)) != 1 {
		t.Fatal("an Idle proves nothing")
	}
}

func TestAFollowUpMessageIsCheckedTooAndAWaitCountsAsANudge(t *testing.T) {
	w := upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStop(ff, "done", T0+1)
	w.Push(ff, "Coordinator says: go on")
	w.TakeSend(ff, T0+2)
	if len(w.DueDeliveries(T0+2+WorkerDeliveryMs)) != 1 {
		t.Fatal("due")
	}
	screenIs(t, w, "something else", T0+3+WorkerDeliveryMs, Nudge{Kind: NudgeWait})
	// A failed send drops the check: the retry is a new send.
	w.SendFailed(ff, "Coordinator says: go on", T0+4+WorkerDeliveryMs)
	if len(w.DueDeliveries(T0+100*WorkerDeliveryMs)) != 0 {
		t.Fatal("no delivery checks")
	}
	w = upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.GetMut(ff).StopRequested = true
	if len(w.DueDeliveries(T0+WorkerDeliveryMs)) != 0 {
		t.Fatal("nothing for a worker being stopped")
	}
}

func TestCodexTrustAndInputScreensFromALiveRunAreReadRight(t *testing.T) {
	sent := "Your task:\nFix it."
	// Its "· esc quit" must not pass for a busy pane.
	if InputStateOf(Codex, codexTrustScreen, sent) != InputTrust || InputStateOf(Claude, codexTrustScreen, sent) != InputTrust {
		t.Fatal("trust")
	}
	afterTrust := "╭────────────────────────────────────╮\n│ >_ OpenAI Codex (v0.162.1)         │\n╰────────────────────────────────────╯\n\n› Ask Codex to do anything\n\n  ? for shortcuts"
	if InputStateOf(Codex, afterTrust, sent) != InputEmpty {
		t.Fatal("after trust")
	}
}

func TestACodexStatusFlipRightAfterTheSendIsNotDeliveryAndALostBriefIsReSent(t *testing.T) {
	w := upWorkers(Codex)
	brief := mustSend(t, w, T0)
	// Live: Herdr went idle, then done, within 1.5 s while the brief went into a trust screen.
	w.OnStatus(ff, PaneIdle, T0+500)
	w.OnStatus(ff, PaneDone, T0+1_000)
	emptySilent(t, w.DueSilent(T0+1_000+ReportGraceMs), "no turn ended")
	w.Push(ff, "Coordinator says: hello?")
	sendIs(t, w, T0+2_000, "", "not ready: the brief is still in flight")
	eqv(t, w.DueDeliveries(T0+WorkerDeliveryMs), readD(ff))
	screenIs(t, w, "› Ask Codex to do anything\n\n  ? for shortcuts", T0+WorkerDeliveryMs, Nudge{Kind: NudgeResend, Text: brief})
	// The re-sent brief arrives and the turn runs; its end is a real end.
	w.OnStatus(ff, PaneWorking, T0+WorkerDeliveryMs+1)
	w.OnStatus(ff, PaneDone, T0+WorkerDeliveryMs+2)
	sendIs(t, w, T0+WorkerDeliveryMs+3, "Coordinator says: hello?")
	// A Claude worker's Idle still makes it ready the old way (after its Stop).
	w = upWorkers(Claude)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneIdle, T0+1)
	w.OnStop(ff, "done", T0+2)
	w.Push(ff, "next")
	mustSend(t, w, T0+3)
}

func TestACodexScreenWithoutThePlaceholderAtTheDeadlineMeansItsPromptArrived(t *testing.T) {
	w := upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneDone, T0+1_000)
	if len(w.DueDeliveries(T0+WorkerDeliveryMs)) != 1 {
		t.Fatal("due")
	}
	screen := "› Fix it.\n\n• Added CODEX.txt and committed it.\n\n› Ask a follow-up question"
	screenIs(t, w, screen, T0+WorkerDeliveryMs, Nudge{Kind: NudgeArrived})
	if len(w.DueDeliveries(T0+10*WorkerDeliveryMs)) != 0 {
		t.Fatal("delivered: no more checks")
	}
	// The held Done stands now: the turn ended without a report, so it is forwarded.
	eqv(t, w.DueSilent(T0+WorkerDeliveryMs+ReportGraceMs), readScreen(ff))
	w.Push(ff, "next")
	mustSend(t, w, T0+WorkerDeliveryMs+1)
	// A trust screen at the deadline is never typed into: it waits (and is given up on).
	w = upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.DueDeliveries(T0 + WorkerDeliveryMs)
	screenIs(t, w, codexTrustScreen, T0+WorkerDeliveryMs, Nudge{Kind: NudgeWait})
	// A report confirms it too, and a held Done stands then.
	w = upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneDone, T0+1)
	w.OnReport(ff, T0)
	w.Push(ff, "next")
	mustSend(t, w, T0+2)
}

func TestCodexSMcpApprovalPromptIsNeverReadAsAnEmptyBox(t *testing.T) {
	// Codex 0.162 blocked on our own report tool (live run 8, before the approval override).
	prompt := "› Ask Codex to do anything\n\n  Allow the pixie MCP server to run tool report_progress?\n\n› 1. Allow\n  2. Allow for this session\n  3. Always allow\n  4. Cancel\n\n  Press enter to confirm"
	if InputStateOf(Codex, prompt, "Your task:\nFix it.") != InputBusy {
		t.Fatal("the prompt arrived; nothing is typed into it")
	}
	w := upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.DueDeliveries(T0 + WorkerDeliveryMs)
	screenIs(t, w, prompt, T0+WorkerDeliveryMs, Nudge{Kind: NudgeBusy}, "no re-send into it")
}

func TestABusyCodexWithAHeldDoneStaysBusyUntilAScreenShowsOtherwise(t *testing.T) {
	busy := "› Ask Codex to do anything\n• Working (25s • esc to interrupt)"
	w := upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneDone, T0+1_000)
	if len(w.DueDeliveries(T0+WorkerDeliveryMs)) != 1 {
		t.Fatal("due")
	}
	at := T0 + WorkerDeliveryMs
	screenIs(t, w, busy, at, Nudge{Kind: NudgeBusy})
	w.Push(ff, "Coordinator says: hello?")
	sendIs(t, w, at, "", "mid-turn: not ready")
	emptySilent(t, w.DueSilent(at+ReportGraceMs), "no grace mid-turn")
	// Re-read every WORKER_DELIVERY_MS while busy; never a nudge, never given up on.
	now := at
	for i := 0; i < 5; i++ {
		if len(w.DueDeliveries(now+WorkerDeliveryMs-1)) != 0 {
			t.Fatal("early")
		}
		now += WorkerDeliveryMs
		eqv(t, w.DueDeliveries(now), readD(ff))
		screenIs(t, w, busy, now, Nudge{Kind: NudgeBusy})
		sendIs(t, w, now, "")
	}
	now += WorkerDeliveryMs
	if len(w.DueDeliveries(now)) != 1 {
		t.Fatal("due")
	}
	screenIs(t, w, "• Added CODEX.txt.\n\n› Ask Codex to do anything", now, Nudge{Kind: NudgeArrived})
	sendIs(t, w, now, "Coordinator says: hello?", "the held Done stood: ready")
	// A new Done from Herdr releases it too, and so does a report.
	w = upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneDone, T0+1)
	w.DueDeliveries(T0 + WorkerDeliveryMs)
	w.OnDeliveryScreen(ff, busy, T0+WorkerDeliveryMs)
	w.OnStatus(ff, PaneDone, T0+WorkerDeliveryMs+5)
	eqv(t, w.DueSilent(T0+WorkerDeliveryMs+5+ReportGraceMs), readScreen(ff))
	if len(w.DueDeliveries(T0+10*WorkerDeliveryMs)) != 0 {
		t.Fatal("no more busy reads")
	}
	w = upWorkers(Codex)
	w.TakeSend(ff, T0)
	w.OnStatus(ff, PaneDone, T0+1)
	w.DueDeliveries(T0 + WorkerDeliveryMs)
	w.OnDeliveryScreen(ff, busy, T0+WorkerDeliveryMs)
	w.OnReport(ff, T0+WorkerDeliveryMs+5)
	w.Push(ff, "next")
	mustSend(t, w, T0+WorkerDeliveryMs+6)
}

func TestAWorkerBeingStoppedGetsNothing(t *testing.T) {
	w := upWorkers(Claude)
	w.GetMut(ff).StopRequested = true
	sendIs(t, w, T0, "")
	eqv(t, w.ForJob(JobIDFromBits(1)), names(ff))
	if w.Len() != 1 {
		t.Fatal("len")
	}
	if _, ok := w.Remove(ff); !ok {
		t.Fatal("remove")
	}
	if !w.IsEmpty() {
		t.Fatal("empty")
	}
}

func TestAKeptCodexSPreRenameMcpApprovalPromptIsStillBusy(t *testing.T) {
	// A Codex left running over the #68 rename still calls the server "mascot".
	prompt := "› Ask Codex to do anything\n\n  Allow the mascot MCP server to run tool report_progress?\n\n› 1. Allow\n  2. Allow for this session\n  3. Always allow\n  4. Cancel\n\n  Press enter to confirm"
	if InputStateOf(Codex, prompt, "Your task:\nFix it.") != InputBusy {
		t.Fatal("the prompt arrived; nothing is typed into it")
	}
}
