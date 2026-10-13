package denoise

// HushAttenLimitDB is the most Hush turns a sound down, in dB. Below -10 dB local SNR the
// model's mask is all zeros (the frame would be muted); the limit mixes the input back so it is
// turned down instead, which keeps a quiet user audible to speech recognition (#97: 18 dB for
// now).
const HushAttenLimitDB = 18.0

// DFN3AttenLimitDB leaves DeepFilterNet3 uncapped (libDF's RuntimeParams default).
const DFN3AttenLimitDB = 100.0

// Config picks the model for New.
type Config struct {
	Model Model
	// Enabled is the initial state of the live switch (settings voice.noiseFilter).
	Enabled bool
	// DFN3ModelPath is the DeepFilterNet3 model archive (DeepFilterNet3.tar.gz, bundled in the
	// app's Resources). Hush needs no path: its model is built in.
	DFN3ModelPath string
	// Logf receives the one failure line; nil means log.Printf.
	Logf func(format string, args ...any)
}

// New makes the mic noise filter for cfg.Model: the chain to16k -> dither -> denoise -> to24k
// (Hush) or to48k -> dither -> denoise -> to24k (DFN3). The model loads on the first enabled frame;
// in a build without libdf (no `deepfilter` tag) it fails to load, logs why once, and passes
// audio through.
func New(cfg Config) *Cleaner {
	opts := Options{Enabled: cfg.Enabled, Logf: cfg.Logf, Dither: true}
	if cfg.Model == DFN3 {
		path := cfg.DFN3ModelPath
		return NewWithLoader(Hop48k, func() (Engine, error) { return loadDFN3(path) }, opts)
	}
	return NewWithLoader(Hop16k, loadHush, opts)
}
