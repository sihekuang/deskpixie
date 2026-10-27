package voice

// Tools the voice model may call. New tools are registered, never hard-coded.
// Port of ai-mascot crates/voice-core/src/tools.rs (+ the Tool trait in traits.rs).

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

// EndConversationTool is the name of the built-in tool that ends the conversation.
const EndConversationTool = "end_conversation"

// ToolOutcome is what a tool returns.
type ToolOutcome struct {
	Output json.RawMessage
	// EndConversation: end the conversation once the current reply has finished playing.
	EndConversation bool
	// EndAfterReply: the model still replies to the result; the conversation ends once that
	// reply has finished playing (the user talking first cancels it).
	EndAfterReply bool
}

// Tool is something the model can call. Invoke runs on its own goroutine and may take long
// (e.g. delegation); ctx is cancelled when the runtime shuts down.
type Tool interface {
	Name() string
	Description() string
	// Parameters is the JSON schema of the arguments object.
	Parameters() json.RawMessage
	Invoke(ctx context.Context, args json.RawMessage) ToolOutcome
}

// BeginningTool is a Tool whose call must be under way before a later call in the same turn
// runs (#367: take_clipboard holds back the delegate that sends what it attaches). The runtime
// calls Begin on its loop, in call order, and runs what Begin returns on the tool's goroutine
// in place of Invoke.
type BeginningTool interface {
	Tool
	Begin(ctx context.Context, args json.RawMessage) func() ToolOutcome
}

// ToolRegistry holds tools by name. Safe for concurrent use.
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewToolRegistry is an empty registry.
func NewToolRegistry() *ToolRegistry { return &ToolRegistry{tools: map[string]Tool{}} }

// DefaultTools holds the tools every conversation has: end_conversation.
func DefaultTools() *ToolRegistry {
	r := NewToolRegistry()
	r.Register(EndConversation{})
	return r
}

// Register adds a tool, replacing any tool with the same name.
func (r *ToolRegistry) Register(t Tool) {
	r.mu.Lock()
	r.tools[t.Name()] = t
	r.mu.Unlock()
}

// Get finds a tool by name.
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Specs are the session config's tool specs, sorted by name.
func (r *ToolRegistry) Specs() []ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, ToolSpec{Name: t.Name(), Description: t.Description(), Parameters: t.Parameters()})
	}
	slices.SortFunc(out, func(a, b ToolSpec) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// EndConversation lets the model end the conversation when the user is clearly done.
type EndConversation struct{}

func (EndConversation) Name() string { return EndConversationTool }

func (EndConversation) Description() string {
	return "End the voice conversation. Call only when the user clearly signals they are done " +
		"(e.g. \"thanks, that's all\", \"bye\"). Say a short goodbye before calling it."
}

func (EndConversation) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

func (EndConversation) Invoke(context.Context, json.RawMessage) ToolOutcome {
	return ToolOutcome{Output: json.RawMessage(`{"ok":true}`), EndConversation: true}
}
