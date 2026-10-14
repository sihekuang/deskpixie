package agent

// Test-only views of AgentHost internals (the Rust tests read these fields directly).

func (h *AgentHost) RecreationsForTest() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.recreations)
}

func (h *AgentHost) ResumeEarlierRunForTest() bool { return h.resumeEarlierRun.Load() }
