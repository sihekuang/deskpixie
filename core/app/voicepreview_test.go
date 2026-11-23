package app

// Voice previews in Settings (#480): voice.preview has the voice service make a short line in a
// voice once, keeps it, and plays it through Pixie's speaker while Pixie is at rest.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// sampleModel says a preview line in 1.6 s of audio (long enough to be kept); hold keeps it
// from answering, fail makes it refuse to connect. With rest set, it sends the first half and
// the second once rest is closed (a service still sending).
type sampleModel struct {
	mu       sync.Mutex
	connects []voice.SessionConfig
	hold     bool
	fail     error
	rest     chan struct{}
	failRest bool // with rest: fails instead of sending the second half
}

const sampleSamples = 38400

func (m *sampleModel) Name() string { return settings.ProviderOpenAILive }

func (m *sampleModel) Connect(_ context.Context, cfg voice.SessionConfig, _ string) (voice.Session, error) {
	m.mu.Lock()
	m.connects = append(m.connects, cfg)
	m.mu.Unlock()
	if m.fail != nil {
		return nil, m.fail
	}
	s := &sampleSession{m: m, events: make(chan voice.Event, 16)}
	s.events <- voice.Event{Kind: voice.EvReady}
	return s, nil
}

func (m *sampleModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.connects)
}

type sampleSession struct {
	m      *sampleModel
	events chan voice.Event
	once   sync.Once
}

func (s *sampleSession) Events() <-chan voice.Event { return s.events }
func (s *sampleSession) Close() error               { s.once.Do(func() { close(s.events) }); return nil }
func (s *sampleSession) Send(c voice.Command) error {
	if c.Kind == voice.CmdInject && !s.m.hold && strings.Contains(c.Text, "Hi, I'm") {
		if s.m.rest == nil {
			s.events <- voice.Event{Kind: voice.EvAudioDelta, Audio: make([]int16, sampleSamples)}
			s.events <- voice.Event{Kind: voice.EvResponseDone}
			return nil
		}
		s.events <- voice.Event{Kind: voice.EvAudioDelta, Audio: make([]int16, sampleSamples/2)}
		go func() {
			<-s.m.rest
			if s.m.failRest {
				s.events <- voice.Event{Kind: voice.EvError, Fatal: true, Message: "server error"}
				return
			}
			s.events <- voice.Event{Kind: voice.EvAudioDelta, Audio: make([]int16, sampleSamples/2)}
			s.events <- voice.Event{Kind: voice.EvResponseDone}
		}()
	}
	return nil
}

func withSampleModel(m *sampleModel) harnessOpt {
	return func(d *Deps, h *harness) {
		d.Providers = map[string]ModelFactory{settings.ProviderOpenAILive: func() voice.Model { return m }}
	}
}

func previewStates(h *harness, voiceName string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, e := range h.events {
		if e["kind"] == "voice.preview" && e["voice"] == voiceName {
			s, _ := e["state"].(string)
			if s == "error" {
				s += ": " + e["error"].(string)
			}
			out = append(out, s)
		}
	}
	return out
}

// played is how much the speaker gets within a moment (PlayClip queues on the runtime's loop).
func played(h *harness) int {
	total := 0
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		n, _ := h.app.rt.PullPlayback(make([]int16, 48000))
		total += n
		time.Sleep(10 * time.Millisecond)
	}
	return total
}

// waitState waits for voiceName's preview to reach state (events stay for previewStates).
func waitState(t *testing.T, h *harness, voiceName, state string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := previewStates(h, voiceName)
		if len(got) > 0 && got[len(got)-1] == state {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never got to %s: %v", voiceName, state, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func liveSettings(c *settings.Config) { c.Voice.Provider = settings.ProviderOpenAILive }

func TestVoicePreviewIsMadeThenKeptAndPlays(t *testing.T) {
	m := &sampleModel{}
	h := newHarness(t, withSampleModel(m), withSettings(liveSettings), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	var res struct{ State string }
	json.Unmarshal(h.call("voice.preview", map[string]string{"voice": "marin"}), &res)
	if res.State != "loading" {
		t.Fatalf("first preview state %q, want loading", res.State)
	}
	waitState(t, h, "marin", "playing")
	if n := played(h); n != sampleSamples {
		t.Fatalf("played %d samples, want the %d made", n, sampleSamples)
	}
	if c := m.connects[0]; c.Voice != "marin" || c.Model != h.app.store.Current().Voice.Model() {
		t.Fatalf("made with %+v, want marin on the saved model", c)
	}
	waitState(t, h, "marin", "done")

	json.Unmarshal(h.call("voice.preview", map[string]string{"voice": "marin"}), &res)
	if res.State != "playing" || m.count() != 1 || played(h) != sampleSamples {
		t.Fatalf("second preview: state %q, %d connects; want it played from the kept clip", res.State, m.count())
	}
	if got := previewStates(h, "marin"); strings.Join(got, ",") != "loading,playing,done,playing" {
		t.Fatalf("events %v", got)
	}
}

// No key: the refusal says so, and no event follows it to clear the row's message.
func TestVoicePreviewWithoutAKey(t *testing.T) {
	h := newHarness(t, withSampleModel(&sampleModel{}), withSettings(liveSettings))
	_, err := h.callErr("voice.preview", map[string]string{"voice": "marin"})
	if err == nil || err.Error() != previewNoKey {
		t.Fatalf("err = %v, want %q", err, previewNoKey)
	}
	time.Sleep(100 * time.Millisecond)
	if got := previewStates(h, "marin"); len(got) != 0 {
		t.Fatalf("events after the refusal: %v", got)
	}
}

// A failure right away still comes after "loading", so the row doesn't spin on.
func TestVoicePreviewQuickFailureComesAfterLoading(t *testing.T) {
	for range 20 {
		m := &sampleModel{fail: errors.New("dial tcp: no route to host")}
		h := newHarness(t, withSampleModel(m), withSettings(liveSettings), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
		h.call("voice.preview", map[string]string{"voice": "marin"})
		waitState(t, h, "marin", "error: Couldn't make the preview: dial tcp: no route to host")
		if got := previewStates(h, "marin"); len(got) != 2 || got[0] != "loading" {
			t.Fatalf("events %v, want loading then the error", got)
		}
	}
}

// Two previews in quick succession: the newer one plays, whatever order their work runs in.
func TestVoicePreviewQuickTakeOverPlaysTheNewer(t *testing.T) {
	for range 10 {
		m := &sampleModel{}
		h := newHarness(t, withSampleModel(m), withSettings(liveSettings), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
		h.call("voice.preview", map[string]string{"voice": "marin"})
		h.call("voice.preview", map[string]string{"voice": "cedar"})
		waitState(t, h, "cedar", "playing")
		if got := previewStates(h, "marin"); got[len(got)-1] != "done" {
			t.Fatalf("marin's events %v, want it ended", got)
		}
	}
}

func TestVoicePreviewWaitsForPixieToBeAtRest(t *testing.T) {
	m := &sampleModel{}
	h := newHarness(t, withSampleModel(m), withSettings(liveSettings), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	h.app.rt.Hotkey()
	waitFor(t, "a conversation", h.app.rt.IsOpen)
	_, err := h.callErr("voice.preview", map[string]string{"voice": "marin"})
	if err == nil || err.Error() != previewBusy {
		t.Fatalf("err = %v, want %q", err, previewBusy)
	}
}

// A second preview, or Stop, takes over one being made: the first ends without playing.
func TestVoicePreviewStopAndTakeOver(t *testing.T) {
	m := &sampleModel{hold: true}
	h := newHarness(t, withSampleModel(m), withSettings(liveSettings), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	h.call("voice.preview", map[string]string{"voice": "marin"})
	waitFor(t, "making marin", func() bool { return m.count() == 1 })
	h.call("voice.preview", map[string]string{"voice": "cedar"})
	waitFor(t, "making cedar", func() bool { return m.count() == 2 })
	h.call("voice.previewStop", nil)
	time.Sleep(100 * time.Millisecond)
	if got := previewStates(h, "marin"); strings.Join(got, ",") != "loading,done" {
		t.Fatalf("marin's events %v, want loading then done", got)
	}
	if got := previewStates(h, "cedar"); strings.Join(got, ",") != "loading,done" {
		t.Fatalf("cedar's events %v, want loading then done", got)
	}
	if n := played(h); n != 0 {
		t.Fatalf("%d samples played after Stop", n)
	}
}

// A preview being made starts playing once its first audio is in, before the service has sent
// the rest; the rest plays as it comes, and the row is done after it.
func TestVoicePreviewPlaysWhileItIsMade(t *testing.T) {
	m := &sampleModel{rest: make(chan struct{})}
	h := newHarness(t, withSampleModel(m), withSettings(liveSettings), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	h.call("voice.preview", map[string]string{"voice": "marin"})
	waitState(t, h, "marin", "playing")
	if n := played(h); n != sampleSamples/2 {
		t.Fatalf("played %d samples before the rest came, want the first %d", n, sampleSamples/2)
	}
	close(m.rest)
	deadline := time.Now().Add(3 * time.Second)
	got := 0
	for got < sampleSamples/2 && time.Now().Before(deadline) {
		got += played(h)
	}
	if got != sampleSamples/2 {
		t.Fatalf("played %d more samples, want the other %d", got, sampleSamples/2)
	}
	waitState(t, h, "marin", "done")
	if got := previewStates(h, "marin"); len(got) != 3 {
		t.Fatalf("events %v, want loading, playing, done", got)
	}
}

// The service failing partway: the row says so and the part already queued stops, so the voice
// doesn't talk on under the error.
func TestVoicePreviewFailingPartwayStopsTheSound(t *testing.T) {
	m := &sampleModel{rest: make(chan struct{}), failRest: true}
	h := newHarness(t, withSampleModel(m), withSettings(liveSettings), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	h.call("voice.preview", map[string]string{"voice": "marin"})
	waitState(t, h, "marin", "playing")
	waitFor(t, "first half queued", func() bool { return h.app.rt.ClipLeft() > 0 })
	close(m.rest)
	waitState(t, h, "marin", "error: Couldn't make the preview: server error")
	waitFor(t, "the sound stopped", func() bool { return h.app.rt.ClipLeft() == 0 })
}

// The service sending slower than the clip plays: the row stays playing until the speaker has
// taken all of it, not for the clip's length from when it started.
func TestVoicePreviewIsDoneOnlyWhenItHasPlayed(t *testing.T) {
	m := &sampleModel{rest: make(chan struct{})}
	h := newHarness(t, withSampleModel(m), withSettings(liveSettings), withKeys(map[string]string{"OPENAI_API_KEY": "sk-test"}))
	h.call("voice.preview", map[string]string{"voice": "marin"})
	waitState(t, h, "marin", "playing")
	played(h) // the first half plays, then the speaker runs dry
	time.Sleep(900 * time.Millisecond)
	close(m.rest) // the rest comes late; the speaker hasn't taken it
	waitFor(t, "second half queued", func() bool { return h.app.rt.ClipLeft() > 0 })
	time.Sleep(1500 * time.Millisecond) // past the settle and what the clip's length alone allows
	if got := previewStates(h, "marin"); got[len(got)-1] != "playing" {
		t.Fatalf("events %v: done before the second half played", got)
	}
	played(h)
	waitState(t, h, "marin", "done")
}
