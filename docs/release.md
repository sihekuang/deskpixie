# Builds and releases

Two GitHub Actions workflows, both on `macos-15` (arm64). The decision is in
`docs/decisions.md` ("Build pipeline shape and libdf in CI").

| Workflow | Runs on | Secrets | Produces |
|---|---|---|---|
| `.github/workflows/ci.yml` | every pull request, every push to `main`, manual dispatch | none | Go + Swift tests, ad-hoc signed Release build, artifact `DeskPixie-<branch>` containing `DeskPixie-0.0.0-ci.<run>-arm64.app.zip` (kept 14 days) |
| `.github/workflows/release.yml` | tag `vX.Y.Z` | `release` environment | tests, Developer ID signed + notarized + stapled `DeskPixie-X.Y.Z-arm64.dmg` and `.app.zip`, GitHub Release |
| `.github/workflows/release.yml` | manual dispatch from `main` | `release` environment | same, Developer ID signed but **not** notarized (pipeline smoke test), artifact only |

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
   `main`). It signs with Developer ID but does not notarize or publish.
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

## One-time setup: signing secrets

The `release` environment exists on `sihekuang/deskpixie`; its secrets do not. They are the
same eight secrets ai-mascot uses. GitHub secrets cannot be read back, so re-enter them from
wherever the originals are stored (the `.p12` export of the Developer ID Application
certificate, its password, the App Store Connect API key `.p8` and its IDs):

```bash
R=sihekuang/deskpixie

# Developer ID Application certificate (.p12) and its export password
base64 -i DeveloperID.p12 | gh secret set MACOS_CERT_P12_BASE64 --env release -R $R
gh secret set MACOS_CERT_PASSWORD --env release -R $R          # prompts for the value
# Any random string; only protects the throwaway CI keychain
openssl rand -base64 24 | gh secret set MACOS_KEYCHAIN_PASSWORD --env release -R $R
# Exactly as `security find-identity -v -p codesigning` prints it
gh secret set MACOS_SIGNING_IDENTITY --env release -R $R \
  --body "Developer ID Application: Daniel Lee (TULKQWS4XL)"
gh secret set APPLE_TEAM_ID --env release -R $R --body "TULKQWS4XL"

# App Store Connect API key for notarytool (Users and Access > Integrations > Keys)
base64 -i AuthKey_XXXXXXXXXX.p8 | gh secret set APPLE_API_KEY_BASE64 --env release -R $R
gh secret set APPLE_API_KEY_ID --env release -R $R             # the key ID (XXXXXXXXXX)
gh secret set APPLE_API_ISSUER_ID --env release -R $R          # the issuer UUID

gh secret list --env release -R $R                             # expect all eight
```

Pipe values (or let `gh` prompt) rather than passing them with `--body`, so they stay out of
shell history; the identity and team ID are not secret. The CI workflow needs none of these.
