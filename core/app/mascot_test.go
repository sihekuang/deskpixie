package app

// Port of ai-mascot src-tauri/src/mascot_state.rs tests (15 of 15).

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/packs"
)

// bundledMascots is the repo's mascots/ folder (Rust uses gen_bit's manifest; the bundled Bit is
// the same pack).
func bundledMascots(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../mascots")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func bit(t *testing.T) *packs.Pack {
	t.Helper()
	found, _ := packs.Load(bundledMascots(t))
	p, ok := found["bit"]
	if !ok {
		t.Fatal("no bit pack")
	}
	return &p
}

// bitWithOneShots is Bit with the given states made non-looping (a user pack could ship this).
func bitWithOneShots(t *testing.T, names ...string) *packs.Pack {
	p := bit(t)
	states := map[string]packs.State{}
	for k, v := range p.States {
		states[k] = v
	}
	for _, n := range names {
		s := states[n]
		s.Loop = false
		states[n] = s
	}
	p.States = states
	return p
}

func TestStartsIdleAndSilent(t *testing.T) {
	s := NewMascotState()
	if s.State != "idle" || s.Talking {
		t.Fatalf("got %q %v", s.State, s.Talking)
	}
}

func TestHotkeyTogglesIdleAndListening(t *testing.T) {
	s := NewMascotState()
	if got := s.ToggleListening(); got != "listening" {
		t.Fatal(got)
	}
	if got := s.ToggleListening(); got != "idle" {
		t.Fatal(got)
	}
}

func TestHotkeyFromAnyOtherStateStartsListening(t *testing.T) {
	s := NewMascotState()
	s.SetState("thinking")
	if got := s.ToggleListening(); got != "listening" {
		t.Fatal(got)
	}
}

func TestSetStateRejectsUnknownNames(t *testing.T) {
	s := NewMascotState()
	if s.SetState("dancing") {
		t.Fatal("accepted dancing")
	}
	if s.State != "idle" {
		t.Fatal(s.State)
	}
	if !s.SetState("delegating") || s.State != "delegating" {
		t.Fatal(s.State)
	}
}

func TestAOneShotStateEndsWhenThePlayerFinishesIt(t *testing.T) {
	s := NewMascotState()
	s.SetState("happy")
	// Bit's happy: 8 frames at 8 fps, plus one frame of slack for the player's timer.
	seq, after, ok := s.OneShot(bit(t))
	if !ok {
		t.Fatal("happy plays once")
	}
	if after != 9*125*time.Millisecond {
		t.Fatal(after)
	}
	if !s.FinishOneShot(seq) || s.State != "idle" {
		t.Fatal(s.State)
	}
	if s.FinishOneShot(seq) {
		t.Fatal("only once")
	}
}

func TestLoopingStatesNeverEndOnTheirOwn(t *testing.T) {
	s := NewMascotState()
	if _, _, ok := s.OneShot(bit(t)); ok {
		t.Fatal("idle")
	}
	s.SetState("thinking")
	if _, _, ok := s.OneShot(bit(t)); ok {
		t.Fatal("thinking")
	}
}

func TestANewerStateChangeCancelsThePendingEnd(t *testing.T) {
	s := NewMascotState()
	s.SetState("happy")
	seq, _, _ := s.OneShot(bit(t))
	s.SetState("listening")
	if s.FinishOneShot(seq) || s.State != "listening" {
		t.Fatal(s.State)
	}
	// Even a fresh `happy` after another change gets its own full run.
	s.SetState("happy")
	if s.FinishOneShot(seq) || s.State != "happy" {
		t.Fatal(s.State)
	}
	s.ToggleListening()
	if _, _, ok := s.OneShot(bit(t)); ok {
		t.Fatal("listening is not a one-shot")
	}
}

func TestAStaleCelebrationCannotCutANewerOneShort(t *testing.T) {
	s := NewMascotState()
	first, ok := s.Celebrate(bit(t))
	if !ok {
		t.Fatal("from idle")
	}
	if first.Then != "idle" || first.After != 9*125*time.Millisecond {
		t.Fatalf("%+v", first)
	}
	s.SetState("idle") // the first one ended some other way
	second, ok := s.Celebrate(bit(t))
	if !ok {
		t.Fatal("idle again")
	}
	if s.EndHold(first) {
		t.Fatal("the first celebration's timer fires late")
	}
	if s.State != "happy" {
		t.Fatal(s.State)
	}
	if !s.EndHold(second) || s.State != "idle" {
		t.Fatal(s.State)
	}
}

func TestALoopingHappyIsHeldForTwoSeconds(t *testing.T) {
	s := NewMascotState()
	looping := bitWithOneShots(t)
	st := looping.States["happy"]
	st.Loop = true
	looping.States["happy"] = st
	h, _ := s.Celebrate(looping)
	if h.After != Hold {
		t.Fatal(h.After)
	}
	s.SetState("idle")
	h, _ = s.Celebrate(nil)
	if h.After != Hold {
		t.Fatal("no pack: the plain hold")
	}
}

func TestANonLoopingErrorFlashRestoresDelegating(t *testing.T) {
	pack := bitWithOneShots(t, "error")
	s := NewMascotState()
	s.SetState("delegating")
	h, ok := s.FlashError(pack)
	if !ok {
		t.Fatal("from delegating")
	}
	if s.State != "error" {
		t.Fatal(s.State)
	}
	if h.Then != "delegating" || h.After != 9*125*time.Millisecond {
		t.Fatalf("%+v", h)
	}
	if !s.EndHold(h) || s.State != "delegating" {
		t.Fatal(s.State)
	}
}

func TestAnErrorFlashFromIdleHoldsALoopingErrorForTwoSeconds(t *testing.T) {
	s := NewMascotState()
	h, _ := s.FlashError(bit(t))
	if h.Then != "idle" || h.After != Hold {
		t.Fatalf("%+v", h)
	}
	s.SetState("listening")
	if s.EndHold(h) {
		t.Fatal("a conversation started meanwhile")
	}
	if s.State != "listening" {
		t.Fatal(s.State)
	}
}

func TestNonLoopingConversationStatesStayHeldSoNothingInterruptsThem(t *testing.T) {
	pack := bitWithOneShots(t, "listening", "thinking", "delegating")
	for _, name := range []string{"listening", "thinking", "delegating"} {
		s := NewMascotState()
		s.SetState(name)
		if _, _, ok := s.OneShot(pack); ok {
			t.Errorf("%s is held by its owner, not mirrored to idle", name)
		}
		if _, ok := s.Celebrate(pack); ok {
			t.Errorf("%s: no celebration", name)
		}
		if name != "delegating" {
			if _, ok := s.FlashError(pack); ok {
				t.Errorf("%s: no error flash", name)
			}
		}
		if s.State != name {
			t.Errorf("state %s, want %s", s.State, name)
		}
	}
}

func TestTalkLevelStaysInRange(t *testing.T) {
	for _, levels := range []uint32{2, 3, 4, 8} {
		for tick := uint64(0); tick < 2000; tick++ {
			if TalkLevel(tick, levels) >= levels {
				t.Fatalf("tick %d levels %d", tick, levels)
			}
		}
	}
}

func TestTalkLevelUsesBothClosedAndFullyOpen(t *testing.T) {
	seen := map[uint32]bool{}
	for tick := uint64(0); tick < 2000; tick++ {
		seen[TalkLevel(tick, 4)] = true
	}
	if !seen[0] || !seen[3] {
		t.Fatal(seen)
	}
}

func TestTalkLevelIsDeterministic(t *testing.T) {
	if TalkLevel(123, 4) != TalkLevel(123, 4) {
		t.Fatal("not deterministic")
	}
}
