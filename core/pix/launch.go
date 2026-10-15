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
const Instruction = "When your task changes, call set_description with a few words describing it."

// Launch is how pix starts the agent.
type Launch struct {
	Path  string
	Args  []string
	Env   []string // added to pix's own environment
	Files []string // per-run files, removed at exit
}

// hookEvents are the Claude hook events pix listens to.
var hookEvents = []string{"UserPromptSubmit", "PostToolUse", "Stop", "Notification"}

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
			hooks[ev] = []any{map[string]any{"hooks": []any{map[string]any{
				"type": "command", "command": strconv.Quote(self) + " hook " + ev, "timeout": 5,
			}}}}
		}
		settings, err := writePrivate(dir, "settings.json", map[string]any{"hooks": hooks})
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
			"-c", "developer_instructions=" + strconv.Quote(Instruction),
			// Codex runs notify with the turn's JSON as its last argument when a turn ends.
			"-c", "notify=" + string(notify),
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
