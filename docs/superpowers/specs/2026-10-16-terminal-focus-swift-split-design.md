# Move terminal opening/focusing from the Go core into the Swift app (#116)

Status: proposed, not yet implemented. Precedes #130 (a coordinator `focus_session` tool),
which builds on this.

## 1. Goal

Today `core/agent/terminal` (`WarpLauncher`, `ItermLauncher`, `ReusingLauncher`) is pure Go that
shells out to `/usr/bin/open`, `osascript` and `ps` to open and raise terminals (#98, #110,
#115). This is macOS-only code sitting inside the core, which is meant to be the shared,
platform-neutral part — the thing a Windows/Linux shell would have to dig out whole. It also
means every permission problem (AppleScript's "System Events" needs Accessibility) is discovered
only by the AppleScript failing; there's no upfront check or user-facing prompt anywhere today.

The original port design (`docs/specs/2026-10-13-deskpixie-port-design.md` §3) already intended
`crates/terminal-macos` to land in **Swift** (NSWorkspace / AppleScript); it landed in Go
instead. This is finishing that split, not inventing a new one.

Success: identical user-visible behaviour to today (click Pixie / an update line / a roster row
→ the right terminal comes forward, or a new one opens if none is attached) — but the only code
touching AppleScript, NSWorkspace, or Accessibility lives in Swift, and the core's job shrinks to
deciding *what* to show.

## 2. Decisions (from the design discussion)

- **Sequencing:** this refactor lands before #130 (the coordinator-facing `focus_session` tool);
  #130 is built on top of whatever interface this produces, not the other way round.
- **Split:** the core decides everything (attach detection via `ps`, which fallback applies,
  every string/URL/AppleScript it needs) and builds it all into one plan; the app executes that
  plan start to finish in one local pass and reports nothing back except to the tray on total
  failure. Settled by evidence, logged in `docs/decisions.md` ("#116 terminal-focus split:
  Swift runs the whole fallback chain in one local pass") — existing project convention, since
  `ReusingLauncher.Open` already runs its whole fallback chain as one function call/one pass
  today, and the bridge has no mechanism for the core to drive the app step-by-step anyway
  (Swift→Go `dp_call` is synchronous; Go→Swift `dp_poll_event` is async-only).
- **How the plan reaches Swift (corrected after builder-check):** none of the four click paths
  are a synchronous call the core can simply return a value from — `pixie.click`, the
  `agent.focusJob`/`agent.focusAgent` bridge methods, and the tray's `agent:open` (via the
  generic `app.action` dispatcher) are all fire-and-forget (`core/app/agentapp/agentapp.go`'s
  `a.wg.Go`, `core/app/agent.go`'s methods documented `(async)` with no return value). Rather
  than make them synchronous (risking head-of-line blocking on Swift's shared `CoreWorker`
  queue, which several unrelated bridge calls share), the core pushes the finished `FocusPlan`
  to the app as a new kind on the existing async `dp_poll_event` queue — the same mechanism
  `notify` and `showPane` already use for "core decides, app shows." This is trigger-agnostic,
  so all four click paths (including the tray item, which #116 explicitly lists) are covered by
  one new case in `EngineCoordinator`'s event switch, with no change to any of the triggering
  calls' fire-and-forget shape. After executing the plan locally, the app makes one new,
  ordinary synchronous `dp_call` back reporting success or failure (not per-rung — one call,
  after the whole local walk finishes), so a total failure still posts to the tray through the
  core's existing `TerminalFailureStatus` path and wording, instead of Swift minting its own.
  Basis: existing project convention (the `notify`/`showPane` push-event pattern) plus avoiding
  a new blocking-queue problem the doc's original premise would have introduced.
- **Why the app must own the kill-stale-clients step:** only the side actually attempting each
  rung knows, at the moment it tries, whether that rung succeeded. If the core killed the old
  attach clients speculatively before handing over the plan, it could kill a client an AX-raise
  was about to successfully bring forward. So the plan carries the pids to kill, but killing them
  is the app's job, done only once every earlier rung in the plan has failed.

## 3. The contract: `FocusPlan`

The core builds one `FocusPlan` per focus request and pushes it to the app as a new event kind
on the existing `dp_poll_event` queue (e.g. `focusPlan`), the same way `notify` and `showPane`
already carry a core decision to the app. The triggering click — `pixie.click`, `agent.focusJob`,
`agent.focusAgent`, or the tray's `agent:open` — stays exactly the fire-and-forget call it is
today; only the background work that already happens after it (`EnsureSession` →
`focusQuietly` → build-the-plan) gains, at the end, one `emit("focusPlan", plan)`. The app walks
the plan top to bottom, stopping at the first rung that shows something:

```go
// FocusPlan: how to show a terminal already attached, or open a new one. The core builds every
// field; the app only executes, trying each present rung in order and stopping at the first
// that shows something. The rungs are NOT uniform across launchers, matching today's exact
// behaviour (verified against reuse_test.go, including TestAnAttachedTerminalWithoutAWindowTitleIsBroughtForward,
// which exercises the generic branch below — it is real, tested code, not dead code):
//   - iTerm attached (app ends `/iTerm.app`): only ItermSelect is set. No fallback — a failed
//     tty-select AppleScript today just fails outright (`bringForward`'s iTerm branch returns
//     directly; `ReusingLauncher.Open` never reaches the kill+reopen code below it).
//   - Attached to anything else that isn't "titled" (today: nothing real — Warp is titled, so
//     this is only hit by a future third launcher — but it's real, tested code): only
//     ActivateApp is set (the attached app's path). Also no fallback, same reason.
//   - Warp attached (titled, not iTerm): FocusURLs and/or RaiseScript are set, plus KillPIDs and
//     NewTerminal for the fallback (today's `openFocusURL` → `raiseWindow` → kill clients →
//     `inner.Open`).
//   - Nothing attached: only NewTerminal is set (today's `l.inner.Open(a)`, nothing to kill).
type FocusPlan struct {
    FocusURLs   []string           `json:"focus_urls,omitempty"`    // Warp attached: try each in order (newest client first)
    RaiseScript []string           `json:"raise_script,omitempty"`  // Warp attached: then run this prebuilt AX-raise AppleScript
    ItermSelect []string           `json:"iterm_select,omitempty"`  // iTerm attached: the ONLY rung, no fallback — prebuilt AppleScript
    ActivateApp string             `json:"activate_app,omitempty"`  // attached to a non-titled, non-iTerm app: the ONLY rung, no fallback
    KillPIDs    []int              `json:"kill_pids,omitempty"`     // Warp fallback only: end these before NewTerminal
    NewTerminal *NewTerminalAction `json:"new_terminal,omitempty"`  // set only when Warp's fallback applies, or nothing is attached
}

// NewTerminalAction is tagged by Kind: "url" (Warp tab-config URL, already written to disk) or
// "applescript" (iTerm: create a window).
type NewTerminalAction struct {
    Kind   string   `json:"kind"`
    URL    string   `json:"url,omitempty"`
    Script []string `json:"script,omitempty"`
}
```

`RaiseScript`/`ItermSelect` carry the AppleScript's exact lines (as `osascript -e` would take them), already built by the core — not raw parameters (app path, window title, tty) for the app to template itself. Keeping every AppleScript-quoting decision in Go (the same `quoted()` helper `reuse.go` already uses) means the app never needs to know AppleScript's string-escaping rules at all; it only ever runs lines it was handed. `FocusURLs` is a list, not one URL, because today's `openFocusURL` tries every attach client's own candidate URL in turn (newest first, skipping clients without one) until one actually opens — which client's `/usr/bin/open` call will succeed is a runtime fact the app discovers, not something the core can predict when it builds the plan.

The core still does everything it does today to build these fields: the `ps` scan for an
attached client (`attached()`), reading `WARP_FOCUS_URL` from the environment, writing the Warp
TOML tab config (idempotent — only rewritten if changed, as today), building the iTerm
AppleScript text. None of that needs macOS frameworks; it's string/file work the core keeps.

The app's job shrinks to four primitives, none of which exist in Swift yet:

1. Open a URL (`NSWorkspace.shared.open`).
2. Run an AppleScript and report whether it succeeded (`NSAppleScript`, replacing `osascript`
   for the AX-raise and iTerm-select scripts the core still builds the text of).
3. Check (and, once, prompt for) Accessibility trust before attempting an AX-raise
   (`AXIsProcessTrustedWithOptions`).
4. Send a pid a signal (`kill(2)` via Darwin — same `SIGHUP` semantics as today's Go
   `CommandRunner` kill).

## 4. Architecture

### 4.1 Core (Go)

- `core/agent/terminal`: `WarpLauncher`/`ItermLauncher`/`ReusingLauncher` become plan builders —
  same detection and string-building logic, new return type (`FocusPlan`, not "execute and
  return error"). Exact type/function names are left to the builder.
- The three existing call sites (`AgentHandle.OpenTerminal`, `FocusJob`, `FocusAgent` in
  `core/agent/runtime.go`) change from `Launcher.Open(Attach{...})` (returns `error`, used
  directly for `TerminalFailureStatus`) to `Planner.Plan(Attach{...})` (returns `FocusPlan`,
  `error`): an error here is a *pre-plan* failure (e.g. `EnsureSession`/`focusQuietly`) and keeps
  going through today's `TerminalFailureStatus` exactly as now; success emits `focusPlan` to the
  app instead of having already executed anything.
- `agent.TerminalLauncher` in `core/agent/traits.go` is replaced by an equivalent planner
  interface; `core/app/agentapp/registry.go`'s `Terminals()` factory map updates accordingly.
- `CommandRunner` keeps the `ps` calls (attach detection); it loses the `/usr/bin/open` and
  `osascript` calls, which move to the app.
- A new, small bridge method (e.g. `agent.reportFocusOutcome {success, detail}`), called once by
  the app after it finishes walking a plan. On `success: false`, the core posts to the tray
  through the same `TerminalFailureStatus` path/wording pre-plan errors already use, so that
  text stays in one place instead of being duplicated in Swift.

### 4.2 App (Swift)

- A small new type (e.g. `TerminalFocusExecutor` in `mac/Packages/DeskPixieCore`) decodes
  `FocusPlan` from the new `focusPlan` event and walks whichever rungs are present: `ItermSelect`
  alone (no fallback) · or `ActivateApp` alone (no fallback) · or each of `FocusURLs` in turn →
  `RaiseScript` (trust check first; if untrusted, show the standard macOS prompt once, then fall
  through; success means the script's own output is exactly `"ok"`, not just "ran without
  error") → kill `KillPIDs` + `NewTerminal` · or `NewTerminal` alone when nothing else is set.
  When it finishes, it calls `agent.reportFocusOutcome` once with the result — not per rung.
- `EngineCoordinator`'s event switch (`mac/DeskPixie/Engine/EngineCoordinator.swift`) gains one
  case, `.focusPlan(let plan): executor.execute(plan)`. Because the event is trigger-agnostic,
  this single case covers all four click paths without touching
  `PixieController.swift`/`UpdatesBubbleController.swift`/`AgentRosterController.swift`/the
  tray's `app.action("agent:open")` handler individually — none of them need to change at all.

## 5. Error handling

- A rung that isn't present (e.g. no `FocusURLs` for an iTerm plan) is skipped, not treated as
  failure.
- `ItermSelect` or `ActivateApp` failing has no fallback — `NewTerminal` is absent from those
  plans, matching today exactly (see §3). The executor reports `success: false` directly.
- Accessibility not trusted: the AX-raise rung is skipped after the one-time prompt, falling
  through to `KillPIDs`/`NewTerminal` exactly as a failed raise does today. Exact prompt copy and
  how often to re-prompt after a decline is left to the builder — a sensible default is "ask
  once per app launch, never nag."
- `KillPIDs` failing to signal a pid (already exited) is not an error; proceed to `NewTerminal`
  regardless.
- Everything present in the plan failing is the only case the app reports as `success: false`;
  the core is the only thing that posts to the tray, for both this and pre-plan failures, so the
  two paths never diverge in wording.

## 6. Testing

- **Go:** `reuse_test.go`/`terminal_test.go`'s existing assertions on exact AppleScript/TOML/URL
  *content* carry over almost unchanged — the core still builds that text, just packages it into
  a `FocusPlan` instead of running it. New assertions check the exact `FocusPlan` value for each
  scenario (attached via Warp with a focus URL, attached via Warp with only a window title,
  attached via iTerm, nothing attached, a failing `ps`) in place of today's "exact shell command
  run" assertions.
- **Swift:** new unit tests for the executor against fakes for each primitive (open URL, run
  AppleScript, check/prompt trust, kill pid): each rung tried in order, stops at first success,
  kill only reached as the last resort, tray posted only on total failure.
- **Live:** `live_warp_test.go` (`-tags live`) needs an equivalent end-to-end check once the
  AppleScript/NSWorkspace calls move to Swift; it can no longer assert purely from Go. Exact
  shape (keep a Go-only test of the plan the live scenario *should* produce, add a manual/Swift
  equivalent, or retarget it as a full app-level smoke test) is left to the builder.

Exact type/function names, the test-by-test rewrite of `terminal_test.go`/`reuse_test.go`, and
`live_warp_test.go`'s retarget (a test-only reference executor, since Go can no longer drive the
real OS calls itself) are fixed in the implementation plan:
`docs/superpowers/plans/2026-10-16-terminal-focus-swift-split.md`.

## 7. Out of scope

- The Accessibility permission *onboarding* flow (a Settings pane section, proactively asking
  before the user ever needs it) — this only adds the one-time in-the-moment prompt.
- Any behaviour change visible to the user; this is an internal split, not a feature.
- #130 (the coordinator `focus_session` tool) — follows once this lands.
