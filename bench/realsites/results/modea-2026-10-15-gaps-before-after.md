# Mode A before/after: modea-2026-10-15.json (sweef) vs modea-2026-10-15-gaps.json (sweef)

- before: sweef 0.1.0 2026-10-15T12:57:26.432Z; after: sweef 0.1.0 2026-10-15T18:22:11.020Z
- host: 4 CPUs Intel(R) Xeon(R) Processor @ 2.10GHz, Chromium 141.0.7390.37
- cold = fresh process and fresh HTTP/bytecode cache per run, until a snapshot with the expected text is in hand; medians over passing runs

## Summary

| | before | after | playwright |
|---|---|---|---|
| passed runs | 130/141 | 130/141 | 129/141 |
| tasks with every run passing | 43/47 | 42/47 | 43/47 |
| median cold ms, tasks passing before and after (44) | 2939 | 2188 | |
| median cold ms, tasks passing for after and playwright (42) | | 2188 | 2029 |
| tasks where after is faster / slower than playwright | | 25 / 17 | |
| median snapshot tokens (est.), 42 tasks | 4088 | 4217 | 7356 |

## By category (after vs playwright, tasks both pass)

| category | tasks | after faster | median after ms | median playwright ms |
|---|---|---|---|---|
| control | 1 | 1 | 356 | 1243 |
| docs | 3 | 3 | 801 | 2025 |
| ecommerce-demo | 3 | 3 | 2438 | 3371 |
| github | 3 | 0 | 4633 | 2950 |
| hacker-news | 4 | 4 | 673 | 1645 |
| login-form | 4 | 3 | 1408 | 1657 |
| mdn | 2 | 0 | 4412 | 1712 |
| news | 2 | 2 | 479 | 1294 |
| owner-site | 7 | 0 | 3028 | 2150 |
| package-registry | 1 | 0 | 3116 | 2301 |
| react-nextjs | 2 | 1 | 2000 | 2310 |
| science | 1 | 1 | 871 | 1759 |
| spa-docs | 3 | 2 | 2426 | 2586 |
| spa-js-rendered | 2 | 2 | 795 | 1860 |
| spa-login | 1 | 1 | 1714 | 1720 |
| wikipedia | 3 | 2 | 2188 | 2218 |

## Every task

| task | category | before ms | after ms | after vs before | passes before/after/pw | playwright ms | after vs playwright | tokens after/pw |
|---|---|---|---|---|---|---|---|---|
| wiki-turing | wikipedia | 3345 | 2541 | **1.32x faster** | 3/3 3/3 3/3 | 2218 | 1.15x slower | 15011/160975 |
| wiki-go-language | wikipedia | 3286 | 2188 | **1.50x faster** | 3/3 3/3 3/3 | 2458 | **1.12x faster** | 15020/88538 |
| wiki-search-flow | wikipedia | 2804 | 1443 | **1.94x faster** | 3/3 3/3 3/3 | 1598 | **1.11x faster** | 8085/14527 |
| github-repo-golang | github | 7795 | 4612 | **1.69x faster** | 3/3 3/3 3/3 | 2950 | 1.56x slower | 4033/7021 |
| github-issue-9 | github | 10805 | 6094 | **1.77x faster** | 3/3 3/3 3/3 | 3128 | 1.95x slower | 6378/15729 |
| github-license-file | github | 15373 | 4633 | **3.32x faster** | 3/3 3/3 3/3 | 2500 | 1.85x slower | 1403/3557 |
| hn-item-1 | hacker-news | 554 | 552 | **1.00x faster** | 3/3 3/3 3/3 | 1559 | **2.82x faster** | 1430/2956 |
| hn-dropbox | hacker-news | 655 | 687 | 1.05x slower | 3/3 3/3 3/3 | 1790 | **2.61x faster** | 15012/60747 |
| hn-front | hacker-news | 603 | 789 | 1.31x slower | 3/3 3/3 3/3 | 1597 | **2.02x faster** | 8146/14554 |
| hn-ask-nav | hacker-news | 553 | 658 | 1.19x slower | 3/3 3/3 3/3 | 1692 | **2.57x faster** | 8146/14554 |
| mdn-array-map | mdn | 4669 | 4856 | 1.04x slower | 3/3 3/3 3/3 | 1836 | 2.65x slower | 13507/13902 |
| mdn-fetch | mdn | 4135 | 3967 | **1.04x faster** | 3/3 3/3 3/3 | 1589 | 2.50x slower | 15010/18689 |
| npm-left-pad | package-registry | 7600 | 5238 | **1.45x faster** | 3/3 2/3 0/3 | FAIL | - | 1809/FAIL |
| pypi-requests | package-registry | 4591 | 3116 | **1.47x faster** | 3/3 3/3 3/3 | 2301 | 1.35x slower | 4253/6991 |
| so-sorted-array | q-and-a | FAIL | FAIL | - | 0/3 0/3 3/3 | 7243 | - | FAIL/85515 |
| reddit-old-programming | forum | FAIL | FAIL | - | 0/3 0/3 0/3 | FAIL | - | FAIL/FAIL |
| react-dev-learn | react-nextjs | 2435 | 1623 | **1.50x faster** | 3/3 3/3 3/3 | 2856 | **1.76x faster** | 6398/12711 |
| nextjs-docs | react-nextjs | 6743 | 2376 | **2.84x faster** | 3/3 3/3 3/3 | 1763 | 1.35x slower | 3056/6051 |
| angular-overview | spa-docs | 3074 | 2189 | **1.40x faster** | 3/3 3/3 3/3 | 2643 | **1.21x faster** | 4888/10347 |
| vue-introduction | spa-docs | 3177 | 2426 | **1.31x faster** | 3/3 3/3 3/3 | 2586 | **1.07x faster** | 4773/9261 |
| redux-getting-started | spa-docs | 4064 | 2555 | **1.59x faster** | 3/3 3/3 3/3 | 2036 | 1.25x slower | 5313/9639 |
| python-docs-json | docs | 2237 | 1703 | **1.31x faster** | 3/3 3/3 3/3 | 1828 | **1.07x faster** | 15016/25640 |
| go-effective-go | docs | 829 | 771 | **1.08x faster** | 3/3 3/3 3/3 | 2928 | **3.80x faster** | 15009/44186 |
| rust-lang-home | docs | 861 | 801 | **1.07x faster** | 3/3 3/3 3/3 | 2025 | **2.53x faster** | 2025/2863 |
| arxiv-attention | science | 1405 | 871 | **1.61x faster** | 3/3 3/3 3/3 | 1759 | **2.02x faster** | 2458/4430 |
| example-com | control | 530 | 356 | **1.49x faster** | 3/3 3/3 3/3 | 1243 | **3.49x faster** | 359/395 |
| saucedemo-login | spa-login | 2047 | 1714 | **1.19x faster** | 3/3 3/3 3/3 | 1720 | **1.00x faster** | 98/162 |
| practice-login | login-form | 2092 | 10887 | 5.21x slower | 3/3 2/3 3/3 | 2892 | 3.76x slower | 703/1123 |
| internet-login | login-form | 1763 | 1313 | **1.34x faster** | 3/3 3/3 3/3 | 1807 | **1.38x faster** | 230/369 |
| internet-dynamic-loading | login-form | 1712 | 1503 | **1.14x faster** | 3/3 3/3 3/3 | 1508 | **1.00x faster** | 109/185 |
| quotes-js-next | spa-js-rendered | 512 | 678 | 1.32x slower | 3/3 3/3 3/3 | 2033 | **3.00x faster** | 494/853 |
| books-travel | ecommerce-demo | 1602 | 706 | **2.27x faster** | 3/3 3/3 3/3 | 1485 | **2.10x faster** | 4181/8285 |
| webscraper-laptops | ecommerce-demo | 3407 | 2651 | **1.29x faster** | 3/3 3/3 3/3 | 3371 | **1.27x faster** | 1293/2344 |
| webscraper-tables | ecommerce-demo | 3335 | 2438 | **1.37x faster** | 3/3 3/3 3/3 | 3509 | **1.44x faster** | 1284/2281 |
| httpbin-form | login-form | 387 | 972 | 2.51x slower | 3/3 3/3 3/3 | 1099 | **1.13x faster** | 276/420 |
| todomvc-react | spa-js-rendered | 641 | 912 | 1.42x slower | 3/3 3/3 3/3 | 1688 | **1.85x faster** | 516/712 |
| search-ddg | search-results | 965 | 1340 | 1.39x slower | 1/3 3/3 0/3 | FAIL | - | 3498/FAIL |
| npr-text | news | 702 | 626 | **1.12x faster** | 3/3 3/3 3/3 | 1392 | **2.22x faster** | 975/1289 |
| cnn-lite | news | 426 | 332 | **1.28x faster** | 3/3 3/3 3/3 | 1197 | **3.61x faster** | 5075/6154 |
| zaide-home | owner-site | FAIL | FAIL | - | 0/3 0/3 0/3 | FAIL | - | FAIL/FAIL |
| t3a-home | owner-site | 4863 | 2637 | **1.84x faster** | 3/3 3/3 3/3 | 2150 | 1.23x slower | 3621/7356 |
| t3a-nav-platform | owner-site | 5410 | 2898 | **1.87x faster** | 3/3 3/3 3/3 | 2410 | 1.20x slower | 3621/7356 |
| t3a-nav-employers | owner-site | 4525 | 2692 | **1.68x faster** | 3/3 3/3 3/3 | 2251 | 1.20x slower | 3621/7356 |
| pp-home | owner-site | 6639 | 3832 | **1.73x faster** | 3/3 3/3 3/3 | 2125 | 1.80x slower | 5064/11410 |
| pp-nav-pricing | owner-site | 4019 | 3028 | **1.33x faster** | 3/3 3/3 3/3 | 2230 | 1.36x slower | 5064/11447 |
| pp-nav-docs | owner-site | 6282 | 3632 | **1.73x faster** | 3/3 3/3 3/3 | 2122 | 1.71x slower | 5064/11410 |
| pp-cta-start | owner-site | 4627 | 3451 | **1.34x faster** | 3/3 3/3 3/3 | 1910 | 1.81x slower | 5064/11447 |

## Slowest 5 (after)

- practice-login: 10887 ms (playwright 2892 ms)
- github-issue-9: 6094 ms (playwright 3128 ms)
- npm-left-pad: 5238 ms
- mdn-array-map: 4856 ms (playwright 1836 ms)
- github-license-file: 4633 ms (playwright 2500 ms)

## Failing runs (after)

- npm-left-pad 2/3: snapshot_mismatch: snapshot lacks expected text
- so-sorted-array 0/3: snapshot_mismatch: snapshot lacks expected text
- reddit-old-programming 0/3: snapshot_mismatch: snapshot lacks expected text
- practice-login 2/3: final_mismatch: final check failed
- zaide-home 0/3: load_error: load_error: ✗ Get "https://zaide.com/": tls: failed to verify certificate: x509: certificate is valid for kst.ye-xiu.com
