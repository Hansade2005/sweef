# Camera, microphone and screen capture

What a page gets from `navigator.mediaDevices.getUserMedia()`, `getDisplayMedia()` and `enumerateDevices()`, where the
pixels and samples come from on each operating system, how permission works, and exactly how far each part has been
checked. Read the status table first: it says what was run and what was only compiled.

## Status

| Part | What it does | Checked how |
|---|---|---|
| Constraint solver (`internal/capture/constraints`) | SelectSettings with min/max/exact/ideal, `advanced`, `OverconstrainedError.constraint` | table tests (about 50 cases) |
| Pixel conversions (`internal/capture`) | YUYV, UYVY, NV12, NV21, I420, YV12, RGB24, BGR24, GREY, MJPEG (also without Huffman tables) to I420 | tests against independent reference conversions; MJPEG compared with a float reference |
| **Linux camera**, V4L2 (`internal/capture/v4l2`) | enumerates `/dev/video*`, mmap streaming, frame-rate control, `EBUSY` to `ErrInUse`, unplug to a closed channel | the whole capture loop runs against an in-memory V4L2 device (`mock_test.go`); ioctl numbers and struct sizes/offsets are asserted against the kernel's amd64/arm64 values. **Never run against a real `/dev/video` node: this machine has none.** |
| **Linux microphone**, PulseAudio/PipeWire (`internal/capture/pulse`) | native protocol client over the unix socket | in-process fake server, and **a real PulseAudio 16.1 server** (`TestRealServerPipeSource`: listing, monitor hiding, default source, mono and stereo record streams, a 440 Hz tone read back at the right frequency and level, unknown-source error). Not run against PipeWire-pulse. |
| Synthetic devices (`internal/capture/synthetic`) | test-pattern camera, tone/noise microphone, `SWEEF_FAKE_DEVICES=1` | unit tests; drives every engine and app test below |
| JS API (`internal/engine/capture_host.go`, `media_prelude.js`) | real `getUserMedia`/`getDisplayMedia`/`enumerateDevices`/Permissions API | engine tests against a scripted broker (about 15) |
| Permission model, prompt, indicators (`internal/app/media.go`, `internal/ui/web/chrome/media.*`) | per-origin decisions in the profile, prompt, display chooser, in-use popover, share bar | app tests with the headless window and the real chrome UI |
| Tab and window capture (`internal/capture/screen`, compositor tap) | `getDisplayMedia` of this tab, other tabs, the browser window | app tests compare the delivered pixels with the page |
| **Windows**, Media Foundation + WASAPI (`internal/capture/wincap`) | camera via `IMFSourceReader`, microphone via shared-mode WASAPI | **UNVERIFIED: never run on real hardware.** Compiled and vetted for windows/amd64 and windows/arm64 only. |
| **macOS**, AVFoundation (`internal/capture/avf`) | camera and microphone through purego's Objective-C runtime | **UNVERIFIED: never run on real hardware.** Compiled and vetted for darwin/amd64 and darwin/arm64 only. |

`SWEEF_NO_NATIVE_CAPTURE=1` disables the three OS backends (V4L2, Pulse, Windows, macOS) and leaves only the synthetic
devices. Use it if an unverified backend misbehaves.

## Not supported, on purpose

* **Screen and other applications' windows.** `getDisplayMedia` offers the tabs of this browser window and the browser
  window itself, because those pixels come from our own compositor. Capturing the operating system's screen or another
  program's window needs OS APIs (X11/Wayland portals, DXGI, ScreenCaptureKit) that are not used here.
* **Tab audio.** `getDisplayMedia({audio:true})` returns a stream without an audio track and the chooser says so. The
  audio output path (`internal/audioout`) is not in this tree; when it exists its mix can become that track.
* **Echo cancellation, noise suppression, automatic gain control.** There is no audio processing. `getSettings()` says
  `false` for all three, and `{echoCancellation:{exact:true}}` is an `OverconstrainedError`. `{ideal:true}` is accepted
  and simply not honoured. A call with two people on speakers will echo.
* **Resampling.** Microphones deliver 16-bit PCM at the device's own rate; the consumer resamples.
* **`audiooutput` devices** are only listed when a provider registers them. Recording a captured stream is
  `MediaRecorder`: [mediarecorder.md](mediarecorder.md).
* **User activation for `getDisplayMedia`** is not enforced.
* **Frames.** Iframes do not run scripts in this engine, so only the top-level document can ask for a device.
  `internal/capture/policy.FrameAllowed` implements the rule for a cross-origin frame (parent policy and `allow`
  attribute) and is tested, but nothing calls it yet. `Permissions-Policy: camera=()` on the top document is honoured.
* **Facing mode.** A camera that does not say which way it faces (every V4L2 webcam) is treated as `"user"`, so
  `facingMode:{exact:"environment"}` fails on a laptop.
* **One mode per camera.** A camera runs in one mode. A second `getUserMedia` of the same device joins the running
  stream and reports its mode; `applyConstraints` on any track restarts the camera in the new mode for all tracks
  sharing it. The page never gets scaled frames: `getSettings()` is what the device delivers.

## How a request flows

```
page JS ──getUserMedia({video:{width:{ideal:1280}}, audio:true})──▶ media_prelude.js
   │  capRequest(token, "gum", json)
   ▼
internal/engine/capture_host.go   (own goroutine; a prompt can wait for minutes)
   1. secure context? Permissions-Policy?                        SecurityError / NotAllowedError
   2. devices exist?                                              NotFoundError
   3. constraints solved against what the devices can really do   OverconstrainedError(constraint)
   4. stored decision for camera / microphone                     denied: NotAllowedError
      "prompt": MediaBroker.RequestMedia  ──▶ app: state.media.prompt ──▶ chrome prompt ──▶ media.answer
   5. capture.AcquireVideo / AcquireAudio (shared, ref-counted)   NotReadableError (in use, no frames in 4 s)
   6. track ids, settings, capabilities back to the page via __swCapEvent
```

`MediaStreamTrack.id` is the id of a `capture.Track`. The WebRTC and recording pipelines get the media with

```go
t := capture.LookupTrack(trackID)
frames, cancel := t.SubscribeVideo()   // *capture.Frame, I420, honours track.enabled by sending black
chunks, cancel := t.SubscribeAudio()   // *capture.AudioChunk, interleaved int16 PCM
<-t.Ended()                            // stop(), unplug, Stop sharing, permission revoked, navigation
```

Frames are shared between subscribers and must be treated as read-only. A subscriber that falls behind loses its oldest
frames; a live source never waits for a reader.

## Permissions

* Decisions are stored per origin in the profile's `sites.json` (`camera`, `microphone`, `screen`: `allow`, `block` or
  absent for "ask"). Screen sharing is never stored as "allow": it asks every time; only a block is remembered.
* The prompt offers **Allow this time**, **Always allow on this site** and **Block** (Block is remembered), plus a
  device list when several devices fit. It is a bubble under the address bar, not a modal: it never takes keyboard
  focus on a button, so text being typed into the page cannot answer it. Escape or the close button dismiss it
  ("Permission dismissed", nothing stored).
* While a camera or microphone is live the tab shows an icon with a red dot (camera, sharing) or a green dot
  (microphone only), the toolbar shows a button with the same dot, and its popover lists each session with **Stop** and
  the site's Ask/Allow/Block choices. Screen sharing also keeps a bar with **Stop sharing** under the toolbar. The site-info
  popover lists stored decisions with a reset button. Blocking a kind ends a live track of that kind and fires the
  Permissions API `change` event.
* **Private windows never persist.** Their decisions go to the in-memory private profile (and "Always allow" is not
  offered); a private tab reads the regular profile's decisions but never writes them.
* **Device ids** are `sha256(profile salt | origin | kind | real id)`: stable for one origin on one profile (the salt
  is in `sites.json`), different on every other origin. `enumerateDevices()` returns one anonymous entry per kind (no id,
  no label, no group) until the page has permission or a live track of that kind.
* Without an embedding app (`sweef open`, the agent daemon) there is no broker: everything is denied, except that
  with `SWEEF_FAKE_DEVICES=1` the fake devices are granted automatically, like Chrome's `--use-fake-ui-for-media-stream`.
  A headless run can never open a real camera.

## Synthetic devices

`SWEEF_FAKE_DEVICES=1` (or the older `SWEEF_FAKE_MEDIA=1`) adds "Fake Camera", "Fake Microphone" and "Fake Speaker".
The camera is eight colour bars moving four pixels per frame with a frame counter and the stream time
(`F000123 00:00:04.100`), 640x480 at 30 fps by default (`SWEEF_FAKE_CAMERA=1280x720@30`, any size from 160x120 to 1920x1080
with the solver). The microphone is a 440 Hz sine at 48 kHz mono, 10 ms chunks (`SWEEF_FAKE_AUDIO=noise|silence`).
Frames and samples are a pure function of the frame number, so recordings are reproducible.

## Checking real hardware

`capdemo` talks to the providers directly, without a browser:

```sh
go run ./internal/capture/cmd/capdemo -list                       # cameras with their modes, microphones with rates
go run ./internal/capture/cmd/capdemo -video 0 -frames 60 -every 10 -size 1280x720 -fps 30 -out /tmp/cap
go run ./internal/capture/cmd/capdemo -audio 0 -seconds 5 -out /tmp/cap      # writes capture.wav, prints peak and rms
go run ./internal/capture/cmd/capdemo -fake -video 0 -frames 5 -out /tmp/cap # the same without hardware
```

It prints the negotiated mode, the number of frames received and the frame rate the device timestamps give, and writes
PNG frames (`frame-0001.png`, ...) or a 16-bit WAV. Each checklist below says what to expect.

### Linux (V4L2 camera, Pulse/PipeWire microphone)

1. `capdemo -list`: every webcam appears once, with its card name, and no `/dev/video` node that is a metadata node.
   Modes match `v4l2-ctl --list-formats-ext` (sizes and rate ranges).
2. `capdemo -video 0 -frames 90 -size 640x480 -fps 30`: the summary says 30 fps within a frame or two, PNGs show the
   room. Repeat with `-size 1280x720` and `-size 1920x1080` (MJPEG on most cameras). On the author's CI VM the decoder
   needs about 4.8 ms (640x480), 13.8 ms (1280x720) and 23.8 ms (1920x1080) per frame (`go test ./internal/capture -bench MJPEG`,
   shared 4-core Xeon, noisy): 1080p30 over MJPEG is close to the limit of one core, and frames are dropped, not queued, when
   it is exceeded.
3. Start `capdemo -video 0` while another program (a browser, `ffplay /dev/video0`) holds the camera: it must fail with
   "device in use", not hang. Unplug the camera during a run: the program must stop with "device not found".
4. `capdemo -audio 0 -seconds 5`, speaking: `capture.wav` plays back correctly and the peak is well above 0.
   With PipeWire check that `pactl list sources short` shows the same sources (monitors are hidden by design).
5. In the browser: `SWEEF_FAKE_DEVICES` unset, open any camera test page over https, expect the prompt; the tab
   gets a red dot while the camera is on and the camera LED goes off after `stop()`.

### Windows (UNVERIFIED)

1. `capdemo.exe -list`: cameras show their friendly names; microphones show the endpoint names, default first.
2. `-video 0 -frames 60`: frames arrive at the camera's rate; PNGs are upright and not colour-swapped (NV12 path).
3. `-audio 0 -seconds 5`: a working WAV at the mix rate (usually 48000 or 44100 Hz). With Settings > Privacy & security >
   Microphone turned off for desktop apps, the error text must mention that setting.
4. The camera LED must go off when `capdemo` exits and after a browser tab calls `track.stop()` (the backend calls
   `IMFActivate::ShutdownObject`).
5. Things most likely to be wrong, in order: the source-reader format negotiation for cameras that only offer MJPEG
   (video processing is requested, but nobody has seen it work), `MF_MT_DEFAULT_STRIDE` for NV12, WASAPI
   `AUTOCONVERTPCM` on devices with unusual mix formats.

### macOS (UNVERIFIED)

1. Build the bundle (`packaging/macos/build-app.sh`): the plist now carries `NSCameraUsageDescription` and
   `NSMicrophoneUsageDescription`, and the entitlements carry `device.camera` and `device.audio-input`. Run the app from
   the bundle: a bare `go run` binary has no usage string and the backend refuses with an error instead of letting macOS
   kill the process.
2. First use shows the system permission sheet; answering "Don't Allow" must produce `NotAllowedError`-style failure with
   a message pointing at System Settings, and no crash.
3. `capdemo -list` (from Terminal, which has its own camera permission), `-video 0 -frames 60`, `-audio 0 -seconds 5`.
4. Things most likely to be wrong: the Objective-C delegate callbacks from the dispatch queue (purego callbacks on foreign
   threads), the 420v pixel buffer plane handling, the audio-settings dictionary keys, session preset names.

## Tests

```sh
go test ./internal/capture/...                     # solver, conversions, V4L2 loop, Pulse client, synthetic devices
go test -run 'Media|Capture' ./internal/engine ./internal/app   # the JS API and the permission flow (needs no hardware)
GOOS=windows GOARCH=amd64 go vet ./internal/capture/...         # the unverified backends compile (also arm64, darwin)
```

Real-server Pulse test: start a PulseAudio with a FIFO source (see the comment on `TestRealServerPipeSource`), set
`SWEEF_TEST_PULSE_FIFO`, `PULSE_SERVER` and (if the server runs as another user) `PULSE_COOKIE`.

Leak tests open and close each source 100 times (V4L2, Pulse, synthetic, screen, shared leases, the engine path) and
fail if goroutines, mapped buffers (counted by the mock device), tracks or shared leases are left behind.
