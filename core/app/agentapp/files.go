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

	"github.com/sihekuang/deskpixie/core/agent"
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

// StateHookTimeouts are the status hooks a Claude agent also gets on a session host that
// detects no status itself (tmux): UserPromptSubmit says working, Stop (a second hook, after
// the one carrying the reply) idle, Notification blocked; all post to /hooks/state.
var StateHookTimeouts = []HookTimeout{{"UserPromptSubmit", 5}, {"Stop", 5}, {"Notification", 5}}

// stateNotifications are the Notification types that mean the agent waits on the owner: a
// permission prompt or an MCP elicitation, not Claude's "still waiting for your input" reminder
// (that one comes after Stop, while the agent is idle).
const stateNotifications = "permission_prompt|elicitation_dialog"

// WithStateHooks adds the StateHookTimeouts hooks to a rendered settings file (its port is
// the one the file's hooks already use).
func WithStateHooks(text string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("not JSON: %v", err)
	}
	hooks, ok := v["hooks"].(map[string]any)
	if !ok {
		return "", errors.New("no hooks")
	}
	stop, _ := get(v, "hooks", "Stop", 0, "hooks", 0, "url").(string)
	base, ok := strings.CutSuffix(stop, "/stop")
	if !ok {
		return "", errors.New("no Stop hook")
	}
	for _, h := range StateHookTimeouts {
		group := map[string]any{"hooks": []any{map[string]any{
			"type":           "http",
			"url":            base + "/state",
			"timeout":        json.Number(strconv.FormatUint(h.Secs, 10)),
			"headers":        map[string]any{"Authorization": "Bearer $PIXIE_AGENT_TOKEN"},
			"allowedEnvVars": []any{"PIXIE_AGENT_TOKEN"},
		}}}
		if h.Event == "Notification" {
			group["matcher"] = stateNotifications
		}
		groups, _ := hooks[h.Event].([]any)
		hooks[h.Event] = append(groups, group)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ValidateStateHooks: each StateHookTimeouts event has an http hook to this port's
// /hooks/state that carries the bearer only by env reference.
func ValidateStateHooks(text string, port uint16) error {
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return fmt.Errorf("not JSON: %v", err)
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/hooks/state", port)
	for _, h := range StateHookTimeouts {
		found := false
		groups, _ := get(v, "hooks", h.Event).([]any)
		for _, g := range groups {
			list, _ := get(g, "hooks").([]any)
			for _, item := range list {
				hook, _ := item.(map[string]any)
				env, _ := hook["allowedEnvVars"].([]any)
				found = found || (hook["type"] == "http" && hook["url"] == url &&
					get(hook, "headers", "Authorization") == "Bearer $PIXIE_AGENT_TOKEN" && len(env) == 1 && env[0] == "PIXIE_AGENT_TOKEN")
			}
		}
		if !found {
			return fmt.Errorf("no %s state hook", h.Event)
		}
	}
	return nil
}

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

// The one rule of coordinator.md that differs per agent ({{ASK_RULE}}): its tool for asking the
// user a question, which the unattended pane must never use.
const (
	ClaudeAskRule = "Do not use the AskUserQuestion tool; ask with `status=\"question\"` instead."
	CodexAskRule  = "Do not use the request_user_input tool; ask with `status=\"question\"` instead."
)

// RenderClaudeMd is the coordinator's CLAUDE.md. projectsRoot is set when workers are on: the
// repository section then says to start workers there; otherwise that it can't edit
// repositories.
func RenderClaudeMd(canvas, projectsRoot string) string {
	return renderCoordinatorMd(canvas, projectsRoot, ClaudeAskRule)
}

// RenderAgentsMd is the same prompt as AGENTS.md, which a Codex coordinator reads (#87).
func RenderAgentsMd(canvas, projectsRoot string) string {
	return renderCoordinatorMd(canvas, projectsRoot, CodexAskRule)
}

func renderCoordinatorMd(canvas, projectsRoot, askRule string) string {
	repoWork := RepoWorkSolo
	if projectsRoot != "" {
		repoWork = strings.ReplaceAll(RepoWorkWorkers, "{{PROJECTS_ROOT}}", projectsRoot)
	}
	md := strings.ReplaceAll(CoordinatorMd, "{{REPO_WORK}}", strings.TrimRightFunc(repoWork, isSpace))
	md = strings.ReplaceAll(md, "{{ASK_RULE}}", askRule)
	return strings.ReplaceAll(md, "{{CANVAS_DIR}}", canvas)
}

func isSpace(r rune) bool { return r == ' ' || r == '\n' || r == '\t' || r == '\r' }

// ValidateSettings: the settings must be valid JSON whose hooks are exactly hooks' events, as
// loopback HTTP hooks on port that carry the bearer only by env reference, each with its
// timeout; they must deny AskUserQuestion (#91) and allow the pixie MCP tools.
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
		if get(hook, "headers", "Authorization") != "Bearer $PIXIE_AGENT_TOKEN" || len(env) != 1 || env[0] != "PIXIE_AGENT_TOKEN" {
			return fmt.Errorf("%s hook must send Bearer $PIXIE_AGENT_TOKEN", h.Event)
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
	if !slices.Contains(allow, any("mcp__pixie")) {
		return errors.New("permissions.allow must include mcp__pixie")
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

// Install is InstallFor without the state hooks (Herdr).
func Install(paths Paths, port uint16, projectsRoot string) error {
	return InstallFor(paths, port, projectsRoot, false)
}

// InstallFor creates the folders and (re)writes CLAUDE.md and both settings files when they
// differ. projectsRoot is set when workers are on. stateHooks adds the status hooks
// (WithStateHooks) for a session host that detects no status itself (tmux).
func InstallFor(paths Paths, port uint16, projectsRoot string, stateHooks bool) error {
	for _, dir := range []string{paths.Root, paths.Workspace, paths.Canvas} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
	}
	if err := writeIfChanged(filepath.Join(paths.Workspace, "CLAUDE.md"), RenderClaudeMd(paths.Canvas, projectsRoot)); err != nil {
		return err
	}
	// Both, whatever the coordinator is now: each agent reads only its own (#87).
	if err := writeIfChanged(filepath.Join(paths.Workspace, "AGENTS.md"), RenderAgentsMd(paths.Canvas, projectsRoot)); err != nil {
		return err
	}
	workerRules := WorkerAllowRules(projectsRoot)
	if projectsRoot == "" {
		workerRules = nil // workers are off
	}
	for _, f := range []struct {
		path  string
		text  string
		hooks []HookTimeout
		rules []string
	}{
		{paths.Settings, RenderSettings(port), HookTimeouts, CoordinatorAllowRules(paths.Workspace, paths.Canvas)},
		{paths.WorkerSettings, RenderWorkerSettings(port), WorkerHookTimeouts, workerRules},
	} {
		text, hooks := f.text, f.hooks
		if len(f.rules) > 0 {
			var err error
			if text, err = WithAllowRules(text, f.rules); err != nil {
				return err
			}
		}
		if stateHooks {
			var err error
			if text, err = WithStateHooks(text); err != nil {
				return err
			}
			hooks = append(slices.Clone(hooks), StateHookTimeouts...)
			if err := ValidateStateHooks(text, port); err != nil {
				return err
			}
		}
		if err := ValidateSettings(text, port, hooks); err != nil {
			return err
		}
		if err := writeIfChanged(f.path, text); err != nil {
			return err
		}
	}
	return nil
}

// CoordinatorKind is agent.coordinatorKind as an agent kind (Claude for anything else: the
// settings window refuses other values).
func CoordinatorKind(setting string) agent.AgentKind {
	if k, ok := agent.ParseAgentKind(setting); ok {
		return k
	}
	return agent.Claude
}

// CoordinatorRecipeFor is the coordinator's launch for kind, without its env: Claude Code with
// CoordinatorArgs (claudeModel), or Codex with agent.CodexCoordinatorArgs (codexModel; the
// hooks on the command line, stateHooks for tmux) and its start checks. A blank model is none.
func CoordinatorRecipeFor(kind agent.AgentKind, paths Paths, claudeModel, codexModel string, port uint16, stateHooks bool) agent.CoordinatorRecipe {
	if kind != agent.Codex {
		return agent.CoordinatorRecipe{Kind: agent.Claude, Cwd: paths.Workspace, Args: CoordinatorArgs(paths, claudeModel, true)}
	}
	return agent.CoordinatorRecipe{
		Kind:     agent.Codex,
		Cwd:      paths.Workspace,
		Args:     agent.CodexCoordinatorArgs(paths.Canvas, port, codexModel, stateHooks),
		Refusals: agent.CodexCoordinatorRefusals(paths.Workspace),
		Ready:    agent.CodexCoordinatorReady(),
	}
}

// KeptFor is what the last quit left running that this run adopts: a kept coordinator of
// another kind than kind is not (its pane is replaced by a start of kind); workers are.
func KeptFor(kept agent.KeptAgents, kind agent.AgentKind) agent.KeptAgents {
	if kept.CoordinatorKind != kind {
		kept.CoordinatorToken, kept.CoordinatorKind = "", 0
	}
	return kept
}

// CoordinatorArgs are Claude Code's arguments for the coordinator (AgentHost adds --continue
// on re-creation). With mcp it also gets the pixie MCP server (--mcp-config; the app always
// passes it: worker tools when workers are on, Pixie's local tools always). A blank
// model is none.
func CoordinatorArgs(paths Paths, model string, mcp bool) []string {
	args := []string{"--permission-mode", "auto", "--settings", paths.Settings, "--add-dir", paths.Canvas}
	if mcp {
		args = append(args, "--mcp-config", paths.CoordinatorMcp())
	}
	if m := strings.TrimSpace(model); m != "" {
		args = append(args, "--model", m)
	}
	return args
}
