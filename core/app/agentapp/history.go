package agentapp

import (
	"context"
	"path/filepath"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/pixhost"
)

// PixHistory is <app_data>/agent/pix-history.json: your past pix sessions (#461).
func (p Paths) PixHistory() string { return filepath.Join(p.Root, "pix-history.json") }

// resumer resumes a past pix session (pixhost.Resumer; tests fake it).
type resumer interface {
	Resume(ctx context.Context, id string) (agent.FocusPlan, error)
}

// pastSessions is History as the Resumer sees it: only sessions not running now, so a click on
// a stale row can't start a second pix on a live conversation.
type pastSessions struct {
	host  *pixhost.Host
	store pixhost.HistoryStore
}

func (p pastSessions) List() []pixhost.HistoryEntry { return p.host.History() }
func (p pastSessions) Update(id string, f func(*pixhost.HistoryEntry)) {
	p.store.Update(id, f)
}

// followHistory refreshes the tray on each History change until the agent stops.
func (a *Agent) followHistory(changes <-chan struct{}) {
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-changes:
			call(a.hooks.RefreshTray)
		}
	}
}
