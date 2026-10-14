#!/usr/bin/env bash
# Cross-compile Sweef for every release target with a pure-Go toolchain.
#
#   scripts/release/build.sh [--targets "linux/amd64 ..."] [--check-repro] [--skip-launchers] [--skip-cdm-host]
#
# Env: VERSION, COMMIT, SOURCE_DATE_EPOCH, DIST (default ./dist),
#      SWEEF_MAIN_PKG (default ./cmd/sweef), SWEEF_MAX_BINARY_BYTES (default 33554432),
#      STRICT_VERSION=1 (fail if the built binary does not report $VERSION)
#
# Output layout:
#   dist/bin/<os>_<arch>/sweef[.exe]        the browser/CLI
#   dist/bin/<os>_<arch>/Sweef[.exe]        GUI launcher (darwin, windows): runs `sweef gui`
#   dist/bin/<os>_<arch>/sweef-cdm[.exe]    CDM host helper (Widevine); the only binary that may be dynamically linked, docs/drm.md
#   dist/bin/SHA256SUMS                     checksums of everything above
set -euo pipefail
. "$(dirname "$0")/lib.sh"

TARGETS="linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"
CHECK_REPRO=0
LAUNCHERS=1
CDM_HOST=1
while [ $# -gt 0 ]; do
  case "$1" in
    --targets) TARGETS="$2"; shift 2 ;;
    --check-repro) CHECK_REPRO=1; shift ;;
    --skip-launchers) LAUNCHERS=0; shift ;;
    --skip-cdm-host) CDM_HOST=0; shift ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done

need go
resolve_all
MAIN_PKG="${SWEEF_MAIN_PKG:-./cmd/sweef}"
MAX_BYTES="${SWEEF_MAX_BINARY_BYTES:-33554432}"
export CGO_ENABLED=0
export GOFLAGS="${GOFLAGS:-} -mod=readonly"
export SOURCE_DATE_EPOCH

log "version=$VERSION commit=$COMMIT epoch=$SOURCE_DATE_EPOCH go=$(go env GOVERSION)"

LDFLAGS="-s -w -buildid= -X main.version=$VERSION -X main.commit=$COMMIT"

# Windows resources (manifest: DPI v2, long paths, UTF-8; version info; icon) are linked in
# via .syso files that live next to the main package only for the duration of the build.
SYSO_FILES=()
cleanup() { for f in ${SYSO_FILES[@]+"${SYSO_FILES[@]}"}; do rm -f "$f"; done; }
trap cleanup EXIT

prepare_windows_resources() {
  "$ROOT/scripts/release/icons.sh" >&2
  local PT; PT="$(pkgtool)"
  local mf="$DIST/windows-resources/sweef.manifest"
  mkdir -p "$DIST/windows-resources"
  sed "s/@VERSION4@/$VERSION4/g" "$ROOT/packaging/windows/sweef.manifest" > "$mf"
  local pre=""; [ -n "$VERSION_PRE" ] && pre="-prerelease"
  for arch in amd64 arm64; do
    "$PT" syso -arch "$arch" -o "$DIST/windows-resources/rsrc_windows_$arch.syso" \
      -manifest "$mf" -ico "$DIST/icons/sweef.ico" -version "$VERSION4" -display-version "$VERSION" \
      -name Sweef -desc "Sweef Browser" -company "Sweef" -original sweef.exe $pre
    local dst="$ROOT/${MAIN_PKG#./}/sweef_windows_$arch.syso"
    cp "$DIST/windows-resources/rsrc_windows_$arch.syso" "$dst"
    SYSO_FILES+=("$dst")
    # the GUI launcher (Sweef.exe) gets the same icon/manifest and its own version strings
    "$PT" syso -arch "$arch" -o "$DIST/windows-resources/launcher_windows_$arch.syso" \
      -manifest "$mf" -ico "$DIST/icons/sweef.ico" -version "$VERSION4" -display-version "$VERSION" \
      -name Sweef -desc "Sweef Browser launcher" -company "Sweef" -original Sweef.exe $pre
    dst="$ROOT/packaging/tools/launcher/sweef_windows_$arch.syso"
    cp "$DIST/windows-resources/launcher_windows_$arch.syso" "$dst"
    SYSO_FILES+=("$dst")
  done
}

build_one() { # os arch outdir
  local os="$1" arch="$2" out="$3" ext=""
  [ "$os" = windows ] && ext=".exe"
  mkdir -p "$out"
  ( cd "$ROOT" && GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "$out/sweef$ext" "$MAIN_PKG" ) \
    || die "build failed for $os/$arch"
  if [ "$LAUNCHERS" = 1 ] && { [ "$os" = darwin ] || [ "$os" = windows ]; }; then
    local lf="-s -w -buildid="
    [ "$os" = windows ] && lf="$lf -H=windowsgui"
    ( cd "$ROOT" && GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "$lf" -o "$out/Sweef$ext" ./packaging/tools/launcher ) \
      || die "launcher build failed for $os/$arch"
  fi
  if [ "$CDM_HOST" = 1 ]; then
    # The helper dlopen's the user's Widevine CDM, so unlike sweef it is built to be dynamically linked on Linux
    # (purego's loader import). It never ships a CDM. The browser binary must stay static: see
    # cmd/sweef/static_linux_test.go.
    ( cd "$ROOT" && GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "-s -w -buildid=" -o "$out/sweef-cdm$ext" ./cmd/sweef-cdm ) \
      || die "sweef-cdm build failed for $os/$arch"
  fi
  local size; size="$(wc -c < "$out/sweef$ext" | tr -d ' ')"
  printf '    %-14s %10s bytes (%s MiB)\n' "$os/$arch" "$size" "$((size / 1048576))" >&2
  if [ "$size" -gt "$MAX_BYTES" ]; then
    die "$os/$arch binary is $size bytes, over the budget of $MAX_BYTES (SWEEF_MAX_BINARY_BYTES). Investigate before shipping."
  fi
}

build_all() { # outroot
  local t
  for t in $TARGETS; do
    build_one "${t%/*}" "${t#*/}" "$1/${t%/*}_${t#*/}"
  done
}

case " $TARGETS " in *" windows/"*) prepare_windows_resources ;; esac

if [ "$CHECK_REPRO" = 1 ]; then
  log "reproducibility check: building twice (second with a cold, separate GOCACHE)"
  A="$DIST/repro/a"; B="$DIST/repro/b"
  rm -rf "$DIST/repro"; build_all "$A"
  cold="$(mktemp -d)"
  GOCACHE="$cold" build_all "$B" || { rm -rf "$cold"; exit 1; }
  chmod -R u+w "$cold" 2>/dev/null; rm -rf "$cold"
  ( cd "$A" && find . -type f | LC_ALL=C sort ) | while read -r f; do
    if ! cmp -s "$A/$f" "$B/$f"; then echo "NOT REPRODUCIBLE: $f" >&2; echo bad > "$DIST/repro/.bad"; fi
  done
  [ ! -e "$DIST/repro/.bad" ] || die "builds differ"
  log "reproducible: all binaries byte-identical across two builds"
  rm -rf "$DIST/repro"
fi

rm -rf "${DIST:?}/bin"
build_all "$DIST/bin"

# Does the binary report the version we injected? (-X silently ignores a const.)
host_os="$(go env GOHOSTOS)"; host_arch="$(go env GOHOSTARCH)"
hb="$DIST/bin/${host_os}_${host_arch}/sweef"
if [ -x "$hb" ]; then
  reported="$("$hb" version 2>&1 | head -n1 || true)"
  case "$reported" in
    *"$VERSION"*) log "binary reports: $reported" ;;
    *)
      msg="built binary reports '$reported', expected '$VERSION'. cmd/sweef/main.go declares version as a const, so -X main.version cannot take effect: change it to 'var version = \"dev\"' (and add 'var commit = \"unknown\"')."
      if [ "${STRICT_VERSION:-0}" = 1 ]; then die "$msg"; else warn "$msg"; fi ;;
  esac
fi

( cd "$DIST/bin" && find . -type f ! -name SHA256SUMS | sed 's|^\./||' | LC_ALL=C sort | while read -r f; do
    printf '%s  %s\n' "$(sha256_of "$f")" "$f"; done > SHA256SUMS )
log "wrote $DIST/bin/SHA256SUMS"
