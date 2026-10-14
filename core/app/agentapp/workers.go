package agentapp

// Worker wiring (spec §5.5, §12): agent.workers as the controller's policy, projectsRoot, the
// coordinator's MCP config, and the worker adapters (git worktrees, launch files, tokens).
// Port of src-tauri/src/agent/workers.rs.

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/agent/httpserver"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/settings"
)

// GitTimeout is how long one git command may take (a worktree checkout of a large
// repository); a whole Ensure stops at agent.EnsureBudget (asserted below it in a test, the
// Rust compile-time assert).
const GitTimeout = 45 * time.Second

// GitCleanupTimeout is how long the `remove -f -f` cleanup of a failed or timed-out
// `worktree add` may take.
const GitCleanupTimeout = 60 * time.Second

// Policy is agent.workers for the controller. Unknown kinds are logged and left out; max is
// 1–16.
func Policy(w settings.WorkerSettings) agent.WorkerPolicy {
	var kinds []agent.AgentKind
	for _, k := range w.Kinds {
		kind, ok := agent.ParseAgentKind(k)
		switch {
		case !ok:
			log.Printf("agent: unknown worker kind %q in agent.workers.kinds", k)
		case !slices.Contains(kinds, kind):
			kinds = append(kinds, kind)
		}
	}
	if w.Enabled && len(kinds) == 0 {
		log.Print("agent: agent.workers.kinds has no usable kind; workers are off")
	}
	return agent.WorkerPolicy{Enabled: w.Enabled && len(kinds) > 0, Max: int(min(max(w.Max, 1), 16)), Kinds: kinds}
}

// WorkersOn: agent.workers.enabled with a usable kind (policy of the same settings). Enabled
// with no usable kind is a setup problem: the error is its sticky status line.
func WorkersOn(w settings.WorkerSettings, policy agent.WorkerPolicy) (bool, error) {
	if w.Enabled && !policy.Enabled {
		return false, errors.New("Agent: workers off: no usable worker kind")
	}
	return w.Enabled, nil
}

// WorkersSetup is whether workers start, decided from agent.workers and agent.projectsRoot.
type WorkersSetup struct {
	// Policy is Enabled only when workers really start.
	Policy agent.WorkerPolicy
	// Root is the canonical projects root when workers start ("" otherwise).
	Root string
	// Notice says why enabled workers are off (a tray notice; "" = none). It never disables
	// delegation: the coordinator works solo.
	Notice string
}

// SetupWorkers: workers on, or off with a notice (logged) when they are enabled but can't
// run: no usable kind, or an unusable projects root.
func SetupWorkers(w settings.WorkerSettings, projectsRootRaw, home string) WorkersSetup {
	policy := Policy(w)
	off := func(notice string) WorkersSetup {
		policy.Enabled = false
		return WorkersSetup{Policy: policy, Notice: notice}
	}
	on, err := WorkersOn(w, policy)
	switch {
	case err != nil: // Policy logged why
		return off(err.Error())
	case !on:
		return WorkersSetup{Policy: policy}
	}
	root, err := ProjectsRoot(projectsRootRaw, home)
	if err != nil {
		log.Printf("agent: workers off: projectsRoot not usable: %v", err)
		return off("Agent: workers off: projectsRoot not usable")
	}
	return WorkersSetup{Policy: policy, Root: root}
}

// TerminalDir is where the agent terminal starts: the projects folder when it is usable
// (see ProjectsRoot), else the home folder.
func TerminalDir(projectsRootRaw, home string) string {
	if root, err := ProjectsRoot(projectsRootRaw, home); err == nil {
		return root
	}
	return home
}

// ProjectsRoot is agent.projectsRoot: only ~ and ~/… are expanded (not ~user); a relative
// path is taken from the home folder. The result is canonical, an existing folder, and
// strictly below the home folder (a root that wide, or outside it, is a mistake).
func ProjectsRoot(raw, home string) (string, error) {
	raw = strings.TrimSpace(raw)
	var joined string
	switch {
	case raw == "":
		return "", errors.New("empty")
	case raw == "~":
		joined = home
	case strings.HasPrefix(raw, "~/"):
		joined = filepath.Join(home, raw[2:])
	case strings.HasPrefix(raw, "~"):
		return "", errors.New("~user is not supported")
	case filepath.IsAbs(raw):
		joined = raw
	default:
		joined = filepath.Join(home, raw)
	}
	root, err := canonical(joined)
	if err != nil {
		return "", fmt.Errorf("%s: %w", joined, err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a folder", root)
	}
	h, err := canonical(home)
	if err != nil {
		h = home
	}
	if root == h || !strings.HasPrefix(root, strings.TrimSuffix(h, "/")+"/") {
		return "", fmt.Errorf("%s is not a folder below your home folder", root)
	}
	return root, nil
}

func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// WriteCoordinatorMcp writes the coordinator's --mcp-config (0600; it holds the coordinator's
// bearer).
func WriteCoordinatorMcp(paths Paths, port uint16, token string) error {
	if err := agentfs.WritePrivate(paths.CoordinatorMcp(), agent.McpConfigJSON(port, token)); err != nil {
		return fmt.Errorf("%s: %w", paths.CoordinatorMcp(), err)
	}
	return nil
}

// WorkerAdapters are the worker adapters. Folder trust is granted only under the projects
// root. model "" is none.
func WorkerAdapters(paths Paths, root, home string, port uint16, model string, tokens *httpserver.Tokens) *agent.WorkerDeps {
	trust := agentfs.NewClaudeTrustStore(filepath.Join(home, ".claude.json"), []string{root})
	worktrees := agentfs.NewGitWorktrees(root, paths.Workspace, home, herdr.PatientRunner(GitTimeout)).
		WithCleanupRunner(herdr.PatientRunner(GitCleanupTimeout))
	return &agent.WorkerDeps{
		Worktrees: worktrees,
		Tokens:    tokens,
		Recipes:   agentfs.NewFileWorkerRecipes(paths.McpDir, paths.WorkerSettings, port, strings.TrimSpace(model), trust),
	}
}
