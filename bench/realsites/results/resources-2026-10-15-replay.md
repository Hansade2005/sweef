# Resource use through bench/replay, 2026-10-15

Each site in a fresh process, 3 runs, medians; pages served from a local recording (`bench/replay`). Chromium 141 = the whole browser process tree (browser, renderer, GPU, utility); Sweef = its session daemon (the CLI processes live milliseconds). Load = until `open` / `goto(load)` returns; all = load plus the scripted steps (t3a and pipilot click a nav link, every site scrolls); idle = CPU burnt in the 5 s after. `before` = Sweef at dca9bbf, `after` = worktree-gaps HEAD. Script: `node/resources.mjs --replay`.

| site | load wall ms before / after / Chromium | load CPU s before / after / Chromium | all CPU s before / after / Chromium | idle CPU s per 5 s before / after / Chromium | peak MiB after / Chromium |
|---|---|---|---|---|---|
| t3a | 3880 / 2012 / 713 | 3.52 / 2.88 / 1.29 | 3.87 / 3.33 / 2.56 | 0.15 / 0.16 / 2.58 | 313 / 915 |
| pipilot | 3067 / 1633 / 684 | 4.10 / 2.27 / 1.37 | 5.57 / 4.50 / 2.79 | 0.28 / 0.44 / 0.83 | 357 / 879 |
| gh-repo | 7464 / 3265 / 969 | 8.32 / 4.84 / 1.88 | 8.90 / 4.92 / 2.04 | 1.68 / 0.83 / 1.00 | 376 / 875 |
| gh-issue | 6884 / 3522 / 1245 | 8.13 / 5.86 / 2.46 | 9.50 / 6.24 / 2.89 | 1.86 / 1.65 / 1.74 | 504 / 894 |
| wiki | 2075 / 1206 / 597 | 2.36 / 1.73 / 1.09 | 2.70 / 2.03 / 1.31 | 0.33 / 0.80 / 0.49 | 298 / 818 |
| mdn | 2767 / 2408 / 528 | 3.36 / 3.08 / 1.06 | 3.36 / 3.17 / 1.36 | 0.11 / 0.11 / 0.71 | 282 / 912 |
| nextjs | 4591 / 1934 / 554 | 4.93 / 2.46 / 1.09 | 5.47 / 2.51 / 1.16 | 0.97 / 1.74 / 1.01 | 213 / 824 |
| react | 1539 / 1106 / 888 | 1.91 / 1.45 / 1.88 | 1.92 / 1.57 / 1.92 | 0.89 / 0.76 / 3.98 | 239 / 1146 |
| pypi | 3525 / 2634 / 765 | 3.58 / 3.23 / 1.38 | 3.58 / 3.24 / 1.40 | 0.09 / 0.09 / 0.88 | 338 / 1048 |
| hn | 242 / 103 / 330 | 0.37 / 0.16 / 0.60 | 0.52 / 0.34 / 0.61 | 0.09 / 0.09 / 0.14 | 106 / 727 |
| **sum of 10** | | 40.6 / 28.0 / 14.1 | 45.4 / 31.9 / 18.0 | 6.4 / 6.7 / 13.4 | |

Sweef uses less CPU than Chromium idle (6.7 vs 13.4 s over the ten 5 s windows) and on Hacker News and react.dev, and far less memory everywhere, but more CPU to load script-heavy pages (28 vs 14 s summed): the pages' JavaScript runs in an interpreter. Tried and dropped: GOGC=300 (CPU within 5% either way, peak memory +20-50%).
