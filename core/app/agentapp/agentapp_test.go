package agentapp

import (
	"reflect"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/agent/httpserver"
)

// Port of src-tauri/src/agent/mod.rs tests (6).

func TestTerminalFailuresNameTheTerminal(t *testing.T) {
	for in, want := range map[string]string{
		"could not open Warp: no app": "Coordinator: could not open Warp",
		"could not open iTerm: x":     "Coordinator: could not open iTerm",
		"herdr not found":             "Coordinator: herdr not found",
	} {
		if got := TerminalFailureStatus(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestJobIdsAreRandomAndWellFormed(t *testing.T) {
	a, b := RandomIDs{}.JobID(), RandomIDs{}.JobID()
	if a == b {
		t.Fatal("same id twice")
	}
	if _, ok := agent.ParseJobID(string(a)); !ok {
		t.Fatal(a)
	}
}

func TestTheDelegationToolsAndRecallAreRegistered(t *testing.T) {
	clock := agenttest.ClockAt(1)
	deps := agent.Deps{
		Host:     agent.NewAgentHost(agenttest.NewFakeSessionHost(), clock, agent.CoordinatorRecipe{Cwd: "/w"}),
		Voice:    &agenttest.RecordingVoiceLink{},
		Notifier: &agenttest.RecordingNotifier{},
		Pixie:    &agenttest.RecordingNotifier{},
		Status:   &agenttest.RecordingStatus{},
		Planner:  &agenttest.FakePlanner{},
		Focus:    &agenttest.FakeFocusSink{},
		Store:    &agenttest.MemStore{},
		Clock:    clock,
		IDs:      &agenttest.SeqIds{},
	}
	h := agent.NewRuntime(deps, agent.RuntimeConfig{Settings: agent.DefaultSettings()})
	var names []string
	for _, tool := range DelegationTools(h) {
		names = append(names, tool.Name())
	}
	if !reflect.DeepEqual(names, []string{"delegate", "jobs", "answer_job"}) {
		t.Fatal(names)
	}
}

func TestThePermissionTimeoutStaysInsideTheHookLimit(t *testing.T) {
	if PermissionTimeoutMs(120) != 120_000 || PermissionTimeoutMs(0) != 1_000 {
		t.Fatal(PermissionTimeoutMs(120), PermissionTimeoutMs(0))
	}
	if !(PermissionTimeoutMs(600) < agent.Millis(httpserver.PermissionLimit.Milliseconds())) {
		t.Fatal(PermissionTimeoutMs(600))
	}
}

func TestAHostThatCannotBeBuiltIsASetupProblem(t *testing.T) {
	if got := HostProblem(agent.HostError{Kind: agent.HostNotFound}); got != "Coordinator: herdr not found" {
		t.Fatal(got)
	}
	if got := HostProblem(agent.HostError{Kind: agent.HostNotFound, Host: "tmux"}); got != "Coordinator: tmux not found" {
		t.Fatal(got)
	}
	if got := HostProblem(agent.HostError{Kind: agent.HostOther, Detail: "bad session"}); got != "Coordinator: invalid settings (see log)" {
		t.Fatal(got)
	}
}

func TestTheIntegrationHintIsTheRuledText(t *testing.T) {
	if IntegrationHint != "Coordinator: run `herdr integration install claude`" {
		t.Fatal(IntegrationHint)
	}
}
