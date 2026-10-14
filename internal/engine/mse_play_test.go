package engine

// End-to-end MSE playback: a small DASH-style page (testdata/media/mse/player.html) feeds fMP4/WebM
// segments the way dash.js does; the tests check what was buffered, which picture is on screen, what sound
// reaches the audio output (a recording sink) and how well the two stay together.

import (
	"encoding/json"
	"image"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/audioout"
)

var fixtureColours = [6][3]int{{255, 0, 0}, {0, 128, 0}, {0, 0, 255}, {255, 255, 0}, {255, 0, 255}, {0, 255, 255}}

func fixtureTone(n int) float64 { return 440 + 220*float64(n) }

// useRecordingSink routes the audio of this test into a real-time recording sink.
func useRecordingSink(t *testing.T) (*audioout.Output, *audioout.TestSink) {
	t.Helper()
	out, sink := audioout.NewRealtimeTestOutput()
	audioout.SetDefault(out)
	t.Cleanup(func() { audioout.SetDefault(nil) })
	return out, sink
}

type framePub struct {
	pts float64
	at  time.Time
}

// recordFrames collects every picture a player publishes during the test.
func recordFrames(t *testing.T) (get func() []framePub) {
	var mu sync.Mutex
	var fr []framePub
	f := func(node int64, pts float64, at time.Time) {
		mu.Lock()
		fr = append(fr, framePub{pts, at})
		mu.Unlock()
	}
	mediaFrameHook.Store(&f)
	t.Cleanup(func() { mediaFrameHook.Store(nil) })
	return func() []framePub {
		mu.Lock()
		defer mu.Unlock()
		return append([]framePub(nil), fr...)
	}
}

func openPlayerPage(t *testing.T, query string) (*Tab, *Engine) {
	t.Helper()
	srv := mediaServer(t)
	o := DefaultOptions()
	o.SettleMs = 20
	e := New(o)
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab(srv.URL + "/mse/player.html?" + query)
	if err != nil {
		t.Fatal(err)
	}
	return tab, e
}

func waitTrue(t *testing.T, tab *Tab, expr string, limit time.Duration) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		tab.Settle(30 * time.Millisecond)
		var ok bool
		raw, err := tab.Evaluate("!!(" + expr + ")")
		if err == nil && json.Unmarshal(raw, &ok) == nil && ok {
			return
		}
	}
	var log string
	raw, _ := tab.Evaluate("JSON.stringify(window.T && T.log) + ' err=' + (window.T && T.error)")
	_ = json.Unmarshal(raw, &log)
	t.Fatalf("timeout waiting for %s\nlog: %s\nconsole: %s", expr, log, consoleText(tab))
}

// shownPixel reads the bottom-right pixel of the video's current picture (background colour, away from the box).
func shownPixel(tab *Tab) (w, h int, c [3]int, ok bool) {
	raw, _ := tab.Evaluate("__swIdOf(document.getElementById('v'))")
	var id int64
	_ = json.Unmarshal(raw, &id)
	tab.mu.RLock()
	d := tab.dom
	tab.mu.RUnlock()
	n := d.NodeByID(id)
	if n == nil {
		return
	}
	src := tab.BitmapSourceFor(n)
	if src == nil {
		return
	}
	img := src.Bitmap()
	if img == nil {
		return
	}
	b := img.Bounds()
	w, h = b.Dx(), b.Dy()
	r, g, bl, _ := img.At(b.Max.X-4, b.Max.Y-4).RGBA()
	return w, h, [3]int{int(r >> 8), int(g >> 8), int(bl >> 8)}, true
}

func colourIs(got, want [3]int, tol int) bool {
	for i := range got {
		if d := got[i] - want[i]; d < -tol || d > tol {
			return false
		}
	}
	return true
}

// leftChannel returns the left channel of interleaved stereo.
func leftChannel(rec []float32) []float32 {
	out := make([]float32, len(rec)/2)
	for i := range out {
		out[i] = rec[2*i]
	}
	return out
}

func hzOf(pcm []float32, rate int) float64 {
	n := 0
	for i := 1; i < len(pcm); i++ {
		if (pcm[i-1] < 0) != (pcm[i] < 0) {
			n++
		}
	}
	return float64(n) / 2 / (float64(len(pcm)) / float64(rate))
}

// firstSound is the index of the first device frame with real signal.
func firstSound(rec []float32) int {
	for i := 0; i < len(rec); i += 2 {
		if rec[i] > 0.05 || rec[i] < -0.05 {
			return i / 2
		}
	}
	return -1
}

func jsNum(t *testing.T, tab *Tab, expr string) float64 {
	t.Helper()
	var f float64
	evalJSON(t, tab, expr, &f)
	return f
}

func TestMSEPlaysH264AACInSyncAcrossQualitySwitch(t *testing.T) {
	_, sink := useRecordingSink(t)
	frames := recordFrames(t)
	// segment 1 high quality, 2 low, 3 high again: an adaptive player switching down and up
	tab, _ := openPlayerPage(t, "fam=h264&plan=0,1,0")
	waitTrue(t, tab, "T.ready || T.error", 15*time.Second)
	var errS string
	evalJSON(t, tab, "String(T.error)", &errS)
	if errS != "null" {
		t.Fatalf("player page failed: %s", errS)
	}
	var buffered [][2]float64
	evalJSON(t, tab, "(function(){var r=T.sbv.buffered,o=[];for(var i=0;i<r.length;i++)o.push([r.start(i),r.end(i)]);return o})()", &buffered)
	if len(buffered) != 1 || buffered[0][0] > 0.05 || math.Abs(buffered[0][1]-6) > 0.05 {
		t.Fatalf("video buffered %v, want [0,6]", buffered)
	}
	if dur := jsNum(t, tab, "document.getElementById('v').duration"); dur < 6 || dur > 6.05 {
		t.Fatalf("duration %v", dur)
	}
	// unmuted autoplay is refused until the page has seen a user gesture
	var name string
	evalJSON(t, tab, "document.getElementById('v').play().then(function(){return 'resolved'},function(e){return e.name})", &name)
	if name != "NotAllowedError" {
		t.Fatalf("play() before a gesture: %q, want NotAllowedError", name)
	}
	if err := tab.Click("#v"); err != nil {
		t.Fatal(err)
	}
	evalJSON(t, tab, "document.getElementById('v').play().then(function(){return 'resolved'},function(e){return e.name})", &name)
	if name != "resolved" {
		t.Fatalf("play() after a click: %q", name)
	}
	t0 := time.Now()
	type sample struct {
		ct   float64
		w    int
		col  [3]int
		have bool
	}
	var samples []sample
	for time.Since(t0) < 9*time.Second {
		tab.Settle(25 * time.Millisecond)
		ct := jsNum(t, tab, "document.getElementById('v').currentTime")
		w, _, c, ok := shownPixel(tab)
		samples = append(samples, sample{ct, w, c, ok})
		var ended bool
		evalJSON(t, tab, "document.getElementById('v').ended", &ended)
		if ended {
			break
		}
	}
	elapsed := time.Since(t0)
	var ended bool
	evalJSON(t, tab, "document.getElementById('v').ended", &ended)
	if !ended {
		t.Fatalf("not ended after %v", elapsed)
	}
	// playback takes the media's duration, give or take start-up
	if elapsed < 5000*time.Millisecond || elapsed > 30*time.Second { // generous: a loaded CI machine runs slow
		t.Errorf("6 s of media took %v", elapsed)
	}

	// picture: the colour follows currentTime, away from the second boundaries
	checked, widths := 0, map[int]bool{}
	for _, s := range samples {
		if !s.have || s.ct < 0.3 || s.ct > 5.9 {
			continue
		}
		frac := s.ct - math.Floor(s.ct)
		if frac < 0.25 || frac > 0.8 {
			continue
		}
		widths[s.w] = true
		sec := int(s.ct)
		if !colourIs(s.col, fixtureColours[sec], 45) {
			t.Errorf("at currentTime %.3f the picture shows %v, want %v", s.ct, s.col, fixtureColours[sec])
		}
		checked++
	}
	if checked < 20 {
		t.Fatalf("only %d picture samples checked", checked)
	}
	if !widths[320] || !widths[160] {
		t.Errorf("expected pictures from both qualities, saw widths %v", widths)
	}

	// sound: the tone steps once a second and is what the audio output received
	rec := sink.Recorded()
	i0 := firstSound(rec)
	if i0 < 0 {
		t.Fatal("no sound reached the audio output")
	}
	left := leftChannel(rec)
	for n := 0; n < 6; n++ {
		a := i0 + n*48000 + 12000
		if a+24000 > len(left) {
			t.Fatalf("recording too short for second %d: %d frames", n, len(left))
		}
		got, want := hzOf(left[a:a+24000], 48000), fixtureTone(n)
		if math.Abs(got-want) > want*0.06 {
			t.Errorf("second %d: heard %.0f Hz, want %.0f", n, got, want)
		}
	}

	// A/V sync: when the listener hears second n begin vs when its picture was shown
	pubs := frames()
	if len(pubs) > 12 {
		var pp []float64
		for _, f := range pubs[:12] {
			pp = append(pp, f.pts)
		}
		t.Logf("%d pictures published, first PTS %.3f", len(pubs), pp)
	}
	var skews []float64
	for n := 1; n <= 5; n++ {
		var shownAt time.Time
		for _, f := range pubs {
			if f.pts >= float64(n)-1e-3 {
				shownAt = f.at
				break
			}
		}
		if shownAt.IsZero() {
			t.Fatalf("no picture published for second %d", n)
		}
		heard := sink.FrameTime(i0 + n*48000)
		skews = append(skews, shownAt.Sub(heard).Seconds()*1000)
	}
	t.Logf("A/V skew at the second boundaries (picture shown minus sound heard, ms): %.1f", skews)
	for _, s := range skews {
		if math.Abs(s) > 300 { // 70 ms on an idle machine; the margin is for loaded CI runners
			t.Errorf("A/V skew %.1f ms exceeds 300 ms: %.1f", s, skews)
			break
		}
	}

	// the frame counters players use to decide on a quality switch
	var q struct{ Total, Dropped, Corrupted int }
	evalJSON(t, tab, "(function(){var q=document.getElementById('v').getVideoPlaybackQuality();return {Total:q.totalVideoFrames,Dropped:q.droppedVideoFrames,Corrupted:q.corruptedVideoFrames}})()", &q)
	t.Logf("getVideoPlaybackQuality: %+v", q)
	if q.Total < 90 || q.Total > 220 {
		t.Errorf("totalVideoFrames %d, want about 150", q.Total)
	}
	if q.Dropped > 60 || q.Corrupted != 0 {
		t.Errorf("dropped %d corrupted %d on an idle machine", q.Dropped, q.Corrupted)
	}
}

func TestMSESeekInsideAndBeyondBuffer(t *testing.T) {
	useRecordingSink(t)
	tab, _ := openPlayerPage(t, "fam=h264&plan=0,0,0")
	waitTrue(t, tab, "T.ready", 15*time.Second)
	_ = tab.Click("#v")
	evalJSON(t, tab, "document.getElementById('v').muted=true;document.getElementById('v').play().then(function(){return 1})", nil)
	waitTrue(t, tab, "document.getElementById('v').currentTime > 0.5", 5*time.Second)

	// inside the buffered range: seeking/seeked, picture of the new second
	evalJSON(t, tab, "window.ev=[];['seeking','seeked'].forEach(function(n){v.addEventListener(n,function(){ev.push(n)})});document.getElementById('v').currentTime=4.3;1", nil)
	waitTrue(t, tab, "ev.indexOf('seeked')>=0", 3*time.Second)
	var ev []string
	evalJSON(t, tab, "ev", &ev)
	if len(ev) < 2 || ev[0] != "seeking" || ev[1] != "seeked" {
		t.Fatalf("seek events %v", ev)
	}
	if ct := jsNum(t, tab, "document.getElementById('v').currentTime"); ct < 4.3 || ct > 5.2 {
		t.Fatalf("currentTime after seek: %v", ct)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		tab.Settle(30 * time.Millisecond)
		ct := jsNum(t, tab, "document.getElementById('v').currentTime")
		if _, _, c, ok := shownPixel(tab); ok && ct >= 4.35 && ct < 4.95 && colourIs(c, fixtureColours[4], 45) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("picture after seek to 4.3 is not magenta")
		}
	}
}

func TestMSESeekOutsideBufferWaitsForData(t *testing.T) {
	useRecordingSink(t)
	// the page appends only the first segment; the test then seeks past it and appends the missing data
	tab, _ := openPlayerPage(t, "fam=h264&plan=0,0,0")
	waitTrue(t, tab, "T.ready", 15*time.Second)
	// drop the 4..6 s range so a seek there lands outside the buffered data
	evalJSON(t, tab, `new Promise(function(res){var sb=T.sbv; sb.addEventListener('updateend',function h(){sb.removeEventListener('updateend',h);res(1)}); sb.remove(4,Infinity)})`, nil)
	var b [][2]float64
	evalJSON(t, tab, "(function(){var r=T.sbv.buffered,o=[];for(var i=0;i<r.length;i++)o.push([r.start(i),r.end(i)]);return o})()", &b)
	if len(b) != 1 || math.Abs(b[0][1]-4) > 0.05 {
		t.Fatalf("buffered after remove(4, Infinity): %v", b)
	}
	evalJSON(t, tab, "window.ev=[];var v=document.getElementById('v');v.muted=true;['seeking','seeked','waiting','canplay'].forEach(function(n){v.addEventListener(n,function(){ev.push(n+'@'+v.readyState)})});v.currentTime=5;1", nil)
	tab.Settle(400 * time.Millisecond)
	var ev []string
	evalJSON(t, tab, "ev", &ev)
	seeked := false
	for _, e := range ev {
		if e[:6] == "seeked" {
			seeked = true
		}
	}
	if seeked || len(ev) == 0 || ev[0] != "seeking@1" {
		t.Fatalf("seeking outside the buffered data must stay pending at HAVE_METADATA: %v", ev)
	}
	var seeking bool
	evalJSON(t, tab, "document.getElementById('v').seeking", &seeking)
	if !seeking {
		t.Fatal("seeking should be true while waiting for data")
	}
	// the page now appends the missing segment (rep 0 segment 3) and the seek completes
	evalJSON(t, tab, `T.get('h264/chunk-0-00003.m4s').then(function(b){return T.append(T.sbv,b)}).then(function(){return 1})`, nil)
	waitTrue(t, tab, "ev.some(function(e){return e.indexOf('seeked')==0})", 3*time.Second)
}

func TestMSEVP9OpusWebM(t *testing.T) {
	_, sink := useRecordingSink(t)
	tab, _ := openPlayerPage(t, "fam=vp9")
	waitTrue(t, tab, "T.ready || T.error", 15*time.Second)
	_ = tab.Click("#v")
	evalJSON(t, tab, "document.getElementById('v').currentTime=2.2;document.getElementById('v').play().then(function(){return 1})", nil)
	waitTrue(t, tab, "document.getElementById('v').currentTime > 3.0", 8*time.Second)
	var w, h int
	var c [3]int
	for i := 0; i < 40; i++ {
		tab.Settle(25 * time.Millisecond)
		var ok bool
		w, h, c, ok = shownPixel(tab)
		ct := jsNum(t, tab, "document.getElementById('v').currentTime")
		if ok && ct < 3.0 && colourIs(c, fixtureColours[2], 50) {
			break
		}
		if ct >= 3.0 {
			break
		}
	}
	if w != 320 || h != 180 {
		t.Fatalf("picture %dx%d", w, h)
	}
	if !colourIs(c, fixtureColours[2], 50) && !colourIs(c, fixtureColours[3], 50) {
		t.Errorf("picture colour %v at 2.2-3 s, want blue/yellow", c)
	}
	rec := leftChannel(sink.Recorded())
	i0 := firstSound(sink.Recorded())
	if i0 < 0 || len(rec) < i0+30000 {
		t.Fatalf("no/short Opus sound: first %d of %d", i0, len(rec))
	}
	// the clip starts at 2.2 s: 880 Hz until 3.0 s
	if got := hzOf(rec[i0+3000:i0+30000], 48000); math.Abs(got-880) > 60 {
		t.Errorf("heard %.0f Hz, want ~880 (second 2)", got)
	}
}

func TestMSEAV1VideoOnly(t *testing.T) {
	tab, _ := openPlayerPage(t, "fam=av1")
	useRecordingSink(t)
	waitTrue(t, tab, "T.ready || T.error", 15*time.Second)
	_ = tab.Click("#v")
	evalJSON(t, tab, "var v=document.getElementById('v');v.muted=true;v.currentTime=1.3;v.play().then(function(){return 1})", nil)
	deadline := time.Now().Add(8 * time.Second)
	for {
		tab.Settle(40 * time.Millisecond)
		ct := jsNum(t, tab, "document.getElementById('v').currentTime")
		if _, _, c, ok := shownPixel(tab); ok && ct > 1.4 && ct < 1.9 && colourIs(c, fixtureColours[1], 60) {
			return
		}
		if ct > 1.95 || time.Now().After(deadline) {
			_, _, c, _ := shownPixel(tab)
			t.Fatalf("AV1 picture at %.2f s is %v, want green", ct, c)
		}
	}
}

func TestMSEQuotaExceededAndEviction(t *testing.T) {
	// the limit leaves room for the first two video segments (3571 bytes of samples) and not for a third
	t.Setenv("SWEEF_MSE_QUOTA", "4400")
	useRecordingSink(t)
	tab, _ := openPlayerPage(t, "fam=h264&manual=1")
	waitTrue(t, tab, "T.ready", 15*time.Second)
	appendSeg := `(function(){return T.get('h264/%s').then(function(b){return T.append(T.sbv,b)}).then(function(){return 'ok'},function(e){return e.name||String(e)})})()`
	var res string
	for _, f := range []string{"init-0.m4s", "chunk-0-00001.m4s", "chunk-0-00002.m4s"} {
		evalJSON(t, tab, strings.Replace(appendSeg, "%s", f, 1), &res)
		if res != "ok" {
			t.Fatalf("append %s: %s", f, res)
		}
	}
	// third segment: full, and the playhead (0) has nothing behind it to evict
	evalJSON(t, tab, `T.get('h264/chunk-0-00003.m4s').then(function(b){try{T.sbv.appendBuffer(b);return 'no throw'}catch(e){return e.name+'/'+e.code}})`, &res)
	if res != "QuotaExceededError/22" {
		t.Fatalf("append into a full buffer: %q, want QuotaExceededError/22", res)
	}
	var upd bool
	evalJSON(t, tab, "T.sbv.updating", &upd)
	if upd {
		t.Fatal("a refused append must not leave the buffer updating")
	}
	// the player moved into the second segment: the first one is evicted to make room
	evalJSON(t, tab, "var v=document.getElementById('v'); v.muted=true; v.currentTime=3.9; 1", nil)
	waitTrue(t, tab, "!v.seeking", 5*time.Second)
	evalJSON(t, tab, `T.get('h264/chunk-0-00003.m4s').then(function(b){return T.append(T.sbv,b)}).then(function(){return 'ok'},function(e){return e.name})`, &res)
	if res != "ok" {
		t.Fatalf("append after the playhead moved on: %s", res)
	}
	var b [][2]float64
	evalJSON(t, tab, "(function(){var r=T.sbv.buffered,o=[];for(var i=0;i<r.length;i++)o.push([r.start(i),r.end(i)]);return o})()", &b)
	if len(b) != 1 || b[0][0] < 1.9 || math.Abs(b[0][1]-6) > 0.05 {
		t.Fatalf("after eviction buffered %v, want [~2, 6]", b)
	}
}

func TestMSEMediaSourceEventsAndErrors(t *testing.T) {
	useRecordingSink(t)
	tab := newMediaTab(t)
	var out map[string]any
	evalJSON(t, tab, `(function(){
	  var r = {};
	  var ms = new MediaSource();
	  r.state0 = ms.readyState; r.dur0 = String(ms.duration);
	  try { ms.addSourceBuffer('video/webm; codecs="vp8"'); } catch (e) { r.addClosed = e.name; }
	  var v = document.createElement('video'); v.src = URL.createObjectURL(ms);
	  return new Promise(function (res) {
	    ms.addEventListener('sourceopen', function () {
	      r.state1 = ms.readyState;
	      try { ms.addSourceBuffer('video/x-bogus'); } catch (e) { r.bogus = e.name; }
	      try { ms.addSourceBuffer(''); } catch (e) { r.empty = e.name; }
	      var sb = ms.addSourceBuffer('video/webm; codecs="vp8, vorbis"');
	      r.mode = sb.mode; r.tso = sb.timestampOffset; r.ws = sb.appendWindowStart; r.we = String(sb.appendWindowEnd);
	      try { sb.appendWindowStart = 5; sb.appendWindowEnd = 4; } catch (e) { r.window = e.name; }
	      try { sb.appendWindowEnd = 3; sb.appendWindowStart = 4; } catch (e) { r.window2 = e.name; }
	      try { sb.remove(0, 1); } catch (e) { r.removeNaN = e.name; }
	      try { ms.duration = -1; } catch (e) { r.negDur = e.name; }
	      try { sb.appendBuffer('not a buffer'); } catch (e) { r.badData = e.name; }
	      sb.appendWindowEnd = Infinity; sb.appendWindowStart = 0;
	      sb.addEventListener('error', function () { r.errorEvent = true; });
	      sb.addEventListener('updateend', function () {
	        r.updating = sb.updating; r.stateAfterError = ms.readyState;
	        try { sb.appendBuffer(new Uint8Array(4)); } catch (e) { r.afterEnded = e.name; }
	        try { ms.removeSourceBuffer(sb); r.removed = true; sb.buffered; } catch (e) { r.removedBuffered = e.name; }
	        res(r);
	      });
	      sb.appendBuffer(new Uint8Array([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20]));
	    });
	  });
	})()`, &out)
	want := map[string]any{
		"state0": "closed", "dur0": "NaN", "addClosed": "InvalidStateError", "state1": "open",
		"bogus": "NotSupportedError", "empty": "TypeError", "mode": "segments", "window": "TypeError", "window2": "TypeError",
		"removeNaN": "TypeError", "negDur": "TypeError", "badData": "TypeError", "errorEvent": true, "stateAfterError": "ended",
		"removedBuffered": "InvalidStateError",
	}
	for k, v := range want {
		if out[k] != v {
			t.Errorf("%s = %v (%T), want %v", k, out[k], out[k], v)
		}
	}
}

func TestMSEIsTypeSupportedMatrix(t *testing.T) {
	tab := newMediaTab(t)
	// the strings YouTube, Twitch, dash.js, hls.js and Shaka probe
	yes := []string{
		`video/mp4; codecs="avc1.42E01E"`, `video/mp4; codecs="avc1.4d401f"`, `video/mp4; codecs="avc1.640028"`, `video/mp4; codecs="avc3.640028"`,
		`video/mp4; codecs="avc1.64001F, mp4a.40.2"`, `video/mp4;codecs=avc1.42E01E`, `video/mp4; codecs='avc1.42E01E'`,
		`audio/mp4; codecs="mp4a.40.2"`, `audio/mp4; codecs="mp4a.40.5"`, `audio/mp4;codecs=mp4a.40.2`, `audio/mp4; codecs="opus"`,
		`video/webm; codecs="vp9"`, `video/webm; codecs="vp09.00.10.08"`, `video/webm; codecs="vp09.02.51.10"`, `video/webm; codecs="vp8"`,
		`video/webm; codecs="vp9, opus"`, `video/webm; codecs="vp8, vorbis"`, `audio/webm; codecs="opus"`, `audio/webm; codecs="vorbis"`,
		`video/webm; codecs="av01.0.05M.08"`, `video/mp4; codecs="av01.0.05M.08"`, `video/mp4; codecs="av01.0.04M.08, mp4a.40.2"`,
		`video/mp4; codecs="hvc1.1.6.L93.B0"`, `video/mp4; codecs="hev1.1.6.L93.B0"`, `video/mp4; codecs="hvc1.2.4.L120.B0"`,
		`video/mp4; codecs="avc1.640028"; width=1920; height=1080; framerate=30`,
	}
	no := []string{
		``, `video/mp4`, `video/webm`, `video/x-flv; codecs="avc1.42E01E"`, `video/mp2t; codecs="avc1.42E01E"`, `audio/mpeg`, `audio/aac`,
		`video/mp4; codecs="ac-3"`, `audio/mp4; codecs="ec-3"`, `audio/mp4; codecs="mp4a.40.2, ac-3"`, `video/mp4; codecs="dvh1.05.01"`,
		`video/webm; codecs="avc1.42E01E"`, `video/mp4; codecs="vp8"`, `video/mp4; codecs="theora"`, `audio/webm; codecs="mp4a.40.2"`,
		`video/mp4; codecs="avc1.42E01E, vp09.00.10.08"`, `audio/mp4; codecs="mp4a.40.2, opus"`, `application/x-mpegURL`, `video/mp4; codecs=`, `text/vtt`,
	}
	for _, m := range yes {
		var got bool
		b, _ := json.Marshal(m)
		evalJSON(t, tab, "MediaSource.isTypeSupported("+string(b)+")", &got)
		if !got {
			t.Errorf("isTypeSupported(%s) = false, want true", m)
		}
	}
	for _, m := range no {
		var got bool
		b, _ := json.Marshal(m)
		evalJSON(t, tab, "MediaSource.isTypeSupported("+string(b)+")", &got)
		if got {
			t.Errorf("isTypeSupported(%s) = true, want false", m)
		}
	}
}

func TestMediaCapabilitiesDecodingInfo(t *testing.T) {
	tab := newMediaTab(t)
	var r map[string]any
	evalJSON(t, tab, `navigator.mediaCapabilities.decodingInfo({type:'media-source', video:{contentType:'video/mp4; codecs="avc1.4d401f"', width:1280, height:720, framerate:30, bitrate:2000000}, audio:{contentType:'audio/mp4; codecs="mp4a.40.2"'}}).then(function(x){return x})`, &r)
	if r["supported"] != true || r["smooth"] != true || r["powerEfficient"] != false {
		t.Errorf("720p30: %v", r)
	}
	// H.264 1080p30 is smooth since frame threading (it was not before), 1080p60 is not claimed
	evalJSON(t, tab, `navigator.mediaCapabilities.decodingInfo({type:'media-source', video:{contentType:'video/mp4; codecs="avc1.640028"', width:1920, height:1080, framerate:30, bitrate:5000000}}).then(function(x){return x})`, &r)
	if r["supported"] != true || r["smooth"] != true {
		t.Errorf("H.264 1080p30: %v", r)
	}
	evalJSON(t, tab, `navigator.mediaCapabilities.decodingInfo({type:'media-source', video:{contentType:'video/mp4; codecs="avc1.640028"', width:1920, height:1080, framerate:60, bitrate:8000000}}).then(function(x){return x})`, &r)
	if r["supported"] != true || r["smooth"] != false {
		t.Errorf("H.264 1080p60 should be supported but not claimed smooth: %v", r)
	}
	evalJSON(t, tab, `navigator.mediaCapabilities.decodingInfo({type:'media-source', video:{contentType:'video/webm; codecs="vp09.00.10.08"', width:3840, height:2160, framerate:60, bitrate:20000000}}).then(function(x){return x})`, &r)
	if r["supported"] != true || r["smooth"] != false {
		t.Errorf("2160p60 should be supported but not smooth: %v", r)
	}
	evalJSON(t, tab, `navigator.mediaCapabilities.decodingInfo({type:'media-source', video:{contentType:'video/mp4; codecs="avc1.4d401f"', width:1280, height:720, framerate:30, bitrate:2000000}, audio:{contentType:'audio/mp4; codecs="ac-3"'}}).then(function(x){return x})`, &r)
	if r["supported"] != false {
		t.Errorf("AC-3: %v", r)
	}
	evalJSON(t, tab, `navigator.mediaCapabilities.decodingInfo({type:'media-source', video:{contentType:'video/mp4; codecs="avc1.4d401f"', width:1280, height:720, framerate:30, bitrate:2000000}, keySystemConfiguration:{keySystem:'com.widevine.alpha'}}).then(function(x){return x})`, &r)
	if r["supported"] != false {
		t.Errorf("EME must report unsupported: %v", r)
	}
	var name string
	evalJSON(t, tab, `navigator.mediaCapabilities.decodingInfo({type:'bogus'}).then(function(){return 'ok'},function(e){return e.name})`, &name)
	if name != "TypeError" {
		t.Errorf("invalid config: %q", name)
	}
}

var _ = image.Rect
