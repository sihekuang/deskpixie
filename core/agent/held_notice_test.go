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
