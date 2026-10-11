package engine

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/fx.html?s=" + name)
	if err != nil {
		t.Fatal(err)
	}
	if clickScenarios[name] {
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
var orderScenarios = []string{"load_auto", "load_meta", "load_none", "play_wav", "seek", "seek_playing", "seek_tu", "seek_paused_tu", "rate_events", "ended_replay", "play_pause_promise", "loop_events", "pause_resume", "error_404", "error_garbage", "error_nosrc", "video_webm", "play_muted_video", "autoplay_blocked", "reload"}

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
	for i := 0; i < 80 && w == 0; i++ {
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
