package pixhost

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// fakeStore is the real HistoryFile on a temporary folder (cheap, and keeps the fake honest),
// with a way to look up one entry.
type fakeStore struct{ *HistoryFile }

func newFakeStore(t *testing.T) fakeStore {
	return fakeStore{NewHistoryFile(filepath.Join(t.TempDir(), "pix-history.json"))}
}

func (f fakeStore) entry(id string) (HistoryEntry, bool) {
	for _, e := range f.List() {
		if e.SessionID == id {
			return e, true
		}
	}
	return HistoryEntry{}, false
}

// fakeClock is a settable now.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newHistoryHost(t *testing.T) (*Host, string, fakeStore, *fakeClock) {
	t.Helper()
	h, sock := newHost(t)
	store, clock := newFakeStore(t), &fakeClock{t: at(0)}
	h.SetHistory(store, clock.now)
	return h, sock, store, clock
}

func sendEvent(p *fakePix, m wire.Msg) {
	m.Kind = wire.KindEvent
	wire.Encode(p.c, m)
}

func TestHistoryRecordsOnSessionEvent(t *testing.T) {
	_, sock, store, _ := newHistoryHost(t)
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", Cwd: "/x/api/src", GitRoot: "/x/api", Where: "WebStorm", Description: "login bug"})
	sendEvent(p, wire.Msg{Event: "session", SessionID: "s1"})
	eventually(t, "not recorded", func() bool { _, ok := store.entry("s1"); return ok })
	e, _ := store.entry("s1")
	if e.Tool != "claude" || e.Cwd != "/x/api/src" || e.GitRoot != "/x/api" || e.Where != "WebStorm" ||
		e.Description != "login bug" || !e.Started.Equal(at(0)) || !e.LastActive.Equal(at(0)) || e.Ended {
		t.Fatalf("%+v", e)
	}
}

func TestHistoryRecordsDescriptionChange(t *testing.T) {
	h, sock, store, clock := newHistoryHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	sendEvent(p, wire.Msg{Event: "description", Source: "agent", Description: "before the id"}) // no id yet: nothing to record
	sendEvent(p, wire.Msg{Event: "session", SessionID: "s1"})
	eventually(t, "not recorded", func() bool { e, ok := store.entry("s1"); return ok && e.Description == "before the id" })
	clock.add(time.Minute)
	sendEvent(p, wire.Msg{Event: "description", Source: "agent", Description: "uploads"})
	eventually(t, "description not recorded", func() bool { e, _ := store.entry("s1"); return e.Description == "uploads" })
	if e, _ := store.entry("s1"); !e.LastActive.Equal(at(1)) || !e.Started.Equal(at(0)) {
		t.Fatalf("%+v", e)
	}
	if err := h.SetDescription(r.Address, "yours"); err != nil { // your own wins, and is kept too
		t.Fatal(err)
	}
	if e, _ := store.entry("s1"); e.Description != "yours" {
		t.Fatalf("%+v", e)
	}
	if len(store.List()) != 1 {
		t.Fatalf("%+v", store.List())
	}
}

func TestHistoryHidesRunning(t *testing.T) {
	h, sock, store, clock := newHistoryHost(t)
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	sendEvent(p, wire.Msg{Event: "session", SessionID: "s1"})
	eventually(t, "not recorded", func() bool { _, ok := store.entry("s1"); return ok })
	if got := h.History(); len(got) != 0 {
		t.Fatalf("a running session is listed: %+v", got)
	}
	clock.add(2 * time.Minute)
	sendEvent(p, wire.Msg{Event: "exited"})
	eventually(t, "not listed after exit", func() bool { return len(h.History()) == 1 })
	if e := h.History()[0]; e.SessionID != "s1" || !e.Ended || !e.LastActive.Equal(at(2)) {
		t.Fatalf("%+v", e)
	}
}

func TestHistoryHidesAnUnreachableSession(t *testing.T) {
	h, sock, store, _ := newHistoryHost(t)
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	sendEvent(p, wire.Msg{Event: "session", SessionID: "s1"})
	eventually(t, "not recorded", func() bool { _, ok := store.entry("s1"); return ok })
	p.c.Close()
	eventually(t, "drop not noticed", func() bool { l := listed(t, h); return len(l) == 1 && !l[0].User.Reachable })
	if got := h.History(); len(got) != 0 {
		t.Fatalf("a reconnecting session is listed: %+v", got)
	}
}

func TestHistoryHidesAResumeInProgress(t *testing.T) {
	// Not reported yet, but its pix runs it: resuming it again would be a second pix on it.
	h, sock, store, _ := newHistoryHost(t)
	store.Record(HistoryEntry{SessionID: "old", Tool: "claude", LastActive: at(0)})
	dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Resumes: "old"})
	if got := h.History(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestHistoryMarksFailedResume(t *testing.T) {
	h, sock, store, _ := newHistoryHost(t)
	store.Record(HistoryEntry{SessionID: "old", Tool: "claude", LastActive: at(0)})
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Resumes: "old"})
	sendEvent(p, wire.Msg{Event: "exited", ExitCode: 1})
	eventually(t, "not marked unresumable", func() bool { e, _ := store.entry("old"); return e.Unresumable })
	if got := h.History(); len(got) != 1 || !got[0].Unresumable {
		t.Fatalf("%+v", got)
	}
}

func TestHistoryAResumeThatRanAWhileIsNotMarked(t *testing.T) {
	h, sock, store, clock := newHistoryHost(t)
	store.Record(HistoryEntry{SessionID: "old", Tool: "claude", LastActive: at(0)})
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Resumes: "old"})
	clock.add(ResumeFailWithin + time.Second)
	sendEvent(p, wire.Msg{Event: "exited"})
	eventually(t, "not removed", func() bool { return len(listed(t, h)) == 0 })
	if e, _ := store.entry("old"); e.Unresumable {
		t.Fatalf("%+v", e)
	}
}

func TestHistoryADroppedResumeIsNotMarked(t *testing.T) {
	// Only an exit says the resume failed: a pix that lost the app may be fine.
	h, sock, store, _ := newHistoryHost(t)
	h.unreachableFor = 50 * time.Millisecond
	store.Record(HistoryEntry{SessionID: "old", Tool: "claude", LastActive: at(0)})
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Resumes: "old"})
	p.c.Close()
	eventually(t, "not dropped", func() bool { return len(listed(t, h)) == 0 })
	if e, _ := store.entry("old"); e.Unresumable {
		t.Fatalf("%+v", e)
	}
}

func TestHistoryAResumeThatReportedIsNotMarked(t *testing.T) {
	h, sock, store, _ := newHistoryHost(t)
	store.Record(HistoryEntry{SessionID: "old", Tool: "claude", Description: "login bug", LastActive: at(0)})
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Resumes: "old"})
	sendEvent(p, wire.Msg{Event: "session", SessionID: "old"})
	sendEvent(p, wire.Msg{Event: "exited"})
	eventually(t, "not removed", func() bool { return len(listed(t, h)) == 0 })
	e, _ := store.entry("old")
	if e.Unresumable || !e.Ended || e.Description != "login bug" || len(store.List()) != 1 {
		t.Fatalf("%+v", store.List())
	}
}

func TestHistoryForkedResumeReplacesOld(t *testing.T) {
	_, sock, store, _ := newHistoryHost(t)
	store.Record(HistoryEntry{SessionID: "old", Tool: "claude", Description: "login bug", LastActive: at(0)})
	p, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Resumes: "old"})
	sendEvent(p, wire.Msg{Event: "session", SessionID: "new"})
	eventually(t, "not replaced", func() bool {
		_, hasOld := store.entry("old")
		_, hasNew := store.entry("new")
		return hasNew && !hasOld
	})
}

func TestHistoryForkKeepsAnOldOneStillRunning(t *testing.T) {
	_, sock, store, _ := newHistoryHost(t)
	a, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api"})
	sendEvent(a, wire.Msg{Event: "session", SessionID: "old"})
	eventually(t, "not recorded", func() bool { _, ok := store.entry("old"); return ok })
	b, _ := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Resumes: "old"})
	sendEvent(b, wire.Msg{Event: "session", SessionID: "new"})
	eventually(t, "new not recorded", func() bool { _, ok := store.entry("new"); return ok })
	if _, ok := store.entry("old"); !ok {
		t.Fatal("a running conversation was dropped from History")
	}
}

func TestHistoryRemoveAndClearPassThrough(t *testing.T) {
	h, _, store, _ := newHistoryHost(t)
	store.Record(HistoryEntry{SessionID: "a", LastActive: at(1)})
	store.Record(HistoryEntry{SessionID: "b", LastActive: at(2)})
	h.RemoveHistory("a")
	changedSoon(t, h, "remove")
	if got := h.History(); len(got) != 1 || got[0].SessionID != "b" {
		t.Fatalf("%+v", got)
	}
	h.ClearHistory()
	changedSoon(t, h, "clear")
	if got := h.History(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestHistoryLeavesCodexOut(t *testing.T) {
	_, sock, store, _ := newHistoryHost(t)
	p, _ := dialPix(t, sock, wire.Msg{Tool: "codex", GitRoot: "/x/api"})
	sendEvent(p, wire.Msg{Event: "session", SessionID: "c1"})
	sendEvent(p, wire.Msg{Event: "exited"})
	time.Sleep(50 * time.Millisecond)
	if got := store.List(); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestHistoryNilStoreIsSafe(t *testing.T) {
	h, sock := newHost(t)
	p, r := dialPix(t, sock, wire.Msg{Tool: "claude", GitRoot: "/x/api", Resumes: "old"})
	sendEvent(p, wire.Msg{Event: "session", SessionID: "s1"})
	sendEvent(p, wire.Msg{Event: "description", Source: "agent", Description: "x"})
	eventually(t, "description", func() bool { return listed(t, h)[0].User.Description == "x" })
	if err := h.SetDescription(r.Address, "y"); err != nil {
		t.Fatal(err)
	}
	sendEvent(p, wire.Msg{Event: "exited"})
	eventually(t, "not removed", func() bool { return len(listed(t, h)) == 0 })
	h.RemoveHistory("s1")
	h.ClearHistory()
	if got := h.History(); got != nil {
		t.Fatalf("%+v", got)
	}
}
