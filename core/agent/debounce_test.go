package agent

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

// Debouncing what the user hears (debounce.go, Router's cap on updates).

func always(JobID) bool { return true }

func wantDue(t *testing.T, got []DueUpdate, want ...DueUpdate) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// ---------- Updates ----------

func TestAnIdenticalOrNearIdenticalReportIsNotSaidAgainWithinTheWindow(t *testing.T) {
	u := NewUpdates()
	if !u.AddReport(job(1), "w", "Worker says it's done: fixed it.", T0) {
		t.Fatal("first")
	}
	if u.AddReport(job(1), "w", "  worker SAYS it's done:   fixed it ", T0+1) {
		t.Fatal("near-identical while held")
	}
	wantDue(t, u.Due(T0+UpdateQuietMs, always), DueUpdate{Job: job(1), Text: "Worker says it's done: fixed it.", Worker: "w"})
	if u.AddReport(job(1), "w", "Worker says it's done: fixed it.", T0+UpdateDedupeMs-1) {
		t.Fatal("said within the window")
	}
	if !u.AddReport(job(2), "w", "Worker says it's done: fixed it.", T0+2) {
		t.Fatal("another job is not a repeat")
	}
	if !u.AddReport(job(1), "w", "Worker says it's done: fixed it.", T0+UpdateQuietMs+UpdateDedupeMs) {
		t.Fatal("after the window it is said again")
	}
}

func TestAJobsUpdatesAreHeldUntilQuietAndCoalesced(t *testing.T) {
	u := NewUpdates()
	u.AddProgress(job(1), "running tests", T0)
	u.AddReport(job(1), "a", "A is stuck: no login.", T0+1_000)
	u.AddReport(job(1), "b", "B says it's done: fixed.", T0+2_000)
	u.AddReport(job(1), "a", "A says it's done: logged in.", T0+3_000) // the latest per worker wins
	u.AddProgress(job(1), "writing the fix", T0+4_000)
	wantDue(t, u.Due(T0+4_000+UpdateQuietMs-1, always))
	wantDue(t, u.Due(T0+4_000+UpdateQuietMs, always),
		DueUpdate{Job: job(1), Text: "2 worker updates: A says it's done: logged in. B says it's done: fixed."})
	wantDue(t, u.Due(T0+UpdateMaxHoldMs*3, always))
}

func TestABusyJobIsHeardAfterTheMaxHold(t *testing.T) {
	u := NewUpdates()
	for now := T0; now < T0+UpdateMaxHoldMs; now += 5_000 {
		u.AddProgress(job(1), fmt.Sprintf("step %d", now), now)
		wantDue(t, u.Due(now, always))
	}
	wantDue(t, u.Due(T0+UpdateMaxHoldMs, always), DueUpdate{Job: job(1), Text: fmt.Sprintf("step %d", T0+UpdateMaxHoldMs-5_000), Progress: true})
}

func TestProgressOnlyWaitsUntilReady(t *testing.T) {
	u := NewUpdates()
	u.AddProgress(job(1), "running tests", T0)
	wantDue(t, u.Due(T0+UpdateQuietMs, func(JobID) bool { return false }))
	wantDue(t, u.Due(T0+UpdateQuietMs+1, always), DueUpdate{Job: job(1), Text: "running tests", Progress: true})
}

func TestSupersedeDropsWhatIsHeld(t *testing.T) {
	u := NewUpdates()
	u.AddReport(job(1), "a", "A says it's done.", T0)
	u.AddReport(job(2), "b", "B says it's done.", T0)
	u.Supersede(job(1))
	wantDue(t, u.Due(T0+UpdateQuietMs, always), DueUpdate{Job: job(2), Text: "B says it's done.", Worker: "b"})
}

// ---------- Router ----------

func fillCap(r *Router, open bool, now Millis) {
	for i := 0; i < 4; i++ {
		r.Route(open, reply("x"), now)
	}
}

func TestUpdatesOverTheCapAreHeldAndMergedWithTheConversationOpen(t *testing.T) {
	r := NewRouter(4)
	fillCap(r, true, 0)
	if got := r.RouteUpdate(true, NewLine(LineInfo, "A done."), 1); got != nil {
		t.Fatalf("held: %+v", got)
	}
	r.RouteUpdate(true, NewLine(LineInfo, "B done."), 2)
	// The coordinator's own lines are still said at once in a conversation.
	wantRoute(t, r.Route(true, reply("Checked."), 3), InjectDelivery(voice.Notice{Text: "Agent reply: Checked."}))
	if len(r.Tick(true, 59_999)) != 0 {
		t.Fatal("no slot yet")
	}
	got := r.Tick(true, 60_000)
	if len(got) != 1 || got[0].Kind != DeliverInject || got[0].Notice.Text != "Agent: 2 more updates. A done. B done." || len(got[0].Lines) != 2 {
		t.Fatalf("%+v", got)
	}
	if len(r.Tick(true, 60_001)) != 0 {
		t.Fatal("merged once")
	}
}

func TestUpdatesOverTheCapWithTheConversationClosedAreOutboxedAndCounted(t *testing.T) {
	r := NewRouter(4)
	fillCap(r, false, 0)
	u := NewLine(LineInfo, "A done.")
	wantRoute(t, r.RouteUpdate(false, u, 1), OutboxDelivery(u))
	if got := notifies(r.Tick(false, 60_000)); !reflect.DeepEqual(got, []string{"1 more update"}) {
		t.Fatal(got)
	}
}

func TestUpdatesHeldWhenTheConversationClosesAreOutboxed(t *testing.T) {
	r := NewRouter(4)
	fillCap(r, true, 0)
	u := NewLine(LineInfo, "A done.")
	r.RouteUpdate(true, u, 1)
	wantRoute(t, r.ConversationClosed(), OutboxDelivery(u))
	if got := notifies(r.Tick(false, 60_000)); !reflect.DeepEqual(got, []string{"1 more update"}) {
		t.Fatal(got)
	}
}

func TestQuestionsBypassTheCapInBothConversationStates(t *testing.T) {
	r := NewRouter(4)
	fillCap(r, true, 0)
	q := NewLine(LineQuestion, "Which branch?")
	wantRoute(t, r.RouteUpdate(true, q, 1), InjectDelivery(voice.Notice{Text: "Agent question: Which branch?"}))
	wantRoute(t, r.RouteUpdate(false, q, 1), ChimeDelivery(), NotifyDelivery("Which branch?"), OutboxDelivery(q))
}

// ---------- controller ----------

func injects(fx []Effect) []string {
	var out []string
	for _, e := range fx {
		if e.Kind == EfInject {
			out = append(out, e.Notice.Text)
		}
	}
	return out
}

func notifyTexts(fx []Effect) []string {
	var out []string
	for _, e := range fx {
		if e.Kind == EfNotify {
			out = append(out, e.Text)
		}
	}
	return out
}

func done(summary string) McpCall {
	return McpCall{Kind: CallReportResult, Outcome: OutcomeDone, Summary: summary, Evidence: "x"}
}

// twoWorkers: speakResults on, two running workers on job 1.
func twoWorkers(t *testing.T) *AgentController {
	t.Helper()
	c := speakingCtl()
	running(t, c)
	admit(t, c)
	up(c, workerName+"-2", PaneIdle)
	c.Step(paneStatus(workerName+"-2", PaneWorking), T0)
	return c
}

func TestARepeatedWorkerReportIsSaidOnce(t *testing.T) {
	c := speakingCtl()
	running(t, c)
	openVoice(c)
	var fx []Effect
	_, _, more := mcp(t, c, workerName, done("Fixed the flaky test"), T0)
	fx = append(fx, more...)
	fx = append(fx, c.Step(TickInput(), T0+UpdateQuietMs)...)
	_, _, more = mcp(t, c, workerName, done("Fixed the flaky test"), T0+UpdateQuietMs+1_000)
	fx = append(fx, more...)
	fx = append(fx, c.Step(TickInput(), T0+3*UpdateQuietMs)...)
	if got := injects(fx); !reflect.DeepEqual(got, []string{"The upload worker in focus says it's done: Fixed the flaky test."}) {
		t.Fatal(got)
	}
}

func TestTwoWorkersFinishingTogetherAreOneLine(t *testing.T) {
	c := twoWorkers(t)
	openVoice(c)
	mcp(t, c, workerName, done("Fixed the flaky test"), T0)
	mcp(t, c, workerName, McpCall{Kind: CallReportProgress, Summary: "tidying up"}, T0+1_000)
	mcp(t, c, workerName+"-2", done("Added a retry"), T0+5_000)
	if got := injects(c.Step(TickInput(), T0+5_000+UpdateQuietMs-1)); len(got) != 0 {
		t.Fatal(got)
	}
	got := injects(c.Step(TickInput(), T0+5_000+UpdateQuietMs))
	want := "2 worker updates: The upload worker in focus says it's done: Fixed the flaky test. The upload 2 worker in focus says it's done: Added a retry."
	if !reflect.DeepEqual(got, []string{want}) {
		t.Fatal(got)
	}
}

func TestTheCoordinatorsReplySupersedesAHeldWorkerReport(t *testing.T) {
	c := speakingCtl()
	running(t, c)
	openVoice(c)
	mcp(t, c, workerName, done("Fixed the flaky test"), T0)
	fx := stop(c, fmt.Sprintf(`<spoken ref="%s" status="working">The fix is in; I'm checking it.</spoken>`, job(1)), T0+5_000)
	fx = append(fx, c.Step(TickInput(), T0+5_000+UpdateQuietMs)...)
	if got := injects(fx); !reflect.DeepEqual(got, []string{"Agent reply: The fix is in; I'm checking it."}) {
		t.Fatal(got)
	}
}

func TestTheCoordinatorsReplyStillGoesAfterTheWorkerLineWasSaid(t *testing.T) {
	c := speakingCtl()
	running(t, c)
	openVoice(c)
	mcp(t, c, workerName, done("Fixed the flaky test"), T0)
	fx := c.Step(TickInput(), T0+UpdateQuietMs)
	fx = append(fx, stop(c, fmt.Sprintf(`<spoken ref="%s" status="done">Fixed and checked.</spoken>`, job(1)), T0+UpdateQuietMs+1)...)
	want := []string{"The upload worker in focus says it's done: Fixed the flaky test.", "Agent reply: Fixed and checked."}
	if got := injects(fx); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}

// fourReplies fills the cap (4 a minute) with the coordinator's lines for job 1.
func fourReplies(c *AgentController, now Millis) []Effect {
	var fx []Effect
	for i := 0; i < 4; i++ {
		fx = append(fx, stop(c, fmt.Sprintf(`<spoken ref="%s" status="working">Step %d.</spoken>`, job(1), i), now)...)
	}
	return fx
}

func TestTheCapCoversWorkerReportsWithTheConversationOpen(t *testing.T) {
	c := speakingCtl()
	running(t, c)
	openVoice(c)
	fourReplies(c, T0)
	mcp(t, c, workerName, done("Fixed the flaky test"), T0+1_000)
	if got := injects(c.Step(TickInput(), T0+1_000+UpdateQuietMs)); len(got) != 0 {
		t.Fatalf("over the cap: %v", got)
	}
	got := injects(c.Step(TickInput(), T0+NotifyWindowMs))
	if !reflect.DeepEqual(got, []string{"The upload worker in focus says it's done: Fixed the flaky test."}) {
		t.Fatal(got)
	}
}

func TestTheCapCoversNarrationWithTheConversationOpen(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	fourReplies(c, T0)
	c.Step(PostToolInput(Coordinator, "Bash", map[string]any{"description": "Run tests"}), T0+1_000)
	// Narration over the cap is dropped (it would be stale by the time a slot frees).
	var fx []Effect
	for now := T0 + 1_000; now <= T0+2*NotifyWindowMs; now += 1_000 {
		fx = append(fx, c.Step(TickInput(), now)...)
	}
	if got := injects(fx); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestTheCapCoversWorkerReportsWithTheConversationClosed(t *testing.T) {
	c := speakingCtl()
	running(t, c)
	fourReplies(c, T0)
	mcp(t, c, workerName, done("Fixed the flaky test"), T0+1_000)
	fx := c.Step(TickInput(), T0+1_000+UpdateQuietMs)
	if got := notifyTexts(fx); len(got) != 0 {
		t.Fatalf("over the cap: %v", got)
	}
	if !anyKind(fx, EfAppend) {
		t.Fatal("outboxed")
	}
	if got := notifyTexts(c.Step(TickInput(), T0+NotifyWindowMs)); !reflect.DeepEqual(got, []string{"1 more update"}) {
		t.Fatal(got)
	}
}

func TestHeldUpdatesGoToTheOutboxWhenTheConversationCloses(t *testing.T) {
	c := speakingCtl()
	running(t, c)
	openVoice(c)
	fourReplies(c, T0)
	mcp(t, c, workerName, done("Fixed the flaky test"), T0+1_000)
	c.Step(TickInput(), T0+1_000+UpdateQuietMs)
	fx := c.Step(voiceClosed(), T0+30_000)
	if !anyKind(fx, EfAppend) {
		t.Fatal(fmtEffects(fx))
	}
	if got := notifyTexts(c.Step(TickInput(), T0+NotifyWindowMs)); !reflect.DeepEqual(got, []string{"1 more update"}) {
		t.Fatal(got)
	}
	// Replayed when a conversation opens.
	got := injects(c.Step(voiceOpened(), T0+NotifyWindowMs+1))
	if len(got) != 1 || !strings.Contains(got[0], "Fixed the flaky test") {
		t.Fatal(got)
	}
}

func TestQuestionsAreNeverHeldByTheDebounceOrTheCap(t *testing.T) {
	for _, open := range []bool{true, false} {
		c := speakingCtl()
		running(t, c)
		if open {
			openVoice(c)
		}
		fourReplies(c, T0)
		mcp(t, c, workerName, done("Fixed the flaky test"), T0+1_000)
		fx := askPush(c, 7, T0+2_000)
		if open {
			wantContains(t, fx, eInject("Agent question: Run git push in coordinator? Say confirm hawk."))
		} else {
			wantContains(t, fx, eNotify("Run git push in coordinator? Say confirm hawk."))
		}
		fx = stop(c, fmt.Sprintf(`<spoken ref="%s" status="question">Which branch?</spoken>`, job(1)), T0+3_000)
		if open {
			wantContains(t, fx, eInject("Agent question: Which branch?"))
		} else {
			wantContains(t, fx, eNotify("Which branch?"))
		}
	}
}

func TestTheQuietTimeIsASettingAndTheMaxHoldFollowsIt(t *testing.T) {
	u := NewUpdatesWith(10_000)
	u.AddReport(job(1), "w", "first", T0)
	wantDue(t, u.Due(T0+9_999, always))
	wantDue(t, u.Due(T0+10_000, always), DueUpdate{Job: job(1), Text: "first", Worker: "w"})
	// Keeps reporting every 5 s: still heard at 1.5× = 15 s.
	u.AddReport(job(2), "w", "a", T0)
	u.AddReport(job(2), "w", "b", T0+5_000)
	u.AddReport(job(2), "w", "c", T0+10_000)
	wantDue(t, u.Due(T0+15_000, always), DueUpdate{Job: job(2), Text: "c", Worker: "w"})
}

func TestAZeroQuietTimeLetsEachUpdateOutAtOnce(t *testing.T) {
	u := NewUpdatesWith(0)
	u.AddReport(job(1), "w", "done", T0)
	wantDue(t, u.Due(T0, always), DueUpdate{Job: job(1), Text: "done", Worker: "w"})
	if u.AddReport(job(1), "w", "done", T0+1) {
		t.Fatal("dedupe still applies")
	}
}

func TestADueUpdateNamesTheWorkerOnlyWhenOneWorkerReported(t *testing.T) {
	u := NewUpdates()
	u.AddReport(job(1), "w", "W says it's done.", T0)
	u.AddReport(job(2), "a", "A says it's done.", T0)
	u.AddReport(job(2), "b", "B says it's done.", T0)
	wantDue(t, u.Due(T0+UpdateQuietMs, always),
		DueUpdate{Job: job(1), Text: "W says it's done.", Worker: "w"},
		DueUpdate{Job: job(2), Text: "2 worker updates: A says it's done. B says it's done."})
}

func TestTheReplayedDigestKeepsEachLinesJobAndWorker(t *testing.T) {
	c := speakingCtl()
	running(t, c)
	l := NewJobLine(LineInfo, "The upload worker says it's done.", job(7))
	l.Worker = "w-upload"
	c.outbox.Push(l, T0)
	c.Step(voiceOpened(), T0+1)
	last := c.injected[len(c.injected)-1].lines
	if len(last) != 1 || last[0].Job != job(7) || last[0].Worker != "w-upload" {
		t.Fatalf("%+v", last)
	}
}
