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
	// previewLead is how much of a clip being made is gathered before it starts playing (0.4 s),
	// so the service sending it barely faster than it plays doesn't make it stutter.
	previewLead = 2 * audio.SampleRate / 5
	// previewTail is how long after a clip leaves the queue the row says done: the speaker
	// takes the queue a little ahead of playing it.
	previewTail = 300 * time.Millisecond
)

// previewRefusal is why a preview can't play, in words for the picker to show as they are.
type previewRefusal string

func (r previewRefusal) Error() string { return string(r) }
func (r previewRefusal) Code() string  { return "refused" }

// previewer is the one preview at a time. gen tells a preview's late results from the current
// one's; voice is the voice whose row shows loading or playing ("" when none does).
type previewer struct {
	mu     sync.Mutex
	gen    int
	voice  string
	cancel context.CancelFunc // ends the clip being made
	timer  *time.Timer        // ends "playing" when the clip has played
}

func (a *App) setupPreviews() {
	a.previews = &voice.Previews{
		Dir:     filepath.Join(a.paths.AppSupport, "voice", "previews"),
		Secrets: a.deps.Secrets,
		Log:     a.voiceLog,
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
// kept, else once the service has made it. The result is the state it starts in; a refusal
// sends no event (the picker shows the refusal itself).
func (a *App) preview(voiceName string) (map[string]any, error) {
	if a.rt == nil {
		return nil, previewRefusal(previewNoRT)
	}
	if a.rt.IsOpen() {
		return nil, previewRefusal(previewBusy)
	}
	v := a.store.Current().Voice
	provider, model := v.Provider, v.Model()
	pcm, cached := a.previews.Cached(provider, model, voiceName)
	if !cached {
		if _, ok := a.deps.Secrets.APIKey(provider); !ok {
			return nil, previewRefusal(previewNoKey)
		}
	}
	gen, ctx := a.takePreview(voiceName)
	if cached {
		if !a.playPreview(gen, voiceName, pcm) {
			return nil, previewRefusal(previewBusy)
		}
		return map[string]any{"state": "playing"}, nil
	}
	// Before the work starts, so a quick failure can't overtake it in the event queue.
	a.emit("voice.preview", map[string]any{"voice": voiceName, "state": "loading"})
	go a.makeAndPlay(ctx, gen, provider, model, voiceName)
	return map[string]any{"state": "loading"}, nil
}

// makeAndPlay has the service make voiceName's clip and plays it as it comes: once previewLead
// of it is in, then each piece as it arrives.
func (a *App) makeAndPlay(ctx context.Context, gen int, provider, model, voiceName string) {
	var lead []int16
	var playing, refused bool
	start := func(pcm []int16) {
		playing = a.startClip(gen, voiceName, pcm)
		refused = !playing
	}
	onAudio := func(piece []int16) {
		switch {
		case refused:
		case playing:
			a.appendClip(gen, piece)
		default:
			if lead = append(lead, piece...); len(lead) >= previewLead {
				start(lead)
			}
		}
	}
	_, err := a.previews.Get(ctx, provider, model, voiceName, onAudio)
	if err == nil && !playing && !refused && len(lead) > 0 {
		start(lead) // a clip shorter than the lead
	}
	switch {
	case errors.Is(err, voice.ErrPreviewCancelled):
		a.endPreview(gen) // a newer preview or Stop took over: nothing, unless it's still ours
	case errors.Is(err, voice.ErrPreviewNoKey):
		a.previewFailed(gen, voiceName, previewNoKey)
	case err != nil:
		a.previewFailed(gen, voiceName, "Couldn't make the preview: "+clipRunes(err.Error(), previewErrRunes))
	case !playing:
		a.previewFailed(gen, voiceName, previewBusy) // nothing if a newer preview took over
	default:
		a.finishClip(gen)
	}
}

// startClip starts playing gen's clip with its first audio and reports it; false when a newer
// preview took over or Pixie isn't at rest.
func (a *App) startClip(gen int, voiceName string, pcm []int16) bool {
	p := &a.previewing
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen != gen || !a.rt.PlayClip(pcm) {
		return false
	}
	a.emit("voice.preview", map[string]any{"voice": voiceName, "state": "playing"})
	return true
}

// appendClip plays more of gen's clip, while it is still the preview.
func (a *App) appendClip(gen int, pcm []int16) {
	p := &a.previewing
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen == gen {
		a.rt.AppendClip(pcm)
	}
}

// finishClip ends gen's preview once the rest of its clip has played: what is still queued,
// checked again when that time is up (the last piece may still be on its way to the queue, and
// the speaker drains the queue a little ahead of playing it).
func (a *App) finishClip(gen int) {
	p := &a.previewing
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.gen != gen {
		return
	}
	var check func()
	check = func() {
		p.mu.Lock()
		if p.gen != gen {
			p.mu.Unlock()
			return
		}
		if left := a.rt.ClipLeft(); left > 0 {
			p.timer = time.AfterFunc(left+previewTail, check)
			p.mu.Unlock()
			return
		}
		p.mu.Unlock()
		a.endPreview(gen)
	}
	p.timer = time.AfterFunc(a.rt.ClipLeft()+previewTail, check)
}

// takePreview makes voiceName the preview ("" for none), ending the one before it (its clip
// being made is cancelled, its sound stopped, its row told done). It returns the new gen and
// the context its clip is made under.
func (a *App) takePreview(voiceName string) (int, context.Context) {
	p := &a.previewing
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	old := p.voice
	if p.cancel != nil {
		p.cancel()
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	p.gen++
	gen := p.gen
	p.voice, p.cancel, p.timer = voiceName, cancel, nil
	a.rt.StopClip() // under p.mu, so it can't land after a newer preview's PlayClip
	p.mu.Unlock()
	if old != "" {
		a.emit("voice.preview", map[string]any{"voice": old, "state": "done"})
	}
	return gen, ctx
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
	p.timer = time.AfterFunc(d+previewTail, func() { a.endPreview(gen) })
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

// previewFailed reports gen's preview failed, and stops what of it was already playing (a
// service that failed partway), so the row's error isn't said over by the voice.
func (a *App) previewFailed(gen int, voiceName, msg string) {
	p := &a.previewing
	p.mu.Lock()
	if p.gen != gen {
		p.mu.Unlock()
		return
	}
	p.voice = ""
	a.rt.StopClip()
	p.mu.Unlock()
	a.emit("voice.preview", map[string]any{"voice": voiceName, "state": "error", "error": msg})
}

// stopPreview stops the preview being made or played.
func (a *App) stopPreview() {
	if a.rt == nil {
		return
	}
	a.takePreview("")
}

// closePreviews cancels a clip being made, at quit.
func (a *App) closePreviews() {
	p := &a.previewing
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	if p.timer != nil {
		p.timer.Stop()
	}
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
