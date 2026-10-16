package pix

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// Instruction is the one line pix adds to the agent's instructions.
const Instruction = "This session runs under pix, so the user and their Pixie assistant can tell it apart from their other sessions by its description. When you start a task, and whenever your task changes, call set_description with 3 to 8 words naming the project and the task, e.g. \"deskpixie: fix login redirect loop\"."

// Launch is how pix starts the agent.
type Launch struct {
	Path  string
	Args  []string
	Env   []string // added to pix's own environment
	Files []string // per-run files, removed at exit
}

// hookEvents are the Claude hook events pix listens to; PreToolUse only for the question menu
// and PermissionRequest for the permission dialogs (#105: the prompts a session waits on).
var hookEvents = []string{"SessionStart", "UserPromptSubmit", "PostToolUse", "Stop", "Notification", "PreToolUse", "PermissionRequest"}

// hookMatchers narrows an event to these tools, or a Notification to the prompts that wait on
// you: not idle_prompt, which comes after a minute idle and would hold every message (#144).
var hookMatchers = map[string]string{"PreToolUse": "AskUserQuestion", "Notification": "permission_prompt|elicitation_dialog"}

// PlanLaunch builds the per-run additions for tool (nothing global is edited); userArgs
// follow them unchanged. self is this pix's path, sock its local socket, dir a private
// folder for the per-run files.
func PlanLaunch(tool string, userArgs []string, self, sock, dir string) (Launch, error) {
	path, err := exec.LookPath(tool)
	if err != nil {
		path = tool // the start then fails with the usual "not found"
	}
	env := []string{"PIX_SESSION=1", "PIX_SOCK=" + sock}
	switch tool {
	case "claude":
		hooks := map[string]any{}
		for _, ev := range hookEvents {
			entry := map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": strconv.Quote(self) + " hook " + ev, "timeout": 5,
			}}}
			if m := hookMatchers[ev]; m != "" {
				entry["matcher"] = m
			}
			hooks[ev] = []any{entry}
		}
		settings, err := writePrivate(dir, "settings.json", map[string]any{
			"hooks": hooks,
			// Only pix's own tool, so describing the work never stops to ask you.
			"permissions": map[string]any{"allow": []string{"mcp__pixie__set_description"}},
		})
		if err != nil {
			return Launch{}, err
		}
		mcp, err := writePrivate(dir, "mcp.json", map[string]any{"mcpServers": map[string]any{
			"pixie": map[string]any{"command": self, "args": []string{"mcp"}},
		}})
		if err != nil {
			return Launch{}, err
		}
		args := []string{"--settings", settings, "--mcp-config", mcp, "--append-system-prompt", Instruction}
		return Launch{Path: path, Args: append(args, userArgs...), Env: env, Files: []string{settings, mcp}}, nil
	case "codex":
		notify, _ := json.Marshal([]string{self, "hook", "Stop"})
		args := []string{
			"-c", "mcp_servers.pixie.command=" + strconv.Quote(self),
			"-c", `mcp_servers.pixie.args=["mcp"]`,
			// Its own tool never asks for approval (Claude's settings allow it the same way).
			"-c", `mcp_servers.pixie.tools.set_description.approval_mode="approve"`,
			"-c", "developer_instructions=" + strconv.Quote(Instruction),
			// Codex runs notify with the turn's JSON as its last argument when a turn ends.
			"-c", "notify=" + string(notify),
			// Its approval prompts arrive as its own OSC 9 notification (#105), which pix reads;
			// text on screen can't fake one. Always: also while you look at the terminal.
			"-c", `tui.notification_method="osc9"`,
			"-c", `tui.notification_condition="always"`,
		}
		return Launch{Path: path, Args: append(args, userArgs...), Env: env}, nil
	}
	return Launch{}, fmt.Errorf("pix: unknown tool %q (use claude or codex)", tool)
}

func writePrivate(dir, name string, v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, name)
	return p, os.WriteFile(p, b, 0o600)
}
