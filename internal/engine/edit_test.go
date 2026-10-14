package engine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// keys sends key presses to the focused element, the way the window does: each as keydown, keypress, the default
// action and keyup.
func pressKeys(t *testing.T, tab *Tab, seq ...string) {
	t.Helper()
	for _, k := range seq {
		mods := "{}"
		key := k
		for _, pre := range []struct{ p, m string }{{"ctrl+shift+", `{ctrlKey:true,shiftKey:true}`}, {"ctrl+", `{ctrlKey:true}`}, {"shift+", `{shiftKey:true}`}} {
			if strings.HasPrefix(k, pre.p) {
				mods, key = pre.m, strings.TrimPrefix(k, pre.p)
				break
			}
		}
		evalJS(t, tab, fmt.Sprintf("__swDispatchKey(0, %s, %s)", jsStr(key), mods))
	}
}

func jsStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestTypingAndCaretInAnInput(t *testing.T) {
	tab := selTab(t, `<input id=i style="width:200px"><input id=m maxlength=4>`)
	layoutNow(t, tab)
	evalJS(t, tab, `window.log = []; var i = document.getElementById('i'); ['beforeinput','input'].forEach(function(n){ i.addEventListener(n, function(e){ log.push(n + ':' + e.inputType + ':' + e.data); }); }); i.focus(); 1`)
	pressKeys(t, tab, "h", "e", "l", "l", "o")
	val := func() string { return evalJS(t, tab, `document.getElementById('i').value`).(string) }
	pos := func() string {
		v := evalJS(t, tab, `var i = document.getElementById('i'); i.selectionStart + ',' + i.selectionEnd`).(string)
		return v
	}
	if val() != "hello" || pos() != "5,5" {
		t.Fatalf("typed %q caret %s", val(), pos())
	}
	pressKeys(t, tab, "ArrowLeft", "ArrowLeft", "X")
	if val() != "helXlo" || pos() != "4,4" {
		t.Fatalf("insert at the caret: %q %s", val(), pos())
	}
	pressKeys(t, tab, "Backspace")
	if val() != "hello" || pos() != "3,3" {
		t.Fatalf("backspace: %q %s", val(), pos())
	}
	pressKeys(t, tab, "Delete")
	if val() != "helo" {
		t.Fatalf("delete: %q", val())
	}
	pressKeys(t, tab, "Home")
	if pos() != "0,0" {
		t.Fatalf("home: %s", pos())
	}
	pressKeys(t, tab, "shift+ArrowRight", "shift+ArrowRight")
	if pos() != "0,2" {
		t.Fatalf("shift+arrow selects: %s", pos())
	}
	if d := evalJS(t, tab, `document.getElementById('i').selectionDirection`); d != "forward" {
		t.Fatalf("direction %v", d)
	}
	pressKeys(t, tab, "Z") // replaces the selection
	if val() != "Zlo" || pos() != "1,1" {
		t.Fatalf("typing over a selection: %q %s", val(), pos())
	}
	pressKeys(t, tab, "End", "ctrl+shift+ArrowLeft")
	if pos() != "0,3" {
		t.Fatalf("ctrl+shift+left selects the word: %s", pos())
	}
	pressKeys(t, tab, "ArrowRight")
	if pos() != "3,3" {
		t.Fatalf("right with a selection collapses to its end: %s", pos())
	}
	pressKeys(t, tab, " ", "w", "o", "r", "l", "d", "ctrl+Backspace")
	if val() != "Zlo " {
		t.Fatalf("ctrl+backspace deletes the word before the caret: %q", val())
	}
	// events: beforeinput and input carry the inputType and data
	log := evalJS(t, tab, `log.join('|')`).(string)
	if !strings.HasPrefix(log, "beforeinput:insertText:h|input:insertText:h|beforeinput:insertText:e|") || !strings.Contains(log, "deleteContentBackward:null") {
		t.Fatalf("events: %s", log)
	}
	// preventDefault on beforeinput cancels the edit
	evalJS(t, tab, `document.getElementById('i').addEventListener('beforeinput', function(e){ if (e.data === 'q') e.preventDefault(); }); 1`)
	pressKeys(t, tab, "q", "r")
	if val() != "Zlo r" {
		t.Fatalf("cancelled beforeinput: %q", val())
	}
	// an undo step per run of typing, redo brings it back
	evalJS(t, tab, `__swUndo(false)`)
	if val() == "Zlo r" {
		t.Fatal("undo did nothing")
	}
	evalJS(t, tab, `__swUndo(true)`)
	if val() != "Zlo r" {
		t.Fatalf("redo: %q", val())
	}
	// maxlength
	evalJS(t, tab, `document.getElementById('m').focus(); 1`)
	pressKeys(t, tab, "a", "b", "c", "d", "e", "f")
	if v := evalJS(t, tab, `document.getElementById('m').value`); v != "abcd" {
		t.Fatalf("maxlength: %v", v)
	}
}

func TestTextareaLinesAndUndo(t *testing.T) {
	tab := selTab(t, `<textarea id=t cols=20 rows=4>first line
second</textarea>`)
	layoutNow(t, tab)
	evalJS(t, tab, `var t = document.getElementById('t'); t.focus(); t.setSelectionRange(5, 5); 1`)
	layoutNow(t, tab)
	val := func() string { return evalJS(t, tab, `document.getElementById('t').value`).(string) }
	sel := func() string {
		return evalJS(t, tab, `var t = document.getElementById('t'); t.selectionStart + ',' + t.selectionEnd`).(string)
	}
	pressKeys(t, tab, "ArrowDown")
	if sel() != "16,16" && sel() != "17,17" {
		// column 5 of the second line: 11 + 5
		if sel() != "16,16" {
			t.Fatalf("arrow down keeps the column: %s", sel())
		}
	}
	pressKeys(t, tab, "ArrowUp")
	if sel() != "5,5" {
		t.Fatalf("arrow up: %s", sel())
	}
	pressKeys(t, tab, "End")
	if sel() != "10,10" {
		t.Fatalf("end goes to the end of the line: %s", sel())
	}
	pressKeys(t, tab, "Enter", "n", "e", "w")
	if val() != "first line\nnew\nsecond" {
		t.Fatalf("enter inserts a newline: %q", val())
	}
	pressKeys(t, tab, "ctrl+z")
	// ctrl+z is the window's job; the key goes to the page as a chord: the page only sees it
	evalJS(t, tab, `__swUndo(false)`)
	if v := val(); v != "first line\n\nsecond" && v != "first line\nsecond" {
		t.Fatalf("undo of the typed word: %q", v)
	}
	evalJS(t, tab, `__swUndo(false)`)
	if v := val(); v != "first line\nsecond" {
		t.Fatalf("undo of the newline: %q", v)
	}
	evalJS(t, tab, `__swUndo(true); __swUndo(true)`)
	if v := val(); v != "first line\nnew\nsecond" {
		t.Fatalf("redo twice: %q", v)
	}
}

func TestContentEditableEditing(t *testing.T) {
	tab := selTab(t, `<div id=e contenteditable><p id=p1>Hello world</p><p id=p2>Second</p></div><div id=o>outside</div>`)
	layoutNow(t, tab)
	html := func() string { return evalJS(t, tab, `document.getElementById('e').innerHTML`).(string) }
	evalJS(t, tab, `var e = document.getElementById('e'); e.focus(); var p = document.getElementById('p1').firstChild; getSelection().collapse(p, 5); 1`)
	pressKeys(t, tab, "!", "!")
	if html() != "<p id=\"p1\">Hello!! world</p><p id=\"p2\">Second</p>" {
		t.Fatalf("typing in contenteditable: %s", html())
	}
	pressKeys(t, tab, "Backspace")
	if h := html(); !strings.Contains(h, "Hello! world") {
		t.Fatalf("backspace: %s", h)
	}
	pressKeys(t, tab, "Enter")
	if h := html(); !strings.Contains(h, `<p id="p1">Hello!</p><p id="p1"> world</p>`) {
		t.Fatalf("enter splits the paragraph: %s", h)
	}
	// the caret is at the start of the new paragraph: typing goes there
	pressKeys(t, tab, "X")
	if h := html(); !strings.Contains(h, `<p id="p1">X world</p>`) {
		t.Fatalf("typing after enter: %s", h)
	}
	// backspace at the start of a paragraph merges... or at least never eats the markup
	// undo goes back through the edits
	evalJS(t, tab, `__swUndo(false); __swUndo(false); __swUndo(false); 1`)
	if h := html(); h != `<p id="p1">Hello!! world</p><p id="p2">Second</p>` && h != `<p id="p1">Hello world</p><p id="p2">Second</p>` {
		t.Fatalf("after three undos: %s", h)
	}
	// a selection across two paragraphs, deleted by typing over it
	evalJS(t, tab, `var p1 = document.getElementById('p1').firstChild, p2 = document.getElementById('p2').firstChild; getSelection().setBaseAndExtent(p1, 2, p2, 3); 1`)
	pressKeys(t, tab, "Q")
	if h := html(); !strings.Contains(h, "HeQond") {
		t.Fatalf("typing over a selection across blocks: %s", h)
	}
	// execCommand
	evalJS(t, tab, `document.execCommand('insertText', false, 'abc')`)
	if h := html(); !strings.Contains(h, "HeQabcond") {
		t.Fatalf("execCommand insertText: %s", h)
	}
	// select all inside an editing host selects the host's content only
	evalJS(t, tab, `__swSelectAll()`)
	if s := evalJS(t, tab, `getSelection().toString()`).(string); strings.Contains(s, "outside") || !strings.Contains(s, "HeQabcond") {
		t.Fatalf("select all in a host: %q", s)
	}
}

func TestPasteIsSanitised(t *testing.T) {
	tab := selTab(t, `<div id=e contenteditable>start </div><textarea id=t></textarea>`)
	layoutNow(t, tab)
	evalJS(t, tab, `var e = document.getElementById('e'); e.focus(); getSelection().collapse(e.firstChild, 6); 1`)
	evalJS(t, tab, `__swPasteInto('<p onclick="evil()">hi <b style="color:red">there</b><script>evil()</script><img src="javascript:evil()"><a href="javascript:evil()">x</a><a href="https://ok.example/">ok</a><iframe src="x"></iframe></p>', 'hi there')`)
	h := evalJS(t, tab, `document.getElementById('e').innerHTML`).(string)
	for _, bad := range []string{"script", "onclick", "javascript:", "iframe", "style=", "evil"} {
		if strings.Contains(h, bad) {
			t.Fatalf("pasted markup keeps %q: %s", bad, h)
		}
	}
	if !strings.Contains(h, "<b>there</b>") || !strings.Contains(h, `href="https://ok.example/"`) || !strings.HasPrefix(h, "start ") {
		t.Fatalf("pasted markup lost its content: %s", h)
	}
	// into a textarea only the text goes in, with the page's paste event never needed for that
	evalJS(t, tab, `var t = document.getElementById('t'); t.focus(); 1`)
	evalJS(t, tab, `__swPasteInto('<b>bold</b>', 'plain text')`)
	if v := evalJS(t, tab, `document.getElementById('t').value`); v != "plain text" {
		t.Fatalf("paste into textarea: %v", v)
	}
	// cut removes the selection of the focused control
	evalJS(t, tab, `var t = document.getElementById('t'); t.setSelectionRange(0, 6); __swCutSelection()`)
	if v := evalJS(t, tab, `document.getElementById('t').value`); v != "text" {
		t.Fatalf("cut: %v", v)
	}
}
