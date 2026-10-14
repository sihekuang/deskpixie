package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sihekuang/deskpixie/core/voice"
)

// AgentController (port of agent-core controller.rs, spec §5–§6): every delegation rule as a
// pure state machine, like voice's Conversation. No I/O and no clock: the caller passes `now`
// and carries out the returned effects, in order. The worker rules are in controller_workers.go.

// BlockedGraceMs: a coordinator Blocked this long with no pending hook permission gets the
// terminal hint.
const BlockedGraceMs Millis = 3_000

const BlockedHint = "The agent is waiting at a prompt in its terminal; click me to open it."

// unreadableDenial is what the agent is told when a request cannot be read aloud plainly
// enough to approve by voice.
const unreadableDenial = "This can't be approved by voice. Ask the user to run it themselves, or to change it so it can be read aloud plainly."

const unreadableNotice = "The agent asked to run something I can't read out safely, so I said no. Open the terminal to see it."

const (
	// MaxPromptFailures: this many prompts failing in a row (no finished turn between) fail the
	// jobs they carried.
	MaxPromptFailures uint32 = 3
	// PromptRetryMs: a failed prompt is retried this long after the first failure, doubling.
	PromptRetryMs Millis = 2_000
	// StopSettleMs: after a Stop, the next prompt goes out on Idle/Done or after this settle,
	// whichever is first.
	StopSettleMs Millis = 1_500
	// PromptWatchdogMs: a prompt whose host call timed out (it may have been typed) with no Stop
	// or status event within this long fails its jobs.
	PromptWatchdogMs Millis = 60_000
	// IdleWithoutStopMs: an Idle/Done with no Stop for the prompt in flight makes the inbox
	// ready after this long (a lost Stop hook must not wedge it).
	IdleWithoutStopMs Millis = 10_000
)

const unreachable = "I couldn't reach the agent"

// timerNotice is how the voice loop's timer notices start (the timer handles its own
// undelivered ones).
const timerNotice = "Timer done:"

// Settings are the controller's `agent.*` settings.
type Settings struct {
	NotifyPerMinute     int
	PermissionTimeoutMs Millis
	Narration           bool
	// KeepPanesOnQuit (`agent.keepPanesOnQuit`): leave the coordinator and workers running when
	// the app quits.
	KeepPanesOnQuit bool
	// Workers is `agent.workers`.
	Workers WorkerPolicy
}

// DefaultSettings: 4 notifications a minute, 2-minute permission timeout, narration on.
func DefaultSettings() Settings {
	return Settings{NotifyPerMinute: 4, PermissionTimeoutMs: 120_000, Narration: true, Workers: DefaultWorkerPolicy()}
}

// WorkerPolicy is `agent.workers` (spec §12): whether the coordinator may start workers, how
// many at once, and which kinds.
type WorkerPolicy struct {
	Enabled bool
	Max     int
	Kinds   []AgentKind
}

// DefaultWorkerPolicy: off, at most 4, Claude and Codex.
func DefaultWorkerPolicy() WorkerPolicy {
	return WorkerPolicy{Enabled: false, Max: 4, Kinds: []AgentKind{Claude, Codex}}
}

// InputKind mirrors the Rust `Input` enum.
type InputKind int

const (
	InRestored           InputKind = iota // Jobs, Outbox, Keep: the stored jobs.jsonl and outbox.jsonl lines, at start
	InDelegate                            // Job, Task, Context ("" = none)
	InCancel                              // Job
	InAnswer                              // Job ("" = the only job waiting on an answer), Text; answered with EfAnswerResult
	InEnsureCoordinator                   // start the coordinator now if it isn't running
	InCoordinatorUp                       // Status: SessionHost ensure_coordinator succeeded
	InCoordinatorFailed                   // HostErr
	InPane                                // Pane
	InStop                                // Agent, Message
	InPermission                          // RequestID, Agent, ToolName, ToolInput
	InPostTool                            // Agent, ToolName, ToolInput
	InVoice                               // Voice
	InInjectBounced                       // Notice: the executor found the conversation closed when it tried to inject it
	InDisable                             // StatusLine: delegation off for a setup reason (sticky: Restart keeps it)
	InNotice                              // StatusLine: a setup note that disables nothing
	InPromptFailed                        // EfPrompt could not be delivered
	InPromptTimedOut                      // the EfPrompt host call timed out: it may or may not have been typed
	InTick                                // about once a second
	InRestart                             // tray ▸ Restart agent
	InShutdown                            // app quit
	InMcp                                 // Agent, Call: a mascot MCP tool call other than start_worker; answered with EfToolResult
	InAdmitWorker                         // Worker, Repo (canonical), Taken: start_worker; answered with EfAdmitted
	InWorkerUp                            // Name, Worktree, Status: an admitted worker's pane is up
	InWorkerFailed                        // Name, Reason: an admitted worker never started
	InWorkerClosed                        // Name: EfStopWorker finished (the pane is closed or was already gone)
	InWorkerScreen                        // Name, Text: EfReadWorker's screen
	InWorkerPromptFailed                  // Name, Text: EfPromptWorker could not be typed
	InAdoptWorker                         // Plan, Worktree, Status: a worker kept over the quit, its pane still there; answered with EfAdmitted
)

// Input is one stimulus for AgentController.Step. Build it with the *Input constructors.
type Input struct {
	Kind       InputKind
	Jobs       []string
	Outbox     []string
	Job        JobID
	Task       string
	Context    string
	Text       string
	Status     PaneStatus
	HostErr    HostError
	Pane       PaneEvent
	Agent      string
	Message    string
	RequestID  RequestID
	ToolName   string
	ToolInput  any
	Voice      VoiceSignal
	Notice     Notice
	StatusLine string
	Call       McpCall
	Worker     WorkerRequest
	Repo       string
	Taken      []string
	Name       string
	Worktree   string
	Reason     string
	// Keep (InRestored): the coordinator was kept running over the quit (agent.keepPanesOnQuit).
	Keep bool
	Plan WorkerPlan
}

// RestoredInput: keep says the coordinator was kept running over the quit: its open jobs stay
// open and it is asked for their results again.
func RestoredInput(jobs, outbox []string, keep bool) Input {
	return Input{Kind: InRestored, Jobs: jobs, Outbox: outbox, Keep: keep}
}
func DelegateInput(id JobID, task, context string) Input {
	return Input{Kind: InDelegate, Job: id, Task: task, Context: context}
}
func CancelInput(id JobID) Input { return Input{Kind: InCancel, Job: id} }

// AnswerInput: id "" means the only job waiting on an answer.
func AnswerInput(id JobID, text string) Input { return Input{Kind: InAnswer, Job: id, Text: text} }
func EnsureCoordinatorInput() Input           { return Input{Kind: InEnsureCoordinator} }
func CoordinatorUpInput(status PaneStatus) Input {
	return Input{Kind: InCoordinatorUp, Status: status}
}
func CoordinatorFailedInput(err HostError) Input {
	return Input{Kind: InCoordinatorFailed, HostErr: err}
}
func PaneInput(ev PaneEvent) Input { return Input{Kind: InPane, Pane: ev} }
func StopInput(agent, message string) Input {
	return Input{Kind: InStop, Agent: agent, Message: message}
}
func PermissionInput(id RequestID, agent, toolName string, toolInput any) Input {
	return Input{Kind: InPermission, RequestID: id, Agent: agent, ToolName: toolName, ToolInput: toolInput}
}
func PostToolInput(agent, toolName string, toolInput any) Input {
	return Input{Kind: InPostTool, Agent: agent, ToolName: toolName, ToolInput: toolInput}
}
func VoiceInput(sig VoiceSignal) Input          { return Input{Kind: InVoice, Voice: sig} }
func InjectBouncedInput(n Notice) Input         { return Input{Kind: InInjectBounced, Notice: n} }
func DisableInput(status string) Input          { return Input{Kind: InDisable, StatusLine: status} }
func NoticeInput(status string) Input           { return Input{Kind: InNotice, StatusLine: status} }
func PromptFailedInput() Input                  { return Input{Kind: InPromptFailed} }
func PromptTimedOutInput() Input                { return Input{Kind: InPromptTimedOut} }
func TickInput() Input                          { return Input{Kind: InTick} }
func RestartInput() Input                       { return Input{Kind: InRestart} }
func ShutdownInput() Input                      { return Input{Kind: InShutdown} }
func McpInput(agent string, call McpCall) Input { return Input{Kind: InMcp, Agent: agent, Call: call} }
func AdmitWorkerInput(req WorkerRequest, repo string, taken []string) Input {
	return Input{Kind: InAdmitWorker, Worker: req, Repo: repo, Taken: taken}
}
func AdoptWorkerInput(plan WorkerPlan, worktree string, status PaneStatus) Input {
	return Input{Kind: InAdoptWorker, Plan: plan, Worktree: worktree, Status: status}
}
func WorkerUpInput(name, worktree string, status PaneStatus) Input {
	return Input{Kind: InWorkerUp, Name: name, Worktree: worktree, Status: status}
}
func WorkerFailedInput(name, reason string) Input {
	return Input{Kind: InWorkerFailed, Name: name, Reason: reason}
}
func WorkerClosedInput(name string) Input { return Input{Kind: InWorkerClosed, Name: name} }
func WorkerScreenInput(name, text string) Input {
	return Input{Kind: InWorkerScreen, Name: name, Text: text}
}
func WorkerPromptFailedInput(name, text string) Input {
	return Input{Kind: InWorkerPromptFailed, Name: name, Text: text}
}

// EffectKind mirrors the Rust `Effect` enum.
type EffectKind int

const (
	EfStartCoordinator EffectKind = iota // SessionHost ensure_coordinator; answered with InCoordinatorUp or InCoordinatorFailed
	EfPrompt                             // Text: type this into the coordinator
	EfInterrupt                          // send Escape to the coordinator
	EfInject                             // Notice
	EfHoldOpen                           // Hold
	EfChime
	EfNotify          // Text
	EfCue             // Cue
	EfDecide          // ID, Decision
	EfAppend          // File, Line
	EfRewrite         // File, Lines
	EfStatus          // Text: the tray's agent status line; "" clears it (Rust's Status(None))
	EfStopCoordinator // Escape, then close the coordinator pane
	EfResetHost       // SessionHost reset: forget the crash history
	EfLog             // Text
	EfAnswerResult    // Job, or Err: the result of InAnswer (on Err no state changed)
	EfAdmitted        // Plan, or Err: the answer to InAdmitWorker
	EfToolResult      // Value, or Err: the answer to InMcp (the error is what the agent sees)
	EfPromptWorker    // Name, Text: type this into a worker
	EfSubmitWorker    // Name: press Enter in the worker's pane (a prompt sits in its input, not submitted)
	EfStopWorker      // Name: Escape, then close the pane after a grace (answered with InWorkerClosed)
	EfReadWorker      // Name: read the worker's screen (answered with InWorkerScreen)
	EfForgetWorker    // Name: the worker is gone: revoke its token and delete its launch files
)

// Effect is one thing the caller must do, in order. Err set (non-empty) is the Err branch of
// EfAnswerResult, EfAdmitted and EfToolResult.
type Effect struct {
	Kind     EffectKind
	Text     string
	Notice   Notice
	Hold     bool
	Cue      Cue
	ID       RequestID
	Decision Decision
	File     string
	Line     string
	Lines    []string
	Job      JobID
	Plan     WorkerPlan
	Value    any
	Err      string
	Name     string
}

type coordinatorState int

const (
	coordAbsent coordinatorState = iota
	coordStarting
	coordUp
)

type injectedNotice struct {
	text  string
	lines []Line
}

type watchdog struct {
	at   Millis
	jobs []JobID
}

// AgentController holds the delegation state. Not safe for concurrent use; the runtime owns it.
type AgentController struct {
	settings    Settings
	jobs        *JobTable
	inbox       *Inbox
	router      *Router
	outbox      *Outbox
	confirms    *Confirmations
	narrator    *Narrator
	voiceOpen   bool
	coordinator coordinatorState
	blocked     optMs
	hinted      bool
	// disabled: why delegation is off for a runtime reason ("" = it isn't). Restart clears it.
	disabled string
	// setupDisabled: why delegation is off for a setup reason (port busy, invalid settings,
	// herdr not found). Sticky: Restart does not clear it, since restarting fixes nothing.
	setupDisabled string
	// notice (InNotice): the status line when nothing else is shown.
	notice string
	// promptFailures: prompts that failed since the coordinator last finished a turn.
	promptFailures uint32
	// retryAt: when a failed prompt is tried again (the coordinator is up and idle; no event will come).
	retryAt optMs
	// settleAt: when the inbox counts as ready after a Stop if no Idle/Done came first.
	settleAt optMs
	// watchdog: a timed-out prompt's deadline for a sign of life, and the jobs it carried.
	watchdog *watchdog
	// awaitingStop: a prompt went out and its turn's Stop hasn't arrived: an Idle/Done alone
	// (which can beat the Stop) does not make the inbox ready.
	awaitingStop bool
	// idleWaitAt: an Idle/Done came while awaiting the Stop: if the Stop is lost, ready at this time.
	idleWaitAt optMs
	held       bool
	status     string
	// injected: recently injected notices and the lines each one carried (none: narration,
	// which is dropped rather than notified if it bounces).
	injected []injectedNotice
	shutDown bool
	workers  *Workers
	// coordStatus: the coordinator pane's latest status.
	coordStatus PaneStatus
	// watch: liveness of the open working jobs (liveness.go).
	watch map[JobID]*liveness
}

func NewAgentController(settings Settings) *AgentController {
	return &AgentController{
		router:   NewRouter(settings.NotifyPerMinute),
		confirms: NewConfirmations(settings.PermissionTimeoutMs),
		narrator: NewNarrator(NarrateIntervalMs),
		settings: settings,
		jobs:     NewJobTable(),
		inbox:    NewInbox(),
		outbox:   NewOutbox(),
		workers:  NewWorkers(),
	}
}

// Disabled is why delegate must refuse, if delegation is off (a setup reason wins).
func (c *AgentController) Disabled() (string, bool) {
	if c.setupDisabled != "" {
		return c.setupDisabled, true
	}
	return c.disabled, c.disabled != ""
}

func (c *AgentController) isDisabled() bool { _, off := c.Disabled(); return off }

func (c *AgentController) Snapshot(now Millis) []JobView { return c.jobs.Snapshot(now) }

func (c *AgentController) OpenJobs() []JobID {
	var out []JobID
	for _, j := range c.jobs.Open() {
		out = append(out, j.ID)
	}
	return out
}

func joinIDs(ids []JobID) string {
	s := make([]string, len(ids))
	for k, id := range ids {
		s[k] = id.String()
	}
	return strings.Join(s, ", ")
}

// ResolveCancel is the `jobs` tool's cancel target: the given open job, or (id "") the only
// open one.
func (c *AgentController) ResolveCancel(id string) (JobID, error) {
	open := c.OpenJobs()
	if raw := strings.TrimSpace(id); raw != "" {
		if j, ok := ParseJobID(raw); ok && slices.Contains(open, j) {
			return j, nil
		}
		return "", fmt.Errorf("No open job %s.", raw)
	}
	switch len(open) {
	case 0:
		return "", errors.New("No job is open.")
	case 1:
		return open[0], nil
	}
	return "", fmt.Errorf("Several jobs are open (%s). Which one?", joinIDs(open))
}

// Step applies one input and returns the effects to carry out, in order.
func (c *AgentController) Step(in Input, now Millis) []Effect {
	var fx []Effect
	switch in.Kind {
	case InRestored:
		c.restore(in.Jobs, in.Outbox, in.Keep, now, &fx)
	case InDelegate:
		c.delegate(in.Job, in.Task, in.Context, now, &fx)
	case InCancel:
		c.cancel(in.Job, now, &fx)
	case InAnswer:
		c.answer(in.Job, in.Text, now, &fx)
	case InEnsureCoordinator:
		if !c.isDisabled() && c.coordinator == coordAbsent {
			c.coordinator = coordStarting
			fx = append(fx, Effect{Kind: EfStartCoordinator})
		}
	case InCoordinatorUp:
		c.coordinator = coordUp
		c.onCoordinatorStatus(in.Status, now, &fx)
		if c.status == "Agent: claude did not start" {
			c.setStatus(c.notice, &fx)
		}
		c.flush(now, &fx)
	case InCoordinatorFailed:
		c.coordinatorFailed(in.HostErr, now, &fx)
	case InPane:
		ev := in.Pane
		switch {
		case ev.Kind == PaneEventStatus && ev.Agent == Coordinator:
			c.onCoordinatorStatus(ev.Status, now, &fx)
			c.flush(now, &fx)
		case ev.Kind == PaneEventExited && ev.Agent == Coordinator:
			fx = append(fx, logEffect("coordinator pane exited"))
			c.coordinator = coordAbsent
			// Its replacement gets the in-flight work again (nothing was typed there).
			c.inbox.CoordinatorLost()
			c.clearTimers()
			c.promptFailures = 0
			c.blocked = optMs{}
			c.hinted = false
			c.flush(now, &fx)
		case ev.Kind == PaneEventStatus:
			c.onWorkerStatus(ev.Agent, ev.Status, now, &fx)
		default:
			c.workerGone(ev.Agent, now, &fx)
		}
	case InStop:
		if in.Agent == Coordinator {
			c.onStop(in.Message, now, &fx)
		} else {
			c.onWorkerStop(in.Agent, in.Message, now, &fx)
		}
	case InPermission:
		c.onPermission(in.RequestID, in.Agent, in.ToolName, in.ToolInput, now, &fx)
	case InPostTool:
		if in.Agent == Coordinator {
			c.onPostTool(in.ToolName, in.ToolInput, now, &fx)
		}
	case InVoice:
		switch in.Voice.Kind {
		case voice.SigOpened:
			c.voiceOpened(&fx)
		case voice.SigClosed:
			c.voiceOpen = false
			for _, n := range in.Voice.Undelivered {
				c.reroute(n, now, &fx)
			}
		case voice.SigUserSaid:
			c.onUserSaid(in.Voice.Text, now, &fx)
		}
	case InInjectBounced:
		c.voiceOpen = false
		c.reroute(in.Notice, now, &fx)
	case InDisable:
		c.setupDisabled = in.StatusLine
		c.disable(in.StatusLine, now, &fx)
	case InNotice:
		c.notice = in.StatusLine
		if c.status == "" {
			c.setStatus(c.notice, &fx)
		}
	case InPromptFailed:
		c.promptFailed(now, &fx)
	case InPromptTimedOut:
		fx = append(fx, logEffect("prompt timed out"))
		c.watchdog = &watchdog{at: now + PromptWatchdogMs, jobs: c.openInFlight()}
	case InTick:
		c.tick(now, &fx)
	case InRestart:
		running := c.coordinator != coordAbsent
		c.disabled = ""
		c.coordinator = coordAbsent
		c.blocked = optMs{}
		c.hinted = false
		c.promptFailures = 0
		c.clearTimers()
		// A setup problem is still there after a restart: its status stays.
		line := c.setupDisabled
		if line == "" {
			line = c.notice
		}
		c.setStatus(line, &fx)
		fx = append(fx, Effect{Kind: EfResetHost})
		if running {
			fx = append(fx, Effect{Kind: EfStopCoordinator})
		}
	case InShutdown:
		c.shutDown = true
		for _, p := range c.confirms.Drain() {
			fx = append(fx, decideEffect(p.ID, Deny("The app quit.")))
		}
		// With no coordinator there is nothing to stop (and stopping must not start a server).
		if !c.settings.KeepPanesOnQuit && c.coordinator != coordAbsent {
			fx = append(fx, Effect{Kind: EfStopCoordinator})
		}
		c.shutdownWorkers(&fx)
	case InMcp:
		c.onMcp(in.Agent, in.Call, now, &fx)
	case InAdmitWorker:
		c.admitWorker(in.Worker, in.Repo, in.Taken, now, &fx)
	case InWorkerUp:
		c.workerUp(in.Name, in.Worktree, in.Status, now, &fx)
	case InAdoptWorker:
		c.adoptWorker(in.Plan, in.Worktree, in.Status, now, &fx)
	case InWorkerFailed:
		c.workerFailed(in.Name, in.Reason, now, &fx)
	case InWorkerClosed:
		c.workerGone(in.Name, now, &fx)
	case InWorkerScreen:
		c.onWorkerScreen(in.Name, in.Text, now, &fx)
	case InWorkerPromptFailed:
		c.workerSendFailed(in.Name, in.Text, now, &fx)
	}
	return fx
}

func logEffect(text string) Effect { return Effect{Kind: EfLog, Text: text} }

func decideEffect(id RequestID, d Decision) Effect {
	return Effect{Kind: EfDecide, ID: id, Decision: d}
}

// openInFlight: the open jobs in the prompt in flight, without repeats.
func (c *AgentController) openInFlight() []JobID {
	var out []JobID
	for _, id := range unique(c.inbox.InFlightRefs()) {
		if c.jobs.IsOpen(id) {
			out = append(out, id)
		}
	}
	return out
}

func (c *AgentController) voiceOpened(fx *[]Effect) {
	c.voiceOpen = true
	c.router.ConversationOpened()
	var digest []Line
	for _, l := range c.outbox.Undelivered() {
		if l.Token == nil || c.confirms.IsPendingToken(*l.Token) {
			digest = append(digest, Line{Text: l.Text, Kind: l.Kind, Token: l.Token})
		}
	}
	notice, records := c.outbox.Replay(c.confirms.IsPendingToken)
	for _, line := range records {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: line})
	}
	if notice != nil {
		c.remember(*notice, digest)
		*fx = append(*fx, Effect{Kind: EfInject, Notice: *notice})
	}
}

// ---------- jobs ----------

// CoordinatorRestartNote goes to a kept coordinator for each job still open: a reply it sent
// while the app was down was lost.
const CoordinatorRestartNote = "The app restarted while you were working on this. If you already replied with its result, reply again with it; otherwise carry on."

func (c *AgentController) restore(jobs, outbox []string, keep bool, now Millis, fx *[]Effect) {
	table, restarted := RestoreJobTable(jobs, now, keep)
	c.jobs = table
	c.outbox = RestoreOutbox(outbox, now)
	// Queued only: the runtime ensures the coordinator once setup problems are known.
	for _, j := range c.jobs.Open() {
		c.inbox.Push(MessageForJob(j.ID, CoordinatorRestartNote))
	}
	for _, id := range restarted {
		task := ""
		if j, ok := c.jobs.Get(id); ok {
			task = short(j.Task)
		}
		c.outbox.Push(NewLine(LineInfo, "Agent: a job stopped when the app restarted: "+task), now)
	}
	*fx = append(*fx,
		Effect{Kind: EfRewrite, File: JobsFile, Lines: c.jobs.Lines()},
		Effect{Kind: EfRewrite, File: OutboxFile, Lines: c.outbox.Lines()},
	)
}

func (c *AgentController) delegate(id JobID, task, context string, now Millis, fx *[]Effect) {
	if c.isDisabled() {
		return
	}
	c.jobs.Create(id, task, now)
	c.saveJob(id, fx)
	text := "New request from the user: " + task
	if ctx := strings.TrimSpace(context); ctx != "" {
		text += "\n" + ctx
	}
	c.inbox.Push(MessageForJob(id, text))
	c.syncHold(fx)
	c.flush(now, fx)
}

func (c *AgentController) cancel(id JobID, now Millis, fx *[]Effect) {
	if _, err := c.jobs.Transition(id, JobCancelled, now); err != nil {
		return
	}
	c.saveJob(id, fx)
	c.narrator.Forget(id)
	c.denyPendingFor(id, "The user cancelled the job.", fx)
	c.stopJobWorkers(id, fx)
	interrupted := c.inbox.Carries(id)
	neverSent := c.inbox.DropQueued(id) && !interrupted
	if interrupted {
		*fx = append(*fx, Effect{Kind: EfInterrupt})
		// We end that turn ourselves (an interrupt fires no Stop): the Idle it brings counts.
		c.awaitingStop = false
		c.idleWaitAt = optMs{}
	}
	if !neverSent {
		c.inbox.Push(MessageForJob(id, fmt.Sprintf("The user cancelled %s. Stop work on it and confirm.", id)))
	}
	if interrupted {
		// The interrupt ends the whole turn: other jobs in that prompt go out again, behind the note.
		c.inbox.RequeueInFlightExcept(id)
	}
	c.syncHold(fx)
	c.flush(now, fx)
}

// flush sends what is queued if the coordinator can take it, starting it first if needed.
func (c *AgentController) flush(now Millis, fx *[]Effect) {
	if c.isDisabled() || !c.inbox.HasQueued() {
		return
	}
	switch c.coordinator {
	case coordAbsent:
		c.coordinator = coordStarting
		*fx = append(*fx, Effect{Kind: EfStartCoordinator})
	case coordStarting:
	case coordUp:
		prompt, ok := c.inbox.TakePrompt()
		if !ok {
			return
		}
		// Sent: the retry and settle timers are spent; its turn's Stop is awaited.
		c.retryAt = optMs{}
		c.settleAt = optMs{}
		c.idleWaitAt = optMs{}
		c.awaitingStop = true
		for _, r := range prompt.Refs {
			if j, ok := c.jobs.Get(r); ok && j.Status == JobDelegated {
				if _, err := c.jobs.Transition(r, JobWorking, now); err == nil {
					c.saveJob(r, fx)
				}
			}
		}
		*fx = append(*fx, Effect{Kind: EfPrompt, Text: prompt.Text})
	}
}

func (c *AgentController) onCoordinatorStatus(status PaneStatus, now Millis, fx *[]Effect) {
	c.coordStatus = status
	idle := status == PaneIdle || status == PaneDone
	switch {
	case idle && c.watchdog != nil:
		// A prompt timed out: whether it landed is the watchdog's call, not an Idle's.
	case idle && c.awaitingStop:
		// Herdr's Idle can beat our Stop hook for the same turn: sending now would put the next
		// prompt in flight before the Stop, which would then count it as answered. Wait for the
		// Stop, or for a while if the Stop is lost.
		if !c.idleWaitAt.set {
			c.idleWaitAt = someMs(now + IdleWithoutStopMs)
		}
	default:
		c.inbox.OnStatus(status)
	}
	if status == PaneWorking || status == PaneBlocked {
		// A sign of life: a timed-out prompt got through (an Idle alone is not one).
		c.watchdog = nil
		// Busy again (e.g. the user typed into it): no settle or retry into a busy TUI.
		c.settleAt = optMs{}
		c.retryAt = optMs{}
		// An Idle flicker mid-turn: the turn goes on, so its Stop is awaited again.
		c.idleWaitAt = optMs{}
	}
	if status == PaneBlocked {
		if !c.blocked.set {
			c.blocked = someMs(now)
		}
	} else {
		c.blocked = optMs{}
		c.hinted = false
		if c.status == "Agent: waiting in terminal" {
			c.setStatus(c.notice, fx)
		}
	}
}

func (c *AgentController) coordinatorFailed(err HostError, now Millis, fx *[]Effect) {
	c.coordinator = coordAbsent
	*fx = append(*fx, logEffect("coordinator start failed: "+errorTag(err)))
	switch err.Kind {
	case HostNotFound:
		c.disable("Agent: herdr not found", now, fx)
	case HostVersionMismatch:
		c.disable("Agent: herdr version mismatch (update herdr)", now, fx)
	case HostGaveUp:
		c.disable("Agent: stopped after repeated crashes", now, fx)
	default:
		c.setStatus("Agent: claude did not start", fx)
		c.inbox = NewInbox()
		for _, id := range c.OpenJobs() {
			c.failJob(id, "the agent could not start", now, fx)
		}
		c.syncHold(fx)
	}
}

// promptFailed: a prompt never reached the coordinator: what it carried goes back to the front
// of the queue, in order. Nothing was typed, so no Stop or status event will follow: while the
// coordinator is up it is tried again on a timer (PromptRetryMs, doubling). After
// MaxPromptFailures in a row, its jobs fail out loud instead.
func (c *AgentController) promptFailed(now Millis, fx *[]Effect) {
	*fx = append(*fx, logEffect("prompt failed"))
	c.promptFailures++
	carried := unique(c.inbox.InFlightRefs())
	c.inbox.RequeueInFlight()
	// Nothing was typed: no Stop will come for it.
	c.awaitingStop = false
	c.idleWaitAt = optMs{}
	if c.promptFailures >= MaxPromptFailures {
		c.promptFailures = 0
		c.retryAt = optMs{}
		for _, id := range carried {
			c.inbox.DropQueued(id)
			c.failJob(id, "the agent could not be reached", now, fx)
		}
		c.syncHold(fx)
	} else if c.coordinator == coordUp {
		c.retryAt = someMs(now + PromptRetryMs<<(c.promptFailures-1))
	}
}

// watchdogFired: no Stop or status event since a prompt timed out: its jobs fail out loud.
func (c *AgentController) watchdogFired(jobs []JobID, now Millis, fx *[]Effect) {
	*fx = append(*fx, logEffect("no sign of life after a timed-out prompt"))
	for _, id := range jobs {
		c.inbox.DropInFlight(id)
		c.inbox.DropQueued(id)
		if j, ok := c.jobs.Get(id); ok {
			c.failJobSaying(id, "the agent could not be reached", unreachable+": "+short(j.Task), now, fx)
		}
	}
	c.syncHold(fx)
}

// tickInbox runs the inbox's timers: the settle after a Stop, the retry of a failed prompt,
// and the watchdog of a timed-out one.
func (c *AgentController) tickInbox(now Millis, fx *[]Effect) {
	watched := c.watchdog != nil && now >= c.watchdog.at
	if watched {
		w := c.watchdog
		c.watchdog = nil
		c.watchdogFired(w.jobs, now, fx)
	}
	settled := c.settleAt.due(now)
	retry := c.retryAt.due(now)
	// Idle/Done came but the Stop never did (a lost hook): don't wait for it any longer.
	idleWaited := c.idleWaitAt.due(now)
	if settled {
		c.settleAt = optMs{}
	}
	if retry {
		c.retryAt = optMs{}
	}
	if idleWaited || watched {
		c.idleWaitAt = optMs{}
		c.awaitingStop = false
	}
	// After a watchdog the coordinator may well be idle: what else is queued gets its chance.
	if (watched || settled || retry || idleWaited) && c.coordinator == coordUp {
		c.inbox.MarkReady()
		c.flush(now, fx)
	}
}

func (c *AgentController) clearTimers() {
	c.retryAt = optMs{}
	c.settleAt = optMs{}
	c.watchdog = nil
	c.idleWaitAt = optMs{}
	c.awaitingStop = false
}

// disable turns delegation off (spec §9): every open job fails with "the agent stopped".
func (c *AgentController) disable(status string, now Millis, fx *[]Effect) {
	c.disabled = status
	c.setStatus(status, fx)
	c.inbox = NewInbox()
	for _, id := range c.OpenJobs() {
		c.failJob(id, "the agent stopped", now, fx)
	}
	c.syncHold(fx)
}

func (c *AgentController) failJob(id JobID, reason string, now Millis, fx *[]Effect) {
	task := ""
	if j, ok := c.jobs.Get(id); ok {
		task = short(j.Task)
	}
	c.failJobSaying(id, reason, "The agent stopped before finishing: "+task, now, fx)
}

// failJobSaying fails an open job and says line (with the error cue).
func (c *AgentController) failJobSaying(id JobID, reason, line string, now Millis, fx *[]Effect) {
	if _, err := c.jobs.Fail(id, reason, now); err != nil {
		return
	}
	c.denyPendingFor(id, "The job ended.", fx)
	c.stopJobWorkers(id, fx)
	c.narrator.Forget(id)
	c.saveJob(id, fx)
	c.deliver(NewLine(LineReply, line), now, fx)
	*fx = append(*fx, Effect{Kind: EfCue, Cue: CueError})
}

// ---------- replies ----------

func (c *AgentController) onStop(message string, now Millis, fx *[]Effect) {
	// The open jobs in the prompt this turn answered.
	answered := c.openInFlight()
	// The turn is over. The next prompt goes out on Idle/Done, or after a short settle if Herdr
	// missed the Idle (the TUI may still be finishing right at the Stop). An Idle that came
	// first doesn't send at once either: the settle covers it.
	c.inbox.FinishTurn()
	c.awaitingStop = false
	c.idleWaitAt = optMs{}
	c.promptFailures = 0
	c.watchdog = nil
	c.settleAt = someMs(now + StopSettleMs)
	blocks := ParseSpoken(message)
	refless := true
	for _, b := range blocks {
		if b.Reference != nil {
			refless = false
		}
	}
	if refless {
		c.onUnreferencedStop(message, blocks, answered, now, fx)
	} else {
		c.onReferencedStop(blocks, now, fx)
	}
	c.flush(now, fx)
}

// onUnreferencedStop handles a reply with no <spoken> block, or only ref-less ones; the last
// block's status (none: done) speaks for it. With exactly one job in the prompt it answered,
// the text is that job's reply with that status. Otherwise it is unsolicited, and if the status
// is done, every job in that prompt is closed with a short generic line (a question or progress
// leaves them open).
func (c *AgentController) onUnreferencedStop(message string, blocks []Spoken, answered []JobID, now Millis, fx *[]Effect) {
	tokens := c.pendingTokens()
	status := SpokenNone
	if len(blocks) > 0 {
		status = blocks[len(blocks)-1].Status
	}
	var texts []string
	if len(blocks) == 0 {
		text := maskPendingTokens(SpokenFallback(message), tokens)
		if text != "" {
			*fx = append(*fx, logEffect("spoken_fallback"))
		}
		texts = []string{text}
	} else {
		for _, b := range blocks {
			texts = append(texts, maskPendingTokens(b.Text, tokens))
		}
	}
	texts = slices.DeleteFunc(texts, func(t string) bool { return t == "" })
	if len(answered) == 1 {
		only := answered[0]
		text := strings.Join(texts, " ")
		if len(texts) == 0 {
			text = c.finishedLine(only)
		}
		c.onReply(only, status, text, now, fx)
		return
	}
	for _, text := range texts {
		c.deliver(NewLine(LineUnsolicited, text), now, fx)
	}
	// A question or progress can't say which job: they stay open. Failed is terminal: they fail
	// (leaving them open would keep Bit holding).
	switch status {
	case SpokenQuestion, SpokenWorking:
		return
	case SpokenFailed:
		for _, id := range answered {
			c.failJob(id, "the agent reported a failure", now, fx)
		}
		c.syncHold(fx)
		return
	}
	for _, id := range answered {
		// No status, and its workers are still at it: the job goes on.
		if status == SpokenNone && c.hasLiveWorkers(id) {
			continue
		}
		c.onReply(id, SpokenDone, c.finishedLine(id), now, fx)
	}
}

// finishedLine is what is said for a job the agent finished without a reply of its own.
func (c *AgentController) finishedLine(id JobID) string {
	task := ""
	if j, ok := c.jobs.Get(id); ok {
		task = short(j.Task)
	}
	return fmt.Sprintf("The agent finished: %s; details are in the terminal.", task)
}

func (c *AgentController) onReferencedStop(blocks []Spoken, now Millis, fx *[]Effect) {
	tokens := c.pendingTokens()
	for _, b := range blocks {
		text := maskPendingTokens(b.Text, tokens)
		// Matched by ref only, never to "the newest job" (#89).
		if b.Reference != nil {
			if id, ok := ParseJobID(*b.Reference); ok && c.jobs.IsOpen(id) {
				c.onReply(id, b.Status, text, now, fx)
				continue
			}
		}
		c.deliver(NewLine(LineUnsolicited, text), now, fx)
	}
}

func (c *AgentController) onReply(id JobID, status SpokenStatus, text string, now Millis, fx *[]Effect) {
	// A missing status means working while the job has live workers, otherwise done.
	if status == SpokenNone {
		status = SpokenDone
		if c.hasLiveWorkers(id) {
			status = SpokenWorking
		}
	}
	switch status {
	case SpokenWorking:
		_, _ = c.jobs.Transition(id, JobWorking, now)
		c.jobs.SetProgress(id, text, now)
		c.saveJob(id, fx)
		c.deliver(NewLine(LineReply, text), now, fx)
	case SpokenQuestion:
		_, _ = c.jobs.Transition(id, JobQuestion, now)
		c.saveJob(id, fx)
		c.deliver(NewLine(LineQuestion, text), now, fx)
	case SpokenDone:
		_, _ = c.jobs.Transition(id, JobDone, now)
		c.saveJob(id, fx)
		c.narrator.Forget(id)
		c.denyPendingFor(id, "The job ended.", fx)
		c.stopJobWorkers(id, fx)
		if c.deliver(NewLine(LineReply, text), now, fx) {
			*fx = append(*fx, Effect{Kind: EfCue, Cue: CueHappy})
		}
	case SpokenFailed:
		_, _ = c.jobs.Fail(id, text, now)
		c.saveJob(id, fx)
		c.narrator.Forget(id)
		c.denyPendingFor(id, "The job ended.", fx)
		c.stopJobWorkers(id, fx)
		c.deliver(NewLine(LineReply, text), now, fx)
		*fx = append(*fx, Effect{Kind: EfCue, Cue: CueError})
	}
	c.syncHold(fx)
}

// ---------- permissions ----------

func (c *AgentController) onPermission(id RequestID, agent, toolName string, toolInput any, now Millis, fx *[]Effect) {
	if c.shutDown || c.isDisabled() {
		why := "Delegation is off."
		if c.shutDown {
			why = "The app quit."
		}
		*fx = append(*fx, decideEffect(id, Deny(why)))
		return
	}
	// A worker's request belongs to its job and is spoken with its repo's name.
	var job JobID
	label := agent
	if agent != Coordinator {
		w := c.workers.Get(agent)
		if w == nil {
			*fx = append(*fx, decideEffect(id, Deny("This worker was stopped.")))
			return
		}
		// Its job ended (or it is being stopped): nothing to ask the user, no token.
		if w.StopRequested || !c.jobs.IsOpen(w.Plan.Job) {
			*fx = append(*fx, decideEffect(id, Deny("Your job has ended; stop working.")))
			return
		}
		job, label = w.Plan.Job, w.Plan.RepoName
	} else if refs := c.inbox.InFlightRefs(); len(refs) == 1 && c.jobs.IsOpen(refs[0]) {
		job = refs[0]
	}
	if !VoiceConfirmable(toolName, toolInput) {
		*fx = append(*fx, decideEffect(id, Deny(unreadableDenial)))
		c.deliver(NewLine(LineInfo, unreadableNotice), now, fx)
		return
	}
	token, ok := c.confirms.Request(id, agent, job, now)
	if !ok {
		*fx = append(*fx, decideEffect(id, Deny("Too many permission requests are waiting.")))
		return
	}
	if job != "" {
		if _, err := c.jobs.Transition(job, JobQuestion, now); err == nil {
			c.saveJob(job, fx)
		}
	}
	text := ConfirmQuestionLine(Describe(toolName, toolInput, label), token)
	c.deliver(Line{Text: text, Kind: LineQuestion, Token: &token}, now, fx)
}

func (c *AgentController) onUserSaid(text string, now Millis, fx *[]Effect) {
	for _, d := range c.confirms.OnTranscript(text) {
		decision := Deny("The user said no.")
		said := "Agent: denied"
		if d.Allow {
			decision, said = Allow(), "Agent: allowed"
		}
		*fx = append(*fx, decideEffect(d.Pending.ID, decision))
		c.resumeJob(d.Pending.Job, now, fx)
		c.deliver(NewLine(LineInfo, said), now, fx)
	}
}

// resumeJob moves a job in Question back to Working once nothing for it is pending.
func (c *AgentController) resumeJob(job JobID, now Millis, fx *[]Effect) {
	if job == "" {
		return
	}
	for _, p := range c.confirms.Pending() {
		if p.Job == job {
			return
		}
	}
	if j, ok := c.jobs.Get(job); ok && j.Status == JobQuestion {
		if _, err := c.jobs.Transition(job, JobWorking, now); err == nil {
			c.saveJob(job, fx)
		}
	}
}

// ---------- narration, ticks ----------

func (c *AgentController) onPostTool(toolName string, toolInput any, now Millis, fx *[]Effect) {
	phrase, ok := Phrase(toolName, toolInput)
	if !ok {
		return
	}
	phrase = maskPendingTokens(phrase, c.pendingTokens())
	refs := c.inbox.InFlightRefs()
	if len(refs) != 1 || !c.jobs.IsOpen(refs[0]) {
		return
	}
	job := refs[0]
	c.jobs.SetProgress(job, phrase, now)
	c.saveJob(job, fx)
	if c.settings.Narration && c.voiceOpen && c.narrator.Allow(job, now) {
		c.narrate(phrase, fx)
	}
}

// narrate injects a progress line (remembered with no lines: dropped if it bounces).
func (c *AgentController) narrate(phrase string, fx *[]Effect) {
	n := Notice{Text: "Agent progress: " + phrase}
	c.remember(n, nil)
	*fx = append(*fx, Effect{Kind: EfInject, Notice: n})
}

func (c *AgentController) tick(now Millis, fx *[]Effect) {
	c.tickInbox(now, fx)
	c.tickWorkers(now, fx)
	c.tickLiveness(now, fx)
	for _, p := range c.confirms.Expire(now) {
		*fx = append(*fx, decideEffect(p.ID, Deny("No answer from the user.")))
		c.resumeJob(p.Job, now, fx)
		c.deliver(NewLine(LineInfo, "Agent: denied after two minutes with no answer."), now, fx)
	}
	for _, d := range c.router.Tick(now) {
		c.applyDelivery(d, now, fx)
	}
	blockedLong := c.blocked.set && satSub(now, c.blocked.v) >= BlockedGraceMs
	if blockedLong && !c.hinted && !c.confirms.HasPendingFor(Coordinator) {
		c.hinted = true
		c.setStatus("Agent: waiting in terminal", fx)
		c.deliver(NewLine(LineInfo, BlockedHint), now, fx)
	}
}

// ---------- helpers ----------

// deliver routes a line; true if the user got it now (spoken or notified), not just outboxed.
func (c *AgentController) deliver(line Line, now Millis, fx *[]Effect) bool {
	reached := false
	for _, d := range c.router.Route(c.voiceOpen, line, now) {
		if d.Kind == DeliverInject {
			c.remember(d.Notice, []Line{line})
		}
		reached = reached || d.Kind == DeliverInject || d.Kind == DeliverNotify
		c.applyDelivery(d, now, fx)
	}
	return reached
}

func (c *AgentController) remember(n Notice, lines []Line) {
	c.injected = append(c.injected, injectedNotice{n.Text, lines})
	if len(c.injected) > 64 {
		c.injected = c.injected[1:]
	}
}

// reroute: a notice that did not reach the user: route the lines it carried again, closed,
// with their original kind and token. Narration is dropped; a permission question whose
// request is gone too. Another part of the app's notice (a timer's "Timer done: …") is not the
// agent's to deliver.
func (c *AgentController) reroute(n Notice, now Millis, fx *[]Effect) {
	var lines []Line
	known := false
	for k := len(c.injected) - 1; k >= 0; k-- {
		if c.injected[k].text == n.Text {
			lines, known = c.injected[k].lines, true
			c.injected = slices.Delete(c.injected, k, k+1)
			break
		}
	}
	if !known && strings.HasPrefix(n.Text, timerNotice) {
		return
	}
	if !known {
		lines = []Line{NewLine(LineInfo, stripPrefix(n.Text))}
	}
	for _, line := range lines {
		if line.Token != nil && !c.confirms.IsPendingToken(*line.Token) {
			continue
		}
		c.deliver(line, now, fx)
	}
}

func (c *AgentController) pendingTokens() []string {
	var out []string
	for _, p := range c.confirms.Pending() {
		out = append(out, p.Token)
	}
	return out
}

func (c *AgentController) denyPendingFor(job JobID, why string, fx *[]Effect) {
	for _, p := range slices.Clone(c.confirms.Pending()) {
		if p.Job == job {
			c.confirms.Remove(p.ID)
			*fx = append(*fx, decideEffect(p.ID, Deny(why)))
		}
	}
}

func (c *AgentController) answer(jobID JobID, text string, now Millis, fx *[]Effect) {
	waiting := func(id JobID) bool {
		j, ok := c.jobs.Get(id)
		return ok && j.Status == JobQuestion
	}
	var id JobID
	var err string
	switch {
	case jobID != "" && c.jobs.IsOpen(jobID) && waiting(jobID):
		id = jobID
	case jobID != "" && c.jobs.IsOpen(jobID):
		err = fmt.Sprintf("%s is not waiting for an answer.", jobID)
	case jobID != "":
		err = fmt.Sprintf("No open job %s.", jobID)
	default:
		var asking []JobID
		for _, o := range c.OpenJobs() {
			if waiting(o) {
				asking = append(asking, o)
			}
		}
		switch len(asking) {
		case 0:
			err = "No job is waiting for an answer."
		case 1:
			id = asking[0]
		default:
			err = fmt.Sprintf("Several jobs are waiting for an answer (%s). Which one?", joinIDs(asking))
		}
	}
	if err == "" && slices.ContainsFunc(c.confirms.Pending(), func(p Pending) bool { return p.Job == id }) {
		err = "That job is waiting for a confirm word."
	}
	if err != "" {
		*fx = append(*fx, Effect{Kind: EfAnswerResult, Err: err})
		return
	}
	if _, e := c.jobs.Transition(id, JobWorking, now); e == nil {
		c.saveJob(id, fx)
	}
	c.inbox.Push(MessageForJob(id, "The user answered: "+strings.TrimSpace(text)))
	*fx = append(*fx, Effect{Kind: EfAnswerResult, Job: id})
	c.flush(now, fx)
}

func (c *AgentController) applyDelivery(d RouteDelivery, now Millis, fx *[]Effect) {
	switch d.Kind {
	case DeliverInject:
		*fx = append(*fx, Effect{Kind: EfInject, Notice: d.Notice})
	case DeliverChime:
		*fx = append(*fx, Effect{Kind: EfChime})
	case DeliverNotify:
		*fx = append(*fx, Effect{Kind: EfNotify, Text: d.Text})
	case DeliverOutbox:
		record := c.outbox.Push(d.Line, now)
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: record})
	}
}

func (c *AgentController) saveJob(id JobID, fx *[]Effect) {
	if line, ok := c.jobs.Line(id); ok {
		*fx = append(*fx, Effect{Kind: EfAppend, File: JobsFile, Line: line})
	}
}

func (c *AgentController) syncHold(fx *[]Effect) {
	if open := c.jobs.AnyOpen(); open != c.held {
		c.held = open
		*fx = append(*fx, Effect{Kind: EfHoldOpen, Hold: open})
	}
}

// setStatus emits the tray status line ("" clears it) when it changed.
func (c *AgentController) setStatus(line string, fx *[]Effect) {
	if c.status != line {
		c.status = line
		*fx = append(*fx, Effect{Kind: EfStatus, Text: line})
	}
}

// maskPendingTokens: agent text must not be able to imitate a confirm prompt: any word that
// contains a currently pending confirm word (after folding case, look-alike letters and
// invisible characters) is removed.
func maskPendingTokens(text string, tokens []string) string {
	if len(tokens) == 0 {
		return text
	}
	var out, word, folded strings.Builder
	flush := func() {
		if word.Len() > 0 {
			f := folded.String()
			if slices.ContainsFunc(tokens, func(t string) bool { return strings.Contains(f, t) }) {
				out.WriteString("(word removed)")
			} else {
				out.WriteString(word.String())
			}
		}
		word.Reset()
		folded.Reset()
	}
	for _, r := range text {
		switch {
		case isInvisible(r):
			word.WriteRune(r)
		case isAlphanumeric(r):
			word.WriteRune(r)
			for _, l := range strings.ToLower(string(r)) {
				folded.WriteRune(foldConfusable(l))
			}
		default:
			flush()
			out.WriteRune(r)
		}
	}
	flush()
	return out.String()
}

// isAlphanumeric is Rust's char::is_alphanumeric (Alphabetic or Numeric).
func isAlphanumeric(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Other_Alphabetic, r)
}

// isInvisible: zero-width and bidi-control characters: they never split a word.
func isInvisible(r rune) bool {
	switch {
	case r == 0x00ad, r == 0x180e, r == 0xfeff:
		return true
	case r >= 0x200b && r <= 0x200f, r >= 0x202a && r <= 0x202e, r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// foldConfusable maps look-alike Cyrillic, Greek and fullwidth letters to ASCII (input is
// already lowercase).
func foldConfusable(r rune) rune {
	if r >= 0xff41 && r <= 0xff5a {
		return 'a' + (r - 0xff41)
	}
	switch r {
	case 'а', 'α':
		return 'a'
	case 'в':
		return 'b'
	case 'с':
		return 'c'
	case 'ԁ':
		return 'd'
	case 'е', 'ε':
		return 'e'
	case 'ɡ':
		return 'g'
	case 'н', 'һ':
		return 'h'
	case 'і', 'ι':
		return 'i'
	case 'ј':
		return 'j'
	case 'к', 'κ':
		return 'k'
	case 'м':
		return 'm'
	case 'о', 'ο':
		return 'o'
	case 'р', 'ρ':
		return 'p'
	case 'ѕ':
		return 's'
	case 'т', 'τ':
		return 't'
	case 'υ':
		return 'u'
	case 'ν':
		return 'v'
	case 'х', 'χ':
		return 'x'
	case 'у':
		return 'y'
	}
	return r
}

// stripPrefix is a notice without the voice model's lead-in, for a notification.
func stripPrefix(text string) string {
	for _, p := range []string{"Agent reply: ", "Agent question: ", "Agent says: ", "Agent progress: "} {
		if rest, ok := strings.CutPrefix(text, p); ok {
			return rest
		}
	}
	return text
}

// errorTag is a host error as a log tag: the variant only, never its text.
func errorTag(e HostError) string {
	switch e.Kind {
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

// unique is ids without repeats, first occurrence kept.
func unique(ids []JobID) []JobID {
	var out []JobID
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// short is a task as spoken in a status line: at most 60 characters.
func short(task string) string {
	t := Clean(task)
	if utf8.RuneCountInString(t) > 60 {
		return string([]rune(t)[:59]) + "…"
	}
	return t
}
