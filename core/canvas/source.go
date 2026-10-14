package canvas

import (
	"sort"
	"strings"
)

// Bound on remembered invalid names; the set is cleared when full.
const maxWarned = 256

// PageEventKind is Added or Changed.
type PageEventKind int

const (
	Added PageEventKind = iota
	Changed
)

// PageEvent is a page appearing or being rewritten.
type PageEvent struct {
	Kind PageEventKind
	ID   PageID
}

// PageSource detects page changes. PollingSource is the real implementation.
type PageSource interface {
	// Poll returns events since the previous call, in page-name order.
	Poll() []PageEvent
}

type stamp struct {
	modNano int64
	len     int64
}

// PollingSource compares (mtime, length) snapshots; there is no watcher dependency.
type PollingSource struct {
	fs      PageFS
	seen    map[string]stamp
	warned  map[string]struct{}
	invalid []string
	err     string
	hasErr  bool
	failing bool
}

// NewPollingSource takes the initial snapshot: pages already present are not reported as
// Added. Invalid names in it are queued for TakeInvalid.
func NewPollingSource(fs PageFS) *PollingSource {
	s := &PollingSource{fs: fs, seen: map[string]stamp{}, warned: map[string]struct{}{}}
	s.Poll()
	return s
}

// TakeInvalid returns names that are not valid page names. Each name is reported once per run.
func (s *PollingSource) TakeInvalid() []string {
	out := s.invalid
	s.invalid = nil
	return out
}

// TakeError returns a listing failure, reported once until listing works again.
func (s *PollingSource) TakeError() (string, bool) {
	if !s.hasErr {
		return "", false
	}
	s.hasErr = false
	return s.err, true
}

func (s *PollingSource) Poll() []PageEvent {
	entries, err := s.fs.List()
	if err != nil {
		if !s.failing {
			s.failing = true
			s.err, s.hasErr = err.Error(), true
		}
		return nil
	}
	s.failing = false
	next := map[string]stamp{}
	var events []PageEvent
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".") {
			continue
		}
		id, err := PageIDFromFileName(e.Name)
		if err != nil {
			if len(s.warned) >= maxWarned {
				s.warned = map[string]struct{}{}
			}
			if _, dup := s.warned[e.Name]; !dup {
				s.warned[e.Name] = struct{}{}
				s.invalid = append(s.invalid, e.Name)
			}
			continue
		}
		st := stamp{e.Modified.UnixNano(), e.Len}
		if old, ok := s.seen[e.Name]; !ok {
			events = append(events, PageEvent{Added, id})
		} else if old != st {
			events = append(events, PageEvent{Changed, id})
		}
		next[e.Name] = st
	}
	s.seen = next
	return events
}
