package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func mkPackDir(t *testing.T, root, id string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, id), 0o755); err != nil {
		t.Fatal(err)
	}
}

func isDirAt(p string) bool {
	m, err := os.Stat(p)
	return err == nil && m.IsDir()
}

// #68: the user's mascots/ becomes pixies/ once; a second run changes nothing.
func TestTheUsersMascotsFolderIsMovedToPixiesOnce(t *testing.T) {
	dir := t.TempDir()
	mkPackDir(t, filepath.Join(dir, "mascots"), "mine")
	roots := UserPackRoots(dir)
	if !slices.Equal(roots, []string{filepath.Join(dir, "pixies")}) {
		t.Fatal(roots)
	}
	if isDirAt(filepath.Join(dir, "mascots")) || !isDirAt(filepath.Join(dir, "pixies/mine")) {
		t.Fatal("moved")
	}
	// Idempotent: a later run leaves pixies/ as it is.
	if again := UserPackRoots(dir); !slices.Equal(again, roots) || !isDirAt(filepath.Join(dir, "pixies/mine")) {
		t.Fatal(again)
	}
}

// With both folders there, neither is moved and both are read, pixies/ last (it wins).
func TestWithBothFoldersBothAreRead(t *testing.T) {
	dir := t.TempDir()
	mkPackDir(t, filepath.Join(dir, "mascots"), "old")
	mkPackDir(t, filepath.Join(dir, "pixies"), "new")
	roots := UserPackRoots(dir)
	if !slices.Equal(roots, []string{filepath.Join(dir, "mascots"), filepath.Join(dir, "pixies")}) {
		t.Fatal(roots)
	}
	if !isDirAt(filepath.Join(dir, "mascots/old")) || !isDirAt(filepath.Join(dir, "pixies/new")) || isDirAt(filepath.Join(dir, "pixies/old")) {
		t.Fatal("nothing moved")
	}
}

// With neither, pixies/ is made.
func TestAFreshInstallGetsAPixiesFolder(t *testing.T) {
	dir := t.TempDir()
	if roots := UserPackRoots(dir); !slices.Equal(roots, []string{filepath.Join(dir, "pixies")}) || !isDirAt(roots[0]) {
		t.Fatal(roots)
	}
	if isDirAt(filepath.Join(dir, "mascots")) {
		t.Fatal("no mascots folder")
	}
}

// End to end: a pack the user put in mascots/ before the rename is still offered.
func TestAPackInTheOldUserFolderIsLoaded(t *testing.T) {
	h := newHarness(t, func(d *Deps, h *harness) {
		src := filepath.Join(h.bundled, "bit")
		dst := filepath.Join(h.dir, "mascots", "olduser")
		mkPackDir(t, filepath.Join(h.dir, "mascots"), "olduser")
		for _, f := range []string{"pixie.json", "sheet.png"} {
			b, err := os.ReadFile(filepath.Join(src, f))
			if err != nil {
				t.Fatal(err)
			}
			name := f
			if f == "pixie.json" {
				name = "mascot.json" // an old pack, in the old folder
			}
			if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	})
	if _, ok := h.app.packs["olduser"]; !ok {
		t.Fatal("old user pack not loaded")
	}
	if !isDirAt(filepath.Join(h.dir, "pixies", "olduser")) {
		t.Fatal("moved into pixies/")
	}
}
