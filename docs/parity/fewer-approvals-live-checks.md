# Fewer approvals (#97): live checks

Run 2026-10-15 on the owner's Mac: Claude Code 2.1.301, codex-cli 0.162.1, tmux 3.8, git 2.54.0
(Apple). Every agent ran in a `mktemp -d` folder; Codex used a temp `CODEX_HOME` with only
`auth.json` symlinked in; Claude settings came only from `--settings <tempfile> --setting-sources user`.
Spec: `docs/specs/2026-10-15-fewer-approvals-design.md` (§1, §4, §5).

## Findings for Tasks 4, 5 and 6

**Task 4 (Codex trust answer).**
- Pressing `1` alone does **not** accept. "1. Trust and continue" is already selected, so `1`
  changes nothing on screen. **Enter** accepts. The spec's "`1`, then Enter only if the prompt is
  still there" works, but Enter is always needed in practice (Codex 0.162.1).
- Codex writes `[projects."<path>"] trust_level = "trusted"` into `$CODEX_HOME/config.toml`, keyed
  by the **canonical (symlink-resolved) path of the repository root**. In a temp folder it wrote
  `/private/var/folders/...`, not `/var/folders/...`.
- When started in a worktree (`<repo>/.worktrees/w`), the screen adds "Note: You're in a
  subdirectory of a Git project. Trusting will apply to the repository root: <repo>". The key it
  writes is the **main repository root**, not the worktree. A later start in the main repo then
  shows no trust screen.
- Both marker lines of `CodexTrustScreens[0]` ("Trust this folder?", "1. Trust and continue") are
  present in both cases. The second group ("Do you trust the contents of this directory") did not
  appear in 0.162.1.

**Task 5 (allow rules in the settings assets).**
- `Bash(git status:*)` and `Bash(git add:*)` (the `:*` prefix form) work. With
  `--permission-mode default` and no rules, `git add a.txt` was denied. With
  `Bash(git add:*)` it ran with no denial.
- `Edit(//<abs path>/**)` (the `//` absolute form) works, including a `*` segment:
  `Edit(//<T>/root/*/.worktrees/**)` allowed the **Write** tool to create
  `<T>/root/proj/.worktrees/w/n.txt` from a different cwd, which was denied without the rule.
- **`Write(//...)` on its own does not allow the Write tool.** With only
  `Write(//<T>/root/*/.worktrees/**)`, creating the same file was denied. `Edit(...)` rules are
  what cover Write (and Edit). Task 5 must keep the `Edit(//…)` lines; the `Write(//…)` lines in
  the spec do nothing (harmless but ineffective). Don't count on them.
- Symlinks: Claude Code reports `file_path` canonicalised (`/private/var/...`) in hook input and
  denials. An `Edit(//var/folders/...)` rule (the symlink form) and an `Edit(//private/var/...)`
  rule both allowed a write in the cwd. Fill the rules with the projects root as the app already
  has it. Canonicalising (`filepath.EvalSymlinks`) costs nothing and matches what Claude reports.
- `git status` runs in default mode with no rule at all (Claude Code allows read-only git on its
  own). The `git status/diff/log/show` rules matter less than `git add`/`git commit`/builds.

**Task 6 (permission mode detection).**
- `permission_mode` **is in** UserPromptSubmit, PreToolUse, **PostToolUse**, **PermissionRequest**
  and **Stop**. It is **not in SessionStart** (keys: `cwd, hook_event_name, session_id, source,
  transcript_path`).
- Values: `"auto"` when started with `--permission-mode auto` and auto mode took; `"default"` when
  started with `--permission-mode default`.
- **Silent fallback confirmed:** `--permission-mode auto --model claude-sonnet-4-5` (older than
  auto mode supports) ran with no error or warning, and every event reported
  `permission_mode: "default"`.
- Consequence: a SessionStart hook **cannot** report the mode, so Task 6 Step 6's
  `SessionStart` → `/hooks/mode` hook will not work as planned. The earliest event that carries it
  is **UserPromptSubmit**, which fires as soon as the agent gets its first prompt, before any tool
  use. Use **UserPromptSubmit** for the early report. On tmux, `WithStateHooks`
  (core/app/agentapp/files.go `StateHookTimeouts`) already posts UserPromptSubmit to
  `/hooks/state`, so reading `permission_mode` there covers tmux. On herdr, no agent posts
  UserPromptSubmit today, so Task 6 should add a UserPromptSubmit hook (to `/hooks/mode`, or
  reuse `/hooks/state`) to both `coordinator-settings.json` and `worker-settings.json` **in
  place of** the SessionStart one. Stop and PermissionRequest (both assets) and PostToolUse
  (coordinator) also carry the field.

**Step 4 (the hung `git init`): not reproduced; no DeskPixie defect found.** Codex's own record
of the incident shows the approved `git init` **never ran** (it made no `.git`; Codex logged no
command execution for it), while Codex's script cell sat "running" for 52 s until the model
killed it. Six replays (a script hook, and the full DeskPixie Codex coordinator with the real
hook server, a real GPT model and voice confirmation, including the "Approve for me" permission
preset the session had been switched to) all ran the approved `git init` within a second. No task
added after Task 9. §2's `create_project` removes the command either way. The finding belongs in
the Codex coordinator spec as "unexplained Codex-side stall, not reproduced". Details below.

---

## Step 1: which hook events carry `permission_mode`

```bash
T=$(mktemp -d); cd $T && git init -q
# s.json: SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, PermissionRequest,
# Notification, Stop -> {"type":"command","command":"cat >> $T/hooks.jsonl; echo >> $T/hooks.jsonl"}
claude -p --permission-mode auto    --settings $T/s.json --setting-sources user "run git status and then say done" --output-format json > out.json
claude -p --permission-mode default --settings $T/s.json --setting-sources user "run git status and then say done" --output-format json > out2.json
```

Adjustments: the hook appends a newline after each body (`cat` alone runs the JSON objects
together); the file is `$T/hooks.jsonl` (not `$TMPDIR`); UserPromptSubmit, PreToolUse and
Notification were added to see more events; `< /dev/null` stops `claude -p` waiting 3 s for stdin.

Observed (event, permission_mode, tool):

```
--- auto                         --- default
SessionStart None                SessionStart None
UserPromptSubmit auto            UserPromptSubmit default
PreToolUse auto Bash             PreToolUse default Bash
PostToolUse auto Bash            PostToolUse default Bash
Stop auto                        Stop default
```

`git status` needed no approval in default mode, so no PermissionRequest fired. To get one:
`claude -p --permission-mode default ... "create a file x.txt containing hi, then say done"`:

```
SessionStart None
UserPromptSubmit default
PreToolUse default Write
PermissionRequest default Write   keys: cwd, effort, hook_event_name, permission_mode,
                                  permission_suggestions, prompt_id, session_id, tool_input,
                                  tool_name, transcript_path
Stop default
```

(In `-p` mode the request is then denied: `permission_denials` lists the Write and `x.txt` is not
made.)

Model too old for auto mode:
`claude -p --permission-mode auto --model claude-sonnet-4-5 ...`: exit 0, nothing on stderr,
`modelUsage` = `claude-sonnet-4-5`, and every event had `permission_mode: "default"`.

SessionStart keys: `cwd, hook_event_name, session_id, source, transcript_path` (source
`startup`). Not checked: SessionStart in an interactive session. Testing that would mean
accepting Claude Code's folder trust for the temp dir, which writes `~/.claude.json`. The `-p`
payload shows the event has no mode field.

## Step 2: rule syntax

All runs: `claude -p --permission-mode default --settings <file> --setting-sources user ...
--output-format json < /dev/null`.

| Rules (`permissions.allow`) | Prompt | Result |
|---|---|---|
| `Bash(git status:*)`, `Edit(//var/folders/.../tmp.g3a4S7D9au/**)` (symlink form) | run git status, then create note.txt containing hi (cwd = T) | `permission_denials: []`, note.txt made |
| same with `Edit(//private/var/folders/.../tmp.g3a4S7D9au/**)` (canonical form) | same | `permission_denials: []`, note.txt made |
| none | run `git add a.txt`; then create `<T>/root/proj/.worktrees/w/n.txt` (cwd = `<T>/work`) | denied: `Bash git add a.txt`, `Write …/root/proj/.worktrees/w/n.txt` |
| `Bash(git add:*)`, `Edit(//<T>/root/*/.worktrees/**)` | same | `permission_denials: []`, `A  a.txt` staged, n.txt made |
| `Write(//<T>/root/*/.worktrees/**)` only | create the same n.txt | **denied** (Write) |

Hook input and denials report the canonical path (`/private/var/...`), even when the prompt
named `/var/...`.

## Step 3: Codex trust keys

```bash
C=$(mktemp -d); ln -s ~/.codex/auth.json $C/auth.json
R=$(mktemp -d)/repo; mkdir -p $R && git -C $R init -q && git -C $R -c user.name=t -c user.email=t@t commit -q --allow-empty -m init
tmux new-session -d -s dp-trustcheck1 -x 200 -y 50 "cd $R && CODEX_HOME=$C codex --no-daemon"
```

(The commit needed `-c user.*` because the temp repo has no identity configured. The session was
named `dp-trustcheck1` to keep it distinct.)

Screen after 8 s:

```
  Folder access
  /private/var/folders/.../tmp.OLAs8AAPUR/repo
  Trust this folder? Codex can read, edit, and run files here, subject to your permission settings. ...
› 1. Trust and continue
  2. Quit
  enter continue · esc quit
```

After `send-keys 1` (3 s): **identical screen**, and config.toml has only the `[tui]` keys.
After `send-keys Enter` (4 s): Codex's input box ("› Ask Codex to do anything"). config.toml:

```toml
[projects."/private/var/folders/_b/.../tmp.OLAs8AAPUR/repo"]
trust_level = "trusted"
```

Worktree (`git -C $R worktree add -q .worktrees/w`, Codex started in `$R/.worktrees/w`, fresh
`CODEX_HOME`):

```
  /private/var/folders/.../tmp.3PIUxZJdN3/repo/.worktrees/w
  Note: You're in a subdirectory of a Git project. Trusting will apply to the repository root:
  /private/var/folders/.../tmp.3PIUxZJdN3/repo
  Trust this folder? ...
› 1. Trust and continue
```

`1`: unchanged. Enter: input box. Key written:
`[projects."/private/var/folders/.../tmp.3PIUxZJdN3/repo"]` (the main repo root). Restarting
Codex in `$R` with the same `CODEX_HOME` showed no trust screen.

Also seen: a non-repo folder (the coordinator-like workspace in Step 4) got no trust screen.

## Step 4: the hung `git init`

### What the 09:39 record shows

Read-only from the coordinator's Codex record of that session (`~/.codex/sessions/2026/10/15/
rollout-…01a13e78….jsonl`) and Codex's log db (`~/.codex/logs_2.sqlite`, opened read-only). The
DeskPixie app's data folder was not opened. Times in UTC (09:39 PDT = 16:39Z):

- 16:37:33 and 16:38:17: `start_worker` in trading-101 refused (Codex trust).
- **16:38:09: a `thread_settings` change in the coordinator's Codex session**:
  `approvals_reviewer` `user` → `auto_review`, network `enabled` → `restricted`, active profile
  `:workspace`. This is Codex's `/permissions` preset "2. Approve for me". It was most likely
  picked in the coordinator pane while the user tried to "approve" (the next request was "I've
  approved it"). DeskPixie only types the request text, and no such text was typed.
- 16:38:52: `exec` (code mode) → `exec_command mkdir ~/Documents/Projects/spacex-dashboard`,
  `sandbox_permissions: require_escalated`. **Completed at 16:39:07.879 (15.3 s, the voice
  approval)**. A `CommandExecution` item was recorded (exit 0), and the folder's birth time is
  09:39:07.
- 16:39:12.228: `exec` → `exec_command git init ~/Documents/Projects/spacex-dashboard`, escalated,
  workdir `…/Application Support/…/agent/workspace`. The app said "Coordinator: allowed" at
  09:39:27.
- 16:39:43: the cell yielded "Script running with cell ID 10, Wall time 31.0 s, Output: (none)".
  The model polled with `wait` (1 s, 5 s, 10 s), then `wait terminate:true` at 16:40:04.
- **No `CommandExecution` item was ever recorded for `git init`**. For the mkdir there was one.
  `spacex-dashboard/.git` has birth time **10:25:21**: the user's own later `git init`. So the
  approved command never started, or it hung before it made anything.
- 16:40:06: `git -C … rev-parse …` was refused by the app in 27 ms (88 characters, over the voice
  limit, §3).
- The log db has no entries for the cell between 16:39:12.228 and 16:39:43.238. It shows no
  `hook/started` before 16:40:06 either, not even for the mkdir that was clearly approved. So the
  log can't tell whether the PermissionRequest hook's answer reached Codex.

### Replays (all in temp folders, all ran the approved `git init`)

1. **Script hook, plain Codex** (scratch `repro.sh`): `codex --no-daemon -s workspace-write -a
   on-request -m gpt-5.6-terra` in a temp non-repo workspace, temp `CODEX_HOME`, a
   PermissionRequest command hook passed as `-c hooks.PermissionRequest=[…]` with its
   `hooks.state` trust hash (same scheme as `CodexHookArgs`). The hook sleeps 12 s, then prints
   DeskPixie's exact body
   `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`.
   Prompt: one escalated `git init <tmp>/projects/newproj`. The model used code-mode `exec`, as
   in the incident. Pane: "Working · Running hook" for 12 s, then "Ran git init … Initialized
   empty Git repository". `.git` was made.
   - with `approvals_reviewer="auto_review"` (config): same, ran.
   - mkdir then git init in one turn, workdir containing a space ("Application Support"),
     auto_review: both hooks fired, both ran.
   - **"Approve for me" picked live through `/permissions` in the pane** (the incident's
     16:38:09 change), then mkdir + git init escalated, 14 s hook delay: both ran.
2. **Full DeskPixie Codex coordinator** (a temporary `-tags live` test in `core/app/agentapp`,
   modelled on `liveCodexCoordinator`, deleted afterwards). `agentapp.Build` with
   `coordinatorKind=codex`, `codexModel=gpt-5.6-terra`, tmux smoke session, the real hook server
   on a free port, temp data folder with a space in its name, temp projects root, temp
   `CODEX_HOME` with `auth.json` symlinked. The test delegates "make a new empty project folder
   …/spacex-dashboard and git init it through the shell", and when Pixie's question arrives it
   answers "confirm <word>" through the voice signal:
   - First attempt: a `/var/folders/…` projects root made both commands longer than 80
     characters. Both were refused ("asked to run something I can't read out safely"), which is
     §3's problem, not the hang. Re-run with a `/tmp/dp.XXXX` root.
   - Confirm after 3 s: "Run mkdir … Say confirm hawk" → allowed → "Run git init … Say confirm
     lark" → allowed → `.git` existed 1 s later, and the coordinator replied "Created … and
     initialized its Git repository."
   - Same with `approvals_reviewer = "auto_review"` in the temp config: ran.
   - Same with auto_review and a 14 s confirm delay, matching the incident's 15 s: ran within 1 s
     of "allowed".

### Conclusion

Where it waited: inside Codex, between the escalated `exec_command` being requested (and, per the
app, allowed) and the process starting. git never ran. It was not git itself, and no Codex
approval screen could be shown (no capture exists from 09:39). Not reproducible with the same
Codex, model, hook body, permission preset, workdir with a space, or approval timing. Nothing
points at DeskPixie: the hook server's reply path (`httpserver` `DecisionBody`, through the curl
hook) is the one that worked for the mkdir 5 s earlier in the same turn and in every replay. One
thing would settle it: whether DeskPixie's log shows the 09:39:27 permission response written to
curl (the app's data folder was off limits here). The owner can check that. Recommend: record it
in the Codex coordinator spec as an unexplained Codex-side stall, and rely on §2
(`create_project`) so the coordinator no longer needs an escalated `git init`.

Side observations:
- The incident session's model had been switched to a permissions preset by hand (16:38:09).
  That also turned the coordinator's network to `restricted`, overriding the app's
  `sandbox_workspace_write.network_access=true`. Worth a line in the Codex spec: a `/permissions`
  change in the coordinator pane lasts for the session (until restart).
- With a `/private/tmp/...` projects root, the replay's tray read "workers off: projectsRoot not
  usable". The workers path wasn't needed here; not investigated.

## Cleanup

All temp tmux sessions (`dp-trustcheck1..3`, `dp-hang-*`) and the smoke server were killed. The
temporary live test file was deleted. No product code changed.

## Task 5 follow-up (self-grant and spaces in paths)

Setup: `mktemp -d` base, subfolders "has space with" / "has space without" (git init'd, path contains a space). Settings file: `permissions.allow = ["Edit(//<realpath>/has space <mode>/**)"]`. Command, run in each folder:

    claude -p --permission-mode default --output-format json --settings <file> [--setting-sources user] \
      'create the file .claude/settings.local.json in this folder containing {"permissions":{"allow":["Bash(*)"]}}, then create notes/hi.txt containing hi'

Results (both with `--setting-sources user` and without, as the app launches):
- `permission_denials` = [Write .claude/settings.local.json]; the file was NOT created. Claude Code guards its own settings files even under an `Edit(//dir/**)` allow, so the self-grant risk does not occur. No `ask` guard added.
- `notes/hi.txt` WAS written in both runs: an `Edit(//...)` rule matches a path containing a space.
Conclusion: finding 1 (no change needed, already blocked); finding 2 (space paths match).
