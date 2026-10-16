# Owner sites: Sweef vs Chromium, 2026-10-15

- Time to usable and interaction latency: Mode A, 3 fresh-process runs per task, medians over passing runs (`modea-2026-10-15.json`, Sweef 632a6f3). Chromium = Playwright 1.56 / Chromium 141 headless.
- Memory, CPU and wall for load and load+interactions, idle CPU over 5 s: `resources-2026-10-15.json` (3 runs, medians; Chromium counted as its whole process tree, Sweef as its daemon), measured with Sweef 4937ba2.
- Screenshot diff: `shots/fidelity.json` (SSIM, 1 = identical; MAE on 0-255; share of differing pixels) at 1280x800 and 390x844 after load and after each interaction, Sweef 4937ba2; side-by-side PNGs (Chromium left, Sweef right) in `shots/<site>/`.
- zaide.com could not be measured: its TLS certificate is for kst.ye-xiu.com (both browsers refuse it) and plain HTTP answers 404.

## t3a

| metric | Sweef | Chromium |
|---|---|---|
| t3a-home: time to usable snapshot, ms (runs passed) | 4863 (3/3) | 2380 (3/3) |
| snapshot tokens (est.) | 3621 | 7356 |
| t3a-nav-platform: time to usable snapshot, ms (runs passed) | 5410 (3/3) | 2580 (3/3) |
| t3a-nav-platform: interaction latency, ms | 529 | 547 |
| t3a-nav-employers: time to usable snapshot, ms (runs passed) | 4525 (3/3) | 2192 (2/3) |
| t3a-nav-employers: interaction latency, ms | 517 | 509 |
| load: wall ms / CPU s | 4867 / 3.94 | 1602 / 1.43 |
| load + interactions: wall ms / CPU s | 5424 / 4.31 | 2417 / 2.75 |
| peak memory, MiB | 295 | 919 |
| idle CPU, s per 5 s | 0.15 | 2.66 |
| screenshot desktop load: SSIM / MAE / differing | 0.896 / 8.4 / 4.7% | reference |
| screenshot desktop scroll: SSIM / MAE / differing | 0.768 / 16.7 / 14.3% | reference |
| screenshot desktop nav-platform: SSIM / MAE / differing | 0.827 / 15.9 / 8.4% | reference |
| screenshot mobile load: SSIM / MAE / differing | 0.933 / 6.6 / 3.7% | reference |
| screenshot mobile scroll: SSIM / MAE / differing | 0.853 / 9.7 / 7.1% | reference |
| screenshot mobile menu: SSIM / MAE / differing | 0.990 / 0.6 / 0.5% | reference |
| interaction parity (URL and state check match Chromium) | 6/6 states | |

## pipilot

| metric | Sweef | Chromium |
|---|---|---|
| pp-home: time to usable snapshot, ms (runs passed) | 6639 (3/3) | 2266 (2/3) |
| snapshot tokens (est.) | 5064 | 11410 |
| pp-nav-pricing: time to usable snapshot, ms (runs passed) | 4019 (3/3) | 2229 (3/3) |
| pp-nav-pricing: interaction latency, ms | 1270 | 192 |
| pp-nav-docs: time to usable snapshot, ms (runs passed) | 6282 (3/3) | 2388 (3/3) |
| pp-nav-docs: interaction latency, ms | 1602 | 142 |
| pp-cta-start: time to usable snapshot, ms (runs passed) | 4627 (3/3) | 2128 (3/3) |
| pp-cta-start: interaction latency, ms | 3496 | 405 |
| load: wall ms / CPU s | 3528 / 2.63 | 1269 / 1.48 |
| load + interactions: wall ms / CPU s | 4905 / 4.37 | 1894 / 3.13 |
| peak memory, MiB | 334 | 881 |
| idle CPU, s per 5 s | 0.80 | 0.89 |
| screenshot desktop load: SSIM / MAE / differing | 0.867 / 8.2 / 5.5% | reference |
| screenshot desktop scroll: SSIM / MAE / differing | 0.871 / 5.6 / 5.2% | reference |
| screenshot desktop nav-pricing: SSIM / MAE / differing | 0.856 / 7.9 / 6.3% | reference |
| screenshot desktop cta: SSIM / MAE / differing | 0.863 / 9.9 / 6.5% | reference |
| screenshot mobile load: SSIM / MAE / differing | 0.840 / 7.5 / 6.7% | reference |
| screenshot mobile scroll: SSIM / MAE / differing | 0.893 / 5.0 / 4.7% | reference |
| screenshot mobile cta: SSIM / MAE / differing | 0.818 / 8.9 / 7.9% | reference |
| interaction parity (URL and state check match Chromium) | 7/7 states | |

## zaide

| metric | Sweef | Chromium |
|---|---|---|
| zaide-home: time to usable snapshot, ms (runs passed) | FAIL (0/3) | FAIL (0/3) |
| screenshot desktop load: SSIM / MAE / differing | - | reference |
| screenshot mobile load: SSIM / MAE / differing | - | reference |
| interaction parity (URL and state check match Chromium) | 0/2 states | |

