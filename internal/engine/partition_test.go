package engine

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// embedPage is the "third party": a document that uses every kind of storage and reports what it found there
// before it wrote. Found counters are 0 in a fresh partition and grow with each load in the same one.
const embedPage = `<!doctype html><title>embed</title><script>
(async () => {
  const r = { cookieSeen: document.cookie };
  try { r.ls = +(localStorage.getItem('n') || 0); localStorage.setItem('n', String(r.ls + 1)); } catch (e) { r.ls = e.name; }
  sessionStorage.setItem('s', '1');
  r.idb = await new Promise((res, rej) => {
    const q = indexedDB.open('e', 1);
    q.onupgradeneeded = () => q.result.createObjectStore('k');
    q.onsuccess = () => {
      const db = q.result, tx = db.transaction('k', 'readwrite'), st = tx.objectStore('k'), g = st.get('n');
      g.onsuccess = () => { const n = g.result || 0; st.put(n + 1, 'n'); tx.oncomplete = () => { db.close(); res(n); }; };
    };
    q.onerror = () => rej(q.error);
  });
  const c = await caches.open('c'); r.cache = (await c.keys()).length; await c.put('/k' + r.cache, new Response('x'));
  const root = await navigator.storage.getDirectory(); const fh = await root.getFileHandle('f', { create: true });
  r.opfs = (await fh.getFile()).size; const w = await fh.createWritable(); await w.write('x'.repeat(r.opfs + 1)); await w.close();
  r.sw = (await navigator.serviceWorker.getRegistrations()).length;
  r.locks = (await navigator.locks.query()).held.length;
  window.result = r;
  window.worker = new Worker('/w.js');
  window.worker.onmessage = (e) => { window.workerIDB = e.data; };
  window.workerBC = [];
  window.bc = new BroadcastChannel('room'); window.bc.onmessage = (e) => window.workerBC.push(e.data);
})().catch((e) => { window.result = { error: String(e) }; });
</script>`

const embedWorker = `
const q = indexedDB.open('e', 1);
q.onsuccess = () => { const g = q.result.transaction('k').objectStore('k').get('n'); g.onsuccess = () => postMessage({ idbN: g.result || 0, ls: typeof localStorage }); };
q.onupgradeneeded = () => q.result.createObjectStore('k');`

type partSites struct {
	*sites
	cookiesSeen map[string][]string // path -> Cookie headers the server received
}

func newPartSites(t *testing.T) *partSites {
	s := &partSites{sites: newSites(t, "a.localhost", "c.localhost", "b.localhost", "tracker.localhost", "x.a.localhost"), cookiesSeen: map[string][]string{}}
	s.page("b.localhost", "/w.js", embedWorker)
	s.handle("b.localhost", "/embed.html", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.cookiesSeen["embed"] = append(s.cookiesSeen["embed"], r.Header.Get("Cookie"))
		s.mu.Unlock()
		if !strings.Contains(r.Header.Get("Cookie"), "uid=") {
			w.Header().Add("Set-Cookie", "uid=embed-"+r.Header.Get("Sec-Fetch-Site")+"; SameSite=None; Secure; Path=/")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(embedPage))
	})
	s.handle("tracker.localhost", "/pixel.png", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.cookiesSeen["pixel"] = append(s.cookiesSeen["pixel"], r.Header.Get("Cookie"))
		s.mu.Unlock()
		if !strings.Contains(r.Header.Get("Cookie"), "tid=") {
			w.Header().Add("Set-Cookie", "tid=t1; SameSite=None; Secure; Path=/")
		}
		w.Header().Set("Content-Type", "image/gif")
		w.Write([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"))
	})
	return s
}

func (s *partSites) seen(key string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.cookiesSeen[key]...)
}

func (s *partSites) embedder(host, path string) {
	s.page(host, path, `<!doctype html><title>`+host+`</title><body><iframe src="`+s.u("b.localhost", "/embed.html")+`"></iframe><script>var im = new Image(); im.src = "`+s.u("tracker.localhost", "/pixel.png")+`";</script></body>`)
}

// frameResult waits for the embed in the tab's first frame to report, and returns what it found.
func frameResult(t *testing.T, tab *Tab) map[string]any {
	t.Helper()
	tab.WaitFrames(10 * time.Second)
	kids := tab.FrameTabs()
	if len(kids) == 0 {
		t.Fatalf("no frame in %s", tab.URL())
	}
	for i := 0; i < 300; i++ {
		raw, err := kids[0].Evaluate(`JSON.stringify(window.result || null)`)
		if err == nil {
			var s string
			_ = json.Unmarshal(raw, &s)
			if s != "null" && s != "" {
				var m map[string]any
				if json.Unmarshal([]byte(s), &m) != nil {
					t.Fatalf("bad result %s", s)
				}
				if m["error"] != nil {
					t.Fatalf("the embed failed: %v\n%s", m["error"], consoleText(tab))
				}
				return m
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the embed in %s never reported\n%s", tab.URL(), consoleText(tab))
	return nil
}

func num(m map[string]any, k string) int { f, _ := m[k].(float64); return int(f) }

// The headline behaviour: the same embed under two sites has two separate sets of state, and under one site
// it keeps its state between loads.
func TestEmbedHasSeparateStorageUnderEachTopLevelSite(t *testing.T) {
	s := newPartSites(t)
	s.embedder("a.localhost", "/one.html")
	s.embedder("a.localhost", "/two.html")
	s.embedder("c.localhost", "/one.html")
	e := newSiteEngine(t)

	open := func(host, path string) *Tab {
		tab, err := e.NewTab(s.u(host, path))
		if err != nil {
			t.Fatal(err)
		}
		return tab
	}
	a1 := open("a.localhost", "/one.html")
	r1 := frameResult(t, a1)
	for _, k := range []string{"ls", "idb", "cache", "opfs"} {
		if num(r1, k) != 0 {
			t.Fatalf("a fresh partition must be empty, %s=%v in %v", k, r1[k], r1)
		}
	}
	// the response of the first load set the cookie in this partition, so its own script sees it; the server
	// saw none on the request (checked below)
	if r1["cookieSeen"] != "uid=embed-cross-site" {
		t.Fatalf("the embed's own cookie should be visible to its script: %v", r1["cookieSeen"])
	}
	// a second page of the same site embedding the same origin sees what the first left: one partition per top-level site
	a2 := open("a.localhost", "/two.html")
	r2 := frameResult(t, a2)
	for _, k := range []string{"ls", "idb", "cache", "opfs"} {
		if num(r2, k) != 1 {
			t.Fatalf("the same embed under the same site must find its state (%s=%v): %v", k, r2[k], r2)
		}
	}
	if r2["cookieSeen"] != "uid=embed-cross-site" {
		t.Fatalf("the embed's cookie should come back under the same site: %v", r2["cookieSeen"])
	}
	// another site: nothing of it
	c1 := open("c.localhost", "/one.html")
	rc := frameResult(t, c1)
	for _, k := range []string{"ls", "idb", "cache", "opfs"} {
		if num(rc, k) != 0 {
			t.Fatalf("the embed under another top-level site must start empty, %s=%v: %v", k, rc[k], rc)
		}
	}
	if rc["cookieSeen"] != "uid=embed-cross-site" { // its own, set by its own first load under c
		t.Fatalf("unexpected cookies in the embed under c: %v", rc["cookieSeen"])
	}
	// the embed's origin visited as a first party is a third universe
	top := open("b.localhost", "/embed.html")
	for i := 0; i < 300; i++ {
		if raw, _ := top.Evaluate(`JSON.stringify(window.result || null)`); string(raw) != `"null"` && len(raw) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	raw, _ := top.Evaluate(`JSON.stringify(window.result)`)
	var fp string
	_ = json.Unmarshal(raw, &fp)
	var rt map[string]any
	_ = json.Unmarshal([]byte(fp), &rt)
	for _, k := range []string{"ls", "idb", "cache", "opfs"} {
		if num(rt, k) != 0 {
			t.Fatalf("first-party storage must not contain what embeds under other sites wrote: %s=%v (%v)", k, rt[k], rt)
		}
	}
	if got := rt["cookieSeen"].(string); got != "uid=embed-none" { // the cookie the first-party visit itself got
		t.Fatalf("first-party document.cookie sees the wrong jar: %q", got)
	}
	// and what it wrote there (first party) stays out of the partitions: a third load under A still finds exactly 1
	a3 := open("a.localhost", "/one.html")
	r3 := frameResult(t, a3)
	if num(r3, "ls") != 2 || num(r3, "idb") != 2 {
		t.Fatalf("third load under A should find 2, got %v", r3)
	}
	// the server saw the cookie only on the loads under A after the first
	seen := s.seen("embed")
	withUID := 0
	for _, c := range seen {
		if strings.Contains(c, "uid=") {
			withUID++
		}
	}
	if withUID != 2 { // a2 and a3; not c1, not the first-party visit
		t.Fatalf("the embed's cookie was sent on %d of %d requests (want 2): %q", withUID, len(seen), seen)
	}
}

// Workers started by a third-party frame belong to its partition.
func TestEmbedWorkerSharesItsPartitionOnly(t *testing.T) {
	s := newPartSites(t)
	s.embedder("a.localhost", "/one.html")
	s.embedder("c.localhost", "/one.html")
	e := newSiteEngine(t)
	for _, host := range []string{"a.localhost", "a.localhost", "c.localhost"} {
		tab, err := e.NewTab(s.u(host, "/one.html"))
		if err != nil {
			t.Fatal(err)
		}
		frameResult(t, tab)
		kid := tab.FrameTabs()[0]
		var got string
		for i := 0; i < 300; i++ {
			raw, _ := kid.Evaluate(`JSON.stringify(window.workerIDB || null)`)
			_ = json.Unmarshal(raw, &got)
			if got != "null" && got != "" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		var m map[string]any
		_ = json.Unmarshal([]byte(got), &m)
		want := map[string]int{"a.localhost#1": 1, "a.localhost#2": 2, "c.localhost#1": 1}
		_ = want
		t.Logf("%s worker saw idb counter %v", host, m["idbN"])
		// The page of the embed wrote 1 before the worker read (it reads after the page's own write).
		if host == "c.localhost" && num(m, "idbN") != 1 {
			t.Fatalf("under c the worker must see only the embed's own write under c (1), got %v", m)
		}
		if m["ls"] != "undefined" {
			t.Fatalf("a worker has no localStorage: %v", m)
		}
	}
}

func TestThirdPartyImageCookiesArePartitioned(t *testing.T) {
	s := newPartSites(t)
	s.embedder("a.localhost", "/one.html")
	s.embedder("a.localhost", "/two.html")
	s.embedder("c.localhost", "/one.html")
	// the tracker is also a site the person visits: its first-party cookie must not ride on embedded requests
	s.page("tracker.localhost", "/", `<!doctype html><title>tracker home</title>`)
	s.handle("tracker.localhost", "/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "fp=1; Path=/")
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<!doctype html><title>tracker</title>"))
	})
	e := newSiteEngine(t)
	if _, err := e.NewTab(s.u("tracker.localhost", "/")); err != nil {
		t.Fatal(err)
	}
	ctx := e.DefaultContext()
	if got := ctx.JarRef().ForURL(s.u("tracker.localhost", "/")); got != "fp=1" {
		t.Fatalf("first-party cookie: %q", got)
	}
	load := func(host, path string) {
		tab, err := e.NewTab(s.u(host, path))
		if err != nil {
			t.Fatal(err)
		}
		tab.WaitFrames(10 * time.Second)
		tab.Settle(300 * time.Millisecond)
	}
	load("a.localhost", "/one.html")
	load("a.localhost", "/two.html")
	load("c.localhost", "/one.html")
	px := s.seen("pixel")
	if len(px) != 3 {
		t.Fatalf("the pixel was requested %d times, want 3: %q", len(px), px)
	}
	if px[0] != "" {
		t.Fatalf("the first request must carry no cookie (the first-party one stays home): %q", px[0])
	}
	if px[1] != "tid=t1" {
		t.Fatalf("under the same top-level site the pixel's cookie comes back, alone: %q", px[1])
	}
	if px[2] != "" {
		t.Fatalf("under another top-level site it must not: %q", px[2])
	}
	// the unpartitioned jar of the tracker still holds only what the person got by visiting it
	if got := ctx.JarRef().ForURL(s.u("tracker.localhost", "/")); got != "fp=1" {
		t.Fatalf("the unpartitioned jar changed: %q", got)
	}
	n := 0
	for _, c := range ctx.JarRef().All("") {
		if c.Name == "tid" && c.Partition != "" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want two partitioned tid cookies (a, c), have %d", n)
	}
}

func TestCookiePolicyModesExceptionsAndTheStorageAccessAPI(t *testing.T) {
	s := newPartSites(t)
	s.embedder("a.localhost", "/one.html")
	e := newSiteEngine(t)
	ctx := e.DefaultContext()
	pixelCookie := func() string {
		before := len(s.hitsFor("tracker.localhost", "/pixel.png"))
		tab, err := e.NewTab(s.u("a.localhost", "/one.html"))
		if err != nil {
			t.Fatal(err)
		}
		tab.WaitFrames(10 * time.Second)
		tab.Settle(300 * time.Millisecond)
		px := s.hitsFor("tracker.localhost", "/pixel.png")
		if len(px) <= before {
			t.Fatal("no pixel request")
		}
		return px[len(px)-1].Cookie
	}
	jarNames := func(partitioned bool) string {
		var n []string
		for _, c := range ctx.JarRef().All("") {
			if c.Name == "tid" && (c.Partition != "") == partitioned {
				n = append(n, c.Name)
			}
		}
		return strings.Join(n, ",")
	}
	// default: partitioned
	pixelCookie()
	if jarNames(true) != "tid" || jarNames(false) != "" {
		t.Fatalf("default policy should store in a partition: part=%q unpart=%q", jarNames(true), jarNames(false))
	}
	// the legacy switch: unpartitioned
	ctx.ClearCookies("")
	ctx.SetPartitionPolicy(PartitionPolicy{Cookies: CookiesAllowed})
	pixelCookie()
	if jarNames(false) != "tid" || jarNames(true) != "" {
		t.Fatalf("CookiesAllowed should store unpartitioned: part=%q unpart=%q", jarNames(true), jarNames(false))
	}
	// blocked: nothing without the Partitioned attribute
	ctx.ClearCookies("")
	ctx.SetPartitionPolicy(PartitionPolicy{Cookies: CookiesBlocked})
	pixelCookie()
	if jarNames(true) != "" || jarNames(false) != "" {
		t.Fatalf("CookiesBlocked should keep nothing: part=%q unpart=%q", jarNames(true), jarNames(false))
	}
	// ... and with the attribute it keeps a partitioned one
	s.handle("tracker.localhost", "/pixel.png", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.cookiesSeen["pixel"] = append(s.cookiesSeen["pixel"], r.Header.Get("Cookie"))
		s.mu.Unlock()
		w.Header().Add("Set-Cookie", "tid=chips; SameSite=None; Secure; Partitioned; Path=/")
		w.Header().Set("Content-Type", "image/gif")
		w.Write([]byte("GIF89a"))
	})
	pixelCookie()
	if jarNames(true) != "tid" {
		t.Fatalf("a Partitioned cookie is kept under CookiesBlocked: part=%q unpart=%q", jarNames(true), jarNames(false))
	}
	if got := pixelCookie(); got != "tid=chips" {
		t.Fatalf("and sent back in the same partition: %q", got)
	}
	// per-site exception: the hook decides
	s.handle("tracker.localhost", "/pixel.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "tid=t1; SameSite=None; Secure; Path=/")
		w.Header().Set("Content-Type", "image/gif")
		w.Write([]byte("GIF89a"))
	})
	ctx.ClearCookies("")
	ctx.SetPartitionPolicy(PartitionPolicy{Cookies: CookiesPartitioned, Exception: func(top, emb string) bool {
		return top == "http://a.localhost" && emb == "http://tracker.localhost"
	}})
	pixelCookie()
	if jarNames(false) != "tid" {
		t.Fatalf("the exception should give the pair the unpartitioned jar: part=%q unpart=%q", jarNames(true), jarNames(false))
	}
	// SameSite: a Lax cookie from a cross-site context is not accepted
	ctx.SetPartitionPolicy(PartitionPolicy{})
	ctx.ClearCookies("")
	s.handle("tracker.localhost", "/pixel.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "lax=1; SameSite=Lax; Path=/")
		w.Header().Add("Set-Cookie", "dflt=1; Path=/")
		w.Header().Set("Content-Type", "image/gif")
		w.Write([]byte("GIF89a"))
	})
	pixelCookie()
	for _, c := range ctx.JarRef().All("") {
		if c.Name == "lax" || c.Name == "dflt" {
			t.Fatalf("Lax and default-SameSite cookies must be refused in a cross-site context: %+v", *c)
		}
	}

	// ---- Storage Access API ----
	ctx.SetPartitionPolicy(PartitionPolicy{})
	ctx.ClearCookies("")
	s.handle("tracker.localhost", "/pixel.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		w.Write([]byte("GIF89a"))
	})
	s.page("b.localhost", "/fp.html", `<!doctype html><title>b</title><button id=go>go</button>`)
	s.handle("b.localhost", "/fp.html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "fp=1; SameSite=None; Secure; Path=/")
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<!doctype html><title>b</title><button id=go>go</button>"))
	})
	tabA, err := e.NewTab(s.u("a.localhost", "/one.html"))
	if err != nil {
		t.Fatal(err)
	}
	frameResult(t, tabA)
	kid := tabA.FrameTabs()[0]
	ask := func() string {
		raw, err := kid.Evaluate(`(async () => { const r = [await document.hasStorageAccess()]; try { await document.requestStorageAccess(); r.push('granted'); } catch (e) { r.push(e.name); } r.push(await document.hasStorageAccess()); return JSON.stringify(r); })()`)
		if err != nil {
			t.Fatal(err)
		}
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	}
	if got := ask(); got != `[false,"NotAllowedError",false]` {
		t.Fatalf("before the person ever used the embed's site: %s", got)
	}
	top, err := e.NewTab(s.u("b.localhost", "/fp.html"))
	if err != nil {
		t.Fatal(err)
	}
	if err := top.Click("#go"); err != nil {
		t.Fatal(err)
	}
	if got := ask(); got != `[false,"granted",true]` {
		t.Fatalf("after using it as a first party: %s", got)
	}
	// the grant lifts the partition for this pair: the first-party cookie now reaches the embed
	kid2 := func() *Tab {
		tab, err := e.NewTab(s.u("a.localhost", "/one.html"))
		if err != nil {
			t.Fatal(err)
		}
		frameResult(t, tab)
		return tab.FrameTabs()[0]
	}()
	r, _ := kid2.Evaluate(`document.cookie`)
	if !strings.Contains(string(r), "fp=1") {
		t.Fatalf("with storage access the embed should see the first-party cookie, document.cookie=%s", r)
	}
	// ... and another top-level site gets nothing
	tabC, _ := e.NewTab(s.u("c.localhost", "/one.html"))
	s.embedder("c.localhost", "/one.html")
	tabC, _ = e.NewTab(s.u("c.localhost", "/one.html"))
	frameResult(t, tabC)
	rc, _ := tabC.FrameTabs()[0].Evaluate(`document.cookie`)
	if strings.Contains(string(rc), "fp=1") {
		t.Fatalf("storage access is granted per top-level site: %s", rc)
	}
	// revoking
	ctx.RevokeThirdPartyCookies("http://a.localhost", "http://b.localhost")
	rr, _ := kid2.Evaluate(`document.hasStorageAccess()`)
	if string(rr) != "false" {
		t.Fatalf("after revoking: %s", rr)
	}
}

func TestClearingASiteClearsItsPartitions(t *testing.T) {
	s := newPartSites(t)
	s.embedder("a.localhost", "/one.html")
	s.embedder("c.localhost", "/one.html")
	e := newSiteEngine(t)
	ctx := e.DefaultContext()
	for _, h := range []string{"a.localhost", "c.localhost"} {
		tab, _ := e.NewTab(s.u(h, "/one.html"))
		frameResult(t, tab)
	}
	keys := func() []string { return ctx.SiteStorage().Origins() }
	dbs := func(substr string) int {
		n := 0
		for _, k := range keys() {
			if strings.Contains(k, substr) {
				n += len(ctx.SiteStorage().Origin(k).Databases())
			}
		}
		return n
	}
	countPart := func() int { // partitioned cookies of the embed's host
		n := 0
		for _, c := range ctx.JarRef().All("") {
			if c.Partition != "" && strings.Contains(c.Domain, "b.localhost") {
				n++
			}
		}
		return n
	}
	buckets := func(substr string) int {
		n := 0
		ctx.mu.RLock()
		for k := range ctx.state {
			if strings.Contains(k, substr) {
				n++
			}
		}
		ctx.mu.RUnlock()
		return n
	}
	if dbs("^top=http://a.localhost") != 1 || dbs("^top=http://c.localhost") != 1 || countPart() == 0 || buckets("^top=http://a.localhost") != 1 {
		t.Fatalf("setup: dbs a=%d c=%d cookies=%d buckets=%d keys=%v", dbs("^top=http://a.localhost"), dbs("^top=http://c.localhost"), countPart(), buckets("^top=http://a.localhost"), keys())
	}
	// clearing the top-level site removes what embeds under it keep, and only that
	ctx.ClearSiteData("http://a.localhost:" + s.port)
	if dbs("^top=http://a.localhost") != 0 || buckets("^top=http://a.localhost") != 0 {
		t.Fatalf("state under a.localhost remains: dbs=%d buckets=%d", dbs("^top=http://a.localhost"), buckets("^top=http://a.localhost"))
	}
	if dbs("^top=http://c.localhost") != 1 || buckets("^top=http://c.localhost") != 1 {
		t.Fatalf("clearing a.localhost must keep c.localhost's partitions: dbs=%d buckets=%d", dbs("^top=http://c.localhost"), buckets("^top=http://c.localhost"))
	}
	// a fresh load of the embed under a finds nothing; under c it finds its earlier state
	tabA, _ := e.NewTab(s.u("a.localhost", "/one.html"))
	if r := frameResult(t, tabA); num(r, "ls") != 0 || num(r, "idb") != 0 || num(r, "cache") != 0 || num(r, "opfs") != 0 {
		t.Fatalf("a.localhost's partition was not cleared: %v", r)
	}
	tabC, _ := e.NewTab(s.u("c.localhost", "/one.html"))
	if r := frameResult(t, tabC); num(r, "ls") != 1 || num(r, "idb") != 1 {
		t.Fatalf("c.localhost's partition must be intact: %v", r)
	}
	// clearing the embed's origin removes all of its partitions, the cookies in them included
	ctx.ClearSiteData("http://b.localhost:" + s.port)
	if dbs("b.localhost") != 0 || buckets("b.localhost") != 0 || countPart() != 0 {
		t.Fatalf("state of the embed remains: dbs=%d buckets=%d cookies=%d", dbs("b.localhost"), buckets("b.localhost"), countPart())
	}
}

func TestPartitionedBroadcastChannelAndFirstPartySameSite(t *testing.T) {
	s := newPartSites(t)
	// x.a.localhost is the same site as a.localhost: embedded there it is a first party, and shares the storage
	// it has when visited directly
	s.page("x.a.localhost", "/store.html", `<!doctype html><title>x</title><script>localStorage.setItem('seen', (localStorage.getItem('seen')||'') + 'x'); window.v = localStorage.getItem('seen');</script>`)
	s.page("a.localhost", "/same.html", `<!doctype html><iframe src="`+s.u("x.a.localhost", "/store.html")+`"></iframe>`)
	s.embedder("a.localhost", "/one.html")
	s.embedder("a.localhost", "/two.html")
	s.embedder("c.localhost", "/one.html")
	e := newSiteEngine(t)
	top, _ := e.NewTab(s.u("x.a.localhost", "/store.html"))
	if v, _ := top.Evaluate(`window.v`); string(v) != `"x"` {
		t.Fatalf("direct visit: %s", v)
	}
	same, _ := e.NewTab(s.u("a.localhost", "/same.html"))
	same.WaitFrames(5 * time.Second)
	if v, _ := same.FrameTabs()[0].Evaluate(`window.v`); string(v) != `"xx"` {
		t.Fatalf("a same-site embed must share the first-party storage: %s", v)
	}
	if k := same.FrameTabs()[0].storageKeyFor(same.FrameTabs()[0].docOrigin()); strings.Contains(k, "^top=") {
		t.Fatalf("a same-site frame must not be partitioned: %s", k)
	}

	// BroadcastChannel: two embeds under A hear each other, the one under C does not hear them
	a1, _ := e.NewTab(s.u("a.localhost", "/one.html"))
	frameResult(t, a1)
	a2, _ := e.NewTab(s.u("a.localhost", "/two.html"))
	frameResult(t, a2)
	c1, _ := e.NewTab(s.u("c.localhost", "/one.html"))
	frameResult(t, c1)
	a1.FrameTabs()[0].Evaluate(`window.bc.postMessage('from a1')`)
	var heardA, heardC string
	for i := 0; i < 200; i++ {
		r, _ := a2.FrameTabs()[0].Evaluate(`JSON.stringify(window.workerBC)`)
		_ = json.Unmarshal(r, &heardA)
		if heardA != "[]" && heardA != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	r, _ := c1.FrameTabs()[0].Evaluate(`JSON.stringify(window.workerBC)`)
	_ = json.Unmarshal(r, &heardC)
	if heardA != `["from a1"]` || heardC != "[]" {
		t.Fatalf("channel namespaces: under the same site %q, under another site %q", heardA, heardC)
	}
}
