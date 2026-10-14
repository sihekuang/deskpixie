package voicetest

import (
	"slices"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/voice"
)

// ---------- UI sink ----------

// Sink records the runtime's UI events (pass Sink.Emit as NewRuntime's emit).
type Sink struct {
	mu     sync.Mutex
	events []voice.UIEvent
	onMic  func(start bool)
}

// Emit records e.
func (s *Sink) Emit(e voice.UIEvent) {
	s.mu.Lock()
	s.events = append(s.events, e)
	onMic := s.onMic
	s.mu.Unlock()
	if e.Kind == "mic" && onMic != nil {
		onMic(e.Text == "start")
	}
}

// Events is everything emitted so far.
func (s *Sink) Events() []voice.UIEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events)
}

// States are the pixie states, in order.
func (s *Sink) States() []voice.PixieState {
	var out []voice.PixieState
	for _, e := range s.Events() {
		if e.Kind == "state" {
			out = append(out, voice.PixieState(e.State))
		}
	}
	return out
}

// Mouths are the mouth levels, in order.
func (s *Sink) Mouths() []int {
	var out []int
	for _, e := range s.Events() {
		if e.Kind == "mouth" {
			out = append(out, e.Level)
		}
	}
	return out
}

// Statuses are the status lines, in order ("" = cleared).
func (s *Sink) Statuses() []string {
	var out []string
	for _, e := range s.Events() {
		if e.Kind == "status" {
			out = append(out, e.Text)
		}
	}
	return out
}

// ---------- mic ----------

// Mic is what a fake mic drives: the runtime's mic entry points.
type Mic interface {
	PushMic(frame []int16, capturedAt time.Time)
	AudioFailed(voice.AudioError)
}

// MicMode is how a FakeMic answers the runtime's "mic start".
type MicMode int

const (
	// MicLiveAtOnce: the mic is on as soon as it starts.
	MicLiveAtOnce MicMode = iota
	// MicAwaitingPermission: the user is being asked; frames flow after Grant.
	MicAwaitingPermission
	// MicFailing: starting fails with FakeMic.FailWith.
	MicFailing
)

// FakeMic stands in for Swift's capture: while the runtime has the mic on it pushes a
// 100 ms frame of silence every 100 ms (a real mic never stops sending), and Say pushes
// loud frames instead. Attach it with Sink.
type FakeMic struct {
	Mode     MicMode
	FailWith voice.AudioError

	mu      sync.Mutex
	rt      Mic
	open    bool
	granted bool
	saying  bool
	stop    chan struct{}
	stopped chan struct{}
}

// NewFakeMic makes a mic in mode; attach it before the first hotkey.
func NewFakeMic(mode MicMode) *FakeMic { return &FakeMic{Mode: mode} }

// Attach connects the mic to the runtime and the sink, and starts its frame loop. Call
// Stop before the test ends.
func (m *FakeMic) Attach(rt Mic, sink *Sink) {
	m.mu.Lock()
	m.rt = rt
	m.stop, m.stopped = make(chan struct{}), make(chan struct{})
	m.mu.Unlock()
	sink.mu.Lock()
	sink.onMic = m.onMic
	sink.mu.Unlock()
	go m.loop()
}

// Stop ends the frame loop.
func (m *FakeMic) Stop() {
	close(m.stop)
	<-m.stopped
}

func (m *FakeMic) onMic(start bool) {
	m.mu.Lock()
	m.open = start
	m.granted = start && m.Mode == MicLiveAtOnce
	fail := start && m.Mode == MicFailing
	rt, e := m.rt, m.FailWith
	m.mu.Unlock()
	if fail {
		rt.AudioFailed(e)
	}
	// Like a real capture's first callback: the mic is on now (PushMic never blocks).
	m.push(Silence(FrameMs))
}

// IsOpen: the runtime has the mic on.
func (m *FakeMic) IsOpen() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.open
}

// Grant: the user allows mic access (MicAwaitingPermission).
func (m *FakeMic) Grant() {
	m.mu.Lock()
	m.granted = m.open
	m.mu.Unlock()
	m.push(Silence(FrameMs))
}

// Fail reports e from the running mic.
func (m *FakeMic) Fail(e voice.AudioError) {
	m.mu.Lock()
	rt, open := m.rt, m.open
	m.mu.Unlock()
	if open {
		rt.AudioFailed(e)
	}
}

func (m *FakeMic) live() (Mic, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rt, m.open && m.granted
}

// push delivers a frame if the mic is on; it reports whether it did.
func (m *FakeMic) push(f []int16) bool {
	rt, ok := m.live()
	if ok {
		rt.PushMic(f, time.Now())
	}
	return ok
}

func (m *FakeMic) loop() {
	defer close(m.stopped)
	t := time.NewTicker(FrameMs * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
		m.mu.Lock()
		saying := m.saying
		m.mu.Unlock()
		if !saying {
			m.push(Silence(FrameMs))
		}
	}
}

// Say "speaks" for ms (loud frames, real-time paced), then 600 ms of silence.
func (m *FakeMic) Say(ms int) {
	m.mu.Lock()
	m.saying = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.saying = false
		m.mu.Unlock()
	}()
	for range ms / FrameMs {
		m.push(Tone(FrameMs, 0.3))
		time.Sleep(FrameMs * time.Millisecond)
	}
	for range 6 {
		m.push(Silence(FrameMs))
		time.Sleep(FrameMs * time.Millisecond)
	}
}

// ---------- playback ----------

// Playback is the runtime's playback side (what Swift's audio engine calls).
type Playback interface {
	PullPlayback(dst []int16) (n int, flush bool)
	ReportPlayback(playedSamples int64, bufferedSamples int, rms float32)
}

// Player stands in for Swift's speaker: every 10 ms it renders 10 ms of what it holds, then
// tops its ring up to 20 ms from the runtime and reports what it played.
type Player struct {
	pb      Playback
	stop    chan struct{}
	stopped chan struct{}
}

const (
	playerStep = 10 * time.Millisecond
	playerHop  = 240 // samples per step
	playerRing = 480
)

// StartPlayer starts pulling from pb. Call Stop before the test ends.
func StartPlayer(pb Playback) *Player {
	p := &Player{pb: pb, stop: make(chan struct{}), stopped: make(chan struct{})}
	go p.loop()
	return p
}

// Stop ends the player.
func (p *Player) Stop() {
	close(p.stop)
	<-p.stopped
}

func (p *Player) loop() {
	defer close(p.stopped)
	var ring []int16
	var played int64
	buf := make([]int16, playerRing)
	t := time.NewTicker(playerStep)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
		}
		n := min(playerHop, len(ring))
		rms := voice.RMS(ring[:n])
		ring = ring[n:]
		played += int64(n)
		for len(ring) < playerRing {
			got, flush := p.pb.PullPlayback(buf[:playerRing-len(ring)])
			if flush {
				ring = nil
				continue
			}
			if got == 0 {
				break
			}
			ring = append(ring, buf[:got]...)
		}
		p.pb.ReportPlayback(played, len(ring), rms)
	}
}
