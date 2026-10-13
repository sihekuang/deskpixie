package voice

// Synthesized start/end chimes (spec §5): no audio files to ship.
// Port of ai-mascot crates/voice-core/src/earcons.rs.

import (
	"math"

	"github.com/sihekuang/deskpixie/core/audio"
)

const (
	chimeNoteSecs  = 0.09
	chimeFadeSecs  = 0.01
	chimeAmplitude = 0.25
)

// ChimeSamples is the chime at 24 kHz mono: start = rising (E5 → A5), end = falling (A5 → D5).
func ChimeSamples(kind Chime) []int16 {
	notes := [2]float32{659.25, 880.0}
	if kind == ChimeEnd {
		notes = [2]float32{880.0, 587.33}
	}
	out := chimeNote(notes[0])
	return append(out, chimeNote(notes[1])...)
}

func chimeNote(freq float32) []int16 {
	const rate = float32(audio.SampleRate)
	n := int(math.Round(float64(rate * chimeNoteSecs)))
	fade := float32(math.Round(float64(rate * chimeFadeSecs)))
	out := make([]int16, n)
	for i := range out {
		t := float32(i) / rate
		edge := float32(min(i, n-1-i))
		env := min(edge/fade, 1)
		v := float32(math.Sin(float64(2*math.Pi*freq*t))) * chimeAmplitude * env * 32767
		out[i] = int16(v)
	}
	return out
}
