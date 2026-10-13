package engine

import (
	"strings"
	"testing"
)

const notifySW = `
self.addEventListener('activate', (e) => e.waitUntil(clients.claim()));
const tell = async (msg) => { for (const c of await clients.matchAll({ includeUncontrolled: true })) c.postMessage(msg); };
self.addEventListener('notificationclick', (e) => {
  const n = e.notification;
  e.notification.close();
  e.waitUntil((async () => {
    await tell({ kind: 'click', title: n.title, action: e.action, tag: n.tag, data: n.data, body: n.body, actions: n.actions.map((a) => a.action + ':' + a.title) });
    if (e.action === 'open') {
      const w = await clients.openWindow('/opened.html');
      await tell({ kind: 'opened', url: w ? w.url.replace(self.location.origin, '') : null, type: w && w.constructor.name });
    } else if (e.action === 'focus') {
      const all = await clients.matchAll({ type: 'window' });
      const f = await all[0].focus();
      await tell({ kind: 'focused', focused: f.focused });
    } else if (e.action === 'navigate') {
      const all = await clients.matchAll({ type: 'window' });
      const nav = await all[0].navigate('/page2.html');
      await (await caches.open('r')).put('/result', new Response(JSON.stringify({ url: nav && nav.url.replace(self.location.origin, ''), same: nav && nav.id === all[0].id })));
    }
  })());
});
self.addEventListener('notificationclose', (e) => { e.waitUntil(tell({ kind: 'close', title: e.notification.title })); });
self.addEventListener('message', (e) => {
  if (e.data === 'ctor') { let r; try { new Notification('x'); r = 'constructed'; } catch (err) { r = err.name; } e.source.postMessage({ kind: 'ctor', r, perm: Notification.permission, hasRequest: typeof Notification.requestPermission }); }
  if (e.data === 'open-without-click') {
    e.waitUntil(clients.openWindow('/nope.html').then(() => tell({ kind: 'bare-open', ok: true }), (err) => tell({ kind: 'bare-open', error: err.name })));
  }
});
`

func TestServiceWorkerNotificationsClickCloseAndWindows(t *testing.T) {
	srv := newSWServer(t, notifySW)
	srv.setExtra("/opened.html", `<!doctype html><html><head><title>opened</title></head><body>opened</body></html>`)
	srv.setExtra("/page2.html", `<!doctype html><html><head><title>two</title></head><body>two</body></html>`)
	p := newPWAEnv(t, "", nil)
	origin := originOf(srv.URL)
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, swHelpers+`await regReady('/sw.js'); window.__msgs = []; navigator.serviceWorker.addEventListener('message', (e) => __msgs.push(e.data)); return 1;`)

	// no permission: showNotification rejects like Chromium
	got := js(t, tab, `const reg = await navigator.serviceWorker.ready; try { await reg.showNotification('x'); return 'ok'; } catch (e) { return e.name + ': ' + e.message; }`)
	expect(t, tab, got, "TypeError: Failed to execute 'showNotification' on 'ServiceWorkerRegistration': No notification permission has been granted for this origin.")

	p.set(origin, "notifications", "granted")
	got = js(t, tab, `
const reg = await navigator.serviceWorker.ready;
await reg.showNotification('Inbox', { body: 'two new', tag: 'mail', data: { id: 7, when: new Date(0) }, actions: [{ action: 'open', title: 'Open' }, { action: 'focus', title: 'Focus' }, { action: 'extra', title: 'Dropped' }], requireInteraction: true, icon: '/icon.png' });
const list = await reg.getNotifications();
const tagged = await reg.getNotifications({ tag: 'mail' });
const none = await reg.getNotifications({ tag: 'other' });
const n = list[0];
return JSON.stringify({ n: list.length, tagged: tagged.length, none: none.length, title: n.title, body: n.body, tag: n.tag, data: n.data.id, date: n.data.when instanceof Date, actions: n.actions.map((a) => a.action), req: n.requireInteraction, maxActions: Notification.maxActions });`)
	expect(t, tab, got, `{"n":1,"tagged":1,"none":0,"title":"Inbox","body":"two new","tag":"mail","data":7,"date":true,"actions":["open","focus"],"req":true,"maxActions":2}`)
	shown := p.waitShown(1)
	n := shown[0]
	if n.Title != "Inbox" || len(n.Actions) != 2 || n.Actions[0].ID != "open" || n.Actions[1].Title != "Focus" || !n.RequireInteraction {
		t.Fatalf("notification = %+v", n)
	}

	// the click reaches the worker with the action and the data, and the worker may open a window
	p.rec.ClickAction(n.ID, "open")
	waitUntil(t, "opened message", func() bool { return strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `"opened"`) })
	msgs := js(t, tab, `return JSON.stringify(__msgs);`)
	if !strings.Contains(msgs, `"kind":"click","title":"Inbox","action":"open","tag":"mail","data":{"id":7`) || !strings.Contains(msgs, `"actions":["open:Open","focus:Focus"]`) {
		t.Fatalf("click message: %s", msgs)
	}
	if !strings.Contains(msgs, `"kind":"opened","url":"/opened.html","type":"WindowClient"`) {
		t.Fatalf("openWindow result: %s", msgs)
	}
	if len(p.opened) != 1 || p.opened[0] != srv.URL+"/opened.html" {
		t.Fatalf("opened windows: %v", p.opened)
	}
	if infos := p.ctx.PWA().Notifications(); len(infos) != 0 {
		t.Fatalf("a clicked notification is gone: %+v", infos)
	}
	if got := js(t, tab, `const reg = await navigator.serviceWorker.ready; return (await reg.getNotifications()).length;`); got != "0" {
		t.Fatalf("getNotifications after click = %s", got)
	}

	// focus() of a WindowClient goes through the app
	js(t, tab, `__msgs.length = 0; const reg = await navigator.serviceWorker.ready; await reg.showNotification('F', { actions: [{ action: 'focus', title: 'F' }] }); return 1;`)
	s := p.waitShown(2)
	p.rec.ClickAction(s[1].ID, "focus")
	waitUntil(t, "focused message", func() bool { return strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `"focused"`) })
	if !strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `"focused":true`) || len(p.focused) == 0 {
		t.Fatalf("focus: %v", p.focused)
	}

	// navigate() of a controlled WindowClient
	js(t, tab, `__msgs.length = 0; const reg = await navigator.serviceWorker.ready; await reg.showNotification('N', { actions: [{ action: 'navigate', title: 'N' }] }); return 1;`)
	s = p.waitShown(3)
	p.rec.ClickAction(s[2].ID, "navigate")
	waitUntil(t, "the navigation", func() bool { return tab.Title() == "two" })
	waitUntil(t, "navigate result", func() bool {
		return js(t, tab, `const r = await (await caches.open('r')).match('/result'); return r ? await r.text() : '';`) != ""
	})
	if got := js(t, tab, `return await (await (await caches.open('r')).match('/result')).text();`); !strings.Contains(got, `"url":"/page2.html"`) {
		t.Fatalf("navigate() resolved with %s", got)
	}
	if title := tab.Title(); title != "two" {
		t.Fatalf("the client's tab did not navigate: %q", title)
	}
}

func TestServiceWorkerNotificationCloseEventAndOpenWindowGate(t *testing.T) {
	srv := newSWServer(t, notifySW)
	p := newPWAEnv(t, "", nil)
	p.set(originOf(srv.URL), "notifications", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, swHelpers+`await regReady('/sw.js'); window.__msgs = []; navigator.serviceWorker.addEventListener('message', (e) => __msgs.push(e.data)); return 1;`)

	// the user dismisses it: notificationclose
	js(t, tab, `const reg = await navigator.serviceWorker.ready; await reg.showNotification('Dismissed'); return 1;`)
	s := p.waitShown(1)
	p.rec.Dismiss(s[0].ID)
	waitUntil(t, "notificationclose", func() bool {
		return strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `"kind":"close","title":"Dismissed"`)
	})

	// close() by the page or worker does not produce notificationclose
	js(t, tab, `__msgs.length = 0; const reg = await navigator.serviceWorker.ready; await reg.showNotification('Closed by script'); (await reg.getNotifications())[0].close(); await new Promise((r) => setTimeout(r, 100)); return 1;`)
	if got := js(t, tab, `return JSON.stringify(__msgs);`); got != "[]" {
		t.Fatalf("close() must not fire notificationclose: %s", got)
	}

	// the Notification constructor does not exist in a worker, but the permission does
	js(t, tab, `__msgs.length = 0; const reg = await navigator.serviceWorker.ready; reg.active.postMessage('ctor'); return 1;`)
	waitUntil(t, "ctor message", func() bool { return strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `"ctor"`) })
	if got := js(t, tab, `return JSON.stringify(__msgs);`); got != `[{"kind":"ctor","r":"TypeError","perm":"granted","hasRequest":"undefined"}]` {
		t.Fatalf("worker Notification: %s", got)
	}

	// openWindow outside a notification click is refused
	js(t, tab, `__msgs.length = 0; const reg = await navigator.serviceWorker.ready; reg.active.postMessage('open-without-click'); return 1;`)
	waitUntil(t, "bare openWindow", func() bool { return strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `bare-open`) })
	if !strings.Contains(js(t, tab, `return JSON.stringify(__msgs);`), `"error":"InvalidAccessError"`) || len(p.opened) != 0 {
		t.Fatalf("bare openWindow: %s opened=%v", js(t, tab, `return JSON.stringify(__msgs);`), p.opened)
	}
}

func TestServiceWorkerNotificationsGoAwayWithTheRegistration(t *testing.T) {
	srv := newSWServer(t, notifySW)
	p := newPWAEnv(t, "", nil)
	p.set(originOf(srv.URL), "notifications", "granted")
	tab := openTab(t, p.e, srv.URL+"/")
	js(t, tab, swHelpers+`const reg = await regReady('/sw.js'); await reg.showNotification('Soon gone'); return 1;`)
	p.waitShown(1)
	js(t, tab, `const reg = await navigator.serviceWorker.ready; await reg.unregister(); return 1;`)
	waitUntil(t, "notification closed with its registration", func() bool { return len(p.rec.Closed()) == 1 })
}
