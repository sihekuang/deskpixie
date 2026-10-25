package chat

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func openAt(t *testing.T, dir string, keep int, now int64) *Store {
	t.Helper()
	s, err := Open(dir, keep, func() int64 { return now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.file.Close() })
	return s
}

func lines(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "thread.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

func TestTheThreadSurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, 7, 10_000)
	a := s.Add(Entry{At: 1_000, Who: Who{Kind: "you"}, Text: "hi"})
	b := s.Add(Entry{At: 2_000, Who: Who{Kind: "pixie"}, Text: "hello", Request: &Request{Item: 3, Kind: "question", Options: []string{"a"}}})
	s.file.Close()

	r := openAt(t, dir, 7, 10_000)
	got := r.Page("", 10)
	if len(got) != 2 || got[0].ID != b.ID || got[1].ID != a.ID || got[0].Request == nil || got[0].Request.Item != 3 {
		t.Fatalf("reopened = %+v", got)
	}
}

func TestAnUpdateIsReplayedOverItsEntry(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, 7, 10_000)
	e := s.Add(Entry{At: 1_000, Who: Who{Kind: "pixie"}, Text: "ok?", Request: &Request{Kind: "permission"}})
	s.Update(e.ID, func(e *Entry) { e.Request.Outcome = "yes" })
	s.file.Close()

	r := openAt(t, dir, 7, 10_000)
	got := r.Page("", 10)
	if len(got) != 1 || got[0].Request.Outcome != "yes" || len(r.Needs()) != 0 {
		t.Fatalf("reopened = %+v", got)
	}
	if n := len(lines(t, dir)); n != 1 {
		t.Fatalf("file has %d lines after compacting, want 1", n)
	}
}

func TestAJoinedLineIsReplayedToo(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, 7, 10_000)
	s.Add(coordinator(1_000, "a", 1))
	s.Add(coordinator(2_000, "b", 2))
	s.file.Close()

	got := openAt(t, dir, 7, 10_000).Page("", 10)
	if len(got) != 1 || got[0].Text != "a\nb" || !slices.Equal(got[0].Lines, []uint64{1, 2}) {
		t.Fatalf("reopened = %+v", got)
	}
}

func TestATornLastLineIsSkipped(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, 7, 10_000)
	s.Add(Entry{At: 1_000, Who: Who{Kind: "you"}, Text: "kept"})
	s.file.Close()
	path := filepath.Join(dir, "thread.jsonl")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(`{"id":"e1","at":2000,"who":{"ki`)
	f.Close()

	r := openAt(t, dir, 7, 10_000)
	if got := r.Page("", 10); len(got) != 1 || got[0].Text != "kept" {
		t.Fatalf("reopened = %+v", got)
	}
	// the next line starts clean, not glued to the torn one
	r.Add(Entry{At: 9_000, Who: Who{Kind: "you"}, Text: "next"})
	r.file.Close()
	if got := openAt(t, dir, 7, 10_000).Page("", 10); len(got) != 2 {
		t.Fatalf("after another write = %+v", got)
	}
}

func TestEntriesOlderThanKeepDaysAreDroppedAndCompacted(t *testing.T) {
	dir := t.TempDir()
	now := int64(20 * dayMs)
	s := openAt(t, dir, 30, now)
	s.Add(Entry{At: now - 8*dayMs, Who: Who{Kind: "you"}, Text: "old"})
	s.Add(Entry{At: now - 6*dayMs, Who: Who{Kind: "pixie"}, Text: "new"})
	e := s.Add(Entry{At: now - 1*dayMs, Who: Who{Kind: "you"}, Text: "newer"})
	s.Update(e.ID, func(e *Entry) { e.Spoken = true })
	s.file.Close()

	r := openAt(t, dir, 7, now)
	if got := r.Page("", 10); len(got) != 2 || got[1].Text != "new" {
		t.Fatalf("reopened = %+v", got)
	}
	if n := len(lines(t, dir)); n != 2 {
		t.Fatalf("file has %d lines, want one per entry (2)", n)
	}
}

func TestSetKeepDaysPrunesNowAndKeepsTheFileInStep(t *testing.T) {
	dir := t.TempDir()
	now := int64(20 * dayMs)
	s := openAt(t, dir, 30, now)
	s.Add(Entry{At: now - 8*dayMs, Who: Who{Kind: "you"}, Text: "old"})
	s.Add(Entry{At: now - dayMs, Who: Who{Kind: "you"}, Text: "new"})
	s.SetKeepDays(7)
	if got := s.Page("", 10); len(got) != 1 || got[0].Text != "new" {
		t.Fatalf("after SetKeepDays = %+v", got)
	}
	s.Add(Entry{At: now, Who: Who{Kind: "pixie"}, Text: "later"}) // appends to the rewritten file
	if n := len(lines(t, dir)); n != 2 {
		t.Fatalf("file has %d lines, want 2", n)
	}
}

func TestClearEmptiesTheFile(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, 7, 10_000)
	s.Add(Entry{ID: "job1", At: 1_000, Who: Who{Kind: "you"}, Text: "a", Lines: []uint64{4}})
	s.Add(Entry{At: 2_000, Who: Who{Kind: "pixie"}, Text: "b"})
	s.MarkRead("job1")
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if len(s.Page("", 10)) != 0 || s.Unread() != 0 {
		t.Fatal("memory not empty")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "thread.jsonl")); len(b) != 0 {
		t.Fatalf("file = %q", b)
	}
	// counter, named and handed are reset
	if e := s.Add(Entry{At: 3_000, Who: Who{Kind: "pixie"}, Text: "c"}); e.ID != "e0" {
		t.Fatalf("first id after Clear = %s", e.ID)
	}
	s.Add(Entry{ID: "job1", At: 3_100, Who: Who{Kind: "session", Name: "w"}, Text: "d", Lines: []uint64{4}})
	if got := s.MarkRead("job1"); !slices.Equal(got, []uint64{4}) {
		t.Fatalf("MarkRead after Clear = %v, want line 4 again", got)
	}
	s.file.Close()
	if got := openAt(t, dir, 7, 10_000).Page("", 10); len(got) != 2 {
		t.Fatalf("reopened after Clear and new adds = %+v", got)
	}
}

func TestClearOnAMemoryStore(t *testing.T) {
	s := NewMemory(func() int64 { return 0 })
	s.Add(Entry{Who: Who{Kind: "you"}, Text: "a"})
	if err := s.Clear(); err != nil || len(s.Page("", 10)) != 0 {
		t.Fatalf("Clear = %v", err)
	}
}

func TestOpenMakesTheFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "chat")
	s := openAt(t, dir, 7, 0)
	s.Add(Entry{Who: Who{Kind: "you"}, Text: "x"})
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("folder = %v, %v", st, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "thread.jsonl"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file = %v, %v", fi, err)
	}
}

func TestOpenWithAFileWhereTheFolderGoesFails(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "chat")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(blocker, 7, func() int64 { return 0 }); err == nil || s != nil {
		t.Fatalf("Open = %v, %v; want an error", s, err)
	}
}

func TestOpenWithAFolderWhereTheFileGoesFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "thread.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(dir, 7, func() int64 { return 0 }); err == nil || s != nil {
		t.Fatalf("Open = %v, %v; want an error", s, err)
	}
}

func TestReopenedNamedEntriesNeverJoin(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, 7, 10_000)
	s.Add(Entry{ID: "job7", At: 1_000, Who: Who{Kind: "coordinator"}, Text: "started"})
	s.file.Close()

	r := openAt(t, dir, 7, 10_000)
	r.Add(coordinator(3_000, "later", 1))
	got := r.Page("", 10)
	if len(got) != 2 || got[1].ID != "job7" || got[1].Text != "started" {
		t.Fatalf("entries = %+v", got)
	}
}

func TestReopenedReadEntriesHandNoLinesOutAgain(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, 7, 10_000)
	a := s.Add(coordinator(1_000, "a", 1))
	if got := s.MarkRead(a.ID); !slices.Equal(got, []uint64{1}) {
		t.Fatalf("first MarkRead = %v", got)
	}
	b := s.Add(Entry{At: 20_000, Who: Who{Kind: "pixie"}, Text: "b", Lines: []uint64{2}})
	s.file.Close()

	r := openAt(t, dir, 7, 30_000)
	if r.Unread() != 1 {
		t.Fatalf("unread = %d, want 1", r.Unread())
	}
	if got := r.MarkRead(b.ID); !slices.Equal(got, []uint64{2}) {
		t.Fatalf("MarkRead after reopen = %v, want only line 2", got)
	}
}

func TestIDsAfterReopeningAreNewOnes(t *testing.T) {
	dir := t.TempDir()
	s := openAt(t, dir, 7, 10_000)
	s.Add(Entry{At: 1_000, Who: Who{Kind: "you"}, Text: "a"})
	s.Add(Entry{ID: "job1", At: 2_000, Who: Who{Kind: "you"}, Text: "b"})
	last := s.Add(Entry{At: 3_000, Who: Who{Kind: "pixie"}, Text: "c"})
	s.file.Close()

	r := openAt(t, dir, 7, 10_000)
	e := r.Add(Entry{At: 4_000, Who: Who{Kind: "you"}, Text: "d"})
	if last.ID != "e1" || e.ID != "e2" {
		t.Fatalf("ids = %s then %s, want e1 then e2", last.ID, e.ID)
	}
	if len(r.Page("", 10)) != 4 {
		t.Fatal("a new id collided with a loaded one")
	}
}
