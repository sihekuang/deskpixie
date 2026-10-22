package agent

import (
	"slices"
	"strings"
	"testing"
)

// #114: a pix session's finished turn, announced when the setting is on (#229: it is by default).

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

func settingOff(s *Settings) { s.SessionTurns = false }

func TestWithTheSettingOffYourOwnTurnIsNotAnnounced(t *testing.T) {
	if fx := sessionTurn(ctlWith(settingOff), longTurn, T0); anyKind(fx, EfInject, EfNotify, EfChime, EfAppend) {
		t.Fatal(fmtEffects(fx))
	}
}

// #229: Pixie handed it a message and the coordinator was told the answer comes back, so it
// does, whatever the setting.
func TestWithTheSettingOffATurnPixieStartedIsStillAnnounced(t *testing.T) {
	asked := longTurn
	asked.FromPixie = true
	wantContains(t, sessionTurn(ctlWith(settingOff), asked, T0), eNotify(longTurnLine))
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
	if len(bubble) != 1 || bubble[0].Kind != LineSession || bubble[0].App != webstorm || bubble[0].Text != longTurnLine ||
		bubble[0].Session != "api-claude" {
		t.Fatalf("the bubble line brings up its session: %+v", bubble)
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
	rec := o.Push(Line{Kind: LineSession, Text: "api finished.", App: webstorm, Session: "api-claude"}, T0)
	var back OutboxLine
	if err := back.UnmarshalJSON([]byte(rec)); err != nil || back.App != webstorm || back.Kind != LineSession ||
		back.Session != "api-claude" {
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

// #194: told again, a line replaces the unheard one saying the same: the bubble lists it once.
func TestALineToldAgainIsListedOnce(t *testing.T) {
	c := turnCtl(nil)
	sessionTurn(c, longTurn, T0)
	fx := sessionTurn(c, longTurn, T0+120_000)
	items, ok := lastUpdates(t, fx)
	if !ok || len(items) != 1 || items[0].At != T0+120_000 {
		t.Fatalf("%+v", items)
	}
	if n := len(effectsOfKind(fx, EfAppend)); n != 2 {
		t.Fatalf("the older line is saved as delivered too: %s", fmtEffects(fx))
	}
}

// #194: a rebuild restarts the app and the session registers again: its older pix isn't told
// again until OutdatedRetellMs has passed.
func TestAnOlderPixIsNotToldAgainAfterARestart(t *testing.T) {
	ev := PaneEvent{Kind: PaneEventOutdated, Agent: "api-claude", Outdated: &SessionOutdated{Label: "landing", Tool: "claude", App: webstorm}}
	first := ctl()
	fx := first.Step(PaneInput(ev), T0)
	items, _ := lastUpdates(t, fx)
	if len(items) != 1 || items[0].Session != "api-claude" {
		t.Fatalf("%+v", items)
	}
	restarted := func(now Millis) *AgentController {
		c := ctl()
		c.outbox = RestoreOutbox(first.outbox.Lines(), now)
		return c
	}
	if fx := restarted(T0+300_000).Step(PaneInput(ev), T0+300_000); anyKind(fx, EfNotify, EfAppend) {
		t.Fatal(fmtEffects(fx))
	}
	later := T0 + OutdatedRetellMs
	wantContains(t, restarted(later).Step(PaneInput(ev), later), eNotify("landing runs an older pix than the app: restart it with pix claude --resume to update it."))
}

// #261: what Pixie is told about a session names it, so the caption can offer to go there.
func TestASessionUpdateForPixieNamesItsSession(t *testing.T) {
	c := turnCtl(nil)
	openVoice(c)
	var got []string
	for _, e := range sessionTurn(c, longTurn, T0) {
		if e.Kind == EfInject {
			got = e.Notice.Sessions
		}
	}
	if !slices.Equal(got, []string{"api-claude"}) {
		t.Fatal(got)
	}
}
