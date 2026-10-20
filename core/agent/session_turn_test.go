package agent

import (
	"strings"
	"testing"
)

// #114: a pix session's finished turn, announced when the setting is on.

const webstorm = "com.jetbrains.WebStorm"

func turnCtl(edit func(*Settings)) *AgentController {
	return ctlWith(func(s *Settings) {
		s.SessionTurns = true
		if edit != nil {
			edit(s)
		}
	})
}

func sessionTurn(c *AgentController, t SessionTurn, now Millis) []Effect {
	return c.Step(PaneInput(PaneEvent{Kind: PaneEventTurn, Agent: "api-claude", Turn: &t}), now)
}

var longTurn = SessionTurn{Label: "deskpixie: fix login redirect",
	Reply: "I traced the loop to the cookie path.\n\nThe redirect loop is fixed and all 412 tests pass.",
	App:   webstorm, Duration: 45_000}

const longTurnLine = "deskpixie: fix login redirect is done: the redirect loop is fixed and all 412 tests pass."

func TestWithTheSettingOffATurnIsNotAnnounced(t *testing.T) {
	if fx := sessionTurn(ctl(), longTurn, T0); anyKind(fx, EfInject, EfNotify, EfChime, EfAppend) {
		t.Fatal(fmtEffects(fx))
	}
}

func TestYourOwnShortTurnStaysQuiet(t *testing.T) {
	short := longTurn
	short.Duration = 5_000
	if fx := sessionTurn(turnCtl(nil), short, T0); anyKind(fx, EfInject, EfNotify, EfChime, EfAppend) {
		t.Fatal(fmtEffects(fx))
	}
}

func TestYourOwnLongTurnIsAnnouncedWithItsGist(t *testing.T) {
	fx := sessionTurn(turnCtl(nil), longTurn, T0)
	wantContains(t, fx, eNotify(longTurnLine))
	if !anyKind(fx, EfChime) || !anyKind(fx, EfAppend) {
		t.Fatal(fmtEffects(fx))
	}
	var bubble []UpdateItem
	for _, e := range effectsOfKind(fx, EfUpdates) {
		bubble = e.Value.([]UpdateItem)
	}
	if len(bubble) != 1 || bubble[0].Kind != LineSession || bubble[0].App != webstorm || bubble[0].Text != longTurnLine {
		t.Fatalf("the bubble line activates its app: %+v", bubble)
	}
}

func TestATurnPixieStartedIsAnnouncedHoweverShort(t *testing.T) {
	quick := longTurn
	quick.Duration, quick.FromPixie = 1_000, true
	wantContains(t, sessionTurn(turnCtl(nil), quick, T0), eNotify(longTurnLine))
}

func TestATurnThatAsksIsAnnouncedOverTheCap(t *testing.T) {
	c := turnCtl(func(s *Settings) { s.NotifyPerMinute = 1 })
	sessionTurn(c, longTurn, T0) // takes the minute's only slot
	asks := longTurn
	asks.Reply = "Fixed the loop.\n\nShould I also update the docs?"
	wantContains(t, sessionTurn(c, asks, T0+1), eNotify("deskpixie: fix login redirect is asking: should I also update the docs?"))
}

func TestInAConversationTheVoiceModelGetsTheReply(t *testing.T) {
	c := turnCtl(nil)
	openVoice(c)
	wantContains(t, sessionTurn(c, longTurn, T0), eInject("Session update: "+longTurnLine+
		" Its reply, to sum up in one sentence: I traced the loop to the cookie path. The redirect loop is fixed and all 412 tests pass."))
}

func TestASessionTurnNeverSpeaksAPendingConfirmWord(t *testing.T) {
	c := workingOnJob1()
	c.settings.SessionTurns = true
	askPush(c, 7, T0) // "hawk" is pending
	turn := SessionTurn{Label: "hawk rollout", Reply: "Say hawk to approve the push.", Duration: 60_000}
	fx := sessionTurn(c, turn, T0)
	if !anyKind(fx, EfNotify, EfInject) {
		t.Fatal(fmtEffects(fx))
	}
	for _, e := range fx {
		if (e.Kind == EfNotify && strings.Contains(strings.ToLower(e.Text), "hawk")) ||
			(e.Kind == EfInject && strings.Contains(strings.ToLower(e.Notice.Text), "hawk")) {
			t.Fatalf("confirm word spoken: %s", fmtEffects(fx))
		}
	}
}

func TestASessionLineKeepsItsAppInTheOutbox(t *testing.T) {
	o := NewOutbox()
	rec := o.Push(Line{Kind: LineSession, Text: "api finished.", App: webstorm}, T0)
	var back OutboxLine
	if err := back.UnmarshalJSON([]byte(rec)); err != nil || back.App != webstorm || back.Kind != LineSession {
		t.Fatalf("%s → %+v %v", rec, back, err)
	}
}

// #191: a session that registers on an older pix than the app's is told, whatever the session
// turns setting, with how to update it; nothing is stopped or restarted.
func TestASessionOnAnOlderPixIsToldOnce(t *testing.T) {
	ev := func(tool string) PaneEvent {
		return PaneEvent{Kind: PaneEventOutdated, Agent: "api-claude", Outdated: &SessionOutdated{Label: "deskpixie: fix login", Tool: tool, App: webstorm}}
	}
	fx := ctl().Step(PaneInput(ev("claude")), T0)
	wantContains(t, fx, eNotify("deskpixie: fix login runs an older pix than the app: restart it with pix claude --resume to update it."))
	if anyKind(fx, EfStopWorker, EfPrompt, EfTellSession, EfStopCoordinator) {
		t.Fatal(fmtEffects(fx))
	}
	wantContains(t, ctl().Step(PaneInput(ev("codex")), T0), eNotify("deskpixie: fix login runs an older pix than the app: restart it with pix codex resume to update it."))
}
