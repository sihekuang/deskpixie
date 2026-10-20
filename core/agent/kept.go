package agent

// What a quit with agent.keepPanesOnQuit leaves running, for the next run to adopt: the
// coordinator's token and each live worker's plan, worktree and token. Without them the next
// run could not tell its agents from strangers (tokens otherwise live one run, in memory).

// KeptAgents is the kept-agents file (kept.json). Zero = nothing kept.
type KeptAgents struct {
	// CoordinatorToken is set when the coordinator was running at the quit.
	CoordinatorToken string `json:"coordinatorToken,omitempty"`
	// CoordinatorKind is that coordinator's agent (#87; omitted for Claude, as in every file
	// from before it). A coordinator of another kind than the one configured is not adopted.
	CoordinatorKind AgentKind `json:"coordinatorKind,omitempty"`
	// CoordinatorStamp is what that coordinator was started with (CoordinatorRecipe.Stamp,
	// #189): a run that would start it differently replaces it. "" in files from before it.
	CoordinatorStamp string       `json:"coordinatorStamp,omitempty"`
	Workers          []KeptWorker `json:"workers,omitempty"`
}

// KeptWorker is one worker left running.
type KeptWorker struct {
	Plan     WorkerPlan `json:"plan"`
	Worktree string     `json:"worktree"`
	Token    string     `json:"token"`
}

// KeptStore saves the kept-agents file (private: it holds bearer tokens).
type KeptStore interface {
	Save(KeptAgents) error
}
