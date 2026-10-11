package engine

import (
	"encoding/json"
	"math"
	"os"
	"testing"
	"time"
)

func evalJSON(t *testing.T, tab *Tab, js string, out any) {
	t.Helper()
	raw, err := tab.Evaluate(js)
	if err != nil {
		t.Fatalf("evaluate: %v\n%s", err, js)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
	}
}

func newMediaTab(t *testing.T) *Tab {
	t.Helper()
	srv := mediaServer(t)
	o := DefaultOptions()
	o.SettleMs = 30
	e := New(o)
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab(srv.URL + "/fx.html?s=none")
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func TestRealtimeAudioContext(t *testing.T) {
	tab := newMediaTab(t)
	var st0 string
	evalJSON(t, tab, `(function(){window.c=new AudioContext();window.ev=[];c.onstatechange=function(){ev.push(c.state)};return c.state})()`, &st0)
	if st0 != "suspended" {
		t.Fatalf("context created without user activation must be suspended, got %s", st0)
	}
	// resume() stays pending until an agent action counts as activation
	evalJSON(t, tab, `(function(){window.resumed=false;c.resume().then(function(){resumed=true});return 1})()`, nil)
	tab.Settle(100 * time.Millisecond)
	var r bool
	evalJSON(t, tab, `resumed`, &r)
	if r {
		t.Fatal("resume() must not resolve without user activation")
	}
	if err := tab.Click("#go"); err != nil {
		t.Fatal(err)
	}
	tab.Settle(100 * time.Millisecond)
	evalJSON(t, tab, `resumed && c.state==='running'`, &r)
	if !r {
		t.Fatal("resume() should resolve after a click")
	}
	var t0 float64
	evalJSON(t, tab, `c.currentTime`, &t0)
	time.Sleep(500 * time.Millisecond) // an idle page's event loop does not wait; wall time still passes
	var t1 float64
	evalJSON(t, tab, `c.currentTime`, &t1)
	t.Logf("currentTime advanced %.3f s in a 0.5 s settle", t1-t0)
	if t1-t0 < 0.4 || t1-t0 > 0.8 {
		t.Fatalf("currentTime advanced %.3f", t1-t0)
	}
	// live graph: oscillator -> analyser has the right spectral peak
	var peak map[string]float64
	evalJSON(t, tab, `(function(){var o=c.createOscillator(),a=c.createAnalyser();a.fftSize=2048;o.frequency.value=1000;o.connect(a);a.connect(c.destination);o.start();window.an=a;window.osc=o;return 1})()`, nil)
	time.Sleep(150 * time.Millisecond)
	evalJSON(t, tab, `(function(){var f=new Float32Array(an.frequencyBinCount);an.getFloatFrequencyData(f);var p=0;for(var i=0;i<f.length;i++)if(f[i]>f[p])p=i;return {bin:p,hz:p*c.sampleRate/an.fftSize}})()`, &peak)
	if math.Abs(peak["hz"]-1000) > 25 {
		t.Fatalf("analyser peak at %v Hz", peak["hz"])
	}
	// onended fires after stop()
	evalJSON(t, tab, `(function(){window.ended=false;osc.onended=function(){ended=true};osc.stop(c.currentTime+0.1);return 1})()`, nil)
	time.Sleep(200 * time.Millisecond)
	tab.Settle(300 * time.Millisecond)
	evalJSON(t, tab, `ended`, &r)
	if !r {
		t.Fatal("ended event did not fire")
	}
	var closed string
	evalJSON(t, tab, `c.close().then(function(){return c.state})`, &closed)
	if closed != "closed" {
		t.Fatalf("state after close: %s", closed)
	}
}

func TestAudioWorkletAndScriptProcessor(t *testing.T) {
	tab := newMediaTab(t)
	var res map[string]any
	evalJSON(t, tab, `(async function(){
	  var c = new OfflineAudioContext(1, 1024, 44100);
	  await c.audioWorklet.addModule('wl.js');
	  var cs = c.createConstantSource(); cs.offset.value = 0.25;
	  var w = new AudioWorkletNode(c, 'gain-proc');
	  var msg = null; w.port.onmessage = function(e){ msg = e.data; };
	  w.parameters.get('g').setValueAtTime(2, 0); w.parameters.get('g').linearRampToValueAtTime(4, 1024/44100);
	  cs.connect(w); w.connect(c.destination); cs.start();
	  var b = await c.startRendering(); var d = b.getChannelData(0);
	  var maxErr = 0; for (var i = 0; i < 1024; i++) { var g = 2 + 2*(i/1024); maxErr = Math.max(maxErr, Math.abs(d[i] - 0.25*g)); }
	  var c2 = new OfflineAudioContext(1, 1024, 44100), s2 = c2.createConstantSource(), sp = c2.createScriptProcessor(256, 1, 1);
	  s2.offset.value = 0.5; sp.onaudioprocess = function(e){ var i = e.inputBuffer.getChannelData(0), o = e.outputBuffer.getChannelData(0); for (var k=0;k<i.length;k++) o[k] = i[k]*0.5; };
	  s2.connect(sp); sp.connect(c2.destination); s2.start();
	  var b2 = await c2.startRendering(); var d2 = b2.getChannelData(0);
	  var err = null; try { new AudioWorkletNode(c, 'nope'); } catch (e) { err = e.name; }
	  return { workletMaxErr: maxErr, params: w.parameters.size, msgSeen: msg !== null, spFirst: d2[0], spLate: d2[800], err: err };
	})()`, &res)
	t.Logf("worklet result: %v", res)
	if res["workletMaxErr"].(float64) > 1e-3 {
		t.Errorf("worklet error %v", res["workletMaxErr"])
	}
	if res["spFirst"].(float64) != 0 || math.Abs(res["spLate"].(float64)-0.25) > 1e-6 {
		t.Errorf("script processor output first=%v late=%v (expect silence for the first block, then 0.25)", res["spFirst"], res["spLate"])
	}
	if res["err"] != "InvalidStateError" {
		t.Errorf("unknown processor error: %v", res["err"])
	}
}

func TestMediaElementSourceAndBinaryFetch(t *testing.T) {
	tab := newMediaTab(t)
	st, _ := os.Stat("testdata/media/tone.wav")
	var info map[string]any
	evalJSON(t, tab, `fetch('tone.wav').then(function(r){return r.arrayBuffer()}).then(function(b){var u=new Uint8Array(b);return {len:b.byteLength,magic:String.fromCharCode(u[0],u[1],u[2],u[3])}})`, &info)
	if int64(info["len"].(float64)) != st.Size() || info["magic"] != "RIFF" {
		t.Fatalf("fetch arrayBuffer corrupted binary: %v (file %d bytes)", info, st.Size())
	}
	if err := tab.Click("#go"); err != nil {
		t.Fatal(err)
	}
	evalJSON(t, tab, `(function(){window.a=new Audio('tone.wav');window.c=new AudioContext();c.resume();window.src=c.createMediaElementSource(a);window.an=c.createAnalyser();an.fftSize=2048;src.connect(an);an.connect(c.destination);return a.play().then(function(){return 1})})()`, nil)
	tab.Settle(400 * time.Millisecond)
	var hz float64
	evalJSON(t, tab, `(function(){var f=new Float32Array(an.frequencyBinCount);an.getFloatFrequencyData(f);var p=0;for(var i=0;i<f.length;i++)if(f[i]>f[p])p=i;return p*c.sampleRate/an.fftSize})()`, &hz)
	if math.Abs(hz-440) > 25 {
		t.Fatalf("MediaElementAudioSourceNode peak at %v Hz, want 440", hz)
	}
	var dupe string
	evalJSON(t, tab, `(function(){try{c.createMediaElementSource(a)}catch(e){return e.name}return 'none'})()`, &dupe)
	if dupe != "InvalidStateError" {
		t.Errorf("second MediaElementSource: %s", dupe)
	}
}

func TestFakeMediaDevicesOptIn(t *testing.T) {
	tab := newMediaTab(t)
	var out []any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(){return ['resolved']},function(e){return [e.name]})`, &out)
	if out[0] != "NotFoundError" {
		t.Fatalf("default getUserMedia: %v", out)
	}
	os.Setenv("SWEEF_FAKE_MEDIA", "1")
	defer os.Unsetenv("SWEEF_FAKE_MEDIA")
	tab2 := newMediaTab(t)
	var res map[string]any
	evalJSON(t, tab2, `navigator.mediaDevices.enumerateDevices().then(function(d){return navigator.mediaDevices.getUserMedia({audio:true,video:{width:320,height:240}}).then(function(s){return {devices:d.length,tracks:s.getTracks().map(function(t){return t.kind}),settings:s.getVideoTracks()[0].getSettings().width}})})`, &res)
	if res["devices"].(float64) != 3 || res["settings"].(float64) != 320 {
		t.Fatalf("fake devices: %v", res)
	}
}

func TestTextTrackSRTAndCues(t *testing.T) {
	tab := newMediaTab(t)
	var res map[string]any
	evalJSON(t, tab, `new Promise(function(resolve){
	  var v=document.createElement('video');v.muted=true;v.src='clip.webm';
	  var tr=document.createElement('track');tr.kind='subtitles';tr.src='sub.srt';tr.default=true;v.appendChild(tr);document.body.appendChild(v);
	  var log=[];v.textTracks.addEventListener('addtrack',function(){log.push('addtrack')});
	  tr.addEventListener('load',function(){
	    var t0=v.textTracks[0],c=t0.cues,n0=c.length;
	    var cue=new VTTCue(0.1,0.2,'x'); t0.addCue(cue);
	    resolve({n:n0,first:[c[1].startTime,c[1].endTime,c[1].text],added:t0.cues.length,mode:t0.mode,ready:tr.readyState,log:log});
	  });
	})`, &res)
	if res["n"].(float64) != 2 || res["ready"].(float64) != 2 || res["added"].(float64) != 3 {
		t.Fatalf("srt cues: %v", res)
	}
	var ev []any
	evalJSON(t, tab, `new Promise(function(resolve){
	  var v=document.createElement('video');v.muted=true;v.src='clip.webm';document.body.appendChild(v);
	  var tt=v.addTextTrack('metadata','m','en');tt.mode='hidden';
	  var c=new VTTCue(0.3,0.6,'hello'),log=[];tt.addCue(c);
	  c.onenter=function(){log.push('enter')};c.onexit=function(){log.push('exit')};tt.oncuechange=function(){log.push('change:'+tt.activeCues.length)};
	  v.addEventListener('canplay',function(){v.play()});
	  setTimeout(function(){resolve(log)},1300);
	})`, &ev)
	t.Logf("cue events: %v", ev)
	want := []any{"enter", "change:1", "exit", "change:0"}
	if len(ev) < 4 || ev[0] != want[0] || ev[1] != want[1] || ev[2] != want[2] || ev[3] != want[3] {
		t.Fatalf("cue event sequence %v, want %v", ev, want)
	}
}
