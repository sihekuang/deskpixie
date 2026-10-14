package agent

import (
	"strings"
	"unicode/utf8"
)

// Short progress phrases from tool calls (port of narrate.rs).

// NarrateIntervalMs: a job's progress is spoken at most this often.
const NarrateIntervalMs Millis = 30_000

const narrateMaxWords = 12

// Phrase: Bash → its description; Edit/Write → "editing <file name>"; start_worker → "starting
// a worker in <repo>"; anything else → false.
func Phrase(toolName string, toolInput any) (string, bool) {
	var text string
	switch toolName {
	case "Bash":
		d := Clean(stringField(toolInput, "description"))
		if d == "" {
			text = "running a command"
		} else {
			text = lowercaseFirst(d)
		}
	case "Edit", "Write", "MultiEdit":
		name, ok := pathFileName(stringField(toolInput, "file_path"))
		if !ok {
			return "", false
		}
		text = "editing " + name
	case "mcp__mascot__start_worker":
		// Only the final path component: never speak a full path.
		if name, ok := pathFileName(Clean(stringField(toolInput, "repo"))); ok {
			text = "starting a worker in " + name
		} else {
			text = "starting a worker"
		}
	default:
		return "", false
	}
	words := strings.Fields(text)
	return strings.Join(words[:min(len(words), narrateMaxWords)], " "), true
}

func lowercaseFirst(s string) string {
	r, size := utf8.DecodeRuneInString(s)
	if size == 0 {
		return ""
	}
	return strings.ToLower(string(r)) + s[size:]
}

// Narrator speaks a job's progress at most once per interval.
type Narrator struct {
	interval Millis
	last     map[JobID]Millis
}

func NewNarrator(interval Millis) *Narrator {
	return &Narrator{interval: interval, last: map[JobID]Millis{}}
}

// Allow is true (and remembered) if job has not been narrated within the interval.
func (n *Narrator) Allow(job JobID, now Millis) bool {
	if t, ok := n.last[job]; ok && satSub(now, t) < n.interval {
		return false
	}
	n.last[job] = now
	return true
}

func (n *Narrator) Forget(job JobID) { delete(n.last, job) }
