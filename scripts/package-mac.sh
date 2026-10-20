#!/usr/bin/env bash
# Verify, (optionally) notarize, and package a Developer ID signed DeskPixie.app.
# Used by .github/workflows/release.yml and scripts/notarize-local.sh.
#
# Inputs (env):
#   APP               path to the signed .app (default mac/build/Build/Products/Release/DeskPixie.app)
#   VERSION           version string for the artifact names (required)
#   OUT_DIR           where the .dmg and .app.zip go (default out)
#   SIGNING_IDENTITY  Developer ID Application identity, used to sign the dmg (required)
#   NOTARIZE          true: notarize + staple app and dmg, then Gatekeeper-check both (default false)
#   NOTARY_KEY_PATH   App Store Connect API key (.p8)    } required when NOTARIZE=true
#   NOTARY_KEY_ID     its key id                         }
#   NOTARY_ISSUER_ID  its issuer id                      }
#
# Flow (Apple's): check every Mach-O has Developer ID + hardened runtime + timestamp, notarize the
# zipped .app and staple it, build the .dmg (app + /Applications symlink) from the stapled app,
# sign the dmg, notarize and staple it, then spctl assess both as notarized. A rejected
# submission prints Apple's `notarytool log` and fails.
#
# Outputs: prints dmg=<path> and zip=<path>; also appended to $GITHUB_OUTPUT when set.
set -euo pipefail

APP="${APP:-mac/build/Build/Products/Release/DeskPixie.app}"
OUT_DIR="${OUT_DIR:-out}"
NOTARIZE="${NOTARIZE:-false}"
: "${VERSION:?VERSION is required}"
: "${SIGNING_IDENTITY:?SIGNING_IDENTITY is required}"

err() {
  if [ -n "${GITHUB_ACTIONS:-}" ]; then echo "::error::$*"; else echo "error: $*" >&2; fi
  exit 1
}

case "$NOTARIZE" in
  true)
    : "${NOTARY_KEY_PATH:?NOTARY_KEY_PATH is required when NOTARIZE=true}"
    : "${NOTARY_KEY_ID:?NOTARY_KEY_ID is required when NOTARIZE=true}"
    : "${NOTARY_ISSUER_ID:?NOTARY_ISSUER_ID is required when NOTARIZE=true}"
    [ -s "$NOTARY_KEY_PATH" ] || err "notary key $NOTARY_KEY_PATH missing or empty"
    ;;
  false) ;;
  *) err "NOTARIZE must be true or false (got '$NOTARIZE')" ;;
esac

test -d "$APP" || err "$APP not found"

TMP=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/package-mac.XXXXXX")
trap 'rm -rf "$TMP"' EXIT

mkdir -p "$OUT_DIR"
DMG="$OUT_DIR/DeskPixie-$VERSION-arm64.dmg"
ZIP="$OUT_DIR/DeskPixie-$VERSION-arm64.app.zip"
rm -f "$DMG" "$ZIP"

echo "=== Verifying signature ==="
codesign --verify --deep --strict --verbose=2 "$APP"
codesign -dvv "$APP" 2>&1 | head -20
ENT=$(codesign -d --entitlements - "$APP" 2>&1)
grep -q 'device.audio-input' <<<"$ENT" || err "audio-input entitlement missing from signed app"
# Every Mach-O must carry Developer ID + hardened runtime + timestamp for notarization.
for f in "$APP" "$APP"/Contents/Frameworks/*.dylib "$APP"/Contents/Helpers/pix; do
  INFO=$(codesign -dvv "$f" 2>&1)
  grep -q '^Authority=Developer ID Application' <<<"$INFO" || err "$f is not signed with Developer ID"
  grep -q 'flags=.*runtime' <<<"$INFO" || err "$f lacks the hardened runtime"
  grep -q '^Timestamp=' <<<"$INFO" || err "$f lacks a secure timestamp"
done

# Submit, wait, and fail with Apple's log unless the submission is Accepted.
notarize() {
  local file="$1" out id status
  echo "=== Notarizing $(basename "$file") ==="
  out=$(xcrun notarytool submit "$file" \
    --key "$NOTARY_KEY_PATH" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER_ID" \
    --wait --timeout 30m --output-format json) || true
  echo "$out"
  id=$(plutil -extract id raw -o - - <<<"$out" 2>/dev/null || true)
  status=$(plutil -extract status raw -o - - <<<"$out" 2>/dev/null || true)
  echo "notarytool: $(basename "$file") id=${id:-?} status=${status:-no result}"
  if [ "$status" != "Accepted" ]; then
    if [ -n "$id" ]; then
      xcrun notarytool log "$id" \
        --key "$NOTARY_KEY_PATH" --key-id "$NOTARY_KEY_ID" --issuer "$NOTARY_ISSUER_ID" || true
    fi
    err "notarization of $file: ${status:-no result}"
  fi
}

# staple FILE: staple the ticket, retrying: the ticket lookup at Apple (api.apple-cloudkit.com)
# sometimes drops the connection ("The network connection was lost", exit 68) right after
# notarization. stapler staple checks the ticket it attaches, so there's no separate
# `stapler validate` (another lookup that can drop); Gatekeeper checks both files at the end.
# Five tries, 60 s apart.
staple() {
  local file="$1" try
  for try in 1 2 3 4 5; do
    if xcrun stapler staple "$file"; then
      return 0
    fi
    echo "stapling $file failed (try $try of 5)"
    [ "$try" -lt 5 ] && sleep 60
  done
  err "stapling $file"
}

# notarized TYPE FILE [ARGS]: Gatekeeper accepts FILE as notarized (not just Developer ID signed).
notarized() {
  local type="$1" file="$2" out
  shift 2
  out=$(spctl --assess --type "$type" -v "$@" "$file" 2>&1) || err "Gatekeeper rejected $file: $out"
  echo "$out"
  echo "$out" | grep -q "source=Notarized Developer ID" || err "$file isn't notarized: $out"
}

if [ "$NOTARIZE" = "true" ]; then
  # 1. Notarize the app (as a zip) and staple the ticket to the .app itself.
  ditto -c -k --keepParent "$APP" "$TMP/notarize-app.zip"
  notarize "$TMP/notarize-app.zip"
  staple "$APP"
fi

# 2. The dmg: the (stapled) app plus an /Applications symlink to drag it onto.
echo "=== Building dmg ==="
STAGE="$TMP/dmg"
mkdir -p "$STAGE"
ditto "$APP" "$STAGE/DeskPixie.app"
ln -s /Applications "$STAGE/Applications"
hdiutil create -volname "DeskPixie" -srcfolder "$STAGE" -ov -format UDZO "$DMG"
codesign --force --sign "$SIGNING_IDENTITY" --timestamp "$DMG"
codesign --verify --verbose=2 "$DMG"

if [ "$NOTARIZE" = "true" ]; then
  # 3. Notarize and staple the dmg, then check both with Gatekeeper.
  notarize "$DMG"
  staple "$DMG"
  notarized open "$DMG" --context context:primary-signature
  notarized execute "$APP"
fi

ditto -c -k --keepParent --sequesterRsrc "$APP" "$ZIP"
ls -lh "$OUT_DIR"
echo "dmg=$DMG"
echo "zip=$ZIP"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  { echo "dmg=$DMG"; echo "zip=$ZIP"; } >> "$GITHUB_OUTPUT"
fi
