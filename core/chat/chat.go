// Package chat is the chat window's thread (#302): what you, Pixie, the coordinator and the
// sessions said, in order, with the requests still waiting on you.
package chat

import (
	"hash/fnv"
	"os"
	"slices"
	"strconv"
	"sync"
)

// JoinMs: lines from one sender that come within this of its last one share an entry.
const JoinMs = 5_000

// PageSize is how many entries a page holds.
const PageSize = 50

// Who says an entry: you, Pixie, the coordinator or one session.
type Who struct {
	Kind   string `json:"kind"`           // you | pixie | coordinator | session
	Name   string `json:"name,omitempty"` // session address
	Label  string `json:"label,omitempty"`
	Yours  bool   `json:"yours,omitempty"`
	Status string `json:"status,omitempty"`
	Color  int    `json:"color"`
}

// Request is a question or permission the entry asks of you, open until it has an Outcome.
type Request struct {
	Token   string   `json:"token,omitempty"`
	Item    uint64   `json:"item"`
	Kind    string   `json:"kind"` // permission | question
	Options []string `json:"options,omitempty"`
	Typed   bool     `json:"typed,omitempty"`
	Multi   bool     `json:"multi,omitempty"` // several picks, with agent.pick (#364)
	Outcome string   `json:"outcome,omitempty"`
	// Job is the job asking ("" = a session's question or a permission no job owns).
	Job string `json:"job,omitempty"`
	// Dismissible: a session's own question with no buttons, which agent.dismiss takes away
	// (#388).
	Dismissible bool `json:"dismissible,omitempty"`
}

// Entry is one message in the thread.
type Entry struct {
	ID          string   `json:"id"`
	At          int64    `json:"at"` // ms
	Who         Who      `json:"who"`
	Text        string   `json:"text"`
	Spoken      bool     `json:"spoken,omitempty"`
	Sessions    []string `json:"sessions,omitempty"`
	Lines       []uint64 `json:"lines,omitempty"` // outbox ids
	Attachments []string `json:"attachments,omitempty"`
	Request     *Request `json:"request,omitempty"`
	Read        bool     `json:"read,omitempty"`
}

// ColorFor is a name's colour, 0..5, the same every time.
func ColorFor(name string) int {
	h := fnv.New32a()
	h.Write([]byte(name))
	return int(h.Sum32() % 6)
}

// Store is the thread, oldest first. Safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	now     func() int64
	entries []Entry
	index   map[string]int  // id -> position in entries
	next    int             // for the next generated id
	named   map[string]bool // ids the caller chose: those entries never take a joined line
	handed  map[uint64]bool // outbox lines MarkRead has returned, so it returns each once

	path     string   // dir/thread.jsonl; empty for a memory store
	file     *os.File // appended to on every change; nil for a memory store
	keepDays int
}

// NewMemory is an empty store; now is the clock in ms, for entries added with no At.
func NewMemory(now func() int64) *Store {
	return &Store{now: now, index: map[string]int{}, named: map[string]bool{}, handed: map[uint64]bool{}}
}

// Add puts e in the thread and returns it as stored. An ID already there is replaced in
// place; a line from the same sender within JoinMs of the last entry joins it (its text on a
// new line, its At the newer one); anything else is appended. An empty ID gets e<n>, and your
// own entries are read when added. An entry with an ID of its own never joins or is joined,
// so the ID keeps pointing at it; replacing one makes it unread again (its lines already
// handed out by MarkRead are not handed out twice).
func (s *Store) Add(e Entry) Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.add(e)
}

// Renew puts e in the thread as a new entry: it replaces the one with its ID, if any, and its
// lines (and the replaced entry's) are handed out by MarkRead again. For a request whose ID
// came back for a new one.
func (s *Store) Renew(e Entry) Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range e.Lines {
		delete(s.handed, l)
	}
	if i, ok := s.index[e.ID]; ok && e.ID != "" {
		for _, l := range s.entries[i].Lines {
			delete(s.handed, l)
		}
	}
	return s.add(e)
}

func (s *Store) add(e Entry) Entry {
	e = clone(e)
	if e.At == 0 && s.now != nil {
		e.At = s.now()
	}
	if e.Who.Kind == "you" {
		e.Read = true
	}
	if i, ok := s.index[e.ID]; ok && e.ID != "" {
		s.entries[i] = e
		s.persist(e)
		return clone(e)
	}
	if n := len(s.entries); n > 0 && e.ID == "" && !s.named[s.entries[n-1].ID] && joins(s.entries[n-1], e) {
		last := &s.entries[n-1]
		last.Text += "\n" + e.Text
		last.At = e.At
		last.Lines = unite(last.Lines, e.Lines)
		last.Sessions = unite(last.Sessions, e.Sessions)
		last.Attachments = append(last.Attachments, e.Attachments...)
		last.Spoken = last.Spoken || e.Spoken
		last.Read = last.Read && e.Read
		s.persist(*last)
		return clone(*last)
	}
	if e.ID == "" {
		e.ID = "e" + strconv.Itoa(s.next)
		s.next++
	} else {
		s.named[e.ID] = true
	}
	s.index[e.ID] = len(s.entries)
	s.entries = append(s.entries, e)
	s.persist(e)
	return clone(e)
}

// joins: whether e continues last, the entry before it.
func joins(last, e Entry) bool {
	return last.Who.Kind == e.Who.Kind && last.Who.Name == e.Who.Name &&
		last.Request == nil && e.Request == nil && e.At-last.At <= JoinMs
}

// unite is a with b's items it lacks, in order.
func unite[T comparable](a, b []T) []T {
	for _, v := range b {
		if !slices.Contains(a, v) {
			a = append(a, v)
		}
	}
	return a
}

// Update changes the entry with this ID by fn and returns it; false when there is none. The
// ID stays what it was. fn runs under the store's lock and must not call the store.
func (s *Store) Update(id string, fn func(*Entry)) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.index[id]
	if !ok {
		return Entry{}, false
	}
	e := clone(s.entries[i])
	fn(&e)
	e.ID = id
	s.entries[i] = e
	s.persist(e)
	return clone(e), true
}

// Get is the entry with this ID; false when there is none.
func (s *Store) Get(id string) (Entry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, ok := s.index[id]
	if !ok {
		return Entry{}, false
	}
	return clone(s.entries[i]), true
}

// Page is up to limit entries (PageSize when limit is not above 0), newest first, that come
// before the entry with ID before; "" starts from the end. An unknown ID gives none.
func (s *Store) Page(before string, limit int) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = PageSize
	}
	end := len(s.entries)
	if before != "" {
		i, ok := s.index[before]
		if !ok {
			return nil
		}
		end = i
	}
	var page []Entry
	for i := end - 1; i >= 0 && len(page) < limit; i-- {
		page = append(page, clone(s.entries[i]))
	}
	return page
}

// Needs are the requests still waiting on you, oldest first.
func (s *Store) Needs() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	var open []Entry
	for _, e := range s.entries {
		if e.Request != nil && e.Request.Outcome == "" {
			open = append(open, clone(e))
		}
	}
	return open
}

// MarkRead marks every entry up to and including upTo read, and returns the outbox line ids
// not handed out before. An unknown ID marks nothing.
func (s *Store) MarkRead(upTo string) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	end, ok := s.index[upTo]
	if !ok {
		return nil
	}
	var lines []uint64
	for i := 0; i <= end; i++ {
		e := &s.entries[i]
		if !e.Read {
			e.Read = true
			s.persist(*e)
		}
		for _, l := range e.Lines {
			if !s.handed[l] {
				s.handed[l] = true
				lines = append(lines, l)
			}
		}
	}
	return lines
}

// MarkLinesRead marks the entries holding any of these outbox lines read (they were heard or
// seen elsewhere), and counts the lines as handed out, so MarkRead never returns them. Returns
// the entries it changed.
func (s *Store) MarkLinesRead(lines []uint64) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, l := range lines {
		s.handed[l] = true
	}
	var changed []Entry
	for i := range s.entries {
		e := &s.entries[i]
		if !e.Read && slices.ContainsFunc(e.Lines, func(l uint64) bool { return slices.Contains(lines, l) }) {
			e.Read = true
			s.persist(*e)
			changed = append(changed, clone(*e))
		}
	}
	return changed
}

// Len is how many entries the thread holds.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Unread is how many entries you have not read, not counting your own.
func (s *Store) Unread() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, e := range s.entries {
		if !e.Read && e.Who.Kind != "you" {
			n++
		}
	}
	return n
}

// clone copies e's slices and request, so no caller shares memory with the store.
func clone(e Entry) Entry {
	e.Sessions = slices.Clone(e.Sessions)
	e.Lines = slices.Clone(e.Lines)
	e.Attachments = slices.Clone(e.Attachments)
	if e.Request != nil {
		r := *e.Request
		r.Options = slices.Clone(r.Options)
		e.Request = &r
	}
	return e
}
