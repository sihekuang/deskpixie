package tmux

// Agent status for a host that detects none itself. tmux knows only whether a pane's process
// is alive, so status comes from three sources, merged per pane:
//   - Claude agents' own hooks (UserPromptSubmit working, Stop idle, Notification blocked),
//     which the runtime hands over through Host.ReportStatus. Exact, so once a Claude agent has
//     reported, its screen is no longer read.
//   - The screen (capture-pane every ScreenPoll) for Codex, which has no hooks, and for a
//     Claude agent that has not reported yet (e.g. one adopted after an app restart), read with
//     the markers the controller already uses (agent.BusyMarkers, the ready prompts). A hooked
//     Claude agent's screen still ends a turn its hooks left open, since a turn can end
//     without a Stop hook: one interrupted with Escape (seen on the Herdr smokes with Claude
//     Code 2.1; the first idle screen after an Escape the host sent counts), or one that
//     failed (an idle screen held for HookIdleGrace).
//   - tmux's pane state (list-panes every ExitPoll): a dead or vanished pane is Exited.
//
// Events follow Herdr's rules: a status only when it changes, an exit once (nothing more for
// that pane after it), and nothing for an agent whose start hasn't finished (StartAgent
// reports that one's failure itself).

import (
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// ClaudeReadyMarkers: Claude Code's input prompt is up (its hint line under the box).
var ClaudeReadyMarkers = []string{"for shortcuts", "Try \"", "shift+tab to cycle"}

// ClaudeDialogMarkers: Claude Code waits on a dialog (the folder-trust prompt, a permission
// prompt); a start counts as done once it shows (as on Herdr 0.9.x), and the agent is blocked.
var ClaudeDialogMarkers = []string{"trust this folder", "Do you trust the files", "Do you want to"}

// CodexApprovalMarkers: Codex waits for an approval.
var CodexApprovalMarkers = []string{
	agent.CodexMcpApprovalMarker,
	"Would you like to run the following command",
	"Would you like to make the following edits",
}

// HookIdleGrace is how long a hooked Claude agent whose hooks left it working or blocked must
// show its idle prompt (and no busy marker) before it counts as idle without a Stop hook.
const HookIdleGrace = 5 * time.Second

// busyLines is how many non-empty lines at the bottom of a screen the busy marker is looked for
// in (agent.InputStateOf's window).
const busyLines = 8

func anyIn(lines []string, markers []string) bool {
	return slices.ContainsFunc(lines, func(l string) bool {
		return slices.ContainsFunc(markers, func(m string) bool { return strings.Contains(l, m) })
	})
}

// Classify reads an agent's status off its screen: blocked on a dialog or approval, working
// while it shows how to interrupt it, idle at its input prompt; Unknown when none of these show.
func Classify(kind agent.AgentKind, screen string) agent.PaneStatus {
	bottom := agent.ScreenBottom(screen, agent.ScreenBottomLines)
	switch kind {
	case agent.Codex:
		// Its trust screen ends with "· esc quit", which is no busy marker: dialogs first.
		if agent.ShowsCodexTrust(screen) || anyIn(bottom, CodexApprovalMarkers) {
			return agent.PaneBlocked
		}
	default:
		if anyIn(bottom, ClaudeDialogMarkers) {
			return agent.PaneBlocked
		}
	}
	if anyIn(bottom[max(0, len(bottom)-busyLines):], agent.BusyMarkers) {
		return agent.PaneWorking
	}
	ready := ClaudeReadyMarkers
	if kind == agent.Codex {
		ready = []string{agent.CodexIdleMarker, "for shortcuts"}
	}
	if anyIn(bottom, ready) {
		return agent.PaneIdle
	}
	return agent.PaneUnknown
}

// StartedScreen: a starting agent is up. Claude: its input prompt or a dialog (folder trust
// counts as started, as on Herdr 0.9.x). Codex: its empty input box (the recipe's Ready
// marker), or its trust screen, which the runtime's start check refuses with its own reason.
func StartedScreen(kind agent.AgentKind, screen string) (agent.PaneStatus, bool) {
	bottom := agent.ScreenBottom(screen, agent.ScreenBottomLines)
	if kind == agent.Codex {
		switch {
		case agent.ShowsCodexTrust(screen):
			return agent.PaneBlocked, true
		case anyIn(bottom, []string{agent.CodexIdleMarker}):
			return Classify(kind, screen), true
		}
		return agent.PaneUnknown, false
	}
	if anyIn(bottom, ClaudeDialogMarkers) {
		return agent.PaneBlocked, true
	}
	if anyIn(bottom, ClaudeReadyMarkers) {
		return Classify(kind, screen), true
	}
	return agent.PaneUnknown, false
}

// tracked is one agent pane the detector knows.
type tracked struct {
	name string
	kind agent.AgentKind
	gen  uint64
	// started: StartAgent finished (or the pane was adopted from List); before that, nothing is
	// reported for it.
	started bool
	// hooked: the agent has reported its status through hooks; its screen only ends a turn the
	// hooks left open (idleSince: since when it has shown idle; interrupted: Escape was sent).
	hooked      bool
	idleSince   time.Time
	interrupted bool
	status      agent.PaneStatus
	// sent is the last status reported to subscribers (hasSent false before the first).
	sent    agent.PaneStatus
	hasSent bool
}

// Generation is a tracked pane and its generation, as Snapshot took them.
type Generation struct {
	Pane string
	Gen  uint64
}

// Row is one list-panes line.
type Row struct {
	Window string
	Pane   string
	Dead   bool
	Kind   string // the window's @deskpixie_kind ("" when not ours)
	Cwd    string
}

// Detector merges the status sources into PaneEvents (see the file comment). Goroutine-safe;
// emit is called under its lock, so it must not block or call back into the detector.
type Detector struct {
	mu    sync.Mutex
	panes map[string]*tracked
	seq   uint64
	emit  func(agent.PaneEvent)
	now   func() time.Time
	grace time.Duration
}

func NewDetector(emit func(agent.PaneEvent)) *Detector {
	return &Detector{panes: map[string]*tracked{}, emit: emit, now: time.Now, grace: HookIdleGrace}
}

// SetClock replaces the clock and HookIdleGrace (tests).
func (d *Detector) SetClock(now func() time.Time, grace time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.now, d.grace = now, grace
}

func (d *Detector) send(evs []agent.PaneEvent) {
	for _, ev := range evs {
		d.emit(ev)
	}
}

// Track starts tracking a new agent pane (not yet started). A pane already tracked is kept.
func (d *Detector) Track(pane, name string, kind agent.AgentKind) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.panes[pane]; ok {
		return
	}
	d.seq++
	d.panes[pane] = &tracked{name: name, kind: kind, gen: d.seq, status: agent.PaneUnknown}
}

// Adopt tracks a running agent found by List (started already); false if it was tracked.
func (d *Detector) Adopt(pane, name string, kind agent.AgentKind) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.panes[pane]; ok {
		return false
	}
	d.seq++
	d.panes[pane] = &tracked{name: name, kind: kind, gen: d.seq, started: true, status: agent.PaneUnknown}
	return true
}

// Forget drops pane without reporting anything (a start that failed, a replaced agent).
func (d *Detector) Forget(pane string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.panes, pane)
}

// update sets t's status and returns the event to send, if it changed (only once started).
func (t *tracked) update(s agent.PaneStatus) []agent.PaneEvent {
	if s == agent.PaneUnknown {
		return nil
	}
	t.status = s
	if !t.started || (t.hasSent && t.sent == s) {
		return nil
	}
	t.sent, t.hasSent = s, true
	return []agent.PaneEvent{{Kind: agent.PaneEventStatus, Agent: t.name, Status: s}}
}

// Started: StartAgent saw pane come up showing status. A status the agent's hooks reported
// meanwhile wins. Returns the status now in effect.
func (d *Detector) Started(pane string, status agent.PaneStatus) agent.PaneStatus {
	d.mu.Lock()
	t, ok := d.panes[pane]
	var evs []agent.PaneEvent
	if ok {
		t.started = true
		if t.hooked || status == agent.PaneUnknown {
			status = t.status
		}
		evs = t.update(status)
		status = t.status
	}
	d.send(evs) // under the lock: events keep their order
	d.mu.Unlock()
	return status
}

// newest is the most recently tracked pane named name.
func (d *Detector) newest(name string) *tracked {
	var best *tracked
	for _, t := range d.panes {
		if t.name == name && (best == nil || t.gen > best.gen) {
			best = t
		}
	}
	return best
}

// Hook is an agent's own status report (its newest pane); from then on its screen isn't read.
func (d *Detector) Hook(name string, status agent.PaneStatus) {
	d.mu.Lock()
	var evs []agent.PaneEvent
	if t := d.newest(name); t != nil && status != agent.PaneUnknown {
		t.hooked, t.idleSince, t.interrupted = true, time.Time{}, false
		evs = t.update(status)
	}
	d.send(evs) // under the lock: events keep their order
	d.mu.Unlock()
}

// Interrupted: the host sent Escape to the agent (its newest pane). A hooked Claude agent's
// turn ends on its next idle screen.
func (d *Detector) Interrupted(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t := d.newest(name); t != nil && t.hooked {
		t.interrupted = true
	}
}

// openTurn: a hooked agent whose hooks say it is in a turn (only its screen can end it early).
func (t *tracked) openTurn() bool {
	return t.status == agent.PaneWorking || t.status == agent.PaneBlocked
}

// ScreenPanes is every started pane whose screen is read: Codex agents, Claude agents that
// haven't reported through hooks yet, and hooked ones in a turn.
func (d *Detector) ScreenPanes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for p, t := range d.panes {
		if t.started && (t.kind == agent.Codex || !t.hooked || t.openTurn()) {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// Screen is a read of pane's screen: its status, if it shows one. For a Claude agent that
// reports through hooks, only an idle screen that ends a turn the hooks left open (see the
// file comment).
func (d *Detector) Screen(pane, screen string) {
	d.mu.Lock()
	var evs []agent.PaneEvent
	t, ok := d.panes[pane]
	switch {
	case !ok:
	case t.kind == agent.Codex || !t.hooked:
		evs = t.update(Classify(t.kind, screen))
	case !t.openTurn() || Classify(t.kind, screen) != agent.PaneIdle:
		t.idleSince = time.Time{}
	default:
		now := d.now()
		if t.idleSince.IsZero() {
			t.idleSince = now
		}
		if t.interrupted || now.Sub(t.idleSince) >= d.grace {
			t.idleSince, t.interrupted = time.Time{}, false
			evs = t.update(agent.PaneIdle)
		}
	}
	d.send(evs) // under the lock: events keep their order
	d.mu.Unlock()
}

// Snapshot is every tracked pane with its generation, taken before a list-panes whose rows go
// to Listed (an agent tracked after it is left alone).
func (d *Detector) Snapshot() []Generation {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Generation, 0, len(d.panes))
	for p, t := range d.panes {
		out = append(out, Generation{p, t.gen})
	}
	slices.SortFunc(out, func(a, b Generation) int { return strings.Compare(a.Pane, b.Pane) })
	return out
}

// Listed reconciles known (a Snapshot) with a list-panes taken after it: a started agent whose
// pane is dead or gone is reported Exited, once, and forgotten. rows nil with gone true means
// the whole session is gone.
func (d *Detector) Listed(known []Generation, rows []Row) {
	d.mu.Lock()
	var evs []agent.PaneEvent
	for _, k := range known {
		t, ok := d.panes[k.Pane]
		if !ok || t.gen != k.Gen || !t.started {
			continue
		}
		i := slices.IndexFunc(rows, func(r Row) bool { return r.Pane == k.Pane })
		if i >= 0 && !rows[i].Dead {
			continue
		}
		delete(d.panes, k.Pane)
		evs = append(evs, agent.PaneEvent{Kind: agent.PaneEventExited, Agent: t.name})
	}
	d.send(evs) // under the lock: events keep their order
	d.mu.Unlock()
}

// Gone: pane was closed (by us): its agent is reported Exited unless it already was, and
// forgotten.
func (d *Detector) Gone(pane string) {
	d.mu.Lock()
	var evs []agent.PaneEvent
	if t, ok := d.panes[pane]; ok {
		delete(d.panes, pane)
		if t.started {
			evs = append(evs, agent.PaneEvent{Kind: agent.PaneEventExited, Agent: t.name})
		}
	}
	d.send(evs) // under the lock: events keep their order
	d.mu.Unlock()
}

// Status is pane's current status (Unknown if untracked or not known yet).
func (d *Detector) Status(pane string) agent.PaneStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.panes[pane]; ok {
		return t.status
	}
	return agent.PaneUnknown
}

// Len is how many panes are tracked.
func (d *Detector) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.panes)
}
