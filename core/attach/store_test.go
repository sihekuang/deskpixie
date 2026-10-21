package attach

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var png = []byte("\x89PNG\r\n\x1a\nfake image bytes")

func newStore(t *testing.T) (*Store, string, *[][]Item) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "attachments")
	s := New(root, time.Now)
	var seen [][]Item
	s.OnChange(func(items []Item) { seen = append(seen, items) })
	return s, root, &seen
}

func TestAddKeepsTheBytesPrivately(t *testing.T) {
	s, root, seen := newStore(t)
	it, err := s.Add("Screenshot 14.32.png", png)
	if err != nil {
		t.Fatal(err)
	}
	if it.Kind != "image" || it.Name != "Screenshot 14.32.png" || it.Size != int64(len(png)) {
		t.Fatalf("%+v", it)
	}
	if b, _ := os.ReadFile(it.Path); !bytes.Equal(b, png) {
		t.Fatal("content differs")
	}
	if !strings.HasPrefix(it.Path, filepath.Join(root, "pending")+string(filepath.Separator)) {
		t.Fatal(it.Path)
	}
	if fi, _ := os.Stat(it.Path); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Dir(it.Path)); fi.Mode().Perm() != 0o700 {
		t.Fatal(fi.Mode())
	}
	if len(*seen) != 1 || len((*seen)[0]) != 1 {
		t.Fatalf("%v", *seen)
	}
	if k, _ := s.Add("notes.pdf", []byte("x")); k.Kind != "file" {
		t.Fatal(k.Kind)
	}
}

func TestTooBigIsRefused(t *testing.T) {
	s, _, _ := newStore(t)
	_, err := s.Add("big.png", make([]byte, MaxBytes+1))
	if err == nil || err.Error() != "That file is over 2 MB." {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Fatal(s.Len())
	}
}

func TestAtMostFive(t *testing.T) {
	s, _, _ := newStore(t)
	for i := 0; i < MaxItems; i++ {
		if _, err := s.Add("a.png", png); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Add("a.png", png); err == nil || err.Error() != "You can attach up to 5 files." {
		t.Fatal(err)
	}
}

func TestSameNameGetsADistinctFile(t *testing.T) {
	s, _, _ := newStore(t)
	a, _ := s.Add("a.png", []byte("one"))
	b, _ := s.Add("a.png", []byte("two"))
	if a.Path == b.Path {
		t.Fatal("same path")
	}
	if x, _ := os.ReadFile(a.Path); string(x) != "one" {
		t.Fatal(string(x))
	}
	if x, _ := os.ReadFile(b.Path); string(x) != "two" {
		t.Fatal(string(x))
	}
}

func TestNamesCannotEscape(t *testing.T) {
	s, root, _ := newStore(t)
	for _, n := range []string{"../../x\n.png", "/etc/passwd", "", "a/b\x00c.txt"} {
		it, err := s.Add(n, []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(it.Path, filepath.Join(root, "pending")+string(filepath.Separator)) {
			t.Fatalf("%q escaped to %s", n, it.Path)
		}
		if strings.ContainsAny(it.Name, "/\n\x00") || it.Name == "" || it.Name == ".." {
			t.Fatalf("%q named %q", n, it.Name)
		}
	}
}

func TestAddFileCopiesAndChecksSize(t *testing.T) {
	s, _, _ := newStore(t)
	src := filepath.Join(t.TempDir(), "report.pdf")
	_ = os.WriteFile(src, []byte("0123456789"), 0o644)
	it, err := s.AddFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if it.Name != "report.pdf" || it.Path == src {
		t.Fatalf("%+v", it)
	}
	if b, _ := os.ReadFile(it.Path); string(b) != "0123456789" {
		t.Fatal(string(b))
	}
	if b, _ := os.ReadFile(src); string(b) != "0123456789" {
		t.Fatal("original changed")
	}
	for _, bad := range []string{filepath.Join(t.TempDir(), "missing"), t.TempDir()} {
		if _, err := s.AddFile(bad); err == nil || err.Error() != "Couldn't read that file." {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	big := filepath.Join(t.TempDir(), "big.pdf")
	_ = os.WriteFile(big, make([]byte, MaxBytes+1), 0o644)
	if _, err := s.AddFile(big); err == nil || err.Error() != "That file is over 2 MB." {
		t.Fatal(err)
	}
}

func TestRemoveDeletesAtOnce(t *testing.T) {
	s, _, seen := newStore(t)
	it, _ := s.Add("a.png", png)
	if !s.Remove(it.ID) {
		t.Fatal("not removed")
	}
	if _, err := os.Stat(it.Path); !os.IsNotExist(err) {
		t.Fatal("file kept")
	}
	if last := (*seen)[len(*seen)-1]; len(last) != 0 {
		t.Fatalf("%v", last)
	}
	if s.Remove("nope") {
		t.Fatal("removed nothing")
	}
}

func TestTakeMovesAndEmpties(t *testing.T) {
	s, root, _ := newStore(t)
	_, _ = s.Add("a.png", png)
	_, _ = s.Add("b.pdf", []byte("pdf"))
	paths := s.Take()
	if len(paths) != 2 {
		t.Fatal(paths)
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) || !strings.HasPrefix(p, filepath.Join(root, "sent")+string(filepath.Separator)) {
			t.Fatal(p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatal(err)
		}
	}
	if s.Len() != 0 || len(s.Pending()) != 0 {
		t.Fatal("not emptied")
	}
	if s.Take() != nil {
		t.Fatal("second take")
	}
}

func TestSweep(t *testing.T) {
	s, root, _ := newStore(t)
	_, _ = s.Add("left.png", png)
	old := filepath.Join(root, "sent", "old")
	recent := filepath.Join(root, "sent", "recent")
	for _, d := range []string{old, recent} {
		_ = os.MkdirAll(d, 0o700)
		_ = os.WriteFile(filepath.Join(d, "x.png"), png, 0o600)
	}
	long := time.Now().Add(-15 * 24 * time.Hour)
	_ = os.Chtimes(old, long, long)
	fresh := New(root, time.Now) // a new start
	fresh.Sweep()
	if _, err := os.Stat(filepath.Join(root, "pending")); !os.IsNotExist(err) {
		t.Fatal("pending kept")
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("old sent kept")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Fatal("recent sent removed")
	}
	_ = s
}

func sentFile(t *testing.T, s *Store, name string) string {
	t.Helper()
	if _, err := s.Add(name, png); err != nil {
		t.Fatal(err)
	}
	return s.Take()[0]
}

func TestForWorkerCopiesAndRewrites(t *testing.T) {
	s, root, _ := newStore(t)
	p := sentFile(t, s, "x.png")
	dir := t.TempDir()
	got := s.ForWorker("Fix it.\nAttached: "+p+"\nthanks", dir)
	local := filepath.Join(dir, ".deskpixie", "attachments", filepath.Base(filepath.Dir(p)), "x.png")
	if got != "Fix it.\nAttached: "+local+"\nthanks" {
		t.Fatal(got)
	}
	if b, _ := os.ReadFile(local); !bytes.Equal(b, png) {
		t.Fatal("not copied")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".deskpixie", ".gitignore")); string(b) != "*\n" {
		t.Fatalf("%q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "copies.jsonl")); !strings.Contains(string(b), local) {
		t.Fatalf("%q", b)
	}
}

func TestForWorkerIsIdempotent(t *testing.T) {
	s, root, _ := newStore(t)
	p := sentFile(t, s, "x.png")
	dir := t.TempDir()
	once := s.ForWorker("Attached: "+p, dir)
	if twice := s.ForWorker(once, dir); twice != once {
		t.Fatal(twice)
	}
	if again := s.ForWorker("Attached: "+p, dir); again != once {
		t.Fatal(again)
	}
	b, _ := os.ReadFile(filepath.Join(root, "copies.jsonl"))
	if n := strings.Count(string(b), "\n"); n != 1 {
		t.Fatalf("%d records: %q", n, b)
	}
}

func TestForWorkerLeavesOtherPaths(t *testing.T) {
	s, _, _ := newStore(t)
	dir := t.TempDir()
	for _, text := range []string{"Attached: /etc/hosts", "no attachments here", ""} {
		if got := s.ForWorker(text, dir); got != text {
			t.Fatal(got)
		}
	}
	p := sentFile(t, s, "x.png")
	if got := s.ForWorker("Attached: "+p, ""); got != "Attached: "+p {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".deskpixie")); !os.IsNotExist(err) {
		t.Fatal("copied for nothing")
	}
}

func TestSweepRemovesOldCopies(t *testing.T) {
	s, root, _ := newStore(t)
	p := sentFile(t, s, "x.png")
	oldDir, newDir := t.TempDir(), t.TempDir()
	s.now = func() time.Time { return time.Now().Add(-15 * 24 * time.Hour) }
	s.ForWorker("Attached: "+p, oldDir)
	s.now = time.Now
	s.ForWorker("Attached: "+p, newDir)
	fresh := New(root, time.Now)
	fresh.Sweep()
	if _, err := os.Stat(filepath.Join(oldDir, ".deskpixie", "attachments", filepath.Base(filepath.Dir(p)), "x.png")); !os.IsNotExist(err) {
		t.Fatal("old copy kept")
	}
	if _, err := os.Stat(filepath.Join(newDir, ".deskpixie", "attachments", filepath.Base(filepath.Dir(p)), "x.png")); err != nil {
		t.Fatal("new copy removed")
	}
	if b, _ := os.ReadFile(filepath.Join(root, "copies.jsonl")); strings.Contains(string(b), oldDir) {
		t.Fatal("old record kept")
	}
}
