package engine

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/rtcmedia"
	"github.com/sweefbrowser/sweef/internal/rtcmedia/testsrc"
)

// These tests run a real video call between two RTCPeerConnections in one page (or two tabs) over the
// loopback interface: the sender takes the synthetic camera and microphone (the fake devices), VP8 and Opus
// travel through pion, and the receiving <video> element shows the decoded pictures. Nothing here touches
// the internet; the sandbox's HTTPS-proxy-only egress could not carry WebRTC anyway.

// frameLog records every frame pushed to a <video> element.
type frameLog struct {
	mu       sync.Mutex
	n        int
	counters []uint16
	bad      int
	last     *image.YCbCr
	sizes    map[[2]int]int
	times    []time.Time
}

func newFrameLog() *frameLog { return &frameLog{sizes: map[[2]int]int{}} }

func (f *frameLog) install(t *testing.T) {
	h := func(img *image.YCbCr) {
		c, ok := testsrc.ReadCounter(img)
		f.mu.Lock()
		f.n++
		f.last = img
		b := img.Bounds()
		f.sizes[[2]int{b.Dx(), b.Dy()}]++
		f.times = append(f.times, time.Now())
		if ok {
			f.counters = append(f.counters, c)
		} else {
			f.bad++
		}
		f.mu.Unlock()
	}
	rtcFrameHook.Store(&h)
	t.Cleanup(func() { rtcFrameHook.Store(nil) })
}

func (f *frameLog) snapshot() (n int, counters []uint16, last *image.YCbCr) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n, append([]uint16(nil), f.counters...), f.last
}

func psnrYCbCr(img *image.YCbCr, w, h int, n uint32) float64 {
	ref := testsrc.Pattern(w, h, n)
	b := img.Bounds()
	if b.Dx() != w || b.Dy() != h {
		return -1
	}
	var se float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := float64(img.Y[(b.Min.Y+y)*img.YStride+b.Min.X+x]) - float64(ref.Y[y*ref.YStride+x])
			se += d * d
		}
	}
	if se == 0 {
		return 99
	}
	return 10 * math.Log10(255*255*float64(w*h)/se)
}

// callJS builds a call in the page: window.a sends the fake camera and microphone to window.b, whose first
// stream is attached to <video id=rv>.
func callJS(w, h, fps int) string {
	return fmt.Sprintf(`
window.__swFakeMediaDevices = true;
window.tracks = []; window.err = [];
window.a = new RTCPeerConnection(); window.b = new RTCPeerConnection();
a.onicecandidate = function (e) { if (e.candidate) b.addIceCandidate(e.candidate).catch(function (x) { err.push('b:' + x); }); };
b.onicecandidate = function (e) { if (e.candidate) a.addIceCandidate(e.candidate).catch(function (x) { err.push('a:' + x); }); };
var rv = document.createElement('video'); rv.id = 'rv'; rv.autoplay = true; rv.muted = true; document.body.appendChild(rv);
window.rv = rv; window.rvEvents = [];
['loadedmetadata','loadeddata','resize','playing','canplay'].forEach(function (n) { rv.addEventListener(n, function () { rvEvents.push(n + ':' + rv.videoWidth + 'x' + rv.videoHeight + ':' + rv.readyState); }); });
b.ontrack = function (e) { tracks.push(e.track.kind); if (e.track.kind === 'video') rv.srcObject = e.streams[0]; };
(async function () {
  window.local = await navigator.mediaDevices.getUserMedia({ video: { width: %d, height: %d, frameRate: %d }, audio: true });
  local.getTracks().forEach(function (t) { a.addTrack(t, local); });
  var o = await a.createOffer(); await a.setLocalDescription(o);
  await b.setRemoteDescription(a.localDescription);
  var an = await b.createAnswer(); await b.setLocalDescription(an);
  await a.setRemoteDescription(b.localDescription);
})().catch(function (e) { err.push('call:' + e); });
1`, w, h, fps)
}

func pumpFor(tab *Tab, d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		tab.Settle(30 * time.Millisecond)
	}
}

func TestWebRTCVideoCall(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	fl := newFrameLog()
	fl.install(t)
	sink := &rtcmedia.RecordSink{}
	SetRTCAudioSinkFactory(func() rtcmedia.AudioSink { return sink })
	t.Cleanup(func() { SetRTCAudioSinkFactory(nil) })
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, callJS(640, 360, 30), nil)
	waitJS(t, tab, `rv.readyState >= 2 && rv.videoWidth === 640 && rv.videoHeight === 360`, 20*time.Second)
	var evs []string
	evalJSON(t, tab, `rvEvents`, &evs)
	t.Logf("video element events: %v", evs)
	var ready int
	evalJSON(t, tab, `rv.readyState`, &ready)
	if ready < 2 {
		t.Fatalf("readyState %d", ready)
	}

	// 5 seconds of steady state: counters progress, <5% of the source frames lost
	n0, c0, _ := fl.snapshot()
	pumpFor(tab, 5*time.Second)
	n1, c1, last := fl.snapshot()
	cs := c1[len(c0):]
	missing := 0
	for i := 1; i < len(cs); i++ {
		d := int(int16(cs[i] - cs[i-1]))
		if d <= 0 {
			t.Fatalf("frame counter went backwards: %d -> %d", cs[i-1], cs[i])
		}
		missing += d - 1
	}
	span := len(cs) + missing
	t.Logf("5 s: %d frames shown (%.1f fps), %d of %d source frames skipped (%.1f%%), %d unreadable counters", n1-n0, float64(n1-n0)/5, missing, span, 100*float64(missing)/float64(span), fl.bad)
	if float64(missing) > 0.05*float64(span) || n1-n0 < 120 {
		t.Errorf("too many dropped frames: %d skipped of %d, %d shown", missing, span, n1-n0)
	}

	// the pixels in the element match the source
	if c, ok := testsrc.ReadCounter(last); ok {
		ps := psnrYCbCr(last, 640, 360, uint32(c))
		t.Logf("PSNR-Y remote frame %d vs the source pattern: %.2f dB", c, ps)
		if ps < 30 {
			t.Errorf("remote picture differs from the source: %.2f dB", ps)
		}
	} else {
		t.Error("last remote frame has no readable counter")
	}
	// ... and the element's bitmap (what paints) shows the same picture
	raw, _ := tab.Evaluate("__swIdOf(document.getElementById('rv'))")
	var id int64
	_ = json.Unmarshal(raw, &id)
	tab.mu.RLock()
	d := tab.dom
	tab.mu.RUnlock()
	src := tab.BitmapSourceFor(d.NodeByID(id))
	if src == nil || src.Bitmap() == nil {
		t.Fatal("no bitmap source for the video element")
	}
	if w, h := src.Size(); w != 640 || h != 360 {
		t.Errorf("bitmap size %dx%d", w, h)
	}

	// audio reached the sink: the 440 Hz tone
	pcm, rate, ch := sink.Snapshot()
	if rate != 48000 || ch != 2 || len(pcm) < 48000*2 {
		t.Fatalf("audio sink got %d samples (%d Hz, %d ch)", len(pcm), rate, ch)
	}
	tone := testsrc.ToneLevel(pcm[len(pcm)/2:], 2, 48000, 440)
	t.Logf("audio: %d samples, 440 Hz tone purity %.3f", len(pcm)/2, tone)
	if tone < 0.9 {
		t.Errorf("tone purity %.3f", tone)
	}

	// statistics
	evalJSON(t, tab, `(window.st = null, Promise.all([a.getStats(), b.getStats()]).then(function (r) { window.st = r.map(function (rep) { var o = {}; rep.forEach(function (s, id) { o[id] = s; }); return o; }); }), 1)`, nil)
	waitJS(t, tab, `st !== null`, 5*time.Second)
	var st []map[string]map[string]any
	evalJSON(t, tab, `st`, &st)
	find := func(rep map[string]map[string]any, typ, kind string) map[string]any {
		for _, s := range rep {
			if s["type"] == typ && (kind == "" || s["kind"] == kind) {
				return s
			}
		}
		return nil
	}
	num := func(m map[string]any, k string) float64 { v, _ := m[k].(float64); return v }
	ov, ia := find(st[0], "outbound-rtp", "video"), find(st[1], "inbound-rtp", "audio")
	iv, oa := find(st[1], "inbound-rtp", "video"), find(st[0], "outbound-rtp", "audio")
	if ov == nil || iv == nil || ia == nil || oa == nil {
		t.Fatalf("missing rtp stats: %v", keys(st[0]))
	}
	t.Logf("outbound video: framesEncoded %v keyFrames %v %vx%v %.1f fps totalEncodeTime %.2f s qualityLimitationReason %v bytesSent %v packetsSent %v nackCount %v",
		ov["framesEncoded"], ov["keyFramesEncoded"], ov["frameWidth"], ov["frameHeight"], num(ov, "framesPerSecond"), num(ov, "totalEncodeTime"), ov["qualityLimitationReason"], ov["bytesSent"], ov["packetsSent"], ov["nackCount"])
	t.Logf("inbound video: framesDecoded %v dropped %v %vx%v %.1f fps jitter %v packetsLost %v bytesReceived %v jitterBufferDelay %.3f (/%v)",
		iv["framesDecoded"], iv["framesDropped"], iv["frameWidth"], iv["frameHeight"], num(iv, "framesPerSecond"), iv["jitter"], iv["packetsLost"], iv["bytesReceived"], num(iv, "jitterBufferDelay"), iv["jitterBufferEmittedCount"])
	t.Logf("inbound audio: audioLevel %.3f totalAudioEnergy %.3f concealed %v", num(ia, "audioLevel"), num(ia, "totalAudioEnergy"), ia["concealedSamples"])
	if num(ov, "framesEncoded") < 100 || num(ov, "keyFramesEncoded") < 1 || num(ov, "frameWidth") != 640 || num(ov, "totalEncodeTime") <= 0 {
		t.Errorf("outbound-rtp video stats look wrong: %v", ov)
	}
	if num(iv, "framesDecoded") < 100 || num(iv, "frameWidth") != 640 || num(iv, "bytesReceived") <= 0 || num(iv, "framesPerSecond") < 20 {
		t.Errorf("inbound-rtp video stats look wrong: %v", iv)
	}
	if num(ia, "totalAudioEnergy") <= 0 || num(ia, "audioLevel") <= 0 {
		t.Errorf("inbound-rtp audio levels missing: %v", ia)
	}
	if ri := find(st[0], "remote-inbound-rtp", "video"); ri == nil {
		t.Logf("note: no remote-inbound-rtp for video in the sender's report (keys %v)", keys(st[0]))
	} else {
		t.Logf("remote-inbound-rtp video: roundTripTime %v fractionLost %v jitter %v", ri["roundTripTime"], ri["fractionLost"], ri["jitter"])
	}
	if cp := find(st[0], "candidate-pair", ""); cp == nil || num(cp, "availableOutgoingBitrate") <= 0 {
		t.Errorf("candidate-pair availableOutgoingBitrate missing: %v", cp)
	} else {
		t.Logf("candidate-pair: availableOutgoingBitrate %.0f currentRoundTripTime %v congestion control %v", num(cp, "availableOutgoingBitrate"), cp["currentRoundTripTime"], cp["sweefCongestionControl"])
	}
	if find(st[0], "codec", "") == nil {
		t.Errorf("no codec entries")
	}
	if find(st[0], "media-source", "video") == nil {
		t.Errorf("no media-source entry")
	}
	var syn []map[string]any
	evalJSON(t, tab, `b.getReceivers().map(function (r) { return r.getSynchronizationSources(); }).reduce(function (x, y) { return x.concat(y); }, [])`, &syn)
	if len(syn) < 2 {
		t.Errorf("getSynchronizationSources: %v", syn)
	}
	var tr []string
	evalJSON(t, tab, `tracks`, &tr)
	if len(tr) != 2 {
		t.Errorf("track events %v", tr)
	}
}

func keys(m map[string]map[string]any) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+":"+fmt.Sprint(v["type"]))
	}
	return out
}

// Muting, replaceTrack, setParameters, stopping and hanging up.
func TestWebRTCCallControls(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	fl := newFrameLog()
	fl.install(t)
	before := runtime.NumGoroutine()
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, callJS(320, 240, 30), nil)
	waitJS(t, tab, `rv.readyState >= 2 && rv.videoWidth === 320`, 20*time.Second)

	// track.enabled = false: black frames; true: the picture comes back
	evalJSON(t, tab, `(local.getVideoTracks()[0].enabled = false, 1)`, nil)
	pumpFor(tab, 1500*time.Millisecond)
	_, _, last := fl.snapshot()
	if y := meanY(last); y > 30 {
		t.Errorf("disabled track: remote mean luma %.0f, want black", y)
	}
	evalJSON(t, tab, `(local.getVideoTracks()[0].enabled = true, 1)`, nil)
	pumpFor(tab, 1500*time.Millisecond)
	_, _, last = fl.snapshot()
	if y := meanY(last); y < 60 {
		t.Errorf("re-enabled track: remote mean luma %.0f, want the picture back", y)
	}

	// replaceTrack with a canvas track: the remote picture becomes the canvas content (a solid colour)
	evalJSON(t, tab, `(function () {
	  var c = document.createElement('canvas'); c.width = 320; c.height = 240; document.body.appendChild(c);
	  var g = c.getContext('2d'); g.fillStyle = 'rgb(250,20,20)'; g.fillRect(0, 0, 320, 240);
	  window.cvs = c.captureStream(30);
	  window.rep = null;
	  var vs = a.getSenders().filter(function (s) { return s.track && s.track.kind === 'video'; })[0];
	  vs.replaceTrack(cvs.getVideoTracks()[0]).then(function () { window.rep = vs.track === cvs.getVideoTracks()[0]; }, function (e) { window.rep = 'err:' + e; });
	  return 1; })()`, nil)
	waitJS(t, tab, `rep !== null`, 5*time.Second)
	var rep any
	evalJSON(t, tab, `rep`, &rep)
	if rep != true {
		t.Fatalf("replaceTrack: %v", rep)
	}
	deadline := time.Now().Add(10 * time.Second)
	red := false
	for time.Now().Before(deadline) && !red {
		pumpFor(tab, 200*time.Millisecond)
		_, _, last = fl.snapshot()
		r, _, _ := meanRGBLike(last)
		red = r > 150
	}
	if !red {
		t.Errorf("replaceTrack: the remote picture never turned red")
	}

	// setParameters: cap the bitrate and the frame rate, halve the size
	evalJSON(t, tab, `(function () {
	  window.sp = null;
	  var vs = a.getSenders().filter(function (s) { return s.track && s.track.kind === 'video'; })[0];
	  var p = vs.getParameters(); window.pcodecs = p.codecs.map(function (c) { return c.mimeType; });
	  p.encodings[0].maxBitrate = 200000; p.encodings[0].maxFramerate = 10; p.encodings[0].scaleResolutionDownBy = 2;
	  vs.setParameters(p).then(function () { window.sp = true; }, function (e) { window.sp = 'err:' + e; });
	  return 1; })()`, nil)
	waitJS(t, tab, `sp !== null`, 5*time.Second)
	var sp any
	evalJSON(t, tab, `sp`, &sp)
	if sp != true {
		t.Fatalf("setParameters: %v", sp)
	}
	var codecs []string
	evalJSON(t, tab, `pcodecs`, &codecs)
	t.Logf("getParameters().codecs: %v", codecs)
	if len(codecs) == 0 {
		t.Errorf("getParameters().codecs is empty after negotiation")
	}
	start, _, _ := fl.snapshot()
	pumpFor(tab, 3*time.Second)
	end, _, last := fl.snapshot()
	b := last.Bounds()
	fps := float64(end-start) / 3
	t.Logf("after setParameters(maxFramerate 10, scaleResolutionDownBy 2): %dx%d at %.1f fps", b.Dx(), b.Dy(), fps)
	if b.Dx() != 160 || b.Dy() != 120 || fps > 14 {
		t.Errorf("setParameters not honoured: %dx%d %.1f fps", b.Dx(), b.Dy(), fps)
	}
	// active = false stops the stream
	evalJSON(t, tab, `(function () {
	  var vs = a.getSenders().filter(function (s) { return s.track && s.track.kind === 'video'; })[0];
	  var p = vs.getParameters(); p.encodings[0].active = false; vs.setParameters(p); return 1; })()`, nil)
	pumpFor(tab, 800*time.Millisecond)
	s0, _, _ := fl.snapshot()
	pumpFor(tab, 1200*time.Millisecond)
	s1, _, _ := fl.snapshot()
	if s1-s0 > 2 {
		t.Errorf("active=false still delivered %d frames", s1-s0)
	}

	// hang up: every goroutine goes away
	evalJSON(t, tab, `(local.getTracks().forEach(function (t) { t.stop(); }), a.close(), b.close(), 1)`, nil)
	pumpFor(tab, 300*time.Millisecond)
	e.Close()
	var after int
	for i := 0; i < 50; i++ {
		after = runtime.NumGoroutine()
		if after <= before+3 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Logf("goroutines: %d before the call, %d after hanging up", before, after)
	if after > before+3 {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, true)
		t.Errorf("goroutine leak: %d before, %d after\n%s", before, after, buf[:n])
	}
}

func meanY(img *image.YCbCr) float64 {
	if img == nil {
		return -1
	}
	b := img.Bounds()
	var s, n float64
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			s += float64(img.Y[y*img.YStride+x])
			n++
		}
	}
	return s / n
}

// meanRGBLike returns approximate mean R, G, B of the centre of a YCbCr picture.
func meanRGBLike(img *image.YCbCr) (r, g, b float64) {
	if img == nil {
		return
	}
	bd := img.Bounds()
	var n float64
	for y := bd.Min.Y + bd.Dy()/3; y < bd.Max.Y-bd.Dy()/3; y += 4 {
		for x := bd.Min.X + bd.Dx()/3; x < bd.Max.X-bd.Dx()/3; x += 4 {
			yy, cb, cr := float64(img.Y[y*img.YStride+x]), float64(img.Cb[(y/2)*img.CStride+x/2])-128, float64(img.Cr[(y/2)*img.CStride+x/2])-128
			r += 1.164*(yy-16) + 1.596*cr
			g += 1.164*(yy-16) - 0.392*cb - 0.813*cr
			b += 1.164*(yy-16) + 2.017*cb
			n++
		}
	}
	return r / n, g / n, b / n
}

// Loss recovery: a blackout of 800 ms (every media datagram dropped, retransmissions included) must end
// with the receiver asking for a key frame (PLI) and the picture resuming.
func TestWebRTCRecoversFromLoss(t *testing.T) {
	rtcMode(t, WebRTCHostRaw)
	fl := newFrameLog()
	fl.install(t)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, callJS(640, 360, 30), nil)
	waitJS(t, tab, `rv.readyState >= 2`, 20*time.Second)
	pumpFor(tab, 2*time.Second)
	n0, _, _ := fl.snapshot()
	rtcSim.blackout(800 * time.Millisecond)
	t.Cleanup(rtcSim.reset)
	pumpFor(tab, 5*time.Second)
	rtcSim.reset()
	n1, c1, _ := fl.snapshot()
	// frames kept coming after the blackout
	pumpFor(tab, 1500*time.Millisecond)
	n2, _, _ := fl.snapshot()
	t.Logf("frames shown: %d before, %d during blackout+recovery, %d in the 1.5 s after; dropped datagrams: %d", n0, n1-n0, n2-n1, rtcSim.Dropped)
	if n2-n1 < 30 {
		t.Fatalf("video did not resume: %d frames in 1.5 s", n2-n1)
	}
	// look at the stats: the receiver sent PLI, the sender produced more than one key frame
	evalJSON(t, tab, `(window.st = null, Promise.all([a.getStats(), b.getStats()]).then(function (r) { window.st = r.map(function (rep) { var o = {}; rep.forEach(function (s, id) { o[id] = s; }); return o; }); }), 1)`, nil)
	waitJS(t, tab, `st !== null`, 5*time.Second)
	var st []map[string]map[string]any
	evalJSON(t, tab, `st`, &st)
	var keyFrames, plis float64
	for _, s := range st[0] {
		if s["type"] == "outbound-rtp" && s["kind"] == "video" {
			keyFrames, _ = s["keyFramesEncoded"].(float64)
			plis, _ = s["pliCount"].(float64)
		}
	}
	var sent float64
	for _, s := range st[1] {
		if s["type"] == "inbound-rtp" && s["kind"] == "video" {
			sent, _ = s["pliCount"].(float64)
		}
	}
	t.Logf("sender: %.0f key frames, %.0f PLI received; receiver sent %.0f PLI; counters after: %d frames", keyFrames, plis, sent, len(c1))
	if keyFrames < 2 {
		t.Errorf("no recovery key frame was produced (keyFramesEncoded %.0f)", keyFrames)
	}
}

// A bottleneck: the sender must lower its bitrate and picture size, and keep delivering.
func TestWebRTCAdaptsToBandwidth(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 40 s")
	}
	rtcMode(t, WebRTCHostRaw)
	fl := newFrameLog()
	fl.install(t)
	e := newRTCEngine(t)
	tab := newRTCTab(t, e, rtcServer(t))
	evalJSON(t, tab, callJS(1280, 720, 30), nil)
	waitJS(t, tab, `rv.readyState >= 2`, 30*time.Second)
	pumpFor(tab, 4*time.Second)
	_, _, last := fl.snapshot()
	b := last.Bounds()
	t.Logf("unconstrained: %dx%d", b.Dx(), b.Dy())
	rtcSim.set(0, 250_000, 250*time.Millisecond) // 250 kbit/s, 250 ms of queue
	t.Cleanup(rtcSim.reset)
	var w, h int
	var lim, gccStats string
	var target float64
	prev, _, _ := fl.snapshot()
	for i := 0; i < 40; i++ {
		pumpFor(tab, time.Second)
		nowN, _, _ := fl.snapshot()
		_, _, last = fl.snapshot()
		b = last.Bounds()
		w, h = b.Dx(), b.Dy()
		evalJSON(t, tab, `(window.st = null, a.getStats().then(function (rep) { var o = {}; rep.forEach(function (s, id) { o[id] = s; }); window.st = o; }), 1)`, nil)
		waitJS(t, tab, `st !== null`, 5*time.Second)
		var st map[string]map[string]any
		evalJSON(t, tab, `st`, &st)
		for _, s := range st {
			if s["type"] == "outbound-rtp" && s["kind"] == "video" {
				lim, _ = s["qualityLimitationReason"].(string)
				target, _ = s["targetBitrate"].(float64)
			}
			if s["type"] == "candidate-pair" {
				gccStats = fmt.Sprint(s["sweefCongestionControl"])
			}
		}
		t.Logf("t+%2ds: %dx%d, %d frames in the last second, targetBitrate %.0f, reason %q gcc %s", i+1, w, h, nowN-prev, target, lim, gccStats)
		prev = nowN
		if w <= 480 && target < 400_000 && i >= 15 {
			break
		}
	}
	t.Logf("with a 250 kbit/s bottleneck: remote picture %dx%d, targetBitrate %.0f bit/s, qualityLimitationReason %q (dropped %d, passed %d datagrams)", w, h, target, lim, rtcSim.Dropped, rtcSim.Passed)
	if w > 640 {
		t.Errorf("resolution was not reduced: %dx%d", w, h)
	}
	if lim != "bandwidth" {
		t.Errorf("qualityLimitationReason %q, want bandwidth", lim)
	}
	n0, _, _ := fl.snapshot()
	pumpFor(tab, 3*time.Second)
	n1, _, _ := fl.snapshot()
	if n1-n0 < 20 {
		t.Errorf("only %d frames in 3 s through the bottleneck", n1-n0)
	}
}

// CPU cost of a call on this machine, measured over a few seconds of steady state. Not a pass/fail test.
func TestWebRTCCallCPU(t *testing.T) {
	if testing.Short() {
		t.Skip("measurement")
	}
	for _, c := range []struct{ w, h int }{{640, 360}, {1280, 720}} {
		c := c
		t.Run(fmt.Sprintf("%dx%d", c.w, c.h), func(t *testing.T) {
			rtcMode(t, WebRTCHostRaw)
			fl := newFrameLog()
			fl.install(t)
			e := newRTCEngine(t)
			tab := newRTCTab(t, e, rtcServer(t))
			evalJSON(t, tab, callJS(c.w, c.h, 30), nil)
			waitJS(t, tab, fmt.Sprintf(`rv.readyState >= 2 && rv.videoWidth === %d`, c.w), 30*time.Second)
			pumpFor(tab, 3*time.Second) // let the rate and the encoder speed settle
			n0, _, _ := fl.snapshot()
			cpu0, wall0 := procCPU(), time.Now()
			pumpFor(tab, 6*time.Second)
			cpu1, wall1 := procCPU(), time.Now()
			n1, _, _ := fl.snapshot()
			evalJSON(t, tab, `(window.st = null, a.getStats().then(function (rep) { var o = {}; rep.forEach(function (s, id) { o[id] = s; }); window.st = o; }), 1)`, nil)
			waitJS(t, tab, `st !== null`, 5*time.Second)
			var st map[string]map[string]any
			evalJSON(t, tab, `st`, &st)
			var encMs, frames float64
			for _, s := range st {
				if s["type"] == "outbound-rtp" && s["kind"] == "video" {
					tot, _ := s["totalEncodeTime"].(float64)
					frames, _ = s["framesEncoded"].(float64)
					encMs = 1000 * tot / math.Max(frames, 1)
				}
			}
			cores := (cpu1 - cpu0).Seconds() / wall1.Sub(wall0).Seconds()
			t.Logf("%dx%d@30 send+receive in one process: %.2f cores busy (%.1f fps shown), encode %.1f ms/frame average (%.0f frames)", c.w, c.h, cores, float64(n1-n0)/6, encMs, frames)
		})
	}
}

func procCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
