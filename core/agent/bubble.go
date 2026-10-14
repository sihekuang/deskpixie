package agent

import "slices"

// The updates bubble (spec docs/specs/2026-10-14-updates-bubble-design.md): the outbox's
// undelivered lines, shown beside Bit while Bit isn't opening conversations for them.

// UpdateItem is one line in the bubble (the `updates` event's lines; kind lowercase, at in ms).
type UpdateItem struct {
	ID   uint64   `json:"id"`
	Text string   `json:"text"`
	Kind LineKind `json:"kind"`
	At   Millis   `json:"at"`
	// Job and Worker: what the line is about (a click focuses that pane).
	Job    JobID  `json:"job,omitempty"`
	Worker string `json:"worker,omitempty"`
}

// bubbleItems: the undelivered lines, minus permission questions no longer pending. Never nil.
func (c *AgentController) bubbleItems() []UpdateItem {
	items := []UpdateItem{}
	for _, l := range c.outbox.Undelivered() {
		if l.Token != nil && !c.confirms.IsPendingToken(*l.Token) {
			continue
		}
		items = append(items, UpdateItem{ID: l.ID, Text: l.Text, Kind: l.Kind, At: l.At, Job: l.Job, Worker: l.Worker})
	}
	return items
}

// Updates is the bubble's current list, for a shell that missed the last `updates` event (it
// subscribes after the first Step). Empty while Bit opens conversations for updates. Never nil.
func (c *AgentController) Updates() []UpdateItem {
	if c.settings.OpenConversation || c.shutDown {
		return []UpdateItem{}
	}
	return c.bubbleItems()
}

// syncUpdates sends the bubble its list when it changed (every Step ends here). The first
// Step always sends one, so the shell starts from the current list.
func (c *AgentController) syncUpdates(fx *[]Effect) {
	if c.settings.OpenConversation || c.shutDown {
		return
	}
	items := c.bubbleItems()
	if c.bubbleSent && slices.Equal(items, c.bubbleLast) {
		return
	}
	c.bubbleSent, c.bubbleLast = true, items
	*fx = append(*fx, Effect{Kind: EfUpdates, Value: items})
}

// updatesSeen: the bubble was looked at: those lines count as heard, except a question still
// waiting for its answer (a pending permission, or a job still in question).
func (c *AgentController) updatesSeen(ids []uint64, fx *[]Effect) {
	keep := func(l OutboxLine) bool {
		if l.Kind != LineQuestion {
			return false
		}
		if l.Token != nil {
			return c.confirms.IsPendingToken(*l.Token)
		}
		j, ok := c.jobs.Get(l.Job)
		return ok && j.Status == JobQuestion
	}
	for _, r := range c.outbox.MarkSeen(ids, keep) {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: r})
	}
}

// FocusTarget is the pane a click on a bubble line focuses: the line's worker if its pane is
// up, else the job's first worker whose pane is up, else the coordinator.
func (c *AgentController) FocusTarget(job JobID, worker string) string {
	if w := c.workers.Get(worker); worker != "" && w != nil && w.IsUp() {
		return worker
	}
	if job != "" {
		for _, name := range c.workers.ForJob(job) {
			if c.workers.Get(name).IsUp() {
				return name
			}
		}
	}
	return Coordinator
}
