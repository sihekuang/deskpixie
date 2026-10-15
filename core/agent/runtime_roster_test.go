package agent_test

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// rosterRig is a runtime on a composite host: a fake primary and a fake pix host whose
// sessions list as yours. warm starts the coordinator at once.
type rosterRig struct {
	h        *AgentHandle
	host     *agenttest.FakeSessionHost
	user     *pixFake
	roster   *agenttest.RecordingRoster
	launcher *agenttest.FakeLauncher
}

type pixFake struct {
	*agenttest.FakeSessionHost
	ch  chan struct{}
	ctx context.Context
}

// endingSubscribe is the fake's event stream that closes when ctx ends: the fake never closes
// its own, and the composite's merge goroutine would outlive the synctest bubble.
func endingSubscribe(ctx context.Context, in <-chan PaneEvent) <-chan PaneEvent {
	out := make(chan PaneEvent, 64)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-in:
				out <- ev
			}
		}
	}()
	return out
}

func (p *pixFake) Subscribe() <-chan PaneEvent {
	return endingSubscribe(p.ctx, p.FakeSessionHost.Subscribe())
}

type primaryFake struct {
	*agenttest.FakeSessionHost
	ctx context.Context
}

func (p *primaryFake) Subscribe() <-chan PaneEvent {
	return endingSubscribe(p.ctx, p.FakeSessionHost.Subscribe())
}

func (p *pixFake) Owns(n string) bool       { _, ok := p.StatusOf(n); return ok }
func (p *pixFake) Changes() <-chan struct{} { return p.ch }
func (p *pixFake) List(ctx context.Context) ([]AgentInfo, error) {
	l, err := p.FakeSessionHost.List(ctx)
	for i := range l {
		l[i].Origin = OriginUser
		l[i].User = &UserSession{Tool: "claude", Repo: "api", Where: "iTerm", App: "com.googlecode.iterm2", Reachable: true}
	}
	return l, err
}

// connect adds one of your sessions without a pane event, then signals a change.
func (p *pixFake) connect(t *testing.T, name string) {
	if _, err := p.StartAgent(context.Background(), AgentSpec{Name: name}); err != nil {
		t.Fatal(err)
	}
	select {
	case p.ch <- struct{}{}:
	default:
	}
}

// passivePrimary is a primary that lists passively and counts the calls that would start it
// (a non-passive List, EnsureSession).
type passivePrimary struct {
	*primaryFake
	starting atomic.Int32
}

func (p *passivePrimary) List(ctx context.Context) ([]AgentInfo, error) {
	p.starting.Add(1)
	return p.primaryFake.List(ctx)
}

func (p *passivePrimary) EnsureSession(ctx context.Context) error {
	p.starting.Add(1)
	return p.primaryFake.EnsureSession(ctx)
}

func (p *passivePrimary) ListRunning(ctx context.Context) ([]AgentInfo, error) {
	return p.FakeSessionHost.List(ctx)
}

func newRosterRig(t *testing.T, warm bool) *rosterRig {
	return newRosterRigWith(t, warm, func(p *primaryFake) SessionHost { return p })
}

// newRosterRigWith is the rig with the primary wrapped by wrap.
func newRosterRigWith(t *testing.T, warm bool, wrap func(*primaryFake) SessionHost) *rosterRig {
	host := agenttest.NewFakeSessionHost().ManualTurns()
	user := &pixFake{agenttest.NewFakeSessionHost(), make(chan struct{}, 1), t.Context()}
	r := &rosterRig{host: host, user: user, roster: &agenttest.RecordingRoster{}, launcher: &agenttest.FakeLauncher{}}
	clock := agenttest.ClockAt(1_000_000)
	deps := Deps{
		Host:  NewAgentHost(NewCompositeHost(wrap(&primaryFake{host, t.Context()}), user), clock, CoordinatorRecipe{Cwd: "/data/agent/workspace"}),
		Voice: &agenttest.RecordingVoiceLink{}, Notifier: &agenttest.RecordingNotifier{}, Pixie: &agenttest.RecordingNotifier{},
		Status: &agenttest.RecordingStatus{}, Launcher: r.launcher, TerminalDir: "/home/me/Projects",
		Store: &agenttest.MemStore{}, Clock: clock, IDs: &agenttest.SeqIds{}, Updates: &agenttest.RecordingUpdates{},
		Roster: r.roster,
	}
	r.h = NewRuntime(deps, RuntimeConfig{Settings: DefaultSettings(), Warm: warm})
	go r.h.Run(t.Context())
	return r
}

func lastRoster(t *testing.T, r interface{ Views() []RosterView }) RosterView {
	t.Helper()
	v := r.Views()
	if len(v) == 0 {
		t.Fatal("no roster sent")
	}
	return v[len(v)-1]
}

func TestTheRosterListsTheCoordinatorWithoutCountingIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		v := lastRoster(t, r.roster)
		if v.AgentCount != 0 || len(v.Agents) != 1 || v.Agents[0].Kind != "coordinator" {
			t.Fatalf("%+v", v)
		}
		if got := r.h.Roster(); got.AgentCount != 0 || len(got.Agents) != 1 {
			t.Fatalf("Roster is the last view: %+v", got)
		}
	})
}

func TestASessionConnectingWithoutAPaneEventUpdatesTheRoster(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		r.user.connect(t, "api-claude")
		time.Sleep(time.Second)
		v := lastRoster(t, r.roster)
		if v.YourCount != 1 || len(v.Yours) != 1 || v.Yours[0].App != "com.googlecode.iterm2" {
			t.Fatalf("%+v", v)
		}
	})
}

func TestAStatusChangeResendsButAnUnchangedListDoesNot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		n := len(r.roster.Views())
		r.host.SetStatus(Coordinator, PaneWorking)
		time.Sleep(time.Second)
		if v := r.roster.Views(); len(v) != n+1 || v[n].Agents[0].Status != "working" {
			t.Fatalf("%+v", v)
		}
		time.Sleep(RosterBackstop + time.Second)
		if len(r.roster.Views()) != n+1 {
			t.Fatal("the backstop rebuild of an unchanged list sends nothing")
		}
	})
}

func TestAFailingListKeepsTheLastRoster(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		n := len(r.roster.Views())
		r.host.FailList(true)
		r.host.SetStatus(Coordinator, PaneWorking)
		time.Sleep(time.Second)
		if len(r.roster.Views()) != n || r.h.Roster().Agents[0].Status != "idle" {
			t.Fatalf("%+v", r.roster.Views())
		}
	})
}

func TestFocusAgentFocusesThatPaneAndAttaches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		before := len(r.host.Focused())
		if err := r.h.FocusAgent(context.Background(), Coordinator); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if got := r.host.Focused(); len(got) != before+1 || got[len(got)-1] != Coordinator {
			t.Fatalf("the clicked pane is focused: %v", got)
		}
		want := []Attach{{Argv: []string{"fake-herdr", "session", "attach", "test"}, Dir: "/home/me/Projects"}}
		if !reflect.DeepEqual(r.launcher.Attaches(), want) {
			t.Fatal(r.launcher.Attaches())
		}
	})
}

func TestAStartedCoordinatorIsListedWithoutAPaneEvent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, false)
		time.Sleep(time.Second)
		if n := len(lastRoster(t, r.roster).Agents); n != 0 {
			t.Fatalf("not started yet: %d", n)
		}
		if err := r.h.OpenTerminal(context.Background()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if v := lastRoster(t, r.roster); len(v.Agents) != 1 || v.Agents[0].Kind != "coordinator" {
			t.Fatalf("%+v", v)
		}
	})
}

func TestAWorkerStartingAndStoppingShowsInTheRoster(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := withJob(t, true)
		time.Sleep(time.Second)
		okValue(t, call(r, Coordinator, startCall("focus")))
		time.Sleep(RosterDebounce + 50*time.Millisecond)
		v := lastRoster(t, r.roster)
		if v.AgentCount != 1 {
			t.Fatalf("%+v", v)
		}
		var w *RosterRow
		for i := range v.Agents {
			if v.Agents[i].Name == wName {
				w = &v.Agents[i]
			}
		}
		if w == nil || w.Kind != "worker" || w.Detail != "focus" {
			t.Fatalf("%+v", v)
		}
		okValue(t, call(r, Coordinator, McpCall{Kind: CallStopWorker, Name: wName}))
		time.Sleep(WorkerStopGrace + time.Second)
		if v := lastRoster(t, r.roster); v.AgentCount != 0 {
			t.Fatalf("the stopped worker is gone: %+v", v)
		}
	})
}

func TestTheRosterNeverStartsThePrimary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var p *passivePrimary
		r := newRosterRigWith(t, false, func(f *primaryFake) SessionHost {
			p = &passivePrimary{primaryFake: f}
			return p
		})
		time.Sleep(time.Second)
		r.user.connect(t, "api-claude")
		time.Sleep(RosterBackstop + time.Second)
		if v := lastRoster(t, r.roster); v.YourCount != 1 {
			t.Fatalf("%+v", v)
		}
		if n := p.starting.Load(); n != 0 {
			t.Fatalf("the roster rebuilds listed or ensured the primary %d times", n)
		}
	})
}

func TestAFailingPrimaryStillShowsYourSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		r.host.FailList(true)
		r.user.connect(t, "api-claude")
		time.Sleep(time.Second)
		v := lastRoster(t, r.roster)
		if v.YourCount != 1 || len(v.Yours) != 1 {
			t.Fatalf("your session shows: %+v", v)
		}
		if len(v.Agents) != 1 || v.Agents[0].Kind != "coordinator" {
			t.Fatalf("Pixie's agents are kept: %+v", v)
		}
	})
}

func TestAPrimaryThatNeverListedStillShowsYourSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, false)
		r.host.FailList(true)
		r.user.connect(t, "api-claude")
		time.Sleep(time.Second)
		v := lastRoster(t, r.roster)
		if v.YourCount != 1 || len(v.Agents) != 0 || v.Agents == nil {
			t.Fatalf("%+v", v)
		}
	})
}
