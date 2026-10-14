package engine

import (
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Request headers set by the page (fetch and XMLHttpRequest) reach the server.
func TestFetchSendsRequestHeaders(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte(`{"ct":"` + r.Header.Get("Content-Type") + `","key":"` + r.Header.Get("X-Goog-Api-Key") + `","xua":"` + r.Header.Get("X-User-Agent") + `"}`))
	})
	mux.HandleFunc("/bytes", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Write([]byte(hex.EncodeToString(b)))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><body>x`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	evalJSON(t, tab, `(async function () {
	  var o = {};
	  var res = await fetch('/echo', { method: 'POST', headers: { 'content-type': 'application/json+protobuf', 'x-goog-api-key': 'K', 'x-user-agent': 'grpc-web-javascript/0.1' }, body: '[1]' });
	  o.fetch = await res.json();
	  o.xhr = await new Promise(function (ok) {
	    var x = new XMLHttpRequest(); x.open('POST', '/echo'); x.setRequestHeader('Content-Type', 'application/x-test'); x.setRequestHeader('X-Goog-Api-Key', 'X');
	    x.onload = function () { ok(JSON.parse(x.responseText)); }; x.send('a');
	  });
	  var bin = new Uint8Array([0x1f, 0x8b, 0x08, 0x00, 0xff, 0xfe, 0x80, 0x00, 0x7f]);
	  o.binFetch = await (await fetch('/bytes', { method: 'POST', body: bin })).text();
	  o.binBuffer = await (await fetch('/bytes', { method: 'POST', body: bin.buffer })).text();
	  o.binBlob = await (await fetch('/bytes', { method: 'POST', body: new Blob([bin]) })).text();
	  o.binXHR = await new Promise(function (ok) { var x = new XMLHttpRequest(); x.open('POST', '/bytes'); x.onload = function () { ok(x.responseText); }; x.send(bin); });
	  o.binSyncXHR = (function () { var x = new XMLHttpRequest(); x.open('POST', '/bytes', false); x.send(bin); return x.responseText; })();
	  o.text = await (await fetch('/bytes', { method: 'POST', body: 'h\u00e9llo' })).text();
	  return o;
	})()`, &r)
	const want = "1f8b0800fffe80007f"
	for _, k := range []string{"binFetch", "binBuffer", "binBlob", "binXHR", "binSyncXHR"} {
		if r[k] != want {
			t.Errorf("%s: the server received %v, want %s", k, r[k], want)
		}
	}
	if r["text"] != hex.EncodeToString([]byte("h\u00e9llo")) {
		t.Errorf("text body: %v", r["text"])
	}
	f, _ := r["fetch"].(map[string]any)
	if f["ct"] != "application/json+protobuf" || f["key"] != "K" || f["xua"] != "grpc-web-javascript/0.1" {
		t.Errorf("fetch request headers at the server: %v", f)
	}
	x, _ := r["xhr"].(map[string]any)
	if x["ct"] != "application/x-test" || x["key"] != "X" {
		t.Errorf("XMLHttpRequest request headers at the server: %v", x)
	}
}
