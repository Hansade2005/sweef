# Agent-surface benchmark

Sweef's three agent interfaces against the tools people use today, each driven through its real interface with a scripted, deterministic flow (no model, no API key):

| Sweef | Competitor | How it is driven |
|---|---|---|
| `sweef mcp` | Microsoft `@playwright/mcp` 0.0.83 | stdio JSON-RPC, the same MCP client for both (`node/lib.mjs`) |
| `@sweef/browser` (Node SDK, `pkg/sdk/node`) | `playwright-core` 1.56.1 | a fresh Node process per round running the same flow |
| session CLI (`sweef open/click/...`) | Vercel `agent-browser` 0.39.1 | one process per command, as an agent's shell tool runs them |

The competitors drive the preinstalled Chromium (`/opt/pw-browsers/chromium`, Chromium 141 / Playwright build 1194) with `--headless --isolated --no-sandbox` (Playwright MCP), `headless: true, args: ['--no-sandbox']` (playwright-core) and `AGENT_BROWSER_EXECUTABLE_PATH` (agent-browser). Nothing runs `playwright install`. `@playwright/mcp` 0.0.83 ships its own Playwright 1.64 alpha; it worked with Chromium 141 (every flow succeeded).

Results: [results/2026-10-15/report.md](results/2026-10-15/report.md) (generated) and the raw JSON next to it.

## Summary (2026-10-15, HEAD of worktree-surfaces)

| Surface | Sweef | Competitor | |
|---|---:|---:|---|
| MCP cold start to first snapshot | 20 ms | 810 ms | `@playwright/mcp` |
| MCP fixture flow, sum of median call latencies | 22.5 ms | 1388 ms | |
| MCP fixture flow, text tokens returned | 425 | 856 | |
| MCP `tools/list` | 3.9k tokens (54 tools) | 5.1k tokens (25 tools) | |
| MCP peak RSS / CPU (start + 9 flows) | 91 MiB / 0.92 s | 945 MiB / 6.74 s | |
| SDK process start to first snapshot | 77 ms | 720 ms | `playwright-core` |
| SDK peak RSS / CPU per process | 149 MiB / 1.99 s | 852 MiB / 6.45 s | |
| CLI flow, cold caches | 445 ms | 1554 ms | `agent-browser` |
| CLI flow, warm caches | 223 ms | 1536 ms | |
| CLI daemon peak RSS (warm) | 55 MiB | 1119 MiB | |

Where Sweef loses (all rows are in the report):

- **Real-page navigation** on script-heavy sites: Wikipedia 2.4 s vs 0.97 s, GitHub 7.1 s vs 0.92 s, MDN 2.4 s vs 0.40 s, Python docs 0.68 s vs 0.27 s, books.toscrape 0.26 s vs 0.14 s. The time is in the engine, not the interface: running the sites' scripts in QuickJS, layout passes during load (1.3 s on GitHub), and waiting for requests the page starts during load to settle, where Playwright returns at the load event. It is the same before and after this change. Screenshot of the Wikipedia article: 509 ms vs 107 ms (software layout and paint of a very long page).
- **CLI `eval` with cold caches**: 155 ms vs 7 ms. The first JavaScript of a fresh process with an empty cache directory compiles the engine's prelude (about 140 ms); with warm caches it is 10 ms vs 8 ms.
- **CLI per-command floor**: `get title` 10 ms vs 9 ms. A Sweef command is a 39 MB static Go binary (about 7 ms to start against agent-browser's 2 ms; package initialisation is about 5 ms spread over ~190 packages).
- **Real-page CPU**: 111 s vs 108 s for the 56 real-page visits (with RSS 947 vs 1408 MiB).
- **`tools/list` tool count**: 54 tools against 25 (fewer tokens overall, but more tools for the model to choose from).

## Flows

Fixture site (`bench/site`: a landing page, a login form, a React counter), served locally:

- **MCP and SDK**: navigate to `/`, snapshot, click the "Sign in" link (by its snapshot ref in MCP, by `getByRole` in the SDKs), snapshot, fill the Email field, evaluate `document.title`, screenshot (PNG), go back. Nine iterations per session; medians are over iterations 2-9 (iteration 1 is reported as `first`). A flow counts as succeeded when the refs were found, the actions did not error, `evaluate` returned "Sign in" and the screenshot has image data.
- **Real pages (MCP only)**: eight URLs from `bench/realsites/tasks.json` (Wikipedia, GitHub, Hacker News, MDN, Python docs, books.toscrape, rust-lang.org, example.com); navigate, snapshot, evaluate, screenshot, seven times each, medians of 2-7. These go over the live network, so they include the sites' own response times and are noisier than the fixture rows.
- **CLI**: `open /`, `snapshot -i`, `click @<ref of Sign in>`, `snapshot`, `fill #email`, `eval document.title`, `screenshot <file>`, `back`, `get title`, `close`; seven rounds, each starting with no daemon, so `open` includes starting the daemon (and Chromium for agent-browser). Run twice: cold (empty caches every round: Sweef's bytecode/HTTP/font-index caches and agent-browser's HOME) and warm (one cache/profile directory for all rounds, primed by an untimed round).

## Metrics

- **Latency**: wall time per call, request written to response parsed (MCP), promise start to resolution (SDK), process spawn to exit (CLI).
- **Cold start**: MCP, spawn to the first snapshot of the fixture (includes `initialize`, `tools/list`, the first navigate). SDK, Node process start to first snapshot, plus import, launch, new page.
- **Bytes and tokens**: the whole JSON-RPC response line (bytes), the text content (tokens are approximated as text bytes / 4) and the base64 image bytes separately (image tokens depend on pixel size, which is 1280x720 for both). `tools/list` size is reported because hosts send it to the model on every turn.
- **Memory and CPU**: peak RSS summed over the whole process tree (the MCP server and its browser; the SDK's Node process and its server/browser; the CLI's daemon and its browser), sampled every 50 ms from `/proc`; CPU seconds are utime+stime of the tree, per process the maximum seen, so exited children still count.

## Fairness notes

- Same machine (4 vCPU Xeon 2.1 GHz), one product at a time, nothing else running (a separate real-LLM benchmark that had been running on this machine was found dead and was not restarted).
- Defaults of each product, no tuning. `@playwright/mcp` 0.0.83 saves snapshots after actions to files and links them instead of inlining them; Sweef inlines a delta of the changed lines. Both are counted as returned.
- Sweef snapshots are capped at 60,000 characters by default (with a truncation note); `@playwright/mcp` returns the whole tree. On the Wikipedia article this is most of the token difference (about 15k vs 158k tokens).
- What "navigate done" means differs: Sweef's navigate runs the page's scripts and waits until the page settles (async requests started during load, up to the settle budget), Playwright's waits for the load event. On script-heavy real pages Sweef's navigate is slower; see the report.
- Sweef's "before" column is the MCP server at a140c23 (before this change) on the same flow, for the output-size comparison only.

## Reproduce

```bash
bench/surfaces/run.sh [--before /path/to/older/sweef]   # builds sweef, starts bench/site, npm ci, runs all three, writes results/<date>/
```

Or run the pieces from `bench/surfaces/node` (`npm ci` first): `mcp_bench.mjs`, `sdk_bench.mjs`, `cli_bench.mjs [--warm-cache]`, then `report.mjs --mcp ... --sdk ... --cli ... --cli-warm ...`.
