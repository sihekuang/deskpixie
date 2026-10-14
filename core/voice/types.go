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
	// BackendModel and BackendInstructions are for providers that run tools on a separate
	// backend model (GPT-Live's Responses delegation); the others ignore them.
	BackendModel, BackendInstructions string
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
	Failure  *Failure        // EvError with Fatal: the status line to show; nil = FailModelError
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

// Capabilities are what a provider can and can't do; see CapabilityReporter.
type Capabilities struct {
	// Truncate: CmdCancelResponse.HeardMs is meaningful (the model forgets what wasn't heard).
	Truncate bool
	// OwnsTurnTaking: the provider decides barge-in and whether to speak after a tool result;
	// its speech and response events are synthesized.
	OwnsTurnTaking bool
}

// CapabilityReporter is optional for a Model; models without it get {Truncate: true}.
type CapabilityReporter interface{ Capabilities() Capabilities }

// CapabilitiesOf is m's capabilities.
func CapabilitiesOf(m Model) Capabilities {
	if r, ok := m.(CapabilityReporter); ok {
		return r.Capabilities()
	}
	return Capabilities{Truncate: true}
}

// Model is a speech-to-speech provider.
type Model interface {
	Name() string
	Connect(ctx context.Context, cfg SessionConfig, apiKey string) (Session, error)
}
