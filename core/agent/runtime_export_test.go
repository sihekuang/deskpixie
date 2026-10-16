package agent

import "context"

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
