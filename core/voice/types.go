// Package voice holds vendor-neutral voice types shared by the conversation core and adapters.
// Port of ai-mascot's voice-core types.rs and traits.rs.
package voice

import (
	"context"
	"encoding/json"
)

type MascotState string

const (
	StateIdle       MascotState = "idle"
	StateListening  MascotState = "listening"
	StateThinking   MascotState = "thinking"
	StateDelegating MascotState = "delegating"
	StateHappy      MascotState = "happy"
	StateError      MascotState = "error"
)

// TurnDetection decides who determines the user has finished speaking.
type TurnDetection int

const (
	TurnSemantic TurnDetection = iota
	TurnServerVAD
	TurnManual
)

type ToolSpec struct {
	Name, Description string
	Parameters        json.RawMessage // JSON schema of the arguments object
}

type SessionConfig struct {
	Model, Voice, Instructions string
	Tools                      []ToolSpec
	TurnDetection              TurnDetection
	NoisyRoom                  bool
	ProviderNoiseReduction     bool
}

type EventKind int

const (
	EvReady EventKind = iota
	EvSpeechStarted
	EvSpeechStopped
	EvAudioDelta
	EvResponseCreated
	EvResponseDone
	EvToolCall
	EvTranscript
	EvError
)

// Event is what a session reports. Adapters emit a response's tool calls before its EvResponseDone.
type Event struct {
	Kind     EventKind
	Audio    []int16         // EvAudioDelta
	CallID   string          // EvToolCall
	ToolName string          // EvToolCall
	Args     json.RawMessage // EvToolCall
	Role     string          // EvTranscript: "user" | "assistant"
	Text     string          // EvTranscript
	Fatal    bool            // EvError
	Message  string          // EvError
}

type CommandKind int

const (
	CmdAudio CommandKind = iota
	CmdCancelResponse
	CmdToolResult
	CmdInject
	CmdClose
)

type Command struct {
	Kind    CommandKind
	Audio   []int16         // CmdAudio
	HeardMs int64           // CmdCancelResponse: how much of the reply actually played
	CallID  string          // CmdToolResult
	Output  json.RawMessage // CmdToolResult
	Respond bool            // CmdToolResult / CmdInject: ask the model to speak again
	Text    string          // CmdInject
}

// Session is a live voice session. Events closing means the session ended.
type Session interface {
	Send(Command) error
	Events() <-chan Event
	Close() error
}

// Model is a speech-to-speech provider.
type Model interface {
	Name() string
	Connect(ctx context.Context, cfg SessionConfig, apiKey string) (Session, error)
}
