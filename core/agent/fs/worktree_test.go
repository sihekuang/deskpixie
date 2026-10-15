package agentfs

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// fixture is a temp projects root with a temp HOME (no user git config is read).
type fixture struct {
	t               *testing.T
	dir, root, home string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := tempDir(t)
	f := &fixture{t: t, dir: dir, root: filepath.Join(dir, "projects"), home: filepath.Join(dir, "home")}
	mustMkdir(t, f.root)
	mustMkdir(t, f.home)
	return f
}

const gitPath = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"

func (f *fixture) git(dir string, args ...string) string {
	f.t.Helper()
	full := append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "init.defaultBranch=main"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = []string{"PATH=" + gitPath, "HOME=" + f.home}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, errb.String())
	}
	return strings.TrimSpace(out.String())
}

// repo is `<root>/<name>`: a git repo with one commit.
func (f *fixture) repo(name string) string {
	r := filepath.Join(f.root, name)
	mustMkdir(f.t, r)
	f.git(r, "init", "-q")
	mustWrite(f.t, filepath.Join(r, "README.md"), "hi\n")
	f.git(r, "add", "README.md")
	f.git(r, "commit", "-q", "-m", "first")
	return r
}

func (f *fixture) workspace() string { return filepath.Join(f.dir, "app/agent/workspace") }

func (f *fixture) fs() *GitWorktrees {
	return NewGitWorktrees(f.root, f.workspace(), f.home, procRunner{})
}

func (f *fixture) fsWith(r agent.CommandRunner) *GitWorktrees {
	return NewGitWorktrees(f.root, f.workspace(), f.home, r)
}

func (f *fixture) script(name, marker string) string {
	p := filepath.Join(f.dir, name)
	mustWrite(f.t, p, fmt.Sprintf("#!/bin/sh\ntouch %s\n", filepath.Join(f.dir, marker)))
	if err := os.Chmod(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return p
}

func errText(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	return err.Error()
}

func wantContains(t *testing.T, got, sub string) {
	t.Helper()
	if !strings.Contains(got, sub) {
		t.Fatalf("%q does not contain %q", got, sub)
	}
}

func countLine(text, line string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		if l == line {
			n++
		}
	}
	return n
}

func TestARepoResolvesByNameInAnyCaseOrByPath(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	g := f.fs()
	for _, in := range []string{"focus", "Focus", r} {
		got, err := g.ResolveRepo(in)
		if err != nil || got != r {
			t.Fatalf("%s: %q %v", in, got, err)
		}
	}
}

func TestAnythingButARepoRootInsideTheProjectsRootIsRefused(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	mustMkdir(t, filepath.Join(r, "src"))
	mustMkdir(t, filepath.Join(f.root, "plain"))
	outside := filepath.Join(f.dir, "elsewhere")
	mustMkdir(t, outside)
	g := f.fs()
	for _, c := range [][2]string{
		{"nope", "No repository nope"},
		{"plain", "not a git repository"},
		{"focus/src", "not the top folder"},
		{"..", "not inside the projects root"},
		{"../elsewhere", "not inside the projects root"},
		{".", "not inside the projects root"},
	} {
		_, err := g.ResolveRepo(c[0])
		if e := errText(t, err); !strings.Contains(e, c[1]) {
			t.Fatalf("%s: %s", c[0], e)
		}
	}
	_, err := g.ResolveRepo(outside)
	wantContains(t, errText(t, err), "not inside")
	// A symlink inside the root that points outside is judged by where it really goes.
	if err := os.Symlink(outside, filepath.Join(f.root, "sneaky")); err != nil {
		t.Fatal(err)
	}
	_, err = g.ResolveRepo("sneaky")
	wantContains(t, errText(t, err), "not inside")
}

func TestTheCoordinatorsWorkspaceAndWorktreesAreNotRepos(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	g := NewGitWorktrees(f.root, r, f.home, procRunner{})
	_, err := g.ResolveRepo("focus")
	wantContains(t, errText(t, err), "workspace")
	g = f.fs()
	wt, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	_, err = g.ResolveRepo(wt)
	wantContains(t, errText(t, err), "worktree")
}

func TestEnsureMakesAWorktreeOnItsBranchAndHidesItFromTheRepo(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	wt, err := f.fs().Ensure(r, "fix-upload")
	if err != nil {
		t.Fatal(err)
	}
	if wt != filepath.Join(r, ".worktrees/fix-upload") {
		t.Fatal(wt)
	}
	if mustRead(t, filepath.Join(wt, "README.md")) != "hi\n" {
		t.Fatal("README")
	}
	if b := f.git(wt, "rev-parse", "--abbrev-ref", "HEAD"); b != "pixie/fix-upload" {
		t.Fatal(b)
	}
	if n := countLine(mustRead(t, filepath.Join(r, ".git/info/exclude")), ".worktrees/"); n != 1 {
		t.Fatal(n)
	}
	if s := f.git(r, "status", "--porcelain"); s != "" {
		t.Fatalf("the main checkout sees nothing new: %q", s)
	}
}

func TestAnExistingWorktreeOrBranchIsReused(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	g := f.fs()
	wt, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(wt, "work.txt"), "in progress")
	again, err := g.Ensure(r, "fix")
	if err != nil || again != wt {
		t.Fatal(again, err)
	}
	if !exists(filepath.Join(wt, "work.txt")) {
		t.Fatal("kept as is")
	}
	if n := countLine(mustRead(t, filepath.Join(r, ".git/info/exclude")), ".worktrees/"); n != 1 {
		t.Fatalf("excluded once: %d", n)
	}
	f.git(r, "worktree", "remove", "--force", wt)
	again, err = g.Ensure(r, "fix")
	if err != nil || again != wt {
		t.Fatalf("the branch is checked out again: %q %v", again, err)
	}
	if b := f.git(wt, "rev-parse", "--abbrev-ref", "HEAD"); b != "pixie/fix" {
		t.Fatal(b)
	}
}

func TestAFolderInTheWayOrABadSlugIsRefused(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	mustMkdir(t, filepath.Join(r, ".worktrees/fix"))
	g := f.fs()
	_, err := g.Ensure(r, "fix")
	wantContains(t, errText(t, err), "not a worktree")
	for _, bad := range []string{"../x", "Fix", "", "a/b"} {
		if _, err := g.Ensure(r, bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestTheReposHooksNeverRun(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	hook := filepath.Join(r, ".git/hooks/post-checkout")
	mustWrite(t, hook, fmt.Sprintf("#!/bin/sh\ntouch %s\n", filepath.Join(f.dir, "hook-ran")))
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := f.fs().Ensure(r, "fix"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.dir, "hook-ran")) {
		t.Fatal("hook ran")
	}
}

func TestAPlantedFsmonitorNeverRuns(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	s := f.script("fsm.sh", "fsm-ran")
	f.git(r, "config", "core.fsmonitor", s)
	g := f.fs()
	if _, err := g.ResolveRepo("focus"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Ensure(r, "fix"); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.dir, "fsm-ran")) {
		t.Fatal("fsmonitor ran")
	}
}

func TestLocalFilterProgramsAreRefusedAndNeverRun(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	s := f.script("smudge.sh", "smudge-ran")
	f.git(r, "config", "filter.x.smudge", s)
	f.git(r, "config", "filter.x.clean", "cat")
	mustWrite(t, filepath.Join(r, ".gitattributes"), "* filter=x\n")
	f.git(r, "add", ".gitattributes")
	f.git(r, "commit", "-q", "-m", "attrs")
	_, err := f.fs().Ensure(r, "fix")
	wantContains(t, errText(t, err), "defines filter programs")
	if exists(filepath.Join(f.dir, "smudge-ran")) || exists(filepath.Join(r, ".worktrees/fix")) {
		t.Fatal("filter ran or worktree made")
	}
}

func TestASymlinkedWorktreesFolderOrSlugIsRefused(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	outside := filepath.Join(f.dir, "outside")
	mustMkdir(t, outside)
	if err := os.Symlink("../../outside", filepath.Join(r, ".worktrees")); err != nil {
		t.Fatal(err)
	}
	g := f.fs()
	_, err := g.Ensure(r, "fix")
	wantContains(t, errText(t, err), "symlink")
	if count(t, outside) != 0 {
		t.Fatal("nothing created outside")
	}
	if err := os.Remove(filepath.Join(r, ".worktrees")); err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Join(r, ".worktrees"))
	if err := os.Symlink(outside, filepath.Join(r, ".worktrees/fix")); err != nil {
		t.Fatal(err)
	}
	_, err = g.Ensure(r, "fix")
	wantContains(t, errText(t, err), "symlink")
	if count(t, outside) != 0 {
		t.Fatal("nothing created outside")
	}
}

func TestLinkedWorktreesAndSubmodulesAreNotRepos(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	linked := filepath.Join(f.root, "linked")
	f.git(r, "worktree", "add", "-q", "-b", "other", linked)
	g := f.fs()
	_, err := g.ResolveRepo("linked")
	wantContains(t, errText(t, err), "linked worktree")
	up := f.repo("upstream")
	sup := f.repo("super")
	f.git(sup, "-c", "protocol.file.allow=always", "submodule", "add", "-q", up, "sub")
	_, err = g.ResolveRepo("super/sub")
	wantContains(t, errText(t, err), "submodule")
	if _, err := g.ResolveRepo("super"); err != nil {
		t.Fatal(err)
	}
}

func TestALockedOrHalfMadeWorktreeIsNotReused(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	g := f.fs()
	wt, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	f.git(r, "worktree", "lock", "--reason", "initializing", wt)
	_, err = g.Ensure(r, "fix")
	wantContains(t, errText(t, err), "locked")
	f.git(r, "worktree", "unlock", wt)
	if again, err := g.Ensure(r, "fix"); err != nil || again != wt {
		t.Fatal(again, err)
	}
	gitdir := f.git(wt, "rev-parse", "--absolute-git-dir")
	if err := os.Remove(filepath.Join(gitdir, "index")); err != nil {
		t.Fatal(err)
	}
	_, err = g.Ensure(r, "fix")
	wantContains(t, errText(t, err), "incomplete")
}

func TestAFailedAddLeavesNoHalfMadeWorktree(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	// The branch exists and is checked out in the main repo, so `worktree add <path> <branch>` fails.
	f.git(r, "branch", "-m", "pixie/fix")
	_, err := f.fs().Ensure(r, "fix")
	wantContains(t, errText(t, err), "git worktree")
	if exists(filepath.Join(r, ".worktrees/fix")) {
		t.Fatal("worktree left")
	}
	if strings.Contains(f.git(r, "worktree", "list", "--porcelain"), ".worktrees") {
		t.Fatal("registration left")
	}
}

func TestConfigIncludesAreRefusedEvenWhenTheyOnlyApplyInTheWorktree(t *testing.T) {
	f := newFixture(t)
	s := f.script("smudge.sh", "smudge-ran")
	inc := filepath.Join(f.dir, "extra.conf")
	mustWrite(t, inc, fmt.Sprintf("[filter \"x\"]\n\tsmudge = %s\n\tclean = cat\n", s))
	for _, c := range [][2]string{{"path", "include.path"}, {"onbranch", "includeIf.onbranch:pixie/**.path"}, {"gitdir", "includeIf.gitdir:/**.path"}} {
		r := f.repo(c[0])
		mustWrite(t, filepath.Join(r, ".gitattributes"), "* filter=x\n")
		f.git(r, "add", ".gitattributes")
		f.git(r, "commit", "-q", "-m", "attrs")
		f.git(r, "config", c[1], inc)
		_, err := f.fs().Ensure(r, "fix")
		wantContains(t, errText(t, err), "includes other config files")
		if exists(filepath.Join(f.dir, "smudge-ran")) || exists(filepath.Join(r, ".worktrees/fix")) {
			t.Fatalf("%s: filter ran or worktree made", c[0])
		}
	}
}

// impatientAdd times out only `worktree add` (1 ms); everything else runs normally.
type impatientAdd struct{ procRunner }

func (i impatientAdd) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	if slices.Contains(argv, "add") {
		return procRunner{timeout: time.Millisecond}.Run(argv, env)
	}
	return procRunner{}.Run(argv, env)
}

func (i impatientAdd) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	return agent.DefaultRunBy(i, argv, env, deadline)
}

func TestATimedOutAddIsCleanedUpAndTheNextEnsureSucceeds(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	for i := 0; i < 3000; i++ {
		mustWrite(t, filepath.Join(r, fmt.Sprintf("f%d.txt", i)), fmt.Sprintf("%d\n", i))
	}
	f.git(r, "add", ".")
	f.git(r, "commit", "-q", "-m", "many")
	slow := f.fsWith(impatientAdd{}).WithCleanupRunner(procRunner{timeout: 60 * time.Second})
	_, err := slow.Ensure(r, "fix")
	e := errText(t, err)
	if !strings.Contains(e, "timed out") || strings.Contains(e, "git: git:") {
		t.Fatal(e)
	}
	if exists(filepath.Join(r, ".worktrees/fix")) {
		t.Fatal("no folder left")
	}
	if strings.Contains(f.git(r, "worktree", "list", "--porcelain"), ".worktrees") {
		t.Fatal("no registration left")
	}
	wt, err := f.fs().Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(wt, "f2999.txt")) {
		t.Fatal("not checked out")
	}
}

// sluggish: every call takes 200 ms more; RunBy honours the deadline.
type sluggish struct{ procRunner }

func (sluggish) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	time.Sleep(200 * time.Millisecond)
	return procRunner{}.Run(argv, env)
}

func (sluggish) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	time.Sleep(200 * time.Millisecond)
	return procRunner{timeout: 60 * time.Second}.RunBy(argv, env, deadline)
}

func TestEnsureStopsAtItsBudget(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	budget := 700 * time.Millisecond
	slow := f.fsWith(sluggish{}).WithBudget(budget)
	began := time.Now()
	_, err := slow.Ensure(r, "fix")
	wantContains(t, errText(t, err), "timed out")
	if el := time.Since(began); el >= budget+600*time.Millisecond {
		t.Fatal(el)
	}
	if exists(filepath.Join(r, ".worktrees/fix")) {
		t.Fatal("worktree made")
	}
	if _, err := f.fs().Ensure(r, "fix"); err != nil {
		t.Fatalf("the default budget is enough: %v", err)
	}
}

// killedAdd: `worktree add` dies as if killed while holding its branch's ref lock (and leaves
// the given other locks too); everything else runs normally.
type killedAdd struct {
	procRunner
	locks []string
}

func (k killedAdd) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	if slices.Contains(argv, "add") {
		for _, lock := range k.locks {
			if err := os.MkdirAll(filepath.Dir(lock), 0o777); err != nil {
				panic(err)
			}
			if err := os.WriteFile(lock, nil, 0o666); err != nil {
				panic(err)
			}
		}
		return agent.CommandOutput{}, fmt.Errorf("git: timed out after 45s")
	}
	return procRunner{}.Run(argv, env)
}

func (k killedAdd) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	return agent.DefaultRunBy(k, argv, env, deadline)
}

func TestAKilledAddLeavesNoStaleLockOnItsOwnBranchOnly(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	git := filepath.Join(r, ".git")
	own := filepath.Join(git, "refs/heads/pixie/fix.lock")
	others := []string{filepath.Join(git, "refs/heads/mine.lock"), filepath.Join(git, "refs/heads/pixie/other.lock"), filepath.Join(git, "index.lock"), filepath.Join(git, "packed-refs.lock")}
	planted := append([]string{own}, others...)
	killed := f.fsWith(killedAdd{locks: planted})
	_, err := killed.Ensure(r, "fix")
	wantContains(t, errText(t, err), "timed out")
	if exists(own) {
		t.Fatal("the killed add's own branch lock is removed")
	}
	for _, o := range others {
		if !exists(o) {
			t.Fatalf("%s is left alone", o)
		}
		if err := os.Remove(o); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.fs().Ensure(r, "fix"); err != nil {
		t.Fatalf("the next ensure succeeds: %v", err)
	}
}

func TestASymlinkedBranchLockIsNotFollowedOrRemoved(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	target := filepath.Join(f.dir, "keep")
	mustWrite(t, target, "x")
	own := filepath.Join(r, ".git/refs/heads/pixie/fix.lock")
	mustMkdir(t, filepath.Dir(own))
	if err := os.Symlink(target, own); err != nil {
		t.Fatal(err)
	}
	killed := f.fsWith(killedAdd{})
	if _, err := killed.Ensure(r, "fix"); err == nil {
		t.Fatal("expected an error")
	}
	if m, err := os.Lstat(own); err != nil || m.Mode()&os.ModeSymlink == 0 {
		t.Fatal("a symlink is not removed")
	}
	if !exists(target) {
		t.Fatal("target removed")
	}
}

func TestADotGitFileIntoAnotherReposModulesIsASubmodule(t *testing.T) {
	f := newFixture(t)
	r := filepath.Join(f.root, "focus")
	mustMkdir(t, r)
	sep := filepath.Join(f.dir, "outer/.git/modules/focus")
	mustMkdir(t, filepath.Dir(sep))
	f.git(r, "init", "-q", "--separate-git-dir", sep)
	if m, err := os.Stat(filepath.Join(r, ".git")); err != nil || !m.Mode().IsRegular() {
		t.Fatal(".git is not a file")
	}
	_, err := f.fs().ResolveRepo("focus")
	wantContains(t, errText(t, err), "submodule")
}

func TestPerWorktreeConfigIsRefusedAndNeverRunsItsFilters(t *testing.T) {
	f := newFixture(t)
	s := f.script("smudge.sh", "smudge-ran")
	r := f.repo("focus")
	f.git(r, "config", "extensions.worktreeConfig", "true")
	f.git(r, "config", "--worktree", "filter.x.smudge", s)
	f.git(r, "config", "--worktree", "filter.x.clean", "cat")
	mustWrite(t, filepath.Join(r, ".gitattributes"), "* filter=x\n")
	f.git(r, "add", ".gitattributes")
	f.git(r, "commit", "-q", "-m", "attrs")
	_, err := f.fs().Ensure(r, "fix")
	wantContains(t, errText(t, err), "per-worktree git config")
	if exists(filepath.Join(f.dir, "smudge-ran")) {
		t.Fatal("filter ran")
	}
	// An include placed in config.worktree is refused the same way.
	r2 := f.repo("second")
	f.git(r2, "config", "extensions.worktreeConfig", "true")
	inc := filepath.Join(f.dir, "extra.conf")
	mustWrite(t, inc, "[filter \"x\"]\n\tclean = cat\n")
	f.git(r2, "config", "--worktree", "include.path", inc)
	_, err = f.fs().Ensure(r2, "fix")
	wantContains(t, errText(t, err), "per-worktree")
	if exists(filepath.Join(r2, ".worktrees/fix")) {
		t.Fatal("worktree made")
	}
}

func TestLocalFiltersInASeparateGitDirWithAQuotedPathAreRefused(t *testing.T) {
	// git quotes paths with non-ASCII bytes or `"` (e.g. in --show-origin); the check must not depend on paths.
	for _, c := range [][2]string{{"nonascii", "é"}, {"quote", `a"b`}} {
		f := newFixture(t)
		s := f.script("smudge.sh", "smudge-ran")
		r := filepath.Join(f.root, "focus")
		mustMkdir(t, r)
		sep := filepath.Join(f.dir, c[1], "sp.git")
		mustMkdir(t, filepath.Dir(sep))
		f.git(r, "init", "-q", "--separate-git-dir", sep)
		mustWrite(t, filepath.Join(r, ".gitattributes"), "* filter=x\n")
		f.git(r, "add", ".gitattributes")
		f.git(r, "commit", "-q", "-m", "attrs")
		f.git(r, "config", "filter.x.smudge", s)
		f.git(r, "config", "filter.x.clean", "cat")
		_, err := f.fs().Ensure(r, "fix")
		if e := errText(t, err); !strings.Contains(e, "defines filter programs") {
			t.Fatalf("%s: %s", c[0], e)
		}
		if exists(filepath.Join(f.dir, "smudge-ran")) {
			t.Fatalf("%s: the filter ran", c[0])
		}
		if exists(filepath.Join(r, ".worktrees/fix")) {
			t.Fatalf("%s: worktree made", c[0])
		}
	}
}

// cannedScopes answers the config-scope check with a canned reply; everything else runs normally.
type cannedScopes struct {
	procRunner
	status int32
	stdout string
}

func (c cannedScopes) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	if slices.Contains(argv, "--show-scope") {
		return agent.CommandOutput{Status: c.status, Stdout: c.stdout}, nil
	}
	return procRunner{}.Run(argv, env)
}

func (c cannedScopes) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	return agent.DefaultRunBy(c, argv, env, deadline)
}

func TestTheConfigCheckFailsClosedOnUnexpectedGitOutput(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	with := func(status int32, stdout string) *GitWorktrees {
		return f.fsWith(cannedScopes{status: status, stdout: stdout})
	}
	for _, c := range []struct {
		status int32
		stdout string
	}{
		{128, ""},
		{1, "global\x00filter.x.smudge\ncat\x00"},
		{0, "unknown\x00filter.x.smudge\ncat\x00"},
		{0, "command\x00filter.x.smudge\ncat\x00"},
		{0, "worktree\x00include.path\nx\x00"},
		{0, "global\x00filter.x.smudge\ncat\x00local"},
		{0, "global filter.x.smudge cat\n"},
	} {
		if _, err := with(c.status, c.stdout).Ensure(r, "fix"); err == nil {
			t.Fatalf("%d %q: accepted", c.status, c.stdout)
		}
		if exists(filepath.Join(r, ".worktrees/fix")) {
			t.Fatalf("%d %q: worktree made", c.status, c.stdout)
		}
	}
	if _, err := with(0, "system\x00filter.lfs.smudge\ncat\x00global\x00include.path\n/x\x00").Ensure(r, "fix"); err != nil {
		t.Fatal(err)
	}
}

// adminOfWt is the worktree's admin folder (from git, before any tampering).
func (f *fixture) adminOf(wt string) string { return f.git(wt, "rev-parse", "--absolute-git-dir") }

// links are `commondir`, `gitdir` and the worktree's `.git` file as bytes.
func links(t *testing.T, admin, wt string) [3]string {
	return [3]string{mustRead(t, filepath.Join(admin, "commondir")), mustRead(t, filepath.Join(admin, "gitdir")), mustRead(t, filepath.Join(wt, ".git"))}
}

// evilGitDir is a git dir whose config runs script as fsmonitor.
func (f *fixture) evilGitDir(script string) string {
	evil := filepath.Join(f.dir, "evil")
	mustMkdir(f.t, evil)
	f.git(evil, "init", "-q")
	f.git(evil, "config", "core.fsmonitor", script)
	return filepath.Join(evil, ".git")
}

// statusIn runs `git status` in the worktree, with no user config; whether it succeeded is not judged.
func (f *fixture) statusIn(wt string) {
	cmd := exec.Command("git", "-C", wt, "status")
	cmd.Env = []string{"PATH=" + gitPath, "HOME=" + f.home}
	_ = cmd.Run()
}

func TestAuditRestoresExactlyWhatGitWroteAndAPlantedFsmonitorNeverRuns(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	g := f.fs()
	wt, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	admin := f.adminOf(wt)
	original := links(t, admin, wt)
	if original[0] != "../..\n" {
		t.Fatal(original[0])
	}
	s := f.script("fsm.sh", "fsm-ran")
	evil := f.evilGitDir(s)
	mustWrite(t, filepath.Join(admin, "commondir"), evil+"\n")
	mustWrite(t, filepath.Join(admin, "gitdir"), "/somewhere/else/.git\n")
	mustWrite(t, filepath.Join(admin, "config.worktree"), fmt.Sprintf("[core]\n\tfsmonitor = %s\n", s))
	f.statusIn(wt)
	ran := filepath.Join(f.dir, "fsm-ran")
	if !exists(ran) {
		t.Fatal("the plant works before the audit")
	}
	if err := os.Remove(ran); err != nil {
		t.Fatal(err)
	}
	if err := g.Audit(r, "fix"); err != nil {
		t.Fatal(err)
	}
	if links(t, admin, wt) != original {
		t.Fatal("links not restored")
	}
	if exists(filepath.Join(admin, "config.worktree")) {
		t.Fatal("config.worktree left")
	}
	f.statusIn(wt)
	if exists(ran) {
		t.Fatal("the planted fsmonitor ran after the audit")
	}
	if b := f.git(wt, "rev-parse", "--abbrev-ref", "HEAD"); b != "pixie/fix" {
		t.Fatal(b)
	}
	// Nothing to repair: unchanged.
	if err := g.Audit(r, "fix"); err != nil {
		t.Fatal(err)
	}
	if links(t, admin, wt) != original {
		t.Fatal("links changed")
	}
}

func TestAuditReplacesSymlinksAndAGitFolderWithRegularFiles(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	g := f.fs()
	wt, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	admin := f.adminOf(wt)
	original := links(t, admin, wt)
	s := f.script("fsm.sh", "fsm-ran")
	evil := f.evilGitDir(s)
	planted := filepath.Join(f.dir, "planted")
	mustWrite(t, planted, evil+"\n")
	for _, name := range []string{"commondir", "gitdir", "config.worktree"} {
		_ = os.Remove(filepath.Join(admin, name))
		if err := os.Symlink(planted, filepath.Join(admin, name)); err != nil {
			t.Fatal(err)
		}
	}
	// The worktree's `.git` replaced by a whole repository with a planted fsmonitor.
	if err := os.Remove(filepath.Join(wt, ".git")); err != nil {
		t.Fatal(err)
	}
	f.git(wt, "init", "-q")
	f.git(wt, "config", "core.fsmonitor", s)
	if err := g.Audit(r, "fix"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"commondir", "gitdir"} {
		if m, _ := os.Lstat(filepath.Join(admin, name)); !m.Mode().IsRegular() {
			t.Fatal(name)
		}
	}
	if m, _ := os.Lstat(filepath.Join(wt, ".git")); !m.Mode().IsRegular() {
		t.Fatal(".git is not a file")
	}
	if exists(filepath.Join(admin, "config.worktree")) {
		t.Fatal("config.worktree left")
	}
	if links(t, admin, wt) != original {
		t.Fatal("links not restored")
	}
	if mustRead(t, planted) != evil+"\n" {
		t.Fatal("a symlink's target is never written")
	}
	f.statusIn(wt)
	if exists(filepath.Join(f.dir, "fsm-ran")) {
		t.Fatal("fsmonitor ran")
	}
}

func TestAuditKeepsRelativeLinksWhenGitWritesThem(t *testing.T) {
	f := newFixture(t)
	mustWrite(t, filepath.Join(f.home, ".gitconfig"), "[worktree]\n\tuseRelativePaths = true\n")
	r := f.repo("focus")
	g := f.fs()
	wt, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	admin := f.adminOf(wt)
	original := links(t, admin, wt)
	if original[2] != "gitdir: ../../.git/worktrees/fix\n" {
		t.Skipf("this git does not write relative links (%q)", original[2])
	}
	mustWrite(t, filepath.Join(admin, "commondir"), "/tmp\n")
	mustWrite(t, filepath.Join(admin, "gitdir"), "../../../elsewhere/.git\n")
	mustWrite(t, filepath.Join(wt, ".git"), "gitdir: "+admin+"\n")
	if err := g.Audit(r, "fix"); err != nil {
		t.Fatal(err)
	}
	if links(t, admin, wt) != original {
		t.Fatalf("%q", links(t, admin, wt))
	}
}

func TestATamperedWorktreeIsRepairedOnReuse(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	wt, err := f.fs().Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	admin := f.adminOf(wt)
	original := links(t, admin, wt)
	s := f.script("fsm.sh", "fsm-ran")
	evil := f.evilGitDir(s)
	mustWrite(t, filepath.Join(admin, "commondir"), evil+"\n")
	mustWrite(t, filepath.Join(admin, "gitdir"), "/somewhere/else/.git\n")
	// A new app run (nothing remembered) reuses it.
	if again, err := f.fs().Ensure(r, "fix"); err != nil || again != wt {
		t.Fatal(again, err)
	}
	if links(t, admin, wt) != original {
		t.Fatal("links not restored")
	}
	if exists(filepath.Join(f.dir, "fsm-ran")) {
		t.Fatal("fsmonitor ran")
	}
}

func TestATamperedWorktreeWhoseAdminFolderIsAmbiguousIsRefused(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	g := f.fs()
	wt, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	admin := f.adminOf(wt)
	// The user's own worktree elsewhere, also called fix: git names its admin folder fix1.
	other := filepath.Join(f.dir, "other/fix")
	f.git(r, "worktree", "add", "-q", "-b", "other", other)
	if filepath.Base(f.adminOf(other)) != "fix1" {
		t.Fatal(f.adminOf(other))
	}
	s := f.script("fsm.sh", "fsm-ran")
	evil := f.evilGitDir(s)
	mustWrite(t, filepath.Join(admin, "commondir"), evil+"\n")
	mustWrite(t, filepath.Join(admin, "gitdir"), "/somewhere/else/.git\n")
	fresh := f.fs()
	_, err = fresh.Ensure(r, "fix")
	wantContains(t, errText(t, err), "couldn't tell")
	if fresh.Audit(r, "fix") == nil {
		t.Fatal("audit should refuse")
	}
	if mustRead(t, filepath.Join(admin, "commondir")) != evil+"\n" {
		t.Fatal("nothing is changed then")
	}
	// The run that made it remembers its admin folder and repairs it.
	if err := g.Audit(r, "fix"); err != nil {
		t.Fatal(err)
	}
	if mustRead(t, filepath.Join(admin, "commondir")) != "../..\n" {
		t.Fatal("not repaired")
	}
	if b := f.git(other, "rev-parse", "--abbrev-ref", "HEAD"); b != "other" {
		t.Fatalf("the user's worktree is untouched: %s", b)
	}
}

// snapshot is every file in dir with its bytes.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Type().IsRegular() {
			out[e.Name()] = mustRead(t, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func TestAUsersOwnWorktreeNamedLikeTheSlugIsNeverTakenOver(t *testing.T) {
	// Its branch is the user's own, or even `pixie/fix`: either way it is a live worktree elsewhere.
	for _, c := range [][2]string{{"user", "mine"}, {"pixiebranch", "pixie/fix"}, {"mascotbranch", "mascot/fix"}} {
		f := newFixture(t)
		r := f.repo("focus")
		theirs := filepath.Join(f.dir, "elsewhere/fix")
		f.git(r, "worktree", "add", "-q", "-b", c[1], theirs)
		admin := f.adminOf(theirs)
		if filepath.Base(admin) != "fix" {
			t.Fatal(admin)
		}
		mustWrite(t, filepath.Join(admin, "config.worktree"), "[core]\n\tbare = false\n")
		mustWrite(t, filepath.Join(admin, "config"), "[user]\n\tname = me\n")
		before, dotGit := snapshot(t, admin), mustRead(t, filepath.Join(theirs, ".git"))
		// A stray, unregistered folder where the worker's worktree would be.
		stray := filepath.Join(r, ".worktrees/fix")
		mustMkdir(t, stray)
		g := f.fs()
		if _, err := g.Ensure(r, "fix"); err == nil {
			t.Fatalf("%s: ensure accepted", c[0])
		}
		if g.Audit(r, "fix") == nil {
			t.Fatalf("%s: audit accepted", c[0])
		}
		if !reflect.DeepEqual(snapshot(t, admin), before) {
			t.Fatalf("%s: the user's admin files are untouched", c[0])
		}
		if mustRead(t, filepath.Join(theirs, ".git")) != dotGit {
			t.Fatalf("%s: .git changed", c[0])
		}
		if exists(filepath.Join(stray, ".git")) {
			t.Fatalf("%s: stray got a .git", c[0])
		}
		if b := f.git(theirs, "rev-parse", "--abbrev-ref", "HEAD"); b != c[1] {
			t.Fatalf("%s: %s", c[0], b)
		}
	}
}

func TestARememberedAdminFolderOffThePixieBranchIsNotTrusted(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	g := f.fs()
	wt, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	admin := f.adminOf(wt)
	// The folder it remembered now holds another branch (e.g. it was reused): hands off.
	mustWrite(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/mine\n")
	mustWrite(t, filepath.Join(admin, "config.worktree"), "[core]\n\tbare = false\n")
	mustWrite(t, filepath.Join(admin, "gitdir"), "/somewhere/else/.git\n")
	before := snapshot(t, admin)
	if e := errText(t, g.Audit(r, "fix")); e != "That worktree isn't on its pixie branch; I won't reuse it." {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(snapshot(t, admin), before) {
		t.Fatal("nothing is changed then")
	}
}

func TestAWorktreeOffItsPixieBranchIsNotReused(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	g := f.fs()
	wt, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	f.git(wt, "switch", "-q", "-c", "elsewhere")
	_, err = g.Ensure(r, "fix")
	if e := errText(t, err); e != "That worktree isn't on its pixie branch; I won't reuse it." {
		t.Fatal(e)
	}
}

func excluded(t *testing.T, r string) bool {
	b, err := os.ReadFile(filepath.Join(r, ".git/info/exclude"))
	return err == nil && countLine(string(b), ".worktrees/") > 0
}

func TestARefusedRepoGetsNoExcludeLine(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	f.git(r, "config", "extensions.worktreeConfig", "true")
	if _, err := f.fs().Ensure(r, "fix"); err == nil {
		t.Fatal("expected an error")
	}
	if excluded(t, r) {
		t.Fatal("per-worktree config")
	}
	r2 := f.repo("second")
	f.git(r2, "config", "filter.x.clean", "cat")
	if _, err := f.fs().Ensure(r2, "fix"); err == nil {
		t.Fatal("expected an error")
	}
	if excluded(t, r2) {
		t.Fatal("a local filter")
	}
}

func TestAUsersOwnWorktreeInTheWorktreesFolderIsNeverModified(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	theirs := filepath.Join(r, ".worktrees/fix")
	f.git(r, "worktree", "add", "-q", "-b", "mine", theirs)
	admin := f.adminOf(theirs)
	mustWrite(t, filepath.Join(admin, "config.worktree"), "[core]\n\tbare = false\n")
	before, dotGit := snapshot(t, admin), mustRead(t, filepath.Join(theirs, ".git"))
	g := f.fs()
	if got := g.ForeignSlugs(r); !reflect.DeepEqual(got, []string{"fix"}) {
		t.Fatalf("taken: no worker is named for it: %v", got)
	}
	if _, err := g.Ensure(r, "fix"); err == nil {
		t.Fatal("ensure accepted")
	}
	if g.Audit(r, "fix") == nil {
		t.Fatal("audit accepted")
	}
	if !reflect.DeepEqual(snapshot(t, admin), before) {
		t.Fatal("the user's admin files are untouched")
	}
	if mustRead(t, filepath.Join(theirs, ".git")) != dotGit {
		t.Fatal(".git changed")
	}
	if excluded(t, r) {
		t.Fatal("a refused repo gets no exclude line")
	}
	if b := f.git(theirs, "rev-parse", "--abbrev-ref", "HEAD"); b != "mine" {
		t.Fatal(b)
	}
	if _, err := g.Ensure(r, "other"); err != nil {
		t.Fatal(err)
	}
	if got := g.ForeignSlugs(r); !reflect.DeepEqual(got, []string{"fix"}) {
		t.Fatalf("a worker's own worktree is not foreign: %v", got)
	}
}

func TestConcurrentRepairsOfOneFileNeverCollide(t *testing.T) {
	f := newFixture(t)
	target := filepath.Join(f.dir, "gitdir")
	var wg sync.WaitGroup
	var mu sync.Mutex
	failed := 0
	for th := 0; th < 8; th++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if _, err := put(target, []byte(fmt.Sprintf("%d-%d\n", th, i))); err != nil {
					mu.Lock()
					failed++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	if failed != 0 {
		t.Fatalf("%d repairs failed on another's temp file", failed)
	}
	entries, _ := os.ReadDir(f.dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "pixie-audit") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestGlobalFiltersSuchAsLfsStayTrusted(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	mustWrite(t, filepath.Join(f.home, ".gitconfig"), "[filter \"lfs\"]\n\tclean = cat\n\tsmudge = cat\n\tprocess = cat\n")
	if _, err := f.fs().Ensure(r, "fix"); err != nil {
		t.Fatal(err)
	}
}

// EnsureBudget is copied verbatim from the Rust runtime; P9 asserts its relations to the other
// worker-start limits (EnsureBudget + DialogWait + PostStartCheck < WorkerStartLimit).
func TestEnsureBudgetIsTheRustValue(t *testing.T) {
	if EnsureBudget != 55*time.Second {
		t.Fatal(EnsureBudget)
	}
}

func TestCreateProjectMakesARepoAWorkerCanUse(t *testing.T) {
	f := newFixture(t)
	g := f.fs()
	path, err := g.CreateProject("spacex-dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := filepath.EvalSymlinks(filepath.Join(f.root, "spacex-dashboard")); path != want {
		t.Fatalf("%s != %s", path, want)
	}
	if got := f.git(path, "log", "--format=%s"); got != "Start spacex-dashboard" {
		t.Fatalf("%q", got)
	}
	if _, err := g.Ensure(path, "first"); err != nil {
		t.Fatal("a worktree must be creatable at once: ", err)
	}
	if _, err := g.CreateProject("spacex-dashboard"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}
}

func TestCreateProjectRefusesBadNamesWithoutTouchingDisk(t *testing.T) {
	f := newFixture(t)
	g := f.fs()
	mustWrite(t, filepath.Join(f.root, "afile"), "x")
	before, _ := os.ReadDir(f.root)
	for _, name := range []string{"", ".", "..", ".hidden", "-x", "a/b", "naïve", "has space", strings.Repeat("a", 65), "afile"} {
		if _, err := g.CreateProject(name); err == nil {
			t.Errorf("%q was accepted", name)
		}
	}
	after, _ := os.ReadDir(f.root)
	if len(after) != len(before) {
		t.Fatalf("disk changed: %v -> %v", before, after)
	}
}
