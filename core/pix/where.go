// Package pix is the `pix` terminal wrapper (#89): it runs an agent in its own PTY so
// DeskPixie can reach a session you started yourself, in any terminal.
package pix

import "strings"

// Where names the terminal or IDE pix runs in, shown with the session in Bit's list.
func Where(env func(string) string) string {
	if env("TERMINAL_EMULATOR") == "JetBrains-JediTerm" {
		if id := env("__CFBundleIdentifier"); strings.HasPrefix(id, "com.jetbrains.") {
			return strings.TrimPrefix(id, "com.jetbrains.")
		}
		return "JetBrains"
	}
	switch env("TERM_PROGRAM") {
	case "vscode":
		return "VS Code"
	case "iTerm.app":
		return "iTerm"
	case "Apple_Terminal":
		return "Terminal"
	}
	return "terminal"
}

// Nested: already inside a pix session or a Pixie-started agent; pix then just runs the tool.
func Nested(env func(string) string) bool {
	return env("PIX_SESSION") != "" || env("PIXIE_AGENT_TOKEN") != ""
}
