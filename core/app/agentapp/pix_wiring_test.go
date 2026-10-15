package agentapp

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/pix/wire"
)

// shortSocket is a pix socket path of the test's own, short enough for a Unix socket (and
// never the real app's).
func shortSocket(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, "pix.sock")
}

func TestPixSessionsRegisterWithTheApp(t *testing.T) {
	f := newFixture(t, nil)
	if _, err := f.host.StartAgent(context.Background(), agent.AgentSpec{Name: "focus-claude"}); err != nil {
		t.Fatal(err)
	}
	c, err := net.Dial("unix", f.agent.pixSock)
	if err != nil {
		t.Fatalf("the app is not listening for pix: %v", err)
	}
	defer c.Close()
	wire.Encode(c, wire.Msg{Kind: wire.KindRegister, Tool: "claude", GitRoot: "/x/focus"})
	r, _ := wire.NewDecoder(c).Next()
	if r.Kind != wire.KindRegistered || r.Address != "focus-claude-2" {
		t.Fatalf("a pix session's name must not clash with Pixie's agents: %+v", r)
	}
	comp, ok := f.agent.session.(*agent.CompositeHost)
	if !ok {
		t.Fatalf("session host is %T, want the CompositeHost", f.agent.session)
	}
	list, _ := comp.List(context.Background())
	if len(list) != 2 {
		t.Fatalf("%+v", list)
	}
}

func TestQuittingTheAppStopsListeningButNotPixSessions(t *testing.T) {
	f := newFixture(t, nil)
	sock := f.agent.pixSock
	f.agent.Shutdown()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatal("the pix socket outlived the app")
	}
	if f.host.shut.Load() != 1 {
		t.Fatal("the primary host must still be shut down")
	}
}

// activeListSpy is a primary that counts its active List (which may start a server) apart
// from its passive ListRunning.
type activeListSpy struct {
	*agenttest.FakeSessionHost
	active, passive atomic.Int32
}

func (s *activeListSpy) List(ctx context.Context) ([]agent.AgentInfo, error) {
	s.active.Add(1)
	return s.FakeSessionHost.List(ctx)
}

func (s *activeListSpy) ListRunning(ctx context.Context) ([]agent.AgentInfo, error) {
	s.passive.Add(1)
	return s.FakeSessionHost.List(ctx)
}

// A pix hello checks its name against the primary's agents passively: with Herdr stopped, a
// pix session connecting must not start it.
func TestPixNameCheckListsThePrimaryPassively(t *testing.T) {
	spy := &activeListSpy{FakeSessionHost: agenttest.NewFakeSessionHost()}
	f := newFixture(t, func(c *Config) {
		c.Hosts["herdr"] = func(d HostDeps) (agent.SessionHost, error) {
			return NewIntegrationCheck(spy, func() bool { return false }, d.Hint), nil
		}
	})
	c, err := net.Dial("unix", f.agent.pixSock)
	if err != nil {
		t.Fatalf("the app is not listening for pix: %v", err)
	}
	defer c.Close()
	wire.Encode(c, wire.Msg{Kind: wire.KindRegister, Tool: "claude", GitRoot: "/x/focus"})
	if r, _ := wire.NewDecoder(c).Next(); r.Kind != wire.KindRegistered {
		t.Fatalf("%+v", r)
	}
	if n := spy.active.Load(); n != 0 {
		t.Fatalf("pix's name check called the primary's active List %d times", n)
	}
	if spy.passive.Load() == 0 {
		t.Fatal("pix's name check never listed the primary")
	}
}
