package engine

import (
	"testing"
)

// The numbers quoted in docs/storage.md and docs/service-workers.md come from these (go test -run Throughput -v).

func TestCacheThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput measurement")
	}
	srv := cacheServer(t)
	o := DefaultOptions()
	o.StorageDir = t.TempDir()
	e := New(o)
	defer e.Close()
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, `
const c = await caches.open('perf');
const N = 300, r = {};
const body = 'x'.repeat(2048);
let t0 = performance.now();
for (let i = 0; i < N; i++) await c.put('/item/' + i, new Response(body, { headers: { 'content-type': 'text/plain' } }));
r.putMs = performance.now() - t0;
t0 = performance.now();
for (let i = 0; i < N; i++) await c.match('/item/' + i);
r.matchMs = performance.now() - t0;
t0 = performance.now();
const big = new Uint8Array(1 << 20).fill(7);
for (let i = 0; i < 10; i++) await c.put('/big/' + i, new Response(big));
r.put1MiBx10Ms = performance.now() - t0;
t0 = performance.now();
for (let i = 0; i < 10; i++) await (await c.match('/big/' + i)).arrayBuffer();
r.match1MiBx10Ms = performance.now() - t0;
return JSON.stringify(r);
`)
	t.Logf("300 x 2 KiB entries, 10 x 1 MiB: %s", got)
}

func TestServiceWorkerFetchThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput measurement")
	}
	srv := newSWServer(t, basicSW)
	srv.setExtra("/asset.js", "x")
	srv.setExtra("/direct", "direct")
	e := New(DefaultOptions())
	defer e.Close()
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, swHelpers+`
const r = {};
const N = 300;
let t0 = performance.now();
for (let i = 0; i < N; i++) await (await fetch('/direct?i=' + i)).text();
r.networkMs = performance.now() - t0;
await regReady('/sw.js');
t0 = performance.now();
for (let i = 0; i < N; i++) await (await fetch('/api/hello')).text();
r.workerSynthMs = performance.now() - t0;
t0 = performance.now();
for (let i = 0; i < N; i++) await (await fetch('/asset.js')).text();
r.workerCacheMs = performance.now() - t0;
t0 = performance.now();
for (let i = 0; i < N; i++) await (await fetch('/direct?j=' + i)).text(); // controlled, handler does not respond: network
r.workerPassMs = performance.now() - t0;
return JSON.stringify(r);
`)
	t.Logf("300 sequential fetches: %s", got)
}
