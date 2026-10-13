package engine

import (
	"testing"
)

func TestWebLocksOrderingModesAndQuery(t *testing.T) {
	srv := newSWServer(t, basicSW)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, `
const log = [];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let release1;
const p1 = navigator.locks.request('res', async (l) => { log.push('1 got ' + l.name + ' ' + l.mode); await new Promise((r) => { release1 = r; }); log.push('1 done'); return 'a'; });
const p2 = navigator.locks.request('res', async () => { log.push('2 got'); return 'b'; });
const p3 = navigator.locks.request('other', async () => { log.push('3 got (other name, no wait)'); return 'c'; });
await sleep(30);
const q = await navigator.locks.query();
log.push('query held=' + q.held.map((h) => h.name + ':' + h.mode).join(',') + ' pending=' + q.pending.map((h) => h.name + ':' + h.mode).join(','));
release1();
log.push(await Promise.all([p1, p2, p3]));
return log.join('|');`)
	expect(t, tab, got, "1 got res exclusive|3 got (other name, no wait)|query held=res:exclusive pending=res:exclusive|1 done|2 got|a,b,c")

	got = js(t, tab, `
const log = [];
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
// shared locks run together; an exclusive request waits for them, and a later shared one waits behind that exclusive
let relS1; let relS2;
const s1 = navigator.locks.request('rw', { mode: 'shared' }, async () => { log.push('s1'); await new Promise((r) => { relS1 = r; }); });
const s2 = navigator.locks.request('rw', { mode: 'shared' }, async () => { log.push('s2'); await new Promise((r) => { relS2 = r; }); });
await sleep(20);
const x = navigator.locks.request('rw', async () => { log.push('x'); });
const s3 = navigator.locks.request('rw', { mode: 'shared' }, async () => { log.push('s3'); });
await sleep(20);
const marks = [];
marks.push('before release: ' + log.join(','));
relS1(); await sleep(20); marks.push('after one: ' + log.join(','));
relS2(); await Promise.all([s1, s2, x, s3]);
return marks.join('|') + '|final=' + log.join(',');`)
	expect(t, tab, got, "before release: s1,s2|after one: s1,s2|final=s1,s2,x,s3")

	got = js(t, tab, `
const r = {};
let rel; navigator.locks.request('busy', () => new Promise((res) => { rel = res; }));
await new Promise((x) => setTimeout(x, 20));
r.ifAvailableBusy = await navigator.locks.request('busy', { ifAvailable: true }, (l) => l === null ? 'null' : 'lock');
r.ifAvailableFree = await navigator.locks.request('free', { ifAvailable: true }, (l) => l ? l.name : 'null');
const bad = async (label, name, opts) => { try { await navigator.locks.request(name, opts, () => 1); r[label] = 'ok'; } catch (e) { r[label] = e.name; } };
await bad('dash', '-x', {});
await bad('stealIf', 'a', { steal: true, ifAvailable: true });
await bad('stealShared', 'a', { steal: true, mode: 'shared' });
await bad('mode', 'a', { mode: 'wrong' });
await bad('signalIf', 'a', { signal: new AbortController().signal, ifAvailable: true });
try { await navigator.locks.request('a'); r.noCb = 'ok'; } catch (e) { r.noCb = e.name; }
const ab = new AbortController(); ab.abort();
await bad('preAborted', 'busy', { signal: ab.signal });
// abort while queued
const ac = new AbortController();
const queued = navigator.locks.request('busy', { signal: ac.signal }, () => 'never').then(() => 'granted', (e) => e.name);
await new Promise((x) => setTimeout(x, 20));
ac.abort();
r.aborted = await queued;
rel();
r.afterRelease = await navigator.locks.request('busy', () => 'free again');
return JSON.stringify(r);`)
	expect(t, tab, got, `{"ifAvailableBusy":"null","ifAvailableFree":"free","dash":"NotSupportedError","stealIf":"NotSupportedError","stealShared":"NotSupportedError","mode":"TypeError","signalIf":"NotSupportedError","noCb":"TypeError","preAborted":"AbortError","aborted":"AbortError","afterRelease":"free again"}`)

	// a callback that throws releases the lock and rejects the request
	got = js(t, tab, `
const r = {};
try { await navigator.locks.request('t', () => { throw new RangeError('boom'); }); } catch (e) { r.sync = e.name; }
try { await navigator.locks.request('t', async () => { throw new SyntaxError('boom'); }); } catch (e) { r.async = e.name; }
r.next = await navigator.locks.request('t', () => 'ok');
return JSON.stringify(r);`)
	expect(t, tab, got, `{"sync":"RangeError","async":"SyntaxError","next":"ok"}`)
}

func TestWebLocksSteal(t *testing.T) {
	srv := newSWServer(t, basicSW)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, `
const r = {};
const holder = navigator.locks.request('s', () => new Promise(() => {})).then(() => 'resolved', (e) => e.name);
const waiter = navigator.locks.request('s', () => 'waiter ran');
await new Promise((x) => setTimeout(x, 20));
r.stealer = await navigator.locks.request('s', { steal: true }, () => 'stolen');
r.holder = await holder;
r.waiter = await waiter;
return JSON.stringify(r);`)
	expect(t, tab, got, `{"stealer":"stolen","holder":"AbortError","waiter":"waiter ran"}`)
}

func TestWebLocksAreSharedAcrossTabsOfAnOriginAndReleasedOnClose(t *testing.T) {
	srv := newSWServer(t, basicSW)
	other := newSWServer(t, basicSW)
	e := newStorageEngine(t, "")
	a := openTab(t, e, srv.URL+"/")
	b := openTab(t, e, srv.URL+"/")
	c := openTab(t, e, other.URL+"/") // another origin: its own namespace

	js(t, a, `window.__rel = null; window.__held = navigator.locks.request('shared-name', () => new Promise((r) => { window.__rel = r; })); await new Promise((r) => setTimeout(r, 30)); return 1;`)
	js(t, b, `window.__got = 'waiting'; navigator.locks.request('shared-name', () => { window.__got = 'got'; }); return 1;`)
	js(t, c, `window.__got = 'waiting'; navigator.locks.request('shared-name', () => { window.__got = 'got'; }); return 1;`)
	waitUntil(t, "the other origin gets its own lock", func() bool { return js(t, c, `return window.__got;`) == "got" })
	if got := js(t, b, `return window.__got;`); got != "waiting" {
		t.Fatalf("tab b must wait for tab a, state = %s", got)
	}
	q := js(t, b, `const q = await navigator.locks.query(); return q.held.length + ':' + q.pending.length + ':' + (q.held[0].clientId !== q.pending[0].clientId);`)
	if q != "1:1:true" {
		t.Fatalf("query from another tab: %s", q)
	}
	js(t, a, `window.__rel(); return 1;`)
	waitUntil(t, "tab b to get the lock after a releases", func() bool { return js(t, b, `return window.__got;`) == "got" })

	// closing a tab frees what it held and drops what it waited for
	js(t, b, `window.__keep = navigator.locks.request('closing', () => new Promise(() => {})); await new Promise((r) => setTimeout(r, 30)); return 1;`)
	js(t, a, `window.__got2 = 'waiting'; navigator.locks.request('closing', () => { window.__got2 = 'got'; }); return 1;`)
	if js(t, a, `return window.__got2;`) != "waiting" {
		t.Fatal("tab a must wait while b holds the lock")
	}
	if err := e.CloseTab(b.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "release on tab close", func() bool { return js(t, a, `return window.__got2;`) == "got" })
}

func TestWebLocksInServiceWorkerAndPageShareNames(t *testing.T) {
	srv := newSWServer(t, `
self.addEventListener('message', (e) => {
  if (e.data === 'hold') {
    e.waitUntil(navigator.locks.request('cross', async () => { e.source.postMessage('sw has it'); await new Promise((r) => { self.__release = r; }); }));
  } else if (e.data === 'release') { self.__release(); }
  else if (e.data === 'query') { e.waitUntil(navigator.locks.query().then((q) => e.source.postMessage('sw sees ' + q.held.map((h) => h.name).join(',')))); }
});
self.addEventListener('activate', (e) => e.waitUntil(clients.claim()));`)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, swHelpers+`
await regReady('/sw.js');
const msgs = [];
navigator.serviceWorker.addEventListener('message', (e) => msgs.push(e.data));
const reg = await navigator.serviceWorker.ready;
reg.active.postMessage('hold');
await sleep(150);
const log = [msgs.join(',')];
let pageGot = 'waiting';
const pageReq = navigator.locks.request('cross', () => { pageGot = 'got'; });
await sleep(60);
log.push(pageGot);
reg.active.postMessage('query'); await sleep(100);
log.push(msgs[msgs.length - 1]);
reg.active.postMessage('release');
await pageReq;
log.push(pageGot);
return log.join('|');`)
	expect(t, tab, got, "sw has it|waiting|sw sees cross|got")
}
