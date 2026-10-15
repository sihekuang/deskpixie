package agentapp

import (
	"github.com/sihekuang/deskpixie/core/agent/httpserver"
	"github.com/sihekuang/deskpixie/core/tools"
)

// coordinatorDescriptions word Pixie's local tools for the coordinator: the voice wording
// speaks of `delegate` and of Pixie's own conversation. Names and schemas stay the voice ones.
var coordinatorDescriptions = map[string]string{
	"timer": "Start, cancel or list countdown timers: the app's own timers, the same ones Pixie sets by voice. " +
		"When one ends the app tells the person itself (Pixie says it, or a chime and a notification), " +
		"even if you are busy, stopped or restarted. Use it for \"set a timer\" and \"remind me in N minutes\"; " +
		"never wait for one with sleep or a shell command. For a clock time (\"at 5 pm\"), call get_time first " +
		"and pass the seconds until then.",
	"get_weather": "Today's weather and forecast for a place; leave out location for the person's home city " +
		"(Pixie's settings). Answers in about a second: use it instead of web search for weather.",
	"media": "Control the music playing on this Mac (Apple Music or Spotify): play, pause, next, previous, " +
		"set the volume, or say what's playing. Use it instead of AppleScript or checking running apps.",
	"get_time": "The current time and date, here or in another place or time zone. Use it instead of the " +
		"date command or web search.",
	"calculate": "Exact arithmetic and unit conversion. Pass either `expression`, or `value` with `from` and " +
		"`to` units. Use it instead of working math out yourself or running a shell command.",
}

// coordinatorTool is a local tool with the coordinator's description.
type coordinatorTool struct {
	tools.Tool
	description string
}

func (c coordinatorTool) Description() string { return c.description }

// CoordinatorLocalTools are the app's local tools (the very instances the voice loop holds) as
// the coordinator's MCP server serves them.
func CoordinatorLocalTools(local []tools.Tool) []httpserver.LocalTool {
	out := make([]httpserver.LocalTool, 0, len(local))
	for _, t := range local {
		if d, ok := coordinatorDescriptions[t.Name()]; ok {
			out = append(out, coordinatorTool{Tool: t, description: d})
		} else {
			out = append(out, t)
		}
	}
	return out
}
