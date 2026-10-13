# Changelog

All notable changes to Sweef are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and versions follow [Semantic Versioning](https://semver.org/) (while we are `0.x` alpha, minor versions may break
things; the release notes say so when they do).

How to add an entry: see "Changelog" in [CONTRIBUTING.md](CONTRIBUTING.md). Maintainers cut a release by moving
`[Unreleased]` under a new version heading ([docs/releasing.md](docs/releasing.md)).

## [Unreleased]

### Added
- Apache-2.0 `LICENSE` and `NOTICE`.
- Contribution and project health files: `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, `SUPPORT.md`,
  `GOVERNANCE.md`, `CODEOWNERS`, issue forms, pull request template, `Makefile`, `.editorconfig`.
- Automation: CodeQL, `govulncheck`, dependency review, Dependabot, pull request title check, gofmt check.
- `docs/development.md` (package map and contracts), `docs/releasing.md`, and an ADR log in `docs/adr/`.
- `appbench -open-profile` times the key that opens the omnibox dropdown and the key that closes it separately and
  writes a CPU profile.
- WebRTC on pion (pure Go): `RTCPeerConnection`, data channels, transceivers, stats; local IPs hidden behind mDNS by
  default, no STUN unless the page configures it ([docs/webrtc.md](docs/webrtc.md)).
- WebAssembly JavaScript API on wazero (pure Go): `Module`, `Instance`, `Memory` (an ArrayBuffer over the live linear memory), `Table`, `Global`, `validate`/`compile`/`instantiate` and the streaming variants, in pages and Web Workers; no threads, `Tag` or `externref` ([docs/webassembly.md](docs/webassembly.md)).
- IndexedDB, `navigator.storage` and Cache Storage, persisted per origin under the profile's `storage` directory (append-only logs, response bodies on disk, quota, wiped by "Clear browsing data", memory-only in private windows); see docs/storage.md.
- Service workers: `navigator.serviceWorker`, the install/activate/fetch/message lifecycle, real interception of navigations, `fetch()`, XHR and sub-resource loads, registrations that survive a restart; see docs/service-workers.md.
- Web platform layer (see `docs/web-platform.md`): real Streams plus Compression/Decompression and TextEncoder/TextDecoder streams, XPath, URLPattern, the Navigation API, ElementInternals, View Transitions, the CSS Highlight and `@property` registries, `Array.fromAsync`, `scheduler`, the Origin Private File System and honest stand-ins for device, credential, payment and Picture-in-Picture APIs, all loaded lazily.

### Changed
- README shows the real browser UI (first-run setup with the real browser logos, Sidekick, Data Saver, new tab, omnibox), rendered by `uipreview`.
- The omnibox dropdown uses a lighter shadow (the `--sh-2` token plus a 1 px ring instead of `--sh-3`).

### Performance
- Opening the omnibox dropdown: rows and section headings are pooled and reused, and rows no longer get `id`/index
  attribute writes when they move (about 13.0 ms to 11.3 ms median in-process at 1x with history present; the shadow
  change takes roughly another 1 ms off).

### Security
- Updated `golang.org/x/net` from v0.17.0 to v0.61.0 (HTML parser denial-of-service and XSS-class advisories GO-2024-3333, GO-2025-3595, GO-2026-4440, GO-2026-5025 to 5030, HTTP/2 GO-2024-2687, GO-2026-4918, IDNA GO-2026-5026) and the Go toolchain to 1.26.9 (about 30 standard library advisories in `net/http`, `crypto/tls`, `crypto/x509`, `net/url`, ...). `govulncheck ./...` now reports no vulnerabilities; it runs weekly and on every PR.

### Removed
- A 32 MB `appbench` binary that had been committed by mistake (it stays in git history).

## [0.1.0-alpha] - 2026-10-13 (not yet tagged)

First public alpha of the desktop browser. Everything below is measured or marked as not verified in the README.

### Added
- **Desktop browser:** chrome (tabs, omnibox, bookmarks, history, downloads, find, settings, onboarding with real browser
  logos for import) written as HTML/JS and drawn by Sweef's own engine; native window shells for X11 (verified), Win32 and
  Cocoa (build-tested only); installers and packaging for Linux, macOS and Windows (Linux verified).
- **Sidekick agent:** Go-owned agent loop on the Anthropic Messages API (streaming, byte-stable cached prefix with rolling
  cache breakpoints, snapshot deltas, early tool dispatch, safety and confirmation gates). Bring your own key.
- **Data Saver:** exact socket byte counting, off/standard/strict levels, lazy images, size caps with early abort, media on
  play, background tab suspension, RFC 9111 HTTP cache with a 100 MB disk tier, day/week/month budgets with warnings and a
  hard stop.
- **Web fonts:** `@font-face` with WOFF, WOFF2, TTF/OTF and variable fonts, lazy `unicode-range`, CORS, disk cache,
  `document.fonts` / `FontFace`, data-saver gates, hardened decoders with fuzz tests.
- **Progressive page loads:** first frame after HTML plus render-blocking CSS, preload scanner, async fetch/XHR/images,
  module graph prefetch and bytecode cache.
- **Benchmarks:** `bench/vschrome` (same-method comparison with headed Chromium under Xvfb), `bench/datasaver`,
  `internal/app/cmd/appbench`.
- Earlier engine work: Go DOM, QuickJS host bindings, CSS layout and raster, Canvas 2D, software WebGL, media codecs,
  agent CLI and MCP server, batching, snapshot format v2.

### Performance
- Typing in the omnibox with the dropdown open: 4.6 ms p50 / 7.5 ms p99 at 1x (was 13.9 / 20.2 ms) via in-place dropdown
  patching, a cross-pass style cache, an SVG scene cache, exact damage rectangles and concurrent repaint bands.
- First frame on real pages 0.5 to 1.1 s (was 1.3 to 11.9 s).

### Fixed
- Omnibox inserted a typed space twice.
- Render panic on pages whose text starts with a forced break (go.dev).
- Quadratic DOM-collection behaviour (go.dev), module import resolution (MDN, GitHub), `template.innerHTML`, bare
  `addEventListener`, `about:blank`, detached `new Image().src`.

### Known limits
- Chromium is still ahead on JavaScript-heavy page loads (no JIT), on the first key that opens the omnibox dropdown, and
  on compatibility (no DRM, extensions, WebRTC or WebAssembly yet).
- Windows and macOS windows and installers have never been run; CI only builds and unit-tests them.
- Data Saver percentages were measured before web fonts landed and may move by a few points.

[Unreleased]: https://github.com/sweefbrowser/sweef/compare/v0.1.0-alpha...HEAD
[0.1.0-alpha]: https://github.com/sweefbrowser/sweef/commits/main  <!-- becomes the tag link once v0.1.0-alpha is cut -->
