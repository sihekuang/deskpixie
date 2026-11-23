package app

// Voice previews in Settings (#480): voice.preview plays a short line in a voice of the current
// voice service, made once by that service and kept in <app data>/voice/previews. The
// voice.preview event tells the picker how it goes: loading, playing, done or error.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

const (
	previewBusy  = "Previews play when Pixie isn't talking."
	previewNoKey = "Add the voice service's API key to hear previews."
	previewNoRT  = "Pixie's voice isn't set up, so previews can't play."
	// previewErrRunes is the most of a failure reason the picker shows.
	previewErrRunes = 120
)

// previewRefusal is why a preview can't play, in words for the picker to show as they are.
type previewRefusal string

func (r previewRefusal) Error() string { return string(r) }
func (r previewRefusal) Code() string  { return "refused" }

// previewer is the one preview at a time: the voice it is for and its gen, so a late result
// of one taken over is dropped.
type previewer struct {
	mu    sync.Mutex
	gen   int
	voice string
	timer *time.Timer // ends "playing" when the clip has played
}

func (a *App) setupPreviews() {
	a.previews = &voice.Previews{
		Dir:     filepath.Join(a.paths.AppSupport, "voice", "previews"),
		Secrets: a.deps.Secrets,
		Model: func(provider string) voice.Model {
			f, ok := a.deps.Providers[provider]
			if !ok {
				return nil
			}
			return f()
		},
	}
}

func (a *App) registerPreview(r Registrar) {
	r.Register("voice.preview", func(args json.RawMessage) (any, error) {
		var in struct {
			Voice string `json:"voice"`
		}
		if err := decodeArgs("voice.preview", args, &in); err != nil {
			return nil, err
		}
		return a.preview(in.Voice)
	})
	r.Register("voice.previewStop", func(json.RawMessage) (any, error) {
		a.stopPreview()
		return nil, nil
	})
}

// preview plays voiceName's clip for the current voice service and model: at once when it is
// kept, else once the service has made it. The result is the state it starts in.
func (a *App) preview(voiceName string) (map[string]any, error) {
	if a.rt == nil {
		return nil, previewRefusal(previewNoRT)
	}
	if a.rt.IsOpen() {
		return nil, previewRefusal(previewBusy)
	}
	v := a.store.Current().Voice
	provider, model := v.Provider, v.Model()
	gen := a.takePreview(voiceName)
	if pcm, ok := a.previews.Cached(provider, model, voiceName); ok {
		if !a.playPreview(gen, voiceName, pcm) {
			return nil, previewRefusal(previewBusy)
		}
		return map[string]any{"state": "playing"}, nil
	}
	if _, ok := a.deps.Secrets.APIKey(provider); !ok {
		a.endPreview(gen)
		return nil, previewRefusal(previewNoKey)
	}
	go func() {
		pcm, err := a.previews.Get(context.Background(), provider, model, voiceName)
		switch {
		case errors.Is(err, voice.ErrPreviewCancelled):
			return // a newer preview or Stop took over: it reports for itself
		case errors.Is(err, voice.ErrPreviewNoKey):
			a.previewFailed(gen, voiceName, previewNoKey)
		case err != nil:
			a.previewFailed(gen, voiceName, "Couldn't make the preview: "+clipRunes(err.Error(), previewErrRunes))
		case !a.playPreview(gen, voiceName, pcm):
			a.previewFailed(gen, voiceName, previewBusy)
		}
	}()
	a.emit("voice.preview", map[string]any{"voice": voiceName, "state": "loading"})
	return map[string]any{"state": "loading"}, nil
}

// takePreview makes voiceName the preview, stopping any before it, and returns its gen.
func (a *App) takePreview(voiceName string) int {
	p := &a.previewing
	p.mu.Lock()
	old := p.voice
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.gen++
	gen := p.gen
	p.voice = voiceName
	p.mu.Unlock()
	a.previews.Stop()
	a.rt.StopClip()
	if old != "" {
		a.emit("voice.preview", map[string]any{"voice": old, "state": "done"})
	}
	return gen
}

// playPreview plays pcm if gen is still the preview and Pixie is at rest, and reports it.
func (a *App) playPreview(gen int, voiceName string, pcm []int16) bool {
	p := &a.previewing
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen != gen {
		return true // taken over: nothing to say
	}
	if !a.rt.PlayClip(pcm) {
		p.voice = ""
		return false
	}
	d := time.Duration(len(pcm)) * time.Second / audio.SampleRate
	p.timer = time.AfterFunc(d+300*time.Millisecond, func() { a.endPreview(gen) })
	a.emit("voice.preview", map[string]any{"voice": voiceName, "state": "playing"})
	return true
}

// endPreview reports gen's preview done, if it is still the preview.
func (a *App) endPreview(gen int) {
	p := &a.previewing
	p.mu.Lock()
	if p.gen != gen || p.voice == "" {
		p.mu.Unlock()
		return
	}
	voiceName := p.voice
	p.voice, p.timer = "", nil
	p.mu.Unlock()
	a.emit("voice.preview", map[string]any{"voice": voiceName, "state": "done"})
}

func (a *App) previewFailed(gen int, voiceName, msg string) {
	p := &a.previewing
	p.mu.Lock()
	if p.gen != gen {
		p.mu.Unlock()
		return
	}
	p.voice = ""
	p.mu.Unlock()
	a.emit("voice.preview", map[string]any{"voice": voiceName, "state": "error", "error": msg})
}

// stopPreview stops the preview being made or played.
func (a *App) stopPreview() {
	if a.rt == nil {
		return
	}
	a.takePreview("")
	p := &a.previewing
	p.mu.Lock()
	p.voice = ""
	p.mu.Unlock()
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
