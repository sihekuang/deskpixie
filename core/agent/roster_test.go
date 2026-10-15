package agent_test

import (
	"encoding/json"
	"testing"

	. "github.com/sihekuang/deskpixie/core/agent"
)

func TestRosterCoordinatorOnlyIsListedButNotCounted(t *testing.T) {
	v := BuildRoster([]AgentInfo{{Name: Coordinator, Status: PaneIdle}}, nil, "herdr")
	if v.AgentCount != 0 || v.YourCount != 0 || len(v.Agents) != 1 || v.Agents[0].Kind != "coordinator" ||
		v.Agents[0].Label != "Coordinator" || v.Agents[0].Status != "idle" || len(v.Yours) != 0 {
		t.Fatalf("%+v", v)
	}
}

func TestRosterWorkersPanesAndSessions(t *testing.T) {
	list := []AgentInfo{
		{Name: "deskpixie-claude", Status: PaneWorking},
		{Name: Coordinator, Status: PaneWorking},
		{Name: "old-claude", Status: PaneBlocked},
		{Name: "focus-claude", Status: PaneIdle, Origin: OriginUser, User: &UserSession{Tool: "claude", Repo: "focus", Where: "WebStorm", App: "com.jetbrains.WebStorm", Description: "login bug", Reachable: true}},
		{Name: "api-codex", Status: PaneWorking, Origin: OriginUser, User: &UserSession{Tool: "codex", Repo: "api", Where: "iTerm", Reachable: true}},
		{Name: "repo-claude", Status: PaneIdle, Origin: OriginUser, User: &UserSession{Tool: "claude", Where: "terminal", Reachable: false}},
	}
	workers := []WorkerRow{{Name: "deskpixie-claude", Repo: "deskpixie"}}
	v := BuildRoster(list, workers, "herdr")
	b, _ := json.Marshal(v)
	want := `{"agents":[` +
		`{"name":"coordinator","label":"Coordinator","status":"working","kind":"coordinator","reachable":true},` +
		`{"name":"deskpixie-claude","label":"deskpixie-claude","detail":"deskpixie","status":"working","kind":"worker","reachable":true},` +
		`{"name":"old-claude","label":"old-claude","detail":"herdr","status":"blocked","kind":"pane","reachable":true}],` +
		`"yours":[` +
		`{"name":"focus-claude","label":"login bug","detail":"WebStorm","status":"idle","kind":"session","reachable":true,"app":"com.jetbrains.WebStorm"},` +
		`{"name":"api-codex","label":"codex in api","detail":"iTerm","status":"working","kind":"session","reachable":true},` +
		`{"name":"repo-claude","label":"claude","detail":"terminal","status":"idle","kind":"session","reachable":false}],` +
		`"agent_count":2,"your_count":2}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func TestRosterEmptyIsArraysNotNull(t *testing.T) {
	b, _ := json.Marshal(BuildRoster(nil, nil, "tmux"))
	if string(b) != `{"agents":[],"yours":[],"agent_count":0,"your_count":0}` {
		t.Fatal(string(b))
	}
	if b, _ := json.Marshal(EmptyRoster()); string(b) != `{"agents":[],"yours":[],"agent_count":0,"your_count":0}` {
		t.Fatal(string(b))
	}
}
