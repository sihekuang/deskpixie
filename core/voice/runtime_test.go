package voice

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

// fakeSession records commands and lets the test script events.
type fakeSession struct {
	events chan Event

	mu     sync.Mutex
	sent   []Command
	closed bool
	// block, when non-nil, makes Send(CmdAudio) wait until it is closed.
	block chan struct{}
}

func newFakeSession() *fakeSession { return &fakeSession{events: make(chan Event, 64)} }

func (s *fakeSession) Send(c Command) error {
	s.mu.Lock()
	block := s.block
	s.mu.Unlock()
	if block != nil && c.Kind == CmdAudio {
		<-block
	}
	s.mu.Lock()
	s.sent = append(s.sent, c)
	s.mu.Unlock()
	return nil
}
func (s *fakeSession) Events() <-chan Event { return s.events }
func (s *fakeSession) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}
func (s *fakeSession) commands(kind CommandKind) []Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Command
	for _, c := range s.sent {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	return out
}

type connectCall struct {
	cfg SessionConfig
	key string
}

type fakeModel struct {
	sess *fakeSession

	mu    sync.Mutex
	calls []connectCall
}

func (m *fakeModel) Name() string { return "fake" }
func (m *fakeModel) Connect(_ context.Context, cfg SessionConfig, key string) (Session, error) {
	m.mu.Lock()
	m.calls = append(m.calls, connectCall{cfg, key})
	m.mu.Unlock()
	return m.sess, nil
}
func (m *fakeModel) connects() []connectCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.calls)
}

// uiLog collects emitted UI events.
type uiLog struct {
	mu  sync.Mutex
	evs []UIEvent
}

func (l *uiLog) emit(e UIEvent) {
	l.mu.Lock()
	l.evs = append(l.evs, e)
	l.mu.Unlock()
}
func (l *uiLog) all() []UIEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.evs)
}
func (l *uiLog) states() []string {
	var out []string
	for _, e := range l.all() {
		if e.Kind == "state" {
			out = append(out, e.State)
		}
	}
	return out
}
func (l *uiLog) has(want UIEvent) bool { return slices.Contains(l.all(), want) }

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

var testSessionCfg = SessionConfig{Model: "gpt-realtime", Voice: "marin", Instructions: "be Pixie"}

func newTestRuntime(t *testing.T, key string) (*Runtime, *fakeModel, *uiLog) {
	t.Helper()
	m := &fakeModel{sess: newFakeSession()}
	ui := &uiLog{}
	r := NewRuntime(m, audio.Passthrough{}, RuntimeConfig{
		Session:      testSessionCfg,
		APIKey:       key,
		MouthLevels:  4,
		Conversation: DefaultConversationConfig(),
	}, ui.emit)
	t.Cleanup(r.Close)
	return r, m, ui
}

// startListening presses the hotkey and has the fake session report Ready.
func startListening(t *testing.T, r *Runtime, m *fakeModel, ui *uiLog) {
	t.Helper()
	r.Hotkey()
	eventually(t, "connect", func() bool { return len(m.connects()) == 1 })
	m.sess.events <- Event{Kind: EvReady}
	eventually(t, "mic start", func() bool { return ui.has(UIEvent{Kind: "mic", Text: "start"}) })
}

func pcmFrame(v int16, n int) []int16 {
	f := make([]int16, n)
	for i := range f {
		f[i] = v
	}
	return f
}

func TestRuntimeHotkeyConnectsAndListens(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)

	calls := m.connects()
	if calls[0].key != "sk-test" || calls[0].cfg.Model != testSessionCfg.Model ||
		calls[0].cfg.Voice != testSessionCfg.Voice || calls[0].cfg.Instructions != testSessionCfg.Instructions {
		t.Fatalf("Connect called with %+v", calls[0])
	}
	eventually(t, "listening state", func() bool { return slices.Contains(ui.states(), "listening") })
	st := ui.states()
	ti, li := slices.Index(st, "thinking"), slices.Index(st, "listening")
	if ti < 0 || li < ti {
		t.Fatalf("states %v: want thinking then listening", st)
	}
}

func TestRuntimeMicOnlySentWhileActive(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	r.Hotkey()
	eventually(t, "connect", func() bool { return len(m.connects()) == 1 })

	before := pcmFrame(1, audio.FrameSamples)
	r.PushMic(before, time.Now())
	// Once the loop has taken the frame off the channel it handles it before anything else.
	eventually(t, "mic frame consumed", func() bool { return len(r.mic) == 0 })

	m.sess.events <- Event{Kind: EvReady}
	eventually(t, "mic start", func() bool { return ui.has(UIEvent{Kind: "mic", Text: "start"}) })

	after := pcmFrame(2, audio.FrameSamples)
	r.PushMic(after, time.Now())
	eventually(t, "audio sent", func() bool { return len(m.sess.commands(CmdAudio)) == 1 })

	got := m.sess.commands(CmdAudio)
	if !slices.Equal(got[0].Audio, after) {
		t.Fatalf("sent audio[0]=%d len %d, want the post-Ready frame", got[0].Audio[0], len(got[0].Audio))
	}
}

func TestRuntimeOnMicSentReportsCaptureTime(t *testing.T) {
	m := &fakeModel{sess: newFakeSession()}
	ui := &uiLog{}
	var mu sync.Mutex
	var stamps []time.Time
	r := NewRuntime(m, audio.Passthrough{}, RuntimeConfig{
		Session: testSessionCfg, APIKey: "sk-test", MouthLevels: 4,
		Conversation: DefaultConversationConfig(),
		OnMicSent: func(at time.Time) {
			mu.Lock()
			stamps = append(stamps, at)
			mu.Unlock()
		},
	}, ui.emit)
	t.Cleanup(r.Close)
	startListening(t, r, m, ui)

	captured := time.Now().Add(-5 * time.Millisecond)
	r.PushMic(pcmFrame(3, audio.FrameSamples), captured)
	eventually(t, "hook", func() bool { mu.Lock(); defer mu.Unlock(); return len(stamps) == 1 })
	mu.Lock()
	defer mu.Unlock()
	if !stamps[0].Equal(captured) {
		t.Fatalf("OnMicSent got %v, want %v", stamps[0], captured)
	}
	if len(m.sess.commands(CmdAudio)) != 1 {
		t.Fatal("hook ran before Send returned")
	}
}

func TestRuntimePlaybackQueue(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)

	m.sess.events <- Event{Kind: EvAudioDelta, Audio: pcmFrame(7, 2400)}
	eventually(t, "audio queued", func() bool { return r.queuedSamples() == 2400 })

	buf := make([]int16, 1000)
	for i, want := range []int{1000, 1000, 400, 0} {
		n, flush := r.PullPlayback(buf)
		if n != want || flush {
			t.Fatalf("pull %d: n=%d flush=%v, want n=%d flush=false", i, n, flush, want)
		}
		if n > 0 && buf[0] != 7 {
			t.Fatalf("pull %d: buf[0]=%d, want 7", i, buf[0])
		}
	}
}

func TestRuntimeBargeInFlushesAndTruncates(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)

	m.sess.events <- Event{Kind: EvAudioDelta, Audio: pcmFrame(5, 48000)}
	eventually(t, "audio queued", func() bool { return r.queuedSamples() == 48000 })
	r.ReportPlayback(12000, 4800, 0.2)
	m.sess.events <- Event{Kind: EvSpeechStarted}

	eventually(t, "cancel", func() bool { return len(m.sess.commands(CmdCancelResponse)) == 1 })
	if got := m.sess.commands(CmdCancelResponse)[0].HeardMs; got != 500 {
		t.Fatalf("HeardMs = %d, want 500", got)
	}
	n, flush := r.PullPlayback(make([]int16, 1000))
	if n != 0 || !flush {
		t.Fatalf("PullPlayback = (%d, %v), want (0, true)", n, flush)
	}
	if n, flush := r.PullPlayback(make([]int16, 1000)); n != 0 || flush {
		t.Fatalf("second PullPlayback = (%d, %v), want (0, false)", n, flush)
	}
}

func TestRuntimeNoKeyShowsError(t *testing.T) {
	r, m, ui := newTestRuntime(t, "")
	r.Hotkey()
	eventually(t, "error state", func() bool { return slices.Contains(ui.states(), "error") })
	if !ui.has(UIEvent{Kind: "error", Text: "OPENAI_API_KEY not set"}) {
		t.Fatalf("no error event in %+v", ui.all())
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(m.connects()); n != 0 {
		t.Fatalf("Connect called %d times without a key", n)
	}
}

func TestRuntimePushMicNeverBlocks(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)

	unblock := make(chan struct{})
	m.sess.mu.Lock()
	m.sess.block = unblock
	m.sess.mu.Unlock()
	defer close(unblock)

	f := pcmFrame(1, audio.FrameSamples)
	start := time.Now()
	for range 1000 {
		r.PushMic(f, time.Now())
	}
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("1000 PushMic calls took %v", d)
	}
	if r.MicDrops() == 0 {
		t.Fatal("expected dropped frames to be counted")
	}
}

func TestRuntimeMouthFollowsPlaybackWhileSpeaking(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)

	m.sess.events <- Event{Kind: EvAudioDelta, Audio: pcmFrame(5, 48000)}
	eventually(t, "audio queued", func() bool { return r.queuedSamples() == 48000 })
	r.ReportPlayback(0, 4800, 0.3)
	eventually(t, "mouth open", func() bool {
		for _, e := range ui.all() {
			if e.Kind == "mouth" && e.Level > 0 {
				return true
			}
		}
		return false
	})
	// Barge-in leaves Speaking: the mouth closes.
	m.sess.events <- Event{Kind: EvSpeechStarted}
	eventually(t, "mouth closed", func() bool {
		evs := ui.all()
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Kind == "mouth" {
				return evs[i].Level == 0
			}
		}
		return false
	})
}

func TestRuntimeSessionDropFailsToCooldown(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)
	close(m.sess.events)
	eventually(t, "error state", func() bool { return slices.Contains(ui.states(), "error") })
	if !ui.has(UIEvent{Kind: "status", Text: "Voice: connection lost"}) {
		t.Fatalf("no connection-lost status in %+v", ui.all())
	}
	if !ui.has(UIEvent{Kind: "mic", Text: "stop"}) {
		t.Fatal("mic not stopped")
	}
}

func TestRuntimeMicFailedShowsError(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)
	r.MicFailed("denied")
	eventually(t, "status", func() bool {
		return ui.has(UIEvent{Kind: "status", Text: "Voice: microphone access denied"})
	})
	if !slices.Contains(ui.states(), "error") {
		t.Fatalf("states %v: want error", ui.states())
	}
}

func TestRuntimeTranscriptIsEmitted(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)
	m.sess.events <- Event{Kind: EvTranscript, Role: "user", Text: "hi Pixie"}
	eventually(t, "transcript", func() bool {
		return ui.has(UIEvent{Kind: "transcript", Role: "user", Text: "hi Pixie"})
	})
}

// #251: the user's words so far go to the live caption as they come.
func TestRuntimeHeardWordsAreEmitted(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)
	m.sess.events <- Event{Kind: EvUserHeard, Text: "hi Pix"}
	eventually(t, "heard", func() bool { return ui.has(UIEvent{Kind: "heard", Text: "hi Pix"}) })
}

// #251 review: while Pixie is audibly speaking, and EchoTail after, heard words may be Pixie's
// own voice coming back through the mic: not shown.
func TestHeardWordsAreEchoWhilePixieSpeaksAndJustAfter(t *testing.T) {
	now := time.Now()
	r := &Runtime{}
	if r.echoing(now) {
		t.Fatal("quiet")
	}
	r.mouth = 2
	if !r.echoing(now) {
		t.Fatal("speaking")
	}
	r.mouth, r.mouthEnded = 0, now
	if !r.echoing(now.Add(EchoTail / 2)) {
		t.Fatal("just after")
	}
	if r.echoing(now.Add(EchoTail)) {
		t.Fatal("long after")
	}
}

// A turn you started while Pixie was quiet stays yours: OpenAI Realtime transcribes it only once
// you stop, when Pixie may already be answering. It shows until that reply ends (live check).
func TestHeardWordsOfATurnStartedInQuietShowWhilePixieAnswers(t *testing.T) {
	now := time.Now()
	r := &Runtime{}
	r.userStarted(now)
	r.mouth = 2 // Pixie answers before the words come
	if !r.admitsHeard(now) {
		t.Fatal("your turn's words")
	}
	r.mouthClosed(now)
	if r.admitsHeard(now.Add(EchoTail / 2)) {
		t.Fatal("after the reply: echo again")
	}
	r.mouth = 2
	r.userStarted(now) // started while Pixie speaks: may be echo
	if r.admitsHeard(now) {
		t.Fatal("a turn started during Pixie's speech")
	}
}

func TestRuntimeHotkeyAgainEndsAndClosesSession(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	startListening(t, r, m, ui)
	r.Hotkey()
	eventually(t, "session closed", func() bool {
		m.sess.mu.Lock()
		defer m.sess.mu.Unlock()
		return m.sess.closed
	})
	if !ui.has(UIEvent{Kind: "mic", Text: "stop"}) {
		t.Fatal("mic not stopped")
	}
	eventually(t, "idle", func() bool { st := ui.states(); return st[len(st)-1] == "idle" })
}

// A mic failure reported while idle (e.g. the app could not start audio) makes the next
// conversation fail as soon as it asks for the mic, instead of waiting MicPermissionWait.
func TestRuntimeMicFailedWhileIdleFailsNextMicStartPromptly(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	r.MicFailed("Microphone access denied")
	time.Sleep(20 * time.Millisecond) // let the runtime record it while idle
	if slices.Contains(ui.states(), "error") {
		t.Fatalf("idle mic failure must not show error by itself: %v", ui.states())
	}
	start := time.Now()
	startListening(t, r, m, ui)
	eventually(t, "error state", func() bool { return slices.Contains(ui.states(), "error") })
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %v, want prompt failure", d)
	}
	if !ui.has(UIEvent{Kind: "status", Text: "Voice: microphone access denied"}) {
		t.Fatalf("no mic-denied status in %+v", ui.all())
	}
}

// The remembered failure is one-shot: after it has failed one conversation, the next one
// asks for the mic normally.
func TestRuntimeIdleMicFailureIsConsumedOnce(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	r.MicFailed("denied")
	time.Sleep(20 * time.Millisecond)
	startListening(t, r, m, ui)
	eventually(t, "error state", func() bool { return slices.Contains(ui.states(), "error") })
	if r.micUnavailableForTest() != "" {
		t.Fatal("failure not consumed")
	}
}

// Mic frames arriving prove the mic works and clear a remembered failure.
func TestRuntimeMicFramesClearIdleMicFailure(t *testing.T) {
	r, _, _ := newTestRuntime(t, "sk-test")
	r.MicFailed("denied")
	eventually(t, "recorded", func() bool { return r.micUnavailableForTest() != "" })
	r.PushMic(pcmFrame(1, 960), time.Now())
	eventually(t, "cleared", func() bool { return r.micUnavailableForTest() == "" })
}

// micUnavailableForTest returns the remembered idle mic failure ("" when none).
func (r *Runtime) micUnavailableForTest() string {
	if p := r.micUnavailable.Load(); p != nil {
		return p.Msg
	}
	return ""
}
