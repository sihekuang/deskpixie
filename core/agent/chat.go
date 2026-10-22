package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Text mode (#221): a message typed in the text box under Pixie is a job like a delegation,
// and the coordinator's spoken lines for it go to the box instead of being spoken.

// ChatLine is one spoken line for a typed job, as the text box shows it.
type ChatLine struct {
	Job JobID `json:"job"`
	// Status: working | question | done | failed.
	Status string `json:"status"`
	Text   string `json:"text"`
	// Sessions: the sessions the line is about, for the card's buttons that go to them (#261).
	Sessions []string `json:"sessions,omitempty"`
}

// ChatSink is the text box: it gets each line for a typed job.
type ChatSink interface {
	Chat(ChatLine)
}

// LastTyped is the newest job typed in the text box ("" = none).
func (c *AgentController) LastTyped() JobID { return c.lastTyped }

// chatLine is line for the box: a question as asked, else the job's state.
func (c *AgentController) chatLine(line Line) ChatLine {
	status := "done"
	if line.Kind == LineQuestion {
		status = "question"
	} else if j, ok := c.jobs.Get(line.Job); ok {
		switch j.Status {
		case JobWorking, JobDelegated:
			status = "working"
		case JobQuestion:
			status = "question"
		case JobFailed, JobCancelled:
			status = "failed"
		}
	}
	text := line.Text
	if line.Token != nil {
		// A permission request: allowed from the bubble or the notification, not by a spoken word.
		text = strings.TrimSpace(strings.TrimSuffix(text, fmt.Sprintf("Say confirm %s.", *line.Token))) + " " + permissionHint
	}
	return ChatLine{Job: line.Job, Status: status, Text: text, Sessions: c.lineSessions(line)}
}

// permissionHint ends a permission request in the text box.
const permissionHint = "Allow or deny it in Pixie's updates."

// typedNote tells the coordinator where the request came from.
const typedNote = "The user typed this in DeskPixie's text box."

// errEmptyChat: nothing to send.
var errEmptyChat = errors.New("Type a message first.")

// ChatSend is a message typed in the text box: the answer to the last typed job's question
// while it waits for one, else a new typed job (a delegation, with the same reasons to refuse).
func (h *AgentHandle) ChatSend(text string) (JobID, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		if !h.hasAttachments() {
			return "", errEmptyChat
		}
		text = attachedOnly
	}
	h.ctlMu.Lock()
	if session, job := h.ctl.takeBoxAnswer(); session != "" {
		// #241: the box shows that session's question: the words are its answer.
		h.ctlMu.Unlock()
		_, _, err := h.AnswerAsked("", text)
		return job, err
	}
	last := h.ctl.LastTyped()
	j, ok := h.ctl.jobs.Get(last)
	waiting := last != "" && ok && j.Status == JobQuestion && h.ctl.jobs.IsOpen(last)
	h.ctlMu.Unlock()
	if waiting {
		id, _, err := h.AnswerAsked(string(last), text)
		return id, err
	}
	return h.delegate(text, typedNote, true)
}

// ChatStop is Stop in the text box: cancel the last typed job if it is still open.
func (h *AgentHandle) ChatStop() {
	h.ctlMu.Lock()
	last := h.ctl.LastTyped()
	open := last != "" && h.ctl.jobs.IsOpen(last)
	h.ctlMu.Unlock()
	if open {
		_, _ = h.Cancel(string(last))
	}
}

// SetTextMode: the user switched to text (on) or back to voice.
func (h *AgentHandle) SetTextMode(on bool) { h.Input(TextModeInput(on)) }

// onlyStoppedTyped: every job in the prompt in flight is a typed one that is closed (stopped).
func (c *AgentController) onlyStoppedTyped() bool {
	refs := unique(c.inbox.InFlightRefs())
	for _, id := range refs {
		if !c.typed[id] || c.jobs.IsOpen(id) {
			return false
		}
	}
	return len(refs) > 0
}

// deliverUnsolicited delivers a line that names no open job, unless it only confirms a typed
// job's Stop: the box already says "Stopped." (#221).
func (c *AgentController) deliverUnsolicited(text string, now Millis, fx *[]Effect) {
	if c.turnStopped {
		return
	}
	c.deliver(NewLine(LineUnsolicited, text), now, fx)
}

// lineSessions: the sessions a typed job's line is about (#261): those the job was passed to,
// and the one the line itself is from.
func (c *AgentController) lineSessions(line Line) []string {
	out := slices.Clone(c.jobSessions[line.Job])
	if line.Session != "" && !slices.Contains(out, line.Session) {
		out = append(out, line.Session)
	}
	return out
}

// sessionsOf: the sessions lines are about, each once, in order (#261).
func sessionsOf(lines []Line) []string {
	var out []string
	for _, l := range lines {
		if l.Session != "" && !slices.Contains(out, l.Session) {
			out = append(out, l.Session)
		}
	}
	return out
}
