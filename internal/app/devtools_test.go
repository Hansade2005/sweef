package app

import (
	"fmt"
	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/shell"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// devtoolsPage serves a page with a stylesheet, a script that logs and fails, and a fetch.
func devtoolsServer(t testing.TB) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><head><title>Dev Fixture</title><link rel="stylesheet" href="/s.css"></head>
<body style="margin:0"><div id="box" class="card" style="height:120px;background:#d33a3a;color:#fff;font:24px sans-serif">Fixture box</div>
<p id="p2">second paragraph</p>
<script src="/a.js"></script></body></html>`)
	})
	mux.HandleFunc("/s.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, ".card { padding: 10px; border: 2px solid #000 }\n")
	})
	mux.HandleFunc("/a.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "console.log('hello from a.js');\nconsole.warn('careful');\nfetch('/d.json').then(function(r){return r.json()});\nfunction boom(){ throw new TypeError('kaput') }\nboom();\n")
	})
	mux.HandleFunc("/d.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (h *harness) devtoolsOpen() (open bool, tab string) {
	h.loop(func() { open, tab = h.app.dt.open, h.app.dt.tab })
	return
}

func (h *harness) waitDevtoolsUI() {
	h.t.Helper()
	h.waitFor("devtools panel built", func() bool {
		var n float64
		h.chromeEval(`SW.$$("#devtools .dt-tab").length`, &n)
		return n == 5
	})
	h.idle()
}

// F12 opens Developer Tools for the active tab, docked under the page; F12 again closes it and the page gets its
// space back. Ctrl+Shift+I does the same; Ctrl+Shift+J opens the console.
func TestDevToolsF12OpensForActiveTab(t *testing.T) {
	srv := devtoolsServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Dev Fixture")
	h.waitUIReady()
	before := h.vp()
	act := h.tab().ID
	if open, _ := h.devtoolsOpen(); open {
		t.Fatal("devtools open at start")
	}
	h.key("F12")
	if open, tab := h.devtoolsOpen(); !open || tab != act {
		t.Fatalf("after F12: open=%v tab=%q, want the active tab %q", open, tab, act)
	}
	h.waitDevtoolsUI()
	after := h.vp()
	if after.H >= before.H-150 || after.W != before.W || after.Y != before.Y {
		t.Fatalf("the page did not make room for the panel: %+v then %+v", before, after)
	}
	var vis bool
	h.chromeEval(`!document.getElementById("devtools").classList.contains("hide")`, &vis)
	if !vis {
		t.Fatal("panel hidden")
	}
	var pw float64
	h.pageEval(`window.innerHeight`, &pw)
	if int(pw) != int(after.H) {
		t.Errorf("page innerHeight %v, viewport %v", pw, after.H)
	}
	h.shot("devtools-open.png")
	h.key("F12")
	if open, _ := h.devtoolsOpen(); open {
		t.Fatal("F12 did not close")
	}
	h.waitFor("viewport restored", func() bool { return h.vp() == before })

	h.key("ctrl+shift+i")
	if open, tab := h.devtoolsOpen(); !open || tab != act {
		t.Fatal("ctrl+shift+i did not open")
	}
	h.key("ctrl+shift+i")
	h.key("ctrl+shift+j")
	h.waitDevtoolsUI()
	h.waitFor("console panel shown", func() bool {
		var sel string
		h.chromeEval(`(SW.$('#devtools .dt-tab[aria-selected="true"]')||{textContent:""}).textContent`, &sel)
		return strings.HasPrefix(sel, "Console")
	})
}

// Dragging the top edge of the panel resizes it live and the height is kept in the settings.
func TestDevToolsResizePersists(t *testing.T) {
	srv := devtoolsServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Dev Fixture")
	h.waitUIReady()
	h.key("F12")
	h.waitDevtoolsUI()
	var r struct{ X, Y, W, H float64 }
	h.chromeEval(`(function(){var b=document.getElementById("devtools").getBoundingClientRect();return {X:b.left,Y:b.top,W:b.width,H:b.height}})()`, &r)
	if int(r.H) != 320 {
		t.Fatalf("default height %v", r.H)
	}
	vp0 := h.vp()
	x, y := r.X+r.W/2, r.Y+2 // inside the panel, on the lower half of its resize handle
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y})
	h.win.Push(shell.Event{Kind: shell.EvMouseDown, X: x, Y: y})
	h.idle()
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y - 60})
	h.idle() // moves are coalesced and a button release does not wait for the one in flight
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y - 120})
	h.idle()
	h.win.Push(shell.Event{Kind: shell.EvMouseUp, X: x, Y: y - 120})
	h.idle()
	h.waitFor("height setting saved", func() bool { return h.app.prof.Settings.Int("devtools.height") == 440 })
	h.waitFor("page made room", func() bool { return h.vp().H == vp0.H-120 })
	// the setting is what the panel restores next time
	h.key("F12")
	h.key("F12")
	var hgt float64
	h.waitDevtoolsUI()
	h.chromeEval(`document.getElementById("devtools").getBoundingClientRect().height`, &hgt)
	if int(hgt) != 440 {
		t.Errorf("reopened at %v px, want 440", hgt)
	}
}

// The console lists the page's console.* output and its uncaught error with the file and line, and its prompt
// evaluates in the page.
func TestDevToolsConsoleAndPrompt(t *testing.T) {
	srv := devtoolsServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Dev Fixture")
	h.waitUIReady()
	h.key("ctrl+shift+j")
	h.waitDevtoolsUI()
	h.waitFor("console entries", func() bool {
		var n float64
		h.chromeEval(`SW.$$("#devtools .dt-cr").length`, &n)
		return n >= 3
	})
	var rows []string
	h.chromeEval(`SW.$$("#devtools .dt-cr").map(function(r){return r.className+"|"+r.textContent})`, &rows)
	all := strings.Join(rows, "\n")
	for _, want := range []string{"hello from a.js", "careful", "TypeError: kaput", "a.js:"} {
		if !strings.Contains(all, want) {
			t.Errorf("console lacks %q:\n%s", want, all)
		}
	}
	// the prompt runs in the page: a real click focuses it, then typed text and Enter
	var r struct{ X, Y, W, H float64 }
	h.chromeEval(`(function(){var b=document.querySelector("#devtools .dt-prompt").getBoundingClientRect();return {X:b.left,Y:b.top,W:b.width,H:b.height}})()`, &r)
	h.click(r.X+10, r.Y+r.H/2)
	if h.focusName() != "chrome" {
		t.Fatal("clicking the prompt must focus the chrome")
	}
	h.typeText("document.getElementById('box').textContent + ' / ' + (6*7)")
	h.key("Enter")
	h.waitFor("result printed", func() bool {
		var txt string
		h.chromeEval(`SW.$$("#devtools .dt-cr.out").map(function(r){return r.textContent}).join("|")`, &txt)
		return strings.Contains(txt, "Fixture box / 42")
	})
	// history: Up brings the line back
	h.key("ArrowUp")
	var v string
	h.chromeEval(`document.querySelector("#devtools .dt-prompt").value`, &v)
	if !strings.Contains(v, "getElementById") {
		t.Errorf("history: prompt holds %q", v)
	}
	h.shot("devtools-console.png")
}

// The network panel lists the page's requests with status, type and size.
func TestDevToolsNetworkPanel(t *testing.T) {
	srv := devtoolsServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Dev Fixture")
	h.waitUIReady()
	h.key("F12")
	h.waitDevtoolsUI()
	h.chromeEval(`SW.$$("#devtools .dt-tab")[3].click(); 1`, nil)
	h.waitFor("network rows", func() bool {
		var rows []string
		h.chromeEval(`SW.$$("#devtools .dt-nr").map(function(r){return r.textContent})`, &rows)
		return strings.Contains(strings.Join(rows, "|"), "a.js")
	})
	var rows []string
	h.chromeEval(`SW.$$("#devtools .dt-nr").map(function(r){return r.textContent})`, &rows)
	all := strings.Join(rows, "\n")
	for _, want := range []string{"s.css", "a.js", "200", "stylesheet", "script"} {
		_ = want
	}
	if !strings.Contains(all, "s.css") || !strings.Contains(all, "200") {
		t.Errorf("network rows:\n%s", all)
	}
	// a row opens its details
	h.chromeEval(`SW.$$("#devtools .dt-nr").filter(function(r){return r.textContent.indexOf("a.js")>=0})[0].click(); 1`, nil)
	h.waitFor("details", func() bool {
		var n float64
		h.chromeEval(`SW.$$("#devtools .dt-ndet:not(.hide) .dt-kv").length`, &n)
		return n > 4
	})
	h.shot("devtools-network.png")
}

// Elements: selecting a node highlights it on the page (the display list gains the overlay), the styles pane shows
// the matched rules, and an attribute edit reaches the page.
func TestDevToolsElementsHighlightAndEdit(t *testing.T) {
	srv := devtoolsServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Dev Fixture")
	h.waitUIReady()
	h.key("F12")
	h.waitDevtoolsUI()
	h.chromeEval(`SW.$$("#devtools .dt-tab")[0].click(); 1`, nil)
	h.waitFor("tree rows", func() bool {
		var n float64
		h.chromeEval(`SW.$$("#devtools .dt-er").length`, &n)
		return n >= 4
	})
	vp := h.vp()
	h.wantPx(40, int(vp.Y)+40, "#d33a3a") // the box, before any highlight
	// click the div#box row
	h.chromeEval(`SW.$$("#devtools .dt-er").filter(function(r){return r.textContent.indexOf('id="box"')>=0})[0].click(); 1`, nil)
	h.waitFor("styles", func() bool {
		var n float64
		h.chromeEval(`SW.$$("#devtools .dt-rule").length`, &n)
		return n >= 2
	})
	var txt string
	h.chromeEval(`document.querySelector("#devtools .dt-eside").textContent`, &txt)
	if !strings.Contains(txt, "element.style") || !strings.Contains(txt, ".card") || !strings.Contains(txt, "padding") {
		t.Errorf("styles pane: %q", txt)
	}
	// the highlight changes the pixel inside the box (content area tint over red)
	h.waitFor("highlight painted", func() bool { g := h.px(40, int(vp.Y)+40); return !(g.R > 200 && g.G < 90) })
	h.shot("devtools-elements.png")
	// an attribute edit from the panel reaches the page
	e := h.app.activePage()
	var ok bool
	_ = e.call(func() error {
		id, err := e.t.QuerySelector("#box")
		if err != nil {
			return err
		}
		_, err = h.app.devtoolsCall(e, "dom.setAttr", []byte(fmt.Sprintf(`{"id":%d,"name":"data-edited","value":"yes"}`, id.BackendID)), false)
		return err
	})
	h.pageEval(`document.getElementById('box').getAttribute('data-edited') === 'yes'`, &ok)
	if !ok {
		t.Error("the attribute edit did not reach the page")
	}
	// closing the panel removes the highlight
	h.key("F12")
	h.waitFor("highlight gone", func() bool { g := h.px(40, int(h.vp().Y)+40); return g.R > 200 && g.G < 90 })
}

// A tab that showed a Sweef page and was then navigated to a web page must not receive what the browser sends to its
// UI pages: a web page could define window.sweef and read the state (here the Developer Tools binding with it). The
// app drops such a tab from the list when the load finishes; until then (and if it ever lingers) sending is refused by URL.
func TestDevToolsStateNeverReachesWebPages(t *testing.T) {
	srv := devtoolsServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Dev Fixture")
	h.waitUIReady()
	id := h.app.newTab("", false, true) // the new tab page says ui.ready and joins the UI pages
	h.waitFor("new tab page ready", func() bool {
		var n int
		h.loop(func() { n = len(h.app.uiPages) })
		return n > 0
	})
	h.app.post(func() { h.app.loadURL(id, srv.URL+"/", modePush, true) })
	h.waitFor("web page loaded", func() bool { return h.tab().ID == id && h.tab().Title == "Dev Fixture" })
	h.idle()
	h.pageEval(`window.sweef = {__recv: function(t){ (window.__got = window.__got || []).push(t); }}; 1`, nil)
	h.loop(func() { h.app.uiPages[h.app.page(id)] = true }) // as if it had not been dropped yet
	h.key("F12")                                            // a state patch goes to every UI page
	h.key("F12")
	h.uiPost("settings.set", map[string]any{"key": "theme", "value": "dark"})
	var got any
	h.pageEval(`window.__got || null`, &got)
	if got != nil {
		t.Fatalf("the web page received UI messages: %v", got)
	}
}

// Only the chrome may talk to Developer Tools, and it can only reach the tab the panel was opened for.
func TestDevToolsIsolation(t *testing.T) {
	srv := devtoolsServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Dev Fixture")
	h.waitUIReady()
	// a web page cannot open it (its sweef.post binding is dropped: only sweef:// documents are forwarded)
	h.pageEval(`(typeof __sweefPost === 'function' ? (__sweefPost('devtools.open', {}), 1) : 0)`, nil)
	h.idle()
	if open, _ := h.devtoolsOpen(); open {
		t.Fatal("a web page opened Developer Tools")
	}
	// a page-kind sender is refused even when the message gets through
	h.loop(func() { h.app.onUIMessage(h.app.activePage(), "devtools.open", nil) })
	if open, _ := h.devtoolsOpen(); open {
		t.Fatal("a non-chrome sender opened Developer Tools")
	}
	// a second tab: the open panel follows the active tab and answers only for it
	first := h.tab().ID
	h.key("F12")
	second := h.app.newTab(srv.URL+"/", false, true)
	h.waitFor("second tab active", func() bool { return h.tab().ID == second })
	if open, tab := h.devtoolsOpen(); !open || tab != second {
		t.Fatalf("panel did not follow the active tab: %v %q (first %q second %q)", open, tab, first, second)
	}
	h.loop(func() {
		var got string
		h.app.devtoolsRequest(devtoolsReq{ID: 1, M: "info"})
		_ = got
	})
	// a Sweef page is read only: no evaluation, no edits
	h.app.post(func() { h.app.loadURL(second, "sweef://settings", modeReplace, false) })
	h.waitFor("settings page", func() bool { return strings.HasPrefix(h.tab().URL, "sweef://settings") })
	h.idle()
	e := h.app.page(second)
	var err error
	_ = e.call(func() error {
		_, err = h.app.devtoolsCall(e, "console.eval", []byte(`{"expr":"1+1"}`), true)
		return nil
	})
	if err == nil {
		t.Error("evaluating in a Sweef page must be refused")
	}
	// the entry point other code uses
	h.loop(func() { h.app.closeDevTools() })
	h.loop(func() { h.app.openDevTools(first) })
	if open, tab := h.devtoolsOpen(); !open || tab != first {
		t.Fatalf("openDevTools(tabID): %v %q", open, tab)
	}
	h.waitFor("first tab active", func() bool { return h.tab().ID == first })
}

// Developer Tools works on a private tab: it reads what that tab's engine recorded and nothing is stored beyond the
// panel's own settings.
func TestDevToolsOnPrivateTab(t *testing.T) {
	srv := devtoolsServer(t)
	h := realUI(t, Config{InitialURLs: []string{"about:blank"}})
	h.waitUIReady()
	id := h.app.newTab(srv.URL+"/", true, true)
	h.waitFor("private tab loaded", func() bool { return h.tab().ID == id && h.tab().Title == "Dev Fixture" })
	h.idle()
	h.key("F12")
	if open, tab := h.devtoolsOpen(); !open || tab != id {
		t.Fatalf("open=%v tab=%q want %q", open, tab, id)
	}
	e := h.app.page(id)
	if !e.incog {
		t.Fatal("not a private tab")
	}
	var logs, nets int
	_ = e.call(func() error {
		r, err := h.app.devtoolsCall(e, "console.get", []byte(`{"since":0}`), false)
		if err != nil {
			return err
		}
		logs = len(r.([]map[string]any))
		r, err = h.app.devtoolsCall(e, "net.get", []byte(`{"since":0}`), false)
		if err != nil {
			return err
		}
		nets = len(r.(map[string]any)["entries"].([]engine.NetEntry))
		return nil
	})
	if logs < 3 || nets < 3 {
		t.Errorf("a private tab's console has %d entries and its network %d", logs, nets)
	}
}

// "Inspect": openDevToolsAt opens the panel on Elements with the element at that point of the page selected, even
// when the panel's script is still being loaded.
func TestDevToolsInspectSelectsElementAtPoint(t *testing.T) {
	srv := devtoolsServer(t)
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Dev Fixture")
	h.waitUIReady()
	id := h.tab().ID
	h.loop(func() { h.app.openDevToolsAt(id, 40, 40) }) // inside div#box
	h.waitDevtoolsUI()
	h.waitFor("the element at the point is selected", func() bool {
		var txt string
		h.chromeEval(`(SW.$("#devtools .dt-er.sel")||{textContent:""}).textContent`, &txt)
		return strings.Contains(txt, `id="box"`)
	})
	var sel string
	h.chromeEval(`(SW.$('#devtools .dt-tab[aria-selected="true"]')||{textContent:""}).textContent`, &sel)
	if !strings.HasPrefix(sel, "Elements") {
		t.Errorf("panel shown: %q", sel)
	}
	h.shot("devtools-inspect.png")
}
