# Audio output

`internal/audioout` is the one place in Sweef that makes sound. HTMLMediaElement (`<video>`, `<audio>`), Web Audio and,
later, WebRTC remote audio all open a stream on it. It is pure Go (`CGO_ENABLED=0`).

## Pieces

```
HTMLMediaElement player ─┐
Web Audio destination ───┼─> Stream (convert, volume, mute) ─> Group (per tab) ─> Output (mix, master) ─> Backend
WebRTC (not wired yet) ──┘
```

- **Backend** is the clocked device. It calls `pull(dst []float32)` for fixed chunks of interleaved stereo float32 at
  48 kHz (`DeviceRate`, `DeviceChannels`) and reports its own latency.
  - **Linux and the BSDs: the PulseAudio native protocol, in pure Go** (`backend_pulse.go`, on top of the client in
    `internal/capture/pulse`, the same one the microphone uses). It connects to the sound server's unix socket
    (`PULSE_SERVER`, else `$XDG_RUNTIME_DIR/pulse/native`) with the auth cookie, creates a float32 playback stream
    (48 kHz stereo, 60 ms buffer, `adjust_latency`), answers the server's `REQUEST`s with mixed audio in pulls of at
    most 20 ms, and reads the real latency (`GET_PLAYBACK_LATENCY`: the device's latency plus what the server still
    holds) every 250 ms. Idle time corks the stream. PipeWire's pulse server speaks the same protocol. No library is
    loaded: **the Linux binary is statically linked** (a test, `cmd/sweef/static_linux_test.go`, and
    `scripts/release/build.sh`/`verify.sh` fail on any `NEEDED`/`INTERP`). A system with only bare ALSA, no sound
    server, gets the null sink (Sweef plays silently, with correct timing) and a log line (`audio: no sound output (...); playing silently`); there is no ALSA
    backend because loading `libasound` needs dlopen. If the server goes away mid-playback the clock keeps
    running in real time and the audio is dropped.
  - **Windows and macOS: `oto`** (build-tagged, `github.com/ebitengine/oto/v3`, Apache-2.0): WASAPI and
    CoreAudio. `CGO_ENABLED=0` builds; one context per process. Latency estimate: 40 ms device buffer + 40 ms player
    buffer = 80 ms.
  - `null`: discards the audio but consumes it in real time, so the audio clock runs at the right speed with no sound
    card (CI, containers, headless). This is the fallback when no device can be opened.
  - `TestSink`: records everything mixed. Virtual clock (`Advance`) or real-time (`NewRealtimeTestOutput`), with
    `Recorded`, `Peak`, `FirstSound`, `Frames`, `FrameTime`, `SetLatency`.
- **Output** mixes streams, applies the master volume and mute, and wakes or idles the device (an `oto` player that
  supports `Suspend` is suspended when nothing plays).
- **Group** is one per tab. It has a volume, a mute, and an audible indicator.
- **Stream** is what a producer holds.

`SWEEF_AUDIO=null` forces the null backend (the engine tests do this); with it unset, `Default()` tries the real device
and falls back to null.

## Stream contract

```go
s := audioout.Default().Open(rate, channels, group)   // any rate; starts paused; group may be nil
s.Write(pcm)            // interleaved float32 in [-1,1]; blocks only while the 2 s stream buffer is full
s.WritePlanar(chans)    // planar variant (Web Audio)
s.Play(); s.Pause()     // a paused stream keeps its buffer and its position
s.Flush()               // drop buffered audio (seek)
s.SetVolume(v); s.SetMuted(m); s.SetSpeed(r)
s.End()                 // no more data; Done() closes when the buffer has played out
s.Close()
```

Conversion to the device format happens in `Write`: channel mix to stereo, Catmull-Rom resampling, and varispeed for
`SetSpeed` (the stream is resampled so it plays faster or slower, pitch included).

### The timeline

The stream has one timeline measured in the stream's own frames:

- `Written()` is the index of the next frame to be written.
- `Position()` is the frame being heard right now: the frames the device has pulled (interpolated between pulls)
  minus the backend latency. This is what the media element's `currentTime` and the video frame choice follow.
- `Buffered()` is `Written() - Position()`.
- `Flush()` removes the dropped frames from the timeline, so a seek does not move `Position()` backwards by itself.
- The clock stops when the stream underruns (`Underruns()` counts them), and resumes when data arrives.

### Volume, mute, audible

Volume is a linear gain per stream and per group, applied in the mix. A muted stream or group still consumes its audio
so the clock keeps running. A group is **audible** when the mixed peak of its streams exceeded 1e-3 within the last
1.5 s; `Output.Poll()` (and a 250 ms ticker) updates it and calls the group's `OnAudible` callback on a change.

## How the browser uses it

See [media-streaming.md](media-streaming.md) for the player. The rules that matter here:

- **The audio clock is the master.** The video follows `Position()`: late frames are dropped (or the decoder jumps to the
  next key frame when more than 0.25 s behind), early frames are held. When an element has no audio, or its audio ended,
  a wall clock takes over.
- **Tabs.** Each tab owns a `Group`. The engine emits `EvAudio` with `AudioState{Audible, Muted}` when the audible
  state changes; `Tab.SetAudioMuted` mutes the group. The app shows the speaker on the tab and `tab.mute {id}` (see
  [gui-contract.md](gui-contract.md)) toggles it. The mute survives a tab being recreated.
- **Web Audio.** After each render quantum the context writes into a stream (dropping the quantum rather than blocking
  the page if the device is behind). `createMediaElementSource` silences the element's own output and routes it through
  the graph.
- **Autoplay.** Muted video, or video without an audio track, may start without a gesture. Everything else needs a
  user gesture (click, mouse down/up, key, touch; mouse movement does not count), otherwise `play()` rejects with
  `NotAllowedError`. An `AudioContext` created before a gesture stays suspended.

## Limits

- The Pulse backend is tested against an in-process fake server that parses `CREATE_PLAYBACK_STREAM` strictly
  (`internal/capture/pulse/play_test.go`) and against a fake stream for the pump logic; it has not been run against a real
  PulseAudio or PipeWire server here. The `oto` backend is compile-checked for windows and darwin only. `Default()`
  opens the device with a 2 s limit and uses the null sink on timeout or error, and always on CI (`CI` set, or
  `SWEEF_AUDIO=null`), so nothing depends on a sound device.
- `playbackRate` changes pitch (varispeed). The pitch-preserving time stretch (`preservesPitch`) is **not** implemented;
  rates outside 0.25..4 are silent, as in other browsers.
- Rates other than 48 kHz are resampled with Catmull-Rom interpolation, which is good for speech and music but not a
  mastering-grade resampler.
- WebRTC audio is not wired; it should open a stream on the same `Output` and use `Position()` for lip sync.
- Web Audio `AudioContext.currentTime` is still driven by the wall clock, not by the device.

## Binary size

The Pulse client adds a few KB. Dropping `oto`/`purego` from Linux took the stripped linux/amd64 binary from 37.23 MiB (dynamically linked, with the dlopen shim) to 36.99 MiB (statically linked). Windows and macOS builds still include `oto`.
