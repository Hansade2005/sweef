package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// wcHelpers: a canvas with content that changes per frame, PSNR between two canvases, and small utilities.
const wcHelpers = `
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
function paint(ctx, w, h, t) {
  const g = ctx.createLinearGradient(0, 0, w, h);
  g.addColorStop(0, 'rgb(' + (30 + t * 8) + ', 90, 200)'); g.addColorStop(1, 'rgb(220, ' + (40 + t * 10) + ', 60)');
  ctx.fillStyle = g; ctx.fillRect(0, 0, w, h);
  ctx.fillStyle = 'rgb(240, 240, 80)'; ctx.beginPath(); ctx.arc(30 + t * 9, h / 2, 22, 0, 7); ctx.fill();
  ctx.fillStyle = 'rgb(20, 160, 90)'; ctx.fillRect(w - 50, 10 + t * 3, 36, 28);
}
function psnr(a, b) {
  let se = 0, n = 0;
  for (let i = 0; i < a.length; i += 4) for (let c = 0; c < 3; c++) { const d = a[i + c] - b[i + c]; se += d * d; n++; }
  return se === 0 ? 99 : 10 * Math.log10(255 * 255 / (se / n));
}
`

func wcTab(t *testing.T) *Tab {
	t.Helper()
	srv := newCOIServer(t, nil, nil)
	e := newFeatureEngine(t)
	return openTab(t, e, srv.URL+"/a.html")
}

func TestWebCodecsGlobals(t *testing.T) {
	tab := wcTab(t)
	got := js(t, tab, `return JSON.stringify(['VideoDecoder','VideoEncoder','AudioDecoder','AudioEncoder','VideoFrame','AudioData','EncodedVideoChunk','EncodedAudioChunk','VideoColorSpace','ImageDecoder','ImageTrack','ImageTrackList'].map((n) => [n, typeof globalThis[n], Object.prototype.toString.call(globalThis[n].prototype)]));`)
	want := `[["VideoDecoder","function","[object VideoDecoder]"],["VideoEncoder","function","[object VideoEncoder]"],["AudioDecoder","function","[object AudioDecoder]"],["AudioEncoder","function","[object AudioEncoder]"],["VideoFrame","function","[object VideoFrame]"],["AudioData","function","[object AudioData]"],["EncodedVideoChunk","function","[object EncodedVideoChunk]"],["EncodedAudioChunk","function","[object EncodedAudioChunk]"],["VideoColorSpace","function","[object VideoColorSpace]"],["ImageDecoder","function","[object ImageDecoder]"],["ImageTrack","function","[object ImageTrack]"],["ImageTrackList","function","[object ImageTrackList]"]]`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestWebCodecsVideoFrameBuffersAndCopy(t *testing.T) {
	tab := wcTab(t)
	got := js(t, tab, wcHelpers+`
const out = {};
// an I420 frame from bytes: Y 4x2 = 1..8, U 2x1 = 100,101, V 2x1 = 200,201
const data = new Uint8Array([1,2,3,4,5,6,7,8, 100,101, 200,201]);
const f = new VideoFrame(data, { format: 'I420', codedWidth: 4, codedHeight: 2, timestamp: 1000, duration: 40, colorSpace: { matrix: 'bt709', fullRange: false } });
out.attrs = [f.format, f.codedWidth, f.codedHeight, f.displayWidth, f.displayHeight, f.timestamp, f.duration, JSON.stringify(f.visibleRect), JSON.stringify(f.colorSpace), f.allocationSize()];
const dst = new Uint8Array(12);
const layout = await f.copyTo(dst);
out.layout = layout; out.bytes = Array.from(dst);
// a sub-rectangle (aligned to the chroma grid) in the same format
const sub = new Uint8Array(6);
out.rect = [JSON.stringify(await f.copyTo(sub, { rect: { x: 2, y: 0, width: 2, height: 2 } })), Array.from(sub)];
// conversion to RGBA (limited-range BT.709): black (Y 16) and white (Y 235) pixels
const g = new VideoFrame(new Uint8Array([16,235,16,235, 128,128, 128,128]), { format: 'I420', codedWidth: 2, codedHeight: 2, timestamp: 0, colorSpace: { matrix: 'bt709' } });
const rgba = new Uint8Array(16); await g.copyTo(rgba, { format: 'RGBA' });
out.rgba = Array.from(rgba.subarray(0, 8));
// RGBA frames, channel order conversion to BGRX
const r = new VideoFrame(new Uint8Array([10,20,30,255, 40,50,60,128]), { format: 'RGBA', codedWidth: 2, codedHeight: 1, timestamp: 5 });
const bgrx = new Uint8Array(8); await r.copyTo(bgrx, { format: 'BGRX' });
out.bgrx = Array.from(bgrx);
// visible rect, layout with padding, clone and close
const p = new VideoFrame(new Uint8Array(32), { format: 'NV12', codedWidth: 4, codedHeight: 4, timestamp: 7, visibleRect: { x: 0, y: 0, width: 2, height: 2 }, layout: [{ offset: 0, stride: 6 }, { offset: 24, stride: 4 }] });
out.visible = [p.codedWidth, p.visibleRect.width, p.displayWidth];
const c = f.clone(); f.close();
out.closed = [f.format, f.codedWidth, f.timestamp, c.format, c.codedWidth];
try { f.clone(); } catch (e) { out.cloneClosed = e.name; }
try { f.allocationSize(); } catch (e) { out.allocClosed = e.name; }
try { await f.copyTo(new Uint8Array(12)); } catch (e) { out.copyClosed = e.name; }
c.close(); c.close();
// errors
const errs = [];
for (const fn of [
  () => new VideoFrame(new Uint8Array(3), { format: 'I420', codedWidth: 4, codedHeight: 2, timestamp: 0 }),
  () => new VideoFrame(new Uint8Array(12), { format: 'YUY2', codedWidth: 4, codedHeight: 2, timestamp: 0 }),
  () => new VideoFrame(new Uint8Array(12), { format: 'I420', codedWidth: 4, codedHeight: 2 }),
  () => new VideoFrame(new Uint8Array(12), { format: 'I420', codedWidth: 0, codedHeight: 2, timestamp: 0 }),
  () => new VideoFrame({}, { timestamp: 0 }),
  () => new VideoFrame(),
]) { try { fn(); errs.push('no error'); } catch (e) { errs.push(e.name); } }
out.errs = errs;
// a canvas as a source, and a frame as a drawImage source
const cv = document.createElement('canvas'); cv.width = 32; cv.height = 16; const cx = cv.getContext('2d'); cx.fillStyle = 'rgb(200, 100, 50)'; cx.fillRect(0, 0, 32, 16);
const fc = new VideoFrame(cv, { timestamp: 42 });
out.fromCanvas = [fc.format, fc.codedWidth, fc.codedHeight, fc.timestamp];
const cv2 = document.createElement('canvas'); cv2.width = 32; cv2.height = 16; const c2 = cv2.getContext('2d'); c2.drawImage(fc, 0, 0);
out.drawn = Array.from(c2.getImageData(5, 5, 1, 1).data);
const bm = await createImageBitmap(fc);
out.bitmap = [bm.width, bm.height];
try { new VideoFrame(cv, {}); } catch (e) { out.noTimestamp = e.name; }
return JSON.stringify(out);`)
	want := `{"attrs":["I420",4,2,4,2,1000,40,"{\"x\":0,\"y\":0,\"width\":4,\"height\":2,\"top\":0,\"right\":4,\"bottom\":2,\"left\":0}","{\"primaries\":null,\"transfer\":null,\"matrix\":\"bt709\",\"fullRange\":false}",12],"layout":[{"offset":0,"stride":4},{"offset":8,"stride":2},{"offset":10,"stride":2}],"bytes":[1,2,3,4,5,6,7,8,100,101,200,201],"rect":["[{\"offset\":0,\"stride\":2},{\"offset\":4,\"stride\":1},{\"offset\":5,\"stride\":1}]",[3,4,7,8,101,201]],"rgba":[0,0,0,255,255,255,255,255],"bgrx":[30,20,10,255,60,50,40,255],"visible":[4,2,2],"closed":[null,0,0,"I420",4],"cloneClosed":"InvalidStateError","allocClosed":"InvalidStateError","copyClosed":"InvalidStateError","errs":["TypeError","TypeError","TypeError","TypeError","TypeError","TypeError"],"fromCanvas":["RGBA",32,16,42],"drawn":[200,100,50,255],"bitmap":[32,16],"noTimestamp":"TypeError"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestWebCodecsVP8RoundTripPSNR(t *testing.T) {
	tab := wcTab(t)
	got := js(t, tab, wcHelpers+`
const W = 160, H = 96, N = 10;
const src = document.createElement('canvas'); src.width = W; src.height = H; const sctx = src.getContext('2d');
const originals = [];
const chunks = [], metas = [];
const enc = new VideoEncoder({ output: (c, m) => { chunks.push(c); metas.push(m); }, error: (e) => { throw e; } });
enc.configure({ codec: 'vp8', width: W, height: H, bitrate: 3000000, framerate: 30 });
const supportedBefore = await VideoEncoder.isConfigSupported({ codec: 'vp8', width: W, height: H });
for (let i = 0; i < N; i++) {
  paint(sctx, W, H, i);
  originals.push(sctx.getImageData(0, 0, W, H).data);
  const fr = new VideoFrame(src, { timestamp: i * 33333, duration: 33333 });
  enc.encode(fr, { keyFrame: i === 0 });
  fr.close();
}
const queued = enc.encodeQueueSize;
await enc.flush();
const out = { supported: supportedBefore.supported, encoded: chunks.length, key: chunks.map((c) => c.type[0]).join(''), ts: chunks.slice(0, 3).map((c) => c.timestamp), dur: chunks[0].duration, bytes: chunks.reduce((s, c) => s + c.byteLength, 0) };
out.cfg = metas[0].decoderConfig; out.cfgOnce = metas.slice(1).every((m) => !m.decoderConfig);
out.queueAfter = enc.encodeQueueSize;
const frames = [];
const dec = new VideoDecoder({ output: (f) => frames.push(f), error: (e) => { throw e; } });
dec.configure({ codec: 'vp8', codedWidth: W, codedHeight: H });
for (const c of chunks) dec.decode(c);
await dec.flush();
out.decoded = frames.length;
const dst = document.createElement('canvas'); dst.width = W; dst.height = H; const dctx = dst.getContext('2d');
let worst = 99, mean = 0;
frames.forEach((f, i) => { dctx.drawImage(f, 0, 0); const p = psnr(originals[i], dctx.getImageData(0, 0, W, H).data); worst = Math.min(worst, p); mean += p / frames.length; });
out.worst = worst; out.mean = mean;
out.frame = [frames[0].format, frames[0].codedWidth, frames[0].codedHeight, frames[0].timestamp, frames[0].duration, frames[3].timestamp, frames.map((f) => f.timestamp).every((t, i) => t === i * 33333)];
frames.forEach((f) => f.close());
enc.close(); dec.close();
out.states = [enc.state, dec.state];
return JSON.stringify(out);`)
	js0 := got
	if !strings.Contains(js0, `"supported":true,"encoded":10,"key":"kddddddddd"`) || !strings.Contains(js0, `"decoded":10`) ||
		!strings.Contains(js0, `"cfg":{"codec":"vp8","codedWidth":160,"codedHeight":96,"displayAspectWidth":160,"displayAspectHeight":96}`) ||
		!strings.Contains(js0, `"cfgOnce":true`) || !strings.Contains(js0, `"queueAfter":0`) ||
		!strings.Contains(js0, `"frame":["I420",160,96,0,33333,99999,true]`) || !strings.Contains(js0, `"states":["closed","closed"]`) {
		t.Fatalf("round trip result:\n%s\nconsole:\n%s", got, consoleText(tab))
	}
	var r struct{ Worst, Mean float64 }
	if err := json.Unmarshal([]byte(got), &r); err != nil || r.Worst < 28 {
		t.Fatalf("PSNR too low (%v): %s", err, got)
	}
	t.Logf("WebCodecs VP8 encode -> decode through canvas: worst PSNR %.1f dB, mean %.1f dB over 10 frames", r.Worst, r.Mean)
}

func TestWebCodecsOpusRoundTrip(t *testing.T) {
	tab := wcTab(t)
	got := js(t, tab, wcHelpers+`
const rate = 48000, ch = 2, per = 960, total = 40;
const chunks = [], metas = [];
const enc = new AudioEncoder({ output: (c, m) => { chunks.push(c); metas.push(m); }, error: (e) => { throw e; } });
const sup = await AudioEncoder.isConfigSupported({ codec: 'opus', sampleRate: rate, numberOfChannels: ch, bitrate: 96000 });
enc.configure({ codec: 'opus', sampleRate: rate, numberOfChannels: ch, bitrate: 96000 });
const input = [];
for (let f = 0; f < total; f++) {
  const pcm = new Float32Array(per * ch);
  for (let i = 0; i < per; i++) { const s = 0.4 * Math.sin(2 * Math.PI * 440 * (f * per + i) / rate); pcm[i * 2] = s; pcm[i * 2 + 1] = -s; input.push(s); }
  const ad = new AudioData({ format: 'f32', sampleRate: rate, numberOfFrames: per, numberOfChannels: ch, timestamp: Math.round(f * per * 1e6 / rate), data: pcm });
  enc.encode(ad); ad.close();
}
await enc.flush();
const out = { supported: sup.supported, chunks: chunks.length, dur: chunks[0].duration, ts: chunks.slice(0, 3).map((c) => c.timestamp), cfg: { codec: metas[0].decoderConfig.codec, rate: metas[0].decoderConfig.sampleRate, ch: metas[0].decoderConfig.numberOfChannels, desc: new Uint8Array(metas[0].decoderConfig.description).length } };
const decoded = [];
const dec = new AudioDecoder({ output: (a) => decoded.push(a), error: (e) => { throw e; } });
dec.configure({ codec: 'opus', sampleRate: rate, numberOfChannels: ch, description: metas[0].decoderConfig.description });
for (const c of chunks) dec.decode(c);
await dec.flush();
out.decoded = [decoded.length, decoded[0].format, decoded[0].sampleRate, decoded[0].numberOfChannels, decoded[0].numberOfFrames, decoded[0].duration, decoded[1].timestamp];
const left = [];
for (const a of decoded) { const p = new Float32Array(a.numberOfFrames); a.copyTo(p, { planeIndex: 0 }); for (const v of p) left.push(v); a.close(); }
let best = Infinity, lag = 0;
for (let l = 0; l < 1500; l++) { let se = 0; for (let i = 8000; i < 30000; i += 3) { const d = input[i] - left[i + l]; se += d * d; } if (se < best) { best = se; lag = l; } }
let sig = 0; for (let i = 8000; i < 30000; i += 3) sig += input[i] * input[i];
out.snr = 10 * Math.log10(sig / best);
return JSON.stringify(out);`)
	for _, w := range []string{`"supported":true`, `"chunks":40`, `"dur":20000`, `"ts":[0,20000,40000]`, `"cfg":{"codec":"opus","rate":48000,"ch":2,"desc":19}`, `"decoded":[40,"f32-planar",48000,2,960,20000,20000]`} {
		if !strings.Contains(got, w) {
			t.Fatalf("missing %s in %s\nconsole:\n%s", w, got, consoleText(tab))
		}
	}
	var r struct{ Snr float64 }
	if err := json.Unmarshal([]byte(got), &r); err != nil || r.Snr < 15 {
		t.Fatalf("Opus round trip SNR too low (%v): %s", err, got)
	}
	t.Logf("WebCodecs Opus encode -> decode: SNR %.1f dB", r.Snr)
}
