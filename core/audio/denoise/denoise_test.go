package denoise

// Ports of crates/audio-macos/src/denoise.rs tests (the 17 that need no model). The nine model
// tests are in libdf_test.go (build tag deepfilter). Extra Go-only tests are at the bottom.

import (
	"errors"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	frame = 960
	rate  = 24000.0
)

// identity copies the hop through: shows what the resampling alone does.
type identity struct{ hop int }

func (e identity) Hop() int { return e.hop }
func (identity) ProcessHop(noisy, clean []float32) error {
	copy(clean, noisy)
	return nil
}
func (identity) Reset() error { return nil }
func (identity) Close() error { return nil }

type broken struct{}

func (broken) Hop() int                        { return Hop48k }
func (broken) ProcessHop(_, _ []float32) error { return errors.New("hop failed") }
func (broken) Reset() error                    { return nil }
func (broken) Close() error                    { return nil }

type panicky struct{}

func (panicky) Hop() int                        { return Hop48k }
func (panicky) ProcessHop(_, _ []float32) error { panic("inside the model") }
func (panicky) Reset() error                    { return nil }
func (panicky) Close() error                    { return nil }

// negate negates the hop: shows that the model ran.
type negate struct{}

func (negate) Hop() int { return Hop48k }
func (negate) ProcessHop(noisy, clean []float32) error {
	for i, n := range noisy {
		clean[i] = -n
	}
	return nil
}
func (negate) Reset() error { return nil }
func (negate) Close() error { return nil }

func loadIdentity() (Engine, error)   { return identity{Hop48k}, nil }
func loadIdentity16() (Engine, error) { return identity{Hop16k}, nil }

func rms(x []int16) float64 {
	var s float64
	for _, v := range x {
		s += float64(v) * float64(v)
	}
	return math.Sqrt(s / math.Max(1, float64(len(x))))
}

func db(ratio float64) float64 { return 20 * math.Log10(math.Max(ratio, 1e-9)) }

func sine(freq, amp float64, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*freq*float64(i)/rate))
	}
	return out
}

// filtered runs input through c in 40 ms frames.
func filtered(t *testing.T, c *Cleaner, input []int16) []int16 {
	t.Helper()
	out := make([]int16, 0, len(input))
	for i := 0; i < len(input); i += frame {
		chunk := input[i:min(i+frame, len(input))]
		f := append([]int16(nil), chunk...)
		got := c.Process(f)
		if len(got) != len(chunk) {
			t.Fatalf("a frame keeps its length: %d != %d", len(got), len(chunk))
		}
		out = append(out, got...)
	}
	return out
}

// settled starts loading and waits until the model is ready or has failed.
func settled(t *testing.T, c *Cleaner) *Cleaner {
	t.Helper()
	c.Prepare()
	start := time.Now()
	for !c.Ready() && c.Failed() == "" {
		if time.Since(start) > 60*time.Second {
			t.Fatal("the model never loaded")
		}
		time.Sleep(2 * time.Millisecond)
	}
	return c
}

func newC(hop int, load Loader) *Cleaner {
	return NewWithLoader(hop, load, Options{Enabled: true, Logf: func(string, ...any) {}})
}

func equal(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestTheHalfBandFilterIsSymmetricWithUnityDCGain(t *testing.T) {
	h := halfBand()
	if h[center] != 0.5 {
		t.Fatalf("centre %v", h[center])
	}
	var sum float32
	for _, v := range h {
		sum += v
	}
	if math.Abs(float64(sum)-1) >= 1e-5 {
		t.Fatalf("DC gain %v", sum)
	}
	for i := 0; i < taps; i++ {
		if math.Abs(float64(h[i]-h[taps-1-i])) >= 1e-7 {
			t.Fatalf("tap %d not symmetric", i)
		}
		if i != center && (i-center)%2 == 0 && h[i] != 0 {
			t.Fatalf("even tap %d = %v", i, h[i])
		}
	}
}

func TestResamplingKeepsSpeechBandTonesAtLevel(t *testing.T) {
	for _, freq := range []float64{200, 1000, 4000, 8000} {
		f := settled(t, newC(Hop48k, loadIdentity))
		input := sine(freq, 10000, frame*10)
		out := filtered(t, f, input)
		change := db(rms(out[frame:]) / rms(input[frame:]))
		if math.Abs(change) >= 0.5 {
			t.Errorf("%v Hz changed by %.2f dB", freq, change)
		}
	}
}

// goertzel returns the power of freq (Hz) in x sampled at sr.
func goertzel(x []float32, freq, sr float64) float64 {
	w := 2 * math.Pi * freq / sr
	var s1, s2 float64
	for _, v := range x {
		s0 := float64(v) + 2*math.Cos(w)*s1 - s2
		s2, s1 = s1, s0
	}
	return s1*s1 + s2*s2 - 2*math.Cos(w)*s1*s2
}

func TestUpsamplingRejectsTheImage(t *testing.T) {
	// A 6 kHz tone zero-stuffed to 48 kHz has an image at 18 kHz; the half-band removes it.
	up := newUpsampler()
	tone := sine(6000, 10000, 4800)
	hi := make([]float32, 0, 2*len(tone))
	for _, s := range tone {
		a, b := up.push(float32(s))
		hi = append(hi, a, b)
	}
	settledPart := hi[200:]
	rejection := 10 * math.Log10(goertzel(settledPart, 18000, 48000)/goertzel(settledPart, 6000, 48000))
	if rejection >= -50 {
		t.Fatalf("image only %.1f dB down", rejection)
	}
}

func TestAModelThatCannotLoadPassesAudioThrough(t *testing.T) {
	f := settled(t, newC(Hop48k, func() (Engine, error) { return nil, errors.New("no model here") }))
	if !strings.Contains(f.Failed(), "no model here") {
		t.Fatalf("failed = %q", f.Failed())
	}
	input := sine(440, 5000, frame*3)
	if !equal(filtered(t, f, input), input) {
		t.Fatal("audio changed")
	}
	f.Reset()
	if !equal(filtered(t, f, input), input) {
		t.Fatal("audio changed after reset")
	}
}

func TestAFailingOrPanickingModelPassesAudioThrough(t *testing.T) {
	input := sine(440, 5000, frame*3)
	b := settled(t, newC(Hop48k, func() (Engine, error) { return broken{}, nil }))
	if !equal(filtered(t, b, input), input) {
		t.Fatal("broken model changed audio")
	}
	if b.Failed() != "hop failed" {
		t.Fatalf("failed = %q", b.Failed())
	}
	p := settled(t, newC(Hop48k, func() (Engine, error) { return panicky{}, nil }))
	if !equal(filtered(t, p, input), input) {
		t.Fatal("panicky model changed audio")
	}
	if p.Failed() == "" {
		t.Fatal("panicky model has not failed")
	}
	pl := settled(t, newC(Hop48k, func() (Engine, error) { panic("loading") }))
	if !equal(filtered(t, pl, input), input) {
		t.Fatal("panic on load changed audio")
	}
	if !strings.Contains(pl.Failed(), "panicked") {
		t.Fatalf("failed = %q", pl.Failed())
	}
}

func TestAWorkingModelHasNotFailed(t *testing.T) {
	f := settled(t, newC(Hop48k, loadIdentity))
	filtered(t, f, sine(440, 5000, frame*3))
	if f.Failed() != "" {
		t.Fatalf("failed = %q", f.Failed())
	}
}

func TestFramesPassThroughWithoutDelayWhileASlowModelLoads(t *testing.T) {
	slow := func() (Engine, error) {
		time.Sleep(400 * time.Millisecond)
		return negate{}, nil
	}
	f := newC(Hop48k, slow)
	input := sine(440, 5000, frame)
	start := time.Now()
	passedThrough := 0
	for {
		fr := append([]int16(nil), input...)
		t0 := time.Now()
		out := f.Process(fr)
		if time.Since(t0) >= 50*time.Millisecond {
			t.Fatal("a frame waited for the model")
		}
		if !equal(out, input) {
			break // the model is in use now
		}
		passedThrough++
		if time.Since(start) >= 10*time.Second {
			t.Fatal("the model never took over")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if passedThrough == 0 {
		t.Fatal("the first frames pass through while loading")
	}
	if time.Since(start) < 400*time.Millisecond {
		t.Fatal("the model took over before it loaded")
	}
}

func TestFramesThatAreNotWholeHopsPassThrough(t *testing.T) {
	f := settled(t, newC(Hop48k, loadIdentity))
	input := sine(440, 5000, HopIn+7)
	if !equal(filtered(t, f, input), input) {
		t.Fatal("a partial-hop frame changed")
	}
}

func TestHopSizedAndFrameSizedChunksGiveTheSameOutput(t *testing.T) {
	input := sine(700, 8000, frame*3)
	whole := settled(t, newC(Hop48k, loadIdentity))
	hops := settled(t, newC(Hop48k, loadIdentity))
	var byHop []int16
	for i := 0; i < len(input); i += HopIn {
		fr := append([]int16(nil), input[i:i+HopIn]...)
		byHop = append(byHop, hops.Process(fr)...)
	}
	if !equal(filtered(t, whole, input), byHop) {
		t.Fatal("hop-sized and frame-sized chunks differ")
	}
}

func peakIndex(x []int16) int {
	best, bi := -1, 0
	for i, v := range x {
		a := int(v)
		if a < 0 {
			a = -a
		}
		if a > best {
			best, bi = a, i
		}
	}
	return bi
}

func TestTheResamplersDelayAudioBy23Samples(t *testing.T) {
	// Up: 12 input samples; down: 23 samples at 48 kHz = 11.5; plus the half-sample phase of
	// the odd upsampled outputs: 23 samples (~1 ms) at 24 kHz in all.
	f := settled(t, newC(Hop48k, loadIdentity))
	input := make([]int16, frame)
	input[100] = 20000
	out := filtered(t, f, input)
	peak := peakIndex(out)
	if peak != 123 {
		t.Fatalf("peak at %d", peak)
	}
	if out[peak] <= 19000 {
		t.Fatalf("the impulse keeps its height (%d)", out[peak])
	}
}

// Only the wrapper is checked: the resampling and buffers (libdf allocates inside the model).
func TestTheWrapperDoesNotAllocatePerFrameAfterWarmUp(t *testing.T) {
	f := settled(t, newC(Hop48k, loadIdentity))
	fr := sine(440, 5000, frame)
	f.Process(fr)
	if n := testing.AllocsPerRun(20, func() { f.Process(fr) }); n != 0 {
		t.Fatalf("%v allocations per frame", n)
	}
	if n := testing.AllocsPerRun(5, func() { f.Reset(); f.Process(fr) }); n != 0 {
		t.Fatalf("%v allocations per reset + frame", n)
	}
}

func TestThe16kRoundTripKeepsTonesUnder7kHzAtLevel(t *testing.T) {
	for _, freq := range []float64{200, 1000, 4000, 7000} {
		f := settled(t, newC(Hop16k, loadIdentity16))
		input := sine(freq, 10000, frame*10)
		out := filtered(t, f, input)
		change := db(rms(out[frame:]) / rms(input[frame:]))
		if math.Abs(change) >= 0.5 {
			t.Errorf("%v Hz changed by %.2f dB", freq, change)
		}
	}
}

func TestGoingDownTo16kRemovesWhatWouldFoldBack(t *testing.T) {
	// A 10 kHz tone is above 16 kHz's 8 kHz Nyquist: unfiltered it would fold to 6 kHz.
	src := sine(10000, 10000, HopIn*40)
	tone := make([]float32, len(src))
	for i, s := range src {
		tone[i] = float32(s)
	}
	down := newRational(2, 3)
	low := make([]float32, Hop16k*40)
	for k := 0; k < 40; k++ {
		down.run(tone[k*HopIn:(k+1)*HopIn], low[k*Hop16k:(k+1)*Hop16k])
	}
	var pl, pt float64
	for _, v := range low[Hop16k*4:] {
		pl += float64(v) * float64(v)
	}
	for _, v := range tone[HopIn*4:] {
		pt += float64(v) * float64(v)
	}
	leak := 10 * math.Log10(math.Max(pl/pt*1.5, 1e-12))
	if leak >= -50 {
		t.Fatalf("the 10 kHz tone leaked through at %.1f dB", leak)
	}
}

func TestThe16kRoundTripDelaysAudioByAbout5ms(t *testing.T) {
	// Each low-pass delays by 119.5 samples at 48 kHz: 239 in all, about 120 at 24 kHz.
	f := settled(t, newC(Hop16k, loadIdentity16))
	input := make([]int16, frame)
	input[100] = 20000
	peak := peakIndex(filtered(t, f, input))
	if peak < 219 || peak > 221 {
		t.Fatalf("the impulse came out at %d", peak)
	}
}

func TestThe16kWrapperDoesNotAllocatePerFrameOrOnReset(t *testing.T) {
	f := settled(t, newC(Hop16k, loadIdentity16))
	fr := sine(440, 5000, frame)
	f.Process(fr)
	if n := testing.AllocsPerRun(20, func() { f.Process(fr) }); n != 0 {
		t.Fatalf("%v allocations per frame", n)
	}
	if n := testing.AllocsPerRun(5, func() { f.Reset(); f.Process(fr) }); n != 0 {
		t.Fatalf("%v allocations per reset + frame", n)
	}
}

func TestAReset16kFilterGivesTheSameOutputAsAFreshOne(t *testing.T) {
	input := sine(700, 8000, frame*3)
	fresh := settled(t, newC(Hop16k, loadIdentity16))
	first := filtered(t, fresh, input)
	used := settled(t, newC(Hop16k, loadIdentity16))
	filtered(t, used, sine(3000, 9000, frame*2))
	used.Reset()
	if !equal(filtered(t, used, input), first) {
		t.Fatal("a reset filter differs from a fresh one")
	}
}

func TestNoiseModelsParseFromSettingsNames(t *testing.T) {
	if m, ok := ParseModel("hush"); !ok || m != Hush {
		t.Fatal("hush")
	}
	if m, ok := ParseModel("dfn3"); !ok || m != DFN3 {
		t.Fatal("dfn3")
	}
	if _, ok := ParseModel("rnnoise"); ok {
		t.Fatal("rnnoise parsed")
	}
	var def Model
	if def != Hush {
		t.Fatal("the default model is not Hush")
	}
}

// --- Go-only tests: the live switch, Close, logging once, dither, the default build. ---

func TestSetEnabledSwitchesLivePerFrame(t *testing.T) {
	f := settled(t, newC(Hop48k, func() (Engine, error) { return negate{}, nil }))
	input := sine(440, 5000, frame)
	if equal(f.Process(append([]int16(nil), input...)), input) {
		t.Fatal("enabled: the model did not run")
	}
	f.SetEnabled(false)
	if f.Enabled() {
		t.Fatal("still enabled")
	}
	if !equal(f.Process(append([]int16(nil), input...)), input) {
		t.Fatal("disabled: audio changed")
	}
	f.SetEnabled(true)
	if equal(f.Process(append([]int16(nil), input...)), input) {
		t.Fatal("re-enabled: the model did not run")
	}
}

func TestADisabledFilterDoesNotLoadItsModel(t *testing.T) {
	var mu sync.Mutex
	loads := 0
	f := NewWithLoader(Hop48k, func() (Engine, error) {
		mu.Lock()
		loads++
		mu.Unlock()
		return identity{Hop48k}, nil
	}, Options{Logf: func(string, ...any) {}})
	input := sine(440, 5000, frame)
	for range 3 {
		if !equal(f.Process(append([]int16(nil), input...)), input) {
			t.Fatal("disabled: audio changed")
		}
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if loads != 0 {
		t.Fatalf("loaded %d times while disabled", loads)
	}
}

func TestReEnablingStartsTheResamplersClean(t *testing.T) {
	input := sine(700, 8000, frame*3)
	fresh := settled(t, newC(Hop16k, loadIdentity16))
	first := filtered(t, fresh, input)
	used := settled(t, newC(Hop16k, loadIdentity16))
	filtered(t, used, sine(3000, 9000, frame*2))
	used.SetEnabled(false)
	used.SetEnabled(true)
	if !equal(filtered(t, used, input), first) {
		t.Fatal("stale resampler history leaked across the switch")
	}
}

type closeCounter struct {
	identity
	mu     *sync.Mutex
	closed *int
}

func (c closeCounter) Close() error {
	c.mu.Lock()
	*c.closed++
	c.mu.Unlock()
	return nil
}

func TestCloseClosesTheEngineAndPassesThroughAfter(t *testing.T) {
	var mu sync.Mutex
	closed := 0
	f := settled(t, newC(Hop48k, func() (Engine, error) {
		return closeCounter{identity{Hop48k}, &mu, &closed}, nil
	}))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close() // twice is fine
	mu.Lock()
	if closed != 1 {
		t.Fatalf("engine closed %d times", closed)
	}
	mu.Unlock()
	input := sine(440, 5000, frame)
	if !equal(f.Process(append([]int16(nil), input...)), input) {
		t.Fatal("a closed filter changed audio")
	}
}

func TestCloseWhileLoadingClosesTheEngineWhenItArrives(t *testing.T) {
	var mu sync.Mutex
	closed := 0
	release := make(chan struct{})
	f := newC(Hop48k, func() (Engine, error) {
		<-release
		return closeCounter{identity{Hop48k}, &mu, &closed}, nil
	})
	f.Prepare()
	_ = f.Close()
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		c := closed
		mu.Unlock()
		if c == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the late engine was never closed")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestAFailureIsLoggedOnce(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	f := NewWithLoader(Hop48k, func() (Engine, error) { return broken{}, nil }, Options{
		Enabled: true,
		Logf: func(format string, args ...any) {
			mu.Lock()
			logs = append(logs, format)
			mu.Unlock()
		},
	})
	settled(t, f)
	filtered(t, f, sine(440, 5000, frame*5))
	f.Reset()
	filtered(t, f, sine(440, 5000, frame*5))
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 {
		t.Fatalf("logged %d times: %v", len(logs), logs)
	}
}

func TestAnEngineWithTheWrongHopFails(t *testing.T) {
	f := settled(t, newC(Hop16k, loadIdentity))
	if !strings.Contains(f.Failed(), "hop") {
		t.Fatalf("failed = %q", f.Failed())
	}
	input := sine(440, 5000, frame)
	if !equal(filtered(t, f, input), input) {
		t.Fatal("audio changed")
	}
}

func TestDitherAddsFaintNoiseOnlyToNearSilentHops(t *testing.T) {
	d := newDither()
	quiet := make([]float32, Hop16k)
	d.apply(quiet)
	var ms float64
	for _, v := range quiet {
		ms += float64(v) * float64(v)
	}
	r := math.Sqrt(ms / float64(len(quiet)))
	if dbfs := 20 * math.Log10(r); math.Abs(dbfs-(-68)) > 1.5 {
		t.Fatalf("dither at %.1f dBFS, want about -68", dbfs)
	}
	loud := make([]float32, Hop16k)
	for i := range loud {
		loud[i] = 0.01
	}
	d.apply(loud)
	for _, v := range loud {
		if v != 0.01 {
			t.Fatal("a loud hop was dithered")
		}
	}
	// Deterministic: a fresh dither replays exactly.
	a, b := make([]float32, Hop48k), make([]float32, Hop48k)
	d1, d2 := newDither(), newDither()
	d1.apply(a)
	d2.apply(b)
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("dither is not deterministic")
		}
	}
}

func TestNewWithoutLibdfPassesThroughAndSaysWhy(t *testing.T) {
	if LibdfAvailable {
		t.Skip("built with libdf")
	}
	var mu sync.Mutex
	var logs []string
	f := New(Config{Model: Hush, Enabled: true, Logf: func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, format)
		mu.Unlock()
	}})
	defer f.Close()
	settled(t, f)
	if !strings.Contains(f.Failed(), "deepfilter") {
		t.Fatalf("failed = %q", f.Failed())
	}
	input := sine(440, 5000, frame*2)
	if !equal(filtered(t, f, input), input) {
		t.Fatal("audio changed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 {
		t.Fatalf("logged %d times", len(logs))
	}
}
