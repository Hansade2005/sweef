package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// onclick="..." attributes run (they did not: only addEventListener and the on* properties did).
func TestInlineEventHandlerAttributes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><body>
<button id="a" onclick="window.a = (window.a || 0) + 1; this.setAttribute('data-x', event.type)">a</button>
<a id="link" href="#nope" onclick="window.link = 1; return false">l</a>
<button id="p" onclick="window.p = 1">p</button>
<button id="bad" onclick="this is not javascript">b</button>
<input id="i" value="v" onchange="window.changed = value + '!'">`))
	}))
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{"#a", "#a", "#link", "#p", "#bad"} {
		if err := tab.Click(sel); err != nil {
			t.Fatal(err)
		}
	}
	var r map[string]any
	evalJSON(t, tab, `(function () {
	  var p = document.getElementById('p');
	  p.onclick = null;
	  p.dispatchEvent(new Event('click'));
	  var cnt = window.p;
	  p.setAttribute('onclick', 'window.p = 2');
	  p.onclick = undefined;
	  var i = document.getElementById('i'); i.dispatchEvent(new Event('change'));
	  return { a: window.a, ev: document.getElementById('a').getAttribute('data-x'), link: window.link, hash: location.hash, p: cnt, changed: window.changed };
	})()`, &r)
	if r["a"] != 2.0 || r["ev"] != "click" {
		t.Errorf("onclick attribute: %v", r)
	}
	if r["link"] != 1.0 || r["hash"] != "" {
		t.Errorf("return false in an inline handler must cancel the link: %v", r)
	}
	if r["p"] != 1.0 {
		t.Errorf("the clicked button ran its handler once; assigning onclick = null disables the attribute: %v", r)
	}
	if r["changed"] != "v!" {
		t.Errorf("an input's own value is in scope of its handler: %v", r)
	}
}
