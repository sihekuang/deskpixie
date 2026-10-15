package tmux

import (
	"reflect"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// #87: a Codex coordinator reports its status through hooks, like a Claude agent: once it has,
// its screen only ends a turn the hooks left open. Codex workers (no hooks) still follow their
// screen (TestCodexStatusFollowsItsScreen).
func TestAHookedCodexAgentIsTreatedLikeAHookedClaudeAgent(t *testing.T) {
	e := &events{}
	d := NewDetector(e.emit)
	now := time.Unix(1000, 0)
	d.SetClock(func() time.Time { return now }, 5*time.Second)
	d.Track("%1", "coordinator", agent.Codex)
	d.Started("%1", agent.PaneIdle)
	wantEvents(t, e, status("coordinator", agent.PaneIdle))
	d.Hook("coordinator", agent.PaneWorking)
	wantEvents(t, e, status("coordinator", agent.PaneWorking))
	// An idle-looking screen mid-turn (Codex keeps its placeholder) doesn't end it at once.
	d.Screen("%1", codexIdle)
	wantEvents(t, e)
	d.Hook("coordinator", agent.PaneIdle)
	wantEvents(t, e, status("coordinator", agent.PaneIdle))
	if got := d.ScreenPanes(); len(got) != 0 {
		t.Fatal("an idle hooked Codex agent's screen is not read", got)
	}
	d.Screen("%1", codexWorking) // a late read: ignored
	wantEvents(t, e)
	// A turn the hooks left open ends on its idle screen after an Escape.
	d.Hook("coordinator", agent.PaneWorking)
	wantEvents(t, e, status("coordinator", agent.PaneWorking))
	if got := d.ScreenPanes(); !reflect.DeepEqual(got, []string{"%1"}) {
		t.Fatal(got)
	}
	d.Interrupted("coordinator")
	d.Screen("%1", codexIdle)
	wantEvents(t, e, status("coordinator", agent.PaneIdle))
}
