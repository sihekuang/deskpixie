package agent

import (
	"context"
	"time"
)

// The seams of the delegation core (port of agent-core traits.rs). Core logic depends only on
// these; adapters implement them. Rust's async methods take a context here.

// SessionHost is a terminal multiplexer session holding the agents (Herdr now).
// Focuser is a SessionHost that can bring an agent's tab to the front of the session, so a
// terminal attached to it shows that agent (optional: Herdr has it).
type Focuser interface {
	Focus(ctx context.Context, agent string) error
}

type SessionHost interface {
	// Name is the registry name, e.g. "herdr".
	Name() string
	// EnsureSession: the named session is running and ready for tabs. Idempotent.
	EnsureSession(ctx context.Context) error
	// StartAgent opens a new tab running the agent and waits until its TUI is ready.
	StartAgent(ctx context.Context, spec AgentSpec) (AgentInfo, error)
	// ReplaceAgent starts spec in place of a running agent of the same name (its pane closes).
	// DefaultReplaceAgent closes first, then starts; a host whose pane ids are reused opens the
	// new tab before closing the old pane, so a late close can't end the new agent.
	ReplaceAgent(ctx context.Context, spec AgentSpec) (AgentInfo, error)
	// Prompt types text into the agent and submits it.
	Prompt(ctx context.Context, agent, text string) error
	// SendKeys sends key presses, e.g. ["Escape"].
	SendKeys(ctx context.Context, agent string, keys []string) error
	// Read returns the last lines of the agent's screen, without ANSI codes.
	Read(ctx context.Context, agent string, lines uint16) (string, error)
	// Close closes the agent's pane (the agent process ends).
	Close(ctx context.Context, agent string) error
	// CloseExact closes agent's pane only if it is still pane (as StartAgent returned it): a
	// newer agent of the same name is left alone, and a pane already gone is fine.
	// DefaultCloseExact looks the pane up with List.
	CloseExact(ctx context.Context, agent, pane string) error
	List(ctx context.Context) ([]AgentInfo, error)
	// Subscribe returns status changes and exits of named agents, without replayed duplicates.
	Subscribe() <-chan PaneEvent
	// AttachCommand is the argv that attaches a terminal to the session.
	AttachCommand() []string
}

// DefaultReplaceAgent is Rust's default replace_agent: close (NoAgent is fine), then start.
func DefaultReplaceAgent(ctx context.Context, h SessionHost, spec AgentSpec) (AgentInfo, error) {
	if err := h.Close(ctx, spec.Name); err != nil && !IsHostError(err, HostNoAgent) {
		return AgentInfo{}, err
	}
	return h.StartAgent(ctx, spec)
}

// DefaultCloseExact is Rust's default close_exact: close only if List still shows pane.
func DefaultCloseExact(ctx context.Context, h SessionHost, agent, pane string) error {
	agents, err := h.List(ctx)
	if err != nil {
		return err
	}
	for _, a := range agents {
		if a.Name == agent {
			if a.Pane == pane {
				return h.Close(ctx, agent)
			}
			return nil
		}
	}
	return nil
}

// Attach is what a terminal opens: the attach command and the folder it starts in.
type Attach struct {
	Argv []string
	Dir  string // absolute starting folder; "" = the terminal's default
}

// TerminalLauncher opens a terminal tab or window running an Attach (Warp, iTerm).
type TerminalLauncher interface {
	Name() string
	Open(a Attach) error // LaunchError
}

// VoiceLink is the delegation side's only view of the voice loop.
type VoiceLink interface {
	IsOpen() bool
	Inject(notice Notice)
	HoldOpen(on bool)
}

type Notifier interface {
	Chime()
	Notify(title, body string)
}

type MascotCue interface {
	Cue(cue Cue)
}

// StatusSink is the tray's one-line agent status, e.g. "Agent: herdr not found"; "" clears it.
type StatusSink interface {
	SetStatus(line string)
}

// TrustStore pre-accepts the agent CLI's folder-trust dialog for dir; returns the paths it trusted.
type TrustStore interface {
	Grant(dir string) ([]string, error) // TrustError
}

// WorktreeFs is git worktrees for workers: the only place the app writes into a user's repo.
type WorktreeFs interface {
	// ResolveRepo: repo (a folder name under the projects root, or a path) as a canonical git
	// repository root strictly inside the projects root, and not the coordinator's workspace.
	ResolveRepo(repo string) (string, error) // WorktreeError
	// Ensure: `<repo>/.worktrees/<slug>` on branch `mascot/<slug>` (an existing one is reused),
	// with `.worktrees/` in the repo's `.git/info/exclude`. Returns the worktree's canonical path.
	Ensure(repo, slug string) (string, error) // WorktreeError
	// Audit re-checks the git links of `<repo>/.worktrees/<slug>`, which a sandboxed worker
	// could write; an error when the admin folder can't be identified safely (nothing changed).
	Audit(repo, slug string) error
	// ForeignSlugs: names in `<repo>/.worktrees/` that are not a worktree on `mascot/<name>`.
	ForeignSlugs(repo string) []string
}

// TokenIssuer is per-agent bearer tokens for the hook/MCP server (in memory, never logged).
type TokenIssuer interface {
	// Issue returns a fresh token for identity, replacing any earlier one.
	Issue(identity string) string
	Revoke(identity string)
	// Restore makes a kept token identity's again (an agent left running over a quit).
	Restore(identity, token string)
	// Token is identity's current token, if it has one.
	Token(identity string) (string, bool)
}

// WorkerRecipes says how each worker kind is launched: the files its CLI needs and its spec.
type WorkerRecipes interface {
	// Prepare writes what the agent's CLI needs and returns the launch spec; an error for a
	// kind it can't launch.
	Prepare(launch WorkerLaunch) (PreparedWorker, error)
	// Discard deletes what Prepare wrote for name (its pane closed). Idempotent.
	Discard(name string)
}

// Store is append-only line files (jsonl) under the app's agent data folder.
type Store interface {
	// Load returns every line of file; empty if it doesn't exist.
	Load(file string) []string
	Append(file, line string)
	// Rewrite replaces the whole file (compaction at start).
	Rewrite(file string, lines []string)
}

type Clock interface {
	NowMs() Millis
}

type IdSource interface {
	JobID() JobID
}

// CommandOutput is a finished process's status and output.
type CommandOutput struct {
	Status int32
	Stdout string
	Stderr string
}

// CommandRunner runs external programs (used by adapters; the core never runs processes).
type CommandRunner interface {
	// Run runs to completion with exactly env (nothing inherited from the parent).
	Run(argv []string, env []EnvVar) (CommandOutput, error)
	// RunBy is Run that also gives up at deadline when that comes before the runner's own
	// timeout. DefaultRunBy ignores the deadline (callers check the time between calls too).
	RunBy(argv []string, env []EnvVar, deadline time.Time) (CommandOutput, error)
	// SpawnDetached starts a detached process with exactly env and doesn't wait.
	SpawnDetached(argv []string, env []EnvVar) error
}

// DefaultRunBy is Rust's default run_by: the deadline is ignored.
func DefaultRunBy(r CommandRunner, argv []string, env []EnvVar, _ time.Time) (CommandOutput, error) {
	return r.Run(argv, env)
}

// ErrNoAgent builds HostError{Kind: HostNoAgent}.
func ErrNoAgent(name string) error { return HostError{Kind: HostNoAgent, Detail: name} }
