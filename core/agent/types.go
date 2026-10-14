// Package agent is the delegation core (port of ai-mascot crates/agent-core): jobs, routing,
// confirm words, the coordinator inbox, worker state and the controller. Pure: no processes,
// no sockets. Adapters implement the interfaces in traits.go and the composition root injects
// them.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sihekuang/deskpixie/core/voice"
)

// Notice and VoiceSignal are voice-core's types, re-exported as Rust's agent-core does.
type (
	Notice      = voice.Notice
	VoiceSignal = voice.VoiceSignal
)

// Millis is milliseconds since the Unix epoch, from Clock.NowMs.
type Millis = uint64

// Coordinator is the coordinator's agent name in the session (and its Herdr tab label).
const Coordinator = "coordinator"

// JobID is "job_" + 12 lowercase hex digits. The zero value "" means "no job" where Rust has
// Option<JobId>.
type JobID string

// ParseJobID accepts only "job_" + 12 lowercase hex digits.
func ParseJobID(s string) (JobID, bool) {
	hex, ok := strings.CutPrefix(s, "job_")
	if !ok || len(hex) != 12 {
		return "", false
	}
	for i := 0; i < len(hex); i++ {
		b := hex[i]
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
			return "", false
		}
	}
	return JobID(s), true
}

// JobIDFromBits is the id for 48 random (or, in tests, sequential) bits.
func JobIDFromBits(bits uint64) JobID {
	return JobID(fmt.Sprintf("job_%012x", bits&0xffff_ffff_ffff))
}

func (j JobID) String() string { return string(j) }

// UnmarshalJSON rejects anything that is not a job id (serde try_from in Rust).
func (j *JobID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	id, ok := ParseJobID(s)
	if !ok {
		return fmt.Errorf("not a job id: %q", s)
	}
	*j = id
	return nil
}

// enumJSON maps an int enum to and from its lowercase serde names.
func enumMarshal(names []string, v int) ([]byte, error) {
	if v < 0 || v >= len(names) {
		return nil, fmt.Errorf("bad enum value %d", v)
	}
	return json.Marshal(names[v])
}

func enumUnmarshal(names []string, b []byte) (int, error) {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return 0, err
	}
	for i, n := range names {
		if n == s {
			return i, nil
		}
	}
	return 0, fmt.Errorf("unknown variant %q", s)
}

// JobStatus is a job's lifecycle state (serialized lowercase).
type JobStatus int

const (
	JobDelegated JobStatus = iota
	JobWorking
	JobQuestion
	JobDone
	JobFailed
	JobCancelled
)

var jobStatusNames = []string{"delegated", "working", "question", "done", "failed", "cancelled"}

func (s JobStatus) String() string {
	if s < 0 || int(s) >= len(jobStatusNames) {
		return fmt.Sprintf("JobStatus(%d)", int(s))
	}
	return jobStatusNames[s]
}

// IsOpen: Delegated, Working or Question.
func (s JobStatus) IsOpen() bool { return s == JobDelegated || s == JobWorking || s == JobQuestion }

func (s JobStatus) MarshalJSON() ([]byte, error) { return enumMarshal(jobStatusNames, int(s)) }

func (s *JobStatus) UnmarshalJSON(b []byte) error {
	v, err := enumUnmarshal(jobStatusNames, b)
	*s = JobStatus(v)
	return err
}

// Job is one delegated job. Its JSON form (snake_case, nulls for absent options, workers as a
// list) matches Rust's jobs.jsonl lines.
type Job struct {
	ID      JobID     `json:"id"`
	Task    string    `json:"task"`
	Status  JobStatus `json:"status"`
	Created Millis    `json:"created"`
	Updated Millis    `json:"updated"`
	// LastProgress is the latest narration phrase or progress line.
	LastProgress *string `json:"last_progress"`
	// Reason says why it failed (e.g. "app restarted").
	Reason *string `json:"reason"`
	// Workers started for this job, oldest first.
	Workers []WorkerRow `json:"workers"`
}

// MarshalJSON writes workers as [] (never null), as serde does for an empty Vec.
func (j Job) MarshalJSON() ([]byte, error) {
	type plain Job
	p := plain(j)
	if p.Workers == nil {
		p.Workers = []WorkerRow{}
	}
	return json.Marshal(p)
}

// UnmarshalJSON requires the fields serde requires (id, task, status, created, updated);
// last_progress, reason and workers default.
func (j *Job) UnmarshalJSON(b []byte) error {
	var raw struct {
		ID           *JobID      `json:"id"`
		Task         *string     `json:"task"`
		Status       *JobStatus  `json:"status"`
		Created      *Millis     `json:"created"`
		Updated      *Millis     `json:"updated"`
		LastProgress *string     `json:"last_progress"`
		Reason       *string     `json:"reason"`
		Workers      []WorkerRow `json:"workers"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw.ID == nil || raw.Task == nil || raw.Status == nil || raw.Created == nil || raw.Updated == nil {
		return errors.New("job: missing field")
	}
	*j = Job{ID: *raw.ID, Task: *raw.Task, Status: *raw.Status, Created: *raw.Created, Updated: *raw.Updated,
		LastProgress: raw.LastProgress, Reason: raw.Reason, Workers: raw.Workers}
	return nil
}

// AgentKind is which agent CLI a pane runs.
type AgentKind int

const (
	Claude AgentKind = iota
	Codex
)

var agentKindNames = []string{"claude", "codex"}

// ParseAgentKind accepts claude | codex (any case, trimmed).
func ParseAgentKind(s string) (AgentKind, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "claude":
		return Claude, true
	case "codex":
		return Codex, true
	}
	return 0, false
}

// String is the multiplexer's agent kind name (`herdr agent start --kind <name>`).
func (k AgentKind) String() string {
	if k < 0 || int(k) >= len(agentKindNames) {
		return fmt.Sprintf("AgentKind(%d)", int(k))
	}
	return agentKindNames[k]
}

func (k AgentKind) MarshalJSON() ([]byte, error) { return enumMarshal(agentKindNames, int(k)) }

func (k *AgentKind) UnmarshalJSON(b []byte) error {
	v, err := enumUnmarshal(agentKindNames, b)
	*k = AgentKind(v)
	return err
}

// WorkerStatus is a worker's lifecycle as the job table records it.
type WorkerStatus int

const (
	// WorkerStarting: admitted; its worktree and pane are being made.
	WorkerStarting WorkerStatus = iota
	// WorkerRunning: its pane is up (working or idle).
	WorkerRunning
	// WorkerBlocked: waiting at a prompt in its terminal.
	WorkerBlocked
	WorkerStopped
	// WorkerFailed: it never started.
	WorkerFailed
)

var workerStatusNames = []string{"starting", "running", "blocked", "stopped", "failed"}

func (s WorkerStatus) String() string {
	if s < 0 || int(s) >= len(workerStatusNames) {
		return fmt.Sprintf("WorkerStatus(%d)", int(s))
	}
	return workerStatusNames[s]
}

// IsLive: starting, running or blocked (it counts toward workers.max).
func (s WorkerStatus) IsLive() bool {
	return s == WorkerStarting || s == WorkerRunning || s == WorkerBlocked
}

func (s WorkerStatus) MarshalJSON() ([]byte, error) { return enumMarshal(workerStatusNames, int(s)) }

func (s *WorkerStatus) UnmarshalJSON(b []byte) error {
	v, err := enumUnmarshal(workerStatusNames, b)
	*s = WorkerStatus(v)
	return err
}

// WorkerRow is one worker on a job.
type WorkerRow struct {
	Name string    `json:"name"`
	Kind AgentKind `json:"kind"`
	// Repo is the repository's folder name (never a full path).
	Repo   string       `json:"repo"`
	JobID  JobID        `json:"job_id"`
	Status WorkerStatus `json:"status"`
}

// PaneStatus is an agent pane's state as the multiplexer detects it.
type PaneStatus int

const (
	PaneIdle PaneStatus = iota
	PaneWorking
	PaneBlocked
	PaneDone
	PaneUnknown
)

// ParsePaneStatus takes Herdr's names; anything else is PaneUnknown.
func ParsePaneStatus(s string) PaneStatus {
	switch s {
	case "idle":
		return PaneIdle
	case "working":
		return PaneWorking
	case "blocked":
		return PaneBlocked
	case "done":
		return PaneDone
	}
	return PaneUnknown
}

func (s PaneStatus) String() string {
	switch s {
	case PaneIdle:
		return "idle"
	case PaneWorking:
		return "working"
	case PaneBlocked:
		return "blocked"
	case PaneDone:
		return "done"
	}
	return "unknown"
}

// IsReady: the agent finished (or never started) a turn.
func (s PaneStatus) IsReady() bool { return s == PaneIdle || s == PaneDone }

// PaneEventKind tells PaneEvent variants apart.
type PaneEventKind int

const (
	PaneEventStatus PaneEventKind = iota
	PaneEventExited
)

// PaneEvent is a status change (Status set) or an exit of a named agent.
type PaneEvent struct {
	Kind   PaneEventKind
	Agent  string
	Status PaneStatus // PaneEventStatus only
}

// AgentInfo is a running agent as the session host lists it. Cwd "" means unknown.
type AgentInfo struct {
	Name   string
	Pane   string
	Cwd    string
	Status PaneStatus
}

// EnvVar is one environment entry (Rust's (String, String) pair).
type EnvVar struct {
	Key   string
	Value string
}

// AgentSpec is what SessionHost.StartAgent launches: a new tab running Kind with Args.
type AgentSpec struct {
	Name string
	Kind AgentKind
	Cwd  string
	Args []string
	// Env is the per-tab environment. Only MASCOT_AGENT_TOKEN.
	Env []EnvVar
	// Tab is what the tab shows (label "" = Name), where the host supports it.
	Tab TabLabel
}

// HostErrorKind tells HostError variants apart.
type HostErrorKind int

const (
	HostNotFound HostErrorKind = iota
	HostVersionMismatch
	HostNoAgent
	HostTimeout
	HostGaveUp
	// HostExitedAtStart: the agent process exited during its launch (e.g. `claude --continue`
	// with no conversation to continue). Detail carries the agent kind.
	HostExitedAtStart
	HostOther
)

// HostError is a SessionHost failure; its messages match Rust's thiserror strings.
type HostError struct {
	Kind           HostErrorKind
	Client, Server uint32 // HostVersionMismatch
	Detail         string // NoAgent name, Timeout what, ExitedAtStart kind, Other text
}

func (e HostError) Error() string {
	switch e.Kind {
	case HostNotFound:
		return "herdr not found"
	case HostVersionMismatch:
		return fmt.Sprintf("herdr version mismatch (client protocol %d, server %d)", e.Client, e.Server)
	case HostNoAgent:
		return "no agent named " + e.Detail
	case HostTimeout:
		return "timed out: " + e.Detail
	case HostGaveUp:
		return "stopped after repeated crashes"
	case HostExitedAtStart:
		return e.Detail + " exited while starting"
	}
	return e.Detail
}

// IsHostError reports whether err is a HostError of kind k.
func IsHostError(err error, k HostErrorKind) bool {
	var he HostError
	return errors.As(err, &he) && he.Kind == k
}

// LaunchError, TrustError and WorktreeError are single-string errors.
type (
	LaunchError   string
	TrustError    string
	WorktreeError string
)

func (e LaunchError) Error() string   { return string(e) }
func (e TrustError) Error() string    { return string(e) }
func (e WorktreeError) Error() string { return string(e) }

// WorkerLaunch is what a WorkerRecipes adapter needs to prepare one worker's launch.
type WorkerLaunch struct {
	Name string
	Kind AgentKind
	// Repo is the repository root (canonical).
	Repo string
	// Worktree is `<repo>/.worktrees/<slug>`: the worker's cwd.
	Worktree string
	// Token is the worker's bearer (MASCOT_AGENT_TOKEN).
	Token string
}

// StartRefusal is a screen that fails a start: when the pane shows one of Screens during its
// start (every marker of a group in its bottom ScreenBottomLines lines; e.g. a folder-trust
// prompt the app won't answer for the user), the start fails with Reason and nothing is typed.
type StartRefusal struct {
	Screens [][]string
	Reason  string
}

// ScreenBottomLines is how many non-empty lines at the bottom of a screen a dialog is looked for in.
const ScreenBottomLines = 15

// ScreenBottom is the last n non-empty lines of screen.
func ScreenBottom(screen string, n int) []string {
	var lines []string
	for _, l := range rustLines(screen) {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines[max(0, len(lines)-n):]
}

// ShowsAnyScreen: every marker of one of screens shows in the bottom ScreenBottomLines lines.
func ShowsAnyScreen(screen string, screens [][]string) bool {
	bottom := ScreenBottom(screen, ScreenBottomLines)
	for _, group := range screens {
		if len(group) == 0 {
			continue
		}
		all := true
		for _, m := range group {
			found := false
			for _, l := range bottom {
				if strings.Contains(l, m) {
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// rustLines splits like Rust's str::lines: on '\n', a trailing "\r" dropped from each line, and
// no empty final line for a trailing newline.
func rustLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}

// StartReady is what a start waits for: the pane is not up (and nothing is sent) until it
// shows Marker (e.g. Codex's empty input box) with no refusal screen; if it never does, the
// start fails with Reason, its {last} replaced by the screen's last non-empty line (at most 80
// chars, control characters dropped).
type StartReady struct {
	Marker string
	Reason string
}

// PreparedWorker is a worker's launch spec plus its optional start checks.
type PreparedWorker struct {
	Spec    AgentSpec
	Refusal *StartRefusal
	Ready   *StartReady
}

// Outcome is report_result's outcome.
type Outcome int

const (
	OutcomeDone Outcome = iota
	OutcomeFailed
)

func (o Outcome) String() string {
	if o == OutcomeFailed {
		return "failed"
	}
	return "done"
}

// WorkerRequest is start_worker's arguments, checked and cleaned (ParseToolCall).
type WorkerRequest struct {
	Kind AgentKind
	// Repo is a folder name under the projects root, or a path.
	Repo string
	Task string
	// JobID as given; the controller checks that it names an open job.
	JobID string
	// Name is a name hint (`<repo>-` is added); "" when none was given.
	Name string
}

// McpCallKind tells McpCall variants apart.
type McpCallKind int

const (
	CallStartWorker McpCallKind = iota
	CallMessageWorker
	CallStopWorker
	CallListWorkers
	CallReportProgress
	CallReportResult
	CallAskCoordinator
	CallReportBlocked
)

// McpCall is one mascot MCP tool call, arguments checked and cleaned. Only the fields of its
// Kind are set: StartWorker → Worker; MessageWorker → Name, Text; StopWorker → Name;
// ReportProgress → Summary; ReportResult → Outcome, Summary, Evidence; AskCoordinator →
// Question, Options; ReportBlocked → Reason.
type McpCall struct {
	Kind     McpCallKind
	Worker   WorkerRequest
	Name     string
	Text     string
	Summary  string
	Outcome  Outcome
	Evidence string
	Question string
	Options  []string
	Reason   string
}

// McpReply is a tool's result (JSON value) or, when Err is set, the error text the calling
// agent sees (Rust's Result<Value, String>).
type McpReply struct {
	Value any
	Err   error
}

// Decision is a PermissionRequest answer.
type Decision struct {
	Allow bool
	// Message says why it was denied (shown to the agent); "" when allowed.
	Message string
}

func Allow() Decision              { return Decision{Allow: true} }
func Deny(message string) Decision { return Decision{Allow: false, Message: message} }

// HookEventKind tells HookEvent variants apart.
type HookEventKind int

const (
	HookStop HookEventKind = iota
	HookPermission
	HookPostTool
	// HookMcp is a mascot MCP tool call; answered on McpReply.
	HookMcp
)

// HookEvent is what the hook server reports, with the calling agent resolved from its bearer.
// Stop → Message; Permission → ToolName, ToolInput, Reply; PostTool → ToolName, ToolInput;
// Mcp → Call, McpReply. Reply channels are one-shot: send at most once (buffer them by 1).
type HookEvent struct {
	Kind      HookEventKind
	Agent     string
	Message   string
	ToolName  string
	ToolInput any
	Reply     chan<- Decision
	Call      McpCall
	McpReply  chan<- McpReply
}

// Cue is a one-shot mascot animation the delegation side asks for.
type Cue int

const (
	CueHappy Cue = iota
	CueError
)
