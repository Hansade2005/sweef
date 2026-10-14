package engine

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Page code must be able to send request headers. QuickJS passes the headers object to
// Go as a *quickjs.Object; a bare map[string]any assertion dropped them all, so APIs
// that authenticate by header (Supabase's apikey, Authorization: Bearer) answered 401.
func TestFetchAndXHRSendRequestHeaders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte(r.Header.Get("Apikey") + "|" + r.Header.Get("Authorization")))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>x</body></html>"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"object":  `fetch('/echo',{headers:{apikey:'k1',Authorization:'Bearer t1'}})`,
		"Headers": `fetch('/echo',{headers:new Headers({apikey:'k1',Authorization:'Bearer t1'})})`,
		"array":   `fetch('/echo',{headers:[['apikey','k1'],['Authorization','Bearer t1']]})`,
		"Request": `fetch(new Request('/echo',{headers:{apikey:'k1',Authorization:'Bearer t1'}}))`,
	}
	for name, call := range cases {
		var got string
		evalJSON(t, tab, `window.__h=null;`+call+`.then(function(r){return r.text()}).then(function(s){window.__h=s});true`, nil)
		tab.settle(500e6)
		evalJSON(t, tab, `window.__h`, &got)
		if got != "k1|Bearer t1" {
			t.Errorf("fetch with %s headers: server saw %q", name, got)
		}
	}
	var got string
	evalJSON(t, tab, `(function(){var x=new XMLHttpRequest();x.open('GET','/echo',false);x.setRequestHeader('apikey','k1');x.setRequestHeader('Authorization','Bearer t1');x.send();return x.responseText})()`, &got)
	if got != "k1|Bearer t1" {
		t.Errorf("sync XHR: server saw %q", got)
	}
	evalJSON(t, tab, `window.__x=null;(function(){var x=new XMLHttpRequest();x.open('GET','/echo');x.setRequestHeader('apikey','k1');x.setRequestHeader('Authorization','Bearer t1');x.onload=function(){window.__x=x.responseText};x.send()})();true`, nil)
	tab.settle(500e6)
	evalJSON(t, tab, `window.__x`, &got)
	if got != "k1|Bearer t1" {
		t.Errorf("async XHR: server saw %q", got)
	}
}

// An <img> in the document must fire error when its response is not a decodable image,
// so onerror fallbacks run, and naturalWidth/naturalHeight must be the image's own size,
// not its layout box.
func TestDocumentImageLoadErrorAndNaturalSize(t *testing.T) {
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 320, 200)))
	mux := http.NewServeMux()
	mux.HandleFunc("/ok.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(pngBuf.Bytes())
	})
	mux.HandleFunc("/bad.jpg", func(w http.ResponseWriter, r *http.Request) { // an SPA fallback page, not an image
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html><body>not an image</body></html>"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><script>
window.__log=[];
function mk(id,src){var i=document.createElement('img');i.id=id;i.setAttribute('style','height:20px;width:auto');i.setAttribute('src',src);
i.onload=function(){__log.push(id+':load:'+i.naturalWidth+'x'+i.naturalHeight)};
i.onerror=function(){__log.push(id+':error:'+i.naturalWidth);if(!i.dataset.fb){i.dataset.fb='1';i.src='/ok.png'}};
document.body.appendChild(i)}
mk('good','/ok.png');mk('broken','/bad.jpg');mk('missing','/nope.png');
</script></body></html>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	tab.settle(1e9)
	var log []string
	evalJSON(t, tab, `__log.slice().sort()`, &log)
	want := []string{"broken:error:0", "broken:load:320x200", "good:load:320x200", "missing:error:0", "missing:load:320x200"}
	if len(log) != len(want) {
		t.Fatalf("events %v, want %v", log, want)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("events %v, want %v", log, want)
		}
	}
}
