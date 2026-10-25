package agentapp

// The agent's output as chat-thread entries (#302): Go decides who said what and what is
// asked of you; the thread store (core/chat) keeps them. The sinks in desktop.go feed it.

import (
	"strconv"
	"sync"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/chat"
)

// whoFor says who a line or request is from: the session it is about, else the coordinator.
// A session is labelled from the roster, else by title, else by its address.
func whoFor(session, title string, r agent.RosterView) chat.Who {
	if session == "" {
		return chat.Who{Kind: "coordinator"}
	}
	w := chat.Who{Kind: "session", Name: session, Label: title, Color: chat.ColorFor(session)}
	if w.Label == "" {
		w.Label = session
	}
	for _, row := range append(append([]agent.RosterRow(nil), r.Agents...), r.Yours...) {
		if row.Name == session {
			w.Label, w.Yours, w.Status = row.Label, row.Kind == "session", row.Status
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
	e := chat.Entry{At: int64(l.At), Who: whoFor(l.Session, l.Title, r), Text: briefOr(l.Brief, l.Text), Lines: []uint64{l.ID}}
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
		req := &chat.Request{Item: it.ID, Kind: "question", Options: it.Options, Typed: it.Typed}
		if it.Token != "" {
			req.Token = it.Token
			if len(it.Options) == 0 {
				req.Kind = "permission"
			}
		}
		e := chat.Entry{ID: "req" + strconv.FormatUint(it.ID, 10), At: int64(it.At), Who: whoFor(it.Session, it.Title, r),
			Text: briefOr(it.Brief, it.Text), Lines: []uint64{it.ID}, Request: req}
		if it.Session != "" {
			e.Sessions = []string{it.Session}
		}
		out = append(out, e)
	}
	return out
}

// EntryForChat is a typed job's line as the coordinator's answer; false while it is working.
// The ID is the job's, so an answer that grows replaces its entry.
func EntryForChat(l agent.ChatLine) (chat.Entry, bool) {
	if l.Status != "done" && l.Status != "failed" && l.Status != "question" {
		return chat.Entry{}, false
	}
	return chat.Entry{ID: "job" + string(l.Job), Who: chat.Who{Kind: "coordinator"}, Text: l.Text,
		Sessions: l.Sessions}, true
}

// threadFeed turns what the agent's sinks see into the thread hooks. It keeps the last roster
// (the sinks may run on different goroutines). A nil feed does nothing.
type threadFeed struct {
	hooks  app.AgentHooks
	mu     sync.Mutex
	roster agent.RosterView
}

func newThreadFeed(h app.AgentHooks) *threadFeed {
	return &threadFeed{hooks: h, roster: agent.EmptyRoster()}
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

func (f *threadFeed) requests(items []agent.UpdateItem) {
	if f != nil && f.hooks.ThreadRequests != nil {
		f.hooks.ThreadRequests(RequestEntries(items, f.current()))
	}
}
