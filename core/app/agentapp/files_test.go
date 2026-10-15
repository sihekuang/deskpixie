package agentapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/httpserver"
)

// Port of src-tauri/src/agent/files.rs tests (11).

func decodeJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// at walks v by keys (string) and indices (int).
func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			m, _ := v.(map[string]any)
			v = m[k]
		case int:
			a, _ := v.([]any)
			if k >= len(a) {
				return nil
			}
			v = a[k]
		}
	}
	return v
}

func TestTheShippedSettingsAreValidForAnyPort(t *testing.T) {
	if err := ValidateSettings(RenderSettings(48217), 48217, HookTimeouts); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSettings(RenderWorkerSettings(48217), 48217, WorkerHookTimeouts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(RenderSettings(5000), "http://127.0.0.1:5000/hooks/permission") {
		t.Fatal("port not substituted")
	}
	if strings.Contains(RenderSettings(5000), "{{PORT}}") {
		t.Fatal("placeholder left")
	}
}

func TestTheSettingsHoldNoSecretAndTheSpecRules(t *testing.T) {
	v := decodeJSON(t, RenderSettings(1))
	if at(v, "hooks", "PermissionRequest", 0, "hooks", 0, "timeout") != float64(125) {
		t.Fatal("permission timeout")
	}
	if at(v, "hooks", "Stop", 0, "hooks", 0, "timeout") != float64(5) {
		t.Fatal("stop timeout")
	}
	if at(v, "hooks", "PostToolUse", 0, "matcher") != "Bash|Edit|Write|mcp__pixie__start_worker|mcp__mascot__start_worker" {
		t.Fatal("matcher")
	}
	if !reflect.DeepEqual(at(v, "permissions", "allow"), []any{"mcp__pixie", "mcp__mascot"}) {
		t.Fatal("allow")
	}
	var ask []string
	for _, x := range at(v, "permissions", "ask").([]any) {
		ask = append(ask, x.(string))
	}
	for _, rule := range []string{"Bash(git push:*)", "Bash(rm -rf:*)", "Edit(**/.env*)", "Read(./.env*)"} {
		if !slices.Contains(ask, rule) {
			t.Error(rule)
		}
	}
	for _, r := range ask {
		if strings.HasPrefix(r, "Write(") {
			t.Error("Edit, not Write (#58)")
		}
	}
}

func TestBrokenSettingsAreRefused(t *testing.T) {
	if ValidateSettings("{", 1, HookTimeouts) == nil {
		t.Error("not JSON accepted")
	}
	if ValidateSettings(RenderSettings(1), 2, HookTimeouts) == nil {
		t.Error("wrong port accepted")
	}
	literal := strings.ReplaceAll(RenderSettings(1), "$PIXIE_AGENT_TOKEN", "abc123")
	if ValidateSettings(literal, 1, HookTimeouts) == nil {
		t.Error("literal token accepted")
	}
	noDeny := strings.ReplaceAll(RenderSettings(1), `"AskUserQuestion"`, `"Nothing"`)
	if ValidateSettings(noDeny, 1, HookTimeouts) == nil {
		t.Error("no deny accepted")
	}
	noAllow := strings.ReplaceAll(RenderSettings(1), `"mcp__pixie", "mcp__mascot"]`, `"Nothing"]`)
	if err := ValidateSettings(noAllow, 1, HookTimeouts); err == nil || err.Error() != "permissions.allow must include mcp__pixie" {
		t.Errorf("no allow: %v", err)
	}
}

// R-i: the app's 122 s permission decision must land before Claude Code gives up on the hook
// (125 s); the fire-and-forget hooks get 5 s.
func TestHookTimeoutsAreEnforced(t *testing.T) {
	found := false
	for _, h := range HookTimeouts {
		found = found || (h.Event == "PermissionRequest" && float64(h.Secs) > httpserver.PermissionLimit.Seconds())
	}
	if !found {
		t.Fatal("PermissionRequest must outlast PermissionLimit")
	}
	if !reflect.DeepEqual(HookTimeouts, []HookTimeout{{"Stop", 5}, {"PermissionRequest", 125}, {"PostToolUse", 5}}) {
		t.Fatal(HookTimeouts)
	}
	short := strings.ReplaceAll(RenderSettings(1), `"timeout": 125`, `"timeout": 120`)
	if short == RenderSettings(1) {
		t.Fatal("replacement did nothing")
	}
	if err := ValidateSettings(short, 1, HookTimeouts); err == nil || err.Error() != "PermissionRequest hook timeout must be 125 s" {
		t.Errorf("short: %v", err)
	}
	long := strings.Replace(RenderSettings(1), `"timeout": 5`, `"timeout": 30`, 1)
	if err := ValidateSettings(long, 1, HookTimeouts); err == nil || err.Error() != "Stop hook timeout must be 5 s" {
		t.Errorf("long: %v", err)
	}
	v := decodeJSON(t, RenderSettings(1))
	delete(at(v, "hooks", "PostToolUse", 0, "hooks", 0).(map[string]any), "timeout")
	none, _ := json.Marshal(v)
	if ValidateSettings(string(none), 1, HookTimeouts) == nil {
		t.Error("a missing timeout accepted")
	}
}

func TestInstallWritesClaudeMdAndSettingsIntoAppDataOnly(t *testing.T) {
	data := filepath.Join(t.TempDir(), "install")
	paths := NewPaths(data)
	if err := Install(paths, 48217, ""); err != nil {
		t.Fatal(err)
	}
	md := readFile(t, filepath.Join(paths.Workspace, "CLAUDE.md"))
	if !strings.Contains(md, filepath.Join(data, "canvas")+"/pages/<slug>.html") || !strings.Contains(md, "<spoken ref=") {
		t.Fatal(md)
	}
	if readFile(t, paths.Settings) != RenderSettings(48217) || readFile(t, paths.WorkerSettings) != RenderWorkerSettings(48217) {
		t.Fatal("settings not written")
	}
	if err := Install(paths, 48217, ""); err != nil { // idempotent
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Settings, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Install(paths, 48217, ""); err != nil {
		t.Fatal(err)
	}
	if readFile(t, paths.Settings) != RenderSettings(48217) {
		t.Fatal("not rewritten")
	}
	entries, _ := os.ReadDir(paths.Root)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatal("atomic writes leave no temp file:", e.Name())
		}
	}
	// R-k: everything it wrote is under the app-data folder.
	for _, p := range []string{paths.Root, paths.Workspace, paths.Settings, paths.WorkerSettings, paths.CoordinatorMcp()} {
		if !strings.HasPrefix(p, data+"/") {
			t.Error(p)
		}
	}
}

func TestClaudeMdSaysHowRepositoryWorkIsDone(t *testing.T) {
	solo := RenderClaudeMd("/data/canvas", "")
	if !strings.Contains(solo, "Workers that edit repositories") || strings.Contains(solo, "start_worker") {
		t.Fatal(solo)
	}
	with := RenderClaudeMd("/data/canvas", "/Users/x/Documents/Projects")
	for _, want := range []string{"`start_worker`", "under /Users/x/Documents/Projects", "never merge or push",
		"Give each worker a short `name`", "a new name for unrelated work", "a `pixie/…` branch"} {
		if !strings.Contains(with, want) {
			t.Error(want)
		}
	}
	if strings.Contains(with, "pixie/<name>") {
		t.Error("pixie/<name>")
	}
	for _, md := range []string{solo, with} {
		if strings.Contains(md, "{{") {
			t.Error("every placeholder is filled")
		}
	}
	if !strings.Contains(solo, "DeskPixie coordinator") || strings.Contains(solo, "AI Mascot") {
		t.Error("rename: the coordinator is the DeskPixie coordinator")
	}
}

func TestWorkerSettingsAreStrictAndCarryNoCoordinatorExtras(t *testing.T) {
	v := decodeJSON(t, RenderWorkerSettings(1))
	if _, ok := at(v, "hooks").(map[string]any)["PostToolUse"]; ok {
		t.Fatal("PostToolUse")
	}
	if !reflect.DeepEqual(at(v, "permissions", "allow"), []any{"mcp__pixie", "mcp__mascot"}) {
		t.Fatal("allow")
	}
	// The coordinator's timeouts do not fit a worker without PostToolUse, and vice versa.
	if ValidateSettings(RenderWorkerSettings(1), 1, HookTimeouts) == nil {
		t.Error("worker as coordinator")
	}
	if ValidateSettings(RenderWorkerSettings(2), 1, WorkerHookTimeouts) == nil {
		t.Error("wrong port")
	}
	short := strings.ReplaceAll(RenderWorkerSettings(1), `"timeout": 125`, `"timeout": 120`)
	if ValidateSettings(short, 1, WorkerHookTimeouts) == nil {
		t.Error("short")
	}
	literal := strings.ReplaceAll(RenderWorkerSettings(1), "$PIXIE_AGENT_TOKEN", "abc123")
	if ValidateSettings(literal, 1, WorkerHookTimeouts) == nil {
		t.Error("literal")
	}
	noDeny := strings.ReplaceAll(RenderWorkerSettings(1), `"AskUserQuestion"`, `"Nothing"`)
	if ValidateSettings(noDeny, 1, WorkerHookTimeouts) == nil {
		t.Error("no deny")
	}
}

func TestWorkersAskAndDenyWhatTheCoordinatorDoes(t *testing.T) {
	worker := decodeJSON(t, RenderWorkerSettings(1))
	coordinator := decodeJSON(t, RenderSettings(1))
	if !reflect.DeepEqual(at(worker, "permissions", "ask"), at(coordinator, "permissions", "ask")) {
		t.Error("ask")
	}
	if !reflect.DeepEqual(at(worker, "permissions", "deny"), at(coordinator, "permissions", "deny")) {
		t.Error("deny")
	}
}

func TestAHookEventTheFileDoesNotExpectIsRefused(t *testing.T) {
	v := decodeJSON(t, RenderWorkerSettings(1))
	hooks := at(v, "hooks").(map[string]any)
	hooks["PostToolUse"] = hooks["Stop"]
	b, _ := json.Marshal(v)
	if err := ValidateSettings(string(b), 1, WorkerHookTimeouts); err == nil || err.Error() != "unexpected PostToolUse hook" {
		t.Errorf("worker: %v", err)
	}
	v = decodeJSON(t, RenderSettings(1))
	hooks = at(v, "hooks").(map[string]any)
	hooks["SessionStart"] = hooks["Stop"]
	b, _ = json.Marshal(v)
	if err := ValidateSettings(string(b), 1, HookTimeouts); err == nil || err.Error() != "unexpected SessionStart hook" {
		t.Errorf("coordinator: %v", err)
	}
}

// The MCP config the coordinator is started with points at loopback only.
func TestTheMcpConfigPointsAtLoopbackOnly(t *testing.T) {
	v := decodeJSON(t, agent.McpConfigJSON(48217, "tok"))
	servers := v["mcpServers"].(map[string]any)
	if len(servers) != 1 {
		t.Fatal(servers)
	}
	for _, s := range servers {
		if !strings.HasPrefix(at(s, "url").(string), "http://127.0.0.1:48217/") {
			t.Fatal(s)
		}
	}
}

func TestCoordinatorArgsUseAppDataPaths(t *testing.T) {
	paths := NewPaths("/data")
	want := []string{"--permission-mode", "auto", "--settings", "/data/agent/coordinator-settings.json", "--add-dir", "/data/canvas"}
	if got := CoordinatorArgs(paths, "", false); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if got := CoordinatorArgs(paths, "haiku", false); got[len(got)-1] != "haiku" {
		t.Fatal(got)
	}
	if got := CoordinatorArgs(paths, "  ", false); !reflect.DeepEqual(got, want) {
		t.Fatal("a blank model is none:", got)
	}
	with := CoordinatorArgs(paths, "", true)
	if !reflect.DeepEqual(with[6:], []string{"--mcp-config", "/data/agent/mcp/coordinator.json"}) {
		t.Fatal(with)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTheCoordinatorClosesAWorkerOnceItsWorkIsDone(t *testing.T) {
	for _, phrase := range []string{"Close a worker as soon as its work is done", "call `stop_worker` right away", "the same `name`"} {
		if !strings.Contains(RepoWorkWorkers, phrase) {
			t.Fatal(phrase)
		}
	}
}

// stateHook is the url and bearer of each http hook on event that posts to the state route.
func stateHooks(t *testing.T, text, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	groups, _ := at(decodeJSON(t, text), "hooks", event).([]any)
	for _, g := range groups {
		hooks, _ := at(g, "hooks").([]any)
		for _, h := range hooks {
			m, _ := h.(map[string]any)
			if url, _ := m["url"].(string); strings.HasSuffix(url, "/hooks/state") {
				out = append(out, m)
			}
		}
	}
	return out
}

// Under tmux (which detects no status) Claude agents report working / idle / blocked through
// their own hooks; under Herdr the files are exactly the shipped ones.
func TestUnderTmuxTheClaudeSettingsAlsoReportTheirState(t *testing.T) {
	for name, tc := range map[string]struct {
		render func(uint16) string
		hooks  []HookTimeout
	}{
		"coordinator": {RenderSettings, HookTimeouts},
		"worker":      {RenderWorkerSettings, WorkerHookTimeouts},
	} {
		text, err := WithStateHooks(tc.render(4000))
		if err != nil {
			t.Fatal(name, err)
		}
		if err := ValidateSettings(text, 4000, append(slices.Clone(tc.hooks), StateHookTimeouts...)); err != nil {
			t.Fatal(name, err)
		}
		if err := ValidateStateHooks(text, 4000); err != nil {
			t.Fatal(name, err)
		}
		for _, event := range []string{"UserPromptSubmit", "Stop", "Notification"} {
			hs := stateHooks(t, text, event)
			if len(hs) != 1 {
				t.Fatalf("%s %s: %v", name, event, hs)
			}
			h := hs[0]
			if h["type"] != "http" || h["url"] != "http://127.0.0.1:4000/hooks/state" ||
				at(h, "headers", "Authorization") != "Bearer $PIXIE_AGENT_TOKEN" ||
				!reflect.DeepEqual(h["allowedEnvVars"], []any{"PIXIE_AGENT_TOKEN"}) || h["timeout"] != float64(5) {
				t.Fatalf("%s %s: %v", name, event, h)
			}
		}
		// The Stop hook that carries the reply is still the first.
		if url := at(decodeJSON(t, text), "hooks", "Stop", 0, "hooks", 0, "url"); url != "http://127.0.0.1:4000/hooks/stop" {
			t.Fatal(name, url)
		}
		// Only prompts that wait on the owner count as blocked: Claude's idle reminder doesn't.
		if m := at(decodeJSON(t, text), "hooks", "Notification", 0, "matcher"); m != "permission_prompt|elicitation_dialog" {
			t.Fatal(name, m)
		}
		// The shipped files have none.
		if len(stateHooks(t, tc.render(4000), "Stop")) != 0 || ValidateStateHooks(tc.render(4000), 4000) == nil {
			t.Fatal(name, "herdr's files have no state hooks")
		}
	}
}

func TestInstallForTmuxWritesTheStateHooksAndHerdrDoesNot(t *testing.T) {
	paths := NewPaths(filepath.Join(t.TempDir(), "install"))
	if err := InstallFor(paths, 48217, "", true); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{paths.Settings, paths.WorkerSettings} {
		if err := ValidateStateHooks(readFile(t, p), 48217); err != nil {
			t.Fatal(p, err)
		}
	}
	if err := InstallFor(paths, 48217, "", false); err != nil {
		t.Fatal(err)
	}
	if readFile(t, paths.Settings) != RenderSettings(48217) || readFile(t, paths.WorkerSettings) != RenderWorkerSettings(48217) {
		t.Fatal("herdr: the shipped files")
	}
}

func TestCloseSessionAsksTheUserFirst(t *testing.T) {
	var v struct {
		Permissions struct {
			Ask []string `json:"ask"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(RenderSettings(48217)), &v); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(v.Permissions.Ask, "mcp__pixie__close_session") {
		t.Fatalf("close_session must be in permissions.ask: %v", v.Permissions.Ask)
	}
}

func TestCoordinatorPromptExplainsSessionsYouStart(t *testing.T) {
	for _, root := range []string{"", "/Users/me/Projects"} {
		md := RenderClaudeMd("/canvas", root)
		for _, want := range []string{"list_sessions", "message_session", "interrupt_session", "close_session", "describe_session", "pix"} {
			if !strings.Contains(md, want) {
				t.Errorf("projects root %q: prompt lacks %q", root, want)
			}
		}
	}
}
