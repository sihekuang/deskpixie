# Working on DeskPixie

How changes are made and checked here, for people and coding agents alike. README.md has what
DeskPixie is and how to build it; docs/release.md has the release steps.

## Tracking

- File a GitHub issue for every feature or bug as soon as it comes up, before the work starts.
  Reference it from commits (`(#123)`) and close it from the PR (`Closes #123`).
- One branch and one PR per change, cut from `origin/main`. Work in a git worktree
  (`.worktrees/<name>`); `main` stays checked out in the main checkout, so never switch a
  worktree to it.
- Never commit to `main` directly, and never merge, tag, release or delete anything (releases,
  tags, branches) without the maintainer's go-ahead.

## Go or Swift

DeskPixie is a Go core (`core/`, built as the C-ABI library `libdeskpixie`) under a native
Swift app for macOS (`mac/`). The split is decided, and it is what lets Windows and Linux come
later by writing only a new native shell:

- **Reusable goes to Go.** Anything that isn't about one platform's UI: voice providers and the
  conversation, the coordinator and workers, sessions and `pix`, settings (config, schema,
  validation, defaults), files and folders, permissions logic, Pixie's tools, audio cleanup,
  and every decision (what to try, in what order, what to say). If a Windows app would need the
  same rule, it belongs in Go.
- **UI and the platform go to Swift on the Mac.** Windows, the menu bar, Settings and onboarding
  views, Pixie's panel and sprites, audio capture and playback, the global hotkey, and OS calls
  that only exist on macOS (TCC permissions, NSWorkspace, AppleScript, notifications).
- **Go decides, Swift does.** When a platform action needs a decision, Go makes it and hands the
  shell a plan to carry out; the shell reports the outcome back. Terminal focus is the model:
  Go builds a `FocusPlan`, Swift's `TerminalFocusExecutor` only performs its rungs.
- **They talk through `dp_call` and events**, JSON over the C ABI (`settings.snapshot`,
  `settings.set`, `setup.programs`, …). A new feature that needs both adds a call, not a
  shortcut around the core.
- **Settings are defined once, in Go** (`core/settings`). The Settings window is drawn from the
  schema, so a new setting needs no Swift. Each is live or applies after a restart
  (`ApplyLive` / `ApplyRestart`). settings.json is written with every value on first launch, so
  changing a default only affects new installs.
- Inside Swift, logic that can be tested lives in the package (`mac/Packages/DeskPixieCore`);
  the app target (`mac/DeskPixie`) stays a thin layer of views and OS calls that can't run in a
  unit test.

## Design (SOLID)

- **Single responsibility.** One type, one job: the settings store saves, the schema
  describes, validation checks, the service applies. When a function starts doing two things
  (check *and* change, decide *and* perform), split it, as `FocusPlan` (decide) and
  `TerminalFocusExecutor` (perform) are.
- **Open/closed.** Add a case by adding an implementation, not by editing every caller: session
  hosts (herdr, tmux, behind `agent.SessionHost`), voice providers, Pixie's tools
  (`voice.Tool`), terminals and worker kinds each sit behind one interface, so a new one is a
  new type plus a registration.
- **Liskov substitution.** Every implementation keeps its interface's promises, fakes included:
  a fake `SettingsClient` numbers snapshots and refuses sets like the core does, so tests that
  pass on the fake hold on the real thing.
- **Interface segregation.** Small, purpose-built seams: `TerminalFocusPrimitives` is four OS
  actions, `SettingsClient` is only the settings calls. A caller depends on what it uses, not
  on the whole engine.
- **Dependency inversion.** High-level code depends on interfaces it owns; the real OS, network
  and process are injected at the edge (`agent.Deps`, `CompositionRoot`). That is what makes
  the core testable without a Mac and portable to other platforms.
- Prefer the smallest change that keeps these: no speculative abstraction, no interface with
  one implementation unless it is a test seam or a platform boundary.
- Write code like the code around it: its naming, its comment density, its idioms.

## Tests

Run before every push:

```sh
(cd core && gofmt -l . && go vet ./... && go test ./...)
(cd mac/Packages/DeskPixieCore && swift test)
make -C mac build SIGNING_IDENTITY="Developer ID Application: …" DEVELOPMENT_TEAM=…
```

- Every change comes with tests for its cases, including the failure paths (missing folder,
  no permission, a file in the way, a path outside home).
- Tests never touch the real user: no real home folder, settings, app data, herdr session or
  hook port. Use `t.TempDir()`, a temporary `HOME`, and `DESKPIXIE_SUPPORT_DIR` for an isolated
  app data folder. If a test leaves anything in the real home, that is a bug to fix.
- A flaky test is a bug: fix the race, don't retry it.
- Live tests (`-tags live`) hit real services. Voice runs cost money: run them only when the
  voice model's behavior itself changed, Gemini Live first, and stop there if it fails.
  Coordinator changes get the cheaper coordinator live tests instead.

## Checking it end to end

Unit tests aren't the last word: drive the real app yourself rather than asking someone to
reproduce it.

- Build it signed with the Developer ID (`make -C mac build` or `make -C mac release
  VERSION=x.y.z`), so macOS permissions survive rebuilds.
- Run it isolated: `DESKPIXIE_SUPPORT_DIR=<scratch>` plus, in that settings.json, its own
  `agent.session` and `agent.port`, so it never adopts or kills the real coordinator. Run the
  binary directly with stderr to a file (NSLog text is private in `log show`).
- Look at what the user would see: `screencapture -l <window id>` (ids from
  `CGWindowListCopyWindowInfo`), menu-bar menus through System Events, clicks as CGEvents from a
  small compiled Swift helper. Talk to the coordinator with `herdr --session <name> pane run`.
- Keep test-only hooks (env switches, extra logs) out of commits.
- Clean up afterwards: stop the test app, `herdr session stop/delete` its session, remove the
  folders it made (only empty ones, with `rmdir`), and restart the real app if you stopped it.

## Review, CI and merging

- Before merging, get a quick review of the diff (a second agent is fine) and fix every real
  finding.
- Wait for CI on the PR's head commit. CI is free on this public repo, so a red run is a real
  failure: read `gh run view <id> --log-failed`.
- Merge only with the go-ahead. A release is a `vX.Y.Z` tag on the merge commit on `main`; the
  Release workflow signs, notarizes and staples it (see docs/release.md).

## Writing

- User-facing text is short and plain: say what happened and what to do, no jargon.
- PR descriptions say what changed, how it was tested (including what was checked by hand and
  what wasn't), and close their issue.
