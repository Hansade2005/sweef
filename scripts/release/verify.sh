#!/usr/bin/env bash
# Local/CI sanity checks on the built Linux artifacts. Never touches the host: everything is unpacked or
# installed into a scratch directory. Run after package-all.sh.
#   scripts/release/verify.sh [amd64|arm64]    (default: the host architecture)
set -euo pipefail
. "$(dirname "$0")/lib.sh"
resolve_all
case "$(uname -m)" in x86_64) HOSTARCH=amd64;; aarch64|arm64) HOSTARCH=arm64;; *) HOSTARCH=amd64;; esac
ARCH="${1:-$HOSTARCH}"
scratch="$(mktemp -d)"; trap 'rm -rf "$scratch"' EXIT
fail=0
ok()  { printf '  ok   %s\n' "$*"; }
bad() { printf '  FAIL %s\n' "$*"; fail=1; }
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi; }

DEB="$DIST/$(asset_name deb linux "$ARCH" deb)"
log "deb: $DEB"
need dpkg-deb
check "dpkg-deb --info" dpkg-deb --info "$DEB"
dpkg-deb -x "$DEB" "$scratch/deb"; dpkg-deb -e "$DEB" "$scratch/deb/DEBIAN"
( cd "$scratch/deb" && md5sum -c --quiet DEBIAN/md5sums ) && ok "md5sums match payload" || bad "md5sums"
grep -q "^Version: $DEB_VERSION\$" "$scratch/deb/DEBIAN/control" && ok "control Version=$DEB_VERSION" || bad "control version"
for f in postinst prerm postrm; do [ -x "$scratch/deb/DEBIAN/$f" ] && sh -n "$scratch/deb/DEBIAN/$f" && ok "$f is executable and parses" || bad "$f"; done
D="$scratch/deb/usr/share/applications/sweef.desktop"
grep -q '^Categories=Network;WebBrowser;$' "$D" && ok "Categories=Network;WebBrowser;" || bad "Categories"
grep -q '^MimeType=.*x-scheme-handler/http;.*x-scheme-handler/https;' "$D" && ok "MimeType has http/https" || bad "MimeType"
grep -q '^StartupWMClass=' "$D" && ok "StartupWMClass present" || bad "StartupWMClass"
grep -q '^Exec=sweef gui %U$' "$D" && ok "Exec=sweef gui %U" || bad "Exec"
for n in 16 22 24 32 48 64 128 256 512; do [ -f "$scratch/deb/usr/share/icons/hicolor/${n}x${n}/apps/sweef.png" ] || bad "icon $n"; done; ok "hicolor icons 16..512 present"
[ -f "$scratch/deb/usr/share/icons/hicolor/scalable/apps/sweef.svg" ] && ok "scalable icon" || bad "svg icon"
if command -v desktop-file-validate >/dev/null; then check "desktop-file-validate" desktop-file-validate "$D"; fi
if command -v appstreamcli >/dev/null; then check "appstreamcli validate" appstreamcli validate --no-net "$scratch/deb/usr/share/metainfo/sweef.metainfo.xml"; fi
if command -v lintian >/dev/null; then
  if lintian --tag-display-limit 0 "$DEB" 2>&1 | grep -E '^[EW]:' ; then bad "lintian"; else ok "lintian clean"; fi
fi
if [ "$ARCH" = "$HOSTARCH" ]; then
  out="$("$scratch/deb/usr/bin/sweef" version)"; case "$out" in *"$VERSION"*) ok "installed binary runs: $out";; *) bad "binary reports '$out' (expected $VERSION)";; esac
fi

log "rpm"
RPM="$DIST/$(asset_name rpm linux "$ARCH" rpm)"
if command -v rpm >/dev/null && [ -f "$RPM" ]; then
  check "rpm -K (digests)" rpm -K --nosignature "$RPM"
  rpm -qpl "$RPM" | grep -q '/usr/share/applications/sweef.desktop' && ok "rpm lists desktop file" || bad "rpm desktop file"
else echo "  skip rpm not available"; fi

log "tarball + install.sh in a scratch prefix"
TGZ="$DIST/$(asset_name tar linux "$ARCH" tar.gz)"
cp "$TGZ" "$scratch/"; ( cd "$DIST" && grep " $(basename "$TGZ")\$" SHA256SUMS > "$scratch/SHA256SUMS" ) || bad "tarball not in SHA256SUMS (run checksums.sh)"
H="$scratch/home"; mkdir -p "$H"
HOME="$H" sh "$ROOT/packaging/linux/install.sh" --prefix "$scratch/prefix" --base-url "file://$scratch" --version "$VERSION" -y >/dev/null && ok "install.sh installs (checksum verified)" || bad "install.sh"
if [ "$ARCH" = "$HOSTARCH" ]; then "$scratch/prefix/bin/sweef" version >/dev/null && ok "installed copy runs 'sweef version'" || bad "installed copy"; fi
grep -q "^Exec=$scratch/prefix/bin/sweef gui %U\$" "$scratch/prefix/share/applications/sweef.desktop" && ok "desktop Exec rewritten to absolute path" || bad "desktop Exec rewrite"
echo junk >> "$scratch/$(basename "$TGZ")"
if HOME="$H" sh "$ROOT/packaging/linux/install.sh" --prefix "$scratch/p2" --base-url "file://$scratch" --version "$VERSION" -y >/dev/null 2>&1; then bad "tampered tarball was accepted!"; else ok "tampered tarball rejected"; fi
HOME="$H" "$scratch/prefix/bin/sweef-uninstall" -y >/dev/null && ok "uninstall ran" || bad "uninstall"
left="$(find "$scratch/prefix" -type f ! -name mimeinfo.cache | wc -l | tr -d ' ')"
[ "$left" = 0 ] && ok "uninstall left no files behind" || bad "$left files left after uninstall"

[ "$fail" = 0 ] && log "all checks passed" || die "some checks failed"
