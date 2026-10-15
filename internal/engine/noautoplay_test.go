package engine

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Agent mode (NoAutoplay): a playing video shows its first picture and its clock runs, but the rest of it is
// not decoded.
func TestNoAutoplayDecodesOnlyTheFirstPicture(t *testing.T) {
	useRecordingSink(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/clip.mp4", func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open("testdata/media/h264_wave_320x192.mp4")
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		defer f.Close()
		st, _ := f.Stat()
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "clip", st.ModTime(), f)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><body>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	get := recordFrames(t)
	o := DefaultOptions()
	o.SettleMs = 20
	o.NoAutoplay = true
	e := New(o)
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	evalJSON(t, tab, "(function(){var v=document.createElement('video');v.id='v';v.width=320;v.height=192;v.muted=true;v.loop=true;v.src='/clip.mp4';document.body.appendChild(v);v.play().catch(function(){});return 1})()", nil)
	waitTrue(t, tab, "document.getElementById('v').readyState >= 2", 20*time.Second)
	waitTrue(t, tab, "document.getElementById('v').currentTime > 0.3", 20*time.Second)
	base := len(get())
	for w0 := time.Now(); time.Since(w0) < 1500*time.Millisecond; {
		tab.Settle(100 * time.Millisecond)
	}
	// normal playback delivers ~45 pictures here; a loop restart (a seek) may decode its first picture again
	if n := len(get()) - base; n > 8 {
		t.Fatalf("%d pictures delivered during 1.5 s of agent-mode playback (want only the first picture of each loop)", n)
	}
	if ct := jsNum(t, tab, "document.getElementById('v').currentTime"); ct <= 0 {
		t.Fatalf("clock stopped: currentTime %v", ct)
	}
}
