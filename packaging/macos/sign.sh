#!/usr/bin/env bash
# Sign Sweef.app (macOS only). Skips cleanly, with a loud warning, when the certificate is absent.
#   packaging/macos/sign.sh <Sweef.app>
# Secrets: MACOS_CERT_P12 (base64 of the Developer ID Application .p12), MACOS_CERT_PASSWORD
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
APP="${1:?usage: sign.sh Sweef.app}"
ENT="$ROOT/packaging/macos/entitlements.plist"
[ "$(uname -s)" = Darwin ] || die "codesign only exists on macOS; run this step on a macos runner"

if [ -z "${MACOS_CERT_P12:-}" ] || [ -z "${MACOS_CERT_PASSWORD:-}" ]; then
  warn "MACOS_CERT_P12 / MACOS_CERT_PASSWORD not set: Sweef.app is only AD-HOC signed (not Developer ID signed, not notarized). Gatekeeper will block it on other Macs until the user runs: xattr -dr com.apple.quarantine /Applications/Sweef.app"
  codesign --force --deep --sign - "$APP"
  codesign --verify --verbose=2 "$APP" || true
  exit 0
fi

TMP="${RUNNER_TEMP:-$(mktemp -d)}"
KC="$TMP/sweef-signing.keychain-db"
KCPASS="$(openssl rand -hex 24)"
P12="$TMP/sweef-cert.p12"
cleanup() { security delete-keychain "$KC" >/dev/null 2>&1 || true; rm -f "$P12"; }
trap cleanup EXIT

printf '%s' "$MACOS_CERT_P12" | openssl base64 -d -A > "$P12"
security create-keychain -p "$KCPASS" "$KC"
security set-keychain-settings -lut 21600 "$KC"
security unlock-keychain -p "$KCPASS" "$KC"
security import "$P12" -k "$KC" -P "$MACOS_CERT_PASSWORD" -T /usr/bin/codesign -T /usr/bin/security >/dev/null
security set-key-partition-list -S apple-tool:,apple: -s -k "$KCPASS" "$KC" >/dev/null
# shellcheck disable=SC2046  # intentional: keep the existing keychains in the search list
security list-keychains -d user -s "$KC" $(security list-keychains -d user | tr -d '"')
IDENTITY="$(security find-identity -v -p codesigning "$KC" | awk -F'"' '/Developer ID Application/ {print $2; exit}')"
[ -n "$IDENTITY" ] || die "no 'Developer ID Application' identity found in the imported certificate"
log "signing as: $IDENTITY"

# Inside-out: nested code first, then the bundle. No --deep (Apple discourages it for signing).
for f in "$APP/Contents/MacOS/sweef" "$APP/Contents/MacOS/SweefLauncher"; do
  codesign --force --timestamp --options runtime --entitlements "$ENT" --sign "$IDENTITY" "$f"
done
codesign --force --timestamp --options runtime --entitlements "$ENT" --sign "$IDENTITY" "$APP"
codesign --verify --strict --deep --verbose=2 "$APP"
spctl --assess --type execute -vv "$APP" || log "spctl rejects it until the app is notarized (expected before notarization)"
echo "$IDENTITY" > "${SIGN_IDENTITY_FILE:-$TMP/sweef-identity.txt}"
