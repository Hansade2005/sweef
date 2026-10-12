#!/usr/bin/env bash
# Startup and memory of the Sweef GUI vs headless Chromium on the same local
# fixture pages. Thin wrapper around internal/app/cmd/appbench, which does the
# measuring (time to a screenshot, and RSS/PSS summed over the whole process
# tree with 1 and 10 tabs) and prints a markdown report.
#
#   bench/gui_vs_chromium.sh                       # auto-detects /opt/pw-browsers/chromium*
#   CHROMIUM=/usr/bin/chromium bench/gui_vs_chromium.sh
#   OUT=/tmp/cmp.md bench/gui_vs_chromium.sh -quick
#
# Read the caveats at the end of the report before quoting a ratio: headless
# Chromium draws no browser UI and renders far more of the web; Sweef paints
# its own chrome and is a single process.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT="${OUT:-bench/last-run/gui-vs-chromium.md}"
mkdir -p "$(dirname "$OUT")"
exec go run ./internal/app/cmd/appbench -only-chromium -chromium "${CHROMIUM:-}" -out "$OUT" "$@"
