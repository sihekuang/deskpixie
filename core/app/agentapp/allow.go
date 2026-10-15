package agentapp

// Allow rules (#97): routine work that runs without asking even when Claude Code fell back to
// Manual mode. In auto mode they change nothing (auto allows these anyway). The ask and deny
// lists in the assets are untouched, so push, publish, deploy, rm -rf and .env still ask.

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// abs is a path in Claude Code's absolute rule form: "/Users/u/x" -> "//Users/u/x".
func abs(p string) string { return "/" + strings.TrimRight(p, "/") }

var readCommands = []string{
	"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
}

// WorkerAllowRules: edits inside any worker's worktree, git read/stage/commit, and builds/tests.
func WorkerAllowRules(projectsRoot string) []string {
	wt := abs(projectsRoot) + "/*/.worktrees/**"
	out := []string{"Edit(" + wt + ")"} // an Edit rule also allows Write; a Write rule alone does not
	out = append(out, readCommands...)
	return append(out,
		"Bash(git add:*)", "Bash(git commit:*)", "Bash(git branch:*)", "Bash(git rev-parse:*)",
		"Bash(ls:*)", "Bash(pwd)", "Bash(which:*)",
		"Bash(go build:*)", "Bash(go test:*)", "Bash(go vet:*)",
		"Bash(npm test:*)", "Bash(npm run test:*)", "Bash(npm run build:*)", "Bash(npm run lint:*)",
		"Bash(swift build:*)", "Bash(swift test:*)", "Bash(make:*)",
	)
}

// CoordinatorAllowRules: edits in its workspace and canvas, and git/ls reads. No builds: the
// coordinator delegates those to workers.
func CoordinatorAllowRules(workspace, canvas string) []string {
	out := []string{
		"Edit(" + abs(workspace) + "/**)",
		"Edit(" + abs(canvas) + "/**)",
	}
	out = append(out, readCommands...)
	return append(out, "Bash(git rev-parse:*)", "Bash(git branch:*)", "Bash(ls:*)", "Bash(pwd)", "Bash(which:*)")
}

// WithAllowRules appends rules not already in permissions.allow, keeping everything else.
func WithAllowRules(settingsJSON string, rules []string) (string, error) {
	dec := json.NewDecoder(strings.NewReader(settingsJSON))
	dec.UseNumber() // hook timeouts stay integers
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	perms, _ := v["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
		v["permissions"] = perms
	}
	var allow []any
	if raw, ok := perms["allow"].([]any); ok {
		allow = raw
	}
	for _, r := range rules {
		if !slices.Contains(allow, any(r)) {
			allow = append(allow, r)
		}
	}
	perms["allow"] = allow
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return buf.String(), nil
}
