package agent

import (
	"fmt"
	"reflect"
	"regexp"
)

// The updates bubble (spec docs/specs/2026-10-14-updates-bubble-design.md): what needs you
// (#286): permission questions and menus still waiting (their Allow and Deny, #97), job
// questions, and sessions asking something. Everything else is a reply, not a bubble line.

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
	// Multi: you pick several of its options and send them with agent.pick (#364).
	Multi bool `json:"multi,omitempty"`
	// Note: shown small under the line, never said (a session dialog's command, #105).
	Note string `json:"note,omitempty"`
}

// alerting: a line that needs you to act (#286): a question (permission, menu or job) or a
// session asking something. The rest are replies.
func alerting(l OutboxLine) bool {
	return l.Kind == LineQuestion || (l.Kind == LineSession && l.Asking)
}

// asksSession: a session's own question with no confirm word or job (a turn that asks, a
// notice, a menu without options): it waits until that session's next turn or end.
func asksSession(l OutboxLine) bool {
	return l.Session != "" && l.Token == nil && (l.Asking || (l.Kind == LineQuestion && l.Job == ""))
}

// alertKey: what an alerting line asks about; one badge item each, the newest line (a question
// said, then rerouted unsaid, is in the outbox twice).
func alertKey(l OutboxLine) string {
	switch {
	case l.Token != nil:
		return "token:" + *l.Token
	case l.Job != "":
		return "job:" + string(l.Job)
	case l.Session != "":
		return "session:" + l.Session
	}
	return fmt.Sprint("line:", l.ID)
}

// unresolved: an alerting line still waiting for you: its permission pending, its session's
// question still the latest thing it said, its job still in question.
func (c *AgentController) unresolved(l OutboxLine) bool {
	switch {
	case l.Token != nil:
		return c.confirms.IsPendingToken(*l.Token)
	case asksSession(l):
		return c.asking[l.Session] == l.ID
	case l.Kind == LineQuestion && l.Job != "":
		j, ok := c.jobs.Get(l.Job)
		return ok && j.Status == JobQuestion
	}
	return !l.Delivered
}

// keepHeardAlert: an alerting line said in a conversation or shown in the text box never
// reaches the outbox; it is kept there as heard, so it waits on the badge until answered and a
// later conversation doesn't say it again (#286).
func (c *AgentController) keepHeardAlert(line Line, now Millis, fx *[]Effect) {
	if !alerting(OutboxLine{Kind: line.Kind, Asking: line.Asking}) {
		return
	}
	*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: c.outbox.PushHeard(line, now)})
	c.noteAsking(c.outbox.lines[len(c.outbox.lines)-1])
}

// noteAsking: l, just added to the outbox, is its session's open question if it asks.
func (c *AgentController) noteAsking(l OutboxLine) {
	if asksSession(l) {
		c.asking[l.Session] = l.ID
	}
}

// dropAsking: session took its next turn or ended, so its open question is answered: it leaves
// the badge and is marked delivered, so a later conversation doesn't replay it.
func (c *AgentController) dropAsking(session string, fx *[]Effect) {
	id, ok := c.asking[session]
	if !ok {
		return
	}
	delete(c.asking, session)
	for _, r := range c.outbox.MarkDeliveredWhere(func(l OutboxLine) bool { return l.ID == id }) {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: r})
	}
}

// bubbleItems: the alerting lines still unresolved: permission questions pending (heard or
// not, so Allow and Deny stay reachable, #97), job questions, sessions asking; one per
// alertKey. Never nil.
func (c *AgentController) bubbleItems() []UpdateItem {
	items := []UpdateItem{}
	if c.shutDown {
		return items
	}
	newest := map[string]uint64{}
	for _, l := range c.outbox.lines {
		if alerting(l) {
			newest[alertKey(l)] = l.ID
		}
	}
	for _, l := range c.outbox.lines {
		if !alerting(l) || !c.unresolved(l) || newest[alertKey(l)] != l.ID {
			continue
		}
		item := updateItem(l)
		if l.Token != nil {
			if item.Brief == "" {
				item.Brief = QuestionBrief(l.Text)
			}
			item.Token = *l.Token
			if p, ok := c.confirms.PendingOf(*l.Token); ok {
				item.Options, item.Details, item.Typed, item.Multi = p.Options, p.Details, p.Typed, p.Multi
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

// updatesSeen: lines were seen in the bubble: they count as heard, except an alerting line
// still unresolved, clicked or not (#286). A reply line falls through as before.
func (c *AgentController) updatesSeen(ids []uint64, clicked bool, fx *[]Effect) {
	keep := func(l OutboxLine) bool { return alerting(l) && c.unresolved(l) }
	before := c.outbox.undelivered()
	for _, r := range c.outbox.MarkSeen(ids, keep) {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: r})
	}
	c.heard(before, fx)
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
