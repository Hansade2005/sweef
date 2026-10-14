package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// audioTab returns a tab on a page with a button (a click is the user activation that lets an AudioContext run),
// with the synthetic microphone available.
func audioTab(t *testing.T) *Tab {
	t.Helper()
	rtcMode(t, WebRTCHostRaw)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body><button id="go">go</button></body></html>`)
	}))
	t.Cleanup(srv.Close)
	e := newRTCEngine(t)
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	tab.SetMediaBroker(&testBroker{decide: allow})
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	return tab
}

// activate gives the page a user activation (a click) and lets pending resume() calls settle.
func activate(t *testing.T, tab *Tab) {
	t.Helper()
	if err := tab.Click("#go"); err != nil {
		t.Fatal(err)
	}
	tab.Settle(150 * time.Millisecond)
}

// A running AudioContext is rendered by its own goroutine: while the page's JavaScript is busy the graph goes on,
// so sound does not stutter and currentTime keeps the real clock.
func TestRealtimeContextRendersWhilePageIsBusy(t *testing.T) {
	tab := audioTab(t)
	var res map[string]float64
	evalJSON(t, tab, `(function () {
	  var c = new AudioContext(); window.c = c; c.resume();
	  var o = c.createOscillator(), g = c.createGain(); g.gain.value = 0.1; o.connect(g); g.connect(c.destination); o.start();
	  return { rate: c.sampleRate };
	})()`, &res)
	activate(t, tab)
	evalJSON(t, tab, `({ rate: c.sampleRate, running: c.state === 'running' ? 1 : 0 })`, &res)
	if res["running"] != 1 || res["rate"] != 44100 {
		t.Fatalf("context: %v", res)
	}
	time.Sleep(200 * time.Millisecond)
	evalJSON(t, tab, `(function () {
	  var id = c._id, f0 = __sw.waFrame(id), t0 = Date.now();
	  while (Date.now() - t0 < 400) { /* a long task: no timer, no event, no host call */ }
	  var f1 = __sw.waFrame(id);
	  return { advanced: (f1 - f0) / c.sampleRate };
	})()`, &res)
	t.Logf("the graph advanced %.3f s during a 0.4 s long task", res["advanced"])
	if res["advanced"] < 0.3 {
		t.Fatalf("the graph advanced %.3f s while the page was busy", res["advanced"])
	}
	var st map[string]any
	evalJSON(t, tab, `(function () { var before = c.currentTime; return { t: before, state: c.state }; })()`, &st)
	if st["state"] != "running" || st["t"].(float64) < 0.5 {
		t.Fatalf("clock: %v", st)
	}
	evalJSON(t, tab, `c.suspend().then(function () { window.tS = c.currentTime; return c.state; })`, nil)
	pumpFor(tab, 300*time.Millisecond)
	var t1 float64
	evalJSON(t, tab, `c.currentTime - tS`, &t1)
	if t1 > 0.05 {
		t.Fatalf("a suspended context's time moved by %.3f s", t1)
	}
	evalJSON(t, tab, `(function () { c.resume(); return 1; })()`, nil)
	activate(t, tab)
	pumpFor(tab, 300*time.Millisecond)
	evalJSON(t, tab, `c.currentTime - tS`, &t1)
	if t1 < 0.2 {
		t.Fatalf("a resumed context did not run (moved %.3f s)", t1)
	}
}

// A worklet needs the page: such a context keeps rendering through the page, and still works.
func TestRealtimeContextWithWorkletKeepsPageClock(t *testing.T) {
	tab := audioTab(t)
	evalJSON(t, tab, `(function () { window.c = new AudioContext(); c.resume(); return 1; })()`, nil)
	activate(t, tab)
	evalJSON(t, tab, `(async function () {
	  var url = URL.createObjectURL(new Blob(['class P extends AudioWorkletProcessor { process(i, o) { o[0][0].fill(0.25); return true; } } registerProcessor("p", P);'], { type: 'text/javascript' }));
	  await c.audioWorklet.addModule(url);
	  var w = new AudioWorkletNode(c, 'p'); var a = c.createAnalyser(); w.connect(a); a.connect(c.destination); window.a = a;
	  return 1; })()`, nil)
	pumpFor(tab, 300*time.Millisecond)
	var peak float64
	evalJSON(t, tab, `(function () { var d = new Float32Array(a.fftSize); a.getFloatTimeDomainData(d); return Math.max.apply(null, d); })()`, &peak)
	if peak < 0.2 || peak > 0.3 {
		t.Fatalf("worklet output peak %v", peak)
	}
	var adv float64
	evalJSON(t, tab, `(function () { var t0 = c.currentTime; return t0; })()`, &adv)
	pumpFor(tab, 200*time.Millisecond)
	var t2 float64
	evalJSON(t, tab, `c.currentTime`, &t2)
	if t2-adv < 0.15 {
		t.Fatalf("worklet context time %v -> %v", adv, t2)
	}
}

// MediaStreamAudioSourceNode carries the track's audio into the graph, and MediaStreamAudioDestinationNode turns what
// reaches it into a track that can be read again.
func TestMediaStreamNodesCarryAudio(t *testing.T) {
	tab := audioTab(t)
	var res map[string]any
	evalJSON(t, tab, `(function () { window.c = new AudioContext({ sampleRate: 48000 }); c.resume(); return 1; })()`, nil)
	activate(t, tab)
	evalJSON(t, tab, `(async function () {
	  var out = {}; var c = window.c;
	  // microphone (synthetic 440 Hz tone) -> analyser
	  var mic = await navigator.mediaDevices.getUserMedia({ audio: true });
	  var src = c.createMediaStreamSource(mic), an = c.createAnalyser(); an.fftSize = 2048; src.connect(an);
	  await new Promise(function (r) { setTimeout(r, 400); });
	  var d = new Float32Array(an.fftSize); an.getFloatTimeDomainData(d);
	  var peak = 0; for (var i = 0; i < d.length; i++) peak = Math.max(peak, Math.abs(d[i]));
	  var f = new Float32Array(an.frequencyBinCount); an.getFloatFrequencyData(f);
	  var bin = 0; for (var i = 1; i < f.length; i++) if (f[i] > f[bin]) bin = i;
	  out.micPeak = peak; out.micHz = bin * c.sampleRate / an.fftSize;
	  // oscillator -> destination node -> track -> processor
	  var dest = c.createMediaStreamDestination(), osc = c.createOscillator(); osc.frequency.value = 1000;
	  var g = c.createGain(); g.gain.value = 0.5; osc.connect(g); g.connect(dest); osc.start();
	  var track = dest.stream.getAudioTracks()[0];
	  out.kind = track.kind; out.live = track.readyState;
	  var rd = new MediaStreamTrackProcessor({ track: track }).readable.getReader();
	  var frames = 0, peak2 = 0, rate = 0, zc = 0, prev = 0;
	  while (frames < 24000) {
	    var x = await rd.read(); if (x.done) break;
	    var a = x.value; rate = a.sampleRate; frames += a.numberOfFrames;
	    var b = new Float32Array(a.numberOfFrames); a.copyTo(b, { planeIndex: 0, format: 'f32-planar' });
	    for (var i = 0; i < b.length; i++) { peak2 = Math.max(peak2, Math.abs(b[i])); if (prev < 0 && b[i] >= 0) zc++; prev = b[i]; }
	    a.close();
	  }
	  out.destPeak = peak2; out.destRate = rate; out.destHz = zc / (frames / rate);
	  return out; })()`, &res)
	t.Logf("%v", res)
	if res["micPeak"].(float64) < 0.05 || res["micHz"].(float64) < 380 || res["micHz"].(float64) > 500 {
		t.Errorf("microphone through MediaStreamAudioSourceNode: peak %v, %v Hz", res["micPeak"], res["micHz"])
	}
	if res["kind"] != "audio" || res["live"] != "live" {
		t.Errorf("destination track: %v %v", res["kind"], res["live"])
	}
	if p := res["destPeak"].(float64); p < 0.4 || p > 0.55 {
		t.Errorf("destination peak %v, want about 0.5", p)
	}
	if hz := res["destHz"].(float64); hz < 900 || hz > 1100 {
		t.Errorf("destination tone %v Hz, want 1000", hz)
	}
}

// A WebRTC call's remote audio can go through Web Audio (level meters, effects): createMediaStreamSource on the
// received stream feeds the graph.
func TestRemoteAudioThroughWebAudio(t *testing.T) {
	tab := audioTab(t)
	evalJSON(t, tab, `
window.err = []; window.peak = 0;
var a = new RTCPeerConnection(), b = new RTCPeerConnection(); window.a = a; window.b = b;
a.onicecandidate = function (e) { if (e.candidate) b.addIceCandidate(e.candidate).catch(function (x) { err.push('b:' + x); }); };
b.onicecandidate = function (e) { if (e.candidate) a.addIceCandidate(e.candidate).catch(function (x) { err.push('a:' + x); }); };
var ctx = new AudioContext({ sampleRate: 48000 }); ctx.resume();
b.ontrack = function (e) {
  var src = ctx.createMediaStreamSource(e.streams[0]), an = ctx.createAnalyser(); an.fftSize = 1024; src.connect(an); window.an = an;
};
(async function () {
  var ms = await navigator.mediaDevices.getUserMedia({ audio: true });
  ms.getTracks().forEach(function (t) { a.addTrack(t, ms); });
  var o = await a.createOffer(); await a.setLocalDescription(o);
  await b.setRemoteDescription(a.localDescription);
  var an = await b.createAnswer(); await b.setLocalDescription(an);
  await a.setRemoteDescription(b.localDescription);
})().catch(function (e) { err.push('call:' + e); });
1`, nil)
	activate(t, tab)
	waitJS(t, tab, `window.an && (function () { var d = new Float32Array(an.fftSize); an.getFloatTimeDomainData(d); var p = 0; for (var i = 0; i < d.length; i++) p = Math.max(p, Math.abs(d[i])); window.peak = Math.max(window.peak, p); return window.peak > 0.05; })()`, 25*time.Second)
}
