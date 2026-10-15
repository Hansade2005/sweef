#!/usr/bin/env bash
# Reproduce the agent-surface benchmark: Sweef MCP / SDK / CLI vs @playwright/mcp,
# playwright-core and agent-browser. Uses the preinstalled Chromium; never runs
# `playwright install`.
#
#   bench/surfaces/run.sh [--before /path/to/older/sweef] [--out bench/surfaces/results/$(date +%F)]
#
# Needs: Go, Node 22+, network access for npm ci and the real-page rows.
set -euo pipefail
cd "$(dirname "$0")/../.."
OUT="bench/surfaces/results/$(date +%F)"
BEFORE=()
while [ $# -gt 0 ]; do
  case "$1" in
    --before) BEFORE=(--before "$2"); shift 2;;
    --out) OUT="$2"; shift 2;;
    *) echo "unknown arg $1" >&2; exit 2;;
  esac
done
export CHROME="${CHROME:-/opt/pw-browsers/chromium}" PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-/opt/pw-browsers}"
WORK="$(mktemp -d)"
trap 'kill $SITE_PID 2>/dev/null || true; rm -rf "$WORK"' EXIT
mkdir -p "$OUT"

CGO_ENABLED=0 go build -ldflags='-s -w' -o "$WORK/sweef" ./cmd/sweef   # release flags: static, stripped
go build -o "$WORK/site" ./bench/site
"$WORK/site" -addr 127.0.0.1:18080 >/dev/null 2>&1 &
SITE_PID=$!
for _ in $(seq 1 100); do curl -fs http://127.0.0.1:18080/ >/dev/null 2>&1 && break; sleep 0.1; done

cd bench/surfaces/node
PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm ci --no-audit --no-fund >/dev/null
chmod +x node_modules/agent-browser/bin/agent-browser-linux-x64 2>/dev/null || true
R="$(cd ../../.. && pwd)/$OUT"
node mcp_bench.mjs --sweef "$WORK/sweef" "${BEFORE[@]}" --chrome "$CHROME" -n 9 --real 7 --out "$R/mcp.json"
node sdk_bench.mjs --sweef "$WORK/sweef" -r 5 -n 9 --out "$R/sdk.json"
node cli_bench.mjs --sweef "$WORK/sweef" --chrome "$CHROME" -r 7 --out "$R/cli-cold.json"
node cli_bench.mjs --sweef "$WORK/sweef" --chrome "$CHROME" -r 7 --warm-cache --out "$R/cli-warm.json"
node report.mjs --mcp "$R/mcp.json" --sdk "$R/sdk.json" --cli "$R/cli-cold.json" --cli-warm "$R/cli-warm.json" > "$R/report.md"
echo "wrote $R/report.md"
