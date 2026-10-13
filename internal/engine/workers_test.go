package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// wkServer serves a blank page at every .html path and scripts from a map, so a test can write the worker next to
// the page that starts it.
type wkServer struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string]string
	hits  map[string]int
}

func newWKServer(t *testing.T, files map[string]string) *wkServer {
	t.Helper()
	s := &wkServer{files: files, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		body, ok := s.files[r.URL.Path]
		s.mu.Unlock()
		switch {
		case ok && strings.HasSuffix(r.URL.Path, ".js"):
			w.Header().Set("Content-Type", "application/javascript")
		case ok:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case strings.HasSuffix(r.URL.Path, ".html") || r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			body = "<!doctype html><html><head><title>t</title></head><body><p id=o>-</p><script>window.ready=1</script></body></html>"
		default:
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *wkServer) set(path, body string) {
	s.mu.Lock()
	s.files[path] = body
	s.mu.Unlock()
}

// ---- messaging ------------------------------------------------------------------------------------

func TestWorkerMessagingAndTransfer(t *testing.T) {
	srv := newWKServer(t, map[string]string{"/echo.js": `
self.onmessage = (e) => {
  const d = e.data;
  if (d && d.buf) { const a = new Uint8Array(d.buf); self.postMessage({ len: a.length, sum: a.reduce((x, y) => x + y, 0), back: a.buffer }, [a.buffer]); return; }
  if (d && d.port) { d.port.postMessage('hi from worker'); d.port.onmessage = (m) => d.port.postMessage('echo:' + m.data); return; }
  self.postMessage({ echo: d, isWorker: typeof document === 'undefined' && typeof window === 'undefined' && self instanceof DedicatedWorkerGlobalScope && self instanceof WorkerGlobalScope, loc: self.location.pathname, name: self.name });
};`})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, `
const w = new Worker('/echo.js', { name: 'nm' });
const next = () => new Promise((res) => { w.onmessage = (e) => res(e.data); });
let p = next(); w.postMessage({ a: [1, 2, { b: new Date(5), m: new Map([[1, 'x']]) }] });
const r1 = await p;
const buf = new Uint8Array([1, 2, 3, 250]).buffer;
p = next(); w.postMessage({ buf }, [buf]);
const r2 = await p;
const ch = new MessageChannel();
const msgs = [];
const done = new Promise((res) => { ch.port1.onmessage = (m) => { msgs.push(m.data); if (m.data === 'hi from worker') ch.port1.postMessage('ping'); else res(); }; });
w.postMessage({ port: ch.port2 }, [ch.port2]);
await done;
w.terminate();
return JSON.stringify({ r1: { echo: r1.echo.a[2].b instanceof Date && r1.echo.a[2].m.get(1), isWorker: r1.isWorker, loc: r1.loc, name: r1.name }, detached: buf.byteLength, r2len: r2.len, sum: r2.sum, back: r2.back.byteLength, msgs });`)
	want := `{"r1":{"echo":"x","isWorker":true,"loc":"/echo.js","name":"nm"},"detached":0,"r2len":4,"sum":256,"back":4,"msgs":["hi from worker","echo:ping"]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}
