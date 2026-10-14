package agentapp

// The agents' app-owned files (spec §5.2, §5.4, §5.5): the coordinator's workspace with a
// generated CLAUDE.md, the coordinator's and workers' --settings files, and the coordinator's
// 0600 --mcp-config. Nothing is ever written into a user's project (#75): all of it lives
// under the app-data folder and reaches Claude Code only through --settings, --mcp-config and
// the workspace cwd. Port of src-tauri/src/agent/files.rs.

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

var (
	//go:embed assets/coordinator-settings.json
	SettingsTemplate string
	//go:embed assets/worker-settings.json
	WorkerSettingsTemplate string
	//go:embed assets/coordinator.md
	CoordinatorMd string
	//go:embed assets/repo-work-solo.md
	RepoWorkSolo string
	//go:embed assets/repo-work-workers.md
	RepoWorkWorkers string
)

// HookTimeout is a hook event's required `timeout` in seconds.
type HookTimeout struct {
	Event string
	Secs  uint64
}

// HookTimeouts are the coordinator's hooks. PermissionRequest outlasts the hook server's own
// PermissionLimit (122 s), so the app's decision always lands before Claude Code gives up.
var HookTimeouts = []HookTimeout{{"Stop", 5}, {"PermissionRequest", 125}, {"PostToolUse", 5}}

// WorkerHookTimeouts are a worker's hooks: no PostToolUse (only its report_progress is
// narrated).
var WorkerHookTimeouts = []HookTimeout{{"Stop", 5}, {"PermissionRequest", 125}}

// Paths are the agent's files under the app-data folder.
type Paths struct {
	// Root is <app_data>/agent: jobs.jsonl, outbox.jsonl, the settings files.
	Root string
	// Workspace is <app_data>/agent/workspace: the coordinator's cwd.
	Workspace string
	// Settings is <app_data>/agent/coordinator-settings.json.
	Settings string
	// WorkerSettings is <app_data>/agent/worker-settings.json.
	WorkerSettings string
	// McpDir is <app_data>/agent/mcp: the 0600 --mcp-config files (coordinator.json, <worker>.json).
	McpDir string
	// Canvas is <app_data>/canvas (the canvas pages folder lives under it).
	Canvas string
}

// NewPaths lays the agent's files out under appData.
func NewPaths(appData string) Paths {
	root := filepath.Join(appData, "agent")
	return Paths{
		Root:           root,
		Workspace:      filepath.Join(root, "workspace"),
		Settings:       filepath.Join(root, "coordinator-settings.json"),
		WorkerSettings: filepath.Join(root, "worker-settings.json"),
		McpDir:         filepath.Join(root, "mcp"),
		Canvas:         filepath.Join(appData, "canvas"),
	}
}

// CoordinatorMcp is the coordinator's --mcp-config file.
func (p Paths) CoordinatorMcp() string { return filepath.Join(p.McpDir, "coordinator.json") }

func RenderSettings(port uint16) string {
	return strings.ReplaceAll(SettingsTemplate, "{{PORT}}", strconv.Itoa(int(port)))
}

func RenderWorkerSettings(port uint16) string {
	return strings.ReplaceAll(WorkerSettingsTemplate, "{{PORT}}", strconv.Itoa(int(port)))
}

// RenderClaudeMd is the coordinator's CLAUDE.md. projectsRoot is set when workers are on: the
// repository section then says to start workers there; otherwise that it can't edit
// repositories.
func RenderClaudeMd(canvas, projectsRoot string) string {
	repoWork := RepoWorkSolo
	if projectsRoot != "" {
		repoWork = strings.ReplaceAll(RepoWorkWorkers, "{{PROJECTS_ROOT}}", projectsRoot)
	}
	md := strings.ReplaceAll(CoordinatorMd, "{{REPO_WORK}}", strings.TrimRightFunc(repoWork, isSpace))
	return strings.ReplaceAll(md, "{{CANVAS_DIR}}", canvas)
}

func isSpace(r rune) bool { return r == ' ' || r == '\n' || r == '\t' || r == '\r' }

// ValidateSettings: the settings must be valid JSON whose hooks are exactly hooks' events, as
// loopback HTTP hooks on port that carry the bearer only by env reference, each with its
// timeout; they must deny AskUserQuestion (#91) and allow the mascot MCP tools.
func ValidateSettings(text string, port uint16, hooks []HookTimeout) error {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("not JSON: %v", err)
	}
	if dec.More() {
		return errors.New("not JSON: trailing characters")
	}
	// Exactly the hooks this file is meant to have: another event would run another hook.
	if declared, ok := get(v, "hooks").(map[string]any); ok {
		names := make([]string, 0, len(declared))
		for k := range declared {
			names = append(names, k)
		}
		slices.Sort(names) // serde_json's map order
		for _, event := range names {
			if !slices.ContainsFunc(hooks, func(h HookTimeout) bool { return h.Event == event }) {
				return fmt.Errorf("unexpected %s hook", event)
			}
		}
	}
	prefix := fmt.Sprintf("http://127.0.0.1:%d/hooks/", port)
	for _, h := range hooks {
		hook, ok := get(v, "hooks", h.Event, 0, "hooks", 0).(map[string]any)
		if !ok {
			return fmt.Errorf("no %s hook", h.Event)
		}
		url, _ := hook["url"].(string)
		if hook["type"] != "http" || !strings.HasPrefix(url, prefix) {
			return fmt.Errorf("%s hook is not http://127.0.0.1:%d", h.Event, port)
		}
		env, _ := hook["allowedEnvVars"].([]any)
		if get(hook, "headers", "Authorization") != "Bearer $MASCOT_AGENT_TOKEN" || len(env) != 1 || env[0] != "MASCOT_AGENT_TOKEN" {
			return fmt.Errorf("%s hook must send Bearer $MASCOT_AGENT_TOKEN", h.Event)
		}
		n, ok := hook["timeout"].(json.Number)
		secs, err := strconv.ParseUint(string(n), 10, 64)
		if !ok || err != nil || secs != h.Secs {
			return fmt.Errorf("%s hook timeout must be %d s", h.Event, h.Secs)
		}
	}
	deny, _ := get(v, "permissions", "deny").([]any)
	if !slices.Contains(deny, any("AskUserQuestion")) {
		return errors.New("permissions.deny must include AskUserQuestion")
	}
	allow, _ := get(v, "permissions", "allow").([]any)
	if !slices.Contains(allow, any("mcp__mascot")) {
		return errors.New("permissions.allow must include mcp__mascot")
	}
	return nil
}

// get walks v by object keys (string) and array indices (int); nil when absent.
func get(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[k]
		case int:
			a, ok := v.([]any)
			if !ok || k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

// writeIfChanged writes text atomically (Claude Code never reads a half-written file), only
// when the file differs.
func writeIfChanged(path, text string) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, []byte(text)) {
		return nil
	}
	tmp := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.deskpixie-%d.tmp", filepath.Base(path), os.Getpid()))
	err := os.WriteFile(tmp, []byte(text), 0o644)
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Install creates the folders and (re)writes CLAUDE.md and both settings files when they
// differ. projectsRoot is set when workers are on.
func Install(paths Paths, port uint16, projectsRoot string) error {
	for _, dir := range []string{paths.Root, paths.Workspace, paths.Canvas} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	if err := writeIfChanged(filepath.Join(paths.Workspace, "CLAUDE.md"), RenderClaudeMd(paths.Canvas, projectsRoot)); err != nil {
		return err
	}
	coordinator := RenderSettings(port)
	if err := ValidateSettings(coordinator, port, HookTimeouts); err != nil {
		return err
	}
	if err := writeIfChanged(paths.Settings, coordinator); err != nil {
		return err
	}
	worker := RenderWorkerSettings(port)
	if err := ValidateSettings(worker, port, WorkerHookTimeouts); err != nil {
		return err
	}
	return writeIfChanged(paths.WorkerSettings, worker)
}

// CoordinatorArgs are Claude Code's arguments for the coordinator (AgentHost adds --continue
// on re-creation). With workers on it also gets the mascot MCP server (--mcp-config). A blank
// model is none.
func CoordinatorArgs(paths Paths, model string, workers bool) []string {
	args := []string{"--permission-mode", "auto", "--settings", paths.Settings, "--add-dir", paths.Canvas}
	if workers {
		args = append(args, "--mcp-config", paths.CoordinatorMcp())
	}
	if m := strings.TrimSpace(model); m != "" {
		args = append(args, "--model", m)
	}
	return args
}
