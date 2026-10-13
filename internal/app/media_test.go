package app

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/audioout"
	"github.com/sweefbrowser/sweef/internal/shell"
)

func peakAfter(sink *audioout.TestSink, from int) float32 {
	rec := sink.Recorded()
	var pk float32
	for i := from * 2; i < len(rec); i++ {
		v := rec[i]
		if v < 0 {
			v = -v
		}
		if v > pk {
			pk = v
		}
	}
	return pk
}

// A video played through real input events in the desktop app: the click is the user gesture that lets
// sound start, frames reach the window, the tab strip learns that the tab is audible, and the tab mute
// silences the output without stopping the video.
func TestVideoSoundPictureAndTabMute(t *testing.T) {
	out, sink := audioout.NewRealtimeTestOutput()
	audioout.SetDefault(out)
	t.Cleanup(func() { audioout.SetDefault(nil) })

	clip, err := os.ReadFile("../engine/testdata/media/clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Player</title></head><body style="margin:0;background:#fff">
<video id="v" src="/clip.mp4" width="320" height="240" loop style="display:block"></video>
<button id="b" style="display:block;width:200px;height:40px">play</button>
<script>document.getElementById('b').addEventListener('click',function(){document.getElementById('v').play()})</script>
</body></html>`)
	})
	mux.HandleFunc("/clip.mp4", func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "clip.mp4", time.Time{}, bytes.NewReader(clip))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitFor("page loaded", func() bool { return h.tab().Title == "Player" && !h.tab().Loading })
	h.idle()
	h.waitFor("video ready", func() bool {
		var rs float64
		h.pageEval("document.getElementById('v').readyState", &rs)
		return rs >= 3
	})
	if h.tab().Audio {
		t.Fatal("tab is audible before anything plays")
	}
	// the paused video already shows its first picture
	h.idle()
	if c := h.px(160, vpTop+120); c.R == 255 && c.G == 255 && c.B == 255 {
		t.Error("the paused video shows nothing in the window")
	}

	// a real click on the button is the gesture that lets play() start sound; the app never goes idle
	// while a video plays, so nothing below waits for idle
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: 100, Y: vpTop + 260})
	h.win.Push(shell.Event{Kind: shell.EvMouseDown, X: 100, Y: vpTop + 260})
	h.win.Push(shell.Event{Kind: shell.EvMouseUp, X: 100, Y: vpTop + 260})
	h.waitFor("sound at the audio output", func() bool { return peakAfter(sink, 0) > 0.05 })
	h.waitFor("tab marked audible", func() bool { return h.tab().Audio })
	if dir := os.Getenv("SWEEF_SHOTS"); dir != "" {
		_ = h.win.SavePNG(dir + "/video.png")
	}

	// pictures reach the window: the area under the video changes while it plays
	// (the test clip carries a running counter inside a box on the right)
	seen := map[uint64]bool{}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(seen) < 4 {
		var sum uint64
		for y := 176; y < 224; y += 2 {
			for x := 260; x < 282; x += 2 {
				c := h.px(x, vpTop+y-80)
				sum = sum*131 + uint64(c.R) + 3*uint64(c.G) + 7*uint64(c.B)
			}
		}
		seen[sum] = true
		time.Sleep(30 * time.Millisecond)
	}
	if len(seen) < 3 {
		t.Errorf("the video area of the window did not change while playing: %d distinct pictures", len(seen))
	}

	// mute the tab through the chrome message the speaker icon sends
	id := h.tab().ID
	h.chromeEval("sweef.post('tab.mute',{id:"+jsString(id)+"}); 1", nil)
	h.waitFor("tab muted", func() bool { return h.tab().Muted })
	time.Sleep(200 * time.Millisecond)
	from := sink.Frames()
	time.Sleep(600 * time.Millisecond)
	if pk := peakAfter(sink, from); pk > 0.001 {
		t.Errorf("muted tab still plays: peak %v", pk)
	}
	var paused bool
	h.pageEval("document.getElementById('v').paused", &paused)
	if paused {
		t.Error("muting the tab must not pause the video")
	}
	// unmute again
	h.chromeEval("sweef.post('tab.mute',{id:"+jsString(id)+"}); 1", nil)
	h.waitFor("tab unmuted", func() bool { return !h.tab().Muted })
	time.Sleep(300 * time.Millisecond)
	from = sink.Frames()
	time.Sleep(500 * time.Millisecond)
	if pk := peakAfter(sink, from); pk < 0.05 {
		t.Errorf("unmuted tab is silent: peak %v", pk)
	}
}
