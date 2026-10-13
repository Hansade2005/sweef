#!/usr/bin/env bash
# Side-by-side benchmark: Sweef's desktop GUI vs full, headed Chromium, both under the same Xvfb.
#
#   scripts/bench/vschrome.sh                      # full run: N=15 per browser, label "run-<commit>"
#   scripts/bench/vschrome.sh -label baseline      # label prefix; the commit is appended: baseline-<commit>
#   scripts/bench/vschrome.sh -src worktree        # build Sweef from the working tree instead of `git archive HEAD`
#   scripts/bench/vschrome.sh -quick               # smoke run (3 min): never writes docs/vs-chrome.md
#   scripts/bench/vschrome.sh -runs 20 -idle 30    # any other flag is passed to `vschrome run`
#
# Output: bench/vschrome/results/<label>.json and, for a full run, docs/vs-chrome.md (generated from the JSON).
# A full run takes roughly 2-3 hours (15 iterations x 2 browsers x ~4.5 min). It restarts Xvfb for every measured
# launch, needs root for `drop_caches` (cold-cache start-up), and wants an otherwise idle machine.
#
# Needs: Go, Xvfb, git, tar, gzip, xz, and Chromium (CHROMIUM=/path/to/chrome; default /opt/pw-browsers/chromium).
set -euo pipefail
cd "$(dirname "$0")/../.."
ROOT=$PWD

PREFIX=run
SRC=head
QUICK=0
PASS=()
while [ $# -gt 0 ]; do
  case "$1" in
    -label) PREFIX=$2; shift 2 ;;
    -src) SRC=$2; shift 2 ;;
    -quick) QUICK=1; PASS+=("-quick" "-runs" "1" "-idle" "5"); shift ;;
    *) PASS+=("$1"); shift ;;
  esac
done

for t in Xvfb go git tar gzip; do
  command -v "$t" >/dev/null || { echo "vschrome: missing $t" >&2; exit 1; }
done
CHROMIUM=${CHROMIUM:-/opt/pw-browsers/chromium}
[ -x "$CHROMIUM" ] || { echo "vschrome: Chromium not found at $CHROMIUM (set CHROMIUM=)" >&2; exit 1; }

COMMIT=$(git rev-parse --short HEAD)
DIRTY=false
TMP=$(mktemp -d "${TMPDIR:-/tmp}/vschrome-build-XXXXXX")
trap 'rm -rf "$TMP"' EXIT

case "$SRC" in
  head)
    # The exact committed tree: other people's uncommitted edits cannot break or change the build.
    mkdir "$TMP/src"
    git archive HEAD | tar -x -C "$TMP/src"
    (cd "$TMP/src" && go build -trimpath -ldflags="-s -w" -o "$TMP/sweef" ./cmd/sweef)
    ;;
  worktree)
    if [ -n "$(git status --porcelain -- . ':!bench/vschrome' ':!docs/vs-chrome.md' ':!scripts/bench/vschrome.sh')" ]; then DIRTY=true; fi
    go build -trimpath -ldflags="-s -w" -o "$TMP/sweef" ./cmd/sweef
    ;;
  *) echo "vschrome: -src head|worktree" >&2; exit 2 ;;
esac
go build -o "$TMP/vschrome" ./bench/vschrome

LABEL="$PREFIX-$COMMIT"
[ "$DIRTY" = true ] && LABEL="$LABEL-dirty"
OUT=bench/vschrome/results
mkdir -p "$OUT"

"$TMP/vschrome" run -sweef "$TMP/sweef" -chromium "$CHROMIUM" -label "$LABEL" -out "$OUT" \
  -sweef-commit "$COMMIT" -sweef-dirty="$DIRTY" -sweef-source "$SRC" ${PASS[@]+"${PASS[@]}"}

if [ "$QUICK" = 1 ]; then
  "$TMP/vschrome" report -in "$OUT/$LABEL.json" -out "$OUT/$LABEL.md"
  echo "smoke report: $OUT/$LABEL.md (docs/vs-chrome.md untouched)"
else
  "$TMP/vschrome" report -in "$OUT/$LABEL.json" -out docs/vs-chrome.md
fi
