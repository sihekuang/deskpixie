package audio

import (
	"context"
	"fmt"
	"sync"
)

// ChainCleaner runs a Chain of float32 stages as a Cleaner: 24 kHz mono int16 in, int16 out.
// Safe for use from several goroutines (frames are processed one at a time).
type ChainCleaner struct {
	// OnError, if set, hears each error Process swallows (the frame then passes through).
	OnError func(error)

	mu    sync.Mutex
	chain *Chain
	ctx   context.Context
	buf   []float32
	spill []int16
}

var _ Cleaner = (*ChainCleaner)(nil)

// NewChainCleaner wraps c, which must take and give SampleRate audio.
func NewChainCleaner(c *Chain) (*ChainCleaner, error) {
	if c.InputRate() != SampleRate || c.OutputRate() != SampleRate {
		return nil, fmt.Errorf("audio: a Cleaner chain must run %d Hz to %d Hz, not %d Hz to %d Hz",
			SampleRate, SampleRate, c.InputRate(), c.OutputRate())
	}
	return &ChainCleaner{chain: c, ctx: context.Background()}, nil
}

// Chain is the wrapped chain (e.g. to SetTap on it).
func (a *ChainCleaner) Chain() *Chain { return a.chain }

// Process cleans a frame. If the chain gives back as many samples as it was handed (every
// stage here does), they are written over in and in is returned; otherwise the result is in
// a buffer of the adapter's that is valid until the next call. If a stage fails, the frame
// comes back unchanged and OnError hears why.
func (a *ChainCleaner) Process(in []int16) []int16 {
	out, err := a.Clean(in)
	if err != nil {
		if a.OnError != nil {
			a.OnError(err)
		}
		return in
	}
	return out
}

// Clean is Process that reports a stage's error instead of hiding it; on error in is left
// untouched.
func (a *ChainCleaner) Clean(in []int16) ([]int16, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if cap(a.buf) < len(in) {
		a.buf = make([]float32, len(in))
	}
	buf := a.buf[:len(in)]
	for i, s := range in {
		buf[i] = Int16ToFloat(s)
	}
	out, err := a.chain.Process(a.ctx, buf)
	if err != nil {
		return in, err
	}
	dst := in
	if len(out) != len(in) {
		if cap(a.spill) < len(out) {
			a.spill = make([]int16, len(out))
		}
		dst = a.spill[:len(out)]
	}
	for i, s := range out {
		dst[i] = FloatToInt16(s)
	}
	return dst, nil
}

// Close closes the chain's stages.
func (a *ChainCleaner) Close() error { return a.chain.Close() }
