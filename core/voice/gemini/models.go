package gemini

// Which Gemini model and voice a session uses. Pure. Port of voice-gemini/src/models.rs.
//
// voice.model and voice.voice in settings default to OpenAI values (gpt-realtime, marin).
// Switching only voice.provider to gemini-live must still work, so anything that is not a
// Gemini model id or a Gemini voice is replaced by the defaults here and never sent to Google.
//
// Models: https://ai.google.dev/gemini-api/docs/models lists gemini-3.8-live as the stable
// default Live model (gemini-3.1-flash-live-preview is the legacy preview).
// Voices: https://ai.google.dev/gemini-api/docs/speech-generation (Live native-audio models
// accept the same prebuilt voices as text-to-speech).

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sihekuang/deskpixie/core/voice"
)

const (
	// DefaultAPIURL is where the model list is (GET /v1beta/models).
	DefaultAPIURL = "https://generativelanguage.googleapis.com"
	DefaultModel  = "gemini-3.8-live"
	// DefaultVoice is "Upbeat": suits a small cheerful pixie.
	DefaultVoice = "Puck"
)

// Voices are the 30 prebuilt voices, in Google's order.
var Voices = []string{
	"Zephyr", "Puck", "Charon", "Kore", "Fenrir", "Leda", "Orus", "Aoede", "Callirrhoe", "Autonoe",
	"Enceladus", "Iapetus", "Umbriel", "Algieba", "Despina", "Erinome", "Algenib", "Rasalgethi",
	"Laomedeia", "Achernar", "Alnilam", "Schedar", "Gacrux", "Pulcherrima", "Achird",
	"Zubenelgenubi", "Vindemiatrix", "Sadachbia", "Sadaltager", "Sulafat",
}

type SubstitutionKind int

const (
	SubModel SubstitutionKind = iota
	SubVoice
)

// Substitution is a setting the adapter replaced, for a one-time log line.
type Substitution struct {
	Kind            SubstitutionKind
	Requested, Used string
}

func (s Substitution) Describe() string {
	if s.Kind == SubModel {
		return fmt.Sprintf("voice.model %q is not a Gemini model; using %s", s.Requested, s.Used)
	}
	return fmt.Sprintf("voice.voice %q is not a Gemini voice; using %s", s.Requested, s.Used)
}

// IsModel reports whether id (without models/) is a Gemini Live conversation model:
// gemini-… with live or native-audio in it, not a transcriber or translator. Checked against
// /v1beta/models on 2026-11-22; ListModels also wants bidiGenerateContent.
func IsModel(id string) bool {
	return strings.HasPrefix(id, "gemini-") &&
		(strings.Contains(id, "live") || strings.Contains(id, "native-audio")) &&
		!strings.Contains(id, "transcribe") && !strings.Contains(id, "translate")
}

// GeminiModel is the model id without a models/ prefix, if it names a Gemini Live model.
func GeminiModel(requested string) (string, bool) {
	id := strings.TrimSpace(requested)
	id = strings.TrimPrefix(id, "models/")
	return id, IsModel(id)
}

// GeminiVoice is the canonical spelling of a Gemini voice name (case-insensitive match).
func GeminiVoice(requested string) (string, bool) {
	r := strings.TrimSpace(requested)
	for _, v := range Voices {
		if strings.EqualFold(v, r) {
			return v, true
		}
	}
	return "", false
}

// Resolve returns cfg with a Gemini model and voice, plus what had to be replaced.
func Resolve(cfg voice.SessionConfig) (voice.SessionConfig, []Substitution) {
	out := cfg
	var subs []Substitution
	if id, ok := GeminiModel(cfg.Model); ok {
		out.Model = id
	} else {
		subs = append(subs, Substitution{Kind: SubModel, Requested: cfg.Model, Used: DefaultModel})
		out.Model = DefaultModel
	}
	if v, ok := GeminiVoice(cfg.Voice); ok {
		out.Voice = v
	} else {
		subs = append(subs, Substitution{Kind: SubVoice, Requested: cfg.Voice, Used: DefaultVoice})
		out.Voice = DefaultVoice
	}
	return out, subs
}

// listPages bounds how many pages ListModels follows.
const listPages = 20

// ListModels is the Live models apiKey can use (GET /v1beta/models, every page), the
// default first. The key goes in a header, never the URL.
func (m *model) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, voice.ListTimeout)
	defer cancel()
	var ids []string
	token := ""
	for range listPages {
		q := url.Values{"pageSize": {"1000"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var page struct {
			Models []struct {
				Name    string   `json:"name"`
				Methods []string `json:"supportedGenerationMethods"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		err := voice.GetJSON(ctx, m.client, "gemini models", m.apiURL+"/v1beta/models?"+q.Encode(),
			http.Header{"X-Goog-Api-Key": {apiKey}}, &page)
		if err != nil {
			return nil, err
		}
		for _, md := range page.Models {
			if id := strings.TrimPrefix(md.Name, "models/"); IsModel(id) && slices.Contains(md.Methods, "bidiGenerateContent") {
				ids = append(ids, id)
			}
		}
		if page.NextPageToken == "" || page.NextPageToken == token {
			break
		}
		token = page.NextPageToken
	}
	return voice.OrderModels(ids, DefaultModel), nil
}
