# Keep agents running when the app quits

**Status:** approved by the user 2026-10-13 ("we should let the background herdr running without stopping when frontend app stops").

## Problem

Quitting DeskPixie stops all handed-off work:

1. `agent.keepPanesOnQuit` defaults to `false`, so quit closes the coordinator and worker panes.
2. Even with it on, the next launch can't use them. Tokens are random per run and live only in memory:
   - the coordinator left running is replaced (`ReplaceAgent` + `--continue`), which interrupts its turn;
   - workers left running are untracked, and their MCP/hook calls are rejected.
3. `RestoreJobTable` fails every open job ("app restarted").
4. Results sent while the app is down are lost, because the hook server isn't running.

## Design

The Herdr server already runs detached and survives quit. Keeping agents means the next run must **adopt** them.

1. **Default on.** `agent.keepPanesOnQuit` defaults to `true`. Setting it to `false` keeps today's behaviour, unchanged.
2. **Kept-agents file.** On a quit with keep on, the runtime writes `<app_data>/agent/kept.json` (0600 via `WritePrivate`) before panes and tokens are dropped:
   ```json
   {"coordinatorToken": "…",
    "workers": [{"plan": {WorkerPlan}, "worktree": "…", "token": "…"}]}
   ```
   On start it is read once and deleted. A missing, unreadable or malformed file means nothing is kept, which is today's behaviour. A crash writes no file, so it also falls back to today's behaviour.
3. **Tokens.** Add `Tokens.Restore(identity, token)`. A kept coordinator token is restored and used in the coordinator recipe's env instead of a new `Issue`. Kept worker tokens are restored as each worker is adopted.
4. **Adopt the coordinator.** `CoordinatorRecipe.AdoptEarlier` is set when its token was restored. `EnsureCoordinator` then treats a coordinator found running from an earlier run as this run's: `startedBefore = true` and `Ensured{Started:false, Status}`, so no replace and no `--continue`. If no pane is found, it starts with `--continue` as today.
5. **Keep jobs open.** When the coordinator was kept, `RestoreJobTable` keeps open jobs open; worker rows are fixed up by adoption. Each open job gets a coordinator inbox message:
   > The app restarted while you were working on this. If you already replied with its result, reply again with it; otherwise carry on.
6. **Adopt workers.** After restore, once the start-up worktree re-check is done, the runtime lists the session's agents. For each kept worker whose pane is still listed:
   - restore its token;
   - add it to `known` / LiveWorktrees;
   - step a new controller input, `InAdoptWorker{Plan, Worktree, Status}`. This admits the plan, attaches/marks its job row running, calls `Workers.Up` with a restart note in place of the brief, and flushes it.

   The note is:
   > The app restarted while you worked. If you already finished, call report_result again with your result and evidence; otherwise carry on.

   - A kept worker whose job is no longer open is stopped (`EfStopWorker`).
   - A kept worker whose pane is gone is dropped, and its row is marked stopped.
7. **Mascot status.** Nothing new. Adopted jobs show in `jobs` status as usual.

## Tests (port style: Go table tests next to the code)

- `Tokens.Restore`: resolves to its identity and replaces an earlier token for that identity.
- `kept.json`: round-trip; malformed or missing → empty; deleted after load; 0600.
- `EnsureCoordinator` with `AdoptEarlier`: a running coordinator is adopted (no `ReplaceAgent`, no `--continue`). Without it, the behaviour is unchanged.
- `RestoreJobTable` with keep: open jobs stay open. Without keep: unchanged.
- Controller `InAdoptWorker`: the worker is live and up, its row is running, its queue starts with the restart note (not the brief), and a closed job leads to `EfStopWorker`.
- Shutdown with keep writes `kept.json` with the coordinator and live workers. With keep off it writes nothing.
- Settings default `keepPanesOnQuit: true` (update `settings/config_test.go`).
