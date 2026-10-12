#!/usr/bin/env bash
# Build sweef_<ver>_linux_<arch>.rpm with rpmbuild from a pre-staged tree (no compiler involved,
# so cross-arch RPMs build fine on an x86_64 host).
#   packaging/linux/build-rpm.sh [amd64|arm64 ...]
set -euo pipefail
. "$(dirname "$0")/../../scripts/release/lib.sh"
need rpmbuild "apt install rpm  /  dnf install rpm-build"
resolve_all
umask 022
ARCHES="${*:-amd64 arm64}"

for arch in $ARCHES; do
  rarch="$(rpm_arch "$arch")"
  work="$DIST/work/rpm_$arch"
  rm -rf "$work"; mkdir -p "$work"/{root,top/BUILD,top/RPMS,top/SPECS,top/SRPMS,top/BUILDROOT}
  "$ROOT/packaging/linux/stage.sh" "$work/root" /usr "$arch" sweef
  install -d -m 755 "$work/root/usr/share/doc/sweef"
  install -m 644 "$ROOT/NOTICE" "$work/root/usr/share/doc/sweef/NOTICE"
  clamp_mtimes "$work/root"

  sed -e "s|@RPM_VERSION@|$RPM_VERSION|g" -e "s|@RPM_RELEASE@|$RPM_RELEASE|g" \
      -e "s|@VERSION@|$VERSION|g" -e "s|@COMMIT@|$COMMIT|g" \
      -e "s|@SUMMARY@|$DESCRIPTION_SHORT|g" -e "s|@HOMEPAGE@|$HOMEPAGE|g" \
      -e "s|@MAINTAINER@|$MAINTAINER|g" \
      -e "s|@CHANGELOG_DATE@|$(LC_ALL=C fmt_epoch "$SOURCE_DATE_EPOCH" '%a %b %d %Y')|g" \
      "$ROOT/packaging/linux/sweef.spec.in" > "$work/top/SPECS/sweef.spec"

  SOURCE_DATE_EPOCH="$SOURCE_DATE_EPOCH" rpmbuild -bb --quiet --target "$rarch-linux" \
    --define "_topdir $work/top" \
    --define "stagedir $work/root" \
    --define "_binary_payload w9.gzdio" \
    --define "_buildhost sweef-release" \
    --define "use_source_date_epoch_as_buildtime 1" \
    --define "clamp_mtime_to_source_date_epoch 1" \
    --define "_source_payload w9.gzdio" \
    --define "_rpmfilename %%{ARCH}/%%{NAME}-%%{VERSION}-%%{RELEASE}.%%{ARCH}.rpm" \
    "$work/top/SPECS/sweef.spec"
  built="$(find "$work/top/RPMS" -name '*.rpm' | head -n1)"
  [ -n "$built" ] || die "rpmbuild produced no rpm for $arch"
  out="$DIST/$(asset_name rpm linux "$arch" rpm)"
  cp "$built" "$out"
  log "built $out ($(wc -c < "$out" | tr -d ' ') bytes)"
done
