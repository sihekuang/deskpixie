package agent_test

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// addDirScreen is Claude Code 2.1.329's "Adding a new directory" prompt (2026-11-20).
const addDirScreen = ` Adding a new directory:

 /Users/me/Library/Application Support/com.sihekuang.deskpixie/canvas

 This session hasn't worked here before. Is this a directory you created or one you trust?

 ❯ No, exit
   Yes, add it

 Enter to confirm · Esc to cancel`

// #459 through the runtime: a coordinator waiting at a prompt in its terminal has its screen
// read from the session host, and the user is told which prompt it is and what to pick.
func TestTheRuntimeReadsTheBlockedCoordinatorsScreenToSayWhatToPick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := rigWith(t, &agenttest.MemStore{}, true)
		settle()
		if len(r.host.Starts()) != 1 {
			t.Fatal("not started", r.host.Starts())
		}
		r.host.SetScreen(Coordinator, addDirScreen)
		r.host.SetStatus(Coordinator, PaneBlocked)
		settle()
		r.clock.Advance(BlockedGraceMs)
		time.Sleep(2 * time.Second)
		said := func() bool {
			for _, a := range r.alerts.Alerts() {
				if strings.Contains(a.Body, `choose "Yes, add it"`) {
					return true
				}
			}
			return false
		}
		if !eventually(said) {
			t.Fatal(r.alerts.Alerts())
		}
	})
}
