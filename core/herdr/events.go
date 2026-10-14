package herdr

// Herdr's event stream → agent.PaneEvents for named agents, without replayed duplicates.
//
// Captured from Herdr 0.7.5 (0.9.x's events are a superset; SupportedVersions): right after subscription_started the server replays its journal
// of global pane events (pane_created for panes that already exist, and closes from long ago:
// command-center #57/#69/#70). Status changes arrive only for panes subscribed by id, so the
// stream is re-subscribed whenever an agent starts.

import (
	"encoding/json"
	"sort"

	"github.com/sihekuang/deskpixie/core/agent"
)

// RawKind tells RawEvent variants apart.
type RawKind int

const (
	RawStatus RawKind = iota
	RawClosed
	RawExited
)

// RawEvent is one pane event from the stream (Status carries Status).
type RawEvent struct {
	Kind   RawKind
	Pane   string
	Status agent.PaneStatus
}

// ParseLine turns one event line into a raw event; anything else (pane_created,
// agent_detected, junk) is false.
func ParseLine(line string) (RawEvent, bool) {
	var msg map[string]any
	if json.Unmarshal([]byte(line), &msg) != nil {
		return RawEvent{}, false
	}
	data, ok := msg["data"].(map[string]any)
	if !ok {
		return RawEvent{}, false
	}
	event, ok := msg["event"].(string)
	if !ok {
		return RawEvent{}, false
	}
	pane, hasPane := data["pane_id"].(string)
	switch event {
	case "pane.agent_status_changed", "pane_agent_status_changed":
		s, ok := data["agent_status"].(string)
		if !ok || !hasPane {
			return RawEvent{}, false
		}
		return RawEvent{Kind: RawStatus, Pane: pane, Status: agent.ParsePaneStatus(s)}, true
	case "pane_closed", "pane.closed":
		return RawEvent{Kind: RawClosed, Pane: pane}, hasPane
	case "pane_exited", "pane.exited":
		return RawEvent{Kind: RawExited, Pane: pane}, hasPane
	}
	return RawEvent{}, false
}

// SubscribeParams is events.subscribe's params: closes and exits for every pane, status for
// the named ones (sorted).
func SubscribeParams(panes []string) map[string]any {
	subs := []any{
		map[string]any{"type": "pane.closed"},
		map[string]any{"type": "pane.exited"},
	}
	ids := append([]string(nil), panes...)
	sort.Strings(ids)
	for _, p := range ids {
		subs = append(subs, map[string]any{"type": "pane.agent_status_changed", "pane_id": p})
	}
	return map[string]any{"subscriptions": subs}
}

// ReplayMaxMs: a replay older than this ends by itself (a host that forgets EndReplay must not
// hide exits). Verbatim from Rust.
const ReplayMaxMs uint64 = 2000

// Generation is a known agent's pane and its generation (Rust's (String, u64)).
type Generation struct {
	Pane string
	Gen  uint64
}

// Listed is one agent.list entry for Reconcile: HasAgent false for a pane that carries no
// agent (none detected yet, or its process exited); Rust's (String, Option<PaneStatus>).
type Listed struct {
	Pane     string
	Status   agent.PaneStatus
	HasAgent bool
}

type nameGen struct {
	name string
	gen  uint64
}

// Agents maps pane id → agent name, plus which panes already reported an exit.
//
// Replay phase: right after subscription_started the server replays its journal, and Herdr's
// close/status events carry no terminal id, so a replayed event for a reused pane id is
// indistinguishable from a live one. The host calls BeginReplay on subscription_started and
// EndReplay after a short quiet period, then reconciles with agent.list, which is the source
// of truth for what is alive and its status. While replaying, events for known panes are
// dropped. Not goroutine-safe (the host locks it).
type Agents struct {
	names  map[string]nameGen
	exited map[string]bool
	// last is the last status sent to subscribers, per pane (reconcile sends only differences).
	last map[string]agent.PaneStatus
	// seen: panes whose agent has been seen alive (listed with an agent detected, or a live status).
	seen map[string]bool
	seq  uint64
	// replaySince: when the current replay began (ms on the host's clock); replaying false
	// when not replaying.
	replaySince uint64
	replaying   bool
}

func NewAgents() *Agents {
	return &Agents{names: map[string]nameGen{}, exited: map[string]bool{}, last: map[string]agent.PaneStatus{}, seen: map[string]bool{}}
}

func (a *Agents) Insert(pane, name string) {
	a.seq++
	a.names[pane] = nameGen{name, a.seq}
	delete(a.exited, pane)
	delete(a.last, pane)
	delete(a.seen, pane)
}

// Generations is (pane, generation) of every known agent. A pane id reused by a new agent gets
// a new generation, so a Reconcile against an older snapshot leaves the new agent alone.
func (a *Agents) Generations() []Generation {
	out := make([]Generation, 0, len(a.names))
	for p, ng := range a.names {
		out = append(out, Generation{p, ng.gen})
	}
	return out
}

// MarkSeen: the agent on pane was seen alive by other means (the ready poll, agent.list).
func (a *Agents) MarkSeen(pane string) {
	if _, ok := a.names[pane]; ok {
		a.seen[pane] = true
	}
}

// Forget drops pane without reporting anything (an agent that never started).
func (a *Agents) Forget(pane string) {
	delete(a.names, pane)
	delete(a.exited, pane)
	delete(a.last, pane)
	delete(a.seen, pane)
}

// Replace records a new agent on pane. If the pane id still maps to an older agent, that pane
// must have closed (ids are only reused once free) even though we missed the close: the older
// agent is reported exited, unless that was already done.
func (a *Agents) Replace(pane, name string) (agent.PaneEvent, bool) {
	var gone agent.PaneEvent
	ok := false
	if _, known := a.names[pane]; known {
		gone, ok = a.live(RawEvent{Kind: RawClosed, Pane: pane})
	}
	a.Insert(pane, name)
	return gone, ok
}

// Reconcile after a replay: listed is from an agent.list asked after known was taken. A
// listed agent marks its pane seen alive and gets a Status if that differs from the last one
// sent. A known agent that was seen alive and is now missing, or whose pane no longer carries
// an agent, is reported exited (once). One never seen alive is still launching (Herdr lists it
// only once detected), so reconcile leaves it alone: a live close/exit or StartAgent's own
// ready timeout ends it instead.
func (a *Agents) Reconcile(known []Generation, listed []Listed) []agent.PaneEvent {
	var out []agent.PaneEvent
	for _, k := range known {
		if ng, ok := a.names[k.Pane]; !ok || ng.gen != k.Gen {
			continue
		}
		var entry *Listed
		for i := range listed {
			if listed[i].Pane == k.Pane {
				entry = &listed[i]
				break
			}
		}
		var ev agent.PaneEvent
		var some bool
		switch {
		case entry != nil && entry.HasAgent:
			a.seen[k.Pane] = true
			if last, ok := a.last[k.Pane]; !ok || last != entry.Status {
				ev, some = a.live(RawEvent{Kind: RawStatus, Pane: k.Pane, Status: entry.Status})
			}
		case !a.seen[k.Pane]:
		case entry != nil:
			ev, some = a.live(RawEvent{Kind: RawExited, Pane: k.Pane})
		default:
			ev, some = a.live(RawEvent{Kind: RawClosed, Pane: k.Pane})
		}
		if some {
			out = append(out, ev)
		}
	}
	return out
}

func (a *Agents) Panes() []string {
	out := make([]string, 0, len(a.names))
	for p := range a.names {
		out = append(out, p)
	}
	return out
}

// PaneOf is the pane of the most recently inserted agent with this name.
func (a *Agents) PaneOf(name string) (string, bool) {
	best, bestGen, found := "", uint64(0), false
	for p, ng := range a.names {
		if ng.name == name && (!found || ng.gen > bestGen) {
			best, bestGen, found = p, ng.gen, true
		}
	}
	return best, found
}

// BeginReplay: a (re)subscription started at nowMs; its journal replay follows.
func (a *Agents) BeginReplay(nowMs uint64) {
	a.replaySince, a.replaying = nowMs, true
}

// EndReplay: the replay is over (quiet period elapsed); events are live from here on. The
// host should call this after its quiet period and then reconcile with agent.list;
// TranslateAt only ends a forgotten replay after ReplayMaxMs as a safety net.
func (a *Agents) EndReplay() { a.replaying = false }

// TranslateAt is Translate, first ending a replay that has lasted ReplayMaxMs by nowMs.
func (a *Agents) TranslateAt(raw RawEvent, nowMs uint64) (agent.PaneEvent, bool) {
	if a.replaying && nowMs >= a.replaySince && nowMs-a.replaySince >= ReplayMaxMs {
		a.EndReplay()
	}
	return a.Translate(raw)
}

// Translate is the event for subscribers, if any. Events for panes we don't know (other
// panes, or replays of panes closed earlier) are dropped; an agent's exit is reported once.
func (a *Agents) Translate(raw RawEvent) (agent.PaneEvent, bool) {
	if _, known := a.names[raw.Pane]; a.replaying && known {
		return agent.PaneEvent{}, false
	}
	return a.live(raw)
}

// live is Translate without the replay check.
func (a *Agents) live(raw RawEvent) (agent.PaneEvent, bool) {
	ng, ok := a.names[raw.Pane]
	if !ok {
		return agent.PaneEvent{}, false
	}
	switch raw.Kind {
	case RawStatus:
		if a.exited[raw.Pane] {
			return agent.PaneEvent{}, false
		}
		a.seen[raw.Pane] = true
		a.last[raw.Pane] = raw.Status
		return agent.PaneEvent{Kind: agent.PaneEventStatus, Agent: ng.name, Status: raw.Status}, true
	case RawExited:
		if a.exited[raw.Pane] {
			return agent.PaneEvent{}, false
		}
		a.exited[raw.Pane] = true
		return agent.PaneEvent{Kind: agent.PaneEventExited, Agent: ng.name}, true
	}
	// RawClosed
	delete(a.names, raw.Pane)
	first := !a.exited[raw.Pane]
	delete(a.exited, raw.Pane)
	delete(a.last, raw.Pane)
	delete(a.seen, raw.Pane)
	if !first {
		return agent.PaneEvent{}, false
	}
	return agent.PaneEvent{Kind: agent.PaneEventExited, Agent: ng.name}, true
}
