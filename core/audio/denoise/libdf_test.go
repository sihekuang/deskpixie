//go:build deepfilter

package denoise

// Ports of the nine model tests in crates/audio-macos/src/denoise.rs (mod models). They need
// libdf (make -C core libdf) and run with: make -C core test-denoise.

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points the loader at the fetched libdf unless DESKPIXIE_LIBDF says otherwise.
func TestMain(m *testing.M) {
	if os.Getenv("DESKPIXIE_LIBDF") == "" {
		if abs, err := filepath.Abs(filepath.Join("libdf", "lib", "libdf.dylib")); err == nil {
			_ = os.Setenv("DESKPIXIE_LIBDF", abs)
		}
	}
	os.Exit(m.Run())
}

func dfn3Path(t *testing.T) string {
	t.Helper()
	p := os.Getenv("DESKPIXIE_DFN3_MODEL")
	if p == "" {
		p = filepath.Join("libdf", "models", "DeepFilterNet3.tar.gz")
	}
	if _, err := os.Stat(p); err != nil {
		t.Skipf("no DeepFilterNet3 model at %s (make -C core libdf)", p)
	}
	return p
}

func dfn3(t *testing.T) *Cleaner {
	return settled(t, New(Config{Model: DFN3, Enabled: true, DFN3ModelPath: dfn3Path(t), Logf: t.Logf}))
}

func hush(t *testing.T) *Cleaner {
	return settled(t, New(Config{Model: Hush, Enabled: true, Logf: t.Logf}))
}

// pink is pink noise (Paul Kellet's filter on white noise), scaled to an RMS of amp.
func pink(n int, amp float32) []int16 {
	seed := uint64(7)
	var b [3]float32
	raw := make([]float32, n)
	var ss float32
	for i := range raw {
		seed = seed*6364136223846793005 + 1
		w := float32(seed>>33)/float32(uint64(1)<<31) - 1
		b[0] = 0.99765*b[0] + w*0.0990460
		b[1] = 0.96300*b[1] + w*0.2965164
		b[2] = 0.57000*b[2] + w*1.0526913
		raw[i] = b[0] + b[1] + b[2] + w*0.1848
		ss += raw[i] * raw[i]
	}
	r := float32(math.Sqrt(float64(ss / float32(n))))
	out := make([]int16, n)
	for i, v := range raw {
		out[i] = int16(v / r * amp)
	}
	return out
}

// voiced is a voiced, syllable-like tone: 140 Hz harmonics with falling level, gliding pitch,
// switched on and off four times a second.
func voiced(n int, amp float64) []int16 {
	out := make([]int16, n)
	phase := 0.0
	for i := range out {
		t := float64(i) / rate
		f0 := 140 + 20*math.Sin(2*math.Pi*0.7*t)
		phase += 2 * math.Pi * f0 / rate
		syllable := math.Max(math.Sin(2*math.Pi*4*t), 0)
		var v float64
		for k := 1; k <= 20; k++ {
			v += math.Sin(float64(k)*phase) / float64(k)
		}
		out[i] = int16(amp * syllable * v / 2)
	}
	return out
}

// power is the Goertzel power of freq in x.
func power(x []int16, freq float64) float64 {
	f := make([]float32, len(x))
	for i, v := range x {
		f[i] = float32(v)
	}
	return goertzel(f, freq, rate)
}

// buzz is a voice-like buzz: f0 with 1/k harmonics, syllables 4 times a second from t = 0.
func buzz(f0 float64, n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		t := float64(i) / rate
		syllable := 0.5 + 0.5*math.Cos(2*math.Pi*4*t)
		var v float64
		for k := 1; k <= 20; k++ {
			v += math.Sin(2*math.Pi*f0*float64(k)*t) / float64(k)
		}
		out[i] = int16(amp * syllable * v / 2)
	}
	return out
}

func mix(a, b []int16) []int16 {
	out := make([]int16, len(a))
	for i := range a {
		s := int32(a[i]) + int32(b[i])
		out[i] = int16(max(min(s, 32767), -32768))
	}
	return out
}

func TestSteadyNoiseGetsClearlyQuieter(t *testing.T) {
	// Room-like pink noise at about -30 dBFS settles near -10 dB; at -40 dBFS it is gated out.
	input := pink(frame*200, 1000) // 8 s
	out := filtered(t, dfn3(t), input)
	tail := frame * 100 // the model's noise estimate takes a few seconds to settle
	if drop := db(rms(out[tail:]) / rms(input[tail:])); drop >= -6 {
		t.Errorf("-30 dBFS pink noise only dropped %.1f dB", drop)
	}
	quiet := pink(frame*50, 300)
	out = filtered(t, dfn3(t), quiet)
	if drop := db(rms(out[frame*10:]) / rms(quiet[frame*10:])); drop >= -20 {
		t.Errorf("-40 dBFS pink noise only dropped %.1f dB", drop)
	}
}

func TestAVoicedToneSurvivesWithinAFewDB(t *testing.T) {
	input := voiced(frame*75, 3000)
	out := filtered(t, dfn3(t), input)
	skip := frame * 25
	if change := db(rms(out[skip:]) / rms(input[skip:])); change <= -3 {
		t.Errorf("the tone dropped %.1f dB", change)
	}
}

func silenceReplayRatio(t *testing.T, c *Cleaner) float64 {
	// A 140 Hz voice, 1 s of digital silence, then a 220 Hz voice: 140 Hz must not come back.
	input := buzz(140, frame*25, 4000)
	input = append(input, make([]int16, frame*25)...)
	input = append(input, buzz(220, frame*25, 4000)...)
	out := filtered(t, c, input)
	before := power(out[frame*24:frame*25], 140)
	restart := frame * 50
	replay := power(out[restart:restart+frame], 140)
	return 10 * math.Log10(math.Max(replay/before, 1e-12))
}

func TestAudioAfterSilenceDoesNotReplayWhatCameBefore(t *testing.T) {
	// Replayed, it is back at full level (~0 dB); what remains is the 220 Hz voice's leakage
	// into this short window (~-16 dB).
	if ratio := silenceReplayRatio(t, dfn3(t)); ratio >= -10 {
		t.Errorf("140 Hz came back at %.1f dB after the silence", ratio)
	}
}

func TestAfterResetTheOutputIsDeterministic(t *testing.T) {
	noisy := mix(voiced(frame*10, 3000), pink(frame*10, 1000))
	first := filtered(t, dfn3(t), noisy)
	used := dfn3(t)
	filtered(t, used, pink(frame*7, 3000))
	used.Reset()
	if !equal(filtered(t, used, noisy), first) {
		t.Error("a reset DFN3 filter differs from a fresh one")
	}
}

func TestTheEmbeddedHushModelLoads(t *testing.T) {
	f := hush(t)
	defer f.Close()
	if f.Failed() != "" {
		t.Fatalf("failed: %s", f.Failed())
	}
}

func TestHushTurnsQuietNoiseDownByItsLimitInsteadOfMutingIt(t *testing.T) {
	// Room noise near -45 dBFS: without the limit Hush outputs silence; with it, the noise is
	// exactly HushAttenLimitDB quieter.
	input := pink(frame*50, 190)
	out := filtered(t, hush(t), input)
	tail := frame * 25
	drop := db(rms(out[tail:]) / rms(input[tail:]))
	if math.Abs(drop+HushAttenLimitDB) >= 1.5 {
		t.Errorf("quiet noise dropped %.1f dB, not about -%v dB", drop, HushAttenLimitDB)
	}
}

func TestHushNeverMutesLoudInput(t *testing.T) {
	// Hush is trained on speech, so steady synthetic sounds are only turned down a little
	// (about 2-8 dB); what matters here is that none of it goes silent.
	for _, input := range [][]int16{voiced(frame*75, 3000), pink(frame*75, 1000)} {
		out := filtered(t, hush(t), input)
		skip := frame * 25
		for k := 0; skip+(k+1)*frame <= len(input); k++ {
			i := input[skip+k*frame : skip+(k+1)*frame]
			o := out[skip+k*frame : skip+(k+1)*frame]
			if rms(i) > 300 {
				if change := db(rms(o) / rms(i)); change <= -HushAttenLimitDB-1.5 {
					t.Errorf("frame %d dropped %.1f dB", k, change)
				}
			}
		}
	}
}

func TestHushDoesNotReplayAudioFromBeforeASilence(t *testing.T) {
	if ratio := silenceReplayRatio(t, hush(t)); ratio >= -10 {
		t.Errorf("140 Hz came back at %.1f dB after the silence", ratio)
	}
}

func TestHushAfterResetIsDeterministic(t *testing.T) {
	noisy := mix(voiced(frame*10, 3000), pink(frame*10, 1000))
	first := filtered(t, hush(t), noisy)
	used := hush(t)
	filtered(t, used, pink(frame*7, 3000))
	used.Reset()
	if !equal(filtered(t, used, noisy), first) {
		t.Error("a reset Hush filter differs from a fresh one")
	}
}

// Go-only: a missing DFN3 model fails cleanly (libdf would abort the process on it).
func TestAMissingDFN3ModelPassesThrough(t *testing.T) {
	f := settled(t, New(Config{Model: DFN3, Enabled: true, DFN3ModelPath: "/nonexistent/DeepFilterNet3.tar.gz", Logf: t.Logf}))
	if f.Failed() == "" {
		t.Fatal("a missing model did not fail")
	}
	input := sine(440, 5000, frame*2)
	if !equal(filtered(t, f, input), input) {
		t.Fatal("audio changed")
	}
}
