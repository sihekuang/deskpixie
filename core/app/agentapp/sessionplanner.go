package agentapp

import (
	"context"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/pixhost"
)

// herdrAwareSessions shows a session you resumed in Herdr by its pane (#461); any other the
// usual way, by its terminal app.
type herdrAwareSessions struct {
	inner   agent.SessionPlanner
	paneOf  func(pid int) (string, bool) // pixhost.Host.HerdrPaneOf
	resumer *pixhost.Resumer             // nil: Herdr isn't set up
}

func (p herdrAwareSessions) PlanSession(pid int) (agent.FocusPlan, error) {
	if pane, ok := p.paneOf(pid); ok && p.resumer != nil {
		return p.resumer.Show(context.Background(), pane)
	}
	return p.inner.PlanSession(pid)
}
