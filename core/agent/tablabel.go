package agent

import (
	"context"
	"log"
	"strings"
)

// What the multiplexer shows for an agent's tab, so the owner can tell the tabs apart: a label
// (the tab's name), a title (the pane's title, in place of the agent's own terminal title) and
// display-only metadata (job, repo, branch, task). Agents are still found by name, never by
// label, so the label may change at any time.

// TabSep separates a label's parts. The agent's name always comes first.
const TabSep = " · "

// TabTaskRunes is how much of a task a tab label shows.
const TabTaskRunes = 40

// TabLabel is the tab's label, title and metadata. Label "" is the agent's name; Title ""
// clears the title; a Meta value "" clears that key.
type TabLabel struct {
	Label string
	Title string
	Meta  map[string]string
}

// Labeler is a SessionHost that can relabel an agent's tab after it started (optional: Herdr
// has it).
type Labeler interface {
	Relabel(ctx context.Context, agent string, tab TabLabel) error
}

// ShortTask is task on one line, cut to at most n runes (with "…" when cut).
func ShortTask(task string, n int) string {
	s := strings.Join(strings.Fields(task), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimRight(string(r[:n-1]), " ") + "…"
}

// WorkerTab is a worker's tab: `<name> · <job> · <short task>`, with the job, repo, branch and
// task as metadata.
func WorkerTab(plan WorkerPlan) TabLabel {
	parts := []string{plan.Name}
	if plan.Job != "" {
		parts = append(parts, plan.Job.String())
	}
	if t := ShortTask(plan.Task, TabTaskRunes); t != "" {
		parts = append(parts, t)
	}
	meta := map[string]string{"role": "worker", "job": plan.Job.String(), "repo": plan.RepoName, "task": oneLine(plan.Task)}
	if plan.Slug != "" {
		meta["branch"] = BranchPrefix + plan.Slug
	}
	return TabLabel{Label: strings.Join(parts, TabSep), Meta: meta}
}

// CoordinatorTab is the coordinator's tab while it works on job (`coordinator · <short task>`,
// the task as its title), or idle (job nil: `coordinator`, title and job cleared).
func CoordinatorTab(job *Job) TabLabel {
	if job == nil {
		return TabLabel{Label: Coordinator, Meta: map[string]string{"role": "coordinator", "job": "", "task": ""}}
	}
	label := Coordinator
	if t := ShortTask(job.Task, TabTaskRunes); t != "" {
		label += TabSep + t
	}
	task := oneLine(job.Task)
	return TabLabel{Label: label, Title: task, Meta: map[string]string{"role": "coordinator", "job": job.ID.String(), "task": task}}
}

// relabelQuietly relabels agent's tab if the host can; a failure only leaves the old label.
func relabelQuietly(ctx context.Context, host SessionHost, agent string, tab TabLabel) {
	if l, ok := host.(Labeler); ok {
		if err := l.Relabel(ctx, agent, tab); err != nil && !IsHostError(err, HostNoAgent) {
			log.Printf("agent: could not relabel %s: %s", agent, tag(err))
		}
	}
}
