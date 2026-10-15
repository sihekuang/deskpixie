package agentfs

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// WorktreesDir is the folder (under each repo) that holds the workers' worktrees.
const WorktreesDir = ".worktrees"

const excludeLine = ".worktrees/"

// EnsureBudget is the longest WorktreeFs.Ensure may take, besides the cleanup of a failed
// `worktree add` (which runs only in its own failure path). A start that times out still waits
// for it. Copied verbatim from Rust agent_core::runtime::ENSURE_BUDGET (55 s); P9's runtime
// constants assert EnsureBudget + DialogWait + PostStartCheck < WorkerStartLimit (128 s).
const EnsureBudget = 55 * time.Second

// GitWorktrees is agent.WorktreeFs over git (spec §5.5, #82): workers work in
// `<repo>/.worktrees/<slug>` on branch `pixie/<slug>` (pre-#68 workers: `mascot/<slug>`).
// Besides git's own bookkeeping under `.git/`, the only things written into a user's
// repository are that worktree and one `.worktrees/` line in `.git/info/exclude`.
type GitWorktrees struct {
	root      string // agent.projectsRoot (with ~ expanded)
	workspace string // the coordinator's workspace: never a worker's repo
	env       []agent.EnvVar
	runner    agent.CommandRunner
	// cleanup runs only the cleanup of a failed `worktree add`; give it a generous timeout.
	cleanup agent.CommandRunner
	// budget is the longest one Ensure (or ResolveRepo, Audit) may take, besides that cleanup:
	// no git call starts after it, and none runs past it.
	budget time.Duration
	// admins: each worktree's admin folder (`<common>/worktrees/<id>`), as identified while its
	// links were still git's own (made or audited by this run).
	mu     sync.Mutex
	admins map[string]string
}

var _ agent.WorktreeFs = (*GitWorktrees)(nil)

func werr(format string, a ...any) error { return agent.WorktreeError(fmt.Sprintf(format, a...)) }

// checkConfigScopes judges `git config --show-scope --no-includes -z --get-regexp <filters and
// includes>`: every entry must come from global or system config. Exit 1 with no output means
// none matched; anything else that isn't well-formed `<scope>\0<key>\n<value>\0` records is
// refused (fail closed).
func checkConfigScopes(out agent.CommandOutput) error {
	unexpected := func() error {
		return werr("I couldn't check this repo's git config for filter programs, so I won't create a worktree in it.")
	}
	switch {
	case out.Status == 1 && out.Stdout == "":
		return nil
	case out.Status == 0 && strings.HasSuffix(out.Stdout, "\x00"):
	default:
		return unexpected()
	}
	fields := strings.Split(out.Stdout[:len(out.Stdout)-1], "\x00")
	if len(fields)%2 != 0 {
		return unexpected()
	}
	for i := 0; i < len(fields); i += 2 {
		scope := fields[i]
		key, _, _ := strings.Cut(fields[i+1], "\n")
		if scope == "global" || scope == "system" {
			continue
		}
		if strings.HasPrefix(key, "include") {
			return werr("This repo's git config includes other config files; I won't run them. Remove the includes or use a different repo.")
		}
		return werr("This repo's own git config defines filter programs; I won't run them. Remove them or use a different repo.")
	}
	return nil
}

// NewGitWorktrees: home is the user's home (git reads its config from there). git runs with
// exactly this environment: a fixed PATH, HOME, no terminal prompts and the C locale.
func NewGitWorktrees(root, workspace, home string, runner agent.CommandRunner) *GitWorktrees {
	// git never looks for a repository above the projects root.
	ceiling := root
	if c, err := canonicalize(root); err == nil {
		ceiling = c
	}
	if parent := filepath.Dir(ceiling); parent != ceiling {
		ceiling = parent
	}
	return &GitWorktrees{
		root: root, workspace: workspace,
		env: []agent.EnvVar{
			{Key: "PATH", Value: "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"},
			{Key: "HOME", Value: home},
			{Key: "GIT_TERMINAL_PROMPT", Value: "0"},
			{Key: "LC_ALL", Value: "C"},
			{Key: "GIT_CEILING_DIRECTORIES", Value: ceiling},
		},
		runner: runner, cleanup: runner, budget: EnsureBudget,
		admins: map[string]string{},
	}
}

// WithCleanupRunner sets the runner for the cleanup after a failed or timed-out `worktree add`
// (default: the main runner).
func (g *GitWorktrees) WithCleanupRunner(r agent.CommandRunner) *GitWorktrees {
	g.cleanup = r
	return g
}

// WithBudget sets the budget for one call, from its start.
func (g *GitWorktrees) WithBudget(d time.Duration) *GitWorktrees {
	g.budget = d
	return g
}

func (g *GitWorktrees) deadline() time.Time { return time.Now().Add(g.budget) }

// argv is `git -C <dir> -c core.hooksPath=/dev/null -c core.fsmonitor=false <args>`: the repo's
// hooks (post-checkout, ...) and fsmonitor program never run.
func argv(dir string, args ...string) []string {
	return append([]string{"git", "-C", dir, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, args...)
}

// run runs git (see argv), giving up at by.
func (g *GitWorktrees) run(by time.Time, dir string, args ...string) (agent.CommandOutput, error) {
	if !time.Now().Before(by) {
		return agent.CommandOutput{}, werr("git: timed out: setting up the worktree took too long; try again.")
	}
	out, err := g.runner.RunBy(argv(dir, args...), g.env, by)
	if err != nil {
		msg := err.Error()
		if !strings.HasPrefix(msg, "git") {
			msg = "git: " + msg
		}
		return agent.CommandOutput{}, agent.WorktreeError(msg)
	}
	return out, nil
}

// git is like run, but a non-zero exit is an error carrying git's first stderr line.
func (g *GitWorktrees) git(by time.Time, dir string, args ...string) (string, error) {
	out, err := g.run(by, dir, args...)
	if err != nil {
		return "", err
	}
	if out.Status != 0 {
		why := "failed"
		for _, l := range rustLines(out.Stderr) {
			if strings.TrimSpace(l) != "" {
				why = strings.TrimSpace(l)
				break
			}
		}
		first := ""
		if len(args) > 0 {
			first = args[0]
		}
		return "", werr("git %s: %s", first, why)
	}
	return strings.TrimSpace(out.Stdout), nil
}

// removeStaleBranchLock: after a failed or timed-out `worktree add` (its git was killed with
// its whole process group), removes exactly `<common>/refs/heads/<branch>.lock` (branch is
// `pixie/<slug>`, or `mascot/<slug>` for a pre-#68 worker) if it is a plain file, so the next add
// of that branch isn't blocked. No other lock is ever touched; slug was validated by Ensure.
func (g *GitWorktrees) removeStaleBranchLock(repo, branch string) {
	out, err := g.cleanup.Run(argv(repo, "rev-parse", "--git-common-dir"), g.env)
	if err != nil {
		return
	}
	common := strings.TrimSpace(out.Stdout)
	if out.Status != 0 || common == "" {
		return
	}
	common = joinPath(repo, common)
	lock := filepath.Join(common, "refs/heads", filepath.FromSlash(branch)+".lock")
	if m, err := os.Lstat(lock); err == nil && m.Mode().IsRegular() && os.Remove(lock) == nil {
		fmt.Fprintf(os.Stderr, "agent: removed a stale branch lock left by a killed git for branch %s\n", branch)
	}
}

// onWorkerBranch: ref (`refs/heads/<branch>`) is slug's worker branch, `pixie/<slug>` or the
// pre-#68 `mascot/<slug>`.
func onWorkerBranch(ref, slug string) bool {
	for _, p := range agent.BranchPrefixes {
		if ref == "refs/heads/"+p+slug {
			return true
		}
	}
	return false
}

// headOnWorkerBranch: a HEAD file's contents name slug's worker branch (either prefix).
func headOnWorkerBranch(head []byte, slug string) bool {
	ref, ok := strings.CutPrefix(string(trimASCIIEnd(head)), "ref: ")
	return ok && onWorkerBranch(ref, slug)
}

// exclude makes the repo's `info/exclude` hold `.worktrees/` (added once).
func (g *GitWorktrees) exclude(by time.Time, repo string) error {
	c, err := g.git(by, repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	info := filepath.Join(joinPath(repo, c), "info")
	file := filepath.Join(info, "exclude")
	var text string
	if b, err := os.ReadFile(file); err == nil {
		text = string(b)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return werr("cannot read %s: %v", file, err)
	}
	for _, l := range rustLines(text) {
		switch strings.TrimSpace(l) {
		case ".worktrees/", "/.worktrees/", ".worktrees", "/.worktrees":
			return nil
		}
	}
	if err := os.MkdirAll(info, 0o777); err != nil {
		return werr("cannot create %s: %v", info, err)
	}
	add := ""
	if text != "" && !strings.HasSuffix(text, "\n") {
		add = "\n"
	}
	add += excludeLine + "\n"
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err == nil {
		_, err = f.WriteString(add)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		return werr("cannot write %s: %v", file, err)
	}
	return nil
}

func isASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// adminOf returns the worktree's admin folder, and whether it is positively ours: the one this
// run identified before, else the single folder under `<common>/worktrees/` that git may have
// named for slug (slug, or slug plus a number) whose `gitdir` names this worktree; either only
// while it is on its worker branch, `pixie/<slug>` or the pre-#68 `mascot/<slug>` (else it is the
// user's own).
// Failing both (a worker rewrote its `gitdir`), the only candidate, named exactly slug, is taken
// but not positively: only if it is on its worker branch and is not a live worktree elsewhere (its
// `gitdir` names a `.git` file pointing back at it). A sandboxed worker can write only its own
// admin folder, never create or rename one.
func (g *GitWorktrees) adminOf(common, worktree, slug string) (string, bool, error) {
	base := filepath.Join(common, "worktrees")
	isAdmin := func(a string) bool {
		m, err := os.Lstat(a)
		return filepath.Dir(a) == base && err == nil && m.IsDir()
	}
	unsure := func() error {
		return werr("I couldn't tell which git folder belongs to %s, so I won't use it; remove it with `git worktree remove -f -f %s` and try again.", worktree, worktree)
	}
	onBranch := func(a string) bool {
		h, err := os.ReadFile(filepath.Join(a, "HEAD"))
		return err == nil && headOnWorkerBranch(h, slug)
	}
	notOnBranch := func() error { return werr("That worktree isn't on its pixie branch; I won't reuse it.") }
	g.mu.Lock()
	defer g.mu.Unlock()
	if a, ok := g.admins[worktree]; ok {
		// Still on its branch: a folder git reused for another worktree since is not ours.
		switch ad, on := isAdmin(a), onBranch(a); {
		case ad && on:
			return a, true, nil
		case ad:
			return "", false, notOnBranch()
		default:
			return "", false, unsure()
		}
	}
	named := func(n string) bool {
		rest, ok := strings.CutPrefix(n, slug)
		return ok && isASCIIDigits(rest)
	}
	var candidates []string
	if entries, err := os.ReadDir(base); err == nil {
		for _, e := range entries {
			if p := filepath.Join(base, e.Name()); named(e.Name()) && isAdmin(p) {
				candidates = append(candidates, p)
			}
		}
	}
	target := filepath.Join(worktree, ".git")
	var claimers []string
	for _, a := range candidates {
		if p, ok := namedGitdir(a); ok && p == target {
			claimers = append(claimers, a)
		}
	}
	switch {
	case len(claimers) == 1 && !onBranch(claimers[0]):
		// A folder that names this worktree but is on another branch is the user's own.
		return "", false, notOnBranch()
	case len(claimers) == 1:
		g.admins[worktree] = claimers[0]
		return claimers[0], true, nil
	case len(claimers) == 0 && len(candidates) == 1 && filepath.Base(candidates[0]) == slug:
		only := candidates[0]
		liveElsewhere := false
		if p, ok := namedGitdir(only); ok {
			if a, ok := linkedAdmin(p); ok && a == only {
				liveElsewhere = true
			}
		}
		if onBranch(only) && !liveElsewhere {
			return only, false, nil
		}
		return "", false, unsure()
	case len(claimers) == 0 && len(candidates) == 0:
		return "", false, werr("%s exists but is not a worktree of this repository.", worktree)
	default:
		return "", false, unsure()
	}
}

// auditLinks puts back the links git wrote for `<repo>/.worktrees/<slug>` (absolute, or
// relative under `worktree.useRelativePaths`) and removes per-worktree config.
func (g *GitWorktrees) auditLinks(by time.Time, repo, slug string) error {
	if !agent.IsWorkerName(slug) {
		return werr("%q is not a worktree name.", slug)
	}
	canon, err := canonicalize(repo)
	if err != nil {
		return werr("%s: %v", repo, err)
	}
	repo = canon
	folder := filepath.Join(repo, WorktreesDir)
	worktree := filepath.Join(folder, slug)
	for _, p := range []string{folder, worktree} {
		if m, err := os.Lstat(p); err != nil || !m.IsDir() {
			return werr("%s is not a worktree folder.", p)
		}
	}
	c, err := g.git(by, repo, "rev-parse", "--git-common-dir")
	if err != nil {
		return err
	}
	common, err := canonicalize(joinPath(repo, c))
	if err != nil {
		return werr("the git folder of %s: %v", repo, err)
	}
	admin, ours, err := g.adminOf(common, worktree, slug)
	if err != nil {
		return err
	}
	rel, err := g.run(by, repo, "config", "--bool", "--get", "worktree.useRelativePaths")
	relative := err == nil && rel.Status == 0 && strings.TrimSpace(rel.Stdout) == "true"
	dotGit := filepath.Join(worktree, ".git")
	gitdir, link := dotGit, admin
	if relative {
		gitdir, link = relativePath(admin, dotGit), relativePath(worktree, admin)
	}
	line := func(prefix, p string) []byte { return []byte(prefix + p + "\n") }
	repaired := false
	for _, step := range []struct {
		path    string
		content []byte
	}{
		{filepath.Join(admin, "commondir"), []byte("../..\n")},
		{filepath.Join(admin, "gitdir"), line("", gitdir)},
		{dotGit, line("gitdir: ", link)},
	} {
		changed, err := put(step.path, step.content)
		if err != nil {
			return agent.WorktreeError(err.Error())
		}
		repaired = repaired || changed
	}
	// git never writes a `config` there; `config.worktree` would apply under worktreeConfig.
	// Only from a folder positively ours: never delete what may be the user's own. A folder
	// taken by the fallback keeps its `config.worktree`, which is harmless: git reads it only
	// under `extensions.worktreeConfig`, and Ensure refuses such repositories.
	if ours {
		for _, name := range []string{"config.worktree", "config"} {
			p := filepath.Join(admin, name)
			removed, err := removeAny(p)
			if err != nil {
				return werr("cannot remove %s: %v", p, err)
			}
			repaired = repaired || removed
		}
	}
	if repaired {
		fmt.Fprintf(os.Stderr, "agent: repaired the git links of worktree %s\n", slug)
	}
	return nil
}

func trimASCIIEnd(b []byte) []byte { return bytes.TrimRight(b, " \t\n\v\f\r") }
func trimASCII(b []byte) []byte    { return bytes.Trim(b, " \t\n\v\f\r") }

// namedGitdir is the `.git` path an admin folder's `gitdir` file names (relative to the admin folder).
func namedGitdir(admin string) (string, bool) {
	t, err := os.ReadFile(filepath.Join(admin, "gitdir"))
	if err != nil {
		return "", false
	}
	return joinPath(admin, string(trimASCIIEnd(t))), true
}

// linkedAdmin is the admin folder a worktree's `.git` file (`gitdir: <path>`, relative to its
// folder) names.
func linkedAdmin(dotGit string) (string, bool) {
	t, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	first, _, _ := bytes.Cut(t, []byte("\n"))
	line, ok := bytes.CutPrefix(first, []byte("gitdir:"))
	if !ok {
		return "", false
	}
	return joinPath(filepath.Dir(dotGit), string(trimASCII(line))), true
}

// relativePath is `to` relative to the folder `from` (both absolute and canonical), as git writes it.
func relativePath(from, to string) string {
	a := strings.Split(strings.Trim(from, "/"), "/")
	b := strings.Split(strings.Trim(to, "/"), "/")
	same := 0
	for same < len(a) && same < len(b) && a[same] == b[same] {
		same++
	}
	var parts []string
	for i := same; i < len(a); i++ {
		parts = append(parts, "..")
	}
	parts = append(parts, b[same:]...)
	return strings.Join(parts, "/")
}

// removeAny removes whatever is at path (a file, a symlink itself, or a folder); true if
// something was.
func removeAny(path string) (bool, error) {
	m, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if m.IsDir() {
		return true, os.RemoveAll(path)
	}
	return true, os.Remove(path)
}

var putCounter atomic.Uint64

// put makes path a regular file holding exactly content. A symlink there is replaced, never
// followed (the new file is renamed over it); a folder there is removed. True if it changed.
func put(path string, content []byte) (bool, error) {
	meta, merr := os.Lstat(path)
	if merr == nil && meta.Mode().IsRegular() {
		if c, err := os.ReadFile(path); err == nil && bytes.Equal(c, content) {
			return false, nil
		}
	}
	fail := func(e error) (bool, error) { return false, fmt.Errorf("cannot repair %s: %v", path, e) }
	// Unique per process and call, so two audits never share a temp file.
	tmp := fmt.Sprintf("%s.pixie-audit.%d.%d", path, os.Getpid(), putCounter.Add(1)-1)
	if _, err := removeAny(tmp); err != nil {
		return fail(err)
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return fail(err)
	}
	_, err = f.Write(content)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fail(err)
	}
	if merr == nil && meta.IsDir() {
		if err := os.RemoveAll(path); err != nil {
			return fail(err)
		}
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return true, nil
}

func eqASCIIFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// ResolveRepo: repo (a folder name under the projects root, or a path) as a canonical git
// repository root strictly inside the projects root, and not the coordinator's workspace.
func (g *GitWorktrees) ResolveRepo(repo string) (string, error) {
	by := g.deadline()
	raw := strings.TrimSpace(repo)
	root, err := canonicalize(g.root)
	if err != nil {
		return "", werr("The projects root %s does not exist.", g.root)
	}
	var candidate string
	if filepath.IsAbs(raw) {
		candidate = raw
	} else {
		candidate = root + "/" + raw
	}
	// "Focus" for ~/Documents/Projects/focus: a bare name matches a child folder in any case.
	if _, err := os.Stat(candidate); err != nil && !strings.Contains(raw, "/") {
		if entries, err := os.ReadDir(root); err == nil {
			for _, e := range entries {
				p := filepath.Join(root, e.Name())
				if isDir(p) && eqASCIIFold(e.Name(), raw) {
					candidate = p
					break
				}
			}
		}
	}
	real, err := canonicalize(candidate)
	if err != nil {
		return "", werr("No repository %s under the projects root.", raw)
	}
	if !hasPrefix(real, root) || real == root {
		return "", werr("%s is not inside the projects root.", raw)
	}
	if ws, err := canonicalize(g.workspace); err == nil {
		if hasPrefix(real, ws) || hasPrefix(ws, real) {
			return "", werr("That is the agent's own workspace, not a repository.")
		}
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(real, root), "/")
	if slices.Contains(strings.Split(rel, "/"), WorktreesDir) {
		return "", werr("%s is a worker's worktree, not a repository.", raw)
	}
	top, err := g.git(by, real, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", werr("%s is not a git repository.", raw)
	}
	if c, err := canonicalize(top); err != nil || c != real {
		return "", werr("%s is not the top folder of a git repository.", raw)
	}
	// A repo's own top folder: not a linked worktree (git-dir differs from the common dir), not a submodule.
	dirs, err := g.git(by, real, "rev-parse", "--git-dir", "--git-common-dir")
	if err != nil {
		return "", err
	}
	var resolved []string
	for _, l := range rustLines(dirs) {
		c, err := canonicalize(joinPath(real, strings.TrimSpace(l)))
		if err != nil {
			c = ""
		}
		resolved = append(resolved, c)
		if len(resolved) == 2 {
			break
		}
	}
	if len(resolved) < 2 || resolved[0] == "" || resolved[0] != resolved[1] {
		return "", werr("%s is a linked worktree, not a repository.", raw)
	}
	// A `.git` file pointing into another repo's `modules/` is a submodule even when its superproject is out of sight.
	if t, err := os.ReadFile(filepath.Join(real, ".git")); err == nil && strings.Contains(string(t), "/modules/") {
		return "", werr("%s is a submodule, not a repository.", raw)
	}
	super, err := g.git(by, real, "rev-parse", "--show-superproject-working-tree")
	if err != nil {
		return "", err
	}
	if super != "" {
		return "", werr("%s is a submodule, not a repository.", raw)
	}
	return real, nil
}

// Ensure: `<repo>/.worktrees/<slug>` on branch `pixie/<slug>` (an existing one is reused, also
// one on a pre-#68 `mascot/<slug>` branch; with no worktree, an existing `pixie/<slug>` branch,
// else an existing `mascot/<slug>` one, is checked out again, else `pixie/<slug>` is made), with
// `.worktrees/` in the repo's `.git/info/exclude`. Returns the worktree's canonical path.
func (g *GitWorktrees) Ensure(repo, slug string) (string, error) {
	by := g.deadline()
	if !agent.IsWorkerName(slug) {
		return "", werr("%q is not a worktree name.", slug)
	}
	folder := filepath.Join(repo, WorktreesDir)
	worktree := filepath.Join(folder, slug)
	for _, p := range []string{folder, worktree} {
		if m, err := os.Lstat(p); err == nil && m.Mode()&fs.ModeSymlink != 0 {
			return "", werr("%s is a symlink; I won't follow it.", p)
		}
	}
	inside := func(real string) (string, error) {
		r, err := canonicalize(repo)
		if err != nil {
			return "", werr("%s: %v", repo, err)
		}
		base := filepath.Join(r, WorktreesDir)
		if hasPrefix(real, base) {
			return real, nil
		}
		return "", werr("%s is not inside %s.", real, base)
	}
	perWorktree, err := g.run(by, repo, "config", "--bool", "--get", "extensions.worktreeConfig")
	if err != nil {
		return "", err
	}
	if perWorktree.Status == 0 && strings.TrimSpace(perWorktree.Stdout) == "true" {
		return "", werr("This repo uses per-worktree git config; I won't create worktrees in it.")
	}
	if _, err := os.Stat(worktree); err == nil {
		c, err := canonicalize(worktree)
		if err != nil {
			return "", werr("%s: %v", worktree, err)
		}
		real, err := inside(c)
		if err != nil {
			return "", err
		}
		// A worker may have rewritten its git links (it can write its admin folder): put
		// them back before git runs in it, or refuse it. The audit first checks that the admin
		// folder is on its worker branch: a worktree of the user's own is never touched.
		if err := g.auditLinks(by, repo, slug); err != nil {
			return "", err
		}
		listed, err := g.git(by, repo, "worktree", "list", "--porcelain")
		if err != nil {
			return "", err
		}
		var entry string
		found := false
	blocks:
		for _, block := range strings.Split(listed, "\n\n") {
			for _, l := range rustLines(block) {
				if p, ok := strings.CutPrefix(l, "worktree "); ok {
					if c, err := canonicalize(p); err == nil && c == real {
						entry, found = block, true
						break blocks
					}
				}
			}
		}
		if !found {
			return "", werr("%s exists but is not a worktree of this repository.", worktree)
		}
		onBranch, locked := false, false
		for _, l := range rustLines(entry) {
			if ref, ok := strings.CutPrefix(l, "branch "); ok && onWorkerBranch(ref, slug) {
				onBranch = true
			}
			if l == "locked" || strings.HasPrefix(l, "locked ") {
				locked = true
			}
		}
		if !onBranch {
			return "", werr("That worktree isn't on its pixie branch; I won't reuse it.")
		}
		if locked {
			return "", werr("%s is locked (still being set up?); remove it with `git worktree remove -f -f %s` and try again.", worktree, worktree)
		}
		index, err := g.git(by, real, "rev-parse", "--git-path", "index")
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(joinPath(real, index)); err != nil {
			return "", werr("%s is incomplete (no index); remove it with `git worktree remove -f -f %s` and try again.", worktree, worktree)
		}
		// Only now, past every refusal: a refused repo is left as it was.
		if err := g.exclude(by, repo); err != nil {
			return "", err
		}
		return real, nil
	}
	// Checkout runs filter programs, and includes can pull them in (even only inside the new worktree):
	// refuse any not from global or system config (git-lfs, ... stays trusted). Judged by scope, never by
	// origin path (git quotes unusual paths); anything unexpected is a refusal.
	scopes, err := g.run(by, repo, "config", "--show-scope", "--no-includes", "-z", "--get-regexp", `^(filter\..*\.(smudge|clean|process)|include(if)?\..*)$`)
	if err != nil {
		return "", err
	}
	if err := checkConfigScopes(scopes); err != nil {
		return "", err
	}
	if err := g.exclude(by, repo); err != nil {
		return "", err
	}
	// Drop registrations whose folders are gone (nothing else).
	_, _ = g.run(by, repo, "worktree", "prune")
	// The current prefix first: a worker's pre-#68 branch is reused only when it has no new one.
	branch, found := agent.BranchPrefix+slug, false
	for _, p := range agent.BranchPrefixes {
		has, err := g.run(by, repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+p+slug)
		if err != nil {
			return "", err
		}
		if has.Status == 0 {
			branch, found = p+slug, true
			break
		}
	}
	var added error
	if found {
		_, added = g.git(by, repo, "worktree", "add", worktree, branch)
	} else {
		_, added = g.git(by, repo, "worktree", "add", "-b", branch, worktree, "HEAD")
	}
	if added != nil {
		// A failed or timed-out add must not leave a half-made worktree to be reused later.
		// -f -f also removes a worktree a killed add left locked; the path did not exist before this add.
		_, _ = g.cleanup.Run(argv(repo, "worktree", "remove", "-f", "-f", worktree), g.env)
		g.removeStaleBranchLock(repo, branch)
		return "", added
	}
	c, err := canonicalize(worktree)
	if err != nil {
		return "", werr("%s: %v", worktree, err)
	}
	real, err := inside(c)
	if err != nil {
		return "", err
	}
	// Its admin folder is remembered now, while its links are git's own.
	g.mu.Lock()
	delete(g.admins, real)
	g.mu.Unlock()
	if err := g.auditLinks(by, repo, slug); err != nil {
		return "", err
	}
	return real, nil
}

// Audit re-checks the git links of `<repo>/.worktrees/<slug>`, which a sandboxed worker could
// write; an error when the admin folder can't be identified safely (nothing changed).
func (g *GitWorktrees) Audit(repo, slug string) error {
	return g.auditLinks(g.deadline(), repo, slug)
}

// ForeignSlugs: names in `<repo>/.worktrees/` that are not a worktree on `pixie/<name>` (or the
// pre-#68 `mascot/<name>`).
func (g *GitWorktrees) ForeignSlugs(repo string) []string {
	entries, err := os.ReadDir(filepath.Join(repo, WorktreesDir))
	if err != nil {
		return nil
	}
	var slugs []string
	for _, e := range entries {
		slug := e.Name()
		dotGit := filepath.Join(repo, WorktreesDir, slug, ".git")
		ok := false
		if a, found := linkedAdmin(dotGit); found {
			if h, err := os.ReadFile(filepath.Join(a, "HEAD")); err == nil {
				ok = headOnWorkerBranch(h, slug)
			}
		}
		if !ok {
			slugs = append(slugs, slug)
		}
	}
	slices.Sort(slugs)
	return slugs
}

// projectName: 1–64 of A–Z a–z 0–9 - _ ., not starting with '.' or '-' (#97).
var projectName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)

// CreateProject makes <root>/<name> a new git repository with one empty commit, so a worker's
// worktree can be made from it at once. It refuses a bad name or any existing path, and removes
// only a folder it made itself when a later step fails.
func (g *GitWorktrees) CreateProject(name string) (string, error) {
	if !projectName.MatchString(name) {
		return "", werr("%q is not a usable project name: use letters, digits, '-', '_' or '.'.", name)
	}
	root, err := canonicalize(g.root)
	if err != nil {
		return "", werr("The projects root %s does not exist.", g.root)
	}
	dir := filepath.Join(root, name)
	if _, err := os.Lstat(dir); err == nil {
		return "", werr("%s already exists under the projects root.", name)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", werr("Could not create %s: %v", name, err)
	}
	by := g.deadline()
	fail := func(step string, err error) (string, error) {
		_ = os.RemoveAll(dir)
		return "", werr("Could not %s for %s: %v", step, name, err)
	}
	if _, err := g.git(by, dir, "init", "-q", "-b", "main"); err != nil {
		return fail("initialise git", err)
	}
	args := []string{"commit", "-q", "--allow-empty", "-m", "Start " + name}
	if out, _ := g.run(by, dir, "config", "user.email"); strings.TrimSpace(out.Stdout) == "" {
		args = append([]string{"-c", "user.name=DeskPixie", "-c", "user.email=deskpixie@localhost"}, args...)
	}
	if _, err := g.git(by, dir, args...); err != nil {
		return fail("make the first commit", err)
	}
	return canonicalize(dir)
}
