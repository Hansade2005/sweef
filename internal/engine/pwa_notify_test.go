package engine

import (
	"strings"
	"testing"
	"time"
)

func TestNotificationPermissionFlowAndQuietUI(t *testing.T) {
	srv := newSWServer(t, basicSW)
	p := newPWAEnv(t, "", nil)
	tab := openTab(t, p.e, srv.URL+"/")
	origin := originOf(srv.URL)

	got := js(t, tab, `
const r = {};
r.initial = Notification.permission;
r.max = Notification.maxActions;
r.noGesture = await Notification.requestPermission();
r.after = Notification.permission;
return JSON.stringify(r);`)
	expect(t, tab, got, `{"initial":"default","max":2,"noGesture":"default","after":"default"}`)
	if p.askedN() != 0 {
		t.Fatalf("a request without a user gesture must not reach the user (asked %d times)", p.askedN())
	}

	// with a gesture the question is asked; the callback form and the promise form both answer
	tab.MarkUserActivation()
	got = js(t, tab, `
let viaCb; const viaPromise = await Notification.requestPermission((v) => { viaCb = v; });
await new Promise((r) => setTimeout(r, 20));
return JSON.stringify({ viaPromise, viaCb, now: Notification.permission, q: (await navigator.permissions.query({ name: 'notifications' })).state });`)
	expect(t, tab, got, `{"viaPromise":"granted","viaCb":"granted","now":"granted","q":"granted"}`)
	if p.askedN() != 1 {
		t.Fatalf("asked %d times", p.askedN())
	}
	if a := p.asked[0]; a.Origin != origin || a.Permission != "notifications" || !a.Gesture {
		t.Fatalf("request = %+v", a)
	}
	// once decided, asking again does not ask again
	js(t, tab, `await Notification.requestPermission(); return 1;`)
	if p.askedN() != 1 {
		t.Fatalf("asked again: %d", p.askedN())
	}
}

func TestNotificationConstructorEvents(t *testing.T) {
	srv := newSWServer(t, basicSW)
	p := newPWAEnv(t, "", nil)
	origin := originOf(srv.URL)
	tab := openTab(t, p.e, srv.URL+"/")

	// not granted: an error event, nothing is shown
	got := js(t, tab, `
const n = new Notification('nope');
const ev = await new Promise((res) => { n.onerror = () => res('error'); n.onshow = () => res('show'); });
return ev;`)
	expect(t, tab, got, "error")
	if len(p.rec.Shown()) != 0 {
		t.Fatal("shown without permission")
	}

	p.set(origin, "notifications", "granted")
	got = js(t, tab, `
const log = [];
const n = new Notification('Hello', { body: 'World', tag: 'greet', lang: 'en', dir: 'ltr', icon: '/i.png', requireInteraction: true, silent: false, data: { n: [1, 2, { x: new Date(0) }] }, vibrate: [100], timestamp: 5 });
log.push([n.title, n.body, n.tag, n.lang, n.dir, n.requireInteraction, n.silent, n.timestamp, n.icon.replace(location.origin, ''), JSON.stringify(n.vibrate), n.actions.length, n.renotify].join(','));
log.push(n.data.n[2].x instanceof Date);
window.__n = n;
await new Promise((res) => { n.onshow = res; });
log.push('shown');
return log.join('|');`)
	expect(t, tab, got, `Hello,World,greet,en,ltr,true,false,5,/i.png,[100],0,false|true|shown`)
	shown := p.rec.Shown()
	if len(shown) != 1 {
		t.Fatalf("shown = %d", len(shown))
	}
	n := shown[0]
	if n.Title != "Hello" || n.Body != "World" || n.Origin != origin || n.Tag != "greet" || !n.RequireInteraction || n.Silent || n.Lang != "en" || n.Dir != "ltr" || n.Timestamp != 5 {
		t.Fatalf("notification = %+v", n)
	}

	// the user clicks it: click, then close; the tab is focused
	js(t, tab, `window.__ev = []; __n.onclick = () => __ev.push('click'); __n.onclose = () => __ev.push('close'); return 1;`)
	p.rec.Click(n.ID)
	waitUntil(t, "click and close events", func() bool {
		return js(t, tab, `return __ev.join(',');`) == "click,close"
	})
	if len(p.focused) != 1 {
		t.Fatalf("the clicked notification's tab must be focused: %v", p.focused)
	}

	// close() from the page, and the user dismissing a notification
	js(t, tab, `
window.__ev = [];
window.__a = new Notification('A'); window.__b = new Notification('B');
for (const x of [__a, __b]) { x.onshow = () => __ev.push('show' + x.title); x.onclose = () => __ev.push('close' + x.title); }
await new Promise((r) => setTimeout(r, 100));
return 1;`)
	shown = p.waitShown(3)
	idOf := func(title string) string {
		for _, n := range shown {
			if n.Title == title {
				return n.ID
			}
		}
		t.Fatalf("no notification titled %q in %+v", title, shown)
		return ""
	}
	js(t, tab, `__a.close(); return 1;`)
	waitUntil(t, "close()", func() bool { return len(p.rec.Closed()) == 1 })
	p.rec.Dismiss(idOf("B"))
	waitUntil(t, "dismiss", func() bool { return strings.Contains(js(t, tab, `return __ev.join(',');`), "closeB") })
	if got := js(t, tab, `return __ev.filter((e) => e.startsWith('close')).sort().join(',');`); got != "closeA,closeB" {
		t.Fatalf("close events = %s", got)
	}
	if c := p.rec.Closed(); c[0] != idOf("A") {
		t.Fatalf("close() closed %v, want %s", c, idOf("A"))
	}
}

func TestNotificationOptionValidationAndTags(t *testing.T) {
	srv := newSWServer(t, basicSW)
	p := newPWAEnv(t, "", nil)
	p.set(originOf(srv.URL), "notifications", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	got := js(t, tab, `
const r = {};
const attempt = (label, f) => { try { f(); r[label] = 'ok'; } catch (e) { r[label] = e.name; } };
attempt('noargs', () => new Notification());
attempt('nonew', () => Notification('x'));
attempt('renotify', () => new Notification('x', { renotify: true }));
attempt('dir', () => new Notification('x', { dir: 'sideways' }));
attempt('actions', () => new Notification('x', { actions: [{ action: 'a', title: 'A' }] }));
attempt('silentVibrate', () => new Notification('x', { silent: true, vibrate: [1] }));
attempt('fine', () => new Notification('x', { renotify: true, tag: 't' }));
return JSON.stringify(r);`)
	expect(t, tab, got, `{"noargs":"TypeError","nonew":"TypeError","renotify":"TypeError","dir":"TypeError","actions":"TypeError","silentVibrate":"TypeError","fine":"ok"}`)

	// same tag replaces the earlier notification; without renotify the replacement is silent
	p.waitShown(1)
	js(t, tab, `new Notification('second', { tag: 't' }); await new Promise((r) => setTimeout(r, 50)); new Notification('third', { tag: 't', renotify: true }); await new Promise((r) => setTimeout(r, 50)); return 1;`)
	s := p.waitShown(3)
	if s[1].Title != "second" || !s[1].Silent || s[2].Title != "third" || s[2].Silent || !s[2].Renotify {
		t.Fatalf("replacement flags: %+v %+v", s[1], s[2])
	}
	if infos := p.ctx.PWA().Notifications(); len(infos) != 1 || infos[0].Title != "third" {
		t.Fatalf("a tag keeps one notification: %+v", infos)
	}
	// the earlier ones were closed in the sink so that platforms without tag support do not pile them up
	if len(p.rec.Closed()) != 2 {
		t.Fatalf("closed = %v", p.rec.Closed())
	}
}

func TestNotificationsDoNotDisturbAndIncognito(t *testing.T) {
	srv := newSWServer(t, basicSW)
	p := newPWAEnv(t, "", nil)
	p.set(originOf(srv.URL), "notifications", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	p.dnd.Store(true)
	got := js(t, tab, `const n = new Notification('quiet'); const ev = await new Promise((res) => { n.onshow = () => res('show'); n.onerror = () => res('error'); }); return ev;`)
	expect(t, tab, got, "show")
	if len(p.rec.Shown()) != 0 {
		t.Fatal("do not disturb must keep the notification off the screen")
	}
	if infos := p.ctx.PWA().Notifications(); len(infos) != 1 {
		t.Fatalf("the notification still exists for the API: %+v", infos)
	}
	p.dnd.Store(false)

	// a private context was never configured: everything is denied
	priv := p.e.NewContext("private", ContextOptions{})
	pt, err := priv.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	got = js(t, pt, `
const r = {};
r.perm = Notification.permission;
r.req = await Notification.requestPermission();
r.q = (await navigator.permissions.query({ name: 'notifications' })).state;
r.push = (await navigator.permissions.query({ name: 'push', userVisibleOnly: true })).state;
const n = new Notification('x'); r.ev = await new Promise((res) => { n.onerror = () => res('error'); n.onshow = () => res('show'); });
return JSON.stringify(r);`)
	expect(t, pt, got, `{"perm":"denied","req":"denied","q":"denied","push":"denied","ev":"error"}`)
}

func TestPermissionsQueryTracksChanges(t *testing.T) {
	srv := newSWServer(t, basicSW)
	p := newPWAEnv(t, "", nil)
	origin := originOf(srv.URL)
	tab := openTab(t, p.e, srv.URL+"/")
	got := js(t, tab, `
const q = async (d) => (await navigator.permissions.query(d)).state;
const r = { n: await q({ name: 'notifications' }), push: await q({ name: 'push', userVisibleOnly: true }), periodic: await q({ name: 'periodic-background-sync' }), sync: await q({ name: 'background-sync' }), geo: await q({ name: 'geolocation' }) };
for (const bad of [{ name: 'nonsense' }, {}, null]) { try { await navigator.permissions.query(bad); r['bad' + String(bad && bad.name)] = 'ok'; } catch (e) { r['bad' + String(bad && bad.name)] = e.name; } }
window.__st = await navigator.permissions.query({ name: 'notifications' });
window.__changes = [];
__st.onchange = () => __changes.push(__st.state);
return JSON.stringify(r);`)
	expect(t, tab, got, `{"n":"prompt","push":"prompt","periodic":"denied","sync":"granted","geo":"prompt","badnonsense":"TypeError","badundefined":"TypeError","badnull":"TypeError"}`)
	p.set(origin, "notifications", "granted")
	p.ctx.PWA().PermissionChanged(origin)
	waitUntil(t, "change event", func() bool { return js(t, tab, `return __changes.join(',');`) == "granted" })
	p.set(origin, "notifications", "denied")
	p.ctx.PWA().PermissionChanged("")
	waitUntil(t, "second change", func() bool { return js(t, tab, `return __changes.join(',');`) == "granted,denied" })
	if got := js(t, tab, `return Notification.permission + ',' + (await navigator.permissions.query({ name: 'push', userVisibleOnly: true })).state;`); got != "denied,denied" {
		t.Fatalf("after revoking: %s", got)
	}
}

func TestBadgingAPI(t *testing.T) {
	srv := newSWServer(t, basicSW)
	p := newPWAEnv(t, "", nil)
	origin := originOf(srv.URL)
	tab := openTab(t, p.e, srv.URL+"/")
	got := js(t, tab, `
const r = {};
await navigator.setAppBadge(5); await navigator.setAppBadge(); await navigator.setAppBadge(0); await navigator.setAppBadge(7); await navigator.clearAppBadge();
for (const bad of [-1, 1.5, NaN, 'x']) { try { await navigator.setAppBadge(bad); r[String(bad)] = 'ok'; } catch (e) { r[String(bad)] = e.name; } }
return JSON.stringify(r);`)
	expect(t, tab, got, `{"-1":"TypeError","1.5":"TypeError","NaN":"TypeError","x":"TypeError"}`)
	if log := p.badgeLog(); log != origin+" 5|"+origin+" flag|"+origin+" none|"+origin+" 7|"+origin+" none" {
		t.Fatalf("badge log = %s", log)
	}
	js(t, tab, `await navigator.setAppBadge(3); return 1;`)
	if b := p.ctx.PWA().Badge(origin); !b.Set || b.Count != 3 {
		t.Fatalf("badge = %+v", b)
	}
	if bs := p.ctx.PWA().Badges(); len(bs) != 1 {
		t.Fatalf("badges = %v", bs)
	}
	// not available in an insecure context
	ins := openTab(t, p.e, "about:blank")
	if got := js(t, ins, `return typeof navigator.setAppBadge + ',' + typeof navigator.locks;`); got != "undefined,undefined" {
		t.Fatalf("insecure context: %s", got)
	}
}

func TestOnlineStateAndEvents(t *testing.T) {
	srv := newSWServer(t, basicSW)
	p := newPWAEnv(t, "", nil)
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, `window.__ev = []; addEventListener('online', () => __ev.push('online')); addEventListener('offline', () => __ev.push('offline')); return navigator.onLine;`)
	if got := js(t, tab, `return navigator.onLine;`); got != "true" {
		t.Fatalf("onLine = %s", got)
	}
	p.offline.Store(true)
	p.ctx.PWA().SetOnline(false)
	waitUntil(t, "offline event", func() bool { return js(t, tab, `return __ev.join(',') + ':' + navigator.onLine;`) == "offline:false" })
	p.offline.Store(false)
	p.ctx.PWA().SetOnline(true)
	waitUntil(t, "online event", func() bool {
		return js(t, tab, `return __ev.join(',') + ':' + navigator.onLine;`) == "offline,online:true"
	})
}

func TestUserActivationWindow(t *testing.T) {
	e := newStorageEngine(t, "")
	tab := openTab(t, e, "about:blank")
	if tab.UserActive() {
		t.Fatal("a fresh tab has no activation")
	}
	tab.MarkUserActivation()
	if !tab.UserActive() {
		t.Fatal("activation not recorded")
	}
	tab.pw.activatedAt.Store(time.Now().Add(-6 * time.Second).UnixNano())
	if tab.UserActive() {
		t.Fatal("activation must expire after five seconds")
	}
}
