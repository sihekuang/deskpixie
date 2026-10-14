# Bit's memory comes from the coordinator

Bit keeps no memory of its own. The coordinator (the long-running Claude Code session that
receives every delegated request and keeps its whole conversation) is Bit's memory, reached
two ways. Both exist only when delegation is on (`agent.enabled`).

- **Session block** (`core/agent/memory.go`, `AgentHandle.Facts`): on every voice connect (first
  start and each reconnect, through the runtime's `SessionFacts` hook, so the same for every
  provider: gemini, openai, openailive) the instructions get a "Recent work with the agent" block
  built from the app's job records: each job's time, status and task, plus the coordinator's
  last spoken reply or question (`Job.Reply`, new field in `agent/jobs.jsonl`), the failure
  reason, or the latest progress. Open jobs always, closed ones updated in the last 24 h
  (`MemoryWindowMs`), at most 10 (`MemoryMaxJobs`), each line cut to 300 characters, the block
  capped at 2000 characters (~500 tokens; open jobs first, then the newest). Recall questions
  are left out. Jobs themselves are kept 14 days (`RetentionMs`) as before.
- **`recall` voice tool** (`agent.Recall`): when the user refers to something the conversation
  and the block don't cover, Bit asks the coordinator with a short question. It goes the
  delegate path as a job whose task starts `Recall: `, with context saying it is a context
  question, not a task; `coordinator.md` tells the coordinator to answer at once from memory
  with `status="done"`. The conversation stays open (like a `Check:` fact) and the answer
  arrives as an ordinary "Agent reply:" line. It is as fast as the coordinator's next turn: if
  the coordinator is busy, the question waits for that turn to end.

`persona.md` and `delegation.md` tell Bit to use these rather than ask the user to repeat
context.

## The removed local memory

The earlier local short-term memory (`voice.shortTermMemory`, `voice-memory.json`,
`forget_recent`, `memory:forget`) is gone. A `settings.json` that still has
`voice.shortTermMemory` loads normally; the key is ignored and dropped on the next save. An
existing `~/Library/Application Support/com.sihekuang.deskpixie/voice-memory.json` is left on
disk, unused; delete it by hand if you like.
