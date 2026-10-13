// Package denoise is the optional mic noise filter: a DeepFilterNet model on each 24 kHz mic
// frame, as an audio.Cleaner (port of ai-mascot crates/audio-macos/src/denoise.rs).
//
// Each model works in 10 ms hops at its own rate: Hush (the default) at 16 kHz, DeepFilterNet3
// at 48 kHz. So each frame is resampled to the model's rate, cleaned hop by hop, and resampled
// back, through filters whose state carries across frames. These steps are audio.Stages (the
// interface shared with Howl) run by an audio.Chain: to16k|to48k -> dither -> denoise -> to24k. The model loads on a goroutine;
// until it is ready, audio passes through, so the mic is never held up. Whatever goes wrong
// (the model cannot load, a hop fails, a panic inside the engine), the mic keeps working: audio
// then passes through unchanged and the problem is logged once.
//
// The model itself runs in libdf (DeepFilterNet's C API) through cgo, only in builds with the
// `deepfilter` tag (see libdf/README.md). Without it, New gives a Cleaner that passes audio
// through and logs why.
package denoise

import (
	"fmt"
	"sync"

	"github.com/sihekuang/deskpixie/core/audio"
)

const (
	// HopIn is mic samples per hop (10 ms at 24 kHz).
	HopIn = 240
	// Hop48k is samples per hop at 48 kHz (DeepFilterNet3).
	Hop48k = 480
	// Hop16k is samples per hop at 16 kHz (Hush).
	Hop16k = 160
)

// Engine is a speech enhancer working in 10 ms hops of Hop() samples (Hop48k or Hop16k). It is
// only ever used from one goroutine at a time.
type Engine interface {
	Hop() int
	// ProcessHop cleans one hop; noisy and clean are both Hop() long.
	ProcessHop(noisy, clean []float32) error
	// Reset returns the engine to its freshly loaded state.
	Reset() error
	Close() error
}

// Loader loads an engine; it runs on a goroutine.
type Loader func() (Engine, error)

// Model picks which model cleans the mic (Settings ▸ Voice ▸ Noise model).
type Model int

const (
	// Hush is DeepFilterNet3 retrained at 16 kHz to also turn down background voices. The
	// default.
	Hush Model = iota
	// DFN3 is DeepFilterNet3: steady noise only, 48 kHz.
	DFN3
)

// ParseModel reads the settings value: "hush" or "dfn3".
func ParseModel(name string) (Model, bool) {
	switch name {
	case "hush":
		return Hush, true
	case "dfn3":
		return DFN3, true
	}
	return Hush, false
}

func (m Model) String() string {
	if m == DFN3 {
		return "dfn3"
	}
	return "hush"
}

// Options configure a Cleaner.
type Options struct {
	// Enabled is the initial state of the live switch.
	Enabled bool
	// Logf receives the one failure line; nil means log.Printf.
	Logf func(format string, args ...any)
	// Dither puts the dither stage (dither.go) before the model. libdf needs it, so New sets
	// it; fake engines in tests leave it off to see exactly what the resampling does.
	Dither bool
}

// Cleaner is the noise filter as an audio.Cleaner: a chain of stages (resample to the model's
// rate, dither, the model, resample back) behind an audio.ChainCleaner. Frames are cleaned in
// place. Process, Reset and Close are safe to call from different goroutines; SetEnabled may
// be called at any time and takes effect on the next frame.
type Cleaner struct {
	model   *ModelStage
	toModel *resampleStage
	toMic   *resampleStage
	dither  *ditherStage // nil without Options.Dither
	chain   *audio.Chain
	adapter *audio.ChainCleaner

	mu sync.Mutex
}

var _ audio.Cleaner = (*Cleaner)(nil)

// NewWithLoader makes a Cleaner for engines with hop-sample hops (Hop48k or Hop16k) loaded by
// load. The model loads on the first enabled frame (or on Prepare).
func NewWithLoader(hop int, load Loader, opts Options) *Cleaner {
	model := NewModelStage(hop, load, opts)
	c := &Cleaner{
		model:   model,
		toModel: newResampleStage(audio.SampleRate, model.InputRate()),
		toMic:   newResampleStage(model.InputRate(), audio.SampleRate),
	}
	stages := []audio.Stage{c.toModel}
	if opts.Dither {
		c.dither = newDitherStage(hop, model.InputRate())
		stages = append(stages, c.dither)
	}
	stages = append(stages, model, c.toMic)
	chain, err := audio.NewChain(audio.SampleRate, stages...)
	if err == nil {
		c.adapter, err = audio.NewChainCleaner(chain)
	}
	if err != nil {
		panic("denoise: " + err.Error()) // the stages above always line up
	}
	c.chain = chain
	return c
}

// SetEnabled turns the filter on or off; it is checked on every frame.
func (c *Cleaner) SetEnabled(on bool) { c.model.SetEnabled(on) }

// Enabled reports the live switch.
func (c *Cleaner) Enabled() bool { return c.model.Enabled() }

// Prepare starts loading the model in the background without blocking.
func (c *Cleaner) Prepare() { c.model.Prepare() }

// Ready reports whether the model is ready, picking it up if the loader just finished.
func (c *Cleaner) Ready() bool { return c.model.Ready() }

// Failed says why the filter gave up and passes audio through, or "" if it has not.
func (c *Cleaner) Failed() string { return c.model.Failed() }

// Stages lists the chain's stages and their output rates (names as passed to a tap).
func (c *Cleaner) Stages() []audio.StageInfo { return c.chain.Stages() }

// SetTap installs (nil removes) a hook that sees each stage's output while the model is
// cleaning, e.g. for VOICE_DEBUG recordings per stage. Rates differ per stage: see Stages.
func (c *Cleaner) SetTap(t audio.Tap) { c.chain.SetTap(t) }

// clearFilters forgets the history of the stages around the model, without allocating.
func (c *Cleaner) clearFilters() {
	c.toModel.reset()
	c.toMic.reset()
	if c.dither != nil {
		c.dither.reset()
	}
}

// Process cleans a 24 kHz mono frame in place and returns it; its length never changes. A
// frame that is not whole 10 ms hops, or arrives while the filter is off, loading, failed or
// closed, comes back unchanged (the chain does not run, so there is no resampling delay).
func (c *Cleaner) Process(in []int16) []int16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	on, startClean := c.model.activate()
	if startClean {
		// Back on after a pause, or a fresh model: no stale history smears into this frame.
		c.clearFilters()
	}
	// Only whole hops: the mic path always sends 40 ms (4-hop) frames.
	if !on || len(in) == 0 || len(in)%HopIn != 0 {
		return in
	}
	if why := c.run(in); why != "" {
		c.model.failWith(why)
	}
	return in
}

// run cleans in through the chain; on failure in is left as it was.
func (c *Cleaner) run(in []int16) (why string) {
	defer func() {
		if p := recover(); p != nil {
			why = fmt.Sprintf("the noise filter panicked: %v", p)
		}
	}()
	if _, err := c.adapter.Clean(in); err != nil {
		return err.Error()
	}
	return ""
}

// Reset forgets all state (a new conversation, or the stream was cut).
func (c *Cleaner) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clearFilters()
	c.model.Reset()
}

// Close releases the model; afterwards every frame passes through. Safe to call more than once.
func (c *Cleaner) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.chain.Close()
}
