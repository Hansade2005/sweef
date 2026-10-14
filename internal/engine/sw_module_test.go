package engine

import "testing"

func TestServiceWorkerModuleScript(t *testing.T) {
	srv := newSWServer(t, `
import { greet } from '/greet.js';
self.addEventListener('activate', (e) => e.waitUntil(clients.claim()));
self.addEventListener('fetch', (e) => { if (new URL(e.request.url).pathname === '/hi') e.respondWith(new Response(greet('module'))); });
`)
	srv.setExtra("/greet.js", "export function greet(n) { return 'hello ' + n; }")
	e := newStorageEngine(t, "")
	tab := openTab(t, e, srv.URL+"/")
	got := js(t, tab, swHelpers+`
await regReady('/sw.js', { type: 'module', updateViaCache: 'none' });
const reg = await navigator.serviceWorker.getRegistration();
return (await (await fetch('/hi')).text()) + ' ' + reg.updateViaCache;
`)
	expect(t, tab, got, `hello module none`)
}
