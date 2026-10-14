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

## 2026-10-13 — Bit offloads work to the coordinator by default
**Decision:** The voice agent delegates anything that needs more than what it knows or what was said, and delegates when unsure. It keeps small talk, simple facts, one clarifying question, ending the conversation and the job tools. Delegated tasks stand on their own. DeskPixie owns these prompts (`core/voice/assets/persona.md`, `core/agent/assets/delegation.md`, spec §4.4); ai-mascot's copies are not updated.
**Basis:** User's choice; handoff mechanics follow DeepSeek Harness's `subagent` tool guidance (github.com/deepseek-ai/deepseek-harness).
