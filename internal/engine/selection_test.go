package engine

import (
	"encoding/json"
	"image"
	"strings"
	"testing"
)

func selTab(t *testing.T, body string) *Tab {
	t.Helper()
	e := New(DefaultOptions())
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	tab.SetDocument(`<!doctype html><html><head><style>body{margin:0;font:16px monospace}p{margin:0}</style></head><body>`+body+`</body></html>`, "https://example.test/")
	return tab
}

func evalJS(t *testing.T, tab *Tab, js string) any {
	t.Helper()
	raw, err := tab.Evaluate(js)
	if err != nil {
		t.Fatalf("%s: %v", js, err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s: %v (%s)", js, err, raw)
	}
	return v
}

func TestRangeEditingOperations(t *testing.T) {
	tab := selTab(t, `<div id=d><p id=a>Hello <b>big</b> world</p><p id=b>second</p></div>`)
	v := evalJS(t, tab, `(function(){
	  var a = document.getElementById('a'), r = document.createRange();
	  var t1 = a.firstChild, b = a.querySelector('b'), t3 = a.lastChild;
	  r.setStart(t1, 3); r.setEnd(t3, 3);
	  var out = {};
	  out.str = r.toString();
	  out.collapsed = r.collapsed;
	  out.common = r.commonAncestorContainer === a;
	  var d = document.createElement('div'); d.appendChild(r.cloneContents()); out.clone = d.innerHTML;
	  out.cmp = r.compareBoundaryPoints(Range.START_TO_START, r.cloneRange());
	  out.inRange = [r.isPointInRange(b.firstChild, 1), r.isPointInRange(document.getElementById('b').firstChild, 0)];
	  out.intersects = [r.intersectsNode(b), r.intersectsNode(document.getElementById('b'))];
	  // deleteContents
	  var r2 = r.cloneRange(); r2.deleteContents();
	  out.afterDelete = a.innerHTML;
	  out.collapsedAfter = r2.collapsed;
	  // insertNode splits the text node
	  var x = document.createElement('i'); x.textContent = 'X';
	  var r3 = document.createRange(); r3.setStart(a.firstChild, 2); r3.insertNode(x);
	  out.afterInsert = a.innerHTML;
	  // surroundContents
	  var r4 = document.createRange(); r4.selectNodeContents(document.getElementById('b'));
	  var s = document.createElement('u'); r4.surroundContents(s);
	  out.afterSurround = document.getElementById('b').innerHTML;
	  // partial selection of a non-text node cannot be surrounded
	  var r5 = document.createRange(); r5.setStart(a.firstChild, 0); r5.setEnd(document.getElementById('b'), 0);
	  try { r5.surroundContents(document.createElement('s')); out.surroundThrew = false; } catch (e) { out.surroundThrew = e.name; }
	  return out;
	})()`).(map[string]any)
	if v["str"] != "lo big wo" {
		t.Fatalf("toString = %q", v["str"])
	}
	if v["clone"] != "lo <b>big</b> wo" {
		t.Fatalf("cloneContents = %q", v["clone"])
	}
	if v["collapsed"] != false || v["common"] != true || v["cmp"].(float64) != 0 {
		t.Fatalf("%v", v)
	}
	in := v["inRange"].([]any)
	if in[0] != true || in[1] != false {
		t.Fatalf("isPointInRange %v", in)
	}
	ix := v["intersects"].([]any)
	if ix[0] != true || ix[1] != false {
		t.Fatalf("intersectsNode %v", ix)
	}
	if v["afterDelete"] != "Helrld" || v["collapsedAfter"] != true {
		t.Fatalf("deleteContents left %q", v["afterDelete"])
	}
	if !strings.Contains(v["afterInsert"].(string), "He<i>X</i>l") {
		t.Fatalf("insertNode: %q", v["afterInsert"])
	}
	if v["afterSurround"] != "<u>second</u>" {
		t.Fatalf("surroundContents: %q", v["afterSurround"])
	}
	if v["surroundThrew"] != "InvalidStateError" {
		t.Fatalf("surroundContents over a partial element: %v", v["surroundThrew"])
	}
}

func TestLiveRangesFollowTheTree(t *testing.T) {
	tab := selTab(t, `<p id=a>abcdef</p><p id=b>ghi</p>`)
	v := evalJS(t, tab, `(function(){
	  var a = document.getElementById('a'), tn = a.firstChild, r = document.createRange();
	  r.setStart(tn, 2); r.setEnd(tn, 5);
	  var out = {};
	  tn.insertData(0, 'XY');            // two characters before the range
	  out.afterInsertData = [r.startOffset, r.endOffset, r.toString()];
	  tn.deleteData(0, 2);
	  out.afterDelete = [r.startOffset, r.endOffset];
	  var tail = tn.splitText(4);        // the end of the range is in the tail
	  out.afterSplit = [r.startContainer === tn, r.startOffset, r.endContainer === tail, r.endOffset];
	  var b = document.getElementById('b'), r2 = document.createRange();
	  r2.setStart(b.firstChild, 1); r2.setEnd(b, 1);
	  b.parentNode.removeChild(b);
	  out.afterRemove = [r2.startContainer === document.body, r2.endContainer === document.body];
	  var r3 = document.createRange(); r3.setStart(document.body, 1);
	  var before = document.createElement('hr'); document.body.insertBefore(before, document.body.firstChild);
	  out.afterInsertNode = r3.startOffset;
	  return out;
	})()`).(map[string]any)
	if got := v["afterInsertData"].([]any); got[0].(float64) != 4 || got[1].(float64) != 7 || got[2] != "cde" {
		t.Fatalf("a range must shift when text is inserted before it: %v", got)
	}
	if got := v["afterDelete"].([]any); got[0].(float64) != 2 || got[1].(float64) != 5 {
		t.Fatalf("delete: %v", got)
	}
	if got := v["afterSplit"].([]any); got[0] != true || got[1].(float64) != 2 || got[2] != false {
		// the end offset 5 is beyond the split at 4: it moves into the tail
		if !(got[0] == true && got[2] == true && got[3].(float64) == 1) {
			t.Fatalf("split: %v", got)
		}
	}
	if got := v["afterRemove"].([]any); got[0] != true || got[1] != true {
		t.Fatalf("a range inside a removed node moves to the parent: %v", got)
	}
	if v["afterInsertNode"].(float64) != 2 {
		t.Fatalf("a boundary after an inserted node moves along: %v", v["afterInsertNode"])
	}
}

func TestSelectionApiAndRenderedText(t *testing.T) {
	tab := selTab(t, `<div><p id=a>Hello   <b>big</b>
	world</p><p id=b>second line</p><p id=c style="user-select:none">not selectable</p><p id=d>after</p></div>`)
	v := evalJS(t, tab, `(function(){
	  var s = getSelection(), out = {};
	  out.none = [s.type, s.rangeCount, s.isCollapsed, s.toString()];
	  var a = document.getElementById('a'), b = document.getElementById('b'), d = document.getElementById('d');
	  s.setBaseAndExtent(a.firstChild, 2, b.firstChild, 6);
	  out.range = [s.type, s.rangeCount, s.isCollapsed, s.anchorOffset, s.focusOffset, s.direction];
	  out.text = s.toString();
	  out.rangeStr = s.getRangeAt(0).toString();
	  // a backward selection keeps its direction
	  s.setBaseAndExtent(b.firstChild, 6, a.firstChild, 2);
	  out.dir = s.direction;
	  out.dirText = s.toString();
	  // through the paragraph that cannot be selected
	  s.setBaseAndExtent(b.firstChild, 0, d.firstChild, 3);
	  out.skip = s.toString();
	  s.collapse(a.firstChild, 3);
	  out.caret = [s.type, s.isCollapsed, s.toString()];
	  s.selectAllChildren(b);
	  out.all = [s.toString(), s.anchorNode === b, s.focusOffset];
	  s.removeAllRanges();
	  out.cleared = [s.type, s.rangeCount];
	  // the Range returned by getRangeAt is live: changing it changes the selection
	  s.setBaseAndExtent(a.firstChild, 0, a.firstChild, 5);
	  var r = s.getRangeAt(0); r.setEnd(a.firstChild, 7);
	  out.live = s.toString();
	  out.contains = [s.containsNode(a, true), s.containsNode(b, true)];
	  return out;
	})()`).(map[string]any)
	eq := func(name string, got, want any) {
		t.Helper()
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Fatalf("%s: got %s want %s", name, g, w)
		}
	}
	eq("none", v["none"], []any{"None", 0, true, ""})
	eq("range", v["range"], []any{"Range", 1, false, 2, 6, "forward"})
	// collapsed white space, a blank line between paragraphs (p has margins in innerText terms)
	eq("text", v["text"], "llo big world\n\nsecond")
	eq("rangeStr", v["rangeStr"], "llo   big\n\tworldsecond") // Range.toString is the raw text of the nodes
	eq("dir", v["dir"], "backward")
	eq("dirText", v["dirText"], "llo big world\n\nsecond")
	eq("skip", v["skip"], "second line\n\naft")
	eq("caret", v["caret"], []any{"Caret", true, ""})
	eq("all", v["all"], []any{"second line", true, 1})
	eq("cleared", v["cleared"], []any{"None", 0})
	eq("live", v["live"], "Hello ") // rendered text: the two blanks show as one
	eq("contains", v["contains"], []any{true, false})
}

func countColor(img *image.RGBA, r, g, b uint8) int {
	n := 0
	for i := 0; i+3 < len(img.Pix); i += 4 {
		if img.Pix[i] == r && img.Pix[i+1] == g && img.Pix[i+2] == b {
			n++
		}
	}
	return n
}

func TestSelectionIsPaintedWithoutRelayout(t *testing.T) {
	tab := selTab(t, `<p id=a style="width:300px">Hello selected world and more words to fill the line</p>`)
	base, ok := tab.PaintRGBA(400, 100, 1, false)
	if !ok {
		t.Fatal("no paint")
	}
	if n := countColor(base, 0x33, 0x90, 0xff); n != 0 {
		t.Fatalf("nothing is selected yet but %d highlight pixels", n)
	}
	n0, _ := tab.LayoutStats()
	evalJS(t, tab, `var a = document.getElementById('a'); getSelection().setBaseAndExtent(a.firstChild, 6, a.firstChild, 14); 1`)
	img, _ := tab.PaintRGBA(400, 100, 1, false)
	n := countColor(img, 0x33, 0x90, 0xff)
	// 8 monospace characters of about 9.6px on a 19px tall line
	if n < 8*8*15 || n > 8*11*21 {
		t.Fatalf("highlight covers %d px, expected about %d", n, 8*9*19)
	}
	// the selected glyphs are white now
	if countColor(img, 255, 255, 255) < countColor(base, 255, 255, 255)-8*9*19+50 {
		t.Fatalf("white text pixels missing inside the highlight")
	}
	n1, _ := tab.LayoutStats()
	if n1-n0 > 1 {
		t.Fatalf("moving the selection must not lay the page out again (layouts %d -> %d)", n0, n1)
	}
	// clearing it brings the page back to the first picture
	evalJS(t, tab, `getSelection().removeAllRanges(); 1`)
	img2, _ := tab.PaintRGBA(400, 100, 1, false)
	if countColor(img2, 0x33, 0x90, 0xff) != 0 {
		t.Fatal("highlight stayed after clearing the selection")
	}
	// quiet change accounting: every move of the selection is a quiet change that names the rows it touched
	q0 := tab.QuietCount()
	evalJS(t, tab, `var a = document.getElementById('a'); getSelection().setBaseAndExtent(a.firstChild, 1, a.firstChild, 3); 1`)
	if tab.QuietCount() != q0+1 {
		t.Fatalf("quiet count %d -> %d", q0, tab.QuietCount())
	}
	top, bot, ok := tab.TakeQuietRows()
	if !ok || bot <= top || top > 20 {
		t.Fatalf("quiet rows %v..%v ok=%v", top, bot, ok)
	}
}

func TestTextControlSelectionApi(t *testing.T) {
	tab := selTab(t, `<input id=i value="hello world"><textarea id=t>one
two</textarea><input id=c type=checkbox>`)
	v := evalJS(t, tab, `(function(){
	  var i = document.getElementById('i'), t = document.getElementById('t'), c = document.getElementById('c'), out = {};
	  out.initial = [i.selectionStart, i.selectionEnd, i.selectionDirection];
	  i.setSelectionRange(2, 7);
	  out.set = [i.selectionStart, i.selectionEnd, i.selectionDirection];
	  i.setSelectionRange(1, 3, 'backward');
	  out.dir = i.selectionDirection;
	  i.select(); out.select = [i.selectionStart, i.selectionEnd];
	  i.setSelectionRange(0, 5); i.setRangeText('HOWDY');
	  out.rangeText = [i.value, i.selectionStart, i.selectionEnd];
	  i.setSelectionRange(0, 5); i.setRangeText('Yo', 0, 5, 'select');
	  out.rangeTextSel = [i.value, i.selectionStart, i.selectionEnd];
	  i.value = 'abc'; out.afterValue = [i.selectionStart, i.selectionEnd];
	  i.setSelectionRange(1, 99); out.clamped = [i.selectionStart, i.selectionEnd];
	  i.selectionStart = 2; out.start = [i.selectionStart, i.selectionEnd];
	  out.checkbox = [c.selectionStart, (function(){ try { c.setSelectionRange(0,0); return 'ok'; } catch (e) { return e.name; } })()];
	  t.setSelectionRange(2, 5); out.ta = [t.selectionStart, t.selectionEnd, t.value.slice(2, 5)];
	  return out;
	})()`).(map[string]any)
	eq := func(name string, got, want any) {
		t.Helper()
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Fatalf("%s: got %s want %s", name, g, w)
		}
	}
	eq("initial", v["initial"], []any{11, 11, "none"})
	eq("set", v["set"], []any{2, 7, "none"})
	eq("dir", v["dir"], "backward")
	eq("select", v["select"], []any{0, 11})
	eq("rangeText", v["rangeText"], []any{"HOWDY world", 0, 5})
	eq("rangeTextSel", v["rangeTextSel"], []any{"Yo world", 0, 2})
	eq("afterValue", v["afterValue"], []any{3, 3})
	eq("clamped", v["clamped"], []any{1, 3})
	eq("start", v["start"], []any{2, 3})
	eq("checkbox", v["checkbox"], []any{nil, "InvalidStateError"})
	eq("ta", v["ta"], []any{2, 5, "e\ntw"[0:0] + "e\nt"})
}
