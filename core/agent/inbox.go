package agent

import (
	"slices"
	"strings"
)

// The coordinator's inbox (port of inbox.rs): the app never types into a working coordinator.
// Messages queue here and go out as one prompt once the coordinator is idle.

// Message is one queued message; Text ends with its own `[ref job_…]` line when it is about a job.
type Message struct {
	Text string
	Refs []JobID
}

// MessageForJob is `<text>\n[ref <job>]`.
func MessageForJob(job JobID, text string) Message {
	return Message{Text: text + "\n[ref " + string(job) + "]", Refs: []JobID{job}}
}

func (m Message) refersTo(job JobID) bool { return slices.Contains(m.Refs, job) }

// Prompt is what goes to the coordinator in one turn.
type Prompt struct {
	Text string
	Refs []JobID
}

// Inbox holds queued and in-flight messages.
type Inbox struct {
	queue    []Message
	inFlight []Message
	// ready: the coordinator is idle (or done) and nothing has been sent since.
	ready bool
}

func NewInbox() *Inbox { return &Inbox{} }

// Clone is a deep copy (Rust derives Clone).
func (i *Inbox) Clone() *Inbox {
	return &Inbox{queue: slices.Clone(i.queue), inFlight: slices.Clone(i.inFlight), ready: i.ready}
}

func (i *Inbox) Push(msg Message) { i.queue = append(i.queue, msg) }

func (i *Inbox) HasQueued() bool { return len(i.queue) > 0 }

// OnStatus takes the coordinator's latest status. Idle/Done make it ready; Working/Blocked don't.
func (i *Inbox) OnStatus(status PaneStatus) {
	switch status {
	case PaneIdle, PaneDone:
		i.ready = true
	case PaneWorking, PaneBlocked:
		i.ready = false
	}
}

// TakePrompt returns the whole queue as one prompt (messages separated by a blank line), if
// the coordinator is ready. Afterwards it counts as busy until the next Idle/Done.
func (i *Inbox) TakePrompt() (Prompt, bool) {
	if !i.ready || len(i.queue) == 0 {
		return Prompt{}, false
	}
	i.ready = false
	sent := i.queue
	i.queue = nil
	texts := make([]string, len(sent))
	var refs []JobID
	for k, m := range sent {
		texts[k] = m.Text
		refs = append(refs, m.Refs...)
	}
	i.inFlight = append(i.inFlight, sent...)
	return Prompt{Text: strings.Join(texts, "\n\n"), Refs: refs}, true
}

// InFlightRefs are the refs carried by the prompt the coordinator is working on.
func (i *Inbox) InFlightRefs() []JobID {
	var out []JobID
	for _, m := range i.inFlight {
		out = append(out, m.Refs...)
	}
	return out
}

// Queued: a message about job is waiting to be sent.
func (i *Inbox) Queued(job JobID) bool {
	return slices.ContainsFunc(i.queue, func(m Message) bool { return m.refersTo(job) })
}

func (i *Inbox) Carries(job JobID) bool {
	return slices.ContainsFunc(i.inFlight, func(m Message) bool { return m.refersTo(job) })
}

// FinishTurn: the coordinator finished a turn (its Stop hook fired). Readiness is left to the
// controller: an Idle/Done, or a short settle after the Stop (a short turn's Idle can be missed).
func (i *Inbox) FinishTurn() { i.inFlight = nil }

// MarkReady: the coordinator can take the next prompt (the controller's timers decided so).
func (i *Inbox) MarkReady() { i.ready = true }

// CoordinatorLost: the coordinator pane is gone: what it was working on goes back to the front
// of the queue for its replacement, and nothing is sent until the new one reports idle.
func (i *Inbox) CoordinatorLost() {
	i.RequeueInFlight()
	i.ready = false
}

// RequeueInFlight: a prompt never reached the coordinator: what was in flight goes back to the
// front of the queue, in order. Readiness is unchanged.
func (i *Inbox) RequeueInFlight() {
	again := i.inFlight
	i.inFlight = nil
	i.queue = append(again, i.queue...)
}

// DropInFlight forgets in-flight messages about job (its job ended without the coordinator answering).
func (i *Inbox) DropInFlight(job JobID) {
	i.inFlight = slices.DeleteFunc(i.inFlight, func(m Message) bool { return m.refersTo(job) })
}

// RequeueInFlightExcept: the turn carrying job was interrupted: every other in-flight message
// goes back to the end of the queue to be sent again; only job's stays in flight.
func (i *Inbox) RequeueInFlightExcept(job JobID) {
	var mine, others []Message
	for _, m := range i.inFlight {
		if m.refersTo(job) {
			mine = append(mine, m)
		} else {
			others = append(others, m)
		}
	}
	i.inFlight = mine
	i.queue = append(i.queue, others...)
}

// DropQueued drops queued (not yet sent) messages about job; true if any were dropped.
func (i *Inbox) DropQueued(job JobID) bool {
	before := len(i.queue)
	i.queue = slices.DeleteFunc(i.queue, func(m Message) bool { return m.refersTo(job) })
	return len(i.queue) != before
}
