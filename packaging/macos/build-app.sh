#!/usr/bin/env bash
# Assemble Sweef.app (universal) from the darwin/amd64 and darwin/arm64 builds. Runs on Linux or macOS:
# the fat Mach-O is written by the pure-Go `pkgtool lipo`, the .icns by `pkgtool icns`.
#   packaging/macos/build-app.sh            -> dist/work/macos/Sweef.app and the sweef_<ver>_darwin_universal.zip
# Env: BUNDLE_ID (default io.github.sweefbrowser.sweef), MIN_MACOS (default 12.0)
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
resolve_all
umask 022

BUNDLE_ID="${BUNDLE_ID:-io.github.sweefbrowser.sweef}"
MIN_MACOS="${MIN_MACOS:-12.0}"   # TODO(owner): confirm against the Go toolchain's minimum macOS (Go 1.25+ needs macOS 12)
PT="$(pkgtool)"
work="$DIST/work/macos"
app="$work/Sweef.app"
rm -rf "$work"; mkdir -p "$work/universal"

for n in sweef SweefLauncher; do
  "$PT" lipo -o "$work/universal/$n" "$(require_bin darwin amd64 "$n")" "$(require_bin darwin arm64 "$n")"
  "$PT" lipo-info "$work/universal/$n" >&2
done

[ -f "$DIST/icons/sweef.icns" ] || "$ROOT/scripts/release/icons.sh" >&2

mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
install -m 755 "$work/universal/SweefLauncher" "$app/Contents/MacOS/SweefLauncher"   # bundle executable: runs `sweef gui`
install -m 755 "$work/universal/sweef"  "$app/Contents/MacOS/sweef"
install -m 644 "$DIST/icons/sweef.icns" "$app/Contents/Resources/Sweef.icns"
install -m 644 "$ROOT/NOTICE"           "$app/Contents/Resources/NOTICE"
printf 'APPL????' > "$app/Contents/PkgInfo"
sed -e "s|@BUNDLE_ID@|$BUNDLE_ID|g" -e "s|@VERSION_CORE@|$VERSION_CORE|g" -e "s|@VERSION@|$VERSION|g" \
    -e "s|@BUNDLE_VERSION@|$MAC_BUNDLE_VERSION|g" -e "s|@MIN_OS@|$MIN_MACOS|g" \
    "$ROOT/packaging/macos/Info.plist.in" > "$app/Contents/Info.plist"

# validate the plists (plutil on macOS, plistlib elsewhere)
for p in "$app/Contents/Info.plist" "$ROOT/packaging/macos/entitlements.plist"; do
  if command -v plutil >/dev/null 2>&1; then plutil -lint "$p" >&2
  else python3 -c 'import plistlib,sys; plistlib.load(open(sys.argv[1],"rb"))' "$p" || die "invalid plist: $p"; log "plist ok: $(basename "$p")"; fi
done

clamp_mtimes "$app"
log "assembled $app"

# Unsigned zip (also the Linux-only fallback when hdiutil is unavailable). Signed/notarised
# builds should ship the .dmg; CI replaces this zip with one made from the signed bundle.
zip_out="$DIST/$(asset_name zip darwin universal zip)"
rm -f "$zip_out"
( cd "$work" && find Sweef.app | LC_ALL=C sort | TZ=UTC zip -X -y -q -9 "$zip_out" -@ )
log "wrote $zip_out"
