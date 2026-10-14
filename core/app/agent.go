package app

import (
	"github.com/sihekuang/deskpixie/core/settings"
	"github.com/sihekuang/deskpixie/core/voice"
)

// The delegation agent as the app sees it. core/app/agentapp builds it (the composition root
// that names the concrete agent types); the app only calls these methods. Port of the agent
// touchpoints in ai-mascot src-tauri/src/{lib.rs, actions.rs, tray.rs, voice/sink.rs}.

// AgentVoice is what the agent needs of the voice loop (*voice.Runtime).
type AgentVoice interface {
	Inject(voice.Notice)
	HoldOpen(on bool)
	Subscribe() <-chan voice.VoiceSignal
}

// AgentHooks are what the agent may do to the app. Each is safe from any goroutine.
type AgentHooks struct {
	// Celebrate (a job done) and FlashError (a job failed): Bit's one-shots.
	Celebrate, FlashError func()
	// RefreshTray: the agent's tray rows or status changed.
	RefreshTray func()
	// Notify shows a macOS notification (no sound: the agent chimes on its own).
	Notify func(title, body string)
}

// Agent is the running delegation agent.
type Agent interface {
	// Tools are the voice tools (delegate, jobs, answer_job); Persona the persona section.
	Tools() []voice.Tool
	Persona() string
	// Tray is Tray ▸ Agent: job rows, the status line and the standing hint.
	Tray() AgentTray
	// Menu runs an agent:* menu id; false for an id that is not the agent's.
	Menu(id string) bool
	// OpenTerminal is a click on Bit: attach a terminal to the agent session (async).
	OpenTerminal()
	// AttachVoice links the voice loop once it exists.
	AttachVoice(v AgentVoice)
	// OnMouth is each mouth level the voice loop reports (the echo gate).
	OnMouth(level int)
	// Shutdown is the app's quit: deny pending permissions, stop the panes, clear the MCP
	// configs, stop the session host and the hook server. Idempotent; blocks ~3 s at most.
	Shutdown()
}

// AgentFactory builds the agent from settings; nil when agent.enabled is false.
type AgentFactory func(cfg settings.Config, appSupport string, hooks AgentHooks) Agent
