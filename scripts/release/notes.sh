#!/usr/bin/env bash
# Render GitHub release notes to stdout from packaging/release-notes.md.tmpl.
#   scripts/release/notes.sh [previous-tag]
# Env: MACOS_SIGNED=1 / WINDOWS_SIGNED=1 when the corresponding artifacts really were signed.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
resolve_all
prev="${1:-$(git -C "$ROOT" describe --tags --abbrev=0 --match 'v[0-9]*' "HEAD^" 2>/dev/null || true)}"
if [ -n "$prev" ]; then range="$prev..HEAD"; else range="HEAD"; fi
changes="$(git -C "$ROOT" log --no-merges --format='- %s (%h)' "$range" 2>/dev/null | head -n 60 || true)"
[ -n "$changes" ] || changes="- Initial alpha."

mac_note="unsigned alpha: Gatekeeper will block it, see docs/install.md"; [ "${MACOS_SIGNED:-0}" = 1 ] && mac_note="signed and notarized"
win_note="unsigned alpha: SmartScreen will warn (see docs/install.md)"; [ "${WINDOWS_SIGNED:-0}" = 1 ] && win_note="Authenticode signed (SmartScreen reputation still builds over time)"
if [ "${MACOS_SIGNED:-0}" = 1 ] && [ "${WINDOWS_SIGNED:-0}" = 1 ]; then sign_note="signed; SmartScreen reputation for a new publisher still takes time to build."
elif [ "${MACOS_SIGNED:-0}" = 1 ] || [ "${WINDOWS_SIGNED:-0}" = 1 ]; then sign_note="only partly signed (see the table above); the unsigned ones show OS security warnings."
else sign_note="**unsigned**: macOS Gatekeeper and Windows SmartScreen will show warnings, and Linux packages are not GPG-signed."; fi

awk -v v="$VERSION" -v c="$COMMIT" -v mn="$mac_note" -v wn="$win_note" -v sn="$sign_note" -v ch="$changes" '
  { gsub(/@VERSION@/, v); gsub(/@COMMIT@/, c); gsub(/@MAC_NOTE@/, mn); gsub(/@WIN_NOTE@/, wn); gsub(/@SIGN_NOTE@/, sn)
    if ($0 ~ /@CHANGES@/) { print ch } else print }' "$ROOT/packaging/release-notes.md.tmpl"
