# Sweef Browser

**The agent browser that costs nothing to run.** Sweef is an independent browser engine written in Go: one static binary, no Chromium, a real ES2023 JavaScript engine, and a Model Context Protocol server built in. It gives AI agents a compact, readable view of a page plus reliable actions, on about 15 MB of RAM.

Sweef is not a fork of Chromium. It has its own CSS layout engine, rasterizer, software WebGL and media stack, all measured against Chromium (see [Rendering fidelity](#rendering-fidelity)). It bets that agents need **semantics** (structure, roles, names, text, form state, network truth) more than they need a rasterised screen, and it spends its budget there.

## What you get

| | Playwright + Chromium | Sweef |
|---|---|---|
| Install | ~250 MB browser download | **21 MB static binary** |
| Memory | ~90 to 150 MB idle, 1.1 GB process tree under agent-browser | **13 MB idle, 22 MB typical page, 52 MB peak on a script-heavy Wikipedia article** |
| CPU | 2 to 4 cores wanted | **1 core** |
| Agent interface | CDP + language bindings | **MCP native**, plus a framed JSON-RPC socket and a Node SDK |
| Page view for the model | DOM dumps or custom snapshots | **Playwright-format accessibility tree** with small `[ref=eN]` handles |
| JavaScript | V8 | **QuickJS (ES2023)** via [modernc.org/quickjs](https://pkg.go.dev/modernc.org/quickjs), pure Go, no CGO |
| Pixels, WebGL, media | Yes | **Yes**, pure Go: CSS layout within 1 px of Chromium on 99.5% of boxes, software WebGL 1/2, Canvas 2D, media and Web Audio |

## Benchmark against Vercel agent-browser

Same 11-step agent flow (open a landing page, snapshot, click through to a login form, fill it, submit, open a React app, click in it, eval, snapshot) on a local fixture site. Median of 7 runs, a fresh daemon for every run, agent-browser 0.27 on Chromium 1194. Reproduce with `bench/run.sh`.

| step | Sweef | agent-browser | ratio |
|---|---|---|---|
| open page (cold daemon) | 25 ms | 594 ms | 23.8x |
| snapshot, interactive only | 9 ms | 240 ms | 26.7x |
| click that navigates | 10 ms | 259 ms | 25.9x |
| snapshot, full | 10 ms | 234 ms | 23.4x |
| fill | 8 ms | 227 ms | 28.4x |
| press Enter that submits | 9 ms | 234 ms | 26.0x |
| get title | 8 ms | 225 ms | 28.1x |
| open React 18 app | 26 ms | 259 ms | 10.0x |
| click in React | 13 ms | 229 ms | 17.6x |
| eval | 9 ms | 227 ms | 25.2x |
| whole flow, one command at a time | 162 ms | 3013 ms | 18.6x |
| **whole flow as one `sweef batch`** | **99 ms** | 3013 ms | **30.4x** |
| daemon memory | 38 MB | 1155 MB | 30x less |

Plain steps run 23x to 28x faster. The two React steps are slower (10x to open, 18x to click) because opening the app executes 140 KB of React in a pure-Go JavaScript engine (about 10 ms) and every CLI call pays a process spawn of about 6 ms. Command by command the whole flow is about 18 to 20x, and as one batch about 30 to 37x (run to run variation on this machine). Snapshots are equal or smaller (466 vs 480 bytes on the landing page, 68 vs 137 on the React app).

What makes navigation fast: pages with no scripts get no JavaScript VM at all; pages with scripts take a VM from a pre-warmed pool; framework bundles are compiled to bytecode once and the bytecode is kept on disk across daemon restarts; sub-resources go through an HTTP cache with ETag revalidation; scripts and stylesheets are prefetched in parallel; the previous page is torn down in the background.

## Batching: one call, many steps, one observation

Agents pay per tool call, and a model round trip costs seconds while a Sweef command costs milliseconds. Send the whole plan at once:

```bash
sweef batch "open https://acme.test/login" "fill label=Email ada@example.com" "check 'label=Remember me'" \
            "click 'text=Sign in'" "expect url /dashboard" "get text h1" --observe delta
```

```text
✓ open https://acme.test/login (4ms)  → Sign in
✓ fill label=Email ada@example.com (0ms)
✓ check label=Remember me (0ms)
✓ click text=Sign in (3ms)
✓ expect url /dashboard (0ms)  → {"passed":true}
✓ get text h1 (0ms)  → Welcome back, Ada
6/6 steps in 8ms  Dashboard  https://acme.test/dashboard
+ - heading "Welcome back, Ada" [level=1 ref=e7]
...
```

- **Steps** use the CLI verbs (open, click, fill, type, press, hover, check, select, scroll, get, is, wait, eval, back, forward, reload, snapshot, sleep, download, pdf) or any raw RPC method.
- **expect** asserts with auto-wait: `text`, `url`, `title`, `visible`, `hidden`, `count`, `value`.
- **Failure handling**: the batch stops at the first failing step and reports which one; `--continue` keeps going, and a step marked optional never stops it.
- **Observation**: the batch ends with one view of the page: `interactive`, `full`, `delta` (only the lines that changed since your last snapshot) or `text`.
- The same feature is the `browser_batch` MCP tool and the `Batch.run` RPC method, and `sweef batch -` reads steps from stdin.

## Quick start

```bash
go build -o sweef ./cmd/sweef          # pure Go; cross-compiles with GOOS/GOARCH
./sweef version

# session CLI (a daemon keeps the page alive between calls)
./sweef open https://example.com
./sweef snapshot -i                    # controls and headings with [ref=eN]
./sweef click @e1
./sweef get title
./sweef close

# one-shot
./sweef fetch https://example.com                 # visible text
./sweef snapshot https://example.com --aria       # accessibility tree, YAML
./sweef eval https://example.com 'document.querySelectorAll("p").length'
./sweef shot https://example.com --out page.png   # text-rendered PNG

./sweef mcp                                       # MCP server on stdio
./sweef serve --addr 127.0.0.1:9222               # socket server for SDKs
```

### Session CLI

Every verb agent-browser has for driving a page, at a fraction of the latency: `open`, `snapshot [-i] [-c] [-d n] [-s sel] [--delta]`, `click`, `dblclick`, `fill`, `type`, `press`, `hover`, `focus`, `check`, `uncheck`, `select`, `scroll`, `drag`, `get text|html|value|attr|title|url|count|box`, `is visible|enabled|checked`, `wait <sel|ms> | --text | --url | --fn | --load`, `eval`, `console`, `network`, `back`, `forward`, `reload`, `tab`, `screenshot [--full]`, `pdf [path]`, `downloads`, `download <url>`, `route <glob> --json v | --abort | --continue …`, `unroute`, `bind <name> [reply]`, `bindings`, `batch`, `find role|text|label|placeholder|alt|testid … click|fill|text`, `set viewport|offline|headers|media|geo|ua`, `cookies`, `storage`, `state save|load`, `dialog`, `close`. Refs are accepted as `@e3`, `e3` or `ref=e3`. `--session <name>` isolates sessions, `--json` gives machine output. The daemon exits after an hour idle (`SWEEF_IDLE_TIMEOUT`).

MCP configuration for any MCP host:

```json
{ "mcpServers": { "sweef": { "command": "/usr/local/bin/sweef", "args": ["mcp"] } } }
```

### The agent loop

```text
1. browser_navigate      {url}                       → title, final URL
2. browser_aria_snapshot {}                          → YAML tree, controls carry [ref=eN]
3. browser_click         {selector: "e12"}           → acts on exactly that element
4. browser_aria_snapshot {interactiveOnly: true}     → re-read, cheaply
```

A snapshot of the GitHub login page looks like this:

```yaml
- link "Skip to content" [ref=e1]:
  - /url: "#start-of-content"
- main:
  - heading "Sign in to GitHub" [level=1 ref=e2]
  - form:
    - text: Username or email address
    - textbox "Username or email address" [required ref=e3]
    - text: Password
    - textbox "Password" [required type=password ref=e4]
    - link "Forgot password?" [ref=e5]:
      - /url: /password_reset
    - button "Sign in" [ref=e6]
```

Refs are small integers from a per-document table, so they stay valid while scripts mutate the page. A ref from a previous page returns `ref e12 is from a previous page; take a new snapshot`. Paragraphs and plain text carry no ref, which is where most of the token saving comes from. Snapshot options: `selector` to scope, `interactiveOnly` for controls and headings only, `maxChars` (default 60 000, truncated with a note), `maxTextLen`, `maxDepth`.

## How JavaScript works

Each tab owns a QuickJS VM with a 64 MB heap cap and a 5 s per-script timeout. The Go DOM is the source of truth; a prelude implements `window`, `document`, elements, events, timers, `fetch`, `XMLHttpRequest`, `localStorage`, `MutationObserver` and the rest of the browser API on top of a small host bridge (see `internal/engine/jsbridge.md`). Scripts load the way browsers load them: inline and `src` scripts in order, `async` and `defer` after, `type=module` with relative imports, scripts inserted by page code (webpack chunks, tag managers) when they connect. After `load`, the event loop pumps until the page is idle or the settle budget (`--settle-ms`, default 1500) is spent, and again briefly after every action, so a click that triggers a fetch and a re-render is reflected in the next snapshot.

VMs come from a pre-warmed pool, and the prelude and large scripts are cached as bytecode in memory and on disk, so a React page installs in about 10 ms. WebSockets are real connections, and Web Workers run in their own VM. What this means in practice: server-rendered sites, documentation, forms, dashboards, and React, Preact or jQuery apps run. example.com renders its script-injected paragraphs, react.dev renders its navigation and content, GitHub login and Wikipedia snapshot cleanly. Pages that need exact pixel geometry, WebGL, canvas painting or media playback do not, and Sweef reports script errors in the console log rather than pretending.

## Feature map

Three states: **works** (covered by tests), **partial** (works for the common case, gaps noted), **not planned**.

| Capability | State |
|---|---|
| Locators: css (full selector engine incl. `:has`, `:nth-child`, attribute operators), `text=`, `role=`, `label=` (incl. wrapping labels), `placeholder=`, `alt=`, `testid=`, refs, `.nth/.first/.last/.filter` | works |
| Accessibility snapshot with content, states, refs, scoping, truncation, `--delta` | works |
| Batching: many steps per call with expectations and a final observation (CLI, MCP, RPC) | works |
| Actions: click, fill, type, press, check, uncheck, selectOption, hover, focus, drag, scroll, tap, with events delivered to page listeners and `preventDefault` honoured | works |
| JavaScript: ES2023, DOM API, events, timers, fetch/XHR, storage, modules, dynamic scripts, eval timeout | works |
| Web Workers on their own VM: URL, blob: and data: workers, `importScripts`, fetch, timers | works |
| WebSockets: text and binary frames, cookies sent, frames recorded in the network log | works |
| Visibility from the CSS cascade: inline and linked stylesheets, `@media`, specificity, `!important`, inherited `visibility` | works |
| Forms: GET and POST submission, cookies, redirects | works |
| Routes: glob or `/regex/`, method and body matching; fulfill, abort, or continue with URL, method, body and header rewrites; `times` | works |
| Downloads: attachments, `<a download>`, `blob:` and `data:` URLs, saved to a downloads directory | works |
| PDF export: headings, text, lists, tables, clickable links, pagination, page formats | works |
| Exposed bindings: page calls recorded, JSON replies returned to the page | works |
| Wait states: attached, visible, hidden, enabled, load, `networkidle` | works |
| Console, network (incl. POST bodies) and DOM-mutation logs (bounded rings), HAR export | works |
| Cookies (RFC 6265 jar per context), storage state save/load | works |
| Multiple isolated contexts, tabs, history | works |
| Emulation: viewport, UA, offline, color scheme, extra headers, geolocation, permissions | works |
| Dialogs: alert, confirm, prompt answered from an armed response | works |
| Init scripts | works |
| Geometry: a real CSS engine (cascade, box model, block, inline, float, flex, grid, table, multicol, positioning, transforms) drives element boxes, `getBoundingClientRect`, `getClientRects`, offset/client/scroll metrics, hit-testing, coordinate clicks and screenshots, so they always agree | works (see fidelity below) |
| Screenshots: pixels from the real rasterizer (anti-aliased paths, real fonts, gradients, shadows, filters, blend modes, canvas, WebGL and video frames), viewport or full page | works (text is unhinted, so glyph edges differ slightly from Chromium) |
| Canvas 2D: paths, text, gradients, patterns, filters, shadows, compositing, `ImageData`, `OffscreenCanvas`, `toDataURL` | works |
| WebGL 1 and 2: GLSL ES 1.00 and 3.00 compiler, full state machine, MSAA, transform feedback, UBOs, instancing; three.js, PixiJS and regl render correctly | works |
| Media: `<video>` and `<audio>` element behavior, MSE, text tracks, Web Audio (all node types, worklets, offline rendering) | works (codec limits below) |
| Browser extensions | not planned |

## Rendering fidelity

Measured against Chromium 1194 on this repo's fixtures (`go run ./tools/fidelity`, `testdata/*`). Layout numbers are for 1280x800.

| Area | Result |
|---|---|
| Element boxes, 84 pages (basic CSS through shop, dashboard, email, docs, Bootstrap, container queries, subgrid, writing modes, multicol, clip-path, mask, SVG, bidi) at 1280x800 | mean IoU 0.996, 97.7% of boxes within 1 px, 98.9% within 4 px |
| Same pages at 390x844 and 1920x1080 | mean IoU 0.998 at both |
| Pixels (ink agreement with Chromium screenshots) | 98.5% overall; blog 99.6%, landing 99.9%, Arabic and bidi pages 93 to 96% |
| Glyph pixels, 1134 glyphs (9 faces x 14 sizes, 9 to 100 px) | 0.33% of pixels differ by more than 8/255, none by more than 24/255 |
| Page painter, 8 display-list fixtures | 96% to 100% of pixels within 8/255 (text case 99.9%) |
| Canvas 2D, 42 fixtures | shapes 99.4%, text 99.7% within 8/255 |
| SVG (paths, gradients, clips, masks, filters incl. turbulence and lighting, markers, text, `textPath`, `foreignObject`) | feature pages 98.9% to 99.8% ink |
| CSS `clip-path`, `mask`, multicol fragments | 98% to 99.9% ink |
| Transformed elements at fractional offsets, `content-visibility: auto`, `position: fixed` containing blocks, `foreignObject` contexts | boxes IoU 1.000, ink 97.9% to 100% on 5 fixtures |
| CSS 3D transforms (`rotateX/Y/Z`, `rotate3d`, `translateZ`, `scale3d`, `matrix3d`, `perspective()`, individual `translate`/`rotate`/`scale` with Z or an axis, `perspective`, `perspective-origin`, `transform-style: preserve-3d` with BSP depth sorting, `backface-visibility`, `transform-origin` z, `transform-box`), 9 fixtures incl. cube, carousel and flip card | boxes IoU 1.000 and 100% within 1 px (incl. behind-the-camera mirroring), ink 94.5% to 99.9% (mean 98.7%); hit-testing through the inverse projective map matches `elementFromPoint` except on edge pixels |
| WebGL, 76 fixtures | mean 99.9% of pixels within 4/255 (three.js 99.9%, PixiJS 99.99%, regl 100%) |
| Web Audio, 26 offline graphs | sample-exact for filters, delay, convolver and panner; oscillators within 6e-4 |
| Media elements, 21 scenarios | event order identical to Chromium; `canPlayType` matches in 67 of 67 recorded answers |

A 1280x800 text page paints in about 21 ms. A 100k-triangle WebGL frame at 512x512 takes about 170 ms on 4 cores.

What is still different, honestly:

- **Text.** Glyph edges match Chromium to within 12/255 at 9 to 32 px, including rotated, skewed and scaled text and glyphs with cubic outlines (CFF fonts such as Inter: 0.34% of pixels differ by more than 8/255). SVG text under skew, matrix and non-uniform scale transforms, per-glyph `rotate`, `textPath` and vertical writing mode match to within 0.3% to 2.6%. Not matched: dashed text strokes, rotated stroked text and rotated text decorations, `rotate=` in vertical SVG text, and Arabic diacritics, Hebrew points and Indic scripts.
- **3D transforms.** Layers are rasterized in their own plane and drawn through the projective map with bilinear sampling; the raster scale rule for perspective layers (1x, or the next whole number once the projection grows a layer by more than about 1.27x) was fitted to Chromium measurements, not derived from its source. Where two layers are exactly coplanar (a `preserve-3d` box and its flipped back face) the hit-test winner can differ from Chromium's. `overflow: hidden` on a preserve-3d child with 3D-transformed content shows a clipped sliver in Chromium that Sweef does not reproduce. SVG elements do not take part in 3D contexts, and `getClientRects` returns the projected fragments.
- **CSS not done:** `content-visibility: auto` reopening on scroll, find-in-page and anchors (a static load skips and opens exactly like Chromium), `@container style()` queries, subgrid line names, `mask-border`, `clip-path` and `mask` on plain inline elements, `box-decoration-break: clone` on wrapped inlines, vertical text with RTL or nested orthogonal flows.
- **SVG not done:** CSS `mask` image layers on SVG elements, `kernelUnitLength`, and a few filter inputs (BackgroundImage, FillPaint).
- **Codecs.** Everything below decodes in pure Go, with no ffmpeg. Audio: MP3, Vorbis, FLAC, PCM, Opus (SILK, CELT, hybrid; Ogg, WebM, MP4), AAC-LC and HE-AAC v1/v2 (SBR and PS; checked against real MPEG conformance streams at 72 to 78 dB) in ADTS and MP4, and USAC / xHE-AAC in MP4 (frequency-domain and LPD cores, eSBR with the harmonic transposer, PVC and inter-TES, MPEG Surround 2-1-2 upmix; checked against the real conformance streams at 61 to 89 dB and against libxaac). Video: VP8, VP9 (profiles 0 to 3, 8 to 12 bit, 4:2:0/4:2:2/4:4:0/4:4:4, intra-only and scaled references), H.264 (Baseline, Main, High, High 10, 4:2:2, 4:4:4, 9 to 14 bit, interlaced PAFF and MBAFF, FMO, data partitioning, long-term references, lossless), HEVC (Main, Main 10, Main Still Picture and the range extensions in 4:0:0 to 4:4:4 at 8 to 12 bit, tiles, wavefront, SAO) and AV1 (8, 10 and 12 bit, all common tools, film grain, superres, Annex B, layers and operating points, large-scale tile), bit-exact with ffmpeg, libdav1d and libaom on the test corpus, at 20 to 250 fps at 640x360 on one core. Frames are decoded on demand from the nearest preceding sync sample, so seeking and `drawImage(video)` work without ffmpeg (`SWEEF_NO_FFMPEG=1` proves it). Not decoded natively, so they use ffmpeg when installed: H.264 SP/SI slices and CABAC data partitioning, HEVC `extended_precision`, SCC and multilayer streams, AV1 non-uniform tile spacing in large-scale-tile mode, and USAC streams using time-warped MDCT, MPS212 residual coding, phase coding, temporal shaping or transient steering. USAC and HE-AAC multichannel layouts beyond 5.1 are unverified.
- **WebGL.** No compressed textures, `EXT_sRGB` or multi-draw; lines are 1 px; `sin` differs from SwiftShader in the last digit.
- **Canvas.** `getImageData` on a 1280x720 canvas takes about 0.4 s, because QuickJS cannot share typed arrays with Go. `MediaRecorder`, EME and picture-in-picture reject.
- **Exact pixel identity with Chromium is not a goal.** Screenshots are not byte-identical.

Set `SWEEF_RENDERER=flow` to fall back to the old schematic layout.

## Architecture

```
cmd/sweef              CLI (mcp | serve | fetch | snapshot | shot | eval | call | version)
internal/engine        Browser, BrowserContext, Tab, Locator, actions, waits,
                       dialogs, routes, storage state, PNG renderer, RSS governor,
                       jsvm.go (QuickJS VM + host bridge), scripts.go (loader + lifecycle),
                       dom_prelude.js (browser API in JavaScript), jsbridge.md (contract)
internal/style         CSS parsing, cascade, computed values, UA stylesheet
internal/render        CSS layout engine and display list (block, inline, flex, grid, table, ...)
internal/raster        Rasterizer, Canvas 2D context, page painter
internal/font          Font loading, metrics, shaping-lite, glyph outlines
internal/gl            Software WebGL 1/2 (state machine, rasterizer) + internal/gl/glsl compiler
internal/media         Container parsing, audio decode, frames; media/webaudio DSP graph
internal/net           HTML parser, DOM tree with stable ids, CSS selector engine,
                       locator engines, accessible names, snapshot v2, routes, cookie jar
internal/rpc           SWF1 wire protocol server (framed JSON over TCP/unix)
internal/mcp           MCP server (stdio JSON-RPC, 40+ tools)
pkg/sdk/node           Node.js SDK (Playwright-shaped)
pkg/sdk/py             Python SDK (Playwright-shaped, stdlib only)
```

Concurrency: one VM per tab, serialised; the DOM is behind a tab lock; code that holds the DOM lock never calls into the VM.

Memory: ring buffers for events, 8 MiB cap per document, 64 MiB cap per VM, an RSS governor (`--max-rss-mib`) that trims rings under pressure.

## Building and testing

```bash
go build ./...                      # pure Go, no CGO
go test ./...                       # includes golden snapshots of real pages and
                                    # React / Preact / jQuery fixtures under a local server
GOOS=linux GOARCH=arm64 go build -o sweef-arm64 ./cmd/sweef
```

Requires Go 1.26 (the `go` toolchain downloads it automatically when `GOTOOLCHAIN=auto`). The binary has zero runtime dependencies. Measured on 2026-10-11: `go test ./...` runs golden snapshots of six real pages, the selector engine, ref lifecycle, and JavaScript fixtures (inline DOM build, script order, fetch and timers, preventDefault, React 18 counter with a controlled input, Preact, jQuery incl. ajax, dynamic script insertion, ES modules, localStorage, `location.href` navigation, infinite-loop interrupt).

## Roadmap

- CSS box model (margin, padding, width/height, positioning) in the shared layout, for libraries that measure elements
- `sweef watch` live event stream
- Playwright trace format subset for the trace viewer
- Faster first execution of large framework bundles

## License

MIT (TBD: confirm before public release).
