package audio

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// scale multiplies each sample; it records the order it ran in.
type scale struct {
	name   string
	k      float32
	in, to int
	log    *[]string
	out    []float32
	closed int
}

func (s *scale) Name() string    { return s.name }
func (s *scale) OutputRate() int { return s.to }
func (s *scale) InputRate() int  { return s.in }
func (s *scale) Process(_ context.Context, in []float32) ([]float32, error) {
	if s.log != nil {
		*s.log = append(*s.log, s.name)
	}
	if cap(s.out) < len(in) {
		s.out = make([]float32, len(in))
	}
	s.out = s.out[:len(in)]
	for i, v := range in {
		s.out[i] = v * s.k
	}
	return s.out, nil
}
func (s *scale) Close() error { s.closed++; return nil }

// pairs buffers input and emits only whole pairs; Flush emits the odd one left over.
type pairs struct {
	name string
	buf  []float32
	seen [][]float32
}

func (p *pairs) Name() string    { return p.name }
func (p *pairs) OutputRate() int { return 0 }
func (p *pairs) Process(_ context.Context, in []float32) ([]float32, error) {
	p.seen = append(p.seen, append([]float32(nil), in...))
	p.buf = append(p.buf, in...)
	n := len(p.buf) / 2 * 2
	out := append([]float32(nil), p.buf[:n]...)
	p.buf = append(p.buf[:0], p.buf[n:]...)
	return out, nil
}
func (p *pairs) Flush(context.Context) ([]float32, error) {
	out := append([]float32(nil), p.buf...)
	p.buf = p.buf[:0]
	return out, nil
}

type failing struct{}

func (failing) Name() string    { return "boom" }
func (failing) OutputRate() int { return 0 }
func (failing) Process(context.Context, []float32) ([]float32, error) {
	return nil, errors.New("it broke")
}

type badClose struct{ scale }

func (b *badClose) Close() error { b.closed++; return errors.New("close failed") }

func TestChainRunsStagesInOrder(t *testing.T) {
	var log []string
	c, err := NewChain(24000,
		&scale{name: "a", k: 2, log: &log},
		&scale{name: "b", k: 3, log: &log},
		&scale{name: "c", k: -1, log: &log})
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.Process(context.Background(), []float32{0.1, 0.2})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(log, []string{"a", "b", "c"}) {
		t.Fatalf("order %v", log)
	}
	if len(out) != 2 || out[0] != 0.1*2*3*-1 || out[1] != 0.2*2*3*-1 {
		t.Fatalf("out %v", out)
	}
}

func TestChainTracksTheRateThroughItsStages(t *testing.T) {
	c, err := NewChain(24000,
		&scale{name: "down", k: 1, in: 24000, to: 16000},
		&scale{name: "model", k: 1, in: 16000},
		&scale{name: "up", k: 1, in: 16000, to: 24000})
	if err != nil {
		t.Fatal(err)
	}
	if c.InputRate() != 24000 || c.OutputRate() != 24000 {
		t.Fatalf("rates %d -> %d", c.InputRate(), c.OutputRate())
	}
	want := []StageInfo{{"down", 16000}, {"model", 16000}, {"up", 24000}}
	if got := c.Stages(); !slices.Equal(got, want) {
		t.Fatalf("stages %v", got)
	}
}

func TestChainRejectsARateMismatch(t *testing.T) {
	_, err := NewChain(24000,
		&scale{name: "down", k: 1, in: 24000, to: 16000},
		&scale{name: "model48", k: 1, in: 48000})
	if err == nil || !strings.Contains(err.Error(), "model48") || !strings.Contains(err.Error(), "16000") {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewChain(48000, &scale{name: "down", k: 1, in: 24000}); err == nil {
		t.Fatal("the chain's own input rate is not checked")
	}
	if _, err := NewChain(24000); err != nil {
		t.Fatalf("an empty chain: %v", err)
	}
}

func TestChainWrapsAStageErrorWithItsName(t *testing.T) {
	c, _ := NewChain(24000, &scale{name: "a", k: 1}, failing{})
	_, err := c.Process(context.Background(), []float32{1})
	if err == nil || !strings.Contains(err.Error(), "boom") || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("err = %v", err)
	}
}

func TestChainFlushCascadesThroughLaterStages(t *testing.T) {
	first := &pairs{name: "first"}
	second := &pairs{name: "second"}
	var log []string
	last := &scale{name: "last", k: 10, log: &log}
	c, _ := NewChain(0, first, second, last)
	ctx := context.Background()
	out, _ := c.Process(ctx, []float32{1, 2, 3})
	if !slices.Equal(out, []float32{10, 20}) {
		t.Fatalf("process out %v", out)
	}
	tail, err := c.Flush(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// first's residual (3) goes through second (which holds it, then flushes it) and last.
	if !slices.Equal(tail, []float32{30}) {
		t.Fatalf("flush out %v", tail)
	}
	if got := second.seen[len(second.seen)-1]; !slices.Equal(got, []float32{3}) {
		t.Fatalf("second was handed %v on flush", got)
	}
	if empty, _ := c.Flush(ctx); len(empty) != 0 {
		t.Fatalf("a second flush gives %v", empty)
	}
}

func TestChainCloseClosesEveryStageOnce(t *testing.T) {
	a := &scale{name: "a", k: 1}
	b := &badClose{scale{name: "b", k: 1}}
	d := &scale{name: "d", k: 1}
	c, _ := NewChain(0, a, &pairs{name: "p"}, b, d)
	err := c.Close()
	if err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("err = %v", err)
	}
	if a.closed != 1 || b.closed != 1 || d.closed != 1 {
		t.Fatalf("closed %d %d %d (a failing Close must not stop the rest)", a.closed, b.closed, d.closed)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if a.closed != 1 {
		t.Fatal("closed twice")
	}
}

func TestChainTapsEachStagesOutput(t *testing.T) {
	c, _ := NewChain(0, &scale{name: "a", k: 2}, &pairs{name: "p"}, &scale{name: "b", k: 3})
	type tap struct {
		stage string
		out   []float32
	}
	var got []tap
	c.SetTap(func(stage string, out []float32) {
		got = append(got, tap{stage, append([]float32(nil), out...)})
	})
	ctx := context.Background()
	_, _ = c.Process(ctx, []float32{1, 2, 3})
	_, _ = c.Flush(ctx)
	want := []tap{
		{"a", []float32{2, 4, 6}}, {"p", []float32{2, 4}}, {"b", []float32{6, 12}},
		{"a", nil}, {"p", []float32{6}}, {"b", []float32{18}},
	}
	if len(got) != len(want) {
		t.Fatalf("taps %v", got)
	}
	for i := range want {
		if got[i].stage != want[i].stage || !slices.Equal(got[i].out, want[i].out) {
			t.Fatalf("tap %d = %v, want %v", i, got[i], want[i])
		}
	}
	c.SetTap(nil)
	got = nil
	_, _ = c.Process(ctx, []float32{1})
	if len(got) != 0 {
		t.Fatal("a cleared tap still fires")
	}
}

func TestAChainIsAStage(t *testing.T) {
	inner, _ := NewChain(24000, &scale{name: "x", k: 2, to: 16000})
	var s Stage = inner
	if s.Name() != "chain" || s.OutputRate() != 16000 {
		t.Fatalf("%s %d", s.Name(), s.OutputRate())
	}
	outer, err := NewChain(24000, inner, &scale{name: "y", k: 1, in: 16000})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := outer.Process(context.Background(), []float32{0.25})
	if !slices.Equal(out, []float32{0.5}) {
		t.Fatalf("out %v", out)
	}
}

func TestChainDoesNotAllocatePerBatch(t *testing.T) {
	c, _ := NewChain(24000, &scale{name: "a", k: 2}, &scale{name: "b", k: 0.5})
	c.SetTap(func(string, []float32) {})
	in := make([]float32, 960)
	ctx := context.Background()
	_, _ = c.Process(ctx, in)
	if n := testing.AllocsPerRun(20, func() { _, _ = c.Process(ctx, in) }); n != 0 {
		t.Fatalf("%v allocations per batch", n)
	}
}
