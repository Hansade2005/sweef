# Sweef Browser

**A lightweight, agent-first browser engine — Chromium's feature surface for agentic automation, in a single Go binary that runs on 1 core / 50 MB RAM / 400 MB disk.**

Sweef is not a fork of Chromium. It is an independent browser engine written from scratch in Go, purpose-built so **AI agents are first-class citizens** and humans get a tiny, fast, scriptable browsing tool. Everything Playwright exposes to drive Chromium — locators, auto-waiting, actions, dialogs, network interception, storage state, multi-context isolation, accessibility snapshots, console/network logs — Sweef offers natively, over its own compact wire protocol and MCP server, at a fraction of the memory footprint.

## Why

Driving real Chromium through Playwright/CDP costs ~90–150 MB RSS *per browser*, needs a 200+ MB binary install, and gives agents token-heavy DOM dumps. That is unusable on small VPSes, edge boxes, Raspberry Pi Zero-class hardware, or when you want to run hundreds of parallel agent sessions.

Sweef's bet: agents don't need pixel-perfect rendering. They need **semantics** — structure, roles, names, text, form state, network truth — plus reliable actions. So Sweef implements a full HTML5-compliant-ish DOM, CSS selector engine, ARIA tree, JS execution, and HTTP stack, while replacing the compositor with cheap structured renders and PNG screenshots.

| | Playwright + Chromium | Sweef |
|---|---|---|
| Install size | ~250 MB (browser download) | **~8 MB single static binary** |
| Idle RSS | ~90–150 MB | **~8–15 MB** |
| CPU cores wanted | 2–4 | **1** |
| Agent interface | CDP + language bindings | **MCP native + RPC + SDKs** |
| Page content for LLM | DOM dumps / custom snapshot | **Playwright-format AriaSnapshot YAML with `ref=eN` handles** |
| JS engine | V8 (~8 MB+ heap/tab) | QuickJS (cgo build) or pure-Go DOM bridge |

## Goals

1. **Runs anywhere** — one portable Go binary; x86-64, ARM, RISC-V; cross-compiles with `GOOS/GOARCH`; no CGO required for the default build.
2. **Agent-native** — ships an MCP server (`sweef mcp`) so any LLM tool-runner can browse immediately; every page interaction returns bounded, token-cheap results.
3. **Playwright parity for automation** — locators with all engine prefixes (`text=`, `role=`, `css=`, `label=`, `placeholder=`, `alt=`, `testid=`), auto-waiting, actionability checks, dialogs, downloads-as-blobs, routes/interception, HAR-style network logs, storage-state save/load, multiple isolated contexts, device emulation (viewport, `deviceScaleFactor`, UA, mobile/landscape, geolocation, permissions, offline, color scheme, extra headers), init scripts, exposed bindings.
4. **Human-usable too** — CLI for fetch/snapshot/screenshot/serve, Node & Python SDKs mirroring the Playwright API shape.
5. **Bounded by design** — ring buffers for console/network/DOM-mutation events, capped cookie jar, RSS governor (`--max-rss-mib`) with GC pressure valve. No unbounded growth, ever.

## Non-goals

Pixel-perfect layout, GPU compositing, WebGL/WebAssembly, extensions, media codecs. If you need those, use Chromium. Sweef optimizes for *understanding and acting on the web programmatically*.

## Architecture

```
cmd/sweef              CLI (serve | fetch | snapshot | shot | mcp | call | version)
internal/engine        Browser, BrowserContext, Tab, Locator, actions, waits,
                       dialogs, routes, storage_state, PNG screenshot renderer,
                       metrics governor, event rings
internal/net           HTML parser, DOM tree, CSS selector engine, locator
                       engines, ARIA accessible-name computation,
                       Playwright-format AriaSnapshot YAML, route matching,
                       RFC-6265 cookie jar
internal/js            JS runtime abstraction:
                         -tags quickjs  -> embedded QuickJS (ES2020, cgo)
                         default        -> pure-Go DOM bridge evaluator
                                           (document.*, querySelector,
                                            console.log, numeric/string exprs)
internal/rpc           SWF1 wire protocol server (framed JSON over TCP/unix)
internal/mcp           Model Context Protocol server (stdio JSON-RPC, 50+ tools)
pkg/sdk/node           Node.js SDK (Playwright-shaped, speaks SWF1)
pkg/sdk/py             Python SDK (Playwright-shaped, speaks SWF1)
```

- **Wire protocol**: `SWF1` — 4-byte magic, length-prefixed JSON frames. Tiny, debuggable with `nc`, no HTTP overhead.
- **Concurrency**: one goroutine per tab, locks only inside a tab. Contexts isolate cookies/storage/emulation exactly like Playwright's browser contexts.

## Quick start

```bash
go build -o sweef ./cmd/sweef          # pure-Go build, no CGO needed
./sweef version

# human use
./sweef fetch https://example.com                 # extracted text
./sweef snapshot https://example.com --aria       # Playwright-format a11y YAML
./sweef shot https://example.com --width 800 --height 600 --dsf 2 --out page.png

# serve the RPC socket for SDKs
./sweef serve --addr unix:/tmp/sweef.sock
./sweef call --addr unix:/tmp/sweef.sock Browser.getVersion

# MCP for AI agents (add to Claude Desktop / any MCP host)
./sweef mcp
```

MCP config example:

```json
{ "mcpServers": { "sweef": { "command": "/usr/local/bin/sweef", "args": ["mcp"] } } }
```

### Agent loop, in three calls

```text
1. browser_navigate   {url}                 → load state + title
2. browser_aria_snapshot {}                 → YAML tree, every node has [ref=eN]
3. browser_click      {selector: "e12"}     → act on exactly that element
```

Refs are stable backend node ids; stale refs return a clear error telling the agent to re-snapshot. This is the same contract Playwright MCP ships, without needing Chromium.

## Feature map (vs Playwright)

| Capability | Status |
|---|---|
| Locators: css/text/role/label/placeholder/alt/testid engines, `.nth/.first/.last/.filter(hasText)` | ✅ |
| Auto-waiting + actionability (visible/enabled/stable-by-dom), waitForSelector/Function/URL/LoadState | ✅ |
| Actions: click/dblclick/hover/fill/type/press/check/uncheck/selectOption/tap/dragTo/scroll/setInputFiles | ✅ |
| Keyboard & mouse (down/up/move/click/wheel) with hit-testing | ✅ |
| Accessibility: Playwright-format `ariaSnapshot()` YAML, accessible names, landmarks, states | ✅ |
| Screenshots: PNG with width/height/deviceScaleFactor, viewport defaults; text render | ✅ |
| PDF export of rendered page | ✅ |
| Console / network / DOM-mutation logs (bounded rings, since-SID queries) | ✅ |
| Network: routes with fulfill/abort, request interception, HAR-style log, APIRequestContext equivalent (context.Fetch) | ✅ |
| Cookies: RFC-6265 jar per context, add/clear, Set-Cookie capture | ✅ |
| Storage state: save/load Playwright-compatible JSON (cookies + localStorage) | ✅ |
| Multi-context isolation, tabs, history (back/forward/reload) | ✅ |
| Emulation: viewport+dsf+mobile/landscape, UA, geolocation, permissions, offline, color scheme, extra headers, HTTP basic auth, proxy options | ✅ |
| Dialogs (alert/confirm/prompt/beforeunload), arm accept/dismiss | ✅ |
| Init scripts (run before page scripts, every nav), exposed bindings | ✅ |
| JS eval: QuickJS ES2020 under `-tags quickjs`; pure-Go DOM-bridge subset otherwise | ✅ |
| MCP server + Node/Python SDKs + CLI | ✅ |
| Pixel layout / WebGL / extensions / media | ❌ (non-goals) |

## Building

```bash
go build ./...                      # default: pure Go, fully portable
go test ./...
GOOS=linux GOARCH=arm go build -o sweef-arm ./cmd/sweef   # e.g. Pi Zero
go build -tags quickjs -o sweef-js ./cmd/sweef            # real ES2020 via QuickJS (needs cgo + vendored quickjs)
```

Requires Go ≥ 1.22. The default binary has **zero runtime dependencies**.

## Roadmap

- [ ] Real layout box model (block/inline flow) for accurate bounding boxes
- [ ] WebSocket + SSE frame logging in the network ring
- [ ] Downloads with blob persistence
- [ ] `sweef watch` — live event stream CLI for humans/debugging
- [ ] WASM target for in-browser agent demos
- [ ] Video-free trace viewer compatible with Playwright's trace format subset

## License

MIT (TBD — confirm before public release).
