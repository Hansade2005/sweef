package app

import (
	"bytes"
	"fmt"
	"github.com/sweefbrowser/sweef/internal/app/omnibox"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/shell"
)

func ctxPNG() []byte {
	im := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range im.Pix {
		im.Pix[i] = 0xff
	}
	im.Set(1, 1, color.RGBA{R: 200, A: 255})
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return b.Bytes()
}

// ctxServer serves a page with one of each thing a context menu cares about, at known positions (CSS px).
func ctxServer(t testing.TB) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Ctx</title></head><body style="margin:0">
<a id="lnk" href="/two" style="position:absolute;left:10px;top:10px;width:200px;height:40px;background:#f2c94c">a link</a>
<img id="im" src="/pic.png" style="position:absolute;left:10px;top:70px;width:50px;height:50px">
<a id="both" href="/two" style="position:absolute;left:100px;top:70px;width:60px;height:50px;display:block"><img src="/pic.png" width="60" height="50"></a>
<input id="ed" value="hello world" style="position:absolute;left:10px;top:140px;width:200px;height:30px">
<p id="txt" style="position:absolute;left:10px;top:200px;margin:0;width:200px;height:30px;background:#ddd">some selectable text</p>
<video id="v" src="/v.mp4" loop style="position:absolute;left:300px;top:70px;width:200px;height:100px;background:#000"></video>
<div id="pd" oncontextmenu="event.preventDefault();window.__pd=(window.__pd||0)+1" style="position:absolute;left:300px;top:200px;width:100px;height:50px;background:#c33"></div>
<div id="plain" style="position:absolute;left:600px;top:300px;width:100px;height:100px;background:#eee"></div>
<script>window.__ctx=[];document.addEventListener('contextmenu',function(e){window.__ctx.push([e.clientX,e.clientY,e.target.id||e.target.localName])},true)</script>
</body></html>`)
	})
	mux.HandleFunc("/two", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Page Two</title></head><body>two</body></html>`)
	})
	mux.HandleFunc("/pic.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(ctxPNG())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (h *harness) rightClick(x, y float64) {
	h.t.Helper()
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y})
	h.win.Push(shell.Event{Kind: shell.EvMouseDown, X: x, Y: y, Button: 2})
	h.win.Push(shell.Event{Kind: shell.EvMouseUp, X: x, Y: y, Button: 2})
	h.idle()
}

// pageRightClick right-clicks at a CSS position of the page.
func (h *harness) pageRightClick(cx, cy float64) {
	h.t.Helper()
	vp := h.vp()
	h.rightClick(vp.X+cx, vp.Y+cy)
}

func (h *harness) ctxLabels() (out []string) {
	h.loop(func() {
		var walk func(in []ctxItem, pre string)
		walk = func(in []ctxItem, pre string) {
			for _, it := range in {
				if it.Sep {
					continue
				}
				l := pre + it.Label
				if it.Disabled {
					l += " (disabled)"
				}
				out = append(out, l)
				walk(it.Sub, pre+"  ")
			}
		}
		if h.app.ctx.open {
			walk(h.app.ctx.items, "")
		}
	})
	return
}

func (h *harness) ctxOpen() (b bool) { h.loop(func() { b = h.app.ctx.open }); return }

// ctxPick chooses the item with this label, as the chrome does after a click.
func (h *harness) ctxPick(label string) {
	h.t.Helper()
	var id string
	var tok int
	h.loop(func() {
		var find func(in []ctxItem)
		find = func(in []ctxItem) {
			for _, it := range in {
				if it.Label == label && it.ID != "" {
					id = it.ID
				}
				find(it.Sub)
			}
		}
		find(h.app.ctx.items)
		tok = h.app.ctx.token
	})
	if id == "" {
		h.t.Fatalf("menu has no item %q: %v", label, h.ctxLabels())
	}
	h.uiPost("ctx.pick", map[string]any{"token": tok, "id": id})
	h.idle()
}

func hasLabel(ls []string, want string) bool {
	for _, l := range ls {
		if l == want {
			return true
		}
	}
	return false
}

func (h *harness) wantMenu(kind string, want ...string) {
	h.t.Helper()
	h.waitFor(kind+" menu", func() bool { return h.ctxOpen() })
	ls := h.ctxLabels()
	for _, w := range want {
		if !hasLabel(ls, w) {
			h.t.Fatalf("%s menu lacks %q: %v", kind, w, ls)
		}
	}
}

func newCtxHarness(t *testing.T) (*harness, *httptest.Server) {
	srv := ctxServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Ctx")
	return h, srv
}

func TestCtxLinkMenu(t *testing.T) {
	h, srv := newCtxHarness(t)
	h.pageRightClick(50, 30)
	h.wantMenu("link", "Open link in new tab", "Copy link address", "Save link as…", "Inspect")
	if ls := h.ctxLabels(); hasLabel(ls, "Back") || hasLabel(ls, "Reload") {
		t.Fatalf("a link menu is not the page menu: %v", ls)
	}
	// the page saw the contextmenu event first, with the right coordinates and target
	var ev []any
	h.pageEval(`window.__ctx[0]`, &ev)
	if len(ev) != 3 || ev[0].(float64) != 50 || ev[1].(float64) != 30 || ev[2] != "lnk" {
		t.Fatalf("page saw %v", ev)
	}
	n := len(h.tabIDs())
	h.ctxPick("Open link in new tab")
	h.waitFor("link tab", func() bool { return len(h.tabIDs()) == n+1 })
	if h.app.activePage() == nil || h.tab().URL != srv.URL+"/" {
		t.Fatalf("opening in a new tab must not steal the active tab: %q", h.tab().URL)
	}
	h.pageRightClick(50, 30)
	h.wantMenu("link")
	h.ctxPick("Copy link address")
	if got := h.win.Clipboard(); got != srv.URL+"/two" {
		t.Fatalf("clipboard %q", got)
	}
}

func TestCtxImageMenu(t *testing.T) {
	h, srv := newCtxHarness(t)
	h.pageRightClick(30, 90)
	h.wantMenu("image", "Open image in new tab", "Copy image", "Copy image address", "Save image as…")
	if ls := h.ctxLabels(); hasLabel(ls, "Open link in new tab") || hasLabel(ls, "Reload") {
		t.Fatalf("plain image has neither link nor page items: %v", ls)
	}
	h.ctxPick("Copy image address")
	if got := h.win.Clipboard(); got != srv.URL+"/pic.png" {
		t.Fatalf("clipboard %q", got)
	}
	// an image inside a link has both groups
	h.pageRightClick(130, 90)
	h.wantMenu("image in link", "Open link in new tab", "Open image in new tab")
	h.ctxPick("Copy image")
	h.waitFor("image on clipboard", func() bool {
		var ok bool
		h.loop(func() {
			for _, f := range h.app.clip.readNow() {
				if f.Type == "image/png" && len(f.Data) > 0 {
					ok = true
				}
			}
		})
		return ok
	})
}

func TestCtxSelectionMenu(t *testing.T) {
	h, srv := newCtxHarness(t)
	// the search engine is a local page: a live site would keep the app from going idle on a runner without internet
	saved := omnibox.Engines[0].Template
	omnibox.Engines[0].Template = srv.URL + "/?q=%s"
	t.Cleanup(func() { omnibox.Engines[0].Template = saved })
	// The engine's document selection is still a stub (another change implements it), so the report the page helper
	// would give for a selected paragraph is fed to the menu builder directly; the helper's side is covered by the
	// editable-field tests, which use the real input selection.
	h.loop(func() {
		e := h.app.activePage()
		h.app.pageContextMenu(e, e.t, 60, 215, ctxInfo{Selection: "some selectable text"}, 100, 300)
	})
	h.waitFor("menu", func() bool { return h.ctxOpen() })
	ls := h.ctxLabels()
	if !hasLabel(ls, "Copy") {
		t.Fatalf("selection menu: %v", ls)
	}
	found := false
	for _, l := range ls {
		if strings.HasPrefix(l, "Search ") && strings.Contains(l, "some selectable text") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no Search item for the selection: %v", ls)
	}
	n := len(h.tabIDs())
	var label string
	h.loop(func() {
		for _, it := range h.app.ctx.items {
			if strings.HasPrefix(it.Label, "Search ") {
				label = it.Label
			}
		}
	})
	h.ctxPick(label)
	h.waitFor("search tab", func() bool { return len(h.tabIDs()) == n+1 })
	if u := h.tab().URL; !strings.Contains(u, "some+selectable+text") && !strings.Contains(u, "some%20selectable%20text") {
		t.Fatalf("search url %q", u)
	}
}

func TestCtxEditableMenu(t *testing.T) {
	h, _ := newCtxHarness(t)
	h.pageRightClick(50, 155)
	h.wantMenu("editable", "Undo (disabled)", "Redo (disabled)", "Cut (disabled)", "Copy (disabled)", "Paste (disabled)", "Select all", "Inspect")
	// right-click focuses the field; Select all selects it, and the menu then offers Cut, Copy and Delete
	h.ctxPick("Select all")
	var sel string
	h.pageEval(`document.getElementById('ed').value.slice(document.getElementById('ed').selectionStart, document.getElementById('ed').selectionEnd)`, &sel)
	if sel != "hello world" {
		t.Fatalf("select all selected %q", sel)
	}
	h.pageRightClick(50, 155)
	h.wantMenu("editable with selection", "Cut", "Copy", "Delete")
	h.ctxPick("Cut")
	var v string
	h.pageEval(`document.getElementById('ed').value`, &v)
	if v != "" || h.win.Clipboard() != "hello world" {
		t.Fatalf("cut: field %q clipboard %q", v, h.win.Clipboard())
	}
	// undo restores it
	h.pageRightClick(50, 155)
	h.wantMenu("editable after cut", "Undo")
	h.ctxPick("Undo")
	h.pageEval(`document.getElementById('ed').value`, &v)
	if v != "hello world" {
		t.Fatalf("undo gave %q", v)
	}
	// paste puts the clipboard back
	h.pageEval(`(function(){var e=document.getElementById('ed');e.value='';e.focus();return 1})()`, nil)
	h.pageRightClick(50, 155)
	h.wantMenu("paste", "Paste")
	h.ctxPick("Paste")
	h.pageEval(`document.getElementById('ed').value`, &v)
	if v != "hello world" {
		t.Fatalf("paste gave %q", v)
	}
}

func TestCtxMediaMenu(t *testing.T) {
	h, srv := newCtxHarness(t)
	h.pageRightClick(400, 120)
	h.wantMenu("video", "Play", "Mute", "Loop", "Copy video address", "Save video as…", "Playback speed", "  Normal")
	if ls := h.ctxLabels(); hasLabel(ls, "Picture in picture") {
		t.Logf("picture in picture offered: %v", ls)
	}
	h.ctxPick("Mute")
	var m bool
	h.pageEval(`document.getElementById('v').muted`, &m)
	if !m {
		t.Fatal("Mute did not mute")
	}
	h.pageRightClick(400, 120)
	h.wantMenu("video again", "Unmute")
	h.ctxPick("Copy video address")
	if got := h.win.Clipboard(); got != srv.URL+"/v.mp4" {
		t.Fatalf("clipboard %q", got)
	}
}

func TestCtxPageMenuAndInspect(t *testing.T) {
	h, srv := newCtxHarness(t)
	h.pageRightClick(650, 350)
	h.wantMenu("page", "Back (disabled)", "Forward (disabled)", "Reload", "Save page as…", "Print…", "View page source", "Inspect")
	active := h.app.model.Active(h.app.winID)
	h.ctxPick("Inspect")
	h.waitFor("developer tools open for the active tab", func() bool {
		open, tab := h.devtoolsOpen()
		return open && tab == active
	})
	h.pageRightClick(650, 350)
	h.wantMenu("page")
	n := len(h.tabIDs())
	h.ctxPick("View page source")
	h.waitFor("source tab", func() bool { return len(h.tabIDs()) == n+1 })
	if u := h.tab().URL; u != "view-source:"+srv.URL+"/" {
		t.Fatalf("url %q", u)
	}
}

func TestCtxPageCanCancelTheMenu(t *testing.T) {
	h, _ := newCtxHarness(t)
	h.pageRightClick(350, 220)
	if h.ctxOpen() {
		t.Fatalf("the page called preventDefault: %v", h.ctxLabels())
	}
	var n float64
	h.pageEval(`window.__pd`, &n)
	if n != 1 {
		t.Fatalf("page saw %v contextmenu events", n)
	}
}

func TestCtxDismissedByScrollResizeAndTabChange(t *testing.T) {
	h, _ := newCtxHarness(t)
	h.pageRightClick(650, 350)
	h.wantMenu("page")
	vp := h.vp()
	h.win.Push(shell.Event{Kind: shell.EvWheel, X: vp.X + 100, Y: vp.Y + 100, DY: 40})
	h.idle()
	if h.ctxOpen() {
		t.Fatal("menu survived a scroll")
	}
	h.pageRightClick(650, 350)
	h.wantMenu("page")
	h.win.Push(shell.Event{Kind: shell.EvFocus, Focus: false})
	h.idle()
	if h.ctxOpen() {
		t.Fatal("menu survived window blur")
	}
	h.pageRightClick(650, 350)
	h.wantMenu("page")
	h.loop(func() { h.app.newTab("", false, true) })
	h.idle()
	if h.ctxOpen() {
		t.Fatal("menu survived a tab change")
	}
}

func TestCtxFocusIsRestored(t *testing.T) {
	h, _ := newCtxHarness(t)
	h.click(h.vp().X+700, h.vp().Y+400)
	before := h.focusName()
	h.pageRightClick(650, 350)
	h.wantMenu("page")
	if h.focusName() != "chrome" {
		t.Fatalf("keys must go to the open menu, focus is %q", h.focusName())
	}
	h.ctxPick("Reload")
	if got := h.focusName(); got != before {
		t.Fatalf("focus %q after the menu, was %q", got, before)
	}
}

// ---- tab model actions -------------------------------------------------------------------------------

func TestTabDuplicateCloseOthersRightMuteSite(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/", srv.URL + "/two"}}})
	h.waitFor("two tabs", func() bool { return len(h.tabIDs()) == 2 })
	h.idle()
	ids := h.tabIDs()
	h.loop(func() { h.app.duplicateTab(ids[0]) })
	h.idle()
	got := h.tabIDs()
	if len(got) != 3 || got[0] != ids[0] || got[2] != ids[1] {
		t.Fatalf("duplicate must land right after its source: %v (was %v)", got, ids)
	}
	dup, _ := h.app.model.Tab(got[1])
	src, _ := h.app.model.Tab(ids[0])
	if dup.URL != src.URL || dup.Pinned {
		t.Fatalf("duplicate %+v of %+v", dup, src)
	}
	// mute site: every tab on the same host
	h.loop(func() { h.app.muteSite(ids[0]) })
	for _, id := range got {
		if st, _ := h.app.model.Tab(id); !st.Muted {
			t.Fatalf("tab %s not muted", id)
		}
	}
	h.loop(func() { h.app.muteSite(ids[0]) })
	if st, _ := h.app.model.Tab(ids[1]); st.Muted {
		t.Fatal("second Mute site must unmute")
	}
	// close right, then others (pinned tabs survive)
	h.loop(func() { h.app.togglePin(got[1]); h.app.closeTabsRight(ids[0]) })
	h.idle()
	if now := h.tabIDs(); len(now) != 2 {
		t.Fatalf("close to the right left %v (the pinned duplicate stays)", now)
	}
	h.loop(func() { h.app.closeOtherTabs(ids[0]) })
	h.idle()
	if now := h.tabIDs(); len(now) != 2 {
		t.Fatalf("close others must keep pinned tabs: %v", now)
	}
}

// ---- the real chrome: rendering, keyboard, chrome elements ---------------------------------------------

func menuDOM(h *harness) (labels []string) {
	h.chromeEval(`SW.$$(".ctxm .ctxi .lb").map(function(n){return n.textContent})`, &labels)
	return
}

func TestRealUITabMenuKeyboardAndClose(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	var r struct{ X, Y float64 }
	h.chromeEval(`(function(){var b=document.querySelector('#tabs .tab').getBoundingClientRect();return {X:b.left+b.width/2,Y:b.top+b.height/2}})()`, &r)
	h.rightClick(r.X, r.Y)
	h.waitFor("tab menu in the chrome", func() bool { return len(menuDOM(h)) > 5 })
	ls := menuDOM(h)
	for _, w := range []string{"New tab to the right", "Reload", "Duplicate", "Pin tab", "Mute site", "Close tab", "Close other tabs", "Close tabs to the right", "Reopen closed tab"} {
		if !hasLabel(ls, w) {
			t.Fatalf("tab menu lacks %q: %v", w, ls)
		}
	}
	h.shot("ctx-tab.png")
	// the scrim makes the whole window the chrome's while the menu is open
	if tgt := h.targetAt(600, 400); tgt != "chrome" {
		t.Fatalf("pointer target with a menu open: %q", tgt)
	}
	// Esc closes it
	h.key("Escape")
	if n := len(menuDOM(h)); n != 0 {
		t.Fatalf("menu still open after Escape (%d rows)", n)
	}
	// arrows + Enter: Down x3 -> Duplicate (New tab, Reload, Duplicate)
	h.rightClick(r.X, r.Y)
	h.waitFor("tab menu", func() bool { return len(menuDOM(h)) > 5 })
	for i := 0; i < 3; i++ {
		h.key("ArrowDown")
	}
	n := len(h.tabIDs())
	h.key("Enter")
	h.waitFor("duplicate", func() bool { return len(h.tabIDs()) == n+1 })
	if n := len(menuDOM(h)); n != 0 {
		t.Fatal("menu must close after a choice")
	}
	// outside click closes
	h.rightClick(r.X, r.Y)
	h.waitFor("tab menu", func() bool { return len(menuDOM(h)) > 5 })
	h.click(900, 500)
	if n := len(menuDOM(h)); n != 0 {
		t.Fatal("outside click must close the menu")
	}
}

func TestRealUIPageMenuRendersOverViewport(t *testing.T) {
	h := realUIWithCtxPage(t)
	h.pageRightClick(650, 350)
	h.wantMenu("page", "Reload")
	h.waitFor("menu drawn", func() bool { return len(menuDOM(h)) >= 5 })
	vp := h.vp()
	if a := h.app.comp.ChromeAlphaAt(int(vp.X)+655, int(vp.Y)+360); a == 0 {
		t.Fatal("the menu must be composited over the page area")
	}
	h.shot("ctx-page.png")
	h.key("ArrowDown")
	h.key("ArrowDown")
	h.shot("ctx-page-keyboard.png")
	h.key("Escape")
	if h.ctxOpen() || len(menuDOM(h)) != 0 {
		t.Fatal("Escape must close the menu and tell Go")
	}
	// dark theme
	h.loop(func() { _ = h.app.prof.Settings.Set("theme", "dark") })
	h.idle()
	h.pageRightClick(30, 90)
	h.wantMenu("image", "Copy image")
	h.waitFor("menu drawn", func() bool { return len(menuDOM(h)) >= 5 })
	h.shot("ctx-image-dark.png")
}

func realUIWithCtxPage(t *testing.T) *harness {
	srv := ctxServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Ctx")
	h.waitUIReady()
	return h
}

func TestRealUIOmniboxMenu(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	var r struct{ X, Y float64 }
	h.chromeEval(`(function(){var b=document.getElementById('url').getBoundingClientRect();return {X:b.left+20,Y:b.top+b.height/2}})()`, &r)
	h.rightClick(r.X, r.Y)
	h.waitFor("omnibox menu", func() bool { return len(menuDOM(h)) > 5 })
	ls := menuDOM(h)
	for _, w := range []string{"Undo", "Cut", "Copy", "Paste", "Paste and go", "Select all"} {
		if !hasLabel(ls, w) {
			t.Fatalf("omnibox menu lacks %q: %v", w, ls)
		}
	}
	h.shot("ctx-omnibox.png")
	h.win.SetClipboard(srv.URL + "/blank")
	var row int
	h.chromeEval(`SW.$$(".ctxm .ctxi:not(.dis) .lb").map(function(n){return n.textContent}).indexOf("Paste and go")`, &row)
	for i := 0; i <= row; i++ {
		h.key("ArrowDown")
	}
	h.key("Enter")
	h.waitTitle("Blank Target")
}

func TestRealUIBookmarksBarMenu(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	_ = h.app.prof.Settings.Set("bookmarks.bar", true)
	b, _ := h.app.prof.Bookmarks.Add("bar", "Blank", srv.URL+"/blank")
	h.loop(func() { h.app.markState() })
	h.waitFor("bar item", func() bool {
		var n float64
		h.chromeEval(`SW.$$("#bmbar button[data-id]").length`, &n)
		return n == 1
	})
	var r struct{ X, Y float64 }
	h.chromeEval(`(function(){var b=document.querySelector('#bmbar button').getBoundingClientRect();return {X:b.left+10,Y:b.top+b.height/2}})()`, &r)
	h.rightClick(r.X, r.Y)
	h.waitFor("bookmark menu", func() bool { return len(menuDOM(h)) == 4 })
	if ls := menuDOM(h); !hasLabel(ls, "Open in new tab") || !hasLabel(ls, "Edit…") || !hasLabel(ls, "Delete") {
		t.Fatalf("bookmark menu %v", ls)
	}
	h.shot("ctx-bookmark.png")
	h.uiPost("bookmark.update", map[string]any{"id": b.ID, "title": "Renamed", "url": srv.URL + "/two"})
	if got := h.app.prof.Bookmarks.Children("bar")[0]; got.Title != "Renamed" || got.URL != srv.URL+"/two" {
		t.Fatalf("edit gave %+v", got)
	}
	h.uiPost("bookmark.remove", map[string]any{"id": b.ID})
	if len(h.app.prof.Bookmarks.Children("bar")) != 0 {
		t.Fatal("delete failed")
	}
}

func TestRealUIStripEmptyAreaMenu(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	var r struct{ X, Y float64 }
	h.chromeEval(`(function(){var b=document.getElementById('drag').getBoundingClientRect();return {X:b.left+b.width/2,Y:b.top+b.height/2}})()`, &r)
	h.rightClick(r.X, r.Y)
	h.waitFor("strip menu", func() bool { return len(menuDOM(h)) == 2 })
	if ls := menuDOM(h); !hasLabel(ls, "New tab") || !hasLabel(ls, "Reopen closed tab") {
		t.Fatalf("strip menu %v", ls)
	}
	h.shot("ctx-strip.png")
}

func TestRealUINewTabTileAndBackgroundMenus(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{})
	h.app.prof.History.Visit(srv.URL+"/two", "Page Two", true)
	h.waitUIReady()
	h.loop(func() { h.app.markState() })
	h.waitFor("tile", func() bool {
		var n float64
		h.pageEval(`document.querySelectorAll('#sites .site').length`, &n)
		return n >= 1
	})
	var r struct{ X, Y float64 }
	h.pageEval(`(function(){var b=document.querySelector('#sites .site').getBoundingClientRect();return {X:b.left+b.width/2,Y:b.top+b.height/2}})()`, &r)
	h.pageRightClick(r.X, r.Y)
	h.waitFor("tile menu", func() bool { return h.ctxOpen() })
	for _, w := range []string{"Open", "Open in new tab", "Edit shortcut…", "Remove"} {
		if !hasLabel(h.ctxLabels(), w) {
			t.Fatalf("tile menu lacks %q: %v", w, h.ctxLabels())
		}
	}
	h.waitFor("tile menu drawn", func() bool { return len(menuDOM(h)) == 4 })
	h.shot("ctx-newtab-tile.png")
	h.ctxPick("Remove")
	h.waitFor("tile gone", func() bool {
		var n float64
		h.pageEval(`document.querySelectorAll('#sites .site').length`, &n)
		return n == 0
	})
	h.pageRightClick(900, 560)
	h.waitFor("background menu", func() bool { return h.ctxOpen() })
	for _, w := range []string{"Customize this page", "Reload"} {
		if !hasLabel(h.ctxLabels(), w) {
			t.Fatalf("background menu lacks %q: %v", w, h.ctxLabels())
		}
	}
	h.shot("ctx-newtab-bg.png")
}

func TestRealUIHistoryRowMenu(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{"sweef://history"}})
	h.app.prof.History.Visit(srv.URL+"/two", "Page Two", true)
	h.waitUIReady()
	h.loop(func() { h.app.newTab("sweef://history", false, true) })
	h.waitFor("history row", func() bool {
		var n float64
		h.pageEval(`document.querySelectorAll('.lrow').length`, &n)
		return n >= 1
	})
	var r struct{ X, Y float64 }
	h.pageEval(`(function(){var b=document.querySelector('.lrow').getBoundingClientRect();return {X:b.left+200,Y:b.top+b.height/2}})()`, &r)
	h.pageRightClick(r.X, r.Y)
	h.waitFor("history menu", func() bool { return h.ctxOpen() })
	for _, w := range []string{"Open", "Open in new tab", "Copy link address", "Remove from history"} {
		if !hasLabel(h.ctxLabels(), w) {
			t.Fatalf("history menu lacks %q: %v", w, h.ctxLabels())
		}
	}
	h.waitFor("history menu drawn", func() bool { return len(menuDOM(h)) == 4 })
	h.shot("ctx-history.png")
	h.ctxPick("Copy link address")
	if got := h.win.Clipboard(); got != srv.URL+"/two" {
		t.Fatalf("clipboard %q", got)
	}
}

func TestRealUISidekickMenus(t *testing.T) {
	srv := pageServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/two"}})
	h.waitTitle("Page Two")
	h.waitUIReady()
	h.loop(func() { _ = h.app.prof.Settings.Set("sidebar.open", true) })
	defer func() {
		if t.Failed() {
			var dbg any
			h.chromeEval(`({sk: !!SW.sidekick, open: SW.setting("sidebar.open"), el: !!document.getElementById('skin'), w: document.getElementById('sidebar') && document.getElementById('sidebar').getBoundingClientRect().width})`, &dbg)
			t.Logf("debug %v", dbg)
		}
	}()
	h.waitFor("sidekick input", func() bool {
		h.chromeEval(`SW.sk.focus(); 1`, nil)
		var r struct{ W float64 }
		h.chromeEval(`(function(){var e=document.getElementById('skin');if(!e)return {W:0};var b=e.getBoundingClientRect();return {W:b.width}})()`, &r)
		return r.W > 10
	})
	var r struct{ X, Y float64 }
	h.chromeEval(`(function(){var b=document.getElementById('skin').getBoundingClientRect();return {X:b.left+20,Y:b.top+b.height/2}})()`, &r)
	h.rightClick(r.X, r.Y)
	h.waitFor("input menu", func() bool { return len(menuDOM(h)) >= 6 })
	for _, w := range []string{"Cut", "Copy", "Paste", "Select all"} {
		if !hasLabel(menuDOM(h), w) {
			t.Fatalf("sidekick input menu lacks %q: %v", w, menuDOM(h))
		}
	}
	h.shot("ctx-sidekick-input.png")
	h.key("Escape")
	// a message
	h.chromeEval(`(function(){var f=document.getElementById('skfeed');var m=document.createElement('div');m.className='um';var b=document.createElement('div');b.className='bub';b.textContent='hello sidekick';m.appendChild(b);f.appendChild(m);return 1})()`, nil)
	h.idle()
	h.chromeEval(`(function(){var b=document.querySelector('#skfeed .um .bub').getBoundingClientRect();return {X:b.left+10,Y:b.top+b.height/2}})()`, &r)
	h.rightClick(r.X, r.Y)
	h.waitFor("message menu", func() bool { return len(menuDOM(h)) == 4 })
	for _, w := range []string{"Copy message", "Copy as markdown", "Select all", "Clear conversation"} {
		if !hasLabel(menuDOM(h), w) {
			t.Fatalf("message menu lacks %q: %v", w, menuDOM(h))
		}
	}
	h.shot("ctx-sidekick-message.png")
}

func TestRealUIRightClickElsewhereReopens(t *testing.T) {
	h := realUIWithCtxPage(t)
	h.pageRightClick(650, 350)
	h.wantMenu("page", "Reload")
	h.waitFor("menu drawn", func() bool { return len(menuDOM(h)) >= 5 })
	h.pageRightClick(50, 30) // a link while the page menu is open: the scrim closes it and the link menu opens
	h.waitFor("link menu", func() bool { return hasLabel(h.ctxLabels(), "Copy link address") })
	h.waitFor("link menu drawn", func() bool { return hasLabel(menuDOM(h), "Copy link address") })
}

func TestCtxStalePickIsIgnored(t *testing.T) {
	h, _ := newCtxHarness(t)
	h.pageRightClick(50, 30)
	h.wantMenu("link")
	n := len(h.tabIDs())
	h.uiPost("ctx.pick", map[string]any{"token": 9999, "id": "m1"})
	if len(h.tabIDs()) != n {
		t.Fatal("a pick for another menu must do nothing")
	}
	h.ctxPick("Copy link address")
}
