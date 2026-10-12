# Sweef browser agent (`internal/agent`)

The agent is the sidebar's brain: a Go tool loop around the Anthropic Messages
API, owned end to end (transport, SSE parsing, prompt layout, context
management, safety). The browser side is one interface, `Backend`; the app
forwards `Event` values to the chrome page as `agent.event`.

Status: alpha. Everything below is tested against a local fake of the Messages
API (SSE) and a real in-process engine. It has **not** been run against the
live API (no key was available); see "What is not verified".

## Files

| File | Role |
|---|---|
| `client.go`, `sse.go` | direct `net/http` client, tuned HTTP/2 transport, `Prewarm`, retry/backoff, own SSE parser, timing metrics, `CacheStats`, `CostUSD` |
| `loop.go`, `exec.go` | the agent loop, early dispatch, dependency rules between parallel tool calls, take-control gate, run stats |
| `tools.go`, `prompt.go` | the six tools (byte-stable JSON) and the static system prompt (the cache prefix) |
| `context.go`, `diff.go` | history, request serialization, breakpoints, compaction, deltas, snapshot truncation |
| `safety.go` | untrusted-content wrapper, URL denylist, risk classifier, confirm rules, page visibility, per-run limits |
| `backend.go`, `rpcbackend.go` | `Backend` interface; `RPCBackend` over `*engine.Engine` + `rpc.Server` (`Batch.run`) |
| `cli.go` | `RunCLI` for `sweef agent "<task>"` |

## API the app must call

```go
cfg := agent.Config{APIKey: key /* from keychain / 0600 file; never logged */}
ag, err := agent.New(cfg, backend)       // backend implements agent.Backend
ag.Prewarm(ctx)                          // when the sidebar opens (dial + TLS + h2)

runID, events, err := ag.Start(ctx, text) // agent.send; events closes after done/error
//   events: agent.Event{RunID, Kind: text|thought|action|status|confirm|done|error, Text, Confirm, Stats}
ag.Stop()                                // agent.stop
ag.TakeControl()                         // agent.takeControl: pause; tool calls not yet started are refused
ag.Resume(note)                          // hand back; the model is told pages/tabs may have changed
ag.SetPageVisibility(origin, allowed)    // agent.pageVisibility {origin, allowed}
ag.Reset()                               // new chat
ag.CacheStats()                          // hit rate etc.
```

`Start` returns `ErrBusy` if a run is active (one run at a time per Agent).
`Run(ctx, text, emit)` is the synchronous variant and returns `RunStats`.

`Backend` (see `backend.go` for the full contract):
`Snapshot(tab, delta)`, `Act(ctx, steps)`, `Tabs()`, `OpenTab`, `SwitchTab`,
`CloseTab`, `URL(tab)`, `Title(tab)`, `Screenshot(tab)`, `Confirm(ctx, req)`.
It must be safe for concurrent use. `Confirm` blocks until the user answers;
the agent has already emitted an `EventConfirm` so the UI can show the prompt.
Optional `UserAsker` lets `ask_user` block for a free-form answer; without it
`ask_user` ends the run and the user's next chat message is the answer.

`Snapshot(tab, delta=true)` is implemented by `RPCBackend` but the agent core
always requests full snapshots and diffs them itself (`DeltaText`) against the
text the model has actually seen. A backend-side baseline would be shared
with every other caller (`find`, `read_page`, the user's own snapshot
commands) and silently desynchronise from the conversation; the rpc package's
`lastSnap` map is exactly that kind of global. Backends may ignore `delta`.

Event labels (`action` events) contain element names taken from pages. They
are untrusted text: the chrome page must render them with `textContent`, not
`innerHTML`.

## Request layout and prompt-cache design

Haiku 5.5 requests are built by hand (no SDK types, no maps), in cache order:

```
tools (6, sorted by name, hand-written JSON)       <- breakpoint 1 (last tool)
system (one static block, ~17.5 KB)                <- breakpoint 2
messages: history, append-only between compactions
   ... previous request's last block               <- breakpoint 3 (guaranteed read point)
   ... this request's last stable block            <- breakpoint 4 (write point)
   [volatile tail block: <task_memory>, <run_status>]   not cached, not stored
```

* The static prefix is ~23 KB, estimated at ~5.8K tokens (bytes/4; the real
  tokenizer will differ by roughly +-15%), above Haiku's 4096-token minimum
  cacheable prefix with margin. Content is guidance (tool semantics, ref
  syntax, injection rules, batching examples), not filler. `TestToolDefinitionsAndPrefixSize` fails the build if it
  shrinks below 5K estimated tokens.
* The head (model, limits, thinking, effort, tools, system) is rendered once
  per Agent, so it is byte-identical on every request by construction. No
  timestamps, ids, map iteration or per-user data precede breakpoint 4.
* Messages are stored pre-serialized; building a request is memcpy, with
  `cache_control` injected at write time on the two chosen blocks only. Moving
  the breakpoint therefore never changes stored bytes.
* The scratchpad (`note` field of `act`, kept by the agent) and the turn
  counter are rendered into one extra text block at the end of the **last user
  message**, after the final breakpoint, and are not kept in history. The
  system prompt never changes.
* `TestPrefixByteStableAcrossTurns` hashes the serialized prefix before the
  rolling breakpoint for turns 1..6 and asserts: tools+system identical,
  breakpoint 3 equals the previous request's breakpoint 4, the canonical
  prefix up to it is identical, <= 4 breakpoints, tail after the last one.
* Usage is tracked per request (`input`, `output`, `cache_read`,
  `cache_creation`); `CacheStats.HitRate = cache_read / (cache_read + cache_creation + input)`.

Request rules for Haiku 5.5 that the code enforces and a test checks: no
`temperature`/`top_p`/`top_k`; no assistant prefill (history always ends on a
user message); explicit `max_tokens` (default 8192, because thinking counts
against it); `stream:true`; `eager_input_streaming:true` on every tool;
`thinking:{type:"adaptive"}` (or `disabled`, valid at effort <= high) with
`output_config.effort` default `low`. Stop reasons are checked before content
is used: `refusal` ends the run with an error, `max_tokens` turns truncated
tool input into `is_error` results (streamed tool JSON is validated with
`json.Valid`, then structurally), `pause_turn` and `max_tokens`-without-tools
continue, `tool_use`/`end_turn` as expected.

Thinking blocks are replayed verbatim (the API requires it in tool loops).
If the API answers 400 mentioning thinking after the history was edited by a
compaction, the agent strips all thinking blocks once and retries
(`stripThinking`). Whether Haiku 5.5 enforces the history-editing check is
unverified (see risks).

## Context management

1. **Snapshots enter once, then deltas.** The first observation of a page is
   the (truncated) full snapshot. While the URL is unchanged, later
   observations are `DeltaText(prev, cur)`: removed/added lines, each hunk
   introduced by `@ <nearest unchanged line>`. A navigation, a URL change after
   a click, `observe:"full"`, an invalid baseline, or a delta larger than 60%
   of the page all produce a full snapshot. (The diff trims the common
   prefix/suffix before an LCS, unlike `rpc.LineDiff` whose DP table is
   `(n+1)(m+1)` ints.)
2. **Compaction at a fixed boundary.** Only superseded observations (older
   than the newest full snapshot of the same tab) and screenshots are
   replaced by `[page snapshot of <url> at step N elided; re-read with
   read_page]`; deltas after the live baseline are kept so the chain stays
   valid. It runs only when `turn - KeepRecent` crosses a multiple of
   `Every` (default 8) and the prompt exceeds `MinPromptTokens` (24K). At
   `HardPromptTokens` (60K) all old observations go and those tabs' baselines
   are invalidated. `TestCompactionIsAppendCompatibleBetweenBoundaries`
   asserts history is rewritten only on compaction turns, at most once per
   `Every` turns, and the current page is never elided.
3. **Cost model of a compaction** (also in `context.go`): rewriting old
   results invalidates the cache from the first edited block, so that turn
   pays a cache write (1.25x input price) for the whole suffix instead of a
   read (0.1x), once; the shorter prefix is then warm again. At $0.10/MTok a
   40K-token history costs about $0.0004 per turn to read, so compaction pays
   for itself in latency (prefill time) and in staying under the 100K-token
   price tier, not in dollars. On the compaction turn the "previous request"
   breakpoint is dropped because it would point into rewritten content.
4. **Snapshot cap** (`TruncateSnapshot`, default 6000 tokens): lines with a
   `ref=` are kept first (85% of the budget), then text in document order;
   output keeps document order and ends with a notice naming what was cut.
   "Viewport-near first" is approximated by document order, since neither the
   engine snapshot nor `Backend` exposes the viewport/scroll position.
5. **Task memory**: the `note` field of `act`, replace-style, <= 1500 chars,
   shown back in `<task_memory>` in the tail of the last user message.
6. **Page visibility**: pages the user hid are replaced by `[hidden by user]`
   in snapshots, `find`, `extract`, screenshots and tab lists; the agent also
   refuses to act on such a tab. `TestHiddenPagesNeverReachTheModel` greps
   every request body for a secret from the hidden page.

## Tools

Six tools, all with `eager_input_streaming`: `act` (ordered steps:
navigate, click, hover, type, press, scroll, wait, select, back, forward,
extract, screenshot, expect; plus `observe`, `tab`, `note`), `tabs`,
`read_page`, `find`, `ask_user`, `done`. `act` maps to `Batch.run` of
`internal/rpc` (same verbs as `sweef batch` and MCP `browser_batch`);
`expect`, `wait text/url`, `get text` are the engine's own.

Early dispatch: `Client.Stream` calls `OnBlockStop` at `content_block_stop`;
the loop starts that tool immediately. Calls conflict (and are ordered by
emission order) unless both are pure reads, or both name different explicit
tabs; `tabs` open/switch/close, `ask_user` and `done` are barriers.
Therefore two `find` calls run in parallel, while `act` then `read_page`
never overlap. Tool results always go back in one user message in tool_use
order.

## Safety

* Page text is wrapped in `<untrusted_page_content kind tab url>`; a page
  cannot close the tag (`escapeUntrusted`), and attributes are sanitised.
* Enforced in code before a step runs, via `Backend.Confirm`: payment/order,
  sending/publishing, deleting/cancelling, credential changes, typing into
  password/card fields, and navigation (or clicking a link) to a different
  registrable domain after untrusted text was shown, unless the user's own
  message named that domain or they already approved it. The classifier is
  keyword based (deliberately conservative on irreversible verbs) and acts on
  roles button/link/menuitem; it is a net behind the prompt, not a proof.
  Steps are split around a confirmation: earlier steps run, the risky one waits.
* Denylist: `chrome:`, `sweef:`, `about:` (except blank), `file:`, `data:`,
  `javascript:`, `view-source:`, `devtools:`, `blob:`, `ws:`, `ftp:`, URLs
  with embedded credentials. Internal pages are never observed.
* Limits per run: turns (40), steps (200), navigations (40), cost ($0.50),
  output tokens (60K), steps per `act` (30). The agent may only close tabs it
  opened.

## Transport

Own `http.Transport`: HTTP/2 (`ForceAttemptHTTP2` + `http2.ConfigureTransports`
with 30 s health pings), TLS session cache, 64 KiB buffers, no response
timeout (the context governs), `accept-encoding: identity` so SSE is never
buffered behind gzip, `ProxyFromEnvironment`. `Prewarm` sends one
unauthenticated `HEAD` to the API origin (no key, no tokens billed) so the
first real request reuses a handshaken connection. Retries: 408/409/429/5xx/529
and `overloaded_error`/`rate_limit_error`/`api_error` SSE errors with
exponential jittered backoff, honouring `retry-after` (a wait beyond
`RetryMaxWait`, 30 s, is surfaced instead of slept), only while nothing has
been delivered to the handler; after the first content block a failure is
returned with the partial message. Cancelling the context closes the body at
once (`context.AfterFunc`). Metrics per request: attempts, protocol, reused
connection, headers, first byte, first token, first tool call, total, bytes,
usage.

### Why not the SDK

The prefix must be byte-exact and cheap to rebuild every turn (the SDK
re-marshals typed structs and offers no handle on key order), tool calls must
fire at `content_block_stop` rather than after the accumulator finishes, the
tuned connection lifecycle (prewarm, h2 pings, session cache) and timing
metrics need access to the transport, and the browser binary stays pure Go
with no new module. The cost is that API drift is ours to track: the
request/response structs in `client.go` and `context.go` are the whole surface.

## Cost and latency model

Per request: `cost = in*P + cr*0.1P + cw*1.25P + out*Q` with P=$0.10, Q=$0.50
per MTok (P=$0.50, Q=$2.50 above 100K prompt tokens). Per turn latency:
`RTT/TLS (prewarmed: 0) + prefill(prompt) + decode(out)/rate + tool` where
the tool overlaps decode when it is not the last block.

Illustrative 8-turn task (static prefix 5.9K tokens, first snapshot 4K
tokens, 0.5K-token deltas afterwards, 250 output tokens per turn; computed
from the formula above, not measured):

| Strategy | Cost | Turn-8 prompt |
|---|---|---|
| no caching, deltas | $0.0103 | 14.7K tokens |
| caching + deltas (this design) | $0.0036 | 14.7K tokens |
| caching, full 4K snapshot every turn | $0.0068 | 35.7K tokens |
| no caching, full snapshot every turn | $0.0176 | 35.7K tokens |

At these prices dollars are small either way; what deltas and compaction buy
is a smaller prompt, hence lower prefill time and time-to-first-token, and
headroom under the 100K-token price tier.

## Measured on this machine (fake server, 4 vCPU Xeon @ 2.1 GHz, Go 1.26)

These measure the agent's own overhead, **not** API latency.

| What | Result |
|---|---|
| SSE framing + hot-path delta decode | ~182 MB/s, 2045 events per op with 17 allocs total (~0 per event) |
| Delta decode: fast path vs `encoding/json` | 324 ns / 0 allocs vs 3092 ns / 10 allocs |
| Build a request: 5 / 20 / 40 turns of history (95 / 309 / 596 KiB bodies) | 80 us / 231 us / 672 us |
| Append one turn to history | ~10.6 us, 34 allocs |
| Compaction of a 40-turn history (~121K tokens elided) | 76 us (stage 1), 120 us (stage 2) |
| Snapshot delta, 1000-line page | 137 us |
| Truncate a 4000-line snapshot to 6000 tokens | 1.0 ms |
| Risk classifier per step | 6 us |
| Full loop turn (loopback HTTP, instant browser) | ~0.6 ms/turn, 3.8 MB allocated |
| Cancel an open stream | ~0.15-0.2 ms (test asserts < 250 ms) |
| Stop a run with a tool in flight | ~6 ms |
| Early dispatch | first tool starts ~163 ms before `message_stop` when the model keeps generating 160 ms afterwards |
| Prewarm + connection reuse (loopback) | ~0.7 ms; first request reuses the connection |

Early dispatch only helps when something is generated after the tool block
(more tool calls, trailing text): when a tool_use block is the last thing in
the message, `message_stop` follows within milliseconds. Multi-call turns are
where it matters.

Run: `go test -race ./internal/agent/...`,
`go test -run xxx -bench . ./internal/agent/`.

## GUI integration (`internal/agentbridge`)

`agentbridge.Host` implements `app.AgentHost` (+ `AgentInfoer`, `AgentResumer`, `AgentConfirmer`, `AgentKeyClearer`,
`AgentKeySetter`, `AgentChats`, `AgentSidebarWatcher`, `AgentAttacher`) over `agent.Agent`; `agentbridge.Backend` implements
`agent.Backend` over the app's live tabs. `cmd/sweef/gui_agent.go` constructs it; `app.New` hands it the `*App`.

* **Events**: text (coalesced to ~40 ms, first delta immediately), thought, action `{id,label,status pending|running|ok|error,ms,detail}`
  (act steps are announced `pending`, flipped by the backend as each step runs), status `{state}`, confirm `{id,title,risk,action}`,
  done `{ttft,tps,ms,sources,...}`, error `{code auth|rate_limit|network|unavailable|api|busy|agent,message,retryAfter}`.
  `ttft` is send -> first visible output (text, thought or action) measured in the host; `tps` is output tokens over streaming time
  (`RunStats.OutputTPS`); `apiTtft` (extra field) is the API's own first-token time.
* **Tabs**: steps run on the visible engine tab through its worker (`App.AgentRunOnTab`), one step per worker op so the page repaints
  between steps; navigation goes through the app's `loadURL` (HTTPS upgrade, tracker shield, history); `scroll` and ref'd actions scroll
  the on-screen view and move the overlay cursor (`App.AgentReveal/AgentCursor`).
* **Privacy**: every read and action re-checks the user's per-origin rule at that moment (a click that lands on a blocked origin returns
  `[hidden by user]`; a link to a blocked origin is not followed; an undecided origin raises the app's own visibility card). Incognito tabs
  of a normal window do not exist for the agent. Ask mode never acts: non-read steps are refused and risk cards are not shown.
* **Key**: `agent.setKey` -> `KeyStore` (Windows: DPAPI blob; macOS/Linux: 0600 file in the 0700 profile dir; memory only without a
  profile directory); `ANTHROPIC_API_KEY` is a development fallback; `SWEEF_AGENT_BASE_URL` overrides the API origin.
  The key is never logged, never in `state`, redacted from every error.
* **Tests**: `go test -race ./internal/agentbridge` (real chrome UI + engine + fake Anthropic server). Live:
  `SWEEF_LIVE_ANTHROPIC=1 ANTHROPIC_API_KEY=... go test -run TestLiveSmoke -v ./internal/agentbridge`.

## What is not verified

* Nothing was run against the live API. Field names and behaviours taken from
  the task brief and the public docs, unconfirmed here: `eager_input_streaming`
  on tools, `output_config.effort`, `thinking.display`, `cache_control` on
  `tool_result` blocks, `stop_details.category`, SSE event shapes (the fake
  was written from the documented format, so a shared misreading would not be
  caught).
* Real token counts: the "static prefix > 4096 tokens" check uses bytes/4. If
  the tokenizer is denser, margin is ~40%; verify with `usage.cache_creation_input_tokens`
  on the first live request (it should be ~prefix size, and
  `cache_read_input_tokens` should dominate from turn 2).
* Whether Haiku 5.5 discards earlier assistant thinking blocks on a new user
  message (that would cost one cache miss per follow-up message) and whether it
  enforces the history-editing check on thinking (compaction would then trip
  it; the 400 recovery path exists but is only tested against the fake). Setting
  `Config.Thinking = ThinkingDisabled` removes the question at the price of
  the model's own reasoning.
* Default `effort: low` and the prompt's batching behaviour: unevaluated.
  Expect to tune both against real tasks.
* Pricing multipliers for cache reads/writes (0.1x / 1.25x) are the standard
  ones, assumed for Haiku 5.5.
* `HEAD /` against `api.anthropic.com` for prewarm: any status is accepted;
  if a proxy or the API rejects HEAD, prewarm returns an error that is safe
  to ignore.
* The engine has no scroll/viewport position in snapshots, so truncation is
  document-order, not viewport-order.
* Early dispatch executes a tool before the model's message is complete, so a
  late mid-stream `refusal` or `error` can follow an action that already ran.
  The risk classifier and confirmations still apply to every dispatched step.

## Live measurements (real API, `claude-haiku-5-5`, 2026-10-12)

Task: open news.ycombinator.com, read the top 3 titles, open the first story's comments page, report the
comment count. Run with `sweef agent -v -no-thinking "<task>"` from one container (one region, three runs,
so indicative only; the final benchmark will use more runs and regions).

| Run | Thinking | Turns | Elapsed | Avg time to first byte | Avg time to first token | Tool time (all calls) | Cost |
|---|---|---|---|---|---|---|---|
| 1 | adaptive | 3 (+1 retry) | 7.3 s | 927 ms | 1783 ms | 1050 ms | $0.0037 |
| 2 | disabled | 3 | 6.8 s | 988 ms | 1037 ms | 1124 ms | $0.0040 |
| 3 | disabled | 4 | 8.2 s | 950 ms | 1025 ms | 1038 ms | $0.0031 |

* About 85% of wall-clock is provider time (network + model); engine tool execution is about 1 s of 7-8 s.
* With thinking on, Haiku 5.5 rejected replayed thinking blocks once; the loop recovered by stripping them
  but that rewrote the cache prefix (hit rate 50%). Thinking costs ~0.75 s per turn here, so the
  default for the sidebar is thinking disabled.
* Cache hit rate in short runs is dominated by first-seen page snapshots (written once, read on later
  turns): 47-77% over 3-4 turns. The static prefix (~5.8K tokens) is read from cache from the second
  request on. Hit rate rises with run length; measure on long tasks before claiming a number.
