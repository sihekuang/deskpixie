package herdr

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Port of client.rs tests (7).

var bg = context.Background()

func TestAgentListParsesTheCapturedShape(t *testing.T) {
	tr := newScriptedTransport()
	tr.answer("agent.list", fixture(t, "agent_list.json"))
	agents, err := NewClient(tr).Agents(bg)
	if err != nil {
		t.Fatal(err)
	}
	want := []HerdrAgent{{
		Name:             "coordinator",
		PaneID:           "w1:p2",
		TabID:            "w1:t2",
		Agent:            "claude",
		Status:           agent.PaneBlocked,
		Cwd:              "/tmp/probe/ws",
		InteractiveReady: true,
		LaunchPending:    false,
	}}
	if !reflect.DeepEqual(agents, want) {
		t.Fatalf("%+v", agents)
	}
	if info, ok := agents[0].Info(); !ok || info.Name != "coordinator" {
		t.Fatalf("%+v", info)
	}
}

func TestAgentStartIsLaunchPending(t *testing.T) {
	tr := newScriptedTransport()
	tr.answer("agent.start", fixture(t, "agent_started.json"))
	a, err := NewClient(tr).StartAgent(bg, "coordinator", "claude", "w1:p2", []string{"--model", "haiku"}, 30_000)
	if err != nil {
		t.Fatal(err)
	}
	if a.PaneID != "w1:p2" || a.Status != agent.PaneUnknown || a.InteractiveReady {
		t.Fatalf("%+v", a)
	}
	if a.Agent != "" {
		t.Fatal("launch_pending: no agent detected yet")
	}
	if !a.LaunchPending || a.Exited() {
		t.Fatalf("%+v", a)
	}
	assertJSON(t, tr.sentFor("agent.start")[0],
		`{"name":"coordinator","kind":"claude","pane_id":"w1:p2","args":["--model","haiku"],"timeout_ms":30000}`)
}

func TestWorkspacesAndTabs(t *testing.T) {
	tr := newScriptedTransport()
	tr.answer("workspace.list", fixture(t, "workspace_list.json"))
	tr.answer("workspace.create", fixture(t, "workspace_created.json"))
	tr.answer("tab.create", fixture(t, "tab_created.json"))
	c := NewClient(tr)
	ws, err := c.Workspaces(bg)
	if err != nil || !reflect.DeepEqual(ws, []Workspace{{ID: "w1", Label: "ai-mascot"}}) {
		t.Fatalf("%+v %v", ws, err)
	}
	// Herdr opens the new workspace with an empty tab "1".
	if id, tab, err := c.CreateWorkspace(bg, "ai-mascot", "/Users/x"); err != nil || id != "w1" || tab != "w1:t1" {
		t.Fatalf("%q %q %v", id, tab, err)
	}
	env := []agent.EnvVar{{Key: "MASCOT_AGENT_TOKEN", Value: "tok"}}
	if pane, tab, err := c.CreateTab(bg, "w1", "coordinator", "/ws", env); err != nil || pane != "w1:p2" || tab != "w1:t2" {
		t.Fatalf("%q %q %v", pane, tab, err)
	}
	assertJSON(t, tr.sentFor("tab.create")[0],
		`{"workspace_id":"w1","label":"coordinator","cwd":"/ws","env":{"MASCOT_AGENT_TOKEN":"tok"},"focus":false}`)
}

func TestTabRenameCloseAndMetadata(t *testing.T) {
	tr := newScriptedTransport()
	ok := `{"id":"p1","result":{"type":"ok"}}`
	tr.answer("tab.rename", ok)
	tr.answer("tab.close", ok)
	tr.answer("pane.report_metadata", ok)
	tr.answer("pane.report_metadata", ok)
	c := NewClient(tr)
	if err := c.RenameTab(bg, "w1:t2", "w-a · job_1 · fix it"); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, tr.sentFor("tab.rename")[0], `{"tab_id":"w1:t2","label":"w-a · job_1 · fix it"}`)
	if err := c.CloseTab(bg, "w1:t1"); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, tr.sentFor("tab.close")[0], `{"tab_id":"w1:t1"}`)
	// Values are cut to what Herdr keeps; "" clears a token, and no title clears the title.
	long := strings.Repeat("é", 100)
	if err := c.ReportMetadata(bg, "w1:p2", long, map[string]string{"job": "job_1", "task": ""}); err != nil {
		t.Fatal(err)
	}
	cut := strings.Repeat("é", MetaValueRunes-1) + "…"
	assertJSON(t, tr.sentFor("pane.report_metadata")[0],
		`{"pane_id":"w1:p2","source":"deskpixie","title":"`+cut+`","tokens":{"job":"job_1","task":null}}`)
	if err := c.ReportMetadata(bg, "w1:p2", "", nil); err != nil {
		t.Fatal(err)
	}
	assertJSON(t, tr.sentFor("pane.report_metadata")[1], `{"pane_id":"w1:p2","source":"deskpixie","clear_title":true}`)
}

func TestReadKeepsTheLastLines(t *testing.T) {
	tr := newScriptedTransport()
	tr.answer("agent.read", fixture(t, "pane_read.json"))
	got, err := NewClient(tr).Read(bg, "coordinator", 2)
	if err != nil || got != "TOKLEN=15\n➜  ws" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestErrorsMapToHostErrors(t *testing.T) {
	tr := newScriptedTransport()
	tr.answer("agent.get", fixture(t, "error_agent_not_found.json"))
	_, err := NewClient(tr).GetAgent(bg, "nope")
	if !reflect.DeepEqual(err, hostErr(agent.HostNoAgent, "nope")) {
		t.Fatalf("%#v", err)
	}
	tr.answer("tab.create", fixture(t, "error_no_workspace.json"))
	_, _, err = NewClient(tr).CreateTab(bg, "w9", "x", "/", nil)
	if !reflect.DeepEqual(err, hostErr(agent.HostOther, "workspace_not_found: no active workspace")) {
		t.Fatalf("%#v", err)
	}
	_, _, err = NewClient(tr).CreateTab(bg, "w9", "x", "/", nil)
	if !IsWorkspaceNotFound(err) {
		t.Fatalf("%#v", err)
	}
}

// Captured live from Herdr 0.7.5 about 2-5 s after agent.start: detected and idle, but still
// launch_pending (prompts fail with agent_not_ready here).
func TestTheLaunchPendingWindowIsNeitherExitedNorReady(t *testing.T) {
	v := json.RawMessage(`{"terminal_id":"term_1","name":"td","agent":"claude","agent_status":"idle","workspace_id":"w1","tab_id":"w1:t2","pane_id":"w1:p2","focused":false,"launch_pending":true,"state_change_seq":1,"cwd":"/tmp/ws","revision":2}`)
	a, ok := ParseHerdrAgent(v)
	if !ok {
		t.Fatal("parse")
	}
	if a.Agent != "claude" || a.Status != agent.PaneIdle || !a.LaunchPending || a.InteractiveReady {
		t.Fatalf("%+v", a)
	}
	if a.Exited() {
		t.Fatal("exited")
	}
	if a.Ready() {
		t.Fatal("ready")
	}
	done := a
	done.LaunchPending = false
	done.InteractiveReady = true
	if !done.Ready() {
		t.Fatal("done not ready")
	}
}

func TestAgentNotReadyIsRecognised(t *testing.T) {
	tr := newScriptedTransport()
	tr.answer("agent.prompt", `{"id":"p1","error":{"code":"agent_not_ready","message":"agent td is not an active named agent"}}`)
	err := NewClient(tr).Prompt(bg, "td", "hi")
	if !IsAgentNotReady(err) {
		t.Fatalf("%#v", err)
	}
	if IsAgentNotReady(agent.ErrNoAgent("td")) {
		t.Fatal("NoAgent is not agent_not_ready")
	}
}
