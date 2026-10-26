package agent

import (
	"strings"
	"testing"
)

// #339: a notice saying a session's message still waits (#183) goes unsaid once the session
// has the message: said after the fact, it had you ask for it to be stopped and sent again.

const heldBusy = "Coordinator reply: Your session on " + landing + " hasn't got your message yet: it's busy, and gets it with its next step or when it stops. Say if you want it stopped and sent now."

func eHeldInject(text, session string) Effect {
	return Effect{Kind: EfInject, Notice: Notice{Text: text, Held: session}}
}

func eWithdrawHeld(session string) Effect { return Effect{Kind: EfWithdrawHeld, Name: session} }

func delivered(session string) Input {
	return PaneInput(PaneEvent{Kind: PaneEventDelivered, Agent: session})
}

// A stop-and-send (or a later message) that types it: the waiting notice is withdrawn before
// Pixie is told it was passed on.
func TestAHeldNoticeIsWithdrawnWhenTheMessageIsSent(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	wantEffects(t, visible(c.Step(SessionWaitingInput("repo-claude", landing, "working"), T0)), eHeldInject(heldBusy, "repo-claude"))
	wantEffects(t, visible(c.Step(SessionSentInput("repo-claude", landing), T0+2_000)), eWithdrawHeld("repo-claude"), handedOff)
}

// pix reports what waited delivered (its next step, or once it stopped): withdrawn too, once.
func TestAHeldNoticeIsWithdrawnWhenPixDeliversIt(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	c.Step(SessionWaitingInput("repo-claude", landing, "working"), T0)
	wantEffects(t, visible(c.Step(delivered("repo-claude"), T0+1_000)), eWithdrawHeld("repo-claude"))
	wantNone(t, visible(c.Step(delivered("repo-claude"), T0+2_000)), "nothing held any more")
}

// Another session's delivery, a delivery with nothing held, a send that still waits, or one
// that failed withdraw nothing.
func TestOnlyItsOwnSessionsDeliveryWithdrawsAHeldNotice(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	wantNone(t, visible(c.Step(delivered("repo-claude"), T0)), "nothing held yet")
	c.Step(SessionWaitingInput("repo-claude", landing, "working"), T0)
	wantNone(t, visible(c.Step(delivered("other-claude"), T0+1_000)), "another session")
	fx := visible(c.Step(SessionWaitingInput("repo-claude", landing, "typing"), T0+2_000))
	wantNotContains(t, fx, eWithdrawHeld("repo-claude"), "still waits")
	fx = visible(c.Step(LateSessionSentInput("job_000000000001", "repo-claude", landing, "", "No session repo-claude."), T0+3_000))
	wantNotContains(t, fx, eWithdrawHeld("repo-claude"), "failed")
	fx = visible(c.Step(LateSessionSentInput("job_000000000001", "repo-claude", landing, "working", ""), T0+4_000))
	wantNotContains(t, fx, eWithdrawHeld("repo-claude"), "late, still waiting")
	wantContains(t, visible(c.Step(LateSessionSentInput("job_000000000001", "repo-claude", landing, "", ""), T0+5_000)), eWithdrawHeld("repo-claude"), "late, sent")
}

// With no conversation open the notice waits in the outbox for the next one: delivered, it is
// marked heard there, so the next conversation doesn't replay it.
func TestAHeldLineInTheOutboxIsDroppedWhenDelivered(t *testing.T) {
	c := workingOnJob1()
	c.Step(SessionWaitingInput("repo-claude", landing, "working"), T0)
	if !heldInOutbox(c, "repo-claude") {
		t.Fatalf("the waiting line isn't in the outbox: %+v", c.outbox.Undelivered())
	}
	fx := c.Step(delivered("repo-claude"), T0+1_000)
	wantNotContains(t, visible(fx), eWithdrawHeld("repo-claude"), "Pixie was never handed it")
	if heldInOutbox(c, "repo-claude") {
		t.Fatalf("still undelivered: %+v", c.outbox.Undelivered())
	}
	written := false
	for _, e := range fx {
		written = written || e.Kind == EfAppend && e.File == OutboxFile && strings.Contains(e.Line, `"delivered":true`) && strings.Contains(e.Line, `"held":"repo-claude"`)
	}
	if !written {
		t.Fatalf("not written as delivered: %s", fmtEffects(fx))
	}
	for _, e := range c.Step(voiceOpened(), T0+2_000) {
		if e.Kind == EfInject && strings.Contains(e.Notice.Text, "hasn't got your message yet") {
			t.Fatalf("replayed: %q", e.Notice.Text)
		}
	}
}

// A conversation that closed before Pixie said it hands the notice back to the outbox, still
// tied to its session: delivered, it goes from there too.
func TestABouncedHeldNoticeIsDroppedWhenDelivered(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	c.Step(SessionWaitingInput("repo-claude", landing, "working"), T0)
	c.Step(voiceClosed(Notice{Text: heldBusy, Held: "repo-claude"}), T0+1_000)
	if !heldInOutbox(c, "repo-claude") {
		t.Fatalf("the bounced line isn't in the outbox: %+v", c.outbox.Undelivered())
	}
	c.Step(delivered("repo-claude"), T0+2_000)
	if heldInOutbox(c, "repo-claude") {
		t.Fatalf("still undelivered: %+v", c.outbox.Undelivered())
	}
}

func heldInOutbox(c *AgentController, session string) bool {
	for _, l := range c.outbox.Undelivered() {
		if l.Held == session {
			return true
		}
	}
	return false
}

// The held mark survives a restart: outbox.jsonl keeps it.
func TestOutboxLineKeepsHeld(t *testing.T) {
	var l OutboxLine
	if err := l.UnmarshalJSON([]byte(`{"id":1,"text":"t","kind":"reply","token":null,"at":5,"held":"repo-claude"}`)); err != nil || l.Held != "repo-claude" {
		t.Fatalf("%+v %v", l, err)
	}
	if err := l.UnmarshalJSON([]byte(`{"id":1,"text":"t","kind":"reply","token":null,"at":5}`)); err != nil || l.Held != "" {
		t.Fatalf("an older record: %+v %v", l, err)
	}
}

func eHeldSeqInject(text, session string, seq uint64) Effect {
	return Effect{Kind: EfInject, Notice: Notice{Text: text, Held: session, HeldSeq: seq}}
}

func deliveredUpTo(session string, seq uint64) Input {
	return PaneInput(PaneEvent{Kind: PaneEventDelivered, Agent: session, Seq: seq})
}

// #343 review, 1: A waits, its delivery comes after B's waiting reply. Only A's notice goes:
// the withdrawal names A's number, and B's notice (and outbox line) stay.
func TestADeliveryWithdrawsOnlyTheMessagesItCovers(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	wantEffects(t, visible(c.Step(SessionWaitingInput("repo-claude", landing, "working").WithSeq(1), T0)), eHeldSeqInject(heldBusy, "repo-claude", 1))
	wantEffects(t, visible(c.Step(SessionWaitingInput("repo-claude", landing, "working").WithSeq(2), T0+100)), eHeldSeqInject(heldBusy, "repo-claude", 2))
	wantEffects(t, visible(c.Step(deliveredUpTo("repo-claude", 1), T0+200)), Effect{Kind: EfWithdrawHeld, Name: "repo-claude", Seq: 1})
	wantEffects(t, visible(c.Step(deliveredUpTo("repo-claude", 2), T0+300)), Effect{Kind: EfWithdrawHeld, Name: "repo-claude", Seq: 2})
	wantNone(t, visible(c.Step(deliveredUpTo("repo-claude", 2), T0+400)), "nothing held any more")

	// With no conversation open, the outbox keeps B's line.
	c = workingOnJob1()
	c.Step(SessionWaitingInput("repo-claude", landing, "working").WithSeq(1), T0)
	c.Step(SessionWaitingInput("repo-claude", landing, "typing").WithSeq(2), T0+100)
	c.Step(deliveredUpTo("repo-claude", 1), T0+200)
	var left []uint64
	for _, l := range c.outbox.Undelivered() {
		if l.Held == "repo-claude" {
			left = append(left, l.HeldSeq)
		}
	}
	if len(left) != 1 || left[0] != 2 {
		t.Fatalf("held lines left: %v", left)
	}
}

// #343 review, 2: the delivery steps before the reply that its message waits. That message
// went in already: Pixie says it was passed on, and no waiting notice is queued at all.
func TestAWaitingReplyAfterItsDeliveryIsPassedOn(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	wantNone(t, visible(c.Step(deliveredUpTo("repo-claude", 3), T0)), "nothing held yet")
	wantEffects(t, visible(c.Step(SessionWaitingInput("repo-claude", landing, "working").WithSeq(3), T0+100)), handedOff)
	if c.heldNoticed["repo-claude"] != 0 || len(c.heldNoticed) != 0 {
		t.Fatalf("held noticed: %v", c.heldNoticed)
	}
	// A later message still waits as it says.
	wantEffects(t, visible(c.Step(SessionWaitingInput("repo-claude", landing, "working").WithSeq(4), T0+200)), eHeldSeqInject(heldBusy, "repo-claude", 4))
}

// An older pix numbers nothing: a delivery covers every held notice, as before, and a newer
// pix's numbered notice goes with a delivery from a pix that didn't number it.
func TestUnnumberedDeliveriesCoverEverything(t *testing.T) {
	c := workingOnJob1()
	openVoice(c)
	c.Step(SessionWaitingInput("repo-claude", landing, "working"), T0)
	wantEffects(t, visible(c.Step(delivered("repo-claude"), T0+100)), eWithdrawHeld("repo-claude"))
	c.Step(SessionWaitingInput("repo-claude", landing, "working").WithSeq(5), T0+200)
	wantEffects(t, visible(c.Step(delivered("repo-claude"), T0+300)), eWithdrawHeld("repo-claude"))
	wantNone(t, visible(c.Step(delivered("repo-claude"), T0+400)))
}

// #343 review, 3: a waiting line kept for the next conversation is replayed as a notice of its
// own, still tied to its session and number, so the delivery can withdraw it; the rest of the
// digest goes as before.
func TestAReplayedHeldLineCanStillBeWithdrawn(t *testing.T) {
	c := workingOnJob1()
	c.Step(SessionWaitingInput("repo-claude", landing, "working").WithSeq(7), T0)
	c.deliver(NewLine(LineInfo, "Coordinator: the build is green"), T0+50, &[]Effect{})
	var injects []Effect
	for _, e := range c.Step(voiceOpened(), T0+100) {
		if e.Kind == EfInject {
			injects = append(injects, e)
		}
	}
	if len(injects) != 2 || strings.Contains(injects[0].Notice.Text, "hasn't got") || injects[0].Notice.Held != "" ||
		injects[1].Notice.Held != "repo-claude" || injects[1].Notice.HeldSeq != 7 || !strings.Contains(injects[1].Notice.Text, "hasn't got your message yet") {
		t.Fatalf("replayed %+v", injects)
	}
	wantEffects(t, visible(c.Step(deliveredUpTo("repo-claude", 7), T0+200)), Effect{Kind: EfWithdrawHeld, Name: "repo-claude", Seq: 7})
	// Handed back unsaid when the conversation closes, it is still the session's line.
	c = workingOnJob1()
	c.Step(SessionWaitingInput("repo-claude", landing, "working").WithSeq(8), T0)
	var replayed Notice
	for _, e := range c.Step(voiceOpened(), T0+100) {
		if e.Kind == EfInject {
			replayed = e.Notice
		}
	}
	c.Step(voiceClosed(replayed), T0+200)
	if !heldInOutbox(c, "repo-claude") {
		t.Fatalf("not back in the outbox as the session's: %+v", c.outbox.Undelivered())
	}
	c.Step(deliveredUpTo("repo-claude", 8), T0+300)
	if heldInOutbox(c, "repo-claude") {
		t.Fatalf("still undelivered: %+v", c.outbox.Undelivered())
	}
}
