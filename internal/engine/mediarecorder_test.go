package engine

import (
	"encoding/base64"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media"
)

// recJS is a page helper: record `stream` for ms milliseconds and resolve with the events, the Blob size and its
// bytes in base64. opts may hold timeslice and mimeType.
const recJS = `
window.recordFor = function (stream, ms, opts) {
  opts = opts || {};
  return new Promise(function (resolve, reject) {
    var r, ev = [], chunks = [], t0 = performance.now();
    try { r = opts.mimeType === undefined ? new MediaRecorder(stream) : new MediaRecorder(stream, { mimeType: opts.mimeType, audioBitsPerSecond: opts.abps, videoBitsPerSecond: opts.vbps }); } catch (e) { return reject(e); }
    ['start', 'pause', 'resume', 'error', 'stop'].forEach(function (n) { r.addEventListener(n, function (e) { ev.push(n + (n === 'error' ? ':' + e.error.name : '')); }); });
    r.ondataavailable = function (e) { ev.push('data'); chunks.push(e.data); if (!(e instanceof BlobEvent)) ev.push('notBlobEvent'); window.lastTimecode = e.timecode; };
    r.onstop = function () {
      var blob = new Blob(chunks, { type: r.mimeType });
      var fr = new FileReader();
      fr.onload = function () { window.lastBlob = blob; resolve({ ev: ev, n: chunks.length, size: blob.size, type: blob.type, mime: r.mimeType, state: r.state, b64: String(fr.result).slice(String(fr.result).lastIndexOf(',') + 1), sizes: chunks.map(function (c) { return c.size; }) }); };
      fr.readAsDataURL(blob);
    };
    r.start(opts.timeslice);
    window.rec = r;
    setTimeout(function () { if (opts.during) opts.during(r); }, ms / 2);
    setTimeout(function () { r.stop(); }, ms);
  });
};
`

type recResult struct {
	Ev    []string `json:"ev"`
	N     int      `json:"n"`
	Size  int      `json:"size"`
	Type  string   `json:"type"`
	Mime  string   `json:"mime"`
	State string   `json:"state"`
	B64   string   `json:"b64"`
	Sizes []int    `json:"sizes"`
}

func (r *recResult) bytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(r.B64)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func recTab(t *testing.T) (*Tab, *testBroker) {
	t.Helper()
	fakeOn(t)
	srv := capServer(t, nil)
	b := &testBroker{decide: allow}
	tab := capTab(t, srv.URL, b)
	evalJSON(t, tab, recJS+`1`, nil)
	return tab, b
}

func rms32(s []float32) float64 {
	var a float64
	for _, v := range s {
		a += float64(v) * float64(v)
	}
	return math.Sqrt(a / float64(len(s)))
}

func zeroCrossFreq(s []float32, rate int) float64 {
	first, last, n := -1, 0, 0
	for i := 1; i < len(s); i++ {
		if s[i-1] < 0 && s[i] >= 0 {
			if first < 0 {
				first = i
			}
			last = i
			n++
		}
	}
	if n < 2 {
		return 0
	}
	return float64(n-1) * float64(rate) / float64(last-first)
}

// checkTone decodes a recording with the repo's own decoders and checks length, level and pitch.
func checkTone(t *testing.T, data []byte, wantSecs float64) {
	t.Helper()
	p, err := media.DecodeAudio(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	secs := float64(len(p.Data[0])) / float64(p.SampleRate)
	if math.Abs(secs-wantSecs) > 0.5 {
		t.Errorf("decoded %.2f s, want about %.2f", secs, wantSecs)
	}
	mid := p.Data[0][len(p.Data[0])/4 : len(p.Data[0])*3/4]
	if r := rms32(mid); r < 0.12 || r > 0.22 {
		t.Errorf("rms %.3f, want about 0.177 (a -12 dBFS tone)", r)
	}
	if f := zeroCrossFreq(mid, p.SampleRate); math.Abs(f-440) > 6 {
		t.Errorf("tone at %.1f Hz, want 440", f)
	}
}

func TestMediaRecorderIsTypeSupportedMatrix(t *testing.T) {
	tab, _ := recTab(t)
	yes := []string{"audio/webm", "audio/webm;codecs=opus", `audio/webm;codecs="opus"`, "audio/ogg", "audio/ogg;codecs=opus", "video/webm", "video/webm;codecs=vp8",
		"video/webm;codecs=vp8,opus", "video/webm;codecs=opus", "", "AUDIO/WEBM;CODECS=OPUS"}
	no := []string{"audio/mp4", "video/mp4", "audio/mpeg", "audio/wav", "video/webm;codecs=vp9", "video/webm;codecs=av01", "video/webm;codecs=h264", "video/webm;codecs=avc1.42E01E",
		"audio/webm;codecs=vorbis", "audio/ogg;codecs=vorbis", "audio/ogg;codecs=flac", "video/ogg", "application/octet-stream", "nonsense"}
	var got map[string]bool
	evalJSON(t, tab, `(function(){var o={}; JSON.parse(`+jsonStr(jsonStr(append(yes, no...)))+`).forEach(function(s){o[s]=MediaRecorder.isTypeSupported(s)}); return o})()`, &got)
	for _, s := range yes {
		if !got[s] {
			t.Errorf("isTypeSupported(%q) = false", s)
		}
	}
	for _, s := range no {
		if got[s] {
			t.Errorf("isTypeSupported(%q) = true", s)
		}
	}
	var misc map[string]any
	evalJSON(t, tab, `(function(){var e; try{MediaRecorder.isTypeSupported()}catch(x){e=x.name}
	  return {err:e, str:String(MediaRecorder), tag:Object.prototype.toString.call(MediaRecorder.prototype), len:MediaRecorder.length, ev:typeof BlobEvent, nul:MediaRecorder.isTypeSupported(null), und:MediaRecorder.isTypeSupported(undefined)}})()`, &misc)
	if misc["err"] != "TypeError" || misc["tag"] != "[object MediaRecorder]" || misc["ev"] != "function" || misc["nul"] != false || misc["und"] != false {
		t.Errorf("misc: %v", misc)
	}
}

func TestMediaRecorderConstructorErrors(t *testing.T) {
	tab, _ := recTab(t)
	var out map[string]string
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(s){
	  window.s = s; var o = {};
	  function name(f){ try { f(); return 'none'; } catch(e){ return e.name + ': ' + e.message; } }
	  o.noarg = name(function(){ new MediaRecorder(); });
	  o.notstream = name(function(){ new MediaRecorder({}); });
	  o.nullstream = name(function(){ new MediaRecorder(null); });
	  o.badmime = name(function(){ new MediaRecorder(s, {mimeType:'video/mp4'}); });
	  o.vp9 = name(function(){ new MediaRecorder(s, {mimeType:'video/webm;codecs=vp9'}); });
	  o.badmode = name(function(){ new MediaRecorder(s, {audioBitrateMode:'turbo'}); });
	  o.nonew = name(function(){ MediaRecorder(s); });
	  o.ok = name(function(){ new MediaRecorder(s, {mimeType:'audio/ogg;codecs=opus'}); });
	  o.okempty = name(function(){ new MediaRecorder(s, {}); });
	  o.oknull = name(function(){ new MediaRecorder(s, null); });
	  return o; })`, &out)
	for k, want := range map[string]string{"noarg": "TypeError", "notstream": "TypeError", "nullstream": "TypeError", "badmime": "NotSupportedError", "vp9": "NotSupportedError",
		"badmode": "TypeError", "nonew": "TypeError", "ok": "none", "okempty": "none", "oknull": "none"} {
		if !strings.HasPrefix(out[k], want) {
			t.Errorf("%s: %q, want %s", k, out[k], want)
		}
	}
	if !strings.Contains(out["badmime"], "video/mp4") {
		t.Errorf("the message must name the type: %q", out["badmime"])
	}
}

func TestMediaRecorderStateMachine(t *testing.T) {
	tab, _ := recTab(t)
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(s){
	  var o = {}, log = [];
	  function name(f){ try { f(); return 'none'; } catch(e){ return e.name; } }
	  var r = new MediaRecorder(s, {mimeType:'audio/webm;codecs=opus', audioBitsPerSecond: 48000});
	  o.initial = [r.state, r.mimeType, r.audioBitsPerSecond, r.videoBitsPerSecond, r.audioBitrateMode, r.stream === s];
	  o.pauseInactive = name(function(){ r.pause(); }); o.resumeInactive = name(function(){ r.resume(); }); o.requestInactive = name(function(){ r.requestData(); });
	  o.stopInactive = name(function(){ r.stop(); });
	  ['start','pause','resume','stop','dataavailable','error'].forEach(function(n){ r.addEventListener(n, function(){ log.push(n + ':' + r.state); }); });
	  r.start();
	  o.afterStart = r.state; o.startTwice = name(function(){ r.start(); });
	  r.pause(); o.paused = r.state; r.pause(); o.pauseTwice = r.state;
	  o.startPaused = name(function(){ r.start(); });
	  r.resume(); o.resumed = r.state; r.resume();
	  r.requestData();
	  return new Promise(function(res){
	    setTimeout(function(){ r.stop(); o.afterStop = r.state; o.stopTwice = name(function(){ r.stop(); }); o.startAfterStop = r.state; }, 300);
	    setTimeout(function(){ o.log = log; o.final = r.state; res(o); }, 700);
	  }); })`, &out)
	for k, want := range map[string]any{"pauseInactive": "InvalidStateError", "resumeInactive": "InvalidStateError", "requestInactive": "InvalidStateError", "stopInactive": "none", "afterStart": "recording",
		"startTwice": "InvalidStateError", "paused": "paused", "pauseTwice": "paused", "startPaused": "InvalidStateError", "resumed": "recording", "afterStop": "inactive", "stopTwice": "none", "final": "inactive"} {
		if out[k] != want {
			t.Errorf("%s = %v, want %v", k, out[k], want)
		}
	}
	init := out["initial"].([]any)
	if init[0] != "inactive" || init[1] != "audio/webm;codecs=opus" || init[2].(float64) != 48000 || init[3].(float64) != 0 || init[4] != "variable" || init[5] != true {
		t.Errorf("initial properties: %v", init)
	}
	log := out["log"].([]any)
	var seq []string
	for _, l := range log {
		seq = append(seq, l.(string))
	}
	got := strings.Join(seq, " ")
	// start, pause, resume come first (as tasks), a dataavailable for requestData, then data and stop at the end with state inactive
	if !strings.HasPrefix(got, "start:") || !strings.HasSuffix(got, "dataavailable:inactive stop:inactive") || !strings.Contains(got, "pause:") || !strings.Contains(got, "resume:") {
		t.Errorf("event log: %s", got)
	}
	if strings.Contains(got, "error") {
		t.Errorf("unexpected error event: %s", got)
	}
}

func TestMediaRecorderAudioRoundTrip(t *testing.T) {
	for _, mime := range []string{"audio/webm;codecs=opus", "audio/ogg;codecs=opus", "audio/webm"} {
		t.Run(mime, func(t *testing.T) {
			tab, _ := recTab(t)
			var res recResult
			evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(s){ return recordFor(s, 2000, {mimeType:`+jsonStr(mime)+`, abps: 64000}); })`, &res)
			if res.Mime != mime || res.Type != mime || res.State != "inactive" {
				t.Errorf("mimeType %q, blob type %q, state %q", res.Mime, res.Type, res.State)
			}
			if got := strings.Join(res.Ev, " "); got != "start data stop" {
				t.Errorf("events: %s", got)
			}
			if res.Size < 2000 || res.Size != len(res.bytes(t)) {
				t.Errorf("blob of %d bytes", res.Size)
			}
			checkTone(t, res.bytes(t), 2.0)
			info, err := media.ProbeBytes(res.bytes(t))
			if err != nil || info.Duration < 1.5 || info.Duration > 2.6 {
				t.Errorf("probe: %v %+v", err, info)
			}
			head := string(res.bytes(t)[:4])
			if strings.Contains(mime, "ogg") != (head == "OggS") {
				t.Errorf("container magic %q for %s", head, mime)
			}
		})
	}
}

func TestMediaRecorderDefaultTypes(t *testing.T) {
	tab, _ := recTab(t)
	var out []recResult
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true, video:{width:320,height:240}}).then(function(s){
	  var a = new MediaStream(s.getAudioTracks()), v = new MediaStream(s.getVideoTracks()), res = [];
	  return recordFor(a, 400).then(function(x){ res.push(x); return recordFor(v, 600); }).then(function(x){ res.push(x); return recordFor(s, 600); }).then(function(x){ res.push(x); return res; }); })`, &out)
	want := []string{"audio/webm;codecs=opus", "video/webm;codecs=vp8", "video/webm;codecs=vp8,opus"}
	for i, w := range want {
		if out[i].Mime != w || out[i].Type != w {
			t.Errorf("default type %d: mimeType %q blob %q, want %s", i, out[i].Mime, out[i].Type, w)
		}
	}
}

func TestMediaRecorderTimesliceChunksAndEventOrder(t *testing.T) {
	tab, _ := recTab(t)
	var res recResult
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(s){ return recordFor(s, 1800, {mimeType:'audio/ogg;codecs=opus', timeslice: 250, abps: 32000}); })`, &res)
	if res.N < 5 || res.N > 10 {
		t.Errorf("%d chunks for 1.8 s at 250 ms: %v", res.N, res.sizes())
	}
	ev := res.Ev
	if len(ev) < 4 || ev[0] != "start" || ev[len(ev)-1] != "stop" || ev[len(ev)-2] != "data" {
		t.Errorf("events: %v", ev)
	}
	for i := 1; i < len(ev)-1; i++ {
		if ev[i] != "data" {
			t.Errorf("event %d is %q, only dataavailable may fall between start and stop", i, ev[i])
		}
	}
	for i, n := range res.Sizes {
		if n == 0 {
			t.Errorf("chunk %d is empty", i)
		}
	}
	checkTone(t, res.bytes(t), 1.8)
	var tc float64
	evalJSON(t, tab, `window.lastTimecode`, &tc)
	if tc < 1200 || tc > 2000 {
		t.Errorf("last timecode %.0f ms", tc)
	}
}

func (r *recResult) sizes() []int { return r.Sizes }

func TestMediaRecorderPauseResumeDuration(t *testing.T) {
	tab, _ := recTab(t)
	var res recResult
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(s){
	  return recordFor(s, 2400, {mimeType:'audio/webm;codecs=opus', during: function(r){ r.pause(); setTimeout(function(){ r.resume(); }, 800); }}); })`, &res)
	if got := strings.Join(res.Ev, " "); got != "start pause resume data stop" {
		t.Errorf("events: %s", got)
	}
	p, err := media.DecodeAudio(res.bytes(t))
	if err != nil {
		t.Fatal(err)
	}
	if secs := float64(len(p.Data[0])) / float64(p.SampleRate); secs < 1.2 || secs > 1.9 {
		t.Errorf("recorded %.2f s: 2.4 s of wall time with an 0.8 s pause should leave about 1.6 s", secs)
	}
}

func TestMediaRecorderVideoAudioRoundTrip(t *testing.T) {
	tab, _ := recTab(t)
	var res recResult
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true, video:{width:320,height:240,frameRate:15}}).then(function(s){
	  return recordFor(s, 2000, {mimeType:'video/webm;codecs=vp8,opus', abps: 48000, vbps: 800000}); })`, &res)
	if got := strings.Join(res.Ev, " "); got != "start data stop" {
		t.Errorf("events: %s", got)
	}
	data := res.bytes(t)
	vs, err := media.ExtractVideoSamples(data)
	if err != nil {
		t.Fatal(err)
	}
	if vs.Codec != "vp8" || vs.Width != 320 || vs.Height != 240 || len(vs.Chunks) < 20 {
		t.Fatalf("video track: %s %dx%d with %d chunks", vs.Codec, vs.Width, vs.Height, len(vs.Chunks))
	}
	if !vs.Chunks[0].Key {
		t.Error("first video chunk must be a key frame")
	}
	dec, err := media.NewWCVideoDecoder("vp8", nil)
	if err != nil {
		t.Fatal(err)
	}
	var pics []*media.WCPicture
	for i, c := range vs.Chunks {
		p, err := dec.Decode(c.Data)
		if err != nil || len(p) != 1 {
			t.Fatalf("chunk %d: %v", i, err)
		}
		pics = append(pics, p[0])
	}
	// the synthetic camera draws eight colour bars: the picture must be the bars (left to right, rotated by the frame number)
	last := pics[len(pics)-1]
	if bright, dark := last.Planes[0][100*last.Strides[0]+10], last.Planes[0][100*last.Strides[0]+10]; bright == 0 && dark == 0 {
		t.Error("the decoded picture is black")
	}
	distinct := map[byte]bool{}
	for x := 0; x < 320; x += 8 {
		distinct[last.Planes[0][120*last.Strides[0]+x]/16] = true
	}
	if len(distinct) < 4 {
		t.Errorf("only %d distinct luma levels across the bars", len(distinct))
	}
	checkTone(t, data, 2.0)
	if info, err := media.ProbeBytes(data); err != nil || info.Duration < 1.6 || info.Duration > 2.6 || info.Video() == nil {
		t.Errorf("probe: %v %+v", err, info)
	}
	// audio and video stay in sync: the last audio packet and the last video frame end within a frame or two
	p, err := media.DecodeAudio(data)
	if err != nil {
		t.Fatal(err)
	}
	aEnd := float64(len(p.Data[0])) / float64(p.SampleRate)
	vEnd := float64(vs.Chunks[len(vs.Chunks)-1].PTS) / 1e6
	if math.Abs(aEnd-vEnd) > 0.4 {
		t.Errorf("audio ends at %.2f s, video at %.2f s", aEnd, vEnd)
	}
}

func TestMediaRecorderStopsWhenAllTracksEnd(t *testing.T) {
	tab, _ := recTab(t)
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true, video:{width:320,height:240}}).then(function(s){
	  var r = new MediaRecorder(s), ev = [];
	  r.ondataavailable = function(e){ ev.push('data:' + (e.data.size > 0)); }; r.onstop = function(){ ev.push('stop'); r.__stopped = true; };
	  r.start();
	  return new Promise(function(res){
	    setTimeout(function(){ s.getAudioTracks()[0].stop(); }, 300);
	    setTimeout(function(){ ev.push('after-audio:' + r.state); s.getVideoTracks()[0].stop(); ev.push('state:' + r.state); }, 600);
	    setTimeout(function(){ res({ev: ev, state: r.state}); }, 1100);
	  }); })`, &out)
	got := ""
	for _, e := range out["ev"].([]any) {
		got += e.(string) + " "
	}
	if got != "after-audio:recording state:inactive data:true stop " || out["state"] != "inactive" {
		t.Errorf("events: %s state %v", got, out["state"])
	}
}

func TestMediaRecorderErrors(t *testing.T) {
	tab, _ := recTab(t)
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(s){
	  var o = {}, a = s.getAudioTracks()[0];
	  // a stream whose only track already ended: start() reports an error event and the recorder goes back to inactive
	  var dead = new MediaStream([a.clone()]); dead.getTracks()[0].stop();
	  var r1 = new MediaRecorder(dead), ev1 = [];
	  r1.onerror = function(e){ ev1.push('error:' + e.error.name); }; r1.onstop = function(){ ev1.push('stop'); };
	  r1.start(); ev1.push('sync:' + r1.state);
	  // an empty stream
	  var r2 = new MediaRecorder(new MediaStream()), ev2 = [];
	  r2.onerror = function(e){ ev2.push('error:' + e.error.name); };
	  r2.start();
	  // adding a track while recording is an InvalidModificationError and stops the recording
	  var live = new MediaStream([a.clone()]), r3 = new MediaRecorder(live), ev3 = [];
	  r3.onerror = function(e){ ev3.push('error:' + e.error.name); }; r3.ondataavailable = function(){ ev3.push('data'); }; r3.onstop = function(){ ev3.push('stop'); };
	  r3.start(); live.addTrack(a.clone());
	  return new Promise(function(res){ setTimeout(function(){ res({ev1: ev1, s1: r1.state, ev2: ev2, s2: r2.state, ev3: ev3, s3: r3.state}); }, 600); }); })`, &out)
	join := func(k string) string {
		s := ""
		for _, e := range out[k].([]any) {
			s += e.(string) + " "
		}
		return strings.TrimSpace(s)
	}
	if got := join("ev1"); got != "sync:recording error:NotSupportedError stop" || out["s1"] != "inactive" {
		t.Errorf("ended stream: %q state %v", got, out["s1"])
	}
	if got := join("ev2"); got != "error:NotSupportedError" || out["s2"] != "inactive" {
		t.Errorf("empty stream: %q state %v", got, out["s2"])
	}
	if got := join("ev3"); got != "error:InvalidModificationError data stop" || out["s3"] != "inactive" {
		t.Errorf("track added while recording: %q state %v", got, out["s3"])
	}
}

func TestMediaRecorderCannotRecordWhatThePageWasNotGranted(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	b := &testBroker{decide: allow}
	a := capTab(t, srv.URL, b)
	c := capTab(t, srv.URL, b)
	evalJSON(t, a, recJS+`1`, nil)
	evalJSON(t, c, recJS+`1`, nil)
	var id string
	evalJSON(t, a, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(s){ window.s = s; return s.getAudioTracks()[0].id; })`, &id)
	// the other page forges a track object with the first page's track id: there is nothing to record
	var out map[string]any
	evalJSON(t, c, `(function(){
	  var forged = __swMakeTrack('audio', 'forged'); forged._t.id = `+jsonStr(id)+`;
	  var ms = new MediaStream([forged]), r = new MediaRecorder(ms), ev = [];
	  r.onerror = function(e){ ev.push(e.error.name); }; r.ondataavailable = function(){ ev.push('data'); };
	  r.start();
	  return new Promise(function(res){ setTimeout(function(){ res({ev: ev, state: r.state}); }, 400); }); })()`, &out)
	if ev := out["ev"].([]any); len(ev) != 1 || ev[0] != "NotSupportedError" || out["state"] != "inactive" {
		t.Errorf("a track the page does not own was recordable: %v", out)
	}
	// and a revoked track ends: the recording stops with the data so far
	var rev map[string]any
	evalJSON(t, a, `new Promise(function(res){
	  var r = new MediaRecorder(s), ev = [];
	  r.ondataavailable = function(){ ev.push('data'); }; r.onstop = function(){ ev.push('stop'); };
	  r.start(); setTimeout(function(){ s.getTracks().forEach(function(t){ t.stop(); }); }, 300);
	  setTimeout(function(){ res({ev: ev, state: r.state}); }, 800); })`, &rev)
	if ev := rev["ev"].([]any); len(ev) != 2 || ev[0] != "data" || ev[1] != "stop" {
		t.Errorf("after the permission was taken away: %v", rev)
	}
}

func TestMediaRecorderWebAudioDestination(t *testing.T) {
	tab, _ := recTab(t)
	evalJSON(t, tab, `(function(){
	  var ctx = new AudioContext({sampleRate: 44100}), osc = ctx.createOscillator(), g = ctx.createGain(), dest = ctx.createMediaStreamDestination();
	  osc.frequency.value = 440; g.gain.value = 0.25; osc.connect(g); g.connect(dest); osc.start();
	  window.ctx = ctx; window.dest = dest; window.resumed = ctx.resume().then(function(){ return ctx.state; }); return 1; })()`, nil)
	if err := tab.Click("#c"); err != nil { // a click is the user activation that lets the context run
		t.Fatal(err)
	}
	tab.Settle(100 * time.Millisecond)
	var res recResult
	evalJSON(t, tab, `resumed.then(function(st){ if (st !== 'running') throw new Error('context is ' + st); return recordFor(dest.stream, 1500, {mimeType:'audio/webm;codecs=opus'}); })`, &res)
	data := res.bytes(t)
	p, err := media.DecodeAudio(data)
	if err != nil {
		t.Fatalf("decode: %v (%d bytes, events %v)", err, len(data), res.Ev)
	}
	mid := p.Data[0][len(p.Data[0])/4 : len(p.Data[0])*3/4]
	if r := rms32(mid); r < 0.12 || r > 0.22 {
		t.Errorf("rms %.3f", r)
	}
	if f := zeroCrossFreq(mid, p.SampleRate); math.Abs(f-440) > 8 {
		t.Errorf("tone at %.1f Hz", f)
	}
	if secs := float64(len(p.Data[0])) / float64(p.SampleRate); secs < 0.8 || secs > 2.2 {
		t.Errorf("%.2f s", secs)
	}
}

func TestMediaRecorderCanvasCaptureStream(t *testing.T) {
	tab, _ := recTab(t)
	var res recResult
	evalJSON(t, tab, `(function(){
	  var c = document.getElementById('c'); c.width = 160; c.height = 120; var x = c.getContext('2d'), n = 0;
	  var iv = setInterval(function(){ x.fillStyle = n % 2 ? '#e03030' : '#3050e0'; x.fillRect(0, 0, 160, 120); x.fillStyle = '#ffffff'; x.fillRect(10 + (n * 6) % 100, 20, 30, 30); n++; }, 40);
	  var s = c.captureStream(25);
	  return recordFor(s, 1500, {mimeType:'video/webm;codecs=vp8'}).then(function(r){ clearInterval(iv); return r; }); })()`, &res)
	vs, err := media.ExtractVideoSamples(res.bytes(t))
	if err != nil {
		t.Fatal(err)
	}
	if vs.Width != 160 || vs.Height != 120 || len(vs.Chunks) < 10 {
		t.Fatalf("canvas recording: %dx%d, %d chunks", vs.Width, vs.Height, len(vs.Chunks))
	}
	dec, _ := media.NewWCVideoDecoder("vp8", nil)
	red, blue := 0, 0
	for _, c := range vs.Chunks {
		p, err := dec.Decode(c.Data)
		if err != nil || len(p) != 1 {
			t.Fatalf("decode: %v", err)
		}
		px := p[0]
		if px.Planes[0][100*px.Strides[0]+150] > 0 { // corner pixel, away from the white square
			u, v := px.Planes[1][50*px.Strides[1]+75], px.Planes[2][50*px.Strides[2]+75]
			if v > 150 && u < 120 {
				red++
			} else if u > 150 && v < 120 {
				blue++
			}
		}
	}
	if red < 2 || blue < 2 {
		t.Errorf("the canvas alternates red and blue: saw %d red and %d blue frames", red, blue)
	}
}

// WhatsApp Web records a voice note with audio/ogg;codecs=opus when it can and falls back to WebM Opus, then plays
// the blob back through an <audio> element.
func TestMediaRecorderWhatsAppVoiceNoteFlow(t *testing.T) {
	tab, _ := recTab(t)
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:{echoCancellation:true,noiseSuppression:true,channelCount:1}}).then(function(stream){
	  var type = ['audio/ogg; codecs=opus', 'audio/webm;codecs=opus', 'audio/mp4'].filter(function(t){ return MediaRecorder.isTypeSupported(t); })[0];
	  var rec = new MediaRecorder(stream, {mimeType: type, audioBitsPerSecond: 32000}), chunks = [], t0 = performance.now();
	  rec.ondataavailable = function(e){ if (e.data.size) chunks.push(e.data); };
	  return new Promise(function(res, rej){
	    rec.onstop = function(){
	      stream.getTracks().forEach(function(t){ t.stop(); });
	      var blob = new Blob(chunks, {type: rec.mimeType}), url = URL.createObjectURL(blob), a = new Audio();
	      a.preload = 'auto';
	      a.onerror = function(){ rej(new Error('audio error ' + (a.error && a.error.code) + ' ' + (a.error && a.error.message))); };
	      a.onloadedmetadata = function(){ res({type: type, mime: rec.mimeType, blobType: blob.type, size: blob.size, duration: a.duration, readyState: a.readyState, wall: (performance.now() - t0) / 1000}); };
	      a.src = url;
	    };
	    rec.start(); setTimeout(function(){ rec.stop(); }, 1800);
	  }); })`, &out)
	if out["type"] != "audio/ogg; codecs=opus" || out["mime"] != "audio/ogg; codecs=opus" {
		t.Errorf("the page should have got Ogg Opus: %v", out)
	}
	d, _ := out["duration"].(float64)
	if d < 1.4 || d > 2.3 || out["size"].(float64) < 3000 {
		t.Errorf("voice note: %v", out)
	}
	_ = time.Second
}

func TestMediaRecorderNavigationEndsTheRecordingAndFreesTheDevice(t *testing.T) {
	tab, _ := recTab(t)
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true, video:{width:320,height:240}}).then(function(s){
	  window.r = new MediaRecorder(s); window.got = 0; r.ondataavailable = function(){ got++; }; r.start(100); return 1; })`, nil)
	time.Sleep(400 * time.Millisecond)
	if capture.LiveTracks() == 0 {
		t.Fatal("expected live tracks while recording")
	}
	if err := tab.Navigate(tab.URL()+"?other", nil); err != nil {
		t.Fatal(err)
	}
	tab.Settle(100 * time.Millisecond)
	deadline := time.Now().Add(3 * time.Second)
	for capture.LiveTracks() > 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := capture.LiveTracks(); n != 0 {
		t.Errorf("%d capture tracks still live after the page went away", n)
	}
	var res recResult
	evalJSON(t, tab, recJS+`navigator.mediaDevices.getUserMedia({audio:true}).then(function(s){ return recordFor(s, 600); })`, &res)
	if res.N < 1 || res.Size < 500 {
		t.Errorf("the new page could not record: %+v", res.Ev)
	}
}
