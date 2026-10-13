package engine

import "testing"

const wkIDBHelpers = `
const req = (r) => new Promise((res, rej) => { r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
const done = (tx) => new Promise((res, rej) => { tx.oncomplete = () => res('complete'); tx.onabort = () => rej(tx.error || new Error('aborted')); });
const open = (name, ver, upgrade, ev) => new Promise((res, rej) => { const r = indexedDB.open(name, ver); r.onupgradeneeded = (e) => upgrade && upgrade(r.result, r.transaction, e); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); r.onblocked = () => ev && ev('blocked'); });
`

// rpcWorker is a worker that runs the source it is sent, with the IDB helpers in scope, and posts the result back.
const rpcWorker = wkIDBHelpers + `
const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
self.onmessage = async (e) => {
  try { const fn = new AsyncFunction('ev', e.data.src); self.postMessage({ id: e.data.id, ok: await fn((x) => self.postMessage({ evt: x })) }); }
  catch (err) { self.postMessage({ id: e.data.id, err: String(err && err.stack || err) }); }
};`

const pageRPC = `
const rpc = (w, src) => new Promise((res, rej) => { const id = Math.random(); const h = (e) => { if (e.data.id !== id) return; w.removeEventListener('message', h); e.data.err ? rej(new Error(e.data.err)) : res(e.data.ok); }; w.addEventListener('message', h); w.postMessage({ id, src }); });
`

func TestWorkerSharesIndexedDBWithThePage(t *testing.T) {
	srv := newWKServer(t, map[string]string{"/rpc.js": rpcWorker})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, wkIDBHelpers+pageRPC+`
const db = await open('shared', 1, (d) => { d.createObjectStore('kv', { keyPath: 'k' }); });
let tx = db.transaction('kv', 'readwrite'); tx.objectStore('kv').put({ k: 'fromPage', v: 1 }); await done(tx);
const w = new Worker('/rpc.js');
const seen = await rpc(w, "const db = await open('shared', 1); const o = await req(db.transaction('kv').objectStore('kv').get('fromPage')); const tx = db.transaction('kv', 'readwrite'); tx.objectStore('kv').put({ k: 'fromWorker', v: 2, t: typeof indexedDB + ',' + typeof IDBKeyRange + ',' + typeof IDBTransaction }); await done(tx); db.close(); return o;");
const tx2 = db.transaction('kv'); const fw = await req(tx2.objectStore('kv').get('fromWorker'));
const range = await rpc(w, "const db = await open('shared', 1); const all = await req(db.transaction('kv').objectStore('kv').getAll(IDBKeyRange.bound('a', 'z'))); db.close(); return all.map(x => x.k);");
w.terminate(); db.close();
return JSON.stringify({ seen, fw, range });`)
	want := `{"seen":{"k":"fromPage","v":1},"fw":{"k":"fromWorker","v":2,"t":"object,function,function"},"range":["fromPage","fromWorker"]}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

// A page holds a connection open, a worker asks for a higher version: the page gets versionchange, the worker gets
// blocked, and the upgrade runs the moment the page closes. The same the other way round.
func TestWorkerVersionChangeAcrossContexts(t *testing.T) {
	srv := newWKServer(t, map[string]string{"/rpc.js": rpcWorker})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, wkIDBHelpers+pageRPC+`
const log = [];
const db = await open('vc', 1, (d) => d.createObjectStore('s'));
db.onversionchange = (ev) => { log.push('page:versionchange ' + ev.oldVersion + '->' + ev.newVersion); setTimeout(() => db.close(), 30); };
const w = new Worker('/rpc.js');
w.addEventListener('message', (m) => { if (m.data.evt) log.push('worker:' + m.data.evt); });
const v = await rpc(w, "const d = await open('vc', 2, (d, tx, e) => { d.createObjectStore('t'); }, ev); const v = d.version; d.close(); return v;");
log.push('worker upgraded to ' + v);
// now the worker keeps a connection and the page upgrades
await rpc(w, "self.held = await open('vc', 2); self.held.onversionchange = (e) => { ev('versionchange ' + e.oldVersion + '->' + e.newVersion); setTimeout(() => self.held.close(), 30); }; return 1;");
const db3 = await new Promise((res, rej) => { const r = indexedDB.open('vc', 3); r.onblocked = () => log.push('page:blocked'); r.onupgradeneeded = () => log.push('page:upgrade'); r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error); });
log.push('page v' + db3.version + ' stores=' + [...db3.objectStoreNames].join(','));
db3.close(); w.terminate();
return JSON.stringify(log);`)
	// the two sides report through different queues, so only the order within each side is fixed
	want := `["page:versionchange 1->2","worker:blocked","worker upgraded to 2","worker:versionchange 2->3","page:blocked","page:upgrade","page v3 stores=s,t"]`
	alt := `["page:versionchange 1->2","worker:blocked","worker upgraded to 2","page:blocked","worker:versionchange 2->3","page:upgrade","page v3 stores=s,t"]`
	if got != want && got != alt {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}
