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
| youtube.com (search, watch) | Only grey skeleton boxes; then, with the script cut at 8 MiB fixed, an `Uncaught` with an empty message inside the Shady DOM polyfill and nothing rendered | A chain of DOM differences, each one hiding the next: the Shady DOM polyfill assigns `this.host` on a `DocumentFragment` subclass but `host`/`mode` were getter-only accessors on `DocumentFragment.prototype`; its copies of the `Node.prototype` accessors returned nothing for fragments (every shadow root looked empty); `<iframe sandbox="allow-same-origin" src="about:blank">` had no scripting context for `contentWindow.history`; custom elements inside `<template>` were constructed with their `[[bindings]]` still in the attributes; `Element.attributes` was not array-like (`Array.prototype.map.call` returned holes); `is` was an attribute and `data`, `labels`, `href`, ... existed on every element, which made Polymer Resin replace the real `data` of every custom element with its placeholder `zClosurez`; a `position:fixed` box inside an absolutely positioned one was never laid out (the masthead had no size); a lowercase duplicate `viewbox` overrode the real `viewBox` of inline SVG (the logo); the `popover` attribute did nothing (a tooltip painted as a grey square); a bound method borrowed from an iframe's `History` ran in the iframe (`pushState` failed with "origin 'null'"); a task running longer than 5 s was interrupted, which left the page half-built. | See the section "youtube.com" below and the tests in `internal/engine/polyfill_compat_test.go` and `internal/render/fixed_in_abs_test.go`. |

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

### youtube.com

Measured on 2026-10-14 from a Linux cloud sandbox (headless, `sweef` built from this tree, a shared 4-core machine; the
sandbox's egress IP is flagged by Google, see below). Times are wall-clock from navigation start and vary a lot with load.

**What loads.**

* `https://www.youtube.com/results?search_query=music`: the app shell renders (masthead with logo, search box and
  filter chips), real results appear with titles, channel names, view counts, durations, avatars and real thumbnails
  (`i.ytimg.com` JPEGs decoded and painted). 4 to 8 result rows are rendered at first (YouTube adds more on scroll), the
  first thumbnails and titles after about 15 to 40 s (the 10.8 MB main script runs in QuickJS, no JIT). The console shows
  no error. Screenshots were taken with `Tab.PNGScreenshot`.
* `https://www.youtube.com/`: the shell renders (masthead, mini guide labels, the "Try searching to get started" card),
  **but no video grid: the server sends none.** The page's own data (`ytInitialData`) holds a `feedNudgeRenderer` and no
  `richItemRenderer`; the raw HTML from `curl` is the same (0 `richItemRenderer`, 2 `feedNudgeRenderer`). That is YouTube's
  answer to a visit without cookies or history from this network, not a rendering failure. Not tried: with consent or
  sign-in cookies (Sweef does not automate either).

**What plays.**

* Opening a result from the search page (YouTube's in-page navigation, no top-level `/watch` request) shows the full
  watch page (title, channel, description, related videos with thumbnails) and, after `playVideo()` (muted: autoplay
  needs a user gesture, as in Chromium), the player reaches state 1 (playing) through MSE: `blob:` source, 854x480,
  `currentTime` advancing in real time (11.6 s and 13.5 s at two readings about 2 s apart), 36 s buffered, 332 frames of which the
  playback-quality API reports 81 dropped (AV1 decoded in software, `av01.0.04M.08` video and Opus audio, as the player's stats say). The
  frame painted in the page is the right one (Rick Astley, "Never Gonna Give You Up").
* The same player in an `<iframe src="https://www.youtube.com/embed/...">` of a local page: playing, 854x480, 170 frames
  in 10 s with 41 dropped.
* **Audio was not heard.** The sandbox has no sound device (`audio: no sound output ... playing silently`). The Opus
  stream is fetched and the audio pipeline runs, but nothing was verified by ear or by capturing the output.

**What was not tested.**

* A top-level load of `https://www.youtube.com/watch?v=...` (even `curl` gets `302` to `https://www.google.com/sorry/index`,
  Google's unusual-traffic check, from this network). Sweef does not answer or work around that check, so the watch page
  was only reached through the in-page navigation above. On a network that is not flagged the top-level load takes the
  same path as the in-page one, but this was not run.
* Widevine/DRM titles (not available), signed-in views, Shorts, live streams, the unmuted autoplay path with a real
  click.

**Still wrong on these pages.**

* Several icons are missing: the mini guide's Home/Shorts/Subscriptions/You icons and buttons that use the `yt-icons`
  icon set, whose `<defs>` is empty in the page's markup; the icon names resolve but nothing is drawn (cause not found).
  The hamburger, search, logo and player controls draw.
* Emoji in titles show as empty boxes (no colour emoji font).
* A text placeholder (`1234567890...`, a width-measuring element) is visible in the video info row on the watch page,
  the right end of the masthead (sign-in button) is clipped, and the like/share buttons are unstyled blobs.
* Performance: first paint of content takes tens of seconds on a loaded machine, and software-decoded 480p AV1 drops
  about a quarter of the frames. A single script task is now allowed 30 s (it was 5 s: a task cut off at 5 s left the
  watch page at "Video unavailable"); `--js-timeout-ms` sets it.
* Shadow DOM itself is still the flat model described in [web-platform.md](web-platform.md); Polymer works because
  YouTube loads the Shady DOM polyfill, not because of native shadow roots.

### WhatsApp Web: voice notes (MediaRecorder)

Before this change `MediaRecorder.start()` threw `NotSupportedError`, so the voice-note button could not record. Now
`getUserMedia({audio})` followed by `new MediaRecorder(stream, {mimeType: 'audio/ogg; codecs=opus'})` (or WebM Opus, the usual
fallback) records, delivers `dataavailable` and `stop` in the right order, and the resulting `Blob` plays through
`<audio>.src = URL.createObjectURL(blob)` with the right duration. That exact flow is a test against a local server
(`TestMediaRecorderWhatsAppVoiceNoteFlow`) with the synthetic microphone; the encoded files also decode with ffmpeg.

**WhatsApp Web itself was not run.** Unverified: whether its page code gets as far as the composer (it needs a linked phone,
Workers, IndexedDB at scale, WebAssembly and WebSocket traffic that were not exercised), what MIME type and bitrate it asks
for, whether it re-muxes or analyses the audio with `AudioContext.decodeAudioData` (the repo decodes Ogg Opus and WebM Opus),
and whether it uploads the blob in a form its servers accept. Video notes and calls are separate (WebRTC, see
[webrtc-interop.md](webrtc-interop.md)). Details and limits: [mediarecorder.md](mediarecorder.md).

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

Google Meet and WhatsApp Web were not run against their live services for this entry: the sandbox has no accounts and no camera,
and Sweef does not try to get around sign-in, DRM or anti-abuse checks. YouTube was (see "youtube.com" above); its rows below say what was played.
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
| `MediaRecorder` | WhatsApp voice notes | Implemented (WebM VP8/Opus, Ogg Opus); the voice-note flow works against the synthetic microphone, **not tried on the real site**. See [mediarecorder.md](mediarecorder.md). |
| Web Audio (`AudioContext`, worklets, `MediaStreamAudioSourceNode`, `MediaStreamAudioDestinationNode`) | Meet, WhatsApp | Implemented. Worklets run in the page's VM, not on a separate audio thread. |
| `OffscreenCanvas`, `requestVideoFrameCallback` | Meet (backgrounds) | Implemented; WebGL-based segmentation models are limited by the software WebGL. |
| Fullscreen API | YouTube, Meet | Implemented: the element covers the window, the app hides its chrome, Esc/F11 leave. |
| Media Session | YouTube | Implemented and wired to a now-playing control in the app chrome. |
| `MediaSource` / `ManagedMediaSource`, `isTypeSupported`, `MediaCapabilities` | YouTube | Implemented for the codecs the engine decodes (H.264, HEVC, VP8, VP9, AV1 video; AAC, Opus, Vorbis, MP3 audio, see [media-streaming.md](media-streaming.md)). Played live on 2026-10-14: YouTube's player chose AV1 (`av01.0.04M.08`) and Opus, fed them through `MediaSource`, and the video advanced in real time on the watch page and in the embed player (about a quarter of the frames dropped at 854x480 in software); the sound could not be checked (no audio device in the sandbox). The H.264 and VP9 paths and encrypted streams were not exercised live. Details in the "youtube.com" section above. |
| Autoplay policy, `getVideoPlaybackQuality`, Picture-in-Picture | YouTube | Implemented (autoplay needs user activation or muted playback; PiP is a stub window API). |
| Notification, Push, Permissions, Clipboard (async, with a permission prompt) | WhatsApp Web | Implemented; no push service, so no push message ever arrives. |
| `BroadcastChannel`, Web Locks, IndexedDB (cursor read-ahead), `CompressionStream`, `crypto.subtle`, `WebSocket` binary and large frames | WhatsApp Web | Implemented and tested. WhatsApp's own end-to-end crypto and noise-protocol handshake run in the site's JavaScript or WebAssembly and were not tried. |
| `Intl` (NumberFormat, DateTimeFormat, PluralRules, RelativeTimeFormat, ListFormat, DisplayNames, Collator, Segmenter) | all three | Implemented for 73 locales on CLDR 47 data. Against Node 22 (ICU 77) on seeded matrices: NumberFormat 99.6%, DateTimeFormat 98.3% (ranges 98.1%, calendars 98.1%), Collator 99.85%; Thai, Lao, Khmer, Burmese, Chinese and Japanese word breaking, `chinese`/`dangi` calendars and non-English time zone city names are the gaps ([web-platform.md](web-platform.md)). |
| Widevine / PlayReady DRM | YouTube (some titles), Meet (none) | Not available. Clear Key only. Sweef does not bypass DRM. |

Not claims: that a Meet call connects, that WhatsApp Web links a phone, or that YouTube plays everything (DRM titles, a top-level watch load from a flagged network, and audible sound were not shown). Each site also runs
bot and client checks Sweef does not answer (see the Cloudflare and Akamai entries above) and its own sign-in.
