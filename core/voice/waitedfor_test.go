package voice

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// gateTool is waited for, and answers once open is closed.
type gateTool struct{ open chan struct{} }

func (gateTool) Name() string                { return "gate" }
func (gateTool) Description() string         { return "Gate." }
func (gateTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (gateTool) WaitedFor()                  {}
func (g gateTool) Invoke(context.Context, json.RawMessage) ToolOutcome {
	<-g.open
	return ToolOutcome{Output: json.RawMessage(`{"gate":true}`)}
}

// nextTool answers at once.
type nextTool struct{}

func (nextTool) Name() string                { return "next" }
func (nextTool) Description() string         { return "Next." }
func (nextTool) Parameters() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (nextTool) Invoke(context.Context, json.RawMessage) ToolOutcome {
	return ToolOutcome{Output: json.RawMessage(`{"next":true}`)}
}

func waitedForRuntime(gate gateTool) *Runtime {
	tools := NewToolRegistry()
	tools.Register(gate)
	tools.Register(nextTool{})
	return &Runtime{tools: tools, toolDone: make(chan toolResult, 4), done: make(chan struct{}), ctx: context.Background()}
}

func result(t *testing.T, r *Runtime) string {
	t.Helper()
	select {
	case res := <-r.toolDone:
		return res.callID
	case <-time.After(time.Second):
		t.Fatal("no result")
		return ""
	}
}

// #367: a call after a WaitedForTool in the same turn runs only once it has ended (the delegate
// after take_clipboard sends what it attached).
func TestACallWaitsForAWaitedForToolBeforeIt(t *testing.T) {
	gate := gateTool{open: make(chan struct{})}
	r := waitedForRuntime(gate)
	r.invokeTool("1", "gate", nil)
	r.invokeTool("2", "next", nil)
	select {
	case res := <-r.toolDone:
		t.Fatalf("%s answered before the gate opened", res.callID)
	case <-time.After(50 * time.Millisecond):
	}
	close(gate.open)
	if a, b := result(t, r), result(t, r); a != "1" || b != "2" {
		t.Fatal(a, b)
	}
	// Once it has ended, later calls don't wait for it.
	r.invokeTool("3", "next", nil)
	if got := result(t, r); got != "3" || len(r.waited) != 0 {
		t.Fatal(got, len(r.waited))
	}
}

// A call before a WaitedForTool doesn't wait for it.
func TestACallBeforeAWaitedForToolGoesAtOnce(t *testing.T) {
	gate := gateTool{open: make(chan struct{})}
	defer close(gate.open)
	r := waitedForRuntime(gate)
	r.invokeTool("1", "next", nil)
	r.invokeTool("2", "gate", nil)
	if got := result(t, r); got != "1" {
		t.Fatal(got)
	}
}

// Shutting down frees a call waiting on a WaitedForTool that never ends.
func TestAWaitingCallIsFreedOnShutdown(t *testing.T) {
	gate := gateTool{open: make(chan struct{})}
	defer close(gate.open)
	r := waitedForRuntime(gate)
	ctx, cancel := context.WithCancel(context.Background())
	r.ctx = ctx
	r.invokeTool("1", "gate", nil)
	r.invokeTool("2", "next", nil)
	cancel()
	if got := result(t, r); got != "2" {
		t.Fatal(got)
	}
}
