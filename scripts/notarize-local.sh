#!/usr/bin/env bash
# Local equivalent of the notarizing release workflow, for this Mac: Release build signed with the
# login keychain's Developer ID identity, then scripts/package-mac.sh with NOTARIZE=true.
#
# Usage (repo root):  VERSION=0.0.1-local scripts/notarize-local.sh
#
# Credentials mirror ~/Documents/AppleSigning/sign-and-notarize.sh: local-notarize.env supplies
# APPLE_API_ISSUER_ID; the key is AuthKey_7X3J6MCQ63.p8 next to it. Env overrides:
#   SIGNING_IDENTITY     Developer ID Application identity (auto-detected from the keychain)
#   APPLE_SIGNING_DIR    default ~/Documents/AppleSigning
#   APPLE_API_KEY_P8, APPLE_API_KEY_ID, APPLE_API_ISSUER_ID
# Output: out-local/DeskPixie-$VERSION-arm64.{dmg,app.zip} (gitignored). Nothing is published.
set -euo pipefail

cd "$(dirname "$0")/.."

SIGNING_DIR="${APPLE_SIGNING_DIR:-$HOME/Documents/AppleSigning}"
# shellcheck disable=SC1091
source "$SIGNING_DIR/local-notarize.env"
API_KEY="${APPLE_API_KEY_P8:-$SIGNING_DIR/AuthKey_7X3J6MCQ63.p8}"
API_KEY_ID="${APPLE_API_KEY_ID:-7X3J6MCQ63}"
IDENTITY="${SIGNING_IDENTITY:-$(security find-identity -v -p codesigning | grep "Developer ID Application" | head -1 | sed 's/.*"\(.*\)"/\1/' || true)}"

[ -n "$IDENTITY" ] || { echo "error: no Developer ID Application identity in the keychain" >&2; exit 1; }
[ -n "${APPLE_API_ISSUER_ID:-}" ] || { echo "error: APPLE_API_ISSUER_ID not set (check local-notarize.env)" >&2; exit 1; }
[ -s "$API_KEY" ] || { echo "error: API key $API_KEY not found" >&2; exit 1; }

# The team id is the parenthesised suffix of the identity name.
TEAM_ID=$(sed -n 's/.*(\([A-Z0-9]*\))$/\1/p' <<<"$IDENTITY")
VERSION="${VERSION:-0.0.0}"
echo "Using identity: $IDENTITY"

make -C mac release VERSION="$VERSION" SIGNING_IDENTITY="$IDENTITY" DEVELOPMENT_TEAM="$TEAM_ID"

APP="mac/build/Build/Products/Release/DeskPixie.app" \
VERSION="$VERSION" \
OUT_DIR="out-local" \
SIGNING_IDENTITY="$IDENTITY" \
NOTARIZE=true \
NOTARY_KEY_PATH="$API_KEY" \
NOTARY_KEY_ID="$API_KEY_ID" \
NOTARY_ISSUER_ID="$APPLE_API_ISSUER_ID" \
  scripts/package-mac.sh
