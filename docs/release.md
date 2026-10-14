# Builds and releases

Two GitHub Actions workflows, both on `macos-15` (arm64). The decision is in
`docs/decisions.md` ("Build pipeline shape and libdf in CI").

| Workflow | Runs on | Secrets | Produces |
|---|---|---|---|
| `.github/workflows/ci.yml` | every pull request, every push to `main`, manual dispatch | none | Go + Swift tests, ad-hoc signed Release build, artifact `DeskPixie-<branch>` containing `DeskPixie-0.0.0-ci.<run>-arm64.app.zip` (kept 14 days) |
| `.github/workflows/release.yml` | tag `vX.Y.Z` | `release` environment | tests, Developer ID signed + notarized + stapled `DeskPixie-X.Y.Z-arm64.dmg` and `.app.zip`, GitHub Release |
| `.github/workflows/release.yml` | manual dispatch from `main`, input `notarize` = false (default) | `release` environment | Developer ID signed but **not** notarized (pipeline smoke test), artifact only |
| `.github/workflows/release.yml` | manual dispatch from `main`, input `notarize` = true | `release` environment | signed, notarized and stapled `.dmg` + `.app.zip` as a workflow artifact; **no** GitHub Release |
| `scripts/notarize-local.sh` | this Mac, by hand | login keychain + `~/Documents/AppleSigning` | signed, notarized and stapled `out-local/DeskPixie-<version>-arm64.{dmg,app.zip}`; nothing published |

## Getting a build

**From CI.** Open any CI run for a PR or `main` (Actions tab, or the PR's checks), download the
`DeskPixie-<branch>` artifact, unzip it twice (GitHub zips the artifact; inside is the
`.app.zip`). The app is ad-hoc signed, so Gatekeeper blocks a downloaded copy: right-click >
Open, or `xattr -dr com.apple.quarantine DeskPixie.app`.

```bash
gh run download -R sihekuang/deskpixie --name DeskPixie-<branch>   # latest run's artifact
```

**Locally.**

```bash
make -C mac release VERSION=0.4.0                      # ad-hoc signed
make -C mac release VERSION=0.4.0 \
  SIGNING_IDENTITY="Developer ID Application: <Name> (<TEAMID>)" DEVELOPMENT_TEAM=<TEAMID>
```

Output: `mac/build/Build/Products/Release/DeskPixie.app`. `make release` ends with
`verify-release` (codesign `--deep --strict`, `libdeskpixie.dylib` + `libdf.dylib` in
`Contents/Frameworks`, `DeepFilterNet3.tar.gz` + `design-system` + `mascots` in
`Contents/Resources`, every `@rpath` dependency resolvable, audio-input entitlement). With a
real identity the bundled dylibs are re-signed with the same identity, `--timestamp` and the
hardened runtime, which notarization requires. `BUILD_NUMBER` (CFBundleVersion) defaults to the
CI run number, else 1.

libdf (the DeepFilterNet noise filter) is vendored in `core/audio/denoise/libdf/`, so no Rust
toolchain is needed anywhere. Go comes from `PATH` (CI: `actions/setup-go` reading
`core/go.mod`); xcodegen from Homebrew.

## Cutting a release

1. Merge to `main`, with CI green.
2. Optional smoke test of the signing pipeline: Actions > Release > Run workflow (branch
   `main`). It signs with Developer ID but does not notarize or publish. Tick **notarize**
   (or `gh workflow run release.yml -R sihekuang/deskpixie --ref main -f notarize=true`) to
   also notarize and staple; the result is a workflow artifact, never a GitHub Release.
3. Tag and push (the user does this):

   ```bash
   git tag v0.4.0 && git push origin v0.4.0
   ```

   The tag must match `vMAJOR.MINOR.PATCH`. The workflow runs the tests, builds and signs,
   notarizes the zipped `.app` and staples it, builds the `.dmg` (app + `/Applications`
   symlink) from the stapled app, signs, notarizes and staples the dmg, checks both with
   `spctl --assess`, and creates the GitHub Release with both files and install notes. The
   temporary keychain and API key are deleted in an always-run cleanup step.

If notarization is rejected the step prints Apple's log (`notarytool log`) and fails.

The verify/notarize/package logic lives in `scripts/package-mac.sh` (inputs via env: `APP`,
`VERSION`, `OUT_DIR`, `SIGNING_IDENTITY`, `NOTARIZE`, `NOTARY_KEY_PATH`, `NOTARY_KEY_ID`,
`NOTARY_ISSUER_ID`), so the workflow and the local script run the same steps.

## Notarizing locally

```bash
VERSION=0.0.1-local scripts/notarize-local.sh
```

Mirrors `~/Documents/AppleSigning/sign-and-notarize.sh`: sources `local-notarize.env` for
`APPLE_API_ISSUER_ID`, uses `AuthKey_7X3J6MCQ63.p8` (key id `7X3J6MCQ63`), and picks the
login keychain's "Developer ID Application" identity unless `SIGNING_IDENTITY` is set. It runs
`make -C mac release` with that identity, then `scripts/package-mac.sh` with `NOTARIZE=true`,
writing to `out-local/` (gitignored). It submits to Apple's notary service but publishes nothing.

**Verified 2026-10-13** (`VERSION=0.0.1-local`, this Mac): notarytool `Accepted` for both the
app zip and the dmg; `xcrun stapler validate` on both: "The validate action worked!";

```
$ spctl --assess --type execute -v mac/build/Build/Products/Release/DeskPixie.app
mac/build/Build/Products/Release/DeskPixie.app: accepted
source=Notarized Developer ID
$ spctl -a -t open --context context:primary-signature -v out-local/DeskPixie-0.0.1-local-arm64.dmg
out-local/DeskPixie-0.0.1-local-arm64.dmg: accepted
source=Notarized Developer ID
```

## One-time setup: signing secrets

Same as ai-mascot, Howl and oled-saver: from the repo root run the shared setup script, which
validates the Developer ID `.p12` (temp keychain import) and the App Store Connect API key
(`notarytool history`), then pushes the eight secrets to the repo with `gh secret set`:

```bash
cd ~/Documents/Projects/deskpixie
~/Documents/AppleSigning/setup-notarization.sh
gh secret list -R sihekuang/deskpixie                       # expect all eight
```

The eight are repo secrets (as in ai-mascot): `MACOS_CERT_P12_BASE64`, `MACOS_CERT_PASSWORD`,
`MACOS_KEYCHAIN_PASSWORD`, `MACOS_SIGNING_IDENTITY`, `APPLE_TEAM_ID`, `APPLE_API_KEY_BASE64`,
`APPLE_API_KEY_ID`, `APPLE_API_ISSUER_ID`. The release job declares `environment: release`
(created, no protection rules yet) and can read repo secrets; for stricter protection, later
move them into that environment and restrict it to `main` and `v*` tags. The CI workflow
needs none of these.
