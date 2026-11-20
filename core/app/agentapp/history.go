package agentapp

import (
	"cmp"
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app"
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

// Update writes the raw store, deliberately unsignalled: it only sets HerdrPane, which no row shows.
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

// ResumeGrace is how long after a resume opened its tab a click on it is still ignored: until
// its pix registers, History lists it, and a second click would open a second tab.
const ResumeGrace = 10 * time.Second

// ResumeFailed is the notification text when Herdr itself failed (the log has why).
const ResumeFailed = "Herdr couldn't open it. Try again."

// historyRows is Tray ▸ History: the past sessions, nil without the pix host.
func (a *Agent) historyRows() []app.HistoryRow {
	if a.pix == nil {
		return nil
	}
	var rows []app.HistoryRow
	for _, e := range a.pix.History() {
		folder := cmp.Or(e.GitRoot, e.Cwd)
		if folder != "" {
			folder = filepath.Base(folder)
		}
		rows = append(rows, app.HistoryRow{ID: e.SessionID, Description: e.Description, Folder: folder,
			LastActive: e.LastActive, Unresumable: e.Unresumable})
	}
	return rows
}

// historyMenu runs agent:history:<what>: "resume:<id>", "remove:<id>" or "clear"; false for
// anything else.
func (a *Agent) historyMenu(what string) bool {
	if what == "clear" {
		if a.pix != nil {
			a.pix.ClearHistory()
		}
		return true
	}
	if id, ok := strings.CutPrefix(what, "remove:"); ok && id != "" {
		if a.pix != nil {
			a.pix.RemoveHistory(id)
		}
		return true
	}
	if id, ok := strings.CutPrefix(what, "resume:"); ok && id != "" {
		a.resume(id)
		return true
	}
	return false
}

// resume opens a past session in its Herdr tab and shows it (async). A click on one being
// resumed, or just resumed, is ignored; a failure is a notification saying why.
func (a *Agent) resume(id string) {
	if a.resumer == nil {
		log.Print("agent: resume: sessions you start with pix are unavailable")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return // quitting: nothing new beside Shutdown's wait
	}
	if a.resuming[id] {
		log.Print("agent: resume: that session is already being resumed")
		return
	}
	a.resuming[id] = true
	a.wg.Go(func() {
		plan, err := a.resumer.Resume(a.ctx, id)
		if err != nil {
			a.resumed(id)
			log.Printf("agent: resume: %v", err)
			var le agent.LaunchError
			text := ResumeFailed
			if errors.As(err, &le) {
				text = string(le)
			}
			if a.ctx.Err() == nil && a.hooks.Notify != nil {
				a.hooks.Notify("Couldn't resume", text)
			}
			return
		}
		time.AfterFunc(a.resumeGrace, func() { a.resumed(id) })
		if a.ctx.Err() == nil {
			focusSink{a.hooks}.Focus(plan)
		}
	})
}

// resumed: a click on id resumes it again.
func (a *Agent) resumed(id string) {
	a.mu.Lock()
	delete(a.resuming, id)
	a.mu.Unlock()
}
