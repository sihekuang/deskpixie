package agent

import (
	"encoding/json"
	"testing"
)

// The updates bubble (bubble.go).

func lastUpdates(t *testing.T, fx []Effect) ([]UpdateItem, bool) {
	t.Helper()
	var out []UpdateItem
	found := false
	for _, e := range fx {
		if e.Kind == EfUpdates {
			out, found = e.Value.([]UpdateItem), true
		}
	}
	return out, found
}

func TestAnOutboxedLineIsSentToTheBubbleWithItsJob(t *testing.T) {
	c := workingOnJob1()
	// #286: a reply is not a bubble line; a job's question is, with its job.
	if items, _ := lastUpdates(t, stop(c, doneStop, T0)); len(items) != 0 {
		t.Fatalf("%+v", items)
	}
	items, ok := lastUpdates(t, stop(workingOnJob1(), questionStop, T0+10))
	if !ok || len(items) != 1 || items[0].Text != "Which branch?" || items[0].Job != job(1) || items[0].Kind != LineQuestion || items[0].At != T0+10 {
		t.Fatalf("%+v", items)
	}
}

func TestTheFirstStepSendsAnEmptyListAsAnEmptyArray(t *testing.T) {
	items, ok := lastUpdates(t, ctl().Step(TickInput(), T0))
	if !ok || items == nil || len(items) != 0 {
		t.Fatalf("%v %#v", ok, items)
	}
	b, _ := json.Marshal(items)
	if string(b) != "[]" {
		t.Fatal(string(b))
	}
}

func TestAnUpdateItemSerializesForTheShell(t *testing.T) {
	b, _ := json.Marshal(UpdateItem{ID: 3, Text: "Done.", Kind: LineQuestion, At: 42, Job: job(1), Worker: "w"})
	want := `{"id":3,"text":"Done.","kind":"question","at":42,"job":"job_000000000001","worker":"w"}`
	if string(b) != want {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(UpdateItem{ID: 3, Text: "Hi", Kind: LineInfo, At: 42})
	if want := `{"id":3,"text":"Hi","kind":"info","at":42}`; string(b) != want {
		t.Fatal(string(b))
	}
}

func TestAnUnchangedListIsNotSentAgain(t *testing.T) {
	c := workingOnJob1()
	stop(c, doneStop, T0)
	if _, ok := lastUpdates(t, c.Step(TickInput(), T0+1000)); ok {
		t.Fatal("re-sent")
	}
}

func TestOpeningAConversationEmptiesTheBubble(t *testing.T) {
	c := workingOnJob1()
	stop(c, doneStop, T0)
	// #286: the done line was never in the bubble, and opening a conversation leaves it empty.
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	c.Step(voiceOpened(), T0+1000)
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestSeenLinesLeaveTheBubbleAndAreNotReplayed(t *testing.T) {
	c := workingOnJob1()
	// #286: a reply is never in the bubble; seeing a line that was (a handed-off question) marks
	// it heard.
	if got, _ := lastUpdates(t, stop(c, doneStop, T0)); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	id := c.outbox.lines[len(c.outbox.lines)-1].ID
	fx := c.Step(UpdatesSeenInput([]uint64{id}), T0+1000)
	if !anyKind(fx, EfAppend) {
		t.Fatal("the delivered mark is not saved")
	}
	wantNone(t, visible(c.Step(voiceOpened(), T0+2000)), "seen = heard")
}

func TestAQuestionStillWaitingStaysAfterBeingSeen(t *testing.T) {
	c := workingOnJob1()
	q := `<spoken ref="job_000000000001" status="question">Which branch?</spoken>`
	items, _ := lastUpdates(t, stop(c, q, T0))
	if len(items) != 1 || items[0].Kind != LineQuestion {
		t.Fatalf("%+v", items)
	}
	fx := c.Step(UpdatesSeenInput([]uint64{items[0].ID}), T0+1000)
	if _, ok := lastUpdates(t, fx); ok {
		t.Fatal("the question should still be listed (list unchanged)")
	}
}

func TestAPendingPermissionQuestionStaysAfterBeingSeenAndLeavesOnceExpired(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, askPush(c, 7, T0))
	if len(items) != 1 || items[0].Kind != LineQuestion || items[0].Job != job(1) {
		t.Fatalf("%+v", items)
	}
	if _, ok := lastUpdates(t, c.Step(UpdatesSeenInput([]uint64{items[0].ID}), T0+1000)); ok {
		t.Fatal("a pending permission question was cleared by viewing")
	}
	got, ok := lastUpdates(t, c.Step(TickInput(), T0+DefaultSettings().PermissionTimeoutMs+1000))
	// The question leaves; the expiry's own line ("denied after two minutes") is info, not a bubble line (#286).
	if !ok || len(got) != 0 {
		t.Fatalf("an expired permission question is still listed: %v %+v", ok, got)
	}
}

func TestNoFeedWhenBitOpensConversationsForUpdates(t *testing.T) {
	c := openingCtl()
	if _, ok := lastUpdates(t, stop(c, doneStop, T0)); ok {
		t.Fatal("fed the bubble with openConversationForUpdates on")
	}
}

func TestUpdatesIsTheCurrentListForAShellThatMissedTheEvent(t *testing.T) {
	c := workingOnJob1()
	if got := c.Updates(); got == nil || len(got) != 0 {
		t.Fatalf("nothing yet is an empty list: %#v", got)
	}
	stop(c, doneStop, T0) // #286: a reply is not in the list
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	c = workingOnJob1()
	stop(c, questionStop, T0)
	if got := c.Updates(); len(got) != 1 || got[0].Text != "Which branch?" || got[0].Job != job(1) {
		t.Fatalf("%+v", got)
	}
}

func TestUpdatesIsEmptyWhenBitOpensConversationsForUpdates(t *testing.T) {
	c := openingCtl()
	stop(c, doneStop, T0)
	if got := c.Updates(); got == nil || len(got) != 0 {
		t.Fatalf("%#v", got)
	}
}

func TestAwayHoldsTheOpenAndTheChimeUntilBack(t *testing.T) {
	c := openingCtl()
	c.Step(PresenceInput(true), T0)
	fx := stop(c, doneStop, T0+10)
	if opens(fx) != 0 || anyKind(fx, EfChime) {
		t.Fatal(fmtEffects(fx))
	}
	if !anyKind(fx, EfNotify) {
		t.Fatal("the notification still goes to Notification Center")
	}
	for at := T0 + 1000; at < T0+30_000; at += 1000 {
		if opens(c.Step(TickInput(), at)) != 0 {
			t.Fatal("opened while away")
		}
	}
	if opens(c.Step(PresenceInput(false), T0+60_000)) != 1 {
		t.Fatal("did not open on return")
	}
}

func TestBackWithNothingNewOpensNothing(t *testing.T) {
	c := openingCtl()
	c.Step(PresenceInput(true), T0)
	if opens(c.Step(PresenceInput(false), T0+60_000)) != 0 {
		t.Fatal("opened with nothing to say")
	}
}

func TestFocusTargetPrefersTheLiveWorkerThenTheCoordinator(t *testing.T) {
	c := wctl()
	if got := c.FocusTarget(job(1), "nobody"); got != Coordinator {
		t.Fatal(got)
	}
	if got := c.FocusTarget("", ""); got != Coordinator {
		t.Fatal(got)
	}
	admit(t, c)
	if got := c.FocusTarget(job(1), workerName); got != Coordinator {
		t.Fatal("a worker still starting has no pane to focus:", got)
	}
	up(c, workerName, PaneIdle)
	if got := c.FocusTarget(job(1), ""); got != workerName {
		t.Fatal(got)
	}
	if got := c.FocusTarget(job(1), workerName); got != workerName {
		t.Fatal(got)
	}
	if got := c.FocusTarget(job(1), "gone"); got != workerName {
		t.Fatal(got)
	}
	if got := c.FocusTarget(job(2), ""); got != Coordinator {
		t.Fatal(got)
	}
}

const questionStop = `<spoken ref="job_000000000001" status="question">Which branch?</spoken>`

func TestAQuestionAnsweredInThePaneLeavesTheBubbleAndIsSavedAsDelivered(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, stop(c, questionStop, T0))
	if len(items) != 1 || items[0].Kind != LineQuestion {
		t.Fatalf("%+v", items)
	}
	// The user answered in the herdr pane; the job moved on.
	fx := stop(c, `<spoken ref="job_000000000001" status="working">Using main.</spoken>`, T0+1000)
	got, ok := lastUpdates(t, fx)
	if !ok || len(got) != 0 { // #286: "Using main." is a reply, not a bubble line
		t.Fatalf("the answered question is still listed: %v %+v", ok, got)
	}
	if !anyKind(fx, EfAppend) {
		t.Fatal("the delivered mark is not saved")
	}
	for _, l := range c.outbox.Undelivered() {
		if l.ID == items[0].ID {
			t.Fatal("the answered question is still undelivered")
		}
	}
}

func TestAQuestionLeavesTheBubbleWhenItsJobEnds(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, stop(c, questionStop, T0))
	c.Step(CancelInput(job(1)), T0+1000)
	for _, g := range c.Updates() {
		if g.ID == items[0].ID {
			t.Fatalf("the question of a cancelled job is still listed: %+v", g)
		}
	}
}

func TestAQuestionStaysWhileItsJobIsStillWaiting(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, stop(c, questionStop, T0))
	c.Step(TickInput(), T0+1000)
	if got := c.Updates(); len(got) != 1 || got[0].ID != items[0].ID {
		t.Fatalf("%+v", got)
	}
}

func TestAPermissionQuestionKeepsItsTokenRuleWhenTheJobMovesOn(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, askPush(c, 7, T0))
	// The job leaves Question while the permission is still pending: the token decides.
	_, _ = c.jobs.Transition(job(1), JobWorking, T0+500)
	c.Step(TickInput(), T0+1000)
	found := false
	for _, g := range c.Updates() {
		found = found || g.ID == items[0].ID
	}
	if !found {
		t.Fatal("a pending permission question left with its job's status")
	}
}

// #97: Allow and Deny must be reachable whatever Pixie did with the question, so a permission
// question still waiting stays listed after Pixie reads it out and when Pixie opens
// conversations for updates.
func TestAPendingPermissionQuestionStaysAfterPixieReadsItOut(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, askPush(c, 7, T0))
	c.Step(voiceOpened(), T0+1000)
	if got := c.Updates(); len(got) != 1 || got[0].ID != items[0].ID || got[0].Token == "" {
		t.Fatalf("a read-out permission question left the bubble: %+v", got)
	}
}

func TestAPendingPermissionQuestionIsListedWhenBitOpensConversationsForUpdates(t *testing.T) {
	c := openingCtl()
	askPush(c, 7, T0)
	got := c.Updates()
	if len(got) != 1 || got[0].Kind != LineQuestion || got[0].Token == "" {
		t.Fatalf("%+v", got)
	}
	c.Step(TickInput(), T0+DefaultSettings().PermissionTimeoutMs+1000)
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("an expired question, or another line, is listed: %+v", got)
	}
}

func TestAPermissionQuestionAskedDuringAConversationIsListed(t *testing.T) {
	c := workingOnJob1()
	c.Step(voiceOpened(), T0)
	askPush(c, 7, T0+1000)
	if got := c.Updates(); len(got) != 1 || got[0].Token == "" {
		t.Fatalf("%+v", got)
	}
}

// #199, #286: a job's question stays in the bubble while its job still waits, seen or clicked
// away; it leaves when the job moves on.
func TestAJobsQuestionClickedAwayStaysWhileItsJobWaits(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, stop(c, questionStop, T0))
	c.Step(UpdatesSeenInput([]uint64{items[0].ID}), T0+1000)
	if len(c.Updates()) != 1 {
		t.Fatal("seen only: it stays")
	}
	c.Step(UpdatesDismissedInput([]uint64{items[0].ID}), T0+2000)
	if got := c.Updates(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

// #199: the notification for a line carries that line, so a click on it can do what a click
// on the line does.
func TestANotificationCarriesItsBubbleLine(t *testing.T) {
	c := workingOnJob1()
	fx := stop(c, questionStop, T0) // #286: a done line has no bubble line
	items, _ := lastUpdates(t, fx)
	var notes []Effect
	for _, e := range fx {
		if e.Kind == EfNotify {
			notes = append(notes, e)
		}
	}
	if len(notes) != 1 || notes[0].Update == nil || notes[0].Update.ID != items[0].ID || notes[0].Update.Job != job(1) {
		t.Fatalf("%+v", notes)
	}
}

// #283/#286: with openConversationForUpdates on and text mode, an update reaches no bubble
// (it's a reply card) and a question does.
func TestTheBubbleHoldsOnlyWhatNeedsYou(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true })
	c.Step(TextModeInput(true), T0)
	fx := sessionTurn(c, longTurn, T0+10) // "…is done": a reply
	if items, _ := lastUpdates(t, fx); len(items) != 0 {
		t.Fatalf("a done line in the bubble: %+v", items)
	}
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	items, _ := lastUpdates(t, sessionTurn(c, asking, T0+20))
	if len(items) != 1 || !items[0].Asking {
		t.Fatalf("%+v", items)
	}
}

func TestSeeingAQuestionDoesntTakeItOffTheBadge(t *testing.T) {
	c := turnCtl(nil)
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	items, _ := lastUpdates(t, sessionTurn(c, asking, T0))
	c.Step(UpdatesSeenInput([]uint64{items[0].ID}), T0+10)
	if got := c.Updates(); len(got) != 1 {
		t.Fatalf("seen cleared it: %+v", got)
	}
}

func TestASessionsNextTurnTakesItsQuestionOffTheBadge(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	sessionTurn(c, asking, T0)
	short := longTurn
	short.Duration = 1_000 // not told, but it moved on
	sessionTurn(c, short, T0+10)
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// #361: you answered the session (your reply passed on, or typed in its terminal) and it is
// working on it, its next turn: its question leaves the badge then, not once that turn ends.
// Another session working, or this one blocked, leaves it.
func TestASessionsNextTurnStartingTakesItsQuestionOffTheBadge(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	sessionTurn(c, asking, T0)
	c.Step(paneStatus("api-claude", PaneIdle), T0+1)
	c.Step(paneStatus("web-claude", PaneIdle), T0+2)
	c.Step(paneStatus("web-claude", PaneWorking), T0+3)
	if got := c.Updates(); len(got) != 1 {
		t.Fatalf("another session's turn took it: %+v", got)
	}
	c.Step(paneStatus("api-claude", PaneWorking), T0+4)
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("working on your answer, still on the badge: %+v", got)
	}
}

// Working after blocked is only a dialog answered (a background subagent's permission, say),
// not your answer to what the turn asked.
func TestADialogAnsweredAfterTheTurnKeepsItsQuestion(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	sessionTurn(c, asking, T0)
	c.Step(paneStatus("api-claude", PaneIdle), T0+1)
	c.Step(paneStatus("api-claude", PaneBlocked), T0+2)
	c.Step(paneStatus("api-claude", PaneWorking), T0+3)
	if got := c.Updates(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

// After a restart pix tells its session's status again, which says nothing of a new turn: its
// open question stays (a Codex menu waits while it works).
func TestARestartsFirstStatusKeepsTheOpenQuestion(t *testing.T) {
	ask := OutboxLine{Kind: LineSession, Session: "api-claude", Asking: true, Text: "api: asks"}
	c := restoredCtl(ask)
	c.Step(paneStatus("api-claude", PaneWorking), T0+1)
	if got := c.Updates(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestAlertingLanes(t *testing.T) {
	tok := "hawk"
	for _, tc := range []struct {
		l    OutboxLine
		want bool
	}{
		{OutboxLine{Kind: LineQuestion, Token: &tok}, true},
		{OutboxLine{Kind: LineQuestion, Job: job(1)}, true},
		{OutboxLine{Kind: LineSession, Asking: true}, true},
		{OutboxLine{Kind: LineSession}, false},
		{OutboxLine{Kind: LineReply}, false},
		{OutboxLine{Kind: LineInfo}, false},
		{OutboxLine{Kind: LineUnsolicited}, false},
	} {
		if alerting(tc.l) != tc.want {
			t.Fatalf("%+v", tc.l)
		}
	}
}

// restoredCtl: a controller restored from these outbox lines (all undelivered).
func restoredCtl(lines ...OutboxLine) *AgentController {
	var recs []string
	for i := range lines {
		lines[i].ID, lines[i].At = uint64(i+1), T0
		recs = append(recs, lines[i].record())
	}
	c := ctl()
	c.Step(RestoredInput(nil, recs, true), T0)
	return c
}

// #286: a restart keeps a session's open question on the badge, until that session says
// something else; an info line that only carries the session (an "allowed" line) doesn't clear it.
func TestARestartKeepsASessionsOpenQuestionOnTheBadge(t *testing.T) {
	ask := OutboxLine{Kind: LineSession, Session: "api-claude", Asking: true, Text: "api: asks"}
	if got := restoredCtl(ask).Updates(); len(got) != 1 || !got[0].Asking {
		t.Fatalf("%+v", got)
	}
	next := OutboxLine{Kind: LineSession, Session: "api-claude", Text: "api is done"}
	if got := restoredCtl(ask, next).Updates(); len(got) != 0 {
		t.Fatalf("a later turn left it open: %+v", got)
	}
	info := OutboxLine{Kind: LineInfo, Session: "api-claude", Text: "api: allowed"}
	if got := restoredCtl(ask, info).Updates(); len(got) != 1 {
		t.Fatalf("an info line dropped the question: %+v", got)
	}
}
