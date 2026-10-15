package pix

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestWhere(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"TERMINAL_EMULATOR": "JetBrains-JediTerm", "__CFBundleIdentifier": "com.jetbrains.WebStorm"}, "WebStorm"},
		{map[string]string{"TERMINAL_EMULATOR": "JetBrains-JediTerm"}, "JetBrains"},
		{map[string]string{"TERM_PROGRAM": "vscode"}, "VS Code"},
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, "iTerm"},
		{map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, "Terminal"},
		{map[string]string{}, "terminal"},
	}
	for _, c := range cases {
		if got := Where(envOf(c.env)); got != c.want {
			t.Errorf("%v: got %q want %q", c.env, got, c.want)
		}
	}
}

func TestNested(t *testing.T) {
	if !Nested(envOf(map[string]string{"PIX_SESSION": "1"})) || !Nested(envOf(map[string]string{"PIXIE_AGENT_TOKEN": "t"})) {
		t.Fatal("nested not detected")
	}
	if Nested(envOf(nil)) {
		t.Fatal("false positive")
	}
}

func TestPlanLaunchClaude(t *testing.T) {
	dir := t.TempDir()
	l, err := PlanLaunch("claude", []string{"--resume", "abc"}, "/app/pix", "/tmp/s.sock", dir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Args, " ")
	for _, want := range []string{"--settings", "--mcp-config", "--append-system-prompt", "--resume abc"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %q", want, joined)
		}
	}
	if !strings.HasSuffix(joined, "--resume abc") {
		t.Errorf("user args must come last: %q", joined)
	}
	if !slices.Contains(l.Env, "PIX_SESSION=1") || !slices.Contains(l.Env, "PIX_SOCK=/tmp/s.sock") {
		t.Fatalf("env %v", l.Env)
	}
	var settings map[string]any
	b, _ := os.ReadFile(l.Files[0])
	if err := json.Unmarshal(b, &settings); err != nil {
		t.Fatal(err)
	}
	hooks := settings["hooks"].(map[string]any)
	for _, ev := range []string{"UserPromptSubmit", "PostToolUse", "Stop", "Notification"} {
		if hooks[ev] == nil {
			t.Errorf("no %s hook", ev)
		}
	}
	perms, _ := settings["permissions"].(map[string]any)
	if allow, _ := perms["allow"].([]any); len(allow) != 1 || allow[0] != "mcp__pixie__set_description" {
		t.Fatalf("set_description must not ask for permission (and nothing else is allowed): %v", settings["permissions"])
	}
	if !strings.Contains(string(b), `\"/app/pix\" hook`) {
		t.Fatalf("hooks must call pix hook: %s", b)
	}
	if fi, _ := os.Stat(l.Files[0]); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
}

func TestPlanLaunchCodex(t *testing.T) {
	l, err := PlanLaunch("codex", nil, "/app/pix", "/tmp/s.sock", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(l.Args, " ")
	if !strings.Contains(joined, `mcp_servers.pixie.command="/app/pix"`) {
		t.Fatalf("args %q", joined)
	}
}

func TestPlanLaunchUnknownTool(t *testing.T) {
	if _, err := PlanLaunch("vim", nil, "/p", "/s", t.TempDir()); err == nil {
		t.Fatal("want error")
	}
}
