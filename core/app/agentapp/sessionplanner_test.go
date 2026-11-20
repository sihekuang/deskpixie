package agentapp

import (
	"context"
	"reflect"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/pixhost"
)

// yoursTabs is the yours Herdr session, recording the panes it brings forward.
type yoursTabs struct{ focused []string }

func (y *yoursTabs) RunInTab(context.Context, string, string, []agent.EnvVar, string) (string, error) {
	return "", nil
}
func (y *yoursTabs) FocusPane(_ context.Context, pane string) error {
	y.focused = append(y.focused, pane)
	return nil
}
func (y *yoursTabs) AttachCommand() []string {
	return []string{"herdr", "session", "attach", "deskpixie-yours"}
}

func herdrAware(panes map[int]string, resumer *pixhost.Resumer) (herdrAwareSessions, *agenttest.FakeSessionPlanner) {
	inner := &agenttest.FakeSessionPlanner{Plan: agent.FocusPlan{ActivateApp: "com.googlecode.iterm2"}}
	return herdrAwareSessions{inner: inner, resumer: resumer, paneOf: func(pid int) (string, bool) {
		p, ok := panes[pid]
		return p, ok
	}}, inner
}

// #461: a session resumed in Herdr is shown by its pane in the yours session; any other by its
// terminal app, as before.
func TestASessionResumedInHerdrIsShownByItsPane(t *testing.T) {
	tabs, planner := &yoursTabs{}, &agenttest.FakePlanner{}
	p, inner := herdrAware(map[int]string{42: "w1:p7"}, &pixhost.Resumer{Tabs: tabs, Planner: planner})
	plan, err := p.PlanSession(42)
	if err != nil || plan.NewTerminal == nil || plan.NewTerminal.URL != "fake://opened" {
		t.Fatalf("%+v %v", plan, err)
	}
	if !reflect.DeepEqual(tabs.focused, []string{"w1:p7"}) || !reflect.DeepEqual(planner.Opened(), [][]string{tabs.AttachCommand()}) {
		t.Fatal(tabs.focused, planner.Opened())
	}
	if len(inner.Pids()) != 0 {
		t.Fatal(inner.Pids())
	}
	plan, err = p.PlanSession(43)
	if err != nil || plan.ActivateApp != "com.googlecode.iterm2" || !reflect.DeepEqual(inner.Pids(), []int{43}) {
		t.Fatalf("%+v %v %v", plan, err, inner.Pids())
	}
}

// Without a Resumer (Herdr not set up) even a resumed session is shown the usual way.
func TestWithoutAResumerSessionsAreShownTheUsualWay(t *testing.T) {
	p, inner := herdrAware(map[int]string{42: "w1:p7"}, nil)
	if plan, err := p.PlanSession(42); err != nil || plan.ActivateApp == "" || !reflect.DeepEqual(inner.Pids(), []int{42}) {
		t.Fatalf("%+v %v", plan, err)
	}
}
