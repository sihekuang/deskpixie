package agentapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/settings"
)

// projectsEnv is a home folder and DeskPixie's own projects folder (not made yet), as at a
// first start.
func projectsEnv(t *testing.T) (home, fallback string) {
	t.Helper()
	home = tmpDir(t, "home")
	return home, filepath.Join(tmpDir(t, "data"), "agent", "projects")
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// #216: the default folder is created at start when it is missing.
func TestAMissingProjectsFolderIsCreated(t *testing.T) {
	home, fallback := projectsEnv(t)
	got := UseProjectsFolder("~/Documents/deskpixie", home, fallback)
	want := filepath.Join(home, "Documents", "deskpixie")
	if got.Root != want || got.Fallback || got.Why != "" {
		t.Fatalf("%+v", got)
	}
	if !isDir(want) {
		t.Fatal("not created")
	}
	if isDir(fallback) {
		t.Fatal("the fallback is made only when it's needed")
	}
}

// An existing folder is used as it is: nothing is made or changed.
func TestAnExistingProjectsFolderIsUsedAsItIs(t *testing.T) {
	home, fallback := projectsEnv(t)
	code := filepath.Join(home, "code")
	if err := os.MkdirAll(filepath.Join(code, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := UseProjectsFolder("~/code", home, fallback); got.Root != code || got.Fallback {
		t.Fatalf("%+v", got)
	}
	if entries, _ := os.ReadDir(code); len(entries) != 1 {
		t.Fatal("changed")
	}
}

// Every folder that can't be used or created falls back to DeskPixie's own, created.
func TestAnUnusableProjectsFolderFallsBackToDeskPixiesOwn(t *testing.T) {
	home, _ := projectsEnv(t)
	outside := tmpDir(t, "elsewhere")
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(home, "locked")
	if err := os.MkdirAll(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if err := os.Symlink(outside, filepath.Join(home, "away")); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		"",                            // no folder set
		"~",                           // the home folder itself
		"~other/code",                 // ~user
		outside,                       // outside home (exists)
		filepath.Join(outside, "new"), // outside home (missing: never created there)
		"~/notes.txt",                 // a file in the way
		"~/notes.txt/sub",             // a file where a parent should be
		"~/locked/sub",                // no permission to create it
		"~/away",                      // a link that leads outside home
	} {
		t.Run(raw, func(t *testing.T) {
			fallback := filepath.Join(tmpDir(t, "data"), "agent", "projects")
			got := UseProjectsFolder(raw, home, fallback)
			if got.Root != fallback || !got.Fallback || got.Why == "" {
				t.Fatalf("%+v", got)
			}
			if !isDir(fallback) {
				t.Fatal("fallback not created")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Fatal("created a folder outside home")
	}
	if _, err := os.Stat(filepath.Join(locked, "sub")); err == nil {
		t.Fatal("created inside a locked folder")
	}
}

// Neither folder can be had: no projects folder (workers off, with a notice).
func TestNoProjectsFolderAtAllWhenTheFallbackCantBeMade(t *testing.T) {
	home, _ := projectsEnv(t)
	blocker := filepath.Join(tmpDir(t, "data"), "agent")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil { // a file where agent/ goes
		t.Fatal(err)
	}
	got := UseProjectsFolder("", home, filepath.Join(blocker, "projects"))
	if got.Root != "" || got.Fallback || got.Why == "" {
		t.Fatalf("%+v", got)
	}
}

// The window's check reads without changing anything, and says what DeskPixie will do.
func TestTheProjectsFolderCheckChangesNothing(t *testing.T) {
	home, fallback := projectsEnv(t)
	missing := settings.CheckProjectsFolder("~/Documents/deskpixie", home, fallback)
	if missing.State != settings.FolderMissing || missing.Path != filepath.Join(home, "Documents", "deskpixie") ||
		missing.Fallback != fallback || !strings.Contains(missing.Detail, "create it now") {
		t.Fatalf("%+v", missing)
	}
	if isDir(missing.Path) || isDir(fallback) {
		t.Fatal("the check made a folder")
	}
	outside := settings.CheckProjectsFolder(tmpDir(t, "elsewhere"), home, fallback)
	if outside.State != settings.FolderUnusable || !strings.Contains(outside.Detail, "inside your home folder") ||
		!strings.Contains(outside.Detail, "DeskPixie's own folder") {
		t.Fatalf("%+v", outside)
	}
	if empty := settings.CheckProjectsFolder("  ", home, fallback); empty.State != settings.FolderUnusable || empty.Path != "" {
		t.Fatalf("%+v", empty)
	}
	if err := os.MkdirAll(filepath.Join(home, "code"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ready := settings.CheckProjectsFolder("~/code", home, fallback); ready.State != settings.FolderReady || ready.Detail != "Found" {
		t.Fatalf("%+v", ready)
	}
}

// Create (the window's button) makes only a missing folder below home.
func TestCreateMakesOnlyAMissingFolder(t *testing.T) {
	home, fallback := projectsEnv(t)
	got, err := settings.CreateProjectsFolder("~/Documents/deskpixie", home, fallback)
	if err != nil || got.State != settings.FolderReady || !isDir(filepath.Join(home, "Documents", "deskpixie")) {
		t.Fatalf("%+v %v", got, err)
	}
	outside := filepath.Join(tmpDir(t, "elsewhere"), "new")
	if got, err := settings.CreateProjectsFolder(outside, home, fallback); err != nil || got.State != settings.FolderUnusable || isDir(outside) {
		t.Fatalf("%+v %v", got, err)
	}
	locked := filepath.Join(home, "locked")
	if err := os.MkdirAll(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	if _, err := settings.CreateProjectsFolder("~/locked/sub", home, fallback); err == nil {
		t.Fatal("no error for a folder that can't be made")
	}
}

// The window's fallback (settings) is the folder the agent falls back to.
func TestBothSidesNameTheSameFallback(t *testing.T) {
	if got, want := settings.ProjectsFallback("/data"), ProjectsFallback(NewPaths("/data")); got != want {
		t.Fatal(got, want)
	}
}
