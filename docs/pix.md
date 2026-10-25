# pix: let Pixie reach the agents you start yourself

`pix` is a small command that ships inside DeskPixie. Start Claude Code or Codex with it,
`pix claude` or `pix codex` instead of `claude` or `codex`, in any terminal: WebStorm's,
iTerm, VS Code, Terminal. The agent looks and works exactly as usual. The difference is that
Pixie can now see that session, tell it something, and interrupt it, next to the workers Pixie
starts itself.

Design and reasoning: [the spec](superpowers/specs/2026-10-15-pix-wrapper-design.md) (#89).

## Two kinds of sessions

| | Sessions Pixie starts (the coordinator, workers) | Sessions you start (`pix`) |
|---|---|---|
| Who starts it | Pixie, in Herdr or tmux | You, in any terminal |
| Where it lives | DeskPixie's Herdr or tmux session | The terminal tab you ran `pix` in |
| Pixie can message and interrupt it | Yes | Yes |
| Pixie can close it | Yes | Only after asking you |
| When DeskPixie quits | Kept or closed, as **Keep agent panes on quit** says | **Never ended.** It keeps running and reconnects when DeskPixie is back |
| When you close its tab or quit the terminal app | — | It ends, like plain `claude` would |

You can talk about either kind the same way: "tell the WebStorm one to stop", "what is the
login-bug session doing?".

## Install

1. Settings ▸ Sessions ▸ **pix command** ▸ **Install**. This links `~/.local/bin/pix` to the
   `pix` inside DeskPixie.app, so it updates whenever DeskPixie does. Nothing else outside the
   app changes. **Uninstall** removes the link.
2. If Settings says `~/.local/bin` is not on your PATH, add this line to `~/.zshrc`:
   ```sh
   export PATH="$HOME/.local/bin:$PATH"
   ```
3. Optional: **Copy Aliases** copies two lines for `~/.zshrc`:
   ```sh
   alias claude='pix claude'
   alias codex='pix codex'
   ```
   With them, plain `claude` and `codex` go through pix. Scripted and one-shot runs are passed
   straight through, exactly as without pix: `claude -p …`, anything with piped input or
   output, subcommands such as `claude mcp` or `codex exec`, and `--version`. Without the
   aliases, plain `claude` is untouched: pix never edits `~/.claude.json`,
   `~/.claude/settings.json` or `~/.codex/config.toml`.

Check it with `pix version`.

## Using it in WebStorm (and other JetBrains IDEs)

- **The built-in terminal:** run `pix claude` (or use the aliases). WebStorm's terminal runs
  your shell, so it works as anywhere else.
- **The Claude Code plugin:** if the plugin lets you set the command it starts, point it at
  `pix claude`.
- **The AI chat panel (ACP agents):** the IDE starts those agents itself, without a shell, so
  pix can't wrap them.

## How Pixie tells your sessions apart

Each session has a **description**, shown to Pixie and in the terminal tab's title. It comes
from three places, and the higher one wins:

1. **You:** `pix --description "login bug" claude`, or tell Pixie "call the WebStorm one
   login bug". Yours stays until you clear it.
2. **The agent:** pix gives it a `set_description` tool and asks it to name the project and
   the task in a few words ("deskpixie: fix login redirect loop") when it starts a task and
   when the task changes. Each turn that starts new work reminds it.
3. **Automatic:** the first line of the prompt you typed to start a piece of work. Only what
   you typed counts: Pixie's messages, other sessions' messages, subagent reports, task
   notices, slash commands and short replies ("yes", "got it") never become a description.
   A resumed conversation (`pix claude --resume …`) starts with your last prompt in it, or
   the title Claude gave it. Codex sessions take it from the turn's prompt.

Before any of these, a session is listed as what and where it is ("Claude Code in focus, no
task yet"), never blank. Pixie remembers a session's last few descriptions, so "the
login-bug one" still finds it after it moves on. Pixie only ever says descriptions, never
internal names. When two sessions fit what you said (say, two in the same project), Pixie asks
you which, by description, where it runs and when it started.

## What Pixie can do to your sessions

- **Message it:** in Claude Code it shows as "Another Claude session sent a message: …", and
  Claude answers it like any prompt; in Codex it is typed in for you.
- **Interrupt it:** like pressing Esc. The session keeps running.
- **Close it:** only when you ask, and Pixie asks you to confirm first, with a confirm word
  ("Close your session login bug? Say confirm hawk."), whether the coordinator is Claude Code
  or Codex.

Quitting DeskPixie never ends a session you started.

## Seeing what's running

A chip under Pixie counts what's running: `⚙ 3 · ⌨ 2` is three of Pixie's agents (its workers;
the coordinator is never counted) and two of your `pix` sessions. Hover it for the list; click
a row to bring that agent forward (Pixie's in its Herdr/tmux tab, yours by bringing its
terminal app to the front). With nothing but the coordinator running, there is no chip.

A session started by an older `pix`, or in a terminal Pixie doesn't recognise, is listed but
its row isn't clickable. A session Pixie just lost contact with shows greyed out and isn't
counted until it reconnects.

## Pixie never types over you

For Claude Code, Pixie's messages go into Claude's own inbox, so your typing is never touched.
Claude takes them at once when idle, or after its current turn.

For Codex, pix has to type the message in. It waits until Codex is idle **and** you have typed
nothing since your last Enter and have stopped typing for a moment. Option+Enter, Ctrl-J and
pasted lines keep your draft, so they don't count as Enter; after Enter, pix also waits until
Codex has started on what you sent. While a message waits, the
tab title starts with 📨 and the count.

## Why it's built this way

- **Owning the launch is the only reliable way in.** Every way into a running agent is local
  and specific to Claude or Codex. Tools that coordinate agents reliably (agent-deck, hcom and
  others) all start the agent themselves. pix does the same, but in your terminal, so you keep
  your IDE and your window.
- **Nothing global changes.** pix adds its hooks and its tool to that one run of the agent. Your
  settings files stay as they are, and uninstalling leaves nothing behind.
- **Not a plug-in alone.** A tool the agent loads can't wake an idle agent or interrupt one, and
  messages would wait for your next prompt. pix sits between the terminal and the agent, so it
  can.
- **No A2A (yet).** Everything here is DeskPixie talking to its own parts on this Mac, which a
  private local socket does more simply. If something outside DeskPixie ever needs to reach
  these sessions, an A2A adapter can be added to DeskPixie without changing pix.
- **Not instead of Herdr or tmux.** Pixie's own workers run in the background with no window,
  and you can attach to watch them. That needs a terminal multiplexer, which pix is not.

## Troubleshooting

| You see | Do |
|---|---|
| Settings says "Link broken (the app moved)" | Click **Install** again. |
| Settings says `~/.local/bin/pix` is something else | That file isn't DeskPixie's; it is left alone. Move it if you want pix there. |
| The tab title says "pix: … reinstall the pix command from Settings" | The link points at an older DeskPixie; reinstall it. |
| The tab title says "pix: this conversation is already connected …" | Two pix sessions resumed the same conversation; this one runs without Pixie. |
| The tab title starts with 📨 | Pixie's message is waiting: finish or clear what you've typed, and let the agent finish its turn. |
| `pix claude` inside a session Pixie started | pix just runs `claude`; the outer session is the one Pixie knows. |
