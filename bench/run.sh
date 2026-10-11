#!/usr/bin/env bash
# Head-to-head benchmark: Sweef vs Vercel agent-browser (Chromium).
#
# Both tools run the same agent flow against a local fixture site so network
# noise is excluded:
#   open → snapshot -i → click a link → snapshot → fill a form field →
#   press Enter → get title → eval → snapshot -i → close
#
# Measures wall time per command (median of N runs), cold start to first
# snapshot, snapshot bytes, and daemon RSS. Prints a markdown table.
#
# Usage: bench/run.sh [-n runs] [--ab path/to/agent-browser] [--chrome /path/to/chrome]
set -euo pipefail
cd "$(dirname "$0")/.."

RUNS=5
AB="${AB:-$(command -v agent-browser || true)}"
CHROME="${CHROME:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    -n) RUNS="$2"; shift 2;;
    --ab) AB="$2"; shift 2;;
    --chrome) CHROME="$2"; shift 2;;
    *) echo "unknown arg $1"; exit 2;;
  esac
done

WORK="$(mktemp -d)"
trap 'kill $SITE_PID 2>/dev/null || true; "$SWEEF" close >/dev/null 2>&1 || true; [ -n "$AB" ] && "$AB" close >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT

go build -o "$WORK/sweef" ./cmd/sweef
SWEEF="$WORK/sweef"
export SWEEF_HOME="$WORK/sweef-home"

# fixture site: a landing page, a form page, a React counter
go run ./bench/site -addr 127.0.0.1:18080 >/dev/null 2>&1 &
SITE_PID=$!
for i in $(seq 1 50); do curl -fs http://127.0.0.1:18080/ >/dev/null 2>&1 && break; sleep 0.1; done
BASE=http://127.0.0.1:18080

now_ms() { date +%s%3N; }
median() { sort -n | awk '{a[NR]=$1} END {print (NR%2? a[(NR+1)/2] : (a[NR/2]+a[NR/2+1])/2)}'; }

# run_flow TOOL  -> prints "step ms" lines
run_flow_sweef() {
  local t0 t1
  t0=$(now_ms); "$SWEEF" open "$BASE/" >/dev/null;              t1=$(now_ms); echo "open $((t1-t0))"
  t0=$(now_ms); "$SWEEF" snapshot -i > "$WORK/s1.txt";            t1=$(now_ms); echo "snapshot-i $((t1-t0))"
  t0=$(now_ms); "$SWEEF" click "text=Sign in" >/dev/null;          t1=$(now_ms); echo "click $((t1-t0))"
  t0=$(now_ms); "$SWEEF" snapshot > "$WORK/s2.txt";                t1=$(now_ms); echo "snapshot $((t1-t0))"
  t0=$(now_ms); "$SWEEF" fill "#email" "ada@example.com" >/dev/null; t1=$(now_ms); echo "fill $((t1-t0))"
  t0=$(now_ms); "$SWEEF" press Enter "#email" >/dev/null;          t1=$(now_ms); echo "press $((t1-t0))"
  t0=$(now_ms); "$SWEEF" get title >/dev/null;                     t1=$(now_ms); echo "get-title $((t1-t0))"
  t0=$(now_ms); "$SWEEF" open "$BASE/app" >/dev/null;             t1=$(now_ms); echo "open-react $((t1-t0))"
  t0=$(now_ms); "$SWEEF" click "#inc" >/dev/null;                  t1=$(now_ms); echo "click-react $((t1-t0))"
  t0=$(now_ms); "$SWEEF" eval 'document.querySelector("h1").textContent' >/dev/null; t1=$(now_ms); echo "eval $((t1-t0))"
  t0=$(now_ms); "$SWEEF" snapshot -i > "$WORK/s3.txt";            t1=$(now_ms); echo "snapshot-i-2 $((t1-t0))"
}

run_flow_ab() {
  local t0 t1 chromeflag=()
  [ -n "$CHROME" ] && chromeflag=(--executable-path "$CHROME")
  t0=$(now_ms); "$AB" open "$BASE/" "${chromeflag[@]}" >/dev/null; t1=$(now_ms); echo "open $((t1-t0))"
  t0=$(now_ms); "$AB" snapshot -i > "$WORK/a1.txt";                t1=$(now_ms); echo "snapshot-i $((t1-t0))"
  t0=$(now_ms); "$AB" find text "Sign in" click >/dev/null;        t1=$(now_ms); echo "click $((t1-t0))"
  t0=$(now_ms); "$AB" snapshot > "$WORK/a2.txt";                    t1=$(now_ms); echo "snapshot $((t1-t0))"
  t0=$(now_ms); "$AB" fill "#email" "ada@example.com" >/dev/null;   t1=$(now_ms); echo "fill $((t1-t0))"
  t0=$(now_ms); "$AB" press Enter >/dev/null;                       t1=$(now_ms); echo "press $((t1-t0))"
  t0=$(now_ms); "$AB" get title >/dev/null;                         t1=$(now_ms); echo "get-title $((t1-t0))"
  t0=$(now_ms); "$AB" open "$BASE/app" >/dev/null;                  t1=$(now_ms); echo "open-react $((t1-t0))"
  t0=$(now_ms); "$AB" click "#inc" >/dev/null;                      t1=$(now_ms); echo "click-react $((t1-t0))"
  t0=$(now_ms); "$AB" eval 'document.querySelector("h1").textContent' >/dev/null; t1=$(now_ms); echo "eval $((t1-t0))"
  t0=$(now_ms); "$AB" snapshot -i > "$WORK/a3.txt";                t1=$(now_ms); echo "snapshot-i-2 $((t1-t0))"
}

rss_of() { # rss_of <pattern> -> max RSS KB of matching processes
  ps -eo rss,args | grep -E "$1" | grep -v grep | awk '{s+=$1} END {print s+0}'
}

bench() { # bench NAME flowfn closefn rsspattern
  local name="$1" flow="$2" closefn="$3" pat="$4"
  : > "$WORK/$name.steps"
  for r in $(seq 1 "$RUNS"); do
    $closefn >/dev/null 2>&1 || true
    sleep 0.2
    local t0 t1
    t0=$(now_ms); $flow >> "$WORK/$name.steps"; t1=$(now_ms)
    echo "total $((t1-t0))" >> "$WORK/$name.steps"
    echo "rss $(rss_of "$pat")" >> "$WORK/$name.steps"
  done
}

echo "Benchmarking sweef ($RUNS runs)..." >&2
bench sweef run_flow_sweef "$SWEEF close" 'sweef serve'
SWEEF_S1=$(wc -c < "$WORK/s1.txt"); SWEEF_S2=$(wc -c < "$WORK/s2.txt"); SWEEF_S3=$(wc -c < "$WORK/s3.txt")

if [ -n "$AB" ] && [ -x "$AB" ]; then
  echo "Benchmarking agent-browser ($RUNS runs)..." >&2
  bench ab run_flow_ab "$AB close" 'chrome|agent-browser'
  AB_S1=$(wc -c < "$WORK/a1.txt"); AB_S2=$(wc -c < "$WORK/a2.txt"); AB_S3=$(wc -c < "$WORK/a3.txt")
fi

steps="open snapshot-i click snapshot fill press get-title open-react click-react eval snapshot-i-2 total rss"
echo
echo "| step | sweef (ms) | agent-browser (ms) | speedup |"
echo "|---|---|---|---|"
for s in $steps; do
  sw=$(grep "^$s " "$WORK/sweef.steps" | awk '{print $2}' | median)
  if [ -n "${AB:-}" ] && [ -f "$WORK/ab.steps" ]; then
    ab=$(grep "^$s " "$WORK/ab.steps" | awk '{print $2}' | median)
    if [ "$s" = rss ]; then
      echo "| daemon RSS (KB) | $sw | $ab | $(awk -v a="$ab" -v b="$sw" 'BEGIN{ if (b>0) printf "%.1fx less", a/b; else print "-"}') |"
    else
      echo "| $s | $sw | $ab | $(awk -v a="$ab" -v b="$sw" 'BEGIN{ if (b>0) printf "%.1fx", a/b; else print "-"}') |"
    fi
  else
    echo "| $s | $sw | - | - |"
  fi
done
echo
echo "| snapshot | sweef bytes | agent-browser bytes |"
echo "|---|---|---|"
echo "| landing, interactive only | $SWEEF_S1 | ${AB_S1:--} |"
echo "| form page, full | $SWEEF_S2 | ${AB_S2:--} |"
echo "| react app, interactive only | $SWEEF_S3 | ${AB_S3:--} |"
