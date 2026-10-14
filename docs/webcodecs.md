# WebCodecs

`VideoDecoder`, `VideoEncoder`, `AudioDecoder`, `AudioEncoder`, `VideoFrame`, `AudioData`, `EncodedVideoChunk`,
`EncodedAudioChunk`, `VideoColorSpace`, `ImageDecoder` (with `ImageTrack` and `ImageTrackList`) and the static
`isConfigSupported()` / `ImageDecoder.isTypeSupported()` exist in pages and in dedicated Workers. They are backed by the
pure-Go codecs already in the tree (`internal/media`), with no cgo and no new dependency.

Files: `internal/engine/platformjs/webcodecs.js` (the API: state machines, validation, queues, errors),
`internal/engine/webcodecs.go` (codec goroutines and their events), `webcodecs_data.go` (frame and audio data
registry), `webcodecs_canvas.go` (canvas interop), `internal/media/wcodecs.go` (codec adapters).

## Support matrix

| | Codec strings | Notes |
|---|---|---|
| Video decode | `vp8`, `vp09.*` (and `vp9`), `avc1.*` / `avc3.*`, `hev1.*` / `hvc1.*`, `av01.*` | H.264 takes an `avcC` description, or Annex B when there is none; HEVC takes `hvcC`. Pictures come out in presentation order for streams with B-frames (H.264 reorder depth is read from the SPS VUI; HEVC has its own picture buffer). The output `timestamp` and `duration` are the chunk's. |
| Video encode | `vp8` | Pure-Go VP8 encoder (`internal/media/vp8enc`): `bitrate`, `framerate`, `keyFrame` per frame, `reconfigure()` without a new key frame, a size change produces a key frame. |
| Audio decode | `opus`, `mp4a.40.*` (AAC, with an AudioSpecificConfig `description` or ADTS chunks), `mp3`, `vorbis`, `pcm-u8`, `pcm-s16`, `pcm-s24`, `pcm-s32`, `pcm-f32`, `ulaw`, `alaw` | Output is `f32-planar`. |
| Audio encode | `opus` | `frameDuration` (2.5 to 60 ms), `application` (`audio`, `voip`, `lowdelay`), `bitrate`. |
| Images | `image/png`, `image/jpeg`, `image/gif`, `image/webp`, `image/bmp`, `image/apng` | `ImageDecoder` decodes all frames of an animation, `tracks`, `frameCount`, `decode({frameIndex})`. |

Everything else is rejected the way a browser does: `isConfigSupported()` resolves `{supported: false}`, `configure()`
fails with `NotSupportedError` through the `error` callback and the codec becomes `closed`.

## What the implementation does

* **Off the JS thread.** Every codec owns a goroutine and a job queue. `decode()` / `encode()` only append a job and
  return; output and error callbacks run from the VM's event loop in order. `flush()` resolves after every earlier job's
  output callback ran. `reset()` bumps an epoch: queued jobs are dropped and the late result of a running one is ignored.
  `decodeQueueSize` / `encodeQueueSize` and the `dequeue` event follow the specification.
* **State machine and errors.** `unconfigured` / `configured` / `closed`, `InvalidStateError` for wrong-state calls,
  `DataError` for a first chunk that is not a key frame, `EncodingError` / `OperationError` for failed decodes, `TypeError`
  for bad configuration, `DOMException` types as in Chromium. Closing releases the handles.
* **Pixels stay out of the JS heap.** A `VideoFrame` or `AudioData` is a handle into a reference-counted registry on the
  Go side. `clone()` shares, `close()` releases, and a frame that is garbage collected releases its data. `copyTo()` converts
  to the requested layout (`I420`, `I420A`, `I422`, `I444`, `NV12`, `RGBA`, `RGBX`, `BGRA`, `BGRX`), with `rect` and
  `layout`, and `allocationSize()` agrees with it.
* **Canvas interop.** `new VideoFrame(canvas | ImageBitmap | OffscreenCanvas | HTMLVideoElement | VideoFrame, {timestamp})`;
  a `VideoFrame` is a valid source for `drawImage()`, `createImageBitmap()` and `createPattern()`.
* **Workers and clone.** All the classes exist in dedicated Workers, and `VideoFrame` / `AudioData` can be posted (and
  transferred) between the page and its Workers and via `structuredClone`; they travel by token inside the agent cluster.
* **Teardown.** Leaving the page closes every codec goroutine and drops the registry.

## What it does not do

* Encoders: only VP8 (video) and Opus (audio). There is no H.264, VP9, AV1 or AAC encoder in the tree, so
  `VideoEncoder.isConfigSupported({codec: 'avc1...'})` is `{supported: false}`. Sites that pick H.264 for `VideoEncoder`
  have to take the fallback the site has for that. `bitrateMode`, `scalabilityMode`, `alpha`, `hardwareAcceleration` and
  `latencyMode` are passed through `isConfigSupported()` but not acted on.
* Everything is software. Color is handled for `bt709` and `smpte170m` YUV only; there is no HDR handling. `AudioData`
  does not resample (`sampleRate` is preserved).
* `MediaStreamTrackProcessor` / `MediaStreamTrackGenerator` and `VideoFrame` from a `MediaStream` are separate APIs and
  are not part of this work.
* Speed has not been measured against a browser, and there is no benchmark for these APIs. Do not assume 1080p60 software
  encoding or VP9 decoding.

## Tests

`go test ./internal/engine -short -run 'TestWebCodecs'` and `go test ./internal/media -short -run 'TestWC'`:

* `TestWebCodecsVP8RoundTripPSNR`: frames drawn on a canvas are encoded with `VideoEncoder`, fed to `VideoDecoder`, and the
  decoded frames are compared with the originals; the test fails under 28 dB worst-frame PSNR.
* `TestWebCodecsOpusRoundTrip`: a tone through `AudioEncoder` and `AudioDecoder`, checked for frequency and level.
* `TestWebCodecsDecodeRealStreams`: H.264 (baseline and B-frames), HEVC, VP9, AV1 and AAC streams from `testdata`, whose
  decoded pictures agree with what the media player produces (`TestWCVideoDecodersAgreeWithThePlayer`).
* Configuration support, the state machine and error cases, `VideoFrame` buffers and `copyTo`, `AudioData`, `ImageDecoder`,
  Worker use and frame transfer, and teardown each have a test.
