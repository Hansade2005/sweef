package app

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/privacy"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/shell"
)

func TestShortcutsAndTabOps(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/", srv.URL + "/two"}}})
	h.waitFor("2 tabs", func() bool { return len(h.tabIDs()) == 2 && !h.tab().Loading })
	h.idle()
	h.key("ctrl+t")
	if len(h.tabIDs()) != 3 || h.tab().URL != newTabURL || h.focusName() != "chrome" {
		t.Fatalf("ctrl+t: tabs=%d url=%q focus=%s", len(h.tabIDs()), h.tab().URL, h.focusName())
	}
	h.key("ctrl+w")
	if len(h.tabIDs()) != 2 {
		t.Fatalf("ctrl+w: %d tabs", len(h.tabIDs()))
	}
	// close the active tab, then reopen it with its history
	h.key("ctrl+1")
	h.waitTitle("Home Page")
	h.click(60, vpTop+150) // yellow link -> /two (history of 2 entries)
	h.waitTitle("Page Two")
	h.key("ctrl+w")
	if len(h.tabIDs()) != 1 {
		t.Fatal("close")
	}
	h.key("ctrl+shift+t")
	h.waitFor("reopened", func() bool { return len(h.tabIDs()) == 2 && h.tab().Title == "Page Two" })
	if tb := h.tab(); !tb.CanBack || len(tb.History) != 2 {
		t.Fatalf("reopened tab lost its history: %+v", tb)
	}
	if h.app.model.Order(h.app.winID)[0] != h.app.model.Active(h.app.winID) {
		t.Fatal("reopened tab should return to its old position")
	}
	// bookmark toggle
	h.key("ctrl+d")
	if !h.app.prof.Bookmarks.IsBookmarked(srv.URL + "/two") {
		t.Fatal("ctrl+d did not bookmark")
	}
	h.waitFor("state says bookmarked", func() bool { return h.state("bookmarked") == true })
	h.key("ctrl+d")
	if h.app.prof.Bookmarks.IsBookmarked(srv.URL + "/two") {
		t.Fatal("second ctrl+d should remove")
	}
	// incognito tab
	h.key("ctrl+shift+n")
	if !h.tab().Incognito {
		t.Fatalf("ctrl+shift+n tab not incognito: %+v", h.tab())
	}
	h.waitFor("incognito state", func() bool { return h.state("incognito") == true })
	// fullscreen, sidebar, find, zoom
	h.key("F11")
	if !h.win.Fullscreen() {
		t.Fatal("F11")
	}
	h.key("F11")
	if h.win.Fullscreen() {
		t.Fatal("F11 toggle")
	}
	h.key("ctrl+shift+a")
	if !h.sidebarOpen() {
		t.Fatal("ctrl+shift+a")
	}
	h.key("ctrl+shift+a")
	if h.sidebarOpen() {
		t.Fatal("ctrl+shift+a toggle")
	}
	h.key("ctrl+=")
	if h.app.prof.Settings.Int("zoom.default") != 110 {
		t.Fatalf("zoom %d", h.app.prof.Settings.Int("zoom.default"))
	}
	h.key("ctrl+0")
	if h.app.prof.Settings.Int("zoom.default") != 100 {
		t.Fatal("zoom reset")
	}
	// tab chrome messages
	ids := h.tabIDs()
	h.uiPost("tab.pin", map[string]any{"id": ids[len(ids)-1]})
	if tb, _ := h.app.model.Tab(ids[len(ids)-1]); !tb.Pinned || h.tabIDs()[0] != ids[len(ids)-1] {
		t.Fatal("pin must move the tab to the front")
	}
	h.uiPost("tab.move", map[string]any{"id": ids[0], "to": 1})
	h.uiPost("tab.activate", map[string]any{"id": ids[0]})
	if h.app.model.Active(h.app.winID) != ids[0] {
		t.Fatal("tab.activate")
	}
	h.uiPost("tab.mute", map[string]any{"id": ids[0]})
	if tb, _ := h.app.model.Tab(ids[0]); !tb.Muted {
		t.Fatal("mute")
	}
	h.uiPost("tab.close", map[string]any{"id": ids[0]})
	if _, ok := h.app.model.Tab(ids[0]); ok {
		t.Fatal("tab.close")
	}
}

func TestLastTabClosedQuits(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	h.key("ctrl+w")
	select {
	case err := <-h.done:
		h.done <- err // let the cleanup see it too
	case <-time.After(10 * time.Second):
		t.Fatal("closing the last tab should end the app, like closing the last window")
	}
}

func TestWheelScrollIsCompositorOnly(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/tall"}}})
	h.waitTitle("Tall")
	h.idle()
	h.wantPx(40, vpTop+50, "#2255cc")
	paints := h.pagePaints()
	before := h.win.Presents()
	h.win.Push(shell.Event{Kind: shell.EvWheel, X: 400, Y: 400, DY: 200})
	h.idle()
	if got := h.win.Presents() - before; got < 4 {
		t.Fatalf("smooth scrolling should animate over several frames, saw %d", got)
	}
	if h.pagePaints() != paints {
		t.Fatalf("scrolling within the painted area asked the engine to repaint (%d -> %d)", paints, h.pagePaints())
	}
	h.win.Push(shell.Event{Kind: shell.EvWheel, X: 400, Y: 400, DY: 700})
	h.idle()
	h.wantPx(40, vpTop+50, "#2255cc")  // page y=950: still blue
	h.wantPx(40, vpTop+150, "#cc8822") // page y=1050: orange
	pos, max := h.app.comp.Scroll()
	if pos != 900 || max != 2000-620 {
		t.Fatalf("scroll %d/%d", pos, max)
	}
	if st := h.tab(); st.Scroll != 900 {
		t.Fatalf("scroll not recorded for session restore: %d", st.Scroll)
	}
	// every Present only covered the viewport, never the chrome strip
	for _, d := range h.win.Damages()[before:] {
		if d.Min.Y < vpTop {
			t.Fatalf("scroll damaged the chrome: %v", d)
		}
	}
	// End key scrolls to the bottom and the texture is extended
	h.click(400, 300)
	h.key("End")
	h.waitFor("bottom", func() bool { p, m := h.app.comp.Scroll(); return p == m })
	h.idle()
	h.wantPx(40, 700-20, "#cc8822")
	// Home returns
	h.key("Home")
	h.waitFor("top", func() bool { p, _ := h.app.comp.Scroll(); return p == 0 })
	h.idle()
	h.wantPx(40, vpTop+50, "#2255cc")
	// the page does not render its own scrollbar into the texture; ours fades out
	h.waitFor("scrollbar gone", func() bool { return h.app.comp.Stats().Frames > 0 && !h.animating() })
	h.wantPx(995, vpTop+50, "#2255cc")
	// wheel over the chrome does not scroll the page
	pos0, _ := h.app.comp.Scroll()
	h.win.Push(shell.Event{Kind: shell.EvWheel, X: 300, Y: 10, DY: 500})
	h.idle()
	if p, _ := h.app.comp.Scroll(); p != pos0 {
		t.Fatal("wheel over the toolbar scrolled the page")
	}
}

func TestNewPageResetsScroll(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/tall"}}})
	h.waitTitle("Tall")
	h.win.Push(shell.Event{Kind: shell.EvWheel, X: 400, Y: 400, DY: 600})
	h.idle()
	if p, _ := h.app.comp.Scroll(); p != 600 {
		t.Fatal("setup")
	}
	h.app.post(func() { h.app.navigateInput(h.app.model.Active(h.app.winID), srv.URL+"/two") })
	h.waitTitle("Page Two")
	if p, _ := h.app.comp.Scroll(); p != 0 {
		t.Fatalf("a new page must start at the top, scroll=%d", p)
	}
	h.wantPx(40, vpTop+50, "#2f9e44")
}

func TestCursorFollowsContent(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	move := func(x, y float64) shell.Cursor {
		h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y})
		h.idle()
		return h.win.Cursor()
	}
	if c := move(60, vpTop+150); c != shell.CursorPointer {
		t.Fatalf("over a link: %v", c)
	}
	if c := move(60, vpTop+50); c != shell.CursorDefault {
		t.Fatalf("over the banner: %v", c)
	}
	if c := move(100, vpTop+120+60+60+10+15); c != shell.CursorText {
		t.Fatalf("over the text input: %v", c)
	}
	if c := move(22, toolbarY); c != shell.CursorPointer && c != shell.CursorDefault {
		t.Fatalf("over a chrome button: %v", c)
	}
}

func TestTypingIntoPageAndClipboard(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	h.click(100, vpTop+120+60+60+10+15) // focus the page's text input
	if h.focusName() != "page" {
		t.Fatalf("focus %s", h.focusName())
	}
	h.typeText("hello")
	var v string
	h.pageEval(`document.getElementById('f').value`, &v)
	if v != "hello" {
		t.Fatalf("input value %q", v)
	}
	h.key("Backspace")
	h.pageEval(`document.getElementById('f').value`, &v)
	if v != "hell" {
		t.Fatalf("after backspace %q", v)
	}
	// unicode incl. a non-BMP rune
	h.typeText("é😀")
	h.pageEval(`document.getElementById('f').value`, &v)
	if v != "hellé😀" {
		t.Fatalf("unicode input %q", v)
	}
	// select all + copy: system clipboard receives the text
	h.key("ctrl+a")
	h.key("ctrl+c")
	if got := h.win.Clipboard(); got != "hellé😀" {
		t.Fatalf("clipboard %q", got)
	}
	// cut removes it
	h.key("ctrl+x")
	h.pageEval(`document.getElementById('f').value`, &v)
	if v != "" {
		t.Fatalf("cut left %q", v)
	}
	// paste from the system clipboard
	h.win.SetClipboard("pasted text")
	h.key("ctrl+v")
	h.pageEval(`document.getElementById('f').value`, &v)
	if v != "pasted text" {
		t.Fatalf("paste %q", v)
	}
	// keys typed in the page do not reach the omnibox
	var ob string
	h.chromeEval(`document.getElementById('omnibox').value`, &ob)
	if ob != srv.URL+"/" {
		t.Fatalf("omnibox changed by page typing: %q", ob)
	}
	// ctrl+l moves focus to the omnibox, ctrl+a/ctrl+c copies the URL
	h.key("ctrl+l")
	h.key("ctrl+c")
	if got := h.win.Clipboard(); got != srv.URL+"/" {
		t.Fatalf("omnibox copy %q", got)
	}
	// clicking the page returns focus to it
	h.click(500, vpTop+400)
	if h.focusName() != "page" {
		t.Fatal("click in the viewport must focus the page")
	}
}

func TestSidebarReservesViewport(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	h.wantPx(900, vpTop+50, "#2f9e44")
	var w0 int
	h.pageEval(`window.innerWidth`, &w0)
	if w0 != 1000 {
		t.Fatalf("page width %d", w0)
	}
	h.key("ctrl+shift+a")
	h.waitFor("viewport shrinks", func() bool { return int(h.vp().W) == 1000-380 })
	h.idle()
	h.wantPx(900, vpTop+50, "#20263a") // sidebar background from the chrome page
	h.wantPx(300, vpTop+50, "#2f9e44")
	var w1 int
	h.pageEval(`window.innerWidth`, &w1)
	if w1 != 620 {
		t.Fatalf("the page was not re-laid-out for the narrower viewport: %d", w1)
	}
	// resize the sidebar from the chrome side
	h.uiPost("sidebar.set", map[string]any{"width": 420})
	h.waitFor("wider sidebar", func() bool { return int(h.vp().W) == 1000-420 })
	h.key("ctrl+shift+a")
	h.waitFor("viewport restored", func() bool { return int(h.vp().W) == 1000 })
	h.idle()
	h.wantPx(900, vpTop+50, "#2f9e44")
}

func TestResizeAndHiDPI(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{w: 800, h: 600, scale: 2, cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	scr := h.win.Screen()
	if scr.Bounds().Dx() != 1600 || scr.Bounds().Dy() != 1200 {
		t.Fatalf("device frame %v", scr.Bounds())
	}
	// banner is 120 CSS px tall at 2x: red until device y = 160+240
	h.wantPx(100, 2*vpTop+100, "#d33a3a")
	h.wantPx(100, 2*vpTop+300, "#f2c94c")
	h.wantPx(1500, 20, "#1c2030")
	// window resize (logical) re-lays-out chrome and page
	h.win.Resize(1200, 700, 2)
	h.waitFor("resized", func() bool { return h.logicalW() == 1200 && int(h.vp().W) == 1200 })
	h.idle()
	scr = h.win.Screen()
	if scr.Bounds().Dx() != 2400 || scr.Bounds().Dy() != 1400 {
		t.Fatalf("frame after resize %v", scr.Bounds())
	}
	h.wantPx(2300, 2*vpTop+100, "#d33a3a") // banner spans the new width
	var w int
	h.pageEval(`window.innerWidth`, &w)
	if w != 1200 {
		t.Fatalf("page innerWidth %d", w)
	}
	// dpi change
	h.win.Push(shell.Event{Kind: shell.EvScale, Scale: 1})
	h.waitFor("scale 1", func() bool { return h.scaleV() == 1 })
	h.idle()
	if b := h.win.Screen().Bounds(); b.Dx() != 1200 {
		t.Fatalf("frame after scale change %v", b)
	}
	h.wantPx(1100, vpTop+100, "#d33a3a")
}

func TestTrackerBlockingAndPrivacyHeaders(t *testing.T) {
	var trackerHits atomic.Int32
	l2, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skip("127.0.0.2 not available:", err)
	}
	tracker := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trackerHits.Add(1)
		fmt.Fprint(w, `window.__tracked = true;`)
	}))
	tracker.Listener = l2
	tracker.Start()
	defer tracker.Close()
	var mu sync.Mutex
	var gpc []string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		gpc = append(gpc, r.Header.Get("Sec-GPC")+"/"+r.Header.Get("DNT"))
		mu.Unlock()
		fmt.Fprintf(w, `<!doctype html><title>Site</title><script src="%s/track.js"></script><script src="/first.js"></script>`, tracker.URL)
	}))
	defer site.Close()
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{site.URL + "/"}}})
	h.app.priv.List = privacy.FromText("127.0.0.2\n")
	h.app.post(func() { h.app.reload(h.app.model.Active(h.app.winID), false) })
	h.waitFor("loaded", func() bool { return h.tab().Title == "Site" && !h.tab().Loading })
	h.waitFor("blocked count", func() bool { return h.tab().Blocked >= 1 })
	if trackerHits.Load() != 0 {
		t.Fatalf("tracker server was contacted %d times", trackerHits.Load())
	}
	var tracked any
	h.pageEval(`typeof window.__tracked`, &tracked)
	if tracked != "undefined" {
		t.Fatalf("tracker script ran: %v", tracked)
	}
	if h.app.priv.Total() < 1 || h.app.prof.Stats.Snapshot(1).Blocked < 1 {
		t.Fatal("blocked requests not counted")
	}
	mu.Lock()
	last := gpc[len(gpc)-1]
	mu.Unlock()
	if last != "1/1" {
		t.Fatalf("GPC/DNT headers on a navigation: %q", last)
	}
	// the count is per tab and shown in the state patch
	h.waitFor("state carries blocked", func() bool {
		for _, x := range h.stateTabs() {
			if m := x.(map[string]any); m["blocked"] != nil && m["blocked"].(float64) >= 1 {
				return true
			}
		}
		return false
	})
	// per-site exception turns blocking off for that origin only
	h.uiPost("site.privacy", map[string]any{"origin": site.URL, "noBlock": true})
	h.app.post(func() { h.app.reload(h.app.model.Active(h.app.winID), false) })
	h.waitFor("tracker fetched", func() bool { return trackerHits.Load() >= 1 })
	// switching the GPC setting off removes the header
	h.uiPost("settings.set", map[string]any{"key": "privacy.gpc", "value": false})
	h.app.post(func() { h.app.reload(h.app.model.Active(h.app.winID), false) })
	h.waitFor("no gpc", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return gpc[len(gpc)-1] == "/"
	})
}

func TestIncognitoIsolation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: r.URL.Query().Get("v"), Path: "/"})
		fmt.Fprint(w, `<!doctype html><title>Set</title>ok`)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		var cs []string
		for _, c := range r.Cookies() {
			cs = append(cs, c.Name+"="+c.Value)
		}
		fmt.Fprintf(w, `<!doctype html><title>Echo</title><p id="c">[%s]</p>`, strings.Join(cs, ";"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	prof, _ := store.OpenProfile(dir)
	h := newHarness(t, hopts{cfg: Config{Profile: prof, NoRestore: true, InitialURLs: []string{srv.URL + "/set?v=normal"}}})
	h.waitTitle("Set")
	// incognito tab must not see the normal cookie
	h.key("ctrl+shift+n")
	inc := h.tab()
	if !inc.Incognito {
		t.Fatal("not incognito")
	}
	h.app.post(func() { h.app.navigateInput(h.app.model.Active(h.app.winID), srv.URL+"/echo") })
	h.waitTitle("Echo")
	var c string
	h.pageEval(`document.getElementById('c').textContent`, &c)
	if c != "[]" {
		t.Fatalf("incognito request carried the normal profile's cookie: %s", c)
	}
	// ... and sets its own, invisible to the normal tab
	h.app.post(func() { h.app.navigateInput(h.app.model.Active(h.app.winID), srv.URL+"/set?v=secret") })
	h.waitTitle("Set")
	h.key("ctrl+1")
	h.app.post(func() { h.app.navigateInput(h.app.model.Active(h.app.winID), srv.URL+"/echo") })
	h.waitTitle("Echo")
	h.pageEval(`document.getElementById('c').textContent`, &c)
	// (the engine sends a context cookie twice, once from its own jar and once from the
	// client's; that duplicate predates this test and is harmless)
	if strings.Contains(c, "secret") || !strings.Contains(c, "sid=normal") {
		t.Fatalf("normal tab sees %s (incognito cookie leaked, or the normal one was lost)", c)
	}
	// history: normal profile has the normal visits, never the incognito ones
	if _, ok := h.app.prof.History.Get(srv.URL + "/set?v=secret"); ok {
		t.Fatal("incognito visit recorded in the persistent history")
	}
	if _, ok := h.app.incog.History.Get(srv.URL + "/set?v=secret"); !ok {
		t.Fatal("incognito history should exist in memory while the tab lives")
	}
	// the session file must not mention incognito tabs
	h.app.saveSession(false)
	raw, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "v=secret") {
		t.Fatalf("session file leaks incognito URL: %s", raw)
	}
	// closing the last incognito tab wipes its state
	incID := inc.ID
	h.uiPost("tab.close", map[string]any{"id": incID})
	h.idle()
	if h.hasIncog() {
		t.Fatal("incognito profile survived its last tab")
	}
	// nothing incognito reached disk
	files, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.Contains(string(b), "secret") {
			t.Fatalf("%s contains incognito data", f)
		}
	}
}

func TestIncognitoAppWritesNothing(t *testing.T) {
	srv := pageServer(t)
	dir := filepath.Join(t.TempDir(), "never-created")
	h := newHarness(t, hopts{cfg: Config{Incognito: true, ProfileDir: dir, DownloadsDir: t.TempDir(), InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	h.app.saveSession(true)
	h.close()
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("incognito app created its profile dir")
	}
}

func TestSessionRestore(t *testing.T) {
	srv := pageServer(t)
	dir := t.TempDir()
	prof, _ := store.OpenProfile(dir)
	prof.Settings.Set("startup.mode", "restore")
	prof.Settings.Set("onboarding.done", true)
	h := newHarness(t, hopts{cfg: Config{Profile: prof, InitialURLs: []string{srv.URL + "/", srv.URL + "/tall", srv.URL + "/two"}}})
	h.waitFor("loaded", func() bool {
		for _, id := range h.tabIDs() {
			if tb, _ := h.app.model.Tab(id); tb.Loading || tb.Title == "New Tab" {
				return false
			}
		}
		return len(h.tabIDs()) == 3
	})
	h.key("ctrl+2")
	h.waitTitle("Tall")
	h.win.Push(shell.Event{Kind: shell.EvWheel, X: 400, Y: 400, DY: 700})
	h.idle()
	h.uiPost("tab.pin", map[string]any{"id": h.tabIDs()[0]})
	h.close()

	prof2, _ := store.OpenProfile(dir)
	h2 := newHarness(t, hopts{cfg: Config{Profile: prof2}})
	h2.waitFor("restored", func() bool { return len(h2.tabIDs()) == 3 && h2.tab().Title == "Tall" && !h2.tab().Loading })
	h2.idle()
	var disc, pinned int
	for _, id := range h2.tabIDs() {
		tb, _ := h2.app.model.Tab(id)
		if tb.Discarded {
			disc++
		}
		if tb.Pinned {
			pinned++
		}
	}
	if disc != 2 || pinned != 1 {
		t.Fatalf("restored: %d lazy (want 2), %d pinned (want 1)", disc, pinned)
	}
	h2.waitFor("scroll restored", func() bool { p, _ := h2.app.comp.Scroll(); return p == 700 })
	h2.idle()
	h2.wantPx(40, vpTop+50, "#2255cc")
	// a lazy tab loads when activated
	h2.key("ctrl+3")
	h2.waitTitle("Page Two")
	h2.wantPx(40, vpTop+50, "#2f9e44")
}

func TestSessionRestoreAfterCrashOnly(t *testing.T) {
	srv := pageServer(t)
	dir := t.TempDir()
	prof, _ := store.OpenProfile(dir)
	prof.Settings.Set("onboarding.done", true)
	// default policy ("new tab" startup): a clean exit starts fresh ...
	h := newHarness(t, hopts{cfg: Config{Profile: prof, NoRestore: true, InitialURLs: []string{srv.URL + "/two"}}})
	h.waitTitle("Page Two")
	h.close()
	prof2, _ := store.OpenProfile(dir)
	h2 := newHarness(t, hopts{cfg: Config{Profile: prof2}})
	h2.waitFor("fresh start", func() bool { return len(h2.tabIDs()) == 1 && h2.tab().URL == newTabURL })
	h2.close()
	// ... a crash (no clean-exit flag) restores
	prof3, _ := store.OpenProfile(dir)
	prof3.Session.Save(store.Session{CleanExit: false, Windows: []store.SessionWindow{{Tabs: []store.SessionTab{{URL: srv.URL + "/two", Title: "Page Two"}}}}})
	prof3.Close()
	prof4, _ := store.OpenProfile(dir)
	h3 := newHarness(t, hopts{cfg: Config{Profile: prof4}})
	h3.waitTitle("Page Two")
}

func TestUIMessagesNeverPanic(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	garbage := []string{`null`, `[]`, `"x"`, `42`, `{"tab":5}`, `{"id":[1]}`, `{"viewport":"no"}`, `{"text":{"a":1}}`, `{"to":"x"}`, `{"key":1,"value":{}}`}
	types := []string{"nav.go", "nav.back", "nav.forward", "nav.reload", "nav.stop", "tab.new", "tab.close", "tab.activate", "tab.move", "tab.pin", "tab.mute",
		"omni.input", "bookmark.toggle", "bookmark.add", "bookmark.remove", "menu.open", "agent.send", "agent.stop", "agent.pageVisibility", "settings.set",
		"sidebar.set", "find.query", "find.step", "downloads.open", "downloads.pause", "history.clear", "site.privacy", "zoom", "layout", "overlay", "nonsense", ""}
	c := h.app.chromeTab()
	for _, ty := range types {
		for _, g := range garbage {
			ty, g := ty, g
			h.app.post(func() { h.app.onUIMessage(c, ty, []byte(g)) })
		}
	}
	h.idle()
	if h.tab().Title != "Home Page" && len(h.tabIDs()) == 0 {
		t.Fatal("app state destroyed by garbage input")
	}
}

func TestIdleUsesNoCPUAndNoTimers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("rusage")
	}
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/", srv.URL + "/two"}}})
	h.waitTitle("Home Page")
	h.idle()
	time.Sleep(500 * time.Millisecond)
	if h.animating() {
		t.Fatal("animation timer left running while idle")
	}
	var r0, r1 syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &r0)
	presents := h.win.Presents()
	time.Sleep(2 * time.Second)
	syscall.Getrusage(syscall.RUSAGE_SELF, &r1)
	cpu := time.Duration(r1.Utime.Nano()+r1.Stime.Nano()-r0.Utime.Nano()-r0.Stime.Nano()) * time.Nanosecond
	t.Logf("idle for 2s: %v CPU, %d frames", cpu, h.win.Presents()-presents)
	if h.win.Presents() != presents {
		t.Fatal("frames presented while idle")
	}
	limit := 100 * time.Millisecond // 5% of one core over 2 s (in-suite runs share the process with other tests' background work; alone it is ~9 ms)
	if raceEnabled {
		limit *= 4
	}
	if cpu > limit {
		t.Fatalf("idle CPU %v over 2s", cpu)
	}
}

func TestNoGoroutineLeakAcrossAppLifetimes(t *testing.T) {
	srv := pageServer(t)
	cycle := func() {
		h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/", srv.URL + "/two"}}})
		h.waitFor("loaded", func() bool { return len(h.tabIDs()) == 2 && !h.tab().Loading })
		h.key("ctrl+t")
		h.key("ctrl+w")
		h.win.Push(shell.Event{Kind: shell.EvWheel, X: 400, Y: 400, DY: 300})
		h.idle()
		h.close()
	}
	cycle() // warm up package-level goroutines (engine pools, http transports)
	settle := func() int {
		var n int
		for i := 0; i < 40; i++ {
			runtime.GC()
			n = runtime.NumGoroutine()
			time.Sleep(50 * time.Millisecond)
		}
		return n
	}
	base := settle()
	for i := 0; i < 3; i++ {
		cycle()
	}
	after := settle()
	if after > base+2 {
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("goroutines grew %d -> %d over 3 app lifetimes\n%s", base, after, buf)
	}
	t.Logf("goroutines: %d before, %d after 3 lifetimes", base, after)
}

func TestThirdPartyCookiesBlocked(t *testing.T) {
	l2, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skip("127.0.0.2 not available:", err)
	}
	third := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "track", Value: "1", Path: "/"})
		fmt.Fprint(w, `window.__x = 1;`)
	}))
	third.Listener = l2
	third.Start()
	defer third.Close()
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "first", Value: "1", Path: "/"})
		fmt.Fprintf(w, `<!doctype html><title>Site</title><script src="%s/t.js?n=%s"></script>`, third.URL, r.URL.Query().Get("n"))
	}))
	defer site.Close()
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{site.URL + "/?n=1"}}})
	h.waitFor("loaded", func() bool { return h.tab().Title == "Site" && !h.tab().Loading })
	h.idle()
	jar := h.app.activePage().t.Context().JarRef()
	if ck := jar.ForURL(third.URL + "/"); ck != "" {
		t.Fatalf("third-party cookie persisted: %q", ck)
	}
	if ck := jar.ForURL(site.URL + "/"); !strings.Contains(ck, "first=1") {
		t.Fatalf("first-party cookie was lost: %q", ck)
	}
	// with the switch off the same kind of cookie is kept (a new URL, so the script is not served from cache)
	h.uiPost("settings.set", map[string]any{"key": "privacy.thirdPartyCookies", "value": false})
	h.app.post(func() { h.app.navigateInput(h.app.model.Active(h.app.winID), site.URL+"/?n=2") })
	h.waitFor("second load", func() bool { return h.tab().URL == site.URL+"/?n=2" && !h.tab().Loading })
	h.idle()
	if ck := jar.ForURL(third.URL + "/"); !strings.Contains(ck, "track=1") {
		t.Fatalf("with the switch off the cookie should be kept: %q", ck)
	}
}
