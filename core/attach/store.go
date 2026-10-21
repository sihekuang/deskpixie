// Package attach keeps the attachments waiting for the user's next request (#240): images and
// files pasted, dropped, picked or taken from the clipboard, stored under the coordinator's
// workspace so it can read them, and handed over as paths when the request goes.
//
// Layout under root: pending/<id>/<name> (waiting), sent/<stamp>-<id>/<name> (handed over),
// copies.jsonl (where workers' copies went). Files are 0600, folders 0700.
package attach

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

// MaxBytes is the largest attachment (2 MB); MaxItems the most waiting at once.
const (
	MaxBytes int64 = 2 << 20
	MaxItems       = 5
)

// Retention is how long handed-over attachments and workers' copies are kept, like jobs.
const Retention = 14 * 24 * time.Hour

var (
	errTooBig     = errors.New("That file is over 2 MB.")
	errTooMany    = errors.New("You can attach up to 5 files.")
	errUnreadable = errors.New("Couldn't read that file.")
)

// Item is one waiting attachment. Kind: "image" | "file".
type Item struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// Store is the set of waiting attachments. Safe for concurrent use.
type Store struct {
	root     string
	now      func() time.Time
	mu       sync.Mutex
	items    []Item
	onChange func([]Item)
}

// New is a store under root (created on the first Add).
func New(root string, now func() time.Time) *Store { return &Store{root: root, now: now} }

// OnChange is called after every change, with Pending().
func (s *Store) OnChange(f func([]Item)) {
	s.mu.Lock()
	s.onChange = f
	s.mu.Unlock()
}

// Add stores data (a pasted or copied image, already shrunk by the shell) as name.
func (s *Store) Add(name string, data []byte) (Item, error) {
	if int64(len(data)) > MaxBytes {
		return Item{}, errTooBig
	}
	return s.add(name, int64(len(data)), func(f *os.File) error {
		_, err := f.Write(data)
		return err
	})
}

// AddFile copies a dropped or picked file in; the original is left as it is.
func (s *Store) AddFile(path string) (Item, error) {
	src, err := os.Open(path)
	if err != nil {
		return Item{}, errUnreadable
	}
	defer src.Close()
	fi, err := src.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return Item{}, errUnreadable
	}
	if fi.Size() > MaxBytes {
		return Item{}, errTooBig
	}
	return s.add(filepath.Base(path), fi.Size(), func(f *os.File) error {
		_, err := io.Copy(f, io.LimitReader(src, MaxBytes+1))
		return err
	})
}

func (s *Store) add(name string, size int64, write func(*os.File) error) (Item, error) {
	s.mu.Lock()
	if len(s.items) >= MaxItems {
		s.mu.Unlock()
		return Item{}, errTooMany
	}
	s.mu.Unlock()
	id := newID()
	name = cleanName(name)
	dir := filepath.Join(s.root, "pending", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Item{}, errUnreadable
	}
	_ = os.Chmod(filepath.Join(s.root, "pending"), 0o700)
	_ = os.Chmod(s.root, 0o700)
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Item{}, errUnreadable
	}
	err = write(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.RemoveAll(dir)
		return Item{}, errUnreadable
	}
	it := Item{ID: id, Name: name, Kind: kindOf(name), Path: path, Size: size}
	s.mu.Lock()
	if len(s.items) >= MaxItems { // another add won the last place meanwhile
		s.mu.Unlock()
		_ = os.RemoveAll(dir)
		return Item{}, errTooMany
	}
	s.items = append(s.items, it)
	s.changedLocked()
	return it, nil
}

// Remove deletes a waiting attachment at once; false if there is none with that id.
func (s *Store) Remove(id string) bool {
	s.mu.Lock()
	for i, it := range s.items {
		if it.ID == id {
			s.items = append(s.items[:i:i], s.items[i+1:]...)
			_ = os.RemoveAll(filepath.Dir(it.Path))
			s.changedLocked()
			return true
		}
	}
	s.mu.Unlock()
	return false
}

// Pending are the waiting attachments, oldest first.
func (s *Store) Pending() []Item {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Item(nil), s.items...)
}

// Len is how many are waiting.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.items)
}

// Take hands the waiting attachments over: they move to sent/ and their absolute paths are
// returned (nil when none); the set is empty after.
func (s *Store) Take() []string {
	s.mu.Lock()
	items := s.items
	s.items = nil
	if len(items) == 0 {
		s.mu.Unlock()
		return nil
	}
	stamp := s.now().Format("20060102-150405")
	var paths []string
	for _, it := range items {
		dir := filepath.Join(s.root, "sent", stamp+"-"+it.ID)
		if os.MkdirAll(filepath.Dir(dir), 0o700) != nil || os.Rename(filepath.Dir(it.Path), dir) != nil {
			continue // lost on disk: nothing to hand over
		}
		paths = append(paths, filepath.Join(dir, it.Name))
	}
	s.changedLocked()
	return paths
}

// Sweep runs at start: attachments still waiting when the app last quit are deleted, and
// handed-over ones older than Retention.
func (s *Store) Sweep() {
	_ = os.RemoveAll(filepath.Join(s.root, "pending"))
	sent := filepath.Join(s.root, "sent")
	entries, _ := os.ReadDir(sent)
	cutoff := s.now().Add(-Retention)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(sent, e.Name()))
		}
	}
}

// changedLocked reports the set to OnChange; it unlocks s.mu.
func (s *Store) changedLocked() {
	f, items := s.onChange, append([]Item(nil), s.items...)
	s.mu.Unlock()
	if f != nil {
		f(items)
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// cleanName keeps a file name a plain name inside its folder.
func cleanName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return -1
		}
		return r
	}, filepath.Base(name))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "attachment"
	}
	return name
}

func kindOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return "image"
	}
	return "file"
}
