package proc

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/voice"
)

// Never passed to a session host's server (every pane inherits its environment): agents run on
// the subscription, never on an API key, and never think they are inside another Claude Code or
// Herdr session. Every voice provider's key (OPENAI_API_KEY, GEMINI_API_KEY, …) is scrubbed
// too, from voice's provider table (voice.KeyVars).
var scrubExact = []string{"CLAUDECODE", "CLAUDE_PID", "CLAUDE_EFFORT"}

// ANTHROPIC_* covers keys, tokens, base URLs, custom headers and ANTHROPIC_FOUNDRY_*;
// CLAUDE_CODE_* covers CLAUDE_CODE_USE_{BEDROCK,VERTEX,FOUNDRY} and a parent session's ids;
// HERDR_* covers HERDR_SOCKET_PATH, which would point the CLI at another session.
var scrubPrefixes = []string{"ANTHROPIC_", "CLAUDE_CODE_", "HERDR_"}

// Scrubbed: key is never passed to a session host's server.
func Scrubbed(key string) bool {
	for _, k := range scrubExact {
		if k == key {
			return true
		}
	}
	for _, k := range voice.KeyVars() {
		if k == key {
			return true
		}
	}
	for _, p := range scrubPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// ServerEnv is a session host server's (and every CLI call's) exact environment: parent minus
// the Scrubbed keys and drop, PATH (from parent) extended with InstallDirs (a GUI app's PATH is
// minimal), plus extra; sorted.
func ServerEnv(parent []agent.EnvVar, home string, drop []string, extra ...agent.EnvVar) []agent.EnvVar {
	path, hasPath := "", false
	var out []agent.EnvVar
	for _, e := range parent {
		switch {
		case e.Key == "PATH":
			path, hasPath = e.Value, true
		case Scrubbed(e.Key):
		case containsString(drop, e.Key):
		default:
			out = append(out, e)
		}
	}
	if !hasPath {
		path = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	out = append(out, agent.EnvVar{Key: "PATH", Value: ExtendedPath(path, home)})
	out = append(out, extra...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ExtendedPath is current with InstallDirs appended (those not already in it).
func ExtendedPath(current, home string) string {
	dirs := strings.Split(current, ":")
	for _, extra := range InstallDirs(home) {
		if !containsString(dirs, extra) {
			dirs = append(dirs, extra)
		}
	}
	return strings.Join(dirs, ":")
}

// InstallDirs is where herdr, tmux and claude are usually installed, beyond a GUI app's minimal
// PATH.
func InstallDirs(home string) []string {
	return []string{filepath.Join(home, ".local/bin"), "/opt/homebrew/bin", "/usr/local/bin"}
}

// FindProgram: configured ("" for none) if it exists, else name on pathVar, else in dirs.
func FindProgram(name, configured, pathVar string, dirs []string, exists func(string) bool) (string, bool) {
	if configured != "" {
		return configured, exists(configured)
	}
	var candidates []string
	for _, d := range strings.Split(pathVar, ":") {
		if d != "" {
			candidates = append(candidates, filepath.Join(d, name))
		}
	}
	for _, d := range dirs {
		candidates = append(candidates, filepath.Join(d, name))
	}
	for _, p := range candidates {
		if exists(p) {
			return p, true
		}
	}
	return "", false
}

// IsNotFound: err is a runner's start failure for a program that isn't there (ENOENT, "(os
// error 2)").
func IsNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "(os error 2)")
}
