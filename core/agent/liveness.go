package agent

import "fmt"

// Job liveness: an open job must have something behind it. Without these checks a job whose
// worker went away, or whose question nobody answered, stays open forever (and keeps the
// voice conversation held open).
const (
	// UnbackedNudgeMs: a working job with nothing behind it this long gets one nudge to the
	// coordinator.
	UnbackedNudgeMs Millis = 2 * 60_000
	// UnbackedFailMs: still nothing behind it this long after the nudge: the job fails.
	UnbackedFailMs Millis = 2 * 60_000
	// QuestionExpiryMs: a question the user hasn't answered in this long is closed.
	QuestionExpiryMs Millis = 30 * 60_000
)

// unbackedNudge is what the coordinator is told about a working job with nothing behind it.
const unbackedNudge = "This job shows as working, but no worker is running for it and you haven't replied about it. Reply now with its result, a question for the user, or that it failed. If you handed it to a session, reply done: sessions don't report back, and the person hears its answer from Pixie."

// questionExpiredNote tells the coordinator a question's job was closed.
const questionExpiredNote = "The user didn't answer your question within 30 minutes, so the app closed this job. Drop it; no reply is needed."

// liveness is one open job's watch state; it resets when the job changes (Updated moves).
type liveness struct {
	updated  Millis // the job's Updated when this watch started
	since    Millis // when it was first seen with nothing behind it; 0 = it has something
	nudgedAt Millis // when the coordinator was nudged; 0 = not yet
}

// backed: something is working on job or will be: a message for it queued or in flight, a
// live worker, a pending confirm, or a coordinator that is starting or busy.
func (c *AgentController) backed(job JobID) bool {
	if c.inbox.Carries(job) || c.inbox.Queued(job) || c.hasLiveWorkers(job) || c.backedByConfirm(job) {
		return true
	}
	busy := c.coordStatus == PaneWorking || c.coordStatus == PaneBlocked
	return c.coordinator == coordStarting || c.awaitingStop || (c.coordinator == coordUp && busy)
}

// tickLiveness runs the checks once (on InTick).
func (c *AgentController) tickLiveness(now Millis, fx *[]Effect) {
	if c.isDisabled() || c.shutDown {
		return
	}
	if c.watch == nil {
		c.watch = map[JobID]*liveness{}
	}
	open := map[JobID]bool{}
	changed := false
	for _, j := range c.jobs.Open() {
		open[j.ID] = true
		switch j.Status {
		case JobQuestion:
			delete(c.watch, j.ID)
			if satSub(now, j.Updated) >= QuestionExpiryMs && !c.backedByConfirm(j.ID) {
				c.expireQuestion(j, now, fx)
				changed = true
			}
		case JobDelegated, JobWorking:
			w := c.watch[j.ID]
			if w == nil || w.updated != j.Updated {
				w = &liveness{updated: j.Updated}
				c.watch[j.ID] = w
			}
			if c.backed(j.ID) {
				w.since = 0
				continue
			}
			if w.since == 0 {
				w.since = now
			}
			switch {
			case w.nudgedAt == 0 && satSub(now, w.since) >= UnbackedNudgeMs:
				*fx = append(*fx, logEffect(fmt.Sprintf("job %s: nothing running for it, nudging the coordinator", j.ID)))
				w.nudgedAt, w.since = now, 0
				c.inbox.Push(MessageForJob(j.ID, unbackedNudge))
				c.flush(now, fx)
			case w.nudgedAt != 0 && satSub(now, w.since) >= UnbackedFailMs:
				delete(c.watch, j.ID)
				c.inbox.DropQueued(j.ID)
				c.failJobSaying(j.ID, "nothing was running for it", "Coordinator: a job stopped without finishing: "+short(j.Task), now, fx)
				changed = true
			}
		}
	}
	for id := range c.watch {
		if !open[id] {
			delete(c.watch, id)
		}
	}
	if changed {
		c.syncHold(fx)
	}
}

func (c *AgentController) backedByConfirm(job JobID) bool {
	for _, p := range c.confirms.Pending() {
		if p.Job == job {
			return true
		}
	}
	return false
}

// expireQuestion closes a question nobody answered, says so, and tells the coordinator.
func (c *AgentController) expireQuestion(j Job, now Millis, fx *[]Effect) {
	if _, err := c.jobs.Fail(j.ID, "no answer from the user", now); err != nil {
		return
	}
	*fx = append(*fx, logEffect(fmt.Sprintf("job %s: question unanswered for 30 minutes, closed", j.ID)))
	c.forgetUpdates(j.ID)
	c.stopJobWorkers(j.ID, fx)
	c.saveJob(j.ID, fx)
	c.deliver(NewJobLine(LineInfo, "Coordinator: closed a question you didn't answer: "+short(j.Task), j.ID), now, fx)
	c.inbox.DropQueued(j.ID)
	c.inbox.Push(MessageForJob(j.ID, questionExpiredNote))
	c.flush(now, fx)
}
