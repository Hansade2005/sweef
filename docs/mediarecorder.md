# MediaRecorder

`MediaRecorder` and `BlobEvent` record a `MediaStream` into WebM or Ogg, in pure Go (Opus and VP8 encoders that were
already in the tree, no cgo, no new dependency). Voice notes, in-page screen or camera recordings and canvas or Web
Audio captures work; `MediaRecorder.start()` no longer throws.

Files: `internal/engine/platformjs/recorder.js` (the API: state machine, events, options), `internal/engine/mediarecorder.go`
(sessions, which track feeds what), `mediarecorder_wa.go` (Web Audio tap), `internal/mediarec` (resampler, Opus mixer, VP8
loop, WebM and Ogg writers, interleaver; knows nothing about JavaScript).

## What can be recorded

| `mimeType` | Result |
|---|---|
| `audio/webm`, `audio/webm;codecs=opus` | WebM, one Opus track |
| `video/webm`, `video/webm;codecs=vp8`, `video/webm;codecs=vp8,opus`, `video/webm;codecs=opus` | WebM with VP8 and/or Opus, whatever the stream has |
| `audio/ogg`, `audio/ogg;codecs=opus` | Ogg Opus (RFC 7845: `OpusHead`, `OpusTags`, granule positions, end trimmed to the real length) |
| none given | `audio/webm;codecs=opus`, `video/webm;codecs=vp8` or `video/webm;codecs=vp8,opus`, by the tracks present; `recorder.mimeType` is `""` until `start()`, like Chromium |

`MediaRecorder.isTypeSupported()` answers `false` for everything else: `audio/mp4`, `video/mp4`, `audio/mpeg`, VP9, H.264,
AV1, Vorbis, Ogg with video. A page that asks for MP4 (Safari-style code paths) must fall back to WebM or Ogg like it does
in Chromium and Firefox.

Sources that work:

* `getUserMedia` microphone and camera tracks and `getDisplayMedia` tab or window tracks (the same `capture.Track` the page
  holds, so `enabled = false` records black frames or silence exactly as it does for WebRTC).
* The track of `AudioContext.createMediaStreamDestination()`. The graph renders on the page's thread; a recorder reading
  such a track makes the context render in real time even when nothing else needs it, and samples are timed by the
  context's own clock, not the wall clock.
* `canvas.captureStream(fps)` of this page (sampled at `fps`, default 30).

Sources that do not: `HTMLMediaElement.captureStream()` (its tracks are placeholders), remote `RTCPeerConnection` tracks,
`MediaStreamTrackGenerator`. A stream with only such tracks makes `start()` fire `error` (`NotSupportedError`). Only the
first video track is recorded; several audio tracks are mixed (summed, hard-limited).

## API behaviour

* Constructor: `TypeError` for a non-`MediaStream`; `NotSupportedError` for an unsupported `mimeType`; `TypeError` for a bad
  `audioBitrateMode`. Options: `mimeType`, `audioBitsPerSecond` (default 128000), `videoBitsPerSecond` (default 2500000),
  `bitsPerSecond` (split like Chromium: all to the one kind, or up to 128 kbit/s to audio and the rest to video),
  `audioBitrateMode` (`"constant"` or `"variable"`, default `"variable"`; Opus CBR or VBR). Properties: `stream`, `mimeType`,
  `state`, `audioBitsPerSecond`, `videoBitsPerSecond`, `audioBitrateMode`; handlers `onstart`, `ondataavailable`, `onstop`,
  `onpause`, `onresume`, `onerror`.
* State machine: `start()` in any state but `inactive` throws `InvalidStateError`; `pause()`, `resume()` and `requestData()`
  on an `inactive` recorder throw it; `pause()` while paused, `resume()` while recording and `stop()` while inactive do
  nothing. `stop()` sets `inactive` at once; `dataavailable` (the rest of the data) and then `stop` follow as tasks. `pause` and
  `resume` events are tasks too; `pause()` does not fire `dataavailable` (Chromium does not either).
* `start(timeslice)` emits a `BlobEvent` about every `timeslice` ms (chunks are cut at container boundaries: WebM clusters,
  Ogg pages); without it you get one blob at `stop()`, plus one for each `requestData()`. `BlobEvent.timecode` is the
  recording time in ms of the first sample in the blob. `Blob.type` is `recorder.mimeType`.
* The concatenation of the chunks, in order, is a valid file: it plays in Sweef's `<audio>` and `<video>`, decodes with the
  repo's own demuxers and with ffmpeg (checked ad hoc, never in tests). A WebM recorded without `timeslice` and without
  `requestData()` also carries its `Duration`; chunked WebM has none (as in Chromium) and players find the length by scanning.
* The recording stops by itself when every track ended (`track.stop()`, unplugged device, revoked permission, navigation), with
  the data so far. Adding or removing a track of the stream while recording is an `error` (`InvalidModificationError`) and a stop.
* `start()` on a stream with nothing recordable, or with an unsupported size (VP8 takes 16 to 16383), fires `error`
  (`NotSupportedError`) and then `stop`, and leaves the recorder `inactive`.

## How it works

```
track (capture.Track | canvas | Web Audio tap) ──▶ per-source goroutine ──▶ resample to 48 kHz ──▶ mixer ──▶ Opus (20 ms)
                                                                                                  ▲
camera / canvas frames ──▶ newest frame, ≤ 30 fps, scaled to the first size ──▶ VP8 (key every 3 s) │
                                                                      │                           │
                                                          interleaver (by timestamp) ◀────────────┘
                                                                      ▼
                                                    WebM writer / Ogg writer ──▶ chunk ──▶ event queue ──▶ BlobEvent
```

* **Off the JS thread.** Each recording owns goroutines (one per audio source, one for video, one controller). `start()`
  returns immediately; chunks and `stop` come back through the VM's event queue (the "rec" async event) in the order the
  recorder produced them, and the bytes are handed over through the WebCodecs blob store (typed-array copy, not base64;
  base64 only in a VM without the typed-array natives).
* **Time.** The recording clock is wall time minus time spent paused. Video frames are stamped on arrival. Audio is stamped
  by sample count from an anchor taken when its first samples arrive (and again after a pause). A wall-clock source
  (microphone) that falls more than 200 ms behind gets silence inserted, so a stalled source cannot drift the audio out of
  sync with the video; a Web Audio source is never padded. The interleaver writes packets in timestamp order and never waits
  more than a second for the other track (an idle video track is assumed quiet up to 300 ms ago).
* **Pause** drops incoming samples and frames, flushes the audio frame being filled, and restarts the timeline at the
  recording clock on resume: a 2.4 s session with a 0.8 s pause records about 1.6 s, with no gap.
* **Resampling.** Any input rate (8 kHz to 96 kHz and beyond) is converted to 48 kHz by a streaming windowed-sinc resampler
  (8 zero crossings, Hann window, lowpass at the lower Nyquist so downsampling does not alias, unity DC gain). Mono, stereo
  and more than two channels (front pair) are handled; the recording is mono only if every source is.
* **Memory.** Raw frames never queue: each video subscriber keeps three, the loop encodes only the newest and drops the rest.
  Audio chunk channels hold 160 ms (microphone) or 640 ms (Web Audio). The only thing that grows is encoded data waiting for
  the page: with `timeslice` it is handed out every slice; without it the whole recording is held until `stop()`, as the
  specification requires (about 15 kB per second at 128 kbit/s Opus; 2.5 Mbit/s video is 19 MB per minute). Long video
  recordings should use `timeslice`.
* **Privacy and permission.** A recorder can only read tracks the page itself holds: a track id of another page is not in
  this page's capture table (`capHost`), so nothing is recorded and `start()` reports `NotSupportedError`. Permission is not
  a second prompt: the tracks are the ones `getUserMedia` / `getDisplayMedia` granted, they end when the permission is
  revoked, and the recording then stops. The tab's recording indicator is the capture indicator.

## Known gaps

* Real-time VP8 in software on a loaded machine: frames are dropped to keep up (the encoder adapts its effort), so a 720p
  recording on a slow core records fewer frames per second rather than falling behind. Not measured on real hardware.
* No `MediaStreamTrack.enabled` effect for canvas and Web Audio tracks (it works for camera, microphone and screen).
* `audio/mp4`, `video/mp4`, VP9, H.264, AV1 are not available (no encoders), and `videoKeyFrameIntervalDuration` /
  `videoKeyFrameIntervalCount` are ignored (a key frame every three seconds and on resume).
* WebM has no `Cues` and no `SeekHead`; seeking inside a long recording scans the file. Ogg has no seek index either.
* `MediaRecorder` is not exposed to Workers (it is not in the specification either).
* Windows and macOS capture backends are unverified on hardware (docs/capture.md); the recorder itself is platform neutral
  and cross-compiles.
* WhatsApp Web, Discord and similar sites were **not** run for real; their flow (getUserMedia, `MediaRecorder` with
  `audio/ogg;codecs=opus` or WebM Opus, `Blob`, `<audio>.src`) is reproduced by a test against local servers.

## Tests

```sh
go test ./internal/mediarec                      # resampler, Ogg page CRCs and granules, WebM, mixer, pause, stalls, bounds, goroutine leaks
go test ./internal/engine -run TestMediaRecorder # the JS API against synthetic devices (SWEEF_FAKE_DEVICES is set by the tests)
```

* `isTypeSupported` matrix, constructor and state-machine errors, event order (`dataavailable` before `stop`, `start`/`pause`/`resume` as tasks).
* Two seconds of the synthetic microphone to `audio/webm;codecs=opus`, `audio/ogg;codecs=opus` and bare `audio/webm`, decoded with
  the repo's decoders: duration within 0.5 s, RMS of a -12 dBFS tone, frequency within 6 Hz of 440.
* Resampler at 8, 16, 22.05, 32, 44.1 and 96 kHz: level and pitch preserved, a 30 kHz tone does not alias.
* Video plus audio WebM: VP8 frames decoded and compared with the frames sent (worst-frame PSNR 53 dB on the test picture; the
  test fails under 28 dB), key frame first, timestamps monotonic, audio and video end together.
* Timeslice chunking (count, non-empty, concatenation decodes), pause/resume duration, a stalled microphone, two mixed sources,
  a frame size change, a 300 fps camera (queues bounded, rate capped), an idle video track not delaying audio.
* Web Audio destination, `canvas.captureStream()`, stop when all tracks end, errors, a forged track id, navigation freeing the device,
  and the WhatsApp-style voice-note flow ending in `<audio>.src = URL.createObjectURL(blob)` reporting its duration.
