package pixhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// fakeTabs is a Herdr session that records what it was asked to run and focus.
type fakeTabs struct {
	mu       sync.Mutex
	runs     []tabRun
	focused  []string
	runErr   error
	focusErr error
}

type tabRun struct {
	label, cwd string
	env        []agent.EnvVar
	line       string
}

func (f *fakeTabs) RunInTab(_ context.Context, label, cwd string, env []agent.EnvVar, line string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runErr != nil {
		return "", f.runErr
	}
	f.runs = append(f.runs, tabRun{label, cwd, env, line})
	return "w1:p7", nil
}

func (f *fakeTabs) FocusPane(_ context.Context, pane string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.focused = append(f.focused, pane)
	return f.focusErr
}

func (f *fakeTabs) AttachCommand() []string {
	return []string{"/bin/herdr", "session", "attach", "deskpixie-yours"}
}

var bg = context.Background()

type resumeFixture struct {
	r       *Resumer
	store   fakeStore
	tabs    *fakeTabs
	planner *agenttest.FakePlanner
}

func newResumer(t *testing.T, entries ...HistoryEntry) resumeFixture {
	t.Helper()
	store := newFakeStore(t)
	for _, e := range entries {
		store.Record(e)
	}
	tabs, planner := &fakeTabs{}, &agenttest.FakePlanner{}
	r := &Resumer{History: store, Tabs: tabs, Planner: planner, Pix: "/Apps/Desk Pixie.app/Contents/MacOS/pix",
		IsDir: func(p string) bool { return p == "/x/api" }}
	return resumeFixture{r, store, tabs, planner}
}

var loginBug = HistoryEntry{SessionID: "s1", Tool: "claude", Description: "login bug", Cwd: "/x/api", Started: at(0), LastActive: at(0)}

func TestResumeRunsPixInANewTabAndPlansShowingIt(t *testing.T) {
	f := newResumer(t, loginBug)
	plan, err := f.r.Resume(bg, "s1")
	if err != nil {
		t.Fatal(err)
	}
	want := tabRun{label: "login bug", cwd: "/x/api", env: []agent.EnvVar{{Key: "PIX_WHERE", Value: "Herdr"}},
		line: "'/Apps/Desk Pixie.app/Contents/MacOS/pix' claude --resume s1"}
	if len(f.tabs.runs) != 1 || !reflect.DeepEqual(f.tabs.runs[0], want) {
		t.Fatalf("%+v", f.tabs.runs)
	}
	// Only PIX_WHERE: no worker token, so pix wraps Claude as a session of yours.
	for _, e := range f.tabs.runs[0].env {
		if e.Key == "PIXIE_AGENT_TOKEN" {
			t.Fatal("the worker token leaked into the tab")
		}
	}
	if e, _ := f.store.entry("s1"); e.HerdrPane != "w1:p7" {
		t.Fatalf("%+v", e)
	}
	if !reflect.DeepEqual(f.tabs.focused, []string{"w1:p7"}) {
		t.Fatal(f.tabs.focused)
	}
	if got := f.planner.Opened(); len(got) != 1 || !reflect.DeepEqual(got[0], f.tabs.AttachCommand()) {
		t.Fatal(got)
	}
	if plan.NewTerminal == nil || plan.NewTerminal.URL != "fake://opened" {
		t.Fatalf("%+v", plan)
	}
}

// With no description the tab is named after the project folder.
func TestResumeLabelsAnUndescribedSessionByItsFolder(t *testing.T) {
	e := loginBug
	e.Description = ""
	f := newResumer(t, e)
	if _, err := f.r.Resume(bg, "s1"); err != nil {
		t.Fatal(err)
	}
	if f.tabs.runs[0].label != "api" {
		t.Fatal(f.tabs.runs[0].label)
	}
}

func TestResumeRefuses(t *testing.T) {
	unresumable := loginBug
	unresumable.Unresumable = true
	gone := loginBug
	gone.Cwd = "/x/gone"
	// The id is typed into a terminal: control characters would act before the shell's quoting.
	withID := func(id string) HistoryEntry { e := loginBug; e.SessionID = id; return e }
	cases := []struct {
		name   string
		entry  HistoryEntry
		noTabs bool
		want   error
	}{
		{"not in history", withID("other"), false, ErrNotInHistory},
		{"unresumable", unresumable, false, ErrUnresumable},
		{"no herdr", loginBug, true, ErrNoHerdr},
		{"folder gone", gone, false, ErrFolderGone},
	}
	for _, id := range []string{"s1'; rm -rf ~", "s1\x03", "s1\nrm x", "s1\r", "s1\x1b[A", "s1\x15", "", strings.Repeat("a", 129)} {
		cases = append(cases, struct {
			name   string
			entry  HistoryEntry
			noTabs bool
			want   error
		}{fmt.Sprintf("id %q", id), withID(id), false, ErrUnresumable})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newResumer(t, c.entry)
			if c.noTabs {
				f.r.Tabs = nil
			}
			id := c.entry.SessionID
			if c.want == ErrNotInHistory {
				id = "s1"
			}
			if _, err := f.r.Resume(bg, id); !errors.Is(err, c.want) {
				t.Fatalf("%v, want %v", err, c.want)
			}
			if len(f.tabs.runs) != 0 || len(f.planner.Opened()) != 0 {
				t.Fatal("nothing should run")
			}
		})
	}
}

// Herdr not installed is the same plain "needs Herdr"; any other failure comes back as is,
// and History is left alone.
func TestResumeWhenTheTabCannotBeOpened(t *testing.T) {
	f := newResumer(t, loginBug)
	f.tabs.runErr = agent.HostError{Kind: agent.HostNotFound}
	if _, err := f.r.Resume(bg, "s1"); !errors.Is(err, ErrNoHerdr) {
		t.Fatal(err)
	}
	other := agent.HostError{Kind: agent.HostOther, Detail: "input_failed: x"}
	f.tabs.runErr = other
	if _, err := f.r.Resume(bg, "s1"); !errors.Is(err, other) {
		t.Fatal(err)
	}
	if e, _ := f.store.entry("s1"); e.HerdrPane != "" {
		t.Fatalf("%+v", e)
	}
	if len(f.planner.Opened()) != 0 {
		t.Fatal("nothing to show")
	}
}

// Show still attaches when the pane can't be focused (the session shows, maybe another tab).
func TestShowAttachesEvenWhenFocusFails(t *testing.T) {
	f := newResumer(t)
	f.tabs.focusErr = agent.ErrNoAgent("w1:p7")
	if _, err := f.r.Show(bg, "w1:p7"); err != nil {
		t.Fatal(err)
	}
	if len(f.planner.Opened()) != 1 {
		t.Fatal(f.planner.Opened())
	}
}

// A planner that fails (no terminal set up) fails Show; without Herdr there is nothing to show.
func TestShowFailures(t *testing.T) {
	f := newResumer(t)
	f.planner.SetFail("no terminal")
	if _, err := f.r.Show(bg, "w1:p7"); err == nil || err.Error() != "no terminal" {
		t.Fatal(err)
	}
	f.r.Tabs = nil
	if _, err := f.r.Show(bg, "w1:p7"); !errors.Is(err, ErrNoHerdr) {
		t.Fatal(err)
	}
}

// Without IsDir the real file system decides: a folder resumes; a file in its place, or
// nothing, is a folder gone.
func TestResumeChecksTheRealFolder(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]error{dir: nil, file: ErrFolderGone, filepath.Join(dir, "missing"): ErrFolderGone} {
		e := loginBug
		e.Cwd = path
		f := newResumer(t, e)
		f.r.IsDir = nil
		if _, err := f.r.Resume(bg, "s1"); !errors.Is(err, want) {
			t.Fatalf("%s: %v, want %v", path, err, want)
		}
	}
}
