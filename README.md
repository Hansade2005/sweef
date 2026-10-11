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

Same 11-step agent flow (open, snapshot, click, fill, submit, React counter, eval) on a local fixture site, median of 5 runs, Chromium 1194 for agent-browser 0.27. Reproduce with `bench/run.sh`.

| step | Sweef | agent-browser | ratio |
|---|---|---|---|
| open page | 115 ms | 765 ms | 6.7x |
| snapshot, interactive only | 10 ms | 277 ms | 27.7x |
| click (navigates) | 46 ms | 300 ms | 6.5x |
| snapshot, full | 10 ms | 254 ms | 25.4x |
| fill | 10 ms | 235 ms | 23.5x |
| press Enter (submits) | 36 ms | 243 ms | 6.8x |
| get title | 9 ms | 257 ms | 28.6x |
| open React app | 79 ms | 285 ms | 3.6x |
| click in React | 13 ms | 268 ms | 20.6x |
| eval | 11 ms | 246 ms | 22.4x |
| whole flow | 404 ms | 3523 ms | 8.7x |
| daemon RSS | 25 MB | 1153 MB | 46x less |

Commands that do not navigate are 20 to 29 times faster; navigations are 4 to 7 times faster and bounded by fetching and running the page's scripts. Snapshot sizes are equal or smaller (466 vs 480 bytes on the landing page, 68 vs 137 on the React app).

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

Every verb agent-browser has for driving a page, at a fraction of the latency: `open`, `snapshot [-i] [-c] [-d n] [-s sel] [--delta]`, `click`, `dblclick`, `fill`, `type`, `press`, `hover`, `focus`, `check`, `uncheck`, `select`, `scroll`, `drag`, `get text|html|value|attr|title|url|count|box`, `is visible|enabled|checked`, `wait <sel|ms> | --text | --url | --fn | --load`, `eval`, `console`, `network`, `back`, `forward`, `reload`, `tab`, `screenshot`, `find role|text|label|placeholder|alt|testid … click|fill|text`, `set viewport|offline|headers|media|geo|ua`, `cookies`, `storage`, `state save|load`, `dialog`, `close`. Refs are accepted as `@e3`, `e3` or `ref=e3`. `--session <name>` isolates sessions, `--json` gives machine output. The daemon exits after an hour idle (`SWEEF_IDLE_TIMEOUT`).

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

The prelude is compiled to QuickJS bytecode once per process, so a new document gets a full browser environment in about 19 ms. What this means in practice: server-rendered sites, documentation, forms, dashboards and React, Preact or jQuery apps that do not depend on layout measurement run. example.com renders its script-injected paragraphs, react.dev renders its navigation and content, GitHub login and Wikipedia snapshot cleanly. Pages that need `getBoundingClientRect` to be real, WebGL, canvas painting, video or Web Workers do not, and Sweef says so in the console log rather than pretending.

## Feature map

Three states: **works** (covered by tests), **partial** (works for the common case, gaps noted), **not yet**.

| Capability | State |
|---|---|
| Locators: css (full selector engine incl. `:has`, `:nth-child`, attribute operators), `text=`, `role=`, `label=`, `placeholder=`, `alt=`, `testid=`, refs, `.nth/.first/.last/.filter` | works |
| Accessibility snapshot with content, states, refs, scoping and truncation | works |
| Actions: click, fill, type, press, check, uncheck, selectOption, hover, focus, with events delivered to page listeners and `preventDefault` honoured | works |
| JavaScript: ES2023, DOM API, events, timers, fetch/XHR, storage, modules, dynamic scripts, eval timeout | works |
| Forms: GET and POST submission, cookies, redirects | works |
| Console, network and DOM-mutation logs (bounded rings), HAR export | works |
| Cookies (RFC 6265 jar per context), storage state save/load | works |
| Multiple isolated contexts, tabs, history | works |
| Emulation: viewport, UA, offline, color scheme, extra headers, geolocation, permissions | works |
| Auto-waiting: attached / visible / hidden / enabled, with the JS event loop pumped while waiting | works |
| Routes: fulfill and abort by glob for navigations and page fetches | partial (no request body inspection, no continue-with-modifications) |
| Screenshots: PNG is a text rendering with a bitmap font, not a visual replica | partial |
| Mouse, drag, wheel, tap: events are dispatched; coordinates map to elements through a deterministic pseudo-layout, not real geometry | partial |
| Dialogs: alert / confirm / prompt answered from an armed response | works |
| Init scripts | works |
| Visibility: attribute and inline-style based; no stylesheet cascade | partial |
| `getBoundingClientRect`, `offsetWidth`: deterministic non-zero boxes, not layout | partial |
| Exposed bindings, downloads, PDF export, WebSocket, Web Workers, networkidle | not yet |
| Pixel layout, WebGL, canvas painting, media, extensions | not planned |

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

- Real network idle detection and request-body-aware routes
- Box model good enough for `getBoundingClientRect` consumers (dropdown libraries, virtual lists)
- Downloads to blobs and `sweef watch` live event stream
- Python SDK
- Playwright trace format subset for the trace viewer

## License

MIT (TBD: confirm before public release).
