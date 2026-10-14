package agentfs

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// Back-compat for workers made before the pixie rename (#68): their worktrees are on
// `mascot/<slug>` branches, which stay recognized, reused, audited and cleaned up.

// legacyWorktree makes `<r>/.worktrees/<slug>` on `mascot/<slug>` as a pre-#68 build would have
// (with git itself), with some work in progress in it.
func (f *fixture) legacyWorktree(r, slug string) string {
	f.t.Helper()
	wt := filepath.Join(r, ".worktrees", slug)
	f.git(r, "worktree", "add", "-q", "-b", "mascot/"+slug, wt, "HEAD")
	mustWrite(f.t, filepath.Join(wt, "work.txt"), "in progress")
	return wt
}

func TestALegacyMascotWorktreeIsReusedAndAudited(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	wt := f.legacyWorktree(r, "fix")
	g := f.fs()
	got, err := g.Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := canonicalize(wt); got != c {
		t.Fatal(got)
	}
	if !exists(filepath.Join(wt, "work.txt")) {
		t.Fatal("kept as is")
	}
	if b := f.git(wt, "rev-parse", "--abbrev-ref", "HEAD"); b != "mascot/fix" {
		t.Fatalf("it stays on its own branch: %s", b)
	}
	if f.git(r, "branch", "--list", "pixie/fix") != "" {
		t.Fatal("no pixie branch is made for it")
	}
	if err := g.Audit(r, "fix"); err != nil {
		t.Fatalf("audited as a worker's: %v", err)
	}
	// A fresh GitWorktrees (an app restart) finds its admin folder by its branch too.
	if err := f.fs().Audit(r, "fix"); err != nil {
		t.Fatalf("audited after a restart: %v", err)
	}
	if foreign := g.ForeignSlugs(r); len(foreign) != 0 {
		t.Fatalf("not foreign: %v", foreign)
	}
}

func TestATamperedLegacyWorktreeIsRepairedOnReuse(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	wt := f.legacyWorktree(r, "fix")
	admin := f.adminOf(wt)
	links := func() [2]string {
		return [2]string{mustRead(t, filepath.Join(wt, ".git")), mustRead(t, filepath.Join(admin, "gitdir"))}
	}
	before := links()
	mustWrite(t, filepath.Join(admin, "gitdir"), "/somewhere/else/.git\n")
	if _, err := f.fs().Ensure(r, "fix"); err != nil {
		t.Fatal(err)
	}
	if links() != before {
		t.Fatalf("repaired: %v", links())
	}
}

func TestALegacyWorktreeOffItsBranchIsStillRefused(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	wt := f.legacyWorktree(r, "fix")
	f.git(wt, "switch", "-q", "-c", "elsewhere")
	_, err := f.fs().Ensure(r, "fix")
	if e := errText(t, err); e != "That worktree isn't on its pixie branch; I won't reuse it." {
		t.Fatal(e)
	}
	// Another slug's legacy branch is not this slug's.
	f.git(wt, "switch", "-q", "-c", "mascot/other")
	if _, err := f.fs().Ensure(r, "fix"); err == nil {
		t.Fatal("ensure accepted mascot/other for fix")
	}
	if !slices.Contains(f.fs().ForeignSlugs(r), "fix") {
		t.Fatal("foreign")
	}
}

func TestALegacyBranchWithoutItsWorktreeIsCheckedOutAgain(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	wt := f.legacyWorktree(r, "fix")
	f.git(wt, "add", "work.txt")
	f.git(wt, "commit", "-q", "-m", "work")
	f.git(r, "worktree", "remove", "--force", wt)
	got, err := f.fs().Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	if b := f.git(got, "rev-parse", "--abbrev-ref", "HEAD"); b != "mascot/fix" {
		t.Fatalf("the worker's old branch (and its work) is reused: %s", b)
	}
	if !exists(filepath.Join(got, "work.txt")) {
		t.Fatal("its committed work is there")
	}
	// A new worker gets a pixie branch.
	other, err := f.fs().Ensure(r, "new")
	if err != nil {
		t.Fatal(err)
	}
	if b := f.git(other, "rev-parse", "--abbrev-ref", "HEAD"); b != "pixie/new" {
		t.Fatal(b)
	}
}

func TestAPixieBranchWinsOverALegacyOneOfTheSameSlug(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	f.git(r, "branch", "mascot/fix")
	f.git(r, "branch", "pixie/fix")
	got, err := f.fs().Ensure(r, "fix")
	if err != nil {
		t.Fatal(err)
	}
	if b := f.git(got, "rev-parse", "--abbrev-ref", "HEAD"); b != "pixie/fix" {
		t.Fatal(b)
	}
}

func TestAKilledAddOfALegacyBranchLeavesNoStaleLock(t *testing.T) {
	f := newFixture(t)
	r := f.repo("focus")
	f.git(r, "branch", "mascot/fix")
	git := filepath.Join(r, ".git")
	own := filepath.Join(git, "refs/heads/mascot/fix.lock")
	other := filepath.Join(git, "refs/heads/pixie/fix.lock")
	killed := f.fsWith(killedAdd{locks: []string{own, other}})
	_, err := killed.Ensure(r, "fix")
	wantContains(t, errText(t, err), "timed out")
	if exists(own) {
		t.Fatal("the killed add's own (legacy) branch lock is removed")
	}
	if !exists(other) {
		t.Fatal("a lock on a branch it did not add is left alone")
	}
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	if _, err := f.fs().Ensure(r, "fix"); err != nil {
		t.Fatalf("the next ensure succeeds: %v", err)
	}
}

func TestACodexWorkerOnALegacyBranchMayWriteItsRefs(t *testing.T) {
	d := tempDir(t)
	_, git, l := gitLayout(t, d, "fix")
	mustWrite(t, filepath.Join(git, "worktrees/fix/HEAD"), "ref: refs/heads/mascot/fix\n")
	p, err := codexRecipes(d).Prepare(l)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{git + "/objects", git + "/worktrees/fix",
		git + "/refs/heads/pixie", git + "/logs/refs/heads/pixie",
		git + "/refs/heads/mascot", git + "/logs/refs/heads/mascot"}
	if !reflect.DeepEqual(added(p), want) {
		t.Fatal(added(p))
	}
}

func TestACodexWorkerOnAPixieBranchGetsNoLegacyRefDirs(t *testing.T) {
	d := tempDir(t)
	_, git, l := gitLayout(t, d, "fix")
	mustWrite(t, filepath.Join(git, "worktrees/fix/HEAD"), "ref: refs/heads/pixie/fix\n")
	if _, err := codexRecipes(d).Prepare(l); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(git, "refs/heads/mascot")) {
		t.Fatal("no legacy ref dir is made")
	}
}
