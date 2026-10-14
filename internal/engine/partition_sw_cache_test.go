package engine

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A service worker registered by an embed is the embed's under that top-level site only.
func TestServiceWorkerRegistrationsArePartitioned(t *testing.T) {
	s := newSites(t, "a.localhost", "c.localhost", "b.localhost")
	s.page("b.localhost", "/sw.js", `self.addEventListener('install', () => self.skipWaiting());`)
	s.page("b.localhost", "/reg.html", `<!doctype html><title>reg</title><script>
window.out = null; window.step = 0;
(async () => {
  window.step = 1;
  const before = (await navigator.serviceWorker.getRegistrations()).length;
  window.step = 2;
  const reg = await navigator.serviceWorker.register('/sw.js');
  window.step = 3;
  await navigator.serviceWorker.ready;
  window.step = 4;
  window.out = { before, after: (await navigator.serviceWorker.getRegistrations()).length, scope: reg.scope };
})().catch((e) => { window.out = { error: String(e) }; });
</script>`)
	for _, h := range []string{"a.localhost", "c.localhost"} {
		s.page(h, "/", `<!doctype html><iframe src="`+s.u("b.localhost", "/reg.html")+`"></iframe>`)
	}
	e := newSiteEngine(t)
	read := func(tab *Tab) map[string]any {
		tab.WaitFrames(10 * time.Second)
		kid := tab.FrameTabs()
		if len(kid) == 0 {
			kid = []*Tab{tab}
		}
		for i := 0; i < 100; i++ {
			tab.Settle(50 * time.Millisecond) // a top-level headless tab runs its event loop only when asked
			raw, _ := kid[0].Evaluate(`JSON.stringify(window.out)`)
			var js string
			_ = json.Unmarshal(raw, &js)
			if js != "" && js != "null" {
				var m map[string]any
				_ = json.Unmarshal([]byte(js), &m)
				if m["error"] != nil {
					t.Fatalf("%v\n%s", m["error"], consoleText(tab))
				}
				return m
			}
			time.Sleep(100 * time.Millisecond)
		}
		st, _ := kid[0].Evaluate(`window.step`)
		t.Fatalf("no result, step %s regs %+v\n%s", st, e.DefaultContext().ServiceWorkers(), consoleText(tab))
		return nil
	}
	tabA, _ := e.NewTab(s.u("a.localhost", "/"))
	if r := read(tabA); num(r, "before") != 0 || num(r, "after") != 1 {
		t.Fatalf("under a: %v", r)
	}
	tabC, _ := e.NewTab(s.u("c.localhost", "/"))
	if r := read(tabC); num(r, "before") != 0 || num(r, "after") != 1 {
		t.Fatalf("the registration under a must not be visible under c: %v", r)
	}
	top, _ := e.NewTab(s.u("b.localhost", "/reg.html"))
	if r := read(top); num(r, "before") != 0 || num(r, "after") != 1 {
		t.Fatalf("the embed's registrations must not be visible to its first-party self: %v", r)
	}
	if n := len(e.DefaultContext().ServiceWorkers()); n != 3 {
		t.Fatalf("want three separate registrations, have %d", n)
	}
	e.DefaultContext().ClearSiteData("http://b.localhost:" + s.port)
	if n := len(e.DefaultContext().ServiceWorkers()); n != 0 {
		t.Fatalf("clearing the embed's origin must clear every partition's registration, %d left", n)
	}
}

// The HTTP cache is keyed by top-level site: a copy one site loaded is not a hit for another, which closes the
// cache-probing side channel; within one site it still hits.
func TestHTTPCacheIsPartitionedByTopLevelSite(t *testing.T) {
	s := newSites(t, "a.localhost", "c.localhost", "cdn.localhost")
	s.handle("cdn.localhost", "/lib.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=600")
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte("window.lib = 1;"))
	})
	for _, h := range []string{"a.localhost", "c.localhost"} {
		s.page(h, "/", `<!doctype html><script src="`+s.u("cdn.localhost", "/lib.js")+`"></script>`)
		s.page(h, "/2", `<!doctype html><script src="`+s.u("cdn.localhost", "/lib.js")+`"></script>`)
	}
	e := newSiteEngine(t)
	load := func(h, p string) {
		if _, err := e.NewTab(s.u(h, p)); err != nil {
			t.Fatal(err)
		}
	}
	load("a.localhost", "/")
	load("a.localhost", "/2")
	if n := len(s.hitsFor("cdn.localhost", "/lib.js")); n != 1 {
		t.Fatalf("within one site the second load is a cache hit, the CDN saw %d requests", n)
	}
	load("c.localhost", "/")
	if n := len(s.hitsFor("cdn.localhost", "/lib.js")); n != 2 {
		t.Fatalf("another top-level site must not be served a.localhost's copy, the CDN saw %d requests", n)
	}
	load("c.localhost", "/2")
	if n := len(s.hitsFor("cdn.localhost", "/lib.js")); n != 2 {
		t.Fatalf("and then hits its own copy, the CDN saw %d requests", n)
	}
	_ = strings.TrimSpace
}
