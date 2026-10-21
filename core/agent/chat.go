package agent

import (
	"errors"
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
	return ChatLine{Job: line.Job, Status: status, Text: line.Text}
}

// typedNote tells the coordinator where the request came from.
const typedNote = "The user typed this in DeskPixie's text box."

// errEmptyChat: nothing to send.
var errEmptyChat = errors.New("Type a message first.")

// ChatSend is a message typed in the text box: the answer to the last typed job's question
// while it waits for one, else a new typed job (a delegation, with the same reasons to refuse).
func (h *AgentHandle) ChatSend(text string) (JobID, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errEmptyChat
	}
	h.ctlMu.Lock()
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
