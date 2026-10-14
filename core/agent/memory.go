package agent

// Bit's memory comes from the coordinator side: Bit keeps no record of its own. Each voice
// session (every connect, any provider) gets a compact "Recent work with the agent" block built
// from the job table (task, status, the coordinator's last reply or progress), and the `recall`
// tool asks the coordinator itself about earlier talks, through the delegate path.

import (
	"context"
	"encoding/json"
	"fmt"
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
	// MemoryBudgetChars bounds the block (~4 characters a token, so ~500 tokens).
	MemoryBudgetChars = 2000
	// RecallPrefix marks a recall question in the job's task (and to the coordinator).
	RecallPrefix = "Recall: "
)

const memoryHeader = "Recent work with the agent (from its job records, oldest first; times are local). " +
	"Use it as background so the user needn't repeat themselves; don't recite it unless asked. " +
	"For anything else about earlier talks, call recall:"

// MemoryFacts is the session block for jobs (oldest first): open jobs first, then the most
// recently updated, within MemoryMaxJobs and MemoryBudgetChars, listed oldest first; "" when
// there is nothing. Recall questions are left out.
func MemoryFacts(jobs []Job, now Millis, loc *time.Location) string {
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
	var picked []Job
	used := utf8.RuneCountInString(memoryHeader)
	take := func(j Job) {
		n := utf8.RuneCountInString(memoryLine(j, now, loc)) + 1
		if len(picked) < MemoryMaxJobs && used+n <= MemoryBudgetChars {
			used += n
			picked = append(picked, j)
		}
	}
	for _, j := range open {
		take(j)
	}
	for k := len(closed) - 1; k >= 0; k-- {
		take(closed[k])
	}
	if len(picked) == 0 {
		return ""
	}
	// Back in the order they were delegated.
	keep := map[JobID]bool{}
	for _, j := range picked {
		keep[j.ID] = true
	}
	var lines []string
	for _, j := range jobs {
		if keep[j.ID] {
			lines = append(lines, memoryLine(j, now, loc))
		}
	}
	return memoryHeader + "\n" + strings.Join(lines, "\n")
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
	h.ctlMu.Unlock()
	return MemoryFacts(jobs, now, time.Local)
}

// recallContext tells the coordinator a recall is a question about the past, not work.
const recallContext = "This is a context question from Bit, not a task: answer it from what you remember of " +
	"earlier requests and conversations with the user (and your notes or job history if needed). Don't " +
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
	id, err := r.H.Delegate(RecallPrefix+question, recallContext)
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
