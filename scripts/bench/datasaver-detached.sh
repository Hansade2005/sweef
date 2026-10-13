#!/usr/bin/env bash
# Run the Data Saver benchmark detached and resumably (it takes about two hours).
#
#   scripts/bench/datasaver-detached.sh [label]        starts it in the background and returns
#   tail -f bench/datasaver/results/<label>.log        progress; the results file is rewritten after every cell
#
# The driver builds from a snapshot of the tree (SNAPSHOT=1), so edits made while it runs do not change the run.
# If the machine or container restarts, run the same command again: it resumes the results file of the same label
# and skips every finished cell. It stops when the tool reports a complete run (docs/data-saver-bench.md written).
set -euo pipefail
cd "$(dirname "$0")/../.."
LABEL=${1:-full}
RES=bench/datasaver/results
mkdir -p "$RES"
LOG=$RES/$LABEL.log
if [ "${DETACHED_CHILD:-0}" != 1 ]; then
  DETACHED_CHILD=1 nohup "$0" "$LABEL" >>"$LOG" 2>&1 &
  echo "started pid $! ; log $LOG"
  exit 0
fi
for attempt in 1 2 3 4 5 6; do
  FILE=$(ls -t "$RES"/*-"$LABEL".json 2>/dev/null | head -1 || true)
  ARGS=(-runs "${RUNS:-3}" -label "$LABEL")
  [ -n "$FILE" ] && ARGS+=(-resume "$FILE")
  echo "=== attempt $attempt $(date -u +%FT%TZ) ${FILE:+resuming $FILE}"
  SNAPSHOT=1 scripts/bench/datasaver.sh "${ARGS[@]}" && { echo "=== finished $(date -u +%FT%TZ)"; exit 0; }
  echo "=== attempt $attempt failed, retrying in 30 s"
  sleep 30
done
exit 1
