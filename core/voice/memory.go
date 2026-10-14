package voice

// Short-term memory: a bounded, local record of recent exchanges (what the user said, what Bit
// answered, what was handed to the coordinator and what came back), kept in a JSON file so it
// survives restarts. Each new session gets a compact transcript of it appended to the
// instructions (as SessionFacts), so the user doesn't have to re-explain context.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// MemoryFile is the memory's file name in the app support folder.
	MemoryFile = "voice-memory.json"
	// MemoryWindow: entries older than this are forgotten.
	MemoryWindow = 6 * time.Hour
	// MemoryMaxEntries: at most this many entries are kept, newest first.
	MemoryMaxEntries = 60
	// MemoryEntryChars: longer entries are cut to this many characters.
	MemoryEntryChars = 400
	// MemoryBudgetChars bounds the transcript given to a session (~4 characters a token, so
	// ~750 tokens); the oldest entries are left out first.
	MemoryBudgetChars = 3000
)

// MemoryRole is who an entry is from.
type MemoryRole string

const (
	MemUser        MemoryRole = "user"        // the user's words
	MemBit         MemoryRole = "bit"         // Bit's spoken reply
	MemDelegated   MemoryRole = "delegated"   // a request Bit handed to the coordinator
	MemCoordinator MemoryRole = "coordinator" // a coordinator reply, question or note relayed to Bit
)

func (r MemoryRole) label() string {
	switch r {
	case MemUser:
		return "User"
	case MemBit:
		return "You"
	case MemDelegated:
		return "You handed to the agent"
	case MemCoordinator:
		return "Agent"
	}
	return string(r)
}

// MemoryEntry is one remembered line.
type MemoryEntry struct {
	At   time.Time  `json:"at"`
	Role MemoryRole `json:"role"`
	Text string     `json:"text"`
}

type memoryFile struct {
	Version int           `json:"version"`
	Entries []MemoryEntry `json:"entries"`
}

// Memory is the short-term memory. Safe for concurrent use. A zero path keeps it in memory
// only (tests).
type Memory struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	entries []MemoryEntry // oldest first
}

// NewMemory loads the memory at path (a missing or unreadable file starts empty), dropping
// entries outside the window. now nil = time.Now.
func NewMemory(path string, now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	m := &Memory{path: path, now: now}
	if path == "" {
		return m
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("voice: memory: %v", err)
		}
		return m
	}
	var f memoryFile
	if err := json.Unmarshal(b, &f); err != nil {
		log.Printf("voice: memory: %s unreadable, starting empty: %v", path, err)
		return m
	}
	m.entries = f.Entries
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pruneLocked() {
		m.saveLocked()
	}
	return m
}

// MemoryPath is the memory file in the app support folder.
func MemoryPath(appSupport string) string {
	if appSupport == "" {
		return ""
	}
	return filepath.Join(appSupport, MemoryFile)
}

// Add remembers one line; blank text is ignored. It is saved at once.
func (m *Memory) Add(role MemoryRole, text string) {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return
	}
	if utf8.RuneCountInString(text) > MemoryEntryChars {
		text = string([]rune(text)[:MemoryEntryChars-1]) + "…"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, MemoryEntry{At: m.now(), Role: role, Text: text})
	m.pruneLocked()
	m.saveLocked()
}

// Entries are the remembered lines inside the window, oldest first.
func (m *Memory) Entries() []MemoryEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pruneLocked() {
		m.saveLocked()
	}
	return append([]MemoryEntry(nil), m.entries...)
}

// Clear forgets everything and deletes the file.
func (m *Memory) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = nil
	if m.path == "" {
		return nil
	}
	if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Facts implements SessionFacts: the recent exchanges as a compact transcript, newest kept
// within MemoryBudgetChars; "" when there is nothing to remember.
func (m *Memory) Facts() string {
	entries := m.Entries()
	if len(entries) == 0 {
		return ""
	}
	now := m.now()
	header := "Recent conversations (your short-term memory of earlier talks with the user, oldest first; " +
		"times are local). Use it as background so the user needn't repeat themselves. Don't recite it " +
		"or mention having a memory unless asked; what the user says now wins over it:"
	var lines []string
	used := 0
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		line := fmt.Sprintf("- [%s] %s: %s", stamp(e.At, now), e.Role.label(), e.Text)
		n := utf8.RuneCountInString(line) + 1
		if used+n > MemoryBudgetChars {
			break
		}
		used += n
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return ""
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return header + "\n" + strings.Join(lines, "\n")
}

// stamp is the entry's local time, with the weekday when it was not today.
func stamp(at, now time.Time) string {
	at = at.In(now.Location())
	if at.YearDay() == now.YearDay() && at.Year() == now.Year() {
		return at.Format("3:04 PM")
	}
	return at.Format("Mon 3:04 PM")
}

// pruneLocked drops entries outside the window and beyond MemoryMaxEntries; true if any went.
func (m *Memory) pruneLocked() bool {
	cutoff := m.now().Add(-MemoryWindow)
	kept := m.entries[:0:0]
	for _, e := range m.entries {
		if e.At.After(cutoff) {
			kept = append(kept, e)
		}
	}
	if len(kept) > MemoryMaxEntries {
		kept = kept[len(kept)-MemoryMaxEntries:]
	}
	changed := len(kept) != len(m.entries)
	m.entries = kept
	return changed
}

// saveLocked writes the file (owner-only) via a temp file and rename; errors are logged.
func (m *Memory) saveLocked() {
	if m.path == "" {
		return
	}
	if len(m.entries) == 0 {
		if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
			log.Printf("voice: memory: %v", err)
		}
		return
	}
	b, err := json.MarshalIndent(memoryFile{Version: 1, Entries: m.entries}, "", "  ")
	if err != nil {
		log.Printf("voice: memory: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		log.Printf("voice: memory: %v", err)
		return
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		log.Printf("voice: memory: %v", err)
		return
	}
	if err := os.Rename(tmp, m.path); err != nil {
		log.Printf("voice: memory: %v", err)
	}
}

// ForgetTool is the `forget_recent` tool: the user asks Bit to forget recent conversations.
type ForgetTool struct {
	Memory *Memory
	// OnForget, when set, runs after the memory is cleared (the app stops recording the rest of
	// this conversation).
	OnForget func()
}

func (ForgetTool) Name() string { return "forget_recent" }

func (ForgetTool) Description() string {
	return "Erase your short-term memory of recent conversations. Call only when the user asks you to " +
		"forget, clear or delete what you talked about recently. Then confirm in a few words."
}

func (ForgetTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (t ForgetTool) Invoke(_ context.Context, _ json.RawMessage) ToolOutcome {
	if err := t.Memory.Clear(); err != nil {
		b, _ := json.Marshal(map[string]string{"error": err.Error()})
		return ToolOutcome{Output: b}
	}
	if t.OnForget != nil {
		t.OnForget()
	}
	return ToolOutcome{Output: json.RawMessage(`{"ok":true,"forgotten":true}`)}
}
