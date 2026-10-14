package voice

import (
	"reflect"
	"slices"
	"testing"
	"time"
)

// Open / InOpen: start a conversation like the hotkey, but only from rest and never end one.

func TestOpenFromIdleConnectsLikeTheHotkey(t *testing.T) {
	a, b := newConv(), newConv()
	got := a.Step(OpenInput(), t0())
	want := b.Step(HotkeyInput(), t0())
	if !reflect.DeepEqual(got, want) || a.Phase() != PhaseConnecting {
		t.Fatalf("open %+v, hotkey %+v", got, want)
	}
}

func TestOpenLeavesAConversationUnderWayAlone(t *testing.T) {
	c := newConv()
	c.Step(OpenInput(), t0())
	if fx := c.Step(OpenInput(), ms(t0(), 10)); len(fx) != 0 || c.Phase() != PhaseConnecting {
		t.Fatalf("connecting: %+v (%v)", fx, c.Phase())
	}
	model(c, evReady, ms(t0(), 20))
	if fx := c.Step(OpenInput(), ms(t0(), 30)); len(fx) != 0 || c.Phase() != PhaseListening {
		t.Fatalf("listening: %+v (%v)", fx, c.Phase())
	}
	c.Step(FailedInput(Failure{Kind: FailConnectionLost}), ms(t0(), 40))
	if fx := c.Step(OpenInput(), ms(t0(), 50)); len(fx) != 0 || c.Phase() != PhaseCooldown {
		t.Fatalf("cooldown: %+v (%v)", fx, c.Phase())
	}
}

func TestRuntimeOpenStartsAConversationOnlyAtRest(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	if !r.Open() {
		t.Fatal("refused at rest")
	}
	eventually(t, "connect", func() bool { return len(m.connects()) == 1 })
	if r.Open() {
		t.Fatal("accepted while connecting")
	}
	m.sess.events <- Event{Kind: EvReady}
	eventually(t, "mic start", func() bool { return ui.has(UIEvent{Kind: "mic", Text: "start"}) })
	eventually(t, "open", r.IsOpen)
	if r.Open() {
		t.Fatal("accepted while open")
	}
	if len(m.connects()) != 1 {
		t.Fatal("connected twice")
	}
	r.Hotkey() // the user ends it
	eventually(t, "idle", func() bool { st := ui.states(); return st[len(st)-1] == "idle" })
	eventually(t, "at rest", r.Open)
}

func TestRuntimeOpenRefusesWhileTheMicIsUnusable(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	r.MicFailed("Microphone access denied")
	eventually(t, "mic failure recorded", func() bool { return r.micUnavailable.Load() != nil })
	if r.Open() {
		t.Fatal("accepted with the mic unusable")
	}
	time.Sleep(20 * time.Millisecond)
	if len(m.connects()) != 0 || slices.Contains(ui.states(), "error") {
		t.Fatalf("connects %d, states %v", len(m.connects()), ui.states())
	}
}

func TestRuntimeOpenAfterCloseIsRefused(t *testing.T) {
	r, _, _ := newTestRuntime(t, "sk-test")
	r.Close()
	if r.Open() {
		t.Fatal("accepted after Close")
	}
}
