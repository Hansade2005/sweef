## MCP: `sweef mcp` vs `@playwright/mcp`

Fixture flow (bench/site), 9 iterations in one session; medians of iterations 2..9. 4 vCPU Intel(R) Xeon(R) Processor @ 2.10GHz, Node v22.22.0.

| | Sweef | @playwright/mcp | Sweef vs Playwright |
|---|---:|---:|---|
| cold start: spawn to first snapshot (ms) | 20.2 | 810 | 40.1x faster |
| tools/list (bytes / ~tokens, 54 vs 25 tools) | 15636 / 3909 | 20331 / 5083 | 1.3x less |
| whole flow, sum of median call latencies (ms) | 22.5 | 1388 | 61.7x faster |
| whole flow, text tokens returned (~bytes/4) | 425 | 856 | 2.0x less |
| peak RSS of the process tree (MiB) | 91.2 | 945 | 10.4x less |
| CPU seconds, start + 9 flows | 0.92 | 6.74 | 7.3x less |
| flows that succeeded | 9/9 | 9/9 | |

Per call (median ms; median response text ~tokens; image KB):

| call | Sweef ms | Playwright ms | speed | Sweef tok | Playwright tok | Sweef img KB | Playwright img KB |
|---|---:|---:|---|---:|---:|---:|---:|
| navigate | 1.20 | 65.12 | 54.3x faster | 7 | 52 | 0.0 | 0.0 |
| snapshot | 0.47 | 11.22 | 23.9x faster | 252 | 411 | 0.0 | 0.0 |
| click | 1.07 | 630 | 588x faster | 22 | 77 | 0.0 | 0.0 |
| snapshot-2 | 0.23 | 10.50 | 45.7x faster | 88 | 145 | 0.0 | 0.0 |
| fill | 0.18 | 50.22 | 279x faster | 34 | 27 | 0.0 | 0.0 |
| evaluate | 1.86 | 512 | 276x faster | 2 | 25 | 0.0 | 0.0 |
| screenshot | 16.11 | 47.26 | 2.9x faster | 0 | 73 | 24.7 | 23.1 |
| back | 1.35 | 61.69 | 45.7x faster | 20 | 46 | 0.0 | 0.0 |

Real pages (7 iterations per site, medians of 2..7; live network, so latencies include the sites' own response times):

| site | navigate ms S / P | snapshot ms S / P | snapshot ~tokens S / P | evaluate ms S / P | screenshot ms S / P | ok S / P |
|---|---:|---:|---:|---:|---:|---|
| en.wikipedia.org/wiki/Alan_Turing | 2361 / 966 | 31.7 / 512 | 15010 / 157926 | 1.3 / 522 | 509 / 107 | 7 / 7 |
| github.com/golang/go | 7135 / 921 | 13.2 / 133 | 3997 / 6967 | 0.9 / 1064 | 61.0 / 87.8 | 7 / 7 |
| news.ycombinator.com/ | 128 / 275 | 2.9 / 61.2 | 8206 / 12509 | 0.6 / 514 | 51.6 / 73.0 | 7 / 7 |
| developer.mozilla.org/en-US/docs/Web/JavaScript/ | 2398 / 398 | 13.3 / 525 | 13506 / 15657 | 1.2 / 517 | 88.9 / 92.3 | 7 / 7 |
| docs.python.org/3/library/json.html | 679 / 266 | 7.6 / 124 | 15016 / 26144 | 1.2 / 514 | 46.4 / 88.9 | 7 / 7 |
| books.toscrape.com/ | 263 / 141 | 2.3 / 51.9 | 4180 / 8361 | 0.7 / 513 | 51.9 / 77.2 | 7 / 7 |
| www.rust-lang.org/ | 8.1 / 107 | 1.7 / 24.2 | 2025 / 2960 | 0.6 / 511 | 54.4 / 57.2 | 7 / 7 |
| example.com | 69.7 / 65.3 | 0.6 / 10.9 | 358 / 420 | 0.5 / 512 | 32.2 / 64.6 | 7 / 7 |

Real-page run resources: Sweef peak RSS 947 MiB, CPU 111 s; Playwright peak RSS 1408 MiB, CPU 108 s.

### Sweef MCP output size, before (0.1, a140c23) and after this change

| call | before ~tokens | after ~tokens | before bytes | after bytes |
|---|---:|---:|---:|---:|
| navigate | 12 | 7 | 131 | 104 |
| snapshot | 252 | 252 | 1144 | 1144 |
| click | 3 | 22 | 88 | 162 |
| snapshot-2 | 88 | 88 | 452 | 452 |
| fill | 3 | 34 | 88 | 219 |
| evaluate | 5 | 2 | 99 | 86 |
| screenshot | 22 | 0 | 25473 | 25359 |
| back | 3 | 20 | 88 | 154 |
| tools/list | 3763 | 3909 | 15051 | 15636 |
| en.wikipedia.org/wiki/Alan_Turing: snapshot / screenshot text | 15010 / 32104 | 15010 / 0 | | |
| github.com/golang/go: snapshot / screenshot text | 3997 / 1534 | 3997 / 0 | | |
| news.ycombinator.com/: snapshot / screenshot text | 8207 / 1047 | 8206 / 0 | | |
| developer.mozilla.org/en-US/docs/Web/Jav: snapshot / screenshot text | 13510 / 3393 | 13506 / 0 | | |
| docs.python.org/3/library/json.html: snapshot / screenshot text | 15016 / 7187 | 15016 / 0 | | |
| books.toscrape.com/: snapshot / screenshot text | 4180 / 469 | 4180 / 0 | | |
| www.rust-lang.org/: snapshot / screenshot text | 2025 / 891 | 2025 / 0 | | |
| example.com: snapshot / screenshot text | 358 / 329 | 358 / 0 | | |

## SDK: `@sweef/browser` vs `playwright-core`

5 fresh Node processes per SDK, 9 flow iterations each; cold numbers are medians over processes, per-call numbers medians over warm iterations.

| | Sweef SDK | playwright-core | Sweef vs Playwright |
|---|---:|---:|---|
| import the SDK (ms) | 7.0 | 295 | 42.1x faster |
| launch (ms) | 18.8 | 170 | 9.0x faster |
| new page (ms) | 2.8 | 123 | 43.8x faster |
| process start to first snapshot (ms) | 77.0 | 720 | 9.3x faster |
| close (ms) | 76.6 | 92.0 | 1.2x faster |
| peak RSS of the process tree (MiB) | 149 | 852 | 5.7x less |
| CPU seconds per process | 1.99 | 6.45 | 3.2x less |
| flows that succeeded | 45/45 | 45/45 | |

| call | Sweef ms | playwright-core ms | speed |
|---|---:|---:|---|
| goto | 1.18 | 18.20 | 15.4x faster |
| snapshot | 0.45 | 24.12 | 53.6x faster |
| click | 1.12 | 52.01 | 46.4x faster |
| snapshot-2 | 0.31 | 22.55 | 72.7x faster |
| fill | 0.27 | 9.57 | 35.4x faster |
| evaluate | 0.84 | 3.13 | 3.7x faster |
| screenshot | 15.31 | 51.81 | 3.4x faster |
| back | 1.30 | 18.14 | 14.0x faster |

## CLI: `sweef` vs Vercel `agent-browser` (cold caches)

7 rounds; each round starts with no daemon, so `open` includes daemon (and browser) start. Every command is its own process. Every round starts with empty caches (Sweef bytecode/HTTP/font caches, a fresh agent-browser HOME).

| | sweef | agent-browser | sweef vs agent-browser |
|---|---:|---:|---|
| whole flow, wall (ms) | 445 | 1554 | 3.5x faster |
| peak RSS of the daemon tree (MiB) | 96.9 | 1118 | 11.5x less |
| CPU seconds per round | 0.49 | 3.08 | 6.3x less |
| rounds that succeeded | 7/7 | 7/7 | |

| command | sweef ms | agent-browser ms | speed | sweef ~tokens | agent-browser ~tokens |
|---|---:|---:|---|---:|---:|
| open | 29.4 | 598 | 20.3x faster | 9 | 9 |
| snapshot-i | 11.2 | 21.0 | 1.9x faster | 117 | 120 |
| click | 20.2 | 31.7 | 1.6x faster | 7 | 2 |
| snapshot | 9.7 | 66.8 | 6.9x faster | 88 | 94 |
| fill | 8.4 | 17.7 | 2.1x faster | 4 | 2 |
| eval | 155 | 7.1 | **21.8x slower** | 3 | 3 |
| screenshot | 99.0 | 87.5 | **1.1x slower** | 9 | 16 |
| back | 13.2 | 512 | 38.8x faster | 9 | 6 |
| get-title | 10.9 | 9.1 | **1.2x slower** | 1 | 1 |
| close | 73.8 | 179 | 2.4x faster | 5 | 5 |

## CLI: `sweef` vs Vercel `agent-browser` (warm caches)

7 rounds; each round starts with no daemon, so `open` includes daemon (and browser) start. Every command is its own process. Caches and browser profile are kept across rounds (primed by one untimed round): a second session on the same machine.

| | sweef | agent-browser | sweef vs agent-browser |
|---|---:|---:|---|
| whole flow, wall (ms) | 223 | 1536 | 6.9x faster |
| peak RSS of the daemon tree (MiB) | 54.5 | 1119 | 20.5x less |
| CPU seconds per round | 0.19 | 3.45 | 18.2x less |
| rounds that succeeded | 7/7 | 7/7 | |

| command | sweef ms | agent-browser ms | speed | sweef ~tokens | agent-browser ~tokens |
|---|---:|---:|---|---:|---:|
| open | 27.7 | 578 | 20.9x faster | 9 | 9 |
| snapshot-i | 11.5 | 28.8 | 2.5x faster | 117 | 120 |
| click | 16.1 | 54.7 | 3.4x faster | 7 | 2 |
| snapshot | 10.8 | 59.4 | 5.5x faster | 88 | 94 |
| fill | 10.3 | 26.7 | 2.6x faster | 4 | 2 |
| eval | 10.0 | 8.3 | **1.2x slower** | 3 | 3 |
| screenshot | 35.3 | 73.1 | 2.1x faster | 9 | 16 |
| back | 12.1 | 518 | 42.8x faster | 9 | 6 |
| get-title | 10.0 | 9.2 | **1.1x slower** | 1 | 1 |
| close | 73.4 | 187 | 2.5x faster | 5 | 5 |

