# Site compatibility notes

What is known not to work, why, and what was checked. Measured in the benchmark environment of
`docs/data-saver-bench.md` (a cloud sandbox: all traffic leaves through one egress gateway that terminates TLS), so the
network-level findings are about that path; nothing here is a claim about every network.

Sweef sends the headers an ordinary browser sends (`Accept` with only the image formats it decodes,
`Accept-Language`, `Sec-Fetch-Dest/Mode/Site/User`, `Upgrade-Insecure-Requests`, `Referer` under the default
`strict-origin-when-cross-origin` policy) and a truthful `User-Agent` (the embedder's; the desktop app's is
`Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Safari/537.36 Sweef/0.1`). It does not send
Client Hints, does not copy Chromium's TLS or HTTP/2 fingerprint, and does not try to pass bot-detection challenges.
A site that decides to refuse such a client is listed below as a limitation instead of being worked around.

## Fixed

| Site | Symptom | Cause | Fix |
|---|---|---|---|
| github.com | `403` on the first request | GitHub rejects navigations that carry no `Sec-Fetch-*` headers. Same request with curl and the Sweef user agent: no extra headers 403, `Accept-Language` alone 403, `Upgrade-Insecure-Requests` alone 403, `Sec-Fetch-Dest/Mode/Site/User` alone 200. | The engine now sends the ordinary navigation headers (`browserheaders.go`). |
| bbc.com/news | page never finished loading (60 s timeout in `off` and `standard`) | A lock-order inversion in the worker support: a starting `Worker` holds its VM lock while its script runs and then pushes the result to the page's event queue; the page loop asked "do workers have work?" while holding the queue lock and waiting for the same VM lock. The goroutine dump of the hung process shows `hasLiveWork` and `workerCreate` waiting on each other. `strict` finished because it runs no third-party scripts. | `hasLiveWork` no longer takes a worker's VM lock (the inbox has its own lock). Regression tests in `worker_deadlock_test.go`. |

## Known limitations

### stackoverflow.com, allrecipes.com (Cloudflare managed challenge)

The first response is `403` with `Cf-Mitigated: challenge` and `Server: cloudflare`; the body is Cloudflare's
interactive JavaScript challenge (`/cdn-cgi/challenge-platform/.../orchestrate/chl_page`). It is decided by Cloudflare
from the client's network, TLS and behaviour, not from a header we omit: the response is identical with the headers
above, with Chromium's user agent, and with curl's.

Evidence from the same benchmark run (`bench/datasaver/results/2026-10-13-full.json`, status of the main document):

* stackoverflow.com: `403` in Sweef (off, standard, strict) and in Chromium and Chromium with `Save-Data`.
* allrecipes.com: `403` in Sweef and in plain Chromium, `200` in Chromium with `Save-Data` on the same run: the
  decision is not stable even for one client.

Both are therefore listed as excluded in the benchmark for every browser. Passing the challenge would mean emulating a
Chromium client, which Sweef does not do.

### ebay.com (Akamai Bot Manager)

`403` with `Server: AkamaiGHost`, `X-Cdn: Akamai`, a `bm_s` bot-manager cookie and a 1.8 KB error page, with or without
the headers above. Chromium also gets `403` in the same environment (benchmark results, main document status for
chromium and chromium-savedata). Not worked around.

### Dynamic imports in bundled modules

On some Vite/Rolldown sites (lonelyplanet.com seen) a `import("./chunk.js")` inside a module is requested relative to
the page URL instead of the module, which answers `404` (a handful of small requests per page). Not fixed here: it is module-resolution behaviour in `jsvm.go`, outside the network layer.

### WebAssembly: supported, with limits

The `WebAssembly` global exists (`Module`, `Instance`, `Memory`, `Table`, `Global`, `validate`, `compile`, `instantiate`,
the streaming variants and the three error classes) in pages and Web Workers, running modules as native code through
wazero. Sites that only need the core API work; sites that need `WebAssembly.Tag` or
`externref` at the JS boundary do not, and a module that crosses into JS tens of thousands of times per frame is slow
because each crossing costs microseconds. Threads and `SharedArrayBuffer` work on cross-origin isolated pages (COOP + COEP), see [web-platform.md](web-platform.md). Full list, design and measured costs: [webassembly.md](webassembly.md).
No real site was run for this entry; the evidence is the hand-assembled modules in `internal/engine/wasm_test.go`.

## Google Meet, WhatsApp Web and YouTube: platform APIs

None of these three sites was run against its live service for this entry: the sandbox has no accounts, no camera
and no route to the services' media servers, and Sweef does not try to get around sign-in, DRM or anti-abuse checks.
What exists is a set of feature-detection pages and tests (`internal/engine/compat_sites_test.go`) that run the checks
the sites' own scripts make at start-up against local `httptest` servers, plus per-API tests. "Implemented" below means
the API exists, behaves as specified in those tests and (for the media paths) was exercised between two Sweef peers.
"Untested live" means nothing more than that.

| Need | Site | State |
|---|---|---|
| `getUserMedia` with echoCancellation / noiseSuppression / autoGainControl | Meet, WhatsApp calls | Implemented: partitioned frequency-domain echo canceller, spectral noise suppression and AGC on the send path (`internal/audioproc`), reported in `getSettings` / `getCapabilities`. Not measured against Chromium's WebRTC AEC3; no double-talk detector beyond the canceller's own divergence guard. |
| `RTCPeerConnection`: offer/answer, ICE, DTLS-SRTP, data channels, `getStats`, `jitterBufferTarget`, codec preferences, simulcast send, encoded transforms (`RTCRtpScriptTransform`, insertable streams) | Meet, WhatsApp | Implemented, interoperating with pion peers in tests and with Sweef itself. Codecs: VP8 and Opus only. Real-time H.264 is in the separate H.264 work (`docs/webrtc.md`); Meet and WhatsApp offer VP8/VP9/H.264/AV1 and need one in common, which VP8 provides, but this was not tried against their servers. |
| Screen sharing, `getDisplayMedia` | Meet | Implemented where the host app provides a source; untested live. |
| `MediaStreamTrackProcessor`, `MediaStreamTrackGenerator`, `VideoTrackGenerator` | Meet (effects), WhatsApp | Implemented on the WebCodecs `VideoFrame` / `AudioData` storage; tested with a fake camera and over a peer connection. |
| `MediaRecorder` | WhatsApp voice notes | **Not implemented**: the constructor exists and `start()` throws `NotSupportedError`. Voice-note recording does not work. |
| Web Audio (`AudioContext`, worklets, `MediaStreamAudioSourceNode`, `MediaStreamAudioDestinationNode`) | Meet, WhatsApp | Implemented. Worklets run in the page's VM, not on a separate audio thread. |
| `OffscreenCanvas`, `requestVideoFrameCallback` | Meet (backgrounds) | Implemented; WebGL-based segmentation models are limited by the software WebGL. |
| Fullscreen API | YouTube, Meet | Implemented: the element covers the window, the app hides its chrome, Esc/F11 leave. |
| Media Session | YouTube | Implemented and wired to a now-playing control in the app chrome. |
| `MediaSource` / `ManagedMediaSource`, `isTypeSupported`, `MediaCapabilities` | YouTube | Implemented for the codecs the engine decodes (VP8/VP9 video where available, Opus/Vorbis/AAC audio as per `docs/media.md`). YouTube's default streams are H.264 or AV1 and encrypted for some content: those are not decodable here, so the site's player falls back to what `isTypeSupported` admits. Untested live. |
| Autoplay policy, `getVideoPlaybackQuality`, Picture-in-Picture | YouTube | Implemented (autoplay needs user activation or muted playback; PiP is a stub window API). |
| Notification, Push, Permissions, Clipboard (async, with a permission prompt) | WhatsApp Web | Implemented; no push service, so no push message ever arrives. |
| `BroadcastChannel`, Web Locks, IndexedDB (cursor read-ahead), `CompressionStream`, `crypto.subtle`, `WebSocket` binary and large frames | WhatsApp Web | Implemented and tested. WhatsApp's own end-to-end crypto and noise-protocol handshake run in the site's JavaScript or WebAssembly and were not tried. |
| `Intl` (NumberFormat, DateTimeFormat, PluralRules, RelativeTimeFormat, ListFormat, DisplayNames, Collator, Segmenter) | all three | Implemented for 73 locales on CLDR 47 data, compared with Node 22 in `internal/engine/intl_test.go`; limits in [web-platform.md](web-platform.md). |
| Widevine / PlayReady DRM | YouTube (some titles), Meet (none) | Not available. Clear Key only. Sweef does not bypass DRM. |

Not claims: that YouTube plays, that a Meet call connects, or that WhatsApp Web links a phone. Each site also runs
bot and client checks Sweef does not answer (see the Cloudflare and Akamai entries above) and its own sign-in.
