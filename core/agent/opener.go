package agent

import "github.com/sihekuang/deskpixie/core/voice"

// Opening a conversation for an update (`agent.openConversationForUpdates`, a feature flag, off by
// default). With it on, a job's done or failed line, an agent question or a spoken worker
// report that arrives while no conversation is open still gets today's chime, notification
// and outbox line, and Pixie also opens a conversation the way the hotkey does. A coordinator
// progress reply (status working) opens none, so it gets no chime or notification either (#113):
// it waits in the outbox and the next conversation says it. An opened conversation replays
// the outbox (each line once, marked delivered), so Pixie says the update and then listens;
// it ends on the usual silence timeout.
//
// Worker reports are debounced first (debounce.go): held per job until quiet, deduped and
// coalesced, so a burst of reports is one line and that one line is what opens a
// conversation. Done, failed and question lines are never held and open one at once.
//
// One open at a time: updates that arrive while it is connecting land in the outbox and are
// replayed with the rest (a burst is one conversation). At most notifyPerMinute opens a
// minute. The voice loop refuses an open while a conversation is starting, open, closing or
// showing an error, or the mic could not start: the update stays in the outbox and the open
// is tried again on the next tick (or the next update) until a conversation opens. A failed
// connect is not retried until the next update.

// OpenWaitMs: an open the voice loop accepted but that never reported Opened or Closed is
// given up after this long.
const OpenWaitMs Millis = 30_000

type opener struct {
	perMinute int
	// opened is when each open in the current window was asked for.
	opened []Millis
	// waiting: an open was asked for and has not reported Opened or Closed yet.
	waiting optMs
	// retry: an update is waiting in the outbox for an open that was refused or capped.
	retry bool
}

func newOpener(perMinute int) *opener { return &opener{perMinute: max(perMinute, 1)} }

// deliverOpening delivers a line the user should hear about now (a job's done or failed line,
// a question: never debounced), then opens a conversation for it if none is open (flag on).
// Returns deliver's answer.
func (c *AgentController) deliverOpening(line Line, now Millis, fx *[]Effect) bool {
	reached := c.deliver(line, now, fx)
	c.openForUpdate(now, fx)
	return reached
}

// openForUpdate: a line worth a conversation was just delivered (deliverOpening, or a worker
// report once Updates let it out, deliverUpdate); open one if none is open (flag on). The
// opener's cap is its own: the Router's per-minute slots never hold back an open, and the
// Router never holds back a question, so a question is never stopped by both.
func (c *AgentController) openForUpdate(now Millis, fx *[]Effect) {
	if c.settings.OpenConversation && !c.voiceOpen {
		c.opener.retry = true
		c.tryOpen(now, fx)
	}
}

// tryOpen asks the voice loop to open a conversation, unless one is open or being opened, the
// cap is reached or nothing is left to say.
func (c *AgentController) tryOpen(now Millis, fx *[]Effect) {
	o := c.opener
	if o.waiting.set && satSub(now, o.waiting.v) >= OpenWaitMs {
		o.waiting = optMs{}
		o.retry = true
	}
	if c.away {
		return
	}
	if !c.settings.OpenConversation || !o.retry || c.shutDown || o.waiting.set {
		return
	}
	if c.voiceOpen || len(c.outbox.Undelivered()) == 0 {
		o.retry = false
		return
	}
	for len(o.opened) > 0 && satSub(now, o.opened[0]) >= NotifyWindowMs {
		o.opened = o.opened[1:]
	}
	if len(o.opened) >= o.perMinute {
		return
	}
	o.opened = append(o.opened, now)
	o.waiting = someMs(now)
	o.retry = false
	*fx = append(*fx, Effect{Kind: EfOpenConversation})
}

// setAway (InPresence): while you're away Pixie doesn't open a conversation or chime (the
// notification and the outbox line still happen); on return it tries again.
func (c *AgentController) setAway(away bool, now Millis, fx *[]Effect) {
	c.away = away
	if !away {
		c.tryOpen(now, fx)
	}
}

// openRefused (InOpenRefused): the voice loop was busy; the open does not count against the
// cap and is tried again on the next tick.
func (c *AgentController) openRefused(now Millis, fx *[]Effect) {
	o := c.opener
	if !o.waiting.set {
		return
	}
	o.waiting = optMs{}
	if n := len(o.opened); n > 0 {
		o.opened = o.opened[:n-1]
	}
	o.retry = true
}

// openerSaw is told of the voice loop's Opened and Closed: whichever comes ends the wait.
// Opened replays the outbox; a Closed while waiting is a failed connect, not retried until the
// next update. A Closed with no open asked for (the conversation that made Pixie busy ended)
// leaves a refused open to the next tick.
func (c *AgentController) openerSaw(kind voice.SignalKind) {
	o := c.opener
	if kind == voice.SigOpened || o.waiting.set {
		o.retry = false
	}
	o.waiting = optMs{}
}
