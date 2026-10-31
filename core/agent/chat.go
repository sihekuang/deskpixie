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
	// Asking: the line is the question itself (a job's question or a permission request, or a
	// session asking in the box), which waits on the badge as a request too (#302). A status of
	// question alone only says the job is waiting.
	Asking bool `json:"asking,omitempty"`
	// Session: the line is that session's own turn, the answer to a message typed to it (#241),
	// not the coordinator's ("" = the coordinator's).
	Session string `json:"session,omitempty"`
}

// ChatSink is the text box: it gets each line for a typed job.
type ChatSink interface {
	Chat(ChatLine)
}

// ChatEnder is a ChatSink that is also told when a typed job ends with no line to say so
// (Stop), so what it shows of the job's work in progress can go (#302).
type ChatEnder interface {
	ChatEnded(JobID)
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
	return ChatLine{Job: line.Job, Status: status, Text: text, Sessions: c.lineSessions(line), Asking: line.Kind == LineQuestion}
}

// permissionHint ends a permission request in the text box.
const permissionHint = "Allow or deny it in Pixie's updates."

// typedNote tells the coordinator where the request came from.
const typedNote = "The user typed this in DeskPixie's text box."

// errEmptyChat: nothing to send.
var errEmptyChat = errors.New("Type a message first.")

// ChatSent is where a message typed in the text box went (#302): its job, and whether it
// answered a question waiting for one: the job's own, or (Session set) that session's.
// Attached are the attachments it took, as handed over (#394).
type ChatSent struct {
	Job      JobID
	Answered bool
	Session  string
	Attached []string
}

// ChatSend is a message typed in the text box: the answer to the last typed job's question
// while it waits for one, else a new typed job (a delegation, with the same reasons to refuse).
func (h *AgentHandle) ChatSend(text string) (JobID, error) {
	s, err := h.ChatMessage(text)
	return s.Job, err
}

// ChatMessage is ChatSend that also says what the message answered.
func (h *AgentHandle) ChatMessage(text string) (ChatSent, error) { return h.message(text, inBox) }

// WindowMessage is ChatMessage for a message sent from the chat window (#414): it answers only
// the window's own last request, never what the box under Pixie shows, and leaves the box be.
func (h *AgentHandle) WindowMessage(text string) (ChatSent, error) { return h.message(text, inWindow) }

func (h *AgentHandle) message(text string, from sentFrom) (ChatSent, error) {
	text, err := h.chatText(text)
	if err != nil {
		return ChatSent{}, err
	}
	h.ctlMu.Lock()
	if from == inBox {
		if session, job := h.ctl.takeBoxAnswer(); session != "" {
			// #241: the box shows that session's question: the words are its answer.
			h.ctlMu.Unlock()
			_, where, paths, err := h.answerAsked("", text, true)
			return ChatSent{Job: job, Answered: err == nil && where != toNewRequest, Session: session, Attached: paths}, err
		}
	}
	last := h.ctl.LastTyped()
	if from == inWindow {
		last = h.ctl.lastWindow
	}
	j, ok := h.ctl.jobs.Get(last)
	waiting := last != "" && ok && j.Status == JobQuestion && h.ctl.jobs.IsOpen(last)
	h.ctlMu.Unlock()
	if waiting {
		id, where, paths, err := h.answerAsked(string(last), text, true)
		return ChatSent{Job: id, Answered: err == nil && where != toNewRequest, Attached: paths}, err
	}
	id, paths, err := h.delegate(text, typedNote, from)
	return ChatSent{Job: id, Attached: paths}, err
}

// AnswerJob is Reply… on a job's question in the chat window (#422): the answer to that job's
// question while it waits for one, however the job was asked for. False when it doesn't wait
// (answered, ended, unknown): nothing is sent.
func (h *AgentHandle) AnswerJob(job, text string) (ChatSent, bool, error) {
	id, ok := ParseJobID(strings.TrimSpace(job))
	if !ok {
		return ChatSent{}, false, nil
	}
	text, err := h.chatText(text)
	if err != nil {
		return ChatSent{}, false, err
	}
	if h.quitting() {
		return ChatSent{}, false, errQuitting
	}
	// Checked and answered in one step: a job that stopped waiting meanwhile takes nothing.
	paths := h.take()
	r := h.stepLocked(JobAnswerInput(id, text+attachedLines(paths)))
	switch {
	case r == nil || r.Kind != EfAnswerResult:
		h.giveBack(paths)
		return ChatSent{}, true, errors.New("The answer was not processed.")
	case r.Err == jobNotWaiting:
		h.giveBack(paths)
		return ChatSent{}, false, nil
	case r.Err != "":
		h.giveBack(paths)
		return ChatSent{}, true, errors.New(r.Err)
	}
	return ChatSent{Job: r.Job, Answered: true, Attached: paths}, true, nil
}

// ChatDelegate is a message sent from the chat window to one session, told through the
// coordinator (#302): always a new job, never an answer to a waiting question.
func (h *AgentHandle) ChatDelegate(text string) (ChatSent, error) {
	text, err := h.chatText(text)
	if err != nil {
		return ChatSent{}, err
	}
	id, paths, err := h.delegate(text, typedNote, inWindow)
	return ChatSent{Job: id, Attached: paths}, err
}

// chatText is a typed message trimmed, or what stands for it when only attachments go.
func (h *AgentHandle) chatText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		if !h.hasAttachments() {
			return "", errEmptyChat
		}
		text = attachedOnly
	}
	return text, nil
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
