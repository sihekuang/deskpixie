package app

// Port of ai-mascot src-tauri/src/tray.rs tests: the 2 Go-formatted rows S1 left to P14
// (`agent_job_rows_show_task_status_and_age`, `the_agent_status_line_prefers_the_runtime_over_
// the_integration_hint`). The other 3 (sizes, recent ids, 60-char cut) are Swift's (S1,
// TrayMenuTests).

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAgentJobRowsShowTaskStatusAndAge(t *testing.T) {
	jobs := []JobRow{
		{ID: "job_000000000001", Status: "working", Task: "summarise the issues", AgeSecs: 42},
		{ID: "job_000000000002", Status: "question", Task: strings.Repeat("x", 50), AgeSecs: 125},
	}
	rows := JobItems(jobs)
	if rows[0] != (JobRef{ID: "job_000000000001", Label: "summarise the issues — working, 42 s"}) {
		t.Fatalf("%+v", rows[0])
	}
	if !strings.HasSuffix(rows[1].Label, "… — question, 2 min") {
		t.Fatal(rows[1].Label)
	}
	if n := strings.Count(rows[1].Label, "x"); n != 39 {
		t.Fatal(n)
	}
}

func TestTheAgentStatusLinePrefersTheRuntimeOverTheIntegrationHint(t *testing.T) {
	if got := AgentStatusLine("", ""); got != "" {
		t.Fatal(got)
	}
	if got := AgentStatusLine("", IntegrationHint); got != "Coordinator: run `herdr integration install claude`" {
		t.Fatal(got)
	}
	if got := AgentStatusLine("Coordinator: herdr not found", IntegrationHint); got != "Coordinator: herdr not found" {
		t.Fatal(got)
	}
}

// #461: Tray ▸ History rows: "<description ≤ 40> · <folder> · <how long ago>".
func TestHistoryRowsShowDescriptionFolderAndAge(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	rows := HistoryItems([]HistoryRow{
		{ID: "a", Description: "login bug", Folder: "deskpixie", LastActive: ago(30 * time.Second)},
		{ID: "b", Description: "x", Folder: "api", LastActive: ago(5 * time.Minute)},
		{ID: "c", Description: "x", Folder: "api", LastActive: ago(2*time.Hour + 10*time.Minute)},
		{ID: "d", Description: "x", Folder: "api", LastActive: ago(30 * time.Hour)},
		{ID: "e", Description: "x", Folder: "api", LastActive: ago(3*24*time.Hour + time.Hour)},
		{ID: "f", Folder: "api", LastActive: ago(time.Minute)},
		{ID: "g", Description: strings.Repeat("y", 50), Folder: "api", LastActive: ago(time.Minute), Unresumable: true},
		{ID: "h", Description: "clock skew", LastActive: now.Add(time.Minute)},
	}, now)
	want := []HistoryRef{
		{ID: "a", Label: "login bug · deskpixie · just now", Enabled: true},
		{ID: "b", Label: "x · api · 5 min ago", Enabled: true},
		{ID: "c", Label: "x · api · 2h ago", Enabled: true},
		{ID: "d", Label: "x · api · yesterday", Enabled: true},
		{ID: "e", Label: "x · api · 3 days ago", Enabled: true},
		{ID: "f", Label: "api · 1 min ago", Enabled: true},
		{ID: "g", Label: strings.Repeat("y", 39) + "… · api · 1 min ago", Enabled: false},
		{ID: "h", Label: "clock skew · just now", Enabled: true},
	}
	if !slices.Equal(rows, want) {
		t.Fatalf("%+v", rows)
	}
}

func TestNoHistoryRowsIsAnEmptyList(t *testing.T) {
	if rows := HistoryItems(nil, time.Now()); rows == nil || len(rows) != 0 {
		t.Fatalf("%#v", rows)
	}
}
