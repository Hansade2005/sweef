package engine

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWindowScrollAndGeometry(t *testing.T) {
	body := "<!doctype html><html><body>" + strings.Repeat("<p>line of text</p>", 200) + "<div id=last>end</div></body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(body))
	}))
	defer srv.Close()
	tab := newJSTab(t, srv, "/")
	if got := mustEval(t, tab, `window.scrollY`); got != "0" {
		t.Fatalf("initial scrollY %s", got)
	}
	top := mustEval(t, tab, `document.getElementById('last').getBoundingClientRect().top`)
	mustEval(t, tab, `window.scrollTo(0, 300); 1`)
	if got := mustEval(t, tab, `window.scrollY`); got != "300" {
		t.Fatalf("scrollY after scrollTo = %s", got)
	}
	if got := mustEval(t, tab, `document.documentElement.scrollTop`); got != "300" {
		t.Fatalf("scrollTop = %s", got)
	}
	after := mustEval(t, tab, `document.getElementById('last').getBoundingClientRect().top + 300`)
	if after != top {
		t.Fatalf("rect not scroll-relative: %s vs %s", after, top)
	}
	mustEval(t, tab, `window.scrollBy({top: -100}); 1`)
	if got := mustEval(t, tab, `pageYOffset`); got != "200" {
		t.Fatalf("scrollBy = %s", got)
	}
	mustEval(t, tab, `document.getElementById('last').scrollIntoView(); 1`)
	if got := mustEval(t, tab, `scrollY > 200`); got != "true" {
		t.Fatalf("scrollIntoView did not scroll")
	}
	if got := mustEval(t, tab, `document.documentElement.clientWidth === innerWidth`); got != "true" {
		t.Fatalf("root clientWidth should be the viewport width")
	}
}
