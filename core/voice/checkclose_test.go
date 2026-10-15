package voice

import (
	"testing"
	"time"
)

// A fact check's answer ends the conversation once Pixie has read it: the user asked one
// thing and got it (2026-10-15: it waited the silence timeout instead).
func checkAsked(t *testing.T) *Conversation {
	t.Helper()
	c := newClientConv()
	userTurn(c, "how many workers do you have right now", ms(t0(), 100))
	if _, a := clientCall(c, "item_1", ms(t0(), 200)); a.Task != "Check: how many workers do you have right now" {
		t.Fatal(a.Task)
	}
	c.Step(ToolDoneInput("item_1", false), ms(t0(), 250))
	reply(c, "Let me check that.", ms(t0(), 600))
	ticks(c, ms(t0(), 600), 2*time.Second)
	return c
}

func TestACheckAnswerEndsTheConversationOnceRead(t *testing.T) {
	c := checkAsked(t)
	if !c.IsActive() {
		t.Fatal("closed before the answer")
	}
	fx := c.Step(InjectInput(Notice{Text: "Coordinator reply: No workers are running."}), ms(t0(), 4000))
	if len(injects(fx)) != 1 {
		t.Fatalf("the answer wasn't said: %v", injects(fx))
	}
	reply(c, "No workers are running right now.", ms(t0(), 5000))
	ticks(c, ms(t0(), 5000), 500*time.Millisecond)
	if c.IsActive() {
		t.Fatal("still open after the answer was read")
	}
	if c.EndReason() != "after answering a check" {
		t.Fatal(c.EndReason())
	}
}

func TestTheUserTalkingAfterTheAnswerKeepsItOpen(t *testing.T) {
	c := checkAsked(t)
	c.Step(InjectInput(Notice{Text: "Coordinator reply: No workers are running."}), ms(t0(), 4000))
	model(c, evResponseCreated, ms(t0(), 4500))
	userTurn(c, "and in the cloud?", ms(t0(), 4800))
	ticks(c, ms(t0(), 4800), 500*time.Millisecond)
	if !c.IsActive() {
		t.Fatal("ended while the user was asking a follow-up")
	}
}

func TestAReplyWithNoCheckOpenDoesntEnd(t *testing.T) {
	c := newClientConv()
	userTurn(c, "tell me a joke", ms(t0(), 100))
	reply(c, "Why did the robot cross the road?", ms(t0(), 600))
	ticks(c, ms(t0(), 600), time.Second)
	c.Step(InjectInput(Notice{Text: "Coordinator reply: The build passed."}), ms(t0(), 2000))
	reply(c, "By the way, the build passed.", ms(t0(), 2500))
	ticks(c, ms(t0(), 2500), 500*time.Millisecond)
	if !c.IsActive() {
		t.Fatal("ended after a reply nobody was waiting for")
	}
}
