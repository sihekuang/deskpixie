package voice

import (
	"testing"
	"time"
)

// #71: every way a conversation ends leaves its reason, for the voice log.
func TestEachEndHasAReason(t *testing.T) {
	now := t0()

	c := listening(now)
	tick(c, true, now.Add(testTimeout))
	if got := c.EndReason(); got != "silence timeout" {
		t.Errorf("timeout: %q", got)
	}

	c = listening(now)
	c.Step(HotkeyInput(), ms(now, 100))
	if got := c.EndReason(); got != "hotkey" {
		t.Errorf("hotkey: %q", got)
	}

	c = thinking(now)
	model(c, evResponseCreated, now)
	model(c, evToolCall("c1", EndConversationTool), now)
	model(c, evResponseDone, now)
	c.Step(ToolDoneInput("c1", true), now)
	if got := c.EndReason(); got != "goodbye" {
		t.Errorf("goodbye: %q", got)
	}

	c = thinking(now)
	model(c, evResponseCreated, now)
	model(c, evToolCall("c1", DelegateTool), now)
	model(c, evResponseDone, now)
	c.Step(ToolDoneAfterReplyInput("c1"), now)
	reply(c, "On it, I'll let you know.", ms(now, 100))
	tick(c, true, ms(now, 200))
	if c.IsActive() {
		t.Fatal("still open after the hand-off")
	}
	if got := c.EndReason(); got != "after a hand-off" {
		t.Errorf("hand-off: %q", got)
	}

	c = listening(now)
	c.Step(FailedInput(Failure{Kind: FailConnectionLost}), ms(now, 100))
	if got := c.EndReason(); got == "" || got[:6] != "error:" {
		t.Errorf("error: %q", got)
	}
}

func TestTheAppsHandOffEndHasAReason(t *testing.T) {
	c := newClaimConv(true, true)
	userTurn(c, "build me a small dashboard app", ms(t0(), 100))
	reply(c, "I've handed that off to the coordinator.", ms(t0(), 500))
	ticks(c, ms(t0(), 500), 3*time.Second)
	c.Step(HandOffDoneAfterReplyInput(Notice{}), ms(t0(), 3600))
	ticks(c, ms(t0(), 3600), time.Second)
	if c.IsActive() || c.EndReason() != "after the app's hand-off" {
		t.Fatalf("active %v reason %q", c.IsActive(), c.EndReason())
	}
}
