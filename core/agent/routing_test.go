package agent

import (
	"reflect"
	"testing"

	"github.com/sihekuang/deskpixie/core/voice"
)

// Ported from crates/agent-core/src/routing.rs (8 tests).

func reply(text string) Line { return NewLine(LineReply, text) }

func notifies(d []RouteDelivery) []string {
	var out []string
	for _, x := range d {
		if x.Kind == DeliverNotify {
			out = append(out, x.Text)
		}
	}
	return out
}

func wantRoute(t *testing.T, got []RouteDelivery, want ...RouteDelivery) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestOpenConversationInjectsWithAPrefix(t *testing.T) {
	r := NewRouter(4)
	wantRoute(t, r.Route(true, reply("Tests pass."), 0), InjectDelivery(voice.Notice{Text: "Agent reply: Tests pass."}))
	wantRoute(t, r.Route(true, NewLine(LineQuestion, "Which branch?"), 0), InjectDelivery(voice.Notice{Text: "Agent question: Which branch?"}))
	wantRoute(t, r.Route(true, NewLine(LineUnsolicited, "Hi."), 0), InjectDelivery(voice.Notice{Text: "Agent says: Hi."}))
}

func TestClosedConversationChimesNotifiesAndOutboxes(t *testing.T) {
	r := NewRouter(4)
	wantRoute(t, r.Route(false, reply("Done."), 0), ChimeDelivery(), NotifyDelivery("Done."), OutboxDelivery(reply("Done.")))
}

func TestUnsolicitedLinesAreOutboxedSilentlyWhenClosed(t *testing.T) {
	r := NewRouter(4)
	u := NewLine(LineUnsolicited, "I tidied the notes.")
	wantRoute(t, r.Route(false, u, 0), OutboxDelivery(u))
}

func TestTheCapIsFourAMinuteThenOneCollapsedLine(t *testing.T) {
	r := NewRouter(4)
	for i := Millis(0); i < 4; i++ {
		if n := len(notifies(r.Route(false, reply("x"), i*1_000))); n != 1 {
			t.Fatalf("%d: %d", i, n)
		}
	}
	for i := Millis(4); i < 7; i++ {
		d := r.Route(false, reply("y"), i*1_000)
		if len(notifies(d)) != 0 {
			t.Fatal("capped")
		}
		if d[len(d)-1].Kind != DeliverOutbox {
			t.Fatal("still outboxed")
		}
	}
	if len(r.Tick(false, 59_999)) != 0 {
		t.Fatal("no slot yet")
	}
	if got := notifies(r.Tick(false, 60_000)); !reflect.DeepEqual(got, []string{"3 more updates"}) {
		t.Fatal(got)
	}
	if len(r.Tick(false, 60_001)) != 0 {
		t.Fatal("collapsed only once")
	}
}

func TestQuestionsBypassTheCap(t *testing.T) {
	r := NewRouter(4)
	for i := Millis(0); i < 4; i++ {
		r.Route(false, reply("x"), i)
	}
	tok := "hawk"
	q := Line{Text: "Run git push in focus? Say confirm hawk.", Kind: LineQuestion, Token: &tok}
	if got := notifies(r.Route(false, q, 10)); !reflect.DeepEqual(got, []string{"Run git push in focus? Say confirm hawk."}) {
		t.Fatal(got)
	}
}

func TestTheWindowBoundaryFreesASlotThroughRoute(t *testing.T) {
	r := NewRouter(1)
	if len(notifies(r.Route(false, reply("a"), 0))) != 1 {
		t.Fatal("a")
	}
	if len(notifies(r.Route(false, reply("b"), 59_999))) != 0 {
		t.Fatal("b")
	}
	if len(notifies(r.Route(false, reply("c"), 60_000))) != 1 {
		t.Fatal("c")
	}
}

func TestOpeningAConversationForgetsTheCollapsedCount(t *testing.T) {
	r := NewRouter(1)
	r.Route(false, reply("a"), 0)
	r.Route(false, reply("b"), 1)
	r.ConversationOpened()
	if len(r.Tick(false, 60_000)) != 0 {
		t.Fatal("tick")
	}
}

func TestOneSuppressedLineSaysUpdateSingular(t *testing.T) {
	r := NewRouter(1)
	r.Route(false, reply("a"), 0)
	r.Route(false, reply("b"), 1)
	if got := notifies(r.Tick(false, 60_000)); !reflect.DeepEqual(got, []string{"1 more update"}) {
		t.Fatal(got)
	}
}
