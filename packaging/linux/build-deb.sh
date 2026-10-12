#!/usr/bin/env bash
# Build sweef_<ver>_linux_<arch>.deb using only dpkg-deb (no dpkg-dev, debhelper or fakeroot).
#   packaging/linux/build-deb.sh [amd64|arm64 ...]     (default: both)
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
need dpkg-deb
resolve_all
umask 022
ARCHES="${*:-amd64 arm64}"

for arch in $ARCHES; do
  darch="$(deb_arch "$arch")"
  work="$DIST/work/deb_$arch"
  root="$work/root"
  rm -rf "$work"; mkdir -p "$root"
  "$ROOT/packaging/linux/stage.sh" "$root" /usr "$arch" sweef

  # docs
  doc="$root/usr/share/doc/sweef"
  install -d -m 755 "$doc"
  install -m 644 "$ROOT/NOTICE" "$doc/NOTICE"
  cat > "$doc/copyright" <<EOC
Format: https://www.debian.org/doc/packaging-manuals/copyright-format/1.0/
Upstream-Name: Sweef
Source: $HOMEPAGE

Files: *
Copyright: 2026 the Sweef authors
License: TODO-owner-to-decide
 TODO(owner): the repository does not declare a license yet. Replace this
 paragraph with the real license before the first public release.

Files: usr/bin/sweef
Comment: statically linked Go binary; third-party attributions (including
 LGPL-2.1+ derived AAC/SBR tables) are listed in /usr/share/doc/sweef/NOTICE.
License: see NOTICE
EOC
  chmod 644 "$doc/copyright"
  {
    printf 'sweef (%s) unstable; urgency=low\n\n  * Alpha release %s (commit %s).\n\n -- %s  %s\n' \
      "$DEB_VERSION" "$VERSION" "$COMMIT" "$MAINTAINER" "$(fmt_epoch "$SOURCE_DATE_EPOCH" '%a, %d %b %Y %H:%M:%S +0000')"
  } | gzip -n -9 > "$doc/changelog.gz"
  chmod 644 "$doc/changelog.gz"

  # statically linked Go binaries are intentional
  install -d -m 755 "$root/usr/share/lintian/overrides"
  printf 'sweef: statically-linked-binary [usr/bin/sweef]\n' > "$root/usr/share/lintian/overrides/sweef"
  chmod 644 "$root/usr/share/lintian/overrides/sweef"

  # md5sums (relative paths, no ./ prefix)
  ( cd "$root" && find usr -type f | LC_ALL=C sort | xargs md5sum ) > "$work/md5sums"

  mkdir -p "$root/DEBIAN"
  install -m 644 "$work/md5sums" "$root/DEBIAN/md5sums"
  for s in postinst prerm postrm; do install -m 755 "$ROOT/packaging/linux/deb/$s" "$root/DEBIAN/$s"; done
  size_kib="$(du -sk --apparent-size "$root/usr" | cut -f1)"
  cat > "$root/DEBIAN/control" <<EOC
Package: sweef
Version: $DEB_VERSION
Architecture: $darch
Maintainer: $MAINTAINER
Installed-Size: $size_kib
Section: web
Priority: optional
Homepage: $HOMEPAGE
Provides: gnome-www-browser, x-www-browser
Recommends: xdg-utils
Description: $DESCRIPTION_SHORT
 Sweef is a browser with its own engine, written in pure Go: a single static
 binary with a real JavaScript engine and a built-in Model Context Protocol
 server for AI agents.
 .
 This is an ALPHA release. Run "sweef gui" for the graphical browser; the
 command line subcommands (fetch, snapshot, shot, mcp, ...) work headless.
EOC
  chmod 644 "$root/DEBIAN/control"

  clamp_mtimes "$root"
  out="$DIST/$(asset_name deb linux "$arch" deb)"
  mkdir -p "$DIST"; rm -f "$out"
  dpkg-deb --root-owner-group -Zxz -z6 --build "$root" "$out" >/dev/null
  log "built $out ($(wc -c < "$out" | tr -d ' ') bytes)"
done
