package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Codex as the coordinator (#87, docs/specs/2026-10-15-codex-coordinator-design.md). Codex has
// command hooks only (no http hooks), so each hook is a curl that posts its stdin to the hook
// server. The hooks and their trust come by -c overrides, Codex's session-flags layer: Codex
// reads hook trust from that layer, so exactly these hooks are trusted, for this one process,
// and nothing is written into ~/.codex.

// CodexSessionFlagsSource is the source file Codex 0.162 gives hooks from -c overrides; a
// hook's trust key is `<source>:<event label>:<group>:<handler>`.
const CodexSessionFlagsSource = "/<session-flags>/config.toml"

// CodexHook is one hook the app gives a Codex agent.
type CodexHook struct {
	// Event is Codex's event name (Stop); Label its name in trust keys (stop).
	Event, Label string
	// Matcher is the tool-name regex ("" matches every call).
	Matcher string
	// Route is the hook server's route (/hooks/<route>).
	Route   string
	Timeout uint64
}

// codexPostToolMatcher is the coordinator's PostToolUse matcher, as for Claude Code (Codex's
// apply_patch also answers to Edit and Write).
const codexPostToolMatcher = "Bash|Edit|Write|mcp__pixie__start_worker|mcp__mascot__start_worker"

// CodexCoordinatorHooks are the coordinator's hooks: Stop carries the reply, PermissionRequest
// goes to the permission flow, PostToolUse narrates. stateHooks adds the status hooks for a
// host that detects no status itself (tmux): UserPromptSubmit says working, a second Stop idle.
func CodexCoordinatorHooks(stateHooks bool) []CodexHook {
	hooks := []CodexHook{
		{Event: "Stop", Label: "stop", Route: "stop", Timeout: 5},
		// Outlasts the hook server's own limit (122 s), as for Claude Code.
		{Event: "PermissionRequest", Label: "permission_request", Route: "permission", Timeout: 125},
		{Event: "PostToolUse", Label: "post_tool_use", Matcher: codexPostToolMatcher, Route: "post-tool", Timeout: 5},
	}
	if stateHooks {
		hooks = append(hooks,
			CodexHook{Event: "UserPromptSubmit", Label: "user_prompt_submit", Route: "state", Timeout: 5},
			CodexHook{Event: "Stop", Label: "stop", Route: "state", Timeout: 5},
		)
	}
	return hooks
}

// CodexHookCommand posts the hook's stdin to the hook server. curl reads the bearer from
// PIXIE_AGENT_TOKEN itself (--variable), so the token is never on a command line.
func CodexHookCommand(port uint16, route string) string {
	return fmt.Sprintf(`/usr/bin/curl -sS --variable %%%s --expand-header "Authorization: Bearer {{%s}}" -H "Content-Type: application/json" --data-binary @- http://127.0.0.1:%d/hooks/%s`,
		TokenEnv, TokenEnv, port, route)
}

// CodexHookHash is the trust hash Codex 0.162 computes for a command hook: SHA-256 of the
// compact, key-sorted JSON of {event_name, matcher?, hooks: [{type, command, timeout, async}]}
// (codex-rs hooks/src/engine/discovery.rs hook_hash, config/src/fingerprint.rs).
func CodexHookHash(h CodexHook, command string) string {
	handler := map[string]any{"async": false, "command": command, "timeout": h.Timeout, "type": "command"}
	identity := map[string]any{"event_name": h.Label, "hooks": []any{handler}}
	if h.Matcher != "" {
		identity["matcher"] = h.Matcher
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // serde_json escapes only what JSON requires
	if err := enc.Encode(identity); err != nil {
		panic(err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// tomlString is s as a TOML basic string (JSON's escapes are TOML's).
func tomlString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		panic(err)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

// CodexHookArgs are the -c overrides for hooks on port: one `hooks.<Event>=[…]` per event
// (its groups in order), then `hooks.state={…}` trusting each of them.
func CodexHookArgs(port uint16, hooks []CodexHook) []string {
	var events []string
	groups := map[string][]string{}
	var trust []string
	for _, h := range hooks {
		if _, ok := groups[h.Event]; !ok {
			events = append(events, h.Event)
		}
		command := CodexHookCommand(port, h.Route)
		group := ""
		if h.Matcher != "" {
			group = "matcher=" + tomlString(h.Matcher) + ","
		}
		group = fmt.Sprintf(`{%shooks=[{type="command",command=%s,timeout=%d}]}`, group, tomlString(command), h.Timeout)
		key := fmt.Sprintf("%s:%s:%d:0", CodexSessionFlagsSource, h.Label, len(groups[h.Event]))
		groups[h.Event] = append(groups[h.Event], group)
		trust = append(trust, fmt.Sprintf("%s={trusted_hash=%s}", tomlString(key), tomlString(CodexHookHash(h, command))))
	}
	var args []string
	for _, e := range events {
		args = append(args, "-c", "hooks."+e+"=["+strings.Join(groups[e], ",")+"]")
	}
	sort.Strings(trust)
	return append(args, "-c", "hooks.state={"+strings.Join(trust, ",")+"}")
}

// CodexCoordinatorArgs are Codex's arguments for the coordinator (AgentHost puts `resume
// --last` first on a resume): its own process (--no-daemon: hooks and the MCP bearer need this
// pane's environment), writes to the workspace (its cwd) and the canvas, network on as for the
// Claude coordinator, escalations through the PermissionRequest hook, the pixie MCP server with
// the bearer from PIXIE_AGENT_TOKEN, and the hooks. A blank model is none.
func CodexCoordinatorArgs(canvas string, port uint16, model string, stateHooks bool) []string {
	args := []string{
		"--no-daemon",
		"-s", "workspace-write", "-a", "on-request",
		"--add-dir", canvas,
		"-c", "sandbox_workspace_write.network_access=true",
		"-c", "check_for_update_on_startup=false",
		"-c", fmt.Sprintf("mcp_servers.%s.url=%s", McpServer, McpURL(port)),
		"-c", fmt.Sprintf("mcp_servers.%s.bearer_token_env_var=%s", McpServer, TokenEnv),
		"-c", fmt.Sprintf("mcp_servers.%s.default_tools_approval_mode=\"approve\"", McpServer),
	}
	args = append(args, CodexHookArgs(port, CodexCoordinatorHooks(stateHooks))...)
	if m := strings.TrimSpace(model); m != "" {
		args = append(args, "-m", m)
	}
	return args
}

// CodexHooksReviewScreens is Codex 0.162's "Hooks need review" screen at start.
var CodexHooksReviewScreens = [][]string{{"Hooks need review", "Trust all and continue"}}

// CodexHooksReviewText is why a Codex coordinator start fails on the hooks review screen: the
// app's own hooks come trusted, so the user's hooks (or a Codex that changed how it trusts
// hooks) put it up. Nothing is typed into it.
const CodexHooksReviewText = "Codex asks to review hooks before it starts. Run `codex`, review them with /hooks, then restart the coordinator."

// CodexWorkspaceTrustText is why it fails on Codex's folder-trust screen ({dir}: the workspace).
const CodexWorkspaceTrustText = "Codex asks to trust the coordinator's workspace. Run `codex` once in {dir} and choose Trust, then restart the coordinator."

// CodexCoordinatorRefusals are the screens that fail a Codex coordinator's start.
func CodexCoordinatorRefusals(workspace string) []StartRefusal {
	return []StartRefusal{
		{Screens: [][]string{CodexHooksReviewScreens[0]}, Reason: CodexHooksReviewText},
		{Screens: CodexTrustScreensOwned(), Reason: strings.ReplaceAll(CodexWorkspaceTrustText, "{dir}", workspace)},
	}
}

// CodexCoordinatorNotReady is why it fails when Codex's input box never showed. Unlike a
// worker's, it names no screen line: it becomes the tray status, which is logged.
const CodexCoordinatorNotReady = "Codex didn't become ready; open the coordinator terminal to see why."

// CodexCoordinatorReady is what a Codex coordinator start waits for (as a Codex worker's).
func CodexCoordinatorReady() *StartReady {
	return &StartReady{Marker: CodexIdleMarker, Reason: CodexCoordinatorNotReady, Settle: CodexReadySettle}
}
