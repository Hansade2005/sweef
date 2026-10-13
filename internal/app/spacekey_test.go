package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sweefbrowser/sweef/internal/shell"
)

// pressSpace sends what every native shell sends for the space bar: a key event and the text it produces.
func (h *harness) pressSpace() {
	h.win.Push(shell.Event{Kind: shell.EvKeyDown, Key: " ", Code: "Space"})
	h.win.Push(shell.Event{Kind: shell.EvText, Text: " "})
	h.win.Push(shell.Event{Kind: shell.EvKeyUp, Key: " ", Code: "Space"})
	h.idle()
}

// A space typed into the omnibox or a page field is one space (it was inserted twice: once for the key and once for
// its text, so the omnibox drew "a  the  quick" while the dropdown row collapsed it and looked right).
func TestSpaceKeyTypesOneSpace(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><title>Form</title><body style="margin:0"><input id="f" style="display:block;width:300px;height:30px;margin:10px">
<script>window.keys=[];document.getElementById('f').addEventListener('keydown',function(e){keys.push(e.key)});</script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	h := realUI(t, Config{InitialURLs: []string{srv.URL + "/"}})
	h.waitTitle("Form")
	h.waitUIReady()

	h.key("ctrl+l")
	for _, w := range []string{"the", "quick", "brown"} {
		h.typeText(w)
		h.pressSpace()
	}
	var v string
	h.chromeEval(`document.getElementById('url').value`, &v)
	if v != "the quick brown " {
		t.Fatalf("omnibox value %q", v)
	}

	// a page field: one space, and the page sees one keydown for it
	vp := h.vp()
	h.click(60, vp.Y+25) // the field: this also gives the page the keyboard
	h.typeText("a")
	h.pressSpace()
	h.typeText("b")
	var got string
	h.pageEval(`document.getElementById('f').value + '|' + keys.join(',')`, &got)
	if got != "a b|a, ,b" {
		t.Fatalf("page field %q, want one space and one keydown per key", got)
	}
}

// Space still scrolls a page that is not being typed into.
func TestSpaceKeyStillScrollsThePage(t *testing.T) {
	srv := pageServer(t)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/tall"}}})
	h.waitTitle("Tall")
	before, _ := h.app.comp.Scroll()
	h.win.Push(shell.Event{Kind: shell.EvKeyDown, Key: " ", Code: "Space"})
	h.win.Push(shell.Event{Kind: shell.EvText, Text: " "})
	h.win.Push(shell.Event{Kind: shell.EvKeyUp, Key: " ", Code: "Space"})
	h.waitFor("page to scroll", func() bool { pos, _ := h.app.comp.Scroll(); return pos > before })
}
