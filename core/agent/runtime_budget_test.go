package agent_test

import (
	"testing"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/agent/httpserver"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/tmux"
)

// The budget relations Rust enforces at compile time (`const _: () = assert!(…)`) across
// crates. The Go packages copy the constants, so these tests keep them in step.

// runtime.rs: the timing constants, verbatim.
func TestTheRuntimeBudgetsMatchRust(t *testing.T) {
	for name, c := range map[string][2]time.Duration{
		"Tick":             {Tick, time.Second},
		"ShutdownLimit":    {ShutdownLimit, 3 * time.Second},
		"OpLimit":          {OpLimit, 15 * time.Second},
		"StartOpLimit":     {StartOpLimit, 90 * time.Second},
		"WorkerStartLimit": {WorkerStartLimit, 128 * time.Second},
		"EnsureBudget":     {EnsureBudget, 55 * time.Second},
		"PostStartCheck":   {PostStartCheck, 5 * time.Second},
		"RepoLimit":        {RepoLimit, 10 * time.Second},
		"ListLimit":        {ListLimit, 10 * time.Second},
		"McpCallLimit":     {McpCallLimit, 150 * time.Second},
		"WorkerStopGrace":  {WorkerStopGrace, 5 * time.Second},
		"DialogWait":       {DialogWait, 10 * time.Second},
		"ReadyWindow":      {ReadyWindow, 55 * time.Second},
		"StartScreenPoll":  {StartScreenPoll, 300 * time.Millisecond},
		"StopGrace":        {StopGrace, 500 * time.Millisecond},
	} {
		if c[0] != c[1] {
			t.Errorf("%s = %v, Rust has %v", name, c[0], c[1])
		}
	}
	if LiveWorktreesFile != "live-worktrees.jsonl" {
		t.Error(LiveWorktreesFile)
	}
}

// runtime.rs const asserts.
func TestAWorkerStartFitsInItsLimit(t *testing.T) {
	if !(EnsureBudget+DialogWait+PostStartCheck < WorkerStartLimit) {
		t.Error("EnsureBudget + DialogWait + PostStartCheck < WorkerStartLimit")
	}
	if !(EnsureBudget+ReadyWindow+PostStartCheck < WorkerStartLimit) {
		t.Error("EnsureBudget + ReadyWindow + PostStartCheck < WorkerStartLimit")
	}
}

// agent-http mcp.rs: MCP_CALL_LIMIT == agent_core::runtime::MCP_CALL_LIMIT.
func TestTheHookServerWaitsAsLongAsTheRuntime(t *testing.T) {
	if httpserver.McpCallLimit != McpCallLimit {
		t.Fatalf("httpserver.McpCallLimit %v != agent.McpCallLimit %v", httpserver.McpCallLimit, McpCallLimit)
	}
}

// agent-fs worktree.rs uses agent_core::runtime::ENSURE_BUDGET directly; Go copies it.
func TestTheWorktreeBudgetIsTheRuntimes(t *testing.T) {
	if agentfs.EnsureBudget != EnsureBudget {
		t.Fatalf("agentfs.EnsureBudget %v != agent.EnsureBudget %v", agentfs.EnsureBudget, EnsureBudget)
	}
	if !(agentfs.EnsureBudget+DialogWait+PostStartCheck < WorkerStartLimit) {
		t.Fatal("agentfs.EnsureBudget + DialogWait + PostStartCheck < WorkerStartLimit")
	}
}

// session-herdr host.rs const asserts.
func TestAWorkerStartFitsInTheSessionHostsBudget(t *testing.T) {
	if !(EnsureBudget+herdr.PaneBusyLimit+herdr.StartTimeout+DialogWait+PostStartCheck < WorkerStartLimit) {
		t.Error("EnsureBudget + PaneBusyLimit + StartTimeout + DialogWait + PostStartCheck < WorkerStartLimit")
	}
	if !(ReadyWindow <= herdr.PaneBusyLimit+herdr.StartTimeout+DialogWait) {
		t.Error("ReadyWindow <= PaneBusyLimit + StartTimeout + DialogWait")
	}
	if !(WorkerStartLimit < McpCallLimit) {
		t.Error("WorkerStartLimit < McpCallLimit")
	}
	if !(RepoLimit+ListLimit+WorkerStartLimit < McpCallLimit) {
		t.Error("RepoLimit + ListLimit + WorkerStartLimit < McpCallLimit")
	}
}

// The tmux host's start fits the same budgets (it has no pane-busy retry).
func TestAWorkerStartFitsInTheTmuxHostsBudget(t *testing.T) {
	if !(EnsureBudget+tmux.StartTimeout+DialogWait+PostStartCheck < WorkerStartLimit) {
		t.Error("EnsureBudget + tmux.StartTimeout + DialogWait + PostStartCheck < WorkerStartLimit")
	}
	if !(ReadyWindow <= tmux.StartTimeout+DialogWait) {
		t.Error("ReadyWindow <= tmux.StartTimeout + DialogWait")
	}
	if !(tmux.StartTimeout < StartOpLimit) {
		t.Error("tmux.StartTimeout < StartOpLimit")
	}
}

// AgentHandle is the hook server's runtime.
var _ httpserver.Runtime = (*AgentHandle)(nil)
