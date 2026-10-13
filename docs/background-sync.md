# Background Sync, Periodic Sync, Badging and Web Locks

Code: `internal/engine/pwa_sync.go`, `pwa_locks.go`, `pwa.go`, `platformjs/pwa.js`. Tests: `pwa_sync_test.go`,
`pwa_locks_test.go`, `pwa_notify_test.go`.

## Background Sync

`registration.sync.register(tag)`, `getTags()`, the `sync` event (`SyncEvent.tag`, `lastChance`). Registering needs an
active worker (`InvalidStateError` otherwise) and a configured (non-private) context (`NotAllowedError`). The event fires
as soon as the browser is online. A failed event (rejected `waitUntil`, error, timeout of 5 minutes) is retried: the wait
after the first failure is 5 minutes and grows by a factor of 3 (so 5 min, then 15 min), the third attempt has
`lastChance`, and after it the registration is dropped. These are Chromium's values; they are `SyncPolicy` fields that
tests shorten, and tests drive them with an injected clock. A tag registered again while its event runs fires once more
afterwards; the same tag registered twice while pending is one registration. Registrations are stored per origin
(`pwa.json`) and resume after a restart; they are removed with the service worker registration.

Connectivity: `PWA.SetOnline(bool)` (the app has no network monitor yet, so nothing calls it by itself; tests do) and the
tab offline mode. Offline, due registrations wait and fire on reconnect; pages get `online`/`offline` events and
`navigator.onLine` follows. Nothing fires while the browser is not running.

## Periodic Background Sync

`registration.periodicSync.register(tag, {minInterval})`, `unregister`, `getTags`, the `periodicsync` event. The event
never fires more often than the larger of `minInterval` and 12 hours (`SyncPolicy.PeriodicFloor`), only while online, and
only for origins whose `periodic-background-sync` permission is granted: the app grants it to origins with a bookmark;
the default is denied and the user can override it per site (`sites.json`, key `periodic`; no UI yet). `register`
without the permission rejects `NotAllowedError`. A skipped or failed event waits a full interval. There is no engagement
score and no retry backoff for periodic events.

## Badging

`navigator.setAppBadge(n?)` (no argument is the flag badge, `0` clears), `clearAppBadge()`. State per origin, in memory
(not persisted), reported to the app (`PWAOptions.OnBadge`, `App.Badges`). Secure contexts only.

## Web Locks

`navigator.locks.request(name, [options], callback)` with `mode` `exclusive|shared`, `ifAvailable`, `steal`, `signal`;
`query()`. Locks are shared by every tab and service worker of an origin in the profile's context, queued in order as the
specification says (a shared request waits behind a queued exclusive one), released when the callback's promise settles or
the tab or worker goes away; a stolen lock's holder gets `AbortError`. Dedicated `Worker`s run in a smaller VM without
`navigator.locks`. Private windows have their own namespace.

## Out of scope

Background Fetch is not implemented (`BackgroundFetchManager` does not exist).
