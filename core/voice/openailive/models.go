// Package openailive adapts OpenAI GPT-Live (wss://api.openai.com/v1/live/sessions) to
// voice.Model. GPT-Live is full duplex and owns turn-taking: it has no speech, response or
// truncate events, so the adapter synthesizes turns from audio and transcripts (codec.go).
// Pixie's tools run on a Responses backend model (Responses delegation).
// Design: docs/specs/2026-10-13-gpt-live-provider-design.md.
package openailive

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/sihekuang/deskpixie/core/voice"
)

const (
	// Name is the provider's registry and secrets name (key: OPENAI_API_KEY).
	Name       = "openai-live"
	DefaultURL = "wss://api.openai.com/v1/live/sessions"
	// DefaultAPIURL is where the model list is (GET /v1/models).
	DefaultAPIURL = "https://api.openai.com"
	// DefaultModel is the first GPT-Live model; the account's others come from ListModels.
	DefaultModel = "gpt-live-1"
	// DefaultVoice is GPT-Live's default voice.
	DefaultVoice = "marin"
	// DefaultBackendModel is the Responses model that runs Pixie's tools (OpenAI's "start here").
	DefaultBackendModel = "gpt-6-luna"
)

// Voices are GPT-Live's built-in voices. A custom voice id ("voice_…") also works.
var Voices = []string{
	"alloy", "ash", "ballad", "beacon", "bossa", "brise", "cedar", "cinder", "coral", "delta",
	"echo", "flitz", "gleam", "harema", "juni", "marin", "meridian", "nira", "noeul", "nuri",
	"quartz", "ripple", "sage", "shida", "shimmer", "sillage", "stone", "tempo", "verse",
	"vesper", "willow",
}

// IsModel reports whether id is a GPT-Live conversation model: gpt-live-…, not the
// transcriber (gpt-live-transcribe). Checked against /v1/models on 2026-11-22.
func IsModel(id string) bool {
	return strings.HasPrefix(id, "gpt-live-") && !strings.Contains(id, "transcribe")
}

// Resolve returns cfg with a GPT-Live model and voice, plus a line for each setting it had to
// replace. Settings may still hold another provider's (gpt-realtime, Puck), which OpenAI
// refuses (#473); Gemini does the same (gemini.Resolve). An empty one takes the default
// without a note, and a custom voice id ("voice_…") stays.
func Resolve(cfg voice.SessionConfig) (voice.SessionConfig, []string) {
	var notes []string
	if m := strings.TrimSpace(cfg.Model); IsModel(m) {
		cfg.Model = m
	} else {
		if m != "" {
			notes = append(notes, fmt.Sprintf("voice.model %q is not a GPT-Live model; using %s", cfg.Model, DefaultModel))
		}
		cfg.Model = DefaultModel
	}
	v := strings.TrimSpace(cfg.Voice)
	switch i := slices.IndexFunc(Voices, func(n string) bool { return strings.EqualFold(n, v) }); {
	case strings.HasPrefix(v, "voice_"):
		cfg.Voice = v
	case i >= 0:
		cfg.Voice = Voices[i]
	default:
		if v != "" {
			notes = append(notes, fmt.Sprintf("voice.voice %q is not a GPT-Live voice; using %s", cfg.Voice, DefaultVoice))
		}
		cfg.Voice = DefaultVoice
	}
	return cfg, notes
}

// ListModels is the GPT-Live models apiKey can use (GET /v1/models), the default first.
func (m *model) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, voice.ListTimeout)
	defer cancel()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	err := voice.GetJSON(ctx, m.client, "openai models", m.apiURL+"/v1/models",
		http.Header{"Authorization": {"Bearer " + apiKey}}, &list)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, d := range list.Data {
		if IsModel(d.ID) {
			ids = append(ids, d.ID)
		}
	}
	return voice.OrderModels(ids, DefaultModel), nil
}
