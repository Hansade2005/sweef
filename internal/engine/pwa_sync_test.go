package engine

import (
	"strings"
	"testing"
	"time"
)

// syncWorkerJS logs every sync and periodicsync event into a cache (so the log survives restarts and needs no open
// page) and fails the sync events its page asks it to.
const syncWorkerJS = `
let failLeft = 0, hold = null;
self.addEventListener('activate', (e) => e.waitUntil(clients.claim()));
const log = async (m) => { const c = await caches.open('log'); await c.put('/log/' + Date.now() + '-' + Math.random(), new Response(JSON.stringify(m))); };
self.addEventListener('message', (e) => {
  if (typeof e.data !== 'string') return;
  if (e.data.startsWith('fail:')) failLeft = +e.data.slice(5);
  if (e.data === 'release' && hold) hold();
});
self.addEventListener('sync', (e) => {
  e.waitUntil((async () => {
    await log({ type: 'sync', tag: e.tag, last: e.lastChance, isEvent: e instanceof SyncEvent });
    if (e.tag === 'slow') await new Promise((r) => { hold = r; });
    if (failLeft > 0) { failLeft--; throw new Error('not yet'); }
  })());
});
self.addEventListener('periodicsync', (e) => { e.waitUntil(log({ type: 'periodic', tag: e.tag, isEvent: e instanceof PeriodicSyncEvent })); });
`

const syncHelpers = swHelpers + `
const readLog = async () => { const c = await caches.open('log'); const out = []; for (const k of await c.keys()) out.push(await (await c.match(k)).json()); return out.map((m) => m.type + ':' + m.tag + (m.last === undefined ? '' : ':' + m.last)).join(','); };
const tags = async () => (await (await navigator.serviceWorker.ready).sync.getTags()).join(',');
`

func logIs(t *testing.T, tab *Tab, want string) {
	t.Helper()
	waitUntil(t, "log "+want, func() bool { return js(t, tab, syncHelpers+`return await readLog();`) == want })
}

// logIsSet is logIs for events that fire concurrently, so their order is not defined.
func logIsSet(t *testing.T, tab *Tab, want string) {
	t.Helper()
	waitUntil(t, "log "+want, func() bool {
		return js(t, tab, syncHelpers+`return (await readLog()).split(',').sort().join(',');`) == want
	})
}

func logStays(t *testing.T, tab *Tab, want string) {
	t.Helper()
	time.Sleep(150 * time.Millisecond)
	if got := js(t, tab, syncHelpers+`return await readLog();`); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestBackgroundSyncRetriesWithBackoffAndLastChance(t *testing.T) {
	srv := newSWServer(t, syncWorkerJS)
	p := newPWAEnv(t, "", nil)
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, syncHelpers+`const reg = await regReady('/sw.js'); reg.active.postMessage('fail:99'); await sleep(50); await reg.sync.register('upload'); return 1;`)

	// attempt 1 fires at once; the registration stays while it is retried
	logIs(t, tab, "sync:upload:false")
	if got := js(t, tab, syncHelpers+`return await tags();`); got != "upload" {
		t.Fatalf("tags after a failure = %q", got)
	}
	p.clock.Advance(5*time.Minute - time.Second)
	logStays(t, tab, "sync:upload:false")
	p.clock.Advance(time.Second) // 5 minutes after the first failure
	logIs(t, tab, "sync:upload:false,sync:upload:false")
	p.clock.Advance(15*time.Minute - time.Second) // the wait grows by a factor of three
	logStays(t, tab, "sync:upload:false,sync:upload:false")
	p.clock.Advance(time.Second)
	logIs(t, tab, "sync:upload:false,sync:upload:false,sync:upload:true") // the third attempt is the last chance
	waitUntil(t, "the registration to be dropped", func() bool { return js(t, tab, syncHelpers+`return await tags();`) == "" })
	p.clock.Advance(24 * time.Hour)
	logStays(t, tab, "sync:upload:false,sync:upload:false,sync:upload:true")
}

func TestBackgroundSyncSuccessDuplicatesAndReregistration(t *testing.T) {
	srv := newSWServer(t, syncWorkerJS)
	p := newPWAEnv(t, "", nil)
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, syncHelpers+`const reg = await regReady('/sw.js'); reg.active.postMessage('fail:1'); await sleep(50); await reg.sync.register('once'); return 1;`)
	logIs(t, tab, "sync:once:false")
	p.clock.Advance(5 * time.Minute)
	logIs(t, tab, "sync:once:false,sync:once:false")
	waitUntil(t, "success removes the registration", func() bool { return js(t, tab, syncHelpers+`return await tags();`) == "" })

	// a tag registered again while its event runs fires once more afterwards (and not twice)
	js(t, tab, `const reg = await navigator.serviceWorker.ready; await reg.sync.register('slow'); return 1;`)
	logIs(t, tab, "sync:once:false,sync:once:false,sync:slow:false")
	js(t, tab, `const reg = await navigator.serviceWorker.ready; await reg.sync.register('slow'); await reg.sync.register('slow'); reg.active.postMessage('release'); return 1;`)
	logIs(t, tab, "sync:once:false,sync:once:false,sync:slow:false,sync:slow:false")
	js(t, tab, `const reg = await navigator.serviceWorker.ready; reg.active.postMessage('release'); return 1;`)
	waitUntil(t, "the slow tag to finish", func() bool { return js(t, tab, syncHelpers+`return await tags();`) == "" })
	logStays(t, tab, "sync:once:false,sync:once:false,sync:slow:false,sync:slow:false")
}

func TestBackgroundSyncWaitsForConnectivityAndSurvivesRestart(t *testing.T) {
	srv := newSWServer(t, syncWorkerJS)
	dir := t.TempDir()
	p := newPWAEnv(t, dir, nil)
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, syncHelpers+`await regReady('/sw.js'); return 1;`)
	p.offline.Store(true)
	p.ctx.PWA().SetOnline(false)
	got := js(t, tab, syncHelpers+`const reg = await navigator.serviceWorker.ready; await reg.sync.register('later'); await reg.sync.register('later'); await reg.sync.register('other'); return await tags();`)
	expect(t, tab, got, "later,other")
	logStays(t, tab, "")

	// the browser restarts while still offline: the registrations are on disk
	p.e.Close()
	p2 := newPWAEnvPre(t, dir, nil, func(p *pwaEnv) { p.offline.Store(true); p.clock.now = p.clock.Now().Add(time.Minute) })
	tab2 := openTab(t, p2.e, srv.URL+"/")
	if got := js(t, tab2, syncHelpers+`const regs = await navigator.serviceWorker.getRegistrations(); return (await regs[0].sync.getTags()).join(',');`); got != "later,other" {
		t.Fatalf("after restart: %q", got)
	}
	logStays(t, tab2, "")
	p2.offline.Store(false)
	p2.ctx.PWA().SetOnline(true)
	logIsSet(t, tab2, "sync:later:false,sync:other:false")
}

func TestBackgroundSyncNeedsAnActiveWorkerAndAConfiguredContext(t *testing.T) {
	srv := newSWServer(t, syncWorkerJS)
	p := newPWAEnv(t, "", nil)
	tab := openTab(t, p.e, srv.URL+"/")
	priv := p.e.NewContext("private", ContextOptions{})
	pt, err := priv.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	got := js(t, pt, syncHelpers+`const reg = await regReady('/sw.js'); try { await reg.sync.register('x'); return 'ok'; } catch (e) { return e.name; }`)
	expect(t, pt, got, "NotAllowedError")
	// a registration whose worker never finishes installing has no active worker
	stuck := newSWServer(t, `self.addEventListener('install', (e) => e.waitUntil(new Promise(() => {})));`)
	st := openTab(t, p.e, stuck.URL+"/")
	got = js(t, st, `
const r = {};
const reg = await navigator.serviceWorker.register('/sw.js');
r.hasManagers = typeof reg.sync + ',' + typeof reg.periodicSync + ',' + typeof SyncManager + ',' + typeof PeriodicSyncManager + ',' + typeof SyncEvent;
for (const [k, f] of [['register', () => reg.sync.register('x')], ['tags', () => reg.sync.getTags()], ['periodic', () => reg.periodicSync.getTags()], ['noarg', () => reg.sync.register()]]) { try { await f(); r[k] = 'ok'; } catch (e) { r[k] = e.name; } }
return JSON.stringify(r);`)
	expect(t, st, got, `{"hasManagers":"object,object,function,function,function","register":"InvalidStateError","tags":"InvalidStateError","periodic":"InvalidStateError","noarg":"TypeError"}`)
	_ = tab
}

func TestPeriodicSyncPermissionFloorAndTimers(t *testing.T) {
	srv := newSWServer(t, syncWorkerJS)
	p := newPWAEnv(t, "", nil)
	origin := originOf(srv.URL)
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, syncHelpers+`await regReady('/sw.js'); return 1;`)
	reg := `const reg = await navigator.serviceWorker.ready;`

	// the default is denied: the site has not earned background activity
	got := js(t, tab, reg+`try { await reg.periodicSync.register('news', { minInterval: 1000 }); return 'ok'; } catch (e) { return e.name; }`)
	expect(t, tab, got, "NotAllowedError")
	if got := js(t, tab, `return (await (await navigator.permissions.query({ name: 'periodic-background-sync' })).state);`); got != "denied" {
		t.Fatalf("permission state = %s", got)
	}

	p.set(origin, "periodic-background-sync", "granted")
	got = js(t, tab, reg+`await reg.periodicSync.register('news', { minInterval: 60 * 1000 }); await reg.periodicSync.register('digest', { minInterval: 48 * 3600 * 1000 }); return (await reg.periodicSync.getTags()).sort().join(',');`)
	expect(t, tab, got, "digest,news")
	// minInterval is a request: it never fires more often than the platform floor of twelve hours
	p.clock.Advance(11*time.Hour + 59*time.Minute)
	logStays(t, tab, "")
	p.clock.Advance(time.Minute)
	logIs(t, tab, "periodic:news")
	p.clock.Advance(12 * time.Hour)
	logIs(t, tab, "periodic:news,periodic:news")
	p.clock.Advance(12 * time.Hour)
	logIs(t, tab, "periodic:news,periodic:news,periodic:news")
	p.clock.Advance(12 * time.Hour) // 48 hours since registration: the digest is due, and the news again
	waitUntil(t, "digest", func() bool {
		return strings.Contains(js(t, tab, syncHelpers+`return await readLog();`), "periodic:digest")
	})

	// revoked permission: nothing fires, and the registration waits for the permission to come back
	js(t, tab, reg+`await reg.periodicSync.unregister('digest'); return 1;`)
	if got := js(t, tab, reg+`return (await reg.periodicSync.getTags()).join(',');`); got != "news" {
		t.Fatalf("tags after unregister = %q", got)
	}
	n := js(t, tab, syncHelpers+`return (await readLog()).split(',').length;`)
	p.set(origin, "periodic-background-sync", "prompt")
	p.clock.Advance(12 * time.Hour)
	time.Sleep(150 * time.Millisecond)
	if got := js(t, tab, syncHelpers+`return (await readLog()).split(',').length;`); got != n {
		t.Fatalf("a site without the permission got an event (%s -> %s)", n, got)
	}
}

func TestPeriodicSyncWaitsForConnectivityAndResumesAfterRestart(t *testing.T) {
	srv := newSWServer(t, syncWorkerJS)
	dir := t.TempDir()
	tweak := func(o *PWAOptions) { o.Sync.PeriodicFloor = time.Hour }
	p := newPWAEnv(t, dir, tweak)
	origin := originOf(srv.URL)
	p.set(origin, "periodic-background-sync", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, syncHelpers+`const reg = await regReady('/sw.js'); await reg.periodicSync.register('feed', { minInterval: 2 * 3600 * 1000 }); return 1;`)
	p.clock.Advance(2 * time.Hour) // the registration's own interval is longer than the floor
	logIs(t, tab, "periodic:feed")

	// offline when due: it waits and fires when the connection is back
	p.offline.Store(true)
	p.ctx.PWA().SetOnline(false)
	p.clock.Advance(3 * time.Hour)
	time.Sleep(150 * time.Millisecond)
	logStays(t, tab, "periodic:feed")
	p.offline.Store(false)
	p.ctx.PWA().SetOnline(true)
	logIs(t, tab, "periodic:feed,periodic:feed")

	// after a restart the schedule continues from the last event
	p.e.Close()
	last := p.clock.Now()
	p2 := newPWAEnvPre(t, dir, tweak, func(p *pwaEnv) {
		p.clock.now = last
		p.set(origin, "periodic-background-sync", "granted")
	})
	tab2 := openTab(t, p2.e, srv.URL+"/")
	if got := js(t, tab2, `const regs = await navigator.serviceWorker.getRegistrations(); return (await regs[0].periodicSync.getTags()).join(',');`); got != "feed" {
		t.Fatalf("after restart: %q", got)
	}
	p2.clock.Advance(3 * time.Hour)
	logIs(t, tab2, "periodic:feed,periodic:feed,periodic:feed")
}
