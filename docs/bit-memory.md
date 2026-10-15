# Pixie's memory comes from the coordinator

Pixie keeps no memory of its own. The coordinator (the long-running Claude Code session that
receives every delegated request and keeps its whole conversation) is Pixie's memory, reached
two ways. Both exist only when delegation is on (`agent.enabled`).

- **Session block** (`core/agent/memory.go`, `AgentHandle.Facts`): on every voice connect (first
  start and each reconnect, through the runtime's `SessionFacts` hook, so the same for every
  provider: gemini, openai, openailive) the instructions get a "Recent work with the coordinator" block
  built from the app's job records: each job's time, status and task, plus the coordinator's
  last spoken reply or question (`Job.Reply`, new field in `agent/jobs.jsonl`), the failure
  reason, or the latest progress. Open jobs always, closed ones updated in the last 24 h
  (`MemoryWindowMs`), at most 10 (`MemoryMaxJobs`), each line cut to 300 characters, the block
  capped as below (open jobs first, then the newest). Recall questions
  are left out. Jobs themselves are kept 14 days (`RetentionMs`) as before.
- **`recall` voice tool** (`agent.Recall`): when the user refers to something the conversation
  and the block don't cover, Pixie asks the coordinator with a short question. It goes the
  delegate path as a job whose task starts `Recall: `, with context saying it is a context
  question, not a task; `coordinator.md` tells the coordinator to answer at once from memory
  with `status="done"`. The conversation stays open (like a `Check:` fact) and the answer
  arrives as an ordinary "Coordinator reply:" line. It is as fast as the coordinator's next turn: if
  the coordinator is busy, the question waits for that turn to end.
- **Per-turn memories** (`core/agent/memories.go`): every finished coordinator turn, with or
  without a job ref, leaves exactly one memory in `agent/memories.jsonl`. The coordinator writes
  it as a `<memory ref="job_…">…</memory>` block next to its `<spoken>` blocks (rule in
  `coordinator.md`); it is parsed in `onStop`, stripped before anything is spoken, and masked
  for pending confirm words. A turn without one gets a fallback from the job's task and the
  turn's spoken text (or the reply's first sentences). A block was chosen over an MCP
  `remember` tool because it arrives in the same Stop message as the turn it describes: no
  correlation, nothing lost if the MCP server is down, and exactly one per turn is simple.
  - **Limits, enforced on write and again on load:** cleaned like `<spoken>`, one sentence,
    at most 25 words (`MemoryMaxWords`) and 200 bytes (`MemoryMaxBytes`), cut on a word with
    "…". At most 200 kept (`MemoryMaxKept`), oldest trimmed; survives restarts.
  - The session block adds up to 10 of the newest after the jobs (a fallback memory of a job
    already listed is skipped), the whole block capped at 2600 characters (~650 tokens; jobs
    at most 1500). A `recall` request carries the newest 30 to the coordinator.

`persona.md` and `delegation.md` tell Pixie to use these rather than ask the user to repeat
context.

## The removed local memory

The earlier local short-term memory (`voice.shortTermMemory`, `voice-memory.json`,
`forget_recent`, `memory:forget`) is gone. A `settings.json` that still has
`voice.shortTermMemory` loads normally; the key is ignored and dropped on the next save. An
existing `~/Library/Application Support/com.sihekuang.deskpixie/voice-memory.json` is left on
disk, unused; delete it by hand if you like.
