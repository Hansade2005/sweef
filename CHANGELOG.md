# Changelog

All notable changes to Sweef are recorded here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and versions follow [Semantic Versioning](https://semver.org/) (while we are `0.x` alpha, minor versions may break
things; the release notes say so when they do).

How to add an entry: see "Changelog" in [CONTRIBUTING.md](CONTRIBUTING.md). Maintainers cut a release by moving
`[Unreleased]` under a new version heading ([docs/releasing.md](docs/releasing.md)).

## [Unreleased]

## [0.1.0-alpha] - 2026-10-14

### Security
- State is partitioned by top-level site: cookies, `localStorage`, IndexedDB, Cache Storage, the Origin Private File System, service
  worker registrations, `BroadcastChannel`/`SharedWorker`/Web Locks namespaces and the HTTP cache are keyed by (origin, top-level site)
  in a third-party context. Third-party cookies are neither sent nor stored unpartitioned; `Partitioned` and `SameSite` are honoured;
  the Storage Access API is wired to the policy ([docs/storage-partitioning.md](docs/storage-partitioning.md)).
- `document.cookie` no longer exposes `HttpOnly` cookies; `ClearCookies("")` clears all cookies; the Origin Private File System is no
  longer shared between a private window and the normal one.

### Added
- **A real `Intl`.** `NumberFormat`, `DateTimeFormat`, `PluralRules`, `RelativeTimeFormat`, `ListFormat`, `DisplayNames`,
  `DurationFormat`, `Collator` and `Segmenter` now run on CLDR 47 data for 73 locales with the IANA time zone database
  embedded, instead of an English-only shim; `toLocaleString`, `toLocaleDateString` and `localeCompare` use them. Compared with
  Node 22 case by case (`tools/intlgen`, `internal/engine/intl_test.go`); gaps are listed in
  [docs/web-platform.md](docs/web-platform.md). Adds about 1.2 MB to the binary.
- **`Intl` parity with Chromium.** Measured against Node 22 (ICU 77, CLDR 47) on seeded random matrices (`node tools/intlgen/matrix.js`,
  same seeds before and after): `NumberFormat.format` matches in 99.6% of cases (was 93.4%), `formatRange` 99.4% (was 24.8%),
  `formatToParts` 99.5% (was 75.7%); `DateTimeFormat.format` 98.3% (was 57.4%), `formatRange` / `formatRangeToParts` 98.1% (was 41.2% /
  36.1%), `formatToParts` 98.6% (was 57.3%), `resolvedOptions` 99.3% (was 79.6%); `Collator` 99.85% of comparisons (was 93.05%) and
  100% of resolved locales (was 80.5%). What changed: the date and time pattern of every locale now comes from a port of ICU's
  `DateTimePatternGenerator` (the glue between date and time, `dayPeriod`, hour cycles) and ranges from a port of `DateIntervalFormat`
  (`platformjs/intlpat.js`, `intlrange.js`); time zone names follow ICU's specific, generic, location and localized-GMT rules with
  metazones; the `japanese`, `roc`, `islamic` (all variants), `hebrew`, `indian`, `coptic`, `ethiopic` and `ethioaa` calendars join
  `buddhist` and `persian` (non-Gregorian calendars in the locales that ship their names: 98.1% of 4,000 cases, was 16.5%);
  numbering systems carry their own patterns and symbols; number ranges collapse shared currency symbols, units and compact text the
  way ICU does and use plural ranges; compact currency, percent and unit parts are split into the parts Chrome reports; collation
  follows ICU's root order for the Arabic, Hebrew, Indic, Thai, Lao, Khmer, Myanmar, Georgian, Armenian, Greek, Cyrillic, Hangul and
  kana scripts, reorders a locale's own script first, orders Chinese by pinyin (GB 2312) or stroke (Big5 level 1), and resolves to the
  locale ICU has collation data for. `tools/intlgen/golden.js` now writes about 2,900 cases from Node's output
  (`engine/intl_test.go`), `segcases.js` the segmentation cases of `internal/intl/segment`. The binary grows by 180,224 bytes
  (41,234,558 to 41,414,782). Still missing: dictionary word breaking for Thai, Lao, Khmer, Burmese, Chinese and Japanese (ICU's
  dictionaries are 93 KB to 1.6 MB gzipped, `tools/intlgen/icudict.js`), the `chinese` and `dangi` calendars, and non-English time zone
  city names ([docs/web-platform.md](docs/web-platform.md)).
- **Video-call and player platform APIs.** Fullscreen API with the app giving the page the window; Media Session wired to
  a now-playing control; `ManagedMediaSource`; Async Clipboard API with a per-site permission, copy/cut/paste events,
  `DataTransfer`, `FileList`; a real `WebSocket` client (binary, fragments, close codes, backpressure); native
  `btoa`/`atob`; faster IndexedDB cursors; page visibility; echo cancellation, noise suppression and automatic gain on
  the WebRTC send path; `MediaStreamTrackProcessor`, `MediaStreamTrackGenerator` and `VideoTrackGenerator`; WebRTC
  `MediaCapabilities`. The compatibility status of Google Meet, WhatsApp Web and YouTube, with what was and was not tried
  against the live sites, is in [docs/compat.md](docs/compat.md).
- **`MediaRecorder` and `BlobEvent`.** Record `getUserMedia` / `getDisplayMedia` streams, `canvas.captureStream()` and
  `MediaStreamAudioDestinationNode` streams to WebM (VP8, Opus) or Ogg Opus (RFC 7845), with `timeslice`, `pause()`,
  `resume()`, `requestData()`, `audioBitrateMode`, the specified state machine and `InvalidStateError` / `NotSupportedError` /
  `InvalidModificationError` behaviour, an honest `isTypeSupported()`, and a stop when every track ends. Encoding (a streaming
  resampler, an Opus mixer, VP8, WebM and Ogg writers) runs off the JS thread in the new `internal/mediarec`; the chunks
  concatenate into a file that plays in `<audio>` / `<video>` and decodes with the repo's demuxers. Voice-note flows such as
  WhatsApp Web's no longer hit `NotSupportedError` (the real site was not tried). No MP4, VP9 or H.264 recording; only the
  first video track is recorded ([docs/mediarecorder.md](docs/mediarecorder.md)).
- **Iframes render and take input.** Every `<iframe>` is now a child document with its own JavaScript VM, layout and
  paint, drawn into the parent at the iframe's box (clipped, with the transforms, opacity and radius of its ancestors),
  scrollable inside its box, repainted when it changes, and loaded lazily for `loading="lazy"`. `srcdoc`, nested frames,
  `sandbox` (scripts, same-origin, popups, forms, top navigation), `X-Frame-Options` and CSP `frame-ancestors` are
  supported. Pointer, wheel, keyboard and text input are routed to the frame under the pointer or holding focus, in the
  desktop browser and through the engine API (`MouseClick`, `Keyboard`, `WheelAt`). Not supported: access to another
  window's DOM beyond what same-origin proxies forward, process isolation, scrollbars inside frames. See
  [docs/frames-popups.md](docs/frames-popups.md).
- **`window.open` and popups.** `window.open` returns a `WindowProxy` (`opener`, `name`, `closed`, `close()`, `focus()`,
  `postMessage`, `location`, same-origin property access and calls); links and forms honour `target` (`_blank`, `_top`, `_parent`, frame and window
  names, `rel=opener` / `noopener`); windows open only during a user gesture (5 s in the engine, 2 s in the app, one
  popup per gesture) and a blocked popup is recorded (`Page.popups`) and shown as a toast; `window.close()` closes windows
  that script opened; `postMessage` works across frames, the opener and popups with `targetOrigin` and structured clone
  of common types; `Cross-Origin-Opener-Policy` can sever the opener. In the desktop browser a popup is a tab next to
  its opener with an accent bar in the tab strip. An OAuth round trip (popup, cross-origin redirect back, `opener.postMessage`,
  `window.close`) is covered by tests against local servers; real Google or Facebook sign-in was not tried. Not supported:
  small-window features (size, position).
- Agent: the snapshot includes the content of frames with frame-qualified refs (`f1e5`), and click, fill, type, press and the
  other ref actions reach elements inside same-origin and cross-origin frames. The Sidekick reads a frame only when the
  site of the frame is one the person allowed. `Page.frames` and `Page.popups` RPC methods.
- Cross-origin isolation, `SharedArrayBuffer` and `Atomics` across the page and its dedicated Workers: `crossOriginIsolated` follows
  COOP `same-origin` plus COEP `require-corp` / `credentialless`, `SharedArrayBuffer` exists only then, `Atomics.wait` blocks in
  Workers (and throws on the page), `notify` and `waitAsync` work across VMs. COEP is enforced for cross-origin scripts,
  stylesheets, images and media, frames and Worker scripts; `credentialless` drops credentials. Not done: shared memory with
  SharedWorkers or Service Workers, COOP browsing-context-group switching ([docs/web-platform.md](docs/web-platform.md)).
- WebAssembly threads: shared `WebAssembly.Memory`, atomic instructions and `memory.atomic.wait/notify`, and posting a shared
  `Memory` or a `Module` to Workers, on cross-origin isolated pages. Verified with hand-assembled modules, not with an
  emscripten or wasm-bindgen program ([docs/webassembly.md](docs/webassembly.md)).
- WebCodecs: `VideoDecoder` (VP8, VP9, H.264, HEVC, AV1), `AudioDecoder` (Opus, AAC, MP3, Vorbis, PCM, A-law, mu-law),
  `VideoEncoder` (VP8 only), `AudioEncoder` (Opus only), `VideoFrame`, `AudioData`, `EncodedVideoChunk`, `EncodedAudioChunk`,
  `ImageDecoder` and `isConfigSupported`, off the JS thread, with frames drawable to canvas and creatable from canvas,
  `ImageBitmap` and `<video>`. No H.264/VP9/AV1/AAC encoder and no hardware paths ([docs/webcodecs.md](docs/webcodecs.md)).
- Encrypted Media Extensions with Clear Key: `requestMediaKeySystemAccess('org.w3.clearkey')`, `MediaKeys`, `MediaKeySession`
  (temporary sessions), `setMediaKeys`, `encrypted` / `waitingforkey` events, and `cenc`, `cens`, `cbc1` and `cbcs` decryption of
  fragmented MP4 in the MSE pipeline. Widevine, PlayReady and FairPlay still reject with `NotSupportedError`; no persistent
  sessions, no encrypted WebM, no encrypted progressive `src` ([docs/media-streaming.md](docs/media-streaming.md)).
- `h264.Decoder.SetThreads` / `Flush` and `yuv.Frame.Wait`; a 1080p High profile H.264 fixture with ffmpeg MD5s
  (`internal/media/testdata/h264_1080p_high.mp4`), differential tests for the new kernels (motion compensation, CABAC,
  deblocking, VP9 loop filter, video blit) and benchmarks (`BenchmarkH264Decode1080p`, `BenchmarkConvert1080p`,
  `BenchmarkDrawVideoFrame*`); `TestPlaybackQuality` can paint the page while it plays (`SWEEF_BENCH_PAINT=WxH`).
- Dedicated Workers run on a full script VM of their own goroutine: IndexedDB, Cache Storage, `navigator.storage`, the Origin Private
  File System with `FileSystemSyncAccessHandle`, `fetch`, WebAssembly and nested workers work inside them; real
  `SharedWorker`, `BroadcastChannel` and `navigator.locks`; structured clone with transferred `MessagePort`s and `ArrayBuffer`s.
- `<iframe>` documents are loaded as nested browsing contexts: separate origin, window and VM, `postMessage` with origin checks,
  `parent`/`top`/`frameElement`, same-origin `contentWindow`/`contentDocument`, `sandbox`, `srcdoc`, `X-Frame-Options` and CSP
  `frame-ancestors` ([docs/storage-partitioning.md](docs/storage-partitioning.md)); painting and input are in the entry above.
- `BrowserContext.SetPartitionPolicy`, `GrantThirdPartyCookies`, `engine.SetHostMap` (resolver override), `Options.DisableFrames`.
- Real camera, microphone and tab/window capture for `getUserMedia`, `getDisplayMedia` and `enumerateDevices`: V4L2 cameras and PulseAudio/PipeWire microphones on Linux (pure Go), a permission prompt with per-site choices and in-use indicators, `SWEEF_FAKE_DEVICES=1` test devices and `capdemo`; Windows and macOS backends are included but unverified on hardware (`docs/capture.md`).

- Notifications: `Notification`, `showNotification`/`getNotifications`, `notificationclick`/`notificationclose`,
  `clients.openWindow`, `WindowClient.focus/navigate`, per-site permission with a prompt next to the address bar (quiet UI
  without a gesture, pause after three dismissals), a site info entry, Do not disturb, Permissions API for notifications
  and push. Delivery through `org.freedesktop.Notifications` on Linux (tested against a real `dbus-daemon`), balloon
  notifications on Windows and `NSUserNotification` on macOS (compiled only), and an in-window toast otherwise
  ([docs/notifications.md](docs/notifications.md)).
- Web Push: `PushManager`, `PushSubscription`, `push` and `pushsubscriptionchange` events, RFC 8291 decryption and
  RFC 8292 VAPID (checked against the RFC test vectors), an autopush client, and `sweef pushd`, a self-hostable push
  relay. No push service is built in: set `SWEEF_PUSH_SERVICE` or the `push.service` setting
  ([docs/push.md](docs/push.md)).
- Background Sync (retry 5 min then 15 min, three attempts, `lastChance`), Periodic Background Sync (12 hour floor,
  granted to bookmarked sites), `navigator.setAppBadge`, Web Locks across tabs and workers
  ([docs/background-sync.md](docs/background-sync.md)).
- Apache-2.0 `LICENSE` and `NOTICE`.
- Contribution and project health files: `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`, `SECURITY.md`, `SUPPORT.md`,
  `GOVERNANCE.md`, `CODEOWNERS`, issue forms, pull request template, `Makefile`, `.editorconfig`.
- Automation: CodeQL, `govulncheck`, dependency review, Dependabot, pull request title check, gofmt check.
- `docs/development.md` (package map and contracts), `docs/releasing.md`, and an ADR log in `docs/adr/`.
- `appbench -open-profile` times the key that opens the omnibox dropdown and the key that closes it separately and
  writes a CPU profile.
- Sound for the desktop browser: a pure-Go audio mixer with an `oto` device backend, per-tab audible indicator and mute,
  and the audio clock driving `<video>`/`<audio>` and Web Audio (`docs/audio-output.md`).
- Adaptive streaming: MSE rebuilt on parsed coded frames with a native player, decode-ahead and late-frame handling,
  measured decode and dropped-frame numbers, and a per-site status table (`docs/media-streaming.md`); EME/DRM is not supported.
- WebRTC on pion (pure Go): `RTCPeerConnection`, data channels, transceivers, stats; local IPs hidden behind mDNS by
  default, no STUN unless the page configures it ([docs/webrtc.md](docs/webrtc.md)).
- WebAssembly JavaScript API on wazero (pure Go): `Module`, `Instance`, `Memory` (an ArrayBuffer over the live linear memory), `Table`, `Global`, `validate`/`compile`/`instantiate` and the streaming variants, in pages and Web Workers; no threads, `Tag` or `externref` ([docs/webassembly.md](docs/webassembly.md)).
- IndexedDB, `navigator.storage` and Cache Storage, persisted per origin under the profile's `storage` directory (append-only logs, response bodies on disk, quota, wiped by "Clear browsing data", memory-only in private windows); see docs/storage.md.
- Service workers: `navigator.serviceWorker`, the install/activate/fetch/message lifecycle, real interception of navigations, `fetch()`, XHR and sub-resource loads, registrations that survive a restart; see docs/service-workers.md.
- Web platform layer (see `docs/web-platform.md`): real Streams plus Compression/Decompression and TextEncoder/TextDecoder streams, XPath, URLPattern, the Navigation API, ElementInternals, View Transitions, the CSS Highlight and `@property` registries, `Array.fromAsync`, `scheduler`, the Origin Private File System and honest stand-ins for device, credential, payment and Picture-in-Picture APIs, all loaded lazily.
- WebRTC video and audio: a pure Go VP8 encoder, VP8 and Opus send and receive with jitter buffers, NACK/PLI and bandwidth estimation, `getStats` media entries ([docs/webrtc.md](docs/webrtc.md), [docs/webrtc-interop.md](docs/webrtc-interop.md)).

- Quota management for site storage: a global budget (60% of the profile volume's available space, at most 64 GiB) with at most 20% per origin, exact usage accounting across IndexedDB, Cache Storage, the origin private file system and service worker scripts, `QuotaExceededError` on writes over the cap, eviction of whole least-recently-used best-effort origins under pressure (open connections get `close`), a working `navigator.storage.persist()` decided by the application (bookmarked sites, or a prompt the embedder installs), `estimate()` with real usage and quota, and a per-site storage list with a "Clear" button in Settings > Data. `engine.Options.StorageBudgetMiB`; see docs/storage.md.
- ADR 0002 (the ordered store, with measurements) and `tools/kvbench`, the harness behind it.
- `internal/media/cdm`: the key system layer behind Encrypted Media Extensions, one Go interface for every key system
  (`Registry` with capability negotiation, `Backend`/`Session`, events, `Decrypt`, optional decrypt-and-decode and platform
  renderer interfaces), with Clear Key implemented in full (cenc, cens, cbc1, cbcs). The JavaScript EME API is not part of
  this change. Contract and status: [docs/drm.md](docs/drm.md).
- Widevine host (`com.widevine.alpha`): `cmd/sweef-cdm`, a separate dynamically linked helper process that loads the Widevine
  CDM the user already has (explicit path or `SWEEF_WIDEVINE_CDM`, else the WidevineCdm directory of an installed Google
  Chrome, Chromium or Edge) through purego and implements Chromium's CDM host ABI (interface versions 10, 11, 12), and a
  framed stdin/stdout protocol to the browser, so the Linux browser binary stays static. A VMP signature next to the helper
  (`sweef-cdm.sig`, as castLabs EVS writes it) is passed to the CDM; no signature is ever fabricated. Sweef does not bundle or
  download a CDM. **Not tested against a real Widevine library** (none in the build environment): the host is tested against
  a fake CDM that implements the same binary interface in Go. The desktop CDM is software (L3), typically capped at 720p by
  services.
- PlayReady (Windows, Media Foundation) and FairPlay (macOS, AVContentKeySession) backends, **unverified and compile-checked
  only**, off unless `SWEEF_PLAYREADY=1` / `SWEEF_FAIRPLAY=1`; license exchange only, playback through the platform renderer
  is not implemented.
- `scripts/release/build.sh` also builds `sweef-cdm` next to `sweef` (`--skip-cdm-host` to omit); packages do not install it yet.
- Binary-size tooling ([docs/development.md](docs/development.md), "Binary size budget"): `tools/sizeprof` (sections, bytes per
  package, largest symbols of a build), `tools/startbench` (start-up, first page, first frame, peak RSS and CPU time of two
  binaries, run alternately), `go test ./cmd/sweef -run BinarySizeBudget` (fails over the budget or when the binary grows
  more than 1 MiB past `scripts/release/size-baseline.txt`), headroom and the per-package table in
  `scripts/release/build.sh`, `make size` and `make embeds`. `internal/zpack` is the deflated-embed format.

### Changed
- A page that embeds a third-party frame now makes real requests for it, unless the Data Saver's click-to-load frames are on.
  The README's data-saver comparison with Chromium was measured before frames were loaded and still says Sweef fetches no
  iframe documents.
- `Tab.Keyboard` types into the focused element (it dispatched to the document before).
- `window.open` from page script is no longer "open a tab from the chrome, return null": it goes through the engine
  (popup blocker, opener link, named windows).
- The release binary-size budget is 40 MiB (was 32 MiB): WebRTC (pion), WebAssembly (wazero) and the web platform layer add about 12 MiB. The stripped linux/amd64 build is about 34.7 MiB.
- Embedded data is deflated and inflated on first use (`internal/zpack`, standard-library `compress/flate`): the JavaScript
  preludes and platform modules (`NAME.js.z`), the chrome UI (`internal/ui/web.zar`), the eight Go fonts
  (`internal/font/gofont.zar`) and the IANA time zone database (`internal/intl/zoneinfo.zar`, which replaces the
  `time/tzdata` import; the operating system's database is still looked at first). **After editing a `.js` file under
  `internal/engine` or a file under `internal/ui/web`, run `make embeds` and commit the generated files**; the tests fail
  with that instruction when they are stale. The prelude bytecode cache is now keyed by the deflated bytes, so its file
  names changed once.
- The embedded Go fonts no longer carry their TrueType hinting program (`fpgm`, `prep`, `cvt ` and the glyph instructions,
  more than half of the glyph data); Sweef uses unhinted outlines everywhere and every glyph, advance, mapping and name is
  compared with the x/image originals by tests.
- Sidekick's HTTP/2 health check uses `http.Transport.HTTP2` (`SendPingTimeout`, `PingTimeout`) instead of importing
  `golang.org/x/net/http2`, which linked a second HTTP/2 stack.
- `keymap.ComposeDead` (X11 dead keys) answers from a generated table of the NFC compositions of the dead keys' accents
  instead of linking `golang.org/x/text/unicode/norm`; checked against NFC for every code point of the first three
  planes. It returns "" for a combining mark that is not a dead key's (the package never passed one).
- The Opus theta tables in `third_party/gopus` are `int16` (every value fits; the compiler checks).
- The release build compiles the connection set-up and control-plane packages without inlining
  (`scripts/release/noinline-packages.txt`) and links with `-funcalign=16`. A plain `go build` is unchanged.
- IndexedDB, Cache Storage and the origin private file system are stored in one disk-resident ordered key-value store per origin (goleveldb) instead of an append-only log replayed into memory: memory no longer depends on database size (a 1 GB database opens in 44 ms and reads at about 30 MB resident), cursors stream, transactions commit as one atomic batch, `clear()` and `deleteDatabase` are O(1), values and cache bodies above 64 KiB are files, and a crash or kill leaves each transaction whole or absent. The origin private file system now persists across restarts and is no longer limited to 256 MiB of memory.
- `navigator.storage.persist()` no longer always succeeds in a persistent context: it asks the application's policy and answers no without one. `StorageQuotaMiB` is now a cap per origin that overrides the derived one (0 = derived), not a fixed 1 GiB.
- README shows the real browser UI (first-run setup with the real browser logos, Sidekick, Data Saver, new tab, omnibox), rendered by `uipreview`.
- The omnibox dropdown uses a lighter shadow (the `--sh-2` token plus a 1 px ring instead of `--sh-3`).

### Fixed
- `fetch` and `XMLHttpRequest` send the request headers page code sets (Supabase `apikey`, `Authorization`); they were dropped.
- `<img>` elements in the document fire `error` when the response is not an image and report their real `naturalWidth`/`naturalHeight`.
- Images with `max-width: 100%` (Tailwind's reset) are no longer 0 px wide in inline-block, inline-flex, float, flex-item and max-content boxes.
- Grid area and line names keep their case (Wikipedia's camelCase areas now place their items).
- `sweef eval`, `sweef console` and `sweef network` print results again (the RPC returns `.result`, `.messages`, `.entries`), and the first navigation of a session loads the page once, not twice.
- `getComputedStyle` answers from the cascade for display, position, sizes, margins, padding, borders, colours, fonts, line-height, opacity and z-index.
- Variable web fonts paint at the requested weight (they drew their default instance), and the `opsz` axis follows the font size.
- Absolutely positioned children of flex containers take their static position from `justify-content` and `align-items`/`align-self`.
- Quirks-mode pages no longer grow a needless scrollbar when the first child's top margin collapses through `body`; `document.compatMode` reports `BackCompat` for them.
- Selectors the DOM builds for ids and names escape backslashes and line breaks, and the selector parser understands CSS hex escapes; `CSSStyleSheet.insertRule` keeps nested braces in the declaration block.
- A download name cannot leave the downloads directory (`../x`, `..\x`), clicking a link never navigates the top level to `data:` or runs `vbscript:`, and the settings page ignores section names it does not define.

### Performance
- **The stripped linux/amd64 release binary is 4.11 MiB smaller**: 36,921,470 bytes (35.21 MiB) against 41,234,558
  (39.32 MiB), so 4.79 MiB of the 40 MiB budget are left instead of 0.68 MiB, with every feature and the same output. Each
  step, measured on its own build in this order: JavaScript preludes and platform modules deflated 1,105,920 bytes; chrome
  UI and time zone database deflated 491,520; Opus theta tables as `int16` 196,608; Go fonts deflated 729,088; no
  `x/net/http2` 376,832; Go fonts without hinting programs 266,240; no `x/text/norm` 102,400; control-plane packages
  built without inlining 757,760; `-funcalign=16` 286,720. The other release targets save 3.88 MiB (linux/arm64), 3.92
  (darwin/arm64), 3.89 (windows/arm64), 4.15 (darwin/amd64) and 4.11 (windows/amd64); the tightest target is now darwin/amd64
  with 4.34 MiB left (0.19 MiB before). Start-up did not get slower (medians of alternating runs, old and new binary):
  `sweef version` 7.5 to 7.4 ms; first page with an empty bytecode cache 343.1 to 341.2 ms; the same page with a warm cache
  177.5 to 165.4 ms (it no longer hashes 1.2 MB of script); headless start to first UI frame 1173.4 to 1174.5 ms; peak RSS
  0.8% to 7% lower in every case. The in-process benchmarks of the packages built without inlining stayed within noise
  (`BenchmarkChromeKeystroke` 10.89 to 10.88 ms best of eight; net/http and crypto/tls microbenchmarks within 10% on
  microsecond operations). Details, rejected ideas and the method are in
  [docs/development.md](docs/development.md).
- H.264 video decoding uses frame threads (up to one per core, at most four; `SWEEF_H264_THREADS` overrides, 0 turns them
  off), a faster deblocking filter, motion compensation, CABAC engine and display conversion. Measured on a shared
  4 vCPU VM, medians of five alternating runs of the old and the new build (frames per second of the whole decode /
  CPU time per frame): 720p30 clip 54 to 167 fps (23.5 to 15.4 ms), 1080p30 clip 24.8 to 97 fps (50.7 to 33.1 ms),
  1080p60 clip 28 to 130 fps (46.7 to 27.2 ms). Every picture is identical to the old decoder's (ffmpeg MD5s of a new
  1080p fixture, the repository's conformance streams, 2, 3 and 4 threads, `-race`). Details in
  [docs/media-streaming.md](docs/media-streaming.md).
- Native player, 20 s of 1080p30 H.264 through the page: dropped frames 21%, 53% and 43% before, 0.5%, 0.3% and 12% after
  (load average 3.6 to 7; the 8.7% in the previous measurement was at load 1), and 1080p60 59 to 67% before, 1.8% (load 4)
  to 36% (load 14) after. Painted into a fullscreen box with the page repainting at 60 Hz: 40 to 68% dropped before,
  0% after.
- Video frames are converted and drawn much faster: the RGBA conversion of a 1080p picture 7.6 to 8.8 ms to 3.4 to 3.8 ms
  (four goroutines), drawing it scaled into a 1920x1080 / 1280x720 / 640x360 box 28 to 30 / 19 to 22 / 6.5 to 7.8 ms to
  0.55 / 2.5 to 3.4 / 0.5 to 0.8 ms (pixel-identical to the general image shader), one page paint with a 1080p picture
  40 to 87 ms to 8 to 12 ms (median); opaque solid fills (page backgrounds) replace pixels instead of blending them.
- VP9 1080p: 56 to 44 ms of CPU per frame (48 fps both before and after; the frame rate was already above real time) from
  a loop filter without per-sample overhead, inter prediction written into the frame and waits that sleep instead of
  yielding in a loop (which used 46% of the CPU of a profile taken on a busy machine). The H.264 deblocking wavefront
  uses the same waits.
- `navigator.mediaCapabilities` reports H.264 up to 1080p30 as smooth (the pixel-rate budget was 720p30).
- Opening the omnibox dropdown: rows and section headings are pooled and reused, and rows no longer get `id`/index
  attribute writes when they move (about 13.0 ms to 11.3 ms median in-process at 1x with history present; the shadow
  change takes roughly another 1 ms off).

### Security
- Updated `golang.org/x/net` from v0.17.0 to v0.61.0 (HTML parser denial-of-service and XSS-class advisories GO-2024-3333, GO-2025-3595, GO-2026-4440, GO-2026-5025 to 5030, HTTP/2 GO-2024-2687, GO-2026-4918, IDNA GO-2026-5026) and the Go toolchain to 1.26.9 (about 30 standard library advisories in `net/http`, `crypto/tls`, `crypto/x509`, `net/url`, ...). `govulncheck ./...` now reports no vulnerabilities; it runs weekly and on every PR.

### Removed
- The append-only log format of IndexedDB and Cache Storage. It never shipped; leftover `idb/` and `caches/` directories are deleted when found, and a store in an unknown or unrecoverable format is kept as `kv.bak` while a new one starts.
- A 32 MB `appbench` binary that had been committed by mistake (it stays in git history).

## Pre-release history (first alpha baseline, included in 0.1.0-alpha)

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
  on compatibility (no extensions; DRM is Clear Key plus a Widevine helper that needs a user-installed CDM, see docs/drm.md).
- Windows and macOS windows and installers have never been run; CI only builds and unit-tests them.
- Data Saver percentages were measured before web fonts landed and may move by a few points.

[Unreleased]: https://github.com/sweefbrowser/sweef/compare/v0.1.0-alpha...HEAD
[0.1.0-alpha]: https://github.com/sweefbrowser/sweef/releases/tag/v0.1.0-alpha
