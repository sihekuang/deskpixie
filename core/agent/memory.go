package agent

// Pixie's memory comes from the coordinator side: Pixie keeps no record of its own. Each voice
// session (every connect, any provider) gets a compact "Recent work with the agent" block built
// from the job table (task, status, the coordinator's last reply or progress) and the
// coordinator's per-turn memories (memories.go). Anything else about earlier talks goes to the
// coordinator through delegate, like any request (#164).

import (
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/voice"
)

const (
	// MemoryWindowMs: closed jobs updated longer ago than this are left out (open ones never are).
	MemoryWindowMs Millis = 24 * 60 * 60 * 1000
	// MemoryMaxJobs: at most this many jobs are listed.
	MemoryMaxJobs = 10
	// MemoryEntryChars: each job's line is cut to this many characters.
	MemoryEntryChars = 300
	// MemoryJobsBudgetChars bounds the jobs part of the block.
	MemoryJobsBudgetChars = 1500
	// MemoryMaxNotes: at most this many of the coordinator's memories follow the jobs.
	MemoryMaxNotes = 10
	// MemoryMaxSessions: at most this many of the user's own sessions are listed.
	MemoryMaxSessions = 12
	// MemoryBudgetChars bounds the whole block (~4 characters a token, so ~650 tokens).
	MemoryBudgetChars = 2600
	// RecallPrefix marked a recall question in the job's task (#164 retired the recall tool;
	// saved jobs may still carry it).
	RecallPrefix = "Recall: "
)

const (
	memoryHeader = "Recent work with the coordinator (from its job records, oldest first; times are local). " +
		"Use it as background so the user needn't repeat themselves; don't recite it unless asked. " +
		"New requests and follow-ups still go to delegate, and so does anything else about earlier talks:"
	notesHeader = "Recent work with the coordinator, in its own notes of its latest turns (oldest first). " +
		"Background only: new requests and follow-ups still go to delegate:"
)

// MemoryFacts is the session block: jobs (open first, then the most recently updated, within
// MemoryMaxJobs and MemoryJobsBudgetChars) and then the newest memories (at most
// MemoryMaxNotes, skipping fallback memories of a job already listed), the whole within
// MemoryBudgetChars, each list oldest first; "" when there is nothing. Recall questions are left
// out of the jobs.
func MemoryFacts(jobs []Job, memories []Memory, now Millis, loc *time.Location) string {
	var open, closed []Job
	for _, j := range jobs {
		switch {
		case strings.HasPrefix(j.Task, RecallPrefix):
		case j.Status.IsOpen():
			open = append(open, j)
		default:
			closed = append(closed, j)
		}
	}
	// Open jobs win the budget, then the newest closed ones.
	keep := map[JobID]bool{}
	used := utf8.RuneCountInString(memoryHeader)
	picked := 0
	take := func(j Job) {
		n := utf8.RuneCountInString(memoryLine(j, now, loc)) + 1
		if picked < MemoryMaxJobs && used+n <= MemoryJobsBudgetChars {
			used += n
			picked++
			keep[j.ID] = true
		}
	}
	for _, j := range open {
		take(j)
	}
	for k := len(closed) - 1; k >= 0; k-- {
		take(closed[k])
	}
	var sections []string
	if picked > 0 {
		// Back in the order they were delegated.
		lines := []string{memoryHeader}
		for _, j := range jobs {
			if keep[j.ID] {
				lines = append(lines, memoryLine(j, now, loc))
			}
		}
		sections = append(sections, strings.Join(lines, "\n"))
	} else {
		used = 0
	}
	// Then the newest notes, in what is left.
	used += utf8.RuneCountInString(notesHeader) + 2
	var notes []string
	for k := len(memories) - 1; k >= 0 && len(notes) < MemoryMaxNotes; k-- {
		m := memories[k]
		if m.Source == MemorySourceFallback && m.Job != nil && keep[*m.Job] {
			continue
		}
		line := noteLine(m, now, loc)
		n := utf8.RuneCountInString(line) + 1
		if used+n > MemoryBudgetChars {
			break
		}
		used += n
		notes = append(notes, line)
	}
	if len(notes) > 0 {
		slices.Reverse(notes)
		sections = append(sections, notesHeader+"\n"+strings.Join(notes, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

// noteLine is "- [3:04 PM] <memory>".
func noteLine(m Memory, now Millis, loc *time.Location) string {
	return fmt.Sprintf("- [%s] %s", memoryStamp(m.At, now, loc), m.Text)
}

// memoryLine is "- [3:04 PM] done: <task>. Reply: <reply>", cut to MemoryEntryChars.
func memoryLine(j Job, now Millis, loc *time.Location) string {
	text := oneLine(j.Task)
	var tail string
	switch {
	case j.Reply != nil && strings.TrimSpace(*j.Reply) != "":
		tail = "Reply: " + oneLine(*j.Reply)
	case j.Status == JobFailed && j.Reason != nil:
		tail = "Why: " + oneLine(*j.Reason)
	case j.LastProgress != nil:
		tail = "Latest: " + oneLine(*j.LastProgress)
	}
	if tail != "" {
		text = strings.TrimRight(text, ".") + ". " + tail
	}
	line := fmt.Sprintf("- [%s] %s: %s", memoryStamp(j.Updated, now, loc), j.Status, text)
	if utf8.RuneCountInString(line) > MemoryEntryChars {
		line = string([]rune(line)[:MemoryEntryChars-1]) + "…"
	}
	return line
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// memoryStamp is the local time, with the weekday when it was not today.
func memoryStamp(at, now Millis, loc *time.Location) string {
	t := time.UnixMilli(int64(at)).In(loc)
	n := time.UnixMilli(int64(now)).In(loc)
	if t.YearDay() == n.YearDay() && t.Year() == n.Year() {
		return t.Format("3:04 PM")
	}
	return t.Format("Mon 3:04 PM")
}

// Facts is voice.SessionFacts: the recent jobs as a session block, read on every connect.
func (h *AgentHandle) Facts() string {
	now := h.deps.Clock.NowMs()
	since := Millis(0)
	if now > MemoryWindowMs {
		since = now - MemoryWindowMs
	}
	h.ctlMu.Lock()
	jobs := h.ctl.RecentJobs(since)
	memories := h.ctl.RecentMemories(MemoryMaxNotes + MemoryMaxJobs)
	h.ctlMu.Unlock()
	facts := MemoryFacts(jobs, memories, now, time.Local)
	if sessions := YourSessionsFacts(h.YourSessionLines()); sessions != "" {
		facts = strings.TrimPrefix(facts+"\n\n"+sessions, "\n\n")
	}
	return facts
}

// yourSessionsHeader leads the user's own sessions in Pixie's memory (#170).
const yourSessionsHeader = "The user's own sessions (started with pix; focus_session brings one to the front, " +
	"anything else about them goes to delegate):"

// YourSessionsFacts is the memory's part listing the user's sessions (YourSessionLines), ""
// when there are none.
func YourSessionsFacts(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return yourSessionsHeader + "\n- " + strings.Join(lines, "\n- ")
}

// YourSessionLines is "<name>: <label> (<where>)" for each of the user's reachable sessions,
// from the last roster (at most MemoryMaxSessions).
func (h *AgentHandle) YourSessionLines() []string {
	lines := []string{}
	for _, row := range h.Roster().Yours {
		if !row.Reachable || len(lines) == MemoryMaxSessions {
			continue
		}
		line := row.Name + ": " + oneLine(row.Label)
		if row.Detail != "" {
			line += " (" + row.Detail + ")"
		}
		if utf8.RuneCountInString(line) > MemoryEntryChars {
			line = string([]rune(line)[:MemoryEntryChars-1]) + "…"
		}
		lines = append(lines, line)
	}
	return lines
}

var _ voice.SessionFacts = (*AgentHandle)(nil)
