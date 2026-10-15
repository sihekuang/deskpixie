package agent

import (
	"context"
	"log"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// AgentHost (spec §5.2) starts the coordinator in the session, re-creates it when its pane is
// gone, and gives up after repeated crashes. Built only on SessionHost.
// Port of ai-mascot crates/agent-core/src/host.rs.

const (
	// MaxRecreations re-creations within RecreationWindowMs are allowed; one more gives up.
	MaxRecreations = 3
	// RecreationWindowMs is the window MaxRecreations counts in.
	RecreationWindowMs Millis = 5 * 60_000
	// StopGrace is between Escape and closing the pane when stopping an agent.
	StopGrace = 500 * time.Millisecond
)

// CoordinatorRecipe is how the coordinator is launched (built by the composition root).
type CoordinatorRecipe struct {
	// Cwd is the app-owned workspace (`<app_data>/agent/workspace`).
	Cwd string
	// Kind is the coordinator's agent (Claude, the zero value, or Codex: #87).
	Kind AgentKind
	// Args are the agent's arguments, e.g. Claude Code's `--permission-mode auto --settings …`.
	Args []string
	// Env is [PIXIE_AGENT_TOKEN=<token>].
	Env []EnvVar
	// AdoptEarlier: Env's token was kept from the run before (agent.keepPanesOnQuit), so a
	// coordinator found running from that run still holds it and is adopted as it is.
	AdoptEarlier bool
	// ResumeLast: with no coordinator running, the first start resumes the workspace's last
	// conversation (--continue) instead of starting a blank one.
	ResumeLast bool
	// Refusals and Ready are the start's screen checks (as a worker's, see StartRefusal and
	// StartReady): a refusal screen, or a ready marker that never settles, fails the start with
	// HostRefused and closes the pane. None for Claude.
	Refusals []StartRefusal
	Ready    *StartReady
}

// Ensured is EnsureCoordinator's result: Started false is Rust's Running(Status) (it was
// already running); Started true was started now, Resumed when launched with --continue.
type Ensured struct {
	Started bool
	Status  PaneStatus
	Resumed bool
}

// AgentHost owns the coordinator's lifecycle. Safe for concurrent use.
type AgentHost struct {
	host   SessionHost
	clock  Clock
	recipe CoordinatorRecipe

	startedBefore atomic.Bool
	// resumeEarlierRun: a coordinator from an earlier run was found; starts resume its conversation.
	resumeEarlierRun atomic.Bool
	gaveUp           atomic.Bool
	mu               sync.Mutex
	recreations      []Millis
	// ensuring serializes EnsureCoordinator so concurrent callers cannot start two
	// coordinators. A channel (not a mutex) so a caller whose context ends stops waiting.
	ensuring chan struct{}
}

func NewAgentHost(host SessionHost, clock Clock, recipe CoordinatorRecipe) *AgentHost {
	h := &AgentHost{host: host, clock: clock, recipe: recipe, ensuring: make(chan struct{}, 1)}
	// A failed start with --continue gives this up, as for an adopted coordinator.
	h.resumeEarlierRun.Store(recipe.ResumeLast)
	return h
}

// Token is the coordinator's bearer token (its recipe's PIXIE_AGENT_TOKEN, or the pre-#68
// MASCOT_AGENT_TOKEN), "" if none.
func (h *AgentHost) Token() string {
	for _, e := range h.recipe.Env {
		if slices.Contains(TokenEnvs, e.Key) {
			return e.Value
		}
	}
	return ""
}

// Kind is the coordinator's agent kind.
func (h *AgentHost) Kind() AgentKind { return h.recipe.Kind }

// Session is the session host the coordinator runs in.
func (h *AgentHost) Session() SessionHost { return h.host }

// EnsureCoordinator: the session is running and the coordinator pane exists (started if
// missing). Any start after the first (a crash, or a deliberate StopCoordinator) resumes the
// conversation with --continue. Only a successful start counts as a start or re-creation.
//
// A coordinator found running before this process started one was launched by an earlier run
// (kept on quit, or left by a crash): its hooks carry that run's token, which this run rejects.
// It is replaced, once, by one started with --continue (same conversation, this run's token).
func (h *AgentHost) EnsureCoordinator(ctx context.Context) (Ensured, error) {
	select {
	case h.ensuring <- struct{}{}:
	case <-ctx.Done():
		return Ensured{}, ctx.Err()
	}
	defer func() { <-h.ensuring }()
	if h.gaveUp.Load() {
		return Ensured{}, HostError{Kind: HostGaveUp}
	}
	if err := h.host.EnsureSession(ctx); err != nil {
		return Ensured{}, err
	}
	startedBefore := h.startedBefore.Load()
	agents, err := h.host.List(ctx)
	if err != nil {
		return Ensured{}, err
	}
	idx := slices.IndexFunc(agents, func(a AgentInfo) bool { return a.Name == Coordinator })
	if idx >= 0 && (startedBefore || h.recipe.AdoptEarlier) {
		// Kept over a quit with this run's (restored) token: it is this run's coordinator now.
		h.startedBefore.Store(true)
		return Ensured{Status: agents[idx].Status}, nil
	}
	adopted := idx >= 0
	if adopted {
		// From here on, any start resumes that conversation (even if this one fails).
		h.resumeEarlierRun.Store(true)
	}
	resumed := startedBefore || h.resumeEarlierRun.Load()
	if startedBefore {
		now := h.clock.NowMs()
		h.mu.Lock()
		h.recreations = slices.DeleteFunc(h.recreations, func(t Millis) bool { return satSub(now, t) >= RecreationWindowMs })
		full := len(h.recreations) >= MaxRecreations
		h.mu.Unlock()
		if full {
			h.gaveUp.Store(true)
			return Ensured{}, HostError{Kind: HostGaveUp}
		}
	}
	info, err := h.launch(ctx, adopted, resumed)
	if resumed && err != nil {
		// Any failed start with --continue gives up on an earlier run's conversation: the next
		// attempt starts fresh.
		h.resumeEarlierRun.Store(false)
		// `claude --continue` with no conversation to continue exits at once (an adopted pane
		// that never had a prompt): only then start fresh now, in the same attempt.
		if IsHostError(err, HostExitedAtStart) {
			log.Print("agent: the coordinator exited with --continue; starting a new conversation")
			resumed = false
			info, err = h.launch(ctx, adopted, false)
		}
	}
	if err != nil {
		return Ensured{}, err
	}
	// Its tab opens in the background: bring it to the front, so a terminal attached to the
	// session (the click that started it, or one already open) shows the coordinator.
	focusQuietly(ctx, h.host, Coordinator)
	h.startedBefore.Store(true)
	if startedBefore {
		h.mu.Lock()
		h.recreations = append(h.recreations, h.clock.NowMs())
		h.mu.Unlock()
	}
	return Ensured{Started: true, Status: info.Status, Resumed: resumed}, nil
}

// launch is one coordinator start (replacing an adopted one), resuming its last conversation
// if resume: Claude Code's --continue, or Codex's `resume --last` (the newest session in the
// workspace; with none, Codex starts a new one).
func (h *AgentHost) launch(ctx context.Context, replace, resume bool) (AgentInfo, error) {
	args := slices.Clone(h.recipe.Args)
	if resume {
		if h.recipe.Kind == Codex {
			args = append([]string{"resume", "--last"}, args...)
		} else {
			args = append(args, "--continue")
		}
	}
	spec := AgentSpec{Name: Coordinator, Kind: h.recipe.Kind, Cwd: h.recipe.Cwd, Args: args, Env: slices.Clone(h.recipe.Env)}
	began := time.Now()
	var info AgentInfo
	var err error
	if replace {
		info, err = h.host.ReplaceAgent(ctx, spec)
	} else {
		info, err = h.host.StartAgent(ctx, spec)
	}
	if err != nil {
		return info, err
	}
	if err := checkStartScreen(ctx, h.host, Coordinator, h.recipe.Refusals, h.recipe.Ready, info.Status, began); err != nil {
		if ctx.Err() != nil {
			return AgentInfo{}, ctx.Err()
		}
		// Nothing was typed into it; don't leave it to be adopted later.
		closeBounded(ctx, Coordinator, func(c context.Context) error { return h.host.Close(c, Coordinator) })
		return AgentInfo{}, HostError{Kind: HostRefused, Detail: err.Error()}
	}
	return info, nil
}

// Reset is tray ▸ Restart coordinator: forget the crash history and stop giving up.
func (h *AgentHost) Reset() {
	h.gaveUp.Store(false)
	h.mu.Lock()
	h.recreations = nil
	h.mu.Unlock()
}

// StopCoordinator sends Escape, then closes the pane after StopGrace. A missing pane is fine.
func (h *AgentHost) StopCoordinator(ctx context.Context) error {
	if err := h.host.SendKeys(ctx, Coordinator, []string{"Escape"}); err != nil {
		if IsHostError(err, HostNoAgent) {
			return nil
		}
		return err
	}
	select {
	case <-time.After(StopGrace):
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := h.host.Close(ctx, Coordinator); err != nil && !IsHostError(err, HostNoAgent) {
		return err
	}
	return nil
}

// AttachCommand is the argv that attaches a terminal to the session.
func (h *AgentHost) AttachCommand() []string { return h.host.AttachCommand() }

// focusQuietly focuses agent if the host can; a failure only means the tab stays where it is.
func focusQuietly(ctx context.Context, host SessionHost, agent string) {
	if f, ok := host.(Focuser); ok {
		if err := f.Focus(ctx, agent); err != nil && !IsHostError(err, HostNoAgent) {
			log.Printf("agent: could not focus %s: %v", agent, err)
		}
	}
}
