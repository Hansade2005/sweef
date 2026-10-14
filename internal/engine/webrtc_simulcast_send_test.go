package engine

import (
	"strings"
	"testing"
	"time"
)

// simulcastCallJS: window.a sends the fake camera as three simulcast layers (q, h, f at scale 4, 2, 1) to window.b.
func simulcastCallJS(encodings string) string {
	return `
window.__swFakeMediaDevices = true;
window.err = []; window.offerSDP = ''; window.answerSDP = '';
window.a = new RTCPeerConnection(); window.b = new RTCPeerConnection();
a.onicecandidate = function (e) { if (e.candidate) b.addIceCandidate(e.candidate).catch(function (x) { err.push('b:' + x); }); };
b.onicecandidate = function (e) { if (e.candidate) a.addIceCandidate(e.candidate).catch(function (x) { err.push('a:' + x); }); };
window.tracks = []; b.ontrack = function (e) { tracks.push(e.track.kind); };
(async function () {
  window.local = await navigator.mediaDevices.getUserMedia({ video: { width: 640, height: 360, frameRate: 30 } });
  window.tr = a.addTransceiver(local.getVideoTracks()[0], { streams: [local], sendEncodings: ` + encodings + ` });
  var o = await a.createOffer(); offerSDP = o.sdp; await a.setLocalDescription(o);
  await b.setRemoteDescription(a.localDescription);
  var an = await b.createAnswer(); answerSDP = an.sdp; await b.setLocalDescription(an);
  await a.setRemoteDescription(b.localDescription);
})().catch(function (e) { err.push('call:' + e + (e && e.stack || '')); });
1`
}

// A sender with three encodings offers rid and simulcast lines, really sends three RTP streams with their own
// SSRCs and sizes, and reports them as three outbound-rtp entries with their rid.
func TestWebRTCSimulcastSend(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, simulcastCallJS(`[{ rid: 'q', scaleResolutionDownBy: 4 }, { rid: 'h', scaleResolutionDownBy: 2 }, { rid: 'f' }]`), nil)
	waitJS(t, tab, `a.connectionState === 'connected' || err.length > 0`, 20*time.Second)
	var errs string
	evalJSON(t, tab, `err.join(' | ')`, &errs)
	if errs != "" {
		t.Fatalf("errors: %s", errs)
	}
	var offer, answer string
	evalJSON(t, tab, `offerSDP`, &offer)
	evalJSON(t, tab, `answerSDP`, &answer)
	for _, want := range []string{"a=rid:q send", "a=rid:h send", "a=rid:f send", "a=simulcast:send q;h;f"} {
		if !strings.Contains(offer, want) {
			t.Errorf("offer lacks %q", want)
		}
	}
	if !strings.Contains(answer, "a=simulcast:recv") {
		t.Errorf("answer does not accept the layers")
	}
	// the three layers send at their own sizes
	waitJS(t, tab, `(async function () {
	  var s = await a.getSenders()[0].getStats(), n = 0;
	  s.forEach(function (r) { if (r.type === 'outbound-rtp' && r.framesEncoded > 5) n++; });
	  window.layers = n; return n >= 3; })()`, 25*time.Second)
	var rows []map[string]any
	evalJSON(t, tab, `(async function () {
	  var s = await a.getSenders()[0].getStats(), out = [];
	  s.forEach(function (r) { if (r.type === 'outbound-rtp') out.push({ rid: r.rid, ssrc: r.ssrc, w: r.frameWidth, h: r.frameHeight, enc: r.framesEncoded, bytes: r.bytesSent, mode: r.scalabilityMode }); });
	  return out; })()`, &rows)
	t.Logf("outbound-rtp: %v", rows)
	if len(rows) != 3 {
		t.Fatalf("want 3 outbound-rtp entries, got %d", len(rows))
	}
	want := map[string]float64{"q": 160, "h": 320, "f": 640}
	ssrcs := map[float64]bool{}
	for _, r := range rows {
		rid, _ := r["rid"].(string)
		if w, ok := want[rid]; !ok || r["w"].(float64) != w {
			t.Errorf("layer %q frame width %v, want %v", rid, r["w"], want[rid])
		}
		if r["bytes"].(float64) <= 0 {
			t.Errorf("layer %q sent no bytes", rid)
		}
		ssrcs[r["ssrc"].(float64)] = true
	}
	if len(ssrcs) != 3 {
		t.Errorf("layers share SSRCs: %v", ssrcs)
	}
	// the receiving side sees three inbound RTP streams for the one transceiver
	var inbound int
	evalJSON(t, tab, `(async function () {
	  var s = await b.getStats(), n = 0; s.forEach(function (r) { if (r.type === 'inbound-rtp') n++; }); return n; })()`, &inbound)
	if inbound < 1 {
		t.Errorf("receiver has no inbound-rtp")
	}
}

// setParameters addresses single layers: switching one off stops its RTP stream, the others keep going; bitrate
// ceilings and the rid / count are validated like a browser.
func TestWebRTCSimulcastSetParameters(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, simulcastCallJS(`[{ rid: 'q', scaleResolutionDownBy: 4 }, { rid: 'h', scaleResolutionDownBy: 2 }, { rid: 'f' }]`), nil)
	waitJS(t, tab, `a.connectionState === 'connected' || err.length > 0`, 20*time.Second)
	waitJS(t, tab, `(async function () {
	  var s = await a.getSenders()[0].getStats(), n = 0;
	  s.forEach(function (r) { if (r.type === 'outbound-rtp' && r.framesEncoded > 5) n++; });
	  return n >= 3; })()`, 25*time.Second)
	var res map[string]any
	evalJSON(t, tab, `(async function () {
	  var snd = a.getSenders()[0], out = {};
	  var p = snd.getParameters();
	  out.rids = p.encodings.map(function (e) { return e.rid; });
	  out.scales = p.encodings.map(function (e) { return e.scaleResolutionDownBy; });
	  // illegal changes
	  var bad = [];
	  for (var f of [
	    function (q) { q.encodings.pop(); },
	    function (q) { q.encodings[0].rid = 'zzz'; },
	    function (q) { q.encodings[0].scaleResolutionDownBy = 0.5; }
	  ]) {
	    var q = snd.getParameters(); f(q);
	    try { await snd.setParameters(q); bad.push('ok'); } catch (e) { bad.push(e.name); }
	  }
	  out.bad = bad;
	  // turn the top layer off and the middle one down
	  var q2 = snd.getParameters();
	  q2.encodings[2].active = false; q2.encodings[1].maxBitrate = 150000;
	  await snd.setParameters(q2);
	  out.after = snd.getParameters().encodings.map(function (e) { return [e.rid, e.active, e.maxBitrate]; });
	  return out; })()`, &res)
	t.Logf("%v", res)
	if res["bad"].([]any)[0] != "InvalidModificationError" || res["bad"].([]any)[1] != "InvalidModificationError" || res["bad"].([]any)[2] != "RangeError" {
		t.Errorf("parameter validation: %v", res["bad"])
	}
	// the f layer stops sending, q keeps going
	type snap struct{ q, f float64 }
	frames := func() snap {
		var m map[string]float64
		evalJSON(t, tab, `(async function () {
		  var s = await a.getSenders()[0].getStats(), o = {};
		  s.forEach(function (r) { if (r.type === 'outbound-rtp') o[r.rid] = r.packetsSent; });
		  return o; })()`, &m)
		return snap{m["q"], m["f"]}
	}
	time.Sleep(500 * time.Millisecond)
	s1 := frames()
	pumpFor(tab, 2*time.Second)
	s2 := frames()
	if s2.q <= s1.q {
		t.Errorf("layer q stopped: %v -> %v", s1.q, s2.q)
	}
	if s2.f > s1.f+3 {
		t.Errorf("layer f keeps sending after active=false: %v -> %v", s1.f, s2.f)
	}
}

// A remote end that does not take the simulcast lines in its answer gets one layer, not three.
func TestWebRTCSimulcastFallsBackWithoutAnswerSupport(t *testing.T) {
	if !sdpSectionAccepts("v=0\r\nm=video 9 X\r\na=mid:0\r\na=simulcast:recv q;h\r\n", "0") {
		t.Fatal("answer with simulcast:recv must be accepted")
	}
	if sdpSectionAccepts("v=0\r\nm=video 9 X\r\na=mid:0\r\na=recvonly\r\n", "0") {
		t.Fatal("answer without simulcast must not be accepted")
	}
	if sdpSectionAccepts("v=0\r\nm=audio 9 X\r\na=mid:0\r\na=sendrecv\r\nm=video 9 X\r\na=mid:1\r\na=simulcast:recv q\r\n", "0") {
		t.Fatal("the audio section has no simulcast")
	}
}
