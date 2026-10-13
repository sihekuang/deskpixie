package denoise

import "math"

// kaiserBeta shapes the Kaiser window: about 60 dB of stop-band rejection.
const kaiserBeta = 5.65

func besselI0(x float64) float64 {
	sum, term, k := 1.0, 1.0, 1.0
	for term > 1e-12*sum {
		term *= math.Pow(x/(2*k), 2)
		sum += term
		k++
	}
	return sum
}

const (
	// side is the number of non-zero odd taps on each side of the half-band filter's centre.
	side = 12
	// taps is the half-band FIR length: 47 taps (centre 0.5, every other tap zero).
	taps   = 4*side - 1
	center = taps / 2
)

// halfBand is a Kaiser-windowed half-band low-pass (cutoff at a quarter of the 48 kHz rate,
// i.e. 12 kHz), normalised to unity DC gain. About 60 dB of stop-band rejection; flat to about
// 10 kHz.
func halfBand() [taps]float32 {
	var h [taps]float64
	h[center] = 0.5
	for i := range h {
		j := float64(i - center)
		if (i-center)%2 != 0 {
			r := j / center
			w := besselI0(kaiserBeta*math.Sqrt(math.Max(1-r*r, 0))) / besselI0(kaiserBeta)
			h[i] = math.Sin(math.Pi/2*j) / (math.Pi * j) * w
		}
	}
	var odd float64
	for i, v := range h {
		if i != center {
			odd += v
		}
	}
	var out [taps]float32
	for i, v := range h {
		if i == center {
			out[i] = 0.5
		} else {
			out[i] = float32(v * 0.5 / odd)
		}
	}
	return out
}

var halfBandTaps = halfBand()

// upsampler turns 24 kHz into 48 kHz: each input sample yields two outputs (polyphase
// half-band).
type upsampler struct {
	// taps are the odd-phase taps (doubled for the 2x gain of zero-stuffing).
	taps [2 * side]float32
	// hist holds the last 2*side inputs, oldest first.
	hist [2 * side]float32
}

func newUpsampler() upsampler {
	var u upsampler
	for i := range u.taps {
		u.taps[i] = 2 * halfBandTaps[2*i] // the taps at odd offsets from the centre
	}
	return u
}

func (u *upsampler) clear() { u.hist = [2 * side]float32{} }

func (u *upsampler) push(x float32) (float32, float32) {
	copy(u.hist[:], u.hist[1:])
	u.hist[2*side-1] = x
	var odd float32
	for i, t := range u.taps {
		odd += t * u.hist[i]
	}
	return u.hist[side-1], odd
}

// downsampler turns 48 kHz into 24 kHz: each pair of inputs yields one output.
type downsampler struct {
	taps [taps]float32
	// hist holds the last taps inputs, oldest first.
	hist [taps]float32
}

func newDownsampler() downsampler { return downsampler{taps: halfBandTaps} }

func (d *downsampler) clear() { d.hist = [taps]float32{} }

func (d *downsampler) push(a, b float32) float32 {
	copy(d.hist[:], d.hist[2:])
	d.hist[taps-2] = a
	d.hist[taps-1] = b
	var s float32
	for i, t := range d.taps {
		s += t * d.hist[i]
	}
	return s
}

const (
	// rationalTaps is the length of the 24k <-> 16k low-pass, designed at 48 kHz (the common
	// multiple of both rates).
	rationalTaps = 240
	// rationalCutoffHz sits under 16 kHz's 8 kHz Nyquist by half its ~0.7 kHz transition, so
	// what would fold back sits in the stop band.
	rationalCutoffHz = 7600.0
)

// rationalLowPass is a Kaiser-windowed low-pass at 48 kHz with unity DC gain.
func rationalLowPass() []float64 {
	fc := rationalCutoffHz / 48000.0
	mid := float64(rationalTaps-1) / 2
	h := make([]float64, rationalTaps)
	var sum float64
	for i := range h {
		j := float64(i) - mid
		x := 2 * math.Pi * fc * j
		var sinc float64
		if j == 0 {
			sinc = 2 * fc
		} else {
			sinc = math.Sin(x) / (math.Pi * j)
		}
		r := j / mid
		h[i] = sinc * besselI0(kaiserBeta*math.Sqrt(math.Max(1-r*r, 0))) / besselI0(kaiserBeta)
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	return h
}

// rational is a streaming rational resampler: up by l, low-pass at 48 kHz, down by m
// (polyphase, so only the outputs that are kept get computed). 24k -> 16k is (2, 3);
// 16k -> 24k is (3, 2).
type rational struct {
	l, m int
	// phases[p][j] = l * h[p + j*l]: the taps for an output p steps past an input.
	phases [][]float32
	// hist holds the last "taps per phase" inputs, oldest first.
	hist []float32
	// next is where the next output falls, in 48 kHz steps past the newest input.
	next int
}

func newRational(l, m int) *rational {
	h := rationalLowPass()
	per := rationalTaps / l
	phases := make([][]float32, l)
	for p := range phases {
		phases[p] = make([]float32, per)
		for j := range phases[p] {
			phases[p][j] = float32(float64(l) * h[p+j*l])
		}
	}
	// Before any input the first output is one input away.
	return &rational{l: l, m: m, phases: phases, hist: make([]float32, per), next: l}
}

func (r *rational) clear() {
	clear(r.hist)
	r.next = r.l
}

// run resamples input into out, which must hold exactly len(input)*l/m samples.
func (r *rational) run(input, out []float32) {
	k := 0
	last := len(r.hist) - 1
	for _, x := range input {
		copy(r.hist, r.hist[1:])
		r.hist[last] = x
		r.next -= r.l
		for r.next < r.l {
			// Newest input first against the phase's taps.
			var s float32
			for j, t := range r.phases[r.next] {
				s += t * r.hist[last-j]
			}
			out[k] = s
			k++
			r.next += r.m
		}
	}
}

// rateConv converts 10 ms hops between the mic's 24 kHz and a model's rate.
type rateConv struct {
	hop int
	// 48 kHz: the half-band pair.
	up   upsampler
	down downsampler
	// 16 kHz: 2:3 down and 3:2 back up.
	toModel, fromModel *rational
}

func newRateConv(hop int) *rateConv {
	switch hop {
	case Hop48k:
		return &rateConv{hop: hop, up: newUpsampler(), down: newDownsampler()}
	case Hop16k:
		return &rateConv{hop: hop, toModel: newRational(2, 3), fromModel: newRational(3, 2)}
	}
	panic("denoise: no resampler for this hop size")
}

// clear forgets the filters' history, without allocating.
func (r *rateConv) clear() {
	if r.hop == Hop48k {
		r.up.clear()
		r.down.clear()
		return
	}
	r.toModel.clear()
	r.fromModel.clear()
}

// micToModel converts one mic hop (HopIn samples) to one model hop.
func (r *rateConv) micToModel(mic, out []float32) {
	if r.hop == Hop48k {
		for i, s := range mic {
			out[2*i], out[2*i+1] = r.up.push(s)
		}
		return
	}
	r.toModel.run(mic, out)
}

// modelToMic converts one model hop back to one mic hop.
func (r *rateConv) modelToMic(clean, out []float32) {
	if r.hop == Hop48k {
		for i := range out {
			out[i] = r.down.push(clean[2*i], clean[2*i+1])
		}
		return
	}
	r.fromModel.run(clean, out)
}
