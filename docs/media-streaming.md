# Media playback and adaptive streaming

How `<video>`, `<audio>` and Media Source Extensions (MSE) work in Sweef, what was measured, what works on real
sites and what does not. Everything here is pure Go (`CGO_ENABLED=0`): the demuxers, the decoders, the colour
conversion and the audio output.

Measured on a 4 vCPU shared Linux VM (Go 1.26, load average about 1 while measuring), 2026-10-13. Numbers from
another machine will differ; the methods are at the end so they can be repeated.

## Architecture

```
page JS: HTMLMediaElement / MediaSource        internal/engine/media_prelude.js
   |  playerOpen / Play / Pause / Seek / Rate / Volume / ...    (commands)
   |  playerClock / playerStats                                  (reads)
   v
mediaPlayer (one per element)                  internal/engine/mediaplayer.go
   |-- audio loop: TrackStore audio samples -> AudioDecoder -> audioout.Stream      (the master clock)
   |-- video loop: TrackStore video samples -> VideoDecoder -> picture -> the element's Bitmap
   v
TrackStore (sorted coded frames)               internal/media/track.go
   ^-- progressive file: OpenTracks / OpenTrackSet (mp4, webm direct; mp3 indexed; wav, flac, ogg, aac decoded to PCM chunks)
   ^-- MSE: MSEBuffer = fMP4 / WebM segment parsers + coded frame processing       internal/media/mse*.go
```

* **The audio clock is the master.** `currentTime` is the position of the element's audio stream (frames the device
  has played, minus the output latency). The video loop shows the picture due at that time. With no audio track, or
  after the audio ended, a wall clock takes over. See [audio-output.md](audio-output.md) for the stream, mixer and
  tab mute.
* The JS side keeps the element state machine (readyState, events, `seeking`, `buffered`, ...); it asks the player
  for the clock and tells it what the element wants. The player never blocks the page: decoding runs on its own
  goroutines and pictures are published to the paint path (`Doc.TouchPaint`).
* A `<video>` always emits a bitmap op when painted, because its picture arrives after layout.

### Formats

| | |
|---|---|
| Containers | MP4/fMP4/M4A, WebM/Matroska, Ogg, WAV, FLAC, MP3, ADTS AAC |
| Video | H.264 (baseline to High, CABAC and CAVLC, B-frames), HEVC, VP8, VP9 (profile 0 to 3, tiles), AV1 |
| Audio | AAC (LC, HE), Opus, Vorbis, MP3, PCM; FLAC in files and `<audio>` (not in MSE) |
| MSE `isTypeSupported` | `video/mp4`, `audio/mp4`, `video/webm`, `audio/webm` with codecs from the tables above (FLAC in MSE is claimed by the type tables but has no streaming decoder); at most one video and one audio codec per SourceBuffer, a type without `codecs` is refused |

`navigator.mediaCapabilities.decodingInfo()` reports `supported` from the same tables and `smooth` from a
pixel-rate budget per codec (H.264 28 Mpx/s = 720p30, VP9 63 Mpx/s = 1080p30, AV1 and HEVC 14 Mpx/s), chosen from
the measurements below so that players such as YouTube, Shaka and dash.js pick a rendition the machine can play.
`powerEfficient` is always false (software decoding). `keySystemConfiguration` is never supported.

### Autoplay policy

Muted video (or volume 0, or a video without an audio track) may start without a gesture. Everything else needs
user activation, otherwise `play()` rejects with `NotAllowedError`. Activation comes from real input only: click,
mouse down/up, key, touch (mouse movement does not count). Muted `<audio>` is refused, as in Chromium. An
`AudioContext` created before activation stays `suspended`.

### MSE: what is implemented

* `MediaSource` states and events (`sourceopen`, `sourceended`, `sourceclose`), `duration` rules, `endOfStream`
  (including `network`/`decode` errors), `setLiveSeekableRange`, `isTypeSupported`.
* `SourceBuffer`: `appendBuffer` with an asynchronous update (`updatestart`, `update`, `updateend`, `error`),
  `abort`, `remove`, `changeType`, `mode` (`segments` and `sequence`), `timestampOffset`, `appendWindowStart/End`,
  `buffered`, `updating`. The segment parsers are incremental (fragments may arrive in any chunking). Init
  segments are validated against the type; a bad segment raises `error` and ends the stream with a decode error.
* Coded frame processing per the spec: timestamp offset, append window trimming, discontinuity detection, the
  random-access-point flag, overlapping frames replaced, `buffered` merged with a small gap tolerance (the first
  range starting within a second of 0 is treated as starting at 0, as Chromium does for audio priming).
* Quota and eviction: 150 MB for video and 12 MB for audio per SourceBuffer (`SWEEF_MSE_QUOTA` overrides, for tests).
  An append that does not fit evicts behind the playhead first, whole GOPs at a time; if that is not enough
  `appendBuffer` throws `QuotaExceededError`.
* Seeking inside the buffered range is immediate; seeking outside waits (`readyState` drops to `HAVE_METADATA`,
  `waiting`) and resumes when data arrives. `readyState` follows what is buffered at the playhead
  (`HAVE_CURRENT_DATA` to `HAVE_ENOUGH_DATA`) and playback stalls and resumes when the buffer runs dry and
  refills. Quality switches (a new init segment with a different configuration, in the same or a new
  SourceBuffer) reconfigure the decoder at the next sample.
* Tests build fMP4 H.264+AAC in two qualities, WebM VP9+Opus and fMP4 AV1 fixtures with ffmpeg
  (`internal/engine/testdata/media/mse/gen.sh`, a few tens of KB, committed) and assert the buffered ranges, the
  pixels of the picture shown, the PCM that reaches the audio sink, seeking, quality switch and end of stream
  (`internal/engine/mse_play_test.go`, `internal/media/mse_test.go`, `mse_decode_test.go`).

### Not implemented

* `ManagedMediaSource` and `MediaSource` in dedicated workers (`MediaSource.canConstructInDedicatedWorker` is
  false, `MediaSourceHandle` does not exist). Skipped on purpose; players fall back to window MSE.
* `SourceBuffer.audioTracks/videoTracks/textTracks`, `appendBufferAsync`/`removeAsync`, `stalled` events for MSE.
* **EME / DRM (`requestMediaKeySystemAccess` always rejects, `setMediaKeys` rejects).** Netflix, Disney+, Prime
  Video, YouTube Premium music/movies and every other DRM-protected stream **cannot be played**. The EME interface
  objects (`MediaKeys`, `MediaKeySession`, ...) exist so that player libraries do not crash before being refused.
* Pitch-preserving time stretch: `playbackRate` resamples (the pitch changes). `preservesPitch` is accepted and
  ignored. Audio is muted for rates outside 0.25 to 4; video follows the rate.
* Picture-in-picture, Remote Playback, captions rendering from MSE text tracks, HLS (hls.js uses MSE and works in
  principle; it was not tested here), WebRTC audio (the `audioout` API is ready for it).
* The real sound card path (`oto`) is compile-checked on Linux, Windows and macOS but was never run on hardware in
  this work; every measurement here used the null sink, which keeps the audio clock running in real time.

## Decode speed

Method: `internal/media/decodebench_test.go` decodes every frame in order and reports the median and 95th
percentile of the time per frame. Clips are 20 s of synthetic `testsrc2` plus temporal noise (harder to compress
than most real content), 30 fps, a key frame every 2 s, B-frames for H.264. `ffmpeg` command lines are in
`scratchpad`-style form at the end.

| Clip | fps (whole run) | ms/frame median | p95 | max | CPU ms/frame | RGBA conversion |
|---|---|---|---|---|---|---|
| H.264 High 720p30, 3 Mbit/s | 58 | 14.7 | 31.5 | 102 | 23 | 1.8 ms |
| VP9 720p30, 2 Mbit/s | 110 | 8.2 | 14.8 | 102 | 24 | 1.9 ms |
| H.264 High 1080p30, 6 Mbit/s | 30 | 29.6 | 54.0 | 178 | 44 | 3.6 ms |
| VP9 1080p30, 4 Mbit/s | 55 | 17.1 | 27.0 | 57 | 50 | 4.2 ms |
| AV1 720p30, 2 Mbit/s (SVT-AV1) | 32 | 15.1 | 165.6 | 293 | 31 | 1.7 ms |

"CPU ms/frame" is process CPU time per frame (all goroutines); it is higher than the median where the decoder uses
several goroutines (VP9 tile columns and loop filter, H.264 deblocking, colour conversion in bands).

What was optimised, from profiles:

* VP9: loop filter as a wavefront over superblock rows and planes, linear sliding-window wide filter, motion
  compensation with hoisted filter taps, tile columns on their own goroutines (identical output to the sequential
  decoder, tested), banded colour conversion. 1080p30 went from about 10 fps to about 55 fps on this machine.
* H.264: the deblocking filter (about a quarter of the time) runs as a wavefront on up to four goroutines;
  output identical to the serial filter (`TestH264ParallelDeblockMatchesSerial`, also under `-race`). 1080p went from
  25 to 30 fps. Motion compensation (about 35%) and CABAC residual parsing remain the large items; the decoder is
  generic over bit depth and chroma format (16-bit sample planes), which costs memory bandwidth.

## Playback test: 20 s through the whole page path

`TestPlaybackQuality` plays a muted looping clip in a real tab for 20 s (decoder, picture delivery, the page's
`getVideoPlaybackQuality()`), 2026-10-13, load average about 1:

| Clip | frames reported | dropped | picture interval median / p95 / max | picture lateness p95 / max | CPU (one core = 100%) |
|---|---|---|---|---|---|
| H.264 720p30 | 600 | 6 (1.0%) | 33.3 / 35.7 / 157 ms | 3 / 179 ms | 71% |
| VP9 720p30 | 601 | 2 (0.3%) | 33.4 / 35.7 / 93 ms | 3 / 46 ms | 83% |
| H.264 1080p30 | 597 | 52 (8.7%) | 33.7 / 55.0 / 222 ms | 365 / 520 ms | 129% |
| VP9 1080p30 | 601 | 3 (0.5%) | 33.3 / 36.2 / 104 ms | 6 / 78 ms | 159% |

* The drops in the first three rows happen when the clip loops (decoding restarts at the first key frame) and, for
  1080p H.264, when the decoder cannot keep up. **H.264 1080p30 is not real time on this machine**: it decodes at
  about 30 fps in isolation and about 22 fps while the rest of the page runs, so it falls behind, sheds the
  late frames and resynchronises (below). It is smooth on a machine with more or faster cores; this one is not.
* Policy when the decoder runs late (`internal/engine/mediaplayer.go`): pictures are decoded ahead of the clock into a
  short queue (so a key frame that takes three frame times no longer makes the following pictures late; before
  this, 720p30 H.264 with B-frames dropped 40% of its frames); a picture that is already late and that nothing refers
  to (a non-reference H.264 B-frame) is skipped without being decoded; a late picture that is decoded anyway is
  still shown (moving video behind the sound beats a frozen frame); when the clock is more than 250 ms ahead of
  the decoder, decoding continues from the newest key frame that is already due, which cuts the delay to that key
  frame's age and never freezes the picture. Skipped pictures count as dropped in `getVideoPlaybackQuality()`,
  which is what quality-adapting players read.
* Earlier versions of this policy froze the picture for up to a GOP when they jumped to a *future* key frame
  (1080p H.264: 92% dropped, 2 s freezes); the numbers above are for the current one.

`getVideoPlaybackQuality()`: `totalVideoFrames` counts the frames due since the element started (in presentation
order, so B-frame reordering does not double count), `droppedVideoFrames` those that were skipped, `corruptedVideoFrames`
the access units the decoder rejected, `creationTime` the element's creation time.

## Real sites

Checked with `bench/mediasite` (the tool prints what the player probes, what is appended to which SourceBuffer, the
element's state every second, console errors and failed requests). Dates are when I ran it; sites change.

| Site | 2026-10-13 | Notes |
|---|---|---|
| dash.js reference sample (`reference.dashif.org`, Big Buck Bunny DASH, H.264 + AAC) | **plays**, picture and sound path active | After a click: `currentTime` advances 1.00 s per second, 480x270 to 540p renditions, 1115 frames in 37 s, 1 dropped, buffer grows to 52 s, no console errors. Without a click `play()` is refused with `NotAllowedError` like in Chromium (the sample autoplays unmuted). |
| Shaka Player 4.3.5, minimal page (angel-one DASH, VP9 720x576 + Opus WebM) | **plays** | `isBrowserSupported()` true, manifest parsed, 942 frames in 37.7 s, 0 dropped, buffer eviction behind the playhead works. Needed the fixes listed below (XML documents, EME interfaces, `playbackRate = 0`). |
| Shaka Player official demo page (`shaka-project.github.io/shaka-player/demo`) | **page runs, playback not exercised** | The UI builds (needed SVG interfaces and the URL fix); the "custom asset" flow needs UI interaction that I did not script. Locale `en-US` 404 is normal. |
| MDN `<video>` page | **loads, nothing to play in the page itself** | The examples are in an iframe (`interactive-examples.mdn.mozilla.net`) whose HTML is a template filled in by the editor script. A `<video>` pointed at the same `flower.webm` from MDN's server plays (960x540, 150 frames in 5.06 s, 0 dropped, ends). |
| YouTube embed (`/embed/aqz-KE-bpKQ`, Big Buck Bunny 60fps 4K) | **does not play** | See below. |
| YouTube watch page (`/watch?v=aqz-KE-bpKQ`) | **blocked** | Google answers with HTTP 429 "unusual traffic" (`google.com/sorry`) for this network's address, to `curl` as well as to the engine. Not an engine problem; it could not be tested from here. |
| Netflix, Disney+, Prime Video, YouTube Premium music/movies | **cannot work** | They need EME/DRM, which is not implemented. |

YouTube embed in detail. Without a `Referer` the embedded player refuses to start ("Video player configuration
error", a YouTube policy for embeds); with one (`-referer https://example.com/`) it initialises, the BotGuard
attestation script runs (it needed a JavaScript heap above 64 MiB, now the default is 256 MiB), `GenerateIT` returns
200, `/youtubei/v1/player` returns 200 with a gzip-compressed request body (it uses `CompressionStream`), the player
creates a MediaSource, probes 36 types, asks `decodingInfo` 17 times and adds an `audio/webm; codecs="opus"` and a
`video/webm; codecs="vp09.00.51.08..."` SourceBuffer. Then it POSTs its SABR requests to `googlevideo.com`
(`application/vnd.yt-ump`); the server answers with 1.1 KB (a redirect to another host) and 154 bytes (a context
update) and no media, and the player ends in "Video unavailable". I could not find a cause in the engine. The most
likely ones are the proof-of-origin token that SABR streaming requires from web clients (the attestation runs but
its output may not satisfy the server) and the address of this network. Nothing was appended to a SourceBuffer, so
the MSE path was not reached on YouTube.

### What the real sites broke (fixed, each with a test)

* `fetch`/`XMLHttpRequest` dropped **every custom request header** (a JS object reaches Go as `*quickjs.Object`,
  not a map) and sent `ArrayBuffer`, typed array and `Blob` bodies through a UTF-8 round trip, corrupting gzip and
  protobuf payloads; `Response.body` and `clone()` did the same to binary responses (`TestFetchSendsRequestHeaders`).
* `CompressionStream`/`DecompressionStream` (gzip, deflate, deflate-raw) were missing, and `ReadableStream`,
  `WritableStream` and `TransformStream` were stubs that ignored the transformer: replaced by real implementations,
  `Response` accepts a stream as a body.
* No microtask checkpoint ran between classic scripts, so a script that set up state in a promise callback was not
  ready for the next `<script>` (the YouTube embed's `writeEmbed()`).
* `DOMParser`/`responseXML` for XML types returned an HTML tree (tag names lowercased, no namespaces). They now give a
  namespace-aware XML document with `parsererror` handling, and the nodes pass `instanceof Element` (Shaka filters
  a manifest's `childNodes` with it) (`TestDOMParserXML`).
* `SVGPathElement` and the other SVG element interfaces, and the EME interfaces, did not exist (a `ReferenceError`
  in Shaka's UI and polyfills).
* `new URL("https:../x", "https://h/a/b/")` must be relative (Shaka writes locale URLs that way); it produced
  `https://../x`.
* `video.playbackRate = 0` threw (it is valid).
* Inline event handler attributes (`onclick="..."`) never ran, only `addEventListener` and the `on*` properties did.
* The default JavaScript heap is 256 MiB instead of 64.

### Known problems found on the way, not fixed

* A CSS `src: url(...) format('woff2')` list on YouTube's embed page is parsed so that one font URL ends in
  `)format('woff2'` (a 404).
* Web Audio `AudioContext.currentTime` is driven by the wall clock, not by the audio device.

## Reproducing

```
# decode speed of a clip
SWEEF_BENCH_VIDEO=clip.mp4 SWEEF_BENCH_FRAMES=450 go test -run TestDecodeSpeed -v ./internal/media

# 20 s through the page: dropped frames, picture intervals, lateness, CPU
SWEEF_AUDIO=null SWEEF_BENCH_VIDEO=clip.mp4 SWEEF_BENCH_SECONDS=20 go test -run TestPlaybackQuality -v ./internal/engine

# a real site (needs the network; real sites change)
go run ./bench/mediasite -url 'https://reference.dashif.org/dash.js/latest/samples/getting-started/auto-load-single-video-src.html' -seconds 40 -click video
go run ./bench/mediasite -url 'https://www.youtube.com/embed/aqz-KE-bpKQ' -referer https://example.com/ -seconds 40 -click .ytmCuedOverlayPlayButton
go run ./bench/mediasite -serve page.html -seconds 40      # your own page, served locally
```

`bench/mediasite` flags: `-ua`, `-referer`, `-js-mem`, `-eval`, `-eval-end`, `-play`, `-click`; `MEDIASITE_NET=300` prints
the network log, `MEDIASITE_CONSOLE=900` widens console lines, `MEDIASITE_TIMELINE=1` prints the script timeline.

The benchmark clips (`testsrc2` with `noise=alls=12:allf=t`, 20 s, `-g 60`):

```
ffmpeg -f lavfi -i "testsrc2=size=1920x1080:rate=30,noise=alls=12:allf=t" -t 20 -c:v libx264 -preset medium -profile:v high -b:v 6M -pix_fmt yuv420p -g 60 h264_1080.mp4
ffmpeg -f lavfi -i "testsrc2=size=1920x1080:rate=30,noise=alls=12:allf=t" -t 20 -c:v libvpx-vp9 -b:v 4M -deadline good -cpu-used 4 -row-mt 1 -tile-columns 2 -g 60 vp9_1080.webm
```

(720p: `-b:v 3M` for H.264, `-b:v 2M` for VP9.) The committed fixtures are small: `internal/engine/testdata/media/mse/`
(fMP4 H.264+AAC in two qualities, WebM VP9+Opus, fMP4 AV1; `gen.sh` regenerates them) and
`h264_wave_320x192.mp4` (B-frames, 20x12 macroblocks, used for the deblocking and playback tests).
