package agent

import (
	"encoding/json"
	"slices"
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

func outboxLines(fx []Effect) []OutboxLine {
	var out []OutboxLine
	for _, e := range fx {
		if e.Kind == EfLine {
			out = append(out, e.Outbox)
		}
	}
	return out
}

func TestEveryReplyLineAlsoGoesToTheThread(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	lines := outboxLines(sessionTurn(c, longTurn, T0))
	if len(lines) != 1 || lines[0].Session != "api-claude" || lines[0].ID == 0 {
		t.Fatalf("%+v", lines)
	}
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	if n := len(outboxLines(sessionTurn(c, asking, T0+10))); n != 0 {
		t.Fatalf("%d thread lines for a question", n)
	}
}

// #286 table: open-for-updates holds the card while Pixie opens a conversation to say it.
func TestOpenForUpdatesHoldsTheCardWhileItOpens(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	fx := sessionTurn(c, longTurn, T0)
	if len(replies(fx)) != 0 {
		t.Fatal("a card while Pixie opens a conversation to say it")
	}
	wantContains(t, fx, Effect{Kind: EfOpenConversation}, "opens")
}

func TestAFailedOpenStillShowsTheCard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	sessionTurn(c, longTurn, T0)
	fx := c.Step(VoiceInput(VoiceSignal{Kind: voice.SigClosed}), T0+1_000) // failed connect
	if len(replies(fx)) != 1 {
		t.Fatal(fmtEffects(fx))
	}
}

func TestAnOpenThatNeverReportsShowsTheCard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	sessionTurn(c, longTurn, T0)
	if fx := c.Step(TickInput(), T0+OpenWaitMs-1); len(replies(fx)) != 0 {
		t.Fatalf("shown before the wait ran out: %s", fmtEffects(fx))
	}
	if fx := c.Step(TickInput(), T0+OpenWaitMs); len(replies(fx)) != 1 {
		t.Fatal(fmtEffects(fx))
	}
}

// An opened conversation says the update, and its caption is the card: the held one is dropped,
// and the next update doesn't join it.
func TestAnOpenedConversationDropsTheHeldCard(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	sessionTurn(c, longTurn, T0)
	if fx := c.Step(voiceOpened(), T0+500); len(replies(fx)) != 0 {
		t.Fatal(fmtEffects(fx))
	}
	if fx := c.Step(voiceClosed(), T0+1_000); len(replies(fx)) != 0 {
		t.Fatalf("released after it was said: %s", fmtEffects(fx))
	}
	c.Step(TextModeInput(true), T0+1_500)
	other := longTurn
	other.Label = "live-like: benchmark"
	cards := replies(sessionTurn(c, other, T0+2_000))
	if len(cards) != 1 || cards[0].Count != 1 {
		t.Fatalf("joined a card that was said: %+v", cards)
	}
}

// #319: the chat window shows the update and reads it while the conversation opens; Pixie still
// says it when it opens.
func TestAChatReadWhileItOpensStillLetsPixieSayIt(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	lines := outboxLines(sessionTurn(c, longTurn, T0))
	c.Step(RepliesShownInput([]uint64{lines[0].ID}), T0+100)
	wantContains(t, c.Step(voiceOpened(), T0+300),
		eInject("Coordinator replies while you were away:\n- Session update: "+longTurnLine), "said")
}

// #319: the open fails after the chat window read it: the card shows and the read counts.
func TestAChatReadWhileItOpensCountsOnceTheOpenFails(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	lines := outboxLines(sessionTurn(c, longTurn, T0))
	c.Step(RepliesShownInput([]uint64{lines[0].ID}), T0+100)
	cards := replies(c.Step(voiceClosed(), T0+1_000)) // failed connect
	if len(cards) != 1 {
		t.Fatal("no card after the failed open")
	}
	c.Step(RepliesShownInput(cards[0].Lines), T0+1_100)
	if n := len(c.outbox.Undelivered()); n != 0 {
		t.Fatalf("%d lines undelivered once shown", n)
	}
}

// Once no open is coming (here: refused, to be retried, then text mode turned on), the card
// shows at once.
func TestAHeldCardShowsWhenNoOpenIsComing(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	sessionTurn(c, longTurn, T0)
	if fx := c.Step(OpenRefusedInput(), T0+50); len(replies(fx)) != 0 {
		t.Fatalf("a refused open is retried: %s", fmtEffects(fx))
	}
	if fx := c.Step(TextModeInput(true), T0+100); len(replies(fx)) != 1 {
		t.Fatal(fmtEffects(fx))
	}
}

// Opens refused on and on (the mic never starts): the held card shows by OpenWaitMs anyway.
func TestAHeldCardShowsByTheOpenWaitWhileOpensAreRefused(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	if len(replies(sessionTurn(c, longTurn, T0))) != 0 {
		t.Fatal("not held while the conversation opens")
	}
	shown := 0
	for at := T0 + 1000; at <= T0+OpenWaitMs; at += 1000 {
		shown += len(replies(c.Step(OpenRefusedInput(), at)))
		if at < T0+OpenWaitMs && shown != 0 {
			t.Fatalf("shown at %d, before the wait was up", at-T0)
		}
		shown += len(replies(c.Step(TickInput(), at)))
	}
	if shown != 1 {
		t.Fatalf("shown %d times by OpenWaitMs", shown)
	}
	if len(replies(c.Step(TickInput(), T0+OpenWaitMs+1000))) != 0 {
		t.Fatal("shown again")
	}
}

// The deck's startup call decodes a list: nothing pending is [], not null.
func TestNoPendingRepliesIsAnEmptyList(t *testing.T) {
	c := ctlWith(func(*Settings) {})
	b, err := json.Marshal(map[string]any{"cards": c.PendingReplies(T0)})
	if err != nil || string(b) != `{"cards":[]}` {
		t.Fatalf("%s %v", b, err)
	}
}

func TestTextModeShowsTheCardWithOpenForUpdatesOn(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.OpenConversation = true; s.SessionTurns = true })
	c.Step(TextModeInput(true), T0)
	if len(replies(sessionTurn(c, longTurn, T0+10))) != 1 {
		t.Fatal("no card in text mode")
	}
}

func TestASpokenQuestionWaitsOnTheBadge(t *testing.T) {
	c := workingOnJob1()
	c.Step(VoiceInput(VoiceSignal{Kind: voice.SigOpened}), T0)
	stop(c, `<spoken ref="job_000000000001" status="question">Which branch?</spoken>`, T0+10)
	if got := c.Updates(); len(got) != 1 || got[0].Kind != LineQuestion {
		t.Fatalf("%+v", got)
	}
}

// A session asking in a conversation is said, and waits on the badge until its next turn.
func TestASpokenSessionQuestionWaitsOnTheBadge(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	openVoice(c)
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	fx := sessionTurn(c, asking, T0+10)
	if !anyKind(fx, EfInject) {
		t.Fatalf("not said: %s", fmtEffects(fx))
	}
	if got := c.Updates(); len(got) != 1 || !got[0].Asking {
		t.Fatalf("%+v", got)
	}
	sessionTurn(c, longTurn, T0+20)
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("still asking after its next turn: %+v", got)
	}
}

func TestATypedJobsQuestionIsInTheCardAndOnTheBadge(t *testing.T) {
	c := typedOnJob1()
	fx := stop(c, `<spoken ref="job_000000000001" status="question">Which branch?</spoken>`, T0+10)
	wantChat(t, fx, ChatLine{Job: job(1), Status: "question", Text: "Which branch?"})
	if got := c.Updates(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

// Call 7: a session's question about a message typed in the box is in the box and on the badge.
func TestASessionsQuestionInTheBoxIsOnTheBadge(t *testing.T) {
	c := handedOffTyped(t)
	sessionTurn(c, pixieTurn("Should I also fix the logout page?"), T0+20)
	if got := c.Updates(); len(got) != 1 || !got[0].Asking || got[0].Session != "api-claude" {
		t.Fatalf("%+v", got)
	}
	sessionTurn(c, pixieTurn("The logout page is fixed too."), T0+40)
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// askedLine: the outbox line of the session's question.
func askedLine(t *testing.T, c *AgentController) OutboxLine {
	t.Helper()
	for _, l := range c.outbox.lines {
		if l.Asking {
			return l
		}
	}
	t.Fatal("no question in the outbox")
	return OutboxLine{}
}

// A session that moved on (a turn of its own that isn't told) or ended has answered its
// question: it is marked delivered, so a later conversation doesn't say it.
func TestASessionsAnsweredQuestionIsNotReplayed(t *testing.T) {
	asking := longTurn
	asking.Reply = "Shall I go ahead and push?"
	for name, after := range map[string]func(c *AgentController) []Effect{
		"an untold turn": func(c *AgentController) []Effect {
			short := longTurn
			short.Duration = 1_000
			return sessionTurn(c, short, T0+10)
		},
		"session end": func(c *AgentController) []Effect {
			return c.Step(PaneInput(PaneEvent{Kind: PaneEventExited, Agent: "api-claude"}), T0+10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := ctlWith(func(s *Settings) { s.SessionTurns = true })
			sessionTurn(c, asking, T0)
			fx := after(c)
			l := askedLine(t, c)
			if !l.Delivered {
				t.Fatalf("still undelivered: %+v", l)
			}
			wantContains(t, fx, Effect{Kind: EfAppend, File: OutboxFile, Line: l.record()}, "saved")
			if got := c.Updates(); len(got) != 0 {
				t.Fatalf("%+v", got)
			}
		})
	}
}

// A restart rebuilds a session's open question only from one still undelivered.
func TestARestartDropsADeliveredSessionQuestion(t *testing.T) {
	ask := OutboxLine{Kind: LineSession, Session: "api-claude", Asking: true, Text: "api: asks", Delivered: true}
	if got := restoredCtl(ask).Updates(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

// injected: the notice the step said.
func injected(t *testing.T, fx []Effect) Notice {
	t.Helper()
	for _, e := range fx {
		if e.Kind == EfInject {
			return e.Notice
		}
	}
	t.Fatalf("nothing said: %s", fmtEffects(fx))
	return Notice{}
}

// A question kept as heard that turns out unsaid (the inject bounced, or the conversation closed
// first) goes the usual route, and is still one badge item.
func TestAnUnsaidQuestionIsOneBadgeItem(t *testing.T) {
	for name, ask := range map[string]func(c *AgentController) []Effect{
		"job question": func(c *AgentController) []Effect {
			return stop(c, `<spoken ref="job_000000000001" status="question">Which branch?</spoken>`, T0+10)
		},
		"permission": func(c *AgentController) []Effect { return askPush(c, 7, T0+10) },
	} {
		for how, unsaid := range map[string]func(n Notice) Input{
			"bounced": InjectBouncedInput,
			"closed":  func(n Notice) Input { return voiceClosed(n) },
		} {
			t.Run(name+" "+how, func(t *testing.T) {
				c := workingOnJob1()
				openVoice(c)
				n := injected(t, ask(c))
				c.Step(unsaid(n), T0+20)
				if got := c.Updates(); len(got) != 1 {
					t.Fatalf("%+v", got)
				}
			})
		}
	}
}

// Spec call 3: a session's question with no buttons (a notice) said in a conversation waits on
// the badge until that session's next turn.
func TestASpokenSessionNoticeWaitsOnTheBadge(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	openVoice(c)
	fx := askSession(c, SessionQuestion{ID: "q6", Label: "api", Kind: "notice", Text: "needs your attention"}, T0+10)
	injected(t, fx)
	if got := c.Updates(); len(got) != 1 || got[0].Session != sessionName {
		t.Fatalf("%+v", got)
	}
	sessionTurn(c, longTurn, T0+20)
	if got := c.Updates(); len(got) != 0 {
		t.Fatalf("still there after its next turn: %+v", got)
	}
}

func deliveredIDs(fx []Effect) []uint64 {
	var out []uint64
	for _, e := range fx {
		if e.Kind == EfDelivered {
			out = append(out, e.Value.([]uint64)...)
		}
	}
	return out
}

// #302: a line replayed in a conversation was heard, so the thread shows it read.
func TestALineReplayedInAConversationIsDelivered(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	line := outboxLines(sessionTurn(c, longTurn, T0))[0]
	if got := deliveredIDs(c.Step(voiceOpened(), T0+10)); !slices.Equal(got, []uint64{line.ID}) {
		t.Fatalf("delivered = %v, want [%d]", got, line.ID)
	}
	if got := deliveredIDs(c.Step(voiceOpened(), T0+20)); len(got) != 0 {
		t.Fatal("delivered twice:", got)
	}
}

// #302: a line seen in the bubble was seen, so the thread shows it read.
func TestALineSeenInTheBubbleIsDelivered(t *testing.T) {
	c := ctlWith(func(s *Settings) { s.SessionTurns = true })
	line := outboxLines(sessionTurn(c, longTurn, T0))[0]
	if got := deliveredIDs(c.Step(UpdatesSeenInput([]uint64{line.ID, 999}), T0+10)); !slices.Equal(got, []uint64{line.ID}) {
		t.Fatalf("delivered = %v", got)
	}
}
