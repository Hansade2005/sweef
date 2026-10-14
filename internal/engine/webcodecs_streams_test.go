package engine

import (
	"image"
	"sync"
	"testing"
	"time"
)

func TestTrackProcessorAndGenerator(t *testing.T) {
	rtcMode(t, WebRTCHostRaw) // also turns the fake camera and microphone on
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	pre := `
const rgba = (w, h, c) => { const d = new Uint8Array(w * h * 4); for (let i = 0; i < w * h; i++) d.set(c, i * 4); return d; };
const frame = (w, h, c, ts) => new VideoFrame(rgba(w, h, c), { format: 'RGBA', codedWidth: w, codedHeight: h, timestamp: ts });
`
	runJSCases(t, tab, []jsCase{
		{"generator writes frames a processor reads back", pre + `
const gen = new VideoTrackGenerator(); const w = gen.writable.getWriter();
const r = new MediaStreamTrackProcessor({ track: gen.track }).readable.getReader();
const pending = r.read();
const sizes = [];
for (let i = 0; i < 5; i++) { await w.write(frame(32, 16, [(i + 1) * 40, 0, 0, 255], i * 1000)); }
let got = await pending; sizes.push([got.value.codedWidth, got.value.codedHeight, got.value.format]);
const o = new Uint8Array(got.value.allocationSize({ format: 'RGBA' })); await got.value.copyTo(o, { format: 'RGBA' });
const red = o[0]; got.value.close();
await r.cancel(); gen.track.stop();
return [gen.track.kind, gen.track.readyState, sizes, red > 0, gen.muted, gen.track instanceof MediaStreamTrack];`, `["video","ended",[[32,16,"I420"]],true,false,true]`},
		{"the written frame is consumed (closed)", pre + `const gen = new VideoTrackGenerator(); const w = gen.writable.getWriter(); const f = frame(8, 8, [1, 2, 3, 255], 0); await w.write(f); const out = [f.format, f.codedWidth]; try { await w.write({}); } catch (e) { out.push(e.name); } gen.track.stop(); return out;`, `[null,0,"TypeError"]`},
		{"closing the writable ends the track", pre + `const gen = new VideoTrackGenerator(); const w = gen.writable.getWriter(); const before = gen.track.readyState; await w.close(); return [before, gen.track.readyState];`, `["live","ended"]`},
		{"a fake camera feeds a processor", pre + `
const ms = await navigator.mediaDevices.getUserMedia({ video: { width: 320, height: 240, frameRate: 30 } });
const p = new MediaStreamTrackProcessor({ track: ms.getVideoTracks()[0] }), r = p.readable.getReader();
let n = 0, ts = [], size = null;
while (n < 6) { const x = await r.read(); if (x.done) break; n++; ts.push(x.value.timestamp); size = [x.value.codedWidth, x.value.codedHeight]; x.value.close(); }
await r.cancel(); ms.getTracks().forEach(t => t.stop());
return [n, size, ts.every((v, i) => i === 0 || v > ts[i - 1])];`, `[6,[320,240],true]`},
		{"processor to generator pipes frames through a transform", pre + `
const ms = await navigator.mediaDevices.getUserMedia({ video: { width: 160, height: 120, frameRate: 30 } });
const src = ms.getVideoTracks()[0], gen = new VideoTrackGenerator();
let seen = 0;
const t = new TransformStream({ transform(f, c) { seen++; c.enqueue(f); } });
new MediaStreamTrackProcessor({ track: src }).readable.pipeThrough(t).pipeTo(gen.writable).catch(() => {});
const out = new MediaStreamTrackProcessor({ track: gen.track }).readable.getReader();
let n = 0, size = null;
while (n < 5) { const x = await out.read(); if (x.done) break; n++; size = [x.value.codedWidth, x.value.codedHeight]; x.value.close(); }
src.stop(); gen.track.stop();
return [n, size, seen >= 5];`, `[5,[160,120],true]`},
		{"audio: fake microphone through a processor", pre + `
const ms = await navigator.mediaDevices.getUserMedia({ audio: true });
const r = new MediaStreamTrackProcessor({ track: ms.getAudioTracks()[0] }).readable.getReader();
let frames = 0, rate = 0, ch = 0, peak = 0;
while (frames < 9600) { const x = await r.read(); if (x.done) break; const a = x.value; rate = a.sampleRate; ch = a.numberOfChannels; frames += a.numberOfFrames; const buf = new Float32Array(a.allocationSize({ planeIndex: 0, format: 'f32-planar' }) / 4); a.copyTo(buf, { planeIndex: 0, format: 'f32-planar' }); for (const v of buf) peak = Math.max(peak, Math.abs(v)); a.close(); }
await r.cancel(); ms.getTracks().forEach(t => t.stop());
return [rate > 0, ch >= 1, frames >= 9600, peak > 0.05];`, `[true,true,true,true]`},
		{"audio generator and processor loop", pre + `
const gen = new MediaStreamTrackGenerator({ kind: 'audio' });
const w = gen.writable.getWriter();
const r = new MediaStreamTrackProcessor({ track: gen }).readable.getReader();
const data = new Float32Array(480); for (let i = 0; i < 480; i++) data[i] = 0.5 * Math.sin(i / 10);
const first = r.read();
for (let i = 0; i < 20; i++) await w.write(new AudioData({ format: 'f32-planar', sampleRate: 48000, numberOfFrames: 480, numberOfChannels: 1, timestamp: i * 10000, data }));
const x = await first; const a = x.value;
const buf = new Float32Array(a.numberOfFrames); a.copyTo(buf, { planeIndex: 0, format: 'f32-planar' });
let peak = 0; for (const v of buf) peak = Math.max(peak, Math.abs(v));
const out = [gen.kind, gen instanceof MediaStreamTrack, a.sampleRate, a.numberOfChannels, peak > 0.4 && peak < 0.51];
gen.stop();
return out;`, `["audio",true,48000,1,true]`},
	})
}

// A generated track is a real track: it can be sent over a peer connection, and the far end gets the frames the page
// wrote. The page draws red, green and blue pictures; the far end must show all three, and read them with a
// processor too. Audio goes the same way.
func TestGeneratedTrackOverPeerConnection(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	var mu sync.Mutex
	classes := map[string]int{}
	h := func(img *image.YCbCr) {
		cx, cy := img.Bounds().Dx()/2, img.Bounds().Dy()/2
		cb, cr := int(img.Cb[(cy/2)*img.CStride+cx/2]), int(img.Cr[(cy/2)*img.CStride+cx/2])
		c := "other"
		switch {
		case cr > 170 && cb < 130:
			c = "red"
		case cr < 120 && cb < 120:
			c = "green"
		case cb > 170 && cr < 130:
			c = "blue"
		}
		mu.Lock()
		classes[c]++
		mu.Unlock()
	}
	rtcFrameHook.Store(&h)
	t.Cleanup(func() { rtcFrameHook.Store(nil) })
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, `
window.err = []; window.remoteFrames = 0; window.remoteAudio = 0; window.sent = 0;
window.a = new RTCPeerConnection(); window.b = new RTCPeerConnection();
a.onicecandidate = function (e) { if (e.candidate) b.addIceCandidate(e.candidate).catch(function (x) { err.push('b:' + x); }); };
b.onicecandidate = function (e) { if (e.candidate) a.addIceCandidate(e.candidate).catch(function (x) { err.push('a:' + x); }); };
var rv = document.createElement('video'); rv.id = 'rv'; rv.autoplay = true; rv.muted = true; document.body.appendChild(rv); window.rv = rv;
b.ontrack = function (e) {
  if (e.track.kind === 'video') {
    rv.srcObject = e.streams[0];
    var rd = new MediaStreamTrackProcessor({ track: e.track }).readable.getReader();
    (function loop() { rd.read().then(function (x) { if (x.done) return; remoteFrames++; x.value.close(); loop(); }); })();
  } else {
    var ra = new MediaStreamTrackProcessor({ track: e.track }).readable.getReader();
    (function loop() { ra.read().then(function (x) { if (x.done) return; remoteAudio += x.value.numberOfFrames; x.value.close(); loop(); }); })();
  }
};
(async function () {
  var gv = new VideoTrackGenerator(), ga = new MediaStreamTrackGenerator({ kind: 'audio' });
  var vw = gv.writable.getWriter(), aw = ga.writable.getWriter();
  var colors = [[255, 0, 0, 255], [0, 255, 0, 255], [0, 0, 255, 255]], n = 0, W = 320, H = 240;
  var tone = new Float32Array(480); for (var i = 0; i < 480; i++) tone[i] = 0.4 * Math.sin(i / 8);
  setInterval(function () {
    var d = new Uint8Array(W * H * 4), c = colors[Math.floor(n / 10) % 3];
    for (var i = 0; i < W * H; i++) d.set(c, i * 4);
    vw.write(new VideoFrame(d, { format: 'RGBA', codedWidth: W, codedHeight: H, timestamp: n * 33333 })).catch(function () {});
    aw.write(new AudioData({ format: 'f32-planar', sampleRate: 48000, numberOfFrames: 480, numberOfChannels: 1, timestamp: n * 10000, data: tone })).catch(function () {});
    n++; sent = n;
  }, 33);
  var out = new MediaStream([gv.track, ga]);
  out.getTracks().forEach(function (t) { a.addTrack(t, out); });
  var o = await a.createOffer(); await a.setLocalDescription(o);
  await b.setRemoteDescription(a.localDescription);
  var an = await b.createAnswer(); await b.setLocalDescription(an);
  await a.setRemoteDescription(b.localDescription);
})().catch(function (e) { err.push('call:' + e + (e && e.stack || '')); });
1`, nil)
	waitJS(t, tab, `rv.readyState >= 2 && rv.videoWidth === 320 && remoteFrames > 5 && remoteAudio > 4800`, 30*time.Second)
	pumpFor(tab, 4*time.Second)
	var errs string
	evalJSON(t, tab, `err.join(' | ')`, &errs)
	if errs != "" {
		t.Fatalf("errors: %s", errs)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Logf("remote <video> frames by colour: %v", classes)
	for _, c := range []string{"red", "green", "blue"} {
		if classes[c] < 3 {
			t.Errorf("only %d %s pictures arrived: %v", classes[c], c, classes)
		}
	}
}
