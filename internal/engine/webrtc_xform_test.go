package engine

import (
	"fmt"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/rtcmedia"
)

// xorStream is the JavaScript of a tiny "end-to-end encryption": every payload byte after the codec's clear header
// (10 bytes of a VP8 key frame, 3 of a delta frame, 1 of an Opus packet) is XORed with a key. Applied on the send
// side and again on the receive side it is the identity; a receiver that does not undo it cannot decode.
const xorFn = `
function xorFrame(frame, key) {
  var d = new Uint8Array(frame.data);
  var skip = frame.type === 'key' ? 10 : frame.type === 'delta' ? 3 : 1;
  for (var i = skip; i < d.length; i++) d[i] ^= key;
  frame.data = d.buffer;
  return frame;
}`

// encodedCallJS builds a call whose two ends transform every encoded frame. mode: "streams" uses
// createEncodedStreams on the page's thread; "script" uses RTCRtpScriptTransform with a worker.
func encodedCallJS(mode string, recvKey int) string {
	attachSend, attachRecv := "", ""
	switch mode {
	case "streams":
		attachSend = `
    var s = snd.createEncodedStreams();
    s.readable.pipeThrough(new TransformStream({ transform: function (f, c) { stats.sent++; c.enqueue(xorFrame(f, 0x5a)); } })).pipeTo(s.writable);`
		attachRecv = fmt.Sprintf(`
    var r = rcv.createEncodedStreams();
    r.readable.pipeThrough(new TransformStream({ transform: function (f, c) { stats.recv++; c.enqueue(xorFrame(f, %d)); } })).pipeTo(r.writable);`, recvKey)
	case "script":
		attachSend = `
    snd.transform = new RTCRtpScriptTransform(worker, { key: 0x5a });`
		attachRecv = fmt.Sprintf(`
    rcv.transform = new RTCRtpScriptTransform(worker, { key: %d });`, recvKey)
	}
	return fmt.Sprintf(`
window.__swFakeMediaDevices = true;
window.err = []; window.stats = { sent: 0, recv: 0 };
`+xorFn+`
var cfg = { encodedInsertableStreams: true };
window.a = new RTCPeerConnection(cfg); window.b = new RTCPeerConnection(cfg);
a.onicecandidate = function (e) { if (e.candidate) b.addIceCandidate(e.candidate).catch(function (x) { err.push('b:' + x); }); };
b.onicecandidate = function (e) { if (e.candidate) a.addIceCandidate(e.candidate).catch(function (x) { err.push('a:' + x); }); };
var rv = document.createElement('video'); rv.id = 'rv'; rv.autoplay = true; rv.muted = true; document.body.appendChild(rv); window.rv = rv;
var worker = null;
if (%q === 'script') {
  var src = 'onrtctransform = function (event) { var t = event.transformer, key = t.options.key;' +
    'function xorFrame(frame, key) { var d = new Uint8Array(frame.data); var skip = frame.type === "key" ? 10 : frame.type === "delta" ? 3 : 1; for (var i = skip; i < d.length; i++) d[i] ^= key; frame.data = d.buffer; return frame; }' +
    't.readable.pipeThrough(new TransformStream({ transform: function (f, c) { c.enqueue(xorFrame(f, key)); } })).pipeTo(t.writable); };';
  worker = new Worker(URL.createObjectURL(new Blob([src], { type: 'text/javascript' })));
}
b.ontrack = function (e) {
  if (e.track.kind === 'video') rv.srcObject = e.streams[0];
  var rcv = e.receiver;
  %s
};
(async function () {
  window.local = await navigator.mediaDevices.getUserMedia({ video: { width: 640, height: 360, frameRate: 30 }, audio: true });
  local.getTracks().forEach(function (t) {
    var snd = a.addTrack(t, local);
    %s
  });
  var o = await a.createOffer(); await a.setLocalDescription(o);
  await b.setRemoteDescription(a.localDescription);
  var an = await b.createAnswer(); await b.setLocalDescription(an);
  await a.setRemoteDescription(b.localDescription);
})().catch(function (e) { err.push('call:' + e + (e && e.stack || '')); });
1`, mode, attachRecv, attachSend)
}

func runEncodedCall(t *testing.T, mode string, recvKey int) (*Tab, *frameLog, *rtcmedia.RecordSink) {
	t.Helper()
	rtcMode(t, WebRTCHostRaw)
	fl := newFrameLog()
	fl.install(t)
	sink := &rtcmedia.RecordSink{}
	SetRTCAudioSinkFactory(func() rtcmedia.AudioSink { return sink })
	t.Cleanup(func() { SetRTCAudioSinkFactory(nil) })
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, encodedCallJS(mode, recvKey), nil)
	return tab, fl, sink
}

func testEncodedRoundTrip(t *testing.T, mode string) {
	tab, fl, sink := runEncodedCall(t, mode, 0x5a)
	waitJS(t, tab, `rv.readyState >= 2 && rv.videoWidth === 640 && rv.videoHeight === 360`, 25*time.Second)
	pumpFor(tab, 2*time.Second)
	var errs string
	evalJSON(t, tab, `err.join(' | ')`, &errs)
	if errs != "" {
		t.Fatalf("errors: %s", errs)
	}
	n, counters, last := fl.snapshot()
	if n < 15 || len(counters) < 15 {
		t.Fatalf("only %d frames decoded through the transforms (%d with a readable counter)", n, len(counters))
	}
	if last != nil {
		if p := psnrYCbCr(last, 640, 360, uint32(counters[len(counters)-1])); p < 28 {
			t.Errorf("picture after sender and receiver transforms has PSNR %.1f dB", p)
		}
	}
	if mode == "streams" {
		var st map[string]float64
		evalJSON(t, tab, `stats`, &st)
		if st["sent"] < 20 || st["recv"] < 20 {
			t.Errorf("transform call counts %v", st)
		}
	}
	// audio went through the same transforms: the receiver played samples
	waitJS(t, tab, `(async function () { var s = await b.getStats(), n = 0; s.forEach(function (r) { if (r.type === 'inbound-rtp' && r.kind === 'audio' && r.totalSamplesReceived > 4800) n++; }); return n > 0; })()`, 10*time.Second)
	_ = sink
}

func TestWebRTCEncodedStreams(t *testing.T) { testEncodedRoundTrip(t, "streams") }

func TestWebRTCScriptTransform(t *testing.T) { testEncodedRoundTrip(t, "script") }

// A receiver that cannot undo the sender's transform decodes nothing (frames are corrupt), and keeps asking for
// key frames instead of crashing; the video element never gets a picture of the right size with a counter.
func TestWebRTCEncodedStreamsWrongKey(t *testing.T) {
	tab, fl, _ := runEncodedCall(t, "streams", 0x33)
	pumpFor(tab, 4*time.Second)
	_, counters, _ := fl.snapshot()
	if len(counters) > 3 {
		t.Fatalf("%d readable frames decoded with the wrong key", len(counters))
	}
	var errs string
	evalJSON(t, tab, `err.join(' | ')`, &errs)
	if errs != "" {
		t.Fatalf("errors: %s", errs)
	}
}

func TestWebRTCEncodedStreamsRequireOptIn(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	var got string
	evalJSON(t, tab, `(function () {
	  var out = [];
	  var pc = new RTCPeerConnection(), t = pc.addTransceiver('video');
	  out.push(typeof t.sender.createEncodedStreams, typeof t.receiver.createEncodedStreams, 'transform' in t.sender, typeof RTCRtpScriptTransform, typeof RTCEncodedVideoFrame, typeof RTCEncodedAudioFrame, typeof RTCTransformEvent);
	  try { t.sender.createEncodedStreams(); out.push('ok'); } catch (e) { out.push(e.name); }
	  try { t.sender.transform = {}; out.push('ok'); } catch (e) { out.push(e.name); }
	  try { new RTCRtpScriptTransform({}); out.push('ok'); } catch (e) { out.push(e.name); }
	  var pc2 = new RTCPeerConnection({ encodedInsertableStreams: true }), t2 = pc2.addTransceiver('audio');
	  var s = t2.sender.createEncodedStreams();
	  out.push(typeof s.readable.getReader, typeof s.writable.getWriter);
	  try { t2.sender.createEncodedStreams(); out.push('ok'); } catch (e) { out.push(e.name); }
	  try { new RTCEncodedVideoFrame(); out.push('ok'); } catch (e) { out.push(e.name); }
	  return out.join(','); })()`, &got)
	want := "function,function,true,function,function,function,function,InvalidStateError,TypeError,TypeError,function,function,InvalidStateError,TypeError"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
