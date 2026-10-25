package agent

import "slices"

// ReplyJoinMs: updates that come within this of the last one share a card.
const ReplyJoinMs Millis = 5_000

// ReplyCard is a card in the reply deck (#286): one update, or several that came within
// ReplyJoinMs, a row each.
type ReplyCard struct {
	ID       uint64   `json:"id"`    // its first line's outbox id
	Text     string   `json:"text"`  // the rows, one per line
	Count    int      `json:"count"` // how many rows
	Lines    []uint64 `json:"lines"` // outbox ids, for agent.repliesShown
	Sessions []string `json:"sessions,omitempty"`
	At       Millis   `json:"at"`
}

// ReplySink gets each new or grown reply card (nil in Deps: ignored).
type ReplySink interface{ Reply(ReplyCard) }

// LineSink gets each reply line (one that isn't alerting) as it is pushed, for the chat
// thread (#302), whole rather than joined into a card (nil in Deps: ignored).
type LineSink interface{ Line(OutboxLine) }

// replyRow is a line as one row of a card: "**who:** the news" when it has a title (its brief, else its text).
func replyRow(l OutboxLine) string {
	body := l.Brief
	if body == "" {
		body = l.Text
	}
	if l.Title != "" {
		return "**" + l.Title + ":** " + body
	}
	return body
}

// RepliesShownInput: the reply deck showed these outbox lines.
func RepliesShownInput(ids []uint64) Input { return Input{Kind: InRepliesShown, IDs: ids} }

// add appends a line as the card's next row.
func (card *ReplyCard) add(l OutboxLine) {
	card.Text += "\n" + replyRow(l)
	card.Count++
	card.Lines = append(card.Lines, l.ID)
	card.At = l.At
	if l.Session != "" && !slices.Contains(card.Sessions, l.Session) {
		card.Sessions = append(card.Sessions, l.Session)
	}
}

func newReplyCard(l OutboxLine) *ReplyCard {
	card := &ReplyCard{ID: l.ID, Text: replyRow(l), Count: 1, Lines: []uint64{l.ID}, At: l.At}
	if l.Session != "" {
		card.Sessions = []string{l.Session}
	}
	return card
}

// cardFor gives a reply-lane line (one that isn't alerting) its card: it joins the last card
// when its time is within ReplyJoinMs of the last row's, else starts one. The card is emitted whole
// each time, so a sink replaces the one it has by ID.
func (c *AgentController) cardFor(l OutboxLine, fx *[]Effect) {
	if c.lastCard != nil && l.At-c.lastCard.At <= ReplyJoinMs {
		c.lastCard.add(l)
	} else {
		c.lastCard = newReplyCard(l)
	}
	card := *c.lastCard
	card.Lines = append([]uint64(nil), card.Lines...)
	card.Sessions = append([]string(nil), card.Sessions...)
	if c.settings.OpenConversation && !c.textMode && !c.voiceOpen && !c.away {
		// Pixie opens a conversation to say it, and its caption is the card: held until then,
		// shown if it can't open (releaseHeldCards).
		if n := len(c.heldCards); n > 0 && c.heldCards[n-1].ID == card.ID {
			c.heldCards[n-1] = card
		} else {
			if len(c.heldCards) == 0 {
				c.heldSince = l.At
			}
			c.heldCards = append(c.heldCards, card)
		}
		return
	}
	*fx = append(*fx, Effect{Kind: EfReply, Reply: &card})
}

// releaseHeldCards: the conversation that was to say the held cards can't open: they show.
func (c *AgentController) releaseHeldCards(now Millis, fx *[]Effect) {
	for i := range c.heldCards {
		*fx = append(*fx, Effect{Kind: EfReply, Reply: &c.heldCards[i]})
	}
	c.heldCards = nil
}

// dropHeldCards: a conversation opened and says them; the next update starts a card of its own.
func (c *AgentController) dropHeldCards() {
	if len(c.heldCards) > 0 {
		c.heldCards, c.lastCard = nil, nil
	}
}

// settleHeldCards (every Step ends here): held cards show once no open is on its way (none
// asked for or to be retried, or it can't be any more: text mode, the setting off), or once
// they have waited OpenWaitMs whatever the opener still tries (opens refused on and on, away,
// the cap), so an update is never left showing nowhere. A conversation that opens later still
// replays the outbox.
func (c *AgentController) settleHeldCards(now Millis, fx *[]Effect) {
	o := c.opener
	coming := o.waiting.set || (o.retry && c.settings.OpenConversation && !c.textMode && !c.shutDown)
	if len(c.heldCards) > 0 && (!coming || satSub(now, c.heldSince) >= OpenWaitMs) {
		c.releaseHeldCards(now, fx)
	}
}

// repliesShown marks the lines of a card the deck showed delivered, so they aren't replayed.
func (c *AgentController) repliesShown(ids []uint64, fx *[]Effect) {
	shown := map[uint64]bool{}
	for _, id := range ids {
		shown[id] = true
	}
	for _, r := range c.outbox.MarkDeliveredWhere(func(l OutboxLine) bool { return shown[l.ID] }) {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: r})
	}
}

// PendingReplies: the cards of replies not yet shown and no older than ReplyHistoryMs, oldest
// first, grouped by the same ReplyJoinMs rule (what the deck holds after a restart).
func (c *AgentController) PendingReplies(now Millis) []ReplyCard {
	out := []ReplyCard{} // never nil: the shell decodes a list
	var last Millis
	for _, l := range c.outbox.Undelivered() {
		if alerting(l) || now-l.At > c.settings.ReplyHistoryMs {
			continue
		}
		if n := len(out); n > 0 && l.At-last <= ReplyJoinMs {
			out[n-1].add(l)
		} else {
			out = append(out, *newReplyCard(l))
		}
		last = l.At
	}
	return out
}
