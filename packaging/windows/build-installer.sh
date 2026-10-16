#!/usr/bin/env bash
# Build the NSIS installer(s) and the portable zip(s) for Windows. Runs on Linux, macOS or Windows (Git Bash).
#   packaging/windows/build-installer.sh [amd64|arm64 ...]
# Needs: makensis (apt install nsis / brew install makensis / choco install nsis), zip.
# Signing: run packaging/windows/sign.sh on dist/bin/windows_*/ BEFORE this script to sign the exes,
# and on the produced setup .exe afterwards (scripts/release/package-all.sh does both).
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
need makensis "apt install nsis"
resolve_all
ARCHES="${*:-amd64 arm64}"
[ -f "$DIST/icons/sweef.ico" ] || "$ROOT/scripts/release/icons.sh" >&2

for arch in $ARCHES; do
  src="$(bin_dir windows "$arch")"
  [ -f "$src/sweef.exe" ] && [ -f "$src/SweefLauncher.exe" ] || die "missing exes in $src; run build.sh"
  out="$DIST/sweef_${VERSION}_windows_${arch}_setup.exe"
  mkdir -p "$DIST"
  makensis -V2 -INPUTCHARSET UTF8 \
    -DVERSION="$VERSION" -DVERSION4="$VERSION4" -DARCH="$arch" \
    -DSRCDIR="$src" -DICON="$DIST/icons/sweef.ico" -DNOTICE="$ROOT/NOTICE" -DOUTFILE="$out" \
    "$ROOT/packaging/windows/sweef.nsi" >&2
  log "built $out ($(wc -c < "$out" | tr -d ' ') bytes)"

  # portable zip
  work="$DIST/work/winzip_$arch"; top="sweef-$VERSION-windows-$arch"
  rm -rf "$work"; mkdir -p "$work/$top"
  cp "$src/sweef.exe" "$src/SweefLauncher.exe" "$work/$top/"
  cp "$ROOT/NOTICE" "$work/$top/NOTICE"
  printf 'Sweef %s (alpha), Windows %s - portable\r\n\r\n  SweefLauncher.exe   starts the browser (runs "sweef.exe gui")\r\n  sweef.exe   command line tools: sweef.exe help\r\n\r\nDelete this folder to remove Sweef. The portable build does not register itself as a browser;\r\nuse the setup installer for that.\r\n' "$VERSION" "$arch" > "$work/$top/README.txt"
  zip_out="$DIST/$(asset_name zip windows "$arch" zip)"
  rm -f "$zip_out"
  make_zip "$work" "$top" "$zip_out"
  log "built $zip_out"
done
