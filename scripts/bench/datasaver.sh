#!/usr/bin/env bash
# Run the Data Saver benchmark (bench/datasaver) and regenerate docs/data-saver-bench.md.
#
#   scripts/bench/datasaver.sh [extra datasaver flags]       e.g. -runs 3 -label full
#
# - Uses the engine's DataPolicy/DataStats API (build tag datasaver_engine) when `go doc ./internal/engine DataPolicy`
#   succeeds, so Sweef standard and strict run; otherwise those modes are skipped and the report says why (and prints
#   no "% saved" headline).
# - Outbound traffic goes through $HTTPS_PROXY (the sandbox proxy) via the harness's own recording proxy.
# - SNAPSHOT=1 builds from a copy of the working tree (excluding .git), so concurrent edits by other teams cannot
#   break or change the run half-way; the snapshot hash of internal/engine and internal/net is recorded in the results.
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD

TAGS=""
if go doc ./internal/engine DataPolicy >/dev/null 2>&1; then TAGS="-tags datasaver_engine"; fi
echo "engine data policy API: ${TAGS:+present (building with $TAGS)}${TAGS:-absent (Sweef standard/strict will be skipped)}" >&2

BIN="${TMPDIR:-/tmp}/datasaver.$$"
trap 'rm -f "$BIN"' EXIT
SRC=$ROOT
if [ "${SNAPSHOT:-0}" = 1 ]; then
  SRC="${TMPDIR:-/tmp}/datasaver-snap.$$"
  trap 'rm -f "$BIN"; rm -rf "$SRC"' EXIT
  mkdir -p "$SRC"
  tar --exclude=.git --exclude=./sweef --exclude=bench/datasaver/results -cf - . | tar -xf - -C "$SRC"
fi
H=$(cd "$SRC" && find internal/engine internal/net -type f \( -name '*.go' -o -name '*.js' \) ! -name '*_test.go' | sort | xargs cat | sha256sum | cut -c1-12)
COMMIT=$(git rev-parse --short HEAD)
DIRTY=""; [ -n "$(git status --porcelain -- internal/engine internal/net)" ] && DIRTY=" + uncommitted engine/net changes"
( cd "$SRC" && go build $TAGS -ldflags "-X 'main.buildSource=$COMMIT$DIRTY, engine+net source hash $H'" -o "$BIN" ./bench/datasaver )

# the tool runs from the repo root so that relative paths (sites, results, docs) resolve
"$BIN" "$@"
