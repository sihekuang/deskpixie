package agent

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// The live workers (port of workers.rs): per worker its plan, a message queue sent only while
// it is idle (the inbox rule), its turns, the 60 s no-report grace, and the blocked-pane hint.
// Pure: callers pass now. Persistent rows live in the JobTable; this is runtime state.

const (
	// ReportGraceMs: a worker turn that ends without a report is forwarded after this long.
	ReportGraceMs Millis = 60_000
	// WorkerBlockedGraceMs: a worker blocked this long with no pending hook permission gets the hint.
	WorkerBlockedGraceMs Millis = 3_000
	// WorkerRetryMs: a message that could not be typed is retried after this long, doubling.
	WorkerRetryMs Millis = 2_000
	// MaxWorkerPromptFailures: this many failed sends in a row drop the worker's queued messages.
	MaxWorkerPromptFailures uint32 = 3
	// WorkerIdleWithoutStopMs: a Claude worker that went Idle but whose Stop hook never came is
	// ready after this long.
	WorkerIdleWithoutStopMs Millis = 10_000
	// WorkerDeliveryMs: a prompt the worker shows no sign of having got (Working, Blocked, a Stop
	// or a report) this long after it was sent gets its screen checked.
	WorkerDeliveryMs Millis = 20_000
	// MaxDeliveryNudges: re-sends, Enters and waits for one prompt before the worker is failed.
	MaxDeliveryNudges uint32 = 2
	// WorkerStartReadyMs: a worker up with its brief queued that never went Idle (a stale start
	// status) is treated as idle this long after it came up.
	WorkerStartReadyMs Millis = 30_000
)

// InputState is what a worker's input box shows (see InputStateOf).
type InputState int

const (
	// InputEmpty: the prompt was lost.
	InputEmpty InputState = iota
	// InputTyped: the prompt still sits in it, not submitted.
	InputTyped
	// InputUnknown: can't tell.
	InputUnknown
	// InputBusy: the agent is working (it shows how to interrupt it): the prompt arrived.
	InputBusy
	// InputTrust: Codex's folder-trust prompt: nothing may be typed into it.
	InputTrust
)

func (s InputState) String() string {
	return [...]string{"Empty", "Typed", "Unknown", "Busy", "Trust"}[s]
}

func containsAny(l string, markers []string) bool {
	return slices.ContainsFunc(markers, func(m string) bool { return strings.Contains(l, m) })
}

// InputStateOf reads the input box off screen (its last lines), for a prompt sent that shows no
// sign of having arrived. A Codex folder-trust prompt comes first (its "· esc quit" is no busy
// pane); then Codex's empty-box placeholder (it shows only while the box is empty); then a paste
// placeholder or the end of sent near the bottom of the screen means it was typed but not
// submitted. (Rust: workers::input_state.)
func InputStateOf(kind AgentKind, screen, sent string) InputState {
	if ShowsCodexTrust(screen) {
		return InputTrust
	}
	// Codex asking to run one of our tools: the prompt arrived, and it is blocked mid-turn.
	for _, l := range ScreenBottom(screen, ScreenBottomLines) {
		if containsAny(l, CodexMcpApprovalMarkers) {
			return InputBusy
		}
	}
	var lines []string
	for _, l := range rustLines(screen) {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	bottom := lines[max(0, len(lines)-8):]
	// Both CLIs show how to interrupt them while they work (Codex may keep its placeholder).
	if slices.ContainsFunc(bottom, func(l string) bool { return containsAny(l, BusyMarkers) }) {
		return InputBusy
	}
	if kind == Codex && slices.ContainsFunc(bottom, func(l string) bool { return strings.Contains(l, CodexIdleMarker) }) {
		return InputEmpty
	}
	if slices.ContainsFunc(bottom, func(l string) bool { return containsAny(l, PasteMarkers) }) {
		return InputTyped
	}
	tail := ""
	sentLines := rustLines(sent)
	for k := len(sentLines) - 1; k >= 0; k-- {
		if l := strings.TrimSpace(sentLines[k]); l != "" {
			tail = l
			break
		}
	}
	if utf8.RuneCountInString(tail) > 40 {
		tail = string([]rune(tail)[:40])
	}
	if tail != "" && slices.ContainsFunc(bottom, func(l string) bool { return strings.Contains(l, tail) }) {
		return InputTyped
	}
	return InputUnknown
}

// WorkerDeliveryKind tells WorkerDelivery variants apart.
type WorkerDeliveryKind int

const (
	// DeliveryRead: read its screen (answer with OnDeliveryScreen).
	DeliveryRead WorkerDeliveryKind = iota
	// DeliveryGaveUp: nudged enough: the worker never started on it.
	DeliveryGaveUp
)

// WorkerDelivery is a prompt whose delivery check is due (Rust workers::Delivery).
type WorkerDelivery struct {
	Kind WorkerDeliveryKind
	Name string
}

// NudgeKind tells Nudge variants apart.
type NudgeKind int

const (
	// NudgeResend: the box is empty: send the same text (Nudge.Text) again.
	NudgeResend NudgeKind = iota
	// NudgeEnter: the text sits in the box: press Enter.
	NudgeEnter
	// NudgeWait: can't tell: check again later.
	NudgeWait
	// NudgeBusy: it is working: the prompt arrived; nothing to do.
	NudgeBusy
	// NudgeArrived: Codex: its input no longer shows the empty-box placeholder: the prompt arrived.
	NudgeArrived
)

// Nudge is what to do about a prompt that has not arrived, from its screen.
type Nudge struct {
	Kind NudgeKind
	Text string // NudgeResend
}

// WorkerPlan is what admission decided for a start_worker call.
type WorkerPlan struct {
	Name string
	// Slug is the worktree folder and branch name (`pixie/<slug>`).
	Slug string
	Kind AgentKind
	// Repo is the canonical repository root.
	Repo string
	// RepoName is the repository's folder name (spoken and shown; never a path).
	RepoName string
	Job      JobID
	Task     string
}

// SilentKind tells Silent variants apart.
type SilentKind int

const (
	// SilentSaid: a Claude worker: its Stop hook's last message (Text).
	SilentSaid SilentKind = iota
	// SilentReadScreen: a Codex worker (no Stop hook), or a turn ended by the idle fallback: read its screen.
	SilentReadScreen
)

// Silent is a turn that ended without a report, once its grace has passed.
type Silent struct {
	Kind SilentKind
	Name string
	Text string
}

// optMs is Rust's Option<Millis>.
type optMs struct {
	set bool
	v   Millis
}

func someMs(v Millis) optMs         { return optMs{true, v} }
func (o optMs) due(now Millis) bool { return o.set && now >= o.v }

// Worker is one live worker.
type Worker struct {
	Plan WorkerPlan
	// Worktree is set ("" until then) once the pane is up.
	Worktree string
	// StopRequested: stop as soon as it is up (or now): its job ended, or stop_worker / cancel was called.
	StopRequested bool
	// ScreenPending: Codex: the no-report grace passed and its screen was asked for; the reply is still due.
	ScreenPending bool

	queue []string
	// ready: idle or done, and nothing sent since.
	ready bool
	// inTurn: a prompt was sent and its turn hasn't ended.
	inTurn bool
	// worked: Working was seen during the current turn (Codex: its end is the next Idle/Done).
	worked bool
	// reported: a result, question or block arrived during the current turn.
	reported     bool
	graceAt      optMs
	lastSaid     *string
	failures     uint32
	retryAt      optMs
	blockedSince optMs
	hinted       bool
	// resumable: a turn ended and nothing has resumed it: Working/Blocked or a Stop re-opens it.
	resumable bool
	// awaitingStop: Claude: a turn is in flight and its Stop hook hasn't arrived. An Idle can
	// beat that Stop, so it is not ready (and a Stop is only believed) until then.
	awaitingStop bool
	// idleWaitAt: Idle seen while awaitingStop: ready anyway at this time if the Stop is lost.
	idleWaitAt optMs
	// delivering is the last prompt sent, until the worker shows it got it (Working, Blocked, a
	// Stop or a report). A start-up screen can swallow a prompt and leave the pane idle.
	delivering *string
	// deliveryAt: when its delivery is checked (WorkerDeliveryMs after the send or the last nudge).
	deliveryAt optMs
	// nudges so far for this prompt (MaxDeliveryNudges).
	nudges uint32
	// deliveryRead: its screen was asked for to see why; the reply is still due.
	deliveryRead bool
	// heldIdle: Codex: an Idle/Done that came while its prompt was not yet shown to have
	// arrived. It proves nothing (a trust screen that took the prompt's Enter shows the same),
	// so it is held back. It stands once a report arrives or a screen shows the worker not busy;
	// a screen that shows it busy keeps it held (re-read every WorkerDeliveryMs, see
	// busyCheckAt), and the next Idle/Done from Herdr replaces it.
	heldIdle bool
	// busyCheckAt: Codex, busy with a held Idle/Done: when its screen is read again.
	busyCheckAt optMs
	// busyRead: that screen was asked for; the reply is still due.
	busyRead bool
	// startWaitAt: when a worker still not ready since it came up is treated as idle.
	startWaitAt optMs
}

func newWorker(plan WorkerPlan) *Worker { return &Worker{Plan: plan} }

// delivered: the worker showed it got its last prompt (or it no longer matters).
func (w *Worker) delivered() {
	w.delivering = nil
	w.deliveryAt = optMs{}
	w.nudges = 0
	w.deliveryRead = false
	w.unhold()
}

// unhold: no Idle/Done is held back any more (it stood, or a newer status replaced it).
func (w *Worker) unhold() {
	w.heldIdle = false
	w.busyCheckAt = optMs{}
	w.busyRead = false
}

func (w *Worker) IsUp() bool      { return w.Worktree != "" }
func (w *Worker) IsBlocked() bool { return w.blockedSince.set }

// Workers is the set of live workers, keyed by name (iterated in name order, as Rust's BTreeMap).
type Workers struct {
	live map[string]*Worker
	// notes are diagnostic lines (worker names only, never text) for the caller to log.
	notes []string
}

func NewWorkers() *Workers { return &Workers{live: map[string]*Worker{}} }

// sorted returns the live workers in name order.
func (ws *Workers) sorted() []*Worker {
	keys := make([]string, 0, len(ws.live))
	for k := range ws.live {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*Worker, len(keys))
	for i, k := range keys {
		out[i] = ws.live[k]
	}
	return out
}

func (ws *Workers) note(format string, args ...any) {
	ws.notes = append(ws.notes, fmt.Sprintf(format, args...))
}

// TakeNotes returns the diagnostic lines since the last call.
func (ws *Workers) TakeNotes() []string {
	out := ws.notes
	ws.notes = nil
	return out
}

// Get returns the worker named name, or nil.
func (ws *Workers) Get(name string) *Worker { return ws.live[name] }

// GetMut is Get (Go has no separate mutable borrow); kept for parity with the Rust API.
func (ws *Workers) GetMut(name string) *Worker { return ws.live[name] }

func (ws *Workers) Len() int      { return len(ws.live) }
func (ws *Workers) IsEmpty() bool { return len(ws.live) == 0 }

// Names in sorted order.
func (ws *Workers) Names() []string {
	var out []string
	for _, w := range ws.sorted() {
		out = append(out, w.Plan.Name)
	}
	return out
}

// ForJob returns the live workers of job.
func (ws *Workers) ForJob(job JobID) []string {
	var out []string
	for _, w := range ws.sorted() {
		if w.Plan.Job == job {
			out = append(out, w.Plan.Name)
		}
	}
	return out
}

// Admit adds a new, starting worker. A name that is already live is refused (false).
func (ws *Workers) Admit(plan WorkerPlan) bool {
	if _, ok := ws.live[plan.Name]; ok {
		return false
	}
	ws.live[plan.Name] = newWorker(plan)
	return true
}

// Up: its pane is up in worktree, in status; brief goes first in its queue.
func (ws *Workers) Up(name, worktree string, status PaneStatus, brief string, now Millis) {
	w := ws.live[name]
	if w == nil || w.IsUp() {
		return
	}
	w.Worktree = worktree
	w.queue = append([]string{brief}, w.queue...)
	w.startWaitAt = someMs(now + WorkerStartReadyMs)
	statusOf(w, status, now)
}

func (ws *Workers) Remove(name string) (*Worker, bool) {
	w, ok := ws.live[name]
	delete(ws.live, name)
	return w, ok
}

// Push queues a message; it goes out with TakeSend.
func (ws *Workers) Push(name, text string) {
	if w := ws.live[name]; w != nil {
		w.queue = append(w.queue, text)
	}
}

// TakeSend returns everything queued, as one prompt, if the worker is up, idle and not being
// stopped. A new turn starts: its report flag, grace and screen state reset. Its delivery is
// checked WorkerDeliveryMs from now (see DueDeliveries).
func (ws *Workers) TakeSend(name string, now Millis) (string, bool) {
	w := ws.live[name]
	if w == nil || !w.IsUp() || !w.ready || w.StopRequested || len(w.queue) == 0 {
		return "", false
	}
	w.ready = false
	w.inTurn = true
	w.awaitingStop = w.Plan.Kind == Claude
	w.idleWaitAt = optMs{}
	w.ScreenPending = false
	w.resumable = false
	w.worked = false
	w.reported = false
	w.graceAt = optMs{}
	w.lastSaid = nil
	w.startWaitAt = optMs{}
	w.retryAt = optMs{}
	text := strings.Join(w.queue, "\n\n")
	w.queue = nil
	w.delivered()
	w.delivering = &text
	w.deliveryAt = someMs(now + WorkerDeliveryMs)
	return text, true
}

// SendFailed: the prompt could not be typed: it goes back in front and is tried again after
// WorkerRetryMs (doubling). Returns true when it has failed too often (the queue is dropped).
func (ws *Workers) SendFailed(name, text string, now Millis) bool {
	w := ws.live[name]
	if w == nil {
		return false
	}
	w.inTurn = false
	w.awaitingStop = false
	w.idleWaitAt = optMs{}
	// Not typed: the retry is a new send, checked on its own.
	w.delivered()
	w.failures++
	if w.failures >= MaxWorkerPromptFailures {
		w.failures = 0
		w.queue = nil
		w.retryAt = optMs{}
		w.ready = true
		return true
	}
	w.queue = append([]string{text}, w.queue...)
	w.retryAt = someMs(now + (WorkerRetryMs << (w.failures - 1)))
	return false
}

// OnStatus takes a status event for the worker's pane.
func (ws *Workers) OnStatus(name string, status PaneStatus, now Millis) {
	if w := ws.live[name]; w != nil {
		statusOf(w, status, now)
	}
}

func statusOf(w *Worker, status PaneStatus, now Millis) {
	switch {
	case status == PaneWorking || status == PaneBlocked:
		w.ready = false
		w.retryAt = optMs{}
		w.delivered()
		// An Idle flicker mid-turn: the turn goes on.
		w.idleWaitAt = optMs{}
		if w.resumable {
			reopen(w)
		}
		if w.inTurn {
			w.worked = true
			w.failures = 0
		}
		// Busy again: the turn did not end after all.
		w.graceAt = optMs{}
		if status == PaneBlocked {
			if !w.blockedSince.set {
				w.blockedSince = someMs(now)
			}
		} else {
			w.blockedSince = optMs{}
			w.hinted = false
		}
	case (status == PaneIdle || status == PaneDone) && w.Plan.Kind == Codex && w.delivering != nil:
		// Codex right after a send: neither ready nor the end of a turn (see heldIdle).
		w.heldIdle = true
		w.blockedSince = optMs{}
		w.hinted = false
	case status == PaneIdle || status == PaneDone:
		// A held Idle/Done is replaced by this one.
		w.unhold()
		if w.awaitingStop {
			// Herdr's Idle can beat the Stop hook of the same turn: wait for the Stop, or for a
			// while if it is lost.
			if !w.idleWaitAt.set {
				w.idleWaitAt = someMs(now + WorkerIdleWithoutStopMs)
			}
		} else {
			w.ready = true
		}
		w.blockedSince = optMs{}
		w.hinted = false
		// Codex has no Stop hook: a turn that was seen working ends at Idle/Done.
		if w.Plan.Kind == Codex && w.inTurn && w.worked {
			turnEnded(w, nil, now)
		}
	}
}

// OnStop is a Claude worker's Stop hook: its turn ended with text.
func (ws *Workers) OnStop(name, text string, now Millis) {
	w := ws.live[name]
	if w == nil {
		return
	}
	// A Stop with no turn awaiting one is late or a duplicate (its turn was already ended or
	// replaced): it must not end the current turn or arm a grace with old words.
	if !w.awaitingStop {
		ws.note("worker %s: ignored a stale Stop", name)
		return
	}
	w.awaitingStop = false
	w.idleWaitAt = optMs{}
	w.ready = true
	w.delivered()
	if w.inTurn {
		w.failures = 0
		turnEnded(w, &text, now)
	}
}

// arrived: a Codex prompt shown to have arrived by a report or a screen that shows the worker
// not busy: the turn is on (so its end is forwarded), and an Idle/Done held back meanwhile
// stands now (a report ends the turn anyway).
func arrived(w *Worker, now Millis) {
	held := w.heldIdle
	w.delivered()
	if w.inTurn {
		w.worked = true
	}
	if held {
		statusOf(w, PaneIdle, now)
	}
}

// arrivedBusy: a Codex prompt shown to have arrived by a screen that shows the worker busy: an
// Idle/Done held back meanwhile stays held (it is mid-turn: not ready, no grace), and the
// screen is read again in WorkerDeliveryMs (not a nudge).
func arrivedBusy(w *Worker, now Millis) {
	held := w.heldIdle
	w.delivered()
	if w.inTurn {
		w.worked = true
	}
	if held {
		w.heldIdle = true
		w.busyCheckAt = someMs(now + WorkerDeliveryMs)
	}
}

// reopen: the worker went on after its turn ended: that is a new turn.
func reopen(w *Worker) {
	w.resumable = false
	w.inTurn = true
	w.ScreenPending = false
	w.awaitingStop = w.Plan.Kind == Claude
	w.worked = false
	w.reported = false
}

func turnEnded(w *Worker, said *string, now Millis) {
	w.inTurn = false
	w.worked = false
	w.resumable = true
	if !w.reported {
		w.graceAt = someMs(now + ReportGraceMs)
		w.lastSaid = said
	}
}

// TakeScreenPending: a Codex screen read we asked for has come back: true once, and only if
// the worker is not being stopped.
func (ws *Workers) TakeScreenPending(name string) bool {
	w := ws.live[name]
	if w == nil {
		return false
	}
	pending := w.ScreenPending
	w.ScreenPending = false
	return pending && !w.StopRequested
}

// OnReport: a result, question or block: this turn needs no forwarding.
func (ws *Workers) OnReport(name string, now Millis) {
	if w := ws.live[name]; w != nil {
		w.reported = true
		w.graceAt = optMs{}
		w.lastSaid = nil
		arrived(w, now)
	}
}

// DueDeliveries: prompts with no sign of having arrived WorkerDeliveryMs after they were sent
// (or last nudged): their screen is to be read, or, nudged MaxDeliveryNudges times already,
// the worker is given up on. Each once.
func (ws *Workers) DueDeliveries(now Millis) []WorkerDelivery {
	var out []WorkerDelivery
	for _, w := range ws.sorted() {
		// A busy worker with a held Idle/Done: is it still busy?
		if w.busyCheckAt.due(now) && !w.busyRead && !w.StopRequested {
			w.busyCheckAt = optMs{}
			w.busyRead = true
			out = append(out, WorkerDelivery{DeliveryRead, w.Plan.Name})
			continue
		}
		if !w.deliveryAt.due(now) || w.deliveryRead || w.StopRequested {
			continue
		}
		w.deliveryAt = optMs{}
		name := w.Plan.Name
		if w.nudges >= MaxDeliveryNudges {
			w.delivered()
			ws.note("worker %s: never started on its prompt", name)
			out = append(out, WorkerDelivery{DeliveryGaveUp, name})
		} else {
			w.deliveryRead = true
			ws.note("worker %s: no sign of its prompt; reading its screen", name)
			out = append(out, WorkerDelivery{DeliveryRead, name})
		}
	}
	return out
}

// OnDeliveryScreen takes the screen a DeliveryRead asked for: what to do about the prompt
// (false if no such read was due). Every answer counts as a nudge and re-arms the check.
func (ws *Workers) OnDeliveryScreen(name, screen string, now Millis) (Nudge, bool) {
	w := ws.live[name]
	if w == nil {
		return Nudge{}, false
	}
	if w.busyRead {
		w.busyRead = false
		if w.StopRequested || !w.heldIdle {
			return Nudge{}, false
		}
		if InputStateOf(w.Plan.Kind, screen, "") == InputBusy {
			w.busyCheckAt = someMs(now + WorkerDeliveryMs)
			return Nudge{Kind: NudgeBusy}, true
		}
		// No longer busy: the held Idle/Done stands (ready; the turn ended).
		arrived(w, now)
		ws.note("worker %s: no longer busy; its held idle stands", name)
		return Nudge{Kind: NudgeArrived}, true
	}
	if !w.deliveryRead {
		return Nudge{}, false
	}
	w.deliveryRead = false
	if w.delivering == nil {
		return Nudge{}, false
	}
	text := *w.delivering
	if w.StopRequested {
		w.delivered()
		return Nudge{}, false
	}
	state := InputStateOf(w.Plan.Kind, screen, text)
	if state == InputBusy {
		arrivedBusy(w, now)
		ws.note("worker %s: busy; its prompt arrived", name)
		return Nudge{Kind: NudgeBusy}, true
	}
	// Codex's input no longer shows its placeholder (nor the prompt or a trust screen).
	if state == InputUnknown && w.Plan.Kind == Codex {
		arrived(w, now)
		ws.note("worker %s: no empty input box; its prompt arrived", name)
		return Nudge{Kind: NudgeArrived}, true
	}
	w.nudges++
	w.deliveryAt = someMs(now + WorkerDeliveryMs)
	var nudge Nudge
	kind := "wait"
	switch state {
	case InputEmpty:
		nudge, kind = Nudge{Kind: NudgeResend, Text: text}, "re-sent"
	case InputTyped:
		nudge, kind = Nudge{Kind: NudgeEnter}, "Enter"
	default:
		nudge = Nudge{Kind: NudgeWait}
	}
	ws.note("worker %s: prompt nudge %d (%s)", name, w.nudges, kind)
	return nudge, true
}

// DueSilent returns turns whose grace has passed with no report (each once).
func (ws *Workers) DueSilent(now Millis) []Silent {
	var out []Silent
	for _, w := range ws.sorted() {
		if !w.graceAt.due(now) || w.StopRequested {
			continue
		}
		w.graceAt = optMs{}
		name := w.Plan.Name
		// A real Stop's words go out as they are; a turn ended by the idle fallback has none,
		// so (like Codex) its screen is read.
		if w.Plan.Kind == Claude && w.lastSaid != nil {
			text := *w.lastSaid
			w.lastSaid = nil
			out = append(out, Silent{Kind: SilentSaid, Name: name, Text: text})
		} else {
			w.ScreenPending = true
			out = append(out, Silent{Kind: SilentReadScreen, Name: name})
		}
	}
	return out
}

// DueRetries returns workers whose failed send is due again: marked ready (the caller sends).
func (ws *Workers) DueRetries(now Millis) []string {
	var out []string
	for _, w := range ws.sorted() {
		// A stale start status: no Idle came after the start.
		if w.startWaitAt.due(now) {
			w.startWaitAt = optMs{}
			if !w.ready && !w.inTurn && !w.StopRequested && !w.IsBlocked() && len(w.queue) > 0 {
				ws.note("worker %s: not idle %d s after its start; treated as idle", w.Plan.Name, WorkerStartReadyMs/1000)
				w.ready = true
				out = append(out, w.Plan.Name)
			}
		}
		// A lost Stop: the Idle stands. The turn ended without words (a no-report grace starts).
		if w.idleWaitAt.due(now) {
			w.idleWaitAt = optMs{}
			w.awaitingStop = false
			ws.note("worker %s: idle without a Stop, ready after the wait", w.Plan.Name)
			if !w.StopRequested && !w.IsBlocked() {
				w.ready = true
				if w.inTurn {
					turnEnded(w, nil, now)
				}
				out = append(out, w.Plan.Name)
			}
		}
		if w.retryAt.due(now) {
			w.retryAt = optMs{}
			if w.StopRequested || w.IsBlocked() {
				continue
			}
			w.ready = true
			out = append(out, w.Plan.Name)
		}
	}
	return out
}

// DueHints returns workers blocked for WorkerBlockedGraceMs with no pending permission, each
// once per block.
func (ws *Workers) DueHints(now Millis, hasPending func(string) bool) []string {
	var out []string
	for _, w := range ws.sorted() {
		long := w.blockedSince.set && satSub(now, w.blockedSince.v) >= WorkerBlockedGraceMs
		if long && !w.hinted && !w.StopRequested && !hasPending(w.Plan.Name) {
			w.hinted = true
			out = append(out, w.Plan.Name)
		}
	}
	return out
}
