package chat

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// maxLine is the longest line load reads; a longer one is skipped.
const maxLine = 1 << 20

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
	return s.read(f)
}

// read reads records from f into entries, as load does.
func (s *Store) read(f io.Reader) (dirty bool, err error) {
	pos := map[string]int{}
	r := bufio.NewReaderSize(f, maxLine)
	for {
		line, err := r.ReadSlice('\n')
		if err == bufio.ErrBufferFull { // a line over the limit: skip the rest of it
			dirty = true
			for err == bufio.ErrBufferFull {
				_, err = r.ReadSlice('\n')
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				return dirty, err
			}
			continue
		}
		if err != nil && err != io.EOF {
			return dirty, err
		}
		if err == io.EOF {
			if len(line) == 0 {
				break
			}
			dirty = true // no newline at the end: the next append would be glued to it
		}
		var e Entry
		if json.Unmarshal(line, &e) != nil || e.ID == "" {
			dirty = true
		} else if i, ok := pos[e.ID]; ok {
			s.entries[i] = e
			dirty = true
		} else {
			pos[e.ID] = len(s.entries)
			s.entries = append(s.entries, e)
		}
		if err == io.EOF {
			break
		}
	}
	return dirty, nil
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

// rebuild derives what the file doesn't hold from entries (the generated id counter only ever
// moves forward, so a pruned entry's id is not reused): the id index, the ids that were
// chosen by the caller, the outbox lines already handed out, and the next generated id.
func (s *Store) rebuild() {
	s.index = map[string]int{}
	s.named = map[string]bool{}
	s.handed = map[uint64]bool{}
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
func (s *Store) SetKeepDays(days int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keepDays = days
	if s.prune() {
		s.rebuild()
		return s.rewrite()
	}
	return nil
}

// Close stops the file. The thread stays readable, but nothing more is kept.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	err := s.file.Close()
	s.file = nil
	return err
}

// Clear empties the thread, in memory and in the file. When the file can't be replaced, it is
// emptied in place, so what was cleared never comes back.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.next = 0
	s.rebuild()
	if s.path == "" {
		return nil
	}
	err := s.rewrite()
	if err == nil {
		return nil
	}
	if s.file != nil {
		if s.file.Truncate(0) == nil {
			return nil
		}
	} else if os.Truncate(s.path, 0) == nil {
		return nil
	}
	return err
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
// middle leaves the old one. On a failure the old file and handle stay as they were, so
// appending carries on.
func (s *Store) rewrite() error {
	if s.path == "" {
		return nil
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range s.entries {
		b, err := json.Marshal(e)
		if err != nil {
			return fail(fmt.Errorf("chat: %w", err))
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fail(err)
	}
	if s.file != nil {
		s.file.Close()
	}
	s.file = f // the handle follows the renamed file
	return nil
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
