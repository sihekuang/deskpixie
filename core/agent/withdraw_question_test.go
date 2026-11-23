package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/voice"
)

// #490: a question answered (or gone) before Pixie got to say it goes unsaid: queued behind her
// speech, it asked about something you had already answered.

// askedID: the id of the request the notice in fx asks.
func askedID(t *testing.T, fx []Effect) RequestID {
	t.Helper()
	in := effectsOfKind(fx, EfInject)
	if len(in) != 1 || in[0].Notice.Question == 0 {
		t.Fatalf("%s", fmtEffects(fx))
	}
	return RequestID(in[0].Notice.Question)
}

func TestAPickedMenusNoticeIsWithdrawnOnce(t *testing.T) {
	c := ctl()
	openVoice(c)
	fx := askSession(c, dbChoice, T0)
	id := askedID(t, fx)
	p := c.confirms.Pending()[0]
	fx = c.Step(ChooseInput(p.Token, 1), T0+1)
	wantContains(t, fx, eWithdrawAsk(id))
	if fx := c.Step(ChooseInput(p.Token, 1), T0+2); len(effectsOfKind(fx, EfWithdrawQuestion)) != 0 {
		t.Fatalf("withdrawn twice: %s", fmtEffects(fx))
	}
}

// Answered in its terminal, replaced by the session's next one, or its session closed.
func TestAGoneQuestionsNoticeIsWithdrawn(t *testing.T) {
	next := dbChoice
	next.ID, next.Text = "q9", "Which region?"
	for name, end := range map[string]func(c *AgentController) []Effect{
		"gone":     func(c *AgentController) []Effect { return sessionGone(c, dbChoice.ID, T0+1) },
		"replaced": func(c *AgentController) []Effect { return askSession(c, next, T0+1) },
		"closed": func(c *AgentController) []Effect {
			return c.Step(PaneInput(PaneEvent{Kind: PaneEventExited, Agent: sessionName}), T0+1)
		},
	} {
		c := ctl()
		openVoice(c)
		id := askedID(t, askSession(c, dbChoice, T0))
		fx := end(c)
		if got := effectsOfKind(fx, EfWithdrawQuestion); len(got) != 1 || got[0].ID != id {
			t.Errorf("%s: %s", name, fmtEffects(fx))
		}
		if name == "replaced" && askedID(t, fx) == id {
			t.Errorf("the next question has the last one's id")
		}
	}
}

// Asked again while it waits (pix reconnected), or another session's answer: still to be said.
func TestAWaitingQuestionsNoticeStays(t *testing.T) {
	c := ctl()
	openVoice(c)
	askedID(t, askSession(c, dbChoice, T0))
	fx := askSession(c, dbChoice, T0+1)
	fx = append(fx, sessionGone(c, "q77", T0+2)...)
	if len(effectsOfKind(fx, EfWithdrawQuestion)) != 0 {
		t.Fatalf("%s", fmtEffects(fx))
	}
}

// Nothing said it yet (no conversation open): there is no notice to withdraw.
func TestAQuestionPixieWasNotHandedWithdrawsNothing(t *testing.T) {
	c := ctl()
	fx := askSession(c, dbChoice, T0)
	fx = append(fx, c.Step(ChooseInput(c.confirms.Pending()[0].Token, 1), T0+1)...)
	if len(effectsOfKind(fx, EfWithdrawQuestion)) != 0 {
		t.Fatalf("%s", fmtEffects(fx))
	}
}

// The run of Nov 24: the questions were answered while Pixie was still speaking.
func TestAQuestionAnsweredWhilePixieSpeaksIsNeverSaid(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	v := voice.NewConversation(voice.ConversationConfig{IdleTimeout: time.Hour})
	var said []string
	step := func(in voice.Input) {
		now = now.Add(10 * time.Millisecond)
		for _, e := range v.Step(in, now) {
			if e.Kind == voice.EfInject {
				said = append(said, e.Text)
			}
		}
	}
	model := func(k voice.EventKind) { step(voice.ModelInput(voice.Event{Kind: k})) }
	step(voice.OpenToSpeakInput())
	model(voice.EvReady)
	step(voice.InjectInput(Notice{Text: "Session update: api is done."})) // Pixie is saying this
	c := ctl()
	openVoice(c)
	run := func(fx []Effect) {
		for _, e := range fx {
			switch e.Kind {
			case EfInject:
				step(voice.InjectInput(e.Notice))
			case EfWithdrawQuestion:
				step(voice.WithdrawQuestionInput(uint64(e.ID)))
			}
		}
	}
	second := dbChoice
	second.ID, second.Text = "q9", "Which region?"
	run(askSession(c, dbChoice, T0))
	run(c.Step(ChooseInput(c.confirms.Pending()[0].Token, 1), T0+1))
	run(askSession(c, second, T0+2)) // still waits
	for range 5 {
		model(voice.EvResponseCreated)
		model(voice.EvResponseDone)
		step(voice.TickInput(true))
	}
	if len(said) != 2 || !strings.Contains(said[1], "Which region?") {
		t.Fatalf("said %q", said)
	}
}

var _ QuestionWithdrawer = (*voice.Runtime)(nil)
