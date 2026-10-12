#!/usr/bin/env bash
# Write SHA256SUMS for every release asset in a directory (default: $DIST).
#   scripts/release/checksums.sh [dir]
# Covers sweef_* packages plus install.sh; excludes SHA256SUMS, signatures and latest.json.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
dir="${1:-$DIST}"
cd "$dir"
rm -f SHA256SUMS
files="$(find . -maxdepth 1 -type f \( -name 'sweef_*' -o -name 'install.sh' \) ! -name '*.sig' | sed 's|^\./||' | LC_ALL=C sort)"
[ -n "$files" ] || die "no release assets in $dir"
for f in $files; do printf '%s  %s\n' "$(sha256_of "$f")" "$f"; done > SHA256SUMS
log "wrote $dir/SHA256SUMS ($(wc -l < SHA256SUMS | tr -d ' ') files)"
cat SHA256SUMS >&2
