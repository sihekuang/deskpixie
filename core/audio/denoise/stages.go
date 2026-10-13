package denoise

import (
	"context"
	"fmt"

	"github.com/sihekuang/deskpixie/core/audio"
)

// The filter's chain is: resample the mic to the model's rate -> (dither) -> the model ->
// resample back. These are the stages around the model (ModelStage is in model.go). Each owns
// its output buffer, so a frame allocates nothing once the buffers have grown.

// resampleStage converts between the mic's 24 kHz and a model rate with the streaming filters
// in resample.go; their history carries across batches.
type resampleStage struct {
	name     string
	from, to int
	// block is the input granule that maps to a whole number of outputs.
	block int
	run   func(in, out []float32)
	clear func()
	out   []float32
}

var (
	_ audio.Stage      = (*resampleStage)(nil)
	_ audio.InputRater = (*resampleStage)(nil)
)

// newResampleStage makes the resampler from -> to: 24k<->48k (half-band) or 24k<->16k
// (polyphase rational).
func newResampleStage(from, to int) *resampleStage {
	s := &resampleStage{name: fmt.Sprintf("to%dk", to/1000), from: from, to: to}
	switch {
	case from == audio.SampleRate && to == 48000:
		up := newUpsampler()
		s.block = 1
		s.run = func(in, out []float32) {
			for i, x := range in {
				out[2*i], out[2*i+1] = up.push(x)
			}
		}
		s.clear = up.clear
	case from == 48000 && to == audio.SampleRate:
		down := newDownsampler()
		s.block = 2
		s.run = func(in, out []float32) {
			for i := range out {
				out[i] = down.push(in[2*i], in[2*i+1])
			}
		}
		s.clear = down.clear
	case from == audio.SampleRate && to == 16000:
		r := newRational(2, 3)
		s.block = 3
		s.run, s.clear = r.run, r.clear
	case from == 16000 && to == audio.SampleRate:
		r := newRational(3, 2)
		s.block = 2
		s.run, s.clear = r.run, r.clear
	default:
		panic(fmt.Sprintf("denoise: no resampler from %d Hz to %d Hz", from, to))
	}
	return s
}

func (s *resampleStage) Name() string    { return s.name }
func (s *resampleStage) InputRate() int  { return s.from }
func (s *resampleStage) OutputRate() int { return s.to }

func (s *resampleStage) Process(_ context.Context, in []float32) ([]float32, error) {
	if len(in)%s.block != 0 {
		return nil, fmt.Errorf("resampling %d Hz to %d Hz takes whole %d-sample blocks, got %d samples",
			s.from, s.to, s.block, len(in))
	}
	n := len(in) * s.to / s.from
	if cap(s.out) < n {
		s.out = make([]float32, n)
	}
	out := s.out[:n]
	s.run(in, out)
	return out, nil
}

// reset forgets the filter history, without allocating.
func (s *resampleStage) reset() { s.clear() }

// ditherStage applies dither (dither.go) to each model-rate hop before the model sees it.
// Only libdf needs it, so only New puts it in the chain.
type ditherStage struct {
	hop, rate int
	d         dither
	out       []float32
}

var (
	_ audio.Stage      = (*ditherStage)(nil)
	_ audio.InputRater = (*ditherStage)(nil)
)

func newDitherStage(hop, rate int) *ditherStage {
	return &ditherStage{hop: hop, rate: rate, d: newDither()}
}

func (s *ditherStage) Name() string    { return "dither" }
func (s *ditherStage) InputRate() int  { return s.rate }
func (s *ditherStage) OutputRate() int { return 0 }

func (s *ditherStage) Process(_ context.Context, in []float32) ([]float32, error) {
	if len(in)%s.hop != 0 {
		return nil, fmt.Errorf("dither takes whole %d-sample hops, got %d samples", s.hop, len(in))
	}
	if cap(s.out) < len(in) {
		s.out = make([]float32, len(in))
	}
	out := s.out[:len(in)]
	copy(out, in)
	for k := 0; k < len(out); k += s.hop {
		s.d.apply(out[k : k+s.hop])
	}
	return out, nil
}

// reset restarts the noise sequence, so a reset filter replays exactly.
func (s *ditherStage) reset() { s.d = newDither() }
