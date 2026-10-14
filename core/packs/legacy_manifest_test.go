package packs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A pack made before the pixie rename (#68) has only mascot.json: it still loads.
func TestAPackWithOnlyAPreRenameManifestLoads(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "old")
	copyPack(t, filepath.Join(bundled, "bit"), dir, func(s string) string {
		return strings.Replace(s, `"name": "Bit"`, `"name": "Old"`, 1)
	})
	if err := os.Rename(filepath.Join(dir, Manifest), filepath.Join(dir, LegacyManifest)); err != nil {
		t.Fatal(err)
	}
	ps, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := ps["old"]; !ok || p.Name != "Old" {
		t.Fatalf("%v", ps)
	}
}

// With both, pixie.json is the one read.
func TestPixieJSONWinsOverAPreRenameManifest(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "both")
	copyPack(t, filepath.Join(bundled, "bit"), dir, func(s string) string {
		return strings.Replace(s, `"name": "Bit"`, `"name": "New"`, 1)
	})
	if err := os.WriteFile(filepath.Join(dir, LegacyManifest), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	ps, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if ps["both"].Name != "New" {
		t.Fatalf("%v", ps)
	}
}

// A broken legacy manifest is reported under its own name; no manifest at all under the new one.
func TestManifestErrorsNameTheFileRead(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"broken", "none"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "broken", LegacyManifest), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), "broken: mascot.json:") || !strings.Contains(err.Error(), "none: pixie.json: cannot read") {
		t.Fatal(err)
	}
}
