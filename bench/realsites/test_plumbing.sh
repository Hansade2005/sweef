#!/usr/bin/env bash
# TEST-ONLY: verifies Mode B plumbing against a local mock of the Anthropic API.
# Asserts: Mode B refuses without a key and names the env var; both agents run end to end
# against the mock; results are recorded; mock output is labelled and never a benchmark result.
set -uo pipefail
cd "$(dirname "$0")/node"
SWEEF="${SWEEF:?set SWEEF to a built sweef binary}"
fail() { echo "PLUMBING TEST FAILED: $*" >&2; exit 1; }

# 1. refuses without key
msg=$(env -u ANTHROPIC_API_KEY node modeb.mjs --sweef "$SWEEF" --only example-com 2>&1); rc=$?
[ $rc -eq 2 ] || fail "expected exit 2 without key, got $rc"
echo "$msg" | grep -q 'ANTHROPIC_API_KEY' || fail "refusal message does not name ANTHROPIC_API_KEY"
echo "ok: refuses without ANTHROPIC_API_KEY"

# 2. full loop against the mock
MOCK_ANSWER="The page says this domain is for use in documentation examples" node mock_anthropic.mjs 0 > /tmp/mock-port.$$ &
MPID=$!
trap 'kill $MPID 2>/dev/null; rm -f /tmp/mock-port.$$' EXIT
for i in $(seq 1 50); do [ -s /tmp/mock-port.$$ ] && break; sleep 0.1; done
PORT=$(cat /tmp/mock-port.$$)
OUT=$(mktemp -d)/plumbing.json
env -u ANTHROPIC_API_KEY node modeb.mjs --plumbing-test --base-url "http://127.0.0.1:$PORT" --sweef "$SWEEF" --only example-com --runs 1 --out "$OUT" --pace-ms 100 || fail "modeb run"
node -e '
const {meta,runs}=JSON.parse(require("fs").readFileSync(process.argv[1]));
const bad=(m)=>{console.error("PLUMBING TEST FAILED: "+m);process.exit(1)};
if(!meta.mock||!meta.WARNING) bad("mock label missing");
if(runs.length!==2) bad("expected 2 runs, got "+runs.length);
for(const r of runs){
  if(!r.answer_ok) bad(r.tool+" answer not graded ok: "+JSON.stringify(r).slice(0,300));
  if(r.turns!==2) bad(r.tool+" turns="+r.turns);
  if(!(r.usage.input_tokens>0&&r.usage.output_tokens>0)) bad(r.tool+" usage not recorded");
  if(!(r.cost_usd>0)) bad(r.tool+" cost not computed");
  if(JSON.stringify(r).includes("plumbing-test-dummy-key")) bad("key leaked into results");
}
console.log("ok: both agents completed 2 turns against the mock, usage+cost recorded, no key in results");
' "$OUT" || exit 1
echo "PLUMBING TEST PASSED (mock only; no benchmark numbers were produced)"
