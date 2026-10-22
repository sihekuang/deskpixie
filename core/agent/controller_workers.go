package agent

import (
	"errors"
	"fmt"
	"slices"
)

// The controller's worker rules (port of controller/worker_steps.rs, spec §5.5, §6): the pixie
// MCP tools, worker turns and reports, and stopping a job's workers when it ends.

// admitWorker is start_worker after the runtime resolved the repo: it checks the policy and
// the job, names the worker, and records it as starting (it counts toward workers.max from now on).
func (c *AgentController) admitWorker(req WorkerRequest, repo string, taken []string, now Millis, fx *[]Effect) {
	plan, err := c.planWorker(req, repo, taken)
	if err != nil {
		*fx = append(*fx, Effect{Kind: EfAdmitted, Err: err.Error()})
		return
	}
	if !c.workers.Admit(plan) {
		*fx = append(*fx, Effect{Kind: EfAdmitted, Err: fmt.Sprintf("A worker named %s is already running.", plan.Name)})
		return
	}
	row := WorkerRow{Name: plan.Name, Kind: plan.Kind, Repo: plan.RepoName, JobID: plan.Job, Status: WorkerStarting}
	if c.jobs.AttachWorker(row, now) == nil {
		if j, ok := c.jobs.Get(plan.Job); ok && j.Status == JobDelegated {
			_, _ = c.jobs.Transition(plan.Job, JobWorking, now)
		}
		c.saveJob(plan.Job, fx)
	}
	*fx = append(*fx, logEffect(fmt.Sprintf("worker %s admitted", plan.Name)), Effect{Kind: EfAdmitted, Plan: plan})
}

func (c *AgentController) planWorker(req WorkerRequest, repo string, taken []string) (WorkerPlan, error) {
	if c.shutDown {
		return WorkerPlan{}, errors.New("The app is quitting.")
	}
	if c.isDisabled() {
		return WorkerPlan{}, errors.New("Delegation is off.")
	}
	policy := c.settings.Workers
	if !policy.Enabled {
		return WorkerPlan{}, errors.New("Workers are turned off (agent.workers.enabled).")
	}
	if !slices.Contains(policy.Kinds, req.Kind) {
		return WorkerPlan{}, fmt.Errorf("%s workers are not allowed (agent.workers.kinds).", req.Kind)
	}
	job, ok := ParseJobID(req.JobID)
	if !ok || !c.jobs.IsOpen(job) {
		return WorkerPlan{}, fmt.Errorf("No open job %s.", req.JobID)
	}
	if c.workers.Len() >= policy.Max {
		return WorkerPlan{}, fmt.Errorf("%d workers are already running (agent.workers.max); stop one first.", policy.Max)
	}
	// The folder's name is user data and ends up in the worker's brief and in spoken lines:
	// only its cleaned form (lowercase letters, digits, `-`) goes any further.
	dir, _ := pathFileName(repo)
	repoName := RepoSlug(dir)
	all := append(slices.Clone(taken), c.workers.Names()...)
	// A model-supplied name hint is slugified by WorkerName, which checks the result with IsWorkerName.
	name, slug, ok := WorkerName(repoName, req.Name, req.Task, all)
	if !ok {
		return WorkerPlan{}, errors.New("Could not name the worker; give it another name.")
	}
	return WorkerPlan{Name: name, Slug: slug, Kind: req.Kind, Repo: repo, RepoName: repoName, Job: job, Task: req.Task}, nil
}

// workerUp: the worker's pane is up: its brief goes out once it is idle (or it is stopped at
// once if its job ended while it was starting).
func (c *AgentController) workerUp(name, worktree string, status PaneStatus, now Millis, fx *[]Effect) {
	w := c.workers.Get(name)
	if w == nil {
		// Not admitted (or already forgotten): nothing should run under that name.
		*fx = append(*fx, Effect{Kind: EfStopWorker, Name: name})
		return
	}
	stop, plan := w.StopRequested || c.shutDown, w.Plan
	brief := WorkerBrief(plan.RepoName, plan.Slug, plan.Task)
	c.workers.Up(name, worktree, status, brief, now)
	row := WorkerRunning
	if status == PaneBlocked {
		row = WorkerBlocked
	}
	c.setRow(name, row, now, fx)
	// After quit nothing is briefed; the pane is closed unless keepPanesOnQuit.
	if stop && !(c.shutDown && c.settings.KeepPanesOnQuit) {
		*fx = append(*fx, Effect{Kind: EfStopWorker, Name: name})
	} else if !stop {
		c.flushWorker(name, now, fx)
	}
}

// WorkerRestartNote goes to a worker kept over the quit in place of its brief: a report it made
// while the app was down was rejected.
const WorkerRestartNote = "The app restarted while you worked. If you already finished, call report_result again with your result and evidence; otherwise carry on."

// adoptWorker: a worker kept running over the quit whose pane is still there (agent.keepPanesOnQuit)
// is live again, as if it had just come up, with the restart note in place of its brief. One
// whose job is no longer open is stopped.
func (c *AgentController) adoptWorker(plan WorkerPlan, worktree string, status PaneStatus, now Millis, fx *[]Effect) {
	refuse := func(why string) { *fx = append(*fx, Effect{Kind: EfAdmitted, Err: why}) }
	if c.workers.Get(plan.Name) != nil {
		refuse(fmt.Sprintf("A worker named %s is already running.", plan.Name))
		return
	}
	j, known := c.jobs.Get(plan.Job)
	// Kept open after its job finished (#237): adopted as it is, with nothing to report again.
	// A cancelled or forgotten job's worker is closed.
	// One the restart itself failed was mid-task: closed too.
	finished := known && !j.Status.IsOpen() && j.Status != JobCancelled && !(j.Reason != nil && *j.Reason == RestartReason)
	if c.shutDown || !known || !j.Status.IsOpen() && !finished {
		*fx = append(*fx, Effect{Kind: EfStopWorker, Name: plan.Name})
		refuse(fmt.Sprintf("No open job %s.", plan.Job))
		return
	}
	c.workers.Admit(plan)
	if finished {
		c.workers.UpQuiet(plan.Name, worktree, status, now)
	} else {
		c.workers.Up(plan.Name, worktree, status, WorkerRestartNote, now)
	}
	row := WorkerRunning
	if status == PaneBlocked {
		row = WorkerBlocked
	}
	c.setRow(plan.Name, row, now, fx)
	if j, ok := c.jobs.Get(plan.Job); ok && j.Status == JobDelegated {
		if _, err := c.jobs.Transition(plan.Job, JobWorking, now); err == nil {
			c.saveJob(plan.Job, fx)
		}
	}
	*fx = append(*fx, logEffect(fmt.Sprintf("worker %s adopted", plan.Name)), Effect{Kind: EfAdmitted, Plan: plan})
	c.flushWorker(plan.Name, now, fx)
}

// LiveWorker is a live worker's plan and worktree (what a quit keeps).
type LiveWorker struct {
	Plan     WorkerPlan
	Worktree string
}

// LiveWorkers are the workers that are up and not being stopped, in name order.
func (c *AgentController) LiveWorkers() []LiveWorker {
	var out []LiveWorker
	for _, name := range c.workers.Names() {
		if w := c.workers.Get(name); w != nil && w.IsUp() && !w.StopRequested {
			out = append(out, LiveWorker{Plan: w.Plan, Worktree: w.Worktree})
		}
	}
	return out
}

// workerFailed: the reason goes to the coordinator as start_worker's error; it is never logged
// (free text).
func (c *AgentController) workerFailed(name, _ string, now Millis, fx *[]Effect) {
	if _, ok := c.workers.Remove(name); !ok {
		return
	}
	*fx = append(*fx, logEffect(fmt.Sprintf("worker %s did not start", name)))
	c.setRow(name, WorkerFailed, now, fx)
	*fx = append(*fx, Effect{Kind: EfForgetWorker, Name: name})
	c.clearMode(name, fx)
	c.denyConfirmsOf(name, "The worker stopped.", now, fx)
}

// workerNeverStarted: a worker whose prompt never arrived, after its nudges: the coordinator is
// told, its pane is closed, and it fails like a start that failed (its slot is freed at once).
func (c *AgentController) workerNeverStarted(name string, now Millis, fx *[]Effect) {
	w := c.workers.Get(name)
	if w == nil {
		return
	}
	c.tellCoordinator(w.Plan.Job, NeverStartedLine(name), now, fx)
	// Before ForgetWorker, which the runtime runs after the close.
	*fx = append(*fx, Effect{Kind: EfStopWorker, Name: name})
	c.workerFailed(name, "The worker never started on its task.", now, fx)
}

// denyConfirmsOf denies the permissions a gone worker still has pending (and resumes their jobs).
func (c *AgentController) denyConfirmsOf(name, why string, now Millis, fx *[]Effect) {
	for _, p := range slices.Clone(c.confirms.Pending()) {
		if p.Agent == name {
			c.confirms.Remove(p.ID)
			*fx = append(*fx, decideEffect(p.ID, Deny(why)))
			c.resumeJob(p.Job, now, fx)
		}
	}
}

// workerGone: the worker's pane closed (stopped by us, or it exited). A worker that is still
// starting is left to its start, which reports InWorkerUp or InWorkerFailed.
func (c *AgentController) workerGone(name string, now Millis, fx *[]Effect) {
	if w := c.workers.Get(name); w == nil || !w.IsUp() {
		return
	}
	w, _ := c.workers.Remove(name)
	c.setRow(name, WorkerStopped, now, fx)
	*fx = append(*fx, Effect{Kind: EfForgetWorker, Name: name})
	c.clearMode(name, fx)
	c.denyConfirmsOf(name, "The worker stopped.", now, fx)
	if !w.StopRequested {
		c.tellCoordinator(w.Plan.Job, ExitedLine(name), now, fx)
	}
}

// onMcp answers a pixie MCP tool call (all but start_worker, which the runtime runs).
func (c *AgentController) onMcp(agent string, call McpCall, now Millis, fx *[]Effect) {
	coordinator := agent == Coordinator
	var value any
	var err error
	switch {
	case call.Kind == CallMessageWorker && coordinator:
		value, err = c.messageWorker(call.Name, call.Text, now, fx)
	case call.Kind == CallStopWorker && coordinator:
		value, err = c.stopWorker(call.Name, fx)
	case call.Kind == CallListWorkers && coordinator:
		rows := c.jobs.WorkerRows()
		if rows == nil {
			rows = []WorkerRow{}
		}
		value = map[string]any{"workers": rows}
	case call.Kind == CallReportProgress && !coordinator:
		value, err = c.reportProgress(agent, call.Summary, now, fx)
	case call.Kind == CallReportResult && !coordinator:
		if err = c.report(agent, ResultLine(agent, call.Outcome, call.Summary, call.Evidence), now, fx); err == nil {
			c.sayForWorker(agent, func(who string) string { return WorkerResultSpoken(who, call.Outcome, call.Summary) }, now, fx)
			value = map[string]any{"ok": true}
		}
	case call.Kind == CallAskCoordinator && !coordinator:
		if err = c.report(agent, WorkerQuestionLine(agent, call.Question, call.Options), now, fx); err == nil {
			value = map[string]any{"ok": true, "next": "End your turn now. The answer arrives as a message."}
		}
	case call.Kind == CallReportBlocked && !coordinator:
		if err = c.report(agent, BlockedLine(agent, call.Reason), now, fx); err == nil {
			c.sayForWorker(agent, func(who string) string { return WorkerBlockedSpoken(who, call.Reason) }, now, fx)
			value = map[string]any{"ok": true}
		}
	case call.Kind == CallStartWorker && coordinator:
		err = errors.New("start_worker is handled by the runtime.")
	default:
		err = errors.New("This tool is not available to you.")
	}
	if err != nil {
		*fx = append(*fx, Effect{Kind: EfToolResult, Err: err.Error()})
		return
	}
	*fx = append(*fx, Effect{Kind: EfToolResult, Value: value})
}

func (c *AgentController) messageWorker(name, text string, now Millis, fx *[]Effect) (any, error) {
	w := c.workers.GetMut(name)
	if w == nil || w.StopRequested {
		return nil, fmt.Errorf("No running worker %s.", name)
	}
	// A worker kept open after its job ended, given more work in the request now in flight,
	// works for that request: its reports and permission requests count again (#237).
	// With no single such request its replies would be refused, so nothing is sent.
	if !c.jobs.IsOpen(w.Plan.Job) {
		refs := unique(c.inbox.InFlightRefs())
		if len(refs) != 1 || !c.jobs.MoveWorker(name, refs[0], now) {
			return nil, fmt.Errorf("%s's request has ended, and no single request is in progress for it to join, so its replies would be lost.", name)
		}
		old := w.Plan.Job
		w.Plan.Job = refs[0]
		c.saveJob(old, fx)
		c.saveJob(refs[0], fx)
	}
	c.workers.Push(name, CoordinatorSays(text))
	status := "queued"
	if c.flushWorker(name, now, fx) {
		status = "sent"
	}
	return map[string]any{"name": name, "status": status}, nil
}

func (c *AgentController) stopWorker(name string, fx *[]Effect) (any, error) {
	w := c.workers.GetMut(name)
	if w == nil {
		return nil, fmt.Errorf("No running worker %s.", name)
	}
	if !w.StopRequested {
		w.StopRequested = true
		if w.IsUp() {
			*fx = append(*fx, Effect{Kind: EfStopWorker, Name: name})
		}
	}
	return map[string]any{"name": name, "status": "stopping"}, nil
}

// workerJob is the caller's job, if it is a live worker on an open job.
func (c *AgentController) workerJob(agent string) (JobID, error) {
	w := c.workers.Get(agent)
	if w == nil {
		return "", errors.New("You are not a running worker.")
	}
	if !c.jobs.IsOpen(w.Plan.Job) {
		return "", errors.New("Your job has ended; stop working.")
	}
	return w.Plan.Job, nil
}

// reportProgress: silent for the coordinator; the job's progress line, narrated like the
// coordinator's tool use (open conversation, once per 30 s per job).
func (c *AgentController) reportProgress(agent, summary string, now Millis, fx *[]Effect) (any, error) {
	job, err := c.workerJob(agent)
	if err != nil {
		return nil, err
	}
	phrase := maskPendingTokens(summary, c.pendingTokens())
	c.jobs.SetProgress(job, phrase, now)
	c.saveJob(job, fx)
	c.holdProgress(job, phrase, now)
	return map[string]any{"ok": true}, nil
}

// report: a result, question or block: queued for the coordinator with the job's ref.
func (c *AgentController) report(agent, line string, now Millis, fx *[]Effect) error {
	job, err := c.workerJob(agent)
	if err != nil {
		return err
	}
	c.workers.OnReport(agent, now)
	c.tellCoordinator(job, line, now, fx)
	// A held Codex Idle/Done may have stood: queued messages can go now.
	c.flushWorker(agent, now, fx)
	return nil
}

// sayForWorker tells the user a worker's own report, by the same route as the coordinator's
// replies (spoken in an open conversation, else a notification and the outbox), debounced:
// held per job until it is quiet, a repeat dropped, superseded by the coordinator's reply
// (Updates), and under the per-minute cap.
// Only with agent.workers.speakResults on (off: just as before, the coordinator alone hears it).
// The coordinator still gets the report and gives its own, checked reply. Called after report
// accepted it, so the job is open; a worker being stopped (its job ended or the coordinator
// stopped it) is not spoken for: the coordinator has already moved on.
func (c *AgentController) sayForWorker(agent string, line func(who string) string, now Millis, fx *[]Effect) {
	if !c.settings.SpeakWorkerReports {
		return
	}
	w := c.workers.Get(agent)
	if w == nil || w.StopRequested || c.shutDown {
		return
	}
	text := maskPendingTokens(line(WorkerWho(w.Plan)), c.pendingTokens())
	if text != "" {
		c.updates.AddReport(w.Plan.Job, agent, text, now)
	}
}

func (c *AgentController) onWorkerStatus(name string, status PaneStatus, now Millis, fx *[]Effect) {
	if c.workers.Get(name) == nil {
		return
	}
	c.workers.OnStatus(name, status, now)
	if w := c.workers.Get(name); w != nil && w.IsUp() {
		switch status {
		case PaneBlocked:
			c.setRow(name, WorkerBlocked, now, fx)
		case PaneUnknown:
		default:
			c.setRow(name, WorkerRunning, now, fx)
		}
	}
	c.flushWorker(name, now, fx)
}

func (c *AgentController) onWorkerStop(name, message string, now Millis, fx *[]Effect) {
	if c.workers.Get(name) == nil {
		return
	}
	c.workers.OnStop(name, message, now)
	c.logWorkerNotes(fx)
	c.flushWorker(name, now, fx)
}

// onWorkerScreen: a screen asked for by a delivery check, or a Codex turn without a report
// (the end of its screen goes to the coordinator).
func (c *AgentController) onWorkerScreen(name, text string, now Millis, fx *[]Effect) {
	// A prompt with no sign of having arrived: re-send it, submit it, or wait.
	if nudge, ok := c.workers.OnDeliveryScreen(name, text, now); ok {
		c.logWorkerNotes(fx)
		switch nudge.Kind {
		case NudgeResend:
			*fx = append(*fx, Effect{Kind: EfPromptWorker, Name: name, Text: nudge.Text})
		case NudgeEnter:
			*fx = append(*fx, Effect{Kind: EfSubmitWorker, Name: name})
		case NudgeArrived:
			// A held Idle/Done may have stood: queued messages can go now.
			c.flushWorker(name, now, fx)
		}
		return
	}
	// Only a read we asked for, of a worker that is not being stopped.
	if !c.workers.TakeScreenPending(name) {
		return
	}
	w := c.workers.Get(name)
	if w == nil {
		return
	}
	chars := []rune(text)
	tail := string(chars[max(0, len(chars)-MaxSilentTurnChars):])
	c.tellCoordinator(w.Plan.Job, SilentTurnLine(name, tail), now, fx)
}

func (c *AgentController) workerSendFailed(name, text string, now Millis, fx *[]Effect) {
	*fx = append(*fx, logEffect(fmt.Sprintf("prompt to worker %s failed", name)))
	w := c.workers.Get(name)
	if w == nil {
		return
	}
	job := w.Plan.Job
	if c.workers.SendFailed(name, text, now) {
		c.tellCoordinator(job, UnreachableLine(name), now, fx)
	}
}

// stopJobWorkers stops every live worker of job (the user cancelled it): at once if its pane is
// up, else when it is. A job that ends otherwise keeps its workers until the coordinator stops
// them (#237).
func (c *AgentController) stopJobWorkers(job JobID, fx *[]Effect) {
	for _, name := range c.workers.ForJob(job) {
		if w := c.workers.GetMut(name); w != nil && !w.StopRequested {
			w.StopRequested = true
			if w.IsUp() {
				*fx = append(*fx, Effect{Kind: EfStopWorker, Name: name})
			}
		}
	}
}

func (c *AgentController) hasLiveWorkers(job JobID) bool {
	for _, n := range c.workers.ForJob(job) {
		if w := c.workers.Get(n); w != nil && !w.StopRequested && w.Busy() {
			return true
		}
	}
	return false
}

// shutdownWorkers: app quit: every worker pane is stopped (unless keepPanesOnQuit) and every
// worker's token and files are dropped. A worker still starting is closed by its start.
func (c *AgentController) shutdownWorkers(fx *[]Effect) {
	for _, name := range c.workers.Names() {
		if w := c.workers.Get(name); !c.settings.KeepPanesOnQuit && w != nil && w.IsUp() {
			*fx = append(*fx, Effect{Kind: EfStopWorker, Name: name})
		}
		*fx = append(*fx, Effect{Kind: EfForgetWorker, Name: name})
	}
}

func (c *AgentController) logWorkerNotes(fx *[]Effect) {
	for _, n := range c.workers.TakeNotes() {
		*fx = append(*fx, logEffect(n))
	}
}

func (c *AgentController) tickWorkers(now Millis, fx *[]Effect) {
	for _, s := range c.workers.DueSilent(now) {
		switch s.Kind {
		case SilentSaid:
			if w := c.workers.Get(s.Name); w != nil {
				c.tellCoordinator(w.Plan.Job, SilentTurnLine(s.Name, s.Text), now, fx)
			}
		case SilentReadScreen:
			*fx = append(*fx, Effect{Kind: EfReadWorker, Name: s.Name})
		}
	}
	for _, name := range c.workers.DueRetries(now) {
		c.flushWorker(name, now, fx)
	}
	for _, d := range c.workers.DueDeliveries(now) {
		switch d.Kind {
		case DeliveryRead:
			*fx = append(*fx, Effect{Kind: EfReadWorker, Name: d.Name})
		case DeliveryGaveUp:
			c.workerNeverStarted(d.Name, now, fx)
		}
	}
	c.logWorkerNotes(fx)
	for _, name := range c.workers.DueHints(now, c.confirms.HasPendingFor) {
		w := c.workers.Get(name)
		if w == nil {
			continue
		}
		text := fmt.Sprintf("A worker in %s is waiting at a prompt in its terminal; I'm bringing it up for you.", w.Plan.RepoName)
		if w.Plan.Kind == Codex {
			text = fmt.Sprintf("The Codex worker in %s is waiting for approval in its terminal; I'm bringing it up for you.", w.Plan.RepoName)
		}
		l := NewJobLine(LineInfo, text, w.Plan.Job)
		l.Worker = name
		l.Title, l.Brief = "Worker in "+w.Plan.RepoName, "Waiting at a prompt in its terminal."
		if w.Plan.Kind == Codex {
			l.Title, l.Brief = "Codex worker in "+w.Plan.RepoName, "Waiting for approval in its terminal."
		}
		c.deliver(l, now, fx)
		*fx = append(*fx, Effect{Kind: EfShowPane, Job: w.Plan.Job, Name: name})
	}
}

// flushWorker sends the worker's queue if it can take it; true if a prompt went out.
func (c *AgentController) flushWorker(name string, now Millis, fx *[]Effect) bool {
	if c.shutDown {
		return false
	}
	text, ok := c.workers.TakeSend(name, now)
	if !ok {
		return false
	}
	*fx = append(*fx, Effect{Kind: EfPromptWorker, Name: name, Text: text})
	return true
}

// tellCoordinator queues a line for the coordinator about job (dropped if the job has ended).
func (c *AgentController) tellCoordinator(job JobID, text string, now Millis, fx *[]Effect) {
	if c.shutDown {
		return
	}
	if !c.jobs.IsOpen(job) {
		*fx = append(*fx, logEffect(fmt.Sprintf("dropped a worker line for closed %s", job)))
		return
	}
	c.inbox.Push(MessageForJob(job, text))
	c.flush(now, fx)
}

func (c *AgentController) setRow(name string, status WorkerStatus, now Millis, fx *[]Effect) {
	if job, ok := c.jobs.SetWorkerStatus(name, status, now); ok {
		c.saveJob(job, fx)
	}
}

// WorktreeOf is a live worker's folder ("" if it has none yet).
func (c *AgentController) WorktreeOf(name string) string {
	if w := c.workers.Get(name); w != nil {
		return w.Worktree
	}
	return ""
}
