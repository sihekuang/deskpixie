// Package attach keeps the attachments waiting for the user's next request (#240): images and
// files pasted, dropped, picked or taken from the clipboard, stored under the coordinator's
// workspace so it can read them, and handed over as paths when the request goes.
//
// Layout under root: pending/<id>/<name> (waiting), sent/<stamp>-<id>/<name> (handed over),
// copies.jsonl (where workers' copies went). Files are 0600, folders 0700.
package attach

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	root string
	// copiesLog records workers' copies; outside the workspace, which the coordinator can write.
	copiesLog string
	now       func() time.Time
	mu        sync.Mutex
	// emitMu keeps OnChange calls in the order of the changes they report.
	emitMu   sync.Mutex
	items    []Item
	onChange func([]Item)
}

// New is a store under root (created on the first Add), recording workers' copies in copiesLog.
func New(root, copiesLog string, now func() time.Time) *Store {
	return &Store{root: root, copiesLog: copiesLog, now: now}
}

// Restore puts handed-over attachments back in the waiting set (the request couldn't go).
// Paths that aren't ones Take returned are ignored.
func (s *Store) Restore(paths []string) {
	s.mu.Lock()
	n := len(s.items)
	for _, p := range paths {
		dir, ok := s.sentDir(p)
		if !ok {
			continue
		}
		stampID := filepath.Base(dir)
		id := stampID[strings.LastIndexByte(stampID, '-')+1:]
		back := filepath.Join(s.root, "pending", id)
		if os.MkdirAll(filepath.Dir(back), 0o700) != nil || os.Rename(dir, back) != nil {
			continue
		}
		name := filepath.Base(p)
		size := int64(0)
		if fi, err := os.Lstat(filepath.Join(back, name)); err == nil {
			size = fi.Size()
		}
		s.items = append(s.items, Item{ID: id, Name: name, Kind: kindOf(name), Path: filepath.Join(back, name), Size: size})
	}
	if len(s.items) == n {
		s.mu.Unlock()
		return
	}
	s.changedLocked()
}

// sentDir is the sent/<stamp-id> folder of a handed-over attachment's path, if p is one: a
// regular file (not a link) directly in a real folder under sent/.
func (s *Store) sentDir(p string) (string, bool) {
	sent := filepath.Join(s.root, "sent")
	rel, err := filepath.Rel(sent, filepath.Clean(p))
	if err != nil || strings.HasPrefix(rel, "..") || strings.Count(rel, string(filepath.Separator)) != 1 {
		return "", false
	}
	dir := filepath.Dir(filepath.Join(sent, rel))
	if !isDir(dir) || !isFile(filepath.Join(sent, rel)) {
		return "", false
	}
	return dir, true
}

// isDir / isFile: a real folder or a regular file at path, never through a link.
func isDir(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}

func isFile(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

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
	// Only a regular file: a pipe or a device would never finish reading.
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return Item{}, errUnreadable
	}
	src, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
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
// handed-over ones and workers' copies older than Retention.
func (s *Store) Sweep() {
	_ = os.RemoveAll(filepath.Join(s.root, "pending"))
	s.sweepCopies()
	sent := filepath.Join(s.root, "sent")
	entries, _ := os.ReadDir(sent)
	cutoff := s.now().Add(-Retention)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(sent, e.Name()))
		}
	}
}

// changedLocked reports the set to OnChange; it unlocks s.mu. Reports go out in the order of
// the changes (emitMu is taken before s.mu is let go).
func (s *Store) changedLocked() {
	f, items := s.onChange, append([]Item(nil), s.items...)
	s.emitMu.Lock()
	s.mu.Unlock()
	defer s.emitMu.Unlock()
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

// attachedPrefix starts the line a request names an attachment with.
const attachedPrefix = "Attached: "

// copyRecord is a line of copies.jsonl: where a worker's copy went, and when.
type copyRecord struct {
	Path string    `json:"path"`
	At   time.Time `json:"at"`
}

// ForWorker copies each handed-over attachment text names into the worker folder dir
// (dir/.deskpixie/attachments/<its folder>/<name>, ignored by git) and returns text naming the
// copies. Other paths, and an empty dir, are left as they are; a copy already made is reused.
func (s *Store) ForWorker(text, dir string) string {
	if dir == "" || !strings.Contains(text, attachedPrefix) {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		path, ok := strings.CutPrefix(l, attachedPrefix)
		if !ok {
			continue
		}
		src, ok := s.sentDir(path)
		if !ok {
			continue
		}
		dest := filepath.Join(dir, ".deskpixie", "attachments", filepath.Base(src), filepath.Base(path))
		if s.copyFor(path, dest, dir) {
			lines[i] = attachedPrefix + dest
		}
	}
	return strings.Join(lines, "\n")
}

// copyFor makes the copy at dest (once) and records it; false if it couldn't be made.
func (s *Store) copyFor(src, dest, dir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The worktree is the repo's and the worker's: never follow a link they put there.
	folder := filepath.Join(dir, ".deskpixie")
	for _, d := range []string{folder, filepath.Join(folder, "attachments"), filepath.Dir(dest)} {
		if err := os.Mkdir(d, 0o700); err != nil && !(os.IsExist(err) && isDir(d)) {
			return false
		}
	}
	if fi, err := os.Lstat(dest); err == nil {
		return fi.Mode().IsRegular()
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return false
	}
	if f, err := os.OpenFile(filepath.Join(folder, ".gitignore"), os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600); err == nil {
		_, _ = f.WriteString("*\n")
		_ = f.Close()
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return false
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err != nil || cerr != nil {
		_ = os.Remove(dest)
		return false
	}
	rec, _ := json.Marshal(copyRecord{Path: dest, At: s.now()})
	if f, err := os.OpenFile(s.copiesLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = f.Write(append(rec, '\n'))
		_ = f.Close()
	}
	return true
}

// sweepCopies deletes workers' copies older than Retention and forgets them.
func (s *Store) sweepCopies() {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := s.copiesLog
	data, err := os.ReadFile(list)
	if err != nil {
		return
	}
	cutoff := s.now().Add(-Retention)
	var keep bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		var r copyRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Path == "" {
			continue
		}
		if r.At.Before(cutoff) {
			if isWorkerCopy(r.Path) {
				_ = os.Remove(r.Path)
				_ = os.Remove(filepath.Dir(r.Path)) // its folder, once empty
			}
			continue
		}
		keep.Write(sc.Bytes())
		keep.WriteByte('\n')
	}
	_ = os.WriteFile(list, keep.Bytes(), 0o600)
}

// isWorkerCopy: path is <dir>/.deskpixie/attachments/<folder>/<name>, a regular file in real
// folders (no link on the way), the only shape the sweep deletes.
func isWorkerCopy(path string) bool {
	path = filepath.Clean(path)
	folder := filepath.Dir(path)
	attachments := filepath.Dir(folder)
	deskpixie := filepath.Dir(attachments)
	return filepath.IsAbs(path) && filepath.Base(attachments) == "attachments" && filepath.Base(deskpixie) == ".deskpixie" &&
		isDir(deskpixie) && isDir(attachments) && isDir(folder) && isFile(path)
}
