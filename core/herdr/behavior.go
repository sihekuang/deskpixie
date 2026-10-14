package herdr

import "github.com/sihekuang/deskpixie/core/agent"

// Behavior is what differs between the supported Herdr versions (SupportedVersions), keyed on
// the protocol the CLI and the server agreed on (Supervisor.Protocol). Checked live on 0.9.4
// (docs/parity/live-results.md); the rest is the same on both:
//   - agent.start may answer agent_pane_busy right after tab.create on 0.7.5 (never seen on
//     0.9.4); start retries it on both.
//   - Read uses agent.read's "visible" source on both ("recent" came back empty on 0.7.5).
//   - launch_pending outlasts detection by a few seconds on both: start waits it out.
//   - 0.9.x refuses a whole events.subscribe naming a pane that is gone (pane_not_found); the
//     pump drops that pane and resubscribes (Host.dropGonePane), which is a no-op on 0.7.5.
type Behavior struct {
	// ExitDropsName: an agent process that exits takes its name with it (agent.get answers
	// agent_not_found) instead of leaving its pane named without an agent. 0.9.x.
	ExitDropsName bool
	// BlockedWhileLaunching: an agent blocked on a dialog (Claude Code's folder trust) stays
	// launch_pending until the dialog is answered, and its screen can be read and answered
	// then. 0.9.x; 0.7.5 cleared launch_pending first.
	BlockedWhileLaunching bool
}

// BehaviorFor is the behaviour for a negotiated protocol; 0.7.5's for any other (0 before one
// was negotiated).
func BehaviorFor(protocol uint32) Behavior {
	if protocol == 22 {
		return Behavior{ExitDropsName: true, BlockedWhileLaunching: true}
	}
	return Behavior{}
}

// started: the new agent is ready for prompts, or (BlockedWhileLaunching) detected and blocked
// on a dialog the caller can read and answer.
func (b Behavior) started(a HerdrAgent) bool {
	return a.Ready() || (b.BlockedWhileLaunching && a.LaunchPending && a.Agent != "" && a.Status == agent.PaneBlocked)
}
