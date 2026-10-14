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
