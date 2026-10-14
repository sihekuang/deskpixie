package agentfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sihekuang/deskpixie/core/agent"
)

// CodexExtraArgs: extra Codex arguments for a worker in the given (canonical) repo.
type CodexExtraArgs func(repo string) []string

// FileWorkerRecipes is agent.WorkerRecipes over files (spec §5.5): what each worker kind's CLI
// needs before its pane starts. A Claude worker gets folder trust for its worktree and a 0600
// `mcp/<name>.json` with its bearer; both are app data, never written into the user's repo. A
// Codex worker needs no file: its MCP server comes by `-c` overrides with the bearer from its
// env. Its folder-trust prompt is never answered: the start fails until the user has trusted the
// repo in Codex, and the start waits for Codex's input box before anything is typed.
type FileWorkerRecipes struct {
	mcpDir         string // <app_data>/agent/mcp: one 0600 MCP config per agent
	workerSettings string // <app_data>/agent/worker-settings.json
	port           uint16
	model          string // agent.model (Claude workers only); "" = none
	trust          agent.TrustStore
	codexExtraArgs CodexExtraArgs
}

var _ agent.WorkerRecipes = (*FileWorkerRecipes)(nil)

func NewFileWorkerRecipes(mcpDir, workerSettings string, port uint16, model string, trust agent.TrustStore) *FileWorkerRecipes {
	return &FileWorkerRecipes{mcpDir: mcpDir, workerSettings: workerSettings, port: port, model: model, trust: trust}
}

// WithCodexExtraArgs is for the live smoke only: extra arguments appended to a Codex worker's
// command line, by repo. The app never trusts a folder for the user, so it never calls this.
func (r *FileWorkerRecipes) WithCodexExtraArgs(extra CodexExtraArgs) *FileWorkerRecipes {
	r.codexExtraArgs = extra
	return r
}

// McpConfigPath is `<mcp_dir>/<name>.json`.
func (r *FileWorkerRecipes) McpConfigPath(name string) string {
	return filepath.Join(r.mcpDir, name+".json")
}

func (r *FileWorkerRecipes) claude(l agent.WorkerLaunch) (agent.PreparedWorker, error) {
	if _, err := r.trust.Grant(l.Worktree); err != nil {
		return agent.PreparedWorker{}, fmt.Errorf("could not pre-trust the worktree: %w", err)
	}
	config := r.McpConfigPath(l.Name)
	if err := WritePrivate(config, agent.McpConfigJSON(r.port, l.Token)); err != nil {
		return agent.PreparedWorker{}, fmt.Errorf("cannot write the worker's MCP config: %w", err)
	}
	return agent.PreparedWorker{Spec: agent.AgentSpec{
		Name: l.Name,
		Kind: agent.Claude,
		Cwd:  l.Worktree,
		Args: agent.ClaudeWorkerArgs(r.workerSettings, config, r.model),
		Env:  []agent.EnvVar{{Key: agent.TokenEnv, Value: l.Token}},
	}}, nil
}

// gitCommonDir is the repo's git common dir without running git: `<repo>/.git` itself, or, when
// that is a file (`gitdir: <path>`), the folder it names, followed through its `commondir` if any.
func gitCommonDir(repo string) (string, bool) {
	dotGit := filepath.Join(repo, ".git")
	if isDir(dotGit) {
		return dotGit, true
	}
	b, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	rest, ok := strings.CutPrefix(firstLine(string(b)), "gitdir:")
	if !ok {
		return "", false
	}
	dir := joinPath(repo, strings.TrimSpace(rest))
	if c, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
		return joinPath(dir, strings.TrimSpace(string(c))), true
	}
	return dir, true
}

// codexWritable: the only parts of git's folder a commit in the worktree writes: the object
// store, this worktree's own admin folder, and the `mascot/*` branch refs and their reflogs.
// `hooks/`, `config`, `info/` and `packed-refs` stay read-only. Empty when there is no object store.
func codexWritable(repo, worktree string) []string {
	c, ok := gitCommonDir(repo)
	if !ok {
		return nil
	}
	common, err := canonicalize(c)
	if err != nil {
		return nil
	}
	// Every candidate is canonicalized (so it exists and symlinks are resolved) and must lie
	// inside the canonical common dir; the files that name them are writable by a worker.
	inside := func(p string) (string, bool) {
		c, err := canonicalize(p)
		if err != nil || !isDir(c) || !hasPrefix(c, common) {
			return "", false
		}
		return c, true
	}
	objects, ok := inside(filepath.Join(common, "objects"))
	if !ok {
		return nil
	}
	// A commit writes these; `git pack-refs` may have removed them while empty.
	for _, dir := range []string{"refs/heads/mascot", "logs/refs/heads/mascot"} {
		_ = os.MkdirAll(filepath.Join(common, dir), 0o777)
	}
	// The worktree's `.git` file names its admin folder exactly (git may have suffixed the name),
	// relative to the worktree. It must be `<common>/worktrees/<name>`, and that folder's own
	// `gitdir` must name this worktree (not another worktree's admin folder).
	dotGit := filepath.Join(worktree, ".git")
	var adminPath string
	have := false
	if b, err := os.ReadFile(dotGit); err == nil {
		if rest, ok := strings.CutPrefix(firstLine(string(b)), "gitdir:"); ok {
			adminPath, have = joinPath(worktree, strings.TrimSpace(rest)), true
		}
	}
	if !have {
		adminPath, have = filepath.Join(common, "worktrees", filepath.Base(worktree)), true
	}
	out := []string{objects}
	if a, ok := inside(adminPath); ok && filepath.Dir(a) == filepath.Join(common, "worktrees") && namesWorktree(a, dotGit) {
		out = append(out, a)
	}
	for _, p := range []string{"refs/heads/mascot", "logs/refs/heads/mascot"} {
		if c, ok := inside(filepath.Join(common, p)); ok {
			out = append(out, c)
		}
	}
	return out
}

// namesWorktree: the admin folder's own `gitdir` file (relative to the admin folder) names dotGit.
func namesWorktree(admin, dotGit string) bool {
	b, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return false
	}
	a, err1 := canonicalize(joinPath(admin, strings.TrimSpace(firstLine(string(b)))))
	c, err2 := canonicalize(dotGit)
	return err1 == nil && err2 == nil && a == c
}

// storesRefsAsReftable: the repo keeps its refs in the reftable format (`extensions.refStorage`,
// or a `reftable/` folder): a Codex worker's commit would write outside the dirs it is given.
func storesRefsAsReftable(common string) bool {
	if isDir(filepath.Join(common, "reftable")) {
		return true
	}
	b, err := os.ReadFile(filepath.Join(common, "config"))
	if err != nil {
		return false
	}
	section := ""
	for _, raw := range rustLines(string(b)) {
		line := strings.TrimSpace(raw)
		entry := line
		if rest, ok := strings.CutPrefix(line, "["); ok {
			name, after, _ := strings.Cut(rest, "]")
			section = strings.ToLower(strings.TrimSpace(name))
			entry = strings.TrimSpace(after)
		}
		if key, value, ok := strings.Cut(entry, "="); ok {
			if i := strings.IndexAny(value, "#;"); i >= 0 {
				value = value[:i]
			}
			value = strings.Trim(strings.TrimSpace(value), "\"")
			if section == "extensions" && strings.EqualFold(strings.TrimSpace(key), "refstorage") && strings.EqualFold(value, "reftable") {
				return true
			}
		}
	}
	return false
}

func (r *FileWorkerRecipes) codex(l agent.WorkerLaunch) (agent.PreparedWorker, error) {
	if c, ok := gitCommonDir(l.Repo); ok && storesRefsAsReftable(c) {
		return agent.PreparedWorker{}, fmt.Errorf("This repo stores refs as reftable; Codex workers can't commit in it. Use a Claude worker.")
	}
	args := agent.CodexWorkerArgs(codexWritable(l.Repo, l.Worktree), r.port)
	if r.codexExtraArgs != nil {
		args = append(args, r.codexExtraArgs(l.Repo)...)
	}
	return agent.PreparedWorker{
		Spec: agent.AgentSpec{
			Name: l.Name,
			Kind: agent.Codex,
			Cwd:  l.Worktree,
			Args: args,
			Env:  []agent.EnvVar{{Key: agent.TokenEnv, Value: l.Token}},
		},
		Refusal: &agent.StartRefusal{Screens: agent.CodexTrustScreensOwned(), Reason: agent.CodexTrustRefusal(l.Repo)},
		Ready:   &agent.StartReady{Marker: agent.CodexIdleMarker, Reason: agent.CodexNotReady(l.Repo)},
	}, nil
}

// Prepare writes what the worker's CLI needs and returns the launch spec.
func (r *FileWorkerRecipes) Prepare(launch agent.WorkerLaunch) (agent.PreparedWorker, error) {
	if !agent.IsWorkerName(launch.Name) {
		return agent.PreparedWorker{}, fmt.Errorf("%q is not a worker name", launch.Name)
	}
	if launch.Kind == agent.Codex {
		return r.codex(launch)
	}
	return r.claude(launch)
}

// Discard deletes what Prepare wrote for name (its pane closed). Idempotent.
func (r *FileWorkerRecipes) Discard(name string) {
	if agent.IsWorkerName(name) {
		RemoveQuietly(r.McpConfigPath(name))
	}
}

// ClearMcpDir deletes every `*.json` in dir: MCP configs (with tokens) left by an earlier run.
func ClearMcpDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if n := e.Name(); n != ".json" && filepath.Ext(n) == ".json" { // Rust: ".json" alone has no extension
			RemoveQuietly(filepath.Join(dir, e.Name()))
		}
	}
}
