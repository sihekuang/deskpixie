package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// The async driver (spec §7): owns the injected adapters, feeds inputs to the AgentController
// and carries out its effects. It names no concrete types.
// Port of ai-mascot crates/agent-core/src/runtime.rs.

const (
	// Tick: how often the controller is ticked (permission timeouts, the notification cap, the
	// blocked hint).
	Tick = time.Second
	// ShutdownLimit: the longest quit waits for the agent panes to close.
	ShutdownLimit = 3 * time.Second
	// OpLimit: the longest one host call may take before the runtime gives up on it and moves on.
	OpLimit = 15 * time.Second
	// StartOpLimit: the longest a coordinator start may take (server start, the pane-busy
	// retry and the ready poll).
	StartOpLimit = 90 * time.Second
	// WorkerStartLimit: the longest a worker start may take: its worktree (EnsureBudget), its
	// files, its pane (the session host's pane-busy retry and start timeout), the start screen
	// check (DialogWait) and the check that its pane is still there (PostStartCheck).
	WorkerStartLimit = 128 * time.Second
	// EnsureBudget: the longest WorktreeFs.Ensure may take, besides the cleanup of a failed
	// `worktree add`. A start that times out still waits for it.
	EnsureBudget = 55 * time.Second
	// PostStartCheck: the longest the check that a started worker's pane is still there may take.
	PostStartCheck = 5 * time.Second
	// RepoLimit: the longest start_worker may take to resolve its repository.
	RepoLimit = 10 * time.Second
	// ListLimit: the longest start_worker may take to list the session's agents before admitting.
	ListLimit = 10 * time.Second
	// McpCallLimit is httpserver.McpCallLimit (a test keeps them equal): start_worker must
	// answer within it, so RepoLimit + ListLimit + WorkerStartLimit stays below it.
	McpCallLimit = 150 * time.Second
	// WorkerStopGrace is between Escape and closing a worker's pane on stop_worker (spec §5.5).
	WorkerStopGrace = 5 * time.Second
	// DialogWait: how long the start screen is watched once the pane is started: for a refusal
	// screen on a blocked pane, and at least this long for a ready screen.
	DialogWait = 10 * time.Second
	// ReadyWindow: a start that waits for a ready screen watches it until this long after the
	// pane's start began (or DialogWait after it started, if later).
	ReadyWindow = 55 * time.Second
	// StartScreenPoll is between two reads of the start screen.
	StartScreenPoll = 300 * time.Millisecond
)

// LiveWorktreesFile holds the live workers' repos and worktree slugs (`{"repo":…,"slug":…}`
// lines), kept in step with the known workers: after a crash, their git links are re-checked.
const LiveWorktreesFile = "live-worktrees.jsonl"

// Deps is everything the runtime needs, injected by a composition root.
type Deps struct {
	Host     *AgentHost
	Voice    VoiceLink
	Notifier Notifier
	Mascot   MascotCue
	Status   StatusSink
	Launcher TerminalLauncher
	// TerminalDir is the folder the agent terminal starts in (the projects folder, else home).
	TerminalDir string
	Store       Store
	Clock       Clock
	IDs         IdSource
	// Kept saves what a quit with keepPanesOnQuit leaves running (nil: nothing is saved).
	Kept KeptStore
}

// WorkerDeps are the worker adapters (spec §5.5), injected only when agent.workers.enabled.
type WorkerDeps struct {
	Worktrees WorktreeFs
	Tokens    TokenIssuer
	Recipes   WorkerRecipes
}

// RuntimeConfig configures a runtime.
type RuntimeConfig struct {
	Settings Settings
	// Warm (agent.warm): start the coordinator at app start instead of on the first delegate.
	Warm bool
	// SetupProblem: a setup problem found by the composition root (the tray status line);
	// delegation starts off. "" = none.
	SetupProblem string
	// Notice: a setup note that disables nothing (e.g. "Agent: workers off: …"), shown while
	// nothing else is. "" = none.
	Notice string
	// Kept is what the last run left running (keepPanesOnQuit): its coordinator token (already
	// the recipe's, with AdoptEarlier) keeps the open jobs open; its workers are adopted.
	Kept KeptAgents
}

// hostOp is a call into the session host for the coordinator. They all go through one
// goroutine so they reach the host in the order the controller emitted them (a Prompt never
// overtakes the Interrupt before it).
type hostOpKind int

const (
	opStart hostOpKind = iota
	opPrompt
	opInterrupt
	opStop    // Escape, then close the pane
	opRelabel // tab: the coordinator's tab label
)

type hostOp struct {
	kind hostOpKind
	text string
	tab  TabLabel
}

// mainItem is one thing for the main loop: an effect to carry out or a hook event.
type mainItem struct {
	effect *Effect
	hook   *HookEvent
}

type repoSlug struct{ repo, slug string }

var (
	errQuitting = errors.New("The app is quitting.")
	errTimedOut = errors.New("timeout")
)

// AgentHandle is the app's handle on the delegation runtime. Make it with NewRuntime (or
// NewRuntimeWithWorkers), then call Run once. Safe for concurrent use. It is the hook server's
// httpserver.Runtime (Deliver).
type AgentHandle struct {
	deps      Deps
	workers   *WorkerDeps
	keepPanes bool
	ctx       context.Context
	cancel    context.CancelFunc

	ctlMu sync.Mutex
	ctl   *AgentController

	main  *queue[mainItem]
	ops   *queue[hostOp]
	panes <-chan PaneEvent

	lanesMu sync.Mutex
	// lanes: per worker, its host calls in the order the controller emitted them.
	lanes map[string]*queue[Effect]

	knownMu sync.Mutex
	// known: each admitted worker's repo and worktree slug, until it is forgotten (its
	// worktree's git links are re-checked then).
	known map[string]repoSlug

	closingMu sync.Mutex
	// closing: names of failed starts whose pane is still being closed; taken until it is.
	closing map[string]bool

	repliesMu sync.Mutex
	// replies: PermissionRequest hooks waiting for a decision.
	replies     map[RequestID]chan<- Decision
	nextRequest atomic.Uint64

	// left: live-worktrees lines left by a crash, re-checked when Run starts.
	left []string
	// rechecked is closed once the start-up re-check of worktrees left by a crash (and the
	// adoption of kept workers) is done: no worker is admitted before.
	rechecked chan struct{}
	// kept: workers the last run left running, adopted when Run starts.
	kept         []KeptWorker
	straysLogged atomic.Bool
	// shuttingDown is set first thing in Shutdown: from then on inputs are ignored and host
	// calls other than the final stops are skipped.
	shuttingDown atomic.Bool
}

// NewRuntime builds the runtime without workers (start_worker is refused).
func NewRuntime(deps Deps, cfg RuntimeConfig) *AgentHandle {
	return NewRuntimeWithWorkers(deps, cfg, nil)
}

// NewRuntimeWithWorkers builds the runtime; workers are the worker adapters (nil: off). The
// stored jobs and outbox are restored now; call Run to start carrying out effects.
func NewRuntimeWithWorkers(deps Deps, cfg RuntimeConfig, workers *WorkerDeps) *AgentHandle {
	ctx, cancel := context.WithCancel(context.Background())
	h := &AgentHandle{
		deps: deps, workers: workers, keepPanes: cfg.Settings.KeepPanesOnQuit,
		ctx: ctx, cancel: cancel,
		ctl:  NewAgentController(cfg.Settings),
		main: newQueue[mainItem](), ops: newQueue[hostOp](),
		panes:     deps.Host.Session().Subscribe(),
		lanes:     map[string]*queue[Effect]{},
		known:     map[string]repoSlug{},
		closing:   map[string]bool{},
		replies:   map[RequestID]chan<- Decision{},
		rechecked: make(chan struct{}),
		kept:      cfg.Kept.Workers,
	}
	jobs := deps.Store.Load(JobsFile)
	// Worktrees of workers that were live when the app last ended without forgetting them (a
	// crash): re-checked in Run, and forgotten.
	if workers != nil {
		h.left = deps.Store.Load(LiveWorktreesFile)
	}
	outbox := deps.Store.Load(OutboxFile)
	keptCoordinator := cfg.Kept.CoordinatorToken != ""
	restored := RestoredInput(jobs, outbox, keptCoordinator)
	restored.Memories = deps.Store.Load(MemoriesFile)
	h.Input(restored)
	if len(h.left) > 0 {
		deps.Store.Rewrite(LiveWorktreesFile, []string{})
	}
	// Before a warm start, so a disabled app never starts the coordinator.
	if cfg.Notice != "" {
		h.Input(NoticeInput(cfg.Notice))
	}
	if cfg.SetupProblem != "" {
		h.Input(DisableInput(cfg.SetupProblem))
	}
	// A kept coordinator with open jobs is asked for their results again at once.
	if cfg.Warm || (keptCoordinator && h.hasOpenJobs()) {
		h.Input(EnsureCoordinatorInput())
	}
	return h
}

// Run carries out effects, pane events, hook events and the 1 s tick until ctx ends. Call it
// once (on its own goroutine).
func (h *AgentHandle) Run(ctx context.Context) {
	defer h.cancel()
	go h.recheck()
	// One goroutine runs the coordinator's host calls one at a time, in the order queued.
	go func() {
		for {
			op, ok := h.ops.pop(h.ctx)
			if !ok {
				return
			}
			h.runOp(op)
		}
	}()
	tick := time.NewTicker(Tick)
	defer tick.Stop()
	panes := h.panes
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.ctx.Done():
			return
		case <-h.main.sig:
			for {
				item, ok := h.main.tryPop()
				if !ok {
					break
				}
				if item.effect != nil {
					h.apply(*item.effect)
				} else {
					h.hook(*item.hook)
				}
			}
		case ev, ok := <-panes:
			if !ok {
				panes = nil
				continue
			}
			h.Input(PaneInput(ev))
		case <-tick.C:
			h.Input(TickInput())
		}
	}
}

// recheck re-checks the worktrees left by a crash and adopts the kept workers, then lets
// workers be admitted.
func (h *AgentHandle) recheck() {
	if h.workers != nil {
		for _, line := range h.left {
			recheckLine(h.workers.Worktrees, line)
		}
		h.adoptKept()
	}
	close(h.rechecked)
}

func (h *AgentHandle) hasOpenJobs() bool {
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	return len(h.ctl.jobs.Open()) > 0
}

// adoptKept adopts each worker the last run left running whose pane is still listed: its token
// works again and the controller takes it back (or stops it, its job being over). One whose
// pane is gone is dropped.
func (h *AgentHandle) adoptKept() {
	if len(h.kept) == 0 {
		return
	}
	listed, err := within(h.ctx, ListLimit, h.deps.Host.Session().List)
	if err != nil {
		log.Printf("agent: couldn't list the session to adopt kept workers: %s", tag(err))
		return
	}
	for _, kw := range h.kept {
		name := kw.Plan.Name
		i := slices.IndexFunc(listed, func(a AgentInfo) bool { return a.Name == name })
		if i < 0 {
			log.Printf("agent: kept worker %s is gone", name)
			continue
		}
		// Before the controller briefs it, so its reply is accepted.
		h.workers.Tokens.Restore(name, kw.Token)
		h.withKnown(func(known map[string]repoSlug) { known[name] = repoSlug{kw.Plan.Repo, kw.Plan.Slug} })
		if r := h.stepLocked(AdoptWorkerInput(kw.Plan, kw.Worktree, listed[i].Status)); r == nil || r.Err != "" {
			h.workers.Tokens.Revoke(name)
			h.withKnown(func(known map[string]repoSlug) { delete(known, name) })
		}
	}
}

// saveKept saves what this quit leaves running: the coordinator (if it ran) and the live
// workers, with their tokens. Called before the quit forgets them.
func (h *AgentHandle) saveKept() {
	h.ctlMu.Lock()
	running := h.ctl.coordinator != coordAbsent
	live := h.ctl.LiveWorkers()
	h.ctlMu.Unlock()
	var kept KeptAgents
	if running {
		kept.CoordinatorToken = h.deps.Host.Token()
	}
	if h.workers != nil {
		for _, w := range live {
			if tok, ok := h.workers.Tokens.Token(w.Plan.Name); ok {
				kept.Workers = append(kept.Workers, KeptWorker{Plan: w.Plan, Worktree: w.Worktree, Token: tok})
			}
		}
	}
	if err := h.deps.Kept.Save(kept); err != nil {
		log.Printf("agent: couldn't save the kept agents: %v", err)
	}
}

func (h *AgentHandle) quitting() bool { return h.shuttingDown.Load() }

// stepLocked steps the controller and queues its effects for the main loop while the
// controller lock is held (the queue never blocks), which keeps them in step order across
// goroutines. Returns the step's result effect (AnswerResult, Admitted or ToolResult), if any.
func (h *AgentHandle) stepLocked(in Input) *Effect {
	now := h.deps.Clock.NowMs()
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	var result *Effect
	for _, e := range h.ctl.Step(in, now) {
		switch e.Kind {
		case EfAnswerResult, EfAdmitted, EfToolResult:
			result = &e
		default:
			h.main.push(mainItem{effect: &e})
		}
	}
	return result
}

// step steps the controller and returns every effect (for Shutdown, which runs them itself).
func (h *AgentHandle) step(in Input) []Effect {
	now := h.deps.Clock.NowMs()
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	return h.ctl.Step(in, now)
}

// Input steps the controller with in; ignored once the app is quitting.
func (h *AgentHandle) Input(in Input) {
	if !h.quitting() {
		h.stepLocked(in)
	}
}

// Deliver is httpserver.Runtime: queues a hook event for the main loop. False once the
// runtime has stopped (its Run ended).
func (h *AgentHandle) Deliver(ev HookEvent) bool {
	if h.ctx.Err() != nil {
		return false
	}
	return h.main.push(mainItem{hook: &ev})
}

// Delegate is the delegate tool: a new job id at once, or why delegation is off.
func (h *AgentHandle) Delegate(task, context string) (JobID, error) {
	if h.quitting() {
		return "", errQuitting
	}
	h.ctlMu.Lock()
	why, off := h.ctl.Disabled()
	h.ctlMu.Unlock()
	if off {
		return "", fmt.Errorf("Delegation is off (%s).", why)
	}
	id := h.deps.IDs.JobID()
	h.Input(DelegateInput(id, task, context))
	return id, nil
}

// JobStatus is the jobs tool's status: from the job table, never from the agent.
func (h *AgentHandle) JobStatus() []JobView {
	now := h.deps.Clock.NowMs()
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	return h.ctl.Snapshot(now)
}

// OpenJobs are the open jobs, for the tray.
func (h *AgentHandle) OpenJobs() []JobView {
	return slices.DeleteFunc(h.JobStatus(), func(j JobView) bool { return !j.Status.IsOpen() })
}

// Cancel cancels jobID ("" = the only open job).
func (h *AgentHandle) Cancel(jobID string) (JobID, error) {
	h.ctlMu.Lock()
	id, err := h.ctl.ResolveCancel(jobID)
	h.ctlMu.Unlock()
	if err != nil {
		return "", err
	}
	h.Input(CancelInput(id))
	return id, nil
}

// Answer is answer_job: the user's answer to a job's question (jobID "" = the only job
// waiting). The controller decides which job it goes to and rejects it if none is waiting.
func (h *AgentHandle) Answer(jobID, text string) (JobID, error) {
	var id JobID
	if raw := strings.TrimSpace(jobID); raw != "" {
		j, ok := ParseJobID(raw)
		if !ok {
			return "", fmt.Errorf("No open job %s.", raw)
		}
		id = j
	}
	if h.quitting() {
		return "", errQuitting
	}
	r := h.stepLocked(AnswerInput(id, text))
	if r == nil || r.Kind != EfAnswerResult {
		return "", errors.New("The answer was not processed.")
	}
	if r.Err != "" {
		return "", errors.New(r.Err)
	}
	return r.Job, nil
}

// OpenTerminal is a single click on Bit / Tray ▸ Open agent terminal: the session is running,
// the coordinator is (being) started unless delegation is off, then a terminal attaches.
func (h *AgentHandle) OpenTerminal(ctx context.Context) error {
	if err := h.deps.Host.Session().EnsureSession(ctx); err != nil {
		return err
	}
	h.Input(EnsureCoordinatorInput())
	// A coordinator already running is brought to the front; one being started is focused when it is up.
	focusQuietly(ctx, h.deps.Host.Session(), Coordinator)
	return h.deps.Launcher.Open(Attach{Argv: h.deps.Host.AttachCommand(), Dir: h.deps.TerminalDir})
}

// Voice forwards the voice loop's signals.
func (h *AgentHandle) Voice(sig VoiceSignal) { h.Input(VoiceInput(sig)) }

// Restart is tray ▸ Restart agent.
func (h *AgentHandle) Restart() { h.Input(RestartInput()) }

// Shutdown is the app's quit: deny pending permissions and stop the panes, in parallel,
// within ShutdownLimit in all.
func (h *AgentHandle) Shutdown() {
	// First, so nothing queued or in flight starts or prompts anything from here on.
	h.shuttingDown.Store(true)
	h.lanesMu.Lock()
	for _, l := range h.lanes {
		l.close()
	}
	clear(h.lanes)
	h.lanesMu.Unlock()
	if h.keepPanes && h.deps.Kept != nil {
		h.saveKept()
	}
	ctx, cancel := context.WithTimeout(h.ctx, ShutdownLimit)
	defer cancel()
	// Directly, not through the host-call goroutine (which may be stuck on a hung call). A
	// start still in flight stops its own pane when it finishes.
	var wg sync.WaitGroup
	var stopping []string
	for _, e := range h.step(ShutdownInput()) {
		switch e.Kind {
		case EfDecide, EfForgetWorker:
			h.apply(e)
		case EfStopCoordinator:
			wg.Go(func() {
				if err := h.deps.Host.StopCoordinator(ctx); err != nil {
					log.Printf("agent: stopping the coordinator failed: %s", tag(err))
				}
			})
		case EfStopWorker:
			stopping = append(stopping, e.Name)
		default:
			// The controller emits only the ones above on shutdown; carrying out anything else
			// now (prompting, injecting) would be wrong.
			log.Print("agent: skipped an unexpected effect on shutdown")
		}
	}
	// Every worker still known has its worktree's git links re-checked (best effort), after its
	// pane closed when it is being stopped.
	known := h.takeAllKnown()
	var fs WorktreeFs
	if h.workers != nil {
		fs = h.workers.Worktrees
	}
	session := h.deps.Host.Session()
	for _, name := range stopping {
		k, ok := known[name]
		delete(known, name)
		wg.Go(func() {
			_ = session.SendKeys(ctx, name, []string{"Escape"})
			sleepCtx(ctx, StopGrace)
			closeQuietly(ctx, session, name)
			if fs != nil && ok {
				auditQuietly(fs, name, k)
			}
		})
	}
	// Panes left running (keepPanesOnQuit) could rewrite their links again: they are
	// re-checked when their worktree is next reused instead.
	if fs != nil && !h.keepPanes {
		for name, k := range known {
			wg.Go(func() { auditQuietly(fs, name, k) })
		}
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		log.Print("agent: stopping the agent panes failed: timeout")
	}
	// Any permission the controller did not decide (a hook that raced the quit) is denied here.
	h.denyAllWaiting()
}

// denyAllWaiting denies every permission request still waiting for an answer (the app quits).
func (h *AgentHandle) denyAllWaiting() {
	h.repliesMu.Lock()
	waiting := h.replies
	h.replies = map[RequestID]chan<- Decision{}
	h.repliesMu.Unlock()
	for _, tx := range waiting {
		sendDecision(tx, Deny("The app quit."))
	}
}

// sendDecision answers a one-shot reply channel (buffered by its maker) without blocking.
func sendDecision(tx chan<- Decision, d Decision) {
	if tx == nil {
		return
	}
	select {
	case tx <- d:
	default:
	}
}

// runOp carries out one coordinator host call, giving up on it after OpLimit (StartOpLimit
// for a start). Once the app is quitting nothing runs; Shutdown stops the pane itself.
func (h *AgentHandle) runOp(op hostOp) {
	if h.quitting() {
		return
	}
	limit := OpLimit
	if op.kind == opStart {
		limit = StartOpLimit
	}
	ctx, cancel := context.WithTimeout(h.ctx, limit)
	defer cancel()
	done := make(chan struct{})
	go func() {
		h.runOpInner(ctx, op)
		close(done)
	}()
	select {
	case <-done:
		return
	case <-ctx.Done():
	}
	if h.ctx.Err() != nil {
		return // the runtime stopped
	}
	log.Print("agent: host call failed: timeout")
	if op.kind == opPrompt && !h.quitting() {
		// It may have been typed, so it is not sent again; the controller watches for a sign
		// of life and fails its jobs if none comes.
		h.Input(PromptTimedOutInput())
	}
	if op.kind == opStart && !h.quitting() {
		// The controller must leave Starting: its waiting jobs fail under the usual rules.
		h.Input(CoordinatorFailedInput(HostError{Kind: HostTimeout, Detail: "the coordinator did not start in time"}))
		// Don't leave a half-started pane to be adopted later (best effort).
		_, err := within(h.ctx, OpLimit, func(c context.Context) (struct{}, error) {
			return struct{}{}, h.deps.Host.Session().Close(c, Coordinator)
		})
		if err != nil && !IsHostError(err, HostNoAgent) {
			log.Printf("agent: closing a half-started coordinator failed: %s", tag(err))
		}
	}
}

// runOpInner is the host call itself. A call whose ctx ran out was given up on by runOp, so
// it reports nothing (Rust drops the future).
func (h *AgentHandle) runOpInner(ctx context.Context, op hostOp) {
	host := h.deps.Host
	switch op.kind {
	case opStart:
		ensured, err := host.EnsureCoordinator(ctx)
		if ctx.Err() != nil && h.ctx.Err() == nil && !h.quitting() {
			return
		}
		if h.quitting() {
			// The app quit while the start was in flight: don't leave its pane behind.
			if err == nil {
				sctx, cancel := context.WithTimeout(h.ctx, OpLimit)
				_ = host.StopCoordinator(sctx)
				cancel()
			}
			return
		}
		if err != nil {
			var he HostError
			if !errors.As(err, &he) {
				he = HostError{Kind: HostOther, Detail: err.Error()}
			}
			h.Input(CoordinatorFailedInput(he))
			return
		}
		h.Input(CoordinatorUpInput(ensured.Status))
	case opPrompt:
		if err := host.Session().Prompt(ctx, Coordinator, op.text); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("agent: prompt failed: %s", tag(err))
			// Nothing was typed: the controller queues it again (or gives up on it).
			h.Input(PromptFailedInput())
		}
	case opInterrupt:
		if err := host.Session().SendKeys(ctx, Coordinator, []string{"Escape"}); err != nil {
			log.Printf("agent: interrupt failed: %s", tag(err))
		}
	case opStop:
		if err := host.StopCoordinator(ctx); err != nil {
			log.Printf("agent: stopping the coordinator failed: %s", tag(err))
		}
	case opRelabel:
		relabelQuietly(ctx, host.Session(), Coordinator, op.tab)
	}
}

// hook handles one hook server event.
func (h *AgentHandle) hook(ev HookEvent) {
	switch ev.Kind {
	case HookStop:
		h.Input(StopInput(ev.Agent, ev.Message))
	case HookPostTool:
		h.Input(PostToolInput(ev.Agent, ev.ToolName, ev.ToolInput))
	case HookMcp:
		// Its own goroutine: a start_worker takes seconds, and nothing else may wait on it.
		go func() {
			r := h.mcp(ev.Agent, ev.Call)
			if ev.McpReply != nil {
				select {
				case ev.McpReply <- r:
				default:
				}
			}
		}()
	case HookPermission:
		if h.quitting() {
			sendDecision(ev.Reply, Deny("The app quit."))
			return
		}
		id := h.nextRequest.Add(1)
		h.repliesMu.Lock()
		h.replies[id] = ev.Reply
		h.repliesMu.Unlock()
		h.Input(PermissionInput(id, ev.Agent, ev.ToolName, ev.ToolInput))
		// Raced the quit: Input ignored it and Shutdown may already have drained.
		if h.quitting() {
			h.denyAllWaiting()
		}
	}
}

// mcp answers a mascot MCP tool call.
func (h *AgentHandle) mcp(agent string, call McpCall) McpReply {
	if h.quitting() {
		return McpReply{Err: errQuitting}
	}
	if call.Kind == CallStartWorker {
		return h.startWorker(agent, call.Worker)
	}
	r := h.stepLocked(McpInput(agent, call))
	if r == nil || r.Kind != EfToolResult {
		return McpReply{Err: errors.New("The call was not processed.")}
	}
	if r.Err != "" {
		return McpReply{Err: errors.New(r.Err)}
	}
	return McpReply{Value: r.Value}
}

// startWorker is start_worker (spec §5.5): resolve the repo, admit, make the worktree and
// launch files, start the pane, then hand it to the controller. A refusal is logged with its
// stage and the job id only (never free text).
func (h *AgentHandle) startWorker(agent string, req WorkerRequest) McpReply {
	job := "?"
	if j, ok := ParseJobID(req.JobID); ok {
		job = string(j)
	}
	refuse := func(stage, why string) McpReply {
		log.Printf("agent: start_worker refused (%s) for job %s", stage, job)
		return McpReply{Err: errors.New(why)}
	}
	if agent != Coordinator {
		return refuse("not_coordinator", "Only the coordinator can start workers.")
	}
	if h.workers == nil {
		return refuse("workers_off", "Workers are turned off (agent.workers.enabled).")
	}
	w := *h.workers
	// With the names in its .worktrees/ that are not a worker's (e.g. the user's own). Only once
	// the re-check of worktrees left by a crash is done (within the same limit).
	type resolved struct {
		repo    string
		foreign []string
	}
	found, err := within(h.ctx, RepoLimit, func(ctx context.Context) (resolved, error) {
		select {
		case <-h.rechecked:
		case <-ctx.Done():
			return resolved{}, ctx.Err()
		}
		repo, err := w.Worktrees.ResolveRepo(req.Repo)
		if err != nil {
			return resolved{}, err
		}
		return resolved{repo, w.Worktrees.ForeignSlugs(repo)}, nil
	})
	if errors.Is(err, errTimedOut) {
		return refuse("repo", "Couldn't check the repository in time; try again.")
	}
	if err != nil {
		return refuse("repo", err.Error())
	}
	session := h.deps.Host.Session()
	// The taken names keep a start from ever closing a pane it did not create: without them the
	// start is refused. A failed start's name stays taken until its pane is closed.
	listed, err := within(h.ctx, ListLimit, session.List)
	if err != nil {
		return refuse("list", "Couldn't check the running workers; try again.")
	}
	taken := make([]string, 0, len(listed))
	for _, a := range listed {
		taken = append(taken, a.Name)
	}
	if !h.straysLogged.Swap(true) {
		h.knownMu.Lock()
		known := slices.Collect(maps.Keys(h.known))
		h.knownMu.Unlock()
		if strays := unknownAgents(taken, known); len(strays) > 0 {
			log.Printf("agent: agent panes this run did not start (left by an earlier run?): %s", strings.Join(strays, ", "))
		}
	}
	h.closingMu.Lock()
	for n := range h.closing {
		taken = append(taken, n)
	}
	h.closingMu.Unlock()
	repoName := RepoSlug(dirName(found.repo))
	for _, slug := range found.foreign {
		taken = append(taken, repoName+"-"+slug)
	}
	r := h.stepLocked(AdmitWorkerInput(req, found.repo, taken))
	if r == nil || r.Kind != EfAdmitted {
		return refuse("admit", "The request was not processed.")
	}
	if r.Err != "" {
		return refuse("admit", r.Err)
	}
	plan := r.Plan
	h.withKnown(func(known map[string]repoSlug) { known[plan.Name] = repoSlug{plan.Repo, plan.Slug} })
	// This runs on the hook's own goroutine, which a client hanging up does not stop, so the
	// worker is neither lost nor left counted against workers.max.
	return h.finishStart(w, plan)
}

// dirName is Rust's Path::file_name for a repository root ("" when it has none).
func dirName(p string) string {
	b := filepath.Base(p)
	if b == "." || b == "/" {
		return ""
	}
	return b
}

// launched is a worker start's outcome.
type launched struct {
	worktree string
	status   PaneStatus
	err      error
}

// finishStart runs an admitted worker's start to its end: exactly one of WorkerUp or
// WorkerFailed goes to the controller (unless the app is quitting, when its leftovers are
// dropped here), on success, error, timeout (WorkerStartLimit) and a crash of the start itself
// (the launch runs on a goroutine of its own and recovers a panic).
func (h *AgentHandle) finishStart(w WorkerDeps, plan WorkerPlan) McpReply {
	name := plan.Name
	session := h.deps.Host.Session()
	// The pane StartAgent made, once it has: it is closed by its id.
	var paneMu sync.Mutex
	pane := ""
	// Closed once the start's Ensure has returned (or will never run): giving up on the start
	// does not stop blocking git work, so nothing of a timed-out start is touched before.
	ensureReturned := make(chan struct{})
	var ensureOnce sync.Once
	markEnsured := func() { ensureOnce.Do(func() { close(ensureReturned) }) }
	ctx, cancel := context.WithTimeout(h.ctx, WorkerStartLimit)
	defer cancel()
	done := make(chan launched, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				markEnsured()
				log.Printf("agent: worker %s start crashed", name)
				done <- launched{err: fmt.Errorf("%s did not start: internal error", plan.Kind)}
			}
		}()
		wt, st, err := h.launchWorker(ctx, w, plan, func(p string) {
			paneMu.Lock()
			pane = p
			paneMu.Unlock()
		}, markEnsured)
		// The controller drops Exited for a worker still starting, so a pane that died during
		// the start must be noticed here (within the start's limit).
		if err == nil && !h.quitting() {
			if list, lerr := within(ctx, PostStartCheck, session.List); lerr == nil {
				if !slices.ContainsFunc(list, func(a AgentInfo) bool { return a.Name == name }) {
					err = fmt.Errorf("%s exited at start", plan.Kind)
				}
			}
		}
		done <- launched{wt, st, err}
	}()
	var res launched
	select {
	case res = <-done:
	case <-ctx.Done():
		select {
		case res = <-done:
		default:
			res = launched{err: ctx.Err()}
		}
	}
	if res.err != nil && ctx.Err() != nil {
		res.err = fmt.Errorf("%s did not start in time", plan.Kind)
	}
	cancel()
	paneMu.Lock()
	startedPane := pane
	paneMu.Unlock()
	switch {
	case res.err == nil && !h.quitting():
		h.Input(WorkerUpInput(name, res.worktree, res.status))
		if h.quitting() {
			// The quit came between the check and the input: nothing was briefed.
			k, ok := h.takeKnown(name)
			h.abandon(w, name, startedPane, k, ok)
		}
		return McpReply{Value: map[string]any{"name": name, "worktree": res.worktree}}
	case res.err == nil:
		k, ok := h.takeKnown(name)
		h.abandon(w, name, startedPane, k, ok)
		return McpReply{Err: errQuitting}
	}
	reason := res.err.Error()
	log.Printf("agent: worker %s did not start", name)
	// Its name stays taken, and its git links wait, until its pane is closed and its Ensure
	// has returned.
	h.closingMu.Lock()
	h.closing[name] = true
	h.closingMu.Unlock()
	k, hasKnown := h.takeKnown(name)
	// The slot and the reply go first; closing may take a while.
	h.Input(WorkerFailedInput(name, reason))
	quitting := h.quitting()
	if quitting {
		h.abandon(w, name, startedPane, repoSlug{}, false)
	}
	go func() {
		if !quitting {
			// A pane may be left by a start that timed out; nothing of it may keep running.
			closeStarted(h.ctx, session, name, startedPane)
		}
		select {
		case <-ensureReturned:
		case <-h.ctx.Done():
		}
		// A pane left running (keepPanesOnQuit) is re-checked when its worktree is next reused.
		if hasKnown && !(quitting && h.keepPanes) {
			auditQuietly(w.Worktrees, name, k)
		}
		h.closingMu.Lock()
		delete(h.closing, name)
		h.closingMu.Unlock()
	}()
	return McpReply{Err: errors.New(reason)}
}

// abandon drops the worker's pane (unless keepPanesOnQuit), token and files directly (the app
// is quitting, so the controller no longer takes inputs).
func (h *AgentHandle) abandon(w WorkerDeps, name, pane string, k repoSlug, known bool) {
	if !h.keepPanes {
		closeStarted(h.ctx, h.deps.Host.Session(), name, pane)
	}
	w.Tokens.Revoke(name)
	w.Recipes.Discard(name)
	// A pane left running (keepPanesOnQuit) is re-checked when its worktree is next reused.
	if known && !h.keepPanes {
		auditQuietly(w.Worktrees, name, k)
	}
}

// launchWorker makes the worktree and launch files and starts the pane. It stops at each step
// once ctx is done (Rust aborts the task at its await points).
func (h *AgentHandle) launchWorker(ctx context.Context, w WorkerDeps, plan WorkerPlan, setPane func(string), ensured func()) (string, PaneStatus, error) {
	worktree, err := func() (string, error) {
		defer ensured()
		return w.Worktrees.Ensure(plan.Repo, plan.Slug)
	}()
	if ctx.Err() != nil {
		return "", 0, ctx.Err()
	}
	if err != nil {
		return "", 0, err
	}
	if h.quitting() {
		return "", 0, errQuitting
	}
	launch := WorkerLaunch{Name: plan.Name, Kind: plan.Kind, Repo: plan.Repo, Worktree: worktree, Token: w.Tokens.Issue(plan.Name)}
	prepared, err := w.Recipes.Prepare(launch)
	if ctx.Err() != nil {
		return "", 0, ctx.Err()
	}
	if err != nil {
		return "", 0, err
	}
	if h.quitting() {
		return "", 0, errQuitting
	}
	prepared.Spec.Tab = WorkerTab(plan)
	session := h.deps.Host.Session()
	began := time.Now()
	info, err := session.StartAgent(ctx, prepared.Spec)
	if ctx.Err() != nil {
		if err == nil {
			// Started just as the start was given up on: don't leave the pane behind.
			closeStarted(h.ctx, session, plan.Name, info.Pane)
		}
		return "", 0, ctx.Err()
	}
	if err != nil {
		return "", 0, err
	}
	setPane(info.Pane)
	if err := checkStartScreen(ctx, session, plan, prepared.Refusal, prepared.Ready, info.Status, began); err != nil {
		return "", 0, err
	}
	return worktree, info.Status, nil
}

// takeKnown forgets the worker's repo and slug and returns them (false once taken).
func (h *AgentHandle) takeKnown(name string) (k repoSlug, ok bool) {
	h.withKnown(func(known map[string]repoSlug) {
		k, ok = known[name]
		delete(known, name)
	})
	return
}

// takeAllKnown forgets every known worker's repo and slug and returns them.
func (h *AgentHandle) takeAllKnown() (out map[string]repoSlug) {
	h.withKnown(func(known map[string]repoSlug) {
		out = maps.Clone(known)
		clear(known)
	})
	return
}

// withKnown changes known and keeps LiveWorktreesFile in step (written under the lock, so in
// order). Only when it changed, and never with workers off (a crash list from an earlier run
// with workers stays for the next one).
func (h *AgentHandle) withKnown(change func(map[string]repoSlug)) {
	h.knownMu.Lock()
	defer h.knownMu.Unlock()
	before := maps.Clone(h.known)
	change(h.known)
	if h.workers == nil || maps.Equal(before, h.known) {
		return
	}
	lines := make([]string, 0, len(h.known))
	for _, k := range h.known {
		b, _ := json.Marshal(map[string]string{"repo": k.repo, "slug": k.slug})
		lines = append(lines, string(b))
	}
	slices.Sort(lines)
	h.deps.Store.Rewrite(LiveWorktreesFile, lines)
}

// workerOp queues a worker effect on the worker's lane: each worker's effects run one after
// another in the order the controller emitted them (a prompt never overtakes the stop after
// it), each bounded by OpLimit.
func (h *AgentHandle) workerOp(e Effect) {
	h.lanesMu.Lock()
	defer h.lanesMu.Unlock()
	l, ok := h.lanes[e.Name]
	if !ok {
		l = h.newLane(e.Name)
		h.lanes[e.Name] = l
	}
	l.push(e)
}

// newLane is a worker's lane: a goroutine that runs its effects in order until the lane is
// closed and drained.
func (h *AgentHandle) newLane(name string) *queue[Effect] {
	q := newQueue[Effect]()
	go func() {
		for {
			e, ok := q.pop(h.ctx)
			if !ok {
				return
			}
			stop := e.Kind == EfStopWorker
			h.runWorkerOp(e)
			if stop {
				// Nothing follows a stop; the lane ends once its queue is drained. Only this
				// lane: a new worker of the same name may have one by now.
				h.lanesMu.Lock()
				if h.lanes[name] == q {
					delete(h.lanes, name)
				}
				h.lanesMu.Unlock()
				q.close()
			}
		}
	}()
	return q
}

func (h *AgentHandle) runWorkerOp(e Effect) {
	if e.Kind == EfForgetWorker {
		// Queued after the worker's last ops (its close), so its git links are re-checked only
		// once its pane is gone.
		h.forgetWorker(e.Name)
		return
	}
	if h.quitting() {
		return
	}
	session := h.deps.Host.Session()
	name := e.Name
	switch e.Kind {
	case EfPromptWorker:
		_, err := within(h.ctx, OpLimit, func(c context.Context) (struct{}, error) {
			return struct{}{}, session.Prompt(c, name, e.Text)
		})
		if err != nil && !h.quitting() {
			h.Input(WorkerPromptFailedInput(name, e.Text))
		}
	case EfSubmitWorker:
		within(h.ctx, OpLimit, func(c context.Context) (struct{}, error) {
			return struct{}{}, session.SendKeys(c, name, []string{"Enter"})
		})
	case EfStopWorker:
		within(h.ctx, OpLimit, func(c context.Context) (struct{}, error) {
			return struct{}{}, session.SendKeys(c, name, []string{"Escape"})
		})
		sleepCtx(h.ctx, WorkerStopGrace)
		closeQuietly(h.ctx, session, name)
		h.Input(WorkerClosedInput(name))
	case EfReadWorker:
		text, err := within(h.ctx, OpLimit, func(c context.Context) (string, error) { return session.Read(c, name, 80) })
		if err != nil {
			text = ""
		}
		h.Input(WorkerScreenInput(name, text))
	}
}

// forgetWorker revokes the worker's token, deletes its launch files and re-checks its
// worktree's git links.
func (h *AgentHandle) forgetWorker(name string) {
	if h.workers == nil {
		return
	}
	h.workers.Tokens.Revoke(name)
	h.workers.Recipes.Discard(name)
	// On quit, Shutdown re-checks every worker still known within its limit.
	if !h.quitting() {
		if k, ok := h.takeKnown(name); ok {
			go auditQuietly(h.workers.Worktrees, name, k)
		}
	}
}

func (h *AgentHandle) queueOp(op hostOp) { h.ops.push(op) }

// apply carries out one effect.
func (h *AgentHandle) apply(e Effect) {
	d := &h.deps
	switch e.Kind {
	case EfStartCoordinator:
		h.queueOp(hostOp{kind: opStart})
	case EfPrompt:
		h.queueOp(hostOp{kind: opPrompt, text: e.Text})
	case EfInterrupt:
		h.queueOp(hostOp{kind: opInterrupt})
	case EfInject:
		if d.Voice.IsOpen() {
			d.Voice.Inject(e.Notice)
		} else {
			h.Input(InjectBouncedInput(e.Notice))
		}
	case EfHoldOpen:
		d.Voice.HoldOpen(e.Hold)
	case EfOpenConversation:
		if !d.Voice.Open() {
			h.Input(OpenRefusedInput())
		}
	case EfChime:
		d.Notifier.Chime()
	case EfNotify:
		d.Notifier.Notify("Bit", e.Text)
	case EfCue:
		d.Mascot.Cue(e.Cue)
	case EfDecide:
		h.repliesMu.Lock()
		tx, ok := h.replies[e.ID]
		delete(h.replies, e.ID)
		h.repliesMu.Unlock()
		if ok {
			sendDecision(tx, e.Decision)
		}
	case EfAppend:
		d.Store.Append(e.File, e.Line)
	case EfRewrite:
		d.Store.Rewrite(e.File, e.Lines)
	case EfStatus:
		d.Status.SetStatus(e.Text)
	case EfStopCoordinator:
		h.queueOp(hostOp{kind: opStop})
	case EfLabel:
		h.queueOp(hostOp{kind: opRelabel, tab: e.Tab})
	case EfResetHost:
		d.Host.Reset()
	case EfLog:
		log.Printf("agent: %s", e.Text)
	case EfAnswerResult, EfAdmitted, EfToolResult:
		// Delivered to the caller directly; nothing to carry out.
	case EfPromptWorker, EfSubmitWorker, EfStopWorker, EfReadWorker:
		h.workerOp(e)
	case EfForgetWorker:
		// Its lane ends here; whatever it still has queued (a close) runs first.
		h.lanesMu.Lock()
		l, ok := h.lanes[e.Name]
		delete(h.lanes, e.Name)
		h.lanesMu.Unlock()
		if ok && l.push(e) {
			l.close()
		} else {
			h.forgetWorker(e.Name)
		}
	}
}

// unknownAgents: the agents in listed other than the coordinator that are not known (sorted).
func unknownAgents(listed, known []string) []string {
	var out []string
	for _, n := range listed {
		if n != Coordinator && !slices.Contains(known, n) {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return out
}

// recheckLine handles one LiveWorktreesFile line left by a crash: its repo must still resolve
// under the projects root (else it is skipped) before its worktree's git links are re-checked.
func recheckLine(fs WorktreeFs, line string) {
	var v map[string]any
	_ = json.Unmarshal([]byte(line), &v)
	raw, ok1 := v["repo"].(string)
	slug, ok2 := v["slug"].(string)
	if !ok1 || !ok2 {
		log.Printf("agent: skipped an unreadable line in %s", LiveWorktreesFile)
		return
	}
	repo, err := fs.ResolveRepo(raw)
	if err != nil {
		log.Printf("agent: skipped re-checking %s: its repository no longer resolves", slug)
		return
	}
	auditQuietly(fs, slug, repoSlug{repo, slug})
}

// auditQuietly re-checks a forgotten worker's worktree git links (best effort; logs its name only).
func auditQuietly(fs WorktreeFs, name string, k repoSlug) {
	if err := fs.Audit(k.repo, k.slug); err != nil {
		log.Printf("agent: re-checking the git links of %s failed", name)
	}
}

// closeQuietly closes agent's pane (bounded); a pane already gone is fine.
func closeQuietly(ctx context.Context, session SessionHost, agent string) {
	closeBounded(ctx, agent, func(c context.Context) error { return session.Close(c, agent) })
}

// closeStarted closes the pane a worker start made (bounded): by its id when the start got
// that far (pane != ""), so a newer agent of the same name is never hit; else by name (still
// reserved in closing).
func closeStarted(ctx context.Context, session SessionHost, agent, pane string) {
	if pane == "" {
		closeQuietly(ctx, session, agent)
		return
	}
	closeBounded(ctx, agent, func(c context.Context) error { return session.CloseExact(c, agent, pane) })
}

// closeBounded runs a close within OpLimit, logging a failure (a pane already gone is fine).
func closeBounded(ctx context.Context, agent string, close func(context.Context) error) {
	_, err := within(ctx, OpLimit, func(c context.Context) (struct{}, error) { return struct{}{}, close(c) })
	switch {
	case err == nil || IsHostError(err, HostNoAgent):
	case errors.Is(err, errTimedOut):
		log.Printf("agent: closing %s failed: timeout", agent)
	default:
		log.Printf("agent: closing %s failed: %s", agent, tag(err))
	}
}

// checkStartScreen reads the start screen every StartScreenPoll (each read bounded by the time
// left). A refusal (e.g. a folder-trust prompt the app won't answer) fails the start with its
// reason, and nothing is typed. With ready, the start is up only once the screen shows its
// marker and no refusal screen, watched until ReadyWindow after began (at least DialogWait); if
// it never shows, the start fails with its reason naming the last screen line. Without it, the
// screen is read once, and again for up to DialogWait only while the pane is blocked.
func checkStartScreen(ctx context.Context, session SessionHost, plan WorkerPlan, refusal *StartRefusal, ready *StartReady, status PaneStatus, began time.Time) error {
	if refusal == nil && ready == nil {
		return nil
	}
	deadline := time.Now().Add(DialogWait)
	if ready != nil {
		if r := began.Add(ReadyWindow); r.After(deadline) {
			deadline = r
		}
	}
	last := ""
	for {
		text, err := within(ctx, max(time.Until(deadline), 0), func(c context.Context) (string, error) {
			return session.Read(c, plan.Name, 40)
		})
		if err == nil {
			if refusal != nil && ShowsAnyScreen(text, refusal.Screens) {
				return errors.New(refusal.Reason)
			}
			if ready != nil && strings.Contains(text, ready.Marker) {
				return nil
			}
			last = lastLine(text)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Before(deadline) {
			if ready != nil {
				return errors.New(strings.ReplaceAll(ready.Reason, "{last}", last))
			}
			return nil
		}
		if ready == nil && status != PaneBlocked {
			return nil
		}
		next := time.Now().Add(StartScreenPoll)
		if deadline.Before(next) {
			next = deadline
		}
		sleepCtx(ctx, time.Until(next))
	}
}

// lastLine is the screen's last non-empty line, trimmed, without control characters, at most
// 80 chars.
func lastLine(screen string) string {
	lines := rustLines(screen)
	line := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			line = l
			break
		}
	}
	var b strings.Builder
	n := 0
	for _, r := range line {
		if unicode.Is(unicode.Cc, r) {
			continue
		}
		if n == 80 {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// tag is a host error's tag for logs: the variant only, never its text.
func tag(err error) string {
	var he HostError
	if !errors.As(err, &he) {
		if errors.Is(err, errTimedOut) || errors.Is(err, context.DeadlineExceeded) {
			return "timeout"
		}
		return "other"
	}
	switch he.Kind {
	case HostNotFound:
		return "not_found"
	case HostVersionMismatch:
		return "version_mismatch"
	case HostNoAgent:
		return "no_agent"
	case HostTimeout:
		return "timeout"
	case HostGaveUp:
		return "gave_up"
	case HostExitedAtStart:
		return "exited_at_start"
	}
	return "other"
}

// within runs f with a context that ends after d. When d (or parent) runs out before f
// returns, it gives up on f (which goes on in the background, like a dropped Rust future
// that is still in a blocking call) and returns errTimedOut.
func within[T any](parent context.Context, d time.Duration, f func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(parent, d)
	defer cancel()
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := f(ctx)
		ch <- result{v, err}
	}()
	var zero T
	select {
	case r := <-ch:
		if r.err != nil && ctx.Err() != nil {
			return zero, errTimedOut
		}
		return r.v, r.err
	case <-ctx.Done():
		select {
		case r := <-ch:
			if r.err == nil {
				return r.v, nil
			}
		default:
		}
		return zero, errTimedOut
	}
}

// sleepCtx sleeps for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// queue is an unbounded FIFO (Rust's unbounded mpsc): push never blocks.
type queue[T any] struct {
	mu     sync.Mutex
	items  []T
	closed bool
	// sig has a value whenever items may be non-empty or the queue was closed.
	sig chan struct{}
}

func newQueue[T any]() *queue[T] { return &queue[T]{sig: make(chan struct{}, 1)} }

func (q *queue[T]) signal() {
	select {
	case q.sig <- struct{}{}:
	default:
	}
}

// push adds v; false when the queue is closed.
func (q *queue[T]) push(v T) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	q.items = append(q.items, v)
	q.mu.Unlock()
	q.signal()
	return true
}

// close lets pop drain what is queued, then report the end.
func (q *queue[T]) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.signal()
}

func (q *queue[T]) tryPop() (T, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var zero T
	if len(q.items) == 0 {
		return zero, false
	}
	v := q.items[0]
	q.items[0] = zero
	q.items = q.items[1:]
	return v, true
}

// pop waits for an item; false once the queue is closed and drained, or ctx is done.
func (q *queue[T]) pop(ctx context.Context) (T, bool) {
	for {
		if v, ok := q.tryPop(); ok {
			return v, true
		}
		q.mu.Lock()
		closed := q.closed
		q.mu.Unlock()
		if closed {
			var zero T
			return zero, false
		}
		select {
		case <-q.sig:
		case <-ctx.Done():
			var zero T
			return zero, false
		}
	}
}
