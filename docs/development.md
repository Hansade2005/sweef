# Development guide

How the code is organised and which documents are contracts. Start with [CONTRIBUTING.md](../CONTRIBUTING.md) for the workflow.

## Shape of the system

```
 cmd/sweef            CLI entry: sweef gui | agent | open/snapshot/click (daemon-backed) | mcp | version
   │
   ├── internal/app           the desktop browser: windows, tabs, sessions, settings, history, downloads, Data Saver
   │     ├── compositor       combines chrome, page and overlay layers into damage-tracked frames
   │     ├── headless         a window that records frames and injects input (tests, benchmarks)
   │     ├── omnibox/store/state/privacy   suggestions, profile storage, UI state, tracker blocking
   │     └── cmd/appbench     GUI latency, memory and start-up report (writes internal/app/BENCH.md)
   ├── internal/ui            the browser chrome as HTML/CSS/JS assets (tabs, omnibox, settings, new tab, onboarding)
   ├── internal/shell/*       thin native window shells: x11 (verified), win32, cocoa (build-tested), shared helpers
   ├── internal/agent         Sidekick: the Go agent loop on the Anthropic Messages API
   ├── internal/agentbridge   connects the agent loop to live browser tabs
   └── internal/mcp, rpc, protocol   the automation surface (CLI daemon, MCP server, wire format)

 internal/engine          a tab: loads a page, runs JS, lays out and paints; ties everything below together
   ├── internal/net       HTTP client, cache, DOM tree (Go DOM) and snapshots
   ├── internal/style     CSS parsing, cascade and the cross-pass style cache
   ├── internal/render    box tree, layout (block, inline, flex, grid, table), display list
   ├── internal/raster    software rasteriser, Canvas 2D, SVG, shadows, filters, damage-rect repaint
   ├── internal/font      font discovery, shaping, WOFF/WOFF2/TTF/OTF decoding
   ├── internal/gl        software WebGL (GLSL compiler and pipeline)
   ├── internal/media     audio and video decoders and playback (H.264, HEVC, VP8/9, AV1, AAC, Opus, MP3, ...)
   │   └── cdm            DRM key systems behind EME (Clear Key, Widevine host, PlayReady, FairPlay); docs/drm.md
   ├── internal/pdf       PDF rendering
   └── internal/update    signed auto-update verification library (design: docs/updates.md)
```

JavaScript runs in QuickJS (no JIT) through `modernc.org/quickjs`; the DOM is implemented in Go and exposed through host
functions and a JavaScript prelude (`internal/engine/*_prelude.js`).

## Contracts you must not break casually

| Document | What it fixes |
|---|---|
| [gui-contract.md](gui-contract.md) | messages between the chrome page and the Go app; the native window interface (`internal/shell`) |
| [data-saver.md](data-saver.md) | Data Saver levels, engine API, honesty rules for "saved" and "used" |
| [updates.md](updates.md) | update trust model and file formats |
| [agent.md](agent.md) | the agent loop, caching strategy, safety gates, measured cost |
| [compat.md](compat.md) | what is supported and what is a known gap |

A change that alters one of these needs an ADR ([adr/](adr/README.md)) or at least a discussion in the issue.

## Testing

* `go test -short ./...` is what CI runs on Linux. Heavy codec tests are FATE-gated and only run without `-short`.
* **Fidelity:** `fidelity/` and `tools/fidelity` compare layout and ink against Chromium reference captures made on Linux
  with a fixed font set. They only make sense on Linux with that font set (CI installs it).
* **Paint goldens:** `internal/engine/testdata/paint_golden.txt` pins what the chrome and onboarding pages paint. A moved
  golden must be explained in the PR and regenerated from a clean checkout.
* **Debug cross-checks:** `SWEEF_LAYOUT_CHECK=1` compares every cached layout with a from-scratch one;
  `SWEEF_INCR_CHECK=1` does the same for incremental paint. Run the UI tests with them on after touching layout caches.
* **GUI behaviour tests** run against the headless window (`internal/app/headless`), so they need no display.
* **Real window:** `scripts/bench/vschrome.sh` runs the desktop browser and headed Chromium side by side under Xvfb.

## Benchmarks and the evidence rule

| Tool | Measures |
|---|---|
| `go run ./internal/app/cmd/appbench` | in-process GUI latency, start-up, memory, history/suggest speed → `internal/app/BENCH.md` |
| `scripts/bench/vschrome.sh` | Sweef vs headed Chromium, same method → `docs/vs-chrome.md` |
| `scripts/bench/datasaver.sh` | bytes on the wire for the same pages, three modes plus Chromium → `docs/data-saver-bench.md` |
| `bench/loadtl` | page-load timelines |

Benchmarks refuse to run on a busy machine where noise would mislead; do not edit generated reports by hand.

## Code style

* `gofmt` (CI enforces it, `third_party/` excluded), `go vet -composites=false`.
* Comments explain *why*; keep them short and near the code they explain.
* No cgo. Platform code lives in build-tagged files (`*_unix.go`, `*_windows.go`, `*_darwin.go`).
* Errors are returned, not logged and swallowed; background goroutines must stop when their tab or app closes (a leak
  test guards this).
* Chrome UI code (`internal/ui/web`) is plain ES5-style JavaScript with no build step; its size budget is enforced by a test.
