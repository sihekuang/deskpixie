package voice

// The runtime drives one Conversation: it feeds it inputs (hotkey, model events, mic state,
// a ~30 Hz tick) and carries out its effects against a voice.Session, an audio.Cleaner and a
// Go-side playback queue that the Swift audio engine drains (PullPlayback) and reports on
// (ReportPlayback). Port of ai-mascot crates/voice-core/src/runtime.rs: tools (one goroutine
// per call, results tagged with the conversation generation), Inject, HoldOpen, signals
// (Subscribe/IsOpen), session facts, chimes, SetMouthLevels and VOICE_DEBUG recording.

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

// Tick is how often the runtime ticks the state machine and samples the mouth (~30 Hz).
const Tick = 33 * time.Millisecond

// APIKeyEnv is the key hint used when RuntimeConfig.Secrets is nil (RuntimeConfig.APIKey).
const APIKeyEnv = "OPENAI_API_KEY"

// signalBuffer is how many signals a slow subscriber may fall behind before it misses some.
const signalBuffer = 64

// SessionFacts are facts that change between conversations (today's date, the time zone),
// appended to the instructions on every connect so a long-running app never sends stale ones.
type SessionFacts interface {
	Facts() string
}

// withFacts is the instructions followed by a blank line and facts; blank facts add nothing.
func withFacts(instructions, facts string) string {
	f := strings.TrimSpace(facts)
	if f == "" {
		return instructions
	}
	return instructions + "\n\n" + f
}

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
	// Session.Tools is ignored: the runtime fills it from Tools.
	Session SessionConfig
	// APIKey is used only when Secrets is nil: "" = not configured, the hotkey shows the
	// error state naming APIKeyEnv. Kept for the bridge and tests; the app passes Secrets.
	APIKey string
	// Secrets finds the key for Model.Name() on each connect; nil = APIKey.
	Secrets SecretSource
	// Tools the model may call; nil = DefaultTools() (end_conversation).
	Tools *ToolRegistry
	// Facts, when set, are appended to the instructions on each connect.
	Facts SessionFacts
	// Chimes: play the start/end earcons through the playback queue.
	Chimes bool
	// RecordDir, when set, saves each conversation's mic audio there as WAV (see RecordingDir).
	RecordDir    string
	MouthLevels  int // the active pack's mouth level count
	Conversation ConversationConfig
	// OnMicSent, when set, is called on the runtime goroutine right after Session.Send
	// returns for a mic frame, with the time PushMic was given for it. For latency metrics.
	OnMicSent func(capturedAt time.Time)

	// playHook sees every enqueue (n samples) and flush of the playback queue (tests).
	playHook func(n int, flush bool)
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

// toolResult is a finished tool call, tagged with the conversation generation that invoked it.
type toolResult struct {
	gen     uint64
	callID  string
	outcome ToolOutcome
}

type controlKind int

const (
	ctlInject controlKind = iota
	ctlHoldOpen
	ctlMouthLevels
	ctlOpen
)

type control struct {
	kind   controlKind
	notice Notice
	on     bool
	levels int
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
	hook         func(n int, flush bool)
}

func (p *playbackQueue) enqueue(s []int16) {
	p.mu.Lock()
	p.queue = append(p.queue, s...)
	p.mu.Unlock()
	if p.hook != nil {
		p.hook(len(s), false)
	}
}

func (p *playbackQueue) flush() {
	p.mu.Lock()
	p.queue = nil
	p.buffered = 0
	p.rms = 0
	p.flushPending = true
	p.mu.Unlock()
	if p.hook != nil {
		p.hook(0, true)
	}
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
	secrets SecretSource
	tools   *ToolRegistry

	mic       chan micFrame
	hotkey    chan struct{}
	micFailed chan AudioError
	connected chan connectResult
	toolDone  chan toolResult
	ctlNotify chan struct{}
	ctlMu     sync.Mutex
	ctlQueue  []control
	open      atomic.Bool
	// resting: the conversation is idle (no conversation starting, open, closing or in its
	// error cooldown); updated after every input.
	resting   atomic.Bool
	sigMu     sync.Mutex
	sigSubs   []chan VoiceSignal
	sigClosed bool
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
	micUnavailable atomic.Pointer[AudioError]

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
	recorder      *MicRecorder
	take          *MicTake // the current conversation's recording
	lastTake      <-chan struct{}
}

// NewRuntime starts the runtime goroutine. emit receives UI events on that goroutine; it
// must not block for long. The runtime owns c and closes it in Close.
func NewRuntime(m Model, c audio.Cleaner, cfg RuntimeConfig, emit func(UIEvent)) *Runtime {
	if c == nil {
		c = audio.Passthrough{}
	}
	secrets := cfg.Secrets
	if secrets == nil {
		secrets = fixedSecrets{key: cfg.APIKey, hint: APIKeyEnv}
	}
	tools := cfg.Tools
	if tools == nil {
		tools = DefaultTools()
	}
	cfg.Session.Tools = tools.Specs()
	if CapabilitiesOf(m).OwnsTurnTaking {
		cfg.Conversation.ProviderTurns = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &Runtime{
		model:     m,
		cleaner:   c,
		cfg:       cfg,
		emitFn:    emit,
		secrets:   secrets,
		tools:     tools,
		mic:       make(chan micFrame, micQueueFrames),
		hotkey:    make(chan struct{}, 8),
		micFailed: make(chan AudioError, 4),
		connected: make(chan connectResult, 4),
		toolDone:  make(chan toolResult, 16),
		ctlNotify: make(chan struct{}, 1),
		done:      make(chan struct{}),
		exited:    make(chan struct{}),
		ctx:       ctx,
		cancel:    cancel,
		conv:      NewConversation(cfg.Conversation),
		meter:     NewMouthMeter(cfg.MouthLevels),
	}
	r.resting.Store(true)
	r.pb.hook = cfg.playHook
	if cfg.RecordDir != "" {
		r.recorder = NewMicRecorder(cfg.RecordDir)
	}
	go r.run()
	return r
}

// Open starts a conversation, as the hotkey does, but only when Bit is at rest and the mic is
// usable: false (nothing done) while a conversation is starting, open, closing or showing an
// error, when the mic could not start, or after Close.
func (r *Runtime) Open() bool {
	select {
	case <-r.done:
		return false
	default:
	}
	if !r.resting.Load() || r.micUnavailable.Load() != nil {
		return false
	}
	r.control(control{kind: ctlOpen})
	return true
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
	r.AudioFailed(AudioError{Kind: AudioPermissionDenied, Msg: reason})
}

// AudioFailed reports an audio failure: mid-conversation it ends the conversation with the
// matching status line (e.g. "Voice: audio device changed"); while idle a mic failure makes
// the next conversation fail as soon as it asks for the mic, and a device change is ignored.
func (r *Runtime) AudioFailed(e AudioError) {
	select {
	case r.micFailed <- e:
	default:
	}
}

// Inject asks Bit to say something about n once nobody is talking. If no conversation is
// open it comes straight back as a SigClosed signal with n undelivered.
func (r *Runtime) Inject(n Notice) { r.control(control{kind: ctlInject, notice: n}) }

// HoldOpen: while on, the silence timeout is the hold timeout and Bit rests in delegating.
func (r *Runtime) HoldOpen(on bool) { r.control(control{kind: ctlHoldOpen, on: on}) }

// SetMouthLevels: the active pack changed; its mouth level count.
func (r *Runtime) SetMouthLevels(levels int) {
	r.control(control{kind: ctlMouthLevels, levels: levels})
}

// IsOpen: a conversation is open (SigOpened was sent and SigClosed hasn't been yet). For
// re-syncing a subscriber that lagged and may have missed one of them.
func (r *Runtime) IsOpen() bool { return r.open.Load() }

// Subscribe returns signals from now on: conversation opened/closed and the user's words.
// A subscriber more than 64 signals behind misses some. The channel closes with the runtime.
func (r *Runtime) Subscribe() <-chan VoiceSignal {
	ch := make(chan VoiceSignal, signalBuffer)
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	if r.sigClosed {
		close(ch)
		return ch
	}
	r.sigSubs = append(r.sigSubs, ch)
	return ch
}

// Unsubscribe stops signals to ch (from Subscribe) and closes it. Safe to call more than
// once and after Close.
func (r *Runtime) Unsubscribe(ch <-chan VoiceSignal) {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	for i, c := range r.sigSubs {
		if c == ch {
			r.sigSubs = append(r.sigSubs[:i], r.sigSubs[i+1:]...)
			close(c)
			return
		}
	}
}

func (r *Runtime) control(c control) {
	r.ctlMu.Lock()
	r.ctlQueue = append(r.ctlQueue, c)
	r.ctlMu.Unlock()
	select {
	case r.ctlNotify <- struct{}{}:
	default:
	}
}

func (r *Runtime) publish(s VoiceSignal) {
	switch s.Kind {
	case SigOpened:
		r.open.Store(true)
	case SigClosed:
		r.open.Store(false)
	}
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	for _, ch := range r.sigSubs {
		select {
		case ch <- s:
		default: // a lagging subscriber misses it (IsOpen re-syncs)
		}
	}
}

func (r *Runtime) closeSignals() {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	r.sigClosed = true
	for _, ch := range r.sigSubs {
		close(ch)
	}
	r.sigSubs = nil
}

// Close ends any conversation, stops the runtime goroutine and closes the cleaner.
// Safe to call more than once.
func (r *Runtime) Close() {
	r.closeOnce.Do(func() {
		close(r.done)
		r.cancel()
		<-r.exited
		r.closeSignals()
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
		case e := <-r.micFailed:
			if !r.conv.IsActive() {
				if e.Kind != AudioDeviceChanged {
					r.micUnavailable.Store(&e) // fail the next mic start promptly
				}
				continue
			}
			r.emit(UIEvent{Kind: "error", Text: "microphone: " + e.Error()})
			r.input(FailedInput(FailureFromAudio(e)))
		case <-r.ctlNotify:
			r.ctlMu.Lock()
			cs := r.ctlQueue
			r.ctlQueue = nil
			r.ctlMu.Unlock()
			for _, c := range cs {
				r.onControl(c)
			}
		case c := <-r.connected:
			r.onConnected(c)
		case t := <-r.toolDone:
			r.onToolDone(t)
		case ev, ok := <-r.events:
			if !ok {
				r.session, r.events = nil, nil
				r.input(FailedInput(Failure{Kind: FailConnectionLost}))
				continue
			}
			switch {
			case ev.Kind == EvTranscript:
				if debugEnabled() {
					log.Printf("[voice] %s: %s", ev.Role, ev.Text)
				}
				r.emit(UIEvent{Kind: "transcript", Role: ev.Role, Text: ev.Text})
			case ev.Kind == EvError && !ev.Fatal:
				log.Printf("voice: model warning: %s", ev.Message)
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

func debugEnabled() bool {
	_, ok := os.LookupEnv("VOICE_DEBUG")
	return ok
}

func (r *Runtime) onControl(c control) {
	switch c.kind {
	case ctlInject:
		r.input(InjectInput(c.notice))
	case ctlHoldOpen:
		r.input(HoldOpenInput(c.on))
	case ctlMouthLevels:
		r.cfg.MouthLevels = c.levels
		r.meter = NewMouthMeter(c.levels)
	case ctlOpen:
		r.input(OpenInput())
	}
}

func (r *Runtime) onToolDone(t toolResult) {
	if t.gen != r.gen {
		return // a result from a previous conversation
	}
	out := t.outcome.Output
	if len(out) == 0 {
		out = json.RawMessage("null")
	}
	r.send(Command{Kind: CmdToolResult, CallID: t.callID, Output: out, Respond: !t.outcome.EndConversation})
	if t.outcome.EndAfterReply && !t.outcome.EndConversation {
		r.input(ToolDoneAfterReplyInput(t.callID))
		return
	}
	r.input(ToolDoneInput(t.callID, t.outcome.EndConversation))
}

func (r *Runtime) shutdown() {
	r.apply(Effect{Kind: EfStopMic})
	if r.lastTake != nil {
		select { // let the last recording finish its files
		case <-r.lastTake:
		case <-time.After(time.Second):
		}
	}
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
	r.resting.Store(r.conv.Phase() == PhaseIdle)
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
	if r.take != nil {
		if _, plain := r.cleaner.(audio.Passthrough); !plain {
			r.take.Unfiltered(f.samples)
		}
	}
	out := r.cleaner.Process(f.samples)
	if len(out) == 0 {
		return
	}
	if r.take != nil {
		r.take.Sent(out)
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
		f := Failure{Kind: FailConnectionLost}
		var fe FailureError
		if errors.As(c.err, &fe) {
			f = fe.Failure()
		}
		r.input(FailedInput(f))
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
		key, ok := r.secrets.APIKey(r.model.Name())
		if !ok {
			hint := r.secrets.KeyHint(r.model.Name())
			if hint != "" {
				r.emit(UIEvent{Kind: "error", Text: hint + " not set"})
			} else {
				r.emit(UIEvent{Kind: "error", Text: "API key not set"})
			}
			r.pending = append(r.pending, FailedInput(NoAPIKey(hint)))
			return
		}
		ctx, cancel := context.WithCancel(r.ctx)
		r.connectCancel = cancel
		gen, model, cfg := r.gen, r.model, r.cfg.Session
		if r.cfg.Facts != nil {
			cfg.Instructions = withFacts(cfg.Instructions, r.cfg.Facts.Facts())
		}
		go func() {
			s, err := model.Connect(ctx, cfg, key.Expose())
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
		if r.recorder != nil && r.take == nil {
			r.take = r.recorder.Begin()
		}
		r.emit(UIEvent{Kind: "mic", Text: "start"})
		if e := r.micUnavailable.Swap(nil); e != nil {
			r.emit(UIEvent{Kind: "error", Text: "microphone: " + e.Error()})
			r.pending = append(r.pending, FailedInput(FailureFromAudio(*e)))
		}
	case EfStopMic:
		if r.take != nil {
			r.lastTake = r.take.End()
			r.take = nil
		}
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
		if r.cfg.Chimes {
			r.pb.enqueue(ChimeSamples(fx.Chime))
		}
	case EfInvokeTool:
		r.invokeTool(fx.CallID, fx.ToolName, fx.Args)
	case EfMascot:
		r.emit(UIEvent{Kind: "state", State: string(fx.State)})
	case EfStatus:
		r.emit(UIEvent{Kind: "status", Text: fx.Status})
	case EfInject:
		r.send(Command{Kind: CmdInject, Text: fx.Text, Respond: true})
	case EfSignal:
		r.publish(fx.Signal)
	}
}

// invokeTool runs the tool on its own goroutine; the result comes back through toolDone,
// tagged with the current generation so a result from an earlier conversation is dropped.
func (r *Runtime) invokeTool(callID, name string, args json.RawMessage) {
	gen := r.gen
	deliver := func(o ToolOutcome) {
		select {
		case r.toolDone <- toolResult{gen: gen, callID: callID, outcome: o}:
		case <-r.done:
		}
	}
	tool, ok := r.tools.Get(name)
	if !ok {
		out, _ := json.Marshal(map[string]string{"error": "unknown tool " + name})
		go deliver(ToolOutcome{Output: out})
		return
	}
	ctx := r.ctx
	go func() { deliver(tool.Invoke(ctx, args)) }()
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
