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
