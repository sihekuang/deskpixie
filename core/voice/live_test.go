//go:build live

package voice_test

// Opt-in live smokes against the real OpenAI Realtime and Gemini Live services (V-019/V-020
// "needs live verification"). Run with:
//
//	go test -tags live -run Live -v ./voice/
//
// Each liveProvider runs only when voice.EnvSecrets finds its key through the app's DotenvPaths
// (the environment, ./.env, then ~/Library/Application Support/com.sihekuang.deskpixie/.env);
// otherwise it skips. The key is never printed. No microphone or speaker is used: the
// question is synthesized with `say` + `afconvert` and fed as 40 ms mic frames in real time,
// and a fake speaker drains the playback queue in real time. Each session is kept under 30 s.

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/tools"
	"github.com/sihekuang/deskpixie/core/voice"
	"github.com/sihekuang/deskpixie/core/voice/gemini"
	"github.com/sihekuang/deskpixie/core/voice/openai"
	"github.com/sihekuang/deskpixie/core/voice/openailive"
)

const appSupportID = "com.sihekuang.deskpixie"

func liveSecrets(t *testing.T) *voice.EnvSecrets {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	return voice.NewEnvSecrets(voice.DotenvPaths(filepath.Join(home, "Library", "Application Support", appSupportID)))
}

// speech synthesizes text as 24 kHz mono PCM16 with say + afconvert (skips without them).
func liveSpeech(t *testing.T, text string) []int16 {
	t.Helper()
	for _, tool := range []string{"say", "afconvert"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	dir := t.TempDir()
	aiff, wav := filepath.Join(dir, "q.aiff"), filepath.Join(dir, "q.wav")
	if out, err := exec.Command("say", "-o", aiff, text).CombinedOutput(); err != nil {
		t.Skipf("say failed: %v %s", err, out)
	}
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@24000", "-c", "1", aiff, wav).CombinedOutput(); err != nil {
		t.Fatalf("afconvert: %v %s", err, out)
	}
	b, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	pcm, err := liveParseWAV(b)
	if err != nil {
		t.Fatal(err)
	}
	return pcm
}

// liveParseWAV reads a PCM16 mono 24 kHz WAVE, skipping any extra chunks (afconvert adds FLLR).
func liveParseWAV(b []byte) ([]int16, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, fmt.Errorf("not a WAVE file")
	}
	fmtOK := false
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:off+8]))
		body := off + 8
		if body+size > len(b) {
			size = len(b) - body
		}
		switch id {
		case "fmt ":
			f := b[body : body+size]
			if binary.LittleEndian.Uint16(f[0:2]) != 1 || binary.LittleEndian.Uint16(f[2:4]) != 1 ||
				binary.LittleEndian.Uint32(f[4:8]) != audio.SampleRate || binary.LittleEndian.Uint16(f[14:16]) != 16 {
				return nil, fmt.Errorf("want PCM16 mono 24 kHz, got fmt %x", f[:16])
			}
			fmtOK = true
		case "data":
			if !fmtOK {
				return nil, fmt.Errorf("data before fmt")
			}
			pcm, _ := audio.DecodeLE(b[body : body+size])
			return pcm, nil
		}
		off = body + size + size%2
	}
	return nil, fmt.Errorf("no data chunk")
}

// --- liveSpy model: records what the adapter reports and what the runtime sends ---

type liveSpy struct {
	mu        sync.Mutex
	events    map[voice.EventKind]int
	said      []string // assistant transcripts
	heard     []string // user transcripts
	tools     []string
	cancels   []int64 // HeardMs of each CmdCancelResponse
	errs      []string
	firstAuth time.Time // first audio delta
}

func newLiveSpy() *liveSpy { return &liveSpy{events: map[voice.EventKind]int{}} }

func (s *liveSpy) count(k voice.EventKind) int { s.mu.Lock(); defer s.mu.Unlock(); return s.events[k] }

func (s *liveSpy) assistant() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.said, " ")
}

func (s *liveSpy) cancelled() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.cancels...)
}

func (s *liveSpy) summary() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("ready=%d speechStarted=%d speechStopped=%d audioDeltas=%d responses=%d tools=%v cancels=%v errors=%q user=%q assistant=%q",
		s.events[voice.EvReady], s.events[voice.EvSpeechStarted], s.events[voice.EvSpeechStopped], s.events[voice.EvAudioDelta],
		s.events[voice.EvResponseDone], s.tools, s.cancels, s.errs, s.heard, s.said)
}

type liveSpyModel struct {
	voice.Model
	liveSpy *liveSpy
}

func (m liveSpyModel) Connect(ctx context.Context, cfg voice.SessionConfig, key string) (voice.Session, error) {
	sess, err := m.Model.Connect(ctx, cfg, key)
	if err != nil {
		return nil, err
	}
	out := make(chan voice.Event, 512)
	go func() {
		defer close(out)
		for ev := range sess.Events() {
			m.liveSpy.mu.Lock()
			m.liveSpy.events[ev.Kind]++
			switch ev.Kind {
			case voice.EvTranscript:
				if ev.Role == "assistant" {
					m.liveSpy.said = append(m.liveSpy.said, ev.Text)
				} else {
					m.liveSpy.heard = append(m.liveSpy.heard, ev.Text)
				}
			case voice.EvToolCall:
				m.liveSpy.tools = append(m.liveSpy.tools, ev.ToolName+string(ev.Args))
			case voice.EvError:
				m.liveSpy.errs = append(m.liveSpy.errs, ev.Message)
			case voice.EvAudioDelta:
				if m.liveSpy.firstAuth.IsZero() {
					m.liveSpy.firstAuth = time.Now()
				}
			}
			m.liveSpy.mu.Unlock()
			out <- ev
		}
	}()
	return liveSpySession{Session: sess, liveSpy: m.liveSpy, events: out}, nil
}

type liveSpySession struct {
	voice.Session
	liveSpy *liveSpy
	events  chan voice.Event
}

func (s liveSpySession) Events() <-chan voice.Event { return s.events }

func (s liveSpySession) Send(c voice.Command) error {
	if c.Kind == voice.CmdCancelResponse {
		s.liveSpy.mu.Lock()
		s.liveSpy.cancels = append(s.liveSpy.cancels, c.HeardMs)
		s.liveSpy.mu.Unlock()
	}
	return s.Session.Send(c)
}

// --- wire proxy (OpenAI): records the client message types actually sent ---

type liveWireLog struct {
	mu    sync.Mutex
	types []string
}

func (w *liveWireLog) has(typ string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, t := range w.types {
		if t == typ {
			return true
		}
	}
	return false
}

// openaiProxy forwards the adapter's websocket to OpenAI unchanged (the Authorization header
// passes through in memory, never logged) and records each client message's "type".
func openaiProxy(t *testing.T, wl *liveWireLog) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up, _, err := websocket.Dial(r.Context(), "wss://api.openai.com/v1/realtime?"+r.URL.RawQuery,
			&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {r.Header.Get("Authorization")}}})
		if err != nil {
			http.Error(w, "upstream dial failed", http.StatusBadGateway)
			return
		}
		defer up.CloseNow()
		up.SetReadLimit(16 << 20)
		down, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer down.CloseNow()
		down.SetReadLimit(16 << 20)
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			defer cancel()
			for {
				typ, b, err := up.Read(ctx)
				if err != nil {
					return
				}
				if down.Write(ctx, typ, b) != nil {
					return
				}
			}
		}()
		for {
			typ, b, err := down.Read(ctx)
			if err != nil {
				return
			}
			if i := strings.Index(string(b), `"type":"`); i >= 0 {
				rest := string(b[i+8:])
				if j := strings.IndexByte(rest, '"'); j > 0 {
					wl.mu.Lock()
					wl.types = append(wl.types, rest[:j])
					wl.mu.Unlock()
				}
			}
			if up.Write(ctx, typ, b) != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/realtime"
}

// --- the liveRig: a real Runtime with a fake mic and a real-time fake speaker ---

type liveRig struct {
	t       *testing.T
	rt      *voice.Runtime
	liveSpy *liveSpy
	mu      sync.Mutex
	mic     []int16 // samples still to send; silence when empty
	played  int64   // samples the fake speaker has played
	flushes int
	stop    chan struct{}
	done    chan struct{}
}

func newLiveRig(t *testing.T, m voice.Model, cfg voice.SessionConfig, secrets voice.SecretSource) *liveRig {
	reg := voice.DefaultTools()
	reg.Register(app.AdaptTool(tools.Calculate{}))
	return newLiveRigWith(t, m, cfg, secrets, reg)
}

// newLiveRigWith is newLiveRig with the voice loop's tools given.
func newLiveRigWith(t *testing.T, m voice.Model, cfg voice.SessionConfig, secrets voice.SecretSource, reg *voice.ToolRegistry) *liveRig {
	sp := newLiveSpy()
	r := &liveRig{t: t, liveSpy: sp, stop: make(chan struct{}), done: make(chan struct{})}
	r.rt = voice.NewRuntime(liveSpyModel{Model: m, liveSpy: sp}, nil, voice.RuntimeConfig{
		Session: cfg, Secrets: secrets, Tools: reg, MouthLevels: 4,
		Conversation: voice.DefaultConversationConfig(),
	}, func(e voice.UIEvent) {
		if e.Kind == "state" || e.Kind == "status" || e.Kind == "error" {
			t.Logf("ui %s %s%s", e.Kind, e.State, e.Text)
		}
	})
	go r.loop()
	t.Cleanup(func() {
		close(r.stop)
		<-r.done
		r.rt.Close()
	})
	return r
}

// loop is the audio clock: every 40 ms one mic frame in and up to 40 ms of playback out.
func (r *liveRig) loop() {
	defer close(r.done)
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	out := make([]int16, audio.FrameSamples)
	for {
		select {
		case <-r.stop:
			return
		case <-tick.C:
		}
		frame := make([]int16, audio.FrameSamples)
		r.mu.Lock()
		n := copy(frame, r.mic)
		r.mic = r.mic[n:]
		r.mu.Unlock()
		r.rt.PushMic(frame, time.Now())
		got, flush := r.rt.PullPlayback(out)
		r.mu.Lock()
		if flush {
			r.flushes++
		}
		r.played += int64(got)
		played := r.played
		r.mu.Unlock()
		var rms float32
		if got > 0 {
			rms = voice.RMS(out[:got])
		}
		r.rt.ReportPlayback(played, 0, rms)
	}
}

func (r *liveRig) say(pcm []int16) {
	r.mu.Lock()
	r.mic = append(r.mic, pcm...)
	r.mu.Unlock()
}

func (r *liveRig) playedMs() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.played * 1000 / audio.SampleRate
}
func (r *liveRig) flushed() int { r.mu.Lock(); defer r.mu.Unlock(); return r.flushes }

func (r *liveRig) waitFor(what string, within time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Errorf("timed out waiting for %s; %s", what, r.liveSpy.summary())
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
	return true
}

type liveProvider struct {
	name  string
	model func(wl *liveWireLog, t *testing.T) voice.Model
	cfg   voice.SessionConfig
}

func liveProviders() []liveProvider {
	return []liveProvider{
		{
			name:  openai.Name,
			model: func(wl *liveWireLog, t *testing.T) voice.Model { return openai.New(openaiProxy(t, wl)) },
			cfg:   voice.SessionConfig{Model: "gpt-realtime", Voice: "marin", TurnDetection: voice.TurnSemantic},
		},
		{
			name:  gemini.Name,
			model: func(_ *liveWireLog, _ *testing.T) voice.Model { return gemini.New("") },
			cfg:   voice.SessionConfig{Model: gemini.DefaultModel, Voice: gemini.DefaultVoice, TurnDetection: voice.TurnSemantic},
		},
		{
			name:  openailive.Name,
			model: func(_ *liveWireLog, _ *testing.T) voice.Model { return openailive.New("") },
			cfg:   voice.SessionConfig{Model: openailive.DefaultModel, Voice: openailive.DefaultVoice},
		},
	}
}

const liveInstructions = "You are Pixie, a friendly voice assistant. Answer in one short sentence. " +
	"Use the calculate tool for arithmetic."

func TestLiveVoiceProviders(t *testing.T) {
	secrets := liveSecrets(t)
	question := liveSpeech(t, "What is two plus two?")
	long := liveSpeech(t, "Please count slowly from one to thirty.")
	lead := make([]int16, audio.SampleRate/2) // 0.5 s of silence before speaking
	for _, p := range liveProviders() {
		t.Run(p.name, func(t *testing.T) {
			if _, ok := secrets.APIKey(p.name); !ok {
				v, _ := voice.VarFor(p.name)
				t.Skipf("no %s found in the environment or the app's .env files", v)
			}
			cfg := p.cfg
			cfg.Instructions = liveInstructions

			t.Run("answer", func(t *testing.T) {
				wl := &liveWireLog{}
				r := newLiveRig(t, p.model(wl, t), cfg, secrets)
				r.rt.Hotkey()
				if !r.waitFor("Ready", 10*time.Second, func() bool { return r.liveSpy.count(voice.EvReady) > 0 }) {
					return
				}
				r.say(append(append([]int16(nil), lead...), question...))
				r.waitFor(`an assistant transcript with "4" or "four"`, 20*time.Second, func() bool {
					a := strings.ToLower(r.liveSpy.assistant())
					return strings.Contains(a, "4") || strings.Contains(a, "four")
				})
				r.rt.Hotkey()
				if r.liveSpy.count(voice.EvSpeechStarted) == 0 || r.liveSpy.count(voice.EvSpeechStopped) == 0 {
					t.Errorf("no SpeechStarted/Stopped: %s", r.liveSpy.summary())
				}
				if r.liveSpy.count(voice.EvAudioDelta) == 0 || r.playedMs() == 0 {
					t.Errorf("no audio played: %s", r.liveSpy.summary())
				}
				t.Logf("%s played=%d ms", r.liveSpy.summary(), r.playedMs())
			})

			t.Run("barge-in", func(t *testing.T) {
				wl := &liveWireLog{}
				r := newLiveRig(t, p.model(wl, t), cfg, secrets)
				r.rt.Hotkey()
				if !r.waitFor("Ready", 10*time.Second, func() bool { return r.liveSpy.count(voice.EvReady) > 0 }) {
					return
				}
				r.say(append(append([]int16(nil), lead...), long...))
				if !r.waitFor("1 s of the reply played", 15*time.Second, func() bool { return r.playedMs() >= 1000 }) {
					r.rt.Hotkey()
					return
				}
				r.say(question) // talk over Pixie
				if p.name == openailive.Name {
					// GPT-Live owns barge-in: it yields, then the user's turn starts.
					started := r.liveSpy.count(voice.EvSpeechStarted)
					r.waitFor("GPT-Live yields and the user's turn starts", 10*time.Second, func() bool {
						return r.liveSpy.count(voice.EvSpeechStarted) > started
					})
					r.rt.Hotkey()
					t.Logf("%s played=%d ms flushes=%d", r.liveSpy.summary(), r.playedMs(), r.flushed())
					return
				}
				r.waitFor("an interrupt (cancel sent and playback flushed)", 8*time.Second, func() bool {
					return len(r.liveSpy.cancelled()) > 0 && r.flushed() > 0
				})
				if c := r.liveSpy.cancelled(); len(c) > 0 && c[0] <= 0 {
					t.Errorf("cancel carries no heard duration: %v", c)
				}
				if p.name == openai.Name {
					r.waitFor("conversation.item.truncate on the wire", 2*time.Second, func() bool {
						return wl.has("conversation.item.truncate")
					})
				}
				r.rt.Hotkey()
				t.Logf("%s played=%d ms flushes=%d", r.liveSpy.summary(), r.playedMs(), r.flushed())
			})
		})
	}
}
