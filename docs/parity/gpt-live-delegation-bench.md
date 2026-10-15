# GPT-Live delegation benchmark: backend vs agent (#70)

Date: 2026-10-14. Branch `live-client-delegation` (experiment, not for merge).
Harness: `core/voice/bench_live_test.go`:

```
BENCH_GPTLIVE=1 BENCH_OUT=../docs/parity/gpt-live-delegation-bench.json \
  go test -tags live -run TestLiveBenchGPTLiveDelegation -v -timeout 90m ./voice/
BENCH_SUMMARY=../docs/parity/gpt-live-delegation-bench.json go test -tags live -run TestLiveBenchGPTLiveDelegation ./voice/   # re-print the tables
```

Setup:
- A real `voice.Runtime` + the `openailive` adapter + real `gpt-live-1`, through a local proxy that timestamps the wire. Bit's real instructions (persona + `delegation.md` + session facts, home city Paris).
- **backend** = `voice.liveDelegation: backend`, Responses delegation, backend model `gpt-6-luna` (settings default), all of Bit's tools declared.
- **agent** = `voice.liveDelegation: agent`, client delegation, the router of this branch.
- **Fake agent**: `delegate` records the task and context, returns a job id at once and injects `Agent reply: finished: <task>` (weather tasks: "17 degrees with light rain in Paris") 3 s later. Fake `jobs` (one job "check the build", working), `answer_job`, `recall`; fake `get_weather` (Paris 17 °C, light rain), `timer`, `media`; real `get_time`, `calculate`, `end_conversation`. Nothing reaches Claude.
- 21 scenarios (23 turns), spoken with macOS `say`, 24 kHz PCM16 in real time. One session per scenario, N=3 per mode (126 sessions, 1,352 s of GPT-Live billed ≈ $1.13, plus 578k backend input tokens in backend mode).
- Latencies are from the end of the user's audio (last mic sample sent). "Route" = what the turn reached: `agent` (delegate / jobs / answer_job / recall, or a client delegation), `local:<tool>`, `none`. `any` turns are excluded from precision/recall.
- Raw data: `gpt-live-delegation-bench.json` (all turns), `gpt-live-delegation-bench-rerun.json` (agent mode, the two multi-turn scenarios after the split-utterance fix, below).

## Summary

| metric | backend | agent |
|---|---|---|
| turns (not reached) | 68 (1) | 66 (3) |
| route as labelled | 64/68 (94%) | 66/66 (100%) |
| to-agent precision | 23/23 (100%) | 30/30 (100%) |
| to-agent recall | 23/23 (100%) | 30/30 (100%) |
| task keywords ok | 19/23 (83%) | 29/30 (97%) |
| quick/GK answer ok | 20/21 (95%) | 21/21 (100%) |
| delegation.created ms p50/p90 | 1099 / 1840 | 833 / 1173 |
| tool call ms p50/p90 | 2517 / 4238 | 781 / 1073 |
| task at agent ms p50/p90 | 2744 / 4814 | 833 / 1173 |
| first audio ms p50/p90 (all) | 1137 / 1681 | 1065 / 1604 |
| first audio ms p50 (no tool / to agent) | 906 / 1291 | 868 / 1394 |
| goodbye ends the session < 5 s | 0/3 (0%) | 3/3 (100%) |
| turns with errors | 0 | 1 |
| GPT-Live seconds billed (sessions) | 748 (63) | 604 (63) |
| backend tokens in/out | 577778 / 4062 | 0 / 0 |

| scenario | turn | expect (backend / agent) | backend routes | agent routes | backend task | agent task |
|---|---|---|---|---|---|---|
| time | 1 | local:get_time / none | local:get_time, local:get_time, local:get_time | none, none, none |  |  |
| weather | 1 | local:get_weather / agent | local:get_weather, local:get_weather, local:get_weather | agent, agent, agent |  | Check: What's the weather in Paris right now |
| timer | 1 | local:timer / agent | local:timer, local:timer, local:timer | agent, agent, agent |  | Set a timer for ten minutes. |
| math | 1 | local:calculate / any | local:calculate, local:calculate, local:calculate | agent, agent, agent |  | Check: What is seventeen times twenty-three? |
| percent | 1 | local:calculate / any | local:calculate, local:calculate, local:calculate | none, none, agent |  | Check: What's fifteen percent of eighty |
| music | 1 | local:media / agent | local:media, none✗, local:media | agent, agent, agent |  | Pause the music |
| build | 1 | agent / agent | agent, agent, agent | agent, agent, agent | Check why the build failed. | Check why the build failed. |
| worker | 1 | agent / agent | agent, agent, agent | agent, agent, agent | Start a worker to fix the flaky login tests in the DeskPixie repo. | Start a worker to fix the flaky login tests in the DeskPixie repo. |
| jobs | 1 | agent / agent | agent, agent, agent | agent, agent, agent | status ✗ | Check: What's the status of my jobs |
| email | 1 | agent / agent | agent, agent, agent | agent, agent, agent | Write a short email to Sam saying the user will be late. Do not send it; provide a concise draft. | Can you write a short email to Sam saying I'll be late |
| news | 1 | agent / agent | agent, agent, agent | agent, agent, agent | Check: Look up the latest news about the Mars mission. | Look up the latest news about the Mars mission |
| calendar | 1 | agent / agent | agent, agent, agent | agent, agent, agent | Check the user's calendar for tomorrow and report what events are scheduled. | Check: What's on my calendar tomorrow |
| flaky | 1 | any / any | none, agent, agent | agent, agent, agent | Investigate why the login tests were flaky yesterday and identify the likely cause and next step. | yesterday |
| flaky | 2 | agent / agent | agent, agent, (not reached) | (not reached), (not reached), (not reached) | Investigate the flaky login tests from yesterday. Identify likely cause and any relevant evidence; do not make changes unless needed to diagnose. |  |
| repo | 1 | any / any | none, none, none | none, none, none |  |  |
| repo | 2 | agent / agent | agent, agent, agent | agent, agent, agent | Run the DeskPixie repo's test suite and report what fails, including relevant error details. | Run its tests and tell me what fails |
| capital | 1 | none / none | none, none, none | none, none, none |  |  |
| austen | 1 | none / none | none, none, none | none, none, none |  |  |
| spider | 1 | none / none | none, none, none | none, none, none |  |  |
| howareyou | 1 | none / none | none, none, none | none, none, none |  |  |
| joke | 1 | none / none | none, none, none | none, none, none |  |  |
| thanks | 1 | none / none | none, none, none | none, none, none |  |  |
| bye | 1 | local:end_conversation / none | none✗, none✗, none✗ | none, none, none |  |  |


Corrections to the automatic checks (the raw JSON keeps the original flags):
- backend `jobs`: the 3 "task ✗" are the `jobs` tool's `{"action":"status"}`, which has no task text: not a routing or task error. Backend task keywords are then 19/20.
- backend `math` rep 2: "That's three ninety-one." is right; the regexp missed it. Backend quick answers are then 21/21.
- agent `percent` rep 2: the one error is `server_error` `context_injection_incomplete` ("The session closed before the estimated context injection completed."), from closing the session while a time-context append was in flight. Harmless.

## Per kind (p50, ms)

| kind | backend first audio | backend task at agent | agent first audio | agent task at agent |
|---|---|---|---|---|
| quick (time, weather, timer, math, music) | 1416 | – (local tools) | 1364 | 953 |
| agent work | 1143 | 2573 | 1341 | 734 |
| multi-turn | 1437 | 4238 | 1332 | 1060 |
| general knowledge | 965 | – | 909 | – |
| small talk | 839 | – | 858 | – |
| goodbye | 746 | – | 745 | – |

"First audio" is GPT-Live's first sound, usually a filler ("One sec.", "Checking."). In backend mode the answer to a quick question comes in a second reply after the backend's tool call (tool call p50 2.5 s); in agent mode a quick answer is either immediate (time, simple maths) or the agent's reply 3 s (fake delay) after the task.

## Multi-turn and the split-utterance fix

The first run exposed a task-building bug in agent mode: GPT-Live's input transcript of one sentence sometimes comes in two pieces ("The login tests were flaky" | "yesterday", "I'm working on the DeskPixie repo" | "today"), so the task was only the last piece ("yesterday") or the first piece fell out of the context. Fixed in this branch (the task is now all the user's words since Bit last spoke; test `TestClientDelegationJoinsASplitUtterance`) and rerun (agent, `flaky` + `repo`, N=3):

| scenario | turn | agent route ×3 | agent task | context |
|---|---|---|---|---|
| flaky | 1 | agent, agent, agent | "The login tests were flaky yesterday" (all 3) | – |
| flaky | 2 | not reached ×3 | – | – |
| repo | 1 | none ×3 | – | – |
| repo | 2 | agent ×3 | "Run its tests and tell me what fails" | "User: I'm working on the DeskPixie repo today / Bit: …" (keywords ok 3/3) |

`flaky` turn 2 is never reached in agent mode: GPT-Live delegates the statement "The login tests were flaky yesterday" itself (3/3 in both runs; backend mode did so 2/3), and a work hand-off ends the conversation after the acknowledgement (as `delegate` does in backend mode), so "Can you look into it?" is never asked.

## Observations (numbers above)

- **Routing**: both modes 100% precision and recall for "to the agent" on the labelled turns. Backend mode missed one `media` call (said "pausing it now" without calling it; rep 2) and never called `end_conversation` for the goodbye (0/3; the session ended on the 7 s silence timeout). Agent mode ended all 3 goodbyes through the transcript check (closed 2.3–2.5 s after the user stopped, including Bit's goodbye; backend: 8.9–9.4 s).
- **Latency to the agent**: client delegation gets the task to the agent in 833 ms p50 (1173 p90) vs 2744 ms (4814 p90) for the backend model's `delegate` call; `session.delegation.created` itself is 833 vs 1099 ms p50.
- **Quick questions in agent mode**: time 3/3 answered from the pushed time context, no delegation; general knowledge 9/9 without delegation; `percent` answered directly 2/3; `math` delegated 3/3 (answered from the fake agent's reply). Weather, timer and music always go to the agent (by design), so they cost an agent turn.
- **Tasks**: backend tasks are model-written (often expanded: "Write a short email to Sam saying the user will be late. Do not send it; provide a concise draft."); agent tasks are the user's words (`Check: ` for questions) plus up to 6 earlier lines as context. For "Run its tests…" the backend resolves "its" in the task; the agent task keeps "its" and the context carries the referent.
- **Cost**: agent mode billed 604 s of GPT-Live vs 748 s (shorter sessions: work hand-offs end sooner, no backend round trip) and 0 backend tokens vs 577,778 input / 4,062 output tokens (about 14k input tokens per session that used the backend).
- **Fake-reply artefact (both modes)**: with the uninformative fake reply ("finished: <task>"), GPT-Live sometimes invented content when relaying it (agent `calendar`: "one 45-minute appointment tomorrow with Alexand…"; agent `jobs`: "There are no active jobs right now"; backend `news`: "here's the latest news"). This says nothing about routing but shows GPT-Live will embellish an "Agent reply:".
