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
	launcher *agenttest.FakePlanner
	focus    *agenttest.FakeFocusSink
	sessions *agenttest.FakeSessionPlanner
	voice    *agenttest.RecordingVoiceLink
}

type pixFake struct {
	*agenttest.FakeSessionHost
	ch  chan struct{}
	ctx context.Context
	// about: a session's repo and description, by name (none: repo "api", no description).
	about map[string][2]string
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
		l[i].User = &UserSession{Tool: "claude", Repo: "api", Where: "iTerm", App: "com.googlecode.iterm2", Reachable: true, Pid: 4242}
		if a, ok := p.about[l[i].Name]; ok {
			l[i].User.Repo, l[i].User.Description = a[0], a[1]
		}
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
	user := &pixFake{FakeSessionHost: agenttest.NewFakeSessionHost(), ch: make(chan struct{}, 1), ctx: t.Context()}
	r := &rosterRig{host: host, user: user, roster: &agenttest.RecordingRoster{}, launcher: &agenttest.FakePlanner{}, focus: &agenttest.FakeFocusSink{},
		sessions: &agenttest.FakeSessionPlanner{Plan: FocusPlan{FocusURLs: []string{"warp://session/abc"}}}, voice: &agenttest.RecordingVoiceLink{}}
	clock := agenttest.ClockAt(1_000_000)
	deps := Deps{
		Host:  NewAgentHost(NewCompositeHost(wrap(&primaryFake{host, t.Context()}), user), clock, CoordinatorRecipe{Cwd: "/data/agent/workspace"}),
		Voice: r.voice, Notifier: &agenttest.RecordingNotifier{}, Pixie: &agenttest.RecordingNotifier{},
		Status: &agenttest.RecordingStatus{}, Planner: r.launcher, Sessions: r.sessions, Focus: r.focus, TerminalDir: "/home/me/Projects",
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

// #148: a click on one of your sessions shows that session's own terminal, planned from its
// process — Pixie's session is not started and nothing is attached.
func TestFocusSessionShowsThatSessionsOwnTerminal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, false)
		r.user.connect(t, "api-claude")
		time.Sleep(time.Second)
		if err := r.h.FocusSession(context.Background(), "api-claude"); err != nil {
			t.Fatal(err)
		}
		if got := r.sessions.Pids(); !reflect.DeepEqual(got, []int{4242}) {
			t.Fatalf("planned from its process: %v", got)
		}
		if got := r.focus.Sent(); len(got) != 1 || !reflect.DeepEqual(got[0], FocusPlan{FocusURLs: []string{"warp://session/abc"}}) {
			t.Fatalf("its plan goes to the app: %+v", got)
		}
		if len(r.launcher.Attaches()) != 0 || len(r.host.Focused()) != 0 {
			t.Fatalf("nothing attached or focused in Pixie's session: %v %v", r.launcher.Attaches(), r.host.Focused())
		}
	})
}

// Finding your session lists passively: a click on it must not start Pixie's session host.
func TestFocusSessionDoesNotStartPixiesSession(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var primary *passivePrimary
		r := newRosterRigWith(t, false, func(p *primaryFake) SessionHost {
			primary = &passivePrimary{primaryFake: p}
			return primary
		})
		r.user.connect(t, "api-claude")
		time.Sleep(time.Second)
		before := primary.starting.Load()
		if err := r.h.FocusSession(context.Background(), "api-claude"); err != nil {
			t.Fatal(err)
		}
		if n := primary.starting.Load(); n != before {
			t.Fatalf("the primary was asked to start %d time(s)", n-before)
		}
	})
}

// The roster lists your sessions even when Pixie's host can't list; a click must still work.
func TestFocusSessionWorksWhilePixiesHostCannotList(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, false)
		r.user.connect(t, "api-claude")
		time.Sleep(time.Second)
		r.host.FailList(true)
		if err := r.h.FocusSession(context.Background(), "api-claude"); err != nil {
			t.Fatal(err)
		}
		if got := r.focus.Sent(); len(got) != 1 {
			t.Fatalf("its plan goes to the app: %+v", got)
		}
	})
}

func TestFocusSessionOnlyFocusesYourSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		for _, name := range []string{"nobody-claude", Coordinator} {
			if err := r.h.FocusSession(context.Background(), name); err == nil {
				t.Fatalf("%s: no error", name)
			}
		}
		if len(r.sessions.Pids()) != 0 || len(r.focus.Sent()) != 0 {
			t.Fatalf("nothing planned or sent: %v %v", r.sessions.Pids(), r.focus.Sent())
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

// #234: the busy dot counts the part of the roster kept from before when its host can't be
// listed: a working coordinator kept from the last list still makes the roster busy.
func TestTheKeptPartStillCountsForBusy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		r.host.SetStatus(Coordinator, PaneWorking)
		time.Sleep(time.Second)
		if !lastRoster(t, r.roster).Busy {
			t.Fatal("a working coordinator isn't busy")
		}
		r.host.FailList(true)
		r.user.connect(t, "api-claude")
		r.user.SetStatus("api-claude", PaneIdle)
		time.Sleep(time.Second)
		if v := lastRoster(t, r.roster); len(v.Yours) != 1 || !v.Busy {
			t.Fatalf("%+v", v)
		}
	})
}
