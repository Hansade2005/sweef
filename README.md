# Sweef Browser

**The agent browser that costs nothing to run.** Sweef is an independent browser engine written in Go: one static binary, no Chromium, a real ES2023 JavaScript engine, and a Model Context Protocol server built in. It gives AI agents a compact, readable view of a page plus reliable actions, on about 15 MB of RAM.

Sweef is not a fork of Chromium and does not do pixel layout. It bets that agents need **semantics** (structure, roles, names, text, form state, network truth) more than they need a rasterised screen, and it spends its budget there.

## What you get

| | Playwright + Chromium | Sweef |
|---|---|---|
| Install | ~250 MB browser download | **11 MB static binary** |
| Memory | ~90 to 150 MB idle, 1.1 GB process tree under agent-browser | **13 MB idle, 22 MB typical page, 52 MB peak on a script-heavy Wikipedia article** |
| CPU | 2 to 4 cores wanted | **1 core** |
| Agent interface | CDP + language bindings | **MCP native**, plus a framed JSON-RPC socket and a Node SDK |
| Page view for the model | DOM dumps or custom snapshots | **Playwright-format accessibility tree** with small `[ref=eN]` handles |
| JavaScript | V8 | **QuickJS (ES2023)** via [modernc.org/quickjs](https://pkg.go.dev/modernc.org/quickjs), pure Go, no CGO |
| Pixels, WebGL, media | Yes | No, by design |

## Benchmark against Vercel agent-browser

Same 11-step agent flow (open a landing page, snapshot, click through to a login form, fill it, submit, open a React app, click in it, eval, snapshot) on a local fixture site. Median of 7 runs, a fresh daemon for every run, agent-browser 0.27 on Chromium 1194. Reproduce with `bench/run.sh`.

| step | Sweef | agent-browser | ratio |
|---|---|---|---|
| open page (cold daemon) | 20 ms | 560 ms | 28.0x |
| snapshot, interactive only | 7 ms | 236 ms | 33.7x |
| click that navigates | 7 ms | 253 ms | 36.1x |
| snapshot, full | 6 ms | 221 ms | 36.8x |
| fill | 5 ms | 220 ms | 44.0x |
| press Enter that submits | 6 ms | 229 ms | 38.2x |
| get title | 5 ms | 218 ms | 43.6x |
| open React 18 app | 22 ms | 255 ms | 11.6x |
| click in React | 8 ms | 226 ms | 28.2x |
| eval | 6 ms | 219 ms | 36.5x |
| whole flow, one command at a time | 121 ms | 2904 ms | 24.0x |
| **whole flow as one `sweef batch`** | **56 ms** | 2904 ms | **51.9x** |
| daemon memory | 26 MB | 1149 MB | 45x less |

Every step is over 20x except opening the React app, which executes 140 KB of React in a pure-Go JavaScript engine (about 10 ms) plus the process spawn every CLI call pays. Snapshots are equal or smaller (466 vs 480 bytes on the landing page, 68 vs 137 on the React app).

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
| Geometry: one flow layout drives element boxes, `getBoundingClientRect`, coordinate clicks and screenshots, so they always agree. Block and inline flow, wrapping, display from CSS, flex rows, table rows, heading sizes, control sizes | partial (no CSS box model: margins, padding, positioning and grid are approximated) |
| Screenshots: a schematic PNG drawn from that layout, viewport or full page | partial (not a pixel replica) |
| Pixel-accurate rendering, WebGL, canvas painting, media playback, extensions | not planned |

## Architecture

```
cmd/sweef              CLI (mcp | serve | fetch | snapshot | shot | eval | call | version)
internal/engine        Browser, BrowserContext, Tab, Locator, actions, waits,
                       dialogs, routes, storage state, PNG renderer, RSS governor,
                       jsvm.go (QuickJS VM + host bridge), scripts.go (loader + lifecycle),
                       dom_prelude.js (browser API in JavaScript), jsbridge.md (contract)
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
