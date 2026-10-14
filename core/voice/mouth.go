package voice

import "math"

// Turns the loudness of Bit's own voice into mouth levels (spec §5 "Mouth"). Fast attack,
// slower release, so the mouth snaps open on syllables and eases shut.
// Port of ai-mascot crates/voice-core/src/mouth_meter.rs.

const (
	mouthAttack  = 0.6
	mouthRelease = 0.25
	// mouthOpenAt: below this smoothed RMS the mouth is closed.
	mouthOpenAt = 0.02
	// mouthStep: each further step of this much RMS opens the mouth one more level.
	mouthStep = 0.08
)

// MouthMeter smooths playback RMS into a mouth level in [0, levels-1].
type MouthMeter struct {
	levels   int
	smoothed float32
}

// NewMouthMeter takes the pack's mouth level count; fewer than 2 is treated as 2.
func NewMouthMeter(levels int) *MouthMeter {
	return &MouthMeter{levels: max(levels, 2)}
}

// Update feeds one RMS sample (0..1; NaN/Inf count as 0, others are clamped) and returns the level.
func (m *MouthMeter) Update(rms float32) int {
	r := float64(rms)
	if math.IsNaN(r) || math.IsInf(r, 0) {
		r = 0
	}
	x := float32(min(max(r, 0), 1))
	k := float32(mouthRelease)
	if x > m.smoothed {
		k = mouthAttack
	}
	m.smoothed += (x - m.smoothed) * k
	return m.Level()
}

// Level is the current mouth level without feeding a sample.
func (m *MouthMeter) Level() int {
	if m.smoothed < mouthOpenAt {
		return 0
	}
	top := m.levels - 1
	return min(1+int((m.smoothed-mouthOpenAt)/mouthStep), top)
}

// Reset closes the mouth immediately.
func (m *MouthMeter) Reset() { m.smoothed = 0 }
