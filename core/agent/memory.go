package agent

// Bit's memory comes from the coordinator side: Bit keeps no record of its own. Each voice
// session (every connect, any provider) gets a compact "Recent work with the agent" block built
// from the job table (task, status, the coordinator's last reply or progress) and the
// coordinator's per-turn memories (memories.go), and the `recall` tool asks the coordinator
// itself about earlier talks, through the delegate path, with its newest memories attached.

import (
	"context"
	"encoding/json"
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
	// MemoryBudgetChars bounds the whole block (~4 characters a token, so ~650 tokens).
	MemoryBudgetChars = 2600
	// RecallNotes: this many of the newest memories go with a recall question.
	RecallNotes = 30
	// RecallPrefix marks a recall question in the job's task (and to the coordinator).
	RecallPrefix = "Recall: "
)

const (
	memoryHeader = "Recent work with the agent (from its job records, oldest first; times are local). " +
		"Use it as background so the user needn't repeat themselves; don't recite it unless asked. " +
		"New requests and follow-ups still go to delegate. " +
		"For anything else about earlier talks, call recall:"
	notesHeader = "Recent work with the agent, in its own notes of its latest turns (oldest first). " +
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
	return MemoryFacts(jobs, memories, now, time.Local)
}

// recallContext is what goes with a recall question: what it is, and the newest memories, so
// the answer holds even if the coordinator's own conversation was lost.
func (h *AgentHandle) recallContext() string {
	now := h.deps.Clock.NowMs()
	h.ctlMu.Lock()
	memories := h.ctl.RecentMemories(RecallNotes)
	h.ctlMu.Unlock()
	if len(memories) == 0 {
		return recallContext
	}
	lines := []string{recallContext, "Your saved memories of recent turns (oldest first):"}
	for _, m := range memories {
		line := noteLine(m, now, time.Local)
		if m.Job != nil {
			line += " [" + string(*m.Job) + "]"
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// recallContext tells the coordinator a recall is a question about the past, not work.
const recallContext = "This is a context question from Bit, not a task: answer it from what you remember of " +
	"earlier requests and conversations with the user (and your saved memories below or job history if needed). Don't " +
	"start any work. Reply at once with status done, in one or two sentences; if you don't know, say so."

// Recall is the `recall` tool: Bit asks the coordinator about earlier talks. It goes the
// delegate path (a job marked with RecallPrefix) and its answer comes back as an Agent reply.
type Recall struct{ H *AgentHandle }

func (Recall) Name() string { return "recall" }

func (Recall) Description() string {
	return "Ask the agent what it remembers of earlier conversations and work with the user, when the user " +
		"refers to something from before (\"that thing we talked about\", \"the earlier report\") that the " +
		"conversation and your recent-work notes don't cover. Not for new work: use delegate. Returns at once; " +
		"the answer arrives as an \"Agent reply:\" line."
}

func (Recall) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"question":{"type":"string","description":"What you need to remember, in a short question, with any names or details the user gave."}},` +
		`"required":["question"],"additionalProperties":false}`)
}

func (r Recall) Invoke(_ context.Context, raw json.RawMessage) voice.ToolOutcome {
	question := textArg(decodeArgs(raw), "question")
	if question == "" {
		return toolReply(map[string]any{"error": "question is required"})
	}
	id, err := r.H.Delegate(RecallPrefix+question, r.H.recallContext())
	if err != nil {
		return toolError(err)
	}
	// Like a "Check:" fact, the conversation stays open for the answer.
	return toolReply(map[string]any{"job_id": id.String(), "status": "asked"})
}

var (
	_ voice.Tool         = Recall{}
	_ voice.SessionFacts = (*AgentHandle)(nil)
)
