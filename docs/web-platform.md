# Web platform coverage

What the JavaScript environment provides beyond the DOM prelude, and what it
deliberately does not. Status is one of:

- **implemented**: behaves to the specification for what a headless engine can do.
- **stub**: the API exists and validates its input; the sentence says what it really does.
- **absent**: intentionally missing, so feature detection answers honestly.

`TestPlatformCoverage` (`internal/engine/platform_coverage_test.go`) asserts that
every implemented name is present in a fresh tab and that every name listed as
absent below stays absent. Behaviour is covered by the table-driven tests in
`internal/engine/platform_*_test.go`.

## How it is built

Everything in this document lives in `internal/engine/platformjs/` and is wired
by `internal/engine/platform.go`. Nothing here costs start-up time unless a page
touches it.

- `boot.js` is the only file evaluated in every VM. It holds a table of names and
  installs one lazy accessor per name (on the global object's prototype for
  globals, on the real prototype for members such as `Navigator.prototype.wakeLock`).
- The first access of a name evaluates `platformjs/<module>.js`
  (`__sw.platformSrc`), which defines the real property and so removes the
  accessor. Assigning to a name first (`window.URLPattern = polyfill`) simply
  replaces it. `h.js` (shared helpers) loads with the first module.
- Go side: `platform_codec.go` (compression, one host function `codec`),
  `platform_opfs.go` (file system storage, one host function `opfs`).

To add an API: write `platformjs/<module>.js`, add its names to the table in
`boot.js`, add the names to `platformImplemented` in the coverage test.

Start-up impact, measured on a 4-core VM by interleaving builds of the old and
new prelude in one process (garbage collected before each sample, 300 samples
each, load of the prelude bytecode into a fresh VM): median 24.0 ms before,
24.8 ms after (+0.8 ms; trimmed mean +0.7 ms). Two things keep it that low:
nothing is parsed until used, and `buildVM` now keeps QuickJS's cycle collector
off while the prelude loads (it has nothing to collect then and each pass
re-marked the whole heap; about 1.3 ms of every build, and it was growing with
the prelude). `BenchmarkBuildVM` and `BenchmarkBuildVMWithoutPlatform`
(`platform_bench_test.go`) reproduce the comparison; expect run-to-run noise of a few ms
on a shared machine, so compare medians of many runs.

Web Workers run in a separate, much smaller VM (`worker_prelude.js`). None of
the APIs below exist inside workers yet.

## Streams and compression

| API | Status | Notes |
|---|---|---|
| ReadableStream, default and byte controllers, readers (default and BYOB), `tee`, `pipeTo`, `pipeThrough`, async iteration, `ReadableStream.from` | implemented | Replaces the old one-chunk stubs. `Response.body` and `Blob.stream()` return real streams. Byte chunks are copied on `enqueue` (no buffer transfer). |
| WritableStream, writer, controller, TransformStream, controller | implemented | Back-pressure, `desiredSize`, abort and error propagation follow the Streams Standard. |
| CountQueuingStrategy, ByteLengthQueuingStrategy | implemented | |
| CompressionStream | implemented | `gzip`, `deflate` (zlib), `deflate-raw`. Go's compress/flate; output is emitted as the encoder produces it. `brotli` is rejected with TypeError (the vendored brotli library only decodes). |
| DecompressionStream | implemented | Same formats plus `brotli`. Streaming: input is consumed chunk by chunk, output is released in 64 KiB windows while the readable side is being read, so a tiny input that expands hugely cannot flood memory. Corrupt, truncated, empty input and trailing garbage reject with TypeError. |
| TextEncoderStream, TextDecoderStream | implemented | Lone surrogates across chunks, partial UTF-8 sequences across chunks, `fatal`, `ignoreBOM`. Legacy labels: `windows-1252`/`latin1`/`ascii` only; other labels throw RangeError. |

## Documents and URLs

| API | Status | Notes |
|---|---|---|
| `document.evaluate`, `createExpression`, `createNSResolver`, XPathEvaluator, XPathExpression, XPathResult, XPathNSResolver | implemented | XPath 1.0 as browsers ship it: 13 axes (the namespace axis is empty, as namespace nodes are not exposed), predicates, the whole core function library, union, all result types, snapshots, iterators that throw `InvalidStateError` after a mutation, namespace resolvers (function or object). No variables (`$x` throws, as in browsers). HTML element names match case-insensitively. Evaluated in JavaScript over the live DOM. |
| URLPattern | implemented | Full pattern syntax (named groups, wildcards, regexp groups, modifiers, group braces, escapes), constructor strings, init dictionaries, `baseURL`, `ignoreCase`, normalised component getters, `test`/`exec`, `hasRegExpGroups`. Hostnames are lower-cased but not IDNA-converted, because the engine's `URL` class does not do that. As in the specification, `user:pass@` and `scheme:path` in a constructor string need the colon escaped (`\:`). |

## ECMAScript

QuickJS (ES2023 plus parts of later editions) already ships `Iterator` helpers,
`Set` methods (`union`, `intersection`, ...), `Object.groupBy`/`Map.groupBy`,
`Promise.withResolvers`/`try`, change-array-by-copy, `ArrayBuffer.prototype.transfer`,
`isWellFormed`, base64/hex on typed arrays, `RegExp.escape`, `Error.isError` and the
RegExp `v` flag (probed and covered by `TestECMAScriptBuiltins`, so a QuickJS
downgrade is noticed). The `v` flag therefore needs no polyfill: it is native.

| API | Status | Notes |
|---|---|---|
| `Array.fromAsync` | implemented | Async and sync iterables, array-likes, mapper with `thisArg`, subclass constructors, iterator closing on mapper errors. |
| `Symbol.dispose`, `Symbol.asyncDispose`, `DisposableStack`, `AsyncDisposableStack`, `SuppressedError` | implemented | Library half of explicit resource management. The `using` / `await using` syntax cannot be polyfilled and is a SyntaxError. |
| `Atomics.waitAsync` | implemented | Single agent: a waiter is woken by `Atomics.notify` in the same agent or by its timeout. |
| `Intl.Locale` | implemented | BCP 47 parsing, canonical casing, `-u-` keywords, options overrides, `maximize`/`minimize` using a likely-subtags table for the major languages only. |
| `Intl.NumberFormat`, `DateTimeFormat`, `PluralRules`, `RelativeTimeFormat`, `ListFormat`, `DisplayNames`, `DurationFormat`, `Collator`, `Segmenter`, `getCanonicalLocales`, `supportedValuesOf`, and the `toLocale*String` / `localeCompare` methods | implemented | CLDR 47 data for 73 locales generated by `tools/intlgen` (about 750 KB gzipped, `internal/intl/data`), the IANA time zone database embedded deflated (`internal/intl/zoneinfo.zar`, 139 KB, where `time/tzdata` stores the 408 KB zip uncompressed), exact decimal arithmetic for number formatting (nine rounding modes, increments, compact/scientific/engineering, units, currencies, `formatRange`), skeleton matching for date patterns, three calendars (gregory, buddhist, persian), UAX #29 grapheme/word/sentence segmentation (conformance-tested). Compared with Node 22 (ICU 77): number formatting matches in all but 32 of 47,000 generated cases (arabic-indic digit oddities); date formatting in about 89% of an adversarial matrix of 39 locales x 49 option sets x 7 instants x 8 zones, and in all common cases (`tools/intlgen/cmp-*.js`, `engine/intl_test.go`). Known gaps: other locales fall back to their language or to English; calendars other than gregory/buddhist/persian are formatted as gregory; `dayPeriod` option and generic zone names (`shortGeneric`) are approximate; East Asian collation (pinyin, stroke) and dictionary-based word breaking (Thai, CJK) are not implemented; pt/id/bn glue between date and time differs from ICU in places. |
| `Error.captureStackTrace` | implemented | Sets `stack` on the object (V8 convenience that libraries probe for). |
| `scheduler.postTask`, `scheduler.yield`, `Scheduler`, `TaskController`, `TaskSignal`, `TaskPriorityChangeEvent` | implemented | Three priorities, delays, abort signals, `setPriority` re-queueing pending tasks and firing `prioritychange`, `TaskSignal.any`. Tasks run one per timer tick. |
| `Temporal` | absent | Not attempted. Too large to polyfill faithfully; pages detect it and fall back. |
| `Iterator.zip`, `Promise.allKeyed`, `Math.clamp` | absent | Not shipped by the engine and not in a published edition (or not stable) yet. |

## Navigation, rendering hooks, custom elements

| API | Status | Notes |
|---|---|---|
| History: `back`, `forward`, `go`, `pushState`, `replaceState`, hash changes | implemented | The prelude now keeps a real entry list for the document; traversal is asynchronous and fires `popstate` (with the entry's state) and `hashchange`. Moving past the first or last entry of the document still only fires a null-state `popstate`, because the cross-document history belongs to Go. |
| `window.navigation`, Navigation, NavigationHistoryEntry, NavigationDestination, NavigationTransition, NavigationActivation, NavigateEvent, NavigationCurrentEntryChangeEvent | implemented | `entries`, `currentEntry`, `canGoBack/Forward`, `navigate` (push/replace/auto, state, info), `reload`, `traverseTo`, `back`, `forward`, `updateCurrentEntry`, `navigate` / `navigatesuccess` / `navigateerror` / `currententrychange` / `dispose` events, `NavigateEvent.intercept()` with handlers and `signal`, ongoing navigation aborted by a newer one. `history.pushState` and friends fire `navigate` first. Limits: only this document's entries are listed (not the cross-document ones), `userInitiated` is always false, no `formData`/`downloadRequest`, `scroll()` and focus handling do nothing, an un-intercepted cross-document `navigate()` hands over to the engine and its promises never settle. |
| ViewTransition, `document.startViewTransition`, `document.activeViewTransition`, ViewTransitionTypeSet | stub | There is no compositor: the update callback runs, `ready`/`updateCallbackDone`/`finished` settle with the right ordering and rejection behaviour, `skipTransition()` and supersession work. Nothing animates. |
| ElementInternals, `attachInternals()`, CustomStateSet, ValidityState | implemented | Form-associated custom elements (`static formAssociated`): `setFormValue`, `setValidity` (flags, message, anchor), `validity`, `labels`, `form` (including the `form` attribute), `willValidate`, `checkValidity`, `reportValidity` (fires `invalid`), `states`, ARIA defaults and `role`. `new FormData(form)` includes the submitted values and `form.reset()` calls `formResetCallback`. Limits: a native form submission does not include custom-element values (only `FormData`), `formAssociatedCallback`/`formDisabledCallback`/`formStateRestoreCallback` are not called, and `:state()` selectors do not match. |
| CSS.highlights, Highlight, HighlightRegistry, AbstractRange | stub | Ranges are stored and iterable with the full Set/Map interface and validation; nothing is painted. |
| CSS.registerProperty, CSSPropertyRule, CSSRule | stub | Definitions are validated (name, syntax string, initial value against the syntax) and stored; they do not affect style resolution. Read them back with `CSS.registeredProperty(name)` (non-standard helper). |
| ContentVisibilityAutoStateChangeEvent | stub | The class exists; the engine never dispatches it (no `content-visibility: auto` handling). |

## Devices and platform objects

| API | Status | Notes |
|---|---|---|
| `screen.orientation`, ScreenOrientation | stub | `type` follows the viewport shape (`landscape-primary`/`portrait-primary`), `angle` is 0, `lock()` rejects `NotSupportedError`, `unlock()` is a no-op, `onchange` never fires. |
| DeviceOrientationEvent, DeviceMotionEvent (+ Acceleration, RotationRate) | stub | Classes with their fields; `requestPermission()` resolves `'denied'`; no event is ever dispatched. |
| `navigator.getGamepads()`, Gamepad, GamepadButton, GamepadEvent | stub | Returns `[null, null, null, null]`; no gamepad ever connects. |
| `navigator.wakeLock`, WakeLock, WakeLockSentinel | stub | `request('screen')` resolves a sentinel that records `released` and fires `release`; nothing is kept awake. Rejects `NotAllowedError` for hidden documents. |
| `navigator.mediaSession`, MediaSession, MediaMetadata | stub | Stores `metadata`, `playbackState`, action handlers, position state with the specified validation; no OS media controls. |
| `navigator.credentials`, CredentialsContainer, Credential, PasswordCredential, FederatedCredential | stub | Behaves as a browser with no credential provider: `get()` resolves `null` (`publicKey` rejects `NotAllowedError`), `create()`/`store()` reject `NotAllowedError`, `preventSilentAccess()` resolves. Aborted signals reject with their reason. |
| PublicKeyCredential | stub | `isUserVerifyingPlatformAuthenticatorAvailable()` and `isConditionalMediationAvailable()` resolve `false`; `getClientCapabilities()` resolves `{}`. |
| PaymentRequest, PaymentResponse, PaymentRequestUpdateEvent | stub | The constructor validates method data and totals; `canMakePayment()`/`hasEnrolledInstrument()` resolve `false`; `show()` rejects `NotSupportedError`; `abort()` rejects `InvalidStateError`. |
| `document.hasStorageAccess()` / `requestStorageAccess()` | implemented | The engine only runs top-level documents, so both report first-party access (`true` / resolved); a nested context would be rejected `NotAllowedError`. |
| `trustedTypes`, TrustedTypePolicyFactory, TrustedTypePolicy, TrustedHTML/Script/ScriptURL | stub | Policies run their callbacks and return wrapper objects (`isHTML` etc., `emptyHTML`, `defaultPolicy`, `getAttributeType`, `getPropertyType` work). Sinks accept strings and trusted values alike: no enforcement, no CSP `require-trusted-types-for`. |
| `document.pictureInPictureEnabled`, `requestPictureInPicture()`, `exitPictureInPicture()`, `disablePictureInPicture` | stub | Provided by the media prelude, matched against Chromium: `pictureInPictureEnabled` is `false`, `requestPictureInPicture()` rejects (`InvalidStateError` until the video's metadata is loaded, then `NotSupportedError`), `exitPictureInPicture()` rejects `InvalidStateError`. The platform layer only fills in whatever is missing. |
| PictureInPictureWindow, PictureInPictureEvent | stub | Classes only; no window is ever created. |
| Sanitizer API | absent | Skipped on purpose. |

## Shared memory and cross-origin isolation

| API | Status | Notes |
|---|---|---|
| `crossOriginIsolated`, `SharedArrayBuffer`, `Atomics` on shared memory | implemented | A document is cross-origin isolated when its response has `Cross-Origin-Opener-Policy: same-origin` and `Cross-Origin-Embedder-Policy: require-corp` or `credentialless`. Only then do `crossOriginIsolated` read `true` and `SharedArrayBuffer` exist (it is absent otherwise, as in Chromium); dedicated Workers inherit the state of their creator. A buffer posted to a Worker (or back, or between two Workers of one page) is the same memory: the page and its dedicated workers form one agent cluster, the bytes are real Go memory shared by the VMs, and `Atomics.add/load/store/compareExchange/...` are real atomic operations. |
| `Atomics.wait`, `notify`, `waitAsync` | implemented | `wait` blocks the calling goroutine and is allowed in Workers only (it throws `TypeError` on the page, like browsers); `notify` and `waitAsync` work across VMs and wake in FIFO order, with timeouts; the QuickJS built-in wait queue is replaced by one over Go queues because the native one is per-VM and has no `waitAsync`. `worker.terminate()` and page teardown interrupt a worker that is blocked in `Atomics.wait`. |
| COEP enforcement | implemented | With `require-corp`, a cross-origin script, stylesheet, image or media response is blocked unless it is CORS-enabled (`Access-Control-Allow-Origin` of `*` or the document origin) or sent `Cross-Origin-Resource-Policy: cross-origin` (`same-site` and `same-origin` are honoured by site/origin). `fetch()` and XHR already need CORS. A `fetch(..., {mode: 'no-cors'})` is not checked. With `credentialless` such requests are sent without cookies or credentials and are not blocked. A frame inside a COEP document must carry a COEP header itself (and, under `require-corp`, a cross-origin frame also needs CORP: `cross-origin`), and a Worker script must carry one as well, otherwise it is blocked. |
| Shared memory in SharedWorkers, Service Workers, `BroadcastChannel`, `postMessage` to other windows or frames | partial | Not part of the agent cluster: posting a `SharedArrayBuffer` to those throws `DataCloneError`. A frame has its own VM and is not in the page's cluster either. |
| `Cross-Origin-Opener-Policy`: severing `window.opener`, `same-origin-allow-popups` | implemented | At every top-level navigation of a popup (and of its opener) the policies of the two documents decide whether they may keep their references (`coopMatch` in `windows.go`): a cut link makes `window.opener` `null`, `closed` `true` for the other side, and clears the popup's name. An OAuth popup of a `same-origin-allow-popups` page keeps its opener. See [frames-popups.md](frames-popups.md). |
| `Cross-Origin-Opener-Policy`: moving popups to a new browsing context group (agent cluster, `SharedArrayBuffer`), reporting endpoints | not implemented | The group is only the opener link; a popup does not get a separate agent cluster and no COOP report is sent. |

Frames, `window.open`, popups, painting and input of frames: [frames-popups.md](frames-popups.md).

Tests: `go test ./internal/engine -run 'TestCrossOrigin|TestCOEP|TestSharedArrayBuffer|TestAtomics|TestTerminatingAWorker'` use
`httptest` servers that send the headers and real Workers; the counter test increments one `Int32Array` from two workers
and checks the sum, the wait/notify tests ping-pong between the page and a worker and between two workers.

## WebCodecs

`VideoDecoder`, `VideoEncoder`, `AudioDecoder`, `AudioEncoder`, `VideoFrame`, `AudioData`, `EncodedVideoChunk`,
`EncodedAudioChunk`, `ImageDecoder` (with `ImageTrack`/`ImageTrackList`) and every `isConfigSupported()` exist in pages
and Workers, backed by the pure-Go codecs of `internal/media`, running off the JavaScript thread. What is supported and
what is not, per codec, is in [webcodecs.md](webcodecs.md).

## MediaRecorder

`MediaRecorder` and `BlobEvent` record `getUserMedia` / `getDisplayMedia` streams, `canvas.captureStream()` and the stream of a
`MediaStreamAudioDestinationNode` into WebM (Opus, VP8) or Ogg (Opus), with `timeslice`, `pause()` / `resume()` /
`requestData()`, the specified state machine and events, and `isTypeSupported()` that says `false` for what has no encoder
(MP4, VP9, H.264, AV1). Encoding runs off the JavaScript thread. Supported types, limits and tests: [mediarecorder.md](mediarecorder.md).

## Encrypted Media Extensions

Clear Key only (`org.w3.clearkey`), temporary sessions, in the MSE pipeline. Widevine, PlayReady and FairPlay reject
with `NotSupportedError`. See [media-streaming.md](media-streaming.md).

## File system

| API | Status | Notes |
|---|---|---|
| `navigator.storage.getDirectory()` (Origin Private File System), StorageManager | implemented | A memory-backed tree per origin, shared by every tab of that origin and kept across navigations for the life of the process (it is not written to the profile yet). 256 MiB quota per origin; `estimate()` reports it. |
| FileSystemHandle, FileSystemFileHandle, FileSystemDirectoryHandle | implemented | `getFileHandle`, `getDirectoryHandle` (`create`), `removeEntry` (`recursive`), `resolve`, `entries`/`keys`/`values`/async iteration, `getFile` (a `File` with a type guessed from the extension), `isSameEntry`, `move`, `remove`, permissions always `granted`. Errors are the specified DOMExceptions (`NotFoundError`, `TypeMismatchError`, `InvalidModificationError`, `TypeError` for bad names). |
| FileSystemWritableFileStream | implemented | A real `WritableStream`: `write` (strings, buffers, Blobs, `{type,data,position}`), `seek`, `truncate`, `keepExistingData`, atomic on `close()` (a failed or aborted stream leaves the file untouched), usable as a `pipeTo` target. |
| FileSystemSyncAccessHandle (`createSyncAccessHandle`) | absent | Dedicated-worker only in browsers, and the worker VM has none of these APIs. Follow-up together with worker support. |
| `showOpenFilePicker`, `showSaveFilePicker`, `showDirectoryPicker` | stub | Options are validated, then the promise rejects with `AbortError` ("The user aborted a request."), exactly what a dismissed dialog does. They need native dialog integration in the shell; tracked as a follow-up. |

## Hardware and sensors: intentionally absent

Pages feature-detect these by presence, so their absence is the correct answer
for a desktop engine without the hardware. They are listed in the coverage test
as names that must stay absent.

| Name | Status |
|---|---|
| `navigator.bluetooth`, `Bluetooth` | absent |
| `navigator.usb`, `USB` | absent |
| `navigator.hid`, `HID` | absent |
| `navigator.serial`, `Serial` | absent |
| `navigator.gpu`, `GPU`, `GPUAdapter`, `GPUDevice` (WebGPU) | absent |
| `navigator.xr`, `XRSystem` | absent |
| `NDEFReader` (Web NFC) | absent |
| Generic Sensor API (`Accelerometer`, `Gyroscope`, `Magnetometer`, `AbsoluteOrientationSensor`, `AmbientLightSensor`) | absent |

WebAssembly, IndexedDB, Cache Storage, Service Workers and WebRTC are owned by
other work streams and are neither listed nor asserted here.

## Notifications, push, background sync, locks

| API | Status | Notes |
|---|---|---|
| Notification, NotificationEvent | implemented | Secure contexts; `vibrate` ignored; see [notifications.md](notifications.md). |
| Permissions, PermissionStatus | implemented | `notifications`, `push`, `periodic-background-sync`, `background-sync` are answered; other names report `prompt` unless another module registers `__permHandlers[name]`. |
| PushManager, PushSubscription, PushEvent, PushMessageData, PushSubscriptionChangeEvent | implemented | Needs a configured push service ([push.md](push.md)); `aes128gcm` only. |
| SyncManager, SyncEvent, PeriodicSyncManager, PeriodicSyncEvent | implemented | [background-sync.md](background-sync.md). |
| navigator.setAppBadge, clearAppBadge | implemented | Secure contexts; the badge is reported to the app, no shell draws it. |
| navigator.locks, LockManager, Lock | implemented | Documents and service workers; not dedicated Workers. |

## Media, windowing and clipboard APIs added for video-call and player sites

| API | Status | Notes |
|---|---|---|
| Fullscreen API (`requestFullscreen`, `exitFullscreen`, `fullscreenElement`, `:fullscreen`, `fullscreenchange`) | implemented | The element is laid out to cover the viewport; the app gives the page the whole window and hides its chrome. Esc, F11, tab switch and navigation leave fullscreen. |
| Media Session | implemented | `metadata`, `playbackState`, action handlers, `setPositionState`; the app shows a now-playing popover whose buttons run the page's handlers (falling back to the media element). |
| `ManagedMediaSource` | implemented | `streaming`, `startstreaming`/`endstreaming`, `bufferedchange`; same demuxers as `MediaSource`. |
| `MediaCapabilities` for WebRTC | implemented | `decodingInfo` / `encodingInfo` with `type: 'webrtc'` admit VP8 and Opus only. |
| Async Clipboard API, copy/cut/paste events, `DataTransfer`, `FileList` | implemented | `readText` / `read` need a permission (prompted per site); `ClipboardItem` carries `text/plain`, `text/html` and images. |
| `WebSocket` | implemented | Own RFC 6455 client: fragmentation, binary frames up to the memory limit, close codes, ping/pong, `bufferedAmount`, subprotocols, cookies on the handshake. |
| `btoa` / `atob` | implemented in Go | Orders of magnitude faster than the JS loops they replaced. |
| IndexedDB cursors | faster | Read-ahead buffers 64 rows per host call; about 2x on cursor-heavy loops. |
| Page visibility | implemented | Background tabs report `document.hidden` and fire `visibilitychange`. |
| `navigator.userAgentData` | implemented | Truthful brands (the browser's own name, no Chromium impersonation). |
| `MediaStreamTrackProcessor`, `MediaStreamTrackGenerator`, `VideoTrackGenerator` | implemented | On the WebCodecs frame storage. Audio tracks yield `AudioData`. |
| Voice processing (echo cancellation, noise suppression, auto gain) | implemented | Applied to microphone audio on the way to a peer connection. |
| Document Picture-in-Picture, `ImageCapture`, File System Access pickers | absent / stubs | Pickers reject; the Origin Private File System works. |
| `navigator.hardwareConcurrency`, `deviceMemory` | fixed | Both report 1: pages see a small machine. |
