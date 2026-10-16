package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// devPage is the fixture of the Developer Tools tests: a page with a linked stylesheet, a linked script, an inline
// style, an inline script, an image, a fetch and an uncaught error.
func devServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><html><head><title>Dev Page</title>
<link rel="stylesheet" href="/site.css">
<style>
#box { color: green; }
@media (min-width: 100px) { .big { font-size: 20px; } }
@media (max-width: 10px) { .big { font-size: 99px; } }
</style></head>
<body><div id="box" class="big card" style="margin-top: 7px; color: red" data-x="1">Hello <b>world</b><!-- note --></div>
<p class="card">second</p>
<img src="/pic.png" width="8" height="8">
<script src="/app.js"></script>
<script>console.log("inline ran"); console.warn("careful");</script>
</body></html>`)
	})
	mux.HandleFunc("/site.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, "body { margin: 0 }\n.card { padding: 4px; color: blue }\n.big { color: purple !important; border: 1px solid #000 }\n")
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "max-age=60")
		fmt.Fprint(w, "window.appLoaded = 1;\nconsole.log(\"app says hi\");\nfetch('/data.json').then(function(r){return r.json()}).then(function(j){window.got = j.n});\nfetch('/missing').catch(function(){});\n")
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/data.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"n": 42}`)
	})
	mux.HandleFunc("/pic.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG\r\n\x1a\n"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func devTab(t *testing.T) (*Tab, *httptest.Server) {
	t.Helper()
	srv := devServer(t)
	e := newEng(t)
	tab, err := e.NewTab("")
	if err != nil {
		t.Fatal(err)
	}
	tab.DevAttach(true)
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	tab.Settle(300 * time.Millisecond)
	tab.PaintRGBA(400, 300, 1, false) // layout starts the image fetch
	tab.Settle(200 * time.Millisecond)
	return tab, srv
}

func TestDevNetworkRecorder(t *testing.T) {
	tab, srv := devTab(t)
	byURL := map[string]NetEntry{}
	for _, e := range tab.DevNetwork(0) {
		byURL[strings.TrimPrefix(e.URL, srv.URL)] = e
	}
	doc, ok := byURL["/"]
	if !ok || doc.Type != "document" || doc.Status != 200 || doc.Method != "GET" || !doc.HasBody || doc.MIME != "text/html" {
		t.Fatalf("document entry: %+v (all: %v)", doc, byURL)
	}
	if doc.ReqHeaders["User-Agent"] == "" || doc.RespHeaders["Content-Type"] == "" {
		t.Errorf("headers missing: %v / %v", doc.ReqHeaders, doc.RespHeaders)
	}
	css := byURL["/site.css"]
	if css.Type != "style" || css.Status != 200 || css.BodySize == 0 || !css.HasBody || css.Nav != doc.Nav {
		t.Errorf("stylesheet entry: %+v", css)
	}
	js := byURL["/app.js"]
	if js.Type != "script" || js.Status != 200 || js.DurMs < 0 || js.Pending || js.StartMs == 0 {
		t.Errorf("script entry: %+v", js)
	}
	if j := byURL["/data.json"]; j.Type != "xhr" || j.MIME != "application/json" || !j.HasBody {
		t.Errorf("fetch entry: %+v", j)
	} else if body, _, ok := tab.DevNetBody(j.ID); !ok || string(body) != `{"n": 42}` {
		t.Errorf("fetch body: %q %v", body, ok)
	}
	if m := byURL["/missing"]; m.Status != 404 {
		t.Errorf("404 entry: %+v", m)
	}
	if p := byURL["/pic.png"]; p.Type != "image" {
		t.Errorf("image entry: %+v", p)
	}
	// the new entries after a known id
	last := tab.DevNetwork(0)
	if got := tab.DevNetwork(last[len(last)-1].ID); len(got) != 0 {
		t.Errorf("nothing newer expected, got %d", len(got))
	}
	// a second load: new navigation number, and the cache state of a reused script is recorded
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	tab.Settle(300 * time.Millisecond)
	var js2 NetEntry
	for _, e := range tab.DevNetwork(0) {
		if strings.HasSuffix(e.URL, "/app.js") {
			js2 = e
		}
	}
	if js2.Nav <= js.Nav {
		t.Errorf("second navigation did not get a new number: %d then %d", js.Nav, js2.Nav)
	}
	if js2.Cache == "" {
		t.Errorf("second load of a max-age script should note the cache outcome: %+v", js2)
	}
	tab.DevClearNetwork()
	if n := len(tab.DevNetwork(0)); n != 0 {
		t.Errorf("clear: %d left", n)
	}
}

func TestDevNetworkBlockedAndBodyBudget(t *testing.T) {
	srv := devServer(t)
	e := newEng(t)
	tab, _ := e.NewTab("")
	tab.Route("**/site.css", func(r *http.Request) (int, string, bool) {
		tab.DevNoteBlocked(r.URL.String(), "tracker blocker")
		return 0, "", true
	})
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	tab.Settle(200 * time.Millisecond)
	var blocked NetEntry
	for _, en := range tab.DevNetwork(0) {
		if strings.HasSuffix(en.URL, "/site.css") {
			blocked = en
		}
	}
	if !blocked.Blocked || blocked.Reason != "tracker blocker" || blocked.Status != 0 || blocked.Error == "" {
		t.Fatalf("blocked entry: %+v", blocked)
	}
	// without DevAttach only documents, scripts and styles keep a body
	for _, en := range tab.DevNetwork(0) {
		if (en.Type == "xhr" || en.Type == "image") && en.HasBody {
			t.Errorf("%s %s kept a body without Developer Tools open", en.Type, en.URL)
		}
	}
	if _, _, ok := tab.DevNetBody(blocked.ID); ok {
		t.Error("a blocked request has no body")
	}
}

func TestDevSources(t *testing.T) {
	tab, srv := devTab(t)
	src := tab.DevSources()
	kinds := map[string]int{}
	var appKey, cssKey, inlineScript, inlineStyle string
	for _, s := range src {
		kinds[s.Kind]++
		switch {
		case strings.HasSuffix(s.URL, "/app.js"):
			appKey = s.Key
		case strings.HasSuffix(s.URL, "/site.css"):
			cssKey = s.Key
		case s.Inline && s.Kind == "script":
			inlineScript = s.Key
		case s.Inline && s.Kind == "style":
			inlineStyle = s.Key
		}
	}
	if kinds["document"] != 1 || kinds["script"] != 2 || kinds["style"] != 2 {
		t.Fatalf("sources by kind %v: %+v", kinds, src)
	}
	for key, want := range map[string]string{appKey: "window.appLoaded = 1;", cssKey: ".card { padding: 4px", inlineScript: "inline ran", inlineStyle: "#box { color: green; }"} {
		txt, err := tab.DevSource(key)
		if err != nil || !strings.Contains(txt, want) {
			t.Errorf("source %q: %q, %v (want %q)", key, txt, err, want)
		}
	}
	if _, err := tab.DevSource("net:99999"); err == nil {
		t.Error("unknown source should fail")
	}
	_ = srv
}

func TestDevDOMTreeAndEdit(t *testing.T) {
	tab, _ := devTab(t)
	root, err := tab.DevDOMRoot()
	if err != nil || root.Name != "#document" || root.Kids == 0 {
		t.Fatalf("root: %+v %v", root, err)
	}
	// find #box through the tree, the way the panel would: html > body > div
	b, err := tab.QuerySelector("#box")
	if err != nil {
		t.Fatal(err)
	}
	boxID := b.BackendID
	n, err := tab.DevDOMNode(boxID)
	if err != nil || n.Name != "div" {
		t.Fatalf("node: %+v %v", n, err)
	}
	attrs := map[string]string{}
	for _, a := range n.Attrs {
		attrs[a[0]] = a[1]
	}
	if attrs["id"] != "box" || attrs["data-x"] != "1" || !strings.Contains(attrs["class"], "card") {
		t.Errorf("attrs: %v", n.Attrs)
	}
	kids, err := tab.DevDOMChildren(boxID, 0)
	if err != nil || len(kids) != 3 || kids[0].Type != 3 || kids[0].Value != "Hello " || kids[1].Name != "b" || kids[2].Type != 8 || kids[2].Value != " note " {
		t.Fatalf("children: %+v %v", kids, err)
	}
	path, err := tab.DevDOMPath(boxID)
	if err != nil || len(path) < 4 {
		t.Errorf("path: %v %v", path, err)
	}
	// edits are made as page script would make them: a MutationObserver is told
	if _, err := tab.DevEval(`window.moSeen = []; new MutationObserver(function(rs){ rs.forEach(function(r){ moSeen.push(r.type + ":" + (r.attributeName || "")) }) }).observe(document.body, {attributes: true, characterData: true, childList: true, subtree: true}); 1`); err != nil {
		t.Fatal(err)
	}
	// edits are live: the page sees them and the document changes
	if err := tab.DevSetAttr(boxID, "data-x", "2"); err != nil {
		t.Fatal(err)
	}
	if err := tab.DevSetAttr(boxID, "bad name", "x"); err == nil {
		t.Error("an invalid attribute name must be refused")
	}
	if err := tab.DevSetText(kids[0].ID, "Bye "); err != nil {
		t.Fatal(err)
	}
	r, err := tab.DevEval(`document.getElementById('box').dataset.x + '|' + document.getElementById('box').firstChild.data`)
	if err != nil || !r.OK || r.Text != "2|Bye " {
		t.Errorf("after edit: %+v %v", r, err)
	}
	if err := tab.DevRemoveAttr(boxID, "data-x"); err != nil {
		t.Fatal(err)
	}
	if v, _ := tab.DevEval(`document.getElementById('box').hasAttribute('data-x')`); v.Text != "false" {
		t.Errorf("attribute not removed: %+v", v)
	}
	if err := tab.DevRemoveNode(kids[1].ID); err != nil {
		t.Fatal(err)
	}
	tab.Settle(50 * time.Millisecond)
	if mo, _ := tab.DevEval(`moSeen.join()`); !strings.Contains(mo.Text, "attributes:data-x") || !strings.Contains(mo.Text, "childList:") {
		t.Errorf("a MutationObserver should have seen the panel's edits: %q", mo.Text)
	}
	if v, _ := tab.DevEval(`document.querySelectorAll('#box b').length`); v.Text != "0" {
		t.Errorf("node not removed: %+v", v)
	}
	body, _ := tab.QuerySelector("body")
	if err := tab.DevRemoveNode(body.BackendID); err == nil {
		t.Error("body must not be deletable")
	}
	if err := tab.DevSetAttr(999999, "a", "b"); err == nil {
		t.Error("unknown node must fail")
	}
}

func TestDevStyleMatchedComputedBox(t *testing.T) {
	tab, _ := devTab(t)
	b, _ := tab.QuerySelector("#box")
	st, err := tab.DevStyle(b.BackendID)
	if err != nil {
		t.Fatal(err)
	}
	var sels []string
	for _, r := range st.Rules {
		sels = append(sels, r.Selector)
	}
	// inline first, then by priority: the .big rule from the sheet loses to nothing it declares !important
	want := []string{"element.style", "#box", ".card", ".big", ".big"}
	if got := strings.Join(sels, " | "); !strings.Contains(got, "element.style") || !strings.Contains(got, "#box") || !strings.Contains(got, ".card") || !strings.Contains(got, ".big") {
		t.Fatalf("selectors %q, want to include %v", got, want)
	}
	if st.Rules[0].Selector != "element.style" {
		t.Errorf("element.style must come first: %v", sels)
	}
	// the max-width:10px media rule does not match the viewport and must not be listed
	for _, r := range st.Rules {
		for _, d := range r.Decls {
			if d.Name == "font-size" && d.Value == "99px" {
				t.Errorf("a non-matching @media rule was listed: %+v", r)
			}
		}
	}
	find := func(sel, prop string) *DevDecl {
		for i := range st.Rules {
			if st.Rules[i].Selector == sel {
				for j := range st.Rules[i].Decls {
					if st.Rules[i].Decls[j].Name == prop {
						return &st.Rules[i].Decls[j]
					}
				}
			}
		}
		return nil
	}
	// color: .big sets it !important (wins), inline red and #box green and .card blue lose
	if d := find(".big", "color"); d == nil || d.Overridden || !d.Important {
		t.Errorf(".big color: %+v", d)
	}
	for _, sel := range []string{"element.style", "#box", ".card"} {
		if d := find(sel, "color"); d == nil || !d.Overridden {
			t.Errorf("%s color should be overridden: %+v", sel, d)
		}
	}
	if d := find("element.style", "margin-top"); d == nil || d.Overridden || d.Value != "7px" {
		t.Errorf("inline margin-top: %+v", d)
	}
	if d := find(".big", "font-size"); d == nil || d.Value != "20px" {
		t.Errorf("font-size from the matching @media: %+v", d)
	}
	for _, r := range st.Rules {
		if r.Selector == ".card" && (!strings.HasSuffix(r.Source, "/site.css") || r.Line != 2) {
			t.Errorf(".card source: %+v", r)
		}
		if r.Selector == "#box" && (r.Source != "<style> 1" || r.Line != 2) {
			t.Errorf("#box source: %+v", r)
		}
	}
	comp := map[string]string{}
	for _, kv := range st.Computed {
		comp[kv[0]] = kv[1]
	}
	if comp["color"] != "rgb(128, 0, 128)" || comp["display"] != "block" || comp["margin-top"] != "7px" || comp["padding-top"] != "4px" || comp["font-size"] != "20px" {
		t.Errorf("computed: color=%q display=%q margin-top=%q padding-top=%q font-size=%q", comp["color"], comp["display"], comp["margin-top"], comp["padding-top"], comp["font-size"])
	}
	if st.Box == nil || st.Box.Margin[0] != 7 || st.Box.Padding[0] != 4 || st.Box.Border[0] != 1 || st.Box.W <= 0 || st.Box.H <= 0 {
		t.Errorf("box: %+v", st.Box)
	}
	// a hidden element has no box
	if _, err := tab.DevEval(`document.querySelector('p').style.display = 'none'`); err != nil {
		t.Fatal(err)
	}
	p, _ := tab.QuerySelector("p")
	if hs, err := tab.DevStyle(p.BackendID); err != nil || !hs.Hidden || hs.Box != nil {
		t.Errorf("hidden element: %+v %v", hs, err)
	}
	if _, err := tab.DevStyle(424242); err == nil {
		t.Error("unknown node must fail")
	}
}

func TestDevHighlightPaints(t *testing.T) {
	tab, _ := devTab(t)
	b, _ := tab.QuerySelector("#box")
	before, ok := tab.PaintRGBA(400, 300, 1, false)
	if !ok {
		t.Skip("no render pipeline")
	}
	if !tab.DevHighlight(b.BackendID) {
		t.Fatal("highlight not applied")
	}
	if tab.DevHighlight(b.BackendID) {
		t.Error("setting the same highlight again must report no change")
	}
	if !tab.Dirty() {
		t.Error("a new highlight must make the tab dirty so the app repaints it")
	}
	after, _ := tab.PaintRGBA(400, 300, 1, false)
	diff := 0
	for i := 0; i < len(before.Pix) && i < len(after.Pix); i++ {
		if before.Pix[i] != after.Pix[i] {
			diff++
		}
	}
	if diff < 500 {
		t.Errorf("highlight changed only %d bytes", diff)
	}
	tab.DevHighlight(0)
	clear, _ := tab.PaintRGBA(400, 300, 1, false)
	for i := range before.Pix {
		if before.Pix[i] != clear.Pix[i] {
			t.Fatal("clearing the highlight must restore the page pixels")
		}
	}
}

func TestDevEvalREPL(t *testing.T) {
	tab, _ := devTab(t)
	cases := []struct {
		expr, text string
		ok         bool
	}{
		{`1 + 2`, "3", true},
		{`"hi"`, "hi", true},
		{`[1, "a", {b: 2}]`, `(3) [1, "a", {b: 2}]`, true},
		{`({a: 1, b: [true, null]})`, `{a: 1, b: (2) [true, null]}`, true},
		{`undefined`, "undefined", true},
		{`document.getElementById('box')`, "<div#box.big.card>", true},
		{`var zz = 5; zz * 2`, "10", true},
		{`zz`, "5", true},
		{`Promise.resolve(7)`, "7", true},
		{`window.appLoaded`, "1", true},
		{`throw new Error("boom")`, "Error: boom", false},
		{`nope.x`, "ReferenceError", false},
		{`(function f(){ return 1 })`, "function f()", true},
	}
	for _, c := range cases {
		r, err := tab.DevEval(c.expr)
		if err != nil {
			t.Errorf("%q: %v", c.expr, err)
			continue
		}
		if r.OK != c.ok || !strings.Contains(r.Text, c.text) {
			t.Errorf("%q = %+v, want ok=%v containing %q", c.expr, r, c.ok, c.text)
		}
	}
}

func TestDevStorageAndCookies(t *testing.T) {
	tab, srv := devTab(t)
	if _, err := tab.DevEval(`localStorage.setItem('k1','v1'); sessionStorage.setItem('s1','x'); document.cookie = 'visible=1; path=/'; 1`); err != nil {
		t.Fatal(err)
	}
	local, session := tab.DevStorage()
	if len(local) != 1 || local[0] != [2]string{"k1", "v1"} || len(session) != 1 || session[0][0] != "s1" {
		t.Errorf("storage: %v %v", local, session)
	}
	tab.Context().JarRef().Set(&Cookie{Name: "secret", Value: "s3", Domain: "127.0.0.1", Path: "/", HttpOnly: true, SameSite: 2})
	found, secret := false, false
	for _, c := range tab.DevCookies() {
		if c.Name == "visible" && c.Value == "1" {
			found = true
		}
		if c.Name == "secret" && c.HttpOnly && c.SameSite == "Lax" {
			secret = true
		}
	}
	if !found || !secret {
		t.Errorf("cookies not listed for %s (visible %v, httpOnly %v): %+v", srv.URL, found, secret, tab.DevCookies())
	}
	if v, _ := tab.DevEval(`document.cookie`); strings.Contains(v.Text, "secret") {
		t.Errorf("the page itself must not see an HttpOnly cookie: %q", v.Text)
	}
}

func TestDevConsoleLocation(t *testing.T) {
	tab, srv := devTab(t)
	for _, ev := range tab.ConsoleLog(0) {
		if m, ok := ev.Data.(map[string]string); ok && m["text"] == "app says hi" {
			if want := srv.URL + "/app.js:2:"; !strings.HasPrefix(m["loc"], want) {
				t.Errorf("loc %q, want prefix %q", m["loc"], want)
			}
			return
		}
	}
	t.Error("console.log from app.js not found")
}

func TestDevConsoleKeepsErrorsWithStack(t *testing.T) {
	srv := devServer(t)
	_ = srv
	e := newEng(t)
	tab, _ := e.NewTab("")
	tab.SetDocument(`<html><body><script>function boom(){ throw new TypeError("bad thing") }
console.log("before");
boom();</script></body></html>`, "http://example.test/page.html")
	tab.Settle(100 * time.Millisecond)
	var texts []string
	for _, ev := range tab.ConsoleLog(0) {
		if m, ok := ev.Data.(map[string]string); ok {
			texts = append(texts, m["level"]+": "+m["text"]+" @"+m["loc"])
		}
	}
	joined := strings.Join(texts, "\n")
	if !strings.Contains(joined, "log: before @<inline script>:2:") || !strings.Contains(joined, "@<inline script>:1:37") {
		t.Errorf("console entries should carry the script and line they came from: %s", joined)
	}
	if !strings.Contains(joined, "log: before") || !strings.Contains(joined, "error: TypeError: bad thing") || !strings.Contains(joined, "boom") {
		t.Errorf("console: %s", joined)
	}
}

// The style the browser adds to every page (no classic scrollbar gutter) is not the page's: it stays out of the tree,
// the sources and the matched rules.
func TestDevToolsHidesBrowserInternals(t *testing.T) {
	e := newEng(t)
	tab, _ := e.NewTab("")
	tab.SetDocument(`<html><head><style>p { color: red }</style><style data-sweef-internal="">html { overflow-y: hidden !important }</style></head><body><p id="p">x</p></body></html>`, "http://example.test/")
	tab.Settle(50 * time.Millisecond)
	var inline []string
	for _, s := range tab.DevSources() {
		if s.Inline && s.Kind == "style" {
			inline = append(inline, s.URL)
		}
	}
	if len(inline) != 1 {
		t.Errorf("inline styles listed: %v", inline)
	}
	head, _ := tab.QuerySelector("head")
	kids, _ := tab.DevDOMChildren(head.BackendID, 0)
	if len(kids) != 1 {
		t.Errorf("head shows %d children, want only the page's style", len(kids))
	}
	html, _ := tab.QuerySelector("html")
	st, err := tab.DevStyle(html.BackendID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Rules {
		for _, d := range r.Decls {
			if d.Name == "overflow-y" {
				t.Errorf("the internal rule was listed: %+v", r)
			}
		}
	}
}
