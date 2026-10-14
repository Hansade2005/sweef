package engine

import (
	"strings"
	"testing"
	"time"
)

// The offer a page gets must look like a browser's: VP8 with RTX, Opus with in-band FEC, telephone-event,
// the feedback types and header extensions services negotiate on, msid, BUNDLE and rtcp-mux.
func TestWebRTCOfferSDP(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, `(function () {
	  window.__swFakeMediaDevices = true; window.sdp = null; window.errs = [];
	  var a = new RTCPeerConnection();
	  navigator.mediaDevices.getUserMedia({ audio: true, video: true }).then(function (s) {
	    s.getTracks().forEach(function (t) { a.addTrack(t, s); });
	    return a.createOffer();
	  }).then(function (o) { window.sdp = o.sdp; }, function (e) { errs.push(String(e)); });
	  return 1; })()`, nil)
	waitJS(t, tab, `sdp !== null || errs.length`, 10*time.Second)
	var sdp string
	evalJSON(t, tab, `sdp || errs.join()`, &sdp)
	t.Logf("offer:\n%s", sdp)
	for _, want := range []string{
		"a=group:BUNDLE", "a=rtcp-mux", "a=msid:", "a=setup:actpass",
		"a=rtpmap:96 VP8/90000", "a=rtpmap:97 rtx/90000", "a=fmtp:97 apt=96",
		"a=rtpmap:111 opus/48000/2", "minptime=10;useinbandfec=1", "telephone-event/8000",
		"a=rtcp-fb:96 goog-remb", "a=rtcp-fb:96 transport-cc", "a=rtcp-fb:96 ccm fir", "a=rtcp-fb:96 nack\r\n", "a=rtcp-fb:96 nack pli",
		"a=rtcp-fb:111 transport-cc",
		"urn:ietf:params:rtp-hdrext:sdes:mid", "urn:ietf:params:rtp-hdrext:ssrc-audio-level",
		"http://www.webrtc.org/experiments/rtp-hdrext/abs-send-time", "http://www.ietf.org/id/draft-holmer-rmcat-transport-wide-cc-extensions-01",
		"urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id", "urn:3gpp:video-orientation", "a=ssrc-group:FID",
	} {
		if !strings.Contains(sdp, want) {
			t.Errorf("offer lacks %q", want)
		}
	}
	for _, bad := range []string{"a=rtcp-fb:97", "a=rtcp-fb:126", "VP9", "H264", "AV1", "PCMU", "G722"} {
		if strings.Contains(sdp, bad) {
			t.Errorf("offer advertises %s, which this build cannot send", bad)
		}
	}
	// capabilities, codec preferences
	var res struct {
		VCodecs []string
		ACodecs []string
		Exts    int
		Pref    string
		PrefErr string
		Reset   string
	}
	evalJSON(t, tab, `(function () {
	  var r = {};
	  r.vCodecs = RTCRtpSender.getCapabilities('video').codecs.map(function (c) { return c.mimeType; });
	  r.aCodecs = RTCRtpReceiver.getCapabilities('audio').codecs.map(function (c) { return c.mimeType; });
	  r.exts = RTCRtpSender.getCapabilities('video').headerExtensions.length;
	  var pc = new RTCPeerConnection(); var tr = pc.addTransceiver('video');
	  var caps = RTCRtpSender.getCapabilities('video').codecs;
	  tr.setCodecPreferences(caps.slice().reverse());
	  r.pref = 'ok';
	  try { tr.setCodecPreferences([{ mimeType: 'video/H264', clockRate: 90000 }]); } catch (e) { r.prefErr = e.name; }
	  try { tr.setCodecPreferences(caps.filter(function (c) { return /rtx/.test(c.mimeType); })); } catch (e) { r.prefErr += ',' + e.name; }
	  tr.setCodecPreferences([]); r.reset = 'ok';
	  return r; })()`, &res)
	t.Logf("capabilities: video %v audio %v, %d header extensions; invalid preference -> %s; reset %s", res.VCodecs, res.ACodecs, res.Exts, res.PrefErr, res.Reset)
	if len(res.VCodecs) != 2 || res.VCodecs[0] != "video/VP8" || len(res.ACodecs) < 1 || res.ACodecs[0] != "audio/opus" || res.Exts < 4 {
		t.Errorf("capabilities %+v", res)
	}
	if !strings.Contains(res.PrefErr, "InvalidModificationError") || res.Reset != "ok" {
		t.Errorf("codec preferences: %+v", res)
	}
}

// setCodecPreferences must change what the SDP contains, not just be validated.
func TestWebRTCCodecPreferencesReachSDP(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, `(function () {
	  window.sdp = null;
	  var a = new RTCPeerConnection();
	  var tr = a.addTransceiver('audio');
	  var caps = RTCRtpSender.getCapabilities('audio').codecs.filter(function (c) { return c.mimeType === 'audio/opus'; });
	  tr.setCodecPreferences(caps);
	  a.createOffer().then(function (o) { window.sdp = o.sdp; });
	  return 1; })()`, nil)
	waitJS(t, tab, `sdp !== null`, 5*time.Second)
	var sdp string
	evalJSON(t, tab, `sdp`, &sdp)
	if !strings.Contains(sdp, "opus/48000/2") || strings.Contains(sdp, "telephone-event") {
		t.Errorf("preferences (opus only) not applied:\n%s", sdp)
	}
}
