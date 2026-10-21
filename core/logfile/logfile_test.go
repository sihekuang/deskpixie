package logfile

import (
	"archive/zip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func size(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// #225: a log keeps at most Max bytes on disk, the file plus one older one.
func TestAWriterKeepsAtMostMaxOnDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "app.log")
	w := New(path, func() int64 { return 1000 })
	line := strings.Repeat("x", 99) + "\n"
	for range 100 {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	cur, old := size(t, path), size(t, path+".1")
	if cur > 500 || old > 500 || cur+old > 1000 {
		t.Fatal(cur, old)
	}
	if old == 0 {
		t.Fatal("the older file is kept")
	}
}

// The limit is read on every write: a smaller setting applies at once.
func TestTheLimitIsLive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	max := int64(10000)
	w := New(path, func() int64 { return max })
	for range 40 {
		_, _ = w.Write([]byte(strings.Repeat("y", 99) + "\n"))
	}
	max = 400
	_, _ = w.Write([]byte("after\n"))
	if size(t, path) > 200 {
		t.Fatal(size(t, path))
	}
}

// A write that can't land is dropped, never an error the logger would stop on.
func TestAWriteThatCantLandIsDropped(t *testing.T) {
	dir := t.TempDir()
	block := filepath.Join(dir, "file")
	_ = os.WriteFile(block, nil, 0o644)
	w := New(filepath.Join(block, "app.log"), func() int64 { return 1000 })
	if n, err := w.Write([]byte("hi\n")); err != nil || n != 3 {
		t.Fatal(n, err)
	}
}

// Files lists what exists, older first.
func TestFilesListsTheOlderOneFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if got := Files(path); len(got) != 0 {
		t.Fatal(got)
	}
	_ = os.WriteFile(path, []byte("now"), 0o600)
	_ = os.WriteFile(path+".1", []byte("before"), 0o600)
	if got := Files(path); len(got) != 2 || got[0] != path+".1" || got[1] != path {
		t.Fatal(got)
	}
}

// Export writes a zip of the logs that exist and an about.txt.
func TestExportZipsTheLogsAndAbout(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "deskpixie.log")
	_ = os.WriteFile(app, []byte("app line\n"), 0o600)
	_ = os.WriteFile(app+".1", []byte("older\n"), 0o600)
	dst := filepath.Join(dir, "out", "logs.zip")
	if err := Export(dst, []string{app, filepath.Join(dir, "missing.log")}, "DeskPixie 0.10.0\n"); err != nil {
		t.Fatal(err)
	}
	r, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	want := []string{"about.txt", "deskpixie.log", "deskpixie.log.1"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatal(names)
	}
}

// Export refuses a path it can't write, leaving nothing behind.
func TestExportFailsCleanly(t *testing.T) {
	dir := t.TempDir()
	block := filepath.Join(dir, "file")
	_ = os.WriteFile(block, nil, 0o644)
	if err := Export(filepath.Join(block, "logs.zip"), nil, "x"); err == nil {
		t.Fatal("no error")
	}
	if err := Export("", nil, "x"); err == nil {
		t.Fatal("empty path accepted")
	}
}
