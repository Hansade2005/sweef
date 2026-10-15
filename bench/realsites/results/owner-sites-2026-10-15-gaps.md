# Owner sites after the gap work, 2026-10-15

- Time to usable and interaction latency: Mode A, live network, 3 fresh-process runs per task with empty caches, medians over passing runs (`modea-2026-10-15-gaps.json`; before = `modea-2026-10-15.json`). Time to usable counts from the first command (Sweef: daemon start; Chromium: node boot and browser launch) to a snapshot with the expected text, as in the Mode A reports. Chromium = Playwright 1.56 / Chromium 141 headless.
- Load and CPU: through `bench/replay` (local recording, no network noise), 3 runs, medians (`resources-2026-10-15-replay*.json`). Chromium = its whole process tree, Sweef = its daemon.
- Screenshots: `shots/fidelity.json` (SSIM against Chromium, 1 = identical).
- zaide.com could not be measured: its TLS certificate is for kst.ye-xiu.com (both browsers refuse it).

## the3rdacademy.com (t3a)

| metric | Sweef before | Sweef after | Chromium |
|---|---|---|---|
| t3a-home: time to usable snapshot, ms (runs passed) | 4863 (3/3) | 2637 (3/3) | 2150 (3/3) |
| t3a-nav-platform: time to usable snapshot, ms (runs passed) | 5410 (3/3) | 2898 (3/3) | 2410 (3/3) |
| t3a-nav-platform: interaction latency, ms | 529 | 520 | 510 |
| t3a-nav-employers: time to usable snapshot, ms (runs passed) | 4525 (3/3) | 2692 (3/3) | 2251 (3/3) |
| t3a-nav-employers: interaction latency, ms | 517 | 518 | 574 |
| load through replay: wall ms / CPU s | 3880 / 3.52 | 2012 / 2.88 | 713 / 1.29 |
| load + interactions: wall ms / CPU s | 4439 / 3.87 | 2577 / 3.33 | 1953 / 2.56 |
| peak memory, MiB | 282 | 313 | 915 |
| idle CPU, s per 5 s | 0.15 | 0.16 | 2.58 |
| screenshot desktop load: SSIM | | 0.8963 | reference |
| screenshot desktop scroll: SSIM | | 0.768 | reference |
| screenshot desktop nav-platform: SSIM | | 0.827 | reference |
| screenshot mobile load: SSIM | | 0.9332 | reference |
| screenshot mobile scroll: SSIM | | 0.853 | reference |
| screenshot mobile menu: SSIM | | 0.9896 | reference |

## pipilot.dev

| metric | Sweef before | Sweef after | Chromium |
|---|---|---|---|
| pp-home: time to usable snapshot, ms (runs passed) | 6639 (3/3) | 3832 (3/3) | 2125 (3/3) |
| pp-nav-pricing: time to usable snapshot, ms (runs passed) | 4019 (3/3) | 3028 (3/3) | 2230 (3/3) |
| pp-nav-pricing: interaction latency, ms | 1270 | 1110 | 286 |
| pp-nav-docs: time to usable snapshot, ms (runs passed) | 6282 (3/3) | 3632 (3/3) | 2122 (3/3) |
| pp-nav-docs: interaction latency, ms | 1602 | 1354 | 380 |
| pp-cta-start: time to usable snapshot, ms (runs passed) | 4627 (3/3) | 3451 (3/3) | 1910 (3/3) |
| pp-cta-start: interaction latency, ms | 3496 | 2347 | 254 |
| load through replay: wall ms / CPU s | 3067 / 4.10 | 1633 / 2.27 | 684 / 1.37 |
| load + interactions: wall ms / CPU s | 4083 / 5.57 | 3207 / 4.50 | 1244 / 2.79 |
| peak memory, MiB | 333 | 357 | 879 |
| idle CPU, s per 5 s | 0.28 | 0.44 | 0.83 |
| screenshot desktop load: SSIM | | 0.8674 | reference |
| screenshot desktop scroll: SSIM | | 0.8708 | reference |
| screenshot desktop nav-pricing: SSIM | | 0.8564 | reference |
| screenshot desktop cta: SSIM | | 0.8653 | reference |
| screenshot mobile load: SSIM | | 0.8401 | reference |
| screenshot mobile scroll: SSIM | | 0.8928 | reference |
| screenshot mobile cta: SSIM | | 0.8181 | reference |

Sweef stays slower than Chromium to a usable snapshot on these React/Vite sites and uses more CPU to load them (their JavaScript runs in an interpreter), with about a third of the memory and less CPU while idle. A click that navigates to another page returns in Sweef once that page has loaded (pp-cta-start); Playwright's click returns when the navigation starts.
