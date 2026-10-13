package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cacheServer serves a script page plus a few resources to put in caches.
func cacheServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><html><body><script>window.ready = 1;</script></body></html>`)
	})
	mux.HandleFunc("/a.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Origin", "network")
		io.WriteString(w, "alpha:"+r.URL.RawQuery)
	})
	mux.HandleFunc("/b.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "beta")
	})
	mux.HandleFunc("/bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		b := make([]byte, 256)
		for i := range b {
			b[i] = byte(i)
		}
		w.Write(b)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte(strings.Repeat("0123456789abcdef", 64*1024))) // 1 MiB
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestCacheStorageRoundTrip(t *testing.T) {
	srv := cacheServer(t)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, `
const r = {};
const c = await caches.open('v1');
r.openAgain = (await caches.open('v1')) instanceof Cache;
await c.put('/hand.txt', new Response('made by hand', { status: 201, statusText: 'Made', headers: { 'X-Test': 'yes', 'Content-Type': 'text/x-test' } }));
const m = await c.match('/hand.txt');
r.hand = [m.status, m.statusText, m.headers.get('x-test'), m.headers.get('content-type'), await m.text(), m.ok, m.url, m.bodyUsed];
r.again = await (await c.match(new Request('/hand.txt'))).text();   // every match is a fresh Response
r.miss = await c.match('/nope') === undefined;
await c.add('/a.txt?x=1');
await c.addAll(['/b.txt', '/bin']);
r.keys = (await c.keys()).map(q => new URL(q.url).pathname + new URL(q.url).search).join(' ');
r.added = [await (await c.match('/a.txt?x=1')).text(), (await c.match('/a.txt?x=1')).headers.get('x-origin')];
r.search = [await c.match('/a.txt') === undefined, await (await c.match('/a.txt', { ignoreSearch: true })).text()];
const bin = new Uint8Array(await (await c.match('/bin')).arrayBuffer());
r.bin = [bin.length, bin.every((v, i) => v === i)];
// a failing addAll adds nothing
try { await c.addAll(['/b.txt?new=1', '/missing']); r.addAll = 'resolved'; } catch (e) { r.addAll = e.name; }
r.noPartial = await c.match('/b.txt?new=1') === undefined;
// put validation
const bad = [];
for (const [label, f] of [
  ['post', () => c.put(new Request('/p', { method: 'POST', body: 'x' }), new Response('x'))],
  ['206', () => c.put('/p', new Response('x', { status: 206 }))],
  ['vary', () => c.put('/p', new Response('x', { headers: { Vary: '*' } }))],
  ['scheme', () => c.put('ftp://x/y', new Response('x'))],
  ['used', async () => { const rs = new Response('x'); await rs.text(); return c.put('/p', rs); }],
  ['notresp', () => c.put('/p', 'str')],
]) { try { await f(); bad.push(label + ':ok'); } catch (e) { bad.push(label + ':' + e.name); } }
r.bad = bad;
// put replaces in place; delete removes
await c.put('/a.txt?x=1', new Response('replaced'));
r.replaced = [await (await c.match('/a.txt?x=1')).text(), (await c.keys()).length, new URL((await c.keys())[1].url).pathname];
r.deleted = [await c.delete('/b.txt'), await c.delete('/b.txt'), (await c.keys()).length];
await c.put('/a.txt?x=1', new Response('again'));
r.matchAll = (await c.matchAll('/a.txt', { ignoreSearch: true })).length + '/' + (await c.matchAll()).length;
// CacheStorage
const c2 = await caches.open('v2');
await c2.put('/only2', new Response('two'));
r.names = await caches.keys();
r.has = [await caches.has('v1'), await caches.has('nope')];
r.global = [await (await caches.match('/only2')).text(), await caches.match('/only2', { cacheName: 'v1' }) === undefined, await (await caches.match('/hand.txt')).text()];
r.del = [await caches.delete('v2'), await caches.delete('v2'), await caches.keys()];
// responses stay usable after cloning and the clone keeps binary bytes
const cl = (await c.match('/bin')).clone();
r.clone = new Uint8Array(await cl.arrayBuffer()).length;
return JSON.stringify(r);
`)
	want := `{"openAgain":true,"hand":[201,"Made","yes","text/x-test","made by hand",true,"",true],"again":"made by hand","miss":true,"keys":"/hand.txt /a.txt?x=1 /b.txt /bin","added":["alpha:x=1","network"],"search":[true,"alpha:x=1"],"bin":[256,true],"addAll":"TypeError","noPartial":true,"bad":["post:TypeError","206:TypeError","vary:TypeError","scheme:TypeError","used:TypeError","notresp:TypeError"],"replaced":["replaced",4,"/a.txt"],"deleted":[true,false,3],"matchAll":"1/3","names":["v1","v2"],"has":[true,false],"global":["two",true,"made by hand"],"del":[true,false,["v1"]],"clone":256}`
	expect(t, tab, got, want)
}

func TestCacheStoragePersistsBodiesOnDisk(t *testing.T) {
	srv := cacheServer(t)
	dir := t.TempDir()
	mk := func() *Engine { o := DefaultOptions(); o.StorageDir = dir; return New(o) }
	e1 := mk()
	tab := openTab(t, e1, srv.URL+"/")
	got := js(t, tab, `
const c = await caches.open('assets');
await c.addAll(['/big', '/a.txt', '/bin']);
const est = await navigator.storage.estimate();
return JSON.stringify({ caches: est.usageDetails.caches > 1048576, total: est.usage >= est.usageDetails.caches });
`)
	expect(t, tab, got, `{"caches":true,"total":true}`)
	e1.Close()
	bodies, _ := filepath.Glob(filepath.Join(dir, "*", "caches", "bodies", "*"))
	if len(bodies) != 3 {
		t.Fatalf("expected 3 body files, got %v", bodies)
	}
	var total int64
	for _, b := range bodies {
		fi, _ := os.Stat(b)
		total += fi.Size()
	}
	if total != int64(1048576+len("alpha:")+256) {
		t.Fatalf("body bytes on disk: %d", total)
	}
	// an orphan body (crash between file and index record) is swept on the next open
	orphan := filepath.Join(filepath.Dir(bodies[0]), "9999")
	os.WriteFile(orphan, []byte("junk"), 0o600)

	e2 := mk()
	defer e2.Close()
	tab2 := openTab(t, e2, srv.URL+"/")
	got = js(t, tab2, `
const names = await caches.keys();
const c = await caches.open('assets');
const big = await (await c.match('/big')).arrayBuffer();
return JSON.stringify({ names, big: big.byteLength, text: await (await c.match('/a.txt')).text(), n: (await c.keys()).length, est: (await navigator.storage.estimate()).usageDetails.caches > 1048576 });
`)
	expect(t, tab2, got, `{"names":["assets"],"big":1048576,"text":"alpha:","n":3,"est":true}`)
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan body was not removed: %v", err)
	}
	// deleting the cache removes its files
	js(t, tab2, `await caches.delete('assets'); return 1;`)
	if left, _ := filepath.Glob(filepath.Join(dir, "*", "caches", "bodies", "*")); len(left) != 0 {
		t.Fatalf("bodies left after delete: %v", left)
	}
}

func TestCacheStorageEphemeralAndCleared(t *testing.T) {
	srv := cacheServer(t)
	dir := t.TempDir()
	o := DefaultOptions()
	o.StorageDir = dir
	e := New(o)
	defer e.Close()
	priv := e.NewContext("private", ContextOptions{})
	ptab, err := priv.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	js(t, ptab, `const c = await caches.open('p'); await c.add('/a.txt'); return 1;`)
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("private context wrote to disk: %v", ents)
	}
	tab := openTab(t, e, srv.URL+"/")
	js(t, tab, `const c = await caches.open('keep'); await c.add('/a.txt'); return 1;`)
	if b, _ := filepath.Glob(filepath.Join(dir, "*", "caches", "bodies", "*")); len(b) != 1 {
		t.Fatalf("expected one body on disk: %v", b)
	}
	e.DefaultContext().ClearSiteData(siteOriginOf(srv.URL))
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("files left after clearing: %v", ents)
	}
	got := js(t, tab, `return JSON.stringify(await caches.keys());`)
	expect(t, tab, got, `[]`)
	// insecure origins have no caches (everything here is http on a non-loopback name)
	tab3 := openTab(t, e, "about:blank")
	got = js(t, tab3, `return typeof caches;`)
	expect(t, tab3, got, `undefined`)
}

func TestCacheStorageQuota(t *testing.T) {
	srv := cacheServer(t)
	o := DefaultOptions()
	o.StorageQuotaMiB = 1
	e := New(o)
	defer e.Close()
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, `
const c = await caches.open('q');
let name;
try { await c.add('/big'); name = 'ok'; } catch (e) { name = e.name; }
return name + ' ' + (await c.keys()).length;
`)
	expect(t, tab, got, `QuotaExceededError 0`)
}
