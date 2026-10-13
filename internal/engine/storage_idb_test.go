package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// storageServer serves a page with a script (so the tab gets a VM) at /, /a, /b.
func storageServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><html><head><title>storage</title></head><body><script>window.ready = true;</script></body></html>`)
	}
	mux.HandleFunc("/", page)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newStorageEngine(t *testing.T, dir string) *Engine {
	t.Helper()
	o := DefaultOptions()
	o.StorageDir = dir
	o.DownloadsDir = t.TempDir()
	e := New(o)
	t.Cleanup(func() { e.Close() })
	return e
}

// js runs an async snippet in the tab and returns its JSON result.
func js(t *testing.T, tab *Tab, body string) string {
	t.Helper()
	raw := mustEval(t, tab, "(async () => {\n"+body+"\n})()")
	var s string
	if json.Unmarshal([]byte(raw), &s) == nil {
		return s
	}
	return raw
}

// idbPrelude: promise helpers used by the tests.
const idbHelpers = `
const req = (r) => new Promise((res, rej) => { r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
const done = (tx) => new Promise((res, rej) => { tx.oncomplete = () => res('complete'); tx.onabort = () => rej(tx.error || new Error('aborted')); });
const open = (name, ver, upgrade) => new Promise((res, rej) => { const r = indexedDB.open(name, ver); r.onupgradeneeded = (e) => upgrade && upgrade(r.result, r.transaction, e); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); r.onblocked = () => {}; });
`

func openTab(t *testing.T, e *Engine, url string) *Tab {
	t.Helper()
	tab, err := e.NewTab(url)
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func TestIDBPutGetRoundTrip(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('app', 1, (db) => { db.createObjectStore('kv'); db.createObjectStore('people', { keyPath: 'id' }); });
const tx = db.transaction(['kv', 'people'], 'readwrite');
tx.objectStore('kv').put({ a: 1, d: new Date(86400000), r: /x+/gi, m: new Map([[1, 'one']]), s: new Set([1, 2]), u: new Uint8Array([1, 2, 255]), big: 12345678901234567890n, n: null }, 'k1');
tx.objectStore('people').put({ id: 7, name: 'Ada' });
await done(tx);
const rx = db.transaction(['kv', 'people']);
const v = await req(rx.objectStore('kv').get('k1'));
const p = await req(rx.objectStore('people').get(7));
const miss = await req(rx.objectStore('kv').get('nope'));
return JSON.stringify({ a: v.a, d: v.d instanceof Date && v.d.getTime(), r: v.r.source + '/' + v.r.flags, m: [...v.m], s: [...v.s], u: [...v.u], big: String(v.big), n: v.n, p, miss: miss === undefined, name: db.name, version: db.version, stores: [...db.objectStoreNames] });
`)
	want := `{"a":1,"d":86400000,"r":"x+/gi","m":[[1,"one"]],"s":[1,2],"u":[1,2,255],"big":"12345678901234567890","n":null,"p":{"id":7,"name":"Ada"},"miss":true,"name":"app","version":1,"stores":["kv","people"]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func expect(t *testing.T, tab *Tab, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

var _ = strings.TrimSpace
