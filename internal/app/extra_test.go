package app

import (
	"fmt"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTabDiscardAndReload(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/", srv.URL + "/two", srv.URL + "/tall"}}})
	h.waitFor("loaded", func() bool {
		for _, id := range h.tabIDs() {
			if tb, _ := h.app.model.Tab(id); tb.Loading || tb.Title == "New Tab" {
				return false
			}
		}
		return len(h.tabIDs()) == 3
	})
	h.idle()
	engineTabs := len(h.app.eng.ListTabs())
	h.loop(func() { h.app.discardOlderThan(0) })
	h.idle()
	var disc int
	for _, id := range h.tabIDs() {
		if tb, _ := h.app.model.Tab(id); tb.Discarded {
			disc++
		}
	}
	if disc != 2 {
		t.Fatalf("discarded %d tabs, want the 2 background ones", disc)
	}
	h.waitFor("engine tabs freed", func() bool { return len(h.app.eng.ListTabs()) <= engineTabs-2 })
	// the discarded tab keeps its title/url in the strip and comes back on demand
	h.key("ctrl+3")
	h.waitTitle("Tall")
	h.waitFor("reloaded", func() bool { return !h.tab().Loading })
	h.idle()
	h.wantPx(40, vpTop+50, "#2255cc")
	if h.tab().Discarded {
		t.Fatal("still marked discarded")
	}
}

func TestTitleAndURLChangesFromPageScripts(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/pushed" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Before</title><div style="height:50px;background:#f2c94c"></div><script>
setTimeout(function(){ document.title = 'After'; }, 150);
setTimeout(function(){ history.pushState({}, '', '/pushed'); }, 300);
</script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("After")
	h.waitFor("pushState url", func() bool { return h.tab().URL == srv.URL+"/pushed" })
	if tb := h.tab(); !tb.CanBack {
		t.Fatalf("pushState should add a history entry: %+v", tb)
	}
	// title changes reach the history store too
	if e, ok := h.app.prof.History.Get(srv.URL + "/pushed"); !ok || e.Title != "After" {
		t.Fatalf("history title %+v ok=%v", e, ok)
	}
}

func TestFaviconFetchedAndCached(t *testing.T) {
	// a 1x1 PNG
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89,
		0, 0, 0, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0xf8, 0xcf, 0xc0, 0xf0, 0x1f, 0, 0x05, 0, 0x01, 0xff, 0x89, 0x99, 0x3d, 0x1d, 0, 0, 0, 0, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82}
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `<!doctype html><title>Icon</title><link rel="icon" href="/icon.png"><p>x</p>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitFor("favicon", func() bool { return len(h.tab().Favicon) > 20 })
	if f := h.tab().Favicon; f[:22] != "data:image/png;base64," {
		t.Fatalf("favicon %q", f[:30])
	}
	// reload: served from the cache
	h.app.post(func() { h.app.reload(h.app.model.Active(h.app.winID), false) })
	h.waitFor("favicon again", func() bool { return len(h.tab().Favicon) > 20 && !h.tab().Loading })
	h.idle()
	if hits != 1 {
		t.Fatalf("icon fetched %d times", hits)
	}
	// it is part of the state sent to the chrome page
	found := false
	for _, x := range h.stateTabs() {
		if m := x.(map[string]any); m["favicon"] != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("favicon missing from the state snapshot")
	}
}

func TestICOtoPNG(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nXXXXXXXX")
	ico := []byte{0, 0, 1, 0, 2, 0}
	// entry 1: 16px bmp-ish (ignored), entry 2: 32px PNG
	e1 := []byte{16, 16, 0, 0, 1, 0, 32, 0, 4, 0, 0, 0, 38, 0, 0, 0}
	e2 := []byte{32, 32, 0, 0, 1, 0, 32, 0, byte(len(png)), 0, 0, 0, 42, 0, 0, 0}
	ico = append(ico, e1...)
	ico = append(ico, e2...)
	ico = append(ico, 1, 2, 3, 4)
	ico = append(ico, png...)
	got, ok := icoToPNG(ico)
	if !ok || string(got) != string(png) {
		t.Fatalf("icoToPNG: %v %q", ok, got)
	}
	if _, ok := icoToPNG([]byte("not an ico at all, long enough though")); ok {
		t.Fatal("garbage accepted")
	}
}

// What the chrome draws over the viewport belongs to the chrome: the page only
// gets the pointer where the chrome is transparent.
func TestChromeOverPageGetsPointerEvents(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	h.app.prof.History.Visit(srv.URL+"/blank", "Blank Target", false)
	h.key("ctrl+l")
	h.typeText(srv.URL + "/bla")
	h.waitFor("dropdown", func() bool { return h.app.comp.ChromeAlphaAt(130, 90) == 255 })
	h.idle()
	// beside the dropdown a click reaches the page ...
	if tg := h.targetAt(700, 90); tg != "page" {
		t.Fatalf("pointer beside the dropdown goes to %q", tg)
	}
	// ... on the dropdown it belongs to the chrome
	if tg := h.targetAt(130, 90); tg != "chrome" {
		t.Fatalf("pointer on the dropdown goes to %q", tg)
	}
}

func TestScrimInChromeCapturesThePage(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	if tg := h.targetAt(500, 300); tg != "page" {
		t.Fatalf("without a scrim: %q", tg)
	}
	// a transparent full-window click-catcher, like the one behind a popover
	h.chromeEval(`(function(){var s=document.createElement('div');s.id='scrim';s.style.cssText='position:fixed;left:0;top:0;right:0;bottom:0';
s.addEventListener('mousedown',function(){window.__scrimHit=1});document.body.appendChild(s);return 1})()`, nil)
	h.waitFor("probe sees the scrim", func() bool { return h.app.holeBusy.Load() })
	if tg := h.targetAt(500, 300); tg != "chrome" {
		t.Fatalf("with a scrim: %q", tg)
	}
	h.click(500, 300)
	var hit any
	h.chromeEval(`window.__scrimHit`, &hit)
	if hit == nil {
		t.Fatal("the scrim never saw the click")
	}
	// the click must not have reached the page
	var clicks any
	h.pageEval(`window.__pageClicked`, &clicks)
	if clicks != nil {
		t.Fatal("the page received a click meant for the scrim")
	}
	h.chromeEval(`document.getElementById('scrim').remove(); 1`, nil)
	h.waitFor("probe clears", func() bool { return !h.app.holeBusy.Load() })
	h.idle()
}

func TestOverlayAgentSurface(t *testing.T) {
	srv := pageServer(t)
	fake := NewFakeAgent()
	fake.Delay = 50 * 1e6 // 50ms between events
	h := newHarness(t, hopts{cfg: Config{Agent: fake, InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	h.app.prof.Sites.SetAgent(srv.URL, store.VisAllowed)
	if h.ovlVisible() {
		t.Fatal("overlay visible before any agent run")
	}
	// the overlay costs nothing while off: nothing painted, nothing blended
	if h.app.comp.Stats().Paint["overlay"].N != 0 {
		t.Fatal("overlay painted while off")
	}
	h.uiPost("agent.send", map[string]any{"text": "do the thing", "mode": "agent"})
	h.waitFor("overlay acting", func() bool { return h.ovlVisible() })
	h.waitFor("overlay pixels", func() bool { return h.px(10, vpTop+10) != h.px(10, vpTop+100) || near(h.px(2, vpTop+100), "#ff2d95") })
	h.idle()
	// the glow border of the placeholder overlay is drawn over the page, which stays visible inside it
	h.wantPx(2, vpTop+100, "#ff2d95")
	h.wantPx(500, vpTop+100, "#2f9e44")
	// the pill takes mouse events, the rest of the window does not
	rects := func() (r []rect) { h.loop(func() { r = h.app.ovl.rects }); return }
	h.waitFor("hit rects", func() bool { return len(rects()) == 1 })
	pill := rects()[0]
	if tg := h.targetAt(pill.X+pill.W/2, pill.Y+pill.H/2); tg != "overlay" {
		t.Fatalf("pill: %q", tg)
	}
	if tg := h.targetAt(500, vpTop+200); tg != "page" {
		t.Fatalf("outside the pill the page must get the pointer, got %q", tg)
	}
	// the run finishes: "done" shows, then the overlay goes away on stop
	h.waitFor("done", func() bool { var s string; h.loop(func() { s = h.app.ovl.state }); return s == "done" })
	h.uiPost("agent.stop", nil)
	h.waitFor("stopped", func() bool { var s string; h.loop(func() { s = h.app.ovl.state }); return s == "stopped" })
	h.app.post(func() { h.app.setOverlay("off", "", "") })
	h.waitFor("overlay off", func() bool { return !h.ovlVisible() })
	h.idle()
	h.wantPx(2, vpTop+100, "#2f9e44")
}
