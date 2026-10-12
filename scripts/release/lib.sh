#!/usr/bin/env bash
# Shared helpers for Sweef release scripts. Source it; do not execute it.
# Compatible with bash 3.2 (macOS runners).

if [ -n "${SWEEF_RELEASE_LIB:-}" ]; then return 0; fi
SWEEF_RELEASE_LIB=1

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export ROOT

log()  { printf '==> %s\n' "$*" >&2; }
warn() { printf '\n!!! WARNING: %s\n\n' "$*" >&2; if [ -n "${GITHUB_ACTIONS:-}" ]; then printf '::warning::%s\n' "$*" >&2; fi; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required tool not found: $1${2:+ ($2)}"; }

# --- version -----------------------------------------------------------------
# Order: $VERSION override, exact git tag vX.Y.Z[-pre], else 0.1.0-alpha.<commit count>.
resolve_version() {
  local v tag n
  if [ -n "${VERSION:-}" ]; then
    v="$VERSION"
  elif tag="$(git -C "$ROOT" describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null)"; then
    v="$tag"
  else
    n="$(git -C "$ROOT" rev-list --count HEAD 2>/dev/null || echo 0)"
    v="0.1.0-alpha.$n"
  fi
  v="${v#v}"
  case "$v" in
    [0-9]*.[0-9]*.[0-9]*) ;;
    *) die "bad version '$v' (want X.Y.Z or X.Y.Z-pre.N)" ;;
  esac
  case "$v" in *[!0-9A-Za-z.+-]*) die "bad characters in version '$v'" ;; esac
  VERSION="$v"
  VERSION_CORE="${v%%-*}"
  VERSION_CORE="${VERSION_CORE%%+*}"
  VERSION_PRE=""
  case "$v" in *-*) VERSION_PRE="${v#*-}"; VERSION_PRE="${VERSION_PRE%%+*}" ;; esac
  # trailing integer of the pre-release (alpha.7 -> 7), 0 when absent
  VERSION_NUM=0
  if [ -n "$VERSION_PRE" ]; then
    n="${VERSION_PRE##*[!0-9]}"
    [ -n "$n" ] && VERSION_NUM="$n"
  fi
  # Debian: 0.1.0~alpha.7 sorts before 0.1.0
  DEB_VERSION="$VERSION_CORE${VERSION_PRE:+~$VERSION_PRE}"
  # RPM: pre-releases use Release "0.<pre>" so the final release (Release 1) sorts higher
  RPM_VERSION="$VERSION_CORE"
  if [ -n "$VERSION_PRE" ]; then RPM_RELEASE="0.$(printf %s "$VERSION_PRE" | tr -- '-' '_')"; else RPM_RELEASE="1"; fi
  # Windows file version a.b.c.d ; macOS bundle version
  VERSION4="$VERSION_CORE.$VERSION_NUM"
  MAC_BUNDLE_VERSION="$VERSION4"
  export VERSION VERSION_CORE VERSION_PRE VERSION_NUM DEB_VERSION RPM_VERSION RPM_RELEASE VERSION4 MAC_BUNDLE_VERSION
}

resolve_commit() {
  if [ -z "${COMMIT:-}" ]; then
    COMMIT="$(git -C "$ROOT" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)"
    if [ "$COMMIT" != unknown ] && ! git -C "$ROOT" diff --quiet HEAD -- 2>/dev/null; then
      COMMIT="$COMMIT-dirty"
      warn "working tree has uncommitted changes; build is NOT reproducible from commit ${COMMIT%-dirty}"
    fi
  fi
  export COMMIT
}

# SOURCE_DATE_EPOCH: env, else last commit time, else a fixed constant.
resolve_epoch() {
  if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
    SOURCE_DATE_EPOCH="$(git -C "$ROOT" log -1 --format=%ct 2>/dev/null || true)"
    [ -n "$SOURCE_DATE_EPOCH" ] || SOURCE_DATE_EPOCH=1700000000
  fi
  export SOURCE_DATE_EPOCH
}

DIST="${DIST:-$ROOT/dist}"
export DIST
resolve_all() { resolve_version; resolve_commit; resolve_epoch; }

# --- deterministic helpers -----------------------------------------------------
# Format an epoch as UTC with a GNU/BSD date compatible call.
fmt_epoch() { # epoch format
  date -u -d "@$1" "+$2" 2>/dev/null || date -u -r "$1" "+$2"
}

# touch every path under $1 to SOURCE_DATE_EPOCH
clamp_mtimes() {
  local ts; ts="$(fmt_epoch "$SOURCE_DATE_EPOCH" %Y%m%d%H%M.%S)"
  find "$1" -exec touch -h -t "$ts" {} +
}

# Reproducible tar.gz of directory $2 (relative name) inside $1 -> $3
make_targz() { # parentdir name out
  need tar; need gzip
  ( cd "$1" && tar --sort=name --mtime="@$SOURCE_DATE_EPOCH" --owner=0 --group=0 --numeric-owner \
      --pax-option=exthdr.name=%d/PaxHeaders/%f,delete=atime,delete=ctime -cf - "$2" | gzip -n -9 > "$3" )
}

# Reproducible zip of directory $2 inside $1 -> $3
make_zip() { # parentdir name out
  need zip
  clamp_mtimes "$1/$2"
  ( cd "$1" && find "$2" | LC_ALL=C sort | TZ=UTC zip -X -q -9 "$3" -@ )
}

sha256_of() { # file
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# Build the host-native pkgtool once and echo its path.
pkgtool() {
  local bin="$DIST/tools/pkgtool"
  if [ ! -x "$bin" ]; then
    mkdir -p "$DIST/tools"
    ( cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -o "$bin" ./packaging/tools/pkgtool ) || die "cannot build pkgtool"
  fi
  printf '%s' "$bin"
}

# Go arch -> distro arch names
deb_arch()  { case "$1" in amd64) echo amd64;; arm64) echo arm64;; *) die "no deb arch for $1";; esac; }
rpm_arch()  { case "$1" in amd64) echo x86_64;; arm64) echo aarch64;; *) die "no rpm arch for $1";; esac; }
appimage_arch() { rpm_arch "$1"; }

# Where build.sh leaves a binary
bin_dir() { printf '%s/bin/%s_%s' "$DIST" "$1" "$2"; }
require_bin() { # os arch name
  local p; p="$(bin_dir "$1" "$2")/$3"
  [ -f "$p" ] || die "missing $p; run scripts/release/build.sh first"
  printf '%s' "$p"
}

# Version string a user would see in `sweef version`
asset_name() { # kind os arch ext  -> sweef_<ver>_<os>_<arch>.<ext>
  printf 'sweef_%s_%s_%s.%s' "$VERSION" "$2" "$3" "$4"
}

: "${MAINTAINER:=Sweef Maintainers <noreply@users.noreply.github.com>}"
: "${HOMEPAGE:=https://github.com/sweefbrowser/sweef}"
: "${DESCRIPTION_SHORT:=Independent pure-Go browser engine with a built-in agent interface}"
export MAINTAINER HOMEPAGE DESCRIPTION_SHORT
