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

1. Settings ▸ Coordinator ▸ **pix command** ▸ **Install**. This links `~/.local/bin/pix` to the
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
   With them, plain `claude` and `codex` go through pix. Without them, plain `claude` is
   untouched: pix never edits `~/.claude.json`, `~/.claude/settings.json` or
   `~/.codex/config.toml`.

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
2. **The agent:** pix gives it a `set_description` tool and asks it to use it when its task
   changes ("fixing login redirect loop").
3. **Automatic:** the first few words of your first prompt for a new piece of work.

Pixie remembers a session's last few descriptions, so "the login-bug one" still finds it after
it moves on. Pixie only ever says descriptions, never internal names.

## What Pixie can do to your sessions

- **Message it:** the message arrives as if you had typed it.
- **Interrupt it:** like pressing Esc. The session keeps running.
- **Close it:** only when you ask, and you're asked to confirm.

Quitting DeskPixie never ends a session you started.

## Pixie never types over you

For Claude Code, Pixie's messages go into Claude's own inbox, so your typing is never touched.

For Codex, pix has to type the message in. It waits until Codex is idle **and** you have typed
nothing since your last Enter and have stopped typing for a moment. While a message waits, the
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
