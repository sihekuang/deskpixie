package agentapp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/proc"
)

// managedSettingsPath is where an organization's Claude Code policy lives on macOS.
const managedSettingsPath = "/Library/Application Support/ClaudeCode/managed-settings.json"

var permissionModeLine = regexp.MustCompile(`--permission-mode[^\n]*\n?[^\n]*`)
var autoChoice = regexp.MustCompile(`["'(, ]auto["',) ]`)

// claudeAuto asks `claude --help` (3 s limit) whether --permission-mode offers "auto". claude
// is found the way the session host's panes find it: on the PATH extended with the usual install
// dirs (a GUI app's own PATH is minimal), and run with the session host's scrubbed environment.
// Unknown when claude isn't found or the run fails or times out.
func claudeAuto(home string, environ []string, exists func(string) bool, run func(bin string, env []string) (string, error)) agent.ClaudeAuto {
	parent, pathVar := parentEnv(HostDeps{Environ: environ})
	bin, ok := proc.FindProgram("claude", "", pathVar, proc.InstallDirs(home), exists)
	if !ok {
		return agent.ClaudeAutoUnknown
	}
	var env []string
	for _, e := range proc.ServerEnv(parent, home, nil) {
		env = append(env, e.Key+"="+e.Value)
	}
	help, err := run(bin, env)
	if err != nil {
		return agent.ClaudeAutoUnknown
	}
	if helpListsAuto(help) {
		return agent.ClaudeListsAuto
	}
	return agent.ClaudeLacksAuto
}

// runHelp runs bin --help with exactly env, for at most 3 s.
func runHelp(bin string, env []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, "--help")
	c.Env = env
	out, err := c.Output()
	return string(out), err
}

func helpListsAuto(help string) bool {
	return autoChoice.MatchString(permissionModeLine.FindString(help))
}

// managedOff: the organization's managed settings set permissions.disableAutoMode to "disable"
// (a missing or unreadable file is false).
func managedOff() bool {
	b, err := os.ReadFile(managedSettingsPath)
	if err != nil {
		return false
	}
	var s struct {
		Permissions struct {
			DisableAutoMode string `json:"disableAutoMode"`
		} `json:"permissions"`
	}
	return json.Unmarshal(b, &s) == nil && s.Permissions.DisableAutoMode == "disable"
}
