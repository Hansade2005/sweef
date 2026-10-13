package engine

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// cacheCase describes one resource the cache tests serve.
type cacheCase struct {
	path    string
	cc      string // Cache-Control ("" = none sent)
	etag    string
	vary    string
	body    string
	extra   map[string]string
	noValid bool // do not honour If-None-Match
}

func serveCacheSite(s *dsSite, cases []cacheCase) {
	byPath := map[string]cacheCase{}
	for _, c := range cases {
		byPath[c.path] = c
	}
	s.mu.Lock()
	s.hook = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/page" {
			w.Header().Set("Content-Type", "text/html")
			var b strings.Builder
			b.WriteString("<!doctype html><html><head><title>cache</title></head><body><p>cache page</p>")
			for _, c := range cases {
				if strings.HasSuffix(c.path, ".js") {
					fmt.Fprintf(&b, `<script src="%s"></script>`, c.path)
				}
			}
			b.WriteString("</body></html>")
			fmt.Fprint(w, b.String())
			return true
		}
		c, ok := byPath[r.URL.Path]
		if !ok {
			return false
		}
		h := w.Header()
		if strings.HasSuffix(c.path, ".js") {
			h.Set("Content-Type", "text/javascript")
		} else {
			h.Set("Content-Type", "text/html")
		}
		if c.cc != "" {
			h.Set("Cache-Control", c.cc)
		}
		if c.vary != "" {
			h.Set("Vary", c.vary)
		}
		for k, v := range c.extra {
			h.Set(k, v)
		}
		if c.etag != "" {
			h.Set("ETag", c.etag)
			if !c.noValid && r.Header.Get("If-None-Match") == c.etag {
				w.WriteHeader(http.StatusNotModified)
				return true
			}
		}
		fmt.Fprint(w, c.body)
		return true
	}
	s.mu.Unlock()
}

func jsBody(tag string, n int) string {
	return "window." + tag + "=1;/*" + string(filler(n, int64(len(tag)))) + "*/"
}

// reqsFor returns the requests the site saw for path since the last reset.
func reqsFor(s *dsSite, path string) []reqRec {
	var out []reqRec
	for _, r := range s.requests() {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func nav(t *testing.T, tab *Tab, url string) {
	t.Helper()
	if err := tab.Navigate(url, nil); err != nil {
		t.Fatal(err)
	}
}

func TestExtendedCacheRevalidatesWithConditionalRequests(t *testing.T) {
	s := newDSSite(t)
	serveCacheSite(s, []cacheCase{
		{path: "/c/plain.js", etag: `"p1"`, cc: "max-age=0", body: jsBody("plain", 30<<10)},
	})
	e := newDSEngine(t)
	c, tab := ctxTab(t, e, polPtr(SaverStandard))
	nav(t, tab, s.URL()+"/page")
	first := tab.DataStats()
	if rs := reqsFor(s, "/c/plain.js"); len(rs) != 1 || rs[0].Header.Get("If-None-Match") != "" {
		t.Fatalf("first load: %v", rs)
	}
	scriptCost := first.ByKind[KindScript]
	if scriptCost < 30<<10 {
		t.Fatalf("script cost %d", scriptCost)
	}
	s.reset()
	nav(t, tab, s.URL()+"/page")
	rs := reqsFor(s, "/c/plain.js")
	if len(rs) != 1 || rs[0].Header.Get("If-None-Match") != `"p1"` {
		t.Fatalf("second load must revalidate with If-None-Match: %v", rs)
	}
	second := tab.DataStats()
	if second.ByKind[KindScript] > 600 {
		t.Fatalf("a 304 must cost a few hundred bytes, the script exchange cost %d", second.ByKind[KindScript])
	}
	if second.CacheHitBytes < 30<<10 || second.CacheHitBytes > scriptCost {
		t.Fatalf("CacheHitBytes=%d, want the stored transfer size of the script (first load cost %d)", second.CacheHitBytes, scriptCost)
	}
	if v, _ := tab.Evaluate(`window.plain`); string(v) != "1" {
		t.Fatalf("revalidated script did not run: %s", v)
	}
	if c.DataStats().CacheHitBytes != second.CacheHitBytes {
		t.Fatalf("context stats should include the tab's hit: %d vs %d", c.DataStats().CacheHitBytes, second.CacheHitBytes)
	}
}

func TestExtendedCacheFreshnessRules(t *testing.T) {
	s := newDSSite(t)
	serveCacheSite(s, []cacheCase{
		{path: "/c/app.3f9a1c7e.js", etag: `"v"`, cc: "public, max-age=1", body: jsBody("versioned", 4<<10)},
		{path: "/c/unversioned.js", etag: `"u"`, cc: "public, max-age=1", body: jsBody("unversioned", 4<<10)},
		{path: "/c/imm.js", etag: `"i"`, cc: "public, max-age=1, immutable", body: jsBody("imm", 4<<10)},
		{path: "/c/private.js", etag: `"pr"`, cc: "private, max-age=300", body: jsBody("priv", 4<<10)},
		{path: "/c/nostore.js", etag: `"ns"`, cc: "no-store", body: jsBody("nostore", 4<<10)},
		{path: "/c/nocache.js", etag: `"nc"`, cc: "no-cache", body: jsBody("nocache", 4<<10)},
		{path: "/c/noval.js", cc: "max-age=0", body: jsBody("noval", 4<<10)},
	})
	e := newDSEngine(t)
	for _, level := range []SaverLevel{SaverStandard, SaverOff} {
		t.Run(level.String(), func(t *testing.T) {
			_, tab := ctxTab(t, e, polPtr(level))
			s.reset()
			nav(t, tab, s.URL()+"/page")
			time.Sleep(1200 * time.Millisecond) // past max-age=1
			s.reset()
			nav(t, tab, s.URL()+"/page")
			n := func(p string) int { return len(reqsFor(s, p)) }
			cond := func(p string) bool {
				rs := reqsFor(s, p)
				return len(rs) == 1 && rs[0].Header.Get("If-None-Match") != ""
			}
			if level == SaverStandard {
				if n("/c/app.3f9a1c7e.js") != 0 || n("/c/imm.js") != 0 {
					t.Fatalf("versioned/immutable assets are fresh for a day: %v", s.requests())
				}
				if !cond("/c/unversioned.js") {
					t.Fatalf("an unversioned asset past max-age is revalidated, not refetched: %v", reqsFor(s, "/c/unversioned.js"))
				}
				if n("/c/private.js") != 0 {
					t.Fatalf("private responses are cacheable in a browser cache: %v", reqsFor(s, "/c/private.js"))
				}
				if !cond("/c/nocache.js") {
					t.Fatalf("no-cache must be revalidated on the next navigation: %v", reqsFor(s, "/c/nocache.js"))
				}
			} else {
				// off keeps the engine's old rules: stale after max-age, so revalidated
				if !cond("/c/app.3f9a1c7e.js") || !cond("/c/unversioned.js") {
					t.Fatalf("off: max-age=1 is stale after 1.2s: %v", s.requests())
				}
			}
			// whatever the level: no-store is never stored, a response without a validator cannot be revalidated
			// (and a script is read once per navigation, however many consumers ask for it: prefetch, then loader)
			wantN := 1
			paths := []string{"/c/nostore.js", "/c/noval.js"}
			if level == SaverOff {
				paths = paths[:1] // off serves a max-age=0 response from its 5 s memory window, as before
			}
			for _, p := range paths {
				rs := reqsFor(s, p)
				if len(rs) != wantN {
					t.Fatalf("%s: %d requests", p, len(rs))
				}
				for _, r := range rs {
					if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
						t.Fatalf("%s must be fetched in full, never conditionally: %v", p, r.Header)
					}
				}
			}
		})
	}
}

func TestExtendedCacheVaryAndPartition(t *testing.T) {
	s := newDSSite(t)
	serveCacheSite(s, []cacheCase{
		{path: "/c/user.js", etag: `"x"`, cc: "max-age=600", vary: "Cookie", body: jsBody("user", 2<<10), noValid: true},
		{path: "/c/star.js", cc: "max-age=600", vary: "*", body: jsBody("star", 2<<10)},
		{path: "/c/shared.js", cc: "max-age=600", body: jsBody("shared", 2<<10)},
	})
	e := newDSEngine(t)
	c, tab := ctxTab(t, e, polPtr(SaverStandard))
	setUser := func(u string) {
		c.ClearCookies("")
		_ = c.AddCookies([]map[string]any{{"name": "u", "value": u, "url": s.URL()}})
	}
	setUser("alice")
	nav(t, tab, s.URL()+"/page")
	s.reset()
	nav(t, tab, s.URL()+"/page")
	if n := len(reqsFor(s, "/c/user.js")); n != 0 {
		t.Fatalf("same Cookie: served from the cache, saw %d requests", n)
	}
	setUser("bob")
	s.reset()
	nav(t, tab, s.URL()+"/page")
	if n := len(reqsFor(s, "/c/user.js")); n != 1 {
		t.Fatalf("Vary: Cookie with another cookie must not be served from the cache, saw %d requests", n)
	}
	if n := len(reqsFor(s, "/c/star.js")); n != 1 {
		t.Fatalf("Vary: * is never reusable, saw %d requests", n)
	}
	// another context never reads this context's entries
	_, tab2 := ctxTab(t, e, polPtr(SaverStandard))
	s.reset()
	nav(t, tab2, s.URL()+"/page")
	if n := len(reqsFor(s, "/c/shared.js")); n != 1 {
		t.Fatalf("a second context must fetch for itself, saw %d", n)
	}
}

func TestExtendedCacheDocumentsAreRevalidatedUnlessTheServerSaysFresh(t *testing.T) {
	s := newDSSite(t)
	serveCacheSite(s, []cacheCase{
		{path: "/d/fresh.html", etag: `"d1"`, cc: "max-age=3600", body: "<html><body><p id=t>document one</p></body></html>"},
		{path: "/d/stale.html", etag: `"s1"`, cc: "max-age=0", body: "<html><body><p id=t>stale one</p></body></html>"},
		{path: "/d/heur.html", etag: `"h1"`, extra: map[string]string{"Last-Modified": "Mon, 01 Jan 2024 00:00:00 GMT"}, body: "<html><body><p id=t>heuristic</p></body></html>"},
		{path: "/d/nostore.html", etag: `"d2"`, cc: "no-store", body: "<html><body><p id=t>never stored</p></body></html>"},
		{path: "/d/plain.html", body: "<html><body><p id=t>no validators</p></body></html>"},
	})
	e := newDSEngine(t)
	for _, lvl := range []SaverLevel{SaverStandard, SaverOff} {
		c, tab := ctxTab(t, e, polPtr(lvl))
		nav(t, tab, s.URL()+"/d/fresh.html")
		s.reset()
		nav(t, tab, s.URL()+"/d/fresh.html")
		if rs := reqsFor(s, "/d/fresh.html"); len(rs) != 0 {
			t.Fatalf("%s: the server said fresh for an hour, no request expected: %v", lvl, rs)
		}
		if txt, _ := tab.Text("#t"); txt != "document one" {
			t.Fatalf("%s: fresh document from the cache, got %q", lvl, txt)
		}
		// max-age=0 with a validator: always a conditional request, a 304 shows the stored document
		nav(t, tab, s.URL()+"/d/stale.html")
		s.reset()
		nav(t, tab, s.URL()+"/d/stale.html")
		rs := reqsFor(s, "/d/stale.html")
		if len(rs) != 1 || rs[0].Header.Get("If-None-Match") != `"s1"` {
			t.Fatalf("%s: a stale document is revalidated: %v", lvl, rs)
		}
		if txt, _ := tab.Text("#t"); txt != "stale one" {
			t.Fatalf("%s: 304 must show the stored document, got %q", lvl, txt)
		}
		if hit := c.DataStats().CacheHitBytes; hit < 100 {
			t.Fatalf("%s: a 304 on a document is a measured saving, got %d", lvl, hit)
		}
		// no stated lifetime: never heuristically fresh for a document
		nav(t, tab, s.URL()+"/d/heur.html")
		s.reset()
		nav(t, tab, s.URL()+"/d/heur.html")
		if rs := reqsFor(s, "/d/heur.html"); len(rs) != 1 || rs[0].Header.Get("If-None-Match") != `"h1"` {
			t.Fatalf("%s: a document without a stated lifetime is revalidated, never served on a heuristic: %v", lvl, rs)
		}
		// a changed document replaces the stored one
		s.mu.Lock()
		old := s.hook
		s.hook = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path == "/d/stale.html" {
				w.Header().Set("ETag", `"s1b"`)
				w.Header().Set("Cache-Control", "max-age=0")
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, "<html><body><p id=t>stale two</p></body></html>")
				return true
			}
			return old(w, r)
		}
		s.mu.Unlock()
		nav(t, tab, s.URL()+"/d/stale.html")
		if txt, _ := tab.Text("#t"); txt != "stale two" {
			t.Fatalf("%s: changed document: %q", lvl, txt)
		}
		for _, p := range []string{"/d/nostore.html", "/d/plain.html"} {
			nav(t, tab, s.URL()+p)
			s.reset()
			nav(t, tab, s.URL()+p)
			rs := reqsFor(s, p)
			if len(rs) != 1 || rs[0].Header.Get("If-None-Match") != "" || rs[0].Header.Get("If-Modified-Since") != "" {
				t.Fatalf("%s: %s must not be conditional: %v", lvl, p, rs)
			}
		}
		// reload bypasses the stored copy
		nav(t, tab, s.URL()+"/d/fresh.html")
		s.reset()
		if err := tab.Reload(); err != nil {
			t.Fatal(err)
		}
		if rs := reqsFor(s, "/d/fresh.html"); len(rs) != 1 || rs[0].Header.Get("If-None-Match") != "" {
			t.Fatalf("%s: reload is unconditional: %v", lvl, rs)
		}
		serveCacheSite(s, []cacheCase{
			{path: "/d/fresh.html", etag: `"d1"`, cc: "max-age=3600", body: "<html><body><p id=t>document one</p></body></html>"},
			{path: "/d/stale.html", etag: `"s1"`, cc: "max-age=0", body: "<html><body><p id=t>stale one</p></body></html>"},
			{path: "/d/heur.html", etag: `"h1"`, extra: map[string]string{"Last-Modified": "Mon, 01 Jan 2024 00:00:00 GMT"}, body: "<html><body><p id=t>heuristic</p></body></html>"},
			{path: "/d/nostore.html", etag: `"d2"`, cc: "no-store", body: "<html><body><p id=t>never stored</p></body></html>"},
			{path: "/d/plain.html", body: "<html><body><p id=t>no validators</p></body></html>"},
		})
		s.reset()
	}
}
