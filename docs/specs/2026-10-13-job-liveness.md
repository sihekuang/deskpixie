# DeskPixie: job liveness checks

Date: 2026-10-13
Status: **Approved** by the user (rule 1, and option (a) for questions).

## Problem
An agent job closes only when the coordinator replies "done" or "failed", the user cancels it, or the coordinator can't start. Two kinds of jobs could therefore stay open forever.

The first is a **working job with nothing behind it**. For example, the coordinator says "a worker is on it", and then the worker's pane goes away.

The second is a **question the user never answered**. On 2026-10-13 a merge job stayed in `question` after its worker stopped. The app showed it as in progress, with nothing running in herdr.

An open job also holds the voice conversation open: 180 s of silence before it ends instead of 7 s. With GPT-Live that idle time is billed.

## Rules (checked on every controller tick, about once a second)

### 1. Working jobs need something behind them
A *delegated* or *working* job counts as **backed** while any of these holds:
- a message about it is queued or in flight to the coordinator
- it has a live worker
- it has a pending permission confirm
- the coordinator is starting, has a turn in progress (its Stop hasn't arrived), or reports working or blocked

When a job has gone **unbacked for 2 minutes** (`UnbackedNudgeMs`), the coordinator gets one nudge for it: "This job shows as working, but no worker is running for it and you haven't replied about it. Reply now with its result, a question for the user, or that it failed."

If it is **still unbacked 2 minutes after that** (`UnbackedFailMs`), the job fails with the reason "nothing was running for it". Bit says "Agent: a job stopped without finishing: <task>" and plays the error cue.

Any change to the job resets the watch, since its `Updated` moves. A progress line, a status change or an answer all count.

### 2. Unanswered questions expire after 30 minutes
A job in *question* whose `Updated` is **30 minutes** old (`QuestionExpiryMs`) is closed as failed, with the reason "no answer from the user". This doesn't happen while a permission confirm is pending; those already expire after 2 minutes.

Bit says "Agent: closed a question you didn't answer: <task>", and the coordinator is told: "The user didn't answer your question within 30 minutes, so the app closed this job. Drop it; no reply is needed."

## Not changed
- Restarts still keep open jobs (PR #54). A kept job whose worker doesn't come back is caught by rule 1.
- The watch state is not persisted: after a restart the clocks start again.

## Code
- `core/agent/liveness.go` holds the checks.
- `controller.go` records the coordinator pane's status and calls `tickLiveness` from `tick`.
- `inbox.go` gains `Queued(job)`.
- Tests are in `core/agent/liveness_test.go`: nudge then fail; a progress reply resets the watch; a busy coordinator and an in-flight prompt count as backing; a worker backs its job until its pane goes; a question expires at 30 minutes; an answered question doesn't.
