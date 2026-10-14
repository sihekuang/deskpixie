package voice

import (
	"math"
	"testing"
)

// Ports of ai-mascot crates/voice-core/src/mouth_meter.rs tests.

func TestMouthSilenceIsClosed(t *testing.T) {
	m := NewMouthMeter(4)
	for range 10 {
		if got := m.Update(0); got != 0 {
			t.Fatalf("Update(0) = %d, want 0", got)
		}
	}
}

func TestMouthLoudSpeechOpensFullyWithinThreeUpdates(t *testing.T) {
	m := NewMouthMeter(4)
	var levels []int
	for range 3 {
		levels = append(levels, m.Update(0.3))
	}
	if levels[2] != 3 {
		t.Fatalf("levels %v: last = %d, want 3", levels, levels[2])
	}
	for i := 1; i < len(levels); i++ {
		if levels[i-1] > levels[i] {
			t.Fatalf("attack is not monotonic: %v", levels)
		}
	}
}

func TestMouthClosesAfterSpeechStops(t *testing.T) {
	m := NewMouthMeter(4)
	for range 5 {
		m.Update(0.3)
	}
	closedAfter := 0
	for i := 1; i <= 20; i++ {
		if m.Update(0) == 0 {
			closedAfter = i
			break
		}
	}
	if closedAfter == 0 {
		t.Fatal("mouth never closed")
	}
	if closedAfter < 3 || closedAfter > 15 {
		t.Fatalf("release took %d updates", closedAfter)
	}
}

func TestMouthMediumLoudnessGivesMiddleLevels(t *testing.T) {
	m := NewMouthMeter(4)
	for range 20 {
		m.Update(0.06)
	}
	if got := m.Level(); got != 1 {
		t.Fatalf("level at 0.06 = %d, want 1", got)
	}
	for range 20 {
		m.Update(0.14)
	}
	if got := m.Level(); got != 2 {
		t.Fatalf("level at 0.14 = %d, want 2", got)
	}
}

func TestMouthTwoLevelPackTopsOutAtOne(t *testing.T) {
	m := NewMouthMeter(2)
	for range 10 {
		m.Update(1)
	}
	if got := m.Level(); got != 1 {
		t.Fatalf("level = %d, want 1", got)
	}
}

func TestMouthNaNAndOutOfRangeRMSAreClamped(t *testing.T) {
	m := NewMouthMeter(4)
	if got := m.Update(float32(math.NaN())); got != 0 {
		t.Fatalf("Update(NaN) = %d, want 0", got)
	}
	if got := m.Update(float32(math.Inf(1))); got != 0 {
		t.Fatalf("Update(+Inf) = %d, want 0", got)
	}
	for range 10 {
		m.Update(7.5)
	}
	if got := m.Level(); got != 3 {
		t.Fatalf("level after 7.5 = %d, want 3", got)
	}
	if got := NewMouthMeter(0).Update(1); got != 1 {
		t.Fatalf("fewer than 2 levels is treated as 2: got %d, want 1", got)
	}
}

func TestMouthResetClosesImmediately(t *testing.T) {
	m := NewMouthMeter(4)
	for range 5 {
		m.Update(0.3)
	}
	m.Reset()
	if got := m.Level(); got != 0 {
		t.Fatalf("level after Reset = %d, want 0", got)
	}
}
