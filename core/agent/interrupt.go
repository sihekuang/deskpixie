package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Interrupt is the stop button by an agent that is working (#309), in the roster or the chat
// window: its current turn is interrupted the way Esc in its terminal does. Nothing is closed.
// name is the roster row's: the coordinator, one of Pixie's workers or panes, or a session of
// yours. What is called is decided here:
//   - the coordinator: the controller ends its turn (the jobs whose request it carries are
//     cancelled, as Stop in the text box does);
//   - your session: pix presses Esc, as the coordinator's interrupt_session does (and holds
//     what waits for it until you type there or Pixie sends something new);
//   - one of Pixie's panes: Esc in it; a worker's turn is then over, its messages wait, and the
//     coordinator is told the user stopped it.
//
// A second stop of the same agent before its status was seen to change since the last one
// (within StopRepeatMs) sends nothing and succeeds: another Esc would open Claude's rewind menu
// or Codex's backtrack mode. A stop that timed out may still be delivered, so it holds until the
// status changes, however long. false (no error): it wasn't working by the time the click came, so
// nothing was sent. An error is a whole sentence for the user ("Couldn't stop docs: it isn't
// running anymore.").
func (h *AgentHandle) Interrupt(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return false, ErrStopNoName
	}
	a, err := h.findAgent(ctx, name)
	if err != nil {
		return false, stopFailed(name, err)
	}
	if a.Status != PaneWorking {
		h.stops.changed(name)
		return false, nil
	}
	now := h.deps.Clock.NowMs()
	if !h.stops.take(name, now) {
		return true, nil // the stop before it is still on its way
	}
	sent, err := h.interrupt(ctx, a)
	switch {
	case IsHostError(err, HostTimeout):
		h.stops.hold(name, now) // its Esc may still land when the session catches up
	case err != nil || !sent:
		h.stops.drop(name, now)
	}
	if err != nil {
		return false, stopFailed(name, err)
	}
	return sent, nil
}

// interrupt sends a's stop; an error is the host's own.
func (h *AgentHandle) interrupt(ctx context.Context, a AgentInfo) (bool, error) {
	session := h.deps.Host.Session()
	switch {
	case a.Name == Coordinator && a.Origin != OriginUser:
		if r := h.stepLocked(InterruptTurnInput()); r != nil {
			sent, _ := r.Value.(bool)
			return sent, nil
		}
		return false, nil
	case a.Origin == OriginUser:
		if a.User != nil && !a.User.Reachable {
			return false, errUnreachable
		}
		r := sessionTool(ctx, session, McpCall{Kind: CallInterruptSession, Name: a.Name}, h.maskPending)
		if r.Err != nil {
			return false, r.Err
		}
		return true, nil
	default:
		if err := session.SendKeys(ctx, a.Name, []string{"Escape"}); err != nil {
			return false, err
		}
		h.stepLocked(WorkerInterruptedInput(a.Name)) // a pane not Pixie's worker: Esc was all
		return true, nil
	}
}

// StopRepeatMs: how long a stop holds back the next one of the same agent while its status
// hasn't been seen to change (#309); after that a stop goes regardless (it ignored the Esc).
const StopRepeatMs Millis = 5_000

// stopGuard remembers each agent's last stop, until its status is seen to change.
type stopGuard struct {
	mu   sync.Mutex
	last map[string]stopMark
}

type stopMark struct {
	at      Millis
	changed bool // its status was seen to be other than working since
	held    bool // it timed out: it holds until changed, not only StopRepeatMs
}

// take: a stop of name may go now (and is recorded); false: one went too recently.
func (g *stopGuard) take(name string, now Millis) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.last[name]; ok && !m.changed && (m.held || satSub(now, m.at) < StopRepeatMs) {
		return false
	}
	if g.last == nil {
		g.last = map[string]stopMark{}
	}
	g.last[name] = stopMark{at: now}
	return true
}

// drop forgets the stop taken at now: it didn't go.
func (g *stopGuard) drop(name string, now Millis) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.last[name]; ok && m.at == now {
		delete(g.last, name)
	}
}

// hold keeps the stop taken at now until name's status changes (it timed out).
func (g *stopGuard) hold(name string, now Millis) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.last[name]; ok && m.at == now {
		m.held = true
		g.last[name] = m
	}
}

// changed: name was seen not working, so its next turn may be stopped.
func (g *stopGuard) changed(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.last[name]; ok {
		m.changed = true
		g.last[name] = m
	}
}

// seen takes a listing: every agent in it that isn't working has changed.
func (g *stopGuard) seen(list []AgentInfo) {
	for _, a := range list {
		if a.Status != PaneWorking {
			g.changed(a.Name)
		}
	}
}

// ErrStopNoName: a stop with no agent named (the app never sends one).
var ErrStopNoName = errors.New("Couldn't stop it.")

// errGone and errUnreachable are why a stop couldn't reach its agent.
var (
	errGone        = errors.New("it isn't running anymore")
	errUnreachable = errors.New("its pix can't be reached; it may have been closed")
)

// errCantList: the hosts couldn't be listed, so whether it still runs is unknown.
var errCantList = errors.New("Pixie couldn't check on it. Try again.")

// findAgent is name as the hosts list it now (passively: nothing is started), or why not.
func (h *AgentHandle) findAgent(ctx context.Context, name string) (AgentInfo, error) {
	session := h.deps.Host.Session()
	var parts ListParts
	if c, ok := session.(*CompositeHost); ok {
		parts = c.ListRunningParts(ctx)
	} else {
		parts.Primary, parts.PrimaryErr = ListRunning(ctx, session)
	}
	list := append(parts.Primary, parts.Secondary...)
	h.stops.seen(list)
	for _, a := range list {
		if a.Name == name {
			return a, nil
		}
	}
	if err := errors.Join(parts.PrimaryErr, parts.SecondaryErr); err != nil {
		return AgentInfo{}, errCantList
	}
	return AgentInfo{}, errGone
}

// stopFailed is a failed stop in words for the user: "Couldn't stop <name>: <why>.", or, when
// the hosts couldn't be listed or the failure is no host's, "Pixie couldn't check on it. Try
// again." (their own text is no sentence for the user).
func stopFailed(name string, err error) error {
	if errors.Is(err, errCantList) {
		return errCantList
	}
	who := name
	if name == Coordinator {
		who = "the coordinator"
	}
	var why string
	var he HostError
	switch {
	case errors.Is(err, errGone), errors.Is(err, errUnreachable):
		why = err.Error()
	case !errors.As(err, &he):
		return errCantList
	case he.Kind == HostNoAgent:
		why = errGone.Error()
	case he.Kind == HostTimeout:
		why = "it didn't answer in time"
	case he.Kind == HostOther && he.Detail != "":
		why = he.Detail // pix's own reason, e.g. a startup menu only you should answer (#306)
	default:
		return errCantList
	}
	why = strings.TrimSuffix(strings.TrimSpace(why), ".")
	return fmt.Errorf("Couldn't stop %s: %s.", who, why)
}
