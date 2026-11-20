package agentapp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
	"github.com/sihekuang/deskpixie/core/pixhost"
)

func TestYoursSessionIsItsOwn(t *testing.T) {
	if got := YoursSession("deskpixie"); got != "deskpixie-yours" {
		t.Fatal(got)
	}
}

func TestPixHistoryLivesInTheAgentFolder(t *testing.T) {
	if got := NewPaths("/data").PixHistory(); got != "/data/agent/pix-history.json" {
		t.Fatal(got)
	}
}

// writeHistory puts entries in the app's History file before it starts.
func writeHistory(t *testing.T, appSupport string, entries ...pixhost.HistoryEntry) {
	t.Helper()
	path := NewPaths(appSupport).PixHistory()
	b, err := json.Marshal(entries)
	if err == nil {
		err = os.MkdirAll(filepath.Dir(path), 0o700)
	}
	if err == nil {
		err = os.WriteFile(path, b, 0o600)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// herdrAt sets agent.herdrPath: a file that stands in for herdr (nothing runs it here), or
// a path with nothing there.
func herdrAt(c *Config, path string) { c.Settings.HerdrPath = &path }

func fakeHerdr(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func realResumer(t *testing.T, a *Agent) *pixhost.Resumer {
	t.Helper()
	r, ok := a.resumer.(*pixhost.Resumer)
	if !ok {
		t.Fatalf("resumer is %T", a.resumer)
	}
	return r
}

// No herdr: the Resumer has no Tabs (not a nil *herdr.Host in the interface), and resuming
// says Herdr is needed.
func TestResumeWithoutHerdrSaysSo(t *testing.T) {
	folder := t.TempDir()
	f := newFixture(t, func(c *Config) {
		herdrAt(c, "/definitely/not/herdr")
		writeHistory(t, c.AppSupport, pixhost.HistoryEntry{SessionID: "s1", Tool: "claude", Cwd: folder, LastActive: time.Now()})
	})
	r := realResumer(t, f.agent)
	if r.Tabs != nil {
		t.Fatalf("Tabs is %#v", r.Tabs)
	}
	if _, err := f.agent.resumer.Resume(context.Background(), "s1"); !errors.Is(err, pixhost.ErrNoHerdr) {
		t.Fatal(err)
	}
}

// Herdr installed: resumed sessions run in its own session, also when the coordinator runs
// on tmux.
func TestResumeUsesHerdrWhateverTheSessionHost(t *testing.T) {
	for _, host := range []string{"herdr", "tmux"} {
		t.Run(host, func(t *testing.T) {
			bin, session := fakeHerdr(t), ""
			f := newFixture(t, func(c *Config) {
				session = c.Settings.Session
				c.Settings.SessionHost = host
				c.Hosts[host] = c.Hosts["herdr"]
				herdrAt(c, bin)
			})
			r := realResumer(t, f.agent)
			if r.Tabs == nil {
				t.Fatal("no Tabs with herdr installed")
			}
			want := []string{bin, "session", "attach", YoursSession(session)}
			if got := r.Tabs.AttachCommand(); !equalStrings(got, want) {
				t.Fatalf("%q, want %q", got, want)
			}
			if r.Pix == "" || r.Planner == nil {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A pix host that can't listen: no History, and nothing to resume with.
func TestNoPixHostNoResumer(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.PixSocket = "/dev/null/pix.sock" })
	if f.agent.resumer != nil || f.agent.pix != nil {
		t.Fatalf("%v %v", f.agent.resumer, f.agent.pix)
	}
}

// The History file is the app's: written there when a session is recorded.
func TestHistoryIsKeptInTheAppDataFolder(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := os.Stat(f.agent.paths.PixHistory()); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	f.agent.pix.ClearHistory()
	if _, err := os.Stat(f.agent.paths.PixHistory()); err != nil {
		t.Fatal(err)
	}
}

// #461: a session resumed in Herdr is shown by its tab: through the Resumer, which here says
// Herdr is gone, not by the pid's terminal app.
func TestSessionsResumedInHerdrAreShownThroughTheResumer(t *testing.T) {
	f := newFixture(t, func(c *Config) {
		herdrAt(c, "/definitely/not/herdr")
		writeHistory(t, c.AppSupport, pixhost.HistoryEntry{SessionID: "s1", Tool: "claude", HerdrPane: "w1:p2", LastActive: time.Now()})
	})
	c, err := net.Dial("unix", f.agent.pixSock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	wire.Encode(c, wire.Msg{Kind: wire.KindRegister, Tool: "claude", GitRoot: "/x/api", Where: "Herdr", Pid: os.Getpid(), Resumes: "s1"})
	r, _ := wire.NewDecoder(c).Next()
	if r.Kind != wire.KindRegistered {
		t.Fatalf("%+v", r)
	}
	f.agent.FocusSession(r.Address)
	waitFor(t, "the Resumer's answer", func() bool { return f.agent.Tray().Status == "Coordinator: "+string(pixhost.ErrNoHerdr) })
}

// The tray follows History: a change refreshes it.
func TestHistoryChangesRefreshTheTray(t *testing.T) {
	f := newFixture(t, nil)
	before := f.rec.refreshes()
	f.agent.pix.ClearHistory()
	waitFor(t, "a tray refresh", func() bool { return f.rec.refreshes() > before })
}
