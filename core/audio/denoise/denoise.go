// Package denoise is the optional mic noise filter: a DeepFilterNet model on each 24 kHz mic
// frame, as an audio.Cleaner (port of ai-mascot crates/audio-macos/src/denoise.rs).
//
// Each model works in 10 ms hops at its own rate: Hush (the default) at 16 kHz, DeepFilterNet3
// at 48 kHz. So each frame is resampled to the model's rate, cleaned hop by hop, and resampled
// back, through filters whose state carries across frames. The model loads on a goroutine;
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
	"log"
	"sync"
	"sync/atomic"

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
}

type slotState int

const (
	unloaded slotState = iota
	loading
	ready
	failed // loading or running failed: pass audio through from now on
	closed
)

type loaded struct {
	engine Engine
	err    error
}

// Cleaner is an audio.Cleaner around any Engine, with the resampling to and from its rate.
// Process, Reset and Close are safe to call from different goroutines; SetEnabled may be
// called at any time and takes effect on the next frame.
type Cleaner struct {
	load    Loader
	hop     int
	logf    func(string, ...any)
	enabled atomic.Bool
	// restart is set when the switch goes back on: the next frame starts the filters clean.
	restart atomic.Bool

	mu     sync.Mutex
	state  slotState
	loadCh chan loaded
	engine Engine
	why    string
	rate   *rateConv
	mic    [HopIn]float32
	noisy  []float32
	clean  []float32
	// out is the cleaned 24 kHz frame, written back only once every hop succeeded.
	out []float32
}

var _ audio.Cleaner = (*Cleaner)(nil)

// NewWithLoader makes a Cleaner for engines with hop-sample hops (Hop48k or Hop16k) loaded by
// load. The model loads on the first enabled frame (or on Prepare).
func NewWithLoader(hop int, load Loader, opts Options) *Cleaner {
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}
	c := &Cleaner{
		load:  load,
		hop:   hop,
		logf:  logf,
		rate:  newRateConv(hop),
		noisy: make([]float32, hop),
		clean: make([]float32, hop),
	}
	c.enabled.Store(opts.Enabled)
	return c
}

// SetEnabled turns the filter on or off; it is checked on every frame.
func (c *Cleaner) SetEnabled(on bool) {
	if was := c.enabled.Swap(on); on && !was {
		c.restart.Store(true)
	}
}

// Enabled reports the live switch.
func (c *Cleaner) Enabled() bool { return c.enabled.Load() }

func (c *Cleaner) fail(why string) {
	c.logf("deskpixie: noise filter off, passing the mic through: %s", why)
	c.why = why
	c.state = failed
	if c.engine != nil {
		_ = c.engine.Close()
		c.engine = nil
	}
}

// Prepare starts loading the model in the background without blocking.
func (c *Cleaner) Prepare() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prepare()
}

func (c *Cleaner) prepare() {
	if c.state != unloaded {
		return
	}
	ch := make(chan loaded, 1)
	load := c.load
	go func() {
		var r loaded
		defer func() {
			if p := recover(); p != nil {
				r = loaded{err: fmt.Errorf("loading the model panicked: %v", p)}
			}
			ch <- r
		}()
		e, err := load()
		r = loaded{engine: e, err: err}
	}()
	c.loadCh = ch
	c.state = loading
}

// Ready reports whether the model is ready, picking it up if the loader just finished.
func (c *Cleaner) Ready() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ready()
}

func (c *Cleaner) ready() bool {
	if c.state == loading {
		select {
		case r := <-c.loadCh:
			c.loadCh = nil
			switch {
			case r.err != nil:
				c.fail("cannot load the model: " + r.err.Error())
			case r.engine == nil:
				c.fail("the model loader returned no model")
			case r.engine.Hop() != c.hop:
				_ = r.engine.Close()
				c.fail(fmt.Sprintf("unexpected model format: %d-sample hops, want %d", r.engine.Hop(), c.hop))
			default:
				c.engine = r.engine
				c.state = ready
				// Start the resamplers clean too, in step with the fresh model.
				c.rate.clear()
			}
		default:
		}
	}
	return c.state == ready
}

// Failed says why the filter gave up and passes audio through, or "" if it has not.
func (c *Cleaner) Failed() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == failed {
		return c.why
	}
	return ""
}

// Process cleans a 24 kHz mono frame in place and returns it; its length never changes. A
// frame that is not whole 10 ms hops, or arrives while the filter is off, loading, failed or
// closed, comes back unchanged.
func (c *Cleaner) Process(in []int16) []int16 {
	on := c.enabled.Load()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !on {
		return in
	}
	if c.restart.Swap(false) {
		// Back on after a pause: don't let stale filter history smear into this frame.
		c.rate.clear()
	}
	c.prepare()
	// Only whole hops: the mic path always sends 40 ms (4-hop) frames.
	if len(in) == 0 || len(in)%HopIn != 0 || !c.ready() {
		return in
	}
	if len(c.out) < len(in) {
		c.out = make([]float32, len(in)) // once, on the first frame
	}
	if why := c.run(in); why != "" {
		c.fail(why)
		return in
	}
	for i := range in {
		in[i] = toInt16(c.out[i])
	}
	return in
}

func toInt16(s float32) int16 {
	v := s * 32768
	// Round half away from zero, as Rust's f32::round.
	if v >= 0 {
		v += 0.5
	} else {
		v -= 0.5
	}
	switch {
	case v >= 32767:
		return 32767
	case v <= -32768:
		return -32768
	}
	return int16(v)
}

// run cleans frame into c.out; a non-empty result says why it failed and leaves frame as is.
func (c *Cleaner) run(frame []int16) (why string) {
	defer func() {
		if p := recover(); p != nil {
			why = fmt.Sprintf("the model panicked: %v", p)
		}
	}()
	for k := 0; k+HopIn <= len(frame); k += HopIn {
		for i, s := range frame[k : k+HopIn] {
			c.mic[i] = float32(s) / 32768
		}
		c.rate.micToModel(c.mic[:], c.noisy)
		if err := c.engine.ProcessHop(c.noisy, c.clean); err != nil {
			return err.Error()
		}
		c.rate.modelToMic(c.clean, c.out[k:k+HopIn])
	}
	return ""
}

// Reset forgets all state (a new conversation, or the stream was cut).
func (c *Cleaner) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rate.clear()
	if c.state != ready {
		return
	}
	if why := c.resetEngine(); why != "" {
		c.fail(why)
	}
}

func (c *Cleaner) resetEngine() (why string) {
	defer func() {
		if p := recover(); p != nil {
			why = fmt.Sprintf("resetting the model panicked: %v", p)
		}
	}()
	if err := c.engine.Reset(); err != nil {
		return "cannot reset the model: " + err.Error()
	}
	return ""
}

// Close releases the model; afterwards every frame passes through. Safe to call more than once.
func (c *Cleaner) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var err error
	switch c.state {
	case ready:
		err = c.engine.Close()
		c.engine = nil
	case loading:
		// The loader still owns the engine: close it when it arrives.
		ch := c.loadCh
		go func() {
			if r := <-ch; r.engine != nil {
				_ = r.engine.Close()
			}
		}()
		c.loadCh = nil
	}
	c.state = closed
	return err
}
