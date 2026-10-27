package attach

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

var png = []byte("\x89PNG\r\n\x1a\nfake image bytes")

func newStore(t *testing.T) (*Store, string, *[][]Item) {
	t.Helper()
	tmp := t.TempDir()
	root := filepath.Join(tmp, "workspace", "attachments")
	s := New(root, filepath.Join(tmp, "copies.jsonl"), time.Now)
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

// #367: a Take while a clipboard read is in flight waits for it and hands over what it added;
// ending a read twice doesn't release another one.
func TestTakeWaitsForAClipboardRead(t *testing.T) {
	s, _, _ := newStore(t)
	end := s.BeginRead()
	other := s.BeginRead()
	took := make(chan []string, 1)
	go func() { took <- s.Take() }()
	time.Sleep(50 * time.Millisecond)
	if _, err := s.Add("Screenshot.png", png); err != nil {
		t.Fatal(err)
	}
	end()
	end()
	select {
	case <-took:
		t.Fatal("took with a read still in flight")
	case <-time.After(50 * time.Millisecond):
	}
	other()
	select {
	case paths := <-took:
		if len(paths) != 1 || filepath.Base(paths[0]) != "Screenshot.png" {
			t.Fatal(paths)
		}
	case <-time.After(time.Second):
		t.Fatal("Take never went")
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
	fresh := New(root, s.copiesLog, time.Now) // a new start
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
	s, _, _ := newStore(t)
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
	if b, _ := os.ReadFile(s.copiesLog); !strings.Contains(string(b), local) {
		t.Fatalf("%q", b)
	}
}

func TestForWorkerIsIdempotent(t *testing.T) {
	s, _, _ := newStore(t)
	p := sentFile(t, s, "x.png")
	dir := t.TempDir()
	once := s.ForWorker("Attached: "+p, dir)
	if twice := s.ForWorker(once, dir); twice != once {
		t.Fatal(twice)
	}
	if again := s.ForWorker("Attached: "+p, dir); again != once {
		t.Fatal(again)
	}
	b, _ := os.ReadFile(s.copiesLog)
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
	fresh := New(root, s.copiesLog, time.Now)
	fresh.Sweep()
	if _, err := os.Stat(filepath.Join(oldDir, ".deskpixie", "attachments", filepath.Base(filepath.Dir(p)), "x.png")); !os.IsNotExist(err) {
		t.Fatal("old copy kept")
	}
	if _, err := os.Stat(filepath.Join(newDir, ".deskpixie", "attachments", filepath.Base(filepath.Dir(p)), "x.png")); err != nil {
		t.Fatal("new copy removed")
	}
	if b, _ := os.ReadFile(s.copiesLog); strings.Contains(string(b), oldDir) {
		t.Fatal("old record kept")
	}
}

// The coordinator can write in its workspace: the copies log isn't there, and Sweep deletes only
// what has a worker copy's shape, never through a link.
func TestSweepOnlyDeletesWorkerCopies(t *testing.T) {
	s, root, _ := newStore(t)
	p := sentFile(t, s, "x.png")
	s.ForWorker("Attached: "+p, t.TempDir())
	if _, err := os.Stat(filepath.Join(root, "copies.jsonl")); !os.IsNotExist(err) {
		t.Fatal("the copies log is in the workspace")
	}
	victim := filepath.Join(t.TempDir(), "thesis.docx")
	_ = os.WriteFile(victim, []byte("mine"), 0o600)
	linked := t.TempDir()
	_ = os.MkdirAll(filepath.Join(linked, ".deskpixie"), 0o700)
	_ = os.Symlink(filepath.Dir(victim), filepath.Join(linked, ".deskpixie", "attachments"))
	f, _ := os.OpenFile(s.copiesLog, os.O_APPEND|os.O_WRONLY, 0o600)
	for _, path := range []string{victim, filepath.Join(linked, ".deskpixie", "attachments", "thesis.docx")} {
		_, _ = f.WriteString(`{"path":"` + path + `","at":"2000-01-01T00:00:00Z"}` + "\n")
	}
	_ = f.Close()
	New(root, s.copiesLog, time.Now).Sweep()
	if _, err := os.Stat(victim); err != nil {
		t.Fatal("deleted a file that isn't a worker copy")
	}
}

func TestForWorkerRefusesLinksInTheWorktree(t *testing.T) {
	s, _, _ := newStore(t)
	p := sentFile(t, s, "x.png")
	elsewhere := t.TempDir()
	dir := t.TempDir()
	_ = os.Symlink(elsewhere, filepath.Join(dir, ".deskpixie"))
	if got := s.ForWorker("Attached: "+p, dir); got != "Attached: "+p {
		t.Fatal(got)
	}
	if es, _ := os.ReadDir(elsewhere); len(es) != 0 {
		t.Fatal("wrote through the link")
	}
	victim := filepath.Join(t.TempDir(), ".zshrc")
	_ = os.WriteFile(victim, []byte("export X=1\n"), 0o600)
	dir2 := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir2, ".deskpixie"), 0o700)
	_ = os.Symlink(victim, filepath.Join(dir2, ".deskpixie", ".gitignore"))
	s.ForWorker("Attached: "+p, dir2)
	if b, _ := os.ReadFile(victim); string(b) != "export X=1\n" {
		t.Fatalf("overwrote %q", b)
	}
}

func TestForWorkerRefusesALinkInSent(t *testing.T) {
	s, root, _ := newStore(t)
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	_ = os.WriteFile(secret, []byte("key"), 0o600)
	_ = os.MkdirAll(filepath.Join(root, "sent", "x"), 0o700)
	link := filepath.Join(root, "sent", "x", "y")
	_ = os.Symlink(secret, link)
	if got := s.ForWorker("Attached: "+link, t.TempDir()); got != "Attached: "+link {
		t.Fatal(got)
	}
}

func TestAddFileRefusesAFIFO(t *testing.T) {
	s, _, _ := newStore(t)
	fifo := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan error, 1)
	go func() { _, err := s.AddFile(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil || err.Error() != "Couldn't read that file." {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hung on a FIFO")
	}
}

// Each change is reported after the last: the shell never ends on a set that is gone.
func TestChangesAreReportedInOrder(t *testing.T) {
	for i := 0; i < 50; i++ {
		s, _, _ := newStore(t)
		var mu sync.Mutex
		var last []Item
		s.OnChange(func(items []Item) { mu.Lock(); last = items; mu.Unlock() })
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(2)
			go func() { defer wg.Done(); _, _ = s.Add("a.png", png) }()
			go func() { defer wg.Done(); s.Take() }()
		}
		wg.Wait()
		mu.Lock()
		if len(last) != s.Len() {
			t.Fatalf("reported %d, holds %d", len(last), s.Len())
		}
		mu.Unlock()
	}
}

func TestRestoreBringsThemBack(t *testing.T) {
	s, _, seen := newStore(t)
	_, _ = s.Add("a.png", png)
	paths := s.Take()
	s.Restore(paths)
	items := s.Pending()
	if len(items) != 1 || items[0].Name != "a.png" {
		t.Fatal(items)
	}
	if b, _ := os.ReadFile(items[0].Path); !bytes.Equal(b, png) {
		t.Fatal("content")
	}
	if last := (*seen)[len(*seen)-1]; len(last) != 1 {
		t.Fatal(last)
	}
	s.Restore([]string{"/etc/hosts"}) // not ours: ignored
	if s.Len() != 1 {
		t.Fatal(s.Len())
	}
}
