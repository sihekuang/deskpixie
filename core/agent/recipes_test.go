package agent

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Ported from crates/agent-core/src/recipes.rs (9 tests).

// codexTrustScreen is Codex 0.162.1's folder-trust screen, as a live run showed it (for tests).
const codexTrustScreen = `  Trusting will apply to the repository root:
  /private/var/folders/x/T/projects/demo
  Trust this folder? Codex can read, edit, and run
  files here, subject to your permission settings.
  Folder settings can run code automatically, even
  without a model request. Continue only if you
  trust these files. Your trust decision will be
  saved.
› 1. Trust and continue
  2. Quit
  enter continue · esc quit`

func TestCodexTrustScreensOldAndNewAreRecognisedAndItsInputBoxIsNot(t *testing.T) {
	if !ShowsCodexTrust(codexTrustScreen) {
		t.Fatal("new screen")
	}
	if !ShowsCodexTrust("> Do you trust the contents of this directory?\n  1. Yes, continue\n  2. No, quit") {
		t.Fatal("old screen")
	}
	if ShowsCodexTrust("╭──╮\n│ › Ask Codex to do anything │\n╰──╯") {
		t.Fatal("input box")
	}
	// A diff that mentions the prompt's words is not the prompt.
	diff := "• Edited src/recipes.rs (+1 -0)\n    60 +pub const T: &str = \"Trust this folder?\";\n\n› Ask Codex to do anything"
	if ShowsCodexTrust(diff) {
		t.Fatal("diff")
	}
	// The prompt high above the bottom lines (scrolled away) is not on screen any more.
	var extra []string
	for i := 0; i < 20; i++ {
		extra = append(extra, fmt.Sprintf("line %d", i))
	}
	if ShowsCodexTrust(codexTrustScreen + "\n" + strings.Join(extra, "\n")) {
		t.Fatal("scrolled")
	}
	if got := CodexNotReady("/projects/demo"); got != "Codex didn't become ready (its screen ends with \"{last}\"); open Codex in /projects/demo to see why." {
		t.Fatal(got)
	}
	if got := CodexTrustRefusal("/projects/demo"); got != "Codex asks to trust this repo before it will work in it; I won't trust it for you. Run `codex` once in /projects/demo and choose Trust, then ask again." {
		t.Fatal(got)
	}
}

func TestTheMcpConfigIsOneHTTPServerWithABearerHeader(t *testing.T) {
	var v any
	if err := json.Unmarshal([]byte(McpConfigJSON(48217, "abc")), &v); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"mcpServers": map[string]any{"pixie": map[string]any{
		"type": "http", "url": "http://127.0.0.1:48217/mcp", "headers": map[string]any{"Authorization": "Bearer abc"},
	}}}
	if !reflect.DeepEqual(v, want) {
		t.Fatal(v)
	}
}

func TestClaudeWorkerArgsFollowTheSpec(t *testing.T) {
	got := ClaudeWorkerArgs("/d/agent/worker-settings.json", "/d/agent/mcp/focus-x.json", " haiku ")
	want := []string{"--permission-mode", "auto", "--settings", "/d/agent/worker-settings.json", "--setting-sources", "user", "--mcp-config", "/d/agent/mcp/focus-x.json", "--strict-mcp-config", "--model", "haiku"}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if n := len(ClaudeWorkerArgs("/s", "/m", "")); n != 9 {
		t.Fatal(n)
	}
}

func TestCodexWorkerArgsFollowTheSpec(t *testing.T) {
	writable := []string{"/p/focus/.git/objects", "/p/focus/.git/worktrees/fix"}
	want := []string{
		"-s",
		"workspace-write",
		"-a",
		"on-request",
		"--add-dir",
		"/p/focus/.git/objects",
		"--add-dir",
		"/p/focus/.git/worktrees/fix",
		"-c",
		"mcp_servers.pixie.url=http://127.0.0.1:48217/mcp",
		"-c",
		"mcp_servers.pixie.bearer_token_env_var=PIXIE_AGENT_TOKEN",
		"-c",
		"mcp_servers.pixie.default_tools_approval_mode=\"approve\"",
		"-c",
		"check_for_update_on_startup=false",
	}
	if got := CodexWorkerArgs(writable, 48217); !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
	if slices.Contains(CodexWorkerArgs(nil, 1), "--add-dir") {
		t.Fatal("--add-dir")
	}
	if slices.Contains(CodexWorkerArgs(writable, 1), "-C") {
		t.Fatal("-C")
	}
}

func TestSlugsAreLowercaseDashedAndBounded(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"Fix the flaky Upload test!", 48, "fix-the-flaky-upload-test"},
		{"--Ünïcode__/x--", 48, "n-code-x"},
		{"abcdef", 3, "abc"},
		{"ab-cd", 3, "ab"},
		{"!!!", 10, ""},
	}
	for _, c := range cases {
		if got := Slugify(c.in, c.max); got != c.want {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
}

func wantName(t *testing.T, repo string, hint string, task string, taken []string, name, slug string) {
	t.Helper()
	n, s, ok := WorkerName(repo, hint, task, taken)
	if !ok || n != name || s != slug {
		t.Fatalf("got (%q, %q, %v), want (%q, %q)", n, s, ok, name, slug)
	}
}

func TestNamesAreRepoDashSlugAndNeverCollide(t *testing.T) {
	wantName(t, "Focus", "", "fix the flaky upload test please", nil, "focus-fix-flaky-upload", "fix-flaky-upload")
	wantName(t, "focus", "Focus upload", "x", nil, "focus-upload", "upload")
	taken := []string{"focus-upload", "focus-upload-2"}
	wantName(t, "focus", "upload", "x", taken, "focus-upload-3", "upload-3")
	wantName(t, "!!!", "", "???", nil, "repo-task", "task")
	long, slug, ok := WorkerName(strings.Repeat("r", 60), "", strings.Repeat("word ", 30), nil)
	if !ok || !IsWorkerName(long) || len(long) > MaxName {
		t.Fatal(long)
	}
	if !strings.HasSuffix(long, slug) {
		t.Fatal(long, slug)
	}
}

func TestANameFromTheTaskSkipsFillerWords(t *testing.T) {
	wantName(t, "ai-moscot", "", "In this repository, create a hello.txt file", nil, "ai-moscot-hello-txt-file", "hello-txt-file")
	slugOf := func(task string) string {
		_, s, _ := WorkerName("focus", "", task, nil)
		return s
	}
	for task, want := range map[string]string{
		"Please fix the flaky upload test": "fix-flaky-upload",
		"Make it so":                       "so",
		"Add a test to the repo":           "test",
		"In the repo, please":              "task", // nothing left
	} {
		if got := slugOf(task); got != want {
			t.Errorf("%q: got %q want %q", task, got, want)
		}
	}
}

func TestWorkerNamesAreChecked(t *testing.T) {
	for _, ok := range []string{"focus-fix", "a", "x1-2"} {
		if !IsWorkerName(ok) {
			t.Errorf("%s", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "Focus", "a_b", "coordinator", strings.Repeat("a", 49)} {
		if IsWorkerName(bad) {
			t.Errorf("%s", bad)
		}
	}
}

func TestTheBriefCarriesTheRulesAndTheTask(t *testing.T) {
	b := WorkerBrief("focus", "fix-upload", "Fix the flaky upload test.")
	for _, s := range []string{"own branch (pixie/fix-upload, or mascot/fix-upload for a worktree from before the rename)", "Never push", "report_result"} {
		if !strings.Contains(b, s) {
			t.Errorf("missing %q", s)
		}
	}
	if !strings.HasSuffix(b, "Your task:\nFix the flaky upload test.") {
		t.Fatal(b)
	}
	if got := CoordinatorSays("use main"); got != "Coordinator says: use main" {
		t.Fatal(got)
	}
	// Rename (spec §6): the worker prompt names the DeskPixie coordinator.
	if !strings.HasPrefix(b, "You are a worker for the DeskPixie coordinator. ") {
		t.Fatal(b)
	}
}
