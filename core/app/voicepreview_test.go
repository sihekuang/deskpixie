package app

// Voice previews in Settings (#480): voice.preview has the voice service make a short line in a
// voice once, keeps it, and plays it through Pixie's speaker while Pixie is at rest.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// sampleModel says a preview line in 0.1 s of audio; hold keeps it from answering.
type sampleModel struct {
	mu       sync.Mutex
	connects []voice.SessionConfig
	hold     bool
}

func (m *sampleModel) Name() string { return settings.ProviderOpenAILive }

func (m *sampleModel) Connect(_ context.Context, cfg voice.SessionConfig, _ string) (voice.Session, error) {
	m.mu.Lock()
	m.connects = append(m.connects, cfg)
	m.mu.Unlock()
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
		s.events <- voice.Event{Kind: voice.EvAudioDelta, Audio: make([]int16, 2400)}
		s.events <- voice.Event{Kind: voice.EvResponseDone}
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
	waitFor(t, voiceName+" "+state, func() bool {
		got := previewStates(h, voiceName)
		return len(got) > 0 && got[len(got)-1] == state
	})
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
	if n := played(h); n != 2400 {
		t.Fatalf("played %d samples, want the 2400 made", n)
	}
	if c := m.connects[0]; c.Voice != "marin" || c.Model != h.app.store.Current().Voice.Model() {
		t.Fatalf("made with %+v, want marin on the saved model", c)
	}
	waitState(t, h, "marin", "done")

	json.Unmarshal(h.call("voice.preview", map[string]string{"voice": "marin"}), &res)
	if res.State != "playing" || m.count() != 1 || played(h) != 2400 {
		t.Fatalf("second preview: state %q, %d connects; want it played from the kept clip", res.State, m.count())
	}
	if got := previewStates(h, "marin"); strings.Join(got, ",") != "loading,playing,done,playing" {
		t.Fatalf("events %v", got)
	}
}

func TestVoicePreviewWithoutAKey(t *testing.T) {
	h := newHarness(t, withSampleModel(&sampleModel{}), withSettings(liveSettings))
	_, err := h.callErr("voice.preview", map[string]string{"voice": "marin"})
	if err == nil || err.Error() != previewNoKey {
		t.Fatalf("err = %v, want %q", err, previewNoKey)
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
