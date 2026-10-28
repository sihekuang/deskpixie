package app

import (
	"encoding/json"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sihekuang/deskpixie/core/chat"
)

// The chat window's thread (#302): chat.entries, chat.needs, chat.badge, chat.read and
// chat.clear; every change comes back as `chat.entry` (an entry added or changed), `chat.needs`
// (the open requests, when they change) and `chat.badge` (what Pixie's badge shows), and
// `chat.reset` when entries went (cleared, or older than chatKeepDays): the window reloads.
// `chat.live {who, text, job?}` is words still coming, never stored: yours and Pixie's while
// you talk, a typed job's step while it works; an empty text clears that line, and every line
// clears before its final entry comes.
// What you send, what you and Pixie say aloud, the agent's lines and requests, and your answers
// to them all land here.

// outcomeElsewhere: a request that went from the list before you answered it here.
const outcomeElsewhere = "Answered elsewhere"

// outcomeChatAbout: "Chat about this" on a session menu.
const outcomeChatAbout = "You chose to chat about this"

// threadNow is the thread's clock, in ms.
func threadNow() int64 { return time.Now().UnixMilli() }

// openThread is the thread kept in dir, or one in memory when it can't be kept there.
func openThread(dir string, keepDays uint32) *chat.Store {
	s, err := chat.Open(dir, int(keepDays), threadNow)
	if err != nil {
		log.Printf("chat: the thread can't be kept in %s, so it lasts until quit: %v", dir, err)
		return chat.NewMemory(threadNow)
	}
	return s
}

// threadOp is one change to the thread under a.threadMu: the store's calls and the events they
// make, so events go out in the order the changes were made. end unlocks.
type threadOp struct {
	a       *App
	needs   []string // the open requests' ids before the change
	changed bool
	reset   bool        // entries went: the window reloads
	preview *chat.Entry // the newest entry the change added that isn't yours
}

func (a *App) beginThread() *threadOp {
	a.threadMu.Lock()
	return &threadOp{a: a, needs: entryIDs(a.thread.Needs())}
}

// add adds e and says so.
func (op *threadOp) add(e chat.Entry) chat.Entry {
	s := op.a.thread
	var existed bool
	if e.ID != "" {
		_, existed = s.Get(e.ID)
	}
	var last string
	if p := s.Page("", 1); len(p) == 1 {
		last = p[0].ID
	}
	stored := s.Add(e)
	op.added(stored, !existed && stored.ID != last)
	return stored
}

// renew puts e in as a new entry over the one with its ID, and says so.
func (op *threadOp) renew(e chat.Entry) {
	op.added(op.a.thread.Renew(e), true)
}

func (op *threadOp) added(e chat.Entry, isNew bool) {
	if isNew && e.Who.Kind != "you" {
		op.preview = &e
	}
	op.sent(e)
}

// update changes the entry with this id by fn and says so.
func (op *threadOp) update(id string, fn func(*chat.Entry)) {
	if e, ok := op.a.thread.Update(id, fn); ok {
		op.sent(e)
	}
}

func (op *threadOp) sent(e chat.Entry) {
	op.changed = true
	op.a.emit("chat.entry", map[string]any{"entry": e})
}

// liveKey is one live line: who (you, pixie or coordinator), and the job for the coordinator.
type liveKey struct{ who, job string }

// live sets who's live line (job's, for the coordinator) to text, "" clears it, and says so
// when it changed.
func (op *threadOp) live(who, job, text string) {
	a, k := op.a, liveKey{who, job}
	if a.live[k] == text {
		return
	}
	if text == "" {
		delete(a.live, k)
	} else {
		if a.live == nil {
			a.live = map[liveKey]string{}
		}
		a.live[k] = text
	}
	f := map[string]any{"who": who, "text": text}
	if job != "" {
		f["job"] = job
	}
	a.emit("chat.live", f)
}

// end sends chat.reset when entries went (and clears every live line), the open requests when they changed and the badge
// after any change, then unlocks.
func (op *threadOp) end() {
	a := op.a
	defer a.threadMu.Unlock()
	if op.reset {
		a.emit("chat.reset", nil)
		keys := make([]liveKey, 0, len(a.live))
		for k := range a.live {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(x, y liveKey) int { return strings.Compare(x.who+"\x00"+x.job, y.who+"\x00"+y.job) })
		for _, k := range keys {
			op.live(k.who, k.job, "")
		}
	}
	if !op.changed && !op.reset {
		return
	}
	needs := a.thread.Needs()
	if !slices.Equal(entryIDs(needs), op.needs) {
		a.emit("chat.needs", map[string]any{"entries": orNoEntries(needs)})
	}
	badge := a.badgeLocked()
	if p := op.preview; p != nil {
		badge["preview"] = map[string]any{"id": p.ID, "who": p.Who, "text": p.Text}
	}
	a.emit("chat.badge", badge)
}

// badgeLocked is what Pixie's badge shows: the requests waiting on you and the entries unread.
func (a *App) badgeLocked() map[string]any {
	return map[string]any{"needs": len(a.thread.Needs()), "unread": a.thread.Unread()}
}

func entryIDs(es []chat.Entry) []string {
	ids := make([]string, len(es))
	for i, e := range es {
		ids[i] = e.ID
	}
	return ids
}

func orNoEntries(es []chat.Entry) []chat.Entry {
	if es == nil {
		return []chat.Entry{}
	}
	return es
}

// threadAdd is an entry from the agent (a line or an answer) or the voice loop.
func (a *App) threadAdd(e chat.Entry) {
	op := a.beginThread()
	defer op.end()
	op.add(e)
}

// threadRequests is every open request now. One not stored yet is added; one stored is left as
// it is, outcome and all, unless it is from another time: its line id was given to a new
// request, which replaces it. One still open that no longer takes a typed answer (its session's
// hook let go of it, #347) is changed so. An open one that is no longer listed was answered
// somewhere else.
func (a *App) threadRequests(list []chat.Entry) {
	op := a.beginThread()
	defer op.end()
	listed := map[string]bool{}
	for _, e := range list {
		listed[e.ID] = true
		stored, ok := a.thread.Get(e.ID)
		switch {
		case !ok:
			op.add(e)
		case e.At != 0 && stored.At != e.At:
			op.renew(e)
		case e.Request != nil && stored.Request != nil && stored.Request.Outcome == "" && stored.Request.Typed != e.Request.Typed:
			typed := e.Request.Typed
			op.update(e.ID, func(e *chat.Entry) { e.Request.Typed = typed })
		}
	}
	for _, e := range a.thread.Needs() {
		if !listed[e.ID] {
			op.update(e.ID, func(e *chat.Entry) { e.Request.Outcome = outcomeElsewhere })
		}
	}
}

// threadDelivered: these outbox lines were heard or seen elsewhere, so their entries are read.
func (a *App) threadDelivered(lines []uint64) {
	op := a.beginThread()
	defer op.end()
	for _, e := range a.thread.MarkLinesRead(lines) {
		op.sent(e)
	}
}

// threadAnswered records your answer to the open request with this token: outcome makes its
// text from the request's entry. No open request has it: nothing is recorded.
func (a *App) threadAnswered(token string, outcome func(chat.Entry) string) {
	if token == "" {
		return
	}
	op := a.beginThread()
	defer op.end()
	for _, e := range a.thread.Needs() {
		if e.Request.Token == token {
			text := outcome(e)
			op.update(e.ID, func(e *chat.Entry) { e.Request.Outcome = text })
			return
		}
	}
}

// outcomeDismissed is a request you dismissed (#388): not answered, and no longer waiting.
const outcomeDismissed = "Dismissed"

// threadDismissed records Dismiss on the open request of outbox line item (#388), from the
// bubble or the chat window. None open: nothing is recorded.
func (a *App) threadDismissed(item uint64) {
	op := a.beginThread()
	defer op.end()
	for _, e := range a.thread.Needs() {
		if e.Request.Item == item && e.Request.Dismissible {
			op.update(e.ID, func(e *chat.Entry) { e.Request.Outcome = outcomeDismissed })
			return
		}
	}
}

// threadSentAnswer records a message sent in the box as the answer to the question it
// answered: a request with no token that is the job's own (session "") or that session's. The
// newest one still open, or marked answered elsewhere (the agent's new list may come before
// this does).
func (a *App) threadSentAnswer(job, session, text string) {
	op := a.beginThread()
	defer op.end()
	asked := func(e chat.Entry) bool {
		r := e.Request
		if r == nil || r.Token != "" || (r.Outcome != "" && r.Outcome != outcomeElsewhere) {
			return false
		}
		if session != "" {
			return e.Who.Kind == "session" && e.Who.Name == session
		}
		return r.Job == job && job != ""
	}
	before := ""
	for {
		page := a.thread.Page(before, chat.PageSize)
		for _, e := range page {
			if asked(e) {
				op.update(e.ID, func(e *chat.Entry) { e.Request.Outcome = "You answered: " + text })
				return
			}
		}
		if len(page) < chat.PageSize {
			return
		}
		before = page[len(page)-1].ID
	}
}

func allowedOutcome(allow bool) func(chat.Entry) string {
	return func(e chat.Entry) string {
		if allow {
			return "You allowed " + e.Text
		}
		return "You denied " + e.Text
	}
}

// chosenOutcome names option (from 1) by its text, else its number.
func chosenOutcome(option int) func(chat.Entry) string {
	return func(e chat.Entry) string {
		if option >= 1 && option <= len(e.Request.Options) {
			return "You answered: " + e.Request.Options[option-1]
		}
		return "You answered: " + strconv.Itoa(option)
	}
}

// pickedOutcome names the options picked (from 1) by their text, else their numbers (#364).
func pickedOutcome(options []int) func(chat.Entry) string {
	return func(e chat.Entry) string {
		names := make([]string, len(options))
		for i, o := range options {
			names[i] = strconv.Itoa(o)
			if o >= 1 && o <= len(e.Request.Options) {
				names[i] = e.Request.Options[o-1]
			}
		}
		return "You answered: " + strings.Join(names, ", ")
	}
}

func typedOutcome(text string) func(chat.Entry) string {
	return func(chat.Entry) string { return "You answered: " + text }
}

func fixedOutcome(text string) func(chat.Entry) string {
	return func(chat.Entry) string { return text }
}

// threadLive is who's live line now (job's, for the coordinator); "" clears it.
func (a *App) threadLive(who, job, text string) {
	op := a.beginThread()
	defer op.end()
	op.live(who, job, text)
}

// threadSpoken is a transcript from the voice loop: yours, or Pixie's (read: you heard it). Its
// live line clears first, so it never shows after the words it was.
func (a *App) threadSpoken(role, text string) {
	var e chat.Entry
	switch role {
	case "user":
		e = chat.Entry{Who: chat.Who{Kind: "you"}}
	case "assistant":
		e = chat.Entry{Who: chat.Who{Kind: "pixie"}, Read: true}
	default:
		return
	}
	op := a.beginThread()
	defer op.end()
	op.live(e.Who.Kind, "", "")
	if strings.TrimSpace(text) == "" {
		return
	}
	e.Text, e.Spoken = text, true
	op.add(e)
}

// sessionLabel is the label of the newest entry from the session at this address, else the
// address.
func (a *App) sessionLabel(session string) string {
	a.threadMu.Lock()
	defer a.threadMu.Unlock()
	before := ""
	for {
		page := a.thread.Page(before, chat.PageSize)
		for _, e := range page {
			if e.Who.Name == session && e.Who.Label != "" {
				return e.Who.Label
			}
		}
		if len(page) < chat.PageSize {
			return session
		}
		before = page[len(page)-1].ID
	}
}

// threadSent is your message, sent as job. Its ID is its own, so it never joins the one before
// it (a reply to another session, say) and a later message for the same job (an answer to its
// question) never replaces it. sentSeq starts from the clock, so no earlier run gave it out.
func (a *App) threadSent(job, text string, attachments []string, session string) {
	a.threadMu.Lock()
	if a.sentSeq == 0 {
		a.sentSeq = threadNow()
	}
	a.sentSeq++
	id := "sent" + job + "." + strconv.FormatInt(a.sentSeq, 10)
	a.threadMu.Unlock()
	e := chat.Entry{ID: id, Who: chat.Who{Kind: "you"}, Text: text, Attachments: attachments}
	if session != "" {
		e.Sessions = []string{session}
	}
	a.threadAdd(e)
}

// applyChatKeepDays is a live chatKeepDays change: older entries go now.
func (a *App) applyChatKeepDays(days uint32) error {
	op := a.beginThread()
	defer op.end()
	before := a.thread.Len()
	err := a.thread.SetKeepDays(int(days))
	if err != nil {
		log.Printf("chat: keeping %d days: %v", days, err)
	}
	op.reset = a.thread.Len() != before
	return err
}

func (a *App) registerThread(r Registrar) {
	r.Register("chat.entries", func(args json.RawMessage) (any, error) {
		var in struct {
			Before string `json:"before"`
			Limit  int    `json:"limit"`
		}
		if err := decodeArgs("chat.entries", args, &in); err != nil {
			return nil, err
		}
		return map[string]any{"entries": orNoEntries(a.thread.Page(in.Before, in.Limit))}, nil
	})
	r.Register("chat.needs", func(json.RawMessage) (any, error) {
		return map[string]any{"entries": orNoEntries(a.thread.Needs())}, nil
	})
	r.Register("chat.badge", func(json.RawMessage) (any, error) {
		a.threadMu.Lock()
		defer a.threadMu.Unlock()
		return a.badgeLocked(), nil
	})
	r.Register("chat.read", func(args json.RawMessage) (any, error) {
		var in struct {
			UpTo string `json:"upTo"`
		}
		if err := decodeArgs("chat.read", args, &in); err != nil {
			return nil, err
		}
		op := a.beginThread()
		before := a.thread.Unread()
		ids := a.thread.MarkRead(in.UpTo)
		op.changed = a.thread.Unread() != before
		op.end()
		if len(ids) > 0 && a.agent != nil {
			a.agent.RepliesShown(ids)
		}
		return struct{}{}, nil
	})
	r.Register("chat.clear", func(json.RawMessage) (any, error) {
		op := a.beginThread()
		op.reset = true
		err := a.thread.Clear()
		op.end()
		if err != nil {
			log.Printf("chat: clearing the thread: %v", err)
			return nil, err
		}
		return struct{}{}, nil
	})
}
