#!/usr/bin/env bash
# Create, sign and notarize sweef_<ver>_darwin_universal.dmg from a (signed) Sweef.app. macOS only (hdiutil).
#   packaging/macos/make-dmg.sh <Sweef.app> [out.dmg]
# Notarization secrets: APPLE_ID, APPLE_TEAM_ID, APPLE_APP_PASSWORD (app-specific password).
# Signing of the dmg reuses the identity from sign.sh when MACOS_CERT_P12 was provided (keychain still unlocked).
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
resolve_all
APP="${1:?usage: make-dmg.sh Sweef.app [out.dmg]}"
OUT="${2:-$DIST/$(asset_name dmg darwin universal dmg)}"
[ "$(uname -s)" = Darwin ] || die "hdiutil only exists on macOS. On Linux use the .zip that build-app.sh produces."

stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
cp -R "$APP" "$stage/Sweef.app"
ln -s /Applications "$stage/Applications"
cat > "$stage/README.txt" <<EOR
Drag Sweef to Applications.

This is an ALPHA build. If macOS says the app is "damaged" or from an "unidentified developer",
the build is not notarized; see docs/install.md in the Sweef repository. The workaround is:
    xattr -dr com.apple.quarantine /Applications/Sweef.app
EOR
rm -f "$OUT"
hdiutil create -volname "Sweef $VERSION" -srcfolder "$stage" -fs HFS+ -format UDZO -imagekey zlib-level=9 -ov "$OUT" >/dev/null
log "created $OUT"

IDENTITY=""
[ -n "${SIGN_IDENTITY_FILE:-}" ] && [ -f "$SIGN_IDENTITY_FILE" ] && IDENTITY="$(cat "$SIGN_IDENTITY_FILE")"
if [ -n "$IDENTITY" ]; then codesign --force --timestamp --sign "$IDENTITY" "$OUT"; fi

if [ -z "${APPLE_ID:-}" ] || [ -z "${APPLE_TEAM_ID:-}" ] || [ -z "${APPLE_APP_PASSWORD:-}" ]; then
  warn "APPLE_ID / APPLE_TEAM_ID / APPLE_APP_PASSWORD not set: $OUT is NOT notarized. Users will see a Gatekeeper warning (see docs/install.md)."
  exit 0
fi
if [ -z "$IDENTITY" ]; then
  warn "notarization credentials are set but the app is not Developer ID signed; Apple would reject it. Skipping notarization."
  exit 0
fi
log "submitting to Apple notary service (this can take several minutes)"
xcrun notarytool submit "$OUT" --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" --password "$APPLE_APP_PASSWORD" --wait --timeout 45m
xcrun stapler staple "$OUT"
xcrun stapler validate "$OUT"
spctl --assess --type open --context context:primary-signature -vv "$OUT" || true
