package app

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// AppState is the tray model returned by `app.state` (plan "Shared contract"). Lists always
// encode as [] (never null).
type AppState struct {
	State string    `json:"state"`
	Packs []PackRef `json:"packs"`
	Pack  string    `json:"pack"`
	// PackProblems are the pixies that didn't load and why, one line each (#266).
	PackProblems []string     `json:"packProblems"`
	Scale        int          `json:"scale"`
	Fade         bool         `json:"fade"`
	Talking      bool         `json:"talking"`
	CanvasRecent []CanvasPage `json:"canvasRecent"`
	Agent        AgentState   `json:"agent"`
	StatusLines  []string     `json:"statusLines"`
	Hotkey       string       `json:"hotkey"`
	// AgentModeFix is the one-click fix for the Manual-mode line: ID set, Go handles
	// `app.action`; Copy set, Swift copies the text and calls nothing.
	AgentModeFix *TrayFix `json:"agentModeFix,omitempty"`
}

// TrayFix is a fix offered under the Manual-mode line.
type TrayFix struct {
	ID    string `json:"id,omitempty"`
	Title string `json:"title"`
	Copy  string `json:"copy,omitempty"`
}

// PackRef is one Tray ▸ Pixie row.
type PackRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CanvasPage is one Tray ▸ Canvas ▸ Recent row (Swift cuts long titles to 60 characters).
type CanvasPage struct {
	Slug  string `json:"slug"`
	Title string `json:"title"`
}

// AgentState drives Tray ▸ Coordinator (shown only when Enabled).
type AgentState struct {
	Enabled bool     `json:"enabled"`
	Jobs    []JobRef `json:"jobs"`
}

// JobRef is one agent job row; Label is preformatted by JobItems.
type JobRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// JobRow is what a job row shows (agent-core JobView's fields the tray uses). agentapp maps
// agent.JobView to it.
type JobRow struct {
	ID      string
	Status  string // agent JobStatus name, e.g. "working"
	Task    string
	AgeSecs uint64
}

// IntegrationHint is the agent's standing tray hint when the runtime has no status.
const IntegrationHint = "Coordinator: run `herdr integration install claude`"

// JobItems are the Tray ▸ Coordinator rows: "<task ≤ 40 chars> — <status>, <age>".
func JobItems(jobs []JobRow) []JobRef {
	const maxTask = 40
	out := make([]JobRef, 0, len(jobs))
	for _, j := range jobs {
		task := j.Task
		if utf8.RuneCountInString(task) > maxTask {
			task = string([]rune(task)[:maxTask-1]) + "…"
		}
		age := fmt.Sprintf("%d s", j.AgeSecs)
		if j.AgeSecs >= 60 {
			age = fmt.Sprintf("%d min", j.AgeSecs/60)
		}
		out = append(out, JobRef{ID: j.ID, Label: fmt.Sprintf("%s — %s, %s", task, j.Status, age)})
	}
	return out
}

// AgentStatusLine is the tray's agent line: the runtime's status if it has one, else the
// standing hint; "" for none.
func AgentStatusLine(status, hint string) string {
	if strings.TrimSpace(status) != "" {
		return status
	}
	return hint
}
