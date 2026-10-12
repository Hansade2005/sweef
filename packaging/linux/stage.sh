#!/usr/bin/env bash
# Stage the Linux file tree shared by deb, rpm, AppImage and the tarball.
#   stage.sh <destdir> <prefix> <arch> <exec-for-desktop-file>
# Example: stage.sh /tmp/root /usr amd64 sweef
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
resolve_all

DEST="$1"; PREFIX="$2"; ARCH="$3"; EXEC_PATH="$4"
SRC="$ROOT/packaging/linux"
BIN="$(require_bin linux "$ARCH" sweef)"
P="$DEST$PREFIX"

install -d -m 755 "$P/bin" "$P/share/man/man1" "$P/share/applications" "$P/share/metainfo" "$P/share/icons/hicolor/scalable/apps" "$P/share/sweef"
install -m 755 "$BIN" "$P/bin/sweef"
install -m 755 "$SRC/sweef-browser" "$P/bin/sweef-browser"

# desktop file (validated below by the packagers)
sed -e "s|@EXEC@|$EXEC_PATH|g" -e "s|@TRYEXEC@|$EXEC_PATH|g" "$SRC/sweef.desktop.in" > "$P/share/applications/sweef.desktop"
chmod 644 "$P/share/applications/sweef.desktop"

sed -e "s|@VERSION@|$VERSION|g" -e "s|@DATE@|$(fmt_epoch "$SOURCE_DATE_EPOCH" %Y-%m-%d)|g" "$SRC/sweef.metainfo.xml.in" \
  > "$P/share/metainfo/sweef.metainfo.xml"
chmod 644 "$P/share/metainfo/sweef.metainfo.xml"

[ -f "$DIST/icons/sweef.svg" ] || "$ROOT/scripts/release/icons.sh" >&2
for n in 16 22 24 32 48 64 128 256 512; do
  install -d -m 755 "$P/share/icons/hicolor/${n}x${n}/apps"
  install -m 644 "$DIST/icons/png/$n.png" "$P/share/icons/hicolor/${n}x${n}/apps/sweef.png"
done
install -m 644 "$DIST/icons/sweef.svg" "$P/share/icons/hicolor/scalable/apps/sweef.svg"
install -m 644 "$ROOT/NOTICE" "$P/share/sweef/NOTICE"
gzip -n -9 -c "$SRC/sweef.1" > "$P/share/man/man1/sweef.1.gz"
chmod 644 "$P/share/man/man1/sweef.1.gz"
ln -s sweef.1.gz "$P/share/man/man1/sweef-browser.1.gz"
