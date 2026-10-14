package app

// Port of ai-mascot src-tauri/src/canvas/mod.rs tests (1 of 1), plus Go-only checks of the
// canvas composition (presenter events, URLs).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sihekuang/deskpixie/core/canvas"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInstallGuideCopiesTheGuideAndExamplesAndOverwrites(t *testing.T) {
	root := t.TempDir()
	ds := filepath.Join(root, "ds")
	write(t, filepath.Join(ds, "CANVAS.md"), "guide v1")
	write(t, filepath.Join(ds, "examples/a.html"), "<a>")
	write(t, filepath.Join(ds, "examples/notes.txt"), "skip")
	pages := filepath.Join(root, "data/canvas/pages")
	if err := os.MkdirAll(pages, 0o755); err != nil {
		t.Fatal(err)
	}

	InstallGuide(ds, pages)
	cv := filepath.Join(root, "data/canvas")
	if got := read(t, filepath.Join(cv, "CANVAS.md")); got != "guide v1" {
		t.Fatal(got)
	}
	if got := read(t, filepath.Join(cv, "examples/a.html")); got != "<a>" {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(cv, "examples/notes.txt")); !os.IsNotExist(err) {
		t.Fatal("notes.txt copied")
	}

	write(t, filepath.Join(ds, "CANVAS.md"), "guide v2")
	write(t, filepath.Join(cv, "examples/a.html"), "stale")
	InstallGuide(ds, pages)
	if got := read(t, filepath.Join(cv, "CANVAS.md")); got != "guide v2" {
		t.Fatal(got)
	}
	if got := read(t, filepath.Join(cv, "examples/a.html")); got != "<a>" {
		t.Fatal(got)
	}
}

func TestPageURLsAreCanvasSchemeURLs(t *testing.T) {
	id, err := canvas.PageIDFromSlug("q3")
	if err != nil {
		t.Fatal(err)
	}
	if got := PageURL(id); got != "canvas://localhost/pages/q3.html" {
		t.Fatal(got)
	}
	if EmptyPage != "canvas://localhost/ds/empty.html" {
		t.Fatal(EmptyPage)
	}
}
