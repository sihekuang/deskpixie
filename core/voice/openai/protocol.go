package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

type obj = map[string]any

func pcmFormat() obj { return obj{"type": "audio/pcm", "rate": 24000} }

// sessionUpdate builds the GA-shape session.update message.
func sessionUpdate(cfg voice.SessionConfig) obj {
	var td any
	switch {
	case cfg.TurnDetection == voice.TurnManual:
		td = nil
	case cfg.NoisyRoom:
		// A higher threshold than the default 0.5: a TV or people nearby no longer cut Pixie off.
		td = obj{"type": "server_vad", "threshold": 0.85, "prefix_padding_ms": 300, "silence_duration_ms": 500,
			"create_response": true, "interrupt_response": true}
	case cfg.TurnDetection == voice.TurnSemantic:
		td = obj{"type": "semantic_vad", "eagerness": "auto", "create_response": true, "interrupt_response": true}
	default:
		td = obj{"type": "server_vad", "create_response": true, "interrupt_response": true}
	}
	tools := make([]any, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, obj{"type": "function", "name": t.Name, "description": t.Description, "parameters": params})
	}
	input := obj{
		"format":         pcmFormat(),
		"transcription":  obj{"model": "gpt-4o-mini-transcribe"},
		"turn_detection": td,
	}
	if cfg.ProviderNoiseReduction {
		input["noise_reduction"] = obj{"type": "far_field"}
	}
	return obj{
		"type": "session.update",
		"session": obj{
			"type":              "realtime",
			"instructions":      cfg.Instructions,
			"output_modalities": []string{"audio"},
			"audio": obj{
				"input":  input,
				"output": obj{"format": pcmFormat(), "voice": cfg.Voice},
			},
			"tools":       tools,
			"tool_choice": "auto",
		},
	}
}

func pcmToBase64(pcm []int16) string { return base64.StdEncoding.EncodeToString(audio.EncodeLE(pcm)) }

// decoder turns server messages into neutral events, remembering what encode needs.
// Not safe for concurrent use; the session guards it with a mutex.
type decoder struct {
	ready          bool
	currentItem    string
	responseActive bool
	pendingByte    []byte // odd trailing byte of the previous delta
	// heard: each utterance's transcript so far, by item, for the live caption (#251).
	heard map[string]string
}

func (d *decoder) deltaToPCM(b64 string) ([]int16, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, false
	}
	b := append(append([]byte{}, d.pendingByte...), raw...)
	pcm, rest := audio.DecodeLE(b)
	d.pendingByte = append([]byte{}, rest...)
	return pcm, true
}

type serverMsg struct {
	Type       string `json:"type"`
	ItemID     string `json:"item_id"`
	Delta      string `json:"delta"`
	Transcript string `json:"transcript"`
	Error      struct {
		Code    string `json:"code"`
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	Response struct {
		Status        string `json:"status"`
		StatusDetails struct {
			Error struct {
				Code string `json:"code"`
				Type string `json:"type"`
			} `json:"error"`
		} `json:"status_details"`
		Output []struct {
			Type      string `json:"type"`
			Status    string `json:"status"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
		} `json:"output"`
	} `json:"response"`
}

func (d *decoder) decode(text []byte) []voice.Event {
	var m serverMsg
	if err := json.Unmarshal(text, &m); err != nil {
		return []voice.Event{{Kind: voice.EvError, Message: "unparseable server message"}}
	}
	switch m.Type {
	case "session.updated":
		if d.ready {
			return nil
		}
		d.ready = true
		return []voice.Event{{Kind: voice.EvReady}}
	case "input_audio_buffer.speech_started":
		return []voice.Event{{Kind: voice.EvSpeechStarted}}
	case "input_audio_buffer.speech_stopped":
		return []voice.Event{{Kind: voice.EvSpeechStopped}}
	case "response.created":
		d.responseActive = true
		d.currentItem = ""
		d.pendingByte = nil
		return []voice.Event{{Kind: voice.EvResponseCreated}}
	case "response.output_audio.delta":
		d.currentItem = m.ItemID
		pcm, ok := d.deltaToPCM(m.Delta)
		if !ok {
			return []voice.Event{{Kind: voice.EvError, Message: "bad audio delta"}}
		}
		return []voice.Event{{Kind: voice.EvAudioDelta, Audio: pcm}}
	case "response.output_audio_transcript.done":
		return []voice.Event{{Kind: voice.EvTranscript, Role: "assistant", Text: m.Transcript}}
	case "conversation.item.input_audio_transcription.delta":
		if strings.TrimSpace(m.Delta) == "" {
			return nil
		}
		if d.heard == nil {
			d.heard = map[string]string{}
		}
		d.heard[m.ItemID] += m.Delta
		return []voice.Event{{Kind: voice.EvUserHeard, Text: strings.TrimSpace(d.heard[m.ItemID])}}
	case "conversation.item.input_audio_transcription.completed":
		delete(d.heard, m.ItemID)
		return []voice.Event{{Kind: voice.EvTranscript, Role: "user", Text: m.Transcript}}
	case "response.done":
		d.responseActive = false
		completed := m.Response.Status == "completed"
		var out []voice.Event
		for _, it := range m.Response.Output {
			if !completed || it.Type != "function_call" || it.Status != "completed" {
				continue
			}
			args := json.RawMessage(it.Arguments)
			if !json.Valid(args) {
				args, _ = json.Marshal(it.Arguments)
			}
			out = append(out, voice.Event{Kind: voice.EvToolCall, CallID: it.CallID, ToolName: it.Name, Args: args})
		}
		out = append(out, voice.Event{Kind: voice.EvResponseDone})
		if m.Response.Status == "failed" {
			e := m.Response.StatusDetails.Error
			msg := e.Code
			if msg == "" {
				msg = e.Type
			}
			if msg == "" {
				msg = "response failed"
			}
			out = append(out, voice.Event{Kind: voice.EvError, Fatal: true, Message: msg})
		}
		return out
	case "error":
		if m.Error.Code == "response_cancel_not_active" {
			return nil
		}
		return []voice.Event{{Kind: voice.EvError, Message: fmt.Sprintf("%s: %s", m.Error.Code, m.Error.Message)}}
	}
	return nil
}

// encode maps a command to client messages, given what the decoder has seen.
func (d *decoder) encode(c voice.Command) []obj {
	switch c.Kind {
	case voice.CmdAudio:
		return []obj{{"type": "input_audio_buffer.append", "audio": pcmToBase64(c.Audio)}}
	case voice.CmdCancelResponse:
		var out []obj
		if d.responseActive {
			out = append(out, obj{"type": "response.cancel"})
		}
		if d.currentItem != "" {
			out = append(out, obj{"type": "conversation.item.truncate", "item_id": d.currentItem,
				"content_index": 0, "audio_end_ms": c.HeardMs})
		}
		return out
	case voice.CmdToolResult:
		out := []obj{{"type": "conversation.item.create",
			"item": obj{"type": "function_call_output", "call_id": c.CallID, "output": string(c.Output)}}}
		if c.Respond {
			out = append(out, obj{"type": "response.create"})
		}
		return out
	case voice.CmdInject:
		out := []obj{{"type": "conversation.item.create",
			"item": obj{"type": "message", "role": "user", "content": []obj{{"type": "input_text", "text": c.Text}}}}}
		if c.Respond {
			out = append(out, obj{"type": "response.create"})
		}
		return out
	}
	return nil
}
