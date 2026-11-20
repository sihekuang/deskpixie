// Package pix is the `pix` terminal wrapper (#89): it runs an agent in its own PTY so
// DeskPixie can reach a session you started yourself, in any terminal.
package pix

import "strings"

// Where names the terminal or IDE pix runs in, shown with the session in Bit's list. PIX_WHERE
// wins: DeskPixie sets it on the Herdr tab it resumes a session in (#461).
func Where(env func(string) string) string {
	if w := env("PIX_WHERE"); w != "" {
		return w
	}
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

// ResumeID is the conversation `claude --resume <id>` (#461) or `codex resume <id>` (#468)
// resumes; "" for anything else. -r may carry a search term, and codex resume a session name,
// instead of an id, which is harmless: an unknown id changes nothing.
func ResumeID(tool string, args []string) string {
	if tool == "codex" {
		return codexResumeID(args)
	}
	if tool != "claude" {
		return ""
	}
	for i, a := range args {
		if id, ok := strings.CutPrefix(a, "--resume="); ok {
			return id
		}
		if (a == "--resume" || a == "-r") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			return args[i+1]
		}
	}
	return ""
}

// codexNoValue: Codex's flags around resume that take no value, so a word after one is not
// its value (`codex resume --all <id>`).
var codexNoValue = set("--all", "--include-non-interactive", "--oss", "--search", "--full-auto",
	"--dangerously-bypass-approvals-and-sandbox", "--yolo", "--no-alt-screen")

// codexResumeID is the id in `codex [flags] resume [flags] <id> [prompt]`; "" for the picker or
// --last (the most recent, whichever that is).
func codexResumeID(args []string) string {
	var words []string // the arguments that are neither a flag nor a flag's value
	for i, a := range args {
		switch {
		case a == "--last":
			return ""
		case strings.HasPrefix(a, "-"):
			continue
		case i > 0 && strings.HasPrefix(args[i-1], "-") && !strings.Contains(args[i-1], "=") && !codexNoValue[args[i-1]]:
			continue // likely the flag's value (-c model="o3")
		}
		words = append(words, a)
	}
	if len(words) >= 2 && words[0] == "resume" {
		return words[1]
	}
	return ""
}

// AppID is the bundle id of the app pix runs in: macOS sets __CFBundleIdentifier for an app and
// its terminals inherit it (WebStorm's terminal: com.jetbrains.WebStorm). "" when unknown.
func AppID(env func(string) string) string { return env("__CFBundleIdentifier") }

// Nested: already inside a pix session or a Pixie-started agent; pix then just runs the tool.
func Nested(env func(string) string) bool {
	return env("PIX_SESSION") != "" || env("PIXIE_AGENT_TOKEN") != ""
}

// Subcommands that are not an interactive session (from `claude --help` 2.1.300 and
// `codex --help` 0.162.1); codex resume and fork open one, so they are wrapped.
var oneShot = map[string]map[string]bool{
	"claude": set("agents", "attach", "auth", "auto-mode", "doctor", "gateway", "import", "install", "logs", "mcp",
		"plugin", "plugins", "purge", "respawn", "rm", "setup-token", "stop", "kill", "ultrareview", "update", "upgrade"),
	"codex": set("agents", "exec", "e", "review", "login", "logout", "mcp", "plugin", "app-server", "remote-control", "app",
		"completion", "update", "doctor", "sandbox", "debug", "apply", "a", "queue", "archive", "delete",
		"migrate-rollouts", "unarchive", "cloud", "exec-server", "features", "help"),
}

func set(names ...string) map[string]bool {
	m := map[string]bool{}
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Passthrough: run the tool as it is, without a PTY or a Pixie session. That is a scripted or
// one-shot run (stdin or stdout not a terminal, `claude -p`, a subcommand, --version/--help),
// so `alias claude='pix claude'` changes nothing for it.
func Passthrough(tool string, args []string, stdinTTY, stdoutTTY bool) bool {
	if !stdinTTY || !stdoutTTY {
		return true
	}
	for i, a := range args {
		switch {
		case a == "--version" || a == "-v" || a == "--help" || a == "-h":
			return true
		case tool == "claude" && (a == "-p" || a == "--print"):
			return true
		case strings.HasPrefix(a, "-"):
			continue
		case oneShot[tool][a]:
			return true
		case i > 0 && strings.HasPrefix(args[i-1], "-") && !strings.Contains(args[i-1], "="):
			continue // likely the flag's value (-m o3)
		default:
			return false // a prompt: the session starts
		}
	}
	return false
}
