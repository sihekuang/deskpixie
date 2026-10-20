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
	items, ok := lastUpdates(t, stop(c, doneStop, T0))
	if !ok || len(items) != 1 || items[0].Text != "There are 12 open issues." || items[0].Job != job(1) {
		t.Fatalf("%+v", items)
	}
	if items[0].Kind != LineReply || items[0].At != T0 {
		t.Fatalf("%+v", items[0])
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
	items, ok := lastUpdates(t, c.Step(voiceOpened(), T0+1000))
	if !ok || len(items) != 0 {
		t.Fatalf("%v %+v", ok, items)
	}
}

func TestSeenLinesLeaveTheBubbleAndAreNotReplayed(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, stop(c, doneStop, T0))
	fx := c.Step(UpdatesSeenInput([]uint64{items[0].ID}), T0+1000)
	if got, ok := lastUpdates(t, fx); !ok || len(got) != 0 {
		t.Fatalf("%+v", got)
	}
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
	// The question leaves; the expiry's own line ("denied after two minutes") takes its place.
	if !ok || len(got) != 1 || got[0].ID == items[0].ID || got[0].Kind != LineInfo {
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
	stop(c, doneStop, T0)
	if got := c.Updates(); len(got) != 1 || got[0].Text != "There are 12 open issues." || got[0].Job != job(1) {
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
	if !ok || len(got) != 1 || got[0].ID == items[0].ID || got[0].Text != "Using main." {
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

// #199: a job's question, clicked away, leaves the bubble while its job still waits; only seen,
// it stays.
func TestAJobsQuestionClickedAwayLeavesTheBubble(t *testing.T) {
	c := workingOnJob1()
	items, _ := lastUpdates(t, stop(c, questionStop, T0))
	c.Step(UpdatesSeenInput([]uint64{items[0].ID}), T0+1000)
	if len(c.Updates()) != 1 {
		t.Fatal("seen only: it stays")
	}
	c.Step(UpdatesDismissedInput([]uint64{items[0].ID}), T0+2000)
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// #199: the notification for a line carries that line, so a click on it can do what a click
// on the line does.
func TestANotificationCarriesItsBubbleLine(t *testing.T) {
	c := workingOnJob1()
	fx := stop(c, doneStop, T0)
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
