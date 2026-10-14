package gemini

// Port of ai-mascot crates/voice-gemini/src/resample.rs tests (6/6).

import (
	"math"
	"reflect"
	"testing"
)

func sine(rate int, hz float64, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(math.Sin(2*math.Pi*hz*float64(i)/float64(rate)) * 10_000)
	}
	return out
}

func TestSameRateIsACopy(t *testing.T) {
	r := NewResampler(24_000, 24_000)
	if got := r.Process([]int16{1, -2, 3}); !reflect.DeepEqual(got, []int16{1, -2, 3}) {
		t.Fatal(got)
	}
}

func TestDown24kTo16kKeepsTwoOfEveryThree(t *testing.T) {
	r := NewResampler(24_000, 16_000)
	// Positions 0, 1.5, 3, 4.5 → 0, midpoint of 10/20, 30, midpoint of 40/50.
	if got := r.Process([]int16{0, 10, 20, 30, 40, 50}); !reflect.DeepEqual(got, []int16{0, 15, 30, 45}) {
		t.Fatal(got)
	}
	// 100 ms frames: 2400 samples in, 1600 out over a stream.
	r = NewResampler(24_000, 16_000)
	total := 0
	for range 10 {
		total += len(r.Process(make([]int16, 2400)))
	}
	if total < 15_999 || total > 16_000 {
		t.Fatal(total)
	}
}

func TestUp16kTo24kInterpolates(t *testing.T) {
	r := NewResampler(16_000, 24_000)
	// Step 2/3: positions 0, 0.67, 1.33, 2, 2.67 → 0, 20, 40, 60, 80 (on a 0,30,60,90 ramp).
	if got := r.Process([]int16{0, 30, 60, 90}); !reflect.DeepEqual(got, []int16{0, 20, 40, 60, 80}) {
		t.Fatal(got)
	}
}

func TestChunkedStreamEqualsOneShot(t *testing.T) {
	input := sine(24_000, 440, 4_801)
	whole := NewResampler(24_000, 16_000).Process(input)
	r := NewResampler(24_000, 16_000)
	var pieces []int16
	for i := 0; i < len(input); i += 333 {
		pieces = append(pieces, r.Process(input[i:min(i+333, len(input))])...)
	}
	if !reflect.DeepEqual(pieces, whole) {
		t.Fatal("chunked output differs")
	}
}

func TestAToneSurvivesResampling(t *testing.T) {
	// 440 Hz at 24 kHz → 16 kHz: still 440 Hz, about the same loudness.
	out := NewResampler(24_000, 16_000).Process(sine(24_000, 440, 24_000))
	expected := sine(16_000, 440, len(out))
	maxErr := 0
	for i := range out {
		d := int(out[i]) - int(expected[i])
		if d < 0 {
			d = -d
		}
		maxErr = max(maxErr, d)
	}
	if maxErr >= 200 {
		t.Fatalf("max error %d", maxErr)
	}
}

func TestExtremesDoNotOverflowAndResetRestarts(t *testing.T) {
	r := NewResampler(16_000, 24_000)
	out := r.Process([]int16{math.MaxInt16, math.MinInt16, math.MaxInt16})
	if out[0] != math.MaxInt16 {
		t.Fatal(out)
	}
	low := false
	for _, s := range out {
		low = low || s < -10_000
	}
	if !low {
		t.Fatal(out)
	}
	r.Reset()
	if got := r.Process([]int16{7, 7}); got[0] != 7 {
		t.Fatal(got)
	}
	if got := r.Process(nil); len(got) != 0 {
		t.Fatal(got)
	}
	if r.FromRate() != 16_000 || r.ToRate() != 24_000 {
		t.Fatal(r.FromRate(), r.ToRate())
	}
}
