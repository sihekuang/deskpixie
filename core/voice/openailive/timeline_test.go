package openailive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
	vt "github.com/sihekuang/deskpixie/core/voice/voicetest"
)

// silenceMs is how much silence msgs append, in ms; it fails on any other message.
func silenceMs(t *testing.T, msgs []obj) int {
	t.Helper()
	total := 0
	for _, m := range msgs {
		if m["type"] != "session.input_audio.append" {
			t.Fatalf("message %v, want only input audio", m)
		}
		b, err := base64.StdEncoding.DecodeString(m["audio"].(string))
		if err != nil {
			t.Fatal(err)
		}
		pcm, _ := audio.DecodeLE(b)
		for _, v := range pcm {
			if v != 0 {
				t.Fatalf("filler audio has a non-zero sample %d", v)
			}
		}
		total += len(pcm) * 1000 / audio.SampleRate
	}
	return total
}

// #482: with no mic (a conversation opened only to say updates), GPT-Live's timeline only
// moves with input audio, so the adapter sends silence for the time the mic hasn't covered.
func TestIdleInputFillsTheTimelineWithSilence(t *testing.T) {
	c := newCodec()
	c.inputUntil = at(0)
	if got := c.idleInput(at(50)); len(got) != 0 {
		t.Fatalf("50 ms after the last audio: %v, want nothing yet", got)
	}
	if got := silenceMs(t, c.idleInput(at(150))); got != 150 {
		t.Fatalf("150 ms without audio: %d ms of silence, want 150", got)
	}
	if got := c.idleInput(at(200)); len(got) != 0 {
		t.Fatalf("right after filling: %v, want nothing", got)
	}
	if got := silenceMs(t, c.idleInput(at(300))); got != 150 {
		t.Fatalf("the next gap: %d ms of silence, want 150 (from 150 to 300)", got)
	}
}

// The mic's own audio keeps the timeline moving: no silence is mixed into it.
func TestIdleInputSendsNothingWhileTheMicFlows(t *testing.T) {
	c := newCodec()
	c.inputUntil = at(0)
	for ms := 40; ms <= 400; ms += 40 {
		c.encode(voice.Command{Kind: voice.CmdAudio, Audio: tone(960, 100)}, at(ms))
		if got := c.idleInput(at(ms + 20)); len(got) != 0 {
			t.Fatalf("at %d ms with the mic sending: %v, want nothing", ms+20, got)
		}
	}
}

// A long stall (the process slept) is not replayed as minutes of audio in one go.
func TestIdleInputCapsOneFill(t *testing.T) {
	c := newCodec()
	c.inputUntil = at(0)
	if got := silenceMs(t, c.idleInput(at(60_000))); got != int(maxIdleFill.Milliseconds()) {
		t.Fatalf("after a minute: %d ms of silence, want the cap %d", got, maxIdleFill.Milliseconds())
	}
	if got := c.idleInput(at(60_050)); len(got) != 0 {
		t.Fatalf("right after the capped fill: %v, want nothing", got)
	}
}

// A session with no mic (#482): the adapter itself keeps input audio flowing.
func TestSessionWithoutMicSendsSilence(t *testing.T) {
	var mu sync.Mutex
	silent := 0 // ms of input audio received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			_, data, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var m struct{ Type, Audio string }
			_ = json.Unmarshal(data, &m)
			switch m.Type {
			case "session.start":
				b, _ := json.Marshal(map[string]any{"type": "session.started"})
				_ = c.Write(r.Context(), websocket.MessageText, b)
			case "session.input_audio.append":
				raw, _ := base64.StdEncoding.DecodeString(m.Audio)
				mu.Lock()
				silent += len(raw) / 2 * 1000 / audio.SampleRate
				mu.Unlock()
			}
		}
	}))
	defer srv.Close()
	s, err := New("ws"+strings.TrimPrefix(srv.URL, "http")).Connect(context.Background(), confCfg(), "k")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := silent
		mu.Unlock()
		if got >= 300 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d ms of input audio after 5 s without a mic, want it to keep flowing", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// #482 end to end: a conversation opened only to say an update (no mic) says it with GPT-Live,
// and ends on its own.
func TestSpeakOnlyUpdateIsSaid(t *testing.T) {
	fs := &fakeLive{}
	srv := httptest.NewServer(http.HandlerFunc(fs.serve))
	defer srv.Close()
	sink := &vt.Sink{}
	rt := voice.NewRuntime(New("ws"+strings.TrimPrefix(srv.URL, "http")), nil, voice.RuntimeConfig{
		Session: voice.SessionConfig{Instructions: "be Bit", ClientDelegation: true},
		APIKey:  "k", Tools: voice.DefaultTools(), Conversation: voice.DefaultConversationConfig(),
	}, sink.Emit)
	player := vt.StartPlayer(rt)
	defer func() { player.Stop(); rt.Close() }()
	wait := func(what string, d time.Duration, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(d)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; client events %v", what, fs.clientTypes())
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !rt.OpenToSpeak() {
		t.Fatal("refused at rest")
	}
	wait("the conversation to open", 5*time.Second, rt.IsOpen)
	rt.Inject(voice.Notice{Text: "Updates:\n- Coordinator reply: The build passed."})
	said := func() bool {
		for _, e := range sink.Events() {
			if e.Kind == "transcript" && e.Role == "assistant" {
				return true
			}
		}
		return false
	}
	wait("the update to be said", 5*time.Second, said)
	wait("the conversation to end on its own", 10*time.Second, func() bool { return !rt.IsOpen() })
}

// Mic frames held up behind a slow write, sent after a fill, count by their length: the timeline
// stays with the clock and the filler waits until the clock passes the audio sent.
func TestIdleInputWaitsOutAMicBacklog(t *testing.T) {
	c := newCodec()
	c.inputUntil = at(0)
	if got := silenceMs(t, c.idleInput(at(500))); got != 500 {
		t.Fatalf("a 500 ms stall: %d ms of silence, want 500", got)
	}
	for range 10 { // the 400 ms captured meanwhile, all at once
		c.encode(voice.Command{Kind: voice.CmdAudio, Audio: tone(960, 100)}, at(510))
	}
	for _, ms := range []int{560, 800, 950} {
		if got := c.idleInput(at(ms)); len(got) != 0 {
			t.Fatalf("at %d ms, with mic audio sent up to 900 ms: %v, want nothing", ms, got)
		}
	}
	if got := silenceMs(t, c.idleInput(at(1000))); got != 100 {
		t.Fatalf("at 1000 ms: %d ms of silence, want 100", got)
	}
}
