package agentapp

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/pixhost"
)

// fakeResumer answers Resume with plan or err; with gate set, each call waits for it.
type fakeResumer struct {
	mu    sync.Mutex
	calls []string
	plan  agent.FocusPlan
	err   error
	gate  chan struct{}
}

func (r *fakeResumer) Resume(ctx context.Context, id string) (agent.FocusPlan, error) {
	r.mu.Lock()
	r.calls = append(r.calls, id)
	gate := r.gate
	r.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return r.plan, r.err
}

func (r *fakeResumer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (f *fixture) withResumer(r *fakeResumer) { f.agent.resumer = r }

func (rec *recorder) focusedPlans() []any {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.focused)
}

func (rec *recorder) notifications() [][2]string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.notes)
}

func TestHistoryResumeShowsTheTab(t *testing.T) {
	f := newFixture(t, nil)
	plan := agent.FocusPlan{ActivateApp: "Warp"}
	r := &fakeResumer{plan: plan}
	f.withResumer(r)
	if !f.agent.Menu("agent:history:resume:s1") {
		t.Fatal("not the agent's")
	}
	waitFor(t, "the plan", func() bool { return len(f.rec.focusedPlans()) == 1 })
	if got := f.rec.focusedPlans()[0]; !equalPlans(got, plan) || r.calls[0] != "s1" {
		t.Fatalf("%+v %v", got, r.calls)
	}
}

func equalPlans(got any, want agent.FocusPlan) bool {
	p, ok := got.(agent.FocusPlan)
	return ok && p.ActivateApp == want.ActivateApp
}

func TestHistoryResumeFailureSaysWhy(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{pixhost.ErrFolderGone, "That project folder is gone."},
		{pixhost.ErrNoHerdr, "Resuming needs Herdr. Set it up in Settings ▸ Agents."},
		{errors.New("herdr: socket: connection refused"), ResumeFailed},
	} {
		f := newFixture(t, nil)
		f.withResumer(&fakeResumer{err: c.err})
		f.agent.Menu("agent:history:resume:s1")
		waitFor(t, "a notification", func() bool { return len(f.rec.notifications()) == 1 })
		if got := f.rec.notifications()[0]; got != [2]string{"Couldn't resume", c.want} {
			t.Fatalf("%q", got)
		}
		if len(f.rec.focusedPlans()) != 0 {
			t.Fatal("focused after a failure")
		}
	}
}

// #461: a second click on a session being resumed opens no second tab; once it failed, it
// can be tried again.
func TestHistoryResumeIgnoresASecondClickWhileResuming(t *testing.T) {
	f := newFixture(t, nil)
	r := &fakeResumer{err: pixhost.ErrNoHerdr, gate: make(chan struct{})}
	f.withResumer(r)
	f.agent.Menu("agent:history:resume:s1")
	waitFor(t, "the first resume", func() bool { return r.count() == 1 })
	f.agent.Menu("agent:history:resume:s1")
	f.agent.Menu("agent:history:resume:s2") // another session isn't held up
	waitFor(t, "the other session", func() bool { return r.count() == 2 })
	close(r.gate)
	waitFor(t, "both answered", func() bool { return len(f.rec.notifications()) == 2 })
	if r.calls[0] != "s1" || r.calls[1] != "s2" {
		t.Fatal(r.calls)
	}
	f.agent.Menu("agent:history:resume:s1")
	waitFor(t, "a retry after the failure", func() bool { return r.count() == 3 })
}

// Just resumed, it isn't hidden from History until its pix registers: a click then is ignored
// too, for a while.
func TestHistoryResumeIgnoresAClickJustAfterItOpened(t *testing.T) {
	f := newFixture(t, nil)
	f.agent.resumeGrace = 100 * time.Millisecond
	r := &fakeResumer{}
	f.withResumer(r)
	f.agent.Menu("agent:history:resume:s1")
	waitFor(t, "the plan", func() bool { return len(f.rec.focusedPlans()) == 1 })
	f.agent.Menu("agent:history:resume:s1")
	time.Sleep(20 * time.Millisecond)
	if r.count() != 1 {
		t.Fatal(r.calls)
	}
	waitFor(t, "a click after the grace", func() bool {
		f.agent.Menu("agent:history:resume:s1")
		return r.count() == 2
	})
}

func TestHistoryResumeWithoutAResumerDoesNothing(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.PixSocket = "/dev/null/pix.sock" })
	for _, id := range []string{"agent:history:resume:s1", "agent:history:remove:s1", "agent:history:clear"} {
		if !f.agent.Menu(id) {
			t.Fatal(id)
		}
	}
	if got := f.agent.Tray().History; got != nil {
		t.Fatalf("%+v", got)
	}
}

func TestHistoryMenuIDsThatAreNotOnes(t *testing.T) {
	f := newFixture(t, nil)
	for _, id := range []string{"agent:history:", "agent:history:resume:", "agent:history:remove:", "agent:history:bogus"} {
		if f.agent.Menu(id) {
			t.Fatal(id)
		}
	}
}

// The tray lists History (past sessions only), and Remove and Clear change it.
func TestHistoryTrayRowsAndRemoveAndClear(t *testing.T) {
	now := time.Now()
	f := newFixture(t, func(c *Config) {
		writeHistory(t, c.AppSupport,
			pixhost.HistoryEntry{SessionID: "a", Tool: "claude", Description: "login bug", Cwd: "/x/deskpixie/core", GitRoot: "/x/deskpixie", LastActive: now},
			pixhost.HistoryEntry{SessionID: "b", Tool: "claude", Cwd: "/x/notes", LastActive: now.Add(-time.Hour), Unresumable: true},
			pixhost.HistoryEntry{SessionID: "c", Tool: "claude", Cwd: "/x/web", LastActive: now.Add(-2 * time.Hour)},
			pixhost.HistoryEntry{SessionID: "d", Tool: "claude", LastActive: now.Add(-3 * time.Hour)})
	})
	want := []app.HistoryRow{
		{ID: "a", Description: "login bug", Folder: "deskpixie", LastActive: now},
		{ID: "b", Folder: "notes", LastActive: now.Add(-time.Hour), Unresumable: true},
		{ID: "c", Folder: "web", LastActive: now.Add(-2 * time.Hour)},
		{ID: "d", LastActive: now.Add(-3 * time.Hour)},
	}
	if got := f.agent.Tray().History; !equalRows(got, want) {
		t.Fatalf("%+v", got)
	}
	before := f.rec.refreshes()
	if !f.agent.Menu("agent:history:remove:b") {
		t.Fatal("remove")
	}
	if got := f.agent.Tray().History; !equalRows(got, []app.HistoryRow{want[0], want[2], want[3]}) {
		t.Fatalf("%+v", got)
	}
	waitFor(t, "a tray refresh", func() bool { return f.rec.refreshes() > before })
	if !f.agent.Menu("agent:history:clear") {
		t.Fatal("clear")
	}
	if got := f.agent.Tray().History; len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func equalRows(a, b []app.HistoryRow) bool {
	return slices.EqualFunc(a, b, func(x, y app.HistoryRow) bool {
		return x.ID == y.ID && x.Description == y.Description && x.Folder == y.Folder &&
			x.LastActive.Equal(y.LastActive) && x.Unresumable == y.Unresumable
	})
}
