package agentapp

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestWorkerAllowRulesAreExactlyTheReviewedList(t *testing.T) {
	got := WorkerAllowRules("/Users/u/Documents/Projects")
	want := []string{
		"Edit(//Users/u/Documents/Projects/*/.worktrees/**)",
		"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
		"Bash(git add:*)", "Bash(git commit:*)", "Bash(git branch:*)", "Bash(git rev-parse:*)",
		"Bash(ls:*)", "Bash(pwd)", "Bash(which:*)",
		"Bash(go build:*)", "Bash(go test:*)", "Bash(go vet:*)",
		"Bash(npm test:*)", "Bash(npm run test:*)", "Bash(npm run build:*)", "Bash(npm run lint:*)",
		"Bash(swift build:*)", "Bash(swift test:*)", "Bash(make:*)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestCoordinatorAllowRulesHaveNoBuildsOrTests(t *testing.T) {
	got := CoordinatorAllowRules("/a/workspace", "/a/canvas")
	want := []string{
		"Edit(//a/workspace/**)", "Edit(//a/canvas/**)",
		"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
		"Bash(git rev-parse:*)", "Bash(git branch:*)",
		"Bash(ls:*)", "Bash(pwd)", "Bash(which:*)",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestWithAllowRulesKeepsAskAndDenyAndAppendsOnce(t *testing.T) {
	in := RenderWorkerSettings(4321)
	out, err := WithAllowRules(in, []string{"Bash(ls:*)", "mcp__pixie"})
	if err != nil {
		t.Fatal(err)
	}
	var a, b struct{ Permissions map[string][]string }
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal([]byte(out), &b)
	if !slices.Equal(a.Permissions["ask"], b.Permissions["ask"]) || !slices.Equal(a.Permissions["deny"], b.Permissions["deny"]) {
		t.Fatal("ask/deny changed")
	}
	if want := append(slices.Clone(a.Permissions["allow"]), "Bash(ls:*)"); !slices.Equal(b.Permissions["allow"], want) {
		t.Fatalf("%q", b.Permissions["allow"])
	}
	if err := ValidateSettings(out, 4321, WorkerHookTimeouts); err != nil {
		t.Fatal(err)
	}
}

func installedAllow(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s struct{ Permissions map[string][]string }
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s.Permissions["allow"]
}

func TestInstallForWritesAllowRules(t *testing.T) {
	for _, state := range []bool{false, true} {
		paths := NewPaths(t.TempDir())
		root := t.TempDir()
		if err := InstallFor(paths, 4321, root, state); err != nil {
			t.Fatal(err)
		}
		w, c := installedAllow(t, paths.WorkerSettings), installedAllow(t, paths.Settings)
		for _, r := range WorkerAllowRules(root) {
			if !slices.Contains(w, r) {
				t.Fatalf("worker missing %q", r)
			}
		}
		for _, r := range CoordinatorAllowRules(paths.Workspace, paths.Canvas) {
			if !slices.Contains(c, r) {
				t.Fatalf("coordinator missing %q", r)
			}
		}
	}
}

func TestInstallForWorkersOffSkipsWorkerRules(t *testing.T) {
	paths := NewPaths(t.TempDir())
	if err := InstallFor(paths, 4321, "", false); err != nil {
		t.Fatal(err)
	}
	if w := installedAllow(t, paths.WorkerSettings); slices.Contains(w, "Bash(go test:*)") {
		t.Fatalf("worker rules written with workers off: %q", w)
	}
}
