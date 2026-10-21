package agent

import (
	"strings"
	"testing"
)

// #221: a message typed in the text box is a job like a delegation, but its spoken lines go to
// the box (EfChat), never spoken, notified, chimed or added to the updates bubble.

func typedOnJob1() *AgentController {
	c := ctl()
	c.Step(TypedDelegateInput(job(1), "how are my workers doing?", ""), T0)
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	return c
}

func chats(fx []Effect) []ChatLine {
	var out []ChatLine
	for _, e := range fx {
		if e.Kind == EfChat {
			out = append(out, e.Chat)
		}
	}
	return out
}

func wantChat(t *testing.T, fx []Effect, want ...ChatLine) {
	t.Helper()
	got := chats(fx)
	if len(got) != len(want) {
		t.Fatalf("chat lines %v, want %v (%s)", got, want, fmtEffects(fx))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("chat lines %v, want %v", got, want)
		}
	}
}

// noUsualRoute: nothing of the line was spoken, notified, chimed or put in the outbox.
func noUsualRoute(t *testing.T, fx []Effect) {
	t.Helper()
	for _, e := range fx {
		switch {
		case e.Kind == EfInject, e.Kind == EfNotify, e.Kind == EfChime, e.Kind == EfOpenConversation,
			e.Kind == EfAppend && e.File == OutboxFile:
			t.Fatalf("usual route taken: %s", fmtEffects(fx))
		}
	}
}

func TestATypedJobsReplyGoesToTheCard(t *testing.T) {
	c := typedOnJob1()
	fx := stop(c, `<spoken ref="job_000000000001" status="done">Two are running.</spoken>`, T0+10)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "done", Text: "Two are running."})
	noUsualRoute(t, fx)
	wantJobStatus(t, c, 1, JobDone)
}

func TestATypedJobsReplyIsNotSpokenEvenWithVoiceOpen(t *testing.T) {
	c := typedOnJob1()
	openVoice(c)
	fx := stop(c, `<spoken ref="job_000000000001" status="done">Two are running.</spoken>`, T0+10)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "done", Text: "Two are running."})
	noUsualRoute(t, fx)
}

func TestATypedJobsProgressAndQuestionGoToTheCard(t *testing.T) {
	c := typedOnJob1()
	fx := stop(c, `<spoken ref="job_000000000001" status="working">Looking now.</spoken>`, T0+10)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "working", Text: "Looking now."})
	noUsualRoute(t, fx)
	fx = stop(c, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`, T0+30)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "question", Text: "Which repo?"})
	noUsualRoute(t, fx)
}

func TestATypedJobThatFailsOrStopsSaysSoInTheCard(t *testing.T) {
	c := typedOnJob1()
	fx := stop(c, `<spoken ref="job_000000000001" status="failed">No access.</spoken>`, T0+10)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "failed", Text: "No access."})
	noUsualRoute(t, fx)

	c = typedOnJob1()
	fx = c.Step(CoordinatorFailedInput(HostError{Kind: HostGaveUp}), T0+10)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "failed", Text: "The coordinator stopped before finishing: how are my workers doing?"})
}

func TestATypedPermissionRequestAlsoKeepsItsUsualRoute(t *testing.T) {
	c := typedOnJob1()
	fx := askPush(c, 7, T0+10)
	got := chats(fx)
	if len(got) != 1 || got[0].Status != "question" || got[0].Job != job(1) {
		t.Fatalf("%v", got)
	}
	if !strings.HasSuffix(got[0].Text, permissionHint) || strings.Contains(got[0].Text, "Say confirm") {
		t.Fatalf("the box says where to allow it, and not to say a word: %q", got[0].Text)
	}
	usual := false
	for _, e := range fx {
		if e.Kind == EfNotify || e.Kind == EfAppend && e.File == OutboxFile {
			usual = true
		}
	}
	if !usual {
		t.Fatalf("a permission request must still reach the bubble: %s", fmtEffects(fx))
	}
}

func TestAnUntypedJobIsUnchanged(t *testing.T) {
	c := workingOnJob1()
	fx := stop(c, `<spoken ref="job_000000000001" status="done">All done.</spoken>`, T0+10)
	if len(chats(fx)) != 0 {
		t.Fatal(fmtEffects(fx))
	}
	wantContains(t, fx, eNotify("All done."))
}

func TestTextModeOpensNoConversationForUpdates(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true })
	delegate(c, 1, "summarise the issues")
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	c.Step(TextModeInput(true), T0)
	fx := stop(c, `<spoken ref="job_000000000001" status="done">All done.</spoken>`, T0+10)
	for _, e := range fx {
		if e.Kind == EfOpenConversation {
			t.Fatal("opened a voice conversation in text mode")
		}
	}
	c.Step(TextModeInput(false), T0+20)
	delegate(c, 2, "b")
	fx = stop(c, `<spoken ref="job_000000000002" status="done">B done.</spoken>`, T0+40)
	wantContains(t, fx, Effect{Kind: EfOpenConversation}, "voice mode opens as before")
}

func TestTheLastTypedJobIsKnown(t *testing.T) {
	c := typedOnJob1()
	if c.LastTyped() != job(1) {
		t.Fatal(c.LastTyped())
	}
	delegate(c, 2, "by voice")
	if c.LastTyped() != job(1) {
		t.Fatal("a voice delegation isn't typed")
	}
}

// Review #1: with openConversationForUpdates on, a typed job's progress still goes to the box.
func TestATypedJobsProgressGoesToTheCardWithTheOpenFlagOn(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true })
	c.Step(TypedDelegateInput(job(1), "a slow one", ""), T0)
	c.Step(CoordinatorUpInput(PaneIdle), T0)
	fx := stop(c, `<spoken ref="job_000000000001" status="working">Halfway.</spoken>`, T0+10)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "working", Text: "Halfway."})
	noUsualRoute(t, fx)
}

// Review #2: after Stop, the coordinator's confirmation of the cancel isn't notified.
func TestTheCoordinatorsReplyToAStoppedTypedJobIsDropped(t *testing.T) {
	for _, reply := range []string{
		`Stopped.`, // no block: unreferenced
		`<spoken ref="job_000000000001" status="done">Okay, stopped.</spoken>`, // its closed job
	} {
		c := typedOnJob1()
		c.Step(CancelInput(job(1)), T0+10)
		c.Step(coordStatus(PaneIdle), T0+20)
		fx := stop(c, reply, T0+30)
		noUsualRoute(t, fx)
		if len(chats(fx)) != 0 {
			t.Fatal(reply, fmtEffects(fx))
		}
	}
}

// #231: a typed message the coordinator hands to one of your sessions ends in the box: the
// hand-off shows while the job is open, and the job's close tells the box it's done, even
// though the coordinator's reply only repeats the hand-off (#177).
func TestATypedJobHandedToASessionStopsThinkingWhenItCloses(t *testing.T) {
	c := typedOnJob1()
	passed := "Passed on to your session on " + landing + "."
	wantChat(t, c.Step(SessionSentInput("repo-claude", landing), T0+5), ChatLine{Job: job(1), Status: "working", Text: passed})
	fx := stop(c, `<spoken ref="job_000000000001" status="done">I sent your words to the landing page session.</spoken>`, T0+10)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "done", Text: passed})
	noUsualRoute(t, fx)
	wantJobStatus(t, c, 1, JobDone)
}

// #231 review: the done line is the hand-off as the box first showed it, confirm words masked.
func TestATypedHandOffsDoneLineKeepsPendingConfirmWordsMasked(t *testing.T) {
	c := typedOnJob1()
	askPush(c, 7, T0+1) // "hawk" is pending
	working := chats(c.Step(SessionSentInput("repo-claude", "say hawk to ship"), T0+5))
	fx := stop(c, `<spoken ref="job_000000000001" status="done">Passed it on.</spoken>`, T0+10)
	done := chats(fx)
	if len(working) != 1 || len(done) != 1 || done[0].Status != "done" || done[0].Text != working[0].Text ||
		strings.Contains(strings.ToLower(done[0].Text), "hawk") {
		t.Fatalf("working %v, done %v", working, done)
	}
}
