# WebRTC

`RTCPeerConnection` and the classes around it, built on [pion/webrtc v4](https://github.com/pion/webrtc) (MIT, pure Go, no
cgo). Data channels and signalling are complete enough for real applications; **media is partial and says so below**.

Files: `internal/engine/webrtc.go` (peers, host calls, privacy policy), `webrtc_net.go` (byte metering),
`webrtc_prelude.js` (the page-facing classes), tests in `webrtc_test.go` and `webrtc_events_test.go`.

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

Be clear-eyed: the engine has no camera, microphone or screen capture, and no audio or video encoder reachable from a
`MediaStreamTrack` (the existing `getUserMedia` returns fake tracks only when fake devices are enabled, and they carry no
samples).

* **Negotiation is real.** Offers and answers contain `m=audio` / `m=video` with Opus, G.722, PCMU, PCMA, VP8, VP9, H.264
  and AV1 (pion's default codec set); `msid` stream and track ids are carried and parsed; `track` events fire with the right
  `receiver`, `track`, `streams` and `transceiver`; `addTrack`, `removeTrack`, `replaceTrack`, directions and
  `currentDirection` follow the spec.
* **Sending audio:** a live, attached audio track sends real Opus RTP packets that carry **silence** (20 ms frames,
  50 packets/s) once the connection is up. It keeps the stream, the remote jitter buffer and the stats alive; it is not the
  microphone. `track.stop()` and `replaceTrack(null)` stop it.
* **Sending video:** negotiated, but **no frames are produced**.
* **Receiving:** RTP and RTCP are accepted, read and counted (stats), then dropped. The received `MediaStreamTrack` attaches
  to a media element (`video.srcObject = stream` works) but produces no picture or sound. The Opus decoder in
  `internal/media` could feed Web Audio later; it is not wired.
* Data Saver `strict` level: no silence frames are sent. A remote peer can still send media to us (it is counted and
  dropped); to refuse it set the transceiver `direction` to `inactive`.

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
* **Honoured:** at level `strict` the silence audio feed stops. Background tabs under the saver run no timers; the event pump
  still drains the realtime queue for them, so data channel events are delivered (by construction, not covered by a test).
* Not done: refusing incoming media at `strict`, or capping bitrate. Data channels are not restricted at any level.

## Lifecycle

Peers belong to the page's VM. `jsVM.close()` (navigation away, tab close, engine close) closes every peer connection through
the registered closer, which also waits for the goroutines this feature started (silence pump, RTP/RTCP readers). Tests
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
* `iceCandidatePoolSize` is accepted and ignored. `rtcpMuxPolicy: "negotiate"` behaves as `require`. `setCodecPreferences`
  is validated but the engine's own codec order is used. `setParameters` stores the values; only "all encodings inactive"
  has an effect (the silence feed stops). `sendEncodings` / simulcast, `scaleResolutionDownBy`, `maxBitrate` have no effect.
  `RTCRtpSender.setStreams` only changes what a later `addTrack` advertises.
* `generateCertificate` ignores `expires` (pion certificates last 30 days) and supports ECDSA P-256 and RSA only.
* One `streamId` is advertised per sent track (the first stream passed to `addTrack`).
* No identity providers (`getIdentityAssertion` rejects), no `RTCRtpScriptTransform`, no insertable streams, no
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
