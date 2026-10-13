package audio

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
)

// InputRater is optionally implemented by a stage that only accepts one input rate (a
// resampler, a model trained at one rate). NewChain checks it against the rate flowing into
// the stage. This is a DeskPixie addition: Howl leaves rate alignment to the composer.
type InputRater interface {
	// InputRate is the rate Process expects, or 0 for any rate.
	InputRate() int
}

// Tap sees each stage's output as it leaves the stage, e.g. for debug recording. out is only
// valid during the call (it is usually the stage's own reused buffer): copy it to keep it.
type Tap func(stage string, out []float32)

// StageInfo names a stage in a Chain and the rate of its output (0 if the rate is unknown).
type StageInfo struct {
	Name string
	Rate int
}

// Chain runs stages in order, each one's output feeding the next. It is itself a Stage
// (with Flusher and Closer), so chains nest. Process and Flush must not be called
// concurrently; SetTap may be called at any time.
type Chain struct {
	stages []Stage
	info   []StageInfo
	in     int
	out    int
	tap    atomic.Pointer[Tap]
	closed atomic.Bool
}

var (
	_ Stage   = (*Chain)(nil)
	_ Flusher = (*Chain)(nil)
	_ Closer  = (*Chain)(nil)
)

// NewChain chains stages that take audio at inRate (0: unknown). It fails if a stage that
// declares an InputRate would receive a different rate.
func NewChain(inRate int, stages ...Stage) (*Chain, error) {
	c := &Chain{stages: stages, in: inRate}
	rate := inRate
	for _, st := range stages {
		if ir, ok := st.(InputRater); ok {
			if want := ir.InputRate(); want != 0 && rate != 0 && want != rate {
				return nil, fmt.Errorf("audio: stage %q wants %d Hz but is fed %d Hz", st.Name(), want, rate)
			}
		}
		if r := st.OutputRate(); r != 0 {
			rate = r
		}
		c.info = append(c.info, StageInfo{Name: st.Name(), Rate: rate})
	}
	c.out = rate
	return c, nil
}

// Name is "chain".
func (c *Chain) Name() string { return "chain" }

// InputRate is the rate the chain was built for (0: unknown).
func (c *Chain) InputRate() int { return c.in }

// OutputRate is the rate leaving the last stage (the input rate if no stage changes it).
func (c *Chain) OutputRate() int { return c.out }

// Stages lists the stages in order with the rate of each one's output, e.g. to name and set
// up a recording per tap.
func (c *Chain) Stages() []StageInfo { return append([]StageInfo(nil), c.info...) }

// SetTap installs (or, with nil, removes) the per-stage tap.
func (c *Chain) SetTap(t Tap) {
	if t == nil {
		c.tap.Store(nil)
		return
	}
	c.tap.Store(&t)
}

func (c *Chain) emit(stage string, out []float32) {
	if t := c.tap.Load(); t != nil {
		(*t)(stage, out)
	}
}

// Process runs in through every stage. A stage's error stops the batch and is returned with
// the stage's name.
func (c *Chain) Process(ctx context.Context, in []float32) ([]float32, error) {
	cur := in
	for _, st := range c.stages {
		out, err := st.Process(ctx, cur)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", st.Name(), err)
		}
		c.emit(st.Name(), out)
		cur = out
	}
	return cur, nil
}

// Flush drains the stages in order: each stage's residual is run through the stages after
// it, which are then flushed in turn, so nothing buffered anywhere is lost.
func (c *Chain) Flush(ctx context.Context) ([]float32, error) {
	var carry []float32
	for _, st := range c.stages {
		var out []float32
		if len(carry) > 0 {
			p, err := st.Process(ctx, carry)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", st.Name(), err)
			}
			// Copied: p may be the stage's reused buffer, and the residual is appended to it.
			out = append(out, p...)
		}
		if f, ok := st.(Flusher); ok {
			r, err := f.Flush(ctx)
			if err != nil {
				return nil, fmt.Errorf("%s: flush: %w", st.Name(), err)
			}
			out = append(out, r...)
		}
		c.emit(st.Name(), out)
		carry = out
	}
	return carry, nil
}

// Close closes every stage that is a Closer, all of them even if one fails. Safe to call
// more than once: later calls do nothing.
func (c *Chain) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	var errs []error
	for _, st := range c.stages {
		if cl, ok := st.(Closer); ok {
			if err := cl.Close(); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", st.Name(), err))
			}
		}
	}
	return errors.Join(errs...)
}
