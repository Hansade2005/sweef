package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/headless"
)

// gatedSite serves a page whose blocking script is held until release is closed.
func gatedSite(t *testing.T) (srv *httptest.Server, release func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Gated</title></head><body style="margin:0">
<div style="height:200px;background:#2f9e44"></div><script src="/gate.js"></script>
<div style="height:200px;background:#2255cc"></div><script>document.title = 'Gated ' + (window.gateOpen ? 'open' : 'closed');</script></body></html>`)
	})
	mux.HandleFunc("/gate.js", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
		case <-time.After(20 * time.Second):
		}
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "window.gateOpen = true;")
	})
	srv = httptest.NewServer(mux)
	rel := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(func() { rel(); srv.Close() })
	return srv, rel
}

// The window shows the page as soon as its HTML is in: a blocking script that is still downloading neither holds
// the first frame nor the input, and the page finishes (title set by the late inline script) when the script arrives.
func TestPageIsShownBeforeABlockingScriptArrives(t *testing.T) {
	srv, release := gatedSite(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{"about:blank"}}})
	h.waitFor("initial tab loaded", func() bool { return h.tab().URL == "about:blank" && !h.tab().Loading })
	h.idle()
	h.key("ctrl+l")
	h.typeText(srv.URL + "/")
	start := time.Now()
	for _, e := range headless.KeyEvents("Enter") { // not h.key: that waits for the app to go idle, which a held load never does
		h.win.Push(e)
	}
	h.waitFor("first frame while the script is held", func() bool {
		ok := near(h.px(40, vpTop+50), "#2f9e44")
		if !ok && time.Since(start) > 5*time.Second {
			t.Logf("still waiting: url=%q loading=%v title=%q px=%v pending=%d", h.tab().URL, h.tab().Loading, h.tab().Title, h.px(40, vpTop+50), h.app.pending.Load())
			time.Sleep(time.Second)
		}
		return ok
	})
	if !h.tab().Loading {
		t.Fatal("the tab must still show it is loading while the script is outstanding")
	}
	if h.tab().Title == "Gated closed" || h.tab().Title == "Gated open" {
		t.Fatalf("the late inline script ran before the held script arrived: %q", h.tab().Title)
	}
	// the tab's worker is not parked on the held script: an op queued now runs at once (input, scroll and paint
	// requests reach the page while it loads)
	t0 := time.Now()
	var title string
	h.pageEval(`document.title`, &title)
	if d := time.Since(t0); d > 400*time.Millisecond {
		t.Fatalf("an operation on the loading tab waited %v behind the held script", d)
	}
	release()
	h.waitTitle("Gated open")
	h.waitFor("load finished", func() bool { return !h.tab().Loading })
	h.wantPx(40, vpTop+250, "#2255cc")
}

// The agent drives tabs like the CLI does: it gets the settled page, never the first frame.
func TestAgentNavigateWaitsForTheSettledPage(t *testing.T) {
	srv, release := gatedSite(t)
	h := newHarness(t, hopts{cfg: Config{}})
	h.idle()
	id := h.app.model.Active(h.app.winID)
	go func() { time.Sleep(600 * time.Millisecond); release() }()
	t0 := time.Now()
	if err := h.app.AgentNavigate(context.Background(), id, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < 500*time.Millisecond {
		t.Fatalf("AgentNavigate returned after %v, before the page's script arrived", d)
	}
	var got string
	h.pageEval(`document.title + '|' + window.gateOpen`, &got)
	if got != "Gated open|true" {
		t.Fatalf("page state after AgentNavigate: %q", got)
	}
}

// about:blank is an empty document, not the offline page.
func TestAboutBlankInTheOmnibox(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	h.key("ctrl+l")
	h.typeText("about:blank")
	h.key("Enter")
	h.waitFor("about:blank", func() bool { return h.tab().URL == "about:blank" && !h.tab().Loading })
	var got string
	h.pageEval(`location.href + '|' + document.body.innerText.trim() + '|' + document.documentElement.outerHTML.length`, &got)
	if len(got) < 12 || got[:12] != "about:blank|" || got[12] != '|' {
		t.Fatalf("about:blank shows content: %q", got)
	}
	if h.app.activePage().loadErr.Load() != nil {
		if s, _ := h.app.activePage().loadErr.Load().(string); s != "" {
			t.Fatalf("load error %q", s)
		}
	}
}

// After a progressive load with scripts, timers and images has finished, the app is idle again: no timer, no
// frame, no CPU (the load driver and the image relayout timers end with the load).
func TestIdleAfterAProgressiveLoad(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("rusage")
	}
	var png bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for i := range im.Pix {
		im.Pix[i] = 200
	}
	_ = pngEncode(&png, im)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Idle</title><body style="margin:0"><div style="height:100px;background:#d33a3a"></div>
<img src="/i.png"><img src="/j.png"><script src="/a.js"></script><script src="/b.js" defer></script>`)
	})
	mux.HandleFunc("/i.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(png.Bytes())
	})
	mux.HandleFunc("/j.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(png.Bytes())
	})
	mux.HandleFunc("/a.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "setTimeout(function(){ document.title = 'Idle 2'; }, 120);")
	})
	mux.HandleFunc("/b.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "window.b = 1;")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Idle 2")
	h.waitFor("loaded", func() bool { return !h.tab().Loading })
	h.idle()
	time.Sleep(700 * time.Millisecond) // image relayouts and the last repaint
	if h.animating() {
		t.Fatal("animation timer left running after the load")
	}
	if h.app.pending.Load() != 0 {
		t.Fatalf("worker ops still pending after the load: %d", h.app.pending.Load())
	}
	c0 := processCPU()
	presents := h.win.Presents()
	time.Sleep(2 * time.Second)
	cpu := processCPU() - c0
	if h.win.Presents() != presents {
		t.Fatal("frames presented while idle after the load")
	}
	limit := 100 * time.Millisecond
	if raceEnabled {
		limit *= 4
	}
	if cpu > limit {
		t.Fatalf("idle CPU %v over 2s after a load", cpu)
	}
}

func pngEncode(w io.Writer, im image.Image) error { return png.Encode(w, im) }
