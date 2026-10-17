# Move terminal opening/focusing from the Go core into the Swift app (#116) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The Go core stops shelling out to `osascript`/`/usr/bin/open`/`ps -E` to raise or open a terminal; it only decides *what* to show (a `FocusPlan`) and hands it to the Swift app, which executes it with NSWorkspace/NSAppleScript/Accessibility APIs. No user-visible behaviour changes.

**Architecture:** The core's existing `ReusingLauncher`/`WarpLauncher`/`ItermLauncher` (which execute) become `ReusingPlanner`/`WarpPlanner`/`ItermPlanner` (which return a `FocusPlan` describing every rung to try, in order). The core pushes the finished plan to the app as a new `focusPlan` event on the existing async event queue (the same mechanism `notify`/`showPane` use); the app walks the plan with four new primitives (open a URL, run an AppleScript, check/prompt Accessibility trust, signal a pid) and reports success/failure back once so the core's existing tray-status code stays the only place that writes tray text.

**Tech Stack:** Go (core), Swift (macOS app, `DeskPixieCore` package + `mac/DeskPixie`).

**Spec:** `docs/superpowers/specs/2026-10-16-terminal-focus-swift-split-design.md`

## Global Constraints

- Identical user-visible behaviour to today — this is a refactor, not a feature (design doc §1).
- The three rung groups are NOT uniform and must stay that way: `ItermSelect` alone (no fallback), `ActivateApp` alone (no fallback), and the Warp group `FocusURLs`/`RaiseScript` → `KillPIDs`+`NewTerminal` (has a fallback). Only the Warp group falls back to opening a new terminal; the other two fail outright exactly as `ReusingLauncher.Open`'s early returns do today (design doc §3).
- JSON field names are snake_case (existing convention: `PaneRequest{job,worker}`, `RosterView{agent_count,your_count}`).
- The Warp tab-config file write stays idempotent (only rewritten if its content changed) — unchanged from today's `WarpLauncher.Open`.
- All tray-status text stays centralized in Go's `TerminalFailureStatus`; the app never mints its own tray wording (design doc §2/§4.1).
- Accessibility trust is checked fresh on every raise attempt (never cached as "permanently denied"); the system prompt itself is shown at most once per app launch (design doc §5).

## Review Focus

- A failed iTerm tty-select or a failed "activate app" must NOT fall through to opening a new terminal — that would be a silent behaviour change from today's early-return. Pinned by Task 2's rewritten `reuse_test.go` and Task 4's executor tests (no `NewTerminal` in those plans).
- A pre-plan failure (the coordinator session itself won't start) must still reach the tray through the existing path — it must not get lost because the new focus-plan flow assumes a plan was built. Pinned by Task 1.
- Two rapid clicks producing two in-flight `focusPlan` events/outcome reports must not corrupt the executor's one-shot Accessibility-prompt state (`promptedForAccessibility`) — structurally guaranteed by `TerminalFocusExecutor` being `@MainActor` (calls serialize), but the *sequencing* of that state (prompt on the first call, not the second) is a logic bug a type system can't catch. "Last outcome wins" for the tray line is an accepted simplification, not a bug. Pinned by Task 4's `TestAccessibilityPromptedOnceThenNotNagged`.
- A `FocusPlan` with no rung applicable at all (a planner bug, or a future regression) must report failure, not silently do nothing with no tray feedback. Pinned by Task 4.
- Warp's focus-URL step tries every candidate client (newest first) in order, not just the first — a stale/dead newest client with a bad URL must fall through to the next client's URL before giving up to the raise rung. Pinned by Task 2 (`TestAFocusURLThatIsNotAWarpSessionIsIgnored`-equivalent) and Task 4.

---

### Task 1: Core types, `Deps` wiring, and existing test fixtures (`core/agent`)

**Files:**
- Modify: `core/agent/traits.go` (replace `TerminalLauncher`; add `FocusPlan`, `NewTerminalAction`, `TerminalPlanner`, `FocusSink`)
- Modify: `core/agent/runtime.go` (`Deps.Launcher`→`Deps.Planner`, add `Deps.Focus`; `OpenTerminal`/`FocusJob`/`FocusAgent`)
- Modify: `core/agent/agenttest/fakes.go` (`FakeLauncher`→`FakePlanner`; add `FakeFocusSink`)
- Modify: `core/agent/runtime_test.go`, `core/agent/runtime_worker_test.go`, `core/agent/runtime_worker_session_test.go`, `core/agent/runtime_roster_test.go` (field renames; one assertion per file updated to the new shape)

**Interfaces:**
- Produces (used by every later task):
  ```go
  type FocusPlan struct {
      FocusURLs   []string           `json:"focus_urls,omitempty"`
      RaiseScript []string           `json:"raise_script,omitempty"`
      ItermSelect []string           `json:"iterm_select,omitempty"`
      ActivateApp string             `json:"activate_app,omitempty"`
      KillPIDs    []int              `json:"kill_pids,omitempty"`
      NewTerminal *NewTerminalAction `json:"new_terminal,omitempty"`
  }
  type NewTerminalAction struct {
      Kind   string   `json:"kind"` // "url" | "applescript"
      URL    string   `json:"url,omitempty"`
      Script []string `json:"script,omitempty"`
  }
  type TerminalPlanner interface {
      Name() string
      Plan(a Attach) (FocusPlan, error)
  }
  type FocusSink interface {
      Focus(plan FocusPlan)
  }
  ```
- Consumes: nothing new (this task only introduces the contract and updates the one package that defines/uses `Deps`).

- [ ] **Step 1: Replace `TerminalLauncher` with the `FocusPlan`/`TerminalPlanner`/`FocusSink` types in `core/agent/traits.go`**

  Delete the existing `TerminalLauncher` interface (lines 87-91) and add the block above in its place (doc comments: `FocusPlan` explains the non-uniform rung groups per Global Constraints; `FocusSink` sits next to the existing `PaneSink` at line 123-127, same one-line style).

- [ ] **Step 2: Update `core/agent/runtime.go`'s `Deps` and the three call sites**

  In `Deps` (line ~70-92): rename `Launcher TerminalLauncher` → `Planner TerminalPlanner`; add `Focus FocusSink` next to it.

  `OpenTerminal`, `FocusJob`, `FocusAgent` (lines 487-499, 551-561, 582-589) each currently end with:
  ```go
  return h.deps.Launcher.Open(Attach{Argv: h.deps.Host.AttachCommand(), Dir: h.deps.TerminalDir})
  ```
  Replace with:
  ```go
  plan, err := h.deps.Planner.Plan(Attach{Argv: h.deps.Host.AttachCommand(), Dir: h.deps.TerminalDir})
  if err != nil {
      return err
  }
  h.deps.Focus.Focus(plan)
  return nil
  ```
  (identical in all three methods — a pre-plan error, e.g. from `EnsureSession` earlier in the method, already returns before reaching this point, unchanged).

- [ ] **Step 3: Rename `FakeLauncher`→`FakePlanner` and add `FakeFocusSink` in `core/agent/agenttest/fakes.go`**

  `FakePlanner` keeps `FakeLauncher`'s exact recording semantics (`Opened() [][]string`, `Attaches() []agent.Attach`, `SetFail(why string)`), only `Open(a Attach) error` becomes:
  ```go
  func (l *FakePlanner) Plan(a agent.Attach) (agent.FocusPlan, error) {
      l.mu.Lock()
      defer l.mu.Unlock()
      if l.fail != "" {
          return agent.FocusPlan{}, agent.LaunchError(l.fail)
      }
      l.opened = append(l.opened, agent.Attach{Argv: slices.Clone(a.Argv), Dir: a.Dir})
      return agent.FocusPlan{NewTerminal: &agent.NewTerminalAction{Kind: "url", URL: "fake://opened"}}, nil
  }
  ```
  Update the `var _ agent.TerminalLauncher = (*FakeLauncher)(nil)` compile check (line 1146) to `var _ agent.TerminalPlanner = (*FakePlanner)(nil)`.

  Add:
  ```go
  // FakeFocusSink records every FocusPlan handed to it.
  type FakeFocusSink struct {
      mu   sync.Mutex
      sent []agent.FocusPlan
  }

  func (f *FakeFocusSink) Focus(plan agent.FocusPlan) {
      f.mu.Lock()
      defer f.mu.Unlock()
      f.sent = append(f.sent, plan)
  }

  func (f *FakeFocusSink) Sent() []agent.FocusPlan {
      f.mu.Lock()
      defer f.mu.Unlock()
      return slices.Clone(f.sent)
  }
  ```

- [ ] **Step 4: Fix the four dependent test files' `Deps{}` literals and renamed-type references**

  `core/agent/runtime_test.go`, `runtime_worker_test.go`, `runtime_worker_session_test.go`, `runtime_roster_test.go`: change every `Launcher: &agenttest.FakeLauncher{}` to `Planner: &agenttest.FakePlanner{}, Focus: &agenttest.FakeFocusSink{}`; change struct fields/locals typed `*agenttest.FakeLauncher` to `*agenttest.FakePlanner`.

  In `runtime_test.go` (lines 289-290, 322-323, 986-987, 999-1000) and `runtime_roster_test.go` (lines 207-208), the existing assertions on `r.launcher.Attaches()`/`.Opened()` stay exactly as they are (still proving `Plan` was called with the right `Attach`s) — no change needed to those lines themselves, only to the field's declared type from Step 3.

  Add one new assertion in `runtime_test.go` near the existing `Attaches()` check in the `OpenTerminal` success test (around line 289): that `r.focus.Sent()` (a new `*agenttest.FakeFocusSink` field alongside `r.launcher`) received exactly one plan, equal to what `r.launcher`'s canned `Plan` returned — proving the new `Focus.Focus(plan)` wiring actually threads through, not just that `Plan` was called.

- [ ] **Step 5: Run the core/agent package tests**

  Run: `go test ./core/agent/ ./core/agent/agenttest/...`
  Expected: PASS. (Do not run `./core/agent/...` recursively yet — that also builds `core/agent/terminal`, which doesn't compile against the new `TerminalPlanner` interface until Task 2.)

- [ ] **Step 6: Commit**

  ```bash
  git add core/agent/traits.go core/agent/runtime.go core/agent/agenttest/fakes.go core/agent/runtime_test.go core/agent/runtime_worker_test.go core/agent/runtime_worker_session_test.go core/agent/runtime_roster_test.go
  git commit -m "agent: core decides a FocusPlan instead of executing it (#116)"
  ```

---

### Task 2: `WarpPlanner`/`ItermPlanner`/`ReusingPlanner` build the plan (`core/agent/terminal`)

**Files:**
- Modify: `core/agent/terminal/terminal.go` (`WarpLauncher`→`WarpPlanner`, `ItermLauncher`→`ItermPlanner`)
- Modify: `core/agent/terminal/reuse.go` (`ReusingLauncher`→`ReusingPlanner`)
- Modify: `core/agent/terminal/terminal_test.go`, `core/agent/terminal/reuse_test.go`
- Modify: `core/agent/terminal/live_warp_test.go`

**Interfaces:**
- Consumes: `agent.FocusPlan`, `agent.NewTerminalAction`, `agent.TerminalPlanner` (Task 1).
- Produces: `NewWarpPlanner(dir string) *WarpPlanner`, `NewItermPlanner() *ItermPlanner`, `Reuse(inner agent.TerminalPlanner, runner agent.CommandRunner) *ReusingPlanner` — all implement `agent.TerminalPlanner` with `Plan(a agent.Attach) (agent.FocusPlan, error)`. Used by Task 3's `registry.go`.

- [ ] **Step 1: Rewrite the failing/changed tests in `terminal_test.go` first**

  Every test that today calls `.Open(...)` and inspects the fake runner's `ran` calls instead calls `.Plan(...)` and inspects the returned `agent.FocusPlan`. `NewWarpPlanner`/`NewItermPlanner` drop the `CommandRunner` parameter entirely (neither needs to run anything anymore — only `ReusingPlanner` still shells out, for `ps`). Rewrite, keeping each test's name and intent:

  ```go
  func TestWarpPlanWritesExactlyThisTabConfigAndReturnsItsURL(t *testing.T) {
      dir := filepath.Join(t.TempDir(), "tab_configs")
      plan, err := NewWarpPlanner(dir).Plan(attach())
      if err != nil { t.Fatal(err) }
      b, err := os.ReadFile(filepath.Join(dir, "deskpixie_attach.toml"))
      // ... same `want` TOML string as today (lines 74-76 unchanged) ...
      if want := &agent.NewTerminalAction{Kind: "url", URL: "warp://tab_config/deskpixie_attach"}; !reflect.DeepEqual(plan.NewTerminal, want) {
          t.Fatalf("%+v", plan)
      }
      // ... same temp-file-left-behind check (lines 89-92) ...
  }
  ```
  `TestWarpEscapesQuotesForToml` and `TestWarpDoesNotRewriteAnUnchangedTabConfig` are unchanged (they call `WarpTabConfig`/`NewWarpPlanner(...).Plan(...)` directly against the file on disk, not the runner).

  `TestItermRunsTheExpectedAppleScript` → `TestItermPlanReturnsTheExpectedAppleScript`: assert `plan.NewTerminal` equals `&agent.NewTerminalAction{Kind: "applescript", Script: ItermScript(attach())}` (drop the `fakeRunner`/`r.ran` assertions entirely — nothing runs).

  `TestAFailingLaunchIsAnErrorNamingTheTerminal`: Warp's half no longer applies (writing the TOML can still fail — e.g. an unwritable directory — but there is no runner call left to fail); keep an equivalent for an unwritable `dir` (`os.MkdirAll` fails), and keep iTerm's half exactly (`ItermPlanner.Plan` with control characters still returns `could not open iTerm: the command contains control characters`, since that check doesn't involve the runner either — see `TestControlCharactersAreEscapedForTomlAndRejectedForIterm` and `TestItermRefusesAControlCharacterInTheFolder`, both unchanged in spirit, just call `.Plan` and check the returned `error`, not `r.ran`).

  `TestLaunchersReportTheirNames`: `var _ agent.TerminalPlanner = (*WarpPlanner)(nil)`; `var _ agent.TerminalPlanner = (*ItermPlanner)(nil)`; `NewWarpPlanner("").Name()`/`NewItermPlanner().Name()` unchanged (`"warp"`/`"iterm"`).

  All other tests in the file (`TestPlainWordsStayPlainAndOthersAreQuoted`, `TestWordsStartingWithEqualsAreQuoted`, `TestBackslashesAndAppleScriptSpecialsAreEscaped`, `TestAnOddStartingFolderIsQuotedForTheShellAndEscapedForToml`, `TestNoStartingFolderMeansNoDirectoryAndNoCd`) test pure string functions (`ShellLine`, `AttachLine`, `WarpTabConfig`, `ItermScript`) that aren't touched — unchanged.

- [ ] **Step 2: Run the new tests to see them fail**

  Run: `go test ./core/agent/terminal/ -run TestWarpPlan -v`
  Expected: FAIL (`NewWarpPlanner` undefined)

- [ ] **Step 3: Implement `WarpPlanner`/`ItermPlanner` in `terminal.go`**

  Rename `WarpLauncher`→`WarpPlanner` (drop its `runner` field), `NewWarpLauncher(r, dir)`→`NewWarpPlanner(dir string) *WarpPlanner`. `Plan` keeps `WarpLauncher.Open`'s TOML-write logic (lines 135-151) verbatim, replacing the final `return run(w.runner, []string{"/usr/bin/open", url}, "Warp")` with:
  ```go
  return agent.FocusPlan{NewTerminal: &agent.NewTerminalAction{Kind: "url", URL: url}}, nil
  ```
  (and the `fail` closure's `agent.LaunchError("could not open Warp: " + e.Error())` now returns `agent.FocusPlan{}, agent.LaunchError(...)`).

  Rename `ItermLauncher`→`ItermPlanner` (drop its `runner` field), `NewItermLauncher(r)`→`NewItermPlanner() *ItermPlanner`. `Plan` keeps the control-character validation (lines 174-181) verbatim; instead of building and running the `osascript` command, returns:
  ```go
  return agent.FocusPlan{NewTerminal: &agent.NewTerminalAction{Kind: "applescript", Script: ItermScript(a)}}, nil
  ```

- [ ] **Step 4: Run the `terminal.go` tests**

  Run: `go test ./core/agent/terminal/ -run 'TestWarpPlan|TestItermPlan|TestLaunchersReportTheirNames|TestAFailingLaunch' -v`
  Expected: PASS

- [ ] **Step 5: Rewrite `reuse_test.go`'s expectations around returned plans, not runner calls**

  `ReusingLauncher`→`ReusingPlanner`; `Open`→`Plan`. `countingLauncher`/`titledLauncher` (the test doubles) become `countingPlanner`/`titledPlanner` implementing `Plan(agent.Attach) (agent.FocusPlan, error)` returning a canned `agent.FocusPlan{NewTerminal: &agent.NewTerminalAction{Kind: "url", URL: "fake://new"}}` and incrementing `opened` — the ps-table fixtures (`warpTable`, `warpEnv`) are unchanged.

  Each test now asserts the returned `agent.FocusPlan`, and `psRunner.ran` only ever contains `ps`/`ps -E` calls (nothing else — no `osascript`/`open`/`kill`, since none of those run in Go anymore):

  - `TestAnAttachedWarpSessionIsShownByItsFocusURL`: `plan.FocusURLs == []string{"warp://session/0d845f78c7214b4aa4fe0c2df7ec636c"}` (newest client first — only one candidate here since only `621`'s env has a valid URL ahead of `611`'s... actually both have valid URLs: order is newest-first, so `["warp://session/0d845f78c7214b4aa4fe0c2df7ec636c", "warp://session/1af8eb188540461cb7150e6a371d0e63"]`), `plan.RaiseScript` non-empty (still built, since Swift tries it if every FocusURLs candidate fails at runtime — Go can't know in advance), `plan.KillPIDs == []int{611, 621}`, `plan.NewTerminal` set, `inner.opened == 0`, and `r.ran` contains only the `ps`/`ps -E` calls (no `/usr/bin/open`, since that's now the app's job, not Go's).
  - `TestAWarpFocusURLThatFailsFallsBackToTheWindowRaise` → rename to `TestAWarpPlanCarriesBothTheFocusURLAndTheRaiseScript` (Go can no longer observe the open "failing" at plan-build time — there is nothing left to fall back to *in Go*; the fallback now happens live, in the app's executor, which Task 4 tests instead). Assert the plan has both `FocusURLs` and `RaiseScript` populated, `KillPIDs` and `NewTerminal` populated, and `inner.opened == 0`.
  - `TestAFocusURLThatIsNotAWarpSessionIsIgnored`: `plan.FocusURLs` is empty (both candidates' env values are filtered out by the existing `warpFocusURL` regex, unchanged), `plan.RaiseScript` non-empty.
  - `TestAnAttachedWarpTabsWindowIsRaised` → the `raiseWindow` AppleScript-building logic doesn't change; assert `plan.RaiseScript` contains the same four substrings the test checks today (`"/Applications/Warp.app"`, `name is "DeskPixie"`, `AXRaise`, `frontmost`), `plan.FocusURLs` empty (no `warpEnv` in this fixture), `plan.KillPIDs == []int{611,621}`, `plan.NewTerminal` set.
  - `TestWithNoWarpWindowShowingTheAttachTheOldOnesEndAndANewOneOpens` → rename to `TestWithNothingToRaiseThePlanStillCarriesKillAndNewTerminal` (the "fallback" is no longer something Go observes happening — Go always includes `KillPIDs`/`NewTerminal` in a Warp-attached plan, letting the app decide at runtime whether it's needed). Assert `plan.KillPIDs == []int{611, 621}` and `plan.NewTerminal` set; `inner.opened == 0` (Go's `inner.Plan` is still called to build `NewTerminal`, but that doesn't count as "opening").
  - `TestAnAttachedTerminalWithoutAWindowTitleIsBroughtForward`: assert `plan.ActivateApp == "/Applications/Warp.app"`, every other field zero, `inner.opened == 0`, `r.ran` has only the `ps` call.
  - `TestAnAttachedItermSessionIsSelectedByItsTty`: assert `plan.ItermSelect` equals `ItermSelectScript("/dev/ttys004")` (same three substring checks as today), every other field zero.
  - `TestWithNothingAttachedANewTerminalOpens`, `TestAnAttachOutsideAnyAppOpensANewTerminal`, `TestAFailingPsOpensANewTerminal`: assert `inner.opened == 1` exactly as today (these go through `l.inner.Plan(a)` directly, unchanged shape).
  - `TestReuseKeepsTheTerminalsName`: unchanged.

  Add one new test for the ordering/filtering Global Constraint: `TestWarpFocusURLCandidatesAreNewestFirstAndSkipClientsWithoutOne` — three clients, only the oldest and newest have a valid `WARP_FOCUS_URL`; assert `plan.FocusURLs` lists exactly those two, newest first.

- [ ] **Step 6: Implement `ReusingPlanner.Plan` in `reuse.go`**

  Keep `attached()` verbatim (pure `ps` parsing, untouched). Replace `Open`'s body:
  ```go
  func (l *ReusingPlanner) Plan(a agent.Attach) (agent.FocusPlan, error) {
      app, tty, clients, ok := l.attached(a.Argv)
      if !ok {
          return l.inner.Plan(a)
      }
      titled, isTitled := l.inner.(windowTitled)
      if strings.HasSuffix(app, "/iTerm.app") {
          return agent.FocusPlan{ItermSelect: ItermSelectScript("/dev/" + tty)}, nil
      }
      if !isTitled {
          return agent.FocusPlan{ActivateApp: app}, nil
      }
      plan := agent.FocusPlan{FocusURLs: l.warpFocusURLs(clients), RaiseScript: raiseWindowScript(app, titled.WindowTitle()), KillPIDs: clients}
      newTerminal, err := l.inner.Plan(a)
      if err != nil {
          return agent.FocusPlan{}, err
      }
      plan.NewTerminal = newTerminal.NewTerminal
      return plan, nil
  }
  ```
  Split today's `openFocusURL` (which both finds AND opens) into a pure finder:
  ```go
  // warpFocusURLs is every attach client's own WARP_FOCUS_URL (read via `ps -E`), newest first,
  // clients without one skipped.
  func (l *ReusingPlanner) warpFocusURLs(clients []int) []string {
      var urls []string
      for i := len(clients) - 1; i >= 0; i-- {
          // same `ps -E -ww -o command= -p <pid>` call and `warpFocusURL` regex match as today's
          // openFocusURL (lines 70-79), appending m[1] instead of trying to open it.
      }
      return urls
  }
  ```
  Split today's `raiseWindow` (which both builds AND runs the AppleScript) into a pure builder:
  ```go
  // raiseWindowScript is the System Events AppleScript that raises app's window titled title.
  func raiseWindowScript(app, title string) []string {
      // the exact `[]string{...}` literal from today's raiseWindow (lines 91-100), minus the
      // run(...) call — made a free function (no receiver needed, matching ItermSelectScript).
  }
  ```
  Delete `bringForward`, `l.runner` field's use in `Open`/`raiseWindow`/`openFocusURL` (the `ReusingPlanner` struct keeps `runner agent.CommandRunner` for `attached()`'s `ps` call only), and the `kill` command construction (moved to the app — Task 4).

- [ ] **Step 7: Run the `terminal` package tests**

  Run: `go test ./core/agent/terminal/ -v`
  Expected: PASS (excluding the `-tags live` test, which Step 8 handles)

- [ ] **Step 8: Retarget `live_warp_test.go`**

  The live test can no longer call `l.Open(attach)` and let Go perform the real `/usr/bin/open`/`osascript` calls — Go only builds a plan now. Add a small test-only executor in this file (real `os/exec`, not going through `CommandRunner`) that performs exactly what the Swift executor will do, so the test still proves the *plan* is correct against a real attached Warp session:
  ```go
  // executeLive performs exactly the primitives the Swift app will perform, for this live test's
  // own verification (there is no Swift test target in this repo).
  func executeLive(t *testing.T, plan agent.FocusPlan) {
      t.Helper()
      for _, u := range plan.FocusURLs {
          if exec.Command("/usr/bin/open", u).Run() == nil {
              return
          }
      }
      if len(plan.RaiseScript) > 0 {
          cmd := append([]string{"-e"}, ...) // osascript -e <line> -e <line> ...
          if out, err := exec.Command("/usr/bin/osascript", cmd...).Output(); err == nil && strings.TrimSpace(string(out)) == "ok" {
              return
          }
      }
      for _, pid := range plan.KillPIDs {
          exec.Command("/bin/kill", "-HUP", strconv.Itoa(pid)).Run()
      }
      if plan.NewTerminal != nil && plan.NewTerminal.Kind == "url" {
          exec.Command("/usr/bin/open", plan.NewTerminal.URL).Run()
      }
  }
  ```
  Replace the test's two `l.Open(attach)` calls (lines 110, 152) with `plan, err := l.Plan(attach); ...; executeLive(t, plan)`. The rest of the test (burying the tab, checking `warpTitle()`/`frontApp()`, the `r.take()` assertions on what the *recordingRunner* saw) stays conceptually the same, except `r.take()` after the second `Plan`+`executeLive` now only ever shows the `ps`/`ps -E` calls `Plan` made (since `executeLive` uses its own un-recorded `exec.Command`, not `r`) — change the assertion at lines 157-165 to instead check `plan.FocusURLs` is non-empty and that `executeLive` didn't need to reach `KillPIDs`/`NewTerminal` (add a return value or t.Log from `executeLive` saying which rung it used, and assert it was the focus-URL rung).

- [ ] **Step 9: Commit**

  ```bash
  git add core/agent/terminal/
  git commit -m "terminal: planners decide a FocusPlan instead of executing it (#116)"
  ```

---

### Task 3: Go bridge wiring — hooks, `reportFocusOutcome`, registry (`core/app`, `core/app/agentapp`)

**Files:**
- Modify: `core/app/agentapp/registry.go` (`TerminalFactory` return type; `Terminals()` body)
- Modify: `core/app/agentapp/desktop.go` (new `focusSink` adapter)
- Modify: `core/app/agentapp/agentapp.go` (deps construction; new `Agent.ReportFocusOutcome`)
- Modify: `core/app/agent.go` (`AgentHooks.Focus`; `Agent.ReportFocusOutcome`)
- Modify: `core/app/app.go` (hooks literal; new `agent.reportFocusOutcome` bridge registration)
- Modify: `core/app/agentapp/agentapp_test.go`, `core/app/agentapp/wiring_test.go`

**Interfaces:**
- Consumes: `agent.FocusPlan`, `agent.TerminalPlanner`, `agent.FocusSink` (Task 1); `terminal.NewWarpPlanner`/`NewItermPlanner`/`Reuse` (Task 2).
- Produces: the `focusPlan` event (consumed by Task 5's `EngineCoordinator`); the `agent.reportFocusOutcome` bridge method (called by Task 4's executor).

- [ ] **Step 1: `registry.go`**

  `TerminalFactory func(runner agent.CommandRunner, home string) agent.TerminalLauncher` → return type `agent.TerminalPlanner`. `Terminals()`'s two map entries (lines 110-116):
  ```go
  "warp": func(r agent.CommandRunner, home string) agent.TerminalPlanner {
      return terminal.Reuse(terminal.NewWarpPlanner(filepath.Join(home, ".warp/tab_configs")), r)
  },
  "iterm": func(r agent.CommandRunner, _ string) agent.TerminalPlanner {
      return terminal.Reuse(terminal.NewItermPlanner(), r)
  },
  ```

- [ ] **Step 2: `AgentHooks.Focus` and `Agent.ReportFocusOutcome` in `core/app/agent.go`**

  In `AgentHooks` (next to `ShowPane func(job, worker string)`): add `Focus func(plan any)`.
  In `Agent` interface (next to `FocusAgent(name string)`): add `// ReportFocusOutcome: the shell finished executing a FocusPlan (#116). Failure becomes the tray status line, as for OpenTerminal.` then `ReportFocusOutcome(success bool, detail string)`.

- [ ] **Step 3: `focusSink` adapter in `desktop.go`**

  Next to `paneSink` (lines 77-84):
  ```go
  // focusSink: a FocusPlan to show → the app's `focusPlan` event.
  type focusSink struct{ hooks app.AgentHooks }

  func (f focusSink) Focus(plan agent.FocusPlan) {
      if f.hooks.Focus != nil {
          f.hooks.Focus(plan)
      }
  }
  ```

- [ ] **Step 4: Wire `Deps.Planner`/`Deps.Focus` and add `ReportFocusOutcome` in `agentapp.go`**

  In the `agent.Deps{...}` literal (line ~324-340): rename `Launcher: termFactory(c.Runner, c.Home),` → `Planner: termFactory(c.Runner, c.Home),`; add `Focus: focusSink{c.Hooks},`.

  Next to `OpenTerminal`/`FocusJob`/`FocusAgent` (after line 643): add
  ```go
  // ReportFocusOutcome: the shell finished executing a FocusPlan (#116). A failure becomes the
  // tray status line through the same path as an OpenTerminal failure, so wording never diverges.
  func (a *Agent) ReportFocusOutcome(success bool, detail string) {
      if !success {
          a.setStatus(TerminalFailureStatus(detail))
      }
  }
  ```

- [ ] **Step 5: `app.go` — emit `focusPlan`, register `agent.reportFocusOutcome`**

  In the hooks literal (next to `ShowPane: func(job, worker string) { a.emit("showPane", map[string]any{"job": job, "worker": worker}) }`, line 234): add
  ```go
  Focus: func(plan any) { a.emit("focusPlan", map[string]any{"plan": plan}) },
  ```
  Next to `r.Register("agent.focusAgent", ...)` (after line 1205): add
  ```go
  r.Register("agent.reportFocusOutcome", func(args json.RawMessage) (any, error) {
      var in struct {
          Success bool   `json:"success"`
          Detail  string `json:"detail"`
      }
      if err := decodeArgs("agent.reportFocusOutcome", args, &in); err != nil {
          return nil, err
      }
      if a.agent != nil {
          a.agent.ReportFocusOutcome(in.Success, in.Detail)
      }
      return struct{}{}, nil
  })
  ```

- [ ] **Step 6: Fix `agentapp_test.go`/`wiring_test.go`'s `FakeLauncher` references**

  `agentapp_test.go:44`, `wiring_test.go:112,122`: `Launcher: &agenttest.FakeLauncher{}` → `Planner: &agenttest.FakePlanner{}, Focus: &agenttest.FakeFocusSink{}` (or the equivalent struct-field form at `wiring_test.go:112`). The existing assertions at `wiring_test.go:301-302,306,309,517,519` (`f.launcher.Opened()`, `.Attaches()`, `.SetFail(...)`) are unchanged — only the type name changes to `FakePlanner`, per Task 1 Step 3.

  Add one new test in `wiring_test.go` next to the existing failure test (around line 311): calling `f.agent.ReportFocusOutcome(false, "could not open Warp: no app")` and asserting `waitFor(t, "the failure line", func() bool { return f.agent.Tray().Status == "Coordinator: could not open Warp" })` — proving the new bridge method reaches the tray through the existing `TerminalFailureStatus` parsing (the `could not open X: reason` → `Coordinator: could not open X` rule already tested for `OpenTerminal`).

- [ ] **Step 7: Run the affected packages' tests**

  Run: `go test ./core/app/... ./core/agent/...`
  Expected: PASS

- [ ] **Step 8: Commit**

  ```bash
  git add core/app/agentapp/registry.go core/app/agentapp/desktop.go core/app/agentapp/agentapp.go core/app/agent.go core/app/app.go core/app/agentapp/agentapp_test.go core/app/agentapp/wiring_test.go
  git commit -m "app: push FocusPlan to the shell, report its outcome back (#116)"
  ```

---

### Task 4: Swift `FocusPlan` decode + `TerminalFocusExecutor` (`mac/Packages/DeskPixieCore`)

**Files:**
- Create: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/TerminalFocusExecutor.swift`
- Modify: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Bridge/EngineEvent.swift` (new `.focusPlan` case)
- Create: `mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/TerminalFocusExecutorTests.swift`

**Interfaces:**
- Consumes: `EngineEvent` (existing `decode`/switch pattern, e.g. `.showPane`/`PaneRequest` at `EngineEvent.swift:86,134`).
- Produces (used by Task 5):
  ```swift
  public struct FocusPlan: Decodable, Equatable, Sendable { /* mirrors Go's FocusPlan, snake_case keys */ }
  public struct NewTerminalAction: Decodable, Equatable, Sendable { let kind: String; let url: String?; let script: [String]? }
  public protocol TerminalFocusPrimitives: Sendable {
      func openURL(_ url: String) -> Bool
      func runAppleScript(_ lines: [String]) -> String?   // nil = failed; else trimmed stdout
      func isAccessibilityTrusted(prompt: Bool) -> Bool
      func killPID(_ pid: Int32)
  }
  @MainActor public final class TerminalFocusExecutor {
      public init(primitives: TerminalFocusPrimitives, reportOutcome: @escaping (Bool, String) -> Void)
      public func execute(_ plan: FocusPlan)
  }
  ```

- [ ] **Step 1: Add `FocusPlan`/`NewTerminalAction` and the `.focusPlan` case to `EngineEvent.swift`**

  Add to the `EngineEvent` enum (next to `showPane`): `case focusPlan(FocusPlan)`. Add a private decode struct and switch case, matching the existing `agents`/`showPane` pattern:
  ```swift
  private struct FocusPlanEvent: Decodable { var plan: FocusPlan }
  // ... in decode(_:)'s switch:
  case "focusPlan": return get(FocusPlanEvent.self).map { .focusPlan($0.plan) }
  ```
  `FocusPlan`/`NewTerminalAction` themselves are defined in `TerminalFocusExecutor.swift` (Step 2) since that's where they're consumed; `EngineEvent.swift` only references the type.

- [ ] **Step 2: Write `TerminalFocusExecutorTests.swift` first (TDD)**

  A `FakeTerminalFocusPrimitives: TerminalFocusPrimitives` recording every call and answering from canned per-call results (one test double covers all four methods, similar in spirit to `psRunner` in Go's `reuse_test.go`). One test per plan shape, each asserting (a) which primitive calls happened, in what order, (b) the final `reportOutcome(success, detail)` call:

  - `TestItermSelectAloneHasNoFallback`: a plan with only `itermSelect` set; the fake's `runAppleScript` fails; assert ONLY `runAppleScript` was called (no `openURL`, no `killPID`) and `reportOutcome(false, _)`.
  - `TestActivateAppAloneHasNoFallback`: a plan with only `activateApp` set; `openURL` fails; assert only `openURL` was called and `reportOutcome(false, _)`.
  - `TestFocusURLsTriedInOrderUntilOneSucceeds`: a plan with `focusURLs: ["a", "b", "c"]`; fake fails "a", succeeds "b"; assert `openURL` called with "a" then "b" (not "c"), no `runAppleScript`/`killPID`, `reportOutcome(true, _)`.
  - `TestAllFocusURLsFailFallsThroughToRaise`: `focusURLs` all fail, `raiseScript` set, fake's `runAppleScript` returns `"ok"`; assert the raise was attempted (with a trust check) and `reportOutcome(true, _)`, `killPID` never called.
  - `TestRaiseScriptSucceedsOnlyWhenStdoutIsExactlyOk`: `raiseScript` set, fake's `runAppleScript` returns `"none"` (ran fine, found nothing); assert this counts as NOT successful — falls through to `killPID`+`newTerminal`.
  - `TestAccessibilityPromptedOnceThenNotNagged`: two plans with `raiseScript` executed back to back on the same executor instance; fake's `isAccessibilityTrusted` is called with `prompt: true` the first time and `prompt: false` the second.
  - `TestWarpFallbackKillsThenOpensNewTerminal`: `focusURLs`/`raiseScript` both fail/absent, `killPIDs: [1,2]`, `newTerminal` a `url` kind; assert `killPID` called for both pids (in order) before `openURL` for the new terminal, and `reportOutcome(true, _)`.
  - `TestNewTerminalApplescriptKind`: same as above but `newTerminal.kind == "applescript"`; assert `runAppleScript` (not `openURL`) is the final call.
  - `TestNothingApplicableReportsFailure`: an all-empty `FocusPlan`; assert `reportOutcome(false, _)` with no primitive calls at all.

- [ ] **Step 2b: Run the new tests to see them fail**

  Run: `swift test --filter TerminalFocusExecutorTests` (from `mac/Packages/DeskPixieCore`)
  Expected: FAIL (`TerminalFocusExecutor` undefined)

- [ ] **Step 3: Implement `FocusPlan`/`NewTerminalAction`/`TerminalFocusPrimitives`/`TerminalFocusExecutor` in `TerminalFocusExecutor.swift`**

  `FocusPlan`'s `Decodable` keys match Go's tags exactly: `focus_urls`, `raise_script`, `iterm_select`, `activate_app`, `kill_pids`, `new_terminal`.

  `execute(_:)` walks the rungs in this exact order (one reasonable Swift body from the signatures/tests above — the order itself is the Review Focus item, not left open):
  1. `itermSelect` non-empty → `runAppleScript`; report its success/failure directly; return.
  2. else `activateApp` non-empty → `openURL`; report directly; return.
  3. else: try each of `focusURLs` in order via `openURL`, stop and report success on the first `true`.
  4. if none succeeded and `raiseScript` non-empty: `isAccessibilityTrusted(prompt: !alreadyPrompted)` (set `alreadyPrompted = true` the first time, regardless of the trust result), and if trusted, `runAppleScript(raiseScript)`; treat success as `result == "ok"` (not just non-nil).
  5. if still not shown: run `killPID` for every `killPIDs` entry (ignore individual failures — there is no way to observe one from a `kill(2)` signal in this design, and none is needed), then execute `newTerminal` if present (`openURL` for `"url"`, `runAppleScript` for `"applescript"`) and report that result.
  6. if `newTerminal` is nil at step 5 (true only for the `itermSelect`/`activateApp` cases, already returned by then, or a FocusPlan with nothing set at all): report failure.

  `detail` on failure: a short fixed string per rung is enough (e.g. `"could not show the terminal"`); exact wording is a builder's call (the design doc already leaves Accessibility-prompt copy and detail text open) as long as `TerminalFailureStatus` on the Go side still prefixes it with "Coordinator: " sensibly.

  A real `SystemTerminalFocusPrimitives: TerminalFocusPrimitives` (not test-covered beyond compiling — it only wraps `NSWorkspace.shared.open`, `NSAppleScript.executeAndReturnError`, `AXIsProcessTrustedWithOptions`, and Darwin's `kill(2)`): add it in the same file.

- [ ] **Step 4: Run the tests**

  Run: `swift test --filter TerminalFocusExecutorTests`
  Expected: PASS

- [ ] **Step 5: Commit**

  ```bash
  git add mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/TerminalFocusExecutor.swift mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Bridge/EngineEvent.swift mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/TerminalFocusExecutorTests.swift
  git commit -m "DeskPixieCore: TerminalFocusExecutor walks a core-built FocusPlan (#116)"
  ```

---

### Task 5: Wire the executor into `EngineCoordinator` and report the outcome (`mac/DeskPixie`)

**Files:**
- Modify: `mac/DeskPixie/Engine/EngineCoordinator.swift`

**Interfaces:**
- Consumes: `EngineEvent.focusPlan` (Task 4); `TerminalFocusExecutor`/`SystemTerminalFocusPrimitives` (Task 4); `engine.call` (existing `CoreEngine.call(_:_:)`, as used by `UpdatesBubbleController.swift:113-122`'s `call` helper).

- [ ] **Step 1: Construct the executor and wire the new event case**

  Alongside the coordinator's other owned pieces (e.g. `notifier`, built once at init — see `Notifier()` construction near where `case .notify: notifier.handle(ev)` is wired): add a `let terminalFocus: TerminalFocusExecutor`, constructed with `SystemTerminalFocusPrimitives()` and a `reportOutcome` closure that mirrors `UpdatesBubbleController.swift:113-122`'s `call(_:_:)` pattern — fire-and-forget on the worker queue, logging (not throwing) on a bridge error:
  ```swift
  reportOutcome: { [weak self] success, detail in
      guard let self else { return }
      let engine = self.engine
      self.worker.async {
          do { _ = try engine.call("agent.reportFocusOutcome", ReportFocusOutcomeArgs(success: success, detail: detail)) }
          catch { Self.log.error("agent.reportFocusOutcome: \(String(describing: error), privacy: .public)") }
      }
  }
  ```
  (`ReportFocusOutcomeArgs: Encodable, Sendable { let success: Bool; let detail: String }`, a small private type next to the closure, matching `FocusJobArgs`/`SeenArgs` in `UpdatesBubbleController.swift`.)

  In `handle(_ ev: EngineEvent)`'s switch (next to `case .showPane(let r): bubble?.showPane(r)`): add
  ```swift
  case .focusPlan(let plan):
      terminalFocus.execute(plan)
  ```

- [ ] **Step 2: Build and smoke-check**

  Run: `cd mac && xcodebuild -scheme DeskPixie -destination 'platform=macOS' build` (or the project's existing build command/skill — check for a project-specific `run`/build skill before falling back to raw `xcodebuild`)
  Expected: builds clean.

  Manually: click Pixie with a terminal already attached (Warp and iTerm, if both are available) and confirm it's still brought forward exactly as before the refactor; detach everything and confirm a new terminal still opens. This is the one step in the whole plan that isn't unit-testable — it's exercising real NSWorkspace/AppleScript/Accessibility code for the first time outside a fake.

- [ ] **Step 3: Commit**

  ```bash
  git add mac/DeskPixie/Engine/EngineCoordinator.swift
  git commit -m "DeskPixie: execute the core's FocusPlan and report back (#116)"
  ```

---

## Out of scope (unchanged from the design doc)

- The Accessibility permission *onboarding* flow — only the one-time in-the-moment prompt from Task 4.
- #130 (the coordinator `focus_session` tool) — follows once this plan lands.
