package engine

import (
	"strings"
	"testing"
	"time"
)

// A remote offer with simulcast (what Chrome sends to a media server) must be answered so that the remote
// sends its layers to us ("a=simulcast:recv"), and the page must see one track.
func TestWebRTCAnswersSimulcastOffer(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	offer := strings.Join([]string{
		"v=0",
		"o=- 4611731400430051336 2 IN IP4 127.0.0.1",
		"s=-",
		"t=0 0",
		"a=group:BUNDLE 0",
		"a=extmap-allow-mixed",
		"a=msid-semantic: WMS stream",
		"m=video 9 UDP/TLS/RTP/SAVPF 96 97",
		"c=IN IP4 0.0.0.0",
		"a=rtcp:9 IN IP4 0.0.0.0",
		"a=ice-ufrag:abcd",
		"a=ice-pwd:abcdefghijklmnopqrstuvwx",
		"a=ice-options:trickle",
		"a=fingerprint:sha-256 00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF",
		"a=setup:actpass",
		"a=mid:0",
		"a=extmap:1 urn:ietf:params:rtp-hdrext:sdes:mid",
		"a=extmap:2 urn:ietf:params:rtp-hdrext:sdes:rtp-stream-id",
		"a=extmap:3 urn:ietf:params:rtp-hdrext:sdes:repaired-rtp-stream-id",
		"a=sendonly",
		"a=msid:stream track",
		"a=rtcp-mux",
		"a=rtcp-rsize",
		"a=rtpmap:96 VP8/90000",
		"a=rtcp-fb:96 nack",
		"a=rtcp-fb:96 nack pli",
		"a=rtpmap:97 rtx/90000",
		"a=fmtp:97 apt=96",
		"a=rid:h send",
		"a=rid:m send",
		"a=rid:l send",
		"a=simulcast:send h;m;l",
		"",
	}, "\r\n")
	evalJSON(t, tab, `(function () {
	  window.ans = null; window.errs = []; window.trackCount = 0;
	  var pc = new RTCPeerConnection();
	  pc.ontrack = function () { trackCount++; };
	  pc.setRemoteDescription({ type: 'offer', sdp: `+jsString(offer)+` })
	    .then(function () { return pc.createAnswer(); })
	    .then(function (a) { window.ans = a.sdp; return pc.setLocalDescription(a); })
	    .catch(function (e) { errs.push(String(e && e.message || e)); });
	  return 1; })()`, nil)
	waitJS(t, tab, `ans !== null || errs.length`, 10*time.Second)
	var ans, errs string
	evalJSON(t, tab, `ans || ''`, &ans)
	evalJSON(t, tab, `errs.join()`, &errs)
	t.Logf("answer:\n%s\nerrors: %q", ans, errs)
	if errs != "" || ans == "" {
		t.Fatalf("simulcast offer not accepted: %s", errs)
	}
	if !strings.Contains(ans, "a=simulcast:recv") {
		t.Errorf("the answer does not accept the simulcast layers (no a=simulcast:recv)")
	}
	if !strings.Contains(ans, "a=recvonly") {
		t.Errorf("answer is not recvonly")
	}
}

func jsString(s string) string {
	b := strings.Builder{}
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\r':
			b.WriteString(`\r`)
		case '\n':
			b.WriteString(`\n`)
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
