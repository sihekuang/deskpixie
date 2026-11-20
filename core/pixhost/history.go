package pixhost

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// HistoryLimit is how many past sessions History keeps (#461).
const HistoryLimit = 20

// HistoryEntry is one Claude conversation you ran through pix (#461).
type HistoryEntry struct {
	SessionID   string    `json:"sessionId"`
	Tool        string    `json:"tool"`
	Description string    `json:"description,omitempty"`
	Cwd         string    `json:"cwd,omitempty"`
	GitRoot     string    `json:"gitRoot,omitempty"`
	Where       string    `json:"where,omitempty"`
	Started     time.Time `json:"started"`
	LastActive  time.Time `json:"lastActive"`
	Ended       bool      `json:"ended,omitempty"`
	// Unresumable: a resume of it ended at once without Claude reporting the conversation.
	Unresumable bool `json:"unresumable,omitempty"`
	// HerdrPane: the pane of its tab in the "yours" Herdr session, when it was resumed there.
	HerdrPane string `json:"herdrPane,omitempty"`
	// RunningAs is the address of the session running it now ("" for none): set by
	// Host.History when listed, never kept.
	RunningAs string `json:"-"`
}

// HistoryStore keeps the past sessions (the pix host's seam; HistoryFile is the real one).
type HistoryStore interface {
	// Record merges e into the entry with its SessionID (non-zero fields win), or adds it.
	Record(e HistoryEntry)
	// Update changes the entry with id in place; nothing when there is none.
	Update(id string, f func(*HistoryEntry))
	// List is every entry, the most recently active first.
	List() []HistoryEntry
	Remove(id string)
	Clear()
}

// HistoryFile is History in a JSON file. A damaged file is set aside (.bad) and history starts
// empty; a failed write is logged and the entries stay in memory.
type HistoryFile struct {
	path    string
	mu      sync.Mutex
	entries []HistoryEntry
}

// NewHistoryFile is History kept at path, loaded from it now. A file that can't be read or is
// damaged is set aside as path.bad (when it can be moved) and history starts empty.
func NewHistoryFile(path string) *HistoryFile {
	h := &HistoryFile{path: path}
	b, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(b, &h.entries); err != nil {
			h.entries = nil
		}
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("pix: history: cannot load %s (%v); starting over", path, err)
		os.Rename(path, path+".bad")
	}
	return h
}

func (h *HistoryFile) Record(e HistoryEntry) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i := h.indexLocked(e.SessionID); i < 0 {
		h.entries = append(h.entries, e)
	} else {
		h.entries[i] = merge(h.entries[i], e)
	}
	h.saveLocked()
}

// merge is old with e's non-zero fields over it. Ended and Unresumable change by Update only,
// so a late record racing an exit can't undo it.
func merge(old, e HistoryEntry) HistoryEntry {
	old.Tool = cmp.Or(e.Tool, old.Tool)
	old.Description = cmp.Or(e.Description, old.Description)
	old.Cwd = cmp.Or(e.Cwd, old.Cwd)
	old.GitRoot = cmp.Or(e.GitRoot, old.GitRoot)
	old.Where = cmp.Or(e.Where, old.Where)
	old.HerdrPane = cmp.Or(e.HerdrPane, old.HerdrPane)
	if old.Started.IsZero() {
		old.Started = e.Started
	}
	if e.LastActive.After(old.LastActive) {
		old.LastActive = e.LastActive
	}
	return old
}

func (h *HistoryFile) Update(id string, f func(*HistoryEntry)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i := h.indexLocked(id); i >= 0 {
		f(&h.entries[i])
		h.saveLocked()
	}
}

func (h *HistoryFile) List() []HistoryEntry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.entries)
}

func (h *HistoryFile) Remove(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i := h.indexLocked(id); i >= 0 {
		h.entries = slices.Delete(h.entries, i, i+1)
		h.saveLocked()
	}
}

func (h *HistoryFile) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = nil
	h.saveLocked()
}

// indexLocked is the index of the entry with id, or -1 (h.mu held).
func (h *HistoryFile) indexLocked(id string) int {
	return slices.IndexFunc(h.entries, func(x HistoryEntry) bool { return x.SessionID == id })
}

// saveLocked sorts (newest first), trims to HistoryLimit and writes atomically (h.mu held).
func (h *HistoryFile) saveLocked() {
	slices.SortStableFunc(h.entries, func(a, b HistoryEntry) int { return b.LastActive.Compare(a.LastActive) })
	if len(h.entries) > HistoryLimit {
		h.entries = h.entries[:HistoryLimit]
	}
	entries := h.entries
	if entries == nil {
		entries = []HistoryEntry{}
	}
	b, err := json.MarshalIndent(entries, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(h.path), 0o700)
	}
	if err == nil {
		tmp := h.path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			if err = os.Rename(tmp, h.path); err != nil {
				os.Remove(tmp)
			}
		}
	}
	if err != nil {
		log.Printf("pix: history: cannot save %s: %v", h.path, err)
	}
}

var _ HistoryStore = (*HistoryFile)(nil)
