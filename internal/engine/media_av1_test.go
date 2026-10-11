package engine

import (
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/media"
)

// TestMediaAV1WithoutFFmpeg plays AV1 in MP4 and WebM through the pure-Go
// decoder with ffmpeg disabled.
func TestMediaAV1WithoutFFmpeg(t *testing.T) {
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
	evalJSON(t, tab, `(function(){var v=document.createElement('video');return [v.canPlayType('video/mp4; codecs="av01.0.04M.08"'),v.canPlayType('video/webm; codecs="av01.0.04M.08"')]})()`, &cp)
	if cp[0] != "probably" || cp[1] != "probably" {
		t.Fatalf("canPlayType = %v", cp)
	}
	for _, c := range []struct{ id, src string }{{"a", "av1.mp4"}, {"b", "av1.webm"}} {
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
	}
}
