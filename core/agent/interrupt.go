package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Interrupt is the stop button by an agent that is working (#309), in the roster or the chat
// window: its current turn is interrupted the way Esc in its terminal does. Nothing is closed.
// name is the roster row's: the coordinator, one of Pixie's workers or panes, or a session of
// yours. What is called is decided here:
//   - the coordinator: the controller ends its turn (the typed jobs it carries are cancelled,
//     as Stop in the text box does);
//   - your session: pix presses Esc, as the coordinator's interrupt_session does;
//   - one of Pixie's panes: Esc in it; the controller's own Idle-without-Stop handling ends
//     the turn, as when you press Esc there.
//
// false (no error): it wasn't working by the time the click came, so nothing was sent. An
// error is a whole sentence for the user ("Couldn't stop docs: it isn't running anymore.").
func (h *AgentHandle) Interrupt(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return false, errors.New("Couldn't stop that: it has no name.")
	}
	a, err := h.findAgent(ctx, name)
	if err != nil {
		return false, stopFailed(name, err)
	}
	if a.Status != PaneWorking {
		return false, nil
	}
	session := h.deps.Host.Session()
	switch {
	case a.Name == Coordinator && a.Origin != OriginUser:
		h.Input(InterruptTurnInput())
		return true, nil
	case a.Origin == OriginUser:
		if a.User != nil && !a.User.Reachable {
			return false, stopFailed(name, errUnreachable)
		}
		r := sessionTool(ctx, session, McpCall{Kind: CallInterruptSession, Name: name}, h.maskPending)
		if r.Err != nil {
			return false, stopFailed(name, r.Err)
		}
		return true, nil
	default:
		if err := session.SendKeys(ctx, name, []string{"Escape"}); err != nil {
			return false, stopFailed(name, err)
		}
		return true, nil
	}
}

// errGone and errUnreachable are why a stop couldn't reach its agent.
var (
	errGone        = errors.New("it isn't running anymore")
	errUnreachable = errors.New("its pix can't be reached; it may have been closed")
)

// findAgent is name as the hosts list it now (passively: nothing is started), or why not.
func (h *AgentHandle) findAgent(ctx context.Context, name string) (AgentInfo, error) {
	session := h.deps.Host.Session()
	var parts ListParts
	if c, ok := session.(*CompositeHost); ok {
		parts = c.ListRunningParts(ctx)
	} else {
		parts.Primary, parts.PrimaryErr = ListRunning(ctx, session)
	}
	for _, a := range append(parts.Primary, parts.Secondary...) {
		if a.Name == name {
			return a, nil
		}
	}
	if err := errors.Join(parts.PrimaryErr, parts.SecondaryErr); err != nil {
		return AgentInfo{}, err
	}
	return AgentInfo{}, errGone
}

// stopFailed is a failed stop in words for the user: "Couldn't stop <name>: <why>."
func stopFailed(name string, err error) error {
	who := name
	if name == Coordinator {
		who = "the coordinator"
	}
	why := err.Error()
	var he HostError
	if errors.As(err, &he) {
		switch {
		case he.Kind == HostNoAgent:
			why = errGone.Error()
		case he.Kind == HostTimeout:
			why = "it didn't answer in time"
		case he.Kind == HostOther && he.Detail != "":
			why = he.Detail // pix's own reason, e.g. a startup menu only you should answer (#306)
		}
	}
	why = strings.TrimSuffix(strings.TrimSpace(why), ".")
	return fmt.Errorf("Couldn't stop %s: %s.", who, why)
}
