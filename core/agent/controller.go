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

const BlockedHint = "The coordinator is waiting at a prompt in its terminal; I'm bringing it up for you."

// unreadableDenial is what the agent is told when a request cannot be read aloud plainly
// enough to approve by voice.
const unreadableDenial = "This can't be approved by voice. Ask the user to run it themselves, or to change it so it can be read aloud plainly."

const unreadableNotice = "The coordinator asked to run something I can't read out safely, so I said no. Open the terminal to see it."

const (
	// MaxPromptFailures: this many prompts failing in a row (no finished turn between) fail the
	// jobs they carried.
	MaxPromptFailures uint32 = 3
	// PromptRetryMs: a failed prompt is retried this long after the first failure, doubling.
	PromptRetryMs Millis = 2_000
	// CoordinatorRestartMs: a coordinator that exits is started again this long after (#118).
	// Repeated crashes are bounded by the host (MaxRecreations in RecreationWindowMs).
	CoordinatorRestartMs Millis = 3_000
	// CoordinatorRetryMs: a start that failed is tried again this long after, doubling up to
	// CoordinatorRetryMaxMs (#118).
	CoordinatorRetryMs    Millis = 5_000
	CoordinatorRetryMaxMs Millis = 5 * 60_000
	// StopSettleMs: after a Stop, the next prompt goes out on Idle/Done or after this settle,
	// whichever is first.
	StopSettleMs Millis = 1_500
	// PromptWatchdogMs: a prompt whose host call timed out (it may have been typed) with no Stop
	// or status event within this long fails its jobs.
	PromptWatchdogMs Millis = 60_000
	// IdleWithoutStopMs: an Idle/Done with no Stop for the prompt in flight makes the inbox
	// ready after this long (a lost Stop hook must not wedge it).
	IdleWithoutStopMs Millis = 10_000
	// PromptLandMs: a prompt the coordinator hasn't reported taking (its UserPromptSubmit hook)
	// this long after it was typed, with no other turn running, never landed: it is sent again
	// (#128). Only once the coordinator has reported a prompt: one without the hook is trusted.
	PromptLandMs Millis = 15_000
)

const unreachable = "I couldn't reach the coordinator"

// timerNotice is how the voice loop's timer notices start (the timer handles its own
// undelivered ones).
const timerNotice = "Timer done:"

// Settings are the controller's `agent.*` settings.
type Settings struct {
	NotifyPerMinute     int
	PermissionTimeoutMs Millis
	Narration           bool
	// UpdateQuietMs (`notifications.debounceSecs`): how long a job must be quiet before its
	// updates are said (debounce.go).
	UpdateQuietMs Millis
	// KeepPanesOnQuit (`agent.keepPanesOnQuit`): leave the coordinator and workers running when
	// the app quits.
	KeepPanesOnQuit bool
	// Workers is `agent.workers`.
	Workers WorkerPolicy
	// SpeakWorkerReports (`agent.workers.speakResults`, a feature flag, off by default): a
	// worker's report_result and report_blocked are also said to the user at once.
	SpeakWorkerReports bool
	// SessionTurns (`notifications.sessionTurns`, off by default): a session you started with
	// pix that finishes a turn is announced (#114). SessionTurnsMinMs: a turn of yours shorter
	// than this stays quiet; one Pixie started is always announced.
	SessionTurns      bool
	SessionTurnsMinMs Millis
	// OpenConversation (`agent.openConversationForUpdates`, a feature flag, off by default): a job's done
	// or failed line, an agent question or a spoken worker report that comes while no
	// conversation is open makes Pixie open one and say it (opener.go).
	OpenConversation bool
	// AlwaysForceSend (`agent.alwaysForceSend`, off by default, #205): every message to a
	// session you started with pix is sent as message_session's force sends it.
	AlwaysForceSend bool
	// CoordinatorKind (`agent.coordinatorKind`, #87): the coordinator's agent, named in the
	// tray when it can't start.
	CoordinatorKind AgentKind
	// SpokenPaths: folders a spoken approval names short (#97).
	SpokenPaths SpokenPaths
}

// DefaultSettings: 4 notifications a minute, 2-minute permission timeout, narration on.
func DefaultSettings() Settings {
	return Settings{NotifyPerMinute: 4, PermissionTimeoutMs: 120_000, Narration: true, UpdateQuietMs: UpdateQuietMs, Workers: DefaultWorkerPolicy(), SessionTurnsMinMs: 30_000}
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
	InRestored            InputKind = iota // Jobs, Outbox, Keep: the stored jobs.jsonl and outbox.jsonl lines, at start
	InDelegate                             // Job, Task, Context ("" = none)
	InCancel                               // Job
	InAnswer                               // Job ("" = the only job waiting on an answer), Text; answered with EfAnswerResult
	InEnsureCoordinator                    // start the coordinator now if it isn't running
	InCoordinatorUp                        // Status: SessionHost ensure_coordinator succeeded
	InCoordinatorFailed                    // HostErr
	InPane                                 // Pane
	InStop                                 // Agent, Message
	InPermission                           // RequestID, Agent, ToolName, ToolInput
	InPostTool                             // Agent, ToolName, ToolInput
	InVoice                                // Voice
	InInjectBounced                        // Notice: the executor found the conversation closed when it tried to inject it
	InDisable                              // StatusLine: delegation off for a setup reason (sticky: Restart keeps it)
	InNotice                               // StatusLine: a setup note that disables nothing
	InPromptFailed                         // EfPrompt could not be delivered
	InPromptTimedOut                       // the EfPrompt host call timed out: it may or may not have been typed
	InTick                                 // about once a second
	InRestart                              // tray ▸ Restart coordinator
	InShutdown                             // app quit
	InMcp                                  // Agent, Call: a pixie MCP tool call other than start_worker; answered with EfToolResult
	InAdmitWorker                          // Worker, Repo (canonical), Taken: start_worker; answered with EfAdmitted
	InWorkerUp                             // Name, Worktree, Status: an admitted worker's pane is up
	InWorkerFailed                         // Name, Reason: an admitted worker never started
	InWorkerClosed                         // Name: EfStopWorker finished (the pane is closed or was already gone)
	InWorkerScreen                         // Name, Text: EfReadWorker's screen
	InWorkerPromptFailed                   // Name, Text: EfPromptWorker could not be typed
	InAdoptWorker                          // Plan, Worktree, Status: a worker kept over the quit, its pane still there; answered with EfAdmitted
	InOpenRefused                          // the voice loop was busy and did not start the conversation EfOpenConversation asked for
	InUpdatesSeen                          // IDs: the updates bubble showed these lines long enough to count as heard
	InPresence                             // Away: the user is away (screen locked, displays asleep or idle) or back
	InMode                                 // Agent, Mode: the permission_mode a Claude agent's hook reported
	InDecide                               // Token, Allow: a tap on Allow or Deny in the updates bubble
	InChoose                               // Token, Option: a tap on a session menu's option in the updates bubble (#105)
	InSessionAnswerFailed                  // Name, Text: EfAnswerSession could not be typed (#105)
	InSubmit                               // Agent, Message: a Claude agent took a prompt (its UserPromptSubmit hook); Message is the prompt
	InSessionSent                          // Name, Text, Message: the coordinator's message_session reached your session Name, described Text (#177); Message: why it waits ("" = typed, #183)
	InTextMode                             // Away: text mode on (#221): no conversation is opened for updates
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
	Mode       string
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
	// Typed (InDelegate): the request was typed in the text box (#221).
	Typed bool
	Plan  WorkerPlan
	// Memories (InRestored): memories.jsonl's lines.
	Memories []string
	// IDs (InUpdatesSeen): outbox line ids. Clicked: the user dismissed them (a click on the line
	// or its notification, #199): a job's question goes too.
	IDs     []uint64
	Clicked bool
	// Away (InPresence).
	Away bool
	// Token and Allow (InDecide): the confirm word tapped on, and the answer.
	Token string
	Allow bool
	// Option (InChoose): the menu option tapped on, from 1.
	Option int
}

// RestoredInput: keep says the coordinator was kept running over the quit: its open jobs stay
// open and it is asked for their results again.
func RestoredInput(jobs, outbox []string, keep bool) Input {
	return Input{Kind: InRestored, Jobs: jobs, Outbox: outbox, Keep: keep}
}
func DelegateInput(id JobID, task, context string) Input {
	return Input{Kind: InDelegate, Job: id, Task: task, Context: context}
}

// TypedDelegateInput is DelegateInput for a message typed in the text box (#221): the job's
// lines go to the box (EfChat).
func TypedDelegateInput(id JobID, task, context string) Input {
	return Input{Kind: InDelegate, Job: id, Task: task, Context: context, Typed: true}
}

// TextModeInput: the user switched to text (on) or back to voice (#221).
func TextModeInput(on bool) Input { return Input{Kind: InTextMode, Away: on} }
func CancelInput(id JobID) Input  { return Input{Kind: InCancel, Job: id} }

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
func OpenRefusedInput() Input             { return Input{Kind: InOpenRefused} }

// DecideInput is a tap on Allow or Deny for the permission question with this confirm word.
func DecideInput(token string, allow bool) Input {
	return Input{Kind: InDecide, Token: token, Allow: allow}
}

// ChooseInput is a tap on option (from 1) of the session menu with this token (#105).
func ChooseInput(token string, option int) Input {
	return Input{Kind: InChoose, Token: token, Option: option}
}

// SessionAnswerFailedInput: typing an answer into session name failed (why).
func SessionAnswerFailedInput(name, why string) Input {
	return Input{Kind: InSessionAnswerFailed, Name: name, Text: why}
}
func UpdatesSeenInput(ids []uint64) Input { return Input{Kind: InUpdatesSeen, IDs: ids} }

// UpdatesDismissedInput: the user clicked these lines away (#199).
func UpdatesDismissedInput(ids []uint64) Input {
	return Input{Kind: InUpdatesSeen, IDs: ids, Clicked: true}
}
func PresenceInput(away bool) Input      { return Input{Kind: InPresence, Away: away} }
func ModeInput(agent, mode string) Input { return Input{Kind: InMode, Agent: agent, Mode: mode} }
func SubmitInput(agent, prompt string) Input {
	return Input{Kind: InSubmit, Agent: agent, Message: prompt}
}
func SessionSentInput(name, description string) Input {
	return Input{Kind: InSessionSent, Name: name, Text: description}
}

// SessionWaitingInput: the message reached pix but waits (#183), for reason "working",
// "blocked" or "typing".
func SessionWaitingInput(name, description, reason string) Input {
	return Input{Kind: InSessionSent, Name: name, Text: description, Message: reason}
}
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
	EfNotify           // Text
	EfCue              // Cue
	EfDecide           // ID, Decision
	EfAppend           // File, Line
	EfRewrite          // File, Lines
	EfStatus           // Text: the tray's agent status line; "" clears it (Rust's Status(None))
	EfStopCoordinator  // Escape, then close the coordinator pane
	EfResetHost        // SessionHost reset: forget the crash history
	EfLog              // Text
	EfAnswerResult     // Job, or Err: the result of InAnswer (on Err no state changed)
	EfAdmitted         // Plan, or Err: the answer to InAdmitWorker
	EfToolResult       // Value, or Err: the answer to InMcp (the error is what the agent sees)
	EfPromptWorker     // Name, Text: type this into a worker
	EfSubmitWorker     // Name: press Enter in the worker's pane (a prompt sits in its input, not submitted)
	EfStopWorker       // Name: Escape, then close the pane after a grace (answered with InWorkerClosed)
	EfReadWorker       // Name: read the worker's screen (answered with InWorkerScreen)
	EfForgetWorker     // Name: the worker is gone: revoke its token and delete its launch files
	EfLabel            // Tab: relabel the coordinator's tab (its current request, or idle)
	EfOpenConversation // start a conversation if the voice loop is at rest (else answered with InOpenRefused)
	EfUpdates          // Value: []UpdateItem: the updates bubble's list (sent when it changes)
	EfManual           // Agent, Manual: a Claude agent is (or no longer is) in Manual mode
	EfShowPane         // Job, Name: bring up this agent's pane (Name "": the coordinator) — it waits at a prompt in its terminal (#110)
	EfAnswerSession    // Name, Text, Answer: type your answer to session Name's prompt Text (#105); a failure comes back as InSessionAnswerFailed
	EfTellSession      // Name, Text: type your answer to the question session Name's turn ended on (#161); a failure comes back as InSessionAnswerFailed
	EfChat             // Chat: a spoken line for a job typed in the text box (#221)
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
	Tab      TabLabel
	Agent    string
	Manual   bool
	// Answer (EfAnswerSession): "yes", "no" or a menu option's number.
	Answer string
	// Update (EfNotify): the bubble line the notification is about, nil when none (#199): a
	// click on the notification does what a click on the line does.
	Update *UpdateItem
	// Chat (EfChat): the line for the text box.
	Chat ChatLine
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
	// typed: jobs typed in the text box, whose lines go to the box (#221); lastTyped the newest.
	// Memory only: after a restart their lines take the usual route. textMode: no conversation
	// is opened for updates.
	typed     map[JobID]bool
	lastTyped JobID
	textMode  bool
	// turnStopped: the turn that just ended was only about typed jobs the user stopped, so what
	// it says (the coordinator confirming the stop) is dropped.
	turnStopped bool

	// asked: the question Pixie relayed last, which an answer without a job id answers (#161).
	asked asker

	// modes: agent -> in Manual mode (Claude agents only); manualSaid: Pixie told the user once (#97).
	modes      map[string]bool
	manualSaid bool

	settings    Settings
	jobs        *JobTable
	inbox       *Inbox
	router      *Router
	outbox      *Outbox
	memories    *MemoryLog
	confirms    *Confirmations
	narrator    *Narrator
	updates     *Updates
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
	// submits: the coordinator reports each prompt it takes. Only then is a prompt's landing
	// checked, and a Working with no submitted turn behind it told apart (#127, #128).
	submits bool
	// turnOpen: a turn the coordinator took is running (its submit seen, its Stop not yet).
	turnOpen bool
	// landing: the prompt in flight (landingRefs, landingText) is not yet seen taken; landBy is
	// when it counts as lost (unset while another turn runs: it may be queued behind it).
	landing     bool
	landingRefs []JobID
	landingText string
	landBy      optMs
	held        bool
	status      string
	// injected: recently injected notices and the lines each one carried (none: narration,
	// which is dropped rather than notified if it bounces).
	injected []injectedNotice
	shutDown bool
	// handedOff: jobs whose request the coordinator passed to one of your sessions, which Pixie
	// has already told you (#177); true while it has done nothing else since.
	handedOff map[JobID]bool
	// sessionSeq numbers session prompts' request ids (#105).
	sessionSeq uint64
	// restartAt: when the coordinator is started again after an exit or a failed start (#118);
	// startFailures: failed starts in a row, for the backoff.
	restartAt     optMs
	startFailures uint32
	workers       *Workers
	// coordStatus: the coordinator pane's latest status.
	coordStatus PaneStatus
	// watch: liveness of the open working jobs (liveness.go).
	watch map[JobID]*liveness
	// coordJob: the job the coordinator was last prompted about (its tab shows it while open).
	coordJob JobID
	// coordTab: the coordinator tab's label as last emitted ("" = not since it came up).
	coordTab string
	// opener: opening a conversation for an update (opener.go).
	opener *opener
	// bubbleSent, bubbleLast: the updates bubble's list as last sent (bubble.go).
	bubbleSent bool
	bubbleLast []UpdateItem
	// away: the user is away (InPresence): Pixie opens no conversation and plays no chime.
	away bool
}

func NewAgentController(settings Settings) *AgentController {
	return &AgentController{
		router:    NewRouter(settings.NotifyPerMinute),
		confirms:  NewConfirmations(settings.PermissionTimeoutMs),
		narrator:  NewNarrator(NarrateIntervalMs),
		updates:   NewUpdatesWith(settings.UpdateQuietMs),
		settings:  settings,
		jobs:      NewJobTable(),
		inbox:     NewInbox(),
		outbox:    NewOutbox(),
		memories:  NewMemoryLog(),
		workers:   NewWorkers(),
		handedOff: map[JobID]bool{},
		typed:     map[JobID]bool{},
		opener:    newOpener(settings.NotifyPerMinute),
		modes:     map[string]bool{},
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

// RecentJobs are the open jobs and those updated since `since`, oldest first (Pixie's memory).
func (c *AgentController) RecentJobs(since Millis) []Job { return c.jobs.Recent(since) }

// RecentMemories are the newest n memories, oldest first.
func (c *AgentController) RecentMemories(n int) []Memory { return c.memories.Recent(n) }

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
		c.memories = RestoreMemoryLog(in.Memories)
		fx = append(fx, Effect{Kind: EfRewrite, File: MemoriesFile, Lines: c.memories.Lines()})
	case InDelegate:
		c.delegate(in.Job, in.Task, in.Context, now, &fx)
		if in.Typed && !c.isDisabled() {
			c.typed[in.Job] = true
			c.lastTyped = in.Job
		}
	case InTextMode:
		c.textMode = in.Away
	case InCancel:
		c.cancel(in.Job, now, &fx)
	case InAnswer:
		c.answer(in.Job, in.Text, now, &fx)
	case InEnsureCoordinator:
		c.ensureCoordinator(&fx)
	case InCoordinatorUp:
		c.coordinator = coordUp
		c.restartAt = optMs{}
		c.startFailures = 0
		c.onCoordinatorStatus(in.Status, now, &fx)
		if c.status == "Coordinator: claude did not start" {
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
			c.clearMode(Coordinator, &fx)
			// Its replacement gets the in-flight work again (nothing was typed there).
			c.inbox.CoordinatorLost()
			c.clearTimers()
			c.promptFailures = 0
			c.blocked = optMs{}
			c.hinted = false
			c.flush(now, &fx)
			// #118: kept running while the app is (flush already restarted it if work was queued).
			if c.coordinator == coordAbsent && !c.shutDown && !c.isDisabled() {
				c.restartAt = someMs(now + CoordinatorRestartMs)
			}
		case ev.Kind == PaneEventQuestion && ev.Question != nil:
			c.onSessionQuestion(ev.Agent, *ev.Question, now, &fx)
		case ev.Kind == PaneEventQuestionGone && ev.Question != nil:
			c.confirms.TakeSession(ev.Agent, ev.Question.ID)
		case ev.Kind == PaneEventTurn:
			if ev.Turn != nil {
				c.onSessionTurn(ev.Agent, *ev.Turn, now, &fx)
			}
		case ev.Kind == PaneEventOutdated:
			if ev.Outdated != nil && !c.shutDown {
				// #191: told like a session update, whatever the session turns setting.
				// Each app run tells it once: not again when a run in the last OutdatedRetellMs
				// already did (a rebuild restarts the app, #194).
				o := *ev.Outdated
				label := maskPendingTokens(o.Label, c.pendingTokens())
				text := OutdatedLine(label, o.Tool)
				if !c.outbox.ToldSince(text, satSub(now, OutdatedRetellMs)) {
					line := Line{Kind: LineSession, App: o.App, Session: ev.Agent, Text: text,
						Title: label, Brief: OutdatedBrief(o.Tool)}
					before := c.outbox.Len()
					c.deliverUpdate(line, now, &fx)
					// Said in a conversation or held by the cap, it never reached the outbox: kept
					// there as heard, so the next run knows it was told.
					if c.outbox.Len() == before {
						fx = append(fx, Effect{Kind: EfAppend, File: OutboxFile, Line: c.outbox.PushHeard(line, now)})
					}
				}
			}
		case ev.Kind == PaneEventQuestion || ev.Kind == PaneEventQuestionGone:
		case ev.Kind == PaneEventStatus:
			c.onWorkerStatus(ev.Agent, ev.Status, now, &fx)
		default:
			c.confirms.TakeSession(ev.Agent, "") // a session that closed asks nothing any more
			c.workerGone(ev.Agent, now, &fx)
		}
	case InMode:
		c.onMode(in.Agent, in.Mode, now, &fx)
	case InSubmit:
		if in.Agent == Coordinator {
			c.onSubmit(in.Message, now, &fx)
		}
	case InSessionSent:
		c.onSessionSent(in.Text, in.Message, now, &fx)
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
			c.openerSaw(voice.SigOpened)
			c.voiceOpened(&fx)
			// #118: a request made in this conversation never waits on a cold start.
			c.ensureCoordinator(&fx)
		case voice.SigClosed:
			c.openerSaw(voice.SigClosed)
			c.conversationClosed(now, &fx)
			for _, n := range in.Voice.Undelivered {
				c.reroute(n, now, &fx)
			}
		case voice.SigUserSaid:
			c.onUserSaid(in.Voice.Text, now, &fx)
		}
	case InInjectBounced:
		c.conversationClosed(now, &fx)
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
		c.clearMode(Coordinator, &fx)
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
	case InOpenRefused:
		c.openRefused(now, &fx)
	case InUpdatesSeen:
		c.updatesSeen(in.IDs, in.Clicked, &fx)
	case InDecide:
		// A token already decided (by voice or an earlier tap) or expired settles nothing.
		if d, ok := c.confirms.DecideToken(in.Token, in.Allow); ok {
			c.applyDecided(d, now, &fx)
		}
	case InChoose:
		c.onChoose(in.Token, in.Option, now, &fx)
	case InSessionAnswerFailed:
		c.deliver(NewLine(LineInfo, fmt.Sprintf("Couldn't answer in %s: %s Answer it in its terminal.", in.Name, in.Text)), now, &fx)
	case InPresence:
		c.setAway(in.Away, now, &fx)
	}
	c.dropDecidedQuestions(&fx)
	// A question answered or decided puts its job back to work: the hold follows (#119).
	c.syncHold(&fx)
	c.syncCoordTab(&fx)
	c.syncUpdates(&fx)
	return fx
}

// syncCoordTab relabels the coordinator's tab when its current request changed: the job it was
// last prompted about while that is open, else the newest open job, else idle.
func (c *AgentController) syncCoordTab(fx *[]Effect) {
	if c.coordinator != coordUp || c.shutDown {
		c.coordTab = ""
		return
	}
	var job *Job
	if j, ok := c.jobs.Get(c.coordJob); ok && j.Status.IsOpen() {
		job = &j
	} else if open := c.jobs.Open(); len(open) > 0 {
		job = &open[len(open)-1]
	}
	tab := CoordinatorTab(job)
	key := tab.Label + "\x00" + tab.Meta["job"]
	if key != c.coordTab {
		c.coordTab = key
		*fx = append(*fx, Effect{Kind: EfLabel, Tab: tab})
	}
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
			digest = append(digest, Line{Text: l.Text, Kind: l.Kind, Token: l.Token, Job: l.Job, Worker: l.Worker})
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
		c.outbox.Push(NewLine(LineInfo, "Coordinator: a job stopped when the app restarted: "+task), now)
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
	c.forgetUpdates(id)
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
		if c.submits {
			c.landing, c.landingRefs, c.landingText = true, prompt.Refs, prompt.Text
			c.landBy = optMs{}
			if !c.turnOpen {
				c.landBy = someMs(now + PromptLandMs)
			}
		}
		for _, r := range prompt.Refs {
			if c.jobs.IsOpen(r) {
				c.coordJob = r
			}
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
	if c.submits && status == PaneWorking && !c.turnOpen {
		// No turn is running: Herdr reads a spinner the TUI keeps while a background agent
		// runs, at an idle prompt. It neither holds the inbox nor counts as busy (#127).
		return
	}
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
		if c.status == "Coordinator: waiting in terminal" {
			c.setStatus(c.notice, fx)
		}
	}
}

func (c *AgentController) coordinatorFailed(err HostError, now Millis, fx *[]Effect) {
	c.coordinator = coordAbsent
	c.clearMode(Coordinator, fx)
	*fx = append(*fx, logEffect("coordinator start failed: "+errorTag(err)))
	switch err.Kind {
	case HostNotFound:
		c.disable("Coordinator: "+err.HostName()+" not found", now, fx)
	case HostVersionMismatch:
		host := err.HostName()
		if err.Detail == "" {
			c.disable("Coordinator: "+host+" version mismatch (update "+host+")", now, fx)
		} else {
			// What the user has and which versions are supported (e.g. herdr.SupportedVersions).
			c.disable("Coordinator: "+host+" version mismatch: "+err.Detail, now, fx)
		}
	case HostGaveUp:
		c.disable("Coordinator: stopped after repeated crashes", now, fx)
	default:
		if err.Kind == HostRefused {
			// The start screen's fixed reason (e.g. Codex wants its hooks reviewed).
			c.setStatus("Coordinator: "+err.Detail, fx)
		} else {
			c.setStatus("Coordinator: "+c.settings.CoordinatorKind.String()+" did not start", fx)
		}
		c.inbox = NewInbox()
		for _, id := range c.OpenJobs() {
			c.failJob(id, "the coordinator could not start", now, fx)
		}
		c.syncHold(fx)
		// #118: tried again, waiting longer each time it fails.
		if !c.shutDown {
			wait := CoordinatorRetryMaxMs
			if c.startFailures < 16 {
				wait = min(CoordinatorRetryMs<<c.startFailures, CoordinatorRetryMaxMs)
			}
			c.startFailures++
			c.restartAt = someMs(now + wait)
		}
	}
}

// ensureCoordinator starts the coordinator if it isn't running (or starting) and may run.
func (c *AgentController) ensureCoordinator(fx *[]Effect) {
	if c.shutDown || c.isDisabled() || c.coordinator != coordAbsent {
		return
	}
	c.restartAt = optMs{}
	c.coordinator = coordStarting
	*fx = append(*fx, Effect{Kind: EfStartCoordinator})
}

// onSubmit: the coordinator took a prompt and started a turn on it: the one in flight if it
// carries its refs (or, with none, its text), else another (the user's, or a background agent's
// notification). Either way it is busy until that turn's Stop.
func (c *AgentController) onSubmit(prompt string, now Millis, fx *[]Effect) {
	if !c.submits {
		*fx = append(*fx, logEffect("the coordinator reports the prompts it takes: checking that each one lands"))
	}
	c.submits = true
	c.turnOpen = true
	c.coordStatus = PaneWorking
	c.inbox.OnStatus(PaneWorking)
	c.settleAt = optMs{}
	c.retryAt = optMs{}
	c.idleWaitAt = optMs{}
	if !c.landing {
		return
	}
	if c.isLanding(prompt) {
		c.stopLanding()
		c.watchdog = nil
	} else {
		c.landBy = optMs{}
	}
}

// isLanding: prompt is the one in flight.
func (c *AgentController) isLanding(prompt string) bool {
	if len(c.landingRefs) == 0 {
		return strings.Contains(prompt, strings.TrimSpace(c.landingText))
	}
	for _, r := range c.landingRefs {
		if !strings.Contains(prompt, "[ref "+string(r)+"]") {
			return false
		}
	}
	return true
}

func (c *AgentController) stopLanding() {
	c.landing, c.landingRefs, c.landingText = false, nil, ""
	c.landBy = optMs{}
}

// promptFailed: a prompt never reached the coordinator: what it carried goes back to the front
// of the queue, in order. Nothing was typed, so no Stop or status event will follow: while the
// coordinator is up it is tried again on a timer (PromptRetryMs, doubling). After
// MaxPromptFailures in a row, its jobs fail out loud instead.
func (c *AgentController) promptFailed(now Millis, fx *[]Effect) {
	*fx = append(*fx, logEffect("prompt failed"))
	c.promptFailures++
	c.stopLanding()
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
			c.failJob(id, "the coordinator could not be reached", now, fx)
		}
		c.syncHold(fx)
	} else if c.coordinator == coordUp {
		c.retryAt = someMs(now + PromptRetryMs<<(c.promptFailures-1))
	}
}

// watchdogFired: no Stop or status event since a prompt timed out: its jobs fail out loud.
func (c *AgentController) watchdogFired(jobs []JobID, now Millis, fx *[]Effect) {
	*fx = append(*fx, logEffect("no sign of life after a timed-out prompt"))
	c.stopLanding()
	for _, id := range jobs {
		c.inbox.DropInFlight(id)
		c.inbox.DropQueued(id)
		if j, ok := c.jobs.Get(id); ok {
			c.failJobSaying(id, "the coordinator could not be reached", unreachable+": "+short(j.Task), now, fx)
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
	if c.landBy.due(now) {
		*fx = append(*fx, logEffect("prompt not taken by the coordinator: sending it again"))
		c.watchdog = nil
		c.promptFailed(now, fx)
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
		// A lost Stop also leaves its turn open.
		c.turnOpen = false
		if c.landing && idleWaited && !watched {
			// That lost Stop was another turn's: the prompt in flight still has to land, on its
			// own clock from now, before anything else goes out.
			if !c.landBy.set {
				c.landBy = someMs(now + PromptLandMs)
			}
			idleWaited = false
		} else {
			c.awaitingStop = false
		}
	}
	// After a watchdog the coordinator may well be idle: what else is queued gets its chance.
	if (watched || settled || retry || idleWaited) && c.coordinator == coordUp {
		c.inbox.MarkReady()
		c.flush(now, fx)
	}
}

func (c *AgentController) clearTimers() {
	c.stopLanding()
	c.turnOpen = false
	c.retryAt = optMs{}
	c.settleAt = optMs{}
	c.watchdog = nil
	c.idleWaitAt = optMs{}
	c.awaitingStop = false
}

// disable turns delegation off (spec §9): every open job fails with "the coordinator stopped".
func (c *AgentController) disable(status string, now Millis, fx *[]Effect) {
	c.disabled = status
	c.setStatus(status, fx)
	c.inbox = NewInbox()
	for _, id := range c.OpenJobs() {
		c.failJob(id, "the coordinator stopped", now, fx)
	}
	c.syncHold(fx)
}

func (c *AgentController) failJob(id JobID, reason string, now Millis, fx *[]Effect) {
	task := ""
	if j, ok := c.jobs.Get(id); ok {
		task = short(j.Task)
	}
	c.failJobSaying(id, reason, "The coordinator stopped before finishing: "+task, now, fx)
}

// failJobSaying fails an open job and says line (with the error cue).
func (c *AgentController) failJobSaying(id JobID, reason, line string, now Millis, fx *[]Effect) {
	if _, err := c.jobs.Fail(id, reason, now); err != nil {
		return
	}
	c.denyPendingFor(id, "The job ended.", fx)
	c.stopJobWorkers(id, fx)
	c.forgetUpdates(id)
	c.saveJob(id, fx)
	c.deliverOpening(NewJobLine(LineReply, line, id), now, fx)
	*fx = append(*fx, Effect{Kind: EfCue, Cue: CueError})
}

// ---------- replies ----------

func (c *AgentController) onStop(message string, now Millis, fx *[]Effect) {
	c.turnOpen = false
	c.turnStopped = false
	// The open jobs in the prompt this turn answered.
	var answered []JobID
	if c.landing {
		// The prompt in flight was never taken: this turn was another's (a background agent's
		// notification, say), and answers none of it. It has a while from now to land; nothing
		// else is sent meanwhile.
		c.landBy = someMs(now + PromptLandMs)
	} else {
		answered = c.openInFlight()
		c.turnStopped = c.onlyStoppedTyped()
		// The turn is over. The next prompt goes out on Idle/Done, or after a short settle if
		// Herdr missed the Idle (the TUI may still be finishing right at the Stop). An Idle that
		// came first doesn't send at once either: the settle covers it.
		c.inbox.FinishTurn()
		c.awaitingStop = false
		c.idleWaitAt = optMs{}
		c.promptFailures = 0
		c.watchdog = nil
		c.settleAt = someMs(now + StopSettleMs)
	}
	memory, memoryRef, hasMemory := ParseMemory(message)
	message = StripMemory(message)
	tokens := c.pendingTokens()
	blocks := ParseSpoken(message)
	c.rememberTurn(message, blocks, answered, memory, memoryRef, hasMemory, tokens, now, fx)
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

// rememberTurn keeps exactly one memory of a finished coordinator turn: its <memory> block, or
// else one made from what it said and the task of the job it was about.
func (c *AgentController) rememberTurn(message string, blocks []Spoken, answered []JobID, memory string, memoryRef *string,
	hasMemory bool, tokens []string, now Millis, fx *[]Effect) {
	known := func(ref *string) (JobID, bool) {
		if ref == nil {
			return "", false
		}
		id, ok := ParseJobID(*ref)
		if !ok {
			return "", false
		}
		_, ok = c.jobs.Get(id)
		return id, ok
	}
	m := Memory{At: now, Source: MemorySourceCoordinator}
	if id, ok := known(memoryRef); ok {
		m.Job = &id
	}
	if !hasMemory {
		m.Source = MemorySourceFallback
		for _, b := range blocks {
			if id, ok := known(b.Reference); ok {
				m.Job = &id
				break
			}
		}
		if m.Job == nil && len(answered) > 0 {
			m.Job = &answered[0]
		}
		var said []string
		for _, b := range blocks {
			said = append(said, b.Text)
		}
		if len(blocks) == 0 {
			said = append(said, SpokenFallback(message))
		}
		task := ""
		if m.Job != nil {
			if j, ok := c.jobs.Get(*m.Job); ok {
				task = strings.TrimPrefix(j.Task, RecallPrefix)
			}
		}
		memory = FallbackMemory(task, strings.Join(said, " "))
	}
	// A confirm word must never outlive its question.
	m.Text = CapMemory(maskPendingTokens(memory, tokens))
	if m.Text == "" {
		m.Text = noReplyMemory
	}
	if line, trimmed := c.memories.Add(m); trimmed {
		*fx = append(*fx, Effect{Kind: EfRewrite, File: MemoriesFile, Lines: c.memories.Lines()})
	} else {
		*fx = append(*fx, Effect{Kind: EfAppend, File: MemoriesFile, Line: line})
	}
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
		c.deliverUnsolicited(text, now, fx)
	}
	// A question or progress can't say which job: they stay open. Failed is terminal: they fail
	// (leaving them open would keep Pixie holding).
	switch status {
	case SpokenQuestion, SpokenWorking:
		return
	case SpokenFailed:
		for _, id := range answered {
			c.failJob(id, "the coordinator reported a failure", now, fx)
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
	return fmt.Sprintf("The coordinator finished: %s; details are in the terminal.", task)
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
			} else if ok && c.typed[id] {
				continue // a typed job Stop closed: its confirmation isn't news (#221)
			}
		}
		c.deliverUnsolicited(text, now, fx)
	}
}

func (c *AgentController) onReply(id JobID, status SpokenStatus, text string, now Millis, fx *[]Effect) {
	// The coordinator's checked reply supersedes the job's worker lines still held.
	c.updates.Supersede(id)
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
		line := NewJobLine(LineReply, text, id)
		if c.settings.OpenConversation && !c.voiceOpen && !c.typed[id] {
			// #113: progress opens no conversation, so it doesn't chime or notify either (a
			// chime means one follows); it waits in the outbox for the next conversation.
			c.applyDelivery(OutboxDelivery(line), now, fx)
			break
		}
		c.deliver(line, now, fx)
	case SpokenQuestion:
		_, _ = c.jobs.Transition(id, JobQuestion, now)
		c.jobs.SetReply(id, text, now)
		c.saveJob(id, fx)
		c.askedBy(asker{job: id, at: now})
		c.deliverOpening(NewJobLine(LineQuestion, text, id), now, fx)
	case SpokenDone:
		_, _ = c.jobs.Transition(id, JobDone, now)
		c.jobs.SetReply(id, text, now)
		c.saveJob(id, fx)
		repeats := c.handedOff[id]
		c.forgetUpdates(id)
		c.denyPendingFor(id, "The job ended.", fx)
		c.stopJobWorkers(id, fx)
		if repeats {
			// #177: Pixie said the hand-off already; this only repeats it. A typed job's box
			// still hears it's done (#231), with the hand-off it shows, or it thinks on.
			if j, ok := c.jobs.Get(id); ok && c.typed[id] {
				handOff := text
				if j.LastProgress != nil {
					handOff = *j.LastProgress
				}
				c.deliver(NewJobLine(LineReply, handOff, id), now, fx)
			}
			break
		}
		if c.deliverOpening(NewJobLine(LineReply, text, id), now, fx) {
			*fx = append(*fx, Effect{Kind: EfCue, Cue: CueHappy})
		}
	case SpokenFailed:
		_, _ = c.jobs.Fail(id, text, now)
		c.saveJob(id, fx)
		c.forgetUpdates(id)
		c.denyPendingFor(id, "The job ended.", fx)
		c.stopJobWorkers(id, fx)
		c.deliverOpening(NewJobLine(LineReply, text, id), now, fx)
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
	if !VoiceConfirmableWith(toolName, toolInput, c.settings.SpokenPaths) {
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
	text := ConfirmQuestionLine(DescribeWith(toolName, toolInput, label, c.settings.SpokenPaths), token)
	q := Line{Text: text, Kind: LineQuestion, Token: &token, Job: job}
	if agent != Coordinator {
		q.Worker = agent
	}
	before := c.outbox.Len()
	c.deliverOpening(q, now, fx)
	// Said in a conversation, it never reached the outbox: keep it there as heard, so the bubble
	// lists it with Allow and Deny while it waits (#97).
	if !c.outbox.HasTokenSince(before, token) {
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: c.outbox.PushHeard(q, now)})
	}
}

// ManualNotice is what Pixie says, once per app run, when a Claude agent fell back to Manual.
const ManualNotice = "Heads up: one of my agents is asking before everything because it's running in Claude Code's Manual mode. I've put the reason in the menu."

// clearMode forgets an agent's mode when it ends (a worker closes, the coordinator stops or
// restarts) and clears its tray flag if it was on, so a later "default" flags it again.
func (c *AgentController) clearMode(agent string, fx *[]Effect) {
	was := c.modes[agent]
	delete(c.modes, agent)
	if was {
		*fx = append(*fx, Effect{Kind: EfManual, Agent: agent, Manual: false})
	}
}

// onMode: a Claude agent launched with --permission-mode auto that reports "default" fell back
// to Manual (#97). Codex agents never report it, and anything else is ignored.
func (c *AgentController) onMode(agent, mode string, now Millis, fx *[]Effect) {
	if mode != "auto" && mode != "default" {
		return
	}
	kind := c.settings.CoordinatorKind
	if agent != Coordinator {
		w := c.workers.Get(agent)
		if w == nil {
			return
		}
		kind = w.Plan.Kind
	}
	if kind == Codex {
		return
	}
	manual := mode == "default"
	if c.modes[agent] == manual {
		return
	}
	c.modes[agent] = manual
	*fx = append(*fx, Effect{Kind: EfManual, Agent: agent, Manual: manual})
	if manual && !c.manualSaid {
		c.manualSaid = true
		c.deliver(NewLine(LineInfo, ManualNotice), now, fx)
	}
}

func (c *AgentController) onUserSaid(text string, now Millis, fx *[]Effect) {
	for _, d := range c.confirms.OnTranscript(text) {
		c.applyDecided(d, now, fx)
	}
}

// applyDecided turns one settled request into its effects: the decision, the job resuming, and
// the line saying so. A spoken answer and a tap on the bubble both end here.
func (c *AgentController) applyDecided(d Decided, now Millis, fx *[]Effect) {
	if d.Pending.Session != "" {
		c.answerSession(d.Pending, map[bool]string{true: "yes", false: "no"}[d.Allow], now, fx)
		return
	}
	decision := Deny("The user said no.")
	said := "Coordinator: denied"
	if d.Allow {
		decision, said = Allow(), "Coordinator: allowed"
	}
	*fx = append(*fx, decideEffect(d.Pending.ID, decision))
	c.resumeJob(d.Pending.Job, now, fx)
	c.deliver(NewLine(LineInfo, said), now, fx)
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

// onSessionSent: the request in flight went to one of your sessions (#177). Pixie says so now
// rather than after the coordinator writes its reply; it stays the coordinator's job until then.
// With several requests in flight, which one it was isn't known: the reply tells.
func (c *AgentController) onSessionSent(description, waiting string, now Millis, fx *[]Effect) {
	refs := unique(c.inbox.InFlightRefs())
	if len(refs) != 1 || !c.jobs.IsOpen(refs[0]) {
		return
	}
	job := refs[0]
	session, title := "your session", "Your session"
	if d := oneLine(description); d != "" {
		title = strings.TrimSuffix(d, ".")
		session = "your session on " + title
	}
	text, brief := "Passed on to "+session+".", "Message passed on."
	if waiting != "" {
		// #183: pix holds it; say so, and why.
		why := map[string]string{
			"working": "it's busy, and gets it with its next step or when it stops",
			"blocked": "it's waiting on a question, and gets it once that's answered",
			"typing":  "you're typing there, and it gets it once you send or clear that",
		}[waiting]
		if why == "" {
			why = "it gets it as soon as it can"
		}
		text = strings.ToUpper(session[:1]) + session[1:] + " hasn't got your message yet: " + why + "."
		brief = map[string]string{
			"working": "Message held: it's busy.",
			"blocked": "Message held: it's waiting on a question.",
			"typing":  "Message held: you're typing there.",
		}[waiting]
		if brief == "" {
			brief = "Message held for now."
		}
	}
	c.handedOff[job] = true
	tokens := c.pendingTokens()
	text = maskPendingTokens(text, tokens) // kept masked too: a typed job's box shows it again (#231)
	c.jobs.SetProgress(job, text, now)
	c.saveJob(job, fx)
	line := NewJobLine(LineReply, text, job)
	line.Title, line.Brief = maskPendingTokens(title, tokens), brief
	c.deliverOpening(line, now, fx)
}

func (c *AgentController) onPostTool(toolName string, toolInput any, now Millis, fx *[]Effect) {
	for _, job := range c.inbox.InFlightRefs() {
		if c.handedOff[job] {
			c.handedOff[job] = false // more work since: its reply is news
		}
	}
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
	c.holdProgress(job, phrase, now)
}

// holdProgress holds a job's progress phrase for narration (open conversation only): said once
// the job is quiet, at most once per NarrateIntervalMs, unless a report or reply beats it.
func (c *AgentController) holdProgress(job JobID, phrase string, now Millis) {
	if c.settings.Narration && c.voiceOpen && phrase != "" {
		c.updates.AddProgress(job, phrase, now)
	}
}

// flushUpdates says the jobs' coalesced updates that are due: reports by RouteUpdate, progress
// as narration if the conversation is still open and the cap has a slot (else it is dropped).
func (c *AgentController) flushUpdates(now Millis, fx *[]Effect) {
	if c.shutDown {
		return
	}
	ready := func(job JobID) bool { return !c.voiceOpen || c.narrator.Ready(job, now) }
	for _, u := range c.updates.Due(now, ready) {
		if !u.Progress {
			l := NewJobLine(LineInfo, u.Text, u.Job)
			l.Worker = u.Worker
			c.deliverUpdate(l, now, fx)
			continue
		}
		if c.voiceOpen && c.router.Take(now) {
			c.narrator.Allow(u.Job, now)
			c.narrate(u.Text, fx)
		}
	}
}

// deliverUpdate routes an update line under the cap in both conversation states. It is the
// coalesced line Updates let out, so with no conversation open it is also what opens one
// (agent.openConversationForUpdates).
// MaxSessionDetailBytes caps the reply the voice model is given to sum up (#114).
const MaxSessionDetailBytes = 2 << 10

// onSessionTurn: a session you started with pix finished a turn (#114). With the setting on,
// a turn of yours that ran long enough, or any turn Pixie started, is told like an update:
// its gist, or its question (never held by the cap). Pending confirm words are masked in all
// of it: the label is agent-settable too.
func (c *AgentController) onSessionTurn(name string, t SessionTurn, now Millis, fx *[]Effect) {
	if !c.settings.SessionTurns || c.shutDown {
		return
	}
	if !t.FromPixie && t.Duration < c.settings.SessionTurnsMinMs {
		return
	}
	tokens := c.pendingTokens()
	reply := maskPendingTokens(t.Reply, tokens)
	gist, asking := SessionGist(reply)
	switch {
	case asking:
		c.askedBy(asker{session: name, at: now})
	case c.asked.session == name:
		c.asked = asker{} // it moved on: its question is gone
	}
	label := maskPendingTokens(t.Label, tokens)
	c.deliverUpdate(Line{
		Kind:    LineSession,
		Text:    SessionTurnLine(label, gist, asking),
		Title:   label,
		Brief:   SessionTurnBrief(gist),
		Asking:  asking,
		App:     t.App,
		Session: name,
		Detail:  truncateBytes(strings.Join(strings.Fields(reply), " "), MaxSessionDetailBytes),
	}, now, fx)
}

func (c *AgentController) deliverUpdate(line Line, now Millis, fx *[]Effect) {
	for _, d := range c.router.RouteUpdate(c.voiceOpen, line, now) {
		if d.Kind == DeliverInject {
			c.remember(d.Notice, []Line{line})
		}
		c.applyDelivery(d, now, fx)
	}
	c.openForUpdate(now, fx)
}

func (c *AgentController) forgetUpdates(job JobID) {
	delete(c.handedOff, job)
	c.narrator.Forget(job)
	c.updates.Supersede(job)
}

// conversationClosed: updates the cap held for the conversation go to the outbox.
func (c *AgentController) conversationClosed(now Millis, fx *[]Effect) {
	c.voiceOpen = false
	for _, d := range c.router.ConversationClosed() {
		c.applyDelivery(d, now, fx)
	}
}

// narrate injects a progress line (remembered with no lines: dropped if it bounces).
func (c *AgentController) narrate(phrase string, fx *[]Effect) {
	n := Notice{Text: "Coordinator progress: " + phrase}
	c.remember(n, nil)
	*fx = append(*fx, Effect{Kind: EfInject, Notice: n})
}

func (c *AgentController) tick(now Millis, fx *[]Effect) {
	if c.restartAt.due(now) {
		c.restartAt = optMs{}
		c.ensureCoordinator(fx)
	}
	c.tickInbox(now, fx)
	c.tickWorkers(now, fx)
	c.tickLiveness(now, fx)
	for _, p := range c.confirms.Expire(now) {
		*fx = append(*fx, decideEffect(p.ID, Deny("No answer from the user.")))
		c.resumeJob(p.Job, now, fx)
		c.deliver(NewLine(LineInfo, "Coordinator: denied after two minutes with no answer."), now, fx)
	}
	c.flushUpdates(now, fx)
	for _, d := range c.router.Tick(c.voiceOpen, now) {
		if d.Kind == DeliverInject {
			c.remember(d.Notice, d.Lines)
		}
		c.applyDelivery(d, now, fx)
	}
	c.tryOpen(now, fx)
	blockedLong := c.blocked.set && satSub(now, c.blocked.v) >= BlockedGraceMs
	if blockedLong && !c.hinted && !c.confirms.HasPendingFor(Coordinator) {
		c.hinted = true
		c.setStatus("Coordinator: waiting in terminal", fx)
		l := NewLine(LineInfo, BlockedHint)
		l.Title, l.Brief = "Coordinator", "Waiting at a prompt in its terminal."
		c.deliver(l, now, fx)
		// No job: FocusTarget would pick the job's worker; this is the coordinator's own pane.
		*fx = append(*fx, Effect{Kind: EfShowPane})
	}
}

// ---------- helpers ----------

// deliver routes a line; true if the user got it now (spoken or notified), not just outboxed.
func (c *AgentController) deliver(line Line, now Millis, fx *[]Effect) bool {
	if c.typed[line.Job] {
		*fx = append(*fx, Effect{Kind: EfChat, Chat: c.chatLine(line)})
		if line.Token == nil {
			return true
		}
		// A permission request also goes where it can be allowed: the bubble or a notification.
	}
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

// AskedLast: id's question is the one Pixie relayed last, and it is still waiting.
func (c *AgentController) AskedLast(id JobID) bool {
	j, ok := c.jobs.Get(id)
	return c.asked.session == "" && c.asked.job == id && ok && j.Status == JobQuestion
}

// noAnswerWaiting: an answer with no question waiting for it.
const noAnswerWaiting = "No job is waiting for an answer."

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
	// Any other id is ignored: voice models copy example ids or make them up (Oct 18).
	case c.asked.session != "":
		*fx = append(*fx, Effect{Kind: EfTellSession, Name: c.asked.session, Text: strings.TrimSpace(text)},
			Effect{Kind: EfAnswerResult, Name: c.asked.session})
		c.asked = asker{}
		return
	case c.asked.job != "" && c.jobs.IsOpen(c.asked.job) && waiting(c.asked.job):
		id = c.asked.job
	default:
		var asking []JobID
		for _, o := range c.OpenJobs() {
			if waiting(o) {
				asking = append(asking, o)
			}
		}
		switch len(asking) {
		case 0:
			err = noAnswerWaiting
		case 1:
			id = asking[0]
		default:
			// Several waiting, none asked last: the coordinator works out which this answers.
			c.inbox.Push(c.answerToSort(asking, text))
			*fx = append(*fx, Effect{Kind: EfAnswerResult, Text: "coordinator"})
			c.asked = asker{}
			c.flush(now, fx)
			return
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
		if !c.away {
			*fx = append(*fx, Effect{Kind: EfChime})
		}
	case DeliverNotify:
		*fx = append(*fx, Effect{Kind: EfNotify, Text: d.Text})
	case DeliverOutbox:
		for _, r := range c.outbox.Repeats(d.Line) {
			*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: r})
		}
		record := c.outbox.Push(d.Line, now)
		*fx = append(*fx, Effect{Kind: EfAppend, File: OutboxFile, Line: record})
		c.attachNotification(*fx, d.Line)
	}
}

func (c *AgentController) saveJob(id JobID, fx *[]Effect) {
	if line, ok := c.jobs.Line(id); ok {
		*fx = append(*fx, Effect{Kind: EfAppend, File: JobsFile, Line: line})
	}
}

// syncHold holds Pixie in delegating (and the conversation open) while a job is running or a
// permission of an agent's waits on a spoken confirm (it expires in two minutes); a job that
// only waits on the user's answer to a question doesn't, nor does a pix session's prompt, which
// waits until answered (#119).
func (c *AgentController) syncHold(fx *[]Effect) {
	asking := slices.ContainsFunc(c.confirms.Pending(), func(p Pending) bool { return p.Session == "" })
	if running := c.jobs.AnyRunning() || asking; running != c.held {
		c.held = running
		*fx = append(*fx, Effect{Kind: EfHoldOpen, Hold: running})
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
// invisible characters) is removed. A confirm prompt it made up for a word that is not pending
// is defused too (see defuseInventedPrompts).
func maskPendingTokens(text string, tokens []string) string {
	text = defuseInventedPrompts(text, tokens)
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
	for _, p := range []string{"Coordinator reply: ", "Coordinator question: ", "Coordinator says: ", "Coordinator progress: "} {
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
	case HostRefused:
		return "refused"
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

// asker is who asked the question Pixie relayed last (#161): a job, or a session whose turn
// ended on a question. Two asked at once (several questions in one Stop) are no one: which to
// answer is the user's to say.
type asker struct {
	job     JobID
	session string
	at      Millis
	tie     bool
}

func (c *AgentController) askedBy(a asker) {
	if prev := c.asked; prev.at == a.at && (prev.job != "" || prev.session != "" || prev.tie) && prev != a {
		a = asker{tie: true, at: a.at}
	}
	c.asked = a
}

// answerToSort is the coordinator's message for an answer to one of several waiting questions
// (#161): it works out which, and asks the user only if it can't tell.
func (c *AgentController) answerToSort(asking []JobID, text string) Message {
	var b strings.Builder
	fmt.Fprintf(&b, "The user answered: %q. Several of your questions are waiting:", strings.TrimSpace(text))
	for _, id := range asking {
		q := ""
		if j, ok := c.jobs.Get(id); ok && j.Reply != nil {
			q = *j.Reply
		}
		fmt.Fprintf(&b, "\n- %s asked: %q", id, q)
	}
	b.WriteString("\nYou work out which question it answers (all of them, if they ask the same thing) and go on with it; ask the user only if you can't tell.")
	for _, id := range asking {
		fmt.Fprintf(&b, "\n[ref %s]", id)
	}
	return Message{Text: b.String(), Refs: asking}
}
