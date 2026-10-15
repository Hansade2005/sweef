## MCP: `sweef mcp` vs `@playwright/mcp`

Fixture flow (bench/site), 9 iterations in one session; medians of iterations 2..9. 4 vCPU Intel(R) Xeon(R) Processor @ 2.10GHz, Node v22.22.0.

| | Sweef | @playwright/mcp | Sweef vs Playwright |
|---|---:|---:|---|
| cold start: spawn to first snapshot (ms) | 23.3 | 999 | 42.9x faster |
| tools/list (bytes / ~tokens, 54 vs 25 tools) | 15636 / 3909 | 20331 / 5083 | 1.3x less |
| whole flow, sum of median call latencies (ms) | 25.2 | 1397 | 55.4x faster |
| whole flow, text tokens returned (~bytes/4) | 425 | 856 | 2.0x less |
| peak RSS of the process tree (MiB) | 90.5 | 942 | 10.4x less |
| CPU seconds, start + 9 flows | 0.91 | 7.04 | 7.7x less |
| flows that succeeded | 9/9 | 9/9 | |

Per call (median ms; median response text ~tokens; image KB):

| call | Sweef ms | Playwright ms | speed | Sweef tok | Playwright tok | Sweef img KB | Playwright img KB |
|---|---:|---:|---|---:|---:|---:|---:|
| navigate | 1.89 | 67.21 | 35.6x faster | 7 | 52 | 0.0 | 0.0 |
| snapshot | 0.56 | 12.20 | 21.8x faster | 252 | 411 | 0.0 | 0.0 |
| click | 1.22 | 631 | 517x faster | 22 | 77 | 0.0 | 0.0 |
| snapshot-2 | 0.31 | 11.05 | 35.6x faster | 88 | 145 | 0.0 | 0.0 |
| fill | 0.28 | 50.45 | 180x faster | 34 | 27 | 0.0 | 0.0 |
| evaluate | 1.04 | 513 | 493x faster | 2 | 25 | 0.0 | 0.0 |
| screenshot | 17.81 | 50.76 | 2.9x faster | 0 | 73 | 26.2 | 23.1 |
| back | 2.08 | 61.34 | 29.5x faster | 20 | 46 | 0.0 | 0.0 |

Real pages (7 iterations per site, medians of 2..7; live network, so latencies include the sites' own response times):

| site | navigate ms S / P | snapshot ms S / P | snapshot ~tokens S / P | evaluate ms S / P | screenshot ms S / P | ok S / P |
|---|---:|---:|---:|---:|---:|---|
| en.wikipedia.org/wiki/Alan_Turing | 1651 / 1033 | 73.2 / 574 | 15010 / 157844 | 1.5 / 541 | 370 / 110 | 7 / 7 |
| github.com/golang/go | 2781 / 733 | 117 / 150 | 4030 / 7162 | 0.9 / 6042 | 52.8 / 83.0 | 7 / 7 |
| news.ycombinator.com/ | 126 / 254 | 2.8 / 67.7 | 8201 / 12492 | 0.9 / 514 | 41.8 / 71.7 | 7 / 7 |
| developer.mozilla.org/en-US/docs/Web/JavaScript/ | 2250 / 402 | 21.1 / 563 | 13497 / 15655 | 1.4 / 513 | 154 / 83.9 | 7 / 7 |
| docs.python.org/3/library/json.html | 636 / 281 | 7.6 / 106 | 15016 / 26144 | 1.2 / 513 | 40.7 / 85.9 | 7 / 7 |
| books.toscrape.com/ | 245 / 154 | 2.5 / 42.3 | 4180 / 8361 | 0.7 / 514 | 32.6 / 69.5 | 7 / 7 |
| www.rust-lang.org/ | 8.8 / 118 | 1.6 / 23.3 | 2025 / 2960 | 0.7 / 512 | 46.2 / 56.4 | 7 / 7 |
| example.com | 66.2 / 59.2 | 0.7 / 11.4 | 358 / 420 | 0.8 / 513 | 26.2 / 55.0 | 7 / 7 |

Real-page run resources: Sweef peak RSS 888 MiB, CPU 74.59 s; Playwright peak RSS 1356 MiB, CPU 111 s.

### Sweef MCP output size, before (0.1, a140c23) and after this change

| call | before ~tokens | after ~tokens | before bytes | after bytes |
|---|---:|---:|---:|---:|
| navigate | 7 | 7 | 104 | 104 |
| snapshot | 252 | 252 | 1144 | 1144 |
| click | 22 | 22 | 162 | 162 |
| snapshot-2 | 88 | 88 | 452 | 452 |
| fill | 34 | 34 | 219 | 219 |
| evaluate | 2 | 2 | 86 | 86 |
| screenshot | 0 | 0 | 25359 | 26903 |
| back | 20 | 20 | 154 | 154 |
| tools/list | 3909 | 3909 | 15636 | 15636 |
| en.wikipedia.org/wiki/Alan_Turing: snapshot / screenshot text | 15010 / 0 | 15010 / 0 | | |
| github.com/golang/go: snapshot / screenshot text | 4030 / 0 | 4030 / 0 | | |
| news.ycombinator.com/: snapshot / screenshot text | 8217 / 0 | 8201 / 0 | | |
| developer.mozilla.org/en-US/docs/Web/Jav: snapshot / screenshot text | 13510 / 0 | 13497 / 0 | | |
| docs.python.org/3/library/json.html: snapshot / screenshot text | 15016 / 0 | 15016 / 0 | | |
| books.toscrape.com/: snapshot / screenshot text | 4180 / 0 | 4180 / 0 | | |
| www.rust-lang.org/: snapshot / screenshot text | 2025 / 0 | 2025 / 0 | | |
| example.com: snapshot / screenshot text | 358 / 0 | 358 / 0 | | |

## SDK: `@sweef/browser` vs `playwright-core`

5 fresh Node processes per SDK, 9 flow iterations each; cold numbers are medians over processes, per-call numbers medians over warm iterations.

| | Sweef SDK | playwright-core | Sweef vs Playwright |
|---|---:|---:|---|
| import the SDK (ms) | 7.4 | 280 | 37.9x faster |
| launch (ms) | 19.2 | 184 | 9.6x faster |
| new page (ms) | 2.3 | 124 | 54.0x faster |
| process start to first snapshot (ms) | 77.4 | 706 | 9.1x faster |
| close (ms) | 81.0 | 114 | 1.4x faster |
| peak RSS of the process tree (MiB) | 149 | 854 | 5.7x less |
| CPU seconds per process | 1.01 | 7.46 | 7.4x less |
| flows that succeeded | 45/45 | 45/45 | |

| call | Sweef ms | playwright-core ms | speed |
|---|---:|---:|---|
| goto | 1.16 | 17.58 | 15.2x faster |
| snapshot | 0.47 | 26.06 | 55.4x faster |
| click | 0.96 | 55.80 | 58.1x faster |
| snapshot-2 | 0.32 | 23.68 | 74.0x faster |
| fill | 0.30 | 9.98 | 33.3x faster |
| evaluate | 1.02 | 3.60 | 3.5x faster |
| screenshot | 12.96 | 50.55 | 3.9x faster |
| back | 1.40 | 18.50 | 13.2x faster |

## CLI: `sweef` vs Vercel `agent-browser` (cold caches)

7 rounds; each round starts with no daemon, so `open` includes daemon (and browser) start. Every command is its own process. Every round starts with empty caches (Sweef bytecode/HTTP/font caches, a fresh agent-browser HOME).

| | sweef | agent-browser | sweef vs agent-browser |
|---|---:|---:|---|
| whole flow, wall (ms) | 275 | 1488 | 5.4x faster |
| peak RSS of the daemon tree (MiB) | 93.9 | 1129 | 12.0x less |
| CPU seconds per round | 0.30 | 3.58 | 11.9x less |
| rounds that succeeded | 7/7 | 7/7 | |

| command | sweef ms | agent-browser ms | speed | sweef ~tokens | agent-browser ~tokens |
|---|---:|---:|---|---:|---:|
| open | 28.6 | 568 | 19.8x faster | 9 | 9 |
| snapshot-i | 13.0 | 26.3 | 2.0x faster | 117 | 120 |
| click | 12.3 | 22.5 | 1.8x faster | 7 | 2 |
| snapshot | 9.4 | 68.9 | 7.3x faster | 88 | 94 |
| fill | 8.6 | 20.2 | 2.3x faster | 4 | 2 |
| eval | 15.0 | 9.6 | **1.6x slower** | 3 | 3 |
| screenshot | 94.7 | 63.3 | **1.5x slower** | 9 | 16 |
| back | 11.8 | 515 | 43.6x faster | 9 | 6 |
| get-title | 9.9 | 10.2 | 1.0x faster | 1 | 1 |
| close | 71.7 | 180 | 2.5x faster | 5 | 5 |

`sweef-cli`, the session client without the engine linked in (same commands, same daemon):

| command | sweef-cli ms | sweef ms | agent-browser ms | sweef-cli vs agent-browser |
|---|---:|---:|---:|---|
| whole flow | 244 | 275 | 1488 | 6.1x faster |
| open | 21.0 | 28.6 | 568 | 27.0x faster |
| snapshot-i | 5.4 | 13.0 | 26.3 | 4.9x faster |
| click | 7.3 | 12.3 | 22.5 | 3.1x faster |
| snapshot | 4.3 | 9.4 | 68.9 | 16.0x faster |
| fill | 4.0 | 8.6 | 20.2 | 5.0x faster |
| eval | 35.7 | 15.0 | 9.6 | **3.7x slower** |
| screenshot | 86.9 | 94.7 | 63.3 | **1.4x slower** |
| back | 6.2 | 11.8 | 515 | 83.0x faster |
| get-title | 4.3 | 9.9 | 10.2 | 2.4x faster |
| close | 66.2 | 71.7 | 180 | 2.7x faster |

## CLI: `sweef` vs Vercel `agent-browser` (warm caches)

7 rounds; each round starts with no daemon, so `open` includes daemon (and browser) start. Every command is its own process. Caches and browser profile are kept across rounds (primed by one untimed round): a second session on the same machine.

| | sweef | agent-browser | sweef vs agent-browser |
|---|---:|---:|---|
| whole flow, wall (ms) | 216 | 1520 | 7.0x faster |
| peak RSS of the daemon tree (MiB) | 56.1 | 1132 | 20.2x less |
| CPU seconds per round | 0.21 | 3.44 | 16.4x less |
| rounds that succeeded | 7/7 | 7/7 | |

| command | sweef ms | agent-browser ms | speed | sweef ~tokens | agent-browser ~tokens |
|---|---:|---:|---|---:|---:|
| open | 27.4 | 572 | 20.9x faster | 9 | 9 |
| snapshot-i | 10.4 | 24.1 | 2.3x faster | 117 | 120 |
| click | 13.6 | 31.3 | 2.3x faster | 7 | 2 |
| snapshot | 9.1 | 57.0 | 6.3x faster | 88 | 94 |
| fill | 8.9 | 17.6 | 2.0x faster | 4 | 2 |
| eval | 14.6 | 7.8 | **1.9x slower** | 3 | 3 |
| screenshot | 31.4 | 75.8 | 2.4x faster | 9 | 16 |
| back | 12.4 | 514 | 41.4x faster | 9 | 6 |
| get-title | 10.3 | 9.7 | **1.1x slower** | 1 | 1 |
| close | 71.4 | 182 | 2.5x faster | 5 | 5 |

`sweef-cli`, the session client without the engine linked in (same commands, same daemon):

| command | sweef-cli ms | sweef ms | agent-browser ms | sweef-cli vs agent-browser |
|---|---:|---:|---:|---|
| whole flow | 192 | 216 | 1520 | 7.9x faster |
| open | 20.7 | 27.4 | 572 | 27.6x faster |
| snapshot-i | 5.0 | 10.4 | 24.1 | 4.8x faster |
| click | 8.9 | 13.6 | 31.3 | 3.5x faster |
| snapshot | 4.4 | 9.1 | 57.0 | 13.0x faster |
| fill | 4.4 | 8.9 | 17.6 | 4.0x faster |
| eval | 34.9 | 14.6 | 7.8 | **4.5x slower** |
| screenshot | 30.0 | 31.4 | 75.8 | 2.5x faster |
| back | 8.1 | 12.4 | 514 | 63.4x faster |
| get-title | 4.1 | 10.3 | 9.7 | 2.4x faster |
| close | 66.6 | 71.4 | 182 | 2.7x faster |

