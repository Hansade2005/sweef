## MCP: `sweef mcp` vs `@playwright/mcp`

Fixture flow (bench/site), 9 iterations in one session; medians of iterations 2..9. 4 vCPU Intel(R) Xeon(R) Processor @ 2.10GHz, Node v22.22.0.

| | Sweef | @playwright/mcp | Sweef vs Playwright |
|---|---:|---:|---|
| cold start: spawn to first snapshot (ms) | 20.0 | 2611 | 131x faster |
| tools/list (bytes / ~tokens, 54 vs 25 tools) | 15706 / 3927 | 20331 / 5083 | 1.3x less |
| whole flow, sum of median call latencies (ms) | 18.7 | 1322 | 70.7x faster |
| whole flow, text tokens returned (~bytes/4) | 425 | 856 | 2.0x less |
| peak RSS of the process tree (MiB) | 94.3 | 904 | 9.6x less |
| CPU seconds, start + 9 flows | 0.76 | 5.86 | 7.7x less |
| flows that succeeded | 9/9 | 9/9 | |

Per call (median ms; median response text ~tokens; image KB):

| call | Sweef ms | Playwright ms | speed | Sweef tok | Playwright tok | Sweef img KB | Playwright img KB |
|---|---:|---:|---|---:|---:|---:|---:|
| navigate | 1.09 | 51.98 | 47.7x faster | 7 | 52 | 0.0 | 0.0 |
| snapshot | 0.25 | 9.57 | 38.3x faster | 252 | 411 | 0.0 | 0.0 |
| click | 0.77 | 610 | 793x faster | 22 | 77 | 0.0 | 0.0 |
| snapshot-2 | 0.18 | 8.66 | 48.1x faster | 88 | 145 | 0.0 | 0.0 |
| fill | 0.16 | 39.90 | 249x faster | 34 | 27 | 0.0 | 0.0 |
| evaluate | 2.58 | 509 | 197x faster | 2 | 25 | 0.0 | 0.0 |
| screenshot | 12.75 | 40.83 | 3.2x faster | 0 | 73 | 26.2 | 23.1 |
| back | 0.91 | 51.14 | 56.2x faster | 20 | 46 | 0.0 | 0.0 |

Real pages (7 iterations per site, medians of 2..7; live network, so latencies include the sites' own response times):

| site | navigate ms S / P | snapshot ms S / P | snapshot ~tokens S / P | evaluate ms S / P | screenshot ms S / P | ok S / P |
|---|---:|---:|---:|---:|---:|---|
| en.wikipedia.org/wiki/Alan_Turing | 863 / 935 | 57.3 / 458 | 15010 / 157914 | 0.8 / 515 | 311 / 130 | 7 / 7 |
| github.com/golang/go | 1808 / 897 | 159 / 117 | 4005 / 7008 | 0.6 / 1034 | 41.8 / 114 | 7 / 7 |
| news.ycombinator.com/ | 116 / 245 | 2.8 / 57.4 | 8043 / 12326 | 0.4 / 512 | 36.8 / 64.4 | 7 / 7 |
| developer.mozilla.org/en-US/docs/Web/JavaScript/ | 1805 / 326 | 10.8 / 462 | 13496 / 15657 | 0.7 / 509 | 124 / 75.2 | 7 / 7 |
| docs.python.org/3/library/json.html | 478 / 333 | 7.0 / 97.7 | 15016 / 26144 | 0.8 / 512 | 36.3 / 77.5 | 7 / 7 |
| books.toscrape.com/ | 220 / 112 | 2.5 / 35.4 | 4180 / 8361 | 0.5 / 509 | 39.4 / 60.1 | 7 / 7 |
| www.rust-lang.org/ | 7.4 / 87.3 | 1.6 / 16.3 | 2025 / 2960 | 0.4 / 508 | 53.2 / 55.0 | 7 / 7 |
| example.com | 51.0 / 49.8 | 0.5 / 7.8 | 358 / 420 | 0.4 / 508 | 23.4 / 50.9 | 7 / 7 |

Real-page run resources: Sweef peak RSS 656 MiB, CPU 63.06 s; Playwright peak RSS 1341 MiB, CPU 91.93 s.

## SDK: `@sweef/browser` vs `playwright-core`

5 fresh Node processes per SDK, 9 flow iterations each; cold numbers are medians over processes, per-call numbers medians over warm iterations.

| | Sweef SDK | playwright-core | Sweef vs Playwright |
|---|---:|---:|---|
| import the SDK (ms) | 28.0 | 268 | 9.6x faster |
| launch (ms) | 18.8 | 144 | 7.6x faster |
| new page (ms) | 4.8 | 106 | 22.0x faster |
| process start to first snapshot (ms) | 101 | 638 | 6.3x faster |
| close (ms) | 74.8 | 87.8 | 1.2x faster |
| peak RSS of the process tree (MiB) | 152 | 849 | 5.6x less |
| CPU seconds per process | 0.89 | 5.23 | 5.9x less |
| flows that succeeded | 45/45 | 45/45 | |

| call | Sweef ms | playwright-core ms | speed |
|---|---:|---:|---|
| goto | 0.92 | 14.05 | 15.3x faster |
| snapshot | 0.37 | 20.04 | 54.2x faster |
| click | 0.85 | 49.58 | 58.3x faster |
| snapshot-2 | 0.25 | 18.26 | 73.0x faster |
| fill | 0.21 | 6.08 | 29.0x faster |
| evaluate | 0.89 | 2.29 | 2.6x faster |
| screenshot | 13.10 | 48.04 | 3.7x faster |
| back | 0.95 | 14.01 | 14.7x faster |

## CLI: `sweef` vs Vercel `agent-browser` (cold caches)

7 rounds; each round starts with no daemon, so `open` includes daemon (and browser) start. Every command is its own process. Every round starts with empty caches (Sweef bytecode/HTTP/font caches, a fresh agent-browser HOME).

| | sweef | agent-browser | sweef vs agent-browser |
|---|---:|---:|---|
| whole flow, wall (ms) | 254 | 1358 | 5.4x faster |
| peak RSS of the daemon tree (MiB) | 96.8 | 1119 | 11.6x less |
| CPU seconds per round | 0.28 | 2.86 | 10.2x less |
| rounds that succeeded | 7/7 | 7/7 | |

| command | sweef ms | agent-browser ms | speed | sweef ~tokens | agent-browser ~tokens |
|---|---:|---:|---|---:|---:|
| open | 27.9 | 518 | 18.6x faster | 9 | 9 |
| snapshot-i | 11.2 | 17.3 | 1.5x faster | 117 | 120 |
| click | 9.5 | 19.3 | 2.0x faster | 7 | 2 |
| snapshot | 8.6 | 57.4 | 6.7x faster | 88 | 94 |
| fill | 7.2 | 19.2 | 2.7x faster | 4 | 2 |
| eval | 19.4 | 8.4 | **2.3x slower** | 3 | 3 |
| screenshot | 81.8 | 64.6 | **1.3x slower** | 9 | 16 |
| back | 8.8 | 511 | 58.1x faster | 9 | 6 |
| get-title | 7.4 | 7.8 | 1.1x faster | 1 | 1 |
| close | 70.1 | 120 | 1.7x faster | 5 | 5 |

`sweef-cli`, the session client without the engine linked in (same commands, same daemon):

| command | sweef-cli ms | sweef ms | agent-browser ms | sweef-cli vs agent-browser |
|---|---:|---:|---:|---|
| whole flow | 234 | 254 | 1358 | 5.8x faster |
| open | 19.9 | 27.9 | 518 | 26.0x faster |
| snapshot-i | 4.3 | 11.2 | 17.3 | 4.0x faster |
| click | 5.1 | 9.5 | 19.3 | 3.8x faster |
| snapshot | 3.4 | 8.6 | 57.4 | 16.9x faster |
| fill | 3.8 | 7.2 | 19.2 | 5.1x faster |
| eval | 38.0 | 19.4 | 8.4 | **4.5x slower** |
| screenshot | 81.4 | 81.8 | 64.6 | **1.3x slower** |
| back | 5.3 | 8.8 | 511 | 96.4x faster |
| get-title | 3.2 | 7.4 | 7.8 | 2.4x faster |
| close | 64.9 | 70.1 | 120 | 1.9x faster |

## CLI: `sweef` vs Vercel `agent-browser` (warm caches)

7 rounds; each round starts with no daemon, so `open` includes daemon (and browser) start. Every command is its own process. Caches and browser profile are kept across rounds (primed by one untimed round): a second session on the same machine.

| | sweef | agent-browser | sweef vs agent-browser |
|---|---:|---:|---|
| whole flow, wall (ms) | 209 | 1350 | 6.5x faster |
| peak RSS of the daemon tree (MiB) | 56.1 | 1123 | 20.0x less |
| CPU seconds per round | 0.21 | 2.89 | 13.8x less |
| rounds that succeeded | 7/7 | 7/7 | |

| command | sweef ms | agent-browser ms | speed | sweef ~tokens | agent-browser ~tokens |
|---|---:|---:|---|---:|---:|
| open | 24.9 | 496 | 19.9x faster | 9 | 9 |
| snapshot-i | 8.1 | 17.2 | 2.1x faster | 117 | 120 |
| click | 10.9 | 23.2 | 2.1x faster | 7 | 2 |
| snapshot | 10.5 | 48.4 | 4.6x faster | 88 | 94 |
| fill | 8.1 | 12.6 | 1.6x faster | 4 | 2 |
| eval | 19.2 | 6.5 | **3.0x slower** | 3 | 3 |
| screenshot | 35.6 | 69.8 | 2.0x faster | 9 | 16 |
| back | 10.5 | 512 | 48.7x faster | 9 | 6 |
| get-title | 7.0 | 7.4 | 1.1x faster | 1 | 1 |
| close | 69.8 | 173 | 2.5x faster | 5 | 5 |

`sweef-cli`, the session client without the engine linked in (same commands, same daemon):

| command | sweef-cli ms | sweef ms | agent-browser ms | sweef-cli vs agent-browser |
|---|---:|---:|---:|---|
| whole flow | 181 | 209 | 1350 | 7.5x faster |
| open | 20.5 | 24.9 | 496 | 24.2x faster |
| snapshot-i | 4.3 | 8.1 | 17.2 | 4.0x faster |
| click | 6.4 | 10.9 | 23.2 | 3.6x faster |
| snapshot | 3.5 | 10.5 | 48.4 | 13.8x faster |
| fill | 3.9 | 8.1 | 12.6 | 3.2x faster |
| eval | 34.0 | 19.2 | 6.5 | **5.2x slower** |
| screenshot | 29.1 | 35.6 | 69.8 | 2.4x faster |
| back | 5.0 | 10.5 | 512 | 102x faster |
| get-title | 3.8 | 7.0 | 7.4 | 1.9x faster |
| close | 64.8 | 69.8 | 173 | 2.7x faster |

