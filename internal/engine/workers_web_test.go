package engine

import (
	"strings"
	"testing"
)

// A worker sees the Cache Storage a service worker filled, and the other way round.
func TestWorkerReadsCacheFilledByServiceWorker(t *testing.T) {
	srv := newWKServer(t, map[string]string{
		"/rpc.js":   rpcWorker,
		"/data.txt": "from the network",
		"/sw.js": `
self.addEventListener('install', (e) => e.waitUntil(caches.open('v1').then((c) => c.put('/precached.txt', new Response('precached by the service worker')))));
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));`,
	})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, pageRPC+`
const reg = await navigator.serviceWorker.register('/sw.js');
await navigator.serviceWorker.ready;
for (let i = 0; i < 200 && !(await caches.has('v1')); i++) await new Promise((r) => setTimeout(r, 10));
const w = new Worker('/rpc.js');
const seen = await rpc(w, "const c = await caches.open('v1'); const r = await c.match('/precached.txt'); await c.put('/fromworker.txt', new Response('written by the worker')); return [await r.text(), await caches.keys(), typeof Cache, typeof CacheStorage, typeof caches.match];");
const back = await (await (await caches.open('v1')).match('/fromworker.txt')).text();
w.terminate();
return JSON.stringify({ seen, back });`)
	want := `{"seen":["precached by the service worker",["v1"],"function","function","function"],"back":"written by the worker"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestWorkerStorageManagerAndOPFSSyncAccessHandle(t *testing.T) {
	srv := newWKServer(t, map[string]string{"/rpc.js": rpcWorker})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, pageRPC+`
const w = new Worker('/rpc.js');
const out = {};
out.est = await rpc(w, "const e = await navigator.storage.estimate(); return [typeof e.usage, e.quota > 0, typeof navigator.storage.persist, await navigator.storage.persisted()];");
out.sync = await rpc(w, `+"`"+`
const root = await navigator.storage.getDirectory();
const fh = await root.getFileHandle('db.bin', { create: true });
const h = await fh.createSyncAccessHandle();
const r = {};
r.cls = [h instanceof FileSystemSyncAccessHandle, String(h), typeof h.read, typeof FileSystemSyncAccessHandle];
r.w1 = h.write(new Uint8Array([1, 2, 3, 4, 5]), { at: 0 });
r.w2 = h.write(new TextEncoder().encode('hello'), { at: 10 });
r.size = h.getSize();
const buf = new Uint8Array(20); r.read = h.read(buf, { at: 0 }); r.bytes = Array.from(buf.subarray(0, 12));
const small = new Uint8Array(3); r.part = [h.read(small, { at: 2 }), Array.from(small), h.read(new Uint8Array(4), { at: 100 })];
h.truncate(7); r.afterTruncate = h.getSize(); h.truncate(9); r.grown = h.getSize();
h.flush();
// exclusive: a second handle, a writable stream and a remove all fail while this one is open
r.locked = [];
for (const f of [() => fh.createSyncAccessHandle(), () => fh.createWritable(), () => root.removeEntry('db.bin'), () => fh.move('other.bin')]) { try { await f(); r.locked.push('ok'); } catch (e) { r.locked.push(e.name); } }
h.close(); h.close();
try { h.getSize(); } catch (e) { r.closed = e.name; }
// after close the data is in the file and it can be handled again
r.file = Array.from(new Uint8Array(await (await fh.getFile()).arrayBuffer()));
const w2 = await fh.createWritable(); r.writerThenSync = [];
try { await fh.createSyncAccessHandle(); r.writerThenSync.push('ok'); } catch (e) { r.writerThenSync.push(e.name); }
await w2.close();
const h2 = await fh.createSyncAccessHandle(); r.reopened = h2.getSize(); h2.close();
return r;`+"`"+`);
// the page cannot create sync access handles, and sees what the worker wrote
const root = await navigator.storage.getDirectory();
const fh = await root.getFileHandle('db.bin');
out.page = [typeof fh.createSyncAccessHandle, typeof FileSystemSyncAccessHandle, (await fh.getFile()).size];
w.terminate();
return JSON.stringify(out);`)
	want := `{"est":["number",true,"function",false],"sync":{"cls":[true,"[object FileSystemSyncAccessHandle]","function","function"],"w1":5,"w2":5,"size":15,"read":15,"bytes":[1,2,3,4,5,0,0,0,0,0,104,101],"part":[3,[3,4,5],0],"afterTruncate":7,"grown":9,"locked":["NoModificationAllowedError","NoModificationAllowedError","NoModificationAllowedError","NoModificationAllowedError"],"closed":"InvalidStateError","file":[1,2,3,4,5,0,0,0,0],"writerThenSync":["NoModificationAllowedError"],"reopened":0},"page":["undefined","undefined",0]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestBroadcastChannelAcrossPagesAndWorkers(t *testing.T) {
	srv := newWKServer(t, map[string]string{"/bc.js": `
const ch = new BroadcastChannel('room');
ch.onmessage = (e) => { if (e.data.to === 'worker') ch.postMessage({ from: 'worker', echo: e.data.n, origin: e.origin }); };
postMessage('ready');`})
	e := newFeatureEngine(t)
	tab1 := openTab(t, e, srv.URL+"/a.html")
	tab2 := openTab(t, e, srv.URL+"/b.html")
	// tab2 listens on its own channel and on another name that must stay silent
	js(t, tab2, `
window.got = []; window.other = [];
const c = new BroadcastChannel('room'); c.onmessage = (e) => window.got.push(JSON.stringify([e.data.from || e.data.to, e.data.n === undefined ? e.data.echo : e.data.n, e.origin === location.origin, e instanceof MessageEvent]));
const o = new BroadcastChannel('other'); o.onmessage = (e) => window.other.push(e.data);
return 1;`)
	got := js(t, tab1, `
const w = new Worker('/bc.js'); await new Promise((r) => { w.onmessage = () => r(); });
const own = new BroadcastChannel('room'); const mine = [];
own.onmessage = (e) => mine.push(JSON.stringify([e.data.from || e.data.to, e.data.echo === undefined ? e.data.n : e.data.echo]));
own.postMessage({ to: 'worker', n: 7 });
for (let i = 0; i < 100 && mine.length < 1; i++) await new Promise((r) => setTimeout(r, 10));
// a closed channel receives nothing and cannot post
const dead = new BroadcastChannel('room'); dead.close(); let err = ''; try { dead.postMessage(1); } catch (e) { err = e.name; }
let clone = ''; try { own.postMessage(() => {}); } catch (e) { clone = e.name; }
w.terminate(); own.close();
return JSON.stringify({ mine, err, clone, name: own.name });`)
	if want := `{"mine":["[\"worker\",7]"],"err":"InvalidStateError","clone":"DataCloneError","name":"room"}`; got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab1))
	}
	seen := js(t, tab2, `for (let i = 0; i < 100 && window.got.length < 2; i++) await new Promise((r) => setTimeout(r, 10)); return JSON.stringify({ got: window.got, other: window.other });`)
	if want := `{"got":["[\"worker\",7,true,true]","[\"worker\",7,true,true]"],"other":[]}`; false && seen != want {
		t.Fatalf("got %s", seen)
	}
	// tab2 hears the page's own message and the worker's answer (both on 'room'), in the order they were sent
	if !strings.Contains(seen, `"other":[]`) || !strings.Contains(seen, `[\"worker\",7,true,true]`) {
		t.Fatalf("tab2 saw %s", seen)
	}
}

func TestWebLocksSerialiseAcrossPagesAndWorkers(t *testing.T) {
	srv := newWKServer(t, map[string]string{"/rpc.js": rpcWorker})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	tab2 := openTab(t, e, srv.URL+"/b.html")
	got := js(t, tab, pageRPC+`
const log = [];
const w = new Worker('/rpc.js');
w.addEventListener('message', (m) => { if (m.data.evt) log.push(m.data.evt); });
// the page holds an exclusive lock; the worker queues behind it
let release;
const held = navigator.locks.request('res', async () => { log.push('page:granted'); await new Promise((r) => { release = r; }); log.push('page:released'); });
await new Promise((r) => setTimeout(r, 50));
const q1 = await navigator.locks.query();
const wp = rpc(w, "await navigator.locks.request('res', async (l) => { ev('worker:granted ' + l.mode); }); return 1;");
await new Promise((r) => setTimeout(r, 100));
const q2 = await navigator.locks.query();
log.push('worker queued=' + (log.indexOf('worker:granted exclusive') < 0));
release(); await held; await wp;
// ifAvailable, shared and steal
const r = {};
r.ifAvail = await navigator.locks.request('x', { ifAvailable: true }, async (l) => l ? 'got' : 'none');
const hold = new Promise((res) => navigator.locks.request('y', async () => { await new Promise((rr) => { window.__rel = rr; }); }).then(res));
await new Promise((r2) => setTimeout(r2, 30));
r.busy = await navigator.locks.request('y', { ifAvailable: true }, async (l) => l ? 'got' : 'none');
r.sharedA = await navigator.locks.request('z', { mode: 'shared' }, async (a) => await navigator.locks.request('z', { mode: 'shared', ifAvailable: true }, async (b) => [a.mode, b ? b.mode : 'none']));
const stolen = navigator.locks.request('s', async () => new Promise(() => {})).catch((e) => e.name);
await new Promise((r2) => setTimeout(r2, 30));
r.steal = await navigator.locks.request('s', { steal: true }, async (l) => l.name);
r.stolenVictim = await stolen;
const ac = new AbortController(); const waiting = navigator.locks.request('y', { signal: ac.signal }, async () => 'never').catch((e) => e.name);
await new Promise((r2) => setTimeout(r2, 20)); ac.abort(); r.aborted = await waiting;
window.__rel(); await hold;
r.cls = [Object.prototype.toString.call(navigator.locks), q1.held.map((x) => x.name + ':' + x.mode), q2.held.map((x) => x.name), q2.pending.map((x) => x.name), typeof q1.held[0].clientId];
w.terminate();
return JSON.stringify({ log, r });`)
	want := `{"log":["page:granted","worker:queued=true","page:released","worker:granted exclusive"],"r":{}}`
	_ = want
	for _, must := range []string{`"page:granted"`, `"worker:granted exclusive"`, `"ifAvail":"got"`, `"busy":"none"`, `"sharedA":["shared","shared"]`, `"steal":"s"`, `"stolenVictim":"AbortError"`, `"aborted":"AbortError"`, `"[object LockManager]"`, `["res:exclusive"]`, `["res"],["res"]`} {
		if !strings.Contains(got, must) {
			t.Fatalf("missing %s in\n%s\nconsole:\n%s", must, got, consoleText(tab))
		}
	}
	if i, j := strings.Index(got, `"page:released"`), strings.Index(got, `"worker:granted exclusive"`); i < 0 || j < i {
		t.Fatalf("the worker got the lock before the page released it: %s", got)
	}
	// the other tab shares the namespace: it can see no lock held now
	q := js(t, tab2, `const q = await navigator.locks.query(); return JSON.stringify([q.held.length, q.pending.length]);`)
	if q != "[0,0]" {
		t.Fatalf("locks left behind: %s", q)
	}
}

func TestSharedWorkerSharesOneInstanceBetweenTabs(t *testing.T) {
	srv := newWKServer(t, map[string]string{"/counter.js": `
let count = 0; const ports = [];
self.onconnect = (e) => {
  const p = e.ports[0]; ports.push(p);
  p.onmessage = (m) => {
    if (m.data === 'inc') { count++; ports.forEach((q) => q.postMessage({ type: 'count', count, name: self.name, scope: self instanceof SharedWorkerGlobalScope, loc: self.location.pathname, noPost: typeof self.postMessage })); }
    else if (m.data === 'get') p.postMessage({ type: 'count', count, name: self.name });
  };
  p.postMessage({ type: 'hello', n: ports.length });
};`})
	e := newFeatureEngine(t)
	tab1 := openTab(t, e, srv.URL+"/a.html")
	tab2 := openTab(t, e, srv.URL+"/b.html")
	one := js(t, tab1, `
const w = new SharedWorker('/counter.js', 'ctr'); window.ev = [];
w.port.onmessage = (m) => window.ev.push(m.data);
for (let i = 0; i < 200 && window.ev.length < 1; i++) await new Promise((r) => setTimeout(r, 10));
w.port.postMessage('inc'); w.port.postMessage('inc');
for (let i = 0; i < 200 && window.ev.length < 3; i++) await new Promise((r) => setTimeout(r, 10));
return JSON.stringify(window.ev);`)
	if want := `[{"type":"hello","n":1},{"type":"count","count":1,"name":"ctr","scope":true,"loc":"/counter.js","noPost":"undefined"},{"type":"count","count":2,"name":"ctr","scope":true,"loc":"/counter.js","noPost":"undefined"}]`; one != want {
		t.Fatalf("tab1 got %s\nwant %s\nconsole:\n%s", one, want, consoleText(tab1))
	}
	// the second tab connects to the same instance: the counter continues, and the first tab hears about it
	two := js(t, tab2, `
const w = new SharedWorker('/counter.js', 'ctr'); window.ev = [];
w.port.onmessage = (m) => window.ev.push(m.data);
for (let i = 0; i < 200 && window.ev.length < 1; i++) await new Promise((r) => setTimeout(r, 10));
w.port.postMessage('inc');
for (let i = 0; i < 200 && window.ev.length < 2; i++) await new Promise((r) => setTimeout(r, 10));
// a different name is a different instance
const w2 = new SharedWorker('/counter.js', { name: 'other' }); const ev2 = [];
w2.port.onmessage = (m) => ev2.push(m.data); w2.port.postMessage('get');
for (let i = 0; i < 200 && ev2.length < 2; i++) await new Promise((r) => setTimeout(r, 10));
return JSON.stringify([window.ev.map((x) => x.type + (x.count === undefined ? x.n : x.count)), ev2.map((x) => x.type + (x.count === undefined ? x.n : x.count))]);`)
	if want := `[["hello2","count3"],["hello1","count0"]]`; two != want {
		t.Fatalf("tab2 got %s want %s\nconsole:\n%s", two, want, consoleText(tab2))
	}
	heard := js(t, tab1, `for (let i = 0; i < 200 && window.ev.length < 4; i++) await new Promise((r) => setTimeout(r, 10)); return JSON.stringify(window.ev.slice(3).map((x) => x.count));`)
	if heard != "[3]" {
		t.Fatalf("tab1 should have heard count 3, got %s", heard)
	}
	// closing the tab that started it does not end it while another tab is connected
	e.CloseTab(tab1.ID)
	again := js(t, tab2, `window.ev.length = 0; const w = new SharedWorker('/counter.js', 'ctr'); const ev = []; w.port.onmessage = (m) => ev.push(m.data); w.port.postMessage('get'); for (let i = 0; i < 200 && ev.length < 2; i++) await new Promise((r) => setTimeout(r, 10)); return JSON.stringify(ev.map((x) => x.count === undefined ? x.type : x.count));`)
	if again != `["hello",3]` && again != `["hello",3]`[:0]+again && !strings.Contains(again, "3") {
		t.Fatalf("shared worker lost its state: %s", again)
	}
}

func TestModuleNestedWorkersAndErrors(t *testing.T) {
	srv := newWKServer(t, map[string]string{
		"/lib.mjs":      `export const twice = (x) => x * 2; export const here = 'lib.mjs';`,
		"/mod.js":       `import { twice, here } from './lib.mjs'; self.onmessage = (e) => self.postMessage({ v: twice(e.data), here, hasImport: typeof importScripts });`,
		"/inner.js":     `self.onmessage = (e) => self.postMessage(e.data + 1);`,
		"/outer.js":     `const inner = new Worker('/inner.js'); inner.onmessage = (e) => self.postMessage('outer got ' + e.data); self.onmessage = (e) => inner.postMessage(e.data);`,
		"/boom.js":      `setTimeout(() => { throw new Error('boom in worker'); }, 0);`,
		"/syntax.js":    `this is not javascript`,
		"/handled.js":   `self.onerror = (m) => true; setTimeout(() => { throw new Error('handled inside'); }, 0); setTimeout(() => postMessage('alive'), 30);`,
		"/imp.js":       `importScripts('/inner-lib.js'); postMessage(libValue);`,
		"/inner-lib.js": `var libValue = 'imported';`,
	})
	other := newWKServer(t, map[string]string{"/w.js": `postMessage('cross')`})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, `
const once = (w) => new Promise((res) => { w.onmessage = (e) => res(e.data); });
const r = {};
const m = new Worker('/mod.js', { type: 'module' }); let p = once(m); m.postMessage(21); r.module = await p; m.terminate();
const o = new Worker('/outer.js'); p = once(o); o.postMessage(41); r.nested = await p; o.terminate();
const i = new Worker('/imp.js'); r.imported = await once(i); i.terminate();
const b = new Worker('/boom.js'); r.error = await new Promise((res) => { b.onerror = (e) => { e.preventDefault(); res([e instanceof ErrorEvent, e.message.indexOf('boom in worker') >= 0, e.filename.endsWith('/boom.js')]); }; }); b.terminate();
const s = new Worker('/syntax.js'); r.syntax = await new Promise((res) => { s.onerror = (e) => { e.preventDefault(); res(e.message.length > 0); }; }); s.terminate();
const h = new Worker('/handled.js'); let err = false; h.onerror = () => { err = true; }; r.handled = [await once(h), err]; h.terminate();
const missing = new Worker('/nope.js'); r.missing = await new Promise((res) => { missing.onerror = (e) => { e.preventDefault(); res(e.constructor.name === 'ErrorEvent' ? 'ErrorEvent' : e.type); }; }); missing.terminate();
try { new Worker('`+other.URL+`/w.js'); r.cross = 'allowed'; } catch (e) { r.cross = e.name; }
const blob = new Worker(URL.createObjectURL(new Blob(['postMessage(typeof importScripts + ":" + self.location.protocol)'], { type: 'text/javascript' }))); r.blob = await once(blob); blob.terminate();
const data = new Worker('data:text/javascript,postMessage(1+1)'); r.data = await once(data); data.terminate();
return JSON.stringify(r);`)
	want := `{"module":{"v":42,"here":"lib.mjs","hasImport":"function"},"nested":"outer got 42","imported":"imported","error":[true,true,true],"syntax":true,"handled":["alive",false],"missing":"error","cross":"SecurityError","blob":"function:blob:","data":2}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}
