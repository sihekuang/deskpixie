package agent

import "reflect"

// The updates bubble (spec docs/specs/2026-10-14-updates-bubble-design.md): the outbox's
// undelivered lines, shown beside Pixie while Pixie isn't opening conversations for them, and
// permission questions still waiting, always (their Allow and Deny, #97).

// UpdateItem is one line in the bubble (the `updates` event's lines; kind lowercase, at in ms).
type UpdateItem struct {
	ID   uint64   `json:"id"`
	Text string   `json:"text"`
	Kind LineKind `json:"kind"`
	At   Millis   `json:"at"`
	// Job and Worker: what the line is about (a click focuses that pane).
	Job    JobID  `json:"job,omitempty"`
	Worker string `json:"worker,omitempty"`
	// Token is the confirm word of a permission question still waiting; the bubble's Allow and
	// Deny buttons answer with it (#97). Empty for every other line.
	Token string `json:"token,omitempty"`
	// App: a session's app (bundle id); a click activates it instead of a pane (#105).
	App string `json:"app,omitempty"`
	// Options: a session menu's choices (#105): the bubble shows one button each instead of
	// Allow and Deny, answered with agent.choose.
	Options []string `json:"options,omitempty"`
}

// bubbleItems: the undelivered lines, minus permission questions no longer pending, while Pixie
// isn't opening conversations for updates; and always every permission question still pending,
// heard or not, so its Allow and Deny stay reachable (#97). Never nil.
func (c *AgentController) bubbleItems() []UpdateItem {
	items := []UpdateItem{}
	if c.shutDown {
		return items
	}
	for _, l := range c.outbox.lines {
		pending := l.Token != nil && c.confirms.IsPendingToken(*l.Token)
		if !pending && (l.Token != nil || l.Delivered || c.settings.OpenConversation) {
			continue
		}
		item := UpdateItem{ID: l.ID, Text: l.Text, Kind: l.Kind, At: l.At, Job: l.Job, Worker: l.Worker, App: l.App}
		if l.Token != nil {
			item.Token = *l.Token
			item.Options = c.confirms.OptionsOf(*l.Token)
		}
		items = append(items, item)
	}
	return items
}

// Updates is the bubble's current list, for a shell that missed the last `updates` event (it
// subscribes after the first Step). Never nil.
func (c *AgentController) Updates() []UpdateItem { return c.bubbleItems() }

// syncUpdates sends the bubble its list when it changed (every Step ends here). The first
// Step always sends one, so the shell starts from the current list.
func (c *AgentController) syncUpdates(fx *[]Effect) {
	if c.shutDown {
		return
	}
	items := c.bubbleItems()
	if c.bubbleSent && reflect.DeepEqual(items, c.bubbleLast) {
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

// dropDecidedQuestions: a job question (no permission token) whose job has left Question (it
// was answered in the pane, moved on, or ended) is no longer waiting for you: it is marked
// delivered so the bubble and the next conversation drop it. Permission questions keep their
// token rule.
func (c *AgentController) dropDecidedQuestions(fx *[]Effect) {
	decided := func(l OutboxLine) bool {
		if l.Kind != LineQuestion || l.Token != nil || l.Job == "" {
			return false
		}
		j, ok := c.jobs.Get(l.Job)
		return !ok || j.Status != JobQuestion
	}
	for _, r := range c.outbox.MarkDeliveredWhere(decided) {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: r})
	}
}

// WorkerRows are the workers of open jobs and any still running (list_workers' rows).
func (c *AgentController) WorkerRows() []WorkerRow { return c.jobs.WorkerRows() }

// OpenTarget is the pane a click on Pixie focuses (#110): the worker waiting at a prompt in its
// terminal if its pane is up, else the coordinator.
func (c *AgentController) OpenTarget() string {
	if w := c.workers.Get(c.workers.Waiting(c.confirms.HasPendingFor)); w != nil && w.IsUp() {
		return w.Plan.Name
	}
	return Coordinator
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
