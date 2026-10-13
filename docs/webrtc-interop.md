# WebRTC interoperability with call services

Read [webrtc.md](webrtc.md) first for what the engine implements. This page lists, per service, what the page needs from
the browser, what Sweef provides, and what is **unverified**.

**Nothing here was tested against a real service.** The environment this was built in only reaches the internet through
an HTTPS proxy: no UDP, no STUN/TURN, no real signalling servers, no second browser. All media tests are offline loopback
tests between two peers in one page (`go test ./internal/engine -run 'TestMedia|TestVideo|TestAudio'`). Treat every
"provides" line below as "has the API and passes the offline tests", not as "works with the service". No service is
claimed to work.

## What every service needs, and what Sweef provides

| Need | Sweef |
|---|---|
| `RTCPeerConnection`, Unified Plan, BUNDLE, rtcp-mux, ICE (trickle), DTLS-SRTP | yes (pion) |
| VP8 video, Opus audio | yes, the only codecs offered |
| H.264, VP9, AV1 | **no**; peers that insist on them cannot send us video |
| `getUserMedia` / `getDisplayMedia` | provided by the device capture work, not this one; the hooks are `RegisterRTCTrackSource` / `SetRTCTrackResolver` |
| Echo cancellation, noise suppression | **no**; use headphones. `getSettings()` does not claim them |
| `getStats()` with `outbound-rtp`, `inbound-rtp`, `remote-inbound-rtp`, `candidate-pair` | yes |
| `setParameters` (bitrate, framerate, scale, active), `replaceTrack`, `setCodecPreferences`, capabilities | yes |
| Simulcast sending (several layers) | **no**: a remote simulcast offer is accepted, one layer is sent |
| Insertable streams / `RTCRtpScriptTransform` (E2EE) | **no**; feature detection fails |
| UDP reachability | not through an HTTP/SOCKS proxy; the constructor throws while a proxy is set unless `SetWebRTCAllowWithProxy(true)` |
| JavaScript speed for large call apps | unmeasured for these apps |
| Real-time 720p30 encode | **not on a loaded 4-core box** (see webrtc.md); 360p30 is |

## Google Meet

* Needs: unified plan with simulcast (it sends several layers and expects the sender to follow `setParameters` /
  `sendEncodings`), VP8 or VP9 or AV1 negotiation, data channels, transport-cc, `getStats`, insertable streams for some
  features, camera and microphone with echo cancellation.
* Sweef: VP8 + Opus, one layer, data channels, transport-cc, stats, no insertable streams, no echo cancellation.
* Unverified: whether Meet accepts a client that sends one VP8 layer, whether its page code runs, and whether it blocks
  unknown user agents. Likely gaps: no simulcast, Meet's reliance on features the engine lacks elsewhere (Workers,
  WebCodecs, WebAssembly threads, etc.).

## Zoom web client

* Needs: WebRTC for the web client, or WebSocket media with WebAssembly decoding in the fallback path; `SharedArrayBuffer`
  and WebAssembly are required by the Zoom web client for video.
* Sweef: WebRTC as above. WebAssembly is separate work in this repository.
* Unverified: everything. Zoom's own transport may not use the WebRTC media path at all.

## Jitsi Meet

* Needs: WebRTC with VP8 (Jitsi's default), Opus, data channels (bridge channel), simulcast (`sendEncodings` with three
  layers, `a=simulcast`), transport-cc, `getStats`, `RTCRtpSender.setParameters`. The conference runs through the
  videobridge, which also works with one layer, at reduced quality for large rooms.
* Sweef: VP8 + Opus, data channels, one layer, accepts a simulcast offer from the bridge.
* Unverified: the full join flow (XMPP over WebSocket, bridge ICE/UDP) and whether the bridge accepts a single layer in
  the configured mode. Jitsi is the likeliest candidate of the five because its media stack is standard VP8 + Opus, but
  that is an expectation, not a result.

## Microsoft Teams (web)

* Needs: WebRTC with H.264 preferred in many tenants, plus its own scripts and likely insertable streams or WebAssembly
  components, camera/microphone, and a user agent it recognises (Teams limits supported browsers).
* Sweef: no H.264. VP8 is only used if Teams offers it.
* Unverified: everything; Teams may refuse to run or negotiate no common video codec.

## Discord (web)

* Needs: WebRTC with VP8/H.264 for video and Opus for voice, data channels, `getUserMedia`, an `RTCPeerConnection`
  signalled through Discord's gateway, and, for current voice servers, encrypted transport details of its own.
* Sweef: VP8 + Opus.
* Unverified: everything.

## Browser-only checks that were done

* SDP shape (codecs, payload types, rtcp-fb, extensions, msid, bundle, simulcast answer) is asserted in
  `webrtc_sdp_test.go` and `webrtc_simulcast_test.go`.
* Loss, blackout and a bandwidth cap are simulated in `webrtc_media_loss_test.go`.
* The VP8 stream decodes with ffmpeg (libvpx) when ffmpeg is installed (`vp8enc` tests).

## How to verify against a service

Run the browser with a UDP-capable network, `SWEEF_WEBRTC_DEBUG=1` for pion logs, open a call, and check `getStats()` in a
page console (`bytesSent`, `framesDecoded`, `availableOutgoingBitrate`). Please record the result in this file with the
date and the Sweef build, including failures.
