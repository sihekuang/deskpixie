package logfile

import (
	"archive/zip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
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

// An older file over the limit (an old log from before the cap, or a smaller setting) keeps
// its newest lines, never deleted outright.
func TestAnOversizeLogKeepsItsNewestLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.log")
	var b strings.Builder
	for i := range 200 {
		b.WriteString(strings.Repeat("m", 30) + " line " + strconv.Itoa(i) + "\n")
	}
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
	w := New(path, func() int64 { return 1000 })
	_, _ = w.Write([]byte("new\n"))
	old, _ := os.ReadFile(path + ".1")
	if len(old) == 0 || len(old) > 500 {
		t.Fatal(len(old))
	}
	if !strings.HasSuffix(string(old), " line 199\n") || !strings.HasPrefix(string(old), "mmm") {
		t.Fatalf("not the newest whole lines: %q", old)
	}
	cur, _ := os.ReadFile(path)
	if string(cur) != "new\n" {
		t.Fatalf("%q", cur)
	}
}

// The export is a normal file to share, its entries dated like the logs.
func TestTheExportIsShareable(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "deskpixie.log")
	_ = os.WriteFile(app, []byte("x\n"), 0o600)
	when := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	_ = os.Chtimes(app, when, when)
	dst := filepath.Join(dir, "logs.zip")
	if err := Export(dst, []string{app}, "about"); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o644 {
		t.Fatal(fi.Mode())
	}
	r, _ := zip.OpenReader(dst)
	defer r.Close()
	for _, f := range r.File {
		if f.Name == "deskpixie.log" && !f.Modified.Equal(when) {
			t.Fatal(f.Modified)
		}
	}
}

// One write bigger than half the limit never pushes the history out for an empty file.
func TestAnEmptyFileIsNotRotated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	_ = os.WriteFile(path+".1", []byte("history\n"), 0o600)
	_ = os.WriteFile(path, nil, 0o600)
	w := New(path, func() int64 { return 100 })
	_, _ = w.Write([]byte(strings.Repeat("z", 80) + "\n"))
	if b, _ := os.ReadFile(path + ".1"); string(b) != "history\n" {
		t.Fatalf("%q", b)
	}
}

// A trim that can't be done (a full disk, a read-only folder) isn't tried again on every write.
func TestAFailedTrimIsNotRetriedEachWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	_ = os.WriteFile(path+".1", []byte(strings.Repeat("o\n", 1000)), 0o600)
	_ = os.WriteFile(path, []byte("x\n"), 0o600)
	_ = os.Chmod(dir, 0o500)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	w := New(path, func() int64 { return 100 })
	_, _ = w.Write([]byte("a\n"))
	if w.failedTrim == (trimMark{}) {
		t.Fatal("the failed trim isn't remembered")
	}
	tries := w.trims
	_, _ = w.Write([]byte("b\n"))
	if w.trims != tries {
		t.Fatal("tried again")
	}
}
