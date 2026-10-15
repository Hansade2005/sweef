# Real-site benchmark, Mode A (no LLM)

Generated from `modea-2026-10-15-base-binary.json` on 2026-10-15T12:03:20.282Z.

- host: 4 CPUs (Intel(R) Xeon(R) Processor @ 2.10GHz), 15.7 GB RAM, 6.18.44-fc-v114
- versions: sweef `sweef 0.1.0`, Chromium 141.0.7390.37, playwright-core 1.56.1, node v22.22.0, agent-browser NOT AVAILABLE (not on PATH; not measured)
- 3 fresh-process runs per task and tool; medians over successful runs only; per-navigation timeout 30s; tools: sweef

## Pass rate (correctness first)

A run passes only if the page loaded, the snapshot contained the expected text, all scripted steps succeeded and the final page state matched the check.

| tool | passed runs | tasks with all runs passing | tasks with no passing run |
|---|---|---|---|
| sweef | 95/141 | 30/47 | 14 |

## Cold: time to a usable snapshot (ms, median)

From invoking the tool in a fresh process (no daemon / browser running) until a snapshot containing the expected text is in hand. For Sweef this is `open` (starts the daemon) plus `snapshot`, two CLI spawns. For Playwright it is process start (node boot) + Chromium launch + goto(load) + `_snapshotForAI()`. Lower is better.

| task | sweef |
|---|---|
| wiki-turing | 5067 |
| wiki-go-language | 4434 |
| wiki-search-flow | 2826 |
| github-repo-golang | 2221 |
| github-issue-9 | 1722 |
| github-license-file | FAIL |
| hn-item-1 | 588 |
| hn-dropbox | 866 |
| hn-front | 564 |
| hn-ask-nav | 577 |
| mdn-array-map | 9699 |
| mdn-fetch | 7346 |
| npm-left-pad | 8396 |
| pypi-requests | 6322 |
| so-sorted-array | FAIL |
| reddit-old-programming | FAIL |
| react-dev-learn | 6404 |
| nextjs-docs | 15017 |
| angular-overview | 6291 |
| vue-introduction | 5955 |
| redux-getting-started | 4627 |
| python-docs-json | 2976 |
| go-effective-go | 5018 |
| rust-lang-home | 623 |
| arxiv-attention | 1074 |
| example-com | 307 |
| saucedemo-login | FAIL |
| practice-login | 1314 |
| internet-login | 537 |
| internet-dynamic-loading | FAIL |
| quotes-js-next | 722 |
| books-travel | 1775 |
| webscraper-laptops | 5147 |
| webscraper-tables | 4893 |
| httpbin-form | 320 |
| todomvc-react | FAIL |
| search-ddg | 1065 |
| npr-text | 829 |
| cnn-lite | 359 |
| zaide-home | FAIL |
| t3a-home | FAIL |
| t3a-nav-platform | FAIL |
| t3a-nav-employers | FAIL |
| pp-home | FAIL |
| pp-nav-pricing | FAIL |
| pp-nav-docs | FAIL |
| pp-cta-start | FAIL |

## Warm: repeat visit in the same session (ms, median)

Same URL opened again in the live session (HTTP cache populated, browser / daemon already running): navigation plus usable snapshot.

| task | sweef |
|---|---|
| wiki-turing | 3406 |
| wiki-go-language | 2856 |
| wiki-search-flow | 2402 |
| github-repo-golang | 1412 |
| github-issue-9 | 2044 |
| github-license-file | FAIL |
| hn-item-1 | 151 |
| hn-dropbox | 163 |
| hn-front | 153 |
| hn-ask-nav | 184 |
| mdn-array-map | 4828 |
| mdn-fetch | 3698 |
| npm-left-pad | 5112 |
| pypi-requests | 4606 |
| so-sorted-array | FAIL |
| reddit-old-programming | FAIL |
| react-dev-learn | 6998 |
| nextjs-docs | 13675 |
| angular-overview | 4944 |
| vue-introduction | 2052 |
| redux-getting-started | 3396 |
| python-docs-json | 1969 |
| go-effective-go | 3444 |
| rust-lang-home | 40 |
| arxiv-attention | 177 |
| example-com | 86 |
| saucedemo-login | FAIL |
| practice-login | 183 |
| internet-login | 116 |
| internet-dynamic-loading | FAIL |
| quotes-js-next | 188 |
| books-travel | 525 |
| webscraper-laptops | 2395 |
| webscraper-tables | 2444 |
| httpbin-form | 76 |
| todomvc-react | FAIL |
| search-ddg | 77 |
| npr-text | 137 |
| cnn-lite | 48 |
| zaide-home | FAIL |
| t3a-home | FAIL |
| t3a-nav-platform | FAIL |
| t3a-nav-employers | FAIL |
| pp-home | FAIL |
| pp-nav-pricing | FAIL |
| pp-nav-docs | FAIL |
| pp-cta-start | FAIL |

## Snapshot size (cold, full snapshot)

Bytes and estimated tokens (bytes/4; an estimate, not a tokenizer count). The formats differ: Sweef emits a YAML accessibility tree with `/url:` lines, Playwright `_snapshotForAI` emits its own aria tree. Smaller is cheaper to put in an agent context, provided the needed text survives (the check above).

| task | sweef KB | sweef tok | sweef -i KB |
|---|---|---|---|
| wiki-turing | 58.5 | 14976 | 58.6 |
| wiki-go-language | 58.6 | 15005 | 58.7 |
| wiki-search-flow | 34.6 | 8856 | 28.7 |
| github-repo-golang | 11.9 | 3038 | 10.4 |
| github-issue-9 | 25.6 | 6553 | 18.0 |
| github-license-file | FAIL | FAIL | - |
| hn-item-1 | 5.6 | 1441 | 4.6 |
| hn-dropbox | 58.5 | 14976 | 47.3 |
| hn-front | 31.7 | 8108 | 26.4 |
| hn-ask-nav | 31.7 | 8108 | 26.4 |
| mdn-array-map | 52.7 | 13500 | 41.7 |
| mdn-fetch | 58.7 | 15015 | 51.7 |
| npm-left-pad | 7.1 | 1809 | 5.9 |
| pypi-requests | 17.1 | 4387 | 13.1 |
| so-sorted-array | FAIL | FAIL | - |
| reddit-old-programming | FAIL | FAIL | - |
| react-dev-learn | 23.4 | 5997 | 7.1 |
| nextjs-docs | 8.8 | 2245 | 5.6 |
| angular-overview | 38.2 | 9792 | 35.2 |
| vue-introduction | 11.6 | 2974 | 3.8 |
| redux-getting-started | 19.6 | 5025 | 12.1 |
| python-docs-json | 58.7 | 15019 | 27.1 |
| go-effective-go | 58.6 | 15000 | 19.8 |
| rust-lang-home | 7.9 | 2025 | 5.2 |
| arxiv-attention | 9.5 | 2427 | 7.6 |
| example-com | 1.4 | 359 | 0.1 |
| saucedemo-login | FAIL | FAIL | - |
| practice-login | 3.0 | 771 | 1.5 |
| internet-login | 0.9 | 230 | 0.6 |
| internet-dynamic-loading | FAIL | FAIL | - |
| quotes-js-next | 1.9 | 494 | 0.4 |
| books-travel | 16.2 | 4157 | 14.8 |
| webscraper-laptops | 5.0 | 1278 | 4.3 |
| webscraper-tables | 5.0 | 1290 | 3.8 |
| httpbin-form | 1.1 | 276 | 0.7 |
| todomvc-react | FAIL | FAIL | - |
| search-ddg | 13.4 | 3427 | 13.1 |
| npr-text | 3.8 | 982 | 3.7 |
| cnn-lite | 19.7 | 5043 | 19.5 |
| zaide-home | FAIL | FAIL | - |
| t3a-home | FAIL | FAIL | - |
| t3a-nav-platform | FAIL | FAIL | - |
| t3a-nav-employers | FAIL | FAIL | - |
| pp-home | FAIL | FAIL | - |
| pp-nav-pricing | FAIL | FAIL | - |
| pp-nav-docs | FAIL | FAIL | - |
| pp-cta-start | FAIL | FAIL | - |

## Step latency (ms, median over scripted steps; CLI tools include process spawn)

Sweef and agent-browser are driven as CLIs (one process spawn per command, as an agent shell tool would). Playwright steps are in-process calls with no spawn cost, which favours Playwright. Click/fill return semantics also differ (auto-wait vs settle), so treat this column as indicative only.

| task | steps | sweef |
|---|---|---|
| wiki-search-flow | 2 | 2794 |
| hn-ask-nav | 1 | 164 |
| saucedemo-login | 3 | FAIL |
| practice-login | 3 | 13 |
| internet-login | 3 | 13 |
| internet-dynamic-loading | 2 | FAIL |
| quotes-js-next | 1 | 177 |
| books-travel | 1 | 629 |
| webscraper-laptops | 2 | 3100 |
| httpbin-form | 2 | 38 |
| todomvc-react | 2 | FAIL |
| t3a-nav-platform | 1 | FAIL |
| t3a-nav-employers | 1 | FAIL |
| pp-nav-pricing | 1 | FAIL |
| pp-nav-docs | 1 | FAIL |
| pp-cta-start | 1 | FAIL |

Follow-up `snapshot` latency on the current page (ms, median), a pure step: sweef 16.

## Failures (not hidden)

| task | tool | failed runs | classes | first error |
|---|---|---|---|---|
| github-license-file | sweef | 3/3 | snapshot_mismatch | snapshot lacks expected text |
| hn-dropbox | sweef | 1/3 | load_error | load_error: ✗ Get "https://news.ycombinator.com/item?id=8863": read tcp 127.0.0.1:52942->127.0.0.1:33141: read: connection reset by peer |
| npm-left-pad | sweef | 1/3 | snapshot_mismatch | snapshot lacks expected text |
| so-sorted-array | sweef | 3/3 | snapshot_mismatch | snapshot lacks expected text |
| reddit-old-programming | sweef | 3/3 | snapshot_mismatch | snapshot lacks expected text |
| saucedemo-login | sweef | 3/3 | final_mismatch | final check failed |
| internet-dynamic-loading | sweef | 3/3 | step_error | step_error: ✗ engine: timeout waiting for condition: locator "text=Hello World!" never became visible |
| todomvc-react | sweef | 3/3 | final_mismatch | final check failed |
| search-ddg | sweef | 2/3 | snapshot_mismatch | snapshot lacks expected text |
| zaide-home | sweef | 3/3 | load_error | load_error: ✗ Get "https://zaide.com/": tls: failed to verify certificate: x509: certificate is valid for kst.ye-xiu.com, not zaide.com |
| t3a-home | sweef | 3/3 | snapshot_mismatch | snapshot lacks expected text |
| t3a-nav-platform | sweef | 3/3 | step_error | step_error: ✗ engine: timeout waiting for condition: locator "header a[href=\"/platform\"]" never became visible |
| t3a-nav-employers | sweef | 3/3 | step_error | step_error: ✗ engine: timeout waiting for condition: locator "header a[href=\"/employers\"]" never became visible |
| pp-home | sweef | 3/3 | load_error | load_error: ✗ read unix @->/tmp/sweef-home-RLu5sj/default.sock: i/o timeout |
| pp-nav-pricing | sweef | 3/3 | load_error | load_error: ✗ read unix @->/tmp/sweef-home-8YhHDV/default.sock: i/o timeout |
| pp-nav-docs | sweef | 3/3 | load_error | load_error: ✗ read unix @->/tmp/sweef-home-2OXUQg/default.sock: i/o timeout |
| pp-cta-start | sweef | 3/3 | load_error | load_error: ✗ read unix @->/tmp/sweef-home-O7ACzj/default.sock: i/o timeout |

## Slowest 5 Sweef cases (cold, passing runs)

| rank | task | sweef ms | playwright ms | open | snapshot | KB |
|---|---|---|---|---|---|---|
| 1 | nextjs-docs | 15017 | - | 14826 | 186 | 8.8 |
| 2 | mdn-array-map | 9699 | - | 9656 | 26 | 52.7 |
| 3 | npm-left-pad | 8396 | - | 8366 | 16 | 7.1 |
| 4 | mdn-fetch | 7346 | - | 7309 | 20 | 58.7 |
| 5 | react-dev-learn | 6404 | - | 4747 | 18 | 23.4 |

## Method notes

- Each run: fresh process (Sweef: new SWEEF_HOME and daemon killed after; Playwright: new node process and new Chromium).
- cold = first visit in that fresh process (includes daemon / browser launch); warm = repeat visit of the same URL in the same session (HTTP cache populated).
- Tool order is rotated per run and requests are paced to limit rate-limit bias.
