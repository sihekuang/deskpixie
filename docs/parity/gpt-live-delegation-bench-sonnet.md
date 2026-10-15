# GPT-Live agent mode with the real coordinator (Sonnet) (#70)

Date: 2026-10-14. Branch `live-client-delegation` (experiment, not for merge). Follows `gpt-live-delegation-bench.md` (fake agent).
Harness: `core/voice/bench_sonnet_live_test.go`:

```
BENCH_SONNET=1 BENCH_OUT=../docs/parity/gpt-live-delegation-bench-sonnet.json \
  go test -tags live -run TestLiveBenchGPTLiveSonnet -v -timeout 120m ./voice/
```

Setup:
- `voice.liveDelegation: agent`, the real `gpt-live-1`, Bit's real instructions, through the timestamping proxy (as in the fake-agent bench).
- The real agent (`agentapp.Build`): Claude Code coordinator with `agent.model = "sonnet"`, on the herdr smoke session `deskpixie-smoke` (stopped and deleted afterwards), temp app data and projects root, its own hook port. Workers off. Warm start (one unmeasured warm-up task). One coordinator, scenarios run one at a time.
- Differences from the app, so each reply is heard in the session that asked: `delegate`'s EndAfterReply is cleared (the session stays open until the reply has been spoken, at most 100 s after the task), and open jobs are cancelled after each scenario. No permission question was asked, so nothing was confirmed or denied.
- Same 21 scenarios, N=2 (42 sessions, 860 s of GPT-Live ≈ $0.72; the run took 16.5 min). Claude cost wasn't measured.
- The "relevant" check is a deterministic regexp on the reply (topic words, or 391/12). It checks relevance, not truth.

## Comparison (agent mode; ms from the end of the user's audio, p50 / p90)

| metric | fake agent (N=3) | Sonnet coordinator (N=2) |
|---|---|---|
| first audio (all turns) | 1065 / 1604 | 1020 / 1598 |
| delegation.created → task at the agent | 833 / 1173 | 760 / 1392 |
| first progress line | – | 8119 / 26320 (5 turns) |
| coordinator reply/question line | 3000 (fixed delay) | 7485 / 19783 (23 of 24) |
| Bit's first audio of that reply | not measured | 8784 / 20432 |
| route as labelled | 66/66 | 46/46 |
| turns sent to the agent | 37 (math 3/3, percent 1/3) | 24 (math, percent answered directly 4/4) |
| quick/GK answers right without the agent | 21/21 | 12/12 |
| clarifying questions from the coordinator | – | 1 ("which project or build service?", build rep 1) |
| no reply within 160 s | – | 1 (flaky turn 2, rep 1) |
| GPT-Live seconds | 604 (63 sessions) | 860 (42 sessions, held open for replies) |

## Per scenario (Sonnet; reply ms / reply audio ms, rep 1 · rep 2)

| scenario | reply | note |
|---|---|---|
| weather | 13110/13831 · 9463/10276 | real weather ("clear, 14 °C"); relevant 2/2 |
| timer | 12368/13170 · 7485/8423 | the coordinator set a timer itself ("going off at 10:54 PM"); a later `jobs` reply and Bit announced it going off |
| math, percent | – | GPT-Live answered directly, right 4/4 (the fake-agent run delegated 17×23 every time) |
| music | 6783/8494 · 5648/7446 | "Neither Music nor Spotify is running": an honest can't |
| build | 6859/9368 (question) · 10091/10789 | rep 1 asked which project; rep 2 checked the real DeskPixie CI on GitHub ("no failed build") |
| worker | 2625/4178 · 2837/5314 | "Workers … are turned off": right for this setup |
| jobs | 4294/4886 · 7997/8784 | correct job status (the timers); the regexp flagged it "not relevant" because it never says "job": a check miss |
| email | 5039/5777 · 3472/5990 | draft written, not sent |
| news | 19783/20432 · 16134/16846 | real news (Japan's MMX to Phobos); the slowest |
| calendar | 8315/9129 · 6174/6910 | "Google Calendar isn't signed in": an honest can't |
| flaky | t1 4149 · 3736; t2 none · 12042/16276 | GPT-Live delegated the statement (turn 1) both times; the coordinator answered "Noted…". Rep 1 turn 2 got no reply (its second task went to a coordinator still busy with turn 1's follow-up search); rep 2 searched the repo and CI: "no login tests found" |
| repo | t2 28297/29734 · 40360/41180 | **ran the real DeskPixie tests** (rep 2 also the Swift tests: "404 run"): correct, but it touched the user's main checkout (below) |
| capital, austen, spider, small talk, bye | – | not delegated 2/2 each; goodbye ended the session |

## Observations

- Routing and task delivery match the fake-agent run (task at the agent 760 ms p50). The added latency is the coordinator: 7.5 s p50 to its line and about 0.8–2.5 s more until Bit starts saying it. Quick-ish tasks (worker, email, jobs, music) take 2.6–8 s; web lookups (weather, news) 9–20 s; running test suites 28–40 s.
- The coordinator did things the backend's local tools do (weather, a working timer) and said plainly what it couldn't do (music not running, calendar not signed in, workers off). It asked one clarifying question in 24 tasks.
- A statement ("the login tests were flaky yesterday") becomes a coordinator task in agent mode. With the session held open, the follow-up "can you look into it?" lands while that job runs; once (rep 1) its reply didn't come within 160 s.
- **The coordinator found and used the user's real repository** (`~/Documents/Projects/deskpixie`, outside the temp projects root): it ran `go test` and `swift test` there and read GitHub CI, with no permission question. `git status` there is clean afterwards, but `mac/Packages/DeskPixieCore/.build` was rebuilt (22:57). This is the coordinator's normal reach, but it means this benchmark isn't sandboxed from the user's files.
