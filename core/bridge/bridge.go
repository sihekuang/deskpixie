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
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/app/agentapp"
	"github.com/sihekuang/deskpixie/core/logfile"
	"github.com/sihekuang/deskpixie/core/metrics"
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

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

// Config is the JSON Swift passes to dp_configure: paths only. Everything else comes from
// settings.json in AppSupport (P14). Example:
//
//	{"appSupport":"/Users/me/Library/Application Support/com.sihekuang.deskpixie",
//	 "bundledPixies":"/Applications/DeskPixie.app/Contents/Resources/pixies",
//	 "designSystem":"/Applications/DeskPixie.app/Contents/Resources/design-system",
//	 "dfn3ModelPath":"/Applications/DeskPixie.app/Contents/Resources/DeepFilterNet3.tar.gz",
//	 "platform":"macos"}
//
// Phase 3's config ({apiKey, model, voice, personaPath, idleTimeoutSecs, pack, packRoots,
// noisyRoom}) is still accepted while Swift moves over: packRoots[0] becomes bundledPixies
// and the folder above the last root becomes appSupport; the other old keys are ignored
// (settings.json and env/.env own them now).
type Config struct {
	app.Paths
	// PackRoots is Phase 3's [bundled, user] pixie roots (deprecated).
	PackRoots []string `json:"packRoots"`
}

// paths resolves the legacy keys.
func (c Config) paths() app.Paths {
	p := c.Paths
	if len(c.PackRoots) > 0 {
		if p.BundledPixies == "" && len(c.PackRoots) > 1 {
			p.BundledPixies = c.PackRoots[0]
		}
		if p.AppSupport == "" {
			p.AppSupport = filepath.Dir(filepath.Clean(c.PackRoots[len(c.PackRoots)-1]))
		}
	}
	return p
}

// Options are the engine's injectable dependencies (tests).
type Options struct {
	MetricsLogPath  string        // "" = logs/metrics.log in the configured app's data folder (#225)
	MetricsInterval time.Duration // 0 = DefaultMetricsInterval
	// Providers replaces the voice provider registry (nil = OpenAI Realtime + Gemini Live).
	Providers map[string]app.ModelFactory
	// Secrets replaces the API key lookup (nil = env, then .env in the cwd and app support).
	Secrets voice.SecretSource
	// Run replaces starting helper programs (`open`); nil = os/exec.
	Run func(program string, args ...string) error
	// Agent builds the delegation agent; nil = agentapp.Factory (herdr, the hook server on
	// 127.0.0.1:agent.port, ~/.claude.json trust). Tests pass NoAgent.
	Agent app.AgentFactory
}

// NoAgent is an agent factory that never builds one (delegation off).
var NoAgent app.AgentFactory = func(settings.Config, string, app.AgentHooks) app.Agent { return nil }

// Engine owns at most one app core (and so one voice.Runtime), the event queue and the
// metrics recorder. Safe for concurrent use.
type Engine struct {
	opts Options
	rec  metrics.Recorder
	q    eventQueue

	cfgMu  sync.Mutex // serialises Configure and Close
	closed bool       // guarded by cfgMu

	app atomic.Pointer[app.App]
	// metricsPath is where metrics go once configured ("" before: none are kept).
	metricsPath atomic.Pointer[string]
	rt          atomic.Pointer[voice.Runtime]
	drops       atomic.Uint64 // mic frames dropped (cumulative across runtimes)

	errMu   sync.Mutex
	lastErr string

	calls handlers // dp_call methods registered on this engine

	stop      chan struct{}
	loopDone  chan struct{}
	closeOnce sync.Once
}

// New starts an engine (and its metrics loop) with no app; Configure builds one.
func New(o Options) *Engine {
	if o.MetricsInterval <= 0 {
		o.MetricsInterval = DefaultMetricsInterval
	}
	e := &Engine{opts: o, stop: make(chan struct{}), loopDone: make(chan struct{})}
	if o.MetricsLogPath != "" {
		e.metricsPath.Store(&o.MetricsLogPath) // a set path is used from the start
	}
	go e.metricsLoop()
	return e
}

// Configure parses the paths and (re)builds the app core from settings.json. An app that
// already exists is closed once the new one is built; on error the previous one is kept.
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
	paths := c.paths()
	if paths.AppSupport == "" {
		return fmt.Errorf("%w: appSupport is required", ErrBadJSON)
	}

	e.cfgMu.Lock()
	defer e.cfgMu.Unlock()
	if e.closed {
		return ErrNotInit
	}
	// With the other logs in the app's data folder, so an isolated app (DESKPIXIE_SUPPORT_DIR)
	// never writes the real user's (#225).
	metricsPath := e.opts.MetricsLogPath
	if metricsPath == "" {
		metricsPath = filepath.Join(paths.AppSupport, "logs", "metrics.log")
	}
	// The core's log is kept in the app's data folder too, not only on stderr (#225), from
	// before the app starts so its startup lines are in it.
	prevLog := log.Writer()
	log.SetOutput(io.MultiWriter(os.Stderr, logfile.New(app.AppLogPath(paths.AppSupport), e.logMax)))
	a, err := app.New(app.Deps{
		Paths:     paths,
		Emit:      e.q.push,
		Providers: e.opts.Providers,
		Secrets:   e.opts.Secrets,
		Run:       e.opts.Run,
		OnMicSent: e.onMicSent,
		Agent:     e.agentFactory(),

		MetricsLogPath: metricsPath,
	})
	if err != nil {
		log.SetOutput(prevLog) // the running app (if any) keeps its log
		return err
	}
	old := e.app.Swap(a)
	e.metricsPath.Store(&metricsPath)
	e.rt.Store(a.Runtime())
	a.Register(registrar{e})
	if old != nil {
		old.Close()
	}
	return nil
}

// logMax is the Log size setting in bytes (the default before an app is configured).
func (e *Engine) logMax() int64 {
	if a := e.app.Load(); a != nil {
		return a.LogMaxBytes()
	}
	return logfile.DefaultMax
}

func (e *Engine) agentFactory() app.AgentFactory {
	if e.opts.Agent != nil {
		return e.opts.Agent
	}
	return agentapp.Factory
}

// registrar adapts the engine's registry to app.Registrar.
type registrar struct{ e *Engine }

func (r registrar) Register(method string, fn func(json.RawMessage) (any, error)) {
	r.e.Register(method, Handler(fn))
}

// Hotkey starts or ends a conversation. No-op before Configure.
func (e *Engine) Hotkey() {
	if a := e.app.Load(); a != nil {
		a.Hotkey()
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

// PackJSON is {"pack":<resolved pack>,"sheetPath":"/abs/sheet.png","scale":n,"hitMask":
// "<base64>"} for the pack shown, or nil before a successful Configure.
func (e *Engine) PackJSON() []byte {
	if a := e.app.Load(); a != nil {
		return a.PackJSON()
	}
	return nil
}

// LastError is the last Configure error ("" after a success).
func (e *Engine) LastError() string {
	e.errMu.Lock()
	defer e.errMu.Unlock()
	return e.lastErr
}

// Close stops the metrics loop and closes the app core. Idempotent.
func (e *Engine) Close() {
	e.closeOnce.Do(func() {
		close(e.stop)
		<-e.loopDone
		e.cfgMu.Lock()
		e.closed = true
		e.rt.Store(nil)
		if a := e.app.Swap(nil); a != nil {
			a.Close() // its shutdown lines still reach its log
			log.SetOutput(os.Stderr)
		}
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

// emit queues a voice-style UI event (metrics). It never blocks.
func (e *Engine) emit(ev voice.UIEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	e.q.push(ev.Kind, b)
}

func (e *Engine) onMicSent(capturedAt time.Time) {
	e.rec.MicToSend(time.Since(capturedAt))
}

func (e *Engine) metricsLoop() {
	defer close(e.loopDone)
	t := time.NewTicker(e.opts.MetricsInterval)
	defer t.Stop()
	// Kept to the Log size setting like the other logs (#225).
	var metricsLog *logfile.Writer
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
			if p := e.metricsPath.Load(); p != nil {
				if metricsLog == nil || metricsLog.Path() != *p {
					metricsLog = logfile.New(*p, e.logMax)
				}
				_, _ = metricsLog.Write([]byte(now.Format(time.RFC3339) + " " + string(snap) + "\n"))
			}
		}
	}
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
