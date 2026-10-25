package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

const dayMs = 24 * 60 * 60 * 1000

// generatedID is what Add makes for an entry with no ID of its own.
var generatedID = regexp.MustCompile(`^e[0-9]+$`)

// Open is the thread kept in dir/thread.jsonl, made if it isn't there. Entries older than
// keepDays (by now) are dropped; keepDays of 0 or less keeps everything. Every Add and Update
// appends the entry's whole JSON as one line, so on a reopen the last line per ID wins, and a
// line that doesn't parse (a torn write) is skipped. The file is rewritten compacted when
// anything was dropped, replaced or skipped.
func Open(dir string, keepDays int, now func() int64) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := NewMemory(now)
	s.path = filepath.Join(dir, "thread.jsonl")
	s.keepDays = keepDays
	dirty, err := s.load()
	if err != nil {
		return nil, err
	}
	if s.prune() {
		dirty = true
	}
	s.rebuild()
	if dirty {
		err = s.rewrite()
	} else {
		err = s.reopen()
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// load reads the file into entries; dirty when it holds anything but one good line per entry.
func (s *Store) load() (dirty bool, err error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	pos := map[string]int{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.ID == "" {
			dirty = true
			continue
		}
		if i, ok := pos[e.ID]; ok {
			s.entries[i] = e
			dirty = true
			continue
		}
		pos[e.ID] = len(s.entries)
		s.entries = append(s.entries, e)
	}
	return dirty, sc.Err()
}

// prune drops entries older than keepDays; whether it dropped any.
func (s *Store) prune() bool {
	if s.keepDays <= 0 || s.now == nil {
		return false
	}
	cutoff := s.now() - int64(s.keepDays)*dayMs
	kept := s.entries[:0]
	for _, e := range s.entries {
		if e.At >= cutoff {
			kept = append(kept, e)
		}
	}
	dropped := len(kept) != len(s.entries)
	s.entries = kept
	return dropped
}

// rebuild derives what the file doesn't hold from entries: the id index, the ids that were
// chosen by the caller, the outbox lines already handed out, and the next generated id.
func (s *Store) rebuild() {
	s.index = map[string]int{}
	s.named = map[string]bool{}
	s.handed = map[uint64]bool{}
	s.next = 0
	for i, e := range s.entries {
		s.index[e.ID] = i
		if generatedID.MatchString(e.ID) {
			if n, err := strconv.Atoi(e.ID[1:]); err == nil && n >= s.next {
				s.next = n + 1
			}
		} else {
			s.named[e.ID] = true
		}
		if e.Read {
			for _, l := range e.Lines {
				s.handed[l] = true
			}
		}
	}
}

// SetKeepDays changes how long entries are kept, and drops the ones older than that now.
func (s *Store) SetKeepDays(days int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keepDays = days
	if s.prune() {
		s.rebuild()
		s.rewrite()
	}
}

// Clear empties the thread, in memory and in the file.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.rebuild()
	if s.path == "" {
		return nil
	}
	return s.rewrite()
}

// persist appends e's whole JSON as one line. A store with no file keeps nothing. Called with
// the lock held; a failed write loses the line from the file, not from the thread.
func (s *Store) persist(e Entry) {
	if s.file == nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.file.Write(append(b, '\n'))
}

// rewrite replaces the file with one line per entry, through a temp file so a crash in the
// middle leaves the old one.
func (s *Store) rewrite() error {
	if s.path == "" {
		return nil
	}
	if s.file != nil {
		s.file.Close()
		s.file = nil
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range s.entries {
		b, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return fmt.Errorf("chat: %w", err)
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	return s.reopen()
}

// reopen opens the file for appending.
func (s *Store) reopen() error {
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	s.file = f
	return nil
}
