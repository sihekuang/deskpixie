package app

// What the mascot is doing. Talking is independent of state (mouth layer). Port of ai-mascot
// src-tauri/src/mascot_state.rs.

import (
	"math"
	"slices"
	"time"

	"github.com/sihekuang/deskpixie/core/packs"
)

// Hold is how long a looping `happy` or `error` is shown by Celebrate / FlashError.
const Hold = 2 * time.Second

// transient are the states the app shows briefly and then leaves on its own.
var transient = []string{"happy", "error"}

// MascotState is the mascot's state and talking flag. Not safe for concurrent use (the app
// guards it with its lock).
type MascotState struct {
	State   string
	Talking bool
	// seq is bumped on every state change, so a pending one-shot end can tell it is stale.
	seq uint64
}

// NewMascotState starts idle and silent.
func NewMascotState() MascotState { return MascotState{State: "idle"} }

// ToggleListening: listening → idle, anything else → listening (the hotkey without voice).
func (s *MascotState) ToggleListening() string {
	if s.State == "listening" {
		s.State = "idle"
	} else {
		s.State = "listening"
	}
	s.seq++
	return s.State
}

// SetState switches to a known state name; false (no change) for an unknown one.
func (s *MascotState) SetState(name string) bool {
	if !slices.Contains(packs.StateNames[:], name) {
		return false
	}
	s.State = name
	s.seq++
	return true
}

// playOnce is how long the player takes to play the current state once, if it does not loop.
// One frame of slack keeps the app from cutting the last frame short.
func (s *MascotState) playOnce(p *packs.Pack) (time.Duration, bool) {
	if p == nil {
		return 0, false
	}
	spec, ok := p.States[s.State]
	if !ok || spec.Loop {
		return 0, false
	}
	frameMs := 1000 / uint64(max(p.FPS, 1))
	return time.Duration((uint64(spec.Frames)+1)*frameMs) * time.Millisecond, true
}

// OneShot: for a non-looping transient state (`happy` or `error`, e.g. from the tray), this
// change's sequence number and how long the player takes to play it once, after which it falls
// back to `idle` on its own. Conversation states are never mirrored: their owner holds them.
func (s *MascotState) OneShot(p *packs.Pack) (seq uint64, after time.Duration, ok bool) {
	if !slices.Contains(transient, s.State) {
		return 0, 0, false
	}
	after, ok = s.playOnce(p)
	return s.seq, after, ok
}

// FinishOneShot: the one-shot started by change seq has played out: back to idle. False (no
// change) when anything changed the state since.
func (s *MascotState) FinishOneShot(seq uint64) bool {
	return s.EndHold(HeldState{Seq: seq, Then: "idle"})
}

// HeldState is a state shown for a while (Celebrate, FlashError): which change started it,
// where to go back to, and when. (Rust `Hold`; the Go name avoids clashing with the constant.)
type HeldState struct {
	Seq   uint64
	Then  string
	After time.Duration
}

// Celebrate plays `happy` (a new canvas page). Only from idle, so a conversation is never
// interrupted. The caller announces `happy` and calls EndHold after HeldState.After.
func (s *MascotState) Celebrate(p *packs.Pack) (HeldState, bool) {
	if s.State != "idle" {
		return HeldState{}, false
	}
	return s.hold("happy", p)
}

// FlashError shows `error` (a delegated job failed), then returns to where it was. Only from a
// resting state (idle or delegating).
func (s *MascotState) FlashError(p *packs.Pack) (HeldState, bool) {
	if s.State != "idle" && s.State != "delegating" {
		return HeldState{}, false
	}
	return s.hold("error", p)
}

func (s *MascotState) hold(name string, p *packs.Pack) (HeldState, bool) {
	then := s.State
	if !s.SetState(name) {
		return HeldState{}, false
	}
	after, ok := s.playOnce(p)
	if !ok {
		after = Hold
	}
	return HeldState{Seq: s.seq, Then: then, After: after}, true
}

// EndHold goes back to h.Then, unless anything changed the state since the hold started.
func (s *MascotState) EndHold(h HeldState) bool {
	return h.Seq == s.seq && s.SetState(h.Then)
}

// TalkLevel is the fake speech envelope for "Simulate talking", sampled at ~30 Hz.
func TalkLevel(tick uint64, levels uint32) uint32 {
	t := float64(tick)
	envelope := math.Abs(math.Sin(t*0.43) * math.Sin(t*0.17))
	var top float64
	if levels > 0 {
		top = float64(levels - 1)
	}
	return uint32(math.Round(envelope * top))
}
