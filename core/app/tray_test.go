package app

// Port of ai-mascot src-tauri/src/tray.rs tests: the 2 Go-formatted rows S1 left to P14
// (`agent_job_rows_show_task_status_and_age`, `the_agent_status_line_prefers_the_runtime_over_
// the_integration_hint`). The other 3 (sizes, recent ids, 60-char cut) are Swift's (S1,
// TrayMenuTests).

import (
	"strings"
	"testing"
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
