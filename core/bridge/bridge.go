// Package bridge is the engine the libdeskpixie C ABI wraps. All logic lives here, in pure
// Go, so it can be tested; core/cmd/libdeskpixie only converts C types.
//
// Threading: PushAudio, PullAudio and ReportPlayback are called from Swift's audio threads
// and never take a lock that Configure or Destroy hold while waiting, so they never block
// on a runtime being torn down. UI events queue in a bounded buffer that drops (oldest
// mouth first) rather than block the runtime goroutine.
package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/metrics"
	"github.com/sihekuang/deskpixie/core/packs"
	"github.com/sihekuang/deskpixie/core/voice"
	"github.com/sihekuang/deskpixie/core/voice/openai"
)

// Defaults applied to an empty Config field.
const (
	DefaultModel           = "gpt-realtime"
	DefaultVoice           = "marin"
	DefaultIdleTimeoutSecs = 7
	DefaultPack            = "bit"
)

// holdTimeout is the silence timeout while a hold is on (unused in Phase 3, which has no tools).
const holdTimeout = 180 * time.Second

// DefaultMetricsInterval is how often a metrics snapshot is emitted and logged.
const DefaultMetricsInterval = 10 * time.Second

// eventQueueCap bounds the UI event queue.
const eventQueueCap = 256

// Errors returned by Configure. dp_configure maps them to 1 (ErrNotInit), 2 (ErrBadJSON)
// and 3 (anything else: a build error).
var (
	ErrNotInit = errors.New("engine not initialized")
	ErrBadJSON = errors.New("invalid config JSON")
)

// Config is the JSON Swift passes to Configure.
type Config struct {
	APIKey          string   `json:"apiKey"`
	Model           string   `json:"model"`
	Voice           string   `json:"voice"`
	PersonaPath     string   `json:"personaPath"`
	IdleTimeoutSecs int      `json:"idleTimeoutSecs"`
	Pack            string   `json:"pack"`
	PackRoots       []string `json:"packRoots"` // bundled first, then user
	NoisyRoom       bool     `json:"noisyRoom"`
}

func (c *Config) applyDefaults() {
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.Voice == "" {
		c.Voice = DefaultVoice
	}
	if c.IdleTimeoutSecs <= 0 {
		c.IdleTimeoutSecs = DefaultIdleTimeoutSecs
	}
	if c.Pack == "" {
		c.Pack = DefaultPack
	}
}

// Options are the engine's injectable dependencies (tests).
type Options struct {
	MetricsLogPath  string             // "" = ~/Library/Logs/DeskPixie/metrics.log
	MetricsInterval time.Duration      // 0 = DefaultMetricsInterval
	NewModel        func() voice.Model // nil = OpenAI Realtime
}

// DefaultMetricsLogPath is ~/Library/Logs/DeskPixie/metrics.log ("" if HOME is unknown).
func DefaultMetricsLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Logs", "DeskPixie", "metrics.log")
}

// Engine owns at most one voice.Runtime plus the active pack, the event queue and the
// metrics recorder. Safe for concurrent use.
type Engine struct {
	opts Options
	rec  metrics.Recorder
	q    eventQueue

	cfgMu  sync.Mutex // serialises Configure and Close
	closed bool       // guarded by cfgMu

	rt    atomic.Pointer[voice.Runtime]
	pack  atomic.Pointer[packs.Pack]
	drops atomic.Uint64 // mic frames dropped (cumulative across runtimes)

	errMu   sync.Mutex
	lastErr string

	calls handlers  // dp_call methods registered on this engine
	tray  trayModel // what app.state reports until P14

	stop      chan struct{}
	loopDone  chan struct{}
	closeOnce sync.Once
}

// New starts an engine (and its metrics loop) with no runtime; Configure builds one.
func New(o Options) *Engine {
	if o.MetricsLogPath == "" {
		o.MetricsLogPath = DefaultMetricsLogPath()
	}
	if o.MetricsInterval <= 0 {
		o.MetricsInterval = DefaultMetricsInterval
	}
	if o.NewModel == nil {
		o.NewModel = func() voice.Model { return openai.New("") }
	}
	e := &Engine{opts: o, stop: make(chan struct{}), loopDone: make(chan struct{})}
	e.tray.state = string(voice.StateIdle)
	e.registerTempHandlers()
	go e.metricsLoop()
	return e
}

// Configure parses cfg, resolves the pack and (re)builds the runtime. A runtime that
// already exists is closed first. On error the previous runtime and pack are kept.
func (e *Engine) Configure(jsonCfg []byte) error {
	err := e.configure(jsonCfg)
	e.setErr(err)
	return err
}

func (e *Engine) configure(jsonCfg []byte) error {
	var c Config
	if err := json.Unmarshal(jsonCfg, &c); err != nil {
		return fmt.Errorf("%w: %v", ErrBadJSON, err)
	}
	c.applyDefaults()

	found, rejected := packs.Load(c.PackRoots...)
	if rejected != nil {
		log.Printf("deskpixie: skipped invalid packs: %v", rejected)
	}
	pack, ok := packs.Select(found, c.Pack)
	if !ok {
		return fmt.Errorf("no valid mascot pack in packRoots %q", c.PackRoots)
	}

	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	if e.closed {
		return ErrNotInit
	}
	if old := e.rt.Swap(nil); old != nil {
		old.Close()
		e.emit(voice.UIEvent{Kind: "state", State: string(voice.StateIdle)})
	}
	e.pack.Store(&pack)
	e.setPacks(found, pack.ID)
	rt := voice.NewRuntime(e.opts.NewModel(), nil, voice.RuntimeConfig{
		Session: voice.SessionConfig{
			Model:        c.Model,
			Voice:        c.Voice,
			Instructions: voice.Instructions(c.PersonaPath),
			NoisyRoom:    c.NoisyRoom,
		},
		APIKey:      c.APIKey,
		MouthLevels: int(pack.Mouth.Levels),
		Conversation: voice.ConversationConfig{
			IdleTimeout: time.Duration(c.IdleTimeoutSecs) * time.Second,
			HoldTimeout: holdTimeout,
		},
		OnMicSent: e.onMicSent,
	}, e.emit)
	e.rt.Store(rt)
	return nil
}

// Hotkey starts or ends a conversation. No-op before Configure.
func (e *Engine) Hotkey() {
	if rt := e.rt.Load(); rt != nil {
		rt.Hotkey()
	}
}

// PushAudio hands one mic frame (copied) to the runtime. capturedUnixNanos 0 = unknown
// (now). It never blocks; it returns false when the frame was dropped (queue full or no
// runtime). Assumes a single producer (Swift's mic thread) for exact drop accounting.
func (e *Engine) PushAudio(s []int16, capturedUnixNanos int64) bool {
	rt := e.rt.Load()
	if rt == nil {
		return false
	}
	at := time.Now()
	if capturedUnixNanos != 0 {
		at = time.Unix(0, capturedUnixNanos)
	}
	before := rt.MicDrops()
	rt.PushMic(s, at)
	if rt.MicDrops() != before {
		e.rec.MicDrops(e.drops.Add(1))
		return false
	}
	return true
}

// PullAudio copies queued playback samples into dst. flush=true (n=0) means drop
// everything Swift has buffered (barge-in).
func (e *Engine) PullAudio(dst []int16) (int, bool) {
	rt := e.rt.Load()
	if rt == nil {
		return 0, false
	}
	return rt.PullPlayback(dst)
}

// ReportPlayback is Swift's playback position (cumulative played samples), its buffered
// sample count, the RMS of what it rendered and its cumulative underrun count.
func (e *Engine) ReportPlayback(played int64, buffered int, rms float32, underruns int64) {
	e.rec.Underruns(underruns)
	if rt := e.rt.Load(); rt != nil {
		rt.ReportPlayback(played, buffered, rms)
	}
}

// MicFailed reports that Swift could not start the mic (e.g. permission denied).
func (e *Engine) MicFailed(reason string) {
	if rt := e.rt.Load(); rt != nil {
		rt.MicFailed(reason)
	}
}

// PollEvent returns the oldest queued UI event as JSON, or nil when there is none.
func (e *Engine) PollEvent() []byte { return e.q.pop() }

// PackJSON is {"pack":<resolved pack>,"sheetPath":"/abs/sheet.png"}, or nil before a
// successful Configure.
func (e *Engine) PackJSON() []byte {
	p := e.pack.Load()
	if p == nil {
		return nil
	}
	b, err := json.Marshal(struct {
		Pack      *packs.Pack `json:"pack"`
		SheetPath string      `json:"sheetPath"`
	}{p, p.SheetPath})
	if err != nil {
		return nil
	}
	return b
}

// LastError is the last Configure error ("" after a success).
func (e *Engine) LastError() string {
	e.errMu.Lock()
	defer e.errMu.Unlock()
	return e.lastErr
}

// Close stops the metrics loop and closes the runtime. Idempotent.
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		close(e.stop)
		<-e.loopDone
		e.cfgMu.Lock()
		e.closed = true
		if rt := e.rt.Swap(nil); rt != nil {
			rt.Close()
		}
		e.pack.Store(nil)
		e.cfgMu.Unlock()
	})
}

func (e *Engine) setErr(err error) {
	e.errMu.Lock()
	defer e.errMu.Unlock()
	if err == nil {
		e.lastErr = ""
	} else {
		e.lastErr = err.Error()
	}
}

// emit is the runtime's event sink. It runs on the runtime goroutine and never blocks.
// A state or status change is followed by a `tray` event so Swift re-reads app.state.
func (e *Engine) emit(ev voice.UIEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	e.q.push(ev.Kind, b)
	if e.observe(ev) {
		e.pushTray()
	}
}

func (e *Engine) onMicSent(capturedAt time.Time) {
	e.rec.MicToSend(time.Since(capturedAt))
}

func (e *Engine) metricsLoop() {
	defer close(e.loopDone)
	t := time.NewTicker(e.opts.MetricsInterval)
	defer t.Stop()
	logFailed := false
	for {
		select {
		case <-e.stop:
			return
		case now := <-t.C:
			snap, err := json.Marshal(e.rec.Snapshot())
			if err != nil {
				continue
			}
			e.emit(voice.UIEvent{Kind: "metrics", Text: string(snap)})
			if err := appendLine(e.opts.MetricsLogPath, now.Format(time.RFC3339)+" "+string(snap)+"\n"); err != nil && !logFailed {
				logFailed = true
				log.Printf("deskpixie: metrics log: %v", err)
			}
		}
	}
}

func appendLine(path, line string) error {
	if path == "" {
		return errors.New("no metrics log path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(line)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// eventQueue is a bounded FIFO of encoded UI events. When full, push drops the oldest
// "mouth" event (stale animation frames matter least), else the oldest event. Its lock is
// held only for slice bookkeeping, so push never blocks the caller in any real sense.
type eventQueue struct {
	mu      sync.Mutex
	items   []queuedEvent
	dropped uint64
}

type queuedEvent struct {
	kind string
	data []byte
}

func (q *eventQueue) push(kind string, data []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= eventQueueCap {
		victim := 0
		for i, it := range q.items {
			if it.kind == "mouth" {
				victim = i
				break
			}
		}
		q.items = append(q.items[:victim], q.items[victim+1:]...)
		q.dropped++
	}
	q.items = append(q.items, queuedEvent{kind, data})
}

func (q *eventQueue) pop() []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return nil
	}
	b := q.items[0].data
	q.items[0] = queuedEvent{}
	q.items = q.items[1:]
	if len(q.items) == 0 {
		q.items = nil
	}
	return b
}

// --- process-wide engine, used by the C ABI ---

var (
	globalMu       sync.Mutex // serialises Init and Destroy
	global         atomic.Pointer[Engine]
	defaultOptions Options // tests override (metrics log path)
)

// Init creates the process-wide engine. Idempotent.
func Init() {
	globalMu.Lock()
	defer globalMu.Unlock()
	if global.Load() == nil {
		global.Store(New(defaultOptions))
	}
}

// Configure configures the process-wide engine (ErrNotInit before Init).
func Configure(jsonCfg []byte) error {
	e := global.Load()
	if e == nil {
		notInitErr.Store(true)
		return ErrNotInit
	}
	notInitErr.Store(false)
	return e.Configure(jsonCfg)
}

var notInitErr atomic.Bool

// Hotkey: see Engine.Hotkey.
func Hotkey() {
	if e := global.Load(); e != nil {
		e.Hotkey()
	}
}

// PushAudio: see Engine.PushAudio. False (dropped) before Init.
func PushAudio(s []int16, capturedUnixNanos int64) bool {
	if e := global.Load(); e != nil {
		return e.PushAudio(s, capturedUnixNanos)
	}
	return false
}

// PullAudio: see Engine.PullAudio.
func PullAudio(dst []int16) (int, bool) {
	if e := global.Load(); e != nil {
		return e.PullAudio(dst)
	}
	return 0, false
}

// ReportPlayback: see Engine.ReportPlayback.
func ReportPlayback(played int64, buffered int, rms float32, underruns int64) {
	if e := global.Load(); e != nil {
		e.ReportPlayback(played, buffered, rms, underruns)
	}
}

// MicFailed: see Engine.MicFailed.
func MicFailed(reason string) {
	if e := global.Load(); e != nil {
		e.MicFailed(reason)
	}
}

// PollEvent: see Engine.PollEvent. Nil before Init.
func PollEvent() []byte {
	if e := global.Load(); e != nil {
		return e.PollEvent()
	}
	return nil
}

// PackJSON: see Engine.PackJSON. Nil before Init/Configure.
func PackJSON() []byte {
	if e := global.Load(); e != nil {
		return e.PackJSON()
	}
	return nil
}

// LastError is the last Configure error, including calling it before Init.
func LastError() string {
	if notInitErr.Load() {
		return ErrNotInit.Error()
	}
	if e := global.Load(); e != nil {
		return e.LastError()
	}
	return ""
}

// Destroy closes the process-wide engine. Idempotent; Init may be called again after.
func Destroy() {
	globalMu.Lock()
	defer globalMu.Unlock()
	if e := global.Swap(nil); e != nil {
		e.Close()
	}
}
