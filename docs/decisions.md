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
