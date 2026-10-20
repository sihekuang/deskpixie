package agent

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
)

// Lines the user has not heard yet (port of outbox.rs): replayed as one notice when a
// conversation opens.

// OutboxFile is the file name under `<app_data>/agent/`.
const OutboxFile = "outbox.jsonl"

// OutboxLine is one outbox.jsonl record (field names as in Rust).
type OutboxLine struct {
	ID        uint64   `json:"id"`
	Text      string   `json:"text"`
	Kind      LineKind `json:"kind"`
	Token     *string  `json:"token"`
	At        Millis   `json:"at"`
	Delivered bool     `json:"delivered"`
	// Job and Worker: what the line is about (absent in older records).
	Job    JobID  `json:"job,omitempty"`
	Worker string `json:"worker,omitempty"`
	// App: a session line's app (bundle id, #105, #114).
	App string `json:"app,omitempty"`
	// Session: the session a session line is about (a click brings up its tab, #194).
	Session string `json:"session,omitempty"`
	// Note: shown small under a line, never said (a session dialog's command, #105).
	Note string `json:"note,omitempty"`
}

// UnmarshalJSON requires id, text, kind and at; token and delivered default (as serde does).
func (l *OutboxLine) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID        *uint64   `json:"id"`
		Text      *string   `json:"text"`
		Kind      *LineKind `json:"kind"`
		Token     *string   `json:"token"`
		At        *Millis   `json:"at"`
		Delivered bool      `json:"delivered"`
		Job       JobID     `json:"job"`
		Worker    string    `json:"worker"`
		App       string    `json:"app"`
		Session   string    `json:"session"`
		Note      string    `json:"note"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw.ID == nil || raw.Text == nil || raw.Kind == nil || raw.At == nil {
		return errors.New("outbox line: missing field")
	}
	*l = OutboxLine{ID: *raw.ID, Text: *raw.Text, Kind: *raw.Kind, Token: raw.Token, At: *raw.At, Delivered: raw.Delivered, Job: raw.Job, Worker: raw.Worker, App: raw.App, Session: raw.Session, Note: raw.Note}
	return nil
}

func (l OutboxLine) record() string {
	b, err := json.Marshal(l)
	if err != nil {
		panic("outbox line serializes: " + err.Error())
	}
	return string(b)
}

// Outbox holds the lines in order.
type Outbox struct {
	lines  []OutboxLine
	nextID uint64
}

func NewOutbox() *Outbox { return &Outbox{} }

// Push adds a line; returns its jsonl record to append.
func (o *Outbox) Push(line Line, now Millis) string {
	o.nextID++
	entry := OutboxLine{ID: o.nextID, Text: line.Text, Kind: line.Kind, Token: line.Token, At: now, Job: line.Job, Worker: line.Worker, App: line.App, Session: line.Session, Note: line.Note}
	o.lines = append(o.lines, entry)
	return entry.record()
}

// Len is how many lines are kept.
func (o *Outbox) Len() int { return len(o.lines) }

// HasTokenSince: a line from index from on carries this confirm word.
func (o *Outbox) HasTokenSince(from int, token string) bool {
	return slices.ContainsFunc(o.lines[min(from, len(o.lines)):], func(l OutboxLine) bool { return l.Token != nil && *l.Token == token })
}

// PushHeard adds a line Pixie has already said (delivered); returns its jsonl record.
func (o *Outbox) PushHeard(line Line, now Millis) string {
	o.Push(line, now)
	o.lines[len(o.lines)-1].Delivered = true
	return o.lines[len(o.lines)-1].record()
}

func (o *Outbox) Undelivered() []OutboxLine {
	var out []OutboxLine
	for _, l := range o.lines {
		if !l.Delivered {
			out = append(out, l)
		}
	}
	return out
}

// Replay (on Opened) returns every undelivered line, in order, as one notice; each is marked
// delivered. Permission questions whose token is no longer pending (decided or expired) are
// dropped. Returns the notice (nil if nothing is left) and the jsonl records to append.
func (o *Outbox) Replay(tokenPending func(string) bool) (*Notice, []string) {
	var items, records []string
	for k := range o.lines {
		l := &o.lines[k]
		if l.Delivered {
			continue
		}
		l.Delivered = true
		records = append(records, l.record())
		if stale := l.Token != nil && !tokenPending(*l.Token); !stale {
			items = append(items, "- "+l.Kind.Prefix()+l.Text)
		}
	}
	if len(items) == 0 {
		return nil, records
	}
	return &Notice{Text: "Coordinator replies while you were away:\n" + strings.Join(items, "\n")}, records
}

// MarkSeen (the updates bubble was looked at) marks those undelivered lines delivered, except
// the ones keep says still need you (a question waiting for its answer). Unknown or already
// delivered ids are ignored. Returns the jsonl records to append.
func (o *Outbox) MarkSeen(ids []uint64, keep func(OutboxLine) bool) []string {
	var records []string
	for k := range o.lines {
		l := &o.lines[k]
		if l.Delivered || !slices.Contains(ids, l.ID) || keep(*l) {
			continue
		}
		l.Delivered = true
		records = append(records, l.record())
	}
	return records
}

// Repeats marks the undelivered lines saying what line says (same kind and text, no confirm
// word) delivered: told again, only the newest stays in the bubble (#194). Returns the jsonl
// records to append.
func (o *Outbox) Repeats(line Line) []string {
	if line.Token != nil {
		return nil
	}
	return o.MarkDeliveredWhere(func(l OutboxLine) bool {
		return l.Token == nil && l.Kind == line.Kind && l.Text == line.Text
	})
}

// ToldSince: a line with this text was added after since (heard or not).
func (o *Outbox) ToldSince(text string, since Millis) bool {
	return slices.ContainsFunc(o.lines, func(l OutboxLine) bool { return l.At > since && l.Text == text })
}

// MarkDeliveredWhere marks the undelivered lines match picks delivered (a question decided
// elsewhere). Returns the jsonl records to append.
func (o *Outbox) MarkDeliveredWhere(match func(OutboxLine) bool) []string {
	var records []string
	for k := range o.lines {
		l := &o.lines[k]
		if l.Delivered || !match(*l) {
			continue
		}
		l.Delivered = true
		records = append(records, l.record())
	}
	return records
}

// Lines is every line as jsonl (the compacted file).
func (o *Outbox) Lines() []string {
	out := make([]string, len(o.lines))
	for k, l := range o.lines {
		out[k] = l.record()
	}
	return out
}

// RestoreOutbox rebuilds from outbox.jsonl: the last record per id wins; delivered lines older
// than 14 days are dropped; permission questions from a previous run are dropped (their
// requests died with that run).
func RestoreOutbox(records []string, now Millis) *Outbox {
	var order []uint64
	latest := map[uint64]OutboxLine{}
	for _, r := range records {
		var l OutboxLine
		if err := json.Unmarshal([]byte(r), &l); err != nil {
			continue
		}
		if _, seen := latest[l.ID]; !seen {
			order = append(order, l.ID)
		}
		latest[l.ID] = l
	}
	out := NewOutbox()
	for _, id := range order {
		l := latest[id]
		out.nextID = max(out.nextID, l.ID)
		old := l.Delivered && satSub(now, l.At) > RetentionMs
		if old || l.Token != nil {
			continue
		}
		out.lines = append(out.lines, l)
	}
	return out
}
