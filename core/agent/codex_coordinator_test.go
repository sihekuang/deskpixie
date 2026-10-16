package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// #87: Codex as the coordinator. Its hooks and their trust come by -c overrides (Codex's
// session-flags layer), so nothing is written into ~/.codex.

// probeCommand is the Stop hook of the 2026-10-15 probe; Codex 0.162.1 itself stored its trust
// as probeHash.
const (
	probeCommand = `curl -s -X POST -H "Authorization: Bearer $PIXIE_AGENT_TOKEN" -H "Content-Type: application/json" --data-binary @- http://127.0.0.1:18777/hooks/stop`
	probeHash    = "sha256:5c1b49deac5b1171540f2597015dc39f350e71fd7c624c1844bf77ffca7d97c6"
)

func TestTheHookHashIsTheOneCodexStores(t *testing.T) {
	if got := CodexHookHash(CodexHook{Label: "stop", Timeout: 30}, probeCommand); got != probeHash {
		t.Fatal(got)
	}
	// The matcher and the timeout are part of the hook's identity.
	if CodexHookHash(CodexHook{Label: "stop", Timeout: 5}, probeCommand) == probeHash ||
		CodexHookHash(CodexHook{Label: "stop", Timeout: 30, Matcher: "Bash"}, probeCommand) == probeHash {
		t.Fatal("hash ignores a field")
	}
}

func TestTheHookCommandPostsStdinWithTheBearerFromTheEnv(t *testing.T) {
	got := CodexHookCommand(48217, "permission")
	want := `/usr/bin/curl -sS --variable %PIXIE_AGENT_TOKEN --expand-header "Authorization: Bearer {{PIXIE_AGENT_TOKEN}}" -H "Content-Type: application/json" --data-binary @- http://127.0.0.1:48217/hooks/permission`
	if got != want {
		t.Fatal(got)
	}
	// The token itself is never on a command line.
	if strings.Contains(got, "$PIXIE_AGENT_TOKEN") {
		t.Fatal(got)
	}
}

func TestTheCoordinatorHooksMirrorClaudes(t *testing.T) {
	var got []string
	for _, h := range CodexCoordinatorHooks(false) {
		got = append(got, h.Event+" "+h.Route+" "+h.Matcher)
	}
	want := []string{
		"Stop stop ",
		"PermissionRequest permission ",
		"PostToolUse post-tool Bash|Edit|Write|mcp__pixie__start_worker|mcp__mascot__start_worker",
	}
	if !slices.Equal(got, want) {
		t.Fatal(got)
	}
	timeouts := map[string]uint64{}
	for _, h := range CodexCoordinatorHooks(true) {
		timeouts[h.Event+"/"+h.Route] = h.Timeout
	}
	if len(timeouts) != 5 || timeouts["PermissionRequest/permission"] != 125 || timeouts["Stop/stop"] != 5 ||
		timeouts["Stop/state"] != 5 || timeouts["UserPromptSubmit/state"] != 5 {
		t.Fatal(timeouts)
	}
}

func TestHookArgsCarryEachHookAndItsTrust(t *testing.T) {
	args := CodexHookArgs(48217, CodexCoordinatorHooks(true))
	var values []string
	for i := 0; i < len(args); i += 2 {
		if args[i] != "-c" {
			t.Fatal(args)
		}
		values = append(values, args[i+1])
	}
	stop := CodexHookCommand(48217, "stop")
	state := CodexHookCommand(48217, "state")
	wantStop := `hooks.Stop=[{hooks=[{type="command",command=` + tomlString(stop) + `,timeout=5}]},{hooks=[{type="command",command=` + tomlString(state) + `,timeout=5}]}]`
	if !slices.Contains(values, wantStop) {
		t.Fatal(values)
	}
	last := values[len(values)-1]
	if !strings.HasPrefix(last, "hooks.state={") {
		t.Fatal(last)
	}
	// One trust entry per handler, keyed by its place in the session-flags layer.
	for _, key := range []string{"stop:0:0", "stop:1:0", "permission_request:0:0", "post_tool_use:0:0", "user_prompt_submit:0:0"} {
		if !strings.Contains(last, `"/<session-flags>/config.toml:`+key+`"={trusted_hash="sha256:`) {
			t.Fatal(key, last)
		}
	}
	hash := CodexHookHash(CodexHook{Label: "stop", Timeout: 5}, stop)
	if !strings.Contains(last, `"/<session-flags>/config.toml:stop:0:0"={trusted_hash="`+hash+`"}`) {
		t.Fatal(last)
	}
}

func TestTomlStringsEscapeQuotesAndBackslashes(t *testing.T) {
	if got := tomlString(`a "b" \c <d>`); got != `"a \"b\" \\c <d>"` {
		t.Fatal(got)
	}
}

func TestCodexCoordinatorArgs(t *testing.T) {
	args := CodexCoordinatorArgs("/data/canvas", 48217, "gpt-6-sol", false)
	head := []string{"--no-daemon", "-s", "workspace-write", "-a", "on-request",
		"-c", `approvals_reviewer="auto_review"`, "--add-dir", "/data/canvas"}
	if !slices.Equal(args[:len(head)], head) {
		t.Fatal(args)
	}
	for _, want := range []string{
		"sandbox_workspace_write.network_access=true",
		"check_for_update_on_startup=false",
		"mcp_servers.pixie.url=http://127.0.0.1:48217/mcp",
		"mcp_servers.pixie.bearer_token_env_var=PIXIE_AGENT_TOKEN",
		`mcp_servers.pixie.default_tools_approval_mode="approve"`,
	} {
		i := slices.Index(args, want)
		if i < 1 || args[i-1] != "-c" {
			t.Fatal(want, args)
		}
	}
	if !slices.Equal(args[len(args)-2:], []string{"-m", "gpt-6-sol"}) {
		t.Fatal(args)
	}
	if blank := CodexCoordinatorArgs("/c", 1, "  ", false); slices.Contains(blank, "-m") {
		t.Fatal(blank)
	}
	// The state hooks only on a host that detects no status (tmux).
	if strings.Contains(strings.Join(args, " "), "UserPromptSubmit") {
		t.Fatal(args)
	}
	if !strings.Contains(strings.Join(CodexCoordinatorArgs("/c", 1, "", true), " "), "hooks.UserPromptSubmit=") {
		t.Fatal("no state hooks")
	}
}

func TestTheHooksReviewScreenIsARefusal(t *testing.T) {
	screen := "  Hooks need review\n  6 hooks are new or changed.\n  Hooks can run outside the sandbox after you trust them.\n› 1. Review hooks\n  2. Trust all and continue\n  3. Continue without trusting (hooks won't run)\n  enter confirm · esc skip"
	rs := CodexCoordinatorRefusals("/data/agent/workspace")
	if len(rs) != 2 || !ShowsAnyScreen(screen, rs[0].Screens) || rs[0].Reason != CodexHooksReviewText || ShowsAnyScreen(screen, rs[1].Screens) {
		t.Fatalf("%+v", rs)
	}
	if !ShowsAnyScreen(codexTrustScreen, rs[1].Screens) || ShowsAnyScreen(codexTrustScreen, rs[0].Screens) {
		t.Fatal("folder trust")
	}
	if rs[1].Reason != "Codex asks to trust the coordinator's workspace. Run `codex` once in /data/agent/workspace and choose Trust, then restart the coordinator." {
		t.Fatal(rs[1].Reason)
	}
	for _, r := range rs {
		if ShowsAnyScreen("› Ask Codex to do anything", r.Screens) {
			t.Fatal("input box")
		}
	}
}

func TestTheKeptFileNamesACodexCoordinatorOnly(t *testing.T) {
	b, _ := json.Marshal(KeptAgents{CoordinatorToken: "t"})
	if string(b) != `{"coordinatorToken":"t"}` {
		t.Fatal(string(b)) // Claude (and every file from before #87): unchanged
	}
	b, _ = json.Marshal(KeptAgents{CoordinatorToken: "t", CoordinatorKind: Codex})
	if string(b) != `{"coordinatorToken":"t","coordinatorKind":"codex"}` {
		t.Fatal(string(b))
	}
	var back KeptAgents
	if err := json.Unmarshal(b, &back); err != nil || back.CoordinatorKind != Codex {
		t.Fatal(back, err)
	}
	var old KeptAgents
	if err := json.Unmarshal([]byte(`{"coordinatorToken":"t"}`), &old); err != nil || old.CoordinatorKind != Claude {
		t.Fatal(old, err)
	}
}

func TestCodexPatchesAreNarratedAsEdits(t *testing.T) {
	patch := map[string]any{"command": "*** Begin Patch\n*** Update File: /data/canvas/pages/q3-sales.html\n@@\n-a\n+b\n*** End Patch"}
	if got, ok := Phrase("apply_patch", patch); !ok || got != "editing q3-sales.html" {
		t.Fatal(got, ok)
	}
	add := map[string]any{"command": "*** Begin Patch\n*** Add File: notes.md\n+hi\n*** End Patch"}
	if got, ok := Phrase("apply_patch", add); !ok || got != "editing notes.md" {
		t.Fatal(got, ok)
	}
	if _, ok := Phrase("apply_patch", map[string]any{"command": "garbage"}); ok {
		t.Fatal("no file")
	}
	// Codex's Bash has no description in PostToolUse.
	if got, ok := Phrase("Bash", map[string]any{"command": "ls"}); !ok || got != "running a command" {
		t.Fatal(got, ok)
	}
}
