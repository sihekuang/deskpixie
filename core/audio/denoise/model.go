package denoise

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"

	"github.com/sihekuang/deskpixie/core/audio"
)

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

// ModelStage is the model as an audio.Stage at the model's rate: whole hops of Hop() samples
// in, as many cleaned samples out. It owns the live switch and the model's life: the model
// loads on a goroutine on the first enabled batch (or Prepare); until it is ready, while the
// switch is off, and for good after any failure (logged once) or Close, audio passes through
// unchanged. Safe for use from several goroutines.
type ModelStage struct {
	load Loader
	hop  int
	rate int
	logf func(string, ...any)

	enabled atomic.Bool
	// restart is set when the switch goes back on: the filters around the model start clean.
	restart atomic.Bool

	mu     sync.Mutex
	state  slotState
	loadCh chan loaded
	engine Engine
	why    string
	// fresh is set when a newly loaded engine is picked up: the filters start clean with it.
	fresh bool
	out   []float32
}

var (
	_ audio.Stage      = (*ModelStage)(nil)
	_ audio.InputRater = (*ModelStage)(nil)
	_ audio.Closer     = (*ModelStage)(nil)
)

// NewModelStage makes the stage for engines with hop-sample hops (Hop48k or Hop16k) loaded by
// load.
func NewModelStage(hop int, load Loader, opts Options) *ModelStage {
	logf := opts.Logf
	if logf == nil {
		logf = log.Printf
	}
	m := &ModelStage{load: load, hop: hop, rate: hop * 100, logf: logf}
	m.enabled.Store(opts.Enabled)
	return m
}

// Name is "denoise".
func (m *ModelStage) Name() string { return "denoise" }

// InputRate is the model's rate: 100 hops a second.
func (m *ModelStage) InputRate() int { return m.rate }

// OutputRate is 0: the model keeps its rate.
func (m *ModelStage) OutputRate() int { return 0 }

// Hop is the model's hop size in samples.
func (m *ModelStage) Hop() int { return m.hop }

// SetEnabled turns the model on or off; it is checked on every batch.
func (m *ModelStage) SetEnabled(on bool) {
	if was := m.enabled.Swap(on); on && !was {
		m.restart.Store(true)
	}
}

// Enabled reports the live switch.
func (m *ModelStage) Enabled() bool { return m.enabled.Load() }

// Prepare starts loading the model in the background without blocking.
func (m *ModelStage) Prepare() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepare()
}

func (m *ModelStage) prepare() {
	if m.state != unloaded {
		return
	}
	ch := make(chan loaded, 1)
	load := m.load
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
	m.loadCh = ch
	m.state = loading
}

// Ready reports whether the model is ready, picking it up if the loader just finished.
func (m *ModelStage) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready()
}

func (m *ModelStage) ready() bool {
	if m.state == loading {
		select {
		case r := <-m.loadCh:
			m.loadCh = nil
			switch {
			case r.err != nil:
				m.fail("cannot load the model: " + r.err.Error())
			case r.engine == nil:
				m.fail("the model loader returned no model")
			case r.engine.Hop() != m.hop:
				_ = r.engine.Close()
				m.fail(fmt.Sprintf("unexpected model format: %d-sample hops, want %d", r.engine.Hop(), m.hop))
			default:
				m.engine = r.engine
				m.state = ready
				m.fresh = true
			}
		default:
		}
	}
	return m.state == ready
}

// activate is the Cleaner's per-frame check: whether the model will clean this frame (it is
// on, and ready), and whether the filters around it must start clean first (the switch just
// came back on, or a new engine just arrived). Loading starts here.
func (m *ModelStage) activate() (on, startClean bool) {
	if !m.enabled.Load() {
		return false, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	startClean = m.restart.Swap(false)
	m.prepare()
	on = m.ready()
	if m.fresh {
		m.fresh = false
		startClean = true
	}
	return on, startClean
}

func (m *ModelStage) fail(why string) {
	m.logf("deskpixie: noise filter off, passing the mic through: %s", why)
	m.why = why
	m.state = failed
	if m.engine != nil {
		_ = m.engine.Close()
		m.engine = nil
	}
}

// failWith gives up for a reason found outside the model (e.g. another stage); it does
// nothing once the model has failed or closed.
func (m *ModelStage) failWith(why string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == failed || m.state == closed {
		return
	}
	m.fail(why)
}

// Failed says why the model gave up and passes audio through, or "" if it has not.
func (m *ModelStage) Failed() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == failed {
		return m.why
	}
	return ""
}

// errFailed marks an error the stage has already logged and acted on.
type errFailed struct{ why string }

func (e errFailed) Error() string { return e.why }

// Process cleans whole hops. While the model is off, loading, failed or closed, in comes back
// unchanged. A failing or panicking model fails the stage (logged once) and the batch returns
// an error; from then on audio passes through.
func (m *ModelStage) Process(_ context.Context, in []float32) ([]float32, error) {
	if !m.enabled.Load() {
		return in, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.prepare()
	if !m.ready() {
		return in, nil
	}
	if len(in)%m.hop != 0 {
		return nil, fmt.Errorf("the model takes whole %d-sample hops, got %d samples", m.hop, len(in))
	}
	if cap(m.out) < len(in) {
		m.out = make([]float32, len(in)) // once, on the first batch
	}
	out := m.out[:len(in)]
	if why := m.run(in, out); why != "" {
		m.fail(why)
		return nil, errFailed{why}
	}
	return out, nil
}

func (m *ModelStage) run(in, out []float32) (why string) {
	defer func() {
		if p := recover(); p != nil {
			why = fmt.Sprintf("the model panicked: %v", p)
		}
	}()
	for k := 0; k+m.hop <= len(in); k += m.hop {
		if err := m.engine.ProcessHop(in[k:k+m.hop], out[k:k+m.hop]); err != nil {
			return err.Error()
		}
	}
	return ""
}

// Reset returns a ready model to its freshly loaded state (failing the stage if it cannot).
func (m *ModelStage) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state != ready {
		return
	}
	if why := m.resetEngine(); why != "" {
		m.fail(why)
	}
}

func (m *ModelStage) resetEngine() (why string) {
	defer func() {
		if p := recover(); p != nil {
			why = fmt.Sprintf("resetting the model panicked: %v", p)
		}
	}()
	if err := m.engine.Reset(); err != nil {
		return "cannot reset the model: " + err.Error()
	}
	return ""
}

// Close releases the model; afterwards everything passes through. Safe to call more than
// once.
func (m *ModelStage) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	switch m.state {
	case ready:
		err = m.engine.Close()
		m.engine = nil
	case loading:
		// The loader still owns the engine: close it when it arrives.
		ch := m.loadCh
		go func() {
			if r := <-ch; r.engine != nil {
				_ = r.engine.Close()
			}
		}()
		m.loadCh = nil
	}
	m.state = closed
	return err
}
