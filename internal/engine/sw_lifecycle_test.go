package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// swHelpers: register and wait until the worker is activated.
const swHelpers = `
const settled = (w, states) => new Promise((res) => { if (states.includes(w.state)) return res(w.state); w.addEventListener('statechange', () => { if (states.includes(w.state)) res(w.state); }); });
const regReady = async (url, opts) => {
  const reg = await navigator.serviceWorker.register(url, opts);
  const w = reg.installing || reg.waiting || reg.active;
  await settled(w, ['activated', 'redundant']);
  return reg;
};
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
`

func TestServiceWorkerLifecycleAndFetchControl(t *testing.T) {
	srv := newSWServer(t, basicSW)
	srv.setExtra("/asset.js", "window.asset = 'network';")
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, swHelpers+`
const log = [];
const reg0 = await navigator.serviceWorker.register('/sw.js');
log.push('registered scope=' + reg0.scope.replace(location.origin, '') + ' installing=' + (reg0.installing ? reg0.installing.state : null) + ' controller=' + (navigator.serviceWorker.controller ? 'yes' : 'no'));
const states = [];
const w = reg0.installing;
w.addEventListener('statechange', () => states.push(w.state));
const reg = await navigator.serviceWorker.ready;
log.push('ready same=' + (reg === reg0));
await settled(w, ['activated']);
log.push('controller=' + (navigator.serviceWorker.controller === reg.active) + ' script=' + navigator.serviceWorker.controller.scriptURL.replace(location.origin, ''));
const r = await fetch('/api/hello');
log.push(await r.text() + ' x-sw=' + r.headers.get('x-sw'));
log.push('asset=' + await (await fetch('/asset.js')).text());
const x = await new Promise((res) => { const q = new XMLHttpRequest(); q.open('GET', '/api/hello'); q.onload = () => res(q.responseText + q.status); q.send(); });
log.push('xhr=' + x);
const miss = await fetch('/nothing-here');
log.push('passthrough=' + miss.status);
log.push('states=' + states.join(','));
return log.join('|');
`)
	want := "registered scope=/ installing=installing controller=no|ready same=true|controller=true script=/sw.js|hello from sw x-sw=1|asset=window.asset = 'network';|xhr=hello from sw200|passthrough=404|states=installed,activating,activated"
	expect(t, tab, got, want)
	if n := srv.hit("/api/hello"); n != 0 {
		t.Fatalf("the worker's response reached the server %d times", n)
	}
	if n := srv.hit("/asset.js"); n != 1 {
		t.Fatalf("/asset.js reached the server %d times, want 1 (the install's addAll)", n)
	}
	if n := srv.hit("/nothing-here"); n != 1 {
		t.Fatalf("an unhandled request must go to the network once, got %d", n)
	}
	if infos := e.DefaultContext().ServiceWorkers(); len(infos) != 1 || infos[0].Active != "activated" || infos[0].Installing != "" || infos[0].Waiting != "" {
		t.Fatalf("registrations: %+v", infos)
	}
}

const pagesSW = `
const CACHE = 'pages-v1';
importScripts('/helper.js');
self.addEventListener('install', (e) => e.waitUntil(caches.open(CACHE).then((c) => c.addAll(['/', '/page2.html', '/lib.js']))));
self.addEventListener('activate', (e) => e.waitUntil(clients.claim()));
self.addEventListener('fetch', (e) => {
  if (new URL(e.request.url).pathname === '/helper-says') { e.respondWith(new Response(helper())); return; }
  e.respondWith(caches.match(e.request).then((r) => r || fetch(e.request)));
});
`

func setupPagesSW(srv *swServer) {
	srv.setExtra("/page2.html", `<!doctype html><html><head><title>Page Two</title></head><body><p id="o">two</p><script src="/lib.js"></script></body></html>`)
	srv.setExtra("/lib.js", `document.getElementById('o').textContent = 'two+lib';`)
	srv.setExtra("/helper.js", `function helper() { return 'helped'; }`)
}

func TestServiceWorkerServesNavigationsAndSubresourcesAndSurvivesRestart(t *testing.T) {
	srv := newSWServer(t, pagesSW)
	setupPagesSW(srv)
	dir := t.TempDir()
	mk := func() *Engine { o := DefaultOptions(); o.StorageDir = dir; o.DownloadsDir = t.TempDir(); return New(o) }

	e1 := mk()
	tab := openTab(t, e1, srv.URL+"/")
	js(t, tab, swHelpers+`await regReady('/sw.js'); return 1;`)
	// a navigation inside the scope is answered by the worker, and so are the subresources of the page
	if err := tab.Navigate(srv.URL+"/page2.html", nil); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab.Text("#o"); txt != "two+lib" {
		t.Fatalf("page served through the worker: %q\n%s", txt, consoleText(tab))
	}
	got := js(t, tab, `
let helper = await (await fetch('/helper-says')).text();
return JSON.stringify({ controlled: !!navigator.serviceWorker.controller, helper, title: document.title });
`)
	expect(t, tab, got, `{"controlled":true,"helper":"helped","title":"Page Two"}`)
	if srv.hit("/page2.html") != 1 || srv.hit("/lib.js") != 1 {
		t.Fatalf("the network was used after install: page2=%d lib=%d", srv.hit("/page2.html"), srv.hit("/lib.js"))
	}
	e1.Close()
	files, _ := filepath.Glob(filepath.Join(dir, "*", "sw", "regs.json"))
	if len(files) != 1 {
		t.Fatalf("registration not persisted: %v", files)
	}

	// the server is gone and the engine restarted: the registration comes back from disk and serves the cached page
	srv.Close()
	e2 := mk()
	defer e2.Close()
	tab2 := openTab(t, e2, "about:blank")
	if err := tab2.Navigate(srv.URL+"/page2.html", nil); err != nil {
		t.Fatalf("offline navigation through the persisted worker: %v", err)
	}
	if txt, _ := tab2.Text("#o"); txt != "two+lib" {
		t.Fatalf("offline page: %q\n%s", txt, consoleText(tab2))
	}
	got = js(t, tab2, `
const regs = await navigator.serviceWorker.getRegistrations();
const r = await fetch('/helper-says');
return JSON.stringify({ n: regs.length, active: regs[0].active.state, scope: regs[0].scope.replace(location.origin, ''), controlled: !!navigator.serviceWorker.controller, helper: await r.text() });
`)
	expect(t, tab2, got, `{"n":1,"active":"activated","scope":"/","controlled":true,"helper":"helped"}`)
}

func TestServiceWorkerRegistrationRules(t *testing.T) {
	srv := newSWServer(t, basicSW)
	other := newSWServer(t, basicSW)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, `
const r = {};
const attempt = async (label, f) => { try { const reg = await f(); r[label] = 'ok:' + reg.scope.replace(location.origin, ''); } catch (e) { r[label] = e.name; } };
await attempt('cross', () => navigator.serviceWorker.register('`+other.URL+`/sw.js'));
await attempt('notfound', () => navigator.serviceWorker.register('/missing.js'));
await attempt('mime', () => navigator.serviceWorker.register('/html.js'));
await attempt('scopeAbove', () => navigator.serviceWorker.register('/sub/sw.js', { scope: '/' }));
await attempt('scopeBelow', () => navigator.serviceWorker.register('/sub/sw.js', { scope: '/sub/deeper/' }));
await attempt('allowedHeader', () => navigator.serviceWorker.register('/wide/sw.js', { scope: '/' }));
await attempt('defaultScope', () => navigator.serviceWorker.register('/sub/sw.js'));
await attempt('crossScope', () => navigator.serviceWorker.register('/sub/sw.js', { scope: '`+other.URL+`/sub/' }));
r.get = [(await navigator.serviceWorker.getRegistration('/sub/x.html')).scope.replace(location.origin, ''), (await navigator.serviceWorker.getRegistration('/elsewhere')) === undefined ? 'none' : 'some'];
r.all = (await navigator.serviceWorker.getRegistrations()).map(x => x.scope.replace(location.origin, '')).sort().join(',');
// registering the same script again changes nothing and resolves with the same registration
const a = await navigator.serviceWorker.register('/sub/sw.js');
const b = await navigator.serviceWorker.register('/sub/sw.js');
r.same = a === b;
return JSON.stringify(r);
`)
	want := `{"cross":"SecurityError","notfound":"TypeError","mime":"SecurityError","scopeAbove":"SecurityError","scopeBelow":"ok:/sub/deeper/","allowedHeader":"ok:/","defaultScope":"ok:/sub/","crossScope":"SecurityError","get":["/sub/","some"],"all":"/,/sub/,/sub/deeper/","same":true}`
	// "elsewhere" is covered by the registration for "/"
	want = strings.Replace(want, `"get":["/sub/","some"]`, `"get":["/sub/","some"]`, 1)
	expect(t, tab, got, want)
	// an insecure origin has no service workers at all
	tab2 := openTab(t, e, "about:blank")
	tab2.SetDocument(`<html><body><script>window.x = 1</script></body></html>`, "http://insecure.example/page")
	got = js(t, tab2, `return JSON.stringify([typeof navigator.serviceWorker, window.isSecureContext, typeof caches]);`)
	expect(t, tab2, got, `["undefined",false,"undefined"]`)
}

func TestServiceWorkerUpdateWaitingSkipWaitingAndRedundant(t *testing.T) {
	srv := newSWServer(t, `self.addEventListener('install', () => {}); self.addEventListener('activate', (e) => e.waitUntil(clients.claim())); self.VERSION = 1;`)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, swHelpers+`
const r = {};
const reg = await regReady('/sw.js');
const v1 = reg.active;
r.update1 = await reg.update().then(x => x === reg);   // nothing changed: no new worker
r.noNew = [reg.installing, reg.waiting].map(x => x === null).join();
window.reg = reg; window.v1 = v1;
return JSON.stringify(r);
`)
	expect(t, tab, got, `{"update1":true,"noNew":"true,true"}`)
	// a changed script installs but waits, because a document is controlled by the old worker
	srv.setScript(`self.addEventListener('install', () => {}); self.addEventListener('activate', (e) => e.waitUntil(clients.claim())); self.VERSION = 2;`)
	got = js(t, tab, swHelpers+`
const r = {};
const found = new Promise((res) => reg.addEventListener('updatefound', () => res(reg.installing)));
await reg.update();
const v2 = await found;
r.updatefound = v2.state;
await settled(v2, ['installed']);
r.waiting = [reg.waiting === v2, reg.active === v1, v1.state];
await sleep(100);
r.stillWaiting = [v2.state, reg.waiting === v2];
window.v2 = v2;
return JSON.stringify(r);
`)
	expect(t, tab, got, `{"updatefound":"installing","waiting":[true,true,"activated"],"stillWaiting":["installed",true]}`)
	// the new worker takes over when it asks to (skipWaiting) -- here the script is changed again to do so
	srv.setScript(`self.addEventListener('install', (e) => { self.skipWaiting(); }); self.addEventListener('activate', (e) => e.waitUntil(clients.claim())); self.VERSION = 3;`)
	got = js(t, tab, swHelpers+`
const r = {};
const cc = new Promise((res) => navigator.serviceWorker.addEventListener('controllerchange', () => res('changed')));
await reg.update();
const v3 = reg.installing || reg.waiting;
r.v3 = await settled(v3, ['activated', 'redundant']);
r.cc = await cc;
r.active = reg.active === v3;
r.old = [v1.state, v2.state];
r.ctl = navigator.serviceWorker.controller === v3;
return JSON.stringify(r);
`)
	expect(t, tab, got, `{"v3":"activated","cc":"changed","active":true,"old":["redundant","redundant"],"ctl":true}`)
}

func TestServiceWorkerWaitingWorkerActivatesWhenControlledPageCloses(t *testing.T) {
	srv := newSWServer(t, `self.addEventListener('install', () => {}); self.addEventListener('activate', (e) => e.waitUntil(clients.claim())); self.VERSION = 1;`)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	js(t, tab, swHelpers+`window.reg = await regReady('/sw.js'); return 1;`)
	srv.setScript(`self.addEventListener('activate', (e) => e.waitUntil(clients.claim())); self.VERSION = 2;`)
	js(t, tab, swHelpers+`await reg.update(); await settled(reg.installing || reg.waiting, ['installed']); return 1;`)
	if infos := e.DefaultContext().ServiceWorkers(); infos[0].Waiting != "installed" || infos[0].Active != "activated" {
		t.Fatalf("expected a waiting worker behind an active one: %+v", infos)
	}
	// a second page (uncontrolled so far) sees the registration
	tab2 := openTab(t, e, "about:blank")
	_ = tab2
	// closing the controlled page lets the waiting worker activate
	if err := e.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if infos := e.DefaultContext().ServiceWorkers(); infos[0].Waiting == "" && infos[0].Active == "activated" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waiting worker did not activate after the last controlled page closed: %+v", e.DefaultContext().ServiceWorkers())
}

func TestServiceWorkerMessagesClientsAndPorts(t *testing.T) {
	srv := newSWServer(t, `
self.addEventListener('activate', (e) => e.waitUntil(clients.claim()));
self.addEventListener('message', (e) => {
  const d = e.data;
  if (d && d.cmd === 'echo') e.source.postMessage({ echo: d.value, origin: e.origin === self.location.origin, ports: e.ports.length });
  else if (d && d.cmd === 'port') { e.ports[0].postMessage({ via: 'port', got: d.value }); e.ports[0].onmessage = (m) => e.ports[0].postMessage({ second: m.data }); }
  else if (d && d.cmd === 'clients') {
    e.waitUntil(clients.matchAll().then((cs) => e.source.postMessage({ n: cs.length, same: cs[0].id === e.source.id, url: cs[0].url, type: cs[0].type })));
  } else if (d && d.cmd === 'get') e.waitUntil(clients.get(e.source.id).then((c) => e.source.postMessage({ found: !!c, url: c && c.url })));
  else if (d && d.cmd === 'rich') e.source.postMessage({ date: new Date(0), map: new Map([[1, 2]]), u8: new Uint8Array([1, 2, 3]) });
});
`)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, swHelpers+`
const reg = await regReady('/sw.js');
const msgs = [];
navigator.serviceWorker.addEventListener('message', (e) => msgs.push(e.data));
const next = () => new Promise((res) => { const n = msgs.length; const t = setInterval(() => { if (msgs.length > n) { clearInterval(t); res(msgs[msgs.length - 1]); } }, 5); });
const r = {};
reg.active.postMessage({ cmd: 'echo', value: [1, { a: 'b' }] });
r.echo = await next();
reg.active.postMessage({ cmd: 'clients' });
r.clients = await next();
reg.active.postMessage({ cmd: 'get' });
r.get = await next();
reg.active.postMessage({ cmd: 'rich' });
const rich = await next();
r.rich = [rich.date instanceof Date, rich.date.getTime(), rich.map instanceof Map, rich.map.get(1), [...rich.u8].join('')];
// a MessageChannel port travels to the worker and answers on it
const ch = new MessageChannel();
const replies = [];
ch.port1.onmessage = (m) => replies.push(m.data);
reg.active.postMessage({ cmd: 'port', value: 7 }, [ch.port2]);
await new Promise((res) => { const t = setInterval(() => { if (replies.length) { clearInterval(t); res(); } }, 5); });
ch.port1.postMessage('again');
await new Promise((res) => { const t = setInterval(() => { if (replies.length > 1) { clearInterval(t); res(); } }, 5); });
r.port = replies;
return JSON.stringify(r);
`)
	want := `{"echo":{"echo":[1,{"a":"b"}],"origin":true,"ports":0},"clients":{"n":1,"same":true,"url":"` + srv.URL + `/","type":"window"},"get":{"found":true,"url":"` + srv.URL + `/"},"rich":[true,0,true,2,"123"],"port":[{"via":"port","got":7},{"second":"again"}]}`
	expect(t, tab, got, want)
}

func TestServiceWorkerInstallFailureAndUnregister(t *testing.T) {
	srv := newSWServer(t, `self.addEventListener('install', (e) => e.waitUntil(Promise.reject(new Error('no thanks'))));`)
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, swHelpers+`
const r = {};
const reg = await navigator.serviceWorker.register('/sw.js');
const w = reg.installing;
r.final = await settled(w, ['redundant', 'activated']);
r.slots = [reg.installing, reg.waiting, reg.active].map(x => x === null).join();
r.after = (await navigator.serviceWorker.getRegistrations()).length;
return JSON.stringify(r);
`)
	expect(t, tab, got, `{"final":"redundant","slots":"true,true,true","after":0}`)
	if infos := e.DefaultContext().ServiceWorkers(); len(infos) != 0 {
		t.Fatalf("a registration whose first install failed must go away: %+v", infos)
	}
	// a script with a syntax error fails the same way
	srv.setScript(`this is not javascript`)
	got = js(t, tab, swHelpers+`
const reg = await navigator.serviceWorker.register('/sw.js');
return await settled(reg.installing, ['redundant', 'activated']);
`)
	expect(t, tab, got, `redundant`)

	// unregister: the registration is gone for new lookups, the page keeps its worker until it goes away
	srv.setScript(basicSW)
	srv.setExtra("/asset.js", "x")
	got = js(t, tab, swHelpers+`
const reg = await regReady('/sw.js');
const r = {};
r.hello1 = await (await fetch('/api/hello')).text();
r.unregistered = await reg.unregister();
r.again = await reg.unregister();
r.lookup = (await navigator.serviceWorker.getRegistration()) === undefined;
r.hello2 = await (await fetch('/api/hello')).text();
try { await reg.update(); r.update = 'ok'; } catch (e) { r.update = e.name; }
return JSON.stringify(r);
`)
	expect(t, tab, got, `{"hello1":"hello from sw","unregistered":true,"again":false,"lookup":true,"hello2":"hello from sw","update":"InvalidStateError"}`)
	// a fresh navigation is no longer controlled
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	got = js(t, tab, `return JSON.stringify([!!navigator.serviceWorker.controller, (await (await fetch('/api/hello')).status)]);`)
	expect(t, tab, got, `[false,404]`)
}

func TestServiceWorkerSkipsWhenNotResponding(t *testing.T) {
	// no respondWith: the request goes to the network; a handler that throws or rejects is a network error
	srv := newSWServer(t, `
self.addEventListener('activate', (e) => e.waitUntil(clients.claim()));
self.addEventListener('fetch', (e) => {
  const p = new URL(e.request.url).pathname;
  if (p === '/boom') e.respondWith(Promise.reject(new Error('boom')));
  else if (p === '/notresponse') e.respondWith(Promise.resolve('a string'));
  else if (p === '/proxy') e.respondWith(fetch('/real').then((r) => r.text()).then((t) => new Response('proxied:' + t, { status: 203, statusText: 'Proxy', headers: { 'content-type': 'text/x-p' } })));
  else if (p === '/late') setTimeout(() => { try { e.respondWith(new Response('late')); } catch (err) { console.log('late respondWith: ' + err.name); } }, 10);
  else if (p === '/twice') { e.respondWith(new Response('first')); try { e.respondWith(new Response('second')); } catch (err) { console.log('twice: ' + err.name); } }
});
`)
	srv.setExtra("/real", "network-body")
	srv.setExtra("/late", "late-from-network")
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, swHelpers+`
await regReady('/sw.js');
const r = {};
const f = async (label, path) => { try { const x = await fetch(path); r[label] = x.status + ':' + (await x.text()); } catch (e) { r[label] = e.name; } };
await f('real', '/real');
await f('boom', '/boom');
await f('notresponse', '/notresponse');
await f('proxy', '/proxy');
await f('late', '/late');
await f('twice', '/twice');
const p = await fetch('/proxy');
r.proxyMeta = [p.status, p.statusText, p.headers.get('content-type')];
return JSON.stringify(r);
`)
	want := `{"real":"200:network-body","boom":"TypeError","notresponse":"TypeError","proxy":"203:proxied:network-body","late":"200:late-from-network","twice":"200:first","proxyMeta":[203,"Proxy","text/x-p"]}`
	expect(t, tab, got, want)
	var joined string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		var logs []string
		for _, ev := range e.DefaultContext().ServiceWorkerConsole(srv.URL + "/") {
			if m, ok := ev.Data.(map[string]string); ok {
				logs = append(logs, m["text"])
			}
		}
		joined = strings.Join(logs, "|")
		if strings.Contains(joined, "late respondWith: InvalidStateError") && strings.Contains(joined, "twice: InvalidStateError") {
			return
		}
	}
	t.Fatalf("worker console: %s", joined)
}

func TestServiceWorkerPrivateContextAndClearSiteData(t *testing.T) {
	srv := newSWServer(t, basicSW)
	srv.setExtra("/asset.js", "x")
	dir := t.TempDir()
	o := DefaultOptions()
	o.StorageDir = dir
	e := New(o)
	defer e.Close()
	// a context without storage directory keeps its registration in memory only
	priv := e.NewContext("private", ContextOptions{})
	ptab, err := priv.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	js(t, ptab, swHelpers+`await regReady('/sw.js'); return 1;`)
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("private context wrote to disk: %v", ents)
	}
	// ... and the default context does not see it
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, `return JSON.stringify([(await navigator.serviceWorker.getRegistrations()).length, !!navigator.serviceWorker.controller]);`)
	expect(t, tab, got, `[0,false]`)
	// the default context persists, and clearing site data unregisters and deletes
	js(t, tab, swHelpers+`await regReady('/sw.js'); return 1;`)
	if f, _ := filepath.Glob(filepath.Join(dir, "*", "sw", "regs.json")); len(f) != 1 {
		t.Fatalf("registration not on disk: %v", f)
	}
	e.DefaultContext().ClearSiteData("")
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("site data left after clearing: %v", ents)
	}
	if infos := e.DefaultContext().ServiceWorkers(); len(infos) != 0 {
		t.Fatalf("registrations after clearing: %+v", infos)
	}
	// the other context is untouched
	if infos := priv.ServiceWorkers(); len(infos) != 1 {
		t.Fatalf("private registrations: %+v", infos)
	}
}

func TestServiceWorkerImportScriptsAreStored(t *testing.T) {
	srv := newSWServer(t, `
importScripts('/dep.js');
self.addEventListener('activate', (e) => e.waitUntil(clients.claim()));
self.addEventListener('fetch', (e) => { if (new URL(e.request.url).pathname === '/dep') e.respondWith(new Response(DEP)); });
self.addEventListener('message', (e) => { try { importScripts('/other.js'); e.source.postMessage('imported'); } catch (err) { e.source.postMessage(err.name); } });
`)
	srv.setExtra("/dep.js", "var DEP = 'dep v1';")
	srv.setExtra("/other.js", "var OTHER = 1;")
	dir := t.TempDir()
	mk := func() *Engine { o := DefaultOptions(); o.StorageDir = dir; return New(o) }
	e1 := mk()
	tab := openTab(t, e1, srv.URL+"/")
	got := js(t, tab, swHelpers+`
const reg = await regReady('/sw.js');
return await (await fetch('/dep')).text();
`)
	expect(t, tab, got, `dep v1`)
	// importing something new after installation is refused
	got = js(t, tab, `
const reg = await navigator.serviceWorker.ready;
const msg = new Promise((res) => navigator.serviceWorker.addEventListener('message', (e) => res(e.data)));
reg.active.postMessage('go');
return await msg;
`)
	expect(t, tab, got, `NetworkError`)
	e1.Close()
	srv.setExtra("/dep.js", "var DEP = 'dep v2';")
	srv.Close()
	e2 := mk()
	defer e2.Close()
	tab2 := openTab(t, e2, "about:blank")
	tab2.SetDocument(`<html><body><script>window.x = 1</script></body></html>`, srv.URL+"/")
	got = js(t, tab2, `return await (await fetch('/dep')).text();`)
	expect(t, tab2, got, `dep v1`)
}
