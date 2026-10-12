package app

import (
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
)

const (
	vpTop    = 80 // placeholder chrome: viewport starts below the tab strip + toolbar
	toolbarY = 58
)

func TestThreeTabsOmniboxAndPixels(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/", srv.URL + "/two", srv.URL + "/tall"}}})
	h.waitFor("3 tabs loaded", func() bool {
		ids := h.tabIDs()
		if len(ids) != 3 {
			return false
		}
		for _, id := range ids {
			if tb, _ := h.app.model.Tab(id); tb.Loading || tb.Title == "New Tab" || tb.Title == "" {
				return false
			}
		}
		return true
	})
	h.idle()
	order := h.tabIDs()
	var titles []string
	for _, id := range order {
		tb, _ := h.app.model.Tab(id)
		titles = append(titles, tb.Title)
	}
	if strings.Join(titles, ",") != "Home Page,Page Two,Tall" {
		t.Fatalf("titles %v", titles)
	}
	// first tab is active: red banner at the top-left of the viewport, chrome colours above it
	if h.tab().Title != "Home Page" {
		t.Fatalf("active %q", h.tab().Title)
	}
	h.wantPx(40, vpTop+30, "#d33a3a")  // banner
	h.wantPx(40, vpTop+150, "#f2c94c") // first link block
	h.wantPx(800, 10, "#1c2030")       // tab strip
	h.wantPx(500, 44+36-8, "#3b4258")  // toolbar
	// ctrl+2 -> second tab: green
	h.key("ctrl+2")
	h.waitFor("green", func() bool { return near(h.px(40, vpTop+50), "#2f9e44") })
	// ctrl+tab -> third tab, whose top is blue
	h.key("ctrl+Tab")
	h.waitFor("blue", func() bool { return near(h.px(40, vpTop+50), "#2255cc") })
	h.key("ctrl+shift+Tab")
	h.waitFor("green again", func() bool { return near(h.px(40, vpTop+50), "#2f9e44") })
	h.key("ctrl+9") // last tab
	if h.tab().Title != "Tall" {
		t.Fatalf("ctrl+9 -> %q", h.tab().Title)
	}

	// type in the omnibox and navigate
	h.key("ctrl+l")
	h.typeText(srv.URL + "/two")
	// the chrome draws its suggestion list over the viewport: the page shows around it
	h.waitFor("dropdown ink", func() bool { return h.app.comp.ChromeAlphaAt(130, 90) == 255 })
	h.idle()
	h.wantPx(130, 90, "#232840")
	h.wantPx(700, 90, "#2255cc") // beside the dropdown the page (the Tall tab is active here) is still visible
	h.key("Enter")
	h.waitTitle("Page Two")
	if got := h.tab().URL; got != srv.URL+"/two" {
		t.Fatalf("url %q", got)
	}
	h.waitFor("dropdown closed", func() bool { return h.app.comp.ChromeAlphaAt(130, 90) == 0 })
	h.wantPx(40, vpTop+50, "#2f9e44")
	// the visit is in history, typed
	e, ok := h.app.prof.History.Get(srv.URL + "/two")
	if !ok || e.Typed != 1 {
		t.Fatalf("history entry %+v ok=%v", e, ok)
	}
}

func near(g color.RGBA, hex string) bool {
	w, _ := ParseColor(hex)
	return abs(int(g.R)-int(w.R)) <= 3 && abs(int(g.G)-int(w.G)) <= 3 && abs(int(g.B)-int(w.B)) <= 3
}

func TestLinkClickBackForwardReload(t *testing.T) {
	srv := pageServer(t)
	var hits int
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		hits++
		fmt.Fprintf(w, `<!doctype html><title>Hits %d</title><a href="/next" style="display:block;height:80px;background:#f2c94c">next</a>`, hits)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Next</title><div style="height:90px;background:#2f9e44"></div>`)
	})
	s2 := httptest.NewServer(mux)
	defer s2.Close()
	_ = srv
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{s2.URL + "/"}}})
	h.waitTitle("Hits 1")
	h.click(60, vpTop+30) // the yellow link
	h.waitTitle("Next")
	if h.tab().URL != s2.URL+"/next" || !h.tab().CanBack || h.tab().CanForward {
		t.Fatalf("after click: %+v", h.tab())
	}
	h.key("alt+ArrowLeft")
	h.waitTitle("Hits 2") // back re-fetches in this engine (no bfcache)
	if !h.tab().CanForward || h.tab().CanBack {
		t.Fatalf("after back: back=%v fwd=%v", h.tab().CanBack, h.tab().CanForward)
	}
	h.key("alt+ArrowRight")
	h.waitTitle("Next")
	// back twice in a row must stay on the first page (the engine's own GoBack would break this)
	h.key("alt+ArrowLeft")
	h.waitTitle("Hits 3")
	h.key("alt+ArrowLeft")
	h.idle()
	if h.tab().Title != "Hits 3" || h.tab().CanBack {
		t.Fatalf("back at the start: %+v", h.tab())
	}
	// reload re-fetches and keeps history length
	before := len(h.tab().History)
	h.key("ctrl+r")
	h.waitTitle("Hits 4")
	if len(h.tab().History) != before {
		t.Fatalf("reload changed history: %d -> %d", before, len(h.tab().History))
	}
	// toolbar buttons use the same path
	h.click(22, toolbarY) // back button disabled: no change
	h.key("F5")
	h.waitTitle("Hits 5")
}

func TestNewTabTargetBlankAndPopupGate(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	// target=_blank link opens a new foreground tab
	h.click(60, vpTop+120+60+30)
	h.waitFor("new tab", func() bool { return len(h.tabIDs()) == 2 })
	h.waitTitle("Blank Target")
	if h.app.model.Order(h.app.winID)[1] != h.app.model.Active(h.app.winID) {
		t.Fatal("new tab must be inserted next to the opener and activated")
	}
	// ctrl+click opens in the background
	h.key("ctrl+1")
	h.waitTitle("Home Page")
	h.win.Push(shell.Event{Kind: shell.EvMouseDown, X: 60, Y: vpTop + 150, Mods: shell.ModCtrl})
	h.win.Push(shell.Event{Kind: shell.EvMouseUp, X: 60, Y: vpTop + 150, Mods: shell.ModCtrl})
	h.waitFor("background tab", func() bool { return len(h.tabIDs()) == 3 })
	h.idle()
	if h.tab().Title != "Home Page" {
		t.Fatalf("ctrl+click stole focus: %q", h.tab().Title)
	}
	// window.open without a user gesture is blocked (a gesture counts for 2 s)
	time.Sleep(2200 * time.Millisecond)
	e := h.app.activePage()
	e.call(func() error { _, err := e.t.Evaluate(`window.open('/two')`); return err })
	h.idle()
	if len(h.tabIDs()) != 3 {
		t.Fatal("pop-up without a gesture was opened")
	}
	// ... with a gesture (a click just happened) it opens
	h.click(300, vpTop+500) // empty area counts as a user gesture
	e.call(func() error { _, err := e.t.Evaluate(`window.open('/two')`); return err })
	h.waitFor("popup tab", func() bool { return len(h.tabIDs()) == 4 })
	h.waitTitle("Page Two")
}

func TestErrorPage(t *testing.T) {
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{"http://127.0.0.1:1/"}}})
	h.waitFor("error page", func() bool { return strings.Contains(h.tab().Title, "can't be loaded") && !h.tab().Loading })
	h.idle()
	tb := h.tab()
	if tb.URL != "http://127.0.0.1:1/" || tb.Security.Level != "none" {
		t.Fatalf("error tab: %+v", tb)
	}
	// the error page is rendered (not blank white): it has dark text pixels
	dark := 0
	scr := h.win.Screen()
	for y := vpTop + 20; y < vpTop+300; y += 2 {
		for x := 0; x < 600; x += 2 {
			if c := scr.RGBAAt(x, y); c.R < 100 && c.G < 100 && c.B < 100 {
				dark++
			}
		}
	}
	if dark < 30 {
		t.Fatalf("error page looks blank (%d dark pixels)", dark)
	}
	// errors are not recorded in history
	if _, ok := h.app.prof.History.Get("http://127.0.0.1:1/"); ok {
		t.Fatal("failed load in history")
	}
}

func TestDownloadsFromNavigation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Dl</title><p>x</p>`)
	})
	mux.HandleFunc("/file.zip", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="report.zip"`)
		w.Header().Set("Content-Type", "application/zip")
		w.Write([]byte("PK-not-really-a-zip"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Dl")
	h.app.post(func() { h.app.navigateInput(h.app.model.Active(h.app.winID), srv.URL+"/file.zip") })
	h.waitFor("download recorded", func() bool { return len(h.app.prof.Downloads.List()) == 1 })
	it := h.app.prof.Downloads.List()[0]
	if it.Name != "report.zip" || it.State != "done" || it.Total != int64(len("PK-not-really-a-zip")) {
		t.Fatalf("download %+v", it)
	}
	h.idle()
	if h.tab().Title != "Dl" {
		t.Fatalf("a download must leave the page in place, got %q", h.tab().Title)
	}
}

func TestExternalSchemeOpensOS(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	h.app.post(func() { h.app.navigateInput(h.app.model.Active(h.app.winID), "mailto:someone@example.com") })
	h.idle()
	got := h.win.Opened()
	if len(got) != 1 || got[0] != "mailto:someone@example.com" {
		t.Fatalf("OpenURL calls: %v", got)
	}
	if h.tab().Title != "Home Page" {
		t.Fatal("external scheme must not navigate the tab")
	}
}

func TestViewSource(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	h.key("ctrl+u")
	h.waitFor("view-source tab", func() bool { return len(h.tabIDs()) == 2 && strings.HasPrefix(h.tab().URL, "view-source:") })
	h.idle()
	e := h.app.activePage()
	var txt string
	e.call(func() error { var err error; txt, err = e.t.Text("body"); return err })
	if !strings.Contains(txt, "<title>Page Two</title>") {
		t.Fatalf("view-source text: %q", txt)
	}
}

func TestInternalPagesServe(t *testing.T) {
	h := newHarness(t, hopts{})
	h.waitFor("newtab", func() bool { return h.tab().Title == "New Tab" && !h.tab().Loading })
	h.wantPx(500, vpTop+300, "#f4f6fb") // placeholder newtab background
	if h.tab().Security.Level != "internal" {
		t.Fatalf("security %+v", h.tab().Security)
	}
	h.app.prof.History.Visit("https://a.test/x", "A page", false)
	h.app.post(func() { h.app.newTab("sweef://history", false, true) })
	h.waitFor("history tab", func() bool { return h.tab().URL == "sweef://history" && !h.tab().Loading })
	e := h.app.activePage()
	var txt string
	e.call(func() error { var err error; txt, err = e.t.Text("body"); return err })
	if !strings.Contains(txt, "A page") {
		t.Fatalf("history page lacks injected data: %q", txt)
	}
	// unknown internal pages 404 instead of hanging
	h.app.post(func() { h.app.newTab("sweef://nosuchpage", false, true) })
	h.waitFor("404 tab", func() bool { return h.tab().URL == "sweef://nosuchpage" && !h.tab().Loading })
}

func TestWebPagesCannotUseTheChromeBridge(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Evil</title><script>
window.__t = typeof window.sweef;
try { __sweefPost('tab.new', {url:'https://evil.test/'}); } catch (e) {}
try { __sweefPost('settings.set', {key:'privacy.trackers', value:'off'}); } catch (e) {}
</script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Evil")
	e := h.app.activePage()
	var typ string
	e.call(func() error { return e.eval(`window.__t`, &typ) })
	if typ != "undefined" {
		t.Fatalf("window.sweef exists in a web page: %q", typ)
	}
	e.call(func() error { e.drainBindings(); return nil })
	h.idle()
	if len(h.tabIDs()) != 1 {
		t.Fatal("web page opened a tab through the privileged bridge")
	}
	if h.app.prof.Settings.Str("privacy.trackers") != "standard" {
		t.Fatal("web page changed a setting through the privileged bridge")
	}
}
