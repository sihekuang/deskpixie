package agent

import (
	"context"
	"time"
)

// Test-only views of runtime internals (Rust's tests reach these as pub(crate)).

// CodexTrustScreenForTest is Codex's folder-trust screen (recipes_test.go's fixture).
const CodexTrustScreenForTest = codexTrustScreen

func CloseStartedForTest(session SessionHost, agent, pane string) {
	closeStarted(context.Background(), session, agent, pane)
}

func UnknownAgentsForTest(listed, known []string) []string { return unknownAgents(listed, known) }

// ExistingUserSessionsForTest and ExistingSessionMessageForTest are issue #126's matching and
// messaging logic, for package agent_test.
func ExistingUserSessionsForTest(ctx context.Context, host SessionHost, repoFolder string) []AgentInfo {
	return existingUserSessions(ctx, host, repoFolder)
}

func ExistingSessionMessageForTest(matches []AgentInfo, repoFolder string) string {
	return existingSessionMessage(matches, repoFolder)
}

// SetConfirmWaitForTest shortens how long a session tool waits for a confirm before it answers.
func SetConfirmWaitForTest(h *AgentHandle, d time.Duration) { h.confirmWait = d }

// LateWorkerOpForTest applies a worker effect as one arriving after Shutdown cleared the lanes
// would (#352) and says whether it made the worker a new lane.
func LateWorkerOpForTest(h *AgentHandle, e Effect) bool {
	h.shuttingDown.Store(true)
	h.lanesMu.Lock()
	for _, l := range h.lanes {
		l.close()
	}
	clear(h.lanes)
	h.lanesMu.Unlock()
	h.workerOp(e)
	h.lanesMu.Lock()
	defer h.lanesMu.Unlock()
	_, made := h.lanes[e.Name]
	return made
}
