# DeskPixie: Port to Go core + Swift shell, shared with Howl

Date: 2026-10-13
Status: **Handoff draft.** This is the roadmap and reuse inventory; it is not yet an approved spec. The next session starts from §9.

**Background.**
- The product is being renamed from "AI Mascot" to **DeskPixie**. The mascot character stays "Bit". The decision is logged in `docs/decisions.md`.
- On 2026-10-11 the user decided to port the app to the layout Howl (`~/Documents/Projects/voice-keyboard`) already uses: a **Go core built as a C-ABI dylib, hosted by a Swift (SwiftUI/AppKit, xcodegen) macOS app**. That decision is also in `docs/decisions.md`. The reasons, in order:
  1. Fewer languages to maintain. Rust exists only here and in arbitrage-trader; everything else is Go and/or Swift.
  2. Sharing audio code with Howl.
- Related work in flight:
  - Issue #97 (mic cleanup).
  - PR #99, the spec for the shared Go audio module: `docs/superpowers/specs/2026-10-12-shared-audio-module-design.md` on branch `audio-module-spec`. This document builds on #99 and does not repeat it.

## 1. Goal

DeskPixie runs as a Swift macOS app over a Go core and shares its audio pipeline and generic UI pieces with Howl. It keeps every behaviour the Rust/Tauri app has today:
- voice loop
- mascot packs
- delegation to coding agents
- canvas
- settings

### Success criteria
- **Feature parity** with the Rust app at the time of cut-over: each item in §3 has a home and passing tests.
- **One audio pipeline:** DeskPixie and Howl import the same Go packages (#99). Neither keeps its own denoiser, resampler or speaker code.
- **The rename is complete:** the name "DeskPixie", bundle id `com.sihekuang.deskpixie`, and the user's existing settings, packs and recordings migrated on first launch.
- **Shipped incrementally:** every phase leaves a working app, and the Rust app keeps shipping until cut-over.

### Out of scope
- Windows and Linux. The app is macOS-only today (`audio-macos`, `terminal-macos`).
- New features during the port. Each one waits for cut-over, or lands in the Rust app first and is ported with the rest.

## 2. Benefits and costs (why, and what it takes)

**Benefits**
1. **One language fewer.** After the port, the same skills, tooling (xcodegen, dylib build, signing, release) and debugging habits work in both voice apps.
2. **Shared audio.** A fix to the denoiser, resampler, VAD or voiceprint helps both apps.
   - Howl gains the most: it ships with an empty DeepFilterNet path, so it does no denoising at all, and its Silero model file is broken (#99 §9).
   - The eval harness and speaker-gate experiments then serve both apps.
3. **A native shell.** Swift calls VoiceProcessingIO, AppKit windows, the overlay and Settings directly, instead of going through Rust FFI.
4. **Howl's tooling for free:** recorder/replay, the session store, the CLI harness and the C-ABI JSON pattern, all already debugged.

**Costs and risks**
- **About 48k lines of working Rust and 6k of JS** have to move (§3). Most of it is ai-mascot-specific, and Howl has no equivalent (§3 summary).
- **Real-time audio in Go:** GC pauses and cgo overhead.
  - Howl works, but it is push-to-talk. DeskPixie is always-on full-duplex audio.
  - This is measured in Phase 3 before committing.
- **A new C-ABI boundary** (JSON, callbacks, Swift↔Go threading) that the Rust app doesn't have.
- **Feature work stalls** while the port runs.

**Middle option (keep in mind).** Benefit 2 does not need the full port. The shared Go audio module can be built as a dylib and called from the current Rust app through C. Phase 2 does exactly this. Whether to go on to Phases 3 and later is decision D2 (§8).

## 3. Reuse inventory: where every part of today's app goes

Line counts are Rust lines on `origin/main` 33dcf70.

| Today (Rust/JS) | Lines | What it does | DeskPixie home | From Howl |
|---|---|---|---|---|
| `crates/audio-macos` | 3,067 | VoiceProcessingIO capture + playback (echo cancellation), device pinning, Hush/DF3 `DfModel`, 24k↔16k resampling | **Swift** for capture/playback/devices; **shared Go** `enhance` + `resample` for cleanup | `core/internal/denoise`, `resample` (via #99); `mac/Howl/Engine` audio tap as a reference |
| `crates/voice-core` | 4,196 | Vendor-neutral voice loop: traits, state machine, runtime | **Go** `deskpixie/voice` | — (Howl is push-to-talk dictation, not a realtime conversation) |
| `crates/voice-openai` | 1,254 | OpenAI Realtime WebSocket adapter | **Go** | — |
| `crates/voice-gemini` | 2,677 | Gemini Live WebSocket adapter | **Go** | — |
| `crates/voice-tools` | 2,038 | Local tools: time, weather, calculate (#85) | **Go** | — |
| `crates/agent-core` | 12,008 | Delegation: jobs, routing, confirm words, coordinator inbox, controller | **Go** (pure logic; port the tests first) | — |
| `crates/agent-fs` | 2,051 | Claude Code folder trust, worker git worktrees, launch files | **Go** | — |
| `crates/agent-http` | 804 | Loopback HTTP server for Claude Code hooks (Stop, PermissionRequest, PostToolUse) | **Go** (`net/http`) | — |
| `crates/session-herdr` | 4,432 | Herdr Unix-socket SessionHost (protocol 17) | **Go** | — |
| `crates/terminal-macos` | 259 | Open Warp / iTerm attached to a session | **Swift** (NSWorkspace / AppleScript) | — |
| `crates/canvas-core` | 2,089 | Canvas page store, URL→file resolver, CSP | **Go** core + **Swift** WKWebView window | — |
| `crates/mascot-pack` | 2,235 | Pack manifests and sprite generators (Bit, Dino, Black Gold, Flash) | **Go** (or keep as a build-time tool) | — |
| `src-tauri` | 10,782 | App shell: windows, tray, hotkey, bridge, settings I/O, canvas window, proximity/layout, agent wiring | **Swift** shell + Go `libdeskpixie` C-ABI | `mac/Howl` AppDelegate, MenuBar, Composition, EngineCoordinator pattern; `core/cmd/libhowl` C-ABI pattern |
| `ui/mascot` (JS) | — | Mascot player | **Swift** native renderer (§4.3) | oled-saver-macos proximity fade |
| `ui/settings` (JS) | — | Settings page | **SwiftUI** | `mac/Howl/UI/Settings/*` (§4.2) |

**Summary.**
- **Taken from Howl:**
  - the whole audio-cleanup layer (#99)
  - the app frame and C-ABI pattern
  - about half of the Settings UI
- **Ported, ai-mascot-specific, with no Howl equivalent (about 34k lines of Rust):**
  - voice loop and providers
  - delegation/agents
  - Herdr
  - canvas
  - packs

The crates are already split along traits with no Tauri dependency (`voice-core`, `agent-core` say "pure" in their docs), so each one ports on its own, tests first.

### 3.1 Noise cancellation and speaker isolation

These come entirely from the shared module (#99 §2).

| Piece | Status |
|---|---|
| **Noise cancellation** (`enhance`): DeepFilterNet via libdf. | Ready to share. ai-mascot's side is the more mature one and flows Rust → Go: Hush model, 18 dB attenuation limit, silence dither, pass-through on failure. |
| **Echo cancellation** | Not shared code: Apple's VoiceProcessingIO, switched on by each app's Swift capture. |
| **Voiceprint** (ECAPA, enrollment, store) | Reusable as is: Howl `core/internal/speaker`. |
| **VAD** (Silero v4) | Reusable, but Howl's model file is a failed download; ship a verified file with a pinned checksum. |
| **Progressive speaker gate** | Experimental and **off**. It did not meet the ship rule on #97: 0.5–2 dB of background reduction, and 6–13% of the user's speech muted at SIR 6/0. |
| **Howl SpeakerGate** (ConvTasNet target speaker extraction) | Not usable live as written. It processes a whole push-to-talk chunk and hard-picks one of two separated sources. Streaming it in short windows risks the choice flipping between windows, plus added latency. It is the only real separation either app has, so it is the next speaker-isolation experiment (Phase 2b), not a promise. |

## 4. Architecture

### 4.1 Layout

```
deskpixie/                       (repo; renamed from ai-mascot, see §6)
  core/                        Go module: github.com/sihekuang/deskpixie/core
    cmd/libdeskpixie/            C-ABI dylib (-buildmode=c-shared), JSON configure + callbacks, as libhowl
    cmd/deskpixie-cli/           headless harness (voice loop, replay), as howl-cli
    voice/  voice/openai/  voice/gemini/  tools/
    agent/  agent/fs/  agent/http/  herdr/
    canvas/  packs/  config/
  mac/                         Swift app, xcodegen project.yml
    DeskPixie/  (AppDelegate, DataMigration, Engine/, Composition/, UI/Mascot, UI/Settings, UI/MenuBar, UI/FirstRun)
  mascots/                     packs (mascot.json + sheet.png), unchanged
```

The audio packages are **not** in this tree. They come from the shared module (#99 §10 Q1: either a new repo or `voice-keyboard/core/pkg`).

**Swift owns:**
- mic capture and playback (VoiceProcessingIO)
- windows, overlay, tray, hotkey, permissions
- terminals

**Go owns:**
- everything else
- the audio cleanup chain between the capture and the voice provider

Swift → Go: 16 kHz frames (or 24 kHz, then `resample`).
Go → Swift: playback audio and UI events, as callbacks.

### 4.2 UI reuse from Howl (`voice-keyboard/mac/Howl`, about 8.5k lines of SwiftUI/AppKit)

| Howl | DeskPixie use |
|---|---|
| `UI/Settings/SettingsComponents.swift`, `SettingsView.swift` | Native Settings frame; replaces `ui/settings`. Supersedes `2026-10-10-native-mac-settings-design.md` (which chose Rust `objc2-app-kit`); keep that spec's *behaviour* rules: save on change, restart bar, inline errors, broken-file banner. |
| `UI/Settings/HotkeyTab.swift` | Shortcut recorder for the voice toggle (today ⌃B) |
| `UI/Settings/VoiceTab.swift`, `EnrollmentSheet.swift` | Voiceprint enrollment (pairs with the shared `voiceprint`) |
| `UI/Settings/Pipeline/*` (StageList, WAVPlayer, CompareView, TSELabView) | Audio pipeline inspector: the debug UI the #97 noise and gate work needed |
| `UI/Settings/OpenAISection.swift`, `AnthropicSection.swift`, `UI/FirstRun/APIKeyPanel.swift` | Provider and key settings; Gemini needs a new section |
| `UI/FirstRun/*` (AccessibilityPanel, ModelDownloader, ModelDownloadPanel) | First run: mic/accessibility permission, downloading the Hush/ECAPA/Silero models |
| `UI/MenuBar/*`, `CueSound.swift` | Tray menu and audio cues |
| `UI/Overlay/WaveformView.swift` | Optional mic-level indicator |
| `Engine/EngineCoordinator.swift`, `Composition/CompositionRoot.swift` | Swift↔Go bridge pattern. **Adapt** it, don't copy it: it is shaped around Howl's engine. |
| `DataMigration.swift` | Pattern for the rename migration (§6). Howl already did VoiceKeyboard → Howl this way. |

**Not reused:** the Howl-only ScreenContext, Dictionary, Prompt and Playground tabs.

**DeskPixie-only UI:**
- the transparent mascot overlay window with `player.js`
- canvas windows
- proximity/layout
- the terminal launch

**How the code is shared.** Copy-and-adapt by default, since Howl's tabs are bound to its settings model. If the two apps keep evolving together, the generic parts can move into a shared Swift package (D4, §8):
- SettingsComponents
- the hotkey recorder
- enrollment
- the pipeline inspector
- the model downloader

### 4.3 The mascot

**Decided 2026-10-13 (D5): native Swift.** The packs (`mascot.json` + `sheet.png`) are unchanged. `player-core.js` (frame stepping, bob, mouth overlay, click/double-click/drag gestures) is ported to Swift with its tests, and drawn with Core Animation layers (nearest-neighbour scaling) in a borderless, transparent NSPanel. Click-through-where-empty uses the sheet's alpha under the cursor; the proximity fade and the 60 Hz cursor poll are borrowed from oled-saver-macos (`BallPhysics.proximityOpacity`, MIT, same author).

### 4.4 Voice prompts: Bit hands work to the coordinator

DeskPixie owns its voice prompts. They started as ai-mascot's and were rewritten here; ai-mascot's copies are not kept in sync.
- `core/voice/assets/persona.md`: who Bit is. Embedded in Phase 3.
- `core/agent/assets/delegation.md`: when and how Bit hands work off. Embedded in Phase 5, appended to the persona when the agent is on.
- The `delegate` tool text (Phase 5, `core/agent`):
  - description: "Hand a task to the capable agent working in the background. Use it for any real work: research, facts to check, live information (time, weather, news, prices), the user's projects, files, code, repositories, writing, summaries, charts, diagrams or pages. The agent does not hear the conversation, so the task and context must stand on their own. Call this BEFORE saying you will hand something off. Returns at once with a job id; the result arrives later as an "Agent reply:" line."
  - `task`: "What the user wants done, in their words." (required)
  - `context`: "Everything from the conversation the agent needs, since it cannot hear it: what pronouns refer to, names, numbers, choices, earlier Agent replies."

**The rule.** Bit is the voice; the coordinator does the work. Bit offloads as much as it can, but not everything.
- **Delegated:** anything that needs more than what Bit already knows or what was said in the conversation. That includes research, files, code, the user's projects, tasks, messages and documents, drafting, summarising, comparing, and visuals. When Bit is unsure, it delegates.
- **Kept by Bit:** small talk, feelings, jokes and opinions; simple, stable common knowledge; repeating what the user just said; one clarifying question; ending the conversation; the job tools (`job_status`, `cancel_job`, `answer_job`). Handing these off would only add delay.
- **How Bit hands off** (taken from DeepSeek Harness's `subagent` tool guidance):
  - The task and context stand on their own, since the coordinator can't hear the conversation.
  - Each independent task is its own `delegate` call.
  - A follow-up to earlier work is new work.
  - Bit doesn't poll a running job.

**Tests.** Each embedding package checks the phrases the behaviour depends on:
- persona: "Bit", "end_conversation"
- delegation: every tool name, "Agent reply:", "Agent question:", "confirm", "Check: ", "Let me check that.", "weather", "Never tell the user to check"
- the `delegate` description: "live information"

## 5. Phases

Each phase ends with a working app and its own spec → plan → review cycle. **Phase 1 is in flight. Nothing from Phase 2 onward has started.**

| Phase | Work | Ends with |
|---|---|---|
| **1. Finish #97** | User's live mic check with Hush on (a quiet voice starts a turn; interrupting Bit works at normal and quiet volume); listening check of the gate spike page; answers to #99 §10; merge #99 | Hush 18 dB shipping in the Rust app; the module spec approved |
| **2. Shared Go audio module** | Build #99: move Howl's `audio`/`resample`/`denoise`/`speaker` out of `internal/`; add `enhance` (Hush), `vad`, `voiceprint`, `cleanup`, `eval`; **Howl adopts it first** | Howl denoises for the first time; WER checked with `eval` |
| 2a. *(optional, the middle option)* | Rust app calls the Go `cleanup` dylib through C instead of `DfModel` | Both apps on one pipeline without the port |
| 2b. *(experiment)* | Streaming test of Howl's ConvTasNet SpeakerGate on the #97 bench | A go/no-go for real speaker isolation |
| **3. Skeleton + go/no-go** | `mac/DeskPixie` xcodegen app + `core/cmd/libdeskpixie`; Swift VoiceProcessingIO capture → Go cleanup → OpenAI Realtime → playback; mascot WKWebView; tray + hotkey. **Measure** latency, glitches and GC pauses against the Rust app | A talking Bit in the new stack, and a numbers-based go/no-go for the rest |
| **4. Voice parity** | voice-core state machine, Gemini adapter, local tools, settings (SwiftUI from Howl), first-run, data migration (§6) | Daily-drivable DeskPixie without delegation |
| **5. Delegation parity** | agent-core, agent-fs, agent-http, session-herdr, terminal launch, worker flow (#93 lands here or in Rust first) | Feature parity |
| **6. Cut-over** | Release pipeline (signing, notarization) from Howl's setup, rename the repo, retire `src-tauri` + crates | DeskPixie 1.0; Rust removed |

**Porting rule for every crate:** port its tests first, as Go table tests with the same cases, then the code. A crate counts as ported when its old test cases pass in Go.

## 6. The rename to DeskPixie

**Scope (user's choice, 2026-10-13): full rename.** That covers:
- the visible name
- the bundle id
- the data folder, with a migration
- the default Herdr session

What is still open is *when* the technical part lands (D3, §8): in today's Rust app, or with the Swift app in Phase 3.

### 6.1 What changes

| Item | Today | DeskPixie | Notes |
|---|---|---|---|
| Product name | "AI Mascot" | "DeskPixie" | `tauri.conf.json` `productName`, window/tray titles, `Info.plist` usage texts, README, design-system docs |
| Mascot | "Bit" | "Bit" (unchanged) | — |
| Bundle id | `com.sihekuang.aimascot` | `com.sihekuang.deskpixie` | `tauri.conf.json` `identifier`, and every `Application Support/com.sihekuang.aimascot` path in README, `design-system/CANVAS.md`, `design-system/empty.html` |
| Data folder | `~/Library/Application Support/com.sihekuang.aimascot/` (`settings.json`, `mascots/`, `canvas/`, `agent/`, `recordings/`) | `…/com.sihekuang.deskpixie/` | Migrated on first launch (§6.2) |
| Default Herdr session | `ai-mascot` | `deskpixie` | `config.rs` default; the migration also rewrites a `settings.json` that still names `ai-mascot` |
| Rust package / lib | `ai-mascot` / `ai_mascot_lib` | `deskpixie` / `deskpixie_lib` | `src-tauri/Cargo.toml`, `main.rs`, `Cargo.lock` |
| Identifiers in code | `ai-mascot` | `deskpixie` | MCP `serverInfo.name` (agent-http), weather user agent (voice-tools), Warp tab stem `ai_mascot_attach` (terminal-macos), temp-file and smoke-session prefixes, Herdr test fixtures |
| User-facing strings | "AI Mascot isn't allowed to control …", "the AI Mascot's coordinator" | "DeskPixie …" | voice-tools `media.rs`, `assets/agent/coordinator.md`, agent-core `recipes.rs` worker prompt, settings page error text |
| Native settings classes | `AIMascotSettings*` (ObjC class, frame and tab keys) | `DeskPixieSettings*` | `settings/native/{controls,window}.rs` |
| Release | `APP_NAME: AI Mascot`, `AI-Mascot-<v>-arm64.dmg` | `DeskPixie`, `DeskPixie-<v>-arm64.dmg` | `.github/workflows/release.yml` |
| Secrets | API keys from env / `.env` (no Keychain today) | Keychain, service "DeskPixie", as Howl does | Phase 4 |
| GitHub repo | `sihekuang/ai-mascot` | `sihekuang/deskpixie` | The user renames it (GitHub keeps redirects); then update the URLs |
| Local folder | `~/Documents/Projects/ai-moscot` | the user's choice | Any time; `git worktree repair` afterwards |
| Go module | — | `github.com/sihekuang/deskpixie/core` | Phase 3 |

**Left as is:**
- Historical specs in `docs/superpowers/specs/`: they are records.
- The invalid-session test case `"Ai Mascot"` in `agent/registry.rs`: it is test data.

The sweep touches about 41 files, found with `git grep -i 'ai[- ]mascot\|aimascot\|ai_mascot\|AIMascot'`.

### 6.2 Data migration

This is the Howl `DataMigration` pattern; Howl did VoiceKeyboard → Howl the same way. It runs first on launch, before anything reads the data folder.
1. **Runs only when needed:** the new folder is missing or empty, and the old folder exists. This makes it idempotent and means it never overwrites DeskPixie's own data.
2. **Copies, never moves:** an older build still finds its data, so rollback is free.
3. **Copies into a temporary sibling (`.com.sihekuang.deskpixie.migrating-<pid>`), then renames it into place:** a crash mid-copy leaves no half-filled folder. Symlinks are copied as links.
4. **Rewrites `agent.session` in the copied `settings.json` to `deskpixie`, but only when it is exactly `ai-mascot`:**
   - a custom session name is kept
   - a broken file is left alone, and the existing broken-file banner handles it
5. **Tests cover:** a plain copy, the old folder kept, an empty new folder, a new folder with data (untouched), no old folder, a second run (no-op), symlinks, the session rewrite, a custom session, and broken JSON.

In the Rust app this is a small `src-tauri/src/migrate.rs` called at the top of `setup`. In the Swift app it is `DataMigration.swift`. Whichever ships first owns it, and the other only needs it if it uses a different id.

### 6.3 Side effects for the user (one time)

- **macOS asks again** for microphone access and for Automation access to Music and Spotify. These permissions are tied to the bundle id.
- **A fresh coordinator session `deskpixie` starts in Herdr.** The old `ai-mascot` session keeps running until it is closed by hand.
- **The old data folder stays** until it is deleted by hand.
- **The Settings window's saved size and tab reset**, because its keys move with the bundle id.

## 7. Testing

- **Go:** the ported crate tests (§5 rule), plus Howl-style integration tests behind build tags for anything needing models or network.
- **Audio:** the #99 `eval` harness (WER and leak) runs on every change to the cleanup chain, for both apps.
- **Swift:** unit tests for the migration, the settings model and the engine bridge. A manual live voice check per phase (the #97 checklist: quiet voice starts a turn, barge-in at normal and quiet volume).
- **Phase 3 go/no-go numbers:** mic → provider latency, playback underruns, and the max GC pause over a 10-minute conversation, compared against the Rust app on the same machine.

## 8. Open decisions for the user

| # | Decision | Notes |
|---|---|---|
| D1 | #99 §10: where the shared audio module lives (new repo vs `voice-keyboard/core/pkg`); keep or drop the gate stage; whether the port waits for the live checks | Blocks Phase 2 |
| D2 | Full port, or stop at the middle option (Phase 2a) | Decide after Phase 3's numbers at the latest |
| D3 | When the full rename (§6) lands: in today's Rust app (the name changes now; the migration is a Rust module later replaced by Swift's) or with the Swift app in Phase 3 (one migration, but the name waits) | §6 |
| D4 | Copy Howl's UI pieces, or extract a shared Swift package | §4.2 |
| D5 | ~~Mascot renderer~~ **Decided 2026-10-13: native Swift** | §4.3 |

## 9. Handoff: state as of 2026-10-13, and where to start

**Repos and checkouts**
- DeskPixie / ai-mascot: `~/Documents/Projects/ai-moscot`, repo `sihekuang/ai-mascot`, `main` at 33dcf70.
- Howl: `~/Documents/Projects/voice-keyboard` (Go `core/`, Swift `mac/`).

**Open work**
- **#97** (mic cleanup): agent side done (status in comment 6131309875). Waiting on the user's live mic check and their answers to #99 §10.
- **PR #99** (`audio-module-spec`): the shared audio module spec, with gate v2 results in §7.1. Open.
- **Branch `hush-gate-fix`** (pushed, no PR): gate v2 spike and bench, `spike/hushgate`. The listening page is at `spike/hushgate/local/out/index.html` (local only, gitignored).
- **Other open issues:**
  - #93 (selectable coordinator; first backend is Codex)
  - #96 (strategy; its research doc is still untracked in the main checkout under `docs/research/`)
  - #85 / #82 (local tools)

**Housekeeping still pending** (the user's OK was asked for, not yet given)
- Remove the merged worktrees: `.claude/worktrees/agent-*` (4), `refactor-shared-audio`, `spike-hush-gate`, `.worktrees/hush`, `settings-page`, `audio-spec`, `live-check`, and `../ai-moscot-build-investigate`.
- Move the main checkout off the merged `mic-recording` branch onto `main`.
- Commit `docs/research/similar-projects-2026-10-12.md`.

**Next session, in order**
1. Collect D1 and the #97 live-check result. Close #97 and merge #99.
2. Write the Phase 2 plan from #99 (writing-plans), in the module's home chosen in D1.
3. Write the Phase 3 spec (skeleton + go/no-go) from §4 and §5 of this document.

**Constraints the user has set**
- Never print or commit API keys (`.env` is gitignored); subagents never read `.env`.
- Path-scoped commits; worktrees under `.worktrees/`; never use a bare `git stash`.
- Merge reviewed PRs without asking, after a subagent review. Don't push tags or trigger release workflows.
- Architectural work goes spec → user approval → plan.
- Specs follow SOLID, with traits/interfaces at every external boundary.
