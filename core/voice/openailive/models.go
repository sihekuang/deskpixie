// Package openailive adapts OpenAI GPT-Live (wss://api.openai.com/v1/live/sessions) to
// voice.Model. GPT-Live is full duplex and owns turn-taking: it has no speech, response or
// truncate events, so the adapter synthesizes turns from audio and transcripts (codec.go).
// Pixie's tools run on a Responses backend model (Responses delegation).
// Design: docs/specs/2026-10-13-gpt-live-provider-design.md.
package openailive

const (
	// Name is the provider's registry and secrets name (key: OPENAI_API_KEY).
	Name       = "openai-live"
	DefaultURL = "wss://api.openai.com/v1/live/sessions"
	// DefaultModel is the only GPT-Live model.
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
