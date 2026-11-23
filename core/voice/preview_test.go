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

// spoken is a reply of n+3 samples. Clips shorter than previewKeepSamples aren't kept, so tests
// of keeping use long ones.
func spoken(n int) []Event {
	return []Event{
		{Kind: EvResponseCreated},
		{Kind: EvAudioDelta, Audio: make([]int16, n)},
		{Kind: EvAudioDelta, Audio: []int16{1, 2, 3}},
		{Kind: EvResponseDone},
	}
}

const long = 3 * 24000 // a whole line: 3 s

func newPreviews(t *testing.T, m *previewModel, key bool) *Previews {
	t.Helper()
	secrets := fixedSecrets{}
	if key {
		secrets = fixedSecrets{key: "sk-test"}
	}
	return &Previews{
		Dir: filepath.Join(t.TempDir(), "previews"), Secrets: secrets, Timeout: 2 * time.Second,
		Settle: 20 * time.Millisecond,
		Model: func(p string) Model {
			if p == "openai-live" {
				return m
			}
			return nil
		},
	}
}

func TestPreviewIsMadeOnceThenKept(t *testing.T) {
	m := &previewModel{reply: spoken(long)}
	p := newPreviews(t, m, true)
	pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin", nil)
	if err != nil || len(pcm) != long+3 {
		t.Fatalf("Get = %d samples, %v; want %d, nil", len(pcm), err, long+3)
	}
	if c := m.connects[0]; c.Voice != "marin" || c.Model != "gpt-live-1" || len(c.Tools) != 0 {
		t.Fatalf("connected with %+v, want the voice and model, no tools", c)
	}
	if len(m.injected) != 1 || !strings.Contains(m.injected[0], "Hi, I'm Marin.") {
		t.Fatalf("injected %q, want the preview line", m.injected)
	}
	again, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin", nil)
	if err != nil || len(again) != long+3 || again[long+2] != 3 || m.calls() != 1 {
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
		{"failed reply", &previewModel{reply: []Event{{Kind: EvError, Message: "response failed: quota"}, {Kind: EvResponseDone}}}, true, "quota"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newPreviews(t, tc.m, tc.key)
			p.Timeout = 200 * time.Millisecond
			_, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin", nil)
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
		if _, err := p.Get(context.Background(), c[0], c[1], c[2], nil); err == nil {
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
	m := &previewModel{reply: spoken(long)}
	p := newPreviews(t, m, true)
	path, _ := p.path("openai-live", "gpt-live-1", "marin")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte{1}, 0o644) // half a sample
	pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin", nil)
	if err != nil || len(pcm) != long+3 || m.calls() != 1 {
		t.Fatalf("Get = %d samples, %v, %d connects; want it made again", len(pcm), err, m.calls())
	}
}

func TestPreviewUnwritableFolderStillPlays(t *testing.T) {
	m := &previewModel{reply: spoken(10)}
	p := newPreviews(t, m, true)
	os.WriteFile(p.Dir, []byte("a file in the way"), 0o644)
	pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin", nil)
	if err != nil || len(pcm) != 13 {
		t.Fatalf("Get = %d samples, %v; want the clip anyway", len(pcm), err)
	}
}

// The caller's ctx ending cancels the clip being made.
func TestPreviewEndsWithItsContext(t *testing.T) {
	m := &previewModel{hold: true}
	p := newPreviews(t, m, true)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Get(ctx, "openai-live", "gpt-live-1", "marin", nil)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for m.calls() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrPreviewCancelled) {
			t.Fatalf("err = %v, want cancelled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelling didn't stop the preview being made")
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

	r.PlayClip(make([]int16, 2400))
	r.AppendClip(make([]int16, 2400)) // a preview still arriving
	eventually(t, "clip queued", func() bool { return r.queuedSamples() == 4800 })
	startListening(t, r, m, ui)
	if n := r.queuedSamples(); n != 0 {
		t.Fatalf("%d samples of the clip still queued in a conversation", n)
	}
	if r.PlayClip(make([]int16, 2400)) {
		t.Fatal("PlayClip played over a conversation")
	}
	r.AppendClip(make([]int16, 600)) // the rest of a preview the conversation cut off
	time.Sleep(50 * time.Millisecond)
	if n := r.queuedSamples(); n != 0 {
		t.Fatalf("AppendClip queued %d samples in a conversation", n)
	}
	m.sess.events <- Event{Kind: EvAudioDelta, Audio: make([]int16, 1200)}
	eventually(t, "reply queued", func() bool { return r.queuedSamples() == 1200 })
	r.StopClip()
	time.Sleep(50 * time.Millisecond)
	if n := r.queuedSamples(); n != 1200 {
		t.Fatalf("StopClip cut the conversation's audio: %d queued, want 1200", n)
	}
}

// A short reply (cut off, failed) is played but not kept; a line split over two replies (GPT-Live
// ends a reply at a pause) is kept whole.
func TestPreviewKeepsOnlyAWholeLine(t *testing.T) {
	m := &previewModel{reply: spoken(100)}
	p := newPreviews(t, m, true)
	if pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin", nil); err != nil || len(pcm) != 103 {
		t.Fatalf("Get = %d samples, %v", len(pcm), err)
	}
	if _, ok := p.Cached("openai-live", "gpt-live-1", "marin"); ok {
		t.Fatal("a 4 ms clip was kept")
	}

	half := long / 2
	m = &previewModel{reply: append(spoken(half), spoken(half)...)}
	p = newPreviews(t, m, true)
	p.Settle = 300 * time.Millisecond
	pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "cedar", nil)
	if err != nil || len(pcm) != 2*(half+3) {
		t.Fatalf("Get = %d samples, %v; want both halves, %d", len(pcm), err, 2*(half+3))
	}
	if _, ok := p.Cached("openai-live", "gpt-live-1", "cedar"); !ok {
		t.Fatal("the whole line wasn't kept")
	}
}

// Gemini's model ids may carry "models/": the clip is kept under the bare id.
func TestPreviewGeminiModelsPrefix(t *testing.T) {
	m := &previewModel{reply: spoken(long)}
	p := newPreviews(t, m, true)
	p.Model = func(string) Model { return m }
	if _, err := p.Get(context.Background(), "gemini-live", "models/gemini-3.8-live", "Kore", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Cached("gemini-live", "gemini-3.8-live", "Kore"); !ok {
		t.Fatal("not kept under the bare model id")
	}
}

// A clip being made comes to onAudio piece by piece as the service sends it; a kept one in one
// piece. Each one made gets a log line with its timings.
func TestPreviewStreamsAndLogsTimings(t *testing.T) {
	m := &previewModel{reply: spoken(long)}
	p := newPreviews(t, m, true)
	logPath := filepath.Join(t.TempDir(), "voice.log")
	p.Log = NewVoiceLog(logPath, nil)
	var pieces []int
	pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin", func(a []int16) { pieces = append(pieces, len(a)) })
	if err != nil || len(pcm) != long+3 {
		t.Fatalf("Get = %d samples, %v", len(pcm), err)
	}
	if len(pieces) != 2 || pieces[0] != long || pieces[1] != 3 {
		t.Fatalf("pieces %v, want [%d 3] as they came", pieces, long)
	}
	pieces = nil
	if _, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin", func(a []int16) { pieces = append(pieces, len(a)) }); err != nil {
		t.Fatal(err)
	}
	if len(pieces) != 1 || pieces[0] != long+3 {
		t.Fatalf("kept clip came as %v, want one piece", pieces)
	}
	b, _ := os.ReadFile(logPath)
	line := string(b)
	for _, want := range []string{"voice preview openai-live gpt-live-1 marin: ready ", "first audio ", "whole ", "(3000 ms of audio)"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log %q lacks %q", line, want)
		}
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("log has %d lines, want 1 (the kept clip isn't logged): %q", strings.Count(line, "\n"), line)
	}
}

// A reply longer than previewMaxSamples is cut there, and onAudio never gets past it.
func TestPreviewStreamStopsAtTheLimit(t *testing.T) {
	m := &previewModel{reply: spoken(previewMaxSamples + 100)}
	p := newPreviews(t, m, true)
	got := 0
	pcm, err := p.Get(context.Background(), "openai-live", "gpt-live-1", "marin", func(a []int16) { got += len(a) })
	if err != nil || len(pcm) != previewMaxSamples || got != previewMaxSamples {
		t.Fatalf("Get = %d samples (%d streamed), %v; want %d", len(pcm), got, err, previewMaxSamples)
	}
}
