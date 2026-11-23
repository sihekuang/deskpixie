package voice

// Voice previews (#480): a short line in a voice, made once by the provider itself through
// Model.Connect and kept in <app data>/voice/previews, so Settings can play it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/sihekuang/deskpixie/core/audio"
)

const (
	// previewVersion is in each clip's file name: changing the line or the instructions bumps
	// it, so old clips are made again.
	previewVersion = 1
	// PreviewTimeout bounds making one clip.
	PreviewTimeout = 20 * time.Second
	// previewMaxSamples bounds a clip (15 s): a provider that keeps talking is cut off.
	previewMaxSamples = 15 * audio.SampleRate
	// previewKeepSamples: a clip shorter than this (1.5 s; the line takes about 3) is played but
	// not kept, since the reply was likely cut short (a failed or interrupted response).
	previewKeepSamples = 3 * audio.SampleRate / 2
	// previewSettle: after a reply ends, more audio this soon is still the line (GPT-Live ends a
	// reply at a pause between its sentences).
	previewSettle = 800 * time.Millisecond

	previewInstructions = "You are reading a voice sample. When you are given a line, say " +
		"exactly that line, word for word, in a natural, friendly tone, then stop. Add nothing."
)

var (
	// ErrPreviewNoKey: the provider's API key isn't set.
	ErrPreviewNoKey = errors.New("no API key")
	// ErrPreviewCancelled: a newer preview (or Stop) took its place.
	ErrPreviewCancelled = errors.New("cancelled")
	errPreviewSilent    = errors.New("the voice service sent no audio")
	errPreviewTimeout   = errors.New("the voice service took too long")
	safeName            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// PreviewLine is what a voice says in its preview.
func PreviewLine(voice string) string {
	r := []rune(voice)
	if len(r) > 0 {
		r[0] = unicode.ToUpper(r[0])
	}
	return "Hi, I'm " + string(r) + ". This is how I sound."
}

// Previews makes and keeps voice previews. Its caller decides which preview runs: Get stops
// when its ctx ends.
type Previews struct {
	// Dir holds the clips: <Dir>/<provider>/<model>/<voice>-v<n>.pcm (24 kHz mono s16le).
	Dir     string
	Secrets SecretSource
	// Model is provider's adapter; nil when it isn't registered.
	Model func(provider string) Model
	// Timeout bounds making one clip (0: PreviewTimeout).
	Timeout time.Duration
	// Settle is how long to wait for more of the line after a reply ends (0: previewSettle).
	Settle time.Duration
	// Log gets one line per clip made, with its timings (optional).
	Log *VoiceLog
	// Now is the clock (nil: time.Now).
	Now func() time.Time
}

// Cached is the kept clip of voice, if there is one.
func (p *Previews) Cached(provider, model, voice string) ([]int16, bool) {
	path, err := p.path(provider, model, voice)
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	pcm, rest := audio.DecodeLE(b)
	if len(rest) != 0 || len(pcm) == 0 {
		os.Remove(path) // damaged: made again next time
		return nil, false
	}
	return pcm, true
}

// Get is voice's clip: the kept one, or else one made now by the provider (and kept when it is
// whole). onAudio (optional) gets the clip as it comes, to play it at once: the kept one in one
// piece, a new one piece by piece as the provider sends it. ErrPreviewCancelled when ctx ends
// first.
func (p *Previews) Get(ctx context.Context, provider, model, voice string, onAudio func([]int16)) ([]int16, error) {
	path, err := p.path(provider, model, voice)
	if err != nil {
		return nil, err
	}
	if pcm, ok := p.Cached(provider, model, voice); ok {
		if onAudio != nil {
			onAudio(pcm)
		}
		return pcm, nil
	}
	newModel := p.Model
	var m Model
	if newModel != nil {
		m = newModel(provider)
	}
	if m == nil {
		return nil, fmt.Errorf("unknown voice service %q", provider)
	}
	key, ok := p.Secrets.APIKey(provider)
	if !ok {
		return nil, ErrPreviewNoKey
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = PreviewTimeout
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	settle := p.Settle
	if settle <= 0 {
		settle = previewSettle
	}
	now := p.Now
	if now == nil {
		now = time.Now
	}
	t := previewTimes{start: now(), now: now}
	pcm, err := makePreview(ctx, m, SessionConfig{Model: model, Voice: voice, Instructions: previewInstructions}, key, PreviewLine(voice), settle, &t, onAudio)
	p.Log.Printf("voice preview %s %s %s: %s", provider, model, voice, t.summary(err))
	if err != nil {
		if parent.Err() != nil {
			return nil, ErrPreviewCancelled
		}
		if ctx.Err() != nil {
			return nil, errPreviewTimeout
		}
		return nil, err
	}
	if len(pcm) >= previewKeepSamples {
		_ = writeClip(path, pcm) // not kept: still played, and made again next time
	}
	return pcm, nil
}

func (p *Previews) path(provider, model, voice string) (string, error) {
	model = strings.TrimPrefix(model, "models/") // Gemini's ids may carry it
	for _, s := range []string{provider, model, voice} {
		if !safeName.MatchString(s) || strings.Contains(s, "..") {
			return "", fmt.Errorf("no preview for %q", s)
		}
	}
	return filepath.Join(p.Dir, provider, model, fmt.Sprintf("%s-v%d.pcm", voice, previewVersion)), nil
}

// previewTimes is how making one clip went, for the log: when the session was ready, the
// first audio came and the line was whole, from the start.
type previewTimes struct {
	start               time.Time
	now                 func() time.Time
	ready, first, whole time.Duration
	heard               int // samples that came, even of a clip that then failed
}

func (t *previewTimes) mark(d *time.Duration) {
	if *d == 0 {
		*d = t.now().Sub(t.start)
	}
}

func (t *previewTimes) summary(err error) string {
	ms := func(d time.Duration) string {
		if d == 0 {
			return "-"
		}
		return fmt.Sprintf("%d ms", d.Milliseconds())
	}
	out := fmt.Sprintf("ready %s, first audio %s, whole %s (%d ms of audio)", ms(t.ready), ms(t.first), ms(t.whole), t.heard*1000/audio.SampleRate)
	if err != nil {
		out += ", failed: " + err.Error()
	}
	return out
}

// makePreview opens a session in cfg's voice, has it say line, and returns the audio, handing
// each piece to onAudio (optional) as it comes.
func makePreview(ctx context.Context, m Model, cfg SessionConfig, key Secret, line string, settle time.Duration, t *previewTimes, onAudio func([]int16)) ([]int16, error) {
	s, err := m.Connect(ctx, cfg, key.Expose())
	if err != nil {
		return nil, err
	}
	defer s.Close()
	var pcm []int16
	sent := false
	lastErr := ""                // the provider's last non-fatal error, for a reply with no audio
	var settled <-chan time.Time // a reply ended with audio: done unless more comes by then
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-settled:
			t.mark(&t.whole)
			return pcm, nil
		case e, ok := <-s.Events():
			if !ok {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				return nil, errors.New("the voice service closed the session")
			}
			switch e.Kind {
			case EvReady:
				t.mark(&t.ready)
				if !sent {
					sent = true
					if err := s.Send(Command{Kind: CmdInject, Text: "Say this line: " + line, Respond: true}); err != nil {
						return nil, err
					}
				}
			case EvAudioDelta:
				if sent {
					t.mark(&t.first)
					settled = nil
					piece := e.Audio
					if room := previewMaxSamples - len(pcm); len(piece) > room {
						piece = piece[:room]
					}
					pcm = append(pcm, piece...)
					t.heard = len(pcm)
					if onAudio != nil && len(piece) > 0 {
						onAudio(piece)
					}
					if len(pcm) >= previewMaxSamples {
						t.mark(&t.whole)
						return pcm, nil
					}
				}
			case EvResponseDone:
				if sent && len(pcm) > 0 {
					settled = time.After(settle)
				} else if sent {
					if lastErr != "" {
						return nil, errors.New(lastErr)
					}
					return nil, errPreviewSilent
				}
			case EvError:
				if e.Fatal {
					return nil, errors.New(e.Message)
				}
				lastErr = e.Message
			}
		}
	}
}

// writeClip saves pcm at path atomically.
func writeClip(path string, pcm []int16) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".clip-*")
	if err != nil {
		return err
	}
	_, werr := f.Write(audio.EncodeLE(pcm))
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(f.Name())
		return errors.Join(werr, cerr)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}
