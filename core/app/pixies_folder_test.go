package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func packIDs(s AppState) []string {
	var ids []string
	for _, p := range s.Packs {
		ids = append(ids, p.ID)
	}
	return ids
}

func hasPack(s AppState, id string) bool {
	for _, p := range s.Packs {
		if p.ID == id {
			return true
		}
	}
	return false
}

// Choosing a pixies folder in Settings reads it at once (#266): its pixies join the bundled
// ones, one that didn't load says why, and Open Pixies Folder opens it.
func TestChoosingAPixiesFolderLoadsItsPixies(t *testing.T) {
	h := newHarness(t)
	art := t.TempDir()
	writePack(t, art, "mycat", 4)
	writePack(t, art, "myowl", 4)
	write(t, filepath.Join(art, "myowl", "pixie.json"), strings.Replace(read(t, filepath.Join(art, "myowl", "pixie.json")), `"fps": 8`, `"fps": 0`, 1))
	if hasPack(h.state(), "mycat") {
		t.Fatal("not before the folder is chosen")
	}
	h.take()
	if s := h.set("pixiesFolder", art); s.SetError != nil && string(s.SetError) != "null" {
		t.Fatalf("set: %s", s.SetError)
	}
	st := h.state()
	if !hasPack(st, "mycat") || !hasPack(st, "bit") || hasPack(st, "myowl") {
		t.Fatalf("packs %v", packIDs(st))
	}
	if len(st.PackProblems) != 1 || st.PackProblems[0] != "myowl didn't load: fps: fps 0 must be between 1 and 60" {
		t.Fatalf("problems %q", st.PackProblems)
	}
	if len(h.kinds(h.take(), "pack")) != 0 {
		t.Fatal("Bit is still shown: it isn't drawn again")
	}
	h.set("pack", "mycat")
	if ev := h.kinds(h.take(), "pack"); len(ev) != 1 || ev[0]["pack"].(map[string]any)["name"] != "Mycat" {
		t.Fatalf("your pixie can be chosen: %v", ev)
	}
	h.action("pixies:folder")
	h.mu.Lock()
	runs := h.runs
	h.mu.Unlock()
	if len(runs) != 1 || runs[0][0] != "open" || runs[0][1] != art {
		t.Fatalf("runs %v", runs)
	}
}

// With no folder chosen the app reads, and opens, <AppSupport>/pixies; clearing a chosen
// folder goes back to it.
func TestNoPixiesFolderUsesTheAppsOwn(t *testing.T) {
	h := newHarness(t)
	own := filepath.Join(h.dir, UserPacksDir)
	writePack(t, own, "mine", 4)
	art := t.TempDir()
	h.set("pixiesFolder", art)
	if hasPack(h.state(), "mine") {
		t.Fatal("a chosen folder replaces the app's own")
	}
	h.set("pixiesFolder", nil)
	if !hasPack(h.state(), "mine") {
		t.Fatalf("packs %v", packIDs(h.state()))
	}
	h.action("pixies:folder")
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.runs) != 1 || h.runs[0][1] != own {
		t.Fatalf("runs %v", h.runs)
	}
}

// Reload Pixies picks up a pixie added to the folder, and a fixed one stops being a problem.
func TestReloadPicksUpNewAndFixedPixies(t *testing.T) {
	h := newHarness(t)
	own := filepath.Join(h.dir, UserPacksDir)
	if err := os.MkdirAll(filepath.Join(own, "draft"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.action("reload")
	if p := h.state().PackProblems; len(p) != 1 || p[0] != "draft didn't load: pixie.json: cannot read: not found" {
		t.Fatalf("problems %q", p)
	}
	if err := os.Remove(filepath.Join(own, "draft")); err != nil {
		t.Fatal(err)
	}
	writePack(t, own, "draft", 4)
	h.action("reload")
	st := h.state()
	if !hasPack(st, "draft") || len(st.PackProblems) != 0 {
		t.Fatalf("packs %v problems %q", packIDs(st), st.PackProblems)
	}
}

// A chosen folder that is later deleted or renamed falls back to the app's own folder rather
// than losing every user pixie.
func TestAMissingPixiesFolderFallsBack(t *testing.T) {
	appSupport := t.TempDir()
	gone := filepath.Join(t.TempDir(), "gone")
	roots, user := PackRoots("/bundled", appSupport, &gone, t.TempDir())
	own := filepath.Join(appSupport, UserPacksDir)
	if user != own || len(roots) != 2 || roots[0] != "/bundled" || roots[1] != own {
		t.Fatal(roots, user)
	}
	if st, err := os.Stat(own); err != nil || !st.IsDir() {
		t.Fatal("the app's own folder is made", err)
	}
}

func TestPackRootsWithAChosenFolder(t *testing.T) {
	home := t.TempDir()
	art := filepath.Join(home, "art")
	if err := os.Mkdir(art, 0o755); err != nil {
		t.Fatal(err)
	}
	tilde := "~/art"
	appSupport := t.TempDir()
	roots, user := PackRoots("", appSupport, &tilde, home)
	if user != art || len(roots) != 1 || roots[0] != art {
		t.Fatal(roots, user)
	}
	if _, err := os.Stat(filepath.Join(appSupport, UserPacksDir)); err == nil {
		t.Fatal("the app's own folder isn't touched when a folder is chosen")
	}
}

// Opening Settings reads the pixies again; Pixie is drawn again only when the pixie shown
// changed, so it doesn't restart its animation each time.
func TestReloadRedrawsOnlyWhenThePixieShownChanged(t *testing.T) {
	root := t.TempDir()
	writePack(t, root, "bit", 4)
	h := newHarness(t, withBundled(root))
	h.take()
	h.action("reload")
	if n := len(h.kinds(h.take(), "pack")); n != 0 {
		t.Fatalf("%d pack events with nothing changed", n)
	}
	sheet := filepath.Join(root, "bit", "sheet.png")
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(sheet, later, later); err != nil {
		t.Fatal(err)
	}
	h.action("reload")
	ev := h.kinds(h.take(), "pack")
	if len(ev) != 1 {
		t.Fatalf("%d pack events after the sheet was redrawn", len(ev))
	}
	// The path is the same, so the shell is told the sheet's new version to read it again.
	if v, ok := ev[0]["sheetVersion"].(float64); !ok || int64(v)/1e9 != later.Unix() {
		t.Fatalf("sheetVersion %v, want %d s", ev[0]["sheetVersion"], later.Unix())
	}
	write(t, filepath.Join(root, "bit", "pixie.json"), strings.Replace(read(t, filepath.Join(root, "bit", "pixie.json")), `"fps": 8`, `"fps": 10`, 1))
	h.action("reload")
	if n := len(h.kinds(h.take(), "pack")); n != 1 {
		t.Fatalf("%d pack events after the manifest changed", n)
	}
}
