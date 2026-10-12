#!/usr/bin/env bash
# Package everything that can be built on the current OS from dist/bin (run build.sh first).
#   scripts/release/package-all.sh [--only linux|windows|macos]
# Linux host:  deb, rpm, AppImage, tar.gz (linux) + NSIS setup, zip (windows) + unsigned .app zip (macos)
# macOS host:  additionally signs the .app and builds/notarizes the .dmg (needs the secrets, else warns)
set -euo pipefail
. "$(dirname "$0")/lib.sh"
ONLY=""
while [ $# -gt 0 ]; do case "$1" in --only) ONLY="$2"; shift 2;; *) die "unknown argument $1";; esac; done
resolve_all
want() { [ -z "$ONLY" ] || [ "$ONLY" = "$1" ]; }

if want linux; then
  "$ROOT/packaging/linux/build-deb.sh"
  "$ROOT/packaging/linux/build-rpm.sh"
  "$ROOT/packaging/linux/build-appimage.sh"
  "$ROOT/packaging/linux/build-tarball.sh"
  install -m 755 "$ROOT/packaging/linux/install.sh" "$DIST/install.sh.tmp"
  sed "s|@DEFAULT_VERSION@|$VERSION|" "$DIST/install.sh.tmp" > "$DIST/install.sh"; rm -f "$DIST/install.sh.tmp"; chmod 755 "$DIST/install.sh"
fi
if want windows; then
  "$ROOT/packaging/windows/sign.sh" "$DIST"/bin/windows_*/*.exe   # warns and continues when no certificate
  "$ROOT/packaging/windows/build-installer.sh"
  "$ROOT/packaging/windows/sign.sh" "$DIST"/sweef_*_setup.exe
  # the portable zips were built before the setup exes were signed but after the inner exes, so they are already signed.
fi
if want macos; then
  "$ROOT/packaging/macos/build-app.sh"
  if [ "$(uname -s)" = Darwin ]; then
    SIGN_IDENTITY_FILE="$DIST/work/macos/identity.txt" "$ROOT/packaging/macos/sign.sh" "$DIST/work/macos/Sweef.app"
    ( cd "$DIST/work/macos" && rm -f "$DIST/$(asset_name zip darwin universal zip)" && ditto -c -k --sequesterRsrc --keepParent Sweef.app "$DIST/$(asset_name zip darwin universal zip)" )
    SIGN_IDENTITY_FILE="$DIST/work/macos/identity.txt" "$ROOT/packaging/macos/make-dmg.sh" "$DIST/work/macos/Sweef.app"
  else
    warn "not on macOS: skipping .app signing and .dmg creation (needs codesign/hdiutil on a macos runner). Only the unsigned .zip was produced."
  fi
fi
log "artifacts in $DIST:"; for f in "$DIST"/sweef_* "$DIST"/install.sh; do [ -f "$f" ] && echo "  $(basename "$f")" >&2; done; true
