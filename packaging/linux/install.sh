#!/bin/sh
# Sweef installer for Linux (user-level by default, no root needed).
#
#   curl -fsSL https://raw.githubusercontent.com/sweefbrowser/sweef/main/packaging/linux/install.sh | sh
#   curl -fsSL .../install.sh | sh -s -- --prefix /opt/sweef --version 0.1.0-alpha.7
#
# Options:
#   --prefix DIR      install under DIR (default: $HOME/.local)
#   --version VER     install a specific version (default: latest release, including pre-releases)
#   --base-url URL    download from URL instead of GitHub Releases (must contain SHA256SUMS)
#   --sha256 HEX      additionally require the tarball to have exactly this SHA-256
#   --from-dir DIR    install from an already extracted tarball directory (offline)
#   --no-desktop      do not install the desktop entry / icons / metainfo
#   --set-default     also make Sweef the default web browser (xdg-settings)
#   --uninstall       remove a previous installation made with the same --prefix
#   --yes, -y         do not ask questions
#   --help
#
# Safety: the whole script is wrapped in main() so a truncated download cannot run half of it;
# the downloaded tarball is verified against SHA256SUMS from the same release before anything is
# extracted; nothing outside --prefix is touched except ~/.local/share caches via the usual
# desktop tools. SHA256SUMS comes from the same origin as the tarball, so it detects corruption
# and mismatched mirrors, not a compromised release page: use --sha256 with a hash obtained
# elsewhere (or the GitHub attestation, see docs/install.md) when that matters.
set -eu

# Replaced with the release version when this file is attached to a GitHub release.
DEFAULT_VERSION="@DEFAULT_VERSION@"
REPO="sweefbrowser/sweef"

say()  { printf '%s\n' "$*"; }
err()  { printf 'sweef-install: error: %s\n' "$*" >&2; exit 1; }
warn() { printf 'sweef-install: warning: %s\n' "$*" >&2; }

have() { command -v "$1" >/dev/null 2>&1; }

ask() { # prompt -> 0 for yes
  [ "$ASSUME_YES" = 1 ] && return 0
  if [ -r /dev/tty ] && [ -w /dev/tty ]; then
    printf '%s [Y/n] ' "$1" > /dev/tty
    read -r reply < /dev/tty || reply=""
    case "$reply" in n|N|no|NO) return 1 ;; esac
  fi
  return 0
}

download() { # url dest
  case "$1" in
    file://*) cp "${1#file://}" "$2" ;;
    *)
      if have curl; then curl -fsSL --retry 3 -o "$2" "$1"
      elif have wget; then wget -q -O "$2" "$1"
      else err "need curl or wget"; fi ;;
  esac
}

sha256_of() {
  if have sha256sum; then sha256sum "$1" | cut -d' ' -f1
  elif have shasum; then shasum -a 256 "$1" | cut -d' ' -f1
  elif have openssl; then openssl dgst -sha256 "$1" | sed 's/^.*= //'
  else err "need sha256sum, shasum or openssl to verify the download"; fi
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    *) err "unsupported CPU architecture: $(uname -m) (Sweef ships amd64 and arm64)" ;;
  esac
}

latest_version() {
  [ -n "$DEFAULT_VERSION" ] && case "$DEFAULT_VERSION" in @*) ;; *) echo "$DEFAULT_VERSION"; return ;; esac
  tmp="$(mktemp)"
  download "https://api.github.com/repos/$REPO/releases?per_page=1" "$tmp" || { rm -f "$tmp"; err "could not look up the latest release; pass --version"; }
  v="$(sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' "$tmp" | head -n 1)"
  rm -f "$tmp"
  [ -n "$v" ] || err "no release found; pass --version"
  echo "$v"
}

# Refuse anything that is not an absolute path under the prefix.
safe_path() {
  case "$1" in
    "$PREFIX"/*) ;;
    *) return 1 ;;
  esac
  case "$1" in *..*) return 1 ;; esac
  return 0
}

do_uninstall() {
  manifest="$PREFIX/share/sweef/install-manifest.txt"
  [ -f "$manifest" ] || err "no installation found under $PREFIX (missing $manifest)"
  say "This will remove Sweef from $PREFIX ($(wc -l < "$manifest" | tr -d ' ') files)."
  say "Your browsing data (~/.config/sweef, ~/.local/share/sweef, ~/.cache/sweef) is NOT touched."
  ask "Continue?" || { say "Cancelled."; exit 0; }
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    if safe_path "$f"; then
      rm -f "$f"
      d="$(dirname "$f")"
      while [ "$d" != "$PREFIX" ] && [ "$d" != "/" ] && rmdir "$d" 2>/dev/null; do d="$(dirname "$d")"; done
    else
      warn "skipping unexpected manifest entry: $f"
    fi
  done < "$manifest"
  rm -f "$manifest"
  rmdir "$PREFIX/share/sweef" 2>/dev/null || true
  refresh_caches
  say "Sweef was removed."
}

refresh_caches() {
  if [ "$DESKTOP" = 1 ]; then
    have update-desktop-database && update-desktop-database -q "$PREFIX/share/applications" 2>/dev/null || true
    have gtk-update-icon-cache && [ -f "$PREFIX/share/icons/hicolor/index.theme" ] && gtk-update-icon-cache -q -t -f "$PREFIX/share/icons/hicolor" 2>/dev/null || true
  fi
}

install_from_dir() { # srcdir
  src="$1"
  [ -x "$src/bin/sweef" ] || err "$src/bin/sweef not found: not a Sweef tarball directory"
  say "Installing to $PREFIX ..."
  manifest_tmp="$(mktemp)"
  put() { # mode srcfile destfile
    mkdir -p "$(dirname "$3")"
    cp "$2" "$3.new.$$"
    chmod "$1" "$3.new.$$"
    mv -f "$3.new.$$" "$3"
    printf '%s\n' "$3" >> "$manifest_tmp"
  }
  put 755 "$src/bin/sweef" "$PREFIX/bin/sweef"
  put 755 "$src/bin/sweef-browser" "$PREFIX/bin/sweef-browser"
  [ -f "$src/NOTICE" ] && put 644 "$src/NOTICE" "$PREFIX/share/sweef/NOTICE"
  [ -f "$src/install.sh" ] && put 755 "$src/install.sh" "$PREFIX/share/sweef/install.sh"

  if [ "$DESKTOP" = 1 ]; then
    # absolute Exec so the entry works even when $PREFIX/bin is not on the launcher's PATH
    exec_q="$PREFIX/bin/sweef"
    case "$exec_q" in *" "*) exec_q="\"$exec_q\"" ;; esac
    desk_tmp="$(mktemp)"
    sed -e "s|^Exec=sweef |Exec=$exec_q |" -e "s|^TryExec=sweef\$|TryExec=$PREFIX/bin/sweef|" \
        "$src/share/applications/sweef.desktop" > "$desk_tmp"
    put 644 "$desk_tmp" "$PREFIX/share/applications/sweef.desktop"
    rm -f "$desk_tmp"
    for f in "$src"/share/icons/hicolor/*/apps/*; do
      [ -f "$f" ] || continue
      rel="${f#"$src"/}"
      put 644 "$f" "$PREFIX/$rel"
    done
    [ -f "$src/share/metainfo/sweef.metainfo.xml" ] && put 644 "$src/share/metainfo/sweef.metainfo.xml" "$PREFIX/share/metainfo/sweef.metainfo.xml"
    [ -f "$src/share/man/man1/sweef.1.gz" ] && put 644 "$src/share/man/man1/sweef.1.gz" "$PREFIX/share/man/man1/sweef.1.gz"
  fi

  # uninstall helper
  unin="$(mktemp)"
  cat > "$unin" <<EOF
#!/bin/sh
# Generated by the Sweef installer.
exec "$PREFIX/share/sweef/install.sh" --uninstall --prefix "$PREFIX" "\$@"
EOF
  if [ -f "$PREFIX/share/sweef/install.sh" ]; then put 755 "$unin" "$PREFIX/bin/sweef-uninstall"; fi
  rm -f "$unin"

  printf '%s\n' "$PREFIX/share/sweef/install-manifest.txt" >> "$manifest_tmp"
  mkdir -p "$PREFIX/share/sweef"
  mv -f "$manifest_tmp" "$PREFIX/share/sweef/install-manifest.txt"
  chmod 644 "$PREFIX/share/sweef/install-manifest.txt"
  refresh_caches

  if [ "$SET_DEFAULT" = 1 ]; then
    if have xdg-settings; then xdg-settings set default-web-browser sweef.desktop && say "Sweef is now your default browser."
    else warn "xdg-settings not found; set the default browser in your desktop settings."; fi
  fi

  say ""
  "$PREFIX/bin/sweef" version || true
  say ""
  say "Installed. Start it from your applications menu, or run:  $PREFIX/bin/sweef gui"
  case ":$PATH:" in
    *":$PREFIX/bin:"*) ;;
    *) say "Note: $PREFIX/bin is not on your PATH. Add it to run 'sweef' from a terminal." ;;
  esac
  say "Make it your default browser:  xdg-settings set default-web-browser sweef.desktop"
  say "Uninstall:                     $PREFIX/bin/sweef-uninstall"
}

main() {
  PREFIX="${HOME:-/tmp}/.local"
  VERSION_ARG=""
  BASE_URL=""
  WANT_SHA=""
  FROM_DIR=""
  DESKTOP=1
  SET_DEFAULT=0
  UNINSTALL=0
  ASSUME_YES=0
  while [ $# -gt 0 ]; do
    case "$1" in
      --prefix)      [ $# -ge 2 ] || err "--prefix needs a value"; PREFIX="$2"; shift 2 ;;
      --prefix=*)    PREFIX="${1#*=}"; shift ;;
      --version)     [ $# -ge 2 ] || err "--version needs a value"; VERSION_ARG="$2"; shift 2 ;;
      --version=*)   VERSION_ARG="${1#*=}"; shift ;;
      --base-url)    [ $# -ge 2 ] || err "--base-url needs a value"; BASE_URL="$2"; shift 2 ;;
      --sha256)      [ $# -ge 2 ] || err "--sha256 needs a value"; WANT_SHA="$2"; shift 2 ;;
      --from-dir)    [ $# -ge 2 ] || err "--from-dir needs a value"; FROM_DIR="$2"; shift 2 ;;
      --no-desktop)  DESKTOP=0; shift ;;
      --set-default) SET_DEFAULT=1; shift ;;
      --uninstall)   UNINSTALL=1; shift ;;
      -y|--yes)      ASSUME_YES=1; shift ;;
      -h|--help)     sed -n '2,/^set -eu/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'; exit 0 ;;
      *) err "unknown option: $1 (try --help)" ;;
    esac
  done

  case "$PREFIX" in
    /*) ;;
    *) err "--prefix must be an absolute path" ;;
  esac
  PREFIX="${PREFIX%/}"
  [ -n "$PREFIX" ] || err "refusing to use / as prefix"

  [ "$(uname -s)" = Linux ] || err "this installer is for Linux. On macOS use the .dmg, on Windows the setup .exe (see docs/install.md)."

  if [ "$UNINSTALL" = 1 ]; then do_uninstall; exit 0; fi

  if [ -n "$FROM_DIR" ]; then install_from_dir "$(cd "$FROM_DIR" && pwd)"; exit 0; fi

  arch="$(detect_arch)"
  ver="${VERSION_ARG:-$(latest_version)}"
  ver="${ver#v}"
  name="sweef_${ver}_linux_${arch}.tar.gz"
  [ -n "$BASE_URL" ] || BASE_URL="https://github.com/$REPO/releases/download/v${ver}"
  BASE_URL="${BASE_URL%/}"

  work="$(mktemp -d)"
  trap 'rm -rf "$work"' EXIT INT TERM
  say "Downloading Sweef $ver ($arch) ..."
  download "$BASE_URL/$name" "$work/$name" || err "download failed: $BASE_URL/$name"
  download "$BASE_URL/SHA256SUMS" "$work/SHA256SUMS" || err "download failed: $BASE_URL/SHA256SUMS"

  want="$(awk -v n="$name" '$2==n || $2=="*" n {print $1; exit}' "$work/SHA256SUMS")"
  [ -n "$want" ] || err "$name is not listed in SHA256SUMS"
  got="$(sha256_of "$work/$name")"
  [ "$got" = "$want" ] || err "checksum mismatch for $name (expected $want, got $got). Nothing was installed."
  if [ -n "$WANT_SHA" ] && [ "$got" != "$WANT_SHA" ]; then
    err "checksum differs from the one you pinned with --sha256 (got $got). Nothing was installed."
  fi
  say "Checksum OK ($got)"

  mkdir -p "$work/x"
  tar -xzf "$work/$name" -C "$work/x"
  dir="$(find "$work/x" -mindepth 1 -maxdepth 1 -type d | head -n 1)"
  [ -n "$dir" ] || err "unexpected tarball layout"
  install_from_dir "$dir"
}

main "$@"
