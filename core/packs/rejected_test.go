package packs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A pack that didn't load says which folder and why, so the app can show it (#266).
func TestRejectionsNameTheFolderAndTheReason(t *testing.T) {
	user := t.TempDir()
	copyPack(t, filepath.Join(bundled, "dino"), filepath.Join(user, "my-cat"), nil)
	copyPack(t, filepath.Join(bundled, "dino"), filepath.Join(user, "my-owl"), func(s string) string {
		return strings.Replace(s, `"row": 6, "anchor"`, `"row": 9, "anchor"`, 1)
	})
	if err := os.Mkdir(filepath.Join(user, "sketches"), 0o755); err != nil {
		t.Fatal(err)
	}
	ps, err := Load(user)
	if _, ok := ps["my-cat"]; !ok || len(ps) != 1 {
		t.Fatalf("loaded %v", ps)
	}
	rs := Rejections(err)
	if len(rs) != 2 {
		t.Fatalf("rejections %v from %v", rs, err)
	}
	if rs[0].ID != "my-owl" || rs[0].Dir != filepath.Join(user, "my-owl") || rs[0].Err.Error() != "mouth.row: row 9 exceeds sheet rows (7)" {
		t.Errorf("%+v", rs[0])
	}
	if rs[1].ID != "sketches" || rs[1].Err.Error() != "pixie.json: cannot read: not found" {
		t.Errorf("%+v", rs[1])
	}
	if Rejections(nil) != nil {
		t.Error("no error, no rejections")
	}
}

// A folder of art kept in git has .git and the like: they aren't pixies.
func TestHiddenFoldersAreSkipped(t *testing.T) {
	user := t.TempDir()
	copyPack(t, filepath.Join(bundled, "dino"), filepath.Join(user, "my-cat"), nil)
	if err := os.MkdirAll(filepath.Join(user, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	ps, err := Load(user)
	if err != nil || len(ps) != 1 {
		t.Fatal(ps, err)
	}
}

// The reasons name the sheet as the manifest does, not the full path on disk.
func TestSheetErrorsNameTheSheet(t *testing.T) {
	user := t.TempDir()
	copyPack(t, filepath.Join(bundled, "dino"), filepath.Join(user, "gone"), nil)
	if err := os.Remove(filepath.Join(user, "gone", "sheet.png")); err != nil {
		t.Fatal(err)
	}
	copyPack(t, filepath.Join(bundled, "dino"), filepath.Join(user, "text"), nil)
	if err := os.WriteFile(filepath.Join(user, "text", "sheet.png"), []byte("not a png"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(user)
	rs := Rejections(err)
	if len(rs) != 2 || rs[0].Err.Error() != "sheet: sheet.png not found" || !strings.HasPrefix(rs[1].Err.Error(), "sheet: sheet.png is not a PNG") {
		t.Fatal(rs)
	}
}
