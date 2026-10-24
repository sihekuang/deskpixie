package agent

import (
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

func replies(fx []Effect) []ReplyCard {
	var out []ReplyCard
	for _, e := range fx {
		if e.Kind == EfReply {
			out = append(out, *e.Reply)
		}
	}
	return out
}

func TestADoneLineIsAReplyCard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	cards := replies(sessionTurn(c, longTurn, T0))
	if len(cards) != 1 || cards[0].Count != 1 ||
		cards[0].Text != "**deskpixie: fix login redirect:** The redirect loop is fixed and all 412 tests pass." {
		t.Fatalf("%+v", cards)
	}
}

func TestUpdatesWithinFiveSecondsShareACard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	first := replies(sessionTurn(c, longTurn, T0))[0]
	other := longTurn
	other.Label = "live-like: benchmark"
	second := replies(c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: "live-claude", Turn: &other}), T0+4_000))[0]
	if second.ID != first.ID || second.Count != 2 || len(second.Lines) != 2 || !strings.Contains(second.Text, "\n**live-like: benchmark:**") {
		t.Fatalf("%+v", second)
	}
	third := replies(sessionTurn(c, longTurn, T0+4_000+ReplyJoinMs+1))[0]
	if third.ID == first.ID || third.Count != 1 {
		t.Fatalf("a gap starts a new card: %+v", third)
	}
}

func TestEveryReplyLaneLineGetsACardEvenOverTheCap(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.NotifyPerMinute = 1; s.SessionTurns = true })
	sessionTurn(c, longTurn, T0)
	cards := replies(sessionTurn(c, longTurn, T0+10))
	if len(cards) != 1 || cards[0].Count != 2 {
		t.Fatalf("over the cap: %+v", cards)
	}
}

func TestAShownCardIsDeliveredAndNotReplayed(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	card := replies(sessionTurn(c, longTurn, T0))[0]
	fx := c.Step(RepliesShownInput(card.Lines), T0+10)
	if len(c.outbox.Undelivered()) != 0 {
		t.Fatal("still undelivered after it was shown")
	}
	if n := len(appendedTo(fx, OutboxFile)); n != 1 {
		t.Fatalf("%d outbox records appended", n)
	}
}

func TestPendingRepliesComeBackWithinTheHistory(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true; s.ReplyHistoryMs = 300_000 })
	sessionTurn(c, longTurn, T0)
	sessionTurn(c, longTurn, T0+400_000)
	got := c.PendingReplies(T0 + 500_000)
	if len(got) != 1 || got[0].At != T0+400_000 {
		t.Fatalf("%+v", got)
	}
}

func TestVoiceOpenSendsNoCard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	c.Step(VoiceInput(VoiceSignal{Kind: voice.SigOpened}), T0)
	if n := len(replies(sessionTurn(c, longTurn, T0+10))); n != 0 {
		t.Fatal("a card for a spoken line: the caption is its card")
	}
}

func appendedTo(fx []Effect, file string) []string {
	var out []string
	for _, e := range fx {
		if e.Kind == EfAppend && e.File == file {
			out = append(out, e.Line)
		}
	}
	return out
}

func TestATitledLineWithNoBriefKeepsItsTitle(t *testing.T) {
	row := replyRow(OutboxLine{Title: "deskpixie", Text: "Finished the thing."})
	if row != "**deskpixie:** Finished the thing." {
		t.Fatal(row)
	}
	if row := replyRow(OutboxLine{Text: "Plain."}); row != "Plain." {
		t.Fatal(row)
	}
}

func TestPendingRepliesGroupsLinesWithinFiveSeconds(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.ReplyHistoryMs = 300_000 })
	c.outbox.Push(NewLine(LineSession, "A done."), T0)
	c.outbox.Push(NewLine(LineSession, "B done."), T0+4_000)
	c.outbox.Push(NewLine(LineSession, "C done."), T0+4_000+ReplyJoinMs+1)
	got := c.PendingReplies(T0 + 20_000)
	if len(got) != 2 || got[0].Count != 2 || got[0].Text != "A done.\nB done." || got[1].Count != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestPendingRepliesSkipAlertingLines(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.ReplyHistoryMs = 300_000 })
	token := "gold"
	c.outbox.Push(Line{Kind: LineQuestion, Text: "Allow?", Token: &token}, T0)
	c.outbox.Push(Line{Kind: LineSession, Text: "Shall I?", Asking: true, Session: "s"}, T0+10)
	c.outbox.Push(NewLine(LineQuestion, "Which repo?"), T0+20)
	if got := c.PendingReplies(T0 + 100); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestAnUnsolicitedLineGetsACard(t *testing.T) {
	c := ctl()
	var out []Effect
	c.deliverUnsolicited("I tidied the notes.", T0, &out)
	if cards := replies(out); len(cards) != 1 || cards[0].Text != "I tidied the notes." {
		t.Fatalf("%+v", out)
	}
}

func TestAnAlertingLineGetsNoCard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	if n := len(replies(sessionTurn(c, asking, T0))); n != 0 {
		t.Fatalf("%d cards for a question", n)
	}
}
