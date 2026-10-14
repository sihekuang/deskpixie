# Live smoke results

## 2026-10-14: Herdr 0.9.4 (protocol 22), issue #63

Run on the `herdr-compat` branch (macOS, herdr 0.9.4 from Homebrew at `/opt/homebrew/bin/herdr`,
Claude Code 2.1.300), twice in a row, both green:

```
cd core && go test -count=1 -tags live -run Live -v -timeout 15m ./herdr/ ./app/agentapp/
```

| Test | Run 1 | Run 2 | What it checked |
|---|---|---|---|
| `herdr` TestLiveAgentSmoke | PASS (40 s) | PASS (53 s) | As below for 0.7.5, on protocol 22. Run 2 hit 0.9.x's refused subscription for a gone pane and recovered ("a tracked pane is gone; resubscribing without it") |
| `herdr` TestLiveWorkerSmoke | PASS (72 s) | PASS (58 s) | As below for 0.7.5 (Claude part; Codex not installed) |
| `agentapp` TestLiveDelegationEndToEnd | PASS (52 s) | PASS (14 s) | The voice loop got `Agent reply: pong` (the test now fails on any other Agent reply, e.g. the failure notice that echoes the task); Shutdown about 1 s; no panes and no `mcp/*.json` left |

Before the fix every smoke failed on `herdr version mismatch (client protocol 17, server 22)`,
except the delegation smoke, which passed wrongly on "Agent reply: The agent stopped before
finishing: Reply with the word pong…".

Herdr 0.7.5 (protocol 17) is no longer installed on this machine: it is covered by the
captured fixtures and the fake server only (`core/herdr/testdata/*_17.json`,
`herdrtest.StartProtocol(t, 17)`), not live.

What 0.9.4 does differently, seen live (handled in `core/herdr/behavior.go` and the event pump):

- An agent that exits during its launch (`claude --continue` with nothing to continue) loses
  its name: `agent.get` answers `agent_not_found`, where 0.7.5 kept the pane named without an
  agent. The start used to wait out its timeout ("claude did not start"); now it fails with
  ExitedAtStart and the coordinator restarts without `--continue`.
- An agent blocked on Claude Code's folder-trust dialog stays `launch_pending` (status
  `blocked`) until the dialog is answered; its screen can be read and keys sent meanwhile. On 22
  a detected, blocked agent counts as started.
- `events.subscribe` naming a pane that no longer exists is refused whole (`pane_not_found`).
- `agent.start` never answered `agent_pane_busy` right after `tab.create` (retried on both).
- `agent.read` "visible" works; still used on both versions.

Facts (`info` lines, both runs): `herdr status --json` echoes the session name; `agent.list`
while launching: `agent` absent, `agent_status=unknown`, `launch_pending=true`; after `/exit`
the pane stays with `agent` absent; Escape fires no Stop hook; `permissions.ask` raises
PermissionRequest under `--permission-mode auto`.

**Claude hook:** `herdr integration status` says `claude: outdated (v7 < v10)`; the app shows
its "run `herdr integration install claude`" hint. All smokes passed with the outdated hook;
it was not updated (that needs the owner's OK).

## 2026-10-13: Herdr 0.7.5 (protocol 17)

Run 2026-10-13 on the `parity` branch (macOS, herdr 0.7.5, Claude Code from `~/.local/bin`):

```
cd core && go test -tags live -run Live -v -timeout 15m ./voice/ ./herdr/ ./app/agentapp/
```

Result: **all pass**, about 2 minutes total (the three packages run as separate binaries; a
file lock in `core/herdr/smoketest` serializes the two that use the Herdr smoke session).

| Test | Result | What it checked |
|---|---|---|
| `voice` TestLiveVoiceProviders/openai-realtime/answer | PASS (4.1 s) | Ready; SpeechStarted/Stopped; `calculate {"expression":"2 + 2"}` called; audio deltas played; assistant transcript "2 plus 2 equals 4." |
| `voice` TestLiveVoiceProviders/openai-realtime/barge-in | PASS (5.1 s) | "Count slowly to thirty", then the question again after 1.1 s of the reply: CmdCancelResponse (heard 1120 ms), the playback flush, and `conversation.item.truncate` seen on the wire (a local proxy logs the client message types) |
| `voice` TestLiveVoiceProviders/gemini-live/answer | PASS (5.8 s) | Ready; SpeechStarted/Stopped; `calculate {"expression":"2+2"}`; audio played; transcript "Two plus two equals four." |
| `voice` TestLiveVoiceProviders/gemini-live/barge-in | PASS (5.1 s) | Cancel (heard 1200 ms) and playback flush while it was counting. Gemini has no truncate message: the adapter closes the response and mutes until the turn ends |
| `herdr` TestLiveAgentSmoke (agent_smoke.rs, D-543) | PASS (61 s) | Session `deskpixie-smoke` started; the parent's fake `ANTHROPIC_API_KEY` is scrubbed (the coordinator runs on the subscription); Stop hook carries `<spoken ref=… status=…>`; PermissionRequest under `--permission-mode auto`: allow ran `touch`, deny blocked it; SessionHost conformance live (7 steps) |
| `herdr` TestLiveWorkerSmoke (worker_smoke.rs, D-544) | PASS (56 s) | A coordinator (sonnet) calls `start_worker` over MCP; a haiku Claude worker commits HELLO.txt on `mascot/<slug>` in `.worktrees/`, reports with `report_result`, and its Stop hook arrives; the main checkout is untouched and `.worktrees/` is in `.git/info/exclude`; the job's end closes the pane and deletes its MCP config; cancelling a job stops its worker; shutdown leaves no panes |
| `agentapp` TestLiveDelegationEndToEnd | PASS (8 s alone; 70 s in the full run, waiting for the lock) | `agentapp.Build` with the real Herdr host on `deskpixie-smoke`, hook server on a free port; the `delegate` voice tool; the voice loop got `Agent reply: Pong.`; Shutdown took about 1.1 s; no panes and no `mcp/*.json` left |

Skips: none. Each voice provider skips cleanly when `voice.EnvSecrets` (the app's DotenvPaths:
the environment, `./.env`, then `~/Library/Application Support/com.sihekuang.deskpixie/.env`)
finds no key; the Herdr smokes skip without herdr or claude; the voice smoke skips without
`say`/`afconvert`.

Port: the hook port was never wrong. Every smoke binds `127.0.0.1:0` and asserts it is not
48217 (the running app's port); the settings files carry the bound port.

## Facts seen live (the `info` lines)

- `herdr status --json` echoes the session name verbatim: yes.
- `agent.list` while launching: `agent` absent, `agent_status=unknown`, `launch_pending=true`.
- A fresh temp workspace shows Claude Code's folder-trust dialog (the smoke answers it; the app
  pre-trusts its own workspace; the worker smoke, which pre-trusts like the app, answered no
  dialogs).
- `permissions.ask` rules raise PermissionRequest under `--permission-mode auto`: yes.
- After `/exit` the pane is gone from `agent.list` (it does not stay with `agent` absent).
- Escape during a turn fires no Stop hook (1 Stop during conformance).
- `herdr integration status`: `claude: current`, `codex: not installed`.

## Codex (opt-in)

`SMOKE_CODEX=1 go test -tags live -run TestLiveWorkerSmoke -v ./herdr/` adds worker_smoke's
`--codex` part. Run separately: **PASS (93 s)**. A Codex start in the untrusted repo showed
the trust screen and failed with "Codex asks to trust this repo…", typing nothing and saving no
trust; in the trusted repo (the smoke-only `-c projects={…}` override) the Codex worker
reached the app over MCP with its bearer and committed CODEX.txt on `mascot/codex-hi` from
its workspace-write sandbox, then stopped with its job.

## Side effects

As in the Rust smokes, Claude Code records folder trust for the temp workspace and worktrees
in `~/.claude.json`. Nothing else outside the temp folders is written; the `deskpixie-smoke`
session is stopped and deleted at the end (`SMOKE_KEEP=1` keeps it).
