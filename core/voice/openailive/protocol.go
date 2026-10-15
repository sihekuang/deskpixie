package openailive

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

type obj = map[string]any

// livePolicies is appended to the instructions GPT-Live speaks from: the labelled policies
// OpenAI's live prompting guide asks for.
const livePolicies = `Speaking (GPT-Live):
- Backchannel policy: keep backchannels ("mm-hm", "right") rare and short, and never talk over the user while they are giving you a task.
- Interruption policy: when the user starts talking over you, stop and listen, unless it is only a short acknowledgement like "yeah" or "ok".%s
- Delegation policy: every tool named above (get_time, get_weather, calculate, timer, media, end_conversation, delegate, jobs, answer_job and any others) runs through your backend. Whenever these instructions say to call a tool, delegate to the backend before you answer, and never guess the answer while you wait. When the backend's result arrives, say it in one or two short sentences.
- Read any confirm phrase ("Say confirm <word>") exactly as written, word for word.`

const noisyRoomPolicy = ` The room is noisy: ignore quieter voices in the background, like a TV or people nearby, and only stop for the user speaking clearly to you.`

// backendPreamble starts the backend model's instructions; Pixie's full instructions follow, so
// the tool rules (when to delegate, jobs, answer_job, end_conversation) are the same as on the
// other providers.
const backendPreamble = `You are the backend for Pixie, a voice assistant. A separate voice model talks with the user and hands you the conversation whenever a tool may be needed. Choose and call the right tools by the rules below, then answer in one or two short, plain, spoken-style sentences for the voice to say. Call end_conversation only when the user has clearly said goodbye.

Pixie's instructions:
`

// sessionStart builds the session.start message. The config is strict: no unknown fields.
func sessionStart(cfg voice.SessionConfig, eventID string) obj {
	model := cfg.Model
	if model == "" {
		model = DefaultModel
	}
	var v any = cfg.Voice
	switch {
	case cfg.Voice == "":
		v = DefaultVoice
	case strings.HasPrefix(cfg.Voice, "voice_"):
		v = obj{"id": cfg.Voice}
	}
	noisy := ""
	if cfg.NoisyRoom {
		noisy = noisyRoomPolicy
	}
	live := strings.TrimSpace(cfg.Instructions + "\n\n" + strings.Replace(livePolicies, "%s", noisy, 1))
	backendModel := cfg.BackendModel
	if backendModel == "" {
		backendModel = DefaultBackendModel
	}
	backend := cfg.BackendInstructions
	if backend == "" {
		backend = cfg.Instructions
	}
	tools := make([]any, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, obj{"type": "function", "name": t.Name, "description": t.Description, "parameters": params})
	}
	responses := obj{
		"model":        backendModel,
		"instructions": backendPreamble + backend,
		"tools":        tools,
	}
	if len(tools) > 0 {
		responses["tool_choice"] = "auto"
		responses["parallel_tool_calls"] = true
	}
	return obj{
		"type":     "session.start",
		"event_id": eventID,
		"session": obj{
			"model":        model,
			"instructions": live,
			"audio": obj{
				"format": obj{"type": "audio/pcm", "rate": audio.SampleRate},
				"output": obj{"voice": v},
			},
			"delegation": obj{"type": "responses", "responses": responses},
			"store":      false,
		},
	}
}

func pcmToBase64(pcm []int16) string { return base64.StdEncoding.EncodeToString(audio.EncodeLE(pcm)) }

// serverMsg is every server event's fields that the codec reads.
type serverMsg struct {
	Type         string `json:"type"`
	Delta        string `json:"delta"`
	StartMs      *int64 `json:"start_ms"`
	EndMs        *int64 `json:"end_ms"`
	Reason       string `json:"reason"`
	DelegationID string `json:"delegation_id"`
	Delegation   struct {
		ID     string `json:"id"`
		Target string `json:"target"`
	} `json:"delegation"`
	Event struct {
		Type string `json:"type"`
		Item struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
		} `json:"item"`
		Response struct {
			Status string `json:"status"`
			Error  *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	} `json:"event"`
	Usage struct {
		Seconds float64 `json:"seconds"`
	} `json:"usage"`
	Error apiError `json:"error"`
}

type apiError struct {
	Type          string `json:"type"`
	Code          string `json:"code"`
	Message       string `json:"message"`
	Param         string `json:"param"`
	ClientEventID string `json:"client_event_id"`
}

func (e apiError) String() string {
	s := e.Code
	if s == "" {
		s = e.Type
	}
	if e.Param != "" {
		s += " (" + e.Param + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// startupFailure maps an error before session.started to the tray failure.
func startupFailure(e apiError) voice.Failure {
	switch e.Code {
	case "invalid_api_key", "authentication_error":
		return voice.SetupFailure("OpenAI rejected the API key")
	case "model_not_found", "permission_denied", "insufficient_permissions", "unsupported_model":
		return voice.Failure{Kind: voice.FailNoAccess, Msg: "GPT-Live"}
	case "rate_limit_exceeded", "insufficient_quota", "session_limit_exceeded":
		return voice.SetupFailure("OpenAI quota or rate limit reached")
	}
	switch e.Type {
	case "authentication_error":
		return voice.SetupFailure("OpenAI rejected the API key")
	case "permission_error":
		return voice.Failure{Kind: voice.FailNoAccess, Msg: "GPT-Live"}
	}
	return voice.Failure{Kind: voice.FailModelError}
}

// httpFailure maps a failed WebSocket upgrade's HTTP status to the tray failure.
func httpFailure(status int) (voice.Failure, bool) {
	switch status {
	case 401:
		return voice.SetupFailure("OpenAI rejected the API key"), true
	case 403, 404:
		return voice.Failure{Kind: voice.FailNoAccess, Msg: "GPT-Live"}, true
	case 429:
		return voice.SetupFailure("OpenAI quota or rate limit reached"), true
	}
	return voice.Failure{}, false
}

// closedFailure maps session.closed's reason; ok=false for a close we asked for.
func closedFailure(reason string) (voice.Failure, bool) {
	switch reason {
	case "close_requested":
		return voice.Failure{}, false
	case "expired":
		return voice.Failure{Kind: voice.FailModelError, Msg: "GPT-Live session expired"}, true
	case "content":
		return voice.Failure{Kind: voice.FailModelError, Msg: "ended by the safety filter"}, true
	}
	return voice.Failure{Kind: voice.FailConnectionLost}, true
}

// connectError is a Connect error that carries its tray failure (voice.FailureError).
type connectError struct {
	msg string
	f   voice.Failure
}

func (e *connectError) Error() string          { return e.msg }
func (e *connectError) Failure() voice.Failure { return e.f }
