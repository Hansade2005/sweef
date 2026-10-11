package engine

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// fetch() and XHR must hand page code the exact bytes of a binary response.
func TestBinaryFetchAndXHRAreByteExact(t *testing.T) {
	payload := make([]byte, 4096)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write(payload)
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
	sum := func(u8 string) string {
		return `(function(b){var s=0,h=0;for(var i=0;i<b.length;i++){s+=b[i];h=(h*31+b[i])>>>0}return b.length+':'+s+':'+h})(` + u8 + `)`
	}
	var want string
	{
		s, h := 0, uint32(0)
		for _, b := range payload {
			s += int(b)
			h = h*31 + uint32(b)
		}
		want = itoa(len(payload)) + ":" + itoa(s) + ":" + itoa(int(h))
	}
	var got string
	evalJSON(t, tab, `window.__r=null;fetch('/bin').then(function(r){return r.arrayBuffer()}).then(function(ab){window.__r=`+sum("new Uint8Array(ab)")+`});true`, nil)
	tab.settle(500e6)
	evalJSON(t, tab, `window.__r`, &got)
	if got != want {
		t.Fatalf("fetch arrayBuffer: got %q want %q", got, want)
	}
	evalJSON(t, tab, `window.__x=null;var x=new XMLHttpRequest();x.open('GET','/bin');x.responseType='arraybuffer';x.onload=function(){window.__x=`+sum("new Uint8Array(x.response)")+`};x.send();true`, nil)
	tab.settle(500e6)
	evalJSON(t, tab, `window.__x`, &got)
	if got != want {
		t.Fatalf("xhr arraybuffer: got %q want %q", got, want)
	}
	evalJSON(t, tab, `window.__b=null;fetch('/bin').then(function(r){return r.blob()}).then(function(b){return b.arrayBuffer()}).then(function(ab){window.__b=`+sum("new Uint8Array(ab)")+`});true`, nil)
	tab.settle(500e6)
	evalJSON(t, tab, `window.__b`, &got)
	if got != want {
		t.Fatalf("fetch blob: got %q want %q", got, want)
	}
}
