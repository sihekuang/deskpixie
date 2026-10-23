package agent

import (
	"reflect"
	"regexp"
)

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
	// App: a session line's app (bundle id); a click activates it instead of a pane.
	App string `json:"app,omitempty"`
	// Session: the session a session line is about; a click brings up its tab (#194).
	Session string `json:"session,omitempty"`
	// Title and Brief: the short form the bubble shows instead of Text when set (#197): who the
	// line is about, on its own line, and the news. Asking: it asks you something.
	Title  string `json:"title,omitempty"`
	Brief  string `json:"brief,omitempty"`
	Asking bool   `json:"asking,omitempty"`
	// Options: a session menu's choices (#105): the bubble shows one button each instead of
	// Allow and Deny, answered with agent.choose.
	Options []string `json:"options,omitempty"`
	// Details and Typed (#268): each option's description, and that the menu also takes a
	// typed answer (agent.typedAnswer) and "Chat about this" (agent.chatAbout).
	Details []string `json:"details,omitempty"`
	Typed   bool     `json:"typed,omitempty"`
	// Note: shown small under the line, never said (a session dialog's command, #105).
	Note string `json:"note,omitempty"`
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
		item := updateItem(l)
		if l.Token != nil {
			if item.Brief == "" {
				item.Brief = QuestionBrief(l.Text)
			}
			item.Token = *l.Token
			if p, ok := c.confirms.PendingOf(*l.Token); ok {
				item.Options, item.Details, item.Typed = p.Options, p.Details, p.Typed
			}
		}
		items = append(items, item)
	}
	return items
}

func updateItem(l OutboxLine) UpdateItem {
	return UpdateItem{ID: l.ID, Text: l.Text, Kind: l.Kind, At: l.At, Job: l.Job, Worker: l.Worker, App: l.App,
		Session: l.Session, Title: l.Title, Brief: l.Brief, Asking: l.Asking, Note: l.Note}
}

// attachNotification: line was just added to the outbox; the notification Route sent for it
// in this step gets its bubble line, so a click on it does what a click on the line does
// (#199). It relies on Route's order: the Notify comes before the Outbox delivery of the same
// line, and each earlier one was attached at its own push (Update set).
func (c *AgentController) attachNotification(fx []Effect, line Line) {
	n := len(c.outbox.lines)
	if n == 0 {
		return
	}
	for i := len(fx) - 1; i >= 0; i-- {
		if fx[i].Kind == EfNotify && fx[i].Update == nil && fx[i].Text == line.Text {
			item := updateItem(c.outbox.lines[n-1])
			fx[i].Update = &item
			return
		}
	}
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

// updatesSeen: lines were seen in the bubble: they count as heard, except a question still
// waiting for its answer (a pending permission, or a job still in question). Clicked away by
// the user (#199), a job's question goes too: only a pending permission keeps its buttons.
func (c *AgentController) updatesSeen(ids []uint64, clicked bool, fx *[]Effect) {
	keep := func(l OutboxLine) bool {
		if l.Kind != LineQuestion {
			return false
		}
		if l.Token != nil {
			return c.confirms.IsPendingToken(*l.Token)
		}
		if clicked {
			return false
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

// bubbleHow is how a coordinator question says to answer it aloud or in the bubble: the
// bubble's own buttons make it noise there (#197).
var bubbleHow = regexp.MustCompile(`\s*(Say confirm \S+(, or choose in the bubble)?\.|Choose in the bubble\.|Allow or deny it in the bubble\.)\s*$`)

// QuestionBrief is the bubble's short form of a question with buttons (#197): without how to
// answer it. "" when there is nothing to leave out (the bubble shows the text).
func QuestionBrief(text string) string {
	brief := bubbleHow.ReplaceAllString(text, "")
	if brief == text || brief == "" {
		return ""
	}
	return upperFirst(brief)
}
