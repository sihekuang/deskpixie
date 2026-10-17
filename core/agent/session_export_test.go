package agent

import "context"

// SessionToolForTest runs a session tool for package agent_test, masking nothing.
func SessionToolForTest(ctx context.Context, host SessionHost, call McpCall) McpReply {
	return sessionTool(ctx, host, call, func(s string) string { return s })
}

// SessionToolMaskedForTest runs a session tool with mask applied to what sessions wrote.
var SessionToolMaskedForTest = sessionTool
