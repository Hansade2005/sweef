# Service workers

A page can register a script that sits between the document and the network: it is installed once, survives the
tab and the browser restart, and answers requests of the pages it controls. Storage the worker uses (Cache Storage,
IndexedDB) is described in [storage.md](storage.md).

Code (package `internal/engine`): `sw.go` (registrations, lifecycle, persistence), `sw_runtime.go` (the worker's VM),
`sw_host.go` (host functions, message ports), `sw_intercept.go` (request interception, documents as clients),
`sw_page_prelude.js` (`navigator.serviceWorker` and friends), `sw_scope.js` (the worker's global scope).
Tests: `sw_test.go`, `sw_lifecycle_test.go`, `sw_module_test.go`, `sw_leak_test.go`.

## Supported surface

**In a document** (secure contexts: https, or http on `localhost`, `*.localhost`, loopback addresses; elsewhere
`navigator.serviceWorker` is `undefined`)

* `navigator.serviceWorker.register(url, {scope, type, updateViaCache})`, `getRegistration(clientURL)`,
  `getRegistrations()`, `ready`, `controller`, `startMessages()`, events `controllerchange`, `message`.
* `ServiceWorkerRegistration`: `installing`, `waiting`, `active`, `scope`, `updateViaCache`, `update()`,
  `unregister()`, `onupdatefound`. `navigationPreload`, `pushManager` and `showNotification` exist only as inert
  stubs (see Not done).
* `ServiceWorker`: `scriptURL`, `state`, `postMessage(message, transfer)` (structured clone; `MessageChannel` ports can be
  transferred), `onstatechange`. State changes arrive one at a time and in order: `installing`, `installed`,
  `activating`, `activated`, `redundant`; `updatefound` fires when a new installing worker appears.
* Registration rules: the script must be http(s) and same-origin with the page and be served with a JavaScript MIME
  type (`SecurityError` otherwise) and a 2xx status (`TypeError`); the scope defaults to the script's directory and
  must not be wider than it unless the script response carries `Service-Worker-Allowed`; scope and script origin must
  match. Registering what is registered already (same scope, script URL and type) resolves with the same registration
  and does nothing. A different script URL for an existing scope replaces its worker.

**In the worker** (`ServiceWorkerGlobalScope`; module workers with `type: "module"` too)

* Events `install` (`InstallEvent`), `activate`, `fetch` (`FetchEvent`: `request`, `clientId`, `resultingClientId`,
  `respondWith()`, `waitUntil()`, `handled`), `message` (`ExtendableMessageEvent`: `data`, `origin`, `source`, `ports`),
  with `waitUntil` (promises extend the event; a rejection fails an install) and the usual `oninstall`... handlers.
* `self.skipWaiting()`, `self.clients` (`get`, `matchAll({includeUncontrolled})`, `claim()`), `Client` /
  `WindowClient` (`id`, `url`, `type`, `frameType`, `visibilityState`, `postMessage`), `self.registration`,
  `self.serviceWorker`, `importScripts()`, `caches`, `indexedDB`, `fetch`, `console`, timers, `Request` / `Response` /
  `Headers`, `URL`, `Blob`, `TextEncoder`, `crypto`, `structuredClone`. `window`, `document`, `localStorage` and the like
  are removed. A `fetch()` made by the worker goes to the network; it is never routed back to a service worker.

### Lifecycle

`register()` fetches the script (no HTTP cache), creates or updates the registration and resolves with it while the
worker is `installing`. Then, in the background: the script is evaluated in its VM and `install` is dispatched; the
worker waits for the promises given to `waitUntil` (30 s limit, failure makes it `redundant` and, if it was the first
worker of the registration, removes the registration). It becomes `waiting` and activates at once when there is no
active worker, or it called `skipWaiting()`, or no document is controlled by the old worker; otherwise it waits and
activates as soon as the last controlled document is closed or navigated away. Activation retires the old worker
(`redundant`, VM stopped), dispatches `activate` (30 s limit; `clients.claim()` works there) and announces
`activated` only after the registration is written to disk. A controlling worker change fires `controllerchange` in
the documents it controls.

`registration.update()` re-fetches the main script and installs a new worker only when its bytes differ from the newest
worker's. `unregister()` removes the registration at once; documents it controls keep their worker until they go away,
new navigations are not controlled.

### Request interception

* A navigation to a URL inside the scope of an activated registration is sent to the worker as a `fetch` event with
  `request.mode === "navigate"`, `destination "document"`. What it answers is the document.
* Everything a controlled document requests afterwards goes the same way: `fetch()`, XHR (also synchronous), scripts,
  stylesheets, images, fonts, media, including the loads the preload scanner starts before the document is committed.
  `request.destination` is `script`, `style`, `image`, `font`, `video`, `iframe` or `""` (fetch / XHR).
* If no `fetch` listener calls `respondWith`, the request goes to the network unchanged. If the promise passed to
  `respondWith` rejects, or resolves with something that is not a `Response`, or the worker does not answer within
  30 s, the request fails as a network error (`TypeError: Failed to fetch`). `respondWith` after the handler returned
  or a second call throws `InvalidStateError`.
* Controllers are decided per document: the registration with the longest scope that is a prefix of the document URL.
  `clients.claim()` takes over documents that loaded before the worker activated.

### Persistence and privacy

An activated worker is written to `<profile>/storage/<origin>/sw/` (`regs.json`, the main script and the scripts it
imported with `importScripts` while installing, named by content hash). After a restart the registration is loaded the
first time a document or navigation in its scope needs it; the worker's VM starts then, runs its script (no `install` or
`activate` again) and answers, so a page can be served from Cache Storage while the network is down
(`TestServiceWorkerServesNavigationsAndSubresourcesAndSurvivesRestart` navigates offline after a restart). Only
`importScripts` dependencies are stored; modules loaded with `import` are fetched when the worker starts, so a
module worker cannot start offline. A waiting worker is not kept across a restart.

Registrations belong to a `BrowserContext`: a private window's context has no storage directory, so its workers live
in memory until the context is closed and are invisible to the normal window (`TestServiceWorkerPrivateContext...`).
"Clear browsing data" unregisters every worker of the profile, stops the VMs and deletes the files. Workers keep running
while the browser runs: there is no idle termination, so a worker that registers a timer keeps firing it. Engine and
context shutdown stop every worker VM and wait for their goroutines (`TestServiceWorkerEngineCloseLeavesNoGoroutines`,
the app's goroutine leak test).

### How it is built

The worker's VM is an ordinary script VM bound to a hidden tab that no one can navigate or list. It therefore has the
page `fetch`, `Request`/`Response`, `caches`, `indexedDB`, timers and console; `sw_scope.js` turns the global into a
worker scope. The hidden tab has no controller, which is why the worker's own requests reach the network. One
goroutine per running worker pumps its VM; events are dispatched by queueing a call and waiting for the worker to
report that the event's promises settled. A document hands a request to the worker from `Tab.httpDo` (sub-resources)
and `Tab.fetch` (navigations), the two places the engine makes requests for a page. The worker's console is the hidden
tab's console ring (`BrowserContext.ServiceWorkerConsole`).

## Not done (honest list)

* **Push, Background Sync, Periodic Background Sync, Background Fetch, Notifications API from a worker**: none of them.
  `registration.pushManager.subscribe()` rejects, `showNotification()` rejects, `registration.sync` / `periodicSync`
  do not exist, there are no `push`, `sync`, `notificationclick` events.
* **Navigation preload**: `registration.navigationPreload` is a stub that resolves and changes nothing;
  `FetchEvent.preloadResponse` resolves `undefined`.
* **Navigations**: a redirect (3xx) answered by the worker is not followed; form `POST` navigations do not go through the
  worker; a hard reload (cache bypass) is not told apart and goes through the worker like any navigation; iframes are
  not loaded by this engine at all. Dedicated `Worker`s and
  their requests are not controlled by a worker. There is no 24-hour update check on navigation: workers update on
  `register()` / `update()` only, and only the main script is compared (changes to imported scripts alone do not
  trigger an update). `updateViaCache` is stored and reported but the script fetch always bypasses the HTTP cache.
* **`clients.openWindow()` and `WindowClient.navigate()` reject** (`NotAllowedError`); `focus()` resolves without
  doing anything; `visibilityState` is always `visible`, `focused` always `false`; only window clients exist.
* **Request fidelity**: the `Request` a fetch event sees carries the headers the page set (not the ones the engine adds
  on the wire), `credentials` is always `include`, `referrer` is empty, `signal` is never aborted, a body is a string
  (binary `FormData` / `Blob` bodies of requests are not forwarded). Responses are buffered, not streamed; opaque
  responses are not modelled, and `Set-Cookie` headers in a response the worker makes up are ignored.
* **Lifetime**: no termination of idle workers and no per-event lifetime limits other than the 30 s timeouts above;
  `waitUntil` on a `fetch` event does not extend anything because nothing is shut down. `ServiceWorker.onerror` is not
  fired for runtime errors; they go to the worker console and the worker's own `error` event.
* **`BroadcastChannel`** is the engine's existing no-op, so workers and pages cannot talk through it; use
  `postMessage` and `MessageChannel`.

## Measured

Same machine as in [storage.md](storage.md) (4 vCPU, no JIT, loopback), `go test -run 'Throughput|StartupTimes' -v
./internal/engine`:

| | |
|---|---|
| `register()` of a worker that caches 3 URLs in `install`, until `activated` | about 65 ms |
| first navigation after an engine restart, answered by the persisted worker (its VM starts; the server is down) | about 65 ms, against 60 ms for the second one |
| 300 sequential `fetch()` calls to the server, no worker | 0.66-0.74 s (2.2-2.5 ms each, almost all of it QuickJS and promise machinery) |
| the same 300 answered by the worker with a synthesized `Response` | 0.54-0.58 s |
| the same 300 answered by the worker from `caches.match` | 0.55-0.58 s |
| the same 300 with a controlled page whose worker does not respond (network) | 0.73-0.76 s (the worker round trip adds about 0.3 ms) |
