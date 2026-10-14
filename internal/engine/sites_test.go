package engine

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// sites is one local server that answers for several host names, so a test can have genuinely different sites
// (a.localhost, b.localhost, tracker.localhost ...) without DNS: SetHostMap sends every one of those names to the
// server. Hosts ending in .localhost are secure contexts, so Cache Storage and service workers work on them.
type sites struct {
	*httptest.Server
	mu    sync.Mutex
	rules map[string]siteResp // "host/path"
	hits  []siteHit
	port  string
}

type siteResp struct {
	status  int
	ct      string
	headers http.Header
	body    string
	fn      func(w http.ResponseWriter, r *http.Request)
}

type siteHit struct {
	Host, Path, Cookie, Referer, Dest, Site, Mode string
}

func newSites(t *testing.T, hosts ...string) *sites {
	t.Helper()
	s := &sites{rules: map[string]siteResp{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	_, s.port, _ = net.SplitHostPort(strings.TrimPrefix(s.Server.URL, "http://"))
	m := map[string]string{}
	for _, h := range hosts {
		m[h] = "127.0.0.1"
	}
	SetHostMap(m)
	t.Cleanup(func() { SetHostMap(nil); s.Server.Close() })
	return s
}

func (s *sites) serve(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	s.mu.Lock()
	s.hits = append(s.hits, siteHit{Host: host, Path: r.URL.Path, Cookie: r.Header.Get("Cookie"), Referer: r.Header.Get("Referer"),
		Dest: r.Header.Get("Sec-Fetch-Dest"), Site: r.Header.Get("Sec-Fetch-Site"), Mode: r.Header.Get("Sec-Fetch-Mode")})
	rule, ok := s.rules[host+r.URL.Path]
	s.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	if rule.fn != nil {
		rule.fn(w, r)
		return
	}
	for k, v := range rule.headers {
		w.Header()[k] = v
	}
	ct := rule.ct
	if ct == "" {
		ct = "text/html; charset=utf-8"
		if strings.HasSuffix(r.URL.Path, ".js") {
			ct = "application/javascript"
		}
	}
	w.Header().Set("Content-Type", ct)
	if rule.status != 0 {
		w.WriteHeader(rule.status)
	}
	io.WriteString(w, rule.body)
}

// URL of a path on a host.
func (s *sites) u(host, path string) string { return "http://" + host + ":" + s.port + path }

// page registers an HTML (or other) body for host/path.
func (s *sites) page(host, path, body string, hdr ...string) *sites {
	h := http.Header{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h.Add(hdr[i], hdr[i+1])
	}
	s.mu.Lock()
	s.rules[host+path] = siteResp{body: body, headers: h}
	s.mu.Unlock()
	return s
}

func (s *sites) handle(host, path string, fn func(w http.ResponseWriter, r *http.Request)) {
	s.mu.Lock()
	s.rules[host+path] = siteResp{fn: fn}
	s.mu.Unlock()
}

// hitsFor returns the recorded requests for host/path.
func (s *sites) hitsFor(host, path string) []siteHit {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []siteHit
	for _, h := range s.hits {
		if h.Host == host && h.Path == path {
			out = append(out, h)
		}
	}
	return out
}

func newSiteEngine(t *testing.T) *Engine {
	t.Helper()
	o := DefaultOptions()
	o.ProxyURL = "direct"
	o.DownloadsDir = t.TempDir()
	e := New(o)
	t.Cleanup(func() { e.Close() })
	return e
}

func waitText(t *testing.T, tab *Tab, sel, want string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if got, _ := tab.Text(sel); got == want {
			return
		}
		tab.Settle(20 * 1000 * 1000)
	}
	got, _ := tab.Text(sel)
	t.Fatalf("%s: got %q want %q\n%s", sel, got, want, consoleText(tab))
}

var _ = fmt.Sprint
