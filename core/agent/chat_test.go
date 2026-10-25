package agent

import (
	"slices"
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
		// The sessions a line is about are checked on their own (#261).
		if got[i].Job != want[i].Job || got[i].Status != want[i].Status || got[i].Text != want[i].Text {
			t.Fatalf("chat lines %v, want %v", got, want)
		}
	}
}

// notSaid: nothing of the line was spoken, notified or chimed, and no conversation opened for it.
func notSaid(t *testing.T, fx []Effect) {
	t.Helper()
	if anyKind(fx, EfInject, EfNotify, EfChime, EfOpenConversation) {
		t.Fatalf("said: %s", fmtEffects(fx))
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
	notSaid(t, fx) // kept as heard, so it waits on the badge too (#286 call 7)
	if !chats(fx)[0].Asking {
		t.Fatal("the question itself is asking") // #302
	}
}

// #302: a line on a job waiting on its question has the job's status, but isn't the question.
func TestAnInfoLineOnAWaitingTypedJobIsNotAsking(t *testing.T) {
	c := typedOnJob1()
	stop(c, `<spoken ref="job_000000000001" status="question">Which repo?</spoken>`, T0+30)
	l := c.chatLine(NewJobLine(LineInfo, "Waiting at a prompt in its terminal.", job(1)))
	if l.Status != "question" || l.Asking {
		t.Fatalf("%+v", l)
	}
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

// #241: a typed message handed to one of your sessions gets that session's reply in the box,
// while the box still shows it; it is not spoken, notified or put in the outbox.

// handedOffTyped: job 1, typed in text mode, was passed to the session api-claude and closed.
func handedOffTyped(t *testing.T) *AgentController {
	t.Helper()
	c := typedOnJob1()
	c.Step(TextModeInput(true), T0)
	c.Step(SessionSentInput("api-claude", landing), T0+5)
	stop(c, `<spoken ref="job_000000000001" status="done">Passed it on.</spoken>`, T0+10)
	wantJobStatus(t, c, 1, JobDone)
	return c
}

func pixieTurn(reply string) SessionTurn {
	return SessionTurn{Label: "deskpixie: fix login redirect", Reply: reply, App: webstorm, Duration: 45_000, FromPixie: true}
}

func TestASessionsReplyToATypedMessageGoesToTheBox(t *testing.T) {
	c := handedOffTyped(t)
	fx := sessionTurn(c, pixieTurn("The redirect loop is fixed and all 412 tests pass."), T0+20)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "done", Text: "The redirect loop is fixed and all 412 tests pass."})
	noUsualRoute(t, fx)
	if got := chats(fx)[0].Session; got != "api-claude" {
		t.Fatalf("the session's own turn, not the coordinator's: Session = %q", got) // #302
	}
	// Its later turns too (a background task it waited on), even ones Pixie didn't prompt.
	later := pixieTurn("Also pushed the branch.")
	later.FromPixie = false
	fx = sessionTurn(c, later, T0+30)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "done", Text: "Also pushed the branch."})
	noUsualRoute(t, fx)
}

func TestASessionsQuestionAboutATypedMessageAsksInTheBox(t *testing.T) {
	c := handedOffTyped(t)
	fx := sessionTurn(c, pixieTurn("Should I also fix the logout page?"), T0+20)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "question", Text: "Should I also fix the logout page?"})
	notSaid(t, fx) // kept as heard, so it waits on the badge too (#286 call 7)
}

// When the box no longer shows that message (voice mode, or a newer typed one), the reply
// takes the usual route, so it is never lost.
func TestASessionsReplyTheBoxDoesntShowTakesTheUsualRoute(t *testing.T) {
	for name, after := range map[string]func(c *AgentController){
		"voice mode": func(c *AgentController) { c.Step(TextModeInput(false), T0+15) },
		"newer typed": func(c *AgentController) {
			c.Step(TypedDelegateInput(job(2), "and the docs?", ""), T0+15)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := handedOffTyped(t)
			after(c)
			fx := sessionTurn(c, pixieTurn("The redirect loop is fixed."), T0+20)
			for _, l := range chats(fx) {
				if l.Job == job(1) {
					t.Fatalf("went to the box: %s", fmtEffects(fx))
				}
			}
			if !anyKind(fx, EfInject, EfNotify, EfAppend) {
				t.Fatalf("lost: %s", fmtEffects(fx))
			}
		})
	}
}

func TestAnotherSessionsReplyIsNotTakenForTheTypedMessage(t *testing.T) {
	c := handedOffTyped(t)
	fx := c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: "web-claude", Turn: &SessionTurn{Label: "web", Reply: "Done.", Duration: 45_000, FromPixie: true}}), T0+20)
	wantChat(t, fx)
}

// #241 review: answering the session's question in the box goes back to that session, and its
// next turn answers in the box too.
func TestAnAnswerInTheBoxGoesToTheSessionThatAsked(t *testing.T) {
	c := handedOffTyped(t)
	sessionTurn(c, pixieTurn("Should I also fix the logout page?"), T0+20)
	session, job1 := c.takeBoxAnswer()
	if session != "api-claude" || job1 != job(1) {
		t.Fatalf("%q %s", session, job1)
	}
	wantContains(t, c.Step(AnswerInput("", "yes please"), T0+30), Effect{Kind: EfTellSession, Name: "api-claude", Text: "yes please"})
	fx := sessionTurn(c, pixieTurn("The logout page is fixed too."), T0+40)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "done", Text: "The logout page is fixed too."})
	if s, _ := c.takeBoxAnswer(); s != "" {
		t.Fatalf("taken twice: %q", s)
	}
}

func TestNoBoxAnswerWithoutASessionQuestionInTheBox(t *testing.T) {
	c := handedOffTyped(t)
	sessionTurn(c, pixieTurn("The redirect loop is fixed."), T0+20)
	if s, _ := c.takeBoxAnswer(); s != "" {
		t.Fatalf("%q", s)
	}
}

// #241 review: a session that answers before the coordinator's turn ends keeps its answer in
// the box; the coordinator's closing line doesn't replace it.
func TestASessionAnswerBeforeTheCoordinatorsReplyStays(t *testing.T) {
	c := typedOnJob1()
	c.Step(TextModeInput(true), T0)
	c.Step(SessionSentInput("api-claude", landing), T0+5)
	wantChat(t, sessionTurn(c, pixieTurn("The redirect loop is fixed."), T0+6),
		ChatLine{Job: job(1), Status: "done", Text: "The redirect loop is fixed."})
	fx := stop(c, `<spoken ref="job_000000000001" status="done">Passed it on.</spoken>`, T0+10)
	wantChat(t, fx)
	noUsualRoute(t, fx)
	wantJobStatus(t, c, 1, JobDone)
}

// A hand-off that isn't typed forgets an earlier typed one to the same session.
func TestAVoiceHandOffForgetsATypedOneToTheSameSession(t *testing.T) {
	c := handedOffTyped(t)
	c.Step(DelegateInput(job(2), "tell api to push", ""), T0+15)
	c.Step(TickInput(), T0+15+StopSettleMs)
	c.Step(SessionSentInput("api-claude", landing), T0+16+StopSettleMs)
	for _, l := range chats(sessionTurn(c, pixieTurn("Pushed."), T0+30+StopSettleMs)) {
		if l.Job == job(1) {
			t.Fatal("the voice request's answer went to the box")
		}
	}
}

// #246: a message sent after you confirmed late goes on the request that asked for it, even
// while the coordinator works on nothing; once that request is over, it's a line of its own.
func TestALateSendGoesOnTheRequestThatAskedForIt(t *testing.T) {
	c := typedOnJob1()
	passed := "Passed on to your session on " + landing + "."
	wantChat(t, c.Step(LateSessionSentInput(job(1), "repo-claude", landing, "", ""), T0+5), ChatLine{Job: job(1), Status: "working", Text: passed})
}

func TestALateSendAfterItsRequestEndedIsALineOfItsOwn(t *testing.T) {
	c := typedOnJob1()
	c.Step(CancelInput(job(1)), T0+1)
	fx := c.Step(LateSessionSentInput(job(1), "repo-claude", landing, "", ""), T0+5)
	if len(chats(fx)) != 0 || !strings.Contains(fmtEffects(fx), "Passed on to your session on "+landing) {
		t.Fatal(fmtEffects(fx))
	}
	fx = c.Step(LateSessionSentInput("", "repo-claude", landing, "", "No session repo-claude."), T0+6)
	if !strings.Contains(fmtEffects(fx), "Couldn't reach your session on "+landing+": No session repo-claude.") {
		t.Fatal(fmtEffects(fx))
	}
}

// #261: a typed message's lines name the sessions they are about, for the card's buttons: the
// session it was passed to, from the hand-off on, and the session whose answer shows.
func TestATypedMessagesLinesNameItsSessions(t *testing.T) {
	c := handedOffTyped(t)
	fx := sessionTurn(c, pixieTurn("The redirect loop is fixed."), T0+20)
	if got := chats(fx); len(got) != 1 || !slices.Equal(got[0].Sessions, []string{"api-claude"}) {
		t.Fatalf("session's answer: %+v", got)
	}
	c = typedOnJob1()
	if got := chats(c.Step(SessionSentInput("repo-claude", landing), T0+5)); len(got) != 1 || !slices.Equal(got[0].Sessions, []string{"repo-claude"}) {
		t.Fatalf("hand-off: %+v", got)
	}
	c.Step(SessionSentInput("web-claude", "the docs site"), T0+6)
	fx = stop(c, `<spoken ref="job_000000000001" status="done">Passed both on.</spoken>`, T0+10)
	if got := chats(fx); len(got) != 1 || !slices.Equal(got[0].Sessions, []string{"repo-claude", "web-claude"}) {
		t.Fatalf("the reply after two hand-offs: %+v", got)
	}
	// A message handed to no session names none.
	c = typedOnJob1()
	fx = stop(c, `<spoken ref="job_000000000001" status="done">Two are running.</spoken>`, T0+10)
	if got := chats(fx); len(got) != 1 || got[0].Sessions != nil {
		t.Fatalf("no session: %+v", got)
	}
}
