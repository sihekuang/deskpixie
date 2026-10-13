// Package voicetest holds test doubles for the voice loop and the voice.Model conformance
// suite, for use from any package's tests. Port of ai-mascot crates/voice-core/src/fakes.rs
// and conformance.rs. The fakes pace themselves with time.Sleep, so tests run them inside a
// testing/synctest bubble to get tokio-style paused time.
package voicetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

// FrameMs: fakes exchange audio in 100 ms frames.
const FrameMs = 100

func samples(ms int) int { return audio.SampleRate * ms / 1000 }

// Tone is a 440 Hz tone of ms milliseconds at amplitude amp (0..1).
func Tone(ms int, amp float32) []int16 {
	out := make([]int16, samples(ms))
	for i := range out {
		t := float64(i) / audio.SampleRate
		out[i] = int16(math.Sin(2*math.Pi*440*t) * float64(amp) * 32767)
	}
	return out
}

// Silence is ms milliseconds of zeros.
func Silence(ms int) []int16 { return make([]int16, samples(ms)) }

// ---------- voice model ----------

// ReplyKind says what a scripted reply contains.
type ReplyKind int

const (
	ReplyAudio ReplyKind = iota
	ReplyTool
	ReplyAudioThenTool
	ReplySilent
)

// Reply is what the scripted server does after each user turn (and after a Respond tool
// result or inject).
type Reply struct {
	Kind   ReplyKind
	Frames int    // 100 ms audio frames, real-time paced
	Tool   string // tool name to call
}

func Audio(frames int) Reply { return Reply{Kind: ReplyAudio, Frames: frames} }
func Tool(name string) Reply { return Reply{Kind: ReplyTool, Tool: name} }
func AudioThenTool(frames int, name string) Reply {
	return Reply{Kind: ReplyAudioThenTool, Frames: frames, Tool: name}
}
func Silent() Reply { return Reply{Kind: ReplySilent} }

type server struct {
	mu           sync.Mutex
	script       []Reply
	commands     []voice.Command
	connects     int
	calls        int
	kill         func()
	instructions []string
}

// ScriptedModel is a fake speech-to-speech server that behaves like OpenAI Realtime with
// semantic_vad and interrupt_response: frame loudness is "speech", 5 quiet frames end a
// turn, and speech during a reply cancels it.
type ScriptedModel struct {
	srv          *server
	connectDelay time.Duration
	failConnect  bool
	name         string
}

// NewScriptedModel serves replies in order; Silent once they run out.
func NewScriptedModel(replies ...Reply) *ScriptedModel {
	return &ScriptedModel{srv: &server{script: replies}, name: "scripted"}
}

// FailingModel cannot connect.
func FailingModel() *ScriptedModel {
	m := NewScriptedModel()
	m.failConnect = true
	return m
}

// WithConnectDelay makes Connect take d.
func (m *ScriptedModel) WithConnectDelay(d time.Duration) *ScriptedModel {
	m.connectDelay = d
	return m
}

// WithName changes Name() (it decides which API key the runtime looks up).
func (m *ScriptedModel) WithName(n string) *ScriptedModel {
	m.name = n
	return m
}

func (m *ScriptedModel) Name() string { return m.name }

// Commands is every command received, in order (including audio and Close).
func (m *ScriptedModel) Commands() []voice.Command {
	m.srv.mu.Lock()
	defer m.srv.mu.Unlock()
	return slices.Clone(m.srv.commands)
}

// NonAudio is Commands without the audio frames.
func (m *ScriptedModel) NonAudio() []voice.Command {
	var out []voice.Command
	for _, c := range m.Commands() {
		if c.Kind != voice.CmdAudio {
			out = append(out, c)
		}
	}
	return out
}

// Instructions each connect was given, in order.
func (m *ScriptedModel) Instructions() []string {
	m.srv.mu.Lock()
	defer m.srv.mu.Unlock()
	return slices.Clone(m.srv.instructions)
}

// Connects counts successful connects.
func (m *ScriptedModel) Connects() int {
	m.srv.mu.Lock()
	defer m.srv.mu.Unlock()
	return m.srv.connects
}

// HangUp drops the current session (like a network failure or session expiry).
func (m *ScriptedModel) HangUp() {
	m.srv.mu.Lock()
	kill := m.srv.kill
	m.srv.kill = nil
	m.srv.mu.Unlock()
	if kill != nil {
		kill()
	}
}

// Connect ignores ctx, like the Rust fake: a cancelled connect still returns its session,
// which the runtime must close.
func (m *ScriptedModel) Connect(_ context.Context, cfg voice.SessionConfig, _ string) (voice.Session, error) {
	m.srv.mu.Lock()
	m.srv.instructions = append(m.srv.instructions, cfg.Instructions)
	m.srv.mu.Unlock()
	time.Sleep(m.connectDelay)
	if m.failConnect {
		return nil, errors.New("connect failed: scripted failure")
	}
	ctx, kill := context.WithCancel(context.Background())
	s := &scriptedSession{
		srv:      m.srv,
		cmds:     make(chan voice.Command, 4096),
		events:   make(chan voice.Event, 4096),
		ctx:      ctx,
		kill:     kill,
		finished: make(chan struct{}),
	}
	m.srv.mu.Lock()
	m.srv.connects++
	m.srv.kill = kill
	m.srv.mu.Unlock()
	s.events <- voice.Event{Kind: voice.EvReady}
	go s.serve()
	return s, nil
}

type scriptedSession struct {
	srv       *server
	cmds      chan voice.Command
	events    chan voice.Event
	ctx       context.Context // done once the server stops
	kill      context.CancelFunc
	finished  chan struct{}
	closeOnce sync.Once
}

func (s *scriptedSession) Events() <-chan voice.Event { return s.events }

func (s *scriptedSession) Send(c voice.Command) error {
	select {
	case <-s.ctx.Done():
		return errors.New("session closed")
	default:
	}
	select {
	case s.cmds <- c:
		return nil
	case <-s.ctx.Done():
		return errors.New("session closed")
	}
}

// Close is VoiceCommand::Close: recorded, then the server stops and the events end.
func (s *scriptedSession) Close() error {
	s.closeOnce.Do(func() {
		select {
		case s.cmds <- voice.Command{Kind: voice.CmdClose}:
		case <-s.ctx.Done():
		}
	})
	<-s.finished
	return nil
}

// reply is one running response.
type reply struct {
	mu        sync.Mutex
	cancelled bool
	finished  bool          // sent its ResponseDone (or was cancelled)
	done      chan struct{} // the goroutine has exited
	stop      context.CancelFunc
}

func (s *scriptedSession) serve() {
	defer close(s.finished)
	defer close(s.events)
	var cur *reply
	defer func() {
		if cur != nil {
			cur.abort()
			<-cur.done
		}
	}()
	defer s.kill()
	talking, quiet := false, 0
	for {
		var c voice.Command
		select {
		case <-s.ctx.Done():
			return
		case c = <-s.cmds:
		}
		s.srv.mu.Lock()
		s.srv.commands = append(s.srv.commands, c)
		s.srv.mu.Unlock()
		switch c.Kind {
		case voice.CmdAudio:
			if voice.RMS(c.Audio) > 0.05 {
				quiet = 0
				if !talking {
					talking = true
					s.emit(voice.Event{Kind: voice.EvSpeechStarted})
					s.cancel(&cur) // interrupt_response
				}
			} else if talking {
				quiet++
				if quiet >= 5 {
					talking, quiet = false, 0
					s.emit(voice.Event{Kind: voice.EvSpeechStopped})
					s.startReply(&cur)
				}
			}
		case voice.CmdCancelResponse:
			s.cancel(&cur)
		case voice.CmdToolResult, voice.CmdInject:
			if c.Respond {
				s.startReply(&cur)
			}
		case voice.CmdClose:
			return
		}
	}
}

func (s *scriptedSession) emit(e voice.Event) {
	select {
	case s.events <- e:
	case <-s.ctx.Done():
	}
}

func (r *reply) abort() (wasRunning bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished || r.cancelled {
		return false
	}
	r.cancelled = true
	r.stop()
	return true
}

func (s *scriptedSession) cancel(cur **reply) {
	if *cur == nil {
		return
	}
	r := *cur
	*cur = nil
	if r.abort() {
		s.emit(voice.Event{Kind: voice.EvResponseDone})
	}
}

func (s *scriptedSession) startReply(cur **reply) {
	s.srv.mu.Lock()
	s.srv.calls++
	rp := Silent()
	if len(s.srv.script) > 0 {
		rp, s.srv.script = s.srv.script[0], s.srv.script[1:]
	}
	callID := fmt.Sprintf("call_%d", s.srv.calls)
	s.srv.mu.Unlock()
	s.emit(voice.Event{Kind: voice.EvResponseCreated})
	frames, tool := 0, ""
	switch rp.Kind {
	case ReplyAudio:
		frames = rp.Frames
	case ReplyTool:
		tool = rp.Tool
	case ReplyAudioThenTool:
		frames, tool = rp.Frames, rp.Tool
	}
	ctx, stop := context.WithCancel(s.ctx)
	r := &reply{done: make(chan struct{}), stop: stop}
	*cur = r
	// send emits unless the reply was cancelled; under r.mu so nothing follows a cancel.
	send := func(e voice.Event) bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.cancelled {
			return false
		}
		s.emit(e)
		if e.Kind == voice.EvResponseDone {
			r.finished = true
		}
		return true
	}
	go func() {
		defer close(r.done)
		defer stop()
		for range frames {
			if !send(voice.Event{Kind: voice.EvAudioDelta, Audio: Tone(FrameMs, 0.3)}) {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(FrameMs * time.Millisecond):
			}
		}
		if tool != "" {
			if !send(voice.Event{Kind: voice.EvToolCall, CallID: callID, ToolName: tool, Args: json.RawMessage(`{}`)}) {
				return
			}
		}
		send(voice.Event{Kind: voice.EvResponseDone})
	}()
}
