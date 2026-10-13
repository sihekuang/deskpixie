package voice

// The runtime drives one Conversation: it feeds it inputs (hotkey, model events, mic state,
// a ~30 Hz tick) and carries out its effects against a voice.Session, an audio.Cleaner and a
// Go-side playback queue that the Swift audio engine drains (PullPlayback) and reports on
// (ReportPlayback). Port of ai-mascot crates/voice-core/src/runtime.rs, simplified for
// Phase 3: no tools, no delegation hold, chimes ignored.

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

// Tick is how often the runtime ticks the state machine and samples the mouth (~30 Hz).
const Tick = 33 * time.Millisecond

// APIKeyEnv is the variable the Phase 3 app reads the OpenAI key from.
const APIKeyEnv = "OPENAI_API_KEY"

// micQueueFrames bounds the mic hand-off: 32 frames of 40 ms = 1.28 s. Beyond that PushMic
// drops frames (counted by MicDrops) rather than block the audio thread.
const micQueueFrames = 32

// UIEvent is what the runtime tells the app. Kinds: "state", "mouth", "mic", "status",
// "transcript", "error", "metrics".
type UIEvent struct {
	Kind  string `json:"kind"`
	State string `json:"state,omitempty"`
	Level int    `json:"level,omitempty"`
	Text  string `json:"text,omitempty"`
	Role  string `json:"role,omitempty"`
}

// RuntimeConfig is what NewRuntime needs besides its collaborators.
type RuntimeConfig struct {
	Session      SessionConfig
	APIKey       string // "" = not configured: the hotkey shows the error state
	MouthLevels  int    // the active pack's mouth level count
	Conversation ConversationConfig
	// OnMicSent, when set, is called on the runtime goroutine right after Session.Send
	// returns for a mic frame, with the time PushMic was given for it. For latency metrics.
	OnMicSent func(capturedAt time.Time)
}

type micFrame struct {
	samples []int16
	at      time.Time
}

type connectResult struct {
	gen  uint64
	sess Session
	err  error
}

// playbackQueue is the Go side of playback: samples waiting for Swift to pull, plus what
// Swift last reported about its own ring.
type playbackQueue struct {
	mu           sync.Mutex
	queue        []int16
	flushPending bool    // the next PullPlayback tells Swift to drop its ring
	played       int64   // samples Swift has actually rendered (cumulative)
	buffered     int     // samples Swift holds but hasn't rendered yet (estimate between reports)
	rms          float32 // loudness of what Swift last rendered
}

func (p *playbackQueue) enqueue(s []int16) {
	p.mu.Lock()
	p.queue = append(p.queue, s...)
	p.mu.Unlock()
}

func (p *playbackQueue) flush() {
	p.mu.Lock()
	p.queue = nil
	p.buffered = 0
	p.rms = 0
	p.flushPending = true
	p.mu.Unlock()
}

func (p *playbackQueue) idle() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.queue) == 0 && p.buffered == 0
}

func (p *playbackQueue) playedSamples() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.played
}

func (p *playbackQueue) level() float32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rms
}

// Runtime glues a Conversation to a voice.Model session, a mic Cleaner and the playback
// queue. All exported methods are safe to call from any goroutine; none of them block on
// the runtime goroutine.
type Runtime struct {
	model   Model
	cleaner audio.Cleaner
	cfg     RuntimeConfig
	emitFn  func(UIEvent)

	mic       chan micFrame
	hotkey    chan struct{}
	micFailed chan string
	connected chan connectResult
	done      chan struct{}
	exited    chan struct{}
	closeOnce sync.Once
	ctx       context.Context
	cancel    context.CancelFunc
	drops     atomic.Uint64
	pb        playbackQueue
	// micUnavailable is a mic failure reported while no conversation was active (the app
	// could not start audio). The next EfStartMic fails at once with it instead of waiting
	// MicPermissionWait; it is consumed there, and cleared when mic frames arrive. Written
	// only by the runtime goroutine; atomic so tests can read it.
	micUnavailable atomic.Pointer[string]

	// Owned by the runtime goroutine.
	conv          *Conversation
	meter         *MouthMeter
	mouth         int
	session       Session
	events        <-chan Event
	gen           uint64 // incremented on every Connect; results of older attempts are closed
	connectCancel context.CancelFunc
	micActive     bool  // the conversation wants the mic
	micLiveSent   bool  // InMicLive fed for this mic start (on its first frame)
	responseMark  int64 // pb.played when the current reply started
	pending       []Input
}

// NewRuntime starts the runtime goroutine. emit receives UI events on that goroutine; it
// must not block for long. The runtime owns c and closes it in Close.
func NewRuntime(m Model, c audio.Cleaner, cfg RuntimeConfig, emit func(UIEvent)) *Runtime {
	if c == nil {
		c = audio.Passthrough{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{
		model:     m,
		cleaner:   c,
		cfg:       cfg,
		emitFn:    emit,
		mic:       make(chan micFrame, micQueueFrames),
		hotkey:    make(chan struct{}, 8),
		micFailed: make(chan string, 4),
		connected: make(chan connectResult, 4),
		done:      make(chan struct{}),
		exited:    make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
		conv:      NewConversation(cfg.Conversation),
		meter:     NewMouthMeter(cfg.MouthLevels),
	}
	go r.run()
	return r
}

// Hotkey starts or ends a conversation.
func (r *Runtime) Hotkey() {
	select {
	case r.hotkey <- struct{}{}:
	default: // a burst of presses; the queued ones are enough
	}
}

// PushMic hands over one captured mic frame. It never blocks: when the runtime is behind,
// the frame is dropped and counted (MicDrops). The frame is copied.
func (r *Runtime) PushMic(frame []int16, capturedAt time.Time) {
	f := micFrame{samples: append([]int16(nil), frame...), at: capturedAt}
	select {
	case r.mic <- f:
	default:
		r.drops.Add(1)
	}
}

// MicDrops is how many mic frames PushMic has dropped so far.
func (r *Runtime) MicDrops() uint64 { return r.drops.Load() }

// PullPlayback copies up to len(dst) queued samples into dst. flush=true (with n=0) means
// playback was interrupted: Swift must drop everything it has buffered.
func (r *Runtime) PullPlayback(dst []int16) (n int, flush bool) {
	p := &r.pb
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.flushPending {
		p.flushPending = false
		return 0, true
	}
	n = copy(dst, p.queue)
	p.queue = p.queue[n:]
	if len(p.queue) == 0 {
		p.queue = nil
	}
	// Until Swift's next report, count what it just took as buffered so the turn doesn't
	// look finished while the last chunk is still playing.
	p.buffered += n
	return n, false
}

// ReportPlayback is Swift's view of its ring: total samples rendered so far, samples still
// buffered, and the RMS (0..1) of what it last rendered (drives the mouth).
func (r *Runtime) ReportPlayback(playedSamples int64, bufferedSamples int, rms float32) {
	p := &r.pb
	p.mu.Lock()
	defer p.mu.Unlock()
	p.played = playedSamples
	if p.flushPending {
		// Swift hasn't seen the flush yet; what it reports buffered is about to be dropped.
		return
	}
	p.buffered = max(bufferedSamples, 0)
	p.rms = rms
}

// MicFailed reports that the mic could not start (e.g. permission denied).
func (r *Runtime) MicFailed(reason string) {
	select {
	case r.micFailed <- reason:
	default:
	}
}

// Close ends any conversation, stops the runtime goroutine and closes the cleaner.
// Safe to call more than once.
func (r *Runtime) Close() {
	r.closeOnce.Do(func() {
		close(r.done)
		r.cancel()
		<-r.exited
		_ = r.cleaner.Close()
	})
}

// queuedSamples is how many samples wait in the Go queue (tests).
func (r *Runtime) queuedSamples() int {
	r.pb.mu.Lock()
	defer r.pb.mu.Unlock()
	return len(r.pb.queue)
}

func (r *Runtime) emit(e UIEvent) {
	if r.emitFn != nil {
		r.emitFn(e)
	}
}

func (r *Runtime) run() {
	defer close(r.exited)
	ticker := time.NewTicker(Tick)
	defer ticker.Stop()
	for {
		select {
		case <-r.done:
			r.shutdown()
			return
		case <-r.hotkey:
			r.input(HotkeyInput())
		case reason := <-r.micFailed:
			if !r.conv.IsActive() {
				r.micUnavailable.Store(&reason) // fail the next mic start promptly
				continue
			}
			r.emit(UIEvent{Kind: "error", Text: "microphone: " + reason})
			r.input(FailedInput(Failure{Kind: FailMicDenied}))
		case c := <-r.connected:
			r.onConnected(c)
		case ev, ok := <-r.events:
			if !ok {
				r.session, r.events = nil, nil
				r.input(FailedInput(Failure{Kind: FailConnectionLost}))
				continue
			}
			if ev.Kind == EvTranscript {
				r.emit(UIEvent{Kind: "transcript", Role: ev.Role, Text: ev.Text})
			}
			r.input(ModelInput(ev))
		case f := <-r.mic:
			r.onMic(f)
		case <-ticker.C:
			r.input(TickInput(r.pb.idle()))
			r.updateMouth()
		}
	}
}

func (r *Runtime) shutdown() {
	r.apply(Effect{Kind: EfStopMic})
	if r.connectCancel != nil {
		r.connectCancel()
		r.connectCancel = nil
	}
	if r.session != nil {
		_ = r.session.Close()
		r.session, r.events = nil, nil
	}
	r.pb.flush()
}

// input steps the conversation and carries out the effects; inputs raised while applying
// effects are processed after the current batch.
func (r *Runtime) input(in Input) {
	r.pending = append(r.pending, in)
	for len(r.pending) > 0 {
		next := r.pending[0]
		r.pending = r.pending[1:]
		for _, fx := range r.conv.Step(next, time.Now()) {
			r.apply(fx)
		}
	}
}

func (r *Runtime) onMic(f micFrame) {
	if r.micUnavailable.Load() != nil {
		r.micUnavailable.Store(nil) // frames arriving prove the mic works
	}
	if !r.micActive || r.session == nil {
		return
	}
	if !r.micLiveSent {
		// The first frame after StartMic proves the mic is live (any permission prompt passed).
		r.micLiveSent = true
		r.input(MicLiveInput())
		if !r.micActive || r.session == nil {
			return
		}
	}
	out := r.cleaner.Process(f.samples)
	if len(out) == 0 {
		return
	}
	// An error here means the session is going away; its event stream reports it.
	_ = r.session.Send(Command{Kind: CmdAudio, Audio: out})
	if r.cfg.OnMicSent != nil {
		r.cfg.OnMicSent(f.at)
	}
}

func (r *Runtime) onConnected(c connectResult) {
	current := c.gen == r.gen && r.conv.Phase() == PhaseConnecting
	switch {
	case c.err == nil && current:
		r.session, r.events = c.sess, c.sess.Events()
	case c.err == nil:
		go func() { _ = c.sess.Close() }() // a stale attempt
	case current:
		r.emit(UIEvent{Kind: "error", Text: "connect: " + c.err.Error()})
		r.input(FailedInput(Failure{Kind: FailConnectionLost}))
	}
}

func (r *Runtime) send(c Command) {
	if r.session != nil {
		_ = r.session.Send(c)
	}
}

func (r *Runtime) apply(fx Effect) {
	switch fx.Kind {
	case EfConnect:
		r.gen++
		if r.cfg.APIKey == "" {
			r.emit(UIEvent{Kind: "error", Text: APIKeyEnv + " not set"})
			r.pending = append(r.pending, FailedInput(NoAPIKey(APIKeyEnv)))
			return
		}
		ctx, cancel := context.WithCancel(r.ctx)
		r.connectCancel = cancel
		gen, model, cfg, key := r.gen, r.model, r.cfg.Session, r.cfg.APIKey
		go func() {
			s, err := model.Connect(ctx, cfg, key)
			select {
			case r.connected <- connectResult{gen: gen, sess: s, err: err}:
			case <-r.done:
				if err == nil {
					_ = s.Close()
				}
			}
		}()
	case EfStartMic:
		r.micActive, r.micLiveSent = true, false
		r.emit(UIEvent{Kind: "mic", Text: "start"})
		if reason := r.micUnavailable.Swap(nil); reason != nil {
			r.emit(UIEvent{Kind: "error", Text: "microphone: " + *reason})
			r.pending = append(r.pending, FailedInput(Failure{Kind: FailMicDenied}))
		}
	case EfStopMic:
		if r.micActive {
			r.micActive = false
			r.emit(UIEvent{Kind: "mic", Text: "stop"})
		}
	case EfClose:
		if r.connectCancel != nil {
			r.connectCancel()
			r.connectCancel = nil
		}
		if s := r.session; s != nil {
			r.session, r.events = nil, nil
			go func() { _ = s.Close() }() // the close handshake must not stall the loop
		}
	case EfPlay:
		r.pb.enqueue(fx.Audio)
	case EfResponseStarted:
		r.responseMark = r.pb.playedSamples()
	case EfInterrupt:
		heard := max(r.pb.playedSamples()-r.responseMark, 0)
		r.pb.flush()
		r.send(Command{Kind: CmdCancelResponse, HeardMs: heard * 1000 / audio.SampleRate})
	case EfFlushPlayback:
		r.pb.flush()
	case EfPlayChime:
		// Chimes are out of scope for Phase 3.
	case EfInvokeTool:
		// Phase 3 registers no tools; answer so the turn doesn't wait on it.
		out, _ := json.Marshal(map[string]string{"error": "unknown tool " + fx.ToolName})
		r.send(Command{Kind: CmdToolResult, CallID: fx.CallID, Output: out, Respond: true})
		r.pending = append(r.pending, ToolDoneInput(fx.CallID, false))
	case EfMascot:
		r.emit(UIEvent{Kind: "state", State: string(fx.State)})
	case EfStatus:
		r.emit(UIEvent{Kind: "status", Text: fx.Status})
	case EfInject:
		r.send(Command{Kind: CmdInject, Text: fx.Text, Respond: true})
	case EfSignal:
		// No subscribers in Phase 3.
	}
}

func (r *Runtime) updateMouth() {
	level := 0
	if r.conv.Phase() == PhaseSpeaking {
		level = r.meter.Update(r.pb.level())
	} else {
		r.meter.Reset()
	}
	if level != r.mouth {
		r.mouth = level
		r.emit(UIEvent{Kind: "mouth", Level: level})
	}
}
