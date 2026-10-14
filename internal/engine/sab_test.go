package engine

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// sabServer serves an isolated page and worker scripts that carry the same COOP/COEP headers.
func sabServer(t *testing.T, workers map[string]string) *coiServer {
	t.Helper()
	routes := map[string]route{}
	for p, body := range workers {
		routes[p] = route{body: body, hdr: isolatedHdr}
	}
	return newCOIServer(t, isolatedHdr, routes)
}

const sabHelpers = `
const next = (w) => new Promise((res) => { w.onmessage = (e) => res(e.data); });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
`

func TestSharedArrayBufferCounterAcrossWorkers(t *testing.T) {
	srv := sabServer(t, map[string]string{"/count.js": `
onmessage = (e) => {
  const a = new Int32Array(e.data.sab);
  for (let i = 0; i < e.data.n; i++) Atomics.add(a, 0, 1);
  Atomics.add(a, 1, 1);
  postMessage('done');
};`})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, sabHelpers+`
const sab = new SharedArrayBuffer(16), a = new Int32Array(sab);
const ws = [0, 1, 2].map(() => new Worker('/count.js'));
const done = ws.map((w) => next(w));
ws.forEach((w) => w.postMessage({ sab, n: 2000 }));
await Promise.all(done);
ws.forEach((w) => w.terminate());
return JSON.stringify({ total: Atomics.load(a, 0), finished: Atomics.load(a, 1), isSAB: sab instanceof SharedArrayBuffer, len: sab.byteLength, ctor: sab.constructor === SharedArrayBuffer, tag: Object.prototype.toString.call(sab), isAB: sab instanceof ArrayBuffer });`)
	if want := `{"total":6000,"finished":3,"isSAB":true,"len":16,"ctor":true,"tag":"[object SharedArrayBuffer]","isAB":false}`; got != want {
		t.Fatalf("got %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestAtomicsWaitAndNotifyBetweenPageAndWorker(t *testing.T) {
	srv := sabServer(t, map[string]string{"/waiter.js": `
onmessage = (e) => {
  const a = new Int32Array(e.data.sab);
  const out = {};
  out.notEqual = Atomics.wait(a, 0, 123);                 // value differs: returns at once
  out.timedOut = Atomics.wait(a, 2, 0, 30);               // nobody notifies index 2
  Atomics.store(a, 1, 1); Atomics.notify(a, 1);           // tell the page we are about to block
  const t0 = Date.now();
  out.woken = Atomics.wait(a, 0, 0, 10000);               // the page stores 1 and notifies
  out.blockedMs = Date.now() - t0;
  out.value = Atomics.load(a, 0);
  postMessage(out);
};`})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, sabHelpers+`
const sab = new SharedArrayBuffer(16), a = new Int32Array(sab);
const w = new Worker('/waiter.js');
const res = next(w);
w.postMessage({ sab });
// the page cannot block, but it can wait asynchronously for the worker to announce itself
const r = Atomics.waitAsync(a, 1, 0);
await r.value;
await sleep(150); // let it really block
const t0 = Date.now();
Atomics.store(a, 0, 1);
const woke = Atomics.notify(a, 0, 1);
const out = await res;
w.terminate();
return JSON.stringify({ woke, notEqual: out.notEqual, timedOut: out.timedOut, woken: out.woken, value: out.value, blockedLong: out.blockedMs >= 100 && out.blockedMs < 5000, prompt: Date.now() - t0 < 3000 });`)
	if want := `{"woke":1,"notEqual":"not-equal","timedOut":"timed-out","woken":"ok","value":1,"blockedLong":true,"prompt":true}`; got != want {
		t.Fatalf("got %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestAtomicsWaitAsyncAcrossAgents(t *testing.T) {
	srv := sabServer(t, map[string]string{"/notifier.js": `
onmessage = (e) => {
  const a = new Int32Array(e.data.sab);
  setTimeout(() => { Atomics.store(a, 0, 7); postMessage({ woke: Atomics.notify(a, 0) }); }, 80);
};`, "/asyncwaiter.js": `
onmessage = async (e) => {
  const a = new Int32Array(e.data.sab);
  const r = Atomics.waitAsync(a, 0, 0);
  postMessage({ async: r.async });
  postMessage({ result: await r.value, value: Atomics.load(a, 0) });
};`})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, sabHelpers+`
const out = {};
// 1. a worker wakes a waitAsync of the page
let sab = new SharedArrayBuffer(8), a = new Int32Array(sab);
const w = new Worker('/notifier.js'); const m = next(w);
const r = Atomics.waitAsync(a, 0, 0);
out.async = r.async;
w.postMessage({ sab });
out.result = await r.value; out.value = Atomics.load(a, 0); out.workerWoke = (await m).woke;
w.terminate();
// 2. the page wakes a waitAsync of a worker
sab = new SharedArrayBuffer(8); a = new Int32Array(sab);
const w2 = new Worker('/asyncwaiter.js');
const msgs = [];
const got2 = new Promise((res) => { w2.onmessage = (e) => { msgs.push(e.data); if (msgs.length === 2) res(); }; });
w2.postMessage({ sab });
await sleep(120);
Atomics.store(a, 0, 5);
out.pageNotify = Atomics.notify(a, 0);
await got2;
out.worker = msgs;
w2.terminate();
// 3. timeouts and immediate answers
sab = new SharedArrayBuffer(8); a = new Int32Array(sab);
const t = Atomics.waitAsync(a, 0, 0, 40);
out.timeout = [t.async, await t.value];
out.immediate = [Atomics.waitAsync(a, 0, 9), Atomics.waitAsync(a, 0, 0, 0)];
return JSON.stringify(out);`)
	want := `{"async":true,"result":"ok","value":7,"workerWoke":1,"pageNotify":1,"worker":[{"async":true},{"result":"ok","value":5}],"timeout":[true,"timed-out"],"immediate":[{"async":false,"value":"not-equal"},{"async":false,"value":"timed-out"}]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestSharedArrayBufferBetweenTwoWorkers(t *testing.T) {
	srv := sabServer(t, map[string]string{"/w1.js": `
onmessage = (e) => {
  const a = new Int32Array(e.data.sab, 4); // a view that starts inside the buffer
  postMessage('w1 waiting');
  const r = Atomics.wait(a, 0, 0, 5000);
  postMessage({ r, v: a[0], off: a.byteOffset, shared: a.buffer instanceof SharedArrayBuffer });
};`, "/w2.js": `
onmessage = (e) => {
  const a = new Int32Array(e.data.sab, 4);
  a[0] = 41; Atomics.add(a, 0, 1);
  postMessage({ woke: Atomics.notify(a, 0) });
};`})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, sabHelpers+`
const sab = new SharedArrayBuffer(16);
const w1 = new Worker('/w1.js'), w2 = new Worker('/w2.js');
const log = []; let waiting; const p1 = new Promise((r) => { waiting = r; });
const fin = new Promise((res) => { w1.onmessage = (e) => { if (e.data === 'w1 waiting') waiting(); else { log.push(e.data); res(); } }; });
w1.postMessage({ sab }); await p1; await sleep(150);
const m2 = next(w2); w2.postMessage({ sab }); const n = await m2;
await fin;
w1.terminate(); w2.terminate();
return JSON.stringify({ log, notify: n.woke, page: new Int32Array(sab)[1] });`)
	if want := `{"log":[{"r":"ok","v":42,"off":4,"shared":true}],"notify":1,"page":42}`; got != want {
		t.Fatalf("got %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestAtomicsWaitRulesAndSharingLimits(t *testing.T) {
	srv := sabServer(t, map[string]string{"/w.js": `
onmessage = (e) => {
  const r = {};
  try { Atomics.wait(new Int32Array(new ArrayBuffer(8)), 0, 0, 1); } catch (x) { r.plain = x.name; }
  try { Atomics.wait(new Int16Array(new SharedArrayBuffer(8)), 0, 0, 1); } catch (x) { r.int16 = x.name; }
  try { Atomics.wait(new Int32Array(new SharedArrayBuffer(8)), 9, 0, 1); } catch (x) { r.range = x.name; }
  r.big = Atomics.wait(new BigInt64Array(new SharedArrayBuffer(16)), 0, 1n, 1);
  r.bigTimeout = Atomics.wait(new BigInt64Array(new SharedArrayBuffer(16)), 1, 0n, 5);
  r.isWorker = typeof WorkerGlobalScope;
  r.sab = [typeof SharedArrayBuffer, crossOriginIsolated];
  postMessage(r);
};`})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, sabHelpers+`
const out = {};
try { Atomics.wait(new Int32Array(new SharedArrayBuffer(8)), 0, 0, 1); } catch (x) { out.main = [x.name, x.message]; }
out.notifyPlain = Atomics.notify(new Int32Array(4), 0);
out.notifyNone = Atomics.notify(new Int32Array(new SharedArrayBuffer(8)), 0);
const w = new Worker('/w.js'); const m = next(w); w.postMessage(1); out.worker = await m; w.terminate();
// what cannot be posted
const sab = new SharedArrayBuffer(8);
out.bc = (() => { try { new BroadcastChannel('x').postMessage(sab); return 'posted'; } catch (e) { return e.name; } })();
out.inList = (() => { try { new Worker('/w.js').postMessage(0, [sab]); return 'posted'; } catch (e) { return e.name; } })();
const idb = await new Promise((res) => { const r = indexedDB.open('s', 1); r.onupgradeneeded = () => r.result.createObjectStore('o'); r.onsuccess = () => res(r.result); });
out.idb = (() => { try { idb.transaction('o', 'readwrite').objectStore('o').put(sab, 'k'); return 'stored'; } catch (e) { return e.name; } })();
// structuredClone keeps the memory shared
const c = structuredClone(sab); new Uint8Array(sab)[0] = 99;
out.clone = [c !== undefined, new Uint8Array(c)[0], c instanceof SharedArrayBuffer];
const v = structuredClone({ view: new Int32Array(sab, 4, 1) }); Atomics.store(new Int32Array(sab), 1, 17);
out.cloneView = [v.view.byteOffset, v.view[0]];
// subclasses and species
class S extends SharedArrayBuffer {} const s = new S(8);
out.subclass = [s instanceof S, s instanceof SharedArrayBuffer, s.byteLength, sab.slice(2, 6).byteLength, sab.slice(0, 1) instanceof SharedArrayBuffer];
try { SharedArrayBuffer(1); } catch (e) { out.call = e.name; }
try { new SharedArrayBuffer(-1); } catch (e) { out.neg = e.name; }
return JSON.stringify(out);`)
	want := `{"main":["TypeError","Atomics.wait cannot be called in this context"],"notifyPlain":0,"notifyNone":0,"worker":{"plain":"TypeError","int16":"TypeError","range":"RangeError","big":"not-equal","bigTimeout":"timed-out","isWorker":"function","sab":["function",true]},"bc":"DataCloneError","inList":"DataCloneError","idb":"DataCloneError","clone":[true,99,true],"cloneView":[4,17],"subclass":[true,true,8,4,true],"call":"TypeError","neg":"RangeError"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

// Without isolation the constructor is gone and nothing can be shared.
func TestSharedArrayBufferAbsentWithoutIsolation(t *testing.T) {
	srv := newCOIServer(t, nil, map[string]route{"/w.js": {body: `postMessage([typeof SharedArrayBuffer, crossOriginIsolated, typeof Atomics.wait])`}})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, sabHelpers+`
const w = new Worker('/w.js'); const m = next(w); const r = await m; w.terminate();
let ctor = 'none'; try { new SharedArrayBuffer(8); ctor = 'made'; } catch (e) { ctor = e.name; }
return JSON.stringify({ page: [typeof SharedArrayBuffer, crossOriginIsolated, 'SharedArrayBuffer' in globalThis], worker: r, ctor });`)
	if want := `{"page":["undefined",false,false],"worker":["undefined",false,"function"],"ctor":"ReferenceError"}`; got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
}

// A worker that is blocked in Atomics.wait with no timeout can still be terminated, and leaves nothing behind.
func TestTerminatingAWorkerBlockedInAtomicsWait(t *testing.T) {
	srv := sabServer(t, map[string]string{"/block.js": `
onmessage = (e) => { const a = new Int32Array(e.data.sab); postMessage('blocking'); while (true) Atomics.wait(a, 0, 0); };`})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	before := runtime.NumGoroutine()
	got := js(t, tab, sabHelpers+`
const sab = new SharedArrayBuffer(8);
const w = new Worker('/block.js'); const m = next(w); w.postMessage({ sab }); await m;
await sleep(100);
w.terminate();
await sleep(100);
return 'terminated';`)
	if got != "terminated" {
		t.Fatalf("got %s", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > before+2 {
		time.Sleep(50 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before+2 {
		buf := make([]byte, 1<<16)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("goroutines %d -> %d after terminate\n%s", before, n, strings.Join(strings.Split(string(buf), "\n\n")[:min(8, len(strings.Split(string(buf), "\n\n")))], "\n\n"))
	}
}

// The single-agent waitAsync cases that used to run in the ECMAScript table; SharedArrayBuffer needs an isolated page.
func TestAtomicsWaitAsyncSingleAgent(t *testing.T) {
	srv := newCOIServer(t, isolatedHdr, nil)
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	runJSCases(t, tab, []jsCase{
		{"Atomics.waitAsync not-equal and timeout", `const ia = new Int32Array(new SharedArrayBuffer(8)); const a = Atomics.waitAsync(ia, 0, 1); const b = Atomics.waitAsync(ia, 0, 0, 0); return [a.async, a.value, b.async, b.value];`, `[false,"not-equal",false,"timed-out"]`},
		{"Atomics.waitAsync notified", `const ia = new Int32Array(new SharedArrayBuffer(8)); const w = Atomics.waitAsync(ia, 0, 0); const n = Atomics.notify(ia, 0); return [w.async, n, await w.value];`, `[true,1,"ok"]`},
		{"Atomics.waitAsync times out", `const ia = new Int32Array(new SharedArrayBuffer(8)); const w = Atomics.waitAsync(ia, 1, 0, 10); return await w.value;`, `"timed-out"`},
		{"Atomics.waitAsync rejects plain buffers", `try { Atomics.waitAsync(new Int32Array(2), 0, 0); } catch (e) { return e.name; }`, `"TypeError"`},
	})
}
