package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/headless"
	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/shell"
)

func selServer(t testing.TB) *httptest.Server {
	mux := http.NewServeMux()
	page := func(title, head, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `<!doctype html><html><head><title>%s</title><style>body{margin:0;font:16px/20px monospace}p{margin:0 0 10px}%s</style></head><body>%s</body></html>`, title, head, body)
		}
	}
	mux.HandleFunc("/text", page("Text", `.n{user-select:none}`, `<p id=a>The quick brown fox jumps over the lazy dog</p><p id=b>Second paragraph with <b>bold</b> words</p><p id=c class=n>Not selectable</p><p id=d>Last paragraph</p>`))
	mux.HandleFunc("/fields", page("Fields", ``, `<input id=i value="" style="width:300px"><br><textarea id=t cols=30 rows=4></textarea><div id=e contenteditable style="border:1px solid #888;min-height:40px">start </div>
<script>window.ev=[];['copy','cut','paste'].forEach(function(n){document.addEventListener(n,function(e){ev.push(n+':'+e.cancelable+':'+(e.clipboardData?e.clipboardData.types.join('+'):'-'));});});</script>`))
	mux.HandleFunc("/clipevents", page("ClipEvents", ``, `<p id=a>copy me please</p><input id=i value="field text">
<script>document.addEventListener('copy',function(e){ e.clipboardData.setData('text/plain','CUSTOM'); e.preventDefault(); });
document.getElementById('i').addEventListener('paste',function(e){ window.pasted=e.clipboardData.getData('text/plain'); e.preventDefault(); });</script>`))
	mux.HandleFunc("/long", page("Long", ``, func() string {
		var b strings.Builder
		for i := 0; i < 80; i++ {
			fmt.Fprintf(&b, "<p>Paragraph number %d with some words to select</p>", i)
		}
		return b.String()
	}()))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// winPos returns the window position of character i of the first text node of element id (its left edge, the
// middle of the line).
func (h *harness) charWin(id string, i int) (float64, float64) {
	h.t.Helper()
	var v []float64
	h.pageEval(fmt.Sprintf(`(function(){ var n = document.getElementById(%q).firstChild, r = document.createRange(); r.setStart(n, %d); r.setEnd(n, Math.min(n.length, %d)); var b = r.getBoundingClientRect(); return [b.left, b.top + b.height / 2]; })()`, id, i, i+1), &v)
	vp := h.vp()
	z := 1.0
	return vp.X + v[0]*z, vp.Y + v[1]*z
}

func (h *harness) mouseDown(x, y float64, mods shell.Mods) {
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y, Mods: mods})
	h.win.Push(shell.Event{Kind: shell.EvMouseDown, X: x, Y: y, Mods: mods})
}
func (h *harness) mouseUp(x, y float64, mods shell.Mods) {
	h.win.Push(shell.Event{Kind: shell.EvMouseUp, X: x, Y: y, Mods: mods})
}
func (h *harness) drag(x0, y0, x1, y1 float64) {
	h.mouseDown(x0, y0, 0)
	steps := 6
	for i := 1; i <= steps; i++ {
		f := float64(i) / float64(steps)
		h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: x0 + (x1-x0)*f, Y: y0 + (y1-y0)*f})
	}
	h.mouseUp(x1, y1, 0)
	h.idle()
}

// clipText is the text/plain flavour of the clipboard the app has.
func (h *harness) clipText() string {
	var s string
	h.loop(func() { s = h.win.Clipboard() })
	return s
}

func (h *harness) clipFlavour(typ string) string {
	var out string
	h.loop(func() {
		for _, e := range h.app.clip.readNow() {
			if e.Type == typ {
				out = string(e.Data)
			}
		}
	})
	return out
}

func (h *harness) selText() string {
	var s string
	h.pageEval("getSelection().toString()", &s)
	return s
}

func newSelHarness(t *testing.T, path, title string) *harness {
	srv := selServer(t)
	h := newHarness(t, hopts{})
	h.loadScrollPage(srv, path, title)
	return h
}

func TestMouseDragSelectionCopyPutsTextAndHTMLOnTheClipboard(t *testing.T) {
	h := newSelHarness(t, "/text", "Text")
	x0, y0 := h.charWin("a", 4)  // q of quick
	x1, y1 := h.charWin("b", 17) // inside "with <b>bold</b>"
	h.drag(x0+1, y0, x1+1, y1)
	got := h.selText()
	if !strings.HasPrefix(got, "quick brown fox jumps over the lazy dog\n\nSecond paragraph ") || strings.Contains(got, "with") {
		t.Fatalf("selection after a drag across two paragraphs: %q", got)
	}
	h.shot("selection-drag.png")
	// the highlight is on the screen: blue pixels over the first line
	found := false
	for x := int(x0) + 5; x < int(x0)+60; x++ {
		c := h.px(x, int(y0)-2)
		if c.R == 0x33 && c.G == 0x90 && c.B == 0xff {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no highlight painted behind the selected text at (%v,%v): %v", x0, y0, h.px(int(x0)+10, int(y0)-2))
	}
	h.key("ctrl+c")
	if txt := h.clipText(); txt != got {
		t.Fatalf("clipboard text %q, selection %q", txt, got)
	}
	html := h.clipFlavour("text/html")
	if !strings.Contains(html, `<p id="a">quick brown fox jumps over the lazy dog</p>`) || !strings.Contains(html, `<p id="b">Second paragraph </p>`) {
		t.Fatalf("clipboard html %q", html)
	}
	// the html reached the operating system's clipboard (CF_HTML / text/html / public.html), not only the app's copy
	var osHTML string
	h.loop(func() { osHTML = h.win.ClipboardHTML() })
	if osHTML != html {
		t.Fatalf("window rich clipboard html %q, want %q", osHTML, html)
	}
	// the page repaints with the highlight gone when the selection is cleared by a click elsewhere
	h.click(x0+3, y0+60)
	h.idle()
	if s := h.selText(); s != "" {
		t.Fatalf("click clears the selection, left %q", s)
	}
	c := h.px(int(x0)+10, int(y0)-2)
	if c.R == 0x33 && c.G == 0x90 && c.B == 0xff {
		t.Fatal("highlight still painted after clearing the selection")
	}
}

func TestDoubleTripleClickAndShiftClickSelect(t *testing.T) {
	h := newSelHarness(t, "/text", "Text")
	x, y := h.charWin("a", 6) // in "quick"
	click := func(mods shell.Mods) {
		h.mouseDown(x+2, y, mods)
		h.mouseUp(x+2, y, mods)
	}
	click(0)
	click(0)
	h.idle()
	if got := h.selText(); got != "quick" {
		t.Fatalf("double click selects the word, got %q", got)
	}
	click(0)
	click(0)
	click(0)
	h.idle()
	if got := h.selText(); got != "The quick brown fox jumps over the lazy dog" {
		t.Fatalf("triple click selects the paragraph, got %q", got)
	}
	time.Sleep(doubleClickTime + 100*time.Millisecond)
	click(0)
	h.idle()
	x2, y2 := h.charWin("a", 20)
	h.mouseDown(x2, y2, shell.ModShift)
	h.mouseUp(x2, y2, shell.ModShift)
	h.idle()
	if got := h.selText(); !strings.HasPrefix(got, "ick brown fox") {
		t.Fatalf("shift+click extends the selection, got %q", got)
	}
	// ctrl+a selects the page, but not what the user cannot select
	h.key("ctrl+a")
	all := h.selText()
	if !strings.Contains(all, "quick brown fox") || !strings.Contains(all, "Last paragraph") || strings.Contains(all, "Not selectable") {
		t.Fatalf("select all: %q", all)
	}
	h.key("ctrl+c")
	if h.clipText() != all {
		t.Fatalf("copy after select all: %q", h.clipText())
	}
}

func TestShiftArrowExtendsSelection(t *testing.T) {
	h := newSelHarness(t, "/text", "Text")
	x, y := h.charWin("a", 4)
	h.drag(x+1, y, x+1+3*9.6, y)
	if got := h.selText(); got != "qui" {
		t.Fatalf("setup %q", got)
	}
	h.key("shift+ArrowRight")
	h.key("shift+ArrowRight")
	if got := h.selText(); got != "quick" {
		t.Fatalf("shift+arrow right extends by characters, got %q", got)
	}
	h.key("ctrl+shift+ArrowRight")
	if got := h.selText(); got != "quick brown" {
		t.Fatalf("ctrl+shift+arrow extends by words, got %q", got)
	}
	h.key("shift+ArrowLeft")
	if got := h.selText(); got != "quick brow" {
		t.Fatalf("shift+arrow left shrinks, got %q", got)
	}
}

func TestTypingSelectAllCutPasteUndoInAnInput(t *testing.T) {
	h := newSelHarness(t, "/fields", "Fields")
	var r []float64
	h.pageEval(`(function(){ var b = document.getElementById('i').getBoundingClientRect(); return [b.left, b.top, b.width, b.height]; })()`, &r)
	vp := h.vp()
	h.click(vp.X+r[0]+20, vp.Y+r[1]+r[3]/2)
	h.typeText("hello")
	h.typeText(" there")
	var v string
	h.pageEval("document.getElementById('i').value", &v)
	if v != "hello there" {
		t.Fatalf("typed %q", v)
	}
	h.key("ctrl+a")
	h.pageEval("var i = document.getElementById('i'); i.selectionStart + ',' + i.selectionEnd", &v)
	if v != "0,11" {
		t.Fatalf("select all in an input selects its text: %s", v)
	}
	h.key("ctrl+x")
	h.pageEval("document.getElementById('i').value", &v)
	if v != "" || h.clipText() != "hello there" {
		t.Fatalf("cut: value %q clipboard %q", v, h.clipText())
	}
	h.key("ctrl+v")
	h.pageEval("document.getElementById('i').value", &v)
	if v != "hello there" {
		t.Fatalf("paste: %q", v)
	}
	h.key("ctrl+z")
	h.pageEval("document.getElementById('i').value", &v)
	if v != "" {
		t.Fatalf("undo the paste: %q", v)
	}
	h.key("ctrl+y")
	h.pageEval("document.getElementById('i').value", &v)
	if v != "hello there" {
		t.Fatalf("redo: %q", v)
	}
	// the other clipboard chords
	h.key("ctrl+a")
	h.key("ctrl+Insert")
	if h.clipText() != "hello there" {
		t.Fatalf("ctrl+insert copies: %q", h.clipText())
	}
	h.key("shift+Delete")
	h.pageEval("document.getElementById('i').value", &v)
	if v != "" {
		t.Fatalf("shift+delete cuts: %q", v)
	}
	h.key("shift+Insert")
	h.pageEval("document.getElementById('i').value", &v)
	if v != "hello there" {
		t.Fatalf("shift+insert pastes: %q", v)
	}
	// the caret and selection are painted inside the field
	h.key("ctrl+a")
	h.shot("input-selected.png")
	found := false
	for x := int(vp.X + r[0] + 4); x < int(vp.X+r[0]+60); x++ {
		c := h.px(x, int(vp.Y+r[1]+r[3]/2))
		if c.R == 0x33 && c.G == 0x90 && c.B == 0xff {
			found = true
		}
	}
	if !found {
		t.Fatal("no selection highlight inside the input")
	}
}

func TestContentEditablePasteIsSanitisedAndEventsFire(t *testing.T) {
	h := newSelHarness(t, "/fields", "Fields")
	var r []float64
	h.pageEval(`(function(){ var b = document.getElementById('e').getBoundingClientRect(); return [b.left, b.top, b.width, b.height]; })()`, &r)
	vp := h.vp()
	h.click(vp.X+r[0]+200, vp.Y+r[1]+10)
	h.typeText("abc")
	var html string
	h.pageEval("document.getElementById('e').innerHTML", &html)
	if html != "start abc" {
		t.Fatalf("typing in a contenteditable: %q", html)
	}
	h.app.post(func() {
		_ = h.app.clip.writeNow(clipEntries("<b onclick='x()'>BOLD</b><script>evil()</script>", "BOLD"))
	})
	h.idle()
	h.key("ctrl+v")
	h.pageEval("document.getElementById('e').innerHTML", &html)
	if strings.Contains(html, "script") || strings.Contains(html, "onclick") || !strings.Contains(html, "<b>BOLD</b>") {
		t.Fatalf("pasted html must be sanitised and kept: %q", html)
	}
	var ev []string
	h.pageEval("ev", &ev)
	if len(ev) == 0 || ev[len(ev)-1] != "paste:true:text/plain+text/html" {
		t.Fatalf("paste event: %v", ev)
	}
	h.key("ctrl+a")
	h.key("ctrl+c")
	h.pageEval("ev", &ev)
	if ev[len(ev)-1] != "copy:true:" {
		t.Fatalf("copy event %v", ev)
	}
	h.key("ctrl+x")
	h.pageEval("document.getElementById('e').innerHTML", &html)
	if html != "" && html != "<br>" {
		t.Fatalf("cut empties the editable region: %q", html)
	}
	if !strings.Contains(h.clipText(), "BOLD") {
		t.Fatalf("clipboard after cut %q", h.clipText())
	}
	h.key("ctrl+z")
	h.pageEval("document.getElementById('e').innerHTML", &html)
	if !strings.Contains(html, "BOLD") {
		t.Fatalf("undo the cut: %q", html)
	}
}

func TestPageCanCancelAndReplaceClipboardEvents(t *testing.T) {
	h := newSelHarness(t, "/clipevents", "ClipEvents")
	x0, y0 := h.charWin("a", 0)
	x1, _ := h.charWin("a", 8)
	h.drag(x0+1, y0, x1+1, y0)
	if got := h.selText(); got != "copy me " {
		t.Fatalf("selection %q", got)
	}
	h.key("ctrl+c")
	if h.clipText() != "CUSTOM" {
		t.Fatalf("copy handler's data goes on the clipboard, got %q", h.clipText())
	}
	// paste into the input is cancelled by the page: its handler got the clipboard text and nothing was inserted
	var r []float64
	h.pageEval(`(function(){ var b = document.getElementById('i').getBoundingClientRect(); return [b.left, b.top, b.width, b.height]; })()`, &r)
	vp := h.vp()
	h.click(vp.X+r[0]+10, vp.Y+r[1]+r[3]/2)
	h.key("ctrl+v")
	var pasted, val string
	h.pageEval("window.pasted", &pasted)
	h.pageEval("document.getElementById('i').value", &val)
	if pasted != "CUSTOM" || val != "field text" {
		t.Fatalf("paste handler saw %q, input value %q", pasted, val)
	}
}

func TestDraggingPastTheEdgeScrollsAndExtends(t *testing.T) {
	h := newSelHarness(t, "/long", "Long")
	vp := h.vp()
	sx, sy := vp.X+20, vp.Y+30
	h.mouseDown(sx, sy, 0)
	// move below the bottom of the view and hold there
	bx, by := vp.X+200, vp.Y+vp.H+40
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: bx, Y: by})
	h.waitFor("autoscroll", func() bool { return h.pageScroll() > 400 })
	h.mouseUp(bx, by, 0)
	h.idle()
	got := h.selText()
	if n := strings.Count(got, "Paragraph number"); n < 10 {
		t.Fatalf("the selection should have grown with the scroll, %d paragraphs: %q", n, got)
	}
	// and back up
	pos := h.pageScroll()
	h.mouseDown(vp.X+20, vp.Y+vp.H-30, 0)
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: vp.X + 100, Y: vp.Y - 30})
	h.waitFor("autoscroll up", func() bool { return h.pageScroll() < pos-300 })
	h.mouseUp(vp.X+100, vp.Y-30, 0)
	h.idle()
}

func TestFindHighlightsAndScrollsToTheActiveMatch(t *testing.T) {
	h := newSelHarness(t, "/long", "Long")
	h.app.post(func() { h.app.find("number 7", 0) })
	h.idle()
	if f := h.findState(); f.Matches != 11 || f.Index != 1 {
		// "number 7", "number 70".."number 79": 11 matches
		t.Fatalf("find state %+v", f)
	}
	// the active match is the first one at the top of the view; step to a later match and the view follows
	for i := 0; i < 6; i++ {
		h.app.post(func() { h.app.find("number 7", 1) })
	}
	h.idle()
	if f := h.findState(); f.Index != 7 {
		t.Fatalf("after six steps %+v", f)
	}
	if h.pageScroll() < 300 {
		t.Fatalf("the view must follow the active match, at %d", h.pageScroll())
	}
	h.shot("find.png")
	// orange and yellow highlights are on screen
	var orange, yellow int
	img := h.win.Screen()
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			c := img.RGBAAt(x, y)
			if c.R == 0xff && c.G == 0x96 && c.B == 0x32 {
				orange++
			}
			if c.R == 0xff && c.G == 0xff && c.B == 0x00 {
				yellow++
			}
		}
	}
	if orange < 100 {
		t.Fatalf("active match not painted orange (%d px)", orange)
	}
	_ = yellow
	// closing the find bar removes the highlights
	h.uiPost("find.close", map[string]any{})
	h.idle()
	img = h.win.Screen()
	left := 0
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			c := img.RGBAAt(x, y)
			if c.R == 0xff && c.G == 0x96 && c.B == 0x32 {
				left++
			}
		}
	}
	if left != 0 {
		t.Fatalf("%d orange pixels after closing find", left)
	}
}

var _ = headless.KeyEvents

func clipEntries(html, text string) []engine.ClipboardEntry {
	return []engine.ClipboardEntry{{Type: "text/plain", Data: []byte(text)}, {Type: "text/html", Data: []byte(html)}}
}

func (h *harness) findState() (f state.Find) {
	h.loop(func() { f = h.app.model.Find })
	return
}
