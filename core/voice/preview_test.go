package voice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// previewModel is a provider that answers an injected line as told.
type previewModel struct {
	mu       sync.Mutex
	connects []SessionConfig
	injected []string
	// reply: the events after the line is injected; ready: whether it says EvReady at all.
	reply    []Event
	noReady  bool
	failWith error
	hold     bool // never reply: wait until cancelled
}

func (m *previewModel) Name() string { return "fake" }

func (m *previewModel) Connect(ctx context.Context, cfg SessionConfig, key string) (Session, error) {
	m.mu.Lock()
	m.connects = append(m.connects, cfg)
	m.mu.Unlock()
	if m.failWith != nil {
		return nil, m.failWith
	}
	s := &previewSession{m: m, events: make(chan Event, 64)}
	if !m.noReady {
		s.events <- Event{Kind: EvReady}
	}
	return s, nil
}

func (m *previewModel) calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.connects)
}

type previewSession struct {
	m      *previewModel
	events chan Event
	once   sync.Once
}

func (s *previewSession) Events() <-chan Event { return s.events }
func (s *previewSession) Close() error         { s.once.Do(func() { close(s.events) }); return nil }
func (s *previewSession) Send(c Command) error {
	if c.Kind != CmdInject {
		return nil
	}
	s.m.mu.Lock()
	s.m.injected = append(s.m.injected, c.Text)
	s.m.mu.Unlock()
	if s.m.hold {
		return nil
	}
	for _, e := range s.m.reply {
		s.events <- e
	}
	return nil
}

func spoken(n int) []Event {
	return []Event{
		{Kind: EvResponseCreated},
		{Kind: EvAudioDelta, Audio: make([]int16, n)},
		{Kind: EvAudioDelta, Audio: []int16{1, 2, 3}},
		{Kind: EvResponseDone},
	}
}

func newPreviews(t *testing.T, m *previewModel, key bool) *Previews {
	t.Helper()
	secrets := fixedSecrets{}
	if key {
		secrets = fixedSecrets{key: "sk-test"}
	}
	return &Previews{
		Dir: filepath.Join(t.TempDir(), "previews"), Secrets: secrets, Timeout: 2 * time.Second,
		Model: func(p string) Model {
			if p == "openai-live" {
				return m
			}
			return nil
		},
	}
}

func TestPreviewIsMadeOnceThenKept(t *testing.T) {
	m := &previewModel{reply: spoken(100)}
	p := newPreviews(t, m, true)
	pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin")
	if err != nil || len(pcm) != 103 {
		t.Fatalf("Get = %d samples, %v; want 103, nil", len(pcm), err)
	}
	if c := m.connects[0]; c.Voice != "marin" || c.Model != "gpt-live-1" || len(c.Tools) != 0 {
		t.Fatalf("connected with %+v, want the voice and model, no tools", c)
	}
	if len(m.injected) != 1 || !strings.Contains(m.injected[0], "Hi, I'm Marin.") {
		t.Fatalf("injected %q, want the preview line", m.injected)
	}
	again, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin")
	if err != nil || len(again) != 103 || again[102] != 3 || m.calls() != 1 {
		t.Fatalf("second Get = %d samples, %v after %d connects; want the kept clip, no connect", len(again), err, m.calls())
	}
	if _, ok := p.Cached("openai-live", "gpt-live-1", "marin"); !ok {
		t.Fatal("not cached")
	}
	if _, ok := p.Cached("openai-live", "gpt-live-1.1", "marin"); ok {
		t.Fatal("cached for another model too")
	}
}

func TestPreviewFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *previewModel
		key  bool
		want string
	}{
		{"no key", &previewModel{reply: spoken(10)}, false, "no API key"},
		{"connect fails", &previewModel{failWith: errors.New("gpt-live: invalid_api_key")}, true, "invalid_api_key"},
		{"never ready", &previewModel{noReady: true}, true, "took too long"},
		{"never answers", &previewModel{hold: true}, true, "took too long"},
		{"no audio", &previewModel{reply: []Event{{Kind: EvResponseCreated}, {Kind: EvResponseDone}}}, true, "no audio"},
		{"fatal error", &previewModel{reply: []Event{{Kind: EvError, Fatal: true, Message: "rate limited"}}}, true, "rate limited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPreviews(t, tc.m, tc.key)
			p.Timeout = 200 * time.Millisecond
			_, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "sk-test") {
				t.Fatalf("the key is in the error: %v", err)
			}
			if _, ok := p.Cached("openai-live", "gpt-live-1", "marin"); ok {
				t.Fatal("a failed preview was kept")
			}
		})
	}
}

func TestPreviewRefusesUnknownOrUnsafeNames(t *testing.T) {
	p := newPreviews(t, &previewModel{reply: spoken(10)}, true)
	for _, c := range [][3]string{
		{"nope", "m", "v"}, {"openai-live", "../x", "v"}, {"openai-live", "m", "a/b"}, {"openai-live", "m", ""},
	} {
		if _, err := p.Get(context.Background(), c[0], c[1], c[2]); err == nil {
			t.Fatalf("Get%v: no error", c)
		}
	}
	entries, _ := os.ReadDir(filepath.Dir(p.Dir))
	for _, e := range entries {
		if e.Name() != "previews" {
			t.Fatalf("wrote %s outside the previews folder", e.Name())
		}
	}
}

func TestPreviewDamagedClipIsMadeAgain(t *testing.T) {
	m := &previewModel{reply: spoken(10)}
	p := newPreviews(t, m, true)
	path, _ := p.path("openai-live", "gpt-live-1", "marin")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte{1}, 0o644) // half a sample
	pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin")
	if err != nil || len(pcm) != 13 || m.calls() != 1 {
		t.Fatalf("Get = %d samples, %v, %d connects; want it made again", len(pcm), err, m.calls())
	}
}

func TestPreviewUnwritableFolderStillPlays(t *testing.T) {
	m := &previewModel{reply: spoken(10)}
	p := newPreviews(t, m, true)
	os.WriteFile(p.Dir, []byte("a file in the way"), 0o644)
	pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin")
	if err != nil || len(pcm) != 13 {
		t.Fatalf("Get = %d samples, %v; want the clip anyway", len(pcm), err)
	}
}

// A second preview, or Stop, cancels the one being made.
func TestPreviewANewOneCancelsTheOld(t *testing.T) {
	m := &previewModel{hold: true}
	p := newPreviews(t, m, true)
	done := make(chan error, 1)
	go func() {
		_, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin")
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for m.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	p.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, ErrPreviewCancelled) {
			t.Fatalf("err = %v, want cancelled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop didn't cancel the preview being made")
	}
}

func TestPreviewLine(t *testing.T) {
	if got := PreviewLine("marin"); got != "Hi, I'm Marin. This is how I sound." {
		t.Fatalf("PreviewLine = %q", got)
	}
}

// A preview plays only at rest, and a conversation that starts cuts it off.
func TestRuntimePlayClip(t *testing.T) {
	r, m, ui := newTestRuntime(t, "sk-test")
	if !r.PlayClip(make([]int16, 2400)) {
		t.Fatal("PlayClip refused at rest")
	}
	eventually(t, "clip queued", func() bool { return r.queuedSamples() == 2400 })
	r.StopClip()
	eventually(t, "clip stopped", func() bool { return r.queuedSamples() == 0 })

	r.PlayClip(make([]int16, 4800))
	eventually(t, "clip queued", func() bool { return r.queuedSamples() == 4800 })
	startListening(t, r, m, ui)
	if n := r.queuedSamples(); n != 0 {
		t.Fatalf("%d samples of the clip still queued in a conversation", n)
	}
	if r.PlayClip(make([]int16, 2400)) {
		t.Fatal("PlayClip played over a conversation")
	}
	m.sess.events <- Event{Kind: EvAudioDelta, Audio: make([]int16, 1200)}
	eventually(t, "reply queued", func() bool { return r.queuedSamples() == 1200 })
	r.StopClip()
	time.Sleep(50 * time.Millisecond)
	if n := r.queuedSamples(); n != 1200 {
		t.Fatalf("StopClip cut the conversation's audio: %d queued, want 1200", n)
	}
}
