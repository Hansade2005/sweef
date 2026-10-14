package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// getComputedStyle must answer from the cascade, not only the inline style attribute.
// Expected values are Chromium 141's for the same page.
func TestGetComputedStyleUsesCascade(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<style>body{margin:8px;font:16px/1.5 serif}
#a{display:flex;position:relative;width:300px;padding:10px 20px;margin:5px;border:2px solid red;color:#336699;background:rgba(0,128,0,.5);z-index:3;box-sizing:border-box}
#b{display:none;color:blue}
.g{display:grid;grid-template-columns:1fr 1fr;height:50px}
</style><div id=a>x</div><div id=b>y</div><div id=d class=g></div><span id=e>s</span>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"a.display": "flex", "a.position": "relative", "a.width": "300px", "a.height": "48px",
		"a.color": "rgb(51, 102, 153)", "a.backgroundColor": "rgba(0, 128, 0, 0.5)", "a.zIndex": "3",
		"a.marginTop": "5px", "a.paddingLeft": "20px", "a.borderTopWidth": "2px", "a.boxSizing": "border-box",
		"a.lineHeight": "24px",
		"b.display":    "none", "b.width": "auto", "b.color": "rgb(0, 0, 255)",
		"d.display": "grid", "d.width": "1264px", "d.height": "50px",
		"e.display": "inline", "e.width": "auto",
	}
	for k, w := range want {
		id, prop := k[:1], k[2:]
		var got string
		evalJSON(t, tab, `getComputedStyle(document.getElementById('`+id+`'))['`+prop+`']`, &got)
		if got != w {
			t.Errorf("%s: got %q, want %q", k, got, w)
		}
	}
}

// document.compatMode follows the doctype, as layout does.
func TestCompatModeFollowsDoctype(t *testing.T) {
	for page, want := range map[string]string{"<!doctype html><p>x": "CSS1Compat", "<p>x": "BackCompat"} {
		page := page
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(page))
		})
		srv := httptest.NewServer(mux)
		e := New(DefaultOptions())
		tab, err := e.NewTab(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		var got string
		evalJSON(t, tab, `document.compatMode`, &got)
		if got != want {
			t.Errorf("%q: compatMode %q, want %q", page, got, want)
		}
		e.Close()
		srv.Close()
	}
}
