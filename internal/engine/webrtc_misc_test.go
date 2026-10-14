package engine

import (
	"strings"
	"testing"
	"time"
)

// A page that edits the SDP (stereo Opus, a bandwidth line, vendor flags) must still be able to apply it: the
// edit is not honoured, but setLocalDescription does not fail and the call goes on.
func TestWebRTCEditedSDPIsAccepted(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	var res map[string]any
	evalJSON(t, tab, `(async function () {
	  var out = {};
	  var a = new RTCPeerConnection(), b = new RTCPeerConnection();
	  a.addTransceiver('audio'); a.addTransceiver('video');
	  var o = await a.createOffer();
	  var munged = o.sdp.replace('useinbandfec=1', 'useinbandfec=1;stereo=1;sprop-stereo=1').replace(/(m=video[^\r\n]*\r\n)/, '$1b=AS:500\r\n') + 'a=x-google-flag:conference\r\n';
	  out.changed = munged !== o.sdp;
	  try { await a.setLocalDescription({ type: 'offer', sdp: munged }); out.local = 'ok'; } catch (e) { out.local = e.name + ': ' + e.message; }
	  out.state = a.signalingState;
	  await b.setRemoteDescription(a.localDescription);
	  var an = await b.createAnswer();
	  var m2 = an.sdp.replace('useinbandfec=1', 'useinbandfec=1;stereo=1');
	  try { await b.setLocalDescription({ type: 'answer', sdp: m2 }); out.answer = 'ok'; } catch (e) { out.answer = e.name; }
	  // a change that is not an edit of codec lines (another media section) is still refused
	  var b2 = new RTCPeerConnection(); b2.addTransceiver('audio');
	  var o2 = await b2.createOffer();
	  try { await b2.setLocalDescription({ type: 'offer', sdp: o2.sdp.replace(/a=mid:0/g, 'a=mid:zz').replace('BUNDLE 0', 'BUNDLE zz') }); out.shape = 'accepted'; } catch (e) { out.shape = e.name; }
	  return out; })()`, &res)
	t.Logf("%v", res)
	if res["changed"] != true || res["local"] != "ok" || res["state"] != "have-local-offer" || res["answer"] != "ok" {
		t.Errorf("edited descriptions: %v", res)
	}
	if res["shape"] != "InvalidModificationError" {
		t.Errorf("a structurally different description was %v", res["shape"])
	}
	warned := strings.Contains(consoleText(tab), "edited SDP")
	if !warned {
		t.Errorf("no console warning about the unapplied edit")
	}
}

// jitterBufferTarget (and the older playoutDelayHint) raise the least delay of the receiver's jitter buffer.
func TestWebRTCJitterBufferTarget(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, callJS(640, 360, 30), nil)
	waitJS(t, tab, `rv.readyState >= 2 && rv.videoWidth === 640`, 20*time.Second)
	var res map[string]any
	evalJSON(t, tab, `(function () {
	  var r = b.getReceivers().filter(function (x) { return x.track.kind === 'video'; })[0], out = { initial: r.jitterBufferTarget };
	  r.jitterBufferTarget = 600; out.set = r.jitterBufferTarget; out.hint = r.playoutDelayHint;
	  try { r.jitterBufferTarget = 5000; out.range = 'ok'; } catch (e) { out.range = e.name; }
	  r.jitterBufferTarget = 600;
	  return out; })()`, &res)
	if res["initial"] != nil || res["set"] != 600.0 || res["hint"] != 0.6 || res["range"] != "RangeError" {
		t.Fatalf("jitterBufferTarget: %v", res)
	}
	pumpFor(tab, 4*time.Second)
	var avg float64
	evalJSON(t, tab, `(async function () {
	  var s = await b.getStats(), v = 0;
	  s.forEach(function (r) { if (r.type === 'inbound-rtp' && r.kind === 'video' && r.jitterBufferEmittedCount > 0) v = r.jitterBufferDelay / r.jitterBufferEmittedCount; });
	  return v; })()`, &avg)
	t.Logf("average video jitter buffer delay %.3f s", avg)
	if avg < 0.4 {
		t.Errorf("average jitter buffer delay %.3f s with a 0.6 s target", avg)
	}
}
