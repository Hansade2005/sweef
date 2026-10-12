#!/usr/bin/env bash
# Build sweef_<ver>_linux_<arch>.AppImage. appimagetool and the type2 runtimes are downloaded once,
# pinned by SHA-256 (set APPIMAGETOOL / APPIMAGE_RUNTIME_DIR to use local copies instead).
#   packaging/linux/build-appimage.sh [amd64|arm64 ...]
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
resolve_all
umask 022
ARCHES="${*:-amd64 arm64}"

TOOL_URL="https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-x86_64.AppImage"
TOOL_SHA256="ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0"
RUNTIME_BASE="https://github.com/AppImage/type2-runtime/releases/download/continuous"
# The runtimes are published under a moving "continuous" tag, so they are pinned by hash.
# If upstream republished and the check fails: download both, review, and update these lines.
# shellcheck disable=SC2034  # read through indirect expansion below
RUNTIME_SHA256_x86_64="156f4bdbde9c52d01814600013e0a273f0118dc2de98975f3c8c63427ec79074"
# shellcheck disable=SC2034
RUNTIME_SHA256_aarch64="b4ff0030242d0c3bb12ce40541828303cf167493f4793456f0436edd6255c39d"

CACHE="${APPIMAGE_CACHE:-$DIST/cache/appimage}"
mkdir -p "$CACHE"

fetch_pinned() { # url dest sha
  if [ -f "$2" ] && [ "$(sha256_of "$2")" = "$3" ]; then return 0; fi
  need curl
  log "downloading $(basename "$2")"
  curl -fsSL --retry 3 -o "$2.tmp" "$1" || die "download failed: $1"
  [ "$(sha256_of "$2.tmp")" = "$3" ] || { rm -f "$2.tmp"; die "SHA-256 mismatch for $1 (upstream changed? review and update the pin in $0)"; }
  mv "$2.tmp" "$2"
}

TOOL="${APPIMAGETOOL:-$CACHE/appimagetool-x86_64.AppImage}"
if [ -z "${APPIMAGETOOL:-}" ]; then fetch_pinned "$TOOL_URL" "$TOOL" "$TOOL_SHA256"; chmod +x "$TOOL"; fi

for arch in $ARCHES; do
  aarch="$(appimage_arch "$arch")"
  rt="${APPIMAGE_RUNTIME_DIR:-$CACHE}/runtime-$aarch"
  if [ -z "${APPIMAGE_RUNTIME_DIR:-}" ]; then
    var="RUNTIME_SHA256_$aarch"; fetch_pinned "$RUNTIME_BASE/runtime-$aarch" "$rt" "${!var}"
  fi
  work="$DIST/work/appimage_$arch"
  app="$work/Sweef.AppDir"
  rm -rf "$work"; mkdir -p "$app"
  "$ROOT/packaging/linux/stage.sh" "$app" /usr "$arch" sweef

  cat > "$app/AppRun" <<'EOA'
#!/bin/sh
HERE="$(dirname "$(readlink -f "$0")")"
export PATH="$HERE/usr/bin:$PATH"
exec "$HERE/usr/bin/sweef" gui "$@"
EOA
  chmod 755 "$app/AppRun"
  cp "$app/usr/share/applications/sweef.desktop" "$app/sweef.desktop"
  cp "$DIST/icons/png/256.png" "$app/sweef.png"
  ln -s sweef.png "$app/.DirIcon"
  clamp_mtimes "$app"

  out="$DIST/$(asset_name appimage linux "$arch" AppImage)"
  rm -f "$out"
  ( cd "$work" && ARCH="$aarch" APPIMAGE_EXTRACT_AND_RUN=1 "$TOOL" --appimage-extract-and-run \
      --runtime-file "$rt" -n "$app" "$out" ) >&2 || die "appimagetool failed for $arch"
  chmod 755 "$out"
  log "built $out ($(wc -c < "$out" | tr -d ' ') bytes)"
done
