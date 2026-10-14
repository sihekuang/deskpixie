package audio

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func mustChain(t *testing.T, stages ...Stage) *Chain {
	t.Helper()
	c, err := NewChain(SampleRate, stages...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTheAdapterIsACleanerThatWritesBackInPlace(t *testing.T) {
	a, err := NewChainCleaner(mustChain(t, &scale{name: "half", k: 0.5}))
	if err != nil {
		t.Fatal(err)
	}
	var _ Cleaner = a
	in := []int16{1000, -1000, 32767, -32768, 3}
	out := a.Process(in)
	if &out[0] != &in[0] {
		t.Fatal("not in place")
	}
	// Round half away from zero: 32767/2 = 16383.5 -> 16384; 3/2 = 1.5 -> 2.
	if want := []int16{500, -500, 16384, -16384, 2}; !slices.Equal(out, want) {
		t.Fatalf("out %v, want %v", out, want)
	}
}

func TestTheAdapterClampsToInt16(t *testing.T) {
	a, _ := NewChainCleaner(mustChain(t, &scale{name: "loud", k: 4}))
	if out := a.Process([]int16{20000, -20000, 0}); !slices.Equal(out, []int16{32767, -32768, 0}) {
		t.Fatalf("out %v", out)
	}
}

func TestTheAdapterRoundTripsSamplesExactly(t *testing.T) {
	a, _ := NewChainCleaner(mustChain(t))
	in := []int16{0, 1, -1, 12345, -12345, 32767, -32768}
	if out := a.Process(slices.Clone(in)); !slices.Equal(out, in) {
		t.Fatalf("out %v", out)
	}
}

type erring struct{ calls int }

func (e *erring) Name() string    { return "erring" }
func (e *erring) OutputRate() int { return 0 }
func (e *erring) Process(_ context.Context, in []float32) ([]float32, error) {
	e.calls++
	return nil, errors.New("nope")
}

func TestTheAdapterPassesTheFrameThroughOnAnError(t *testing.T) {
	var errs []error
	a, _ := NewChainCleaner(mustChain(t, &scale{name: "x", k: 2}, &erring{}))
	a.OnError = func(err error) { errs = append(errs, err) }
	in := []int16{1, 2, 3}
	if out := a.Process(in); !slices.Equal(out, []int16{1, 2, 3}) {
		t.Fatalf("out %v", out)
	}
	if _, err := a.Clean([]int16{4}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("Clean err = %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("OnError called %d times (Process only)", len(errs))
	}
}

func TestTheAdapterHandsBackAChangedLength(t *testing.T) {
	a, _ := NewChainCleaner(mustChain(t, &pairs{name: "p"}))
	if out := a.Process([]int16{1, 2, 3}); !slices.Equal(out, []int16{1, 2}) {
		t.Fatalf("out %v", out)
	}
	if out := a.Process([]int16{4}); !slices.Equal(out, []int16{3, 4}) {
		t.Fatalf("out %v", out)
	}
}

func TestTheAdapterNeeds24kHzInAndOut(t *testing.T) {
	c16, _ := NewChain(SampleRate, &scale{name: "down", k: 1, to: 16000})
	if _, err := NewChainCleaner(c16); err == nil {
		t.Fatal("a 16 kHz output was accepted")
	}
	c48, _ := NewChain(48000)
	if _, err := NewChainCleaner(c48); err == nil {
		t.Fatal("a 48 kHz input was accepted")
	}
}

func TestTheAdapterClosesItsChain(t *testing.T) {
	s := &scale{name: "x", k: 1}
	a, _ := NewChainCleaner(mustChain(t, s))
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	if s.closed != 1 {
		t.Fatalf("closed %d", s.closed)
	}
}

func TestTheAdapterDoesNotAllocatePerFrame(t *testing.T) {
	a, _ := NewChainCleaner(mustChain(t, &scale{name: "x", k: 0.5}))
	fr := make([]int16, FrameSamples)
	a.Process(fr)
	if n := testing.AllocsPerRun(20, func() { a.Process(fr) }); n != 0 {
		t.Fatalf("%v allocations per frame", n)
	}
}
