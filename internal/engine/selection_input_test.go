package engine

import (
	"fmt"
	"strings"
	"testing"
)

// charX returns the x of the left edge of character i of the text node of element id, and the y of its line's middle.
func charPos(t *testing.T, tab *Tab, id string, i int) (float64, float64) {
	t.Helper()
	v := evalJS(t, tab, fmt.Sprintf(`(function(){ var n = document.getElementById(%q).firstChild, r = document.createRange(); r.setStart(n, %d); r.setEnd(n, Math.min(n.length, %d)); var b = r.getBoundingClientRect(); var p = [scrollX, scrollY]; return [b.left + p[0], b.top + p[1] + b.height / 2, b.width]; })()`, id, i, i+1)).([]any)
	return v[0].(float64), v[1].(float64)
}

func layoutNow(t *testing.T, tab *Tab) {
	t.Helper()
	if _, ok := tab.PaintRGBA(800, 400, 1, false); !ok {
		t.Fatal("no layout")
	}
}

func selString(t *testing.T, tab *Tab) string {
	t.Helper()
	return evalJS(t, tab, `getSelection().toString()`).(string)
}

func TestMouseDragSelectsText(t *testing.T) {
	tab := selTab(t, `<p id=a>The quick brown fox jumps over the lazy dog</p><p id=b>Second paragraph here</p>`)
	layoutNow(t, tab)
	x0, y := charPos(t, tab, "a", 4)  // the q of quick
	x1, _ := charPos(t, tab, "a", 16) // the f of fox
	if !tab.SelectDown(x0+1, y, 1, false) {
		t.Fatal("a press on text starts a selection")
	}
	if !tab.SelectMove(x1+1, y) {
		t.Fatal("move")
	}
	tab.SelectUp()
	if got := selString(t, tab); got != "quick brown " {
		t.Fatalf("dragged text %q", got)
	}
	// dragging back past the start selects backwards
	tab.SelectDown(x1+1, y, 1, false)
	tab.SelectMove(x0+1, y)
	tab.SelectUp()
	if got := selString(t, tab); got != "quick brown " {
		t.Fatalf("backward drag %q", got)
	}
	if d := evalJS(t, tab, `getSelection().direction`); d != "backward" {
		t.Fatalf("direction %v", d)
	}
	// dragging off the end of the line takes the rest of it; below the last line, the end of the text
	tab.SelectDown(x0+1, y, 1, false)
	tab.SelectMove(780, y)
	if got := selString(t, tab); got != "quick brown fox jumps over the lazy dog" {
		t.Fatalf("to the line end %q", got)
	}
	tab.SelectMove(300, 300)
	tab.SelectUp()
	if got := selString(t, tab); got != "quick brown fox jumps over the lazy dog\n\nSecond paragraph here" {
		t.Fatalf("below the text %q", got)
	}
	// a click collapses
	tab.SelectDown(x0+1, y, 1, false)
	tab.SelectUp()
	if got := selString(t, tab); got != "" {
		t.Fatalf("a click selects nothing, got %q", got)
	}
	if ty := evalJS(t, tab, `getSelection().type`); ty != "Caret" {
		t.Fatalf("type after a click: %v", ty)
	}
}

func TestDoubleAndTripleClick(t *testing.T) {
	tab := selTab(t, `<p id=a>The quick brown fox, jumps over</p><p id=b>Second paragraph here</p>`)
	layoutNow(t, tab)
	x, y := charPos(t, tab, "a", 6) // inside "quick"
	tab.SelectDown(x+2, y, 2, false)
	tab.SelectUp()
	if got := selString(t, tab); got != "quick" {
		t.Fatalf("double click selects the word, got %q", got)
	}
	// dragging after a double click extends by words
	x2, _ := charPos(t, tab, "a", 12) // inside "brown"
	tab.SelectDown(x+2, y, 2, false)
	tab.SelectMove(x2+2, y)
	tab.SelectUp()
	if got := selString(t, tab); got != "quick brown" {
		t.Fatalf("word drag %q", got)
	}
	// on the comma: only the comma
	xc, _ := charPos(t, tab, "a", 19)
	tab.SelectDown(xc+2, y, 2, false)
	if got := selString(t, tab); got != "," {
		t.Fatalf("punctuation %q", got)
	}
	tab.SelectUp()
	// triple click: the paragraph, and not the next one
	tab.SelectDown(x+2, y, 3, false)
	tab.SelectUp()
	if got := selString(t, tab); got != "The quick brown fox, jumps over" {
		t.Fatalf("triple click selects the paragraph, got %q", got)
	}
	// shift+click extends from the anchor
	tab.SelectDown(x+2, y, 1, false)
	tab.SelectUp()
	x3, _ := charPos(t, tab, "a", 25)
	tab.SelectDown(x3, y, 1, true)
	tab.SelectUp()
	if got := selString(t, tab); got != "ick brown fox, jump" {
		t.Fatalf("shift+click extends from where the caret was: %q", got)
	}
	_ = strings.TrimSpace
}

func TestUserSelectNoneIsNotSelectedByTheMouse(t *testing.T) {
	tab := selTab(t, `<p id=a>Selectable text</p><p id=n style="user-select:none">Not for you</p><p id=c>Also selectable</p>`)
	layoutNow(t, tab)
	x, y := charPos(t, tab, "a", 2)
	tab.SelectDown(x, y, 1, false)
	_, yn := charPos(t, tab, "n", 2)
	tab.SelectMove(60, yn)
	tab.SelectUp()
	// nothing of the paragraph that cannot be selected
	got := selString(t, tab)
	if strings.Contains(got, "Not for") {
		t.Fatalf("copied text from a user-select:none element: %q", got)
	}
	// pressing on it does not start a selection and clears the old one
	if tab.SelectDown(40, yn, 1, false) {
		t.Fatal("a press on user-select:none text must not start a selection")
	}
	if got := selString(t, tab); got != "" {
		t.Fatalf("selection should be cleared, got %q", got)
	}
	// painting: no highlight over the hidden paragraph
	_, y2 := charPos(t, tab, "c", 2)
	tab.SelectDown(x, y, 1, false)
	tab.SelectMove(100, y2)
	tab.SelectUp()
	img, _ := tab.PaintRGBA(800, 120, 1, false)
	rows := 0
	nTop := int(yn - 6)
	for yy := nTop; yy < nTop+12; yy++ {
		for xx := 0; xx < 400; xx++ {
			o := img.PixOffset(xx, yy)
			if img.Pix[o] == 0x33 && img.Pix[o+1] == 0x90 && img.Pix[o+2] == 0xff {
				rows++
			}
		}
	}
	if rows != 0 {
		t.Fatalf("%d highlight pixels over the user-select:none paragraph", rows)
	}
}

func TestSelectionInsideTextControls(t *testing.T) {
	tab := selTab(t, `<input id=i value="hello big world" style="width:300px"><textarea id=t cols=12 rows=4>alpha beta gamma
delta</textarea>`)
	layoutNow(t, tab)
	box := evalJS(t, tab, `(function(){ var b = document.getElementById('i').getBoundingClientRect(); return [b.left, b.top, b.width, b.height]; })()`).([]any)
	l, top, h := box[0].(float64), box[1].(float64), box[3].(float64)
	y := top + h/2
	// find the x of characters through the control's own text records
	_, ix, d := tab.freshTextIndex()
	in := d.ElementByID("i")
	tb := ix.Boxes(d.NodeByID(in))[0]
	xs := func(i int) float64 { return tb.X(i) }
	_ = l
	tab.SetFocusNode(in)
	tab.SelectDown(xs(2)+1, y, 1, false)
	tab.SelectMove(xs(9)+1, y)
	tab.SelectUp()
	v := evalJS(t, tab, `var i = document.getElementById('i'); [i.selectionStart, i.selectionEnd, i.value.slice(i.selectionStart, i.selectionEnd), getSelection().toString()]`).([]any)
	if v[0].(float64) != 2 || v[1].(float64) != 9 || v[2] != "llo big" || v[3] != "llo big" {
		t.Fatalf("drag in an input: %v", v)
	}
	// double click: a word; triple click: all of it
	tab.SelectDown(xs(8)+1, y, 2, false)
	tab.SelectUp()
	v = evalJS(t, tab, `var i = document.getElementById('i'); [i.selectionStart, i.selectionEnd]`).([]any)
	if v[0].(float64) != 6 || v[1].(float64) != 9 {
		t.Fatalf("double click in an input selects the word big: %v", v)
	}
	tab.SelectDown(xs(8)+1, y, 3, false)
	tab.SelectUp()
	v = evalJS(t, tab, `var i = document.getElementById('i'); [i.selectionStart, i.selectionEnd]`).([]any)
	if v[0].(float64) != 0 || v[1].(float64) != 15 {
		t.Fatalf("triple click in an input: %v", v)
	}
	// a textarea: lines wrap, a click on the second line is at the second line
	ta := d.NodeByID(d.ElementByID("t"))
	tab.SetFocusNode(d.IDOf(ta))
	lines := ix.Boxes(ta)
	if len(lines) < 3 {
		t.Fatalf("textarea lines: %d", len(lines))
	}
	l2 := lines[1]
	tab.SelectDown(l2.X(2)+1, l2.Rect.Y+l2.Rect.H/2, 1, false)
	tab.SelectUp()
	v = evalJS(t, tab, `var t = document.getElementById('t'); [t.selectionStart, t.selectionEnd]`).([]any)
	if got := int(v[0].(float64)); got != l2.Off(2) || v[1].(float64) != v[0].(float64) {
		t.Fatalf("click on the second visual line of the textarea: %v, want caret at %d", v, l2.Off(2))
	}
}

func TestFindHighlightsAndSteps(t *testing.T) {
	tab := selTab(t, `<p>The cat sat. A <b>cat</b> and another Cat.</p><div style="display:none">cat hidden</div><p>Concat is not a word... but cat is</p>`)
	layoutNow(t, tab)
	r := tab.FindStart("cat", 0)
	// 3 in the first paragraph, "Concat" and the last: hidden text and nothing else
	if r.Count != 5 || r.Index != 0 || r.Top < 0 {
		t.Fatalf("find: %+v", r)
	}
	img, _ := tab.PaintRGBA(800, 120, 1, false)
	if countColor(img, 0xff, 0x96, 0x32) == 0 {
		t.Fatal("the active match is orange")
	}
	if countColor(img, 0xff, 0xff, 0x00) == 0 {
		t.Fatal("the other matches are yellow")
	}
	r2 := tab.FindStep(1)
	if r2.Index != 1 {
		t.Fatalf("step: %+v", r2)
	}
	r3 := tab.FindStep(-1)
	if r3.Index != 4 {
		t.Fatalf("stepping back from the first wraps to the last: %+v", r3)
	}
	if r3.Top <= r2.Top {
		t.Fatalf("the last match is lower on the page than the second: %v vs %v", r3.Top, r2.Top)
	}
	// case-insensitive: the capital in "Cat." counts; a match across an element boundary is found
	tab.FindClear()
	r = tab.FindStart("a cat and", 0)
	if r.Count != 1 {
		t.Fatalf("a match across <b>: %+v", r)
	}
	r = tab.FindStart("zzz", 0)
	if r.Count != 0 || r.Index != -1 {
		t.Fatalf("no match: %+v", r)
	}
	tab.FindClear()
	img, _ = tab.PaintRGBA(800, 120, 1, false)
	if countColor(img, 0xff, 0x96, 0x32) != 0 || countColor(img, 0xff, 0xff, 0x00) != 0 {
		t.Fatal("highlights stay after clearing the search")
	}
}

func TestCaretMovement(t *testing.T) {
	tab := selTab(t, `<p id=a>one two three</p><p id=b>four five six</p><div id=e contenteditable>edit <b>me</b> now</div>`)
	layoutNow(t, tab)
	v := evalJS(t, tab, `(function(){
	  var a = document.getElementById('a').firstChild, b = document.getElementById('b').firstChild, out = [];
	  function mv(n, o, dir) { var id = __swIdOf(n), r = __sw.caretMove(id, o, dir); return r ? [__swWrap(r[0]) === n ? 'same' : __swWrap(r[0]).data, r[1]] : null; }
	  out.push(mv(a, 3, 'right'), mv(a, 3, 'left'), mv(a, 4, 'wordright'), mv(a, 6, 'wordleft'), mv(a, 5, 'lineend'), mv(a, 5, 'linestart'));
	  out.push(mv(a, 13, 'right'));   // end of a paragraph: into the next one
	  out.push(mv(b, 0, 'left'));     // and back
	  var dn = mv(a, 4, 'down'); out.push(dn);  // keeps the column on the next line
	  out.push(mv(b, 4, 'up'), mv(a, 4, 'up'), mv(b, 4, 'docend'), mv(a, 4, 'docstart'));
	  return out;
	})()`).([]any)
	eq := func(i int, want string) {
		t.Helper()
		g := fmt.Sprint(v[i])
		if g != want {
			t.Fatalf("move %d: got %s want %s", i, g, want)
		}
	}
	eq(0, "[same 4]")
	eq(1, "[same 2]")
	eq(2, "[same 7]")
	eq(3, "[same 4]")
	eq(4, "[same 13]")
	eq(5, "[same 0]")
	eq(6, "[four five six 0]")
	eq(7, "[one two three 13]")
	eq(8, "[four five six 4]")
	eq(9, "[one two three 4]")
	eq(10, "<nil>")
	eq(11, "[ now 4]") // the last text of the page is in the editable div
	eq(12, "[same 0]")
}
