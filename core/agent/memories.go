package agent

// The coordinator's memories: every finished coordinator turn leaves exactly one short memory,
// written by the coordinator as a `<memory ref="job_…">…</memory>` block next to its <spoken>
// blocks, or, when the turn has none, made by the app from the turn's spoken text and the job's
// task. They are kept in memories.jsonl (bounded, oldest trimmed) and feed Pixie's session block
// and recall. Like <spoken>, the input is untrusted text: size-bounded, linear scans, cleaned
// and capped on write.

import (
	"encoding/json"
	"strings"
)

const (
	// MemoriesFile is the file name under `<app_data>/agent/`.
	MemoriesFile = "memories.jsonl"
	// MemoryMaxWords: a memory is one sentence of at most this many words.
	MemoryMaxWords = 25
	// MemoryMaxBytes: and at most this many bytes (cut on a word, ending "…").
	MemoryMaxBytes = 200
	// MemoryMaxKept: at most this many memories are kept; the oldest go first.
	MemoryMaxKept = 200
	// MemorySourceCoordinator / MemorySourceFallback: who wrote a memory.
	MemorySourceCoordinator = "coordinator"
	MemorySourceFallback    = "fallback"
	// noReplyMemory is the fallback for a turn with no text and no job.
	noReplyMemory = "The coordinator finished a turn without a reply."
)

// Memory is one remembered coordinator turn.
type Memory struct {
	At   Millis `json:"at"`
	Text string `json:"text"`
	// Job is the job the turn was about, if any.
	Job    *JobID `json:"job,omitempty"`
	Source string `json:"source"`
}

// CapMemory cleans text like <spoken> and keeps one sentence of at most MemoryMaxWords words and
// MemoryMaxBytes bytes; a longer one is cut on a word and ends with "…".
func CapMemory(text string) string {
	clean := Clean(truncateBytes(text, MaxFallbackBytes))
	sentences := splitSentences(clean)
	if len(sentences) == 0 {
		return ""
	}
	words := strings.Fields(sentences[0])
	cut := len(words) > MemoryMaxWords
	if cut {
		words = words[:MemoryMaxWords]
	}
	out := strings.Join(words, " ")
	if len(out) > MemoryMaxBytes {
		cut = true
	}
	if cut {
		limit := MemoryMaxBytes - len("…")
		for len(out) > limit {
			i := strings.LastIndexByte(out, ' ')
			if i <= 0 {
				out = truncateBytes(out, limit)
				break
			}
			out = out[:i]
		}
		out = strings.TrimRight(out, ",;:.!? ") + "…"
	}
	return out
}

// ParseMemory is the first `<memory>` block of a message (an unclosed one runs to the end):
// its capped text and ref attribute; ok false when there is none or it is empty after cleaning.
func ParseMemory(message string) (text string, ref *string, ok bool) {
	rest := truncateBytes(message, MaxParseBytes)
	start, found := findTag(rest, "<memory")
	if !found {
		return "", nil, false
	}
	after := rest[start+len("<memory"):]
	gt, found := tagEnd(after)
	if !found {
		return "", nil, false
	}
	body := after[gt+1:]
	if end := strings.Index(body, "</memory>"); end >= 0 {
		body = body[:end]
	}
	if text = CapMemory(body); text == "" {
		return "", nil, false
	}
	if r, has := attr(parseAttrs(after[:gt]), "ref"); has {
		ref = &r
	}
	return text, ref, true
}

// StripMemory removes every `<memory>` block, so it is never spoken.
func StripMemory(message string) string {
	var out strings.Builder
	rest := message
	for {
		start, found := findTag(rest, "<memory")
		if !found {
			break
		}
		out.WriteString(rest[:start])
		out.WriteByte(' ')
		end := strings.Index(rest[start:], "</memory>")
		if end < 0 {
			return out.String()
		}
		rest = rest[start+end+len("</memory>"):]
	}
	out.WriteString(rest)
	return out.String()
}

// FallbackMemory is the memory of a turn without a <memory> block: the job's task and what was
// said, capped the same way.
func FallbackMemory(task, spoken string) string {
	task = strings.TrimRight(Clean(task), ".!?:; ")
	spoken = Clean(spoken)
	text := spoken
	switch {
	case task != "" && spoken != "":
		text = task + ": " + spoken
	case task != "":
		text = task + ": no reply."
	}
	if m := CapMemory(text); m != "" {
		return m
	}
	return noReplyMemory
}

// MemoryLog holds the memories, oldest first.
type MemoryLog struct{ items []Memory }

func NewMemoryLog() *MemoryLog { return &MemoryLog{} }

func memoryRecord(m Memory) string {
	b, err := json.Marshal(m)
	if err != nil {
		panic("memory serializes: " + err.Error())
	}
	return string(b)
}

// Add keeps m; trimmed is true when the oldest went (rewrite the file with Lines), otherwise
// line is the record to append.
func (l *MemoryLog) Add(m Memory) (line string, trimmed bool) {
	l.items = append(l.items, m)
	if len(l.items) > MemoryMaxKept {
		l.items = append([]Memory(nil), l.items[len(l.items)-MemoryMaxKept:]...)
		return "", true
	}
	return memoryRecord(m), false
}

// Lines is every memory as jsonl.
func (l *MemoryLog) Lines() []string {
	out := make([]string, len(l.items))
	for k, m := range l.items {
		out[k] = memoryRecord(m)
	}
	return out
}

// Recent are the newest n memories, oldest first.
func (l *MemoryLog) Recent(n int) []Memory {
	return append([]Memory(nil), l.items[max(len(l.items)-n, 0):]...)
}

// RestoreMemoryLog reads memories.jsonl: unreadable or blank lines are skipped, each text is
// capped again (the file may have been edited), and only the newest MemoryMaxKept are kept.
func RestoreMemoryLog(lines []string) *MemoryLog {
	l := NewMemoryLog()
	for _, line := range lines {
		var m Memory
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			continue
		}
		if m.Text = CapMemory(m.Text); m.Text == "" {
			continue
		}
		l.items = append(l.items, m)
	}
	if len(l.items) > MemoryMaxKept {
		l.items = l.items[len(l.items)-MemoryMaxKept:]
	}
	return l
}
