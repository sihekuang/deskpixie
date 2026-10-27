package agent

import "slices"

// reask is the question Pixie relayed last, as it was said (#382). When the conversation it
// was said in closes before you say anything (it dropped, or went quiet), the next one says it
// again, once, while it still waits: a new conversation doesn't know it, so your answer would
// go out as a new request instead of to whoever asked.
type reask struct {
	at   Millis // the asker's at: it is still c.asked's question while they match
	line Line
	// said: it reached a conversation (injected, not bounced back unsaid); heard: you said
	// something after it there; due: that conversation closed first; again: said again once.
	said, heard, due, again bool
}

func (r reask) in(lines []Line) bool {
	return r.at != 0 && slices.ContainsFunc(lines, func(l Line) bool { return l.Text == r.line.Text })
}

// reaskInjected: a notice carrying lines goes to the conversation.
func (c *AgentController) reaskInjected(lines []Line) {
	if c.reask.in(lines) {
		c.reask.said, c.reask.heard = true, false
	}
}

// reaskBounced: a notice carrying lines came back unsaid.
func (c *AgentController) reaskBounced(lines []Line) {
	if c.reask.in(lines) {
		c.reask.said = false
	}
}

// reaskHeard: you said something in the conversation.
func (c *AgentController) reaskHeard() {
	if c.reask.said {
		c.reask.heard = true
	}
}

// reaskClosed: the conversation closed (after its unsaid notices went back).
func (c *AgentController) reaskClosed() {
	if c.reask.said && !c.reask.heard && !c.reask.again {
		c.reask.due = true
	}
	c.reask.said = false
}

// reaskOpened: a conversation opened; the question it is due goes in again if it still waits.
func (c *AgentController) reaskOpened(fx *[]Effect) {
	if !c.reask.due {
		return
	}
	c.reask.due = false
	if !c.reaskWaiting() {
		return
	}
	c.reask.again = true
	l := c.reask.line
	n := c.remember(Notice{Text: "Coordinator replies while you were away:\n- " + l.Kind.Prefix() + l.Text}, nil)
	n.Sessions = sessionsOf([]Line{l})
	c.reask.said, c.reask.heard = true, false
	*fx = append(*fx, Effect{Kind: EfInject, Notice: n})
}

// reaskWaiting: the question to say again is still c.asked's, and still waits for you.
func (c *AgentController) reaskWaiting() bool {
	a := c.asked
	if a.at != c.reask.at || a.tie {
		return false
	}
	switch {
	case a.menu != 0:
		return c.menuAsked() != nil
	case a.session != "":
		// Still asking: answered in its terminal, it went to work and asks no more.
		_, ok := c.asking[a.session]
		return ok
	case a.job != "":
		j, ok := c.jobs.Get(a.job)
		return ok && j.Status == JobQuestion
	}
	return false
}
