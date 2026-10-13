package agent

import (
	"encoding/json"
	"fmt"
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
	// LineInfo is the app's own line, already complete (e.g. "Agent: allowed").
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
		return "Agent reply: "
	case LineQuestion:
		return "Agent question: "
	case LineUnsolicited:
		return "Agent says: "
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
}

func NewLine(kind LineKind, text string) Line { return Line{Text: text, Kind: kind} }

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
}

func InjectDelivery(n Notice) RouteDelivery { return RouteDelivery{Kind: DeliverInject, Notice: n} }
func ChimeDelivery() RouteDelivery          { return RouteDelivery{Kind: DeliverChime} }
func NotifyDelivery(t string) RouteDelivery { return RouteDelivery{Kind: DeliverNotify, Text: t} }
func OutboxDelivery(l Line) RouteDelivery   { return RouteDelivery{Kind: DeliverOutbox, Line: l} }

// Router applies the notification cap.
type Router struct {
	perMinute int
	// sent is when each notification in the current window went out.
	sent []Millis
	// suppressed counts lines held back by the cap since the last collapsed notification.
	suppressed int
}

func NewRouter(perMinute int) *Router {
	return &Router{perMinute: max(perMinute, 1)}
}

func (r *Router) Route(conversationOpen bool, line Line, now Millis) []RouteDelivery {
	if conversationOpen {
		return []RouteDelivery{InjectDelivery(Notice{Text: line.Spoken()})}
	}
	if line.Kind == LineUnsolicited {
		return []RouteDelivery{OutboxDelivery(line)}
	}
	r.forgetOld(now)
	var out []RouteDelivery
	if line.Kind == LineQuestion || len(r.sent) < r.perMinute {
		r.sent = append(r.sent, now)
		out = append(out, ChimeDelivery(), NotifyDelivery(line.Text))
	} else {
		r.suppressed++
	}
	return append(out, OutboxDelivery(line))
}

// Tick: once a slot frees up, the held-back lines go out as one "N more updates" notification.
func (r *Router) Tick(now Millis) []RouteDelivery {
	r.forgetOld(now)
	if r.suppressed == 0 || len(r.sent) >= r.perMinute {
		return nil
	}
	n := r.suppressed
	r.suppressed = 0
	r.sent = append(r.sent, now)
	text := "1 more update"
	if n != 1 {
		text = fmt.Sprintf("%d more updates", n)
	}
	return []RouteDelivery{NotifyDelivery(text)}
}

// ConversationOpened is called on VoiceSignal Opened: the outbox replay already covers the
// lines the cap held back, so the "N more updates" notification is dropped.
func (r *Router) ConversationOpened() { r.suppressed = 0 }

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
