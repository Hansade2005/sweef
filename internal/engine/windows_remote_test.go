package engine

import (
	"testing"
	"time"
)

// A same-origin popup is reached like a frame: the opener's functions can be called and the popup's document read.
func TestPopupAndOpenerSameOriginAccess(t *testing.T) {
	app := newSite(t, nil)
	other := newSite(t, nil)
	app.set("/", `<!doctype html><title>opener</title><script>window.got = null; function onAuth(t) { window.got = 'token:' + t; return 'ok'; }</script>`)
	app.set("/pop", `<!doctype html><title>popup</title><h1 id=h>hello</h1><script>window.reply = window.opener.onAuth('abc');</script>`)
	other.set("/pop", `<!doctype html><script>window.r = 'none'; try { window.r = String(window.opener.onAuth) } catch (e) { window.r = e.name } </script>`)
	e := newEng(t)
	tab, _ := e.NewTab(app.URL + "/")
	tab.NoteUserActivation()
	jsonOf(t, tab, `window.w = window.open('/pop'); 1`)
	p := popupOf(t, e, tab)
	waitURL(t, p, "/pop")
	if got := waitEval(t, p, `window.reply`); got != "ok" {
		t.Fatalf("window.opener.onAuth(...) returned %q", got)
	}
	if got := jsonOf(t, tab, `window.got`); got != `"token:abc"` {
		t.Fatalf("the opener's function ran with %s", got)
	}
	if got := jsonOf(t, tab, `window.w.document.getElementById('h').textContent`); got != `"hello"` {
		t.Fatalf("popup document: %s", got)
	}
	// another origin gets a SecurityError, not the function
	tab.NoteUserActivation()
	jsonOf(t, tab, `window.w2 = window.open('`+other.URL+`/pop'); 1`)
	var p2 *Tab
	waitFor(t, "second popup", 3*time.Second, func() bool {
		ts := e.ListTabs()
		if len(ts) == 3 {
			p2 = ts[2]
		}
		return p2 != nil
	})
	if got := waitEval(t, p2, `window.r`); got != "SecurityError" {
		t.Fatalf("cross-origin opener access: %q", got)
	}
}
