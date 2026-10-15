package settings

import (
	"reflect"
	"testing"
)

// #87: the coordinator may be Claude Code (default) or Codex; each kind has its own model list.

func TestTheCoordinatorKindDefaultsToClaude(t *testing.T) {
	d := Default()
	if d.Agent.CoordinatorKind != CoordinatorClaude || d.Agent.CodexModel != nil {
		t.Fatalf("%+v", d.Agent)
	}
	// A settings file from before #87 has neither key: it loads as Claude.
	c, err := ParseConfig([]byte(`{"agent":{"enabled":true}}`))
	if err != nil || c.Agent.CoordinatorKind != CoordinatorClaude {
		t.Fatal(c.Agent.CoordinatorKind, err)
	}
}

func TestTheCoordinatorAgentPickerOffersClaudeAndCodex(t *testing.T) {
	f := FieldByPath("agent.coordinatorKind")
	if f == nil || f.Section != SectionAgent || f.Label != "Coordinator agent" || f.Apply != ApplyRestart {
		t.Fatalf("%+v", f)
	}
	var values []string
	for _, o := range f.Kind.Choices.Static {
		values = append(values, o.Value)
	}
	if f.Kind.Type != KindChoice || f.Kind.Choices.Open || !reflect.DeepEqual(values, []string{"claude", "codex"}) {
		t.Fatalf("%+v", f.Kind)
	}
	c := Default()
	c.Agent.CoordinatorKind = "gemini"
	found := false
	for _, e := range Validate(c, Env{Home: t.TempDir(), Platform: Macos}) {
		found = found || e.Path == "agent.coordinatorKind" && e.Message == NotAChoice
	}
	if !found {
		t.Fatal("an unknown coordinator kind is not refused")
	}
}

func TestTheModelListFollowsTheCoordinatorAgent(t *testing.T) {
	claude := FieldByPath("agent.model")
	if !reflect.DeepEqual(claude.VisibleWhen, []Condition{{Key: "agent.coordinatorKind", In: []string{"claude"}}}) {
		t.Fatal(claude.VisibleWhen)
	}
	codex := FieldByPath("agent.codexModel")
	if codex == nil || !reflect.DeepEqual(codex.VisibleWhen, []Condition{{Key: "agent.coordinatorKind", In: []string{"codex"}}}) {
		t.Fatalf("%+v", codex)
	}
	if !codex.Kind.Choices.Open || !codex.Kind.Choices.Null || codex.Kind.Choices.Static[0].Value != "" || codex.Apply != ApplyRestart {
		t.Fatalf("%+v", codex.Kind)
	}
	for _, o := range codex.Kind.Choices.Static[1:] {
		if o.Value[:4] != "gpt-" {
			t.Fatal("not a GPT model:", o.Value)
		}
	}
	// Right under the picker, in this order.
	var agent []string
	for _, f := range Fields {
		if f.Section == SectionAgent {
			agent = append(agent, f.Path)
		}
	}
	at := func(p string) int {
		for i, x := range agent {
			if x == p {
				return i
			}
		}
		return -1
	}
	if k := at("agent.coordinatorKind"); k < 0 || at("agent.model") != k+1 || at("agent.codexModel") != k+2 {
		t.Fatal(agent)
	}
}

func TestTheCodexModelIsClonedAndRoundTrips(t *testing.T) {
	c := Default()
	m := "gpt-6-sol"
	c.Agent.CoordinatorKind, c.Agent.CodexModel = CoordinatorCodex, &m
	d := c.Clone()
	*d.Agent.CodexModel = "x"
	if *c.Agent.CodexModel != "gpt-6-sol" {
		t.Fatal("not a deep copy")
	}
	b, err := c.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseConfig(b)
	if err != nil || !back.Equal(c) {
		t.Fatal(string(b), err)
	}
}
