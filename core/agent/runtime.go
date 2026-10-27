package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"path/filepath"
	"reflect"
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
	// ConfirmWait: the longest a session tool waits for your confirm before it answers "not sent
	// yet" (#246); the coordinator's tool call gives up after about a minute, and the host call
	// (OpLimit) still has to fit.
	ConfirmWait = 25 * time.Second
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
	// AnswerGrace is how long a refusal screen may stay after its Answer was sent.
	AnswerGrace = 3 * time.Second
)

// LiveWorktreesFile holds the live workers' repos and worktree slugs (`{"repo":…,"slug":…}`
// lines), kept in step with the known workers: after a crash, their git links are re-checked.
const LiveWorktreesFile = "live-worktrees.jsonl"

// Deps is everything the runtime needs, injected by a composition root.
type Deps struct {
	Host     *AgentHost
	Voice    VoiceLink
	Notifier Notifier
	Pixie    PixieCue
	Status   StatusSink
	// Modes hears each Claude agent's Manual-mode changes (nil: ignored).
	Modes   ModeSink
	Planner TerminalPlanner
	// Sessions plans showing one of your own sessions' tab (#148; nil: a click on one fails).
	Sessions SessionPlanner
	// Focus hands a built FocusPlan to the shell to execute (#116).
	Focus FocusSink
	// TerminalDir is the folder the agent terminal starts in (the projects folder, else home).
	TerminalDir string
	Store       Store
	Clock       Clock
	IDs         IdSource
	// Kept saves what a quit with keepPanesOnQuit leaves running (nil: nothing is saved).
	Kept KeptStore
	// Updates gets the updates bubble's list (nil: ignored).
	Updates UpdatesSink
	// Replies gets each reply card for the reply deck (nil: ignored).
	Replies ReplySink
	// Lines gets each reply line for the chat thread (nil: ignored).
	Lines LineSink
	// Delivered gets the lines heard or seen, for the chat thread (nil: ignored).
	Delivered DeliveredSink
	// Roster gets the roster beside Pixie on every change (nil: no roster is kept).
	Roster RosterSink
	// Panes brings up an agent's pane waiting at a prompt in its terminal (nil: ignored).
	Panes PaneSink
	// Chat is the text box under Pixie (#221): typed jobs' lines go to it (nil: no text box, and
	// typed messages are ordinary jobs).
	Chat ChatSink
	// Attachments wait for the next request or answer (#240); nil = none.
	Attachments Attachments
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
	// Notice: a setup note that disables nothing (e.g. "Coordinator: workers off: …"), shown while
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
	// forceSend (agent.alwaysForceSend, #205): every message to your sessions is forced.
	forceSend bool
	// markForced (agent.markForcedSends, live, #340): a message forced into your session starts
	// with a note in brackets saying so.
	markForced atomic.Bool
	// confirmWait: ConfirmWait (tests shorten it).
	confirmWait time.Duration
	// awaiting: session tool calls waiting on your late confirm (#246), so a repeat asks nothing.
	awaitMu  sync.Mutex
	awaiting map[string]bool
	ctx      context.Context
	cancel   context.CancelFunc

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

	reconnectMu sync.Mutex
	// reconnect: adopted Claude workers whose next prompt is preceded by McpReconnect (#172):
	// while the app was down, Claude Code may have given up on the pixie server.
	reconnect map[string]bool

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
	// The roster beside Pixie (#91): rosterPoke asks for a rebuild; the last view sent.
	rosterPoke chan struct{}
	rosterMu   sync.Mutex
	rosterLast RosterView
	rosterSent bool
	rosterErr  string // the last roster list error logged ("" = none)
	// stops: the stop button's last stop of each agent, against a second Esc (#309).
	stops stopGuard
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
		deps: deps, workers: workers, keepPanes: cfg.Settings.KeepPanesOnQuit, forceSend: cfg.Settings.AlwaysForceSend, confirmWait: ConfirmWait,
		ctx: ctx, cancel: cancel, rosterPoke: make(chan struct{}, 1),
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
	h.markForced.Store(cfg.Settings.MarkForcedSends)
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
	go h.runRoster()
	h.pokeRoster()
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
			if ev.Kind == PaneEventExited || ev.Kind == PaneEventStatus && ev.Status != PaneWorking {
				h.stops.changed(ev.Agent) // its next turn can be stopped (#309)
			}
			h.Input(PaneInput(ev))
			h.pokeRoster()
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
		} else if kw.Plan.Kind == Claude {
			h.setReconnect(name, true)
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
		kept.CoordinatorKind = h.deps.Host.Kind()
		kept.CoordinatorStamp = h.deps.Host.Stamp()
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
	return h.delegate(task, context, false)
}

func (h *AgentHandle) delegate(task, context string, typed bool) (JobID, error) {
	if h.quitting() {
		return "", errQuitting
	}
	h.ctlMu.Lock()
	why, off := h.ctl.Disabled()
	h.ctlMu.Unlock()
	if off {
		return "", fmt.Errorf("Delegation is off (%s).", why)
	}
	task += h.attached() // only now that it can go
	id := h.deps.IDs.JobID()
	if note := h.requestNote(); note != "" {
		context = strings.TrimSpace(context + "\n" + note)
	}
	if typed && h.deps.Chat != nil {
		h.Input(TypedDelegateInput(id, task, context))
	} else {
		h.Input(DelegateInput(id, task, context))
	}
	return id, nil
}

// RequestNoteLimit: the longest Delegate waits to list your sessions for a new request.
const RequestNoteLimit = time.Second

// requestNote is RequestNote for the sessions and workers open now. Sessions that can't be
// listed in time are left out (the coordinator can still call list_sessions).
func (h *AgentHandle) requestNote() string {
	var sessions []SessionRow
	if c, ok := h.deps.Host.Session().(*CompositeHost); ok {
		ctx, cancel := context.WithTimeout(h.ctx, RequestNoteLimit)
		sessions, _ = userSessionRows(ctx, c.Secondary(), h.maskPending)
		cancel()
	}
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	return RequestNote(sessions, h.ctl.jobs.WorkerRows(), func(id JobID) string {
		j, _ := h.ctl.jobs.Get(id)
		return j.Task
	})
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

// SentTo: the session a cancel of an already closed job means (#332; AgentController.SentTo).
func (h *AgentHandle) SentTo(jobID string) (string, bool) {
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	return h.ctl.SentTo(jobID)
}

// SentStopped: the turn Pixie typed into session was stopped (AgentController.SentStopped).
func (h *AgentHandle) SentStopped(session string) {
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	h.ctl.SentStopped(session)
}

// stopAnswer is a cancel of the job that just asked, as its answer: the user may only have meant
// stop waiting (Oct 18: "you can stop now" killed a test run).
const stopAnswer = "stop (they said it in reply to this question: work out whether they meant stop waiting or stop the work, and ask them if you can't tell)"

// CancelAsked is jobs cancel. A job whose question Pixie relayed last gets the cancel as its
// answer instead, and the result names where it went.
func (h *AgentHandle) CancelAsked(jobID string) (JobID, string, error) {
	h.ctlMu.Lock()
	id, err := h.ctl.ResolveCancel(jobID)
	asked := err == nil && h.ctl.AskedLast(id)
	h.ctlMu.Unlock()
	if !asked {
		id, err := h.Cancel(jobID)
		return id, "", err
	}
	// A stop isn't a message: the waiting attachments stay for the next one.
	if _, _, err := h.answerAsked(string(id), stopAnswer, false); err != nil {
		return "", "", err
	}
	return id, "the coordinator, to work out what to stop", nil
}

// Answer is answer_job: the user's answer to a job's question (jobID "" = the only job
// waiting). The controller decides which job it goes to and rejects it if none is waiting.
func (h *AgentHandle) Answer(jobID, text string) (JobID, error) {
	id, _, err := h.AnswerAsked(jobID, text)
	return id, err
}

// toNewRequest: AnswerAsked found no question waiting, so the words went as a new request.
const toNewRequest = "the coordinator, as a new request"

// AnswerAsked is Answer that also says where the answer went when not to one job (#161): the
// session whose turn asked the question Pixie relayed last, or the coordinator, to work out
// which of several waiting questions it answers.
func (h *AgentHandle) AnswerAsked(jobID, text string) (JobID, string, error) {
	return h.answerAsked(jobID, text, true)
}

// answerAsked is AnswerAsked; withAttachments: the waiting attachments go with the answer
// (given back if it can't go).
func (h *AgentHandle) answerAsked(jobID, text string, withAttachments bool) (_ JobID, _ string, err error) {
	var id JobID
	if raw := strings.TrimSpace(jobID); raw != "" {
		// A malformed id is ignored like an unknown one: the answer goes to whoever asked.
		if j, ok := ParseJobID(raw); ok {
			id = j
		}
	}
	if h.quitting() {
		return "", "", errQuitting
	}
	if withAttachments {
		paths := h.take()
		defer func() {
			if err != nil {
				h.giveBack(paths)
			}
		}()
		text += attachedLines(paths)
	}
	r := h.stepLocked(AnswerInput(id, text))
	if r == nil || r.Kind != EfAnswerResult {
		return "", "", errors.New("The answer was not processed.")
	}
	if r.Err == noAnswerWaiting {
		// Nobody is waiting: the coordinator works out what the words are about, like any request.
		id, err := h.Delegate(text, "The user said this as an answer, but no question was waiting for one.")
		if err != nil {
			return "", "", err
		}
		return id, toNewRequest, nil
	}
	if r.Err != "" {
		return "", "", errors.New(r.Err)
	}
	switch {
	case r.Name != "":
		return r.Job, "the session that asked", nil
	case r.Text == "coordinator":
		return r.Job, "the coordinator, to work out which question it answers", nil
	}
	return r.Job, "", nil
}

// OpenTerminal is a single click on Pixie / Tray ▸ Open coordinator terminal: the session is running,
// the coordinator is (being) started unless delegation is off, then a terminal attaches.
func (h *AgentHandle) OpenTerminal(ctx context.Context) error {
	if err := h.deps.Host.Session().EnsureSession(ctx); err != nil {
		return err
	}
	h.Input(EnsureCoordinatorInput())
	// The agent waiting at a prompt in its terminal, else the coordinator (one already running
	// is brought to the front; one being started is focused when it is up).
	h.ctlMu.Lock()
	target := h.ctl.OpenTarget()
	h.ctlMu.Unlock()
	focusQuietly(ctx, h.deps.Host.Session(), target)
	plan, err := h.deps.Planner.Plan(Attach{Argv: h.deps.Host.AttachCommand(), Dir: h.deps.TerminalDir})
	if err != nil {
		return err
	}
	h.deps.Focus.Focus(plan)
	return nil
}

// UpdatesSeen: the bubble showed these lines long enough to count as heard.
func (h *AgentHandle) UpdatesSeen(ids []uint64) { h.Input(UpdatesSeenInput(ids)) }

// RepliesShown: the reply deck showed the cards holding these lines.
func (h *AgentHandle) RepliesShown(ids []uint64) { h.Input(RepliesShownInput(ids)) }

// PendingReplies: the cards of replies not yet shown, within the history.
func (h *AgentHandle) PendingReplies() []ReplyCard {
	now := h.deps.Clock.NowMs()
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	return h.ctl.PendingReplies(now)
}

// UpdatesDismissed: the user clicked these lines away, in the bubble or as notifications (#199).
func (h *AgentHandle) UpdatesDismissed(ids []uint64) { h.Input(UpdatesDismissedInput(ids)) }

// Decide is a tap on Allow or Deny in the bubble for the question with this confirm word.
func (h *AgentHandle) Decide(token string, allow bool) { h.Input(DecideInput(token, allow)) }

// Choose is a tap on option (from 1) of a session menu in the updates bubble (#105).
func (h *AgentHandle) Choose(token string, option int)  { h.Input(ChooseInput(token, option)) }
func (h *AgentHandle) TypedAnswer(token, text string)   { h.Input(TypedAnswerInput(token, text)) }
func (h *AgentHandle) ChatAbout(token string)           { h.Input(ChatAboutInput(token)) }
func (h *AgentHandle) Pick(token string, options []int) { h.Input(PicksInput(token, options)) }

// answerSession types your answer into a pix session's prompt (#105): taken, said is told; a
// failure is told instead. Both are logged (#268).
func (h *AgentHandle) answerSession(name, id, answer, said string) {
	err := errors.New("your sessions can't be answered from Pixie.")
	if c, ok := h.deps.Host.Session().(*CompositeHost); ok {
		if a, ok := c.Secondary().(SessionAnswerer); ok {
			ctx, cancel := context.WithTimeout(h.ctx, OpLimit)
			err = a.AnswerQuestion(ctx, name, id, answer)
			cancel()
		}
	}
	if err != nil {
		log.Printf("agent: answering %s: %v", name, err)
		why := err.Error()
		var he HostError
		if errors.As(err, &he) {
			switch {
			case he.Kind == HostOther && he.Detail != "":
				why = he.Detail // pix's own reason ("The question is no longer on screen.")
			case he.Kind == HostTimeout:
				why = "the session didn't answer in time."
			case he.Kind == HostNoAgent:
				why = "the session is gone."
			}
		}
		h.Input(SessionAnswerFailedInput(name, why))
		return
	}
	log.Printf("agent: answered %s's question %s with %s", name, id, answer)
	h.Input(SessionAnsweredInput(name, said))
}

// tellSession types your answer into session name, which ended its turn on a question (#161),
// as message_session would (its own verbatim setting decides the prefix).
func (h *AgentHandle) tellSession(name, answer string) {
	ctx, cancel := context.WithTimeout(h.ctx, OpLimit)
	defer cancel()
	if r := h.sessionTool(ctx, McpCall{Kind: CallMessageSession, Name: name, Text: answer, Force: h.forceSend}); r.Err != nil {
		log.Printf("agent: answering %s: %v", name, r.Err)
		h.Input(SessionAnswerFailedInput(name, r.Err.Error()))
	}
}

// sessionTool runs a session tool on your sessions' host, with the settings the app keeps for
// them (#340).
func (h *AgentHandle) sessionTool(ctx context.Context, call McpCall) McpReply {
	call.Mark = h.markForced.Load()
	return sessionTool(ctx, h.deps.Host.Session(), call, h.maskPending)
}

// SetMarkForced: agent.markForcedSends changed (#340); the next forced message follows it.
func (h *AgentHandle) SetMarkForced(on bool) { h.markForced.Store(on) }

// SetAway: the shell's presence (screen locked, displays asleep or idle).
func (h *AgentHandle) SetAway(away bool) { h.Input(PresenceInput(away)) }

// Updates is the bubble's current list (the `updates` event's lines), for the shell's first read.
func (h *AgentHandle) Updates() []UpdateItem {
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	return h.ctl.Updates()
}

// FocusJob is a click on a bubble line: bring that job's pane (FocusTarget) to the front, then
// attach a terminal. Like OpenTerminal it starts the coordinator, so the terminal never attaches
// to an empty session.
func (h *AgentHandle) FocusJob(ctx context.Context, job JobID, worker string) error {
	if err := h.deps.Host.Session().EnsureSession(ctx); err != nil {
		return err
	}
	h.Input(EnsureCoordinatorInput())
	h.ctlMu.Lock()
	target := h.ctl.FocusTarget(job, worker)
	h.ctlMu.Unlock()
	focusQuietly(ctx, h.deps.Host.Session(), target)
	plan, err := h.deps.Planner.Plan(Attach{Argv: h.deps.Host.AttachCommand(), Dir: h.deps.TerminalDir})
	if err != nil {
		return err
	}
	h.deps.Focus.Focus(plan)
	return nil
}

// RosterDebounce coalesces a burst of changes into one rebuild; RosterBackstop rebuilds anyway,
// for a change no event reported.
const (
	RosterDebounce = 250 * time.Millisecond
	RosterBackstop = 30 * time.Second
)

// Roster is the last roster sent (empty before the first), for the shell's first read.
func (h *AgentHandle) Roster() RosterView {
	h.rosterMu.Lock()
	defer h.rosterMu.Unlock()
	if !h.rosterSent {
		return EmptyRoster()
	}
	return h.rosterLast
}

// FocusAgent is a click on one of Pixie's agents in the roster: bring its pane forward, then
// attach a terminal. Your own sessions are not focused here (the app activates their terminal).
func (h *AgentHandle) FocusAgent(ctx context.Context, name string) error {
	if err := h.deps.Host.Session().EnsureSession(ctx); err != nil {
		return err
	}
	h.Input(EnsureCoordinatorInput())
	focusQuietly(ctx, h.deps.Host.Session(), name)
	plan, err := h.deps.Planner.Plan(Attach{Argv: h.deps.Host.AttachCommand(), Dir: h.deps.TerminalDir})
	if err != nil {
		return err
	}
	h.deps.Focus.Focus(plan)
	return nil
}

// FocusSession is a click on one of your own sessions in the roster (#148): bring forward the
// tab it runs in. Pixie's own session is left alone — nothing is started or attached.
func (h *AgentHandle) FocusSession(ctx context.Context, name string) error {
	_, err := h.ShowSession(ctx, name)
	return err
}

// ShowSession is FocusSession for a session Pixie named (#177): its name, or else its project
// or a part of its description that fits only it (MatchSession). It returns the name shown.
func (h *AgentHandle) ShowSession(ctx context.Context, said string) (string, error) {
	if h.deps.Sessions == nil {
		return "", LaunchError("no way to show your sessions")
	}
	list, err := h.yourSessions(ctx)
	if err != nil {
		return "", err
	}
	name, ok := MatchSession(list, said)
	if !ok {
		return "", LaunchError("no session of yours named " + said)
	}
	for _, a := range list {
		if a.Name != name {
			continue
		}
		if a.User.Pid <= 0 {
			return "", LaunchError("could not find the terminal of " + name)
		}
		plan, err := h.deps.Sessions.PlanSession(a.User.Pid)
		if err != nil {
			return "", err
		}
		h.deps.Focus.Focus(plan)
	}
	return name, nil
}

// yourSessions are the sessions you started with pix. They are the composite's pix part:
// listed on its own, so a failing Pixie host (which the roster shows past) can't hide them.
func (h *AgentHandle) yourSessions(ctx context.Context) ([]AgentInfo, error) {
	var list []AgentInfo
	var err error
	if c, ok := h.deps.Host.Session().(*CompositeHost); ok {
		parts := c.ListRunningParts(ctx)
		list, err = parts.Secondary, parts.SecondaryErr
	} else {
		list, err = ListRunning(ctx, h.deps.Host.Session())
	}
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(list, func(a AgentInfo) bool { return a.Origin != OriginUser || a.User == nil }), nil
}

// SessionPlace is where one of your sessions runs (#277): its folder and its terminal app's
// bundle id ("" = unknown).
type SessionPlace struct {
	Name, Folder, App string
}

// SessionPlaces are where your sessions named run, in the order named, from one listing; a name
// that isn't one of yours is left out.
func (h *AgentHandle) SessionPlaces(ctx context.Context, names []string) []SessionPlace {
	list, err := h.yourSessions(ctx)
	if err != nil {
		return nil
	}
	var out []SessionPlace
	for _, n := range names {
		for _, a := range list {
			if a.Name == n {
				out = append(out, SessionPlace{Name: n, Folder: a.Cwd, App: a.User.App})
				break
			}
		}
	}
	return out
}

// MatchSession picks the one session of yours that said names: its name; else its repo or its
// description's project (the part before the colon); else words of its description. Case,
// hyphens and spacing don't count. A step that fits several sessions settles it: none.
func MatchSession(yours []AgentInfo, said string) (string, bool) {
	words := func(s string) string {
		return strings.Join(strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}), " ")
	}
	w := words(said)
	if w == "" {
		return "", false
	}
	steps := []func(a AgentInfo) bool{
		func(a AgentInfo) bool { return a.Name == said },
		func(a AgentInfo) bool {
			project, _, _ := strings.Cut(a.User.Description, ":")
			return words(a.User.Repo) == w || words(project) == w
		},
		func(a AgentInfo) bool { return strings.Contains(" "+words(a.User.Description)+" ", " "+w+" ") },
	}
	for _, fits := range steps {
		var found []string
		for _, a := range yours {
			if fits(a) {
				found = append(found, a.Name)
			}
		}
		if len(found) > 0 {
			return found[0], len(found) == 1
		}
	}
	return "", false
}

func (h *AgentHandle) pokeRoster() {
	select {
	case h.rosterPoke <- struct{}{}:
	default:
	}
}

// runRoster rebuilds the roster after a poke or a host change (debounced), and on the backstop.
func (h *AgentHandle) runRoster() {
	if h.deps.Roster == nil {
		return
	}
	var changes <-chan struct{}
	if n, ok := h.deps.Host.Session().(ChangeNotifier); ok {
		changes = n.Changes()
	}
	back := time.NewTicker(RosterBackstop)
	defer back.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.rosterPoke:
		case <-changes:
		case <-back.C:
		}
		select {
		case <-h.ctx.Done():
			return
		case <-time.After(RosterDebounce):
		}
		select { // a poke during the wait is covered by this rebuild
		case <-h.rosterPoke:
		default:
		}
		h.rebuildRoster()
	}
}

// rebuildRoster lists the hosts passively (never starting Herdr or tmux): when Pixie's host
// can't be listed, its part of the last roster is kept and your sessions still refresh (and
// the other way round).
func (h *AgentHandle) rebuildRoster() {
	if h.quitting() {
		return
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	session := h.deps.Host.Session()
	var parts ListParts
	if c, ok := session.(*CompositeHost); ok {
		parts = c.ListRunningParts(ctx)
	} else {
		parts.Primary, parts.PrimaryErr = ListRunning(ctx, session)
	}
	h.logRosterErr(parts.PrimaryErr)
	if parts.PrimaryErr != nil && parts.SecondaryErr != nil {
		return
	}
	h.ctlMu.Lock()
	workers := h.ctl.WorkerRows()
	h.ctlMu.Unlock()
	h.rosterMu.Lock()
	last := h.rosterLast
	if !h.rosterSent {
		last = EmptyRoster()
	}
	h.rosterMu.Unlock()
	list := append(parts.Primary, parts.Secondary...)
	h.stops.seen(list)
	v := BuildRoster(list, workers, session.Name())
	if parts.PrimaryErr != nil {
		v.Agents, v.AgentCount = last.Agents, last.AgentCount
	}
	if parts.SecondaryErr != nil {
		v.Yours, v.YourCount = last.Yours, last.YourCount
	}
	v.Busy = v.IsBusy() // over the kept part too
	h.rosterMu.Lock()
	if h.rosterSent && reflect.DeepEqual(v, h.rosterLast) {
		h.rosterMu.Unlock()
		return
	}
	h.rosterLast, h.rosterSent = v, true
	h.rosterMu.Unlock()
	h.deps.Roster.Roster(v)
}

// logRosterErr logs a failing roster list once per change of its text, not every backstop.
func (h *AgentHandle) logRosterErr(err error) {
	text := ""
	if err != nil {
		text = err.Error()
	}
	h.rosterMu.Lock()
	changed := text != h.rosterErr
	h.rosterErr = text
	h.rosterMu.Unlock()
	if changed && err != nil {
		log.Printf("agent: roster: %v", err)
	}
}

// Voice forwards the voice loop's signals.
func (h *AgentHandle) Voice(sig VoiceSignal) { h.Input(VoiceInput(sig)) }

// Restart is tray ▸ Restart coordinator.
func (h *AgentHandle) Restart() { h.Input(RestartInput()) }

// ClearCoordinatorModel makes the coordinator's next launches use Claude Code's default model.
func (h *AgentHandle) ClearCoordinatorModel() {
	h.deps.Host.ClearModel()
}

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
	defer h.pokeRoster()
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
		if err := host.PromptCoordinator(ctx, op.text); err != nil {
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
	// The mode first: a permission request is then decided knowing it.
	if ev.PermissionMode != "" {
		h.Input(ModeInput(ev.Agent, ev.PermissionMode))
	}
	switch ev.Kind {
	case HookMode:
	case HookSubmit:
		h.Input(SubmitInput(ev.Agent, ev.Message))
	case HookStop:
		h.Input(StopInput(ev.Agent, ev.Message))
	case HookPostTool:
		h.Input(PostToolInput(ev.Agent, ev.ToolName, ev.ToolInput))
	case HookState:
		// The host turns it into a PaneEvent (deduplicated with what it detects itself), which
		// comes back here as a PaneInput. A host that detects status itself (Herdr) ignores it.
		if r, ok := h.deps.Host.Session().(StatusReporter); ok {
			r.ReportStatus(ev.Agent, ev.Status)
		}
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

// mcp answers a pixie MCP tool call.
func (h *AgentHandle) mcp(agent string, call McpCall) McpReply {
	if h.quitting() {
		return McpReply{Err: errQuitting}
	}
	if call.Kind == CallStartWorker {
		return h.startWorker(agent, call.Worker)
	}
	if call.Kind == CallCreateProject {
		return h.createProject(agent, call.Name)
	}
	if isSessionCall(call.Kind) {
		// Sessions you started with pix (#89): the host answers, not the controller; they
		// are not workers and have no job.
		if agent != Coordinator {
			return McpReply{Err: errors.New("This tool is not available to you.")}
		}
		var tool string
		var input map[string]any
		switch {
		case call.Kind == CallCloseSession:
			tool, input = CloseSessionTool, map[string]any{}
		case call.Kind == CallMessageSession:
			if actions := GatedActions(call.Text); len(actions) > 0 {
				tool, input = MessageSessionTool, map[string]any{"actions": actions}
			}
		}
		if call.Kind == CallMessageSession && h.forceSend {
			call.Force = true
		}
		if tool != "" {
			awaiting := McpReply{Value: map[string]any{"name": call.Name, "status": "awaiting_confirmation", "note": AwaitingConfirmNote}}
			key := fmt.Sprintf("%v\x00%s\x00%s", call.Kind, call.Name, call.Text)
			if !h.await(key, true) {
				return awaiting // already asked; it happens once you confirm
			}
			job := h.inFlightJob()
			d, later, answered := h.confirmSession(call.Name, tool, input)
			if !answered {
				// #246: answered before the coordinator's call gives up; done once you confirm.
				go func() {
					defer h.await(key, false)
					select {
					case d := <-later:
						if d.Allow {
							h.runLateSessionTool(call, job)
						}
					case <-h.ctx.Done():
					}
				}()
				return awaiting
			}
			h.await(key, false)
			if !d.Allow {
				return McpReply{Err: errors.New(d.Message)}
			}
		}
		return h.runSessionTool(call)
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

// AwaitingConfirmNote tells the coordinator a session tool waits on your confirm (#246).
const AwaitingConfirmNote = "Not done yet: it waits for the user to confirm (they've been asked). Don't call it again; it happens as soon as they confirm, and not at all if they say no."

// runSessionTool runs a session tool call on the host and tells the controller what a message
// did (sent, or waiting and why).
func (h *AgentHandle) runSessionTool(call McpCall) McpReply {
	ctx, cancel := context.WithTimeout(h.ctx, OpLimit)
	defer cancel()
	r := h.sessionTool(ctx, call)
	if (call.Kind == CallMessageSession || call.Kind == CallFlushSession || call.Kind == CallStopAndSendSession) && r.Err == nil {
		v, _ := r.Value.(map[string]any)
		if reason, _ := v["reason"].(string); v["status"] == "waiting" {
			h.Input(SessionWaitingInput(call.Name, h.sessionDescription(call.Name), reason).WithSeq(r.Seq, r.Run))
		} else {
			h.Input(SessionSentInput(call.Name, h.sessionDescription(call.Name)).WithSeq(r.Seq, r.Run))
		}
	}
	return r
}

// runLateSessionTool is runSessionTool for a call already answered "awaiting_confirmation"
// (#246): what it did goes on job, the request then in flight, or to you directly.
func (h *AgentHandle) runLateSessionTool(call McpCall, job JobID) {
	ctx, cancel := context.WithTimeout(h.ctx, OpLimit)
	defer cancel()
	r := h.sessionTool(ctx, call)
	if call.Kind != CallMessageSession && call.Kind != CallFlushSession && r.Err == nil {
		return
	}
	why, errText := "", ""
	if r.Err != nil {
		errText = r.Err.Error()
	} else if v, _ := r.Value.(map[string]any); v["status"] == "waiting" {
		why, _ = v["reason"].(string)
	}
	h.Input(LateSessionSentInput(job, call.Name, h.sessionDescription(call.Name), why, errText).WithSeq(r.Seq, r.Run))
}

// await marks key waiting on a late confirm (on) or done (off); false if it already waited.
func (h *AgentHandle) await(key string, on bool) bool {
	h.awaitMu.Lock()
	defer h.awaitMu.Unlock()
	if !on {
		delete(h.awaiting, key)
		return true
	}
	if h.awaiting[key] {
		return false
	}
	if h.awaiting == nil {
		h.awaiting = map[string]bool{}
	}
	h.awaiting[key] = true
	return true
}

// inFlightJob is the one open request the coordinator is working on ("" = none, or several).
func (h *AgentHandle) inFlightJob() JobID {
	h.ctlMu.Lock()
	defer h.ctlMu.Unlock()
	if refs := unique(h.ctl.inbox.InFlightRefs()); len(refs) == 1 && h.ctl.jobs.IsOpen(refs[0]) {
		return refs[0]
	}
	return ""
}

// sessionDescription is how the roster describes your session name ("" = not listed).
func (h *AgentHandle) sessionDescription(name string) string {
	for _, row := range h.Roster().Yours {
		if row.Name == name {
			return row.Label
		}
	}
	return ""
}

// maskPending removes the pending confirm words from text a session wrote, so the coordinator
// can never read one back as your confirmation.
func (h *AgentHandle) maskPending(text string) string {
	h.ctlMu.Lock()
	tokens := h.ctl.pendingTokens()
	h.ctlMu.Unlock()
	return maskPendingTokens(text, tokens)
}

// confirmSession asks you before a session tool does something hard to undo to a session you
// started (close_session ending it, message_session having it push or merge), as a permission
// request answered by a spoken confirm word. A name that is not yours is not asked about:
// sessionTool refuses it. Not answered within confirmWait, answered is false and later brings
// the answer (#246).
func (h *AgentHandle) confirmSession(name, toolName string, input map[string]any) (Decision, <-chan Decision, bool) {
	c, ok := h.deps.Host.Session().(*CompositeHost)
	if !ok || !c.Secondary().Owns(name) {
		return Allow(), nil, true
	}
	t := time.NewTimer(h.confirmWait) // from the call's start: the List below counts too
	defer t.Stop()
	input["name"] = name
	ctx, cancel := context.WithTimeout(h.ctx, OpLimit)
	if list, err := c.Secondary().List(ctx); err == nil {
		for _, a := range list {
			if a.Name == name && a.User != nil {
				input["description"] = a.User.Description
			}
		}
	}
	cancel()
	rx := make(chan Decision, 1)
	if h.quitting() {
		return Deny("The app quit."), nil, true
	}
	// As h.hook's HookPermission, keeping the id: a confirm left waiting is detached from its job.
	id := h.nextRequest.Add(1)
	h.repliesMu.Lock()
	h.replies[id] = rx
	h.repliesMu.Unlock()
	h.Input(PermissionInput(id, Coordinator, toolName, input))
	if h.quitting() {
		h.denyAllWaiting()
	}
	select {
	case d := <-rx:
		return d, nil, true
	case <-t.C:
		// #249: the coordinator ends its request now; this confirm must outlive it.
		h.Input(DetachConfirmInput(id))
		return Decision{}, rx, false
	case <-h.ctx.Done():
		return Deny("The app quit."), nil, true
	}
}

// createProject is create_project (#97): a new repository under the projects root.
func (h *AgentHandle) createProject(agent, name string) McpReply {
	if agent != Coordinator {
		return McpReply{Err: errors.New("This tool is not available to you.")}
	}
	if h.workers == nil {
		return McpReply{Err: errors.New("Workers are turned off (agent.workers.enabled).")}
	}
	path, err := within(h.ctx, RepoLimit, func(context.Context) (string, error) {
		return h.workers.Worktrees.CreateProject(name)
	})
	if errors.Is(err, errTimedOut) {
		return McpReply{Err: errors.New("Couldn't create the project in time; try again.")}
	}
	if err != nil {
		return McpReply{Err: err}
	}
	return McpReply{Value: map[string]any{"name": name, "path": path}}
}

// existingUserSessions is the pix sessions you started yourself (#89) already open on
// repoFolder (the git root's folder name): issue #126, so start_worker can prefer one of these
// over duplicating the work. A host that isn't a CompositeHost, or a listing that fails, has
// none: a worker is still better than refusing one over an unrelated failure.
func existingUserSessions(ctx context.Context, host SessionHost, repoFolder string) []AgentInfo {
	c, ok := host.(*CompositeHost)
	if !ok {
		return nil
	}
	list, err := within(ctx, ListLimit, c.Secondary().List)
	if err != nil {
		return nil
	}
	var out []AgentInfo
	for _, a := range list {
		if a.Origin == OriginUser && a.User != nil && a.User.Repo == repoFolder {
			out = append(out, a)
		}
	}
	return out
}

// sessionRef names a pix session for the coordinator (never for the person: that's the prompt's
// job), including its name so a follow-up message_session can use it.
func sessionRef(a AgentInfo) string {
	d := a.User.Description
	if d == "" {
		d = "no description"
	}
	where := a.User.Where
	if where == "" {
		where = "unknown"
	}
	return fmt.Sprintf("%q in %s (name %s, status %s)", d, where, a.Name, a.Status.String())
}

// existingSessionMessage tells the coordinator about matches so it can message one (or ask the
// person which, per the existing disambiguation rule) instead of starting a worker.
func existingSessionMessage(matches []AgentInfo, repoFolder string) string {
	refs := make([]string, len(matches))
	for i, a := range matches {
		refs[i] = sessionRef(a)
	}
	if len(refs) == 1 {
		return fmt.Sprintf("You already have a session open on %s: %s. Message it with message_session instead, or call start_worker again with override_existing_session: true if the person explicitly asked for a separate worker.", repoFolder, refs[0])
	}
	return fmt.Sprintf("You already have sessions open on %s: %s. Ask the person which one, then message it, or call start_worker again with override_existing_session: true if they explicitly asked for a separate worker.", repoFolder, strings.Join(refs, "; "))
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
	// #126: a pix session already open on this repo beats duplicating the work in a worker,
	// unless the request explicitly overrides that (the person asked for a worker anyway).
	if repoFolder := dirName(found.repo); !req.OverrideExistingSession {
		if matches := existingUserSessions(h.ctx, session, repoFolder); len(matches) > 0 {
			return refuse("existing_session", existingSessionMessage(matches, repoFolder))
		}
	}
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
	var refusals []StartRefusal
	if prepared.Refusal != nil {
		refusals = []StartRefusal{*prepared.Refusal}
	}
	if err := checkStartScreen(ctx, session, plan.Name, refusals, prepared.Ready, info.Status, began); err != nil {
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
	// Once quitting, no lane is made: Shutdown has cleared them and stops, closes and forgets
	// every worker itself, so a forget can't land on a late lane and run after it returns (#352).
	if h.quitting() {
		return
	}
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
	defer h.pokeRoster()
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
		text := e.Text
		if h.deps.Attachments != nil {
			// Its own copies of the job's attachments, in its folder (#240).
			h.ctlMu.Lock()
			dir := h.ctl.WorktreeOf(name)
			h.ctlMu.Unlock()
			text = h.deps.Attachments.ForWorker(text, dir)
		}
		_, err := within(h.ctx, OpLimit, func(c context.Context) (struct{}, error) {
			if h.setReconnect(name, false) {
				if _, err := ReconnectMcp(c, session, name); err != nil {
					// Nothing was typed: the next prompt tries again.
					h.setReconnect(name, true)
					return struct{}{}, err
				}
			}
			return struct{}{}, session.Prompt(c, name, text)
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
	h.setReconnect(name, false)
	// On quit, Shutdown re-checks every worker still known within its limit.
	if !h.quitting() {
		if k, ok := h.takeKnown(name); ok {
			go auditQuietly(h.workers.Worktrees, name, k)
		}
	}
}

func (h *AgentHandle) queueOp(op hostOp) { h.ops.push(op) }

// setReconnect sets whether the worker's next prompt is preceded by McpReconnect, and reports
// whether it was.
func (h *AgentHandle) setReconnect(name string, on bool) bool {
	h.reconnectMu.Lock()
	defer h.reconnectMu.Unlock()
	was := h.reconnect[name]
	if on {
		if h.reconnect == nil {
			h.reconnect = map[string]bool{}
		}
		h.reconnect[name] = true
	} else {
		delete(h.reconnect, name)
	}
	return was
}

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
	case EfAnswerSession:
		go h.answerSession(e.Name, e.Text, e.Answer, e.Line)
	case EfTellSession:
		go h.tellSession(e.Name, e.Text)
	case EfChat:
		if d.Chat != nil {
			d.Chat.Chat(e.Chat)
		}
	case EfInject:
		if d.Voice.IsOpen() {
			d.Voice.Inject(e.Notice)
		} else {
			h.Input(InjectBouncedInput(e.Notice))
		}
	case EfHoldOpen:
		d.Voice.HoldOpen(e.Hold)
	case EfWithdrawHeld:
		if w, ok := d.Voice.(HeldWithdrawer); ok {
			w.WithdrawHeld(e.Name, e.Seq)
		}
	case EfReply:
		if d.Replies != nil {
			d.Replies.Reply(*e.Reply)
		}
	case EfLine:
		if d.Lines != nil {
			d.Lines.Line(e.Outbox)
		}
	case EfChatEnded:
		if ender, ok := d.Chat.(ChatEnder); ok {
			ender.ChatEnded(e.Job)
		}
	case EfDelivered:
		if d.Delivered != nil {
			d.Delivered.Delivered(e.Value.([]uint64))
		}
	case EfUpdates:
		if d.Updates != nil {
			d.Updates.Updates(e.Value.([]UpdateItem))
		}
	case EfOpenConversation:
		if !d.Voice.Open() {
			h.Input(OpenRefusedInput())
		}
	case EfChime:
		d.Notifier.Chime()
	case EfNotify:
		if un, ok := d.Notifier.(UpdateNotifier); ok && e.Update != nil {
			un.NotifyUpdate("Pixie", e.Text, *e.Update)
		} else {
			d.Notifier.Notify("Pixie", e.Text)
		}
	case EfCue:
		d.Pixie.Cue(e.Cue)
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
	case EfShowPane:
		if d.Panes != nil {
			d.Panes.ShowPane(e.Job, e.Name)
		}
	case EfManual:
		if d.Modes != nil {
			d.Modes.ManualMode(e.Agent, e.Manual)
		}
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
// reason, and nothing is typed. With ready, the start is up only once the screen has shown its
// marker and no refusal screen on every read for ready.Settle, watched until ReadyWindow after
// began (at least DialogWait, and on past it while a settle that began before it runs); if it
// never is, the start fails with its reason naming the last screen line. Without it, the
// screen is read once, and again for up to DialogWait only while the pane is blocked.
func checkStartScreen(ctx context.Context, session SessionHost, name string, refusals []StartRefusal, ready *StartReady, status PaneStatus, began time.Time) error {
	if len(refusals) == 0 && ready == nil {
		return nil
	}
	deadline := time.Now().Add(DialogWait)
	if ready != nil {
		if r := began.Add(ReadyWindow); r.After(deadline) {
			deadline = r
		}
	}
	last := ""
	answeredAt := make([]time.Time, len(refusals)) // when each refusal's Answer was sent; zero: not yet
	var since time.Time                            // when the marker began to show on every read; zero: it doesn't
	for {
		text, err := within(ctx, max(time.Until(deadline), 0), func(c context.Context) (string, error) {
			return session.Read(c, name, 40)
		})
		if err == nil {
			for i, r := range refusals {
				if !ShowsAnyScreen(text, r.Screens) {
					continue
				}
				if r.Answer != nil && answeredAt[i].IsZero() {
					if err := session.SendKeys(ctx, name, r.Answer); err != nil {
						return errors.New(r.Reason)
					}
					answeredAt[i] = time.Now()
					log.Printf("agent: answered %s's start screen with %v", name, r.Answer)
					continue
				}
				if r.Answer == nil || time.Since(answeredAt[i]) >= AnswerGrace {
					return errors.New(r.Reason)
				}
			}
			last = lastLine(text)
			if ready != nil {
				if !strings.Contains(text, ready.Marker) {
					since = time.Time{}
				} else {
					if since.IsZero() {
						since = time.Now()
						// A settle that begins before the deadline runs to its end (and one read past it).
						if end := since.Add(ready.Settle + StartScreenPoll); end.After(deadline) {
							deadline = end
						}
					}
					if time.Since(since) >= ready.Settle {
						return nil
					}
				}
			}
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
	case HostRefused:
		return "refused"
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
