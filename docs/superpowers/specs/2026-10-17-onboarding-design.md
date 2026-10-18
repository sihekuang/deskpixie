# Onboarding: first run with your feature options (#149)

## Intent

Someone who just installed DeskPixie should leave the first run knowing what Pixie can do,
having picked the features they want, and with each picked feature working, or with exactly
what is still missing and how to fix it. Today they find features one at a time in Settings,
and setup problems surface later as a disabled tray line ("Voice: no API key (set
OPENAI_API_KEY)", "Coordinator: herdr not found").

## Goals

- A window opens on a new user's first launch, in three steps: Welcome, Set up (choose and set
  up each feature on one page), Done.
- Every choice in it is the real setting (the same control and the same save path as Settings),
  so the window and Settings can never disagree.
- Each setup page checks itself as you go (key found, microphone allowed, herdr and Claude Code
  found, `pix` installed) and offers the fix: a button when the app can do it, a link when the
  user must install something.
- Reopen it any time from the tray: "Welcome to DeskPixie…".

## Non-goals

- Not the full setup-checks page of #80 (herdr version, integration hook, session server
  restarts). The agents page shows the core's current "Coordinator: …" lines, which already carry
  those problems.
- No installing of herdr, Claude Code or Codex for the user; links only. Commands are only ever
  copied, never run (as in the tray).
- No new settings: every switch already exists.

## Anti-goals

- Nagging existing users: someone upgrading with a settings file never gets the window on their
  own; they can open it from the tray.
- Reopening on every launch: closing the window at any step counts as done.
- A second way to write settings that could diverge from Settings.

## The flow

1. **Welcome.** What DeskPixie is in two lines: Pixie on your desktop you talk to, who can hand
   work to agents and keep an eye on the sessions you start yourself. Continue.
2. **Set up.** One scrolling page, a card per feature. The card's top row is the feature, what it
   does and its switch:
   - **Talk to Pixie** (always on: it is DeskPixie; no switch).
   - **Pixie runs agents for you**: `agent.enabled` (Delegation).
   - **Watch your own sessions**: `notifications.sessionTurns` (Announce finished sessions).
   - **Show canvas pages as they're made**: `canvas.autoOpen` (Open new pages).
   Each switch is the Settings control, so a change saves at once. While a feature is on, its
   setup shows in its card under the switch:
   - **Talk to Pixie**: the voice service picker (`voice.provider`), the API key that service
     needs (saved to the Keychain, as in Settings ▸ Voice) with where a key was found, the
     microphone (Allow Microphone while undecided; Open Privacy Settings when denied), and the
     voice hotkey recorder (`hotkey`). Double-clicking Pixie also talks.
   - **Pixie runs agents for you**: the coordinator picker (`agent.coordinatorKind`); checks that
     the session host (`agent.sessionHost`: herdr or tmux) and the coordinator's program
     (`claude` or `codex`) are found, with a link to get each (herdr.dev, Claude Code, Codex) and
     Check Again; and the core's current "Coordinator: …" tray lines, if any.
   - **Watch your own sessions**: the `pix` command, with the Settings ▸ Coordinator controls
     (Install, Copy Aliases, the PATH line when `~/.local/bin` isn't on your PATH).
   - Canvas needs no setup: its card is only the switch.
3. **Done.** A line per chosen feature: ready, or what still needs attention. Finish closes the
   window; when a chosen switch only applies after a restart (Delegation, Announce finished
   sessions, the voice service), the button is "Finish and Restart" and restarts DeskPixie the way
   the Settings Restart bar does.

Back and Continue move between the steps, which the footer names (Welcome · Set up · Done); the
window can be closed at any step. (First built as five or more pages, welcome, choose, a page per
feature, done; cut to three after the first look: too many steps.)

## When it opens

On launch, when both:
- the onboarding has never been finished or closed on this Mac (UserDefaults
  `DeskPixieOnboardingDone`), and
- there was no `settings.json` in the app-data folder before the core started (checked right
  after data migration, so a migrated ai-mascot user counts as existing).

It opens after the pixie is shown, so Pixie is on screen while the window introduces it. Finishing
or closing sets `DeskPixieOnboardingDone`. The tray item opens it regardless.

## Pieces

- **Core** (Go, `core/app`): RPC `setup.programs` → `{herdr, tmux, claude, codex}`, each the path
  found or "", looked up like the session host and `claudeAuto` do (configured `agent.herdrPath`
  / `agent.tmuxPath` first, then PATH, then `~/.local/bin`, `/opt/homebrew/bin`,
  `/usr/local/bin`).
- **Pure model** (`DeskPixieCore/Onboarding`): the pages for a settings snapshot, navigation,
  each page's checks from what was read (key status, microphone status, programs, `pix` state,
  status lines), the Done summary, and the first-run gate. Unit-tested.
- **App** (`mac/DeskPixie/Onboarding`): a window controller (an ordinary titled window; the app
  becomes a regular app while it is open, like Settings), the SwiftUI pages built on
  `SettingsControlView` and the shared `SettingsModel`, the tray item, and the launch hook.

## Calls I made (Claude), to check

- Talk to Pixie is not optional: there is no setting to turn voice off, and it is what DeskPixie
  is.
- Choices save immediately (they are the Settings controls) rather than at Finish; closing
  midway keeps what was chosen.
- Existing users are detected by an existing `settings.json`, not by version.
- Links, not install commands, for herdr / Claude Code / Codex: their install steps change and
  aren't DeskPixie's to own.
- Canvas gets no setup, only its switch.
- The three steps are Welcome, Set up (switches and setup together), Done, rather than folding
  Welcome into a separate choose step.
- Tray item title: "Welcome to DeskPixie…", placed above Settings….

## Left to the builder

Copy, layout and spacing; window size (fixed; 600×640 points as built); SF Symbols per feature.
