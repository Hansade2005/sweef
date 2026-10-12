#!/usr/bin/env bash
# Portable tarball: sweef_<ver>_linux_<arch>.tar.gz containing sweef-<ver>-linux-<arch>/
# (bin/, share/, install.sh). Unpack anywhere and run bin/sweef gui, or ./install.sh --from-dir .
#   packaging/linux/build-tarball.sh [amd64|arm64 ...]
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
resolve_all
umask 022
ARCHES="${*:-amd64 arm64}"

for arch in $ARCHES; do
  work="$DIST/work/tar_$arch"
  top="sweef-$VERSION-linux-$arch"
  rm -rf "$work"; mkdir -p "$work/$top"
  "$ROOT/packaging/linux/stage.sh" "$work/$top" "" "$arch" sweef
  # keep Exec=sweef in the tarball desktop file; install.sh rewrites it to the absolute path
  install -m 644 "$ROOT/NOTICE" "$work/$top/NOTICE"
  sed "s|@DEFAULT_VERSION@|$VERSION|" "$ROOT/packaging/linux/install.sh" > "$work/$top/install.sh"
  chmod 755 "$work/$top/install.sh"
  cat > "$work/$top/README.txt" <<EOR
Sweef $VERSION (alpha), Linux $arch

  ./bin/sweef gui                 start the browser
  ./bin/sweef help                command line tools
  ./install.sh --from-dir .       install into ~/.local with menu entry and icons
  ./install.sh --help

This directory is self-contained; delete it to remove Sweef.
Docs: $HOMEPAGE
EOR
  chmod 644 "$work/$top/README.txt"
  clamp_mtimes "$work/$top"
  out="$DIST/$(asset_name tar linux "$arch" tar.gz)"
  make_targz "$work" "$top" "$out"
  log "built $out ($(wc -c < "$out" | tr -d ' ') bytes)"
done
