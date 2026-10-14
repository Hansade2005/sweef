package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// swServer serves a page, a service worker script, and counts the requests that reach it.
type swServer struct {
	*httptest.Server
	mu     sync.Mutex
	hits   map[string]int
	script string
	extra  map[string]string // path -> body
}

func (s *swServer) hit(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func newSWServer(t *testing.T, script string) *swServer {
	t.Helper()
	s := &swServer{hits: map[string]int{}, script: script, extra: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		body, ok := s.extra[r.URL.Path]
		s.mu.Unlock()
		if ok {
			ct := "text/plain"
			if strings.HasSuffix(r.URL.Path, ".js") {
				ct = "application/javascript"
			} else if strings.HasSuffix(r.URL.Path, ".html") {
				ct = "text/html; charset=utf-8"
			}
			w.Header().Set("Content-Type", ct)
			io.WriteString(w, body)
			return
		}
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><html><head><title>sw</title></head><body><p id="o">-</p><script>window.ready = true;</script></body></html>`)
	})
	mux.HandleFunc("/sw.js", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits["/sw.js"]++
		src := s.script
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, src)
	})
	mux.HandleFunc("/sub/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, s.script)
	})
	mux.HandleFunc("/wide/sw.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Service-Worker-Allowed", "/")
		io.WriteString(w, s.script)
	})
	mux.HandleFunc("/html.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, s.script)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *swServer) setScript(src string) {
	s.mu.Lock()
	s.script = src
	s.mu.Unlock()
}

func (s *swServer) setExtra(path, body string) {
	s.mu.Lock()
	s.extra[path] = body
	s.mu.Unlock()
}

const basicSW = `
const VERSION = 'v1';
self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(VERSION).then((c) => c.addAll(['/asset.js'])));
});
self.addEventListener('activate', (e) => { e.waitUntil(self.clients.claim()); });
self.addEventListener('fetch', (e) => {
  const u = new URL(e.request.url);
  if (u.pathname === '/api/hello') { e.respondWith(new Response('hello from sw', { headers: { 'x-sw': '1' } })); return; }
  if (u.pathname === '/asset.js') { e.respondWith(caches.match(e.request).then((r) => r || fetch(e.request))); return; }
});
self.addEventListener('message', (e) => { e.source.postMessage({ echo: e.data, ports: e.ports.length }); });
`
