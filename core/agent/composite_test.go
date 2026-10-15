package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// userHost is a fake secondary that owns the agents it started.
type userHost struct {
	*agenttest.FakeSessionHost
}

func (u userHost) Owns(n string) bool {
	_, ok := u.StatusOf(n)
	return ok
}

func newComposite(t *testing.T) (*agent.CompositeHost, *agenttest.FakeSessionHost, userHost) {
	t.Helper()
	primary := agenttest.NewFakeSessionHost()
	secondary := userHost{agenttest.NewFakeSessionHost()}
	if _, err := secondary.StartAgent(context.Background(), agent.AgentSpec{Name: "you-1"}); err != nil {
		t.Fatal(err)
	}
	return agent.NewCompositeHost(primary, secondary), primary, secondary
}

func TestCompositeRoutesByOwner(t *testing.T) {
	ctx := context.Background()
	c, primary, secondary := newComposite(t)
	if _, err := c.StartAgent(ctx, agent.AgentSpec{Name: "focus-fix"}); err != nil {
		t.Fatal(err)
	}
	if len(primary.Starts()) != 1 || len(secondary.Starts()) != 1 {
		t.Fatal("StartAgent must go to the primary")
	}
	c.Prompt(ctx, "you-1", "hi")
	c.Prompt(ctx, "focus-fix", "hi")
	c.SendKeys(ctx, "you-1", []string{"Escape"})
	if len(secondary.Prompts()) != 1 || len(primary.Prompts()) != 1 || len(secondary.Keys()) != 1 || len(primary.Keys()) != 0 {
		t.Fatalf("routing: secondary %v %v primary %v %v", secondary.Prompts(), secondary.Keys(), primary.Prompts(), primary.Keys())
	}
	list, _ := c.List(ctx)
	if len(list) != 2 {
		t.Fatalf("merged list: %+v", list)
	}
	if c.Name() != primary.Name() {
		t.Fatal("the name is the primary's")
	}
}

func TestCompositeUnknownAgentIsNoAgent(t *testing.T) {
	c, _, _ := newComposite(t)
	if err := c.Prompt(context.Background(), "nobody", "x"); !agent.IsHostError(err, agent.HostNoAgent) {
		t.Fatalf("%v", err)
	}
}

func TestCompositeListSurvivesSecondaryFailure(t *testing.T) {
	ctx := context.Background()
	c, primary, secondary := newComposite(t)
	primary.StartAgent(ctx, agent.AgentSpec{Name: "focus-fix"})
	secondary.FailList(true)
	list, err := c.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "focus-fix" {
		t.Fatalf("pix sessions must never break Pixie's own list: %+v %v", list, err)
	}
}

func TestCompositeMergesEvents(t *testing.T) {
	c, primary, secondary := newComposite(t)
	events := c.Subscribe()
	primary.StartAgent(context.Background(), agent.AgentSpec{Name: "focus-fix"})
	secondary.SetStatus("you-1", agent.PaneWorking)
	primary.SetStatus("focus-fix", agent.PaneBlocked)
	got := map[string]bool{}
	deadline := time.After(2 * time.Second)
	for !(got["you-1"] && got["focus-fix"]) {
		select {
		case ev := <-events:
			if ev.Kind == agent.PaneEventStatus && ev.Status != agent.PaneIdle {
				got[ev.Agent] = true
			}
		case <-deadline:
			t.Fatalf("merged events: %v", got)
		}
	}
}

func TestCompositeFocusOnlyForPrimaryAgents(t *testing.T) {
	ctx := context.Background()
	c, primary, _ := newComposite(t)
	primary.StartAgent(ctx, agent.AgentSpec{Name: "focus-fix"})
	c.Focus(ctx, "you-1")
	c.Focus(ctx, "focus-fix")
	if f := primary.Focused(); len(f) != 1 || f[0] != "focus-fix" {
		t.Fatalf("focused %v", f)
	}
}
