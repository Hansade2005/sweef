package engine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- pure policy ---------------------------------------------------------------

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestCacheControlParsing(t *testing.T) {
	d := parseCacheControl([]string{`Public, MAX-AGE=60, stale-if-error="30"`, `private, no-cache="Set-Cookie", immutable`})
	if !d.public || !d.private || !d.noCache || !d.immutable || !d.hasMaxAge || d.maxAge != 60*time.Second || d.staleIfError != 30*time.Second {
		t.Fatalf("%+v", d)
	}
	if d := parseCacheControl([]string{"max-age=abc"}); d.hasMaxAge || !d.invalidMaxAge {
		t.Fatalf("invalid max-age: %+v", d)
	}
	if d := parseCacheControl([]string{"max-age=300, max-age=10"}); d.maxAge != 10*time.Second {
		t.Fatalf("smallest max-age wins: %+v", d)
	}
	if d := parseCacheControl([]string{"max-age=99999999999999"}); d.maxAge != maxDelta*time.Second {
		t.Fatalf("clamp: %+v", d)
	}
	if d := parseCacheControl([]string{`x-ext="a,b", no-store`}); !d.noStore {
		t.Fatalf("quoted comma must not split: %+v", d)
	}
}

func TestFreshnessLifetimeHeaderCases(t *testing.T) {
	resp := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	date := resp.Format(http.TimeFormat)
	img := lifetimeInput{kind: KindImage, rawURL: "https://x.test/a.png"}
	doc := lifetimeInput{kind: KindDocument, rawURL: "https://x.test/"}
	cases := []struct {
		name     string
		status   int
		h        http.Header
		in       lifetimeInput
		life     time.Duration
		explicit bool
	}{
		{"max-age", 200, hdr("Cache-Control", "max-age=600"), img, 10 * time.Minute, true},
		{"s-maxage ignored by a private cache", 200, hdr("Cache-Control", "s-maxage=1000, max-age=10"), img, 10 * time.Second, true},
		{"s-maxage alone is no lifetime", 200, hdr("Cache-Control", "s-maxage=1000"), img, 0, false},
		{"max-age beats Expires", 200, hdr("Cache-Control", "max-age=5", "Expires", resp.Add(time.Hour).Format(http.TimeFormat), "Date", date), img, 5 * time.Second, true},
		{"Expires minus Date", 200, hdr("Expires", resp.Add(90*time.Second).Format(http.TimeFormat), "Date", date), img, 90 * time.Second, true},
		{"Expires in the past", 200, hdr("Expires", resp.Add(-time.Hour).Format(http.TimeFormat), "Date", date), img, 0, true},
		{"Expires invalid (0) means expired", 200, hdr("Expires", "0"), img, 0, true},
		{"no-cache", 200, hdr("Cache-Control", "no-cache, max-age=600"), img, 0, true},
		{"Pragma no-cache without Cache-Control", 200, hdr("Pragma", "no-cache", "Expires", resp.Add(time.Hour).Format(http.TimeFormat)), img, 0, true},
		{"max-age=0", 200, hdr("Cache-Control", "max-age=0"), img, 0, true},
		{"invalid max-age is stale", 200, hdr("Cache-Control", "max-age=soon"), img, 0, true},
		{"heuristic 10% of Last-Modified", 200, hdr("Last-Modified", resp.Add(-100*time.Hour).Format(http.TimeFormat), "Date", date), img, 10 * time.Hour, false},
		{"heuristic capped at 7 days", 200, hdr("Last-Modified", resp.Add(-1000*24*time.Hour).Format(http.TimeFormat), "Date", date), img, 7 * 24 * time.Hour, false},
		{"no heuristic for documents", 200, hdr("Last-Modified", resp.Add(-100*time.Hour).Format(http.TimeFormat), "Date", date), doc, 0, false},
		{"no information at all", 200, hdr(), img, 0, false},
		{"immutable without max-age", 200, hdr("Cache-Control", "immutable"), img, 24 * time.Hour, false},
		{"immutable with max-age", 200, hdr("Cache-Control", "max-age=31536000, immutable"), img, 365 * 24 * time.Hour, true},
		{"301 without lifetime", 301, hdr("Location", "/x"), doc, 24 * time.Hour, false},
		{"302 without lifetime", 302, hdr("Location", "/x"), doc, 0, false},
		{"302 with max-age", 302, hdr("Location", "/x", "Cache-Control", "max-age=60"), doc, time.Minute, true},
	}
	for _, c := range cases {
		life, explicit := freshnessLifetime(c.status, c.h, resp, c.in)
		if life != c.life || explicit != c.explicit {
			t.Errorf("%s: got %v explicit=%v, want %v explicit=%v", c.name, life, explicit, c.life, c.explicit)
		}
	}
	// Data Saver: a versioned URL with a validator is fresh for a day even with a short max-age
	ver := lifetimeInput{kind: KindScript, rawURL: "https://x.test/app.3f9a1c7e.js", extend: true}
	if life, _ := freshnessLifetime(200, hdr("Cache-Control", "max-age=60", "ETag", `"a"`), resp, ver); life != 24*time.Hour {
		t.Errorf("extended: %v", life)
	}
	ver.extend = false
	if life, _ := freshnessLifetime(200, hdr("Cache-Control", "max-age=60", "ETag", `"a"`), resp, ver); life != time.Minute {
		t.Errorf("not extended: %v", life)
	}
	ver.extend = true
	if life, _ := freshnessLifetime(200, hdr("Cache-Control", "no-cache", "ETag", `"a"`), resp, ver); life != 0 {
		t.Errorf("extension never overrides no-cache: %v", life)
	}
}

func TestCurrentAge(t *testing.T) {
	resp := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	now := resp.Add(30 * time.Second)
	if a := currentAge(hdr("Age", "100"), resp, now); a != 130*time.Second {
		t.Fatalf("Age header + resident time: %v", a)
	}
	if a := currentAge(hdr("Date", resp.Add(-60*time.Second).Format(http.TimeFormat)), resp, now); a != 90*time.Second {
		t.Fatalf("apparent age: %v", a)
	}
	if a := currentAge(hdr("Date", resp.Add(time.Hour).Format(http.TimeFormat)), resp, now); a != 30*time.Second {
		t.Fatalf("a Date in the future is not a negative age: %v", a)
	}
}

func TestVaryAndStorableHelpers(t *testing.T) {
	names, star := varyNames(hdr("Vary", "Accept-Encoding, Cookie", "Vary", "x-user-state, cookie"))
	if star || strings.Join(names, ",") != "cookie,x-user-state" {
		t.Fatalf("%v %v", names, star)
	}
	if _, star := varyNames(hdr("Vary", "*")); !star {
		t.Fatal("Vary: *")
	}
	if !noStoreResponse(hdr("Cache-Control", "private, no-store")) || !noStoreResponse(hdr("Vary", "*")) || noStoreResponse(hdr("Cache-Control", "private")) {
		t.Fatal("noStoreResponse")
	}
	h := storableHeader(hdr("Set-Cookie", "a=b", "Connection", "close, X-Hop", "X-Hop", "1", "ETag", `"e"`, "Transfer-Encoding", "chunked"))
	if h.Get("Set-Cookie") != "" || h.Get("X-Hop") != "" || h.Get("Connection") != "" || h.Get("Transfer-Encoding") != "" || h.Get("ETag") != `"e"` {
		t.Fatalf("storableHeader: %v", h)
	}
	m := mergeNotModified(hdr("ETag", `"a"`, "Content-Type", "text/css", "Cache-Control", "max-age=1", "Content-Length", "10"),
		hdr("Cache-Control", "max-age=900", "Content-Type", "text/plain", "Content-Length", "0", "Set-Cookie", "x=y", "Date", "Mon, 01 Jan 2024 00:00:00 GMT"))
	if m.Get("Cache-Control") != "max-age=900" || m.Get("Content-Type") != "text/css" || m.Get("Content-Length") != "10" || m.Get("Set-Cookie") != "" || m.Get("Date") == "" {
		t.Fatalf("mergeNotModified: %v", m)
	}
	if staleIfErrorOK(hdr("Cache-Control", "max-age=10, stale-if-error=60, must-revalidate"), 20*time.Second, 10*time.Second) {
		t.Fatal("must-revalidate forbids stale-if-error")
	}
	if !staleIfErrorOK(hdr("Cache-Control", "max-age=10, stale-if-error=60"), 20*time.Second, 10*time.Second) || staleIfErrorOK(hdr("Cache-Control", "max-age=10, stale-if-error=60"), 100*time.Second, 10*time.Second) {
		t.Fatal("stale-if-error window")
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"HTTPS://Example.COM:443/a?b=1#frag": "https://example.com/a?b=1",
		"http://example.com:80":              "http://example.com/",
		"https://example.com:8443/x":         "https://example.com:8443/x",
		"https://user:pw@example.com/x":      "https://example.com/x",
		"https://example.com/a?b=2&a=1":      "https://example.com/a?b=2&a=1", // the query is never reordered
	} {
		if got := normalizeURL(in); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

// ---- through the engine ----------------------------------------------------------

type cacheSrv struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
	h    func(w http.ResponseWriter, r *http.Request)
}

func newCacheSrv(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *cacheSrv {
	s := &cacheSrv{h: h}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r)
		s.mu.Unlock()
		s.h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *cacheSrv) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.reqs {
		if r.URL.Path == path {
			n++
		}
	}
	return n
}

func (s *cacheSrv) last(path string) *http.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.reqs) - 1; i >= 0; i-- {
		if s.reqs[i].URL.Path == path {
			return s.reqs[i]
		}
	}
	return nil
}

// cget makes one request the way a page sub-resource is made and reports body, state and status.
func cget(t *testing.T, tab *Tab, url string, kind DataKind, headers map[string]string) (string, string, int) {
	t.Helper()
	r, err := tab.httpDo(httpOpts{method: "GET", url: url, headers: headers, kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	return string(r.body), r.cacheState, r.status
}

func cacheTab(t *testing.T) (*Engine, *BrowserContext, *Tab) {
	e := newDSEngine(t)
	c, tab := ctxTab(t, e, nil)
	return e, c, tab
}

func wantState(t *testing.T, what, got, prefix string) {
	t.Helper()
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("%s: cache state %q, want prefix %q", what, got, prefix)
	}
}

func TestHTTPCacheFreshnessHeaders(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/plain")
		switch r.URL.Path {
		case "/maxage":
			h.Set("Cache-Control", "max-age=600")
		case "/maxage0":
			h.Set("Cache-Control", "max-age=0")
			h.Set("ETag", `"v1"`)
		case "/expires":
			h.Set("Expires", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
		case "/expired":
			h.Set("Expires", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
			h.Set("ETag", `"v1"`)
		case "/nostore":
			h.Set("Cache-Control", "no-store, max-age=600")
		case "/private":
			h.Set("Cache-Control", "private, max-age=600")
		case "/nocache":
			h.Set("Cache-Control", "no-cache")
			h.Set("ETag", `"nc"`)
		case "/lm":
			h.Set("Last-Modified", time.Now().Add(-1000*time.Hour).UTC().Format(http.TimeFormat))
			h.Set("Date", time.Now().UTC().Format(http.TimeFormat))
		case "/immutable":
			h.Set("Cache-Control", "immutable")
		case "/none":
		case "/setcookie":
			h.Set("Cache-Control", "max-age=600")
			h.Set("Set-Cookie", "sid=1; Path=/")
		}
		if et := h.Get("ETag"); et != "" && r.Header.Get("If-None-Match") == et {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fmt.Fprint(w, "body of "+r.URL.Path)
	})
	_, _, tab := cacheTab(t)
	hits := func(p string, kind DataKind) (string, string) {
		b1, s1, _ := cget(t, tab, srv.URL+p, kind, nil)
		b2, s2, _ := cget(t, tab, srv.URL+p, kind, nil)
		if b1 != "body of "+p || b2 != b1 {
			t.Fatalf("%s: bodies %q %q", p, b1, b2)
		}
		return s1, s2
	}
	for _, p := range []string{"/maxage", "/expires", "/private", "/lm", "/immutable", "/setcookie"} {
		_, s2 := hits(p, KindImage)
		wantState(t, p, s2, "hit")
		if n := srv.count(p); n != 1 {
			t.Errorf("%s fresh: %d requests, want 1", p, n)
		}
	}
	// stale with a validator: conditional request, the stored body is reused on 304
	for _, p := range []string{"/maxage0", "/expired", "/nocache"} {
		_, s2 := hits(p, KindStyle)
		wantState(t, p, s2, "revalidated")
		if srv.count(p) != 2 || srv.last(p).Header.Get("If-None-Match") != `"v1"` && srv.last(p).Header.Get("If-None-Match") != `"nc"` {
			t.Errorf("%s: want a conditional second request, got %d requests, INM=%q", p, srv.count(p), srv.last(p).Header.Get("If-None-Match"))
		}
	}
	// never reused
	for _, p := range []string{"/nostore", "/none"} {
		_, s2 := hits(p, KindImage)
		wantState(t, p, s2, "miss")
		if srv.count(p) != 2 {
			t.Errorf("%s: %d requests, want 2", p, srv.count(p))
		}
	}
	// Set-Cookie is applied on the network response only, never replayed from the cache
	if got := tab.Context().JarRef().ForURL(srv.URL + "/setcookie"); got != "sid=1" {
		t.Errorf("cookie from the network response: %q", got)
	}
}

func TestHTTPCacheLastModifiedRevalidation(t *testing.T) {
	lm := time.Now().Add(-48 * time.Hour).UTC().Format(http.TimeFormat)
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Last-Modified", lm)
		w.Header().Set("Cache-Control", "max-age=0")
		if r.Header.Get("If-Modified-Since") == lm {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		fmt.Fprint(w, "lm body")
	})
	_, _, tab := cacheTab(t)
	cget(t, tab, srv.URL+"/a.css", KindStyle, nil)
	b, st, code := cget(t, tab, srv.URL+"/a.css", KindStyle, nil)
	if b != "lm body" || code != 200 || st != "revalidated" || srv.last("/a.css").Header.Get("If-Modified-Since") != lm {
		t.Fatalf("body %q code %d state %q IMS %q", b, code, st, srv.last("/a.css").Header.Get("If-Modified-Since"))
	}
}

func TestHTTPCache304UpdatesStoredHeaders(t *testing.T) {
	var n atomic.Int32
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"x"`)
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Cache-Control", "max-age=600") // the 304 makes it fresh
			w.WriteHeader(http.StatusNotModified)
			return
		}
		n.Add(1)
		w.Header().Set("Cache-Control", "max-age=0")
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, "css")
	})
	_, _, tab := cacheTab(t)
	cget(t, tab, srv.URL+"/s.css", KindStyle, nil)
	_, st, _ := cget(t, tab, srv.URL+"/s.css", KindStyle, nil)
	wantState(t, "304", st, "revalidated")
	b, st, _ := cget(t, tab, srv.URL+"/s.css", KindStyle, nil)
	wantState(t, "fresh after the 304 renewed it", st, "hit")
	if b != "css" || srv.count("/s.css") != 2 {
		t.Fatalf("body %q requests %d", b, srv.count("/s.css"))
	}
}

func TestHTTPCacheVary(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		switch r.URL.Path {
		case "/lang":
			w.Header().Set("Vary", "Accept-Language")
		case "/star":
			w.Header().Set("Vary", "*")
		case "/cookie":
			w.Header().Set("Vary", "Cookie, Accept-Encoding")
		}
		fmt.Fprint(w, r.URL.Path+"|"+r.Header.Get("Accept-Language")+"|"+r.Header.Get("Cookie"))
	})
	_, _, tab := cacheTab(t)
	get := func(p, lang string) (string, string) {
		b, s, _ := cget(t, tab, srv.URL+p, KindXHR, map[string]string{"Accept-Language": lang})
		return b, s
	}
	get("/lang", "en")
	if _, s := get("/lang", "en"); !strings.HasPrefix(s, "hit") {
		t.Fatalf("same Accept-Language: %q", s)
	}
	if b, s := get("/lang", "de"); !strings.HasPrefix(s, "miss") || !strings.Contains(b, "|de|") {
		t.Fatalf("another Accept-Language must not be served the english copy: %q %q", b, s)
	}
	get("/star", "en")
	if _, s := get("/star", "en"); !strings.HasPrefix(s, "miss") {
		t.Fatalf("Vary: * is never stored: %q", s)
	}
	// Vary: Cookie follows the jar
	tab.Context().JarRef().Set(&Cookie{Name: "a", Value: "1", Domain: "127.0.0.1", Path: "/"})
	cget(t, tab, srv.URL+"/cookie", KindXHR, nil)
	if _, s, _ := cget(t, tab, srv.URL+"/cookie", KindXHR, nil); !strings.HasPrefix(s, "hit") {
		t.Fatalf("same cookie: %q", s)
	}
	tab.Context().JarRef().Set(&Cookie{Name: "a", Value: "2", Domain: "127.0.0.1", Path: "/"})
	if b, s, _ := cget(t, tab, srv.URL+"/cookie", KindXHR, nil); !strings.HasPrefix(s, "miss") || !strings.Contains(b, "a=2") {
		t.Fatalf("another cookie: %q %q", b, s)
	}
}

func TestHTTPCacheRedirects(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/301":
			http.Redirect(w, r, "/final", http.StatusMovedPermanently)
		case "/308":
			w.Header().Set("Cache-Control", "max-age=600")
			http.Redirect(w, r, "/final", http.StatusPermanentRedirect)
		case "/302":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/302fresh":
			w.Header().Set("Cache-Control", "max-age=600")
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Header().Set("Cache-Control", "max-age=600")
			fmt.Fprint(w, "final")
		}
	})
	_, _, tab := cacheTab(t)
	for _, p := range []string{"/301", "/308", "/302fresh"} {
		cget(t, tab, srv.URL+p, KindDocument, nil)
		b, _, _ := cget(t, tab, srv.URL+p, KindDocument, nil)
		if b != "final" || srv.count(p) != 1 {
			t.Errorf("%s: body %q, %d requests to the redirecting URL (cached redirect: want 1)", p, b, srv.count(p))
		}
	}
	for i := 0; i < 2; i++ {
		cget(t, tab, srv.URL+"/302", KindDocument, nil)
	}
	if srv.count("/302") != 2 {
		t.Errorf("a 302 without a lifetime is not cached: %d", srv.count("/302"))
	}
	if srv.count("/final") != 1 {
		t.Errorf("the target is cached too: %d requests", srv.count("/final"))
	}
}

func TestHTTPCacheErrorStatuses(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/404":
			w.WriteHeader(404)
		case "/404fresh":
			w.Header().Set("Cache-Control", "max-age=600")
			w.WriteHeader(404)
		case "/500":
			w.Header().Set("Cache-Control", "max-age=600")
			w.WriteHeader(500)
		}
		fmt.Fprint(w, "err")
	})
	_, _, tab := cacheTab(t)
	for p, want := range map[string]int{"/404": 2, "/404fresh": 1, "/500": 2} {
		cget(t, tab, srv.URL+p, KindImage, nil)
		cget(t, tab, srv.URL+p, KindImage, nil)
		if srv.count(p) != want {
			t.Errorf("%s: %d requests, want %d", p, srv.count(p), want)
		}
	}
}

func TestHTTPCacheStaleIfError(t *testing.T) {
	var fail atomic.Bool
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"e"`)
		if r.URL.Path == "/sie" {
			w.Header().Set("Cache-Control", "max-age=0, stale-if-error=600")
		} else {
			w.Header().Set("Cache-Control", "max-age=0, stale-if-error=600, must-revalidate")
		}
		if fail.Load() {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, "ok")
	})
	_, _, tab := cacheTab(t)
	cget(t, tab, srv.URL+"/sie", KindStyle, nil)
	cget(t, tab, srv.URL+"/mr", KindStyle, nil)
	fail.Store(true)
	if b, s, c := cget(t, tab, srv.URL+"/sie", KindStyle, nil); b != "ok" || c != 200 || s != "stale-if-error" {
		t.Fatalf("stale-if-error: %q %d %q", b, c, s)
	}
	if _, _, c := cget(t, tab, srv.URL+"/mr", KindStyle, nil); c != 503 {
		t.Fatalf("must-revalidate forbids stale-if-error: %d", c)
	}
}

func TestHTTPCacheRangeRequests(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Accept-Ranges", "bytes")
		http.ServeContent(w, r, "m.mp4", time.Unix(1700000000, 0), strings.NewReader(strings.Repeat("0123456789", 100)))
	})
	_, _, tab := cacheTab(t)
	r1 := map[string]string{"Range": "bytes=0-99"}
	r2 := map[string]string{"Range": "bytes=100-199"}
	b, _, c := cget(t, tab, srv.URL+"/m.mp4", KindMedia, r1)
	if c != 206 || len(b) != 100 {
		t.Fatalf("range: %d %d", c, len(b))
	}
	b, s, c := cget(t, tab, srv.URL+"/m.mp4", KindMedia, r1)
	if c != 206 || len(b) != 100 {
		t.Fatalf("cached range: %d %d", c, len(b))
	}
	wantState(t, "same range", s, "hit")
	if _, s, _ := cget(t, tab, srv.URL+"/m.mp4", KindMedia, r2); strings.HasPrefix(s, "hit") {
		t.Fatalf("another range must not hit: %q", s)
	}
	// the full resource is not answered with a range entry and vice versa
	if b, _, c := cget(t, tab, srv.URL+"/m.mp4", KindMedia, nil); c != 200 || len(b) != 1000 {
		t.Fatalf("full: %d %d", c, len(b))
	}
}

func TestHTTPCacheRequestDirectives(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("ETag", `"e"`)
		fmt.Fprint(w, r.Header.Get("If-None-Match")+"|body")
	})
	_, _, tab := cacheTab(t)
	cget(t, tab, srv.URL+"/r", KindXHR, nil)
	// a reload (Cache-Control: no-cache on the request) goes to the network, unconditionally
	b, s, _ := cget(t, tab, srv.URL+"/r", KindXHR, map[string]string{"Cache-Control": "no-cache"})
	if b != "|body" || !strings.HasPrefix(s, "miss") || srv.count("/r") != 2 {
		t.Fatalf("no-cache request: %q %q %d", b, s, srv.count("/r"))
	}
	// and its answer replaced the stored copy
	if _, s, _ := cget(t, tab, srv.URL+"/r", KindXHR, nil); !strings.HasPrefix(s, "hit") {
		t.Fatalf("after reload: %q", s)
	}
	// page-made conditional requests pass through untouched
	if _, s, c := cget(t, tab, srv.URL+"/r", KindXHR, map[string]string{"If-None-Match": `"zzz"`}); c != 200 || s != "bypass" {
		t.Fatalf("conditional by the page: %d %q", c, s)
	}
	// request no-store is not stored
	cget(t, tab, srv.URL+"/r2", KindXHR, map[string]string{"Cache-Control": "no-store"})
	if _, s, _ := cget(t, tab, srv.URL+"/r2", KindXHR, nil); !strings.HasPrefix(s, "miss") {
		t.Fatalf("request no-store: %q", s)
	}
}

func TestHTTPCacheUnsafeMethodInvalidates(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		fmt.Fprint(w, r.Method)
	})
	_, _, tab := cacheTab(t)
	cget(t, tab, srv.URL+"/item", KindXHR, nil)
	if _, err := tab.httpDo(httpOpts{method: "POST", url: srv.URL + "/item", kind: KindXHR}); err != nil {
		t.Fatal(err)
	}
	if _, s, _ := cget(t, tab, srv.URL+"/item", KindXHR, nil); !strings.HasPrefix(s, "miss") {
		t.Fatalf("a POST invalidates the stored GET: %q", s)
	}
}

func TestHTTPCacheNoStoreDropsOlderCopy(t *testing.T) {
	var n atomic.Int32
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 2 {
			w.Header().Set("Cache-Control", "no-store")
		} else {
			w.Header().Set("Cache-Control", "max-age=0")
			w.Header().Set("ETag", `"e"`)
		}
		fmt.Fprint(w, "x")
	})
	_, _, tab := cacheTab(t)
	cget(t, tab, srv.URL+"/a", KindXHR, nil) // stored, stale
	cget(t, tab, srv.URL+"/a", KindXHR, nil) // revalidation answers 200 no-store
	cget(t, tab, srv.URL+"/a", KindXHR, nil)
	if r := srv.last("/a"); r.Header.Get("If-None-Match") != "" {
		t.Fatalf("the no-store answer must have dropped the old copy, got INM %q", r.Header.Get("If-None-Match"))
	}
}

func TestHTTPCacheContextsAreIsolated(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		fmt.Fprint(w, "x")
	})
	e := newDSEngine(t)
	_, t1 := ctxTab(t, e, nil)
	_, t2 := ctxTab(t, e, nil)
	cget(t, t1, srv.URL+"/iso", KindImage, nil)
	cget(t, t2, srv.URL+"/iso", KindImage, nil)
	if srv.count("/iso") != 2 {
		t.Fatalf("a private context must not read what another stored: %d requests", srv.count("/iso"))
	}
}

func TestHTTPCacheTabsInOneContextShare(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		fmt.Fprint(w, "x")
	})
	e := newDSEngine(t)
	c, t1 := ctxTab(t, e, nil)
	t2, _ := c.NewTab("about:blank")
	cget(t, t1, srv.URL+"/sh", KindImage, nil)
	cget(t, t2, srv.URL+"/sh", KindImage, nil)
	if srv.count("/sh") != 1 {
		t.Fatalf("%d requests", srv.count("/sh"))
	}
}

func TestHTTPCacheKeyNormalisation(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		fmt.Fprint(w, r.URL.RawQuery)
	})
	_, _, tab := cacheTab(t)
	cget(t, tab, srv.URL+"/k?b=2&a=1#one", KindImage, nil)
	cget(t, tab, srv.URL+"/k?b=2&a=1#two", KindImage, nil)
	cget(t, tab, srv.URL+"/k?a=1&b=2", KindImage, nil)
	if n := srv.count("/k"); n != 2 {
		t.Fatalf("fragments are ignored, query order is not: %d requests, want 2", n)
	}
}

func TestHTTPCacheSavesBytesOnRepeatNavigation(t *testing.T) {
	big := strings.Repeat("a", 50<<10)
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><script src="/app.js"></script></head><body><p id=t>hi</p></body></html>`)
		case "/app.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			fmt.Fprint(w, "window.ok=1;/*"+big+"*/")
		}
	})
	e := newDSEngine(t)
	c, tab := ctxTab(t, e, nil)
	nav(t, tab, srv.URL+"/")
	cold := c.DataStats().Used()
	nav(t, tab, srv.URL+"/")
	warm := c.DataStats().Used() - cold
	if srv.count("/app.js") != 1 {
		t.Fatalf("immutable script fetched %d times", srv.count("/app.js"))
	}
	if warm > cold/10 || cold < 50<<10 {
		t.Fatalf("warm visit cost %d of %d cold", warm, cold)
	}
	if v, _ := tab.Evaluate(`window.ok`); string(v) != "1" {
		t.Fatal("cached script did not run")
	}
}

// ---- disk tier -------------------------------------------------------------------

func diskEngine(t *testing.T, dir string) *Engine {
	t.Helper()
	persistLoopback = true
	t.Cleanup(func() { persistLoopback = false })
	o := DefaultOptions()
	o.CacheDir = dir
	e := New(o)
	t.Cleanup(func() { e.Close() })
	return e
}

func TestHTTPCachePersistsOnDiskAcrossEngines(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Set-Cookie", "tracker=1")
		fmt.Fprint(w, "png bytes")
	})
	dir := t.TempDir()
	e1 := diskEngine(t, dir)
	tab1, _ := e1.NewTab("about:blank")
	cget(t, tab1, srv.URL+"/p.png", KindImage, nil)
	e1.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "http", "*", "*"))
	if len(files) != 1 {
		t.Fatalf("expected one cache file, got %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if strings.Contains(string(b), "tracker") {
		t.Fatal("Set-Cookie must not be persisted")
	}
	e2 := diskEngine(t, dir)
	tab2, _ := e2.NewTab("about:blank")
	body, st, _ := cget(t, tab2, srv.URL+"/p.png", KindImage, nil)
	if body != "png bytes" || st != "hit" || srv.count("/p.png") != 1 {
		t.Fatalf("second process: %q %q, %d requests", body, st, srv.count("/p.png"))
	}
}

func TestHTTPCacheIncognitoNeverTouchesDisk(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		fmt.Fprint(w, "secret")
	})
	dir := t.TempDir()
	e := diskEngine(t, dir)
	_, tab := ctxTab(t, e, nil)
	cget(t, tab, srv.URL+"/i", KindImage, nil)
	e.res.flush()
	if files, _ := filepath.Glob(filepath.Join(dir, "http", "*", "*")); len(files) != 0 {
		t.Fatalf("a non-default context wrote to disk: %v", files)
	}
}

func TestHTTPCacheLoopbackIsNotPersistedByDefault(t *testing.T) {
	srv := newCacheSrv(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		fmt.Fprint(w, "dev")
	})
	dir := t.TempDir()
	o := DefaultOptions()
	o.CacheDir = dir
	e := New(o)
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	cget(t, tab, srv.URL+"/l", KindImage, nil)
	e.res.flush()
	if files, _ := filepath.Glob(filepath.Join(dir, "http", "*", "*")); len(files) != 0 {
		t.Fatalf("localhost responses must not outlive the server: %v", files)
	}
}

func TestDiskCacheEvictsLeastRecentlyUsed(t *testing.T) {
	dir := t.TempDir()
	d := newDiskCache(dir, 40<<10)
	mk := func(i int) *cacheEntry {
		return &cacheEntry{Key: fmt.Sprintf("https://x.test/%d", i), URL: "u", Status: 200, Header: hdr("ETag", "x"), Resp: time.Now(), Body: []byte(strings.Repeat("z", 10<<10)), BodyN: 10 << 10}
	}
	for i := 0; i < 3; i++ {
		d.write(mk(i))
		d.flush()
		time.Sleep(20 * time.Millisecond) // distinct mtimes
	}
	if d.read("https://x.test/0") == nil { // touch: file 0 stays recent? mtime only moves after an hour, so make it so
		t.Fatal("entry 0 missing")
	}
	old := time.Now().Add(-3 * time.Hour)
	_ = os.Chtimes(d.path("https://x.test/0"), old, old)
	for i := 3; i < 8; i++ {
		d.write(mk(i))
		d.flush()
		time.Sleep(20 * time.Millisecond)
	}
	if d.read("https://x.test/0") != nil {
		t.Fatal("the oldest entry should have been evicted")
	}
	if d.read("https://x.test/7") == nil {
		t.Fatal("the newest entry must survive")
	}
	var total int64
	files, _ := filepath.Glob(filepath.Join(dir, "*", "*"))
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			total += st.Size()
		}
	}
	if total > 40<<10 {
		t.Fatalf("directory is %d bytes, cap is %d", total, 40<<10)
	}
	d.close()
}

func TestDiskCacheCorruptFilesAreMisses(t *testing.T) {
	d := newDiskCache(t.TempDir(), 1<<20)
	e := &cacheEntry{Key: "https://x.test/c", Status: 200, Header: hdr("ETag", "x"), Resp: time.Now(), Body: []byte("hello")}
	e.BodyN = 5
	d.write(e)
	d.flush()
	p := d.path(e.Key)
	if d.read(e.Key) == nil {
		t.Fatal("round trip")
	}
	b, _ := os.ReadFile(p)
	_ = os.WriteFile(p, b[:len(b)-2], 0o600) // truncated body
	if d.read(e.Key) != nil {
		t.Fatal("truncated file must be a miss")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("corrupt file should have been removed")
	}
	d.close()
}

func TestDiskCacheConcurrentWritersAndReaders(t *testing.T) {
	d := newDiskCache(t.TempDir(), 8<<20)
	defer d.close()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				body := []byte(strings.Repeat(string(rune('a'+w)), 1000+i))
				e := &cacheEntry{Key: fmt.Sprintf("https://x.test/%d", i%5), Status: 200, Header: hdr("ETag", "x"), Resp: time.Now(), Body: body}
				e.BodyN = len(body)
				d.write(e)
				if got := d.read(e.Key); got != nil {
					// whatever writer won, the entry must be whole and self-consistent
					if len(got.Body) != got.BodyN || strings.Trim(string(got.Body), string(got.Body[:1])) != "" {
						t.Errorf("torn entry: %d bytes, BodyN %d", len(got.Body), got.BodyN)
						return
					}
				}
			}
		}(w)
	}
	wg.Wait()
	d.flush()
}

func TestMemoryTierEvictsAndBoundsSize(t *testing.T) {
	c := newHTTPCache(1, "", 0) // 1 MiB
	for i := 0; i < 20; i++ {
		c.put(&cacheEntry{Key: fmt.Sprintf("k%d", i), Status: 200, Header: http.Header{}, Body: make([]byte, 200<<10)}, false)
	}
	if c.bytes > c.max {
		t.Fatalf("memory tier %d over its cap %d", c.bytes, c.max)
	}
	if c.peek("k19") == nil || c.peek("k0") != nil {
		t.Fatal("LRU order")
	}
}
