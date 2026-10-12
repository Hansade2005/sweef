package engine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/sweefbrowser/sweef/internal/media"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixtureResult is what testdata/media/fx.js leaves in window.__result.
type fixtureResult struct {
	Events [][]any        `json:"events"`
	X      map[string]any `json:"x"`
}

func mediaServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata/media")))
	t.Cleanup(srv.Close)
	return srv
}

var clickScenarios = map[string]bool{"seek_tu": true, "loop_events": true, "ended_replay": true, "play_pause_promise": true, "play_wav": true, "autoplay_gesture": true, "seek_playing": true, "loop": true, "rate": true, "pause_resume": true, "error_nosrc": true}

func runFixture(t *testing.T, srv *httptest.Server, name string) *fixtureResult {
	t.Helper()
	o := DefaultOptions()
	o.SettleMs = 30
	e := New(o)
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/fx.html?s=" + name)
	if err != nil {
		t.Fatal(err)
	}
	if clickScenarios[name] {
		tab.Settle(430 * time.Millisecond) // the Chromium reference clicked ~450 ms after load
		if err := tab.Click("#go"); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		tab.Settle(100 * time.Millisecond)
		v, _ := tab.Evaluate("window.__done")
		if string(v) == "true" {
			break
		}
	}
	raw, err := tab.Evaluate("JSON.stringify(window.__result)")
	if err != nil {
		t.Fatal(err)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("result: %s", raw)
	}
	var r fixtureResult
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func loadRef(t *testing.T, name string) *fixtureResult {
	b, err := os.ReadFile(filepath.Join("testdata/media/.ref", name+".json"))
	if err != nil {
		t.Skipf("no Chromium reference for %s: %v", name, err)
	}
	var r fixtureResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

// eventNames reduces a log to its order of event names, dropping the
// events whose count/timing legitimately differs (progress/suspend/stalled
// repeats, timeupdate unless asked) and collapsing consecutive duplicates.
func eventNames(ev [][]any, keepTU bool) []string {
	var out []string
	for _, e := range ev {
		n, _ := e[0].(string)
		switch n {
		case "progress", "suspend", "stalled":
			continue
		case "timeupdate":
			if !keepTU {
				continue
			}
		}
		if len(out) > 0 && out[len(out)-1] == n && (n == "volumechange" || n == "canplay" || n == "canplaythrough" || n == "durationchange") {
			// source switching repeats these; keep order only
		}
		out = append(out, n)
	}
	return out
}

func eventTime(ev [][]any, name string, nth int) float64 {
	c := 0
	for _, e := range ev {
		if e[0] == name {
			if c == nth {
				f, _ := e[1].(float64)
				return f
			}
			c++
		}
	}
	return -1
}

// Scenarios whose event ORDER must equal Chromium's.
var orderScenarios = []string{"mse_basic", "load_auto", "load_meta", "load_none", "play_wav", "seek", "seek_playing", "seek_tu", "seek_paused_tu", "rate_events", "ended_replay", "play_pause_promise", "loop_events", "pause_resume", "error_404", "error_garbage", "error_nosrc", "video_webm", "play_muted_video", "autoplay_blocked", "reload"}

func TestMediaEventOrderMatchesChromium(t *testing.T) {
	srv := mediaServer(t)
	for _, name := range orderScenarios {
		name := name
		t.Run(name, func(t *testing.T) {
			ref := loadRef(t, name)
			got := runFixture(t, srv, name)
			keepTU := strings.Contains(name, "_tu") || name == "loop_events"
			g, r := eventNames(got.Events, keepTU), eventNames(ref.Events, keepTU)
			if keepTU {
				g, r = squeezeTU(g), squeezeTU(r)
			}
			if name == "mse_basic" { // the final durationchange depends on the exact last-frame duration
				g, r = dropName(g, "durationchange"), dropName(r, "durationchange")
			}
			if strings.Join(g, " ") != strings.Join(r, " ") {
				t.Errorf("event order differs\n sweef:    %s\n chromium: %s", strings.Join(g, " "), strings.Join(r, " "))
			}
		})
	}
}

// squeezeTU collapses runs of timeupdate (cadence is checked separately).
func squeezeTU(in []string) []string {
	var out []string
	for _, s := range in {
		if s == "timeupdate" && len(out) > 0 && out[len(out)-1] == "timeupdate" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func closeTo(a, b any, tol float64) bool {
	fa, ok1 := a.(float64)
	fb, ok2 := b.(float64)
	if ok1 && ok2 {
		return math.Abs(fa-fb) <= tol
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func TestMediaKeyPropertiesMatchChromium(t *testing.T) {
	srv := mediaServer(t)
	cases := map[string][]string{
		"load_auto":          {"duration", "readyState", "networkState", "paused", "ended", "seeking", "currentTime", "preload", "src", "buffered", "seekable"},
		"load_meta":          {"duration", "networkState", "preload"},
		"load_none":          {"duration", "readyState", "networkState"},
		"play_muted_audio":   {"play"},
		"play_muted_video":   {"play", "paused", "readyState", "videoWidth", "videoHeight", "duration"},
		"autoplay_blocked":   {"paused", "readyState"},
		"seek":               {"seekRet", "seekingNow", "ctNow", "currentTime", "seeking"},
		"attrs":              {"volErr", "rateErr", "ctErr", "volume", "muted", "defaultMuted", "playbackRate", "defaultPlaybackRate", "preservesPitch", "controls", "loop", "autoplay", "crossOrigin", "preload", "networkState"},
		"error_404":          {"readyState", "networkState", "errCode", "errCtor", "play"},
		"error_garbage":      {"readyState", "networkState", "errCode"},
		"error_nosrc":        {"networkState", "readyState", "error"},
		"source_select":      {"currentSrc", "readyState", "networkState", "duration"},
		"reload":             {"readyState", "networkState", "duration", "src"},
		"video_webm":         {"videoWidth", "videoHeight", "duration", "rvfc", "rvfcMeta", "quality", "paused", "ended", "readyState", "ct"},
		"tracks":             {"tracksLen", "mode0", "kind", "label", "lang", "cues", "cue0", "trackReady"},
		"ranges":             {"buffered", "seekable", "playedLen", "idxErr"},
		"pause_resume":       {"ctAtPause", "ctAfterPause", "play2", "ctAfterResume"},
		"loop":               {"paused", "ended", "loop", "playbackRate", "ctMod"},
		"rate":               {"currentTime", "playbackRate", "defaultPlaybackRate"},
		"play_wav":           {"play", "currentTime", "ended", "paused", "duration", "played"},
		"ended_replay":       {"endedState"},
		"play_pause_promise": {"p1"},
		"misc_apis":          {"pip", "mediaKeys", "sink", "sinkId", "eme", "gum", "gumEmpty", "capTracks", "remote", "cps", "devices", "recorder", "voices", "vtt"},
		"mse_basic":          {"supported", "rs0", "rsOpen", "bytes", "rsEnded", "readyState", "videoWidth"},
	}
	for name, keys := range cases {
		name, keys := name, keys
		t.Run(name, func(t *testing.T) {
			ref := loadRef(t, name)
			got := runFixture(t, srv, name)
			for _, k := range keys {
				if !closeTo(got.X[k], ref.X[k], 0.01) {
					t.Errorf("%s.%s = %v (%T), Chromium %v", name, k, got.X[k], got.X[k], ref.X[k])
				}
			}
		})
	}
}

func TestMediaMSEDuration(t *testing.T) {
	srv := mediaServer(t)
	ref, got := loadRef(t, "mse_basic"), runFixture(t, srv, "mse_basic")
	for _, k := range []string{"dur", "duration"} {
		if math.Abs(got.X[k].(float64)-ref.X[k].(float64)) > 0.05 {
			t.Errorf("%s = %v, Chromium %v", k, got.X[k], ref.X[k])
		}
	}
	gb, rb := got.X["buffered"].([]any), ref.X["buffered"].([]any)
	if len(gb) != len(rb) || math.Abs(gb[0].([]any)[1].(float64)-rb[0].([]any)[1].(float64)) > 0.05 {
		t.Errorf("buffered %v vs Chromium %v", gb, rb)
	}
}

func TestMediaPlaybackClockAccuracy(t *testing.T) {
	srv := mediaServer(t)
	got := runFixture(t, srv, "play_wav")
	ref := loadRef(t, "play_wav")
	// elapsed wall time between play() and ended for 1.5 s of audio
	play, ended := eventTime(got.Events, "play", 0), eventTime(got.Events, "ended", 0)
	if play < 0 || ended < 0 {
		t.Fatalf("events: %v", eventNames(got.Events, true))
	}
	elapsed := ended - play
	rplay, rended := eventTime(ref.Events, "play", 0), eventTime(ref.Events, "ended", 0)
	t.Logf("sweef play->ended %.0f ms (Chromium %.0f ms) for 1500 ms of audio", elapsed, rended-rplay)
	if math.Abs(elapsed-1500) > 100 {
		t.Errorf("clock off: %v ms for 1500 ms", elapsed)
	}
	// timeupdate cadence ~250 ms
	gaps, _ := got.X["tuGaps"].([]any)
	if len(gaps) < 3 {
		t.Fatalf("too few timeupdates: %v", got.X)
	}
	for i, g := range gaps[:len(gaps)-1] {
		if f := g.(float64); f < 200 || f > 340 {
			t.Errorf("timeupdate gap %d = %v ms", i, f)
		}
	}
	if tu := got.X["timeupdates"].(float64); tu < 5 || tu > 8 {
		t.Errorf("timeupdates %v (Chromium %v)", tu, ref.X["timeupdates"])
	}
}

func TestMediaCanPlayTypeMatrix(t *testing.T) {
	// Recorded from Chromium 141 (no proprietary codecs); only entries whose
	// answer does not depend on H.264/AAC.
	want := map[string]string{
		`audio/wav`: "maybe", `audio/wav; codecs="1"`: "probably", `audio/wave`: "", `audio/x-wav`: "maybe", `audio/mpeg`: "probably", `audio/mp3`: "probably",
		`audio/mpeg; codecs="mp3"`: "probably", `audio/ogg`: "maybe", `audio/ogg; codecs="vorbis"`: "probably", `audio/ogg; codecs="opus"`: "probably",
		`audio/ogg; codecs="flac"`: "probably", `audio/ogg; codecs="theora"`: "", `video/ogg`: "maybe", `audio/webm`: "maybe", `audio/webm; codecs="opus"`: "probably",
		`audio/webm; codecs="vorbis"`: "probably", `video/webm`: "maybe", `video/webm; codecs="vp8"`: "probably", `video/webm; codecs="vp8, vorbis"`: "probably",
		`video/webm; codecs="vp9"`: "probably", `video/webm; codecs="vp09.00.10.08"`: "probably", `video/webm; codecs="av01.0.04M.08"`: "probably", `video/webm; codecs="vp9, opus"`: "probably",
		`video/mp4`: "maybe", `video/mp4; codecs="av01.0.04M.08"`: "probably", `video/mp4; codecs="vp09.00.10.08"`: "probably", `video/mp4; codecs="flac"`: "probably",
		`video/mp4; codecs="opus"`: "probably", `audio/mp4`: "maybe", `audio/mp4; codecs="flac"`: "probably", `audio/mp4; codecs="opus"`: "probably", `audio/mp4; codecs="mp3"`: "probably",
		`audio/mp4; codecs="mp4a.6B"`: "probably", `audio/flac`: "probably", `audio/x-flac`: "", `audio/mpegurl`: "", `application/vnd.apple.mpegurl`: "", `application/x-mpegURL`: "",
		`application/dash+xml`: "", `video/quicktime`: "", `video/x-matroska`: "", `video/x-msvideo`: "", `application/ogg`: "maybe", `audio/opus`: "", `audio/webm; codecs="vp8"`: "",
		`bogus/type`: "", `video/mp4; codecs="bogus"`: "", `video/webm; codecs="avc1.42E01E"`: "", ``: "", `VIDEO/MP4`: "maybe", `audio/mpeg; codecs="mp4a.40.2"`: "",
		`audio/wav; codecs="0"`: "", `audio/ogg; codecs="speex"`: "", `audio/ogg; codecs="vorbis, opus"`: "probably", `image/gif`: "", `audio/x-mp3`: "probably", `audio/mp3; codecs="mp3"`: "",
	}
	srv := mediaServer(t)
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/fx.html?s=none")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(want)
	raw, err := tab.Evaluate(`(function(){var w=` + string(b) + `,v=document.createElement('video'),bad=[];for(var k in w){var g=v.canPlayType(k);if(g!==w[k])bad.push(k+' => '+JSON.stringify(g)+' want '+JSON.stringify(w[k]));}return bad})()`)
	if err != nil {
		t.Fatal(err)
	}
	var bad []string
	json.Unmarshal(raw, &bad)
	for _, b := range bad {
		t.Error(b)
	}
}

func TestMediaFrameInBitmapRegistry(t *testing.T) {
	srv := mediaServer(t)
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/fx.html?s=none")
	if err != nil {
		t.Fatal(err)
	}
	_, err = tab.Evaluate(`(function(){var v=document.createElement('video');v.id='vid';v.muted=true;v.preload='auto';v.src='clip.webm';document.body.appendChild(v);})()`)
	if err != nil {
		t.Fatal(err)
	}
	var w, h int
	for i := 0; i < 600 && w == 0; i++ {
		tab.Settle(50 * time.Millisecond)
		raw, _ := tab.Evaluate("__swIdOf(document.getElementById('vid'))")
		var id int64
		json.Unmarshal(raw, &id)
		tab.mu.RLock()
		d := tab.dom
		tab.mu.RUnlock()
		if n := d.NodeByID(id); n != nil {
			if src := tab.BitmapSourceFor(n); src != nil && src.Bitmap() != nil {
				w, h = src.Size()
			}
		}
	}
	if w != 160 || h != 120 {
		t.Fatalf("video bitmap size %dx%d, want 160x120", w, h)
	}
	_ = exec.Command
}

func dropName(in []string, n string) []string {
	var out []string
	for _, s := range in {
		if s != n {
			out = append(out, s)
		}
	}
	return out
}

func videoFrame(t *testing.T, tab *Tab, sel string) (w, h int, sum uint64) {
	t.Helper()
	raw, _ := tab.Evaluate("__swIdOf(document.querySelector('" + sel + "'))")
	var id int64
	json.Unmarshal(raw, &id)
	tab.mu.RLock()
	d := tab.dom
	tab.mu.RUnlock()
	n := d.NodeByID(id)
	if n == nil {
		return
	}
	src := tab.BitmapSourceFor(n)
	if src == nil || src.Bitmap() == nil {
		return
	}
	w, h = src.Size()
	img := src.Bitmap()
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 3 {
		for x := b.Min.X; x < b.Max.X; x += 3 {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum = sum*131 + uint64(r>>8)*3 + uint64(g>>8)*5 + uint64(bl>>8)*7
		}
	}
	return
}

func waitFrame(t *testing.T, tab *Tab, sel string, notSum uint64) (int, int, uint64) {
	for i := 0; i < 120; i++ {
		tab.Settle(50 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		if w, h, s := videoFrame(t, tab, sel); w > 0 && s != notSum {
			return w, h, s
		}
	}
	return 0, 0, 0
}

func TestMediaH264NeedsFFmpegAndFramesFollowTime(t *testing.T) {
	srv := mediaServer(t)
	for _, withFF := range []bool{true, false} {
		withFF := withFF
		name := "ffmpeg"
		if !withFF {
			name = "no-ffmpeg"
		}
		t.Run(name, func(t *testing.T) {
			media.SetFFmpegEnabled(withFF)
			defer media.SetFFmpegEnabled(true)
			if withFF && media.FFmpegPath() == "" {
				t.Skip("no ffmpeg")
			}
			e := New(DefaultOptions())
			defer e.Close()
			tab, err := e.NewTab(srv.URL + "/fx.html?s=none")
			if err != nil {
				t.Fatal(err)
			}
			var cp []string
			evalJSON(t, tab, `(function(){var v=document.createElement('video');return [v.canPlayType('video/mp4; codecs="avc1.64001e, mp4a.40.2"'),v.canPlayType('audio/mp4; codecs="mp4a.40.2"'),v.canPlayType('video/webm; codecs="vp8"')]})()`, &cp)
			wantH264 := ""
			if withFF || media.HasNativeVideo("h264") { // AAC-LC itself is native
				wantH264 = "probably"
			}
			if cp[0] != wantH264 || cp[1] != "probably" || cp[2] != "probably" {
				t.Fatalf("canPlayType = %v", cp)
			}
			// MP4 (H.264+AAC)
			var res map[string]any
			evalJSON(t, tab, `new Promise(function(res){var v=document.createElement('video');v.id='m';v.muted=true;v.preload='auto';v.src='clip.mp4';document.body.appendChild(v);v.onerror=function(){res({err:v.error.code})};v.onloadedmetadata=function(){res({w:v.videoWidth,h:v.videoHeight,d:v.duration})};})`, &res)
			if !withFF && !media.HasNativeVideo("h264") {
				if res["err"] != float64(4) {
					t.Fatalf("H.264 without ffmpeg must fail with MEDIA_ERR_SRC_NOT_SUPPORTED, got %v", res)
				}
			} else {
				if res["w"] != float64(160) || res["d"].(float64) < 1.9 {
					t.Fatalf("mp4 metadata: %v", res)
				}
				w, h, s0 := waitFrame(t, tab, "#m", 0)
				if w != 160 || h != 120 {
					t.Fatalf("mp4 frame %dx%d", w, h)
				}
				evalJSON(t, tab, `(function(){document.getElementById('m').currentTime=1.5;return 1})()`, nil)
				_, _, s1 := waitFrame(t, tab, "#m", s0)
				if s1 == 0 || s1 == s0 {
					t.Fatalf("frame did not change after seeking to 1.5 s")
				}
			}
			// WebM VP8: decoded natively, frames follow currentTime with or without ffmpeg
			evalJSON(t, tab, `new Promise(function(res){var v=document.createElement('video');v.id='w';v.muted=true;v.preload='auto';v.src='clip.webm';document.body.appendChild(v);v.onloadeddata=function(){res(1)};v.onerror=function(){res(0)}})`, nil)
			w, h, s0 := waitFrame(t, tab, "#w", 0)
			if w != 160 || h != 120 {
				t.Fatalf("webm frame %dx%d", w, h)
			}
			evalJSON(t, tab, `(function(){document.getElementById('w').currentTime=1.5;return 1})()`, nil)
			tab.Settle(300 * time.Millisecond)
			time.Sleep(300 * time.Millisecond)
			_, _, s1 := videoFrame(t, tab, "#w")
			changed := s1 != s0
			if !changed {
				t.Errorf("the VP8 frame must follow currentTime (native decoder, ffmpeg=%v)", withFF)
			}
		})
	}
}

func TestMSEVideoFrames(t *testing.T) {
	srv := mediaServer(t)
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/fx.html?s=none")
	if err != nil {
		t.Fatal(err)
	}
	evalJSON(t, tab, `new Promise(function(res){
	  var ms=new MediaSource(),v=document.createElement('video');v.id='ms';v.muted=true;document.body.appendChild(v);v.src=URL.createObjectURL(ms);
	  ms.addEventListener('sourceopen',function(){
	    var sb=ms.addSourceBuffer('video/webm; codecs="vp8, vorbis"'),x=new XMLHttpRequest();x.open('GET','clip.webm');x.responseType='arraybuffer';
	    x.onload=function(){sb.addEventListener('updateend',function(){ms.endOfStream();res(1)});sb.appendBuffer(x.response)};x.send();
	  });
	})`, nil)
	w, h, s0 := waitFrame(t, tab, "#ms", 0)
	if w != 160 || h != 120 {
		t.Fatalf("MSE video frame %dx%d", w, h)
	}
	evalJSON(t, tab, `(function(){document.getElementById('ms').currentTime=1.5;return 1})()`, nil)
	tab.Settle(300 * time.Millisecond)
	time.Sleep(300 * time.Millisecond)
	if _, _, s1 := videoFrame(t, tab, "#ms"); media.FFmpegPath() != "" && s1 == s0 {
		t.Errorf("MSE frame did not follow currentTime")
	}
}

// bigWAV builds a mono 16-bit 8 kHz WAV of the given length in seconds.
func bigWAV(seconds int) []byte {
	const rate = 8000
	n := rate * seconds
	b := make([]byte, 44+2*n)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+2*n))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], rate)
	binary.LittleEndian.PutUint32(b[28:], rate*2)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(2*n))
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(8000*math.Sin(2*math.Pi*300*float64(i)/rate))))
	}
	return b
}

func TestMediaRangeRequestsAndHonestBuffered(t *testing.T) {
	wav := bigWAV(600) // ~9.6 MB
	var mu sync.Mutex
	var ranges []string
	mux := http.NewServeMux()
	mux.HandleFunc("/big.wav", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		ranges = append(ranges, r.Header.Get("Range"))
		mu.Unlock()
		w.Header().Set("Content-Type", "audio/wav")
		http.ServeContent(w, r, "big.wav", time.Time{}, bytes.NewReader(wav))
	})
	mux.Handle("/", http.FileServer(http.Dir("testdata/media")))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/fx.html?s=none")
	if err != nil {
		t.Fatal(err)
	}
	var res map[string]any
	evalJSON(t, tab, `new Promise(function(res){var a=new Audio('big.wav');a.onloadeddata=function(){var b=a.buffered;res({dur:a.duration,end:b.length?b.end(b.length-1):0,n:b.length})}})`, &res)
	if res["dur"].(float64) != 600 {
		t.Fatalf("duration %v", res["dur"])
	}
	end := res["end"].(float64)
	if end <= 1 || end >= 100 {
		t.Fatalf("buffered must be a partial range (fetched by Range), got end=%v of 600", end)
	}
	mu.Lock()
	first := append([]string(nil), ranges...)
	mu.Unlock()
	if len(first) == 0 || !strings.HasPrefix(first[0], "bytes=0-") {
		t.Fatalf("first request must be a Range request, got %v", first)
	}
	// seeking far away fetches that region by byte offset, leaving the gap unbuffered
	var after map[string]any
	evalJSON(t, tab, `new Promise(function(res){var a=new Audio('big.wav');a.onloadeddata=function(){a.currentTime=450;a.onseeked=function(){var b=a.buffered,o=[];for(var i=0;i<b.length;i++)o.push([b.start(i),b.end(i)]);res({ranges:o,ct:a.currentTime})}}})`, &after)
	rs := after["ranges"].([]any)
	if len(rs) < 2 {
		t.Fatalf("expected two disjoint buffered ranges after a far seek, got %v", rs)
	}
	last := rs[len(rs)-1].([]any)
	if last[0].(float64) > 450 || last[1].(float64) < 450 {
		t.Fatalf("range around the seek target missing: %v", rs)
	}
	mu.Lock()
	defer mu.Unlock()
	sawFar := false
	for _, r := range ranges {
		var s int64
		if _, err := fmt.Sscanf(r, "bytes=%d-", &s); err == nil && s > int64(len(wav))/2 {
			sawFar = true
		}
	}
	if !sawFar {
		t.Fatalf("no Range request near byte offset of t=450: %v", ranges)
	}
}

// TestMediaNativeVideoWithoutFFmpeg checks that VP8, H.264 and HEVC (B-frames)
// decode with the pure-Go decoders and frames follow currentTime when
// ffmpeg is unavailable.
func TestMediaNativeVideoWithoutFFmpeg(t *testing.T) {
	srv := mediaServer(t)
	media.SetFFmpegEnabled(false)
	defer media.SetFFmpegEnabled(true)
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/fx.html?s=none")
	if err != nil {
		t.Fatal(err)
	}
	var cp []any
	evalJSON(t, tab, `(function(){var v=document.createElement('video');return [v.canPlayType('video/mp4; codecs="avc1.42E01E"'),v.canPlayType('video/mp4; codecs="avc1.640028"'),v.canPlayType('video/webm; codecs="vp8"'),v.canPlayType('video/mp4; codecs="hvc1.1.6.L93.B0"'),MediaSource.isTypeSupported('video/mp4; codecs="avc1.42E01E"')]})()`, &cp)
	if cp[0] != "probably" || cp[1] != "probably" || cp[2] != "probably" || cp[3] != "probably" || cp[4] != true {
		t.Fatalf("canPlayType = %v", cp)
	}
	for _, c := range []struct{ id, src string }{{"h", "h264_bframes.mp4"}, {"x", "hevc_bframes.mp4"}, {"w", "clip.webm"}} {
		evalJSON(t, tab, `new Promise(function(res){var v=document.createElement('video');v.id='`+c.id+`';v.muted=true;v.preload='auto';v.src='`+c.src+`';document.body.appendChild(v);v.onloadeddata=function(){res(1)};v.onerror=function(){res(0)}})`, nil)
		w, h, s0 := waitFrame(t, tab, "#"+c.id, 0)
		if w != 160 || h != 120 {
			t.Fatalf("%s frame %dx%d", c.src, w, h)
		}
		evalJSON(t, tab, `(function(){document.getElementById('`+c.id+`').currentTime=1.5;return 1})()`, nil)
		tab.Settle(300 * time.Millisecond)
		time.Sleep(300 * time.Millisecond)
		_, _, s1 := videoFrame(t, tab, "#"+c.id)
		if s1 == s0 {
			t.Errorf("%s: frame did not change after seeking", c.src)
		}
		evalJSON(t, tab, `(function(){document.getElementById('`+c.id+`').currentTime=0.2;return 1})()`, nil)
		tab.Settle(300 * time.Millisecond)
		time.Sleep(300 * time.Millisecond)
		_, _, s2 := videoFrame(t, tab, "#"+c.id)
		if s2 == s1 {
			t.Errorf("%s: frame did not change after seeking back", c.src)
		}
	}
}
