package voice

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// Port of the 5 #[test]s in ai-mascot crates/voice-core/src/tools.rs.

type echoTool struct{}

func (echoTool) Name() string                { return "echo" }
func (echoTool) Description() string         { return "Echoes its arguments." }
func (echoTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (echoTool) Invoke(_ context.Context, args json.RawMessage) ToolOutcome {
	return ToolOutcome{Output: args}
}

func specNames(r *ToolRegistry) []string {
	var out []string
	for _, s := range r.Specs() {
		out = append(out, s.Name)
	}
	return out
}

func TestDefaultsContainEndConversation(t *testing.T) {
	if got := specNames(DefaultTools()); !slices.Equal(got, []string{EndConversationTool}) {
		t.Fatalf("got %v", got)
	}
}

func TestRegisteringAddsAndReplacesByName(t *testing.T) {
	r := DefaultTools()
	r.Register(echoTool{})
	r.Register(echoTool{})
	if got := specNames(r); !slices.Equal(got, []string{"echo", "end_conversation"}) {
		t.Fatalf("got %v", got)
	}
}

func TestUnknownToolIsNone(t *testing.T) {
	if _, ok := NewToolRegistry().Get("nope"); ok {
		t.Fatal("found an unknown tool")
	}
}

func TestEndConversationSpecTakesNoArguments(t *testing.T) {
	spec := DefaultTools().Specs()[0]
	var got, want any
	_ = json.Unmarshal(spec.Parameters, &got)
	_ = json.Unmarshal([]byte(`{"type":"object","properties":{},"additionalProperties":false}`), &want)
	if !jsonEqual(got, want) {
		t.Fatalf("parameters %s", spec.Parameters)
	}
	if !strings.Contains(spec.Description, "done") {
		t.Fatalf("description %q", spec.Description)
	}
}

func TestEndConversationAsksToEnd(t *testing.T) {
	tool, ok := DefaultTools().Get(EndConversationTool)
	if !ok {
		t.Fatal("missing")
	}
	out := tool.Invoke(context.Background(), json.RawMessage(`{}`))
	if !out.EndConversation || string(out.Output) != `{"ok":true}` {
		t.Fatalf("got %+v (%s)", out, out.Output)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
