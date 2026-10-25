# Decisions

## 2026-10-13 — Repo: new private sihekuang/deskpixie
**Decision:** The Go + Swift port lives in a new private repo `sihekuang/deskpixie`; ai-mascot keeps shipping until cut-over.
**Basis:** User's choice.

## 2026-10-13 — Start with Phase 3, audio cleanup behind an interface
**Decision:** Build the Phase 3 skeleton now with a pass-through `audio.Cleaner`; adopt the shared module (#5) once D1 is decided.
**Basis:** User's choice.

## 2026-10-13 — D5: native Swift mascot renderer
**Decision:** Draw the mascot natively (Core Animation) instead of `player.js` in a WKWebView; borrow proximity fade + cursor polling from oled-saver-macos.
**Basis:** User's choice.

## 2026-10-13 — Minimal code review
**Decision:** No per-task reviewer; tests + build gate each task, one light whole-branch check per phase.
**Basis:** User's choice.

## 2026-10-13 — Build pipeline shape and libdf in CI
**Decision:** Two GitHub Actions workflows on `macos-15` (arm64): `ci.yml` (pull requests and pushes to main: Go + Swift tests, unsigned/ad-hoc Release build, `.app.zip` artifact, no secrets) and `release.yml` mirroring ai-mascot's (`v*` tag → Developer ID sign, notarize, staple, `.dmg` + `.app.zip`, GitHub Release; manual dispatch from main → signed smoke build, not notarized), using the same secret names and a `release` environment. libdf v0.5.6 (`libdf.dylib` + `DeepFilterNet3.tar.gz`) is vendored in git under `core/audio/denoise/libdf/`, as Howl vendors it, so CI builds the noise filter without a Rust toolchain.
**Trigger:** User asked for a build pipeline that generates builds.
**Basis:** Existing project convention.
**Sources:**
- ai-mascot `.github/workflows/release.yml` (origin/main 33dcf70): tag/dispatch triggers, temp keychain, notarytool, dmg signing, artifact names, `release` environment.
- Howl `core/third_party/deepfilter/` (libdf v0.5.6 committed, 16 MB + 7.6 MB) and its `build.yml`.

## 2026-10-13 — Bit offloads work to the coordinator by default
**Decision:** The voice agent delegates anything that needs more than what it knows, what was said and its local tools (time, weather, math), and delegates when unsure. It keeps small talk, simple facts, one clarifying question, ending the conversation and the job tools. Delegated tasks stand on their own (the coordinator can't hear the conversation); independent requests are separate delegate calls; follow-ups are new work; Bit doesn't poll running jobs.
**Basis:** User's choice; handoff mechanics follow DeepSeek Harness's `subagent` tool guidance (github.com/deepseek-ai/deepseek-harness).

## 2026-10-13 — Keep agents running when the app quits
**Decision:** `agent.keepPanesOnQuit` defaults to on. A quit saves the coordinator's and live workers' tokens and plans to a private `agent/kept.json`; the next start restores the tokens, adopts the still-running coordinator (no replace, no `--continue`) and workers, keeps their jobs open, and asks them to re-send results lost while the app was down (spec `docs/superpowers/specs/2026-10-13-keep-agents-on-quit.md`).
**Basis:** User's choice.

## 2026-10-13 — GPT-Live adapter mechanics
**Decision:** Backend model travels in SessionConfig (no ModelFactory change); the backend prompt reuses Bit's full instructions instead of splitting delegation.md; unanswered tool calls don't hold a reply open.
**Trigger:** Implementing the approved GPT-Live spec (PR #50).
**Basis:** Existing project convention: the conformance suite (core/voice/voicetest/conformance.go) and runtime answer tool calls after ResponseDone; SessionConfig already carries per-session provider settings (core/voice/types.go).

## 2026-10-13 — Bit delegates eagerly (#57)
**Decision:** Delegating is Bit's default: every request goes to the coordinator except small talk, simple stable facts Bit is certain of, what the user just said, Bit's own tools (time, weather, math, timers, music) and the job/recall tools. Bit doesn't try first, hesitate or ask permission; a vague request is delegated with what Bit has, and Bit asks only when it can't tell what the task is at all. The persona, delegation rules, delegate tool description and the "Recent work with the agent" block all say the same, with examples, and tests forbid the old hedging lines. Provider tool calling stays on auto (Gemini sends no toolConfig; OpenAI uses tool_choice auto), since forcing a tool call would break small talk.
**Basis:** User's choice (issue #57); supersedes the "one clarifying question" part of the entry above.

## 2026-10-14 — Updates bubble: no Focus / Do Not Disturb check
**Decision:** The bubble skips its pop when a fullscreen app is in front (menu bar hidden) but does not detect Focus / Do Not Disturb.
**Trigger:** Spec §4 asks for badge-only during Focus; the only API, INFocusStatusCenter, needs the Communication Notifications entitlement and a permission prompt.
**Basis:** Planning ruling; revisit if Focus suppression is wanted enough to add the entitlement.

## 2026-10-14 — API keys entered in Settings: Keychain, looked up after the environment
**Decision:** Keys typed into Settings are stored in the macOS Keychain (service "DeskPixie", account = the variable name, e.g. OPENAI_API_KEY). Lookup order: process environment → Keychain → .env files.
**Trigger:** #59; the user did not answer the two design questions and set the goal to finish the discussed features.
**Basis:** Storage: existing project convention — docs/specs/2026-10-13-deskpixie-port-design.md §Secrets row ("Keychain, service \"DeskPixie\", as Howl does", Phase 4). Environment first: unanimous practice that an environment variable overrides stored credentials:
- GitHub CLI, `gh help environment`: GH_TOKEN/GITHUB_TOKEN "take precedence over previously stored credentials" — https://cli.github.com/manual/gh_help_environment
- AWS CLI, "Configuration and credential file settings — precedence": environment variables before the shared credentials file — https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-files.html
- Google Cloud, Application Default Credentials: GOOGLE_APPLICATION_CREDENTIALS checked before the gcloud-stored credentials — https://cloud.google.com/docs/authentication/application-default-credentials
Keychain before .env is a ruling (no source settles it): a key typed into the app is the most deliberate, recent choice; .env still works when no key was typed. Easy to flip if wrong.

**Update (same day):** the user confirmed Keychain storage and set the order to Keychain → environment → .env ("what the user sees goes first"), with Settings always naming the source in use. This supersedes the environment-first order above.

## 2026-10-14 — tmux host: status from Claude hooks + Codex screen reads
**Decision:** Under tmux, Claude agents report working/idle/blocked through Claude Code hooks to DeskPixie's hook server; Codex status is read from the screen with the existing busy/ready markers; exits come from tmux pane state.
**Trigger:** #64; tmux has no agent-status events. The owner hadn't yet approved the proposed design; the active goal says to keep working without pausing.
**Basis:** Ruling. Hooks are exact for Claude (Herdr's own Claude integration uses hooks the same way); Codex has no hooks, and its screen markers are already used by the controller. Revisit if the owner prefers screen-only detection.

**Update (same day):** the owner reviewed and approved the tmux design, including hook-based Claude status.

## 2026-10-14 — Hand off when Bit doesn't know (#71)
**Decision:** A safety net in the shared conversation layer (core/voice, so every provider gets it): when Bit's reply to a user turn is a non-answer ("I don't know", "I'm not sure", "I can't check…", "I don't have access…"), no tool was called in that turn, and the user's words were a question or request (not small talk, thanks, a goodbye or a question about Bit itself), the runtime calls the delegate tool with the user's words (earlier turns as context) and Bit is told "Agent: handed off: <task>". If delegation is off, Bit is told "Agent: can't hand off: delegation is off (<status>)." and says why. Once per turn, 1.5 s after the reply (late transcripts and late tool calls still count), never while ending. English-only, conservative phrase matchers. Setting `voice.handOffWhenUnsure`, on by default, applies on restart.
**Basis:** Owner-approved design on issue #71.

## 2026-10-14 — Check Bit's claims; knowledge boundary (#72)
**Decision:** The #71 hook also runs a claim check, in the same shared conversation layer and with the same per-turn state, timing (1.5 s after the reply and its tools finished) and once-per-turn guard. When Bit's words in a turn claim an action whose tool wasn't called in it (first-person statements only; never questions, offers, conditionals, negations or sentences about earlier turns; English, conservative), the app corrects it: a delegate claim ("I've handed that off", "I asked the agent", "I started a job", "let me hand that off"; "let me check that" as a "Check: " fact) to a request is made true by handing the user's words to the delegate tool, quietly if it works; a timer or media claim gets "Agent: correction: …" so Bit fixes it itself (no guessed arguments), withdrawn if the tool is called late. Only tools in the registry are covered; there is no memory-saving tool, so "I'll remember that" is not covered. Never while ending or for Bit's reply to a notice. A delegate call in a turn the app already handed off is answered "already handed off" without starting a second job (GPT-Live's backend can call late). Setting `voice.checkClaims`, on by default, applies on restart. The persona now draws the knowledge boundary (the user's world and anything current come from a tool or the agent; general knowledge and chat stay with Bit; if unsure, delegate), and job status comes only from the jobs tool and Agent lines.
**Basis:** Owner-approved scope on issue #72 (parts 1–3).

## 2026-10-16 — #116 terminal-focus split: Swift runs the whole fallback chain in one local pass
**Decision:** When terminal-raise/focus execution moves from Go into Swift (issue #116), Swift runs the entire ordered fallback chain itself in one pass (Warp focus URL → AX window raise, after an Accessibility-trust check → iTerm tty-select → kill stale clients + open a new terminal), stopping at first success — not Go orchestrating it step-by-step across separate round trips through the `dp_call`/`dp_poll_event` bridge. Go still decides *what* is possible (the `ps`-based attach detection, which rungs apply) and builds every artifact up front — Warp TOML, AppleScript text, URLs, the pids to kill — in the single response to the triggering `dp_call`; Swift just walks the ladder and, on total failure, posts to the tray itself.
**Trigger:** User asked "so how do we want to do the split?", following up on whether a failed Swift-side AX-raise should fall back locally (mirroring today's Go behavior) or stop and report.
**Basis:** Existing project convention — `core/agent/terminal/reuse.go`'s `ReusingLauncher.Open` already implements this exact chain (focus URL → raiseWindow → kill+reopen) as one function call/one pass at the execution site, with no external step-by-step orchestration. Moving the execution site from Go to Swift under #116 directly extends that pattern; it's also the only option the bridge supports today, since Swift→Go `dp_call` is synchronous but Go→Swift `dp_poll_event` is async-only — Go has no way to synchronously drive Swift step-by-step without new plumbing.

**Update (same day):** a builder-check of the resulting design doc found the delivery premise wrong — `pixie.click`, `agent.focusJob`, `agent.focusAgent`, and the tray's `agent:open` (via the generic `app.action` dispatcher) are all fire-and-forget (`core/app/agentapp/agentapp.go`'s `a.wg.Go`; `core/app/agent.go`'s methods are documented `(async)` with no return value) — there is no synchronous call to return a `FocusPlan` from. Decided: the core pushes the finished `FocusPlan` to the app as a new kind on the existing async `dp_poll_event` queue, the same mechanism `notify`/`showPane` already use for "core decides, app shows." This is trigger-agnostic, so it covers all four click paths (including the tray item, which #116 explicitly lists) with one new event case, without making any triggering call blocking (avoiding head-of-line blocking on Swift's shared `CoreWorker` queue) and without touching any of the four call sites individually. After executing the plan, the app makes one new synchronous call back reporting success/failure, so a total failure still posts to the tray through the core's existing `TerminalFailureStatus` path/wording instead of Swift duplicating it.
**Basis:** Existing project convention (the `notify`/`showPane` push-event pattern) plus avoiding a new blocking-queue problem the original premise would have introduced.

## 2026-10-24 — Settings window layout (#290)
**Decision:** Seven sidebar pages (General, Voice, Coordinator, Sessions, Workers, Notifications, Advanced), each split into named groups with a heading, at most 15 rows a page; rarely used settings move to Advanced; units sit next to numbers. Go defines pages, groups and order once (FieldSpec.Group, Extras with a group); the window draws them. Judgement calls, made by Claude at the user's request ("U make recommendations"):
- Keep the sidebar (System Settings, Raycast and Windows use one; HIG's toolbar tabs don't fit 7 pages).
- Keep the label/control columns and add group headings, not System Settings' grouped cards (Apple's macOS `Form` convention; least rework).
- A setting under a master switch that's off is greyed out with a note naming the switch (`enabledWhen`); a setting that doesn't apply to a choice stays hidden (`visibleWhen`).
- Keep the help text under each row (Windows and Android do; DeskPixie's settings need the explanation).
- Canvas joins the Coordinator page (the coordinator makes canvas pages).
- A new Sessions page holds pix and sessions you start yourself.
**Basis:** Unanimous points from Apple HIG (Settings), Android settings guidelines, Microsoft's app settings guidelines, NN/g (progressive disclosure), UX Movement and macOS System Settings; the rest are Claude's calls as above.
**Sources:** see #290 (research report).
