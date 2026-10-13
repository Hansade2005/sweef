# WebRTC

`RTCPeerConnection` and the classes around it, built on [pion/webrtc v4](https://github.com/pion/webrtc) (MIT, pure Go, no
cgo). Data channels and signalling are complete enough for real applications; audio and video are real (VP8 + Opus, pure Go) and **the limits are listed below**.
Call-service notes are in [webrtc-interop.md](webrtc-interop.md).

Files: `internal/engine/webrtc.go` (peers, host calls, privacy policy), `webrtc_net.go` (byte metering),
`webrtc_prelude.js` (the page-facing classes), `webrtc_media.go` (track pipelines, host calls for media), tests in
`webrtc_*_test.go`. Media lives in `internal/rtcmedia` (RTP, jitter buffers, senders, receivers, congestion control) and
`internal/media/vp8enc` (the VP8 encoder).

## How it fits the engine

* The page-facing classes are JavaScript (`webrtc_prelude.js`, registered with `registerPrelude("webrtc", ...)`). They
  call synchronous host functions on `__sw` (`rtcNew`, `rtcCreateOffer`, `rtcSetLocal`, ...) registered with
  `registerHostExtension("webrtc", ...)`. Nothing in the shared prelude lists was edited.
* pion runs on its own goroutines. Its callbacks (candidates, state changes, data channel traffic) never touch the VM: they
  push `asyncEvent{kind: "rtc"}` onto the page's realtime queue (the one WebSocket frames and worker messages use), and the
  event-loop pump hands them to `__swRtcEvent` on the page thread. A window is woken through the tab's wake hook, as for
  finished images. After each event the engine runs a microtask checkpoint, so `await` continuations run between events.
* `realtime.go` gained two small hooks so a feature does not have to edit the shared switch: `registerAsyncHandler(kind, fn)`
  and `realtime.addCloser(fn)`.
* Promises settle behind the events their operation caused: every operation queues a ticket on the same queue, so
  `signalingstatechange` handlers run before `await pc.setLocalDescription()` returns. `icecandidate` and
  `icegatheringstatechange` events that arrive while an operation is outstanding are held until the awaiting code has
  continued (browsers resolve `setLocalDescription()` before the first candidate event, and pages rely on it). State getters
  read the live state, except inside a state-change handler, where they report the state that event announces.

## Supported surface

| Area | What works |
|---|---|
| `RTCPeerConnection` | constructor with `iceServers` (stun/stuns/turn/turns, validated: `SyntaxError`, `InvalidAccessError`), `iceTransportPolicy` (`all`, `relay`), `bundlePolicy`, `rtcpMuxPolicy` (only `require` is effective), `certificates`; `createOffer` (incl. `iceRestart`, legacy `offerToReceiveAudio/Video`), `createAnswer`, `setLocalDescription` / `setRemoteDescription` (explicit, **implicit** (no argument) and **rollback**), `addIceCandidate` (incl. end-of-candidates), `getConfiguration` / `setConfiguration`, `restartIce`, `close`, `getStats`, `addTrack` / `removeTrack` / `addTransceiver`, `getSenders` / `getReceivers` / `getTransceivers`, `createDataChannel`, static `generateCertificate` (ECDSA P-256, RSASSA-PKCS1-v1_5), legacy callback forms of `createOffer`/`createAnswer`/`setLocal`/`setRemote`/`addIceCandidate` |
| State and events | `signalingState`, `iceGatheringState`, `iceConnectionState`, `connectionState`, `canTrickleIceCandidates`, the six description getters, `onsignalingstatechange`, `onicegatheringstatechange`, `oniceconnectionstatechange`, `onconnectionstatechange`, `onicecandidate` (null at the end), `onnegotiationneeded`, `ontrack`, `ondatachannel` |
| `RTCSessionDescription`, `RTCIceCandidate` | validation as in browsers, candidate attributes parsed (`foundation`, `component`, `protocol`, `priority`, `address`, `port`, `type`, `tcpType`, `relatedAddress`, `relatedPort`), `toJSON` |
| `RTCDataChannel` | reliable and unreliable (`maxRetransmits` / `maxPacketLifeTime`), `ordered`, `negotiated` + `id`, `protocol`, `binaryType` (`blob` default, `arraybuffer`), `send` of string / `ArrayBuffer` / views / `Blob`, `bufferedAmount`, `bufferedAmountLowThreshold`, `onbufferedamountlow`, `onopen` / `onmessage` / `onerror` / `onclosing` / `onclose`, `close()` |
| `RTCRtpSender` / `RTCRtpReceiver` / `RTCRtpTransceiver` | `track`, `replaceTrack`, `getParameters` / `setParameters` (transactionId checked, encodings validated), `setStreams`, `direction` get/set (fires `negotiationneeded`), `currentDirection`, `mid`, `stop()`, `setCodecPreferences` (validated), static `getCapabilities`, `getStats`, `dtmf` |
| `RTCDTMFSender` | `insertDTMF`, `toneBuffer`, `canInsertDTMF`, `ontonechange`. **Stub:** the tones are scheduled and reported, no RFC 4733 packets are sent |
| `RTCCertificate` | `expires`, `getFingerprints`; accepted in the configuration (the offer carries its fingerprint) |
| `RTCDtlsTransport`, `RTCIceTransport`, `RTCSctpTransport` | read-only live views: `state`, `role`, `gatheringState`, `getLocalCandidates`, `getRemoteCandidates`, `getSelectedCandidatePair`, `getLocalParameters`, `getRemoteParameters`, `maxMessageSize`, `maxChannels`, `statechange` / `gatheringstatechange` / `selectedcandidatepairchange` events |
| `getStats()` | a real `RTCStatsReport` (a `Map`) from pion: `peer-connection`, `data-channel`, `transport`, `candidate-pair`, `local-candidate`, `remote-candidate`, `certificate`, plus `inbound-rtp` / `outbound-rtp` and friends when media flows. Selector: a track filters the RTP entries |
| Other | `RTCError`, `RTCErrorEvent`, `RTCTrackEvent`, `RTCDataChannelEvent`, `RTCPeerConnectionIceEvent`, `RTCDTMFToneChangeEvent`, `RTCStatsReport` |

## Media: what is real and what is not

Video is **VP8 only** and audio is **Opus only** (plus telephone-event in the SDP). The offer no longer lists G.722, PCMU,
PCMA, VP9, H.264 or AV1, because the engine can neither encode nor decode them; a remote that offers only those gets an
answer with the video section rejected. Everything below was exercised between two `RTCPeerConnection`s in one page over
loopback (see Tests). **No call to a real service was made**: the build environment only has an HTTPS proxy for egress, so
UDP, STUN, TURN and real browsers were not reachable.

* **Negotiation.** `m=audio` (Opus/48000/2 at PT 111, `minptime=10;useinbandfec=1`, telephone-event 126) and `m=video` (VP8
  at PT 96 with RTX at PT 97), `rtcp-fb` as pion negotiates it for the registered codecs (NACK, PLI, FIR, transport-cc; no REMB); header
  extensions mid, rtp-stream-id, repaired-rtp-stream-id, abs-send-time, transport-wide-cc, audio level, video orientation; `msid`,
  BUNDLE and `a=simulcast:recv` answering. `setCodecPreferences` really reorders and restricts the codecs (an RTX entry
  follows its primary); `RTCRtpSender/Receiver.getCapabilities` return what the engine implements.
* **Sending video.** Any local track is bound to the sender for the whole life of the transceiver, so `replaceTrack`
  only swaps the source. Sources: fake/real capture devices registered through `RegisterRTCTrackSource`, canvas
  `captureStream()`. Frames are scaled to the target size,
  encoded by `vp8enc`, packetised (RFC 7741), and paced. Congestion control is a Google-congestion-control style
  estimator (pion interceptor `cc`) driving the encoder bitrate; resolution steps down when the estimate cannot carry the
  current size. `track.enabled = false` sends black frames and silence; `removeTrack`, `transceiver.stop()`,
  `replaceTrack(null)` and `close()` stop the pipeline. `applyConstraints` and mid-call resolution changes re-size the
  encoder (a key frame follows). `setParameters` honours `encodings[0].active`, `maxBitrate`, `maxFramerate`,
  `scaleResolutionDownBy`. PLI and FIR produce a key frame; a NACK is answered from a history by RTX, limited by a
  retransmission budget (one third of the target bitrate) so a lossy path cannot spiral.
* **Sending audio.** 48 kHz mono/stereo PCM in 20 ms frames, resampled from the source rate, Opus-encoded with in-band FEC
  and a loss-adaptive bitrate. There is an `AudioProcessor` hook where an echo canceller could sit; **there is no echo
  cancellation, noise suppression or auto gain**, so calls need headphones. `getSettings()` therefore does not report
  `echoCancellation` and friends. DTMF is still a stub: tones are scheduled and reported but not sent as RFC 4733.
* **Receiving video.** Packets are depacketised, frames assembled and played out in order by a jitter buffer with an
  adaptive delay. Gaps are repaired with NACK (own generator: about 1.3 RTT between tries, 6 tries, 600 ms limit); when
  a frame cannot be repaired the decoder is paused, a PLI is sent, and display freezes on the last good frame until the
  next key frame (then `unmute` fires). Decoded frames are published to `<video srcObject>`; `videoWidth` / `videoHeight`
  are set, `resize` fires on changes, `readyState` reaches 2+, and `mute` / `unmute` / `ended` fire on the track.
* **Receiving audio.** Opus decode with loss concealment (PLC) and FEC, into an `AudioSink`. The default sink discards
  samples (`NullSink`); `RecordSink` keeps them for tests. Samples reach speakers only through the audio output work (see
  `SetRTCAudioSinkFactory`). Audio plays only while the stream is attached to a media element; the element's `volume` /
  `muted` are not forwarded to the sink. Levels appear in `getStats()` and `getSynchronizationSources()`.
* **Stats.** `outbound-rtp` and `inbound-rtp` for audio and video (packets, bytes, frames, frame size, fps, key frames,
  NACK/PLI/FIR counts, jitter, loss, jitter-buffer delay, `qualityLimitationReason`, encoder times, audio level),
  `remote-inbound-rtp` (round trip time, loss, jitter), `media-source`, `codec`, and `candidate-pair` with
  `availableOutgoingBitrate` and `currentRoundTripTime`. pion's own report has no `outbound-rtp` or `remote-inbound-rtp`
  for tracks we drive, so the engine adds a statistics interceptor and merges its entries.
* **Hooks for embedders** (package `engine`): `RegisterRTCTrackSource` / `SetRTCTrackResolver` map a track to a frame
  source (`internal/capture` providers plug in here), `SetRTCAudioSinkFactory` supplies the playback sink for remote
  audio, `SetRTCAudioProcessorFactory` an echo canceller / noise suppressor.
* **Simulcast.** A remote offer with `a=simulcast:send` is accepted and answered `recv`; the engine sends one layer. A
  local `sendEncodings` with several layers sends only the first.
* **Probes that fail.** `RTCRtpSender.createEncodedStreams`, `RTCRtpScriptTransform` and insertable streams are absent
  (feature detection fails, pages fall back to unencrypted media; E2EE modes of call services will not work).
  `requestVideoFrameCallback` is not driven for live streams.
* **Data Saver** `strict` caps outgoing video at 300 kbit/s (the encoder rate target and the estimate are clamped). A
  remote can still send to us; set the transceiver `direction` to `inactive` to refuse it.

### Measured numbers

Measured on the 4-core development box while other jobs were running (load average about 5), so absolute values are
pessimistic; they are reported as measured, not tuned. The CPU numbers are process CPU time per frame (`getrusage`).

| Case | Result |
|---|---|
| VP8 encoder, 640x360, speed auto | about 11-13 ms wall per frame |
| VP8 encoder, 1280x720, speed auto | about 17-27 ms wall, 42-58 CPU-ms per frame |
| Loopback call, 640x360 | 30 fps shown, 0 % frames lost, PSNR about 50 dB at the offered rate |
| Loopback call, 1280x720 | **not real-time on this box under load**: 14-23 fps shown, encode 18-36 ms wall per frame |
| 5 % random loss | repaired by NACK/RTX, call continues |
| Total blackout (keyframe lost) | recovered through PLI |
| Bandwidth cap | encoder bitrate and resolution step down to the cap |

The encoder bitstream is also decoded by ffmpeg (libvpx) in `TestFFmpegDecodesOurStreams` when ffmpeg is installed; a
side-by-side comparison with libvpx (`SWEEF_VP8_COMPARE=1`) is opt-in. The encoder has no 2-pass, no temporal layers and
no screen-content tools, and is less efficient than libvpx at equal quality.

### Known limits

* The estimator has no probing: it ramps about 8 % per second from 1 Mbit/s and has a loss-controller floor of 100 kbit/s.
  REMB is not generated; only transport-cc feedback is used.
* No RED / ULPFEC, no real echo cancellation, DTMF not sent, no H.264 / VP9 / AV1, no simulcast or SVC sending.
* Element layout is not re-triggered when the video size changes (untested).
* Real services (Meet, Zoom web, Teams, Discord, Jitsi) are unverified, see [webrtc-interop.md](webrtc-interop.md).

## Privacy

* **No local addresses by default.** The host candidate policy has three modes (`WebRTCHostCandidates`):
  * `WebRTCHostMDNS` (default): host candidates are published as `<random-uuid>.local` and resolved by multicast DNS, as in
    Chrome and Firefox. If mDNS cannot run on the machine, host candidates are dropped, never published raw (pion would
    otherwise fall back to raw addresses; the engine filters them).
  * `WebRTCHostNone`: no host candidate at all; connections need a server reflexive or relayed candidate.
  * `WebRTCHostRaw`: raw local addresses. Diagnostics and tests only.

  Set it with `engine.SetWebRTCHostCandidates(...)` before pages run, or `SWEEF_WEBRTC_HOST_CANDIDATES=mdns|none|raw`.
* The filter is applied at the page boundary to everything JavaScript can see: `icecandidate` events, `localDescription` and
  every other description getter, the SDP returned by `createOffer` / `createAnswer`, `getStats()` (`ip` / `address` of host
  candidates are blanked, `networkType` removed) and `RTCIceTransport.getSelectedCandidatePair()`. Server reflexive and
  relayed candidates keep their public address (the page asked for that server) but their related address is replaced by
  `0.0.0.0` / `0`, as Chrome does. `c=` lines are `0.0.0.0`.
* **No STUN or TURN traffic unless the page configures `iceServers`.** There are no default servers. A test points a page
  at a local UDP listener and checks that nothing arrives without `iceServers` and that the binding request arrives with
  them. Server host names are resolved by the system resolver (pion), not by the engine's.
* **Proxies:** WebRTC is UDP and cannot use an HTTP or SOCKS proxy. While a proxy is configured the constructor throws
  `NotSupportedError` instead of silently bypassing it. `engine.SetWebRTCAllowWithProxy(true)` or
  `SWEEF_WEBRTC_ALLOW_WITH_PROXY=1` lifts that.
* **Offline mode** (`Tab` offline emulation) drops the configured `iceServers`, so a peer gathers local candidates only.
* `engine.SetWebRTCEnabled(false)` or `SWEEF_WEBRTC=off` removes the whole API from pages loaded afterwards (the prelude
  does not define it), like a browser built without WebRTC.
* Each connection generates its own certificate; nothing is persisted between sessions.

## Data Saver

* **Counted.** Every unicast UDP socket pion opens goes through a counting `transport.Net` (`webrtc_net.go`) that feeds the
  browsing context's wire counters (`WireIn` / `WireOut`, `Engine.WireTotals()`), the same meter HTTP uses. The count is the
  socket total: DTLS, ICE, STUN, SCTP and RTP included, IP/UDP headers not, multicast mDNS excluded. It is **not** attributed
  to a kind or origin in `DataStats` (`ByKind` / `ByOrigin`), so it appears in "used" but not in the per-kind breakdown. Test:
  a 30,000 byte message between two peers in one page moved 35.7-35.9 kB in and out in the measured runs (both ends are
  counted because both peers live in the page).
* **Honoured:** at level `strict` outgoing video is capped at 300 kbit/s (tested). Background tabs under the saver run no timers; the event pump
  still drains the realtime queue for them, so data channel events are delivered (by construction, not covered by a test).
* Not done: refusing incoming media at `strict`. Data channels are not restricted at any level.

## Lifecycle

Peers belong to the page's VM. `jsVM.close()` (navigation away, tab close, engine close) closes every peer connection through
the registered closer, which also waits for the goroutines this feature started (media senders and receivers, RTP/RTCP readers). Tests
cover navigation, closing a tab with live peers, explicit `close()`, and goroutine counts over repeated connect/close cycles
(3 before, 3 after four cycles in the measured run). `close()` fires no events, rejects pending operations with
`InvalidStateError`, and leaves `signalingState`, `connectionState` and `iceConnectionState` `closed`.

## Limits and deviations

* **SDP munging is rejected.** pion requires `setLocalDescription` to receive exactly the offer or answer it generated
  (`InvalidModificationError`). Descriptions you got from `createOffer` / `createAnswer` / `localDescription` round-trip;
  edited ones do not.
* **Rollback** restores `signalingState` and drops the pending description. pion v4 has no rollback transition, so this
  writes its unexported fields; transceivers created by a rolled-back remote offer are not removed (the next offer reuses
  them).
* **Direction changes** (`transceiver.direction = ...`) also write a pion unexported field, because pion only accepts a
  direction when the transceiver is created. `TestWebRTCDirectionHook` and the rollback test fail loudly if a pion upgrade
  renames either field.
* `iceCandidatePoolSize` is accepted and ignored. `rtcpMuxPolicy: "negotiate"` behaves as `require`. `setParameters` applies `active`, `maxBitrate`,
  `maxFramerate`, `scaleResolutionDownBy` of the first encoding; further `sendEncodings` are ignored (one layer is sent).
  `RTCRtpSender.setStreams` only changes what a later `addTrack` advertises.
* `generateCertificate` ignores `expires` (pion certificates last 30 days) and supports ECDSA P-256 and RSA only.
* One `streamId` is advertised per sent track (the first stream passed to `addTrack`).
* No identity providers (`getIdentityAssertion` rejects), no `RTCRtpScriptTransform`, no insertable streams (probes fail gracefully), no
  `RTCPeerConnection.getDefaultIceServers`, no `icecandidateerror` events, no ICE-TCP candidates (active TCP is
  disabled).
* `maxMessageSize` is whatever pion's SCTP transport reports; a larger `send()` throws `TypeError`.
* pion logs are off. `SWEEF_WEBRTC_DEBUG=1` prints them to stderr.

## Binary size

Measured with `CGO_ENABLED=0 go build -ldflags='-s -w' ./cmd/sweef` on linux/amd64, Go 1.26.9, same machine, same flags,
before (origin/main at `be0a682`) and after:

| | bytes | MiB |
|---|---|---|
| before | 26,271,906 | 25.05 |
| after | 31,006,882 | 29.57 |
| increase | 4,734,976 | 4.52 (+18.0 %) |

The increase is pion (ICE, DTLS, SCTP, SRTP, TURN, mDNS) and its dependencies (`golang.org/x/crypto` is new to the module
graph). The prelude adds no measurable VM start-up cost (a VM build takes about 33 ms with or without it, the difference is
inside the run-to-run noise of about 3 ms).

## Tests

All run offline, over the loopback interface only (`rtcTestLoopback` restricts gathering to it), with generous polling
timeouts and no timing assertions. `go test ./internal/engine -run TestWebRTC`:

* `DataChannelLoopback`, `NegotiatedUnreliableChannels`, `BufferedAmountEvent`: two peers in one page exchange strings,
  `ArrayBuffer`s and a `Blob`, close; negotiated / unreliable channels and argument validation; `bufferedamountlow`.
* `AcrossTwoTabs`: two tabs of one engine, the signalling is done by the test.
* `Stats`, `CountedByWireMeter`: report contents and values; the wire meter sees the traffic.
* `TrackNegotiation`: `m=audio` and `m=video` in the offer, `msid`, `track` events with streams, `addTrack` /
  `replaceTrack` / `removeTrack`, attaching a received stream to a media element.
* `RollbackAndImplicitDescriptions`, `DirectionHook`, `EventsAndStates` (event order, `negotiationneeded`, `restartIce`,
  `close()`), `DTMFAndTransports`, `Certificate`, `ConfigurationValidation`, `DescriptionsAndCandidates`.
* Privacy: `CandidatePolicy` (the filter), `CandidatePrivacy` (no interface address reaches the page by default),
  `HostCandidateModes` (raw / none / mdns), `NoStunWithoutIceServers`, `DisabledAndProxy`.
* Implemented but **not covered by a test**: TURN relaying (only the STUN binding request is checked), offline mode,
  RSA certificates, the legacy callback forms, `close()` rejecting operations that are still pending.
* Lifecycle: `DisabledAndPeersClosedOnNavigation`, `NoGoroutineLeak`. The app-level leak test
  (`go test ./internal/app -run TestNoGoroutineLeakAcrossAppLifetimes`) passes.

### Media tests

All offline and deterministic: a synthetic moving pattern with a drawn frame counter and a pure tone, two peers in one
page, and a link simulator (`rtcLinkSim`) that adds loss, blackouts and a rate bottleneck to the UDP path.
`go test ./internal/engine -run 'TestMedia|TestVideo|TestAudio|TestCaptureStream|TestStream|TestGetUserMedia'` covers
`readyState`, PSNR, counter progress with under 5 % dropped over 5 s, resolution adaptation under a bandwidth cap, PLI
recovery, mute/unmute, `replaceTrack`, `setParameters`, hang-up cleanup and goroutine counts, stats fields, SDP shape,
codec preferences and simulcast answering. Package tests: `go test ./internal/rtcmedia ./internal/media/vp8enc`
(`-short` skips the slow ones; the encoder benchmark reports encode time and CPU per frame).
