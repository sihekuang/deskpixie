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

// replyRow is a line as one row of a card: "**who:** the news" when it has the short form.
func replyRow(l OutboxLine) string {
	if l.Title != "" && l.Brief != "" {
		return "**" + l.Title + ":** " + l.Brief
	}
	return l.Text
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
// when it came within ReplyJoinMs of the last row, else starts one. The card is emitted whole
// each time, so a sink replaces the one it has by ID.
func (c *AgentController) cardFor(l OutboxLine, now Millis, fx *[]Effect) {
	if c.lastCard != nil && now-c.lastRowAt <= ReplyJoinMs {
		c.lastCard.add(l)
	} else {
		c.lastCard = newReplyCard(l)
	}
	c.lastRowAt = now
	card := *c.lastCard
	card.Lines = append([]uint64(nil), card.Lines...)
	card.Sessions = append([]string(nil), card.Sessions...)
	*fx = append(*fx, Effect{Kind: EfReply, Reply: &card})
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
	var out []ReplyCard
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
