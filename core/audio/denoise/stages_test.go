package denoise

// Go-only tests of the stage split (P16): the chain's shape and rates, the per-stage tap, and
// the stages on their own.

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/audio"
)

func TestTheFilterIsAChainOfStages(t *testing.T) {
	hush := NewWithLoader(Hop16k, loadIdentity16, Options{Dither: true})
	want := []audio.StageInfo{{Name: "to16k", Rate: 16000}, {Name: "dither", Rate: 16000}, {Name: "denoise", Rate: 16000}, {Name: "to24k", Rate: 24000}}
	if got := hush.Stages(); !slices.Equal(got, want) {
		t.Fatalf("hush stages %v", got)
	}
	dfn3 := NewWithLoader(Hop48k, loadIdentity, Options{})
	want = []audio.StageInfo{{Name: "to48k", Rate: 48000}, {Name: "denoise", Rate: 48000}, {Name: "to24k", Rate: 24000}}
	if got := dfn3.Stages(); !slices.Equal(got, want) {
		t.Fatalf("dfn3 stages %v", got)
	}
	if got := New(Config{Model: DFN3}).Stages(); got[1].Name != "dither" {
		t.Fatalf("New leaves out the dither stage: %v", got)
	}
}

func TestTheTapSeesEveryStageWhileTheModelCleans(t *testing.T) {
	f := settled(t, newC(Hop16k, loadIdentity16))
	lens := map[string][]int{}
	var order []string
	f.SetTap(func(stage string, out []float32) {
		order = append(order, stage)
		lens[stage] = append(lens[stage], len(out))
	})
	filtered(t, f, sine(440, 5000, frame*2))
	if want := []string{"to16k", "denoise", "to24k", "to16k", "denoise", "to24k"}; !slices.Equal(order, want) {
		t.Fatalf("tap order %v", order)
	}
	if !slices.Equal(lens["to16k"], []int{640, 640}) || !slices.Equal(lens["to24k"], []int{960, 960}) {
		t.Fatalf("tap lengths %v", lens)
	}
	f.SetEnabled(false)
	order = nil
	filtered(t, f, sine(440, 5000, frame))
	if len(order) != 0 {
		t.Fatal("the tap fired while the filter was off")
	}
}

func TestTheModelStagePassesThroughUntilLoadedAndWhenOff(t *testing.T) {
	release := make(chan struct{})
	m := NewModelStage(Hop48k, func() (Engine, error) { <-release; return negate{}, nil },
		Options{Enabled: true, Logf: func(string, ...any) {}})
	defer m.Close()
	ctx := context.Background()
	in := make([]float32, Hop48k)
	in[0] = 0.5
	if out, err := m.Process(ctx, in); err != nil || &out[0] != &in[0] {
		t.Fatal("loading: not passed through", err)
	}
	close(release)
	for !m.Ready() {
	}
	if out, _ := m.Process(ctx, in); out[0] != -0.5 {
		t.Fatalf("ready: the model did not run (%v)", out[0])
	}
	m.SetEnabled(false)
	if out, _ := m.Process(ctx, in); &out[0] != &in[0] {
		t.Fatal("off: not passed through")
	}
	m.SetEnabled(true)
	if _, err := m.Process(ctx, in[:Hop48k-1]); err == nil {
		t.Fatal("a partial hop was accepted")
	}
}

func TestAFailingModelStageReturnsTheEnginesErrorAndPassesThroughAfter(t *testing.T) {
	m := NewModelStage(Hop48k, func() (Engine, error) { return broken{}, nil },
		Options{Enabled: true, Logf: func(string, ...any) {}})
	for !m.Ready() {
		m.Prepare()
	}
	ctx := context.Background()
	in := make([]float32, Hop48k)
	if _, err := m.Process(ctx, in); err == nil || err.Error() != "hop failed" {
		t.Fatalf("err = %v", err)
	}
	if out, err := m.Process(ctx, in); err != nil || &out[0] != &in[0] {
		t.Fatal("failed: not passed through", err)
	}
}

func TestTheResampleStagesTakeWholeBlocks(t *testing.T) {
	ctx := context.Background()
	down := newResampleStage(24000, 16000)
	if _, err := down.Process(ctx, make([]float32, 241)); err == nil || !strings.Contains(err.Error(), "3-sample") {
		t.Fatalf("err = %v", err)
	}
	if out, err := down.Process(ctx, make([]float32, HopIn)); err != nil || len(out) != Hop16k {
		t.Fatalf("%d samples, %v", len(out), err)
	}
	up := newResampleStage(24000, 48000)
	if out, _ := up.Process(ctx, make([]float32, HopIn)); len(out) != Hop48k {
		t.Fatalf("up gave %d", len(out))
	}
	if _, err := newResampleStage(48000, 24000).Process(ctx, make([]float32, 3)); err == nil {
		t.Fatal("an odd 48 kHz batch was accepted")
	}
}

func TestTheDitherStageDithersEachHop(t *testing.T) {
	s := newDitherStage(Hop16k, 16000)
	in := make([]float32, 2*Hop16k)
	for i := Hop16k; i < len(in); i++ {
		in[i] = 0.01 // the second hop is loud
	}
	out, err := s.Process(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if in[0] != 0 {
		t.Fatal("the input was changed in place")
	}
	if out[0] == 0 || out[Hop16k] != 0.01 {
		t.Fatalf("quiet hop %v, loud hop %v", out[0], out[Hop16k])
	}
	first := slices.Clone(out[:Hop16k])
	s.reset()
	again, _ := s.Process(context.Background(), make([]float32, Hop16k))
	if !slices.Equal(again, first) {
		t.Fatal("a reset dither does not replay")
	}
}
