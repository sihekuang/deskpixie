package gemini

// Streaming mono int16 sample-rate conversion by linear interpolation. Port of
// voice-gemini/src/resample.rs.
//
// The voice core's audio is 24 kHz; Gemini Live wants 16 kHz input and sends 24 kHz output.
// Linear interpolation is enough for speech going to a recogniser, and it carries state
// across chunks so frame boundaries produce no clicks or gaps.

import "math"

// Resampler converts a stream of samples from one rate to another, one chunk at a time.
type Resampler struct {
	from, to int
	step     float64 // input samples advanced per output sample
	carry    int16   // last input sample of the previous chunk (index 0 of the next buffer)
	hasCarry bool
	pos      float64 // position of the next output sample, in working-buffer coordinates
}

func NewResampler(from, to int) *Resampler {
	if from <= 0 || to <= 0 {
		panic("gemini: sample rates must be positive")
	}
	return &Resampler{from: from, to: to, step: float64(from) / float64(to)}
}

func (r *Resampler) FromRate() int { return r.from }
func (r *Resampler) ToRate() int   { return r.to }

// Process resamples the next chunk of the stream.
func (r *Resampler) Process(input []int16) []int16 {
	if r.from == r.to {
		return append([]int16{}, input...)
	}
	if len(input) == 0 {
		return []int16{}
	}
	buf := make([]int16, 0, len(input)+1)
	if r.hasCarry {
		buf = append(buf, r.carry)
	}
	buf = append(buf, input...)
	last := float64(len(buf) - 1)
	out := make([]int16, 0, int(math.Ceil(float64(len(input))/r.step))+1)
	// Only interpolate between two known samples; the rest waits for the next chunk.
	for r.pos < last {
		i := int(math.Floor(r.pos))
		frac := r.pos - float64(i)
		a, b := float64(buf[i]), float64(buf[i+1])
		v := math.Round(a + (b-a)*frac)
		out = append(out, int16(max(math.MinInt16, min(math.MaxInt16, v))))
		r.pos += r.step
	}
	r.pos -= last
	r.carry, r.hasCarry = buf[len(buf)-1], true
	return out
}

// Reset forgets the stream so far (e.g. at the start of a new reply).
func (r *Resampler) Reset() {
	r.hasCarry = false
	r.pos = 0
}
