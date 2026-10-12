package app

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/ui"
)

// These tests run the app against the real chrome UI (internal/ui) instead of
// the placeholder: the handshake, state shape, internal pages and the
// hit-testing rules of docs/gui-contract.md.

func realUI(t testing.TB, cfg Config) *harness {
	cfg.ChromeFS = ui.FS
	if cfg.Profile == nil && cfg.ProfileDir == "" {
		cfg.Profile = store.OpenIncognito()
	}
	h := newHarness(t, hopts{w: 1280, h: 800, cfg: cfg})
	return h
}

func (h *harness) waitUIReady() {
	h.t.Helper()
	h.waitFor("chrome ready", func() bool {
		var n float64
		c := h.app.chromeTab()
		if c == nil || c.call(func() error {
			return c.eval(`(window.SW && SW.state && SW.state.tabs) ? SW.state.tabs.length : -1`, &n)
		}) != nil {
			return false
		}
		return n >= 1
	})
	h.idle()
}

func TestRealUIHandshakeAndState(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	var st map[string]any
	h.chromeEval(`SW.state`, &st)
	for _, k := range []string{"platform", "version", "theme", "tabs", "active", "url", "title", "security", "bookmarked", "sidebar", "settings", "downloads", "topSites", "agent", "window", "progress"} {
		if _, ok := st[k]; !ok {
			t.Errorf("state lacks %q: %v", k, keysOf(st))
		}
	}
	settings := st["settings"].(map[string]any)
	for _, k := range []string{"privacy.trackers", "privacy.gpc", "search.engine", "sidebar.width", "startup.mode", "agent.visibilityDefault", "agent.mode"} {
		if _, ok := settings[k]; !ok {
			t.Errorf("settings lacks %q", k)
		}
	}
	tab := st["tabs"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "title", "url", "favicon", "loading", "audible", "muted", "pinned", "crashed"} {
		if _, ok := tab[k]; !ok && k != "favicon" {
			t.Errorf("tab lacks %q: %v", k, tab)
		}
	}
	// the chrome reported its viewport hole and the page tab was laid out into it
	vp := h.vp()
	if vp.W != 1280 || vp.Y < 60 || vp.Y > 140 || vp.H < 500 {
		t.Fatalf("viewport hole %+v", vp)
	}
	var w int
	h.pageEval(`window.innerWidth`, &w)
	if w != 1280 {
		t.Fatalf("page innerWidth %d", w)
	}
	// the page shows through the transparent viewport, the chrome is opaque above it
	h.wantPx(640, int(vp.Y)+100, "#2f9e44")
	if a := h.app.comp.ChromeAlphaAt(640, 10); a != 255 {
		t.Fatalf("tab strip alpha %d", a)
	}
	if a := h.app.comp.ChromeAlphaAt(640, int(vp.Y)+100); a != 0 {
		t.Fatalf("viewport alpha %d", a)
	}
	h.shot("realui-home.png")
}

func keysOf(m map[string]any) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func TestRealUIOmniboxNavigatesAndSuggests(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	h.app.prof.History.Visit(srv.URL+"/blank", "Blank Target", true)
	h.key("ctrl+l")
	if h.focusName() != "chrome" {
		t.Fatal("ctrl+l must move focus to the chrome")
	}
	h.typeText(strings.TrimPrefix(srv.URL, "http://") + "/bla")
	// the chrome's dropdown shows our history suggestion (rows come from omni.suggestions)
	h.waitFor("dropdown rows", func() bool {
		var n float64
		h.chromeEval(`SW.$$("#od .orow").length`, &n)
		return n >= 2 // the UI's own "go/search" row plus ours
	})
	var rows []string
	h.chromeEval(`SW.$$("#od .orow").map(function(r){return r.textContent})`, &rows)
	joined := strings.Join(rows, "|")
	if !strings.Contains(joined, "Blank Target") {
		t.Fatalf("history suggestion missing: %v", rows)
	}
	vp := h.vp()
	// the dropdown overlaps the viewport hole and is drawn over the page
	h.waitFor("dropdown ink over the page", func() bool { return h.app.comp.ChromeAlphaAt(500, int(vp.Y)+20) != 0 })
	h.shot("realui-dropdown.png")
	h.key("Enter")
	h.waitTitle("Blank Target")
	if got := h.tab().URL; got != srv.URL+"/blank" && got != "http://"+strings.TrimPrefix(srv.URL, "http://")+"/bla" {
		t.Logf("navigated to %s", got)
	}
}

func TestRealUINewTabPageAndInternalPages(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	// a visited site shows up on the new tab page
	h.app.prof.History.Visit(srv.URL+"/", "Home Page", false)
	h.key("ctrl+t")
	h.waitFor("newtab loaded", func() bool { return h.tab().URL == newTabURL && !h.tab().Loading })
	h.waitFor("newtab got state", func() bool {
		var n float64
		e := h.app.activePage()
		return e.call(func() error {
			return e.eval(`(window.SW && SW.state && SW.state.tabs) ? SW.state.tabs.length : -1`, &n)
		}) == nil && n >= 2
	})
	var sites float64
	h.waitFor("top sites", func() bool {
		h.pageEval(`SW.$$("#sites .site").length`, &sites)
		return sites >= 1
	})
	h.shot("realui-newtab.png")
	// ui.ready registered the page for state patches: a settings change reaches it
	h.uiPost("settings.set", map[string]any{"key": "theme", "value": "dark"})
	h.waitFor("theme patch reached the new tab page", func() bool {
		var th string
		h.pageEval(`document.documentElement.getAttribute("data-theme")`, &th)
		return th == "dark"
	})

	// settings page: toggling a switch writes the setting through the bridge
	h.uiPost("tab.new", map[string]any{"url": "sweef://settings"})
	h.waitFor("settings loaded", func() bool { return h.tab().URL == "sweef://settings" && !h.tab().Loading })
	h.waitFor("settings rendered", func() bool {
		var n float64
		h.pageEval(`SW.$$(".srow").length`, &n)
		return n > 0
	})
	h.pageEval(`SW.set("privacy.gpc", false); 1`, nil)
	h.waitFor("setting stored", func() bool { return !h.app.prof.Settings.Bool("privacy.gpc") })
	h.shot("realui-settings.png")

	// history page: data.query / data.result
	h.uiPost("tab.new", map[string]any{"url": "sweef://history"})
	h.waitFor("history loaded", func() bool { return h.tab().URL == "sweef://history" && !h.tab().Loading })
	h.waitFor("history rows", func() bool {
		var n float64
		h.pageEval(`SW.$$("#list .lrow").length`, &n)
		return n >= 1
	})
	// deleting a row removes it from the store
	h.pageEval(`SW.$$("#list .lrow .x")[0].click(); 1`, nil)
	h.waitFor("history entry removed", func() bool { return h.app.prof.History.Len() < 2 })
	h.shot("realui-history.png")

	// bookmarks page lists bookmarks
	h.app.prof.Bookmarks.Add("", "Example", "https://example.com/")
	h.uiPost("tab.new", map[string]any{"url": "sweef://bookmarks"})
	h.waitFor("bookmark rows", func() bool {
		var n float64
		h.pageEval(`SW.$$("#list .lrow").length`, &n)
		return h.tab().URL == "sweef://bookmarks" && n >= 1
	})
}

func TestRealUIAcceleratorsFromThePage(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	h.click(500, 400) // focus the page
	if h.focusName() != "page" {
		t.Fatal("focus")
	}
	// Ctrl+T with the page focused becomes ui.command tab.new: the chrome posts tab.new
	h.key("ctrl+t")
	h.waitFor("tab opened by the chrome", func() bool { return len(h.tabIDs()) == 2 })
	// and with the chrome focused, the chrome handles the key itself and Go does not act twice
	h.key("ctrl+t")
	h.waitFor("one more tab", func() bool { return len(h.tabIDs()) == 3 })
	h.idle()
	if len(h.tabIDs()) != 3 {
		t.Fatalf("Ctrl+T opened %d tabs (the key was acted on twice)", len(h.tabIDs())-1)
	}
	// Ctrl+Shift+A toggles the sidebar; the viewport shrinks
	w0 := h.vp().W
	h.key("ctrl+shift+a")
	h.waitFor("sidebar", func() bool { return h.vp().W < w0-200 })
	h.key("ctrl+shift+a")
	h.waitFor("sidebar closed", func() bool { return h.vp().W == w0 })
}

func TestRealUIErrorPage(t *testing.T) {
	h := realUI(t, Config{InitialURLs: []string{"http://127.0.0.1:1/"}})
	h.waitFor("error page", func() bool {
		return strings.Contains(h.tab().URL, "127.0.0.1") && !h.tab().Loading && h.tab().Title != "" && h.tab().Title != "New Tab"
	})
	h.waitFor("error page rendered by the UI", func() bool {
		var n float64
		h.pageEval(`document.querySelectorAll("#err h1").length`, &n)
		return n == 1
	})
	var kind string
	h.pageEval(`document.documentElement.getAttribute("data-kind")`, &kind)
	if kind != "offline" {
		t.Fatalf("kind %q", kind)
	}
	h.shot("realui-error.png")
	// Reload from the error page uses nav.reload and keeps the URL
	h.pageEval(`document.querySelector(".acts .btn.primary").click(); 1`, nil)
	h.idle()
	if !strings.HasPrefix(h.tab().URL, "http://127.0.0.1:1") {
		t.Fatalf("url after reload %q", h.tab().URL)
	}
}

func TestRealUIAgentOverlayAndSidebar(t *testing.T) {
	srv := pageServer(t)
	fake := NewFakeAgent()
	fake.Delay = 600 * 1e6
	if raceEnabled {
		fake.Delay = 4000 * 1e6
	}
	h := realUI(t, Config{Agent: fake, InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	h.app.prof.Sites.SetAgent(srv.URL, store.VisAllowed)
	before := h.win.Screen()
	h.uiPost("agent.send", map[string]any{"text": "summarize", "mode": "agent"})
	h.waitFor("overlay on", func() bool { return h.ovlVisible() })
	h.waitFor("overlay drawn", func() bool {
		now := h.win.Screen()
		diff := 0
		for y := 100; y < 780; y += 7 {
			for x := 0; x < 1280; x += 7 {
				if now.RGBAAt(x, y) != before.RGBAAt(x, y) {
					diff++
				}
			}
		}
		return diff > 50
	})
	h.shot("realui-overlay.png")
	// agent state is exposed to the UI
	var ag map[string]any
	h.chromeEval(`SW.state.agent`, &ag)
	if ag["state"] == nil {
		t.Fatalf("state.agent %v", ag)
	}
	h.waitFor("overlay done", func() bool { var s string; h.loop(func() { s = h.app.ovl.state }); return s == "done" })
	h.uiPost("agent.stop", nil)
	h.waitFor("run idle", func() bool {
		var a map[string]any
		h.chromeEval(`SW.state.agent`, &a)
		return a["state"] == "idle"
	})
	_ = shell.CursorDefault
}

func realUIFS() fs.FS { return ui.FS }
