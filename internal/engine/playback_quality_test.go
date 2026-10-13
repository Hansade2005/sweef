package engine

// Playback quality: a clip is played through the whole page path (native player, decoder, picture delivery)
// and the dropped-frame count, the interval between delivered pictures, how late pictures were at the
// player's clock and the process CPU use are measured.
//
//	TestPlaybackNoDropsWithBFrames   a short B-frame clip must play without dropped frames (always runs)
//	TestPlaybackQuality              a long measurement of any clip (docs/media-streaming.md):
//	  SWEEF_BENCH_VIDEO=/path/clip.mp4 SWEEF_BENCH_SECONDS=20 go test -run TestPlaybackQuality -v ./internal/engine
//
// The clip is served over HTTP with Range support and played muted (muted video may start without a
// gesture), looping, so it can be shorter than the run.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

type playbackReport struct {
	W, H           float64
	Wall, CPU      time.Duration
	Total, Dropped float64 // frames reported by getVideoPlaybackQuality() during the run
	Pictures       int     // pictures actually delivered to the page
	GapMS          []float64
	LateMS         []float64
}

func quantile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[int(p*float64(len(v)-1))]
}

// playClip plays the file for secs seconds and reports what happened.
func playClip(t *testing.T, path string, secs float64) playbackReport {
	t.Helper()
	useRecordingSink(t)
	mux := http.NewServeMux()
	ext := filepath.Ext(path)
	mux.HandleFunc("/clip"+ext, func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer f.Close()
		st, _ := f.Stat()
		w.Header().Set("Content-Type", "video/mp4")
		if ext == ".webm" {
			w.Header().Set("Content-Type", "video/webm")
		}
		http.ServeContent(w, r, "clip", st.ModTime(), f)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><body>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	get := recordFrames(t)
	var lmu sync.Mutex
	var late []float64
	lf := func(ms float64) { lmu.Lock(); late = append(late, ms); lmu.Unlock() }
	mediaLateHook.Store(&lf)
	defer mediaLateHook.Store(nil)
	o := DefaultOptions()
	o.SettleMs = 20
	e := New(o)
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if t.Failed() {
			var st string
			evalJSON(t, tab, "(function(v){return [v.readyState,v.networkState,v.error&&v.error.code,v.error&&v.error.message,v.currentSrc,v.duration].join(' ')})(document.getElementById('v'))", &st)
			t.Logf("element state: %s", st)
		}
	}()
	evalJSON(t, tab, "(function(){var v=document.createElement('video');v.id='v';v.width=640;v.height=360;v.muted=true;v.loop=true;v.preload='auto';v.src='/clip"+ext+"';document.body.appendChild(v);v.play().catch(function(){});return 1})()", nil)
	waitTrue(t, tab, "document.getElementById('v').readyState >= 3", 20*time.Second)
	waitTrue(t, tab, "document.getElementById('v').currentTime > 0.3", 20*time.Second)
	var r playbackReport
	r.W = jsNum(t, tab, "document.getElementById('v').videoWidth")
	r.H = jsNum(t, tab, "document.getElementById('v').videoHeight")

	base := len(get())
	lmu.Lock()
	late = late[:0]
	lmu.Unlock()
	qj := func() (total, dropped float64) {
		return jsNum(t, tab, "document.getElementById('v').getVideoPlaybackQuality().totalVideoFrames"),
			jsNum(t, tab, "document.getElementById('v').getVideoPlaybackQuality().droppedVideoFrames")
	}
	t0q, t0d := qj()
	c0, w0 := cpuTime(), time.Now()
	for time.Since(w0) < time.Duration(secs*float64(time.Second)) {
		tab.Settle(100 * time.Millisecond)
	}
	r.Wall = time.Since(w0)
	r.CPU = cpuTime() - c0
	t1q, t1d := qj()
	r.Total, r.Dropped = t1q-t0q, t1d-t0d
	fr := get()[base:]
	r.Pictures = len(fr)
	for i := 1; i < len(fr); i++ {
		r.GapMS = append(r.GapMS, float64(fr[i].at.Sub(fr[i-1].at).Microseconds())/1000)
	}
	sort.Float64s(r.GapMS)
	lmu.Lock()
	r.LateMS = append([]float64(nil), late...)
	lmu.Unlock()
	sort.Float64s(r.LateMS)
	return r
}

func TestPlaybackNoDropsWithBFrames(t *testing.T) {
	// 320x192 High profile with B-frames and a key frame every 24 frames: 30 fps, 0.8 s long, looping
	r := playClip(t, "testdata/media/h264_wave_320x192.mp4", 2.5)
	rate := 100 * r.Dropped / r.Total
	t.Logf("%.0fx%.0f: %.0f frames in %.1fs, %.0f dropped, %d pictures, interval ms median %.1f p99 %.1f, lateness ms p95 %.0f max %.0f",
		r.W, r.H, r.Total, r.Wall.Seconds(), r.Dropped, r.Pictures, quantile(r.GapMS, .5), quantile(r.GapMS, .99), quantile(r.LateMS, .95), quantile(r.LateMS, 1))
	if r.Total < 60 {
		t.Fatalf("only %.0f frames in %.1f s of playback (30 fps clip)", r.Total, r.Wall.Seconds())
	}
	// the loop restarts decoding from the first key frame once per pass; nothing else may be dropped
	if r.Dropped > 3 || rate > 5 {
		t.Errorf("%.0f of %.0f frames dropped (%.1f%%) on a clip a core decodes many times over", r.Dropped, r.Total, rate)
	}
	if m := quantile(r.LateMS, 1); m > 150 {
		t.Errorf("a picture was shown %.0f ms late", m)
	}
}

func TestPlaybackQuality(t *testing.T) {
	path := os.Getenv("SWEEF_BENCH_VIDEO")
	if path == "" {
		t.Skip("set SWEEF_BENCH_VIDEO to a video file")
	}
	secs := 20
	if n, _ := strconv.Atoi(os.Getenv("SWEEF_BENCH_SECONDS")); n > 0 {
		secs = n
	}
	r := playClip(t, path, float64(secs))
	rate := 0.0
	if r.Total > 0 {
		rate = 100 * r.Dropped / r.Total
	}
	t.Logf("%s %.0fx%.0f: %.1fs wall, %.0f frames reported (%.1f fps), %.0f dropped (%.2f%%), %d pictures delivered; "+
		"picture interval ms median %.1f p95 %.1f p99 %.1f max %.1f; picture lateness at the player clock ms median %.0f p95 %.0f max %.0f; CPU %.0f%% of one core (%.1fs CPU in %.1fs)",
		path, r.W, r.H, r.Wall.Seconds(), r.Total, r.Total/r.Wall.Seconds(), r.Dropped, rate, r.Pictures,
		quantile(r.GapMS, .5), quantile(r.GapMS, .95), quantile(r.GapMS, .99), quantile(r.GapMS, 1),
		quantile(r.LateMS, .5), quantile(r.LateMS, .95), quantile(r.LateMS, 1),
		100*r.CPU.Seconds()/r.Wall.Seconds(), r.CPU.Seconds(), r.Wall.Seconds())
}
