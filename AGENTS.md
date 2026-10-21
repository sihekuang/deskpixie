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

## Where code goes

- Logic lives where it can be unit tested: the Go core (`core/`) or the Swift package
  (`mac/Packages/DeskPixieCore`). The app target (`mac/DeskPixie`) stays a thin layer of views
  and OS calls (TCC, NSWorkspace, AppleScript) that can't run in a unit test.
- Settings are defined once, in Go (`core/settings`: config, schema, validate). The Settings
  window is drawn from the schema, so a new setting needs no Swift. Each one is live or applies
  after a restart (`ApplyLive` / `ApplyRestart`).
- settings.json is written with every value on first launch, so changing a default only
  affects new installs; existing users keep what is saved.
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
