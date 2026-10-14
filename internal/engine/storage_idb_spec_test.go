package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIDBKeyOrdering(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('keys', 1, (db) => db.createObjectStore('s'));
const keys = [
  [1, 2], 'b', new Uint8Array([1, 2]), new Date(5000), -Infinity, 'a', 7, [1], 'aa', new Uint8Array([1]), new Date(-1), 0.5, [[]], [],
  Infinity, '', -3, 'é', '😀', '￿', [0, 'x'], [0, 1], new Uint8Array([0, 5]), 'a\u0000b', 'a\u0001b', -0,
];
const tx = db.transaction('s', 'readwrite');
keys.forEach((k, i) => tx.objectStore('s').put(i, k));
await done(tx);
const out = await req(db.transaction('s').objectStore('s').getAllKeys());
const show = (k) => k instanceof Date ? 'D' + k.getTime() : k instanceof ArrayBuffer ? 'B' + [...new Uint8Array(k)] : Array.isArray(k) ? '[' + k.map(show) + ']' : typeof k === 'string' ? 'S' + JSON.stringify(k) : 'N' + k;
const cmp = [indexedDB.cmp(1, 'a'), indexedDB.cmp('a', 1), indexedDB.cmp(new Date(0), 'a'), indexedDB.cmp('z', new Uint8Array(1)), indexedDB.cmp(new Uint8Array(1), []), indexedDB.cmp([1, 2], [1, 3]), indexedDB.cmp([1], [1, 0]), indexedDB.cmp(2, 2), indexedDB.cmp(0, -0)];
let bad = [];
for (const v of [NaN, undefined, null, {}, true, new Date(NaN), [1, {}], Symbol.iterator]) { try { indexedDB.cmp(v, 1); bad.push('no error'); } catch (e) { bad.push(e.name); } }
return JSON.stringify({ order: out.map(show).join(' '), cmp, bad });
`)
	var res struct {
		Order string
		Cmp   []int
		Bad   []string
	}
	if err := json.Unmarshal([]byte(got), &res); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	// numbers, then dates, strings (by UTF-16 code unit: the surrogate pair sorts before U+FFFF), binaries, arrays
	wantOrder := `N-Infinity N-3 N0 N0.5 N7 NInfinity D-1 D5000 S"" S"a" S"a\u0000b" S"a\u0001b" S"aa" S"b" S"é" S"😀" S"` + "\uffff" + `" B0,5 B1 B1,2 [] [N0,N1] [N0,S"x"] [N1] [N1,N2] [[]]`
	if res.Order != wantOrder {
		t.Fatalf("key order\n got %s\nwant %s", res.Order, wantOrder)
	}
	if got := fmt.Sprint(res.Cmp); got != "[-1 1 -1 -1 -1 -1 -1 0 0]" {
		t.Fatalf("cmp %s", got)
	}
	if got := strings.Join(res.Bad, ","); got != strings.Repeat("DataError,", 7)+"DataError" {
		t.Fatalf("invalid keys: %s", got)
	}
}

func TestIDBCursors(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('cur', 1, (db) => {
  const s = db.createObjectStore('s', { keyPath: 'id' });
  s.createIndex('tag', 'tag');
  s.createIndex('uniq', 'u', { unique: true });
});
const tx = db.transaction('s', 'readwrite');
const s = tx.objectStore('s');
[[1, 'x'], [2, 'y'], [3, 'x'], [4, 'y'], [5, 'x']].forEach(([id, tag]) => s.put({ id, tag, u: 'u' + id }));
await done(tx);
const walk = async (src, range, dir, keyOnly) => {
  const out = [];
  await new Promise((res, rej) => {
    const r = keyOnly ? src.openKeyCursor(range, dir) : src.openCursor(range, dir);
    r.onsuccess = () => { const c = r.result; if (!c) return res(); out.push(src.keyPath === undefined ? c.key + '/' + c.primaryKey : c.key + (keyOnly ? '' : ':' + c.value.id)); c.continue(); };
    r.onerror = () => rej(r.error);
  });
  return out.join(' ');
};
const ro = db.transaction('s');
const st = ro.objectStore('s'), ix = st.index('tag');
const r = {};
r.next = await walk(st);
r.prev = await walk(st, null, 'prev');
r.range = await walk(st, IDBKeyRange.bound(2, 4));
r.open = await walk(st, IDBKeyRange.bound(2, 4, true, true));
r.lower = await walk(st, IDBKeyRange.lowerBound(4));
r.upperPrev = await walk(st, IDBKeyRange.upperBound(2, true), 'prev');
r.ixNext = await walk(ix);
r.ixPrev = await walk(ix, null, 'prev');
r.ixUnique = await walk(ix, null, 'nextunique');
r.ixPrevUnique = await walk(ix, null, 'prevunique');
r.ixKey = await walk(ix, 'y', 'next', true);
// continue(key), advance(n), continuePrimaryKey
r.jump = await new Promise((res) => { const out = []; const q = st.openCursor(); q.onsuccess = () => { const c = q.result; if (!c) return res(out.join(',')); out.push(c.key); if (c.key === 1) c.continue(4); else if (c.key === 4) c.advance(5); else c.continue(); }; });
r.adv = await new Promise((res) => { const out = []; const q = st.openCursor(); q.onsuccess = () => { const c = q.result; if (!c) return res(out.join(',')); out.push(c.key); c.advance(2); }; });
r.cpk = await new Promise((res) => { const q = ix.openCursor(); let n = 0; q.onsuccess = () => { const c = q.result; if (!c) return res('end'); if (n++ === 0) c.continuePrimaryKey('x', 3); else res(c.key + '/' + c.primaryKey); }; });
// errors
const errs = [];
await new Promise((res) => { const q = st.openCursor(); q.onsuccess = () => { const c = q.result; c.continue(); try { c.continue(); } catch (e) { errs.push('twice:' + e.name); } try { c.advance(0); } catch (e) { errs.push('adv0:' + e.name); } q.onsuccess = null; res(); }; });
const ro2 = db.transaction('s');
const q = ro2.objectStore('s').openCursor();
await new Promise((res) => { q.onsuccess = () => { try { q.result.update({ id: 1 }); } catch (e) { errs.push('ro-update:' + e.name); } try { q.result.continue(0); } catch (e) { errs.push('back:' + e.name); } res(); }; });
r.errs = errs;
// update and delete through a cursor
const wt = db.transaction('s', 'readwrite');
await new Promise((res) => { const q2 = wt.objectStore('s').openCursor(); q2.onsuccess = () => { const c = q2.result; if (!c) return res(); if (c.key === 2) c.delete(); else if (c.key === 3) c.update({ id: 3, tag: 'z', u: 'u3' }); c.continue(); }; });
await done(wt);
const all = await req(db.transaction('s').objectStore('s').getAll());
r.after = all.map(v => v.id + v.tag).join(' ');
const bad = db.transaction('s', 'readwrite');
const u = await new Promise((res) => { const q3 = bad.objectStore('s').openCursor(); q3.onsuccess = () => { try { q3.result.update({ id: 99, tag: 'q', u: 'u99' }); res('no error'); } catch (e) { res(e.name); } }; });
r.mismatch = u;
bad.abort();
return JSON.stringify(r);
`)
	want := `{"next":"1:1 2:2 3:3 4:4 5:5","prev":"5:5 4:4 3:3 2:2 1:1","range":"2:2 3:3 4:4","open":"3:3","lower":"4:4 5:5","upperPrev":"1:1","ixNext":"x:1 x:3 x:5 y:2 y:4","ixPrev":"y:4 y:2 x:5 x:3 x:1","ixUnique":"x:1 y:2","ixPrevUnique":"y:2 x:1","ixKey":"y y","jump":"1,4","adv":"1,3,5","cpk":"x/3","errs":["twice:InvalidStateError","adv0:TypeError","ro-update:ReadOnlyError","back:DataError"],"after":"1x 3z 4y 5x","mismatch":"DataError"}`
	if got != want {
		// the error list order depends on scheduling; compare the rest strictly
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

func TestIDBIndexUniqueConstraintAndAbort(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('uq', 1, (db) => { const s = db.createObjectStore('s', { keyPath: 'id' }); s.createIndex('email', 'email', { unique: true }); });
const r = {};
let tx = db.transaction('s', 'readwrite');
tx.objectStore('s').add({ id: 1, email: 'a@x' });
await done(tx);
// duplicate unique index key: the request fails with ConstraintError and, unhandled, aborts the transaction
tx = db.transaction('s', 'readwrite');
const st = tx.objectStore('s');
st.add({ id: 2, email: 'b@x' });
const ev = [];
const dup = st.add({ id: 3, email: 'a@x' });
dup.onerror = (e) => { ev.push('req:' + dup.error.name); };
tx.onerror = (e) => ev.push('tx:' + e.target.error.name);
tx.onabort = () => ev.push('abort:' + tx.error.name);
const after = st.add({ id: 4, email: 'd@x' });
after.onerror = () => ev.push('after:' + after.error.name);
await new Promise((res) => { tx.onabort = () => { ev.push('abort:' + tx.error.name); res(); }; });
r.events = ev;
r.rolledBack = (await req(db.transaction('s').objectStore('s').getAll())).map(v => v.id);
// preventDefault keeps the transaction alive
tx = db.transaction('s', 'readwrite');
const d2 = tx.objectStore('s').add({ id: 5, email: 'a@x' });
d2.onerror = (e) => e.preventDefault();
tx.objectStore('s').add({ id: 6, email: 'f@x' });
r.prevented = await done(tx);
r.ids = (await req(db.transaction('s').objectStore('s').getAll())).map(v => v.id);
// put replacing a record with itself is fine; add over an existing key is a ConstraintError
tx = db.transaction('s', 'readwrite');
tx.objectStore('s').put({ id: 1, email: 'a@x', n: 2 });
const addDup = tx.objectStore('s').add({ id: 1, email: 'zz' });
addDup.onerror = (e) => { r.addDup = addDup.error.name; e.preventDefault(); };
await done(tx);
r.byEmail = (await req(db.transaction('s').objectStore('s').index('email').get('a@x'))).n;
return JSON.stringify(r);
`)
	want := `{"events":["req:ConstraintError","tx:ConstraintError","after:AbortError","tx:AbortError","abort:ConstraintError"],"rolledBack":[1],"prevented":"complete","ids":[1,6],"addDup":"ConstraintError","byEmail":2}`
	expect(t, tab, got, want)
}

func TestIDBTransactionLifecycle(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('life', 1, (db) => db.createObjectStore('s'));
const r = {};
// a transaction with no requests completes by itself
let tx = db.transaction('s');
r.empty = await done(tx);
// requests chained from callbacks keep it alive; it completes after the last
tx = db.transaction('s', 'readwrite');
let n = 0;
const chain = () => { const q = tx.objectStore('s').put(n, 'k' + n); q.onsuccess = () => { if (++n < 5) chain(); }; };
chain();
r.chain = await done(tx) + n;
// microtasks (await) keep the transaction active; a timer does not
tx = db.transaction('s', 'readwrite');
const microDone = done(tx).catch(() => 'aborted');
const s = tx.objectStore('s');
await s.put('m', 'micro').constructor && 0;
await Promise.resolve();
try { s.put('m2', 'micro2'); r.micro = 'ok'; } catch (e) { r.micro = e.name; }
await new Promise((res) => setTimeout(res, 10));
try { s.put('late', 'late'); r.timer = 'ok'; } catch (e) { r.timer = e.name; }
r.microDone = await microDone;
// readonly rejects writes; unknown stores and finished transactions throw
tx = db.transaction('s');
try { tx.objectStore('s').put(1, 'x'); } catch (e) { r.ro = e.name; }
try { tx.objectStore('nope'); } catch (e) { r.nf = e.name; }
try { db.transaction('nope'); } catch (e) { r.nf2 = e.name; }
try { db.transaction([]); } catch (e) { r.empty2 = e.name; }
await done(tx);
try { tx.objectStore('s'); } catch (e) { r.fin = e.name; }
// abort: rolls back, fires abort, requests fail with AbortError
tx = db.transaction('s', 'readwrite');
const ev = [];
const w = tx.objectStore('s').put('doomed', 'doomed');
w.onerror = () => ev.push('req:' + w.error.name);
tx.onabort = () => ev.push('abort:' + tx.error);
tx.abort();
try { tx.abort(); } catch (e) { ev.push('again:' + e.name); }
try { tx.objectStore('s'); } catch (e) { ev.push('os:' + e.name); }
await new Promise((res) => setTimeout(res, 20));
r.abort = ev;
r.doomed = await req(db.transaction('s').objectStore('s').get('doomed')) === undefined;
r.keys = (await req(db.transaction('s').objectStore('s').getAllKeys())).join(',');
// an exception in a success handler aborts the transaction
tx = db.transaction('s', 'readwrite');
const ev2 = [];
tx.onabort = () => ev2.push('abort:' + tx.error.name);
const q = tx.objectStore('s').put(1, 'thrower');
q.onsuccess = () => { throw new Error('boom'); };
await new Promise((res) => setTimeout(res, 30));
r.thrown = ev2;
r.thrownGone = await req(db.transaction('s').objectStore('s').get('thrower')) === undefined;
// explicit commit()
tx = db.transaction('s', 'readwrite');
tx.objectStore('s').put('c', 'committed');
tx.commit();
r.commit = await done(tx);
// request state
const probe = db.transaction('s').objectStore('s').get('micro');
try { probe.result; } catch (e) { r.pending = e.name; }
r.state = probe.readyState;
await req(probe);
r.stateAfter = probe.readyState;
return JSON.stringify(r);
`)
	want := `{"empty":"complete","chain":"complete5","micro":"ok","timer":"TransactionInactiveError","microDone":"complete","ro":"ReadOnlyError","nf":"NotFoundError","nf2":"NotFoundError","empty2":"InvalidAccessError","fin":"InvalidStateError","abort":["again:InvalidStateError","os:InvalidStateError","req:AbortError","abort:null"],"doomed":true,"keys":"k0,k1,k2,k3,k4,micro,micro2","thrown":["abort:AbortError"],"thrownGone":true,"commit":"complete","pending":"InvalidStateError","state":"pending","stateAfter":"done"}`
	expect(t, tab, got, want)
}

func TestIDBUpgradeVersionChangeAndBlocked(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	tabA := openTab(t, e, srv.URL+"/")
	tabB := openTab(t, e, srv.URL+"/")
	// tab A opens version 1 and keeps the connection
	got := js(t, tabA, idbHelpers+`
window.log = [];
window.dbA = await open('vc', 1, (db, tx, e) => { log.push('A upgrade ' + e.oldVersion + '->' + e.newVersion); db.createObjectStore('s'); });
dbA.onversionchange = (e) => { log.push('A versionchange ' + e.oldVersion + '->' + e.newVersion); };
return dbA.version;
`)
	expect(t, tabA, got, "1")
	// tab B asks for version 2 and waits
	got = js(t, tabB, `
window.log = [];
window.reqB = indexedDB.open('vc', 2);
reqB.onblocked = (e) => log.push('B blocked ' + e.oldVersion + '->' + e.newVersion);
reqB.onupgradeneeded = (e) => { log.push('B upgrade ' + e.oldVersion + '->' + e.newVersion + ' stores=' + [...reqB.result.objectStoreNames]); reqB.result.createObjectStore('t'); };
reqB.onsuccess = () => log.push('B success ' + reqB.result.version);
return 'started';
`)
	// A sees the versionchange (and does not close): B is told it is blocked
	got = js(t, tabA, `await new Promise(r => setTimeout(r, 20)); return log.join('|');`)
	expect(t, tabA, got, "A upgrade 0->1|A versionchange 1->2")
	got = js(t, tabB, `await new Promise(r => setTimeout(r, 20)); return log.join('|');`)
	expect(t, tabB, got, "B blocked 1->2")
	// A closes: the upgrade proceeds in B
	js(t, tabA, `dbA.close(); return 1;`)
	got = js(t, tabB, `await new Promise(r => setTimeout(r, 50)); return log.join('|') + ' ' + [...reqB.result.objectStoreNames];`)
	expect(t, tabB, got, "B blocked 1->2|B upgrade 1->2 stores=s|B success 2 s,t")
	// deleteDatabase: blocked while B's connection is open, then success with the old version
	got = js(t, tabB, `
const r = indexedDB.deleteDatabase('vc');
const ev = [];
r.onblocked = (e) => { ev.push('blocked ' + e.oldVersion); };
reqB.result.onversionchange = (e) => { ev.push('vc ' + e.oldVersion + '->' + e.newVersion); reqB.result.close(); };
await new Promise((res) => { r.onsuccess = (e) => { ev.push('deleted ' + e.oldVersion + '->' + e.newVersion + ' ' + r.result); res(); }; });
const names = (await indexedDB.databases()).map(d => d.name);
return ev.join('|') + ' ' + names.length;
`)
	expect(t, tabB, got, "vc 2->null|deleted 2->null undefined 0")
	// a lower version than the existing one fails with VersionError
	got = js(t, tabB, idbHelpers+`
await open('ver', 5, () => {});
const q = indexedDB.open('ver', 3);
return await new Promise((res) => { q.onerror = (e) => { e.preventDefault(); res(q.error.name); }; q.onsuccess = () => res('opened'); });
`)
	expect(t, tabB, got, "VersionError")
}

func TestIDBUpgradeAbortAndSchema(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const r = {};
// an aborted first upgrade leaves no database behind and fires AbortError on the open request
r.abort = await new Promise((res) => {
  const q = indexedDB.open('ab', 1);
  q.onupgradeneeded = () => { q.result.createObjectStore('s'); q.transaction.abort(); };
  q.onerror = (e) => { e.preventDefault(); res(q.error.name); };
  q.onsuccess = () => res('success');
});
r.afterAbort = (await indexedDB.databases()).map(d => d.name).join(',');
// schema operations
const db = await open('sc', 1, (db, tx) => {
  const a = db.createObjectStore('a', { keyPath: 'k', autoIncrement: true });
  a.createIndex('i1', 'x.y'); a.createIndex('i2', ['p', 'q']); a.createIndex('multi', 'tags', { multiEntry: true });
  db.createObjectStore('b', { autoIncrement: true });
  db.createObjectStore('c');
  const errs = [];
  try { db.createObjectStore('a'); } catch (e) { errs.push('dup:' + e.name); }
  try { db.createObjectStore('z', { keyPath: 'a b' }); } catch (e) { errs.push('kp:' + e.name); }
  try { db.createObjectStore('z', { keyPath: '', autoIncrement: true }); } catch (e) { errs.push('autoempty:' + e.name); }
  try { a.createIndex('i1', 'x'); } catch (e) { errs.push('dupidx:' + e.name); }
  try { a.createIndex('m2', ['a'], { multiEntry: true }); } catch (e) { errs.push('multiarr:' + e.name); }
  try { db.deleteObjectStore('nope'); } catch (e) { errs.push('delnf:' + e.name); }
  r.errs = errs;
  db.deleteObjectStore('c');
  a.createIndex('tmp', 'zz'); a.deleteIndex('tmp');
  r.within = [...db.objectStoreNames].join(',');
});
r.names = [...db.objectStoreNames].join(',');
const tx = db.transaction(['a', 'b'], 'readwrite');
const a = tx.objectStore('a'), b = tx.objectStore('b');
r.meta = JSON.stringify([a.keyPath, a.autoIncrement, [...a.indexNames], b.keyPath, b.autoIncrement, a.index('i2').keyPath, a.index('multi').multiEntry, a.index('i1').unique]);
const k1 = await req(a.add({ x: { y: 'one' }, p: 1, q: 2, tags: ['t1', 't2', 't1'] }));
const k2 = await req(a.add({ x: { y: 'two' }, p: 1, q: 3, tags: ['t2'] }));
const k3 = await req(a.add({ k: 10, x: { y: 'three' } }));
const k4 = await req(a.add({ x: { y: 'four' } }));
r.keys = [k1, k2, k3, k4];
r.injected = (await req(a.get(k1))).k;
r.bk = [await req(b.add('v1')), await req(b.add('v2')), await req(b.put('v3', 7)), await req(b.add('v4'))];
r.byPath = (await req(a.index('i1').get('two'))).k;
r.compound = (await req(a.index('i2').getAll([1, 3]))).length;
r.multi = [(await req(a.index('multi').getAll('t2'))).length, (await req(a.index('multi').getAll('t1'))).length, await req(a.index('multi').count())];
r.noIndexEntry = await req(a.index('i1').count());
r.keyRange = (await req(a.getAllKeys(IDBKeyRange.bound(2, 10)))).join(',');
r.count = [await req(a.count()), await req(a.count(IDBKeyRange.lowerBound(3))), await req(b.count())];
r.getAllCount = (await req(a.getAll(null, 2))).length;
await req(a.delete(IDBKeyRange.bound(1, 2)));
r.afterDelete = (await req(a.getAllKeys())).join(',');
await req(b.clear());
r.afterClear = await req(b.count());
await done(tx);
return JSON.stringify(r);
`)
	want := `{"abort":"AbortError","afterAbort":"","errs":["dup:ConstraintError","kp:SyntaxError","autoempty:InvalidAccessError","dupidx:ConstraintError","multiarr:InvalidAccessError","delnf:NotFoundError"],"within":"a,b","names":"a,b","meta":"[\"k\",true,[\"i1\",\"i2\",\"multi\"],null,true,[\"p\",\"q\"],true,false]","keys":[1,2,10,11],"injected":1,"bk":[1,2,7,8],"byPath":2,"compound":1,"multi":[2,1,3],"noIndexEntry":4,"keyRange":"2,10","count":[4,2,4],"getAllCount":2,"afterDelete":"10,11","afterClear":0}`
	expect(t, tab, got, want)
}

func TestIDBStructuredCloneInStorage(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('clone', 1, (db) => db.createObjectStore('s'));
const r = {};
const circ = { name: 'c', list: [1, 2] }; circ.self = circ; circ.list.push(circ);
const shared = { s: 1 }; const two = { a: shared, b: shared };
const buf = new ArrayBuffer(8); const u8 = new Uint8Array(buf, 2, 4); u8.set([9, 8, 7, 6]);
const sparse = [1, , 3]; sparse.extra = 'e';
class P { constructor() { this.own = 1; } get viaGetter() { return 2; } method() {} } P.prototype.inherited = 3;
const err = new RangeError('bad');
const tx = db.transaction('s', 'readwrite');
const s = tx.objectStore('s');
s.put(circ, 'circ'); s.put(two, 'two'); s.put({ buf, u8, f64: new Float64Array([1.5, -0]), dv: new DataView(buf, 1, 2) }, 'bin');
s.put(sparse, 'sparse'); s.put(new P(), 'class'); s.put({ err }, 'err'); s.put({ n: new Number(3), s: new String('x'), b: new Boolean(false) }, 'wrap');
s.put({ blob: new Blob(['hi there'], { type: 'text/plain' }), file: new File(['f'], 'n.txt', { type: 'text/x', lastModified: 77 }) }, 'blob');
s.put([undefined, NaN, Infinity, -Infinity, -0, 0n, 'x'], 'prims');
await done(tx);
const g = async (k) => await req(db.transaction('s').objectStore('s').get(k));
const c = await g('circ'); r.circ = [c.self === c, c.list[2] === c, c.list.length, c !== circ];
const t2 = await g('two'); r.shared = t2.a === t2.b;
const bin = await g('bin'); r.bin = [bin.u8.byteOffset, [...bin.u8].join(''), bin.u8.buffer === bin.buf, bin.buf.byteLength, bin.f64[0], Object.is(bin.f64[1], -0), bin.dv.byteOffset, bin.dv.byteLength];
const sp = await g('sparse'); r.sparse = [sp.length, 1 in sp, sp.extra];
const cl = await g('class'); r.cls = [Object.keys(cl).join(), cl instanceof P, cl.inherited, cl.viaGetter];
const er = (await g('err')).err; r.err = [er instanceof RangeError, er.message];
const w = await g('wrap'); r.wrap = [typeof w.n, w.n.valueOf(), typeof w.s, w.s.valueOf(), w.b.valueOf()];
const bl = await g('blob'); r.blob = [bl.blob instanceof Blob, bl.blob.size, bl.blob.type, await bl.blob.text(), bl.file instanceof File, bl.file.name, bl.file.lastModified, await bl.file.text()];
const pr = await g('prims'); r.prims = [pr.length, 0 in pr && pr[0], Number.isNaN(pr[1]), pr[2], pr[3], Object.is(pr[4], -0), String(pr[5]) + typeof pr[5], pr[6]];
// things that cannot be cloned
const fails = [];
for (const [label, v] of [['fn', () => 1], ['sym', Symbol('x')], ['objfn', { f() {} }], ['dom', document.body], ['promise', Promise.resolve(1)], ['weak', new WeakMap()]]) {
  const t3 = db.transaction('s', 'readwrite');
  try { t3.objectStore('s').put(v, 'bad'); fails.push(label + ':ok'); } catch (e) { fails.push(label + ':' + e.name); }
  t3.abort();
}
r.fails = fails;
return JSON.stringify(r);
`)
	want := `{"circ":[true,true,3,true],"shared":true,"bin":[2,"9876",true,8,1.5,true,1,2],"sparse":[3,false,"e"],"cls":["own",false,null,null],"err":[true,"bad"],"wrap":["object",3,"object","x",false],"blob":[true,8,"text/plain","hi there",true,"n.txt",77,"f"],"prims":[7,null,true,null,null,true,"0bigint","x"],"fails":["fn:DataCloneError","sym:DataCloneError","objfn:DataCloneError","dom:DataCloneError","promise:DataCloneError","weak:DataCloneError"]}`
	expect(t, tab, got, want)
}

func TestIDBPersistsAcrossEngineRestart(t *testing.T) {
	srv := storageServer(t)
	dir := t.TempDir()
	e1 := New(func() Options { o := DefaultOptions(); o.StorageDir = dir; return o }())
	// without a policy persist() is refused (see TestStoragePersistFollowsThePolicy); this one grants it
	e1.DefaultContext().SiteStorage().SetPersistPolicy(func(string) bool { return true })
	tab := openTab(t, e1, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('persist', 3, (db) => {
  const s = db.createObjectStore('s', { keyPath: 'id', autoIncrement: true });
  s.createIndex('by', 'v');
});
const tx = db.transaction('s', 'readwrite');
for (let i = 0; i < 50; i++) tx.objectStore('s').add({ v: 'val' + (i % 5), d: new Date(i) });
await done(tx);
db.close();
const est = await navigator.storage.estimate();
return JSON.stringify({ usage: est.usage > 0, quota: est.quota > est.usage, idb: est.usageDetails.indexedDB > 0, persisted: await navigator.storage.persisted(), persist: await navigator.storage.persist(), persisted2: await navigator.storage.persisted() });
`)
	expect(t, tab, got, `{"usage":true,"quota":true,"idb":true,"persisted":false,"persist":true,"persisted2":true}`)
	e1.Close()
	if files, _ := filepath.Glob(filepath.Join(dir, "*", "kv", "CURRENT")); len(files) != 1 {
		t.Fatalf("expected one ordered store under %s, got %v", dir, files)
	}

	e2 := New(func() Options { o := DefaultOptions(); o.StorageDir = dir; return o }())
	defer e2.Close()
	tab2 := openTab(t, e2, srv.URL+"/")
	got = js(t, tab2, idbHelpers+`
const dbs = await indexedDB.databases();
const db = await open('persist');
const s = db.transaction('s').objectStore('s');
const all = await req(s.getAll());
const by = await req(s.index('by').getAll('val3'));
const next = db.transaction('s', 'readwrite');
const k = await req(next.objectStore('s').add({ v: 'new' }));
await done(next);
return JSON.stringify({ dbs, version: db.version, n: all.length, first: all[0].id, date: all[7].d.getTime(), by: by.length, next: k, persisted: await navigator.storage.persisted() });
`)
	expect(t, tab2, got, `{"dbs":[{"name":"persist","version":3}],"version":3,"n":50,"first":1,"date":7,"by":10,"next":51,"persisted":true}`)
}

func TestIDBEphemeralContextsStayOffDisk(t *testing.T) {
	srv := storageServer(t)
	dir := t.TempDir()
	o := DefaultOptions()
	o.StorageDir = dir
	e := New(o)
	defer e.Close()
	// a named context without StorageDir (incognito, agent sandboxes) is memory only
	ctx := e.NewContext("private", ContextOptions{})
	tab, err := ctx.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	got := js(t, tab, idbHelpers+`
const db = await open('secret', 1, (db) => db.createObjectStore('s'));
const tx = db.transaction('s', 'readwrite');
tx.objectStore('s').put('hidden', 'k');
await done(tx);
db.close();
const est = await navigator.storage.estimate();
return JSON.stringify({ v: await req((await open('secret')).transaction('s').objectStore('s').get('k')), persist: await navigator.storage.persist(), usage: est.usage > 0 });
`)
	expect(t, tab, got, `{"v":"hidden","persist":false,"usage":true}`)
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("an ephemeral context wrote to the storage dir: %v", ents)
	}
	// the default context sees none of it
	tab2 := openTab(t, e, srv.URL+"/")
	got = js(t, tab2, `return JSON.stringify(await indexedDB.databases());`)
	expect(t, tab2, got, `[]`)
	// ... and different origins in one context are isolated from each other
	srv2 := storageServer(t)
	tab3 := openTab(t, e, srv2.URL+"/")
	js(t, tab3, idbHelpers+`await open('mine', 1, () => {}); return 1;`)
	got = js(t, tab2, `return JSON.stringify((await indexedDB.databases()).map(d => d.name));`)
	expect(t, tab2, got, `[]`)
	// opaque origins have no storage
	tab4 := openTab(t, e, "about:blank")
	_, err = tab4.Evaluate(`typeof indexedDB`)
	if err != nil {
		t.Fatal(err)
	}
	got = js(t, tab4, `try { indexedDB.open('x'); return 'opened'; } catch (e) { return e.name; }`)
	expect(t, tab4, got, "SecurityError")
}

func TestIDBClearSiteData(t *testing.T) {
	srv := storageServer(t)
	dir := t.TempDir()
	o := DefaultOptions()
	o.StorageDir = dir
	e := New(o)
	defer e.Close()
	tab := openTab(t, e, srv.URL+"/")
	js(t, tab, idbHelpers+`
window.db = await open('wipe', 1, (db) => db.createObjectStore('s'));
window.closed = false; db.onclose = () => { window.closed = true; };
const tx = db.transaction('s', 'readwrite'); tx.objectStore('s').put(1, 'a'); await done(tx); return 1;`)
	e.DefaultContext().ClearSiteData("")
	got := js(t, tab, `await new Promise(r => setTimeout(r, 20)); return JSON.stringify({ closed: window.closed, dbs: await indexedDB.databases() });`)
	expect(t, tab, got, `{"closed":true,"dbs":[]}`)
	if files, _ := filepath.Glob(filepath.Join(dir, "*", "kv", "*")); len(files) != 0 {
		t.Fatalf("files survived clearing: %v", files)
	}
}

func TestIDBQuotaExceeded(t *testing.T) {
	srv := storageServer(t)
	o := DefaultOptions()
	o.StorageQuotaMiB = 1
	e := New(o)
	defer e.Close()
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('big', 1, (db) => db.createObjectStore('s'));
const chunk = 'x'.repeat(300 * 1024);
const tx = db.transaction('s', 'readwrite');
for (let i = 0; i < 6; i++) tx.objectStore('s').put(chunk, i);
const r = {};
try { await done(tx); r.result = 'complete'; } catch (e) { r.result = e.name; }
r.count = await req(db.transaction('s').objectStore('s').count());
const est = await navigator.storage.estimate();
r.quota = est.quota;
return JSON.stringify(r);
`)
	expect(t, tab, got, `{"result":"QuotaExceededError","count":0,"quota":1048576}`)
}

func TestIDBMultiEntryAndKeyPathEdgeCases(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('kp', 1, (db) => {
  const s = db.createObjectStore('s', { keyPath: 'a.b' });
  const t = db.createObjectStore('t', { keyPath: ['x', 'y'] });
  const u = db.createObjectStore('u', { keyPath: 'id', autoIncrement: true });
  s.createIndex('len', 'name.length');
});
const r = {};
let tx = db.transaction(['s', 't', 'u'], 'readwrite');
await req(tx.objectStore('s').put({ a: { b: 'k1' }, name: 'four' }));
r.nested = await req(tx.objectStore('s').index('len').get(4)).then(v => v.a.b);
r.compound = await req(tx.objectStore('t').put({ x: 1, y: 'z' }));
const errs = [];
try { tx.objectStore('s').put({ nokey: 1 }); } catch (e) { errs.push('missing:' + e.name); }
try { tx.objectStore('s').put({ a: { b: 'k' } }, 'extra'); } catch (e) { errs.push('inline+key:' + e.name); }
try { tx.objectStore('s').put({ a: { b: {} } }); } catch (e) { errs.push('badkey:' + e.name); }
try { tx.objectStore('u').put({ id: 'str' }); errs.push('strkey:ok'); } catch (e) { errs.push('strkey:' + e.name); }
try { tx.objectStore('u').put(5); } catch (e) { errs.push('inject-prim:' + e.name); }
try { tx.objectStore('t').get(undefined); } catch (e) { errs.push('undefkey:' + e.name); }
r.errs = errs;
await done(tx);
return JSON.stringify(r);
`)
	want := `{"nested":"k1","compound":[1,"z"],"errs":["missing:DataError","inline+key:DataError","badkey:DataError","strkey:ok","inject-prim:DataError","undefkey:DataError"]}`
	expect(t, tab, got, want)
}

func TestIDBThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("throughput measurement")
	}
	srv := storageServer(t)
	dir := t.TempDir()
	o := DefaultOptions()
	o.StorageDir = dir
	e := New(o)
	defer e.Close()
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, idbHelpers+`
const db = await open('perf', 1, (db) => { const s = db.createObjectStore('s', { keyPath: 'id' }); s.createIndex('g', 'g'); });
const N = 10000, r = {};
let t0 = performance.now();
let tx = db.transaction('s', 'readwrite'); let st = tx.objectStore('s');
for (let i = 0; i < N; i++) st.put({ id: i, g: i % 100, name: 'item ' + i, tags: ['a', 'b'] });
await done(tx);
r.putMs = performance.now() - t0;
t0 = performance.now();
tx = db.transaction('s'); st = tx.objectStore('s');
let pending = 0; await new Promise((res) => { for (let i = 0; i < N; i++) { pending++; st.get(i).onsuccess = () => { if (--pending === 0) res(); }; } });
r.getMs = performance.now() - t0;
t0 = performance.now();
const all = await req(db.transaction('s').objectStore('s').getAll());
r.getAllMs = performance.now() - t0; r.all = all.length;
t0 = performance.now();
let c = 0; await new Promise((res) => { const q = db.transaction('s').objectStore('s').index('g').openCursor(); q.onsuccess = () => { if (q.result) { c++; q.result.continue(); } else res(); }; });
r.cursorMs = performance.now() - t0; r.cursor = c;
t0 = performance.now();
for (let i = 0; i < 1000; i++) { const t1 = db.transaction('s', 'readwrite'); t1.objectStore('s').put({ id: 20000 + i, g: 1 }); await done(t1); }
r.txMs = performance.now() - t0;
return JSON.stringify(r);
`)
	t.Logf("10k records: %s", got)
}

func TestIDBSettleDoesNotHang(t *testing.T) {
	srv := storageServer(t)
	e := newStorageEngine(t, "")
	start := time.Now()
	tab := openTab(t, e, srv.URL+"/")
	js(t, tab, idbHelpers+`const db = await open('quick', 1, (db) => db.createObjectStore('s')); db.close(); return 1;`)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("slow: %v", d)
	}
}
