package denoise

// dither keeps DeepFilterNet stepping through silence. DfTract::process (which libdf's
// df_process_frame calls) skips a hop whose mean square is below 1e-7 (about -70 dBFS) without
// advancing its buffers, so after silence it would replay the audio from before it. A hop that
// quiet gets faint noise (RMS 4e-4, about -68 dBFS); the model itself gates noise that faint to
// silence. Engines apply it to each model-rate hop before the model sees it.
type dither struct{ seed uint32 }

const (
	ditherSkipBelow = 1e-7
	// ditherAmplitude: uniform noise in +-A has RMS A / sqrt(3).
	ditherAmplitude = 4e-4 * 1.7320508
)

func newDither() dither { return dither{seed: 0x9E3779B9} }

func (d *dither) apply(hop []float32) {
	var ms float32
	for _, v := range hop {
		ms += v * v
	}
	ms /= float32(len(hop))
	if ms >= 2*ditherSkipBelow {
		return
	}
	for i := range hop {
		// xorshift32: deterministic, so a reset replays exactly.
		d.seed ^= d.seed << 13
		d.seed ^= d.seed >> 17
		d.seed ^= d.seed << 5
		hop[i] += (float32(d.seed)/float32(^uint32(0))*2 - 1) * ditherAmplitude
	}
}
