package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Where an agent line goes (port of routing.rs): spoken in an open conversation, otherwise a
// chime and a notification (capped at N a minute, questions bypass the cap) plus the outbox.

// NotifyWindowMs is the notification cap's window.
const NotifyWindowMs Millis = 60_000

// LineKind is what an agent line is (serialized lowercase in outbox.jsonl).
type LineKind int

const (
	// LineReply is a reply for a job (`<spoken ref=…>`).
	LineReply LineKind = iota
	// LineQuestion is a question for the user: a status="question" block or a permission prompt.
	LineQuestion
	// LineUnsolicited is a reply that names no open job (e.g. the user typed into the pane).
	LineUnsolicited
	// LineInfo is the app's own line, already complete (e.g. "Coordinator: allowed").
	LineInfo
)

var lineKindNames = []string{"reply", "question", "unsolicited", "info"}

func (k LineKind) String() string {
	if k < 0 || int(k) >= len(lineKindNames) {
		return fmt.Sprintf("LineKind(%d)", int(k))
	}
	return lineKindNames[k]
}

func (k LineKind) MarshalJSON() ([]byte, error) { return enumMarshal(lineKindNames, int(k)) }

func (k *LineKind) UnmarshalJSON(b []byte) error {
	v, err := enumUnmarshal(lineKindNames, b)
	*k = LineKind(v)
	return err
}

var _ json.Marshaler = LineReply

// Prefix is what the voice model is told, before the line itself.
func (k LineKind) Prefix() string {
	switch k {
	case LineReply:
		return "Coordinator reply: "
	case LineQuestion:
		return "Coordinator question: "
	case LineUnsolicited:
		return "Coordinator says: "
	}
	return ""
}

// Line is one agent line.
type Line struct {
	Text string
	Kind LineKind
	// Token is the confirm word of a permission question (lets the outbox drop it once
	// decided); nil otherwise.
	Token *string
	// Job and Worker: what the line is about (the updates bubble goes to that job's or worker's
	// pane); zero when it is about no one job or worker.
	Job    JobID
	Worker string
	// App: the bundle id of the app a session's line is about (a click activates it, #105).
	App string
}

func NewLine(kind LineKind, text string) Line { return Line{Text: text, Kind: kind} }

func NewJobLine(kind LineKind, text string, job JobID) Line {
	return Line{Text: text, Kind: kind, Job: job}
}

// Spoken is the text as the voice model hears it.
func (l Line) Spoken() string { return l.Kind.Prefix() + l.Text }

// DeliveryKind tells RouteDelivery variants apart.
type DeliveryKind int

const (
	DeliverInject DeliveryKind = iota
	DeliverChime
	DeliverNotify
	DeliverOutbox
)

// RouteDelivery is one thing Router.Route asks for (Rust routing::Delivery): Inject → Notice;
// Chime; Notify → Text; Outbox → Line.
type RouteDelivery struct {
	Kind   DeliveryKind
	Notice Notice
	Text   string
	Line   Line
	// Lines: the lines an Inject carries when they are not just Line (the merged updates
	// Tick says); they are routed again if the notice bounces.
	Lines []Line
}

func InjectDelivery(n Notice) RouteDelivery { return RouteDelivery{Kind: DeliverInject, Notice: n} }
func ChimeDelivery() RouteDelivery          { return RouteDelivery{Kind: DeliverChime} }
func NotifyDelivery(t string) RouteDelivery { return RouteDelivery{Kind: DeliverNotify, Text: t} }
func OutboxDelivery(l Line) RouteDelivery   { return RouteDelivery{Kind: DeliverOutbox, Line: l} }

// MaxMergedUpdates: how many of the held-back updates the merged line says (the latest).
const MaxMergedUpdates = 3

// Router applies the cap of N user-facing updates a minute, across all jobs. Everything said
// or notified takes a slot; questions are never held back. With the conversation closed, a line
// over the cap is only outboxed and Tick later notifies "N more updates". With it open, the
// coordinator's lines are still spoken at once (it is answering the user), but updates (worker
// reports, routed by RouteUpdate) over the cap are held and Tick says them as one line.
type Router struct {
	perMinute int
	// sent is when each update in the current window went out.
	sent []Millis
	// suppressed counts lines held back by the cap since the last collapsed notification.
	suppressed int
	// held: updates held back by the cap while the conversation is open.
	held []Line
}

func NewRouter(perMinute int) *Router {
	return &Router{perMinute: max(perMinute, 1)}
}

func (r *Router) Route(conversationOpen bool, line Line, now Millis) []RouteDelivery {
	if conversationOpen {
		r.forgetOld(now)
		r.sent = append(r.sent, now)
		return []RouteDelivery{InjectDelivery(Notice{Text: line.Spoken()})}
	}
	if line.Kind == LineUnsolicited {
		return []RouteDelivery{OutboxDelivery(line)}
	}
	var out []RouteDelivery
	if line.Kind == LineQuestion || r.Take(now) {
		if line.Kind == LineQuestion {
			r.sent = append(r.sent, now)
		}
		out = append(out, ChimeDelivery(), NotifyDelivery(line.Text))
	} else {
		r.suppressed++
	}
	return append(out, OutboxDelivery(line))
}

// RouteUpdate routes an update (a worker's report): like Route, but held back by the cap with
// the conversation open too.
func (r *Router) RouteUpdate(conversationOpen bool, line Line, now Millis) []RouteDelivery {
	if !conversationOpen || line.Kind == LineQuestion {
		return r.Route(conversationOpen, line, now)
	}
	if r.Take(now) {
		return []RouteDelivery{InjectDelivery(Notice{Text: line.Spoken()})}
	}
	r.held = append(r.held, line)
	return nil
}

// Take takes a slot if one is free (for something said outside Route, like narration).
func (r *Router) Take(now Millis) bool {
	r.forgetOld(now)
	if len(r.sent) >= r.perMinute {
		return false
	}
	r.sent = append(r.sent, now)
	return true
}

// Tick: once a slot frees up, the held-back lines go out as one line: a "N more updates"
// notification (closed), or one spoken line merging them (open).
func (r *Router) Tick(conversationOpen bool, now Millis) []RouteDelivery {
	r.forgetOld(now)
	if len(r.sent) >= r.perMinute {
		return nil
	}
	if conversationOpen && len(r.held) > 0 {
		lines := r.held
		r.held = nil
		r.sent = append(r.sent, now)
		d := InjectDelivery(Notice{Text: mergedUpdates(lines)})
		d.Lines = lines
		return []RouteDelivery{d}
	}
	if r.suppressed == 0 {
		return nil
	}
	n := r.suppressed
	r.suppressed = 0
	r.sent = append(r.sent, now)
	return []RouteDelivery{NotifyDelivery(moreUpdates(n))}
}

func moreUpdates(n int) string {
	if n == 1 {
		return "1 more update"
	}
	return fmt.Sprintf("%d more updates", n)
}

// mergedUpdates: one held line as is; several as "Coordinator: 5 more updates. <the latest three>".
func mergedUpdates(lines []Line) string {
	if len(lines) == 1 {
		return lines[0].Spoken()
	}
	var texts []string
	for _, l := range lines[max(0, len(lines)-MaxMergedUpdates):] {
		texts = append(texts, l.Text)
	}
	return fmt.Sprintf("Coordinator: %s. %s", moreUpdates(len(lines)), strings.Join(texts, " "))
}

// ConversationOpened is called on VoiceSignal Opened: the outbox replay already covers the
// lines the cap held back, so the "N more updates" notification is dropped.
func (r *Router) ConversationOpened() { r.suppressed = 0 }

// ConversationClosed: updates held for the open conversation are outboxed instead, and count
// toward the next "N more updates" notification.
func (r *Router) ConversationClosed() []RouteDelivery {
	var out []RouteDelivery
	for _, l := range r.held {
		out = append(out, OutboxDelivery(l))
	}
	r.suppressed += len(r.held)
	r.held = nil
	return out
}

func (r *Router) forgetOld(now Millis) {
	for len(r.sent) > 0 && satSub(now, r.sent[0]) >= NotifyWindowMs {
		r.sent = r.sent[1:]
	}
}

// satSub is Rust's saturating_sub for Millis.
func satSub(a, b Millis) Millis {
	if a < b {
		return 0
	}
	return a - b
}
