package canvas

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDirFsListsRegularFilesOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.html"), []byte("<title>A</title>"))
	if err := os.Mkdir(filepath.Join(dir, "sub.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "a.html"), filepath.Join(dir, "link.html")); err != nil {
		t.Fatal(err)
	}
	fsys := NewDirFS(dir)
	entries, err := fsys.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if !reflect.DeepEqual(names, []string{"a.html"}) {
		t.Fatalf("names = %v", names)
	}
	if b, _ := fsys.Read("a.html"); string(b) != "<title>A</title>" {
		t.Fatalf("read = %q", b)
	}
	if err := fsys.Remove("a.html"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := fsys.List(); len(entries) != 0 {
		t.Fatalf("still listed: %v", entries)
	}
}

func TestReadLimitedRefusesSymlinksAndCapsLength(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), []byte("secret"))
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "link.html")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "big.html"), []byte(strings.Repeat("x", 100)))
	fsys := NewDirFS(dir)
	if _, err := fsys.ReadLimited("link.html", 1000); err == nil {
		t.Error("symlink followed")
	}
	if b, _ := fsys.ReadLimited("big.html", 10); len(b) != 11 {
		t.Errorf("stops at max + 1: got %d", len(b))
	}
	if b, _ := fsys.ReadLimited("big.html", 1000); len(b) != 100 {
		t.Errorf("full read: got %d", len(b))
	}
}

func TestDirFsRefusesPaths(t *testing.T) {
	fsys := NewDirFS(t.TempDir())
	for _, name := range []string{"../x.html", "a/b.html", "..", "", "a\\b"} {
		if _, err := fsys.Read(name); !errors.Is(err, os.ErrInvalid) {
			t.Errorf("Read(%q) err = %v", name, err)
		}
		if err := fsys.Remove(name); !errors.Is(err, os.ErrInvalid) {
			t.Errorf("Remove(%q) err = %v", name, err)
		}
	}
}
