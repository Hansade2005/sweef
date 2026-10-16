# Real-site benchmark, Mode A (no LLM)

Generated from `modea-2026-10-15-gaps.json` on 2026-10-15T18:22:11.020Z.

- host: 4 CPUs (Intel(R) Xeon(R) Processor @ 2.10GHz), 15.7 GB RAM, 6.18.44-fc-v114
- versions: sweef `sweef 0.1.0`, Chromium 141.0.7390.37, playwright-core 1.56.1, node v22.22.0, agent-browser NOT AVAILABLE (not on PATH; not measured)
- 3 fresh-process runs per task and tool; medians over successful runs only; per-navigation timeout 30s; tools: sweef, playwright

## Pass rate (correctness first)

A run passes only if the page loaded, the snapshot contained the expected text, all scripted steps succeeded and the final page state matched the check.

| tool | passed runs | tasks with all runs passing | tasks with no passing run |
|---|---|---|---|
| sweef | 130/141 | 42/47 | 3 |
| playwright | 129/141 | 43/47 | 4 |

## Cold: time to a usable snapshot (ms, median)

From invoking the tool in a fresh process (no daemon / browser running) until a snapshot containing the expected text is in hand. For Sweef this is `open` (starts the daemon) plus `snapshot`, two CLI spawns. For Playwright it is process start (node boot) + Chromium launch + goto(load) + `_snapshotForAI()`. Lower is better.

| task | sweef | playwright | sweef vs playwright |
|---|---|---|---|
| wiki-turing | 2541 | 2218 | 1.15x slower |
| wiki-go-language | 2188 | 2458 | **1.12x faster** |
| wiki-search-flow | 1443 | 1598 | **1.11x faster** |
| github-repo-golang | 4612 | 2950 | 1.56x slower |
| github-issue-9 | 6094 | 3128 | 1.95x slower |
| github-license-file | 4633 | 2500 | 1.85x slower |
| hn-item-1 | 552 | 1559 | **2.82x faster** |
| hn-dropbox | 687 | 1790 | **2.61x faster** |
| hn-front | 789 | 1597 | **2.02x faster** |
| hn-ask-nav | 658 | 1692 | **2.57x faster** |
| mdn-array-map | 4856 | 1836 | 2.65x slower |
| mdn-fetch | 3967 | 1589 | 2.50x slower |
| npm-left-pad | 5238 | FAIL | sweef only passes |
| pypi-requests | 3116 | 2301 | 1.35x slower |
| so-sorted-array | FAIL | 7243 | playwright only passes |
| reddit-old-programming | FAIL | FAIL | both fail |
| react-dev-learn | 1623 | 2856 | **1.76x faster** |
| nextjs-docs | 2376 | 1763 | 1.35x slower |
| angular-overview | 2189 | 2643 | **1.21x faster** |
| vue-introduction | 2426 | 2586 | **1.07x faster** |
| redux-getting-started | 2555 | 2036 | 1.25x slower |
| python-docs-json | 1703 | 1828 | **1.07x faster** |
| go-effective-go | 771 | 2928 | **3.80x faster** |
| rust-lang-home | 801 | 2025 | **2.53x faster** |
| arxiv-attention | 871 | 1759 | **2.02x faster** |
| example-com | 356 | 1243 | **3.49x faster** |
| saucedemo-login | 1714 | 1720 | **1.00x faster** |
| practice-login | 10887 | 2892 | 3.76x slower |
| internet-login | 1313 | 1807 | **1.38x faster** |
| internet-dynamic-loading | 1503 | 1508 | **1.00x faster** |
| quotes-js-next | 678 | 2033 | **3.00x faster** |
| books-travel | 706 | 1485 | **2.10x faster** |
| webscraper-laptops | 2651 | 3371 | **1.27x faster** |
| webscraper-tables | 2438 | 3509 | **1.44x faster** |
| httpbin-form | 972 | 1099 | **1.13x faster** |
| todomvc-react | 912 | 1688 | **1.85x faster** |
| search-ddg | 1340 | FAIL | sweef only passes |
| npr-text | 626 | 1392 | **2.22x faster** |
| cnn-lite | 332 | 1197 | **3.61x faster** |
| zaide-home | FAIL | FAIL | both fail |
| t3a-home | 2637 | 2150 | 1.23x slower |
| t3a-nav-platform | 2898 | 2410 | 1.20x slower |
| t3a-nav-employers | 2692 | 2251 | 1.20x slower |
| pp-home | 3832 | 2125 | 1.80x slower |
| pp-nav-pricing | 3028 | 2230 | 1.36x slower |
| pp-nav-docs | 3632 | 2122 | 1.71x slower |
| pp-cta-start | 3451 | 1910 | 1.81x slower |

On the 42 tasks where both passed, Sweef was faster cold on 25 and slower on 17.
Median cold: Sweef 2188 ms, Playwright 2029 ms.

## Warm: repeat visit in the same session (ms, median)

Same URL opened again in the live session (HTTP cache populated, browser / daemon already running): navigation plus usable snapshot.

| task | sweef | playwright |
|---|---|---|
| wiki-turing | 1730 | 1063 |
| wiki-go-language | 1365 | 719 |
| wiki-search-flow | 760 | 397 |
| github-repo-golang | 2520 | 1090 |
| github-issue-9 | 3264 | 1242 |
| github-license-file | 4140 | 919 |
| hn-item-1 | 140 | 182 |
| hn-dropbox | 235 | 455 |
| hn-front | 155 | 252 |
| hn-ask-nav | 154 | 227 |
| mdn-array-map | 2277 | 348 |
| mdn-fetch | 1698 | 241 |
| npm-left-pad | 3494 | FAIL |
| pypi-requests | 2198 | 601 |
| so-sorted-array | FAIL | 5487 |
| reddit-old-programming | FAIL | FAIL |
| react-dev-learn | 908 | 797 |
| nextjs-docs | 1651 | 498 |
| angular-overview | 1212 | 482 |
| vue-introduction | 532 | 538 |
| redux-getting-started | 1748 | 438 |
| python-docs-json | 626 | 231 |
| go-effective-go | 488 | 537 |
| rust-lang-home | 40 | 104 |
| arxiv-attention | 128 | 162 |
| example-com | 95 | 44 |
| saucedemo-login | 162 | 102 |
| practice-login | 299 | 660 |
| internet-login | 804 | 132 |
| internet-dynamic-loading | 788 | 155 |
| quotes-js-next | 205 | 191 |
| books-travel | 299 | 88 |
| webscraper-laptops | 580 | 534 |
| webscraper-tables | 561 | 501 |
| httpbin-form | 81 | 508 |
| todomvc-react | 70 | 65 |
| search-ddg | 650 | FAIL |
| npr-text | 93 | 120 |
| cnn-lite | 128 | 117 |
| zaide-home | FAIL | FAIL |
| t3a-home | 1284 | 398 |
| t3a-nav-platform | 1134 | 364 |
| t3a-nav-employers | 1308 | 385 |
| pp-home | 1951 | 721 |
| pp-nav-pricing | 1800 | 738 |
| pp-nav-docs | 1833 | 695 |
| pp-cta-start | 1732 | 604 |

## Snapshot size (cold, full snapshot)

Bytes and estimated tokens (bytes/4; an estimate, not a tokenizer count). The formats differ: Sweef emits a YAML accessibility tree with `/url:` lines, Playwright `_snapshotForAI` emits its own aria tree. Smaller is cheaper to put in an agent context, provided the needed text survives (the check above).

| task | sweef KB | playwright KB | sweef tok | playwright tok | sweef -i KB |
|---|---|---|---|---|---|
| wiki-turing | 58.6 | 628.8 | 15011 | 160975 | 58.6 |
| wiki-go-language | 58.7 | 345.9 | 15020 | 88538 | 58.6 |
| wiki-search-flow | 31.6 | 56.7 | 8085 | 14527 | 25.8 |
| github-repo-golang | 15.8 | 27.4 | 4033 | 7021 | 13.7 |
| github-issue-9 | 24.9 | 61.4 | 6378 | 15729 | 17.3 |
| github-license-file | 5.5 | 13.9 | 1403 | 3557 | 5.0 |
| hn-item-1 | 5.6 | 11.5 | 1430 | 2956 | 4.6 |
| hn-dropbox | 58.6 | 237.3 | 15012 | 60747 | 47.5 |
| hn-front | 31.8 | 56.8 | 8146 | 14554 | 26.5 |
| hn-ask-nav | 31.8 | 56.8 | 8146 | 14554 | 26.5 |
| mdn-array-map | 52.8 | 54.3 | 13507 | 13902 | 41.6 |
| mdn-fetch | 58.6 | 73.0 | 15010 | 18689 | 51.7 |
| npm-left-pad | 7.1 | FAIL | 1809 | FAIL | 5.9 |
| pypi-requests | 16.6 | 27.3 | 4253 | 6991 | 12.7 |
| so-sorted-array | FAIL | 334.0 | FAIL | 85515 | - |
| reddit-old-programming | FAIL | FAIL | FAIL | FAIL | - |
| react-dev-learn | 25.0 | 49.7 | 6398 | 12711 | 9.2 |
| nextjs-docs | 11.9 | 23.6 | 3056 | 6051 | 8.5 |
| angular-overview | 19.1 | 40.4 | 4888 | 10347 | 17.5 |
| vue-introduction | 18.6 | 36.2 | 4773 | 9261 | 10.2 |
| redux-getting-started | 20.8 | 37.7 | 5313 | 9639 | 13.0 |
| python-docs-json | 58.7 | 100.2 | 15016 | 25640 | 27.3 |
| go-effective-go | 58.6 | 172.6 | 15009 | 44186 | 24.2 |
| rust-lang-home | 7.9 | 11.2 | 2025 | 2863 | 5.2 |
| arxiv-attention | 9.6 | 17.3 | 2458 | 4430 | 7.8 |
| example-com | 1.4 | 1.5 | 359 | 395 | 0.1 |
| saucedemo-login | 0.4 | 0.6 | 98 | 162 | 0.2 |
| practice-login | 2.7 | 4.4 | 703 | 1123 | 1.2 |
| internet-login | 0.9 | 1.4 | 230 | 369 | 0.6 |
| internet-dynamic-loading | 0.4 | 0.7 | 109 | 185 | 0.4 |
| quotes-js-next | 1.9 | 3.3 | 494 | 853 | 0.4 |
| books-travel | 16.3 | 32.4 | 4181 | 8285 | 14.9 |
| webscraper-laptops | 5.1 | 9.2 | 1293 | 2344 | 4.3 |
| webscraper-tables | 5.0 | 8.9 | 1284 | 2281 | 3.8 |
| httpbin-form | 1.1 | 1.6 | 276 | 420 | 0.7 |
| todomvc-react | 2.0 | 2.8 | 516 | 712 | 1.5 |
| search-ddg | 13.7 | FAIL | 3498 | FAIL | 13.4 |
| npr-text | 3.8 | 5.0 | 975 | 1289 | 3.7 |
| cnn-lite | 19.8 | 24.0 | 5075 | 6154 | 19.6 |
| zaide-home | FAIL | FAIL | FAIL | FAIL | - |
| t3a-home | 14.1 | 28.7 | 3621 | 7356 | 3.9 |
| t3a-nav-platform | 14.1 | 28.7 | 3621 | 7356 | 3.9 |
| t3a-nav-employers | 14.1 | 28.7 | 3621 | 7356 | 3.9 |
| pp-home | 19.8 | 44.6 | 5064 | 11410 | 10.5 |
| pp-nav-pricing | 19.8 | 44.7 | 5064 | 11447 | 10.4 |
| pp-nav-docs | 19.8 | 44.6 | 5064 | 11410 | 10.4 |
| pp-cta-start | 19.8 | 44.7 | 5064 | 11447 | 10.4 |

## Step latency (ms, median over scripted steps; CLI tools include process spawn)

Sweef and agent-browser are driven as CLIs (one process spawn per command, as an agent shell tool would). Playwright steps are in-process calls with no spawn cost, which favours Playwright. Click/fill return semantics also differ (auto-wait vs settle), so treat this column as indicative only.

| task | steps | sweef | playwright |
|---|---|---|---|
| wiki-search-flow | 2 | 827 | 135 |
| hn-ask-nav | 1 | 141 | 178 |
| saucedemo-login | 3 | 30 | 27 |
| practice-login | 3 | 13 | 30 |
| internet-login | 3 | 16 | 28 |
| internet-dynamic-loading | 2 | 2517 | 2532 |
| quotes-js-next | 1 | 179 | 138 |
| books-travel | 1 | 250 | 150 |
| webscraper-laptops | 2 | 636 | 275 |
| httpbin-form | 2 | 521 | 373 |
| todomvc-react | 2 | 16 | 31 |
| t3a-nav-platform | 1 | 520 | 510 |
| t3a-nav-employers | 1 | 518 | 574 |
| pp-nav-pricing | 1 | 1110 | 286 |
| pp-nav-docs | 1 | 1354 | 380 |
| pp-cta-start | 1 | 2347 | 254 |

Follow-up `snapshot` latency on the current page (ms, median), a pure step: sweef 15, playwright 49.

## Failures (not hidden)

| task | tool | failed runs | classes | first error |
|---|---|---|---|---|
| npm-left-pad | sweef | 1/3 | snapshot_mismatch | snapshot lacks expected text |
| npm-left-pad | playwright | 3/3 | snapshot_mismatch | snapshot lacks expected text |
| so-sorted-array | sweef | 3/3 | snapshot_mismatch | snapshot lacks expected text |
| reddit-old-programming | sweef | 3/3 | snapshot_mismatch | snapshot lacks expected text |
| reddit-old-programming | playwright | 3/3 | snapshot_mismatch | snapshot lacks expected text |
| practice-login | sweef | 1/3 | final_mismatch | final check failed |
| search-ddg | playwright | 3/3 | snapshot_mismatch | snapshot lacks expected text |
| zaide-home | sweef | 3/3 | load_error | load_error: ✗ Get "https://zaide.com/": tls: failed to verify certificate: x509: certificate is valid for kst.ye-xiu.com, not zaide.com |
| zaide-home | playwright | 3/3 | snapshot_mismatch | snapshot lacks expected text |

## Slowest 5 Sweef cases (cold, passing runs)

| rank | task | sweef ms | playwright ms | open | snapshot | KB |
|---|---|---|---|---|---|---|
| 1 | practice-login | 10887 | 2892 | 10862 | 13 | 2.7 |
| 2 | github-issue-9 | 6094 | 3128 | 5468 | 72 | 24.9 |
| 3 | npm-left-pad | 5238 | FAIL | 5202 | 13 | 7.1 |
| 4 | mdn-array-map | 4856 | 1836 | 4789 | 34 | 52.8 |
| 5 | github-license-file | 4633 | 2500 | 4097 | 547 | 5.5 |

## Method notes

- Each run: fresh process (Sweef: new SWEEF_HOME and daemon killed after; Playwright: new node process and new Chromium).
- cold = first visit in that fresh process (includes daemon / browser launch); warm = repeat visit of the same URL in the same session (HTTP cache populated).
- Tool order is rotated per run and requests are paced to limit rate-limit bias.
