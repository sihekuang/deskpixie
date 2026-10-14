package agentfs

import (
	"path/filepath"
	"testing"
)

func TestPrivateFilesAre0600In0700FolderAndReplacedWhole(t *testing.T) {
	d := tempDir(t)
	path := filepath.Join(d, "mcp", "coordinator.json")
	if err := WritePrivate(path, `{"a":1}`); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, path); got != `{"a":1}` {
		t.Fatal(got)
	}
	if m := mode(t, path); m != 0o600 {
		t.Fatalf("file mode %o", m)
	}
	if m := mode(t, filepath.Join(d, "mcp")); m != 0o700 {
		t.Fatalf("dir mode %o", m)
	}
	if err := WritePrivate(path, `{"a":2}`); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, path); got != `{"a":2}` {
		t.Fatal(got)
	}
	if n := count(t, filepath.Join(d, "mcp")); n != 1 {
		t.Fatalf("temp file left: %d entries", n)
	}
	RemoveQuietly(path)
	if exists(path) {
		t.Fatal("not removed")
	}
	RemoveQuietly(path) // already gone: fine
}
