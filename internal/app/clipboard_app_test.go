package app

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/engine"
)

// Copy, cut and paste reach the page as events with a DataTransfer; the page may supply or cancel the data, and
// images the page put on the clipboard come back as files on a paste event.
func TestCopyPasteEventsAndRichClipboard(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home Page")
	h.click(100, vpTop+120+60+60+10+15) // focus the page's text input
	if h.focusName() != "page" {
		t.Fatalf("focus %s", h.focusName())
	}
	h.pageEval(`window.log = []; window.mode = 'default';
	  var f = document.getElementById('f');
	  ['copy', 'cut'].forEach(function (n) { f.addEventListener(n, function (e) {
	    log.push(n + ':' + (e.clipboardData ? e.clipboardData.types.length : 'none'));
	    if (mode === 'custom') { e.clipboardData.setData('text/plain', 'CUSTOM'); e.clipboardData.setData('text/html', '<i>c</i>'); e.preventDefault(); }
	  }); });
	  f.addEventListener('paste', function (e) {
	    var d = e.clipboardData;
	    log.push('paste:' + d.getData('text/plain') + ':' + d.types.join() + ':files=' + d.files.length + (d.files.length ? ':' + d.files[0].type + ':' + d.files[0].size : ''));
	    if (mode === 'block') e.preventDefault();
	  });
	  f.addEventListener('beforeinput', function (e) { log.push('beforeinput:' + e.inputType); });
	  f.addEventListener('input', function (e) { log.push('input:' + e.inputType); });
	  1`, nil)
	h.typeText("abc")
	h.key("ctrl+a")
	h.key("ctrl+c")
	if got := h.win.Clipboard(); got != "abc" {
		t.Fatalf("default copy: %q", got)
	}
	// the page supplies the data and cancels the default
	h.pageEval(`mode = 'custom'; 1`, nil)
	h.key("ctrl+c")
	if got := h.win.Clipboard(); got != "CUSTOM" {
		t.Fatalf("custom copy: %q", got)
	}
	flavours := h.app.clip.readNow()
	var html string
	for _, f := range flavours {
		if f.Type == "text/html" {
			html = string(f.Data)
		}
	}
	if html != "<i>c</i>" {
		t.Fatalf("flavours after a custom copy: %+v", flavours)
	}
	// cut with the default: the selection leaves the field
	h.pageEval(`mode = 'default'; 1`, nil)
	h.key("ctrl+x")
	var v string
	h.pageEval(`document.getElementById('f').value`, &v)
	if v != "" {
		t.Fatalf("cut left %q", v)
	}
	// paste: the event carries the clipboard, then beforeinput and input follow
	h.pageEval(`log.length = 0; 1`, nil)
	h.key("ctrl+v")
	h.pageEval(`document.getElementById('f').value`, &v)
	if v != "abc" { // the cut put the field's text on the clipboard
		t.Fatalf("paste inserted %q", v)
	}
	var log []string
	h.pageEval(`log`, &log)
	want := []string{"paste:abc:text/plain:files=0", "beforeinput:insertFromPaste", "input:insertFromPaste"}
	if len(log) != len(want) {
		t.Fatalf("paste events: %v", log)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("paste event %d: %q, want %q (all %v)", i, log[i], want[i], log)
		}
	}
	// a handler that cancels the paste keeps the field as it is
	h.pageEval(`mode = 'block'; document.getElementById('f').value = ''; log.length = 0; 1`, nil)
	h.key("ctrl+v")
	h.pageEval(`document.getElementById('f').value + '|' + log.length`, &v)
	if v != "|1" {
		t.Fatalf("cancelled paste: %q", v)
	}
	// an image on the clipboard arrives as a file
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	h.loop(func() {
		_ = h.app.clip.writeNow([]engine.ClipboardEntry{{Type: "text/plain", Data: []byte("with image")}, {Type: "image/png", Data: png}})
	})
	h.pageEval(`mode = 'block'; log.length = 0; 1`, nil)
	h.key("ctrl+v")
	var first string
	h.pageEval(`log[0]`, &first)
	if first != "paste:with image:text/plain,Files:files=1:image/png:"+itoa(len(png)) {
		t.Fatalf("image paste: %q", first)
	}
	// the page can read the same data through the Async Clipboard API once the user allows it: the first read asks
	h.pageEval(`window.cb = 'pending'; navigator.clipboard.read().then(function (items) {
	    return items[0].getType('image/png').then(function (b) { return b.size + ':' + items[0].types.join(); });
	  }).then(function (s) { cb = s; }, function (e) { cb = e.name; }); 1`, nil)
	h.waitFor("the clipboard question", func() bool {
		h.pageEval(`new Promise(function (r) { setTimeout(r, 20); }).then(function () { return 1; })`, nil)
		return h.promptID() != ""
	})
	var kind string
	h.loop(func() { kind = h.app.pw.prompts[0].name })
	if kind != "clipboard-read" {
		t.Fatalf("prompt for %q", kind)
	}
	h.uiPost("perm.answer", map[string]any{"id": h.promptID(), "answer": "allow"})
	waitPage(h, `window.cb !== 'pending'`)
	h.pageEval(`cb`, &v)
	if v != itoa(len(png))+":text/plain,image/png" {
		t.Fatalf("async read: %q", v)
	}
	if got := h.app.prof.Sites.Rule(srv.URL).ClipboardRead; got != "granted" {
		t.Fatalf("the decision is not stored: %q", got)
	}
	// ...and a blocked site is refused without a question
	h.app.prof.Sites.SetClipboardRead(srv.URL, "denied")
	h.pageEval(`window.cb = 'pending'; navigator.clipboard.readText().then(function (s) { cb = s; }, function (e) { cb = e.name + ':' + e.message; }); 1`, nil)
	waitPage(h, `window.cb !== 'pending'`)
	h.pageEval(`cb`, &v)
	if v != "NotAllowedError:Read permission denied." {
		t.Fatalf("blocked read: %q", v)
	}
	// writing needs no question
	h.pageEval(`window.cb = 'pending'; navigator.clipboard.writeText('from the page').then(function () { cb = 'ok'; }, function (e) { cb = e.name; }); 1`, nil)
	waitPage(h, `window.cb !== 'pending'`)
	if got := h.win.Clipboard(); got != "from the page" {
		t.Fatalf("writeText: %q", got)
	}
}

// waitPage polls a page expression, running the page's timers between the polls.
func waitPage(h *harness, expr string) {
	h.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		h.pageEval(`new Promise(function (r) { setTimeout(r, 20); }).then(function () { return !!(`+expr+`); })`, &ok)
		if ok {
			return
		}
	}
	h.t.Fatalf("timed out waiting for %s", expr)
}
