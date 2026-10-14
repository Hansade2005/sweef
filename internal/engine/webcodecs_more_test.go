package engine

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Decoders run on real streams: every codec this build decodes, fed chunk by chunk, with the timestamps of the
// container. Output must come out in presentation order (the clips with B-frames prove the reorder buffer).
func TestWebCodecsDecodeRealStreams(t *testing.T) {
	type clip struct{ file, key string }
	clips := []clip{
		{"vp8_inter.webm", "vp8"}, {"vp9_arf.webm", "vp9"}, {"h264_baseline_cavlc.mp4", "h264"},
		{"h264_main_b_cabac.mp4", "h264 B"}, {"av1_inter.mp4", "av1"}, {"hevc_main_b.mp4", "hevc B"},
	}
	for _, c := range clips {
		t.Run(c.file, func(t *testing.T) {
			data, err := os.ReadFile("../media/testdata/" + c.file)
			if err != nil {
				t.Skip(err)
			}
			vs, err := mediaExtract(data)
			if err != nil {
				t.Fatal(err)
			}
			type ch struct {
				D   string `json:"d"`
				Ts  int64  `json:"ts"`
				Dur int64  `json:"dur"`
				K   bool   `json:"k"`
			}
			var chunks []ch
			for _, k := range vs.Chunks {
				chunks = append(chunks, ch{base64.StdEncoding.EncodeToString(k.Data), k.PTS, k.Dur, k.Key})
			}
			in, _ := json.Marshal(map[string]any{"codec": vs.Codec, "desc": base64.StdEncoding.EncodeToString(vs.Description), "w": vs.Width, "h": vs.Height, "chunks": chunks})
			tab := wcTab(t)
			got := js(t, tab, wcHelpers+`
const IN = `+string(in)+`;
const desc = IN.desc ? Uint8Array.fromBase64(IN.desc) : undefined;
const cfg = { codec: IN.codec, codedWidth: IN.w, codedHeight: IN.h, description: desc };
const sup = await VideoDecoder.isConfigSupported(cfg);
const frames = [], errs = [];
const dec = new VideoDecoder({ output: (f) => frames.push(f), error: (e) => errs.push(e.name + ': ' + e.message) });
dec.configure(cfg);
let deq = 0; dec.ondequeue = () => deq++;
for (const c of IN.chunks) dec.decode(new EncodedVideoChunk({ type: c.k ? 'key' : 'delta', timestamp: c.ts, duration: c.dur, data: Uint8Array.fromBase64(c.d) }));
const peak = dec.decodeQueueSize;
await dec.flush();
const ts = frames.map((f) => f.timestamp);
const sorted = ts.every((t, i) => i === 0 || t >= ts[i - 1]);
const cv = document.createElement('canvas'); cv.width = frames[0].displayWidth; cv.height = frames[0].displayHeight;
const cx = cv.getContext('2d'); cx.drawImage(frames[frames.length - 1], 0, 0);
const px = cx.getImageData(cv.width >> 1, cv.height >> 1, 1, 1).data;
const r = { supported: sup.supported, frames: frames.length, errs, sorted, format: frames[0].format, size: [frames[0].codedWidth, frames[0].codedHeight], deq: deq === IN.chunks.length, queue: dec.decodeQueueSize, peak: peak > 0, opaque: px[3] === 255, state: dec.state };
frames.forEach((f) => f.close());
return JSON.stringify(r);`)
			var r struct {
				Supported bool
				Frames    int
				Errs      []string
				Sorted    bool
				Format    string
				Size      [2]int
				Deq, Peak bool
				Queue     int
				Opaque    bool
				State     string
			}
			if err := json.Unmarshal([]byte(got), &r); err != nil {
				t.Fatalf("%v: %s\nconsole:\n%s", err, got, consoleText(tab))
			}
			if !r.Supported || len(r.Errs) > 0 || r.Frames < 5 || !r.Sorted || r.Format != "I420" || r.Size[0] != vs.Width || r.Size[1] != vs.Height || !r.Deq || r.Queue != 0 || !r.Opaque || r.State != "configured" {
				t.Fatalf("%s: %s\nconsole:\n%s", c.key, got, consoleText(tab))
			}
			t.Logf("%s (%s): %d frames, presentation order, %dx%d", c.file, vs.Codec, r.Frames, r.Size[0], r.Size[1])
		})
	}
}

func TestWebCodecsConfigSupportAndStateMachine(t *testing.T) {
	tab := wcTab(t)
	got := js(t, tab, wcHelpers+`
const out = {};
const sup = async (C, cfg) => { try { return (await C.isConfigSupported(cfg)).supported; } catch (e) { return e.name; } };
out.video = [
  await sup(VideoDecoder, { codec: 'vp8' }), await sup(VideoDecoder, { codec: 'vp09.00.10.08' }), await sup(VideoDecoder, { codec: 'avc1.64001f' }),
  await sup(VideoDecoder, { codec: 'hvc1.1.6.L93.B0' }), await sup(VideoDecoder, { codec: 'av01.0.04M.08' }), await sup(VideoDecoder, { codec: 'theora' }),
  await sup(VideoDecoder, { codec: 'avc1.64001f', description: new Uint8Array([1, 2, 3]) }), await sup(VideoDecoder, {}), await sup(VideoDecoder, { codec: '' }),
  await sup(VideoEncoder, { codec: 'vp8', width: 320, height: 240 }), await sup(VideoEncoder, { codec: 'vp8', width: 8, height: 8 }),
  await sup(VideoEncoder, { codec: 'avc1.64001f', width: 320, height: 240 }), await sup(VideoEncoder, { codec: 'vp09.00.10.08', width: 320, height: 240 }), await sup(VideoEncoder, { codec: 'vp8' }),
];
out.audio = [
  await sup(AudioDecoder, { codec: 'opus', sampleRate: 48000, numberOfChannels: 2 }), await sup(AudioDecoder, { codec: 'mp4a.40.2', sampleRate: 44100, numberOfChannels: 2 }),
  await sup(AudioDecoder, { codec: 'mp4a.40.2', sampleRate: 44100, numberOfChannels: 2, aac: { format: 'adts' } }),
  await sup(AudioDecoder, { codec: 'mp3', sampleRate: 44100, numberOfChannels: 2 }), await sup(AudioDecoder, { codec: 'pcm-s16', sampleRate: 8000, numberOfChannels: 1 }),
  await sup(AudioDecoder, { codec: 'ulaw', sampleRate: 8000, numberOfChannels: 1 }), await sup(AudioDecoder, { codec: 'flac', sampleRate: 44100, numberOfChannels: 2 }),
  await sup(AudioDecoder, { codec: 'opus', numberOfChannels: 2 }), await sup(AudioEncoder, { codec: 'opus', sampleRate: 48000, numberOfChannels: 2 }),
  await sup(AudioEncoder, { codec: 'opus', sampleRate: 44100, numberOfChannels: 2 }), await sup(AudioEncoder, { codec: 'mp4a.40.2', sampleRate: 44100, numberOfChannels: 2 }),
];
const cfg = (await VideoEncoder.isConfigSupported({ codec: 'vp8', width: 64, height: 48, bitrate: 1e6, bogus: 1 })).config;
out.echo = [Object.keys(cfg).sort().join(), cfg.bogus];
// state machine of a decoder
const errs = [];
const d = new VideoDecoder({ output: () => {}, error: (e) => errs.push(e.name) });
out.state0 = [d.state, d.decodeQueueSize];
const t1 = (fn) => { try { fn(); return 'ok'; } catch (e) { return e.name; } };
const chunk = (type) => new EncodedVideoChunk({ type, timestamp: 0, data: new Uint8Array(4) });
out.unconfigured = [t1(() => d.decode(chunk('key'))), await d.flush().then(() => 'ok', (e) => e.name)];
out.badConfig = [t1(() => d.configure({})), t1(() => d.configure({ codec: 'vp8', codedWidth: -1 })), d.state];
d.configure({ codec: 'vp8' });
out.configured = [d.state, t1(() => d.decode(chunk('delta'))), t1(() => d.decode({})), t1(() => d.decode())];
// reset aborts a pending flush
d.decode(chunk('key'));
const fl = d.flush().then(() => 'flushed', (e) => e.name);
d.reset();
out.reset = [await fl, d.state, d.decodeQueueSize];
// an undecodable chunk reports an EncodingError through the error callback and closes the decoder
d.configure({ codec: 'vp8' });
d.decode(chunk('key'));
await sleep(150);
out.broken = [errs.join(), d.state, t1(() => d.decode(chunk('key'))), t1(() => d.reset()), t1(() => d.close())];
// unsupported codec: the error callback gets NotSupportedError, asynchronously
const d2 = new VideoDecoder({ output: () => {}, error: (e) => errs.push('d2:' + e.name) });
d2.configure({ codec: 'theora' }); const sync = d2.state;
await sleep(50);
out.unsupported = [sync, d2.state, errs[errs.length - 1]];
out.close = [t1(() => d2.close()), (() => { const x = new VideoDecoder({ output() {}, error() {} }); x.close(); return [x.state, t1(() => x.configure({ codec: 'vp8' })), t1(() => x.reset()), t1(() => x.close())]; })()];
out.ctor = [t1(() => new VideoDecoder()), t1(() => new VideoDecoder({ output() {} })), t1(() => VideoDecoder()), t1(() => new EncodedVideoChunk({ type: 'x', timestamp: 0, data: new Uint8Array(1) })), t1(() => new EncodedVideoChunk({ type: 'key', timestamp: 0 }))];
// chunks
const ec = new EncodedAudioChunk({ type: 'key', timestamp: 5, duration: 7, data: new Uint8Array([9, 8, 7]) });
const cp = new Uint8Array(3); ec.copyTo(cp);
out.chunk = [ec.type, ec.timestamp, ec.duration, ec.byteLength, Array.from(cp), new EncodedVideoChunk({ type: 'delta', timestamp: 1, data: new ArrayBuffer(2) }).duration, t1(() => ec.copyTo(new Uint8Array(1)))];
return JSON.stringify(out);`)
	want := `{"video":[true,true,true,true,true,false,false,"TypeError","TypeError",true,false,false,false,"TypeError"],"audio":[true,false,true,true,true,true,false,"TypeError",true,false,false],"echo":["bitrate,codec,height,width",null],"state0":["unconfigured",0],"unconfigured":["InvalidStateError","InvalidStateError"],"badConfig":["TypeError","TypeError","unconfigured"],"configured":["configured","DataError","TypeError","TypeError"],"reset":["AbortError","unconfigured",0],"broken":["EncodingError","closed","InvalidStateError","InvalidStateError","InvalidStateError"],"unsupported":["closed","closed","d2:NotSupportedError"],"close":["InvalidStateError",["closed","InvalidStateError","InvalidStateError","InvalidStateError"]],"ctor":["TypeError","TypeError","TypeError","TypeError","TypeError"],"chunk":["key",5,7,3,[9,8,7],null,"TypeError"]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

// Frames and audio move between the page and a worker: a decoder runs in the worker (WebCodecs is available
// there), the frames come back by transfer, and a transferred frame is closed in the sender.
func TestWebCodecsInAWorkerAndFrameTransfer(t *testing.T) {
	srv := newCOIServer(t, nil, map[string]route{"/w.js": {body: `
onmessage = async (e) => {
  const d = e.data;
  if (d.cmd === 'make') {
    const f = new VideoFrame(new Uint8Array(d.w * d.h * 4).fill(d.fill), { format: 'RGBA', codedWidth: d.w, codedHeight: d.h, timestamp: 77 });
    postMessage({ frame: f, from: 'worker', ctor: typeof VideoDecoder }, [f]);
    postMessage({ closed: [f.format, f.codedWidth] });
  } else if (d.cmd === 'echo') {
    const bytes = new Uint8Array(d.frame.allocationSize()); await d.frame.copyTo(bytes);
    postMessage({ ts: d.frame.timestamp, first: Array.from(bytes.subarray(0, 4)), fmt: d.frame.format, audio: d.audio.numberOfFrames, rate: d.audio.sampleRate });
    d.frame.close(); d.audio.close();
  }
};`}})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, wcHelpers+`
const w = new Worker('/w.js');
const msgs = []; let wake; w.onmessage = (e) => { msgs.push(e.data); if (wake) wake(); };
const waitFor = async (n) => { while (msgs.length < n) await new Promise((r) => { wake = r; setTimeout(r, 50); }); };
const out = {};
w.postMessage({ cmd: 'make', w: 4, h: 2, fill: 77 });
await waitFor(2);
const f = msgs[0].frame;
out.fromWorker = [msgs[0].from, msgs[0].ctor, f instanceof VideoFrame, f.format, f.codedWidth, f.codedHeight, f.timestamp, msgs[1].closed];
const bytes = new Uint8Array(f.allocationSize()); await f.copyTo(bytes);
out.pixels = Array.from(bytes.subarray(0, 4));
// to the worker and back: cloned (the page keeps its frame), and an AudioData with it
const a = new AudioData({ format: 's16', sampleRate: 8000, numberOfFrames: 4, numberOfChannels: 1, timestamp: 0, data: new Int16Array([1, 2, 3, 4]) });
w.postMessage({ cmd: 'echo', frame: f, audio: a });
await waitFor(3);
out.echo = msgs[2]; out.stillOpen = [f.format, a.numberOfFrames];
// transferring closes the frame here
const g2 = f.clone();
w.postMessage({ cmd: 'echo', frame: g2, audio: a }, [g2]);
out.transferred = [g2.format, g2.codedWidth];
await waitFor(4);
out.echo2 = msgs[3].ts;
f.close(); a.close(); w.terminate();
// structuredClone gives an independent frame; closed frames cannot be cloned or posted
const h = new VideoFrame(new Uint8Array(8), { format: 'RGBA', codedWidth: 2, codedHeight: 1, timestamp: 3 });
const sc = structuredClone(h); h.close();
out.clone = [sc.format, sc.timestamp];
let err = ''; try { new Worker('/w.js').postMessage(h); } catch (e) { err = e.name; }
out.closedPost = err;
err = ''; try { new BroadcastChannel('x').postMessage(sc); } catch (e) { err = e.name; }
out.broadcast = err;
return JSON.stringify(out);`)
	want := `{"fromWorker":["worker","function",true,"RGBA",4,2,77,[null,0]],"pixels":[77,77,77,77],"echo":{"ts":77,"first":[77,77,77,77],"fmt":"RGBA","audio":4,"rate":8000},"stillOpen":["RGBA",4],"transferred":[null,0],"echo2":77,"clone":["RGBA",3],"closedPost":"DataCloneError","broadcast":"DataCloneError"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestWebCodecsAudioDataAndImageDecoder(t *testing.T) {
	// a 2x2 PNG made in Go and a 2-frame GIF
	png := mediaTestPNG()
	gif := mediaTestGIF()
	tab := wcTab(t)
	got := js(t, tab, wcHelpers+`
const out = {};
const a = new AudioData({ format: 's16', sampleRate: 8000, numberOfFrames: 4, numberOfChannels: 2, timestamp: 100, data: new Int16Array([100, -100, 200, -200, 300, -300, 400, -400]) });
out.attrs = [a.format, a.sampleRate, a.numberOfFrames, a.numberOfChannels, a.duration, a.timestamp];
out.alloc = [a.allocationSize({ planeIndex: 0 }), a.allocationSize({ planeIndex: 0, format: 'f32-planar' }), a.allocationSize({ planeIndex: 1, format: 'f32-planar', frameOffset: 1, frameCount: 2 })];
const left = new Float32Array(4); a.copyTo(left, { planeIndex: 0, format: 'f32-planar' });
out.left = Array.from(left).map((v) => Math.round(v * 32768));
const right = new Int16Array(2); a.copyTo(right, { planeIndex: 1, format: 's16-planar', frameOffset: 2, frameCount: 2 });
out.right = Array.from(right);
const inter = new Uint8Array(a.allocationSize({ planeIndex: 0 })); a.copyTo(inter, { planeIndex: 0 });
out.interleaved = Array.from(new Int16Array(inter.buffer));
const c = a.clone(); a.close();
out.closed = [a.format, a.sampleRate, a.numberOfFrames, c.numberOfFrames];
const errs = [];
for (const fn of [() => c.copyTo(new Float32Array(1), { planeIndex: 0, format: 'f32-planar' }), () => c.copyTo(new Float32Array(9), { planeIndex: 5, format: 'f32-planar' }), () => a.clone(), () => c.copyTo(new Uint8Array(100)), () => new AudioData({ format: 'bogus', sampleRate: 1, numberOfFrames: 1, numberOfChannels: 1, timestamp: 0, data: new Uint8Array(4) })]) { try { fn(); errs.push('ok'); } catch (e) { errs.push(e.name); } }
out.errs = errs;
// ImageDecoder: PNG and an animated GIF
const png = Uint8Array.fromBase64('`+base64.StdEncoding.EncodeToString(png)+`');
const dec = new ImageDecoder({ data: png, type: 'image/png' });
await dec.tracks.ready; const r = await dec.decode();
const px = new Uint8Array(r.image.allocationSize()); await r.image.copyTo(px);
out.png = [dec.type, dec.complete, dec.tracks.length, dec.tracks.selectedTrack.animated, dec.tracks.selectedTrack.frameCount, r.image.format, r.image.codedWidth, r.image.codedHeight, r.complete, Array.from(px.subarray(0, 4))];
const gif = Uint8Array.fromBase64('`+base64.StdEncoding.EncodeToString(gif)+`');
const gd = new ImageDecoder({ data: gif, type: 'image/gif' });
await gd.completed;
const f0 = (await gd.decode({ frameIndex: 0 })).image, f1 = (await gd.decode({ frameIndex: 1 })).image;
out.gif = [gd.tracks.selectedTrack.animated, gd.tracks.selectedTrack.frameCount, f0.timestamp, f0.duration, f1.timestamp, f1.duration];
let e2 = ''; try { await gd.decode({ frameIndex: 5 }); } catch (e) { e2 = e.name; }
out.range = e2;
out.supported = [await ImageDecoder.isTypeSupported('image/png'), await ImageDecoder.isTypeSupported('image/gif'), await ImageDecoder.isTypeSupported('image/tiff')];
const bad = new ImageDecoder({ data: new Uint8Array([1, 2, 3, 4]), type: 'image/png' });
out.bad = await bad.completed.then(() => 'decoded', (e) => e.name);
try { new ImageDecoder({ data: png, type: 'image/tiff' }); } catch (e) { out.badType = e.name; }
gd.close(); dec.close();
return JSON.stringify(out);`)
	want := `{"attrs":["s16",8000,4,2,500,100],"alloc":[16,16,8],"left":[100,200,300,400],"right":[-300,-400],"interleaved":[100,-100,200,-200,300,-300,400,-400],"closed":[null,0,0,4],"errs":["RangeError","RangeError","InvalidStateError","TypeError","TypeError"],"png":["image/png",true,1,false,1,"RGBA",2,2,true,[255,0,0,255]],"gif":[true,2,0,100000,100000,200000],"range":"RangeError","supported":[true,true,false],"bad":"EncodingError","badType":"NotSupportedError"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

var _ = strings.Contains

// Leaving the page frees the codecs' goroutines and every frame it still held open.
func TestWebCodecsTeardown(t *testing.T) {
	srv := newCOIServer(t, nil, nil)
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	baseG, baseBytes := runtime.NumGoroutine(), wcLive.Load()
	got := js(t, tab, wcHelpers+`
window.keep = [];
const dec = new VideoDecoder({ output: (f) => window.keep.push(f), error() {} });
dec.configure({ codec: 'vp8' });
const enc = new VideoEncoder({ output() {}, error() {} });
enc.configure({ codec: 'vp8', width: 64, height: 64 });
const ae = new AudioEncoder({ output() {}, error() {} }); ae.configure({ codec: 'opus', sampleRate: 48000, numberOfChannels: 1 });
for (let i = 0; i < 20; i++) window.keep.push(new VideoFrame(new Uint8Array(640 * 480 * 4), { format: 'RGBA', codedWidth: 640, codedHeight: 480, timestamp: i }));
enc.encode(window.keep[0]);
return 'open';`)
	if got != "open" {
		t.Fatalf("got %s", got)
	}
	if wcLive.Load() < baseBytes+20*640*480*4 {
		t.Fatalf("frames are not accounted: %d bytes (base %d)", wcLive.Load(), baseBytes)
	}
	if err := tab.Navigate(srv.URL+"/b.html", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (wcLive.Load() > baseBytes || runtime.NumGoroutine() > baseG+3) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := wcLive.Load(); n > baseBytes {
		t.Errorf("%d bytes of frames still open after the page left (was %d)", n, baseBytes)
	}
	if n := runtime.NumGoroutine(); n > baseG+3 {
		t.Errorf("goroutines %d -> %d", baseG, n)
	}
}
