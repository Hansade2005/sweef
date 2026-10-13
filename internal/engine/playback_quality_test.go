package engine

// A long playback measurement, not a pass/fail test: it plays a clip through the whole page path (native
// player, decoder, picture delivery) for SWEEF_BENCH_SECONDS (default 20) and reports the dropped-frame
// rate, the interval between delivered pictures and the process CPU use.
//
//	SWEEF_BENCH_VIDEO=/path/clip.mp4 SWEEF_BENCH_SECONDS=20 go test -run TestPlaybackQuality -v ./internal/engine
//
// The clip is served over HTTP with Range support and played muted (muted video may start without a
// gesture), looping, so it can be shorter than the run.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func cpuTime() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
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
	useRecordingSink(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/clip", func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(path)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer f.Close()
		st, _ := f.Stat()
		w.Header().Set("Content-Type", "video/mp4")
		if len(path) > 5 && path[len(path)-5:] == ".webm" {
			w.Header().Set("Content-Type", "video/webm")
		}
		http.ServeContent(w, r, "clip", st.ModTime(), f)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><body><video id=v width=640 height=360 muted loop autoplay src="/clip"></video>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	get := recordFrames(t)
	o := DefaultOptions()
	o.SettleMs = 20
	e := New(o)
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	waitTrue(t, tab, "document.getElementById('v').readyState >= 3", 20*time.Second)
	evalJSON(t, tab, "document.getElementById('v').muted=true;document.getElementById('v').play().catch(function(){});1", nil)
	waitTrue(t, tab, "document.getElementById('v').currentTime > 0.3", 20*time.Second)
	var dims [2]float64
	dims[0] = jsNum(t, tab, "document.getElementById('v').videoWidth")
	dims[1] = jsNum(t, tab, "document.getElementById('v').videoHeight")

	base := get()
	qj := func() (total, dropped float64) {
		return jsNum(t, tab, "document.getElementById('v').getVideoPlaybackQuality().totalVideoFrames"),
			jsNum(t, tab, "document.getElementById('v').getVideoPlaybackQuality().droppedVideoFrames")
	}
	t0q, t0d := qj()
	c0, w0 := cpuTime(), time.Now()
	for time.Since(w0) < time.Duration(secs)*time.Second {
		tab.Settle(100 * time.Millisecond)
	}
	wall := time.Since(w0)
	cpu := cpuTime() - c0
	t1q, t1d := qj()
	fr := get()[len(base):]
	var gaps []float64
	for i := 1; i < len(fr); i++ {
		gaps = append(gaps, float64(fr[i].at.Sub(fr[i-1].at).Microseconds())/1000)
	}
	sort.Float64s(gaps)
	pct := func(p float64) float64 {
		if len(gaps) == 0 {
			return 0
		}
		return gaps[int(p*float64(len(gaps)-1))]
	}
	total, dropped := t1q-t0q, t1d-t0d
	rate := 0.0
	if total > 0 {
		rate = 100 * dropped / total
	}
	t.Logf("%s %.0fx%.0f: %.1fs wall, %.0f frames reported (%.1f fps), %.0f dropped (%.2f%%), %d pictures delivered; "+
		"picture interval ms median %.1f p95 %.1f p99 %.1f max %.1f; CPU %.0f%% of one core (%.1fs CPU in %.1fs)",
		path, dims[0], dims[1], wall.Seconds(), total, total/wall.Seconds(), dropped, rate, len(fr),
		pct(.5), pct(.95), pct(.99), pct(1), 100*cpu.Seconds()/wall.Seconds(), cpu.Seconds(), wall.Seconds())
}
