package pixhost

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func at(min int) time.Time { return time.Date(2026, 11, 20, 10, min, 0, 0, time.UTC) }

func TestHistoryRecordUpdatesByID(t *testing.T) {
	h := NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))
	h.Record(HistoryEntry{SessionID: "a", Tool: "claude", Description: "first", Cwd: "/r", Started: at(0), LastActive: at(1)})
	h.Record(HistoryEntry{SessionID: "a", Description: "login bug", Started: at(5), LastActive: at(2)})
	got := h.List()
	if len(got) != 1 || got[0].Description != "login bug" || got[0].Cwd != "/r" || got[0].Tool != "claude" ||
		!got[0].Started.Equal(at(0)) || !got[0].LastActive.Equal(at(2)) {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryNewestFirstAndTrimmed(t *testing.T) {
	h := NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))
	for i := range HistoryLimit + 3 {
		h.Record(HistoryEntry{SessionID: string(rune('a' + i)), LastActive: at(i)})
	}
	got := h.List()
	if len(got) != HistoryLimit || got[0].SessionID != string(rune('a'+HistoryLimit+2)) {
		t.Fatalf("len %d first %q", len(got), got[0].SessionID)
	}
	if last := got[len(got)-1].SessionID; last != "d" { // a, b and c, the oldest, went
		t.Fatalf("last %q", last)
	}
}

func TestHistoryPersistsAcrossLoads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pix-history.json")
	NewHistoryFile(p).Record(HistoryEntry{SessionID: "a", Description: "x", LastActive: at(1)})
	if got := NewHistoryFile(p).List(); len(got) != 1 || got[0].Description != "x" {
		t.Fatalf("got %+v", got)
	}
	if _, err := os.Stat(p + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
}

func TestHistoryMissingFileStartsEmpty(t *testing.T) {
	h := NewHistoryFile(filepath.Join(t.TempDir(), "missing", "pix-history.json"))
	if got := h.List(); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	if got := NewHistoryFile(h.path).List(); len(got) != 1 {
		t.Fatalf("missing folder not made: %+v", got)
	}
}

func TestHistoryRemoveAndClear(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pix-history.json")
	h := NewHistoryFile(p)
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	h.Record(HistoryEntry{SessionID: "b", LastActive: at(2)})
	h.Remove("a")
	h.Remove("missing")
	if got := h.List(); len(got) != 1 || got[0].SessionID != "b" {
		t.Fatalf("after remove %+v", got)
	}
	h.Clear()
	if got := h.List(); len(got) != 0 {
		t.Fatalf("after clear %+v", got)
	}
	if got := NewHistoryFile(p).List(); len(got) != 0 {
		t.Fatalf("clear not saved: %+v", got)
	}
}

func TestHistoryDamagedFileIsSetAside(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pix-history.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := NewHistoryFile(p)
	if got := h.List(); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if b, err := os.ReadFile(p + ".bad"); err != nil || string(b) != "{not json" {
		t.Fatalf("damaged file not set aside: %q %v", b, err)
	}
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	if got := NewHistoryFile(p).List(); len(got) != 1 {
		t.Fatalf("not written after a damaged file: %+v", got)
	}
}

func TestHistoryUnreadableFileIsSetAside(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads any file")
	}
	p := filepath.Join(t.TempDir(), "pix-history.json")
	if err := os.WriteFile(p, []byte(`[{"sessionId":"kept"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	h := NewHistoryFile(p)
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	if err := os.Chmod(p+".bad", 0o600); err != nil {
		t.Fatalf("unreadable file not set aside: %v", err)
	}
	if b, err := os.ReadFile(p + ".bad"); err != nil || string(b) != `[{"sessionId":"kept"}]` {
		t.Fatalf("set-aside file lost its bytes: %q %v", b, err)
	}
	if got := NewHistoryFile(p).List(); len(got) != 1 || got[0].SessionID != "a" {
		t.Fatalf("not written after an unreadable file: %+v", got)
	}
}

// A folder where the file should be is set aside like a damaged file, and History is written
// in its place.
func TestHistoryAFolderInTheWayIsSetAside(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pix-history.json")
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	h := NewHistoryFile(p)
	if fi, err := os.Stat(p + ".bad"); err != nil || !fi.IsDir() {
		t.Fatalf("the folder was not set aside: %v", err)
	}
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	if got := NewHistoryFile(p).List(); len(got) != 1 || got[0].SessionID != "a" {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryUnwritableFolderKeepsInMemory(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil { // a file where the folder should be
		t.Fatal(err)
	}
	h := NewHistoryFile(filepath.Join(blocker, "pix-history.json"))
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	h.Update("a", func(e *HistoryEntry) { e.Ended = true })
	if got := h.List(); len(got) != 1 || !got[0].Ended {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryUpdateKeepsOtherFields(t *testing.T) {
	h := NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))
	h.Record(HistoryEntry{SessionID: "a", Description: "x", Cwd: "/r", LastActive: at(1)})
	h.Update("a", func(e *HistoryEntry) { e.HerdrPane = "w1:p3" })
	h.Update("missing", func(e *HistoryEntry) { e.HerdrPane = "nope" })
	got := h.List()
	if len(got) != 1 || got[0].HerdrPane != "w1:p3" || got[0].Description != "x" {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryRecordKeepsEndedAndUnresumable(t *testing.T) {
	// A late description racing an exit must not undo it: only Update changes them.
	h := NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))
	h.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	h.Update("a", func(e *HistoryEntry) { e.Ended, e.Unresumable = true, true })
	h.Record(HistoryEntry{SessionID: "a", Description: "late", LastActive: at(2)})
	if got := h.List(); len(got) != 1 || !got[0].Ended || !got[0].Unresumable || got[0].Description != "late" {
		t.Fatalf("got %+v", got)
	}
}

func TestHistoryListIsACopy(t *testing.T) {
	h := NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))
	h.Record(HistoryEntry{SessionID: "a", Description: "x", LastActive: at(1)})
	h.List()[0].Description = "changed"
	if got := h.List(); got[0].Description != "x" {
		t.Fatalf("got %+v", got)
	}
}
