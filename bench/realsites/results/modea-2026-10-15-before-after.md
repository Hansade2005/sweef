# Mode A before/after: modea-2026-10-15-base-binary.json (sweef) vs modea-2026-10-15.json (sweef)

- before: sweef 0.1.0 2026-10-15T12:03:20.282Z; after: sweef 0.1.0 2026-10-15T12:57:26.432Z
- host: 4 CPUs Intel(R) Xeon(R) Processor @ 2.10GHz, Chromium 141.0.7390.37
- cold = fresh process and fresh HTTP/bytecode cache per run, until a snapshot with the expected text is in hand; medians over passing runs

## Summary

| | before | after | playwright |
|---|---|---|---|
| passed runs | 95/141 | 130/141 | 127/141 |
| tasks with every run passing | 30/47 | 43/47 | 41/47 |
| median cold ms, tasks passing before and after (33) | 2221 | 2237 | |
| median cold ms, tasks passing for after and playwright (42) | | 2939 | 2125 |
| tasks where after is faster / slower than playwright | | 16 / 26 | |
| median snapshot tokens (est.), 42 tasks | 4157 | 4283 | 7356 |

## By category (after vs playwright, tasks both pass)

| category | tasks | after faster | median after ms | median playwright ms |
|---|---|---|---|---|
| control | 1 | 1 | 530 | 1316 |
| docs | 3 | 2 | 861 | 2049 |
| ecommerce-demo | 3 | 0 | 3335 | 3090 |
| github | 3 | 0 | 10805 | 3326 |
| hacker-news | 4 | 4 | 578 | 1736 |
| login-form | 4 | 2 | 1737 | 1493 |
| mdn | 2 | 0 | 4402 | 1776 |
| news | 2 | 2 | 564 | 1322 |
| owner-site | 7 | 0 | 4863 | 2266 |
| package-registry | 1 | 0 | 4591 | 2262 |
| react-nextjs | 2 | 1 | 4589 | 2179 |
| science | 1 | 1 | 1405 | 1852 |
| spa-docs | 3 | 1 | 3177 | 2956 |
| spa-js-rendered | 2 | 2 | 576 | 1958 |
| spa-login | 1 | 0 | 2047 | 1746 |
| wikipedia | 3 | 0 | 3286 | 2411 |

## Every task

| task | category | before ms | after ms | after vs before | passes before/after/pw | playwright ms | after vs playwright | tokens after/pw |
|---|---|---|---|---|---|---|---|---|
| wiki-turing | wikipedia | 5067 | 3345 | **1.51x faster** | 3/3 3/3 3/3 | 2705 | 1.24x slower | 15011/158476 |
| wiki-go-language | wikipedia | 4434 | 3286 | **1.35x faster** | 3/3 3/3 3/3 | 2411 | 1.36x slower | 15020/88538 |
| wiki-search-flow | wikipedia | 2826 | 2804 | **1.01x faster** | 3/3 3/3 3/3 | 1523 | 1.84x slower | 8085/14527 |
| github-repo-golang | github | 2221 | 7795 | 3.51x slower | 3/3 3/3 3/3 | 2307 | 3.38x slower | 3994/7021 |
| github-issue-9 | github | 1722 | 10805 | 6.27x slower | 3/3 3/3 3/3 | 3326 | 3.25x slower | 6378/15729 |
| github-license-file | github | FAIL | 15373 | - | 0/3 3/3 3/3 | 3377 | 4.55x slower | 1403/3557 |
| hn-item-1 | hacker-news | 588 | 554 | **1.06x faster** | 3/3 3/3 3/3 | 1752 | **3.16x faster** | 1442/2967 |
| hn-dropbox | hacker-news | 866 | 655 | **1.32x faster** | 2/3 3/3 3/3 | 1808 | **2.76x faster** | 15012/60747 |
| hn-front | hacker-news | 564 | 603 | 1.07x slower | 3/3 3/3 3/3 | 1719 | **2.85x faster** | 8167/14581 |
| hn-ask-nav | hacker-news | 577 | 553 | **1.04x faster** | 3/3 3/3 3/3 | 1626 | **2.94x faster** | 8190/14606 |
| mdn-array-map | mdn | 9699 | 4669 | **2.08x faster** | 3/3 3/3 3/3 | 1865 | 2.50x slower | 13508/14141 |
| mdn-fetch | mdn | 7346 | 4135 | **1.78x faster** | 3/3 3/3 3/3 | 1686 | 2.45x slower | 15016/18689 |
| npm-left-pad | package-registry | 8396 | 7600 | **1.10x faster** | 2/3 3/3 0/3 | FAIL | - | 1809/FAIL |
| pypi-requests | package-registry | 6322 | 4591 | **1.38x faster** | 3/3 3/3 3/3 | 2262 | 2.03x slower | 4385/6991 |
| so-sorted-array | q-and-a | FAIL | FAIL | - | 0/3 0/3 3/3 | 7754 | - | FAIL/85537 |
| reddit-old-programming | forum | FAIL | FAIL | - | 0/3 0/3 0/3 | FAIL | - | FAIL/FAIL |
| react-dev-learn | react-nextjs | 6404 | 2435 | **2.63x faster** | 3/3 3/3 3/3 | 2772 | **1.14x faster** | 6398/12711 |
| nextjs-docs | react-nextjs | 15017 | 6743 | **2.23x faster** | 3/3 3/3 3/3 | 1586 | 4.25x slower | 3056/6051 |
| angular-overview | spa-docs | 6291 | 3074 | **2.05x faster** | 3/3 3/3 3/3 | 2956 | 1.04x slower | 4888/10347 |
| vue-introduction | spa-docs | 5955 | 3177 | **1.87x faster** | 3/3 3/3 3/3 | 14484 | **4.56x faster** | 4773/9464 |
| redux-getting-started | spa-docs | 4627 | 4064 | **1.14x faster** | 3/3 3/3 3/3 | 2176 | 1.87x slower | 5438/9639 |
| python-docs-json | docs | 2976 | 2237 | **1.33x faster** | 3/3 3/3 3/3 | 1943 | 1.15x slower | 15016/25640 |
| go-effective-go | docs | 5018 | 829 | **6.05x faster** | 3/3 3/3 3/3 | 2596 | **3.13x faster** | 15009/44186 |
| rust-lang-home | docs | 623 | 861 | 1.38x slower | 3/3 3/3 3/3 | 2049 | **2.38x faster** | 2025/2863 |
| arxiv-attention | science | 1074 | 1405 | 1.31x slower | 3/3 3/3 3/3 | 1852 | **1.32x faster** | 2458/4430 |
| example-com | control | 307 | 530 | 1.73x slower | 3/3 3/3 3/3 | 1316 | **2.48x faster** | 359/395 |
| saucedemo-login | spa-login | FAIL | 2047 | - | 0/3 3/3 3/3 | 1746 | 1.17x slower | 98/162 |
| practice-login | login-form | 1314 | 2092 | 1.59x slower | 3/3 3/3 3/3 | 2654 | **1.27x faster** | 703/1123 |
| internet-login | login-form | 537 | 1763 | 3.28x slower | 3/3 3/3 3/3 | 1459 | 1.21x slower | 230/369 |
| internet-dynamic-loading | login-form | FAIL | 1712 | - | 0/3 3/3 3/3 | 1526 | 1.12x slower | 109/185 |
| quotes-js-next | spa-js-rendered | 722 | 512 | **1.41x faster** | 3/3 3/3 3/3 | 2121 | **4.14x faster** | 494/853 |
| books-travel | ecommerce-demo | 1775 | 1602 | **1.11x faster** | 3/3 3/3 3/3 | 1577 | 1.02x slower | 4181/8285 |
| webscraper-laptops | ecommerce-demo | 5147 | 3407 | **1.51x faster** | 3/3 3/3 3/3 | 3090 | 1.10x slower | 1296/2333 |
| webscraper-tables | ecommerce-demo | 4893 | 3335 | **1.47x faster** | 3/3 3/3 3/3 | 3266 | 1.02x slower | 1284/2281 |
| httpbin-form | login-form | 320 | 387 | 1.21x slower | 3/3 3/3 3/3 | 1173 | **3.03x faster** | 276/420 |
| todomvc-react | spa-js-rendered | FAIL | 641 | - | 0/3 3/3 3/3 | 1794 | **2.80x faster** | 516/712 |
| search-ddg | search-results | 1065 | 965 | **1.10x faster** | 1/3 1/3 0/3 | FAIL | - | 3493/FAIL |
| npr-text | news | 829 | 702 | **1.18x faster** | 3/3 3/3 3/3 | 1442 | **2.05x faster** | 984/1298 |
| cnn-lite | news | 359 | 426 | 1.19x slower | 3/3 3/3 3/3 | 1201 | **2.82x faster** | 5054/6133 |
| zaide-home | owner-site | FAIL | FAIL | - | 0/3 0/3 0/3 | FAIL | - | FAIL/FAIL |
| t3a-home | owner-site | FAIL | 4863 | - | 0/3 3/3 3/3 | 2380 | 2.04x slower | 3621/7356 |
| t3a-nav-platform | owner-site | FAIL | 5410 | - | 0/3 3/3 3/3 | 2580 | 2.10x slower | 3621/7356 |
| t3a-nav-employers | owner-site | FAIL | 4525 | - | 0/3 3/3 2/3 | 2192 | 2.06x slower | 3621/7356 |
| pp-home | owner-site | FAIL | 6639 | - | 0/3 3/3 2/3 | 2266 | 2.93x slower | 5064/11410 |
| pp-nav-pricing | owner-site | FAIL | 4019 | - | 0/3 3/3 3/3 | 2229 | 1.80x slower | 5064/11447 |
| pp-nav-docs | owner-site | FAIL | 6282 | - | 0/3 3/3 3/3 | 2388 | 2.63x slower | 5064/11410 |
| pp-cta-start | owner-site | FAIL | 4627 | - | 0/3 3/3 3/3 | 2128 | 2.17x slower | 5064/11410 |

## Slowest 5 (after)

- github-license-file: 15373 ms (playwright 3377 ms)
- github-issue-9: 10805 ms (playwright 3326 ms)
- github-repo-golang: 7795 ms (playwright 2307 ms)
- npm-left-pad: 7600 ms
- nextjs-docs: 6743 ms (playwright 1586 ms)

## Failing runs (after)

- so-sorted-array 0/3: snapshot_mismatch: snapshot lacks expected text
- reddit-old-programming 0/3: snapshot_mismatch: snapshot lacks expected text
- search-ddg 1/3: load_error: load_error: ✗ Get "https://html.duckduckgo.com/html/?q=golang": read tcp 127.0.0.1:55098->127.0.0.1:33141: read: connect | snapshot_mismatch: snapshot lacks expected text
- zaide-home 0/3: load_error: load_error: ✗ Get "https://zaide.com/": tls: failed to verify certificate: x509: certificate is valid for kst.ye-xiu.com

## Method notes

- before = the previous build (bcabc8e), after = 632a6f3; both measured on 2026-10-15 on the same host with the same harness, one after the other, with nothing else running. Playwright ran in the same session as "after".
- `modea-2026-10-14.json` (the first baseline) is not used here: its Sweef "cold" runs shared one HTTP and bytecode cache, so they were partly warm. The harness now gives every run an empty cache (`SWEEF_CACHE_DIR`), and the previous build was re-measured that way.
- `internet-dynamic-loading` now checks the page's current heading ("Dynamically Loaded"); the old check text was no longer on the page and failed every tool.
- GitHub's "before" times are low because the previous build did not run GitHub's module scripts (they failed to load); the page text was server-rendered. The new build runs them, which is correct and slower.
- Failures for Stack Overflow (Cloudflare challenge), old.reddit.com (login wall), zaide.com (certificate for another host), and network resets from the sandbox proxy (hn-ask-nav, search-ddg) are site or network conditions, not engine errors; they are counted anyway.
