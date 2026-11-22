package openai

// Which OpenAI Realtime model a session uses, and the account's list of them (#477).

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sihekuang/deskpixie/core/voice"
)

const (
	// DefaultAPIURL is where the model list is (GET /v1/models).
	DefaultAPIURL = "https://api.openai.com"
	DefaultModel  = "gpt-realtime"
)

// IsModel reports whether id is a Realtime conversation model: gpt-realtime…, not the
// translator, Whisper or transcriber. Checked against /v1/models on 2026-11-22.
func IsModel(id string) bool {
	if !strings.HasPrefix(id, "gpt-realtime") {
		return false
	}
	for _, not := range []string{"-translate", "-whisper", "-transcribe"} {
		if strings.Contains(id, not) {
			return false
		}
	}
	return true
}

// Resolve returns cfg with a Realtime model, plus a line when it had to replace the setting.
// Settings may hold another provider's (gpt-live-1, gemini-3.8-live), which OpenAI refuses;
// GPT-Live and Gemini do the same. An empty one takes the default without a note.
func Resolve(cfg voice.SessionConfig) (voice.SessionConfig, []string) {
	var notes []string
	if m := strings.TrimSpace(cfg.Model); IsModel(m) {
		cfg.Model = m
	} else {
		if m != "" {
			notes = append(notes, fmt.Sprintf("voice.openaiModel %q is not an OpenAI Realtime model; using %s", cfg.Model, DefaultModel))
		}
		cfg.Model = DefaultModel
	}
	return cfg, notes
}

// ListModels is the Realtime models apiKey can use (GET /v1/models), the default first.
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
