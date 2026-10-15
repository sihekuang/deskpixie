# Fewer approvals — #97 (widened)

Status: design approved in chat by the owner 2026-10-15; this spec awaits review.
Related: #80 (setup checks; gets the auto-mode row), #87/#88 (Codex coordinator spec, whose
"nothing is typed into a trust screen" rule changes here for workers).

## Problem

Using DeskPixie means approving too much, and some of it can't be approved at all.

On 2026-10-15 (09:37–09:40, Codex coordinator) a request to build a Next.js app reached the
coordinator but no worker ever started:

1. `start_worker` in `trading-101` failed twice: Codex wanted its folder trusted, and the app
   refuses to answer that screen ("I won't trust it for you"). The user's "I've approved it" to
   Pixie could do nothing.
2. For a new folder, `start_worker` needs an existing git repo, so the coordinator fell back to
   `mkdir` and `git init` through shell commands, each a voice approval.
3. `git -C /Users/daniel/Documents/Projects/spacex-dashboard rev-parse --is-inside-work-tree`
   (88 characters) was refused as unreadable: the voice limit is 80 and the absolute path is half
   of it.
4. The coordinator's `git init` was approved by voice ("Coordinator: allowed", 09:39:27) and
   still hung ~35 s until the coordinator killed it. Cause unknown.

A second, silent source of friction: the app starts every Claude coordinator and worker with
`--permission-mode auto`, but Claude Code documents that when auto mode isn't available to the
session it **starts in Manual instead**, with no error or warning
(https://code.claude.com/docs/en/permission-modes). That happens when the model is older than
auto mode supports (API: Opus/Sonnet 4.6 or later, Haiku 5.5, Fable; Bedrock/Vertex/Foundry:
Sonnet 5+, Opus 4.7+, Haiku 5.5, Fable), when an administrator set `permissions.disableAutoMode`,
or when Anthropic has turned it off server-side. In Manual nearly every edit and command becomes
a spoken "Say confirm hawk", and nothing tells the user why.

## Goal

When the user asks for work in one of their projects, a worker starts, with at most one spoken
confirmation for anything risky. When auto mode didn't take, the app notices, says why, offers a
fix where one exists, and keeps routine work flowing anyway.

## Non-goals

- Editing the user's own Claude Code settings (`~/.claude/settings.json`) or Codex's config file.
- Changing Codex's approval and sandbox modes (the Codex coordinator spec owns those); the
  auto-mode detection below is for Claude agents.
- Running terminal commands for the user: commands are offered to copy (the #80 rule).
- Setup's page itself (#80); this spec adds one check to it.

## 1. Codex workers trust repos under the projects root

Today `start_worker` for a Codex worker fails when Codex shows its folder-trust screen
(`CodexTrustScreens`, `CodexTrustRefusalText`); the Codex spec found that
`-c projects."<dir>".trust_level="trusted"` does not suppress it, so only Codex's own
`~/.codex/config.toml` can.

New: during a Codex worker's start, when the screen shows a trust prompt
(`ShowsCodexTrust`) **and** the resolved repo is strictly inside the projects root, the app
selects "Trust and continue" once (the key Codex's screen shows for it: `1`, then Enter only if
the screen still shows the prompt), then keeps waiting for Codex's input box as today. Codex
records the trust itself, exactly as if the user had pressed it; the app never writes Codex's
files.

- Exactly one attempt per start. If the trust screen is still there after
  `CodexReadySettle`, or the screen matched only part of a marker group, the start fails with
  today's refusal text.
- Repos outside the projects root, or the coordinator's own workspace, never get an answer
  (unchanged).
- The coordinator (Codex) is unchanged: its workspace is no repo and gets no trust screen; if
  one appears the start still fails.
- Recorded in the job's progress: "Trusted <repo> for Codex."

The owner chose this over asking by voice, or falling back to a Claude worker: asking the
coordinator to work in a project is the consent, the same as Claude workers today (the app
already pre-accepts Claude Code's folder trust through `ClaudeTrustStore`, limited to the same
roots).

## 2. `create_project`

A new coordinator MCP tool:

```
create_project {name}  →  {name, path} | {error}
```

- `name`: 1–64 characters from `A–Z a–z 0–9 - _ .`, not starting with `.` or `-`, no `/`.
- Creates `<projects root>/<name>`, runs `git init -b main`, and makes one empty commit
  (`git commit --allow-empty -m "Start <name>"`, with the app's fixed author
  `DeskPixie <deskpixie@localhost>` only if the repo has no user configured), so `start_worker`
  can make a worktree from it at once.
- Refuses if anything already exists at that path (folder, file or link), if the name is
  invalid, or if the projects root is missing. On a failure after the folder was made, it
  removes only the folder it made.
- Runs through the same git runner the worktree code uses (global/system git config only, as
  today), with the usual timeout.
- The coordinator instructions (both Claude's and Codex's) say: for a new project, call
  `create_project`, then `start_worker` with that name; never `mkdir` or `git init` yourself.

## 3. Voice approvals read the projects path short

`SpeakableCommand` and `Describe` (core/agent/confirm.go) gain one substitution, applied
before the 80-character check: the projects root's absolute path followed by `/` is spoken as
`projects/`, and the user's home followed by `/` as `home/` (the root first, so it wins).
The command that runs is untouched; only what is read and measured changes. With it, the
09:40 command reads "git -C projects/spacex-dashboard rev-parse --is-inside-work-tree"
(64 characters) and can be approved by voice.

## 4. The hung `git init`

First task of the plan, an investigation: replay "approve an escalated `exec_command` by voice"
through a Codex coordinator in a temp projects root, and find where it waits (Codex's sandbox
escalation after our hook allowed it, the hook answer not reaching Codex, or git itself). If the
cause is in DeskPixie, its fix is part of this work, with a test. If it's Codex's, the finding
goes into the Codex coordinator spec; §2 removes the need for that command either way.

## 5. Auto mode: detect, tell, fix, fall back

### Detect

Claude Code puts `permission_mode` in hook input (`default` for Manual, `auto`, …; not every
event carries it). The coordinator posts Stop, PermissionRequest and PostToolUse to the app's
hook server; workers post Stop and PermissionRequest.

- The hook server reads `permission_mode` from every hook event that has it and records it per
  agent (coordinator or worker name).
- An agent the app launched with `--permission-mode auto` that reports `default` is **in
  Manual**. Any later report of `auto` clears it.
- The plan's first step checks live which events carry the field (Claude Code version on the
  owner's Mac); if PostToolUse doesn't, the coordinator also gets a SessionStart hook that only
  reports the mode, so it's known before anything needs approving.

### Tell, with the likely reason

When the coordinator, or any worker, is in Manual, the app works out the most likely cause, in
this order:

| Cause | How it's known | Fix |
|---|---|---|
| The model is too old for auto mode | `agent.model` names a Claude model below the documented minimum | **Fix it**: set that setting to the default model. A DeskPixie setting, so no confirmation. Restarts the coordinator (as "Restart coordinator" does); workers pick it up on their next start. |
| Claude Code is too old | `claude --help` doesn't list `auto` among `--permission-mode` choices | **Copy**: `claude update` |
| The organisation turned it off | `permissions.disableAutoMode` is `"disable"` in Claude Code's managed settings file | none; the message says so |
| Unavailable for this account right now | none of the above | none |

The model check uses a table in core of model ids → supports auto (by the documented rule:
API Opus/Sonnet ≥ 4.6, Haiku 5.5, Fable; unknown ids are assumed to support it, so the check
never blames a model it doesn't know).

Where the user sees it:
- **Tray:** a status line "Coordinator is in Manual mode: <reason>" (or "A worker is …"), with
  the fix as a menu item when there is one ("Use a supported model", "Copy update command").
- **Setup (#80):** a `claude.autoMode` row (group Coordinator) with the same state, reason and
  fix; `skipped` until an agent has reported its mode.
- **Pixie,** once per app run: "Heads up: the coordinator is asking before everything because
  <short reason>. I've put a fix in the menu." (A notice through the usual inject path, so it
  waits for a quiet moment and is spoken next time if the user is away.)

Fixes follow #80's rules: a change inside DeskPixie runs directly; a change outside it would
need a confirmation naming exactly what changes (none here); a terminal command is only
copied, never run.

### Fall back: routine work without asking

The app's own `coordinator-settings.json` and `worker-settings.json` (core/app/agentapp/assets)
gain allow rules, always present (in auto mode they're harmless: these are actions auto mode
allows anyway). The existing `ask` and `deny` lists are unchanged, so push, publish, deploy,
`rm -rf` and `.env` still ask in every mode.

Workers (`worker-settings.json`):

```
"Edit(//<projects root>/*/.worktrees/**)",
"Write(//<projects root>/*/.worktrees/**)",
"Bash(git status:*)", "Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)",
"Bash(git add:*)", "Bash(git commit:*)", "Bash(git branch:*)", "Bash(git rev-parse:*)",
"Bash(ls:*)", "Bash(pwd)", "Bash(which:*)",
"Bash(go build:*)", "Bash(go test:*)", "Bash(go vet:*)",
"Bash(npm test:*)", "Bash(npm run test:*)", "Bash(npm run build:*)", "Bash(npm run lint:*)",
"Bash(swift build:*)", "Bash(swift test:*)", "Bash(make:*)"
```

Coordinator (`coordinator-settings.json`): the same `git` read commands (status, diff, log,
show, rev-parse, branch) and `ls`/`pwd`/`which`, plus `Edit`/`Write` for its own workspace and
canvas folders (`//<workspace>/**`, `//<canvas>/**`). No build or test commands: the coordinator
delegates those to workers.

- `<projects root>`, `<workspace>` and `<canvas>` are filled in when the app writes the settings
  files (it already writes them at start), as absolute paths in Claude Code's `//` form.
- `Read` stays as today (Claude Code allows reads in its working directories without asking).
- Not allowed, so they still ask in Manual: `git push`/`pull`/`fetch`/`reset`/`checkout`/
  `rebase`/`merge`, package installs (`npm install`, `brew`, `go get`), `curl`, `rm`, anything
  writing outside those folders.
- The plan's first step verifies the `//` absolute-path form and the `:*` prefix form on the
  owner's Claude Code version, and that a worker in Manual runs `git status` and edits a file in
  its worktree without a PermissionRequest.

## 6. Tap to approve (optional; the owner may drop it at review)

Voice approvals today are "… Say confirm hawk." spoken, plus the bubble. New: while an approval
is pending, Pixie's bubble shows **Allow** and **Deny** buttons. A tap answers it exactly as the
confirm word or a refusal would (the same controller input), and the spoken question is not
repeated. Voice stays the default; the buttons are for when the user is at the keyboard.

## Architecture (where the changes go)

- **core/agent:** worker start's trust handling (§1, in the Codex start path that today returns
  `CodexTrustRefusal`); `create_project` in the coordinator tool catalog and its handler (§2);
  `SpeakableCommand`/`Describe` path shortening (§3); per-agent permission mode and the
  Manual-mode cause (§5) as a small pure unit (`automode.go`: inputs model id, managed setting,
  whether `claude --help` lists `auto` → cause and fix), fed by the hook server.
- **core/agent/httpserver:** read `permission_mode` from hook bodies and pass it on (§5).
- **core/app/agentapp:** the allow rules in both settings assets, filled with paths (§5); the
  coordinator instructions for `create_project` (§2).
- **core/app + bridge:** the tray status line and fix items (§5); the `claude.autoMode` setup
  check when #80 lands (until then the tray and Pixie's notice carry it).
- **Swift:** tray fix items (§5); the bubble's Allow/Deny buttons (§6).

## Error handling

- Trust answer: any doubt (partial marker match, repo outside the root, screen persists) fails
  the start with today's message; the app types nothing else.
- `create_project`: validation errors and git failures come back as `{error}` in plain words for
  the coordinator to relay; a half-made folder it created is removed.
- Mode detection never blocks anything: an unreadable `permission_mode` is ignored, and the cause
  check falls through to "unavailable right now" on any error.
- A fix that fails shows its error where it was pressed; nothing retries on its own.

## Testing

- **Unit:** trust answering (exact marker groups only, one attempt, outside root refused,
  coordinator never answered); `create_project` (name rules, existing path, first commit exists,
  cleanup on failure, worktree creatable afterwards); path shortening (root before home, the
  09:40 command fits, running command unchanged); `automode` cause table (each row, unknown model
  never blamed); hook bodies with and without `permission_mode`; the settings assets contain
  exactly the listed allow rules with paths filled in; tap-to-approve takes the same path as the
  confirm word.
- **Live smokes (temp folders only, never the user's real repos):** a Codex worker starts in a
  fresh untrusted repo under a temp projects root; `create_project` then `start_worker`; a Claude
  worker forced into Manual (`--permission-mode default`) runs `git status` and edits a file
  without a PermissionRequest; a hook reports `permission_mode`.
