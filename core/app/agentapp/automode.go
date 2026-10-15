package agentapp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"time"
)

// managedSettingsPath is where an organization's Claude Code policy lives on macOS.
const managedSettingsPath = "/Library/Application Support/ClaudeCode/managed-settings.json"

var permissionModeLine = regexp.MustCompile(`--permission-mode[^\n]*\n?[^\n]*`)
var autoChoice = regexp.MustCompile(`["'(, ]auto["',) ]`)

// claudeListsAuto: `claude --help` names "auto" among the --permission-mode choices (3 s limit).
func claudeListsAuto() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--help").Output()
	if err != nil {
		return false
	}
	return helpListsAuto(string(out))
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
