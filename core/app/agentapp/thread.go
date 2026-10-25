package agentapp

// The agent's output as chat-thread entries (#302): Go decides who said what and what is
// asked of you; the thread store (core/chat) keeps them. The sinks in desktop.go feed it.

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/chat"
)

// whoFor says who a line or request is from: the session it is about, else the worker, else
// the coordinator. A session or worker is labelled from the roster, else by title, else by its
// name; a worker is never yours.
func whoFor(session, worker, title string, r agent.RosterView) chat.Who {
	name := session
	if name == "" {
		name = worker
	}
	if name == "" {
		return chat.Who{Kind: "coordinator"}
	}
	w := chat.Who{Kind: "session", Name: name, Label: title, Color: chat.ColorFor(name)}
	if w.Label == "" {
		w.Label = name
	}
	for _, row := range append(append([]agent.RosterRow(nil), r.Agents...), r.Yours...) {
		if row.Name == name {
			w.Label, w.Yours, w.Status = row.Label, session != "" && row.Kind == "session", row.Status
			break
		}
	}
	return w
}

func briefOr(brief, text string) string {
	if brief != "" {
		return brief
	}
	return text
}

// EntryForLine is a reply line as a thread entry.
func EntryForLine(l agent.OutboxLine, r agent.RosterView) chat.Entry {
	e := chat.Entry{At: int64(l.At), Who: whoFor(l.Session, l.Worker, l.Title, r), Text: briefOr(l.Brief, l.Text), Lines: []uint64{l.ID}}
	if l.Session != "" {
		e.Sessions = []string{l.Session}
	}
	return e
}

// RequestEntries are the open requests of the updates list as entries, keyed by their item, so
// the same list twice gives the same IDs.
func RequestEntries(items []agent.UpdateItem, r agent.RosterView) []chat.Entry {
	out := []chat.Entry{}
	for _, it := range items {
		req := &chat.Request{Item: it.ID, Kind: "question", Options: it.Options, Typed: it.Typed, Job: string(it.Job)}
		if it.Token != "" {
			req.Token = it.Token
			if len(it.Options) == 0 {
				req.Kind = "permission"
			}
		}
		e := chat.Entry{ID: "req" + strconv.FormatUint(it.ID, 10), At: int64(it.At), Who: whoFor(it.Session, it.Worker, it.Title, r),
			Text: briefOr(it.Brief, it.Text), Lines: []uint64{it.ID}, Request: req}
		if it.Session != "" {
			e.Sessions = []string{it.Session}
		}
		out = append(out, e)
	}
	return out
}

// EntryForChat is a typed job's line as an answer, with the ID of its turn: the coordinator's,
// or (Session set) that session's own. False for a line still working, and for the question
// itself (Asking), which is in the thread already as a request (the updates list holds it
// too); another line on a job waiting on its question (status question) is kept.
func EntryForChat(l agent.ChatLine, id string, r agent.RosterView) (chat.Entry, bool) {
	if l.Asking || (l.Status != "done" && l.Status != "failed" && l.Status != "question") {
		return chat.Entry{}, false
	}
	return chat.Entry{ID: id, Who: whoFor(l.Session, "", "", r), Text: l.Text, Sessions: l.Sessions}, true
}

// chatTurn is the entry a typed job's answers go to until the turn ends.
type chatTurn struct {
	id, session, text string
}

// threadFeed turns what the agent's sinks see into the thread hooks. It keeps the last roster
// (the sinks may run on different goroutines). A nil feed does nothing.
type threadFeed struct {
	hooks  app.AgentHooks
	mu     sync.Mutex
	roster agent.RosterView
	// turns: each typed job's turn; ended: jobs whose next answer starts a new one (you sent
	// something, or it asked a question). seq numbers the turns, from the clock at start, so a
	// turn's ID is never one an earlier run gave out.
	turns map[agent.JobID]chatTurn
	ended map[agent.JobID]bool
	seq   int64
}

func newThreadFeed(h app.AgentHooks) *threadFeed {
	return &threadFeed{hooks: h, roster: agent.EmptyRoster(), turns: map[agent.JobID]chatTurn{},
		ended: map[agent.JobID]bool{}, seq: time.Now().UnixMilli()}
}

func (f *threadFeed) setRoster(v agent.RosterView) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.roster = v
	f.mu.Unlock()
}

func (f *threadFeed) current() agent.RosterView {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.roster
}

func (f *threadFeed) line(l agent.OutboxLine) {
	if f != nil && f.hooks.ThreadLine != nil {
		f.hooks.ThreadLine(EntryForLine(l, f.current()))
	}
}

// answer is a typed job's line. It grows its turn's entry while it adds to what that said;
// anything else is a turn of its own: after a send or the question itself, a line from someone else (a
// session answering), or a line that says something new.
func (f *threadFeed) answer(l agent.ChatLine) {
	if f == nil || f.hooks.ThreadAnswer == nil {
		return
	}
	f.mu.Lock()
	if l.Asking {
		f.ended[l.Job] = true
	}
	t, ok := f.turns[l.Job]
	if !ok || f.ended[l.Job] || t.session != l.Session || !strings.HasPrefix(l.Text, t.text) {
		f.seq++
		t = chatTurn{id: "job" + string(l.Job) + "." + strconv.FormatInt(f.seq, 10), session: l.Session}
	}
	e, keep := EntryForChat(l, t.id, f.roster)
	if keep {
		t.text = l.Text
		f.turns[l.Job] = t
		delete(f.ended, l.Job)
	}
	f.mu.Unlock()
	if keep {
		f.hooks.ThreadAnswer(e)
	}
}

// sent: you sent something for job, so its next answer is a turn of its own.
func (f *threadFeed) sent(job agent.JobID) {
	if f == nil || job == "" {
		return
	}
	f.mu.Lock()
	f.ended[job] = true
	f.mu.Unlock()
}

func (f *threadFeed) delivered(ids []uint64) {
	if f != nil && f.hooks.ThreadDelivered != nil && len(ids) > 0 {
		f.hooks.ThreadDelivered(ids)
	}
}

func (f *threadFeed) requests(items []agent.UpdateItem) {
	if f != nil && f.hooks.ThreadRequests != nil {
		f.hooks.ThreadRequests(RequestEntries(items, f.current()))
	}
}
