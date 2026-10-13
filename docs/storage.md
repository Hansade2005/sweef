# Origin storage: IndexedDB, Cache Storage, StorageManager

What a page (and a service worker) can keep between visits, where it lives, and what is not done. Service workers have
their own page: [service-workers.md](service-workers.md).

Code: `internal/sitestore` (the data: records, indexes, transaction scheduling, logs on disk; no JavaScript),
`internal/engine/storage_host.go`, `storage_cache.go` (host functions, per-context managers),
`idb_prelude.js`, `cache_prelude.js` (the web API). Tests: `internal/sitestore/*_test.go`,
`internal/engine/storage_*_test.go`, `internal/app/sitestorage_test.go`.

## Supported surface

**IndexedDB** (pages and service workers; not dedicated `Worker`s, see Limits)

* `indexedDB.open(name, version)`, `deleteDatabase`, `databases()`, `cmp`; `IDBOpenDBRequest` with `upgradeneeded`,
  `blocked`, `success`, `error`; `IDBVersionChangeEvent`. Opening a higher version fires `versionchange` at the other
  connections (also in other tabs of the same browsing context) and waits for them to close; `blocked` fires when one
  stays open. Requests for one name are served in order.
* `IDBDatabase`: `createObjectStore` (`keyPath`, `autoIncrement`), `deleteObjectStore`, `transaction(stores, mode,
  {durability})` for `readonly` / `readwrite` (`versionchange` is created by the open), `close`, `onversionchange`,
  `onclose`, `onabort`, `onerror`, `objectStoreNames`.
* `IDBTransaction`: `oncomplete` / `onabort` / `onerror`, `abort()`, `commit()`, `error`, `objectStore()`, `db`, `mode`,
  `durability` (accepted, no effect). A transaction is active while the callback of its latest request runs and while
  the microtasks it queued run (so `await` keeps it alive), is inactive after that, and commits by itself when no request
  is left. A request error that is not `preventDefault()`ed, and an exception in a handler, abort the transaction
  (`ConstraintError` -> `abort` with `tx.error`, outstanding requests fail with `AbortError`, all writes and the key
  generator roll back). `TransactionInactiveError`, `ReadOnlyError`, `InvalidStateError`, `NotFoundError`, `DataError`,
  `DataCloneError`, `QuotaExceededError` are raised where the specification raises them.
* `IDBObjectStore`: `add`, `put`, `get`, `getKey`, `getAll`, `getAllKeys`, `delete` (key or range), `clear`, `count`,
  `openCursor`, `openKeyCursor`, `index`, `createIndex`, `deleteIndex`, rename via `name`, `keyPath`, `autoIncrement`,
  `indexNames`.
* `IDBIndex`: the read methods and both cursors, `unique`, `multiEntry`, array (compound) key paths, rename.
* `IDBKeyRange`: `only`, `lowerBound`, `upperBound`, `bound`, `includes`.
* `IDBCursor` / `IDBCursorWithValue`: `next`, `nextunique`, `prev`, `prevunique`; `continue(key)`, `continuePrimaryKey`,
  `advance(n)`, `update`, `delete`; the cursor sees changes made while it iterates.
* Key order is the specification's: numbers < dates < strings (by UTF-16 code unit) < binary < arrays, element by
  element. `-0` equals `0`, `NaN` and invalid dates are `DataError`.
* Values are stored with a structured clone: primitives, `BigInt`, `NaN`/`Infinity`/`-0`, `Date`, `RegExp`, `Array`
  (holes and extra properties), plain objects (class instances lose their prototype), `Map`, `Set`, `ArrayBuffer`,
  every typed array and `DataView` (sharing one buffer stays shared), `Error` types, `Blob`, `File`, wrapper objects,
  circular and shared references. Functions, symbols, DOM nodes, `Promise`, `WeakMap`, `WeakSet` throw
  `DataCloneError`.

**StorageManager** (`navigator.storage`): `estimate()` returns the real use of the origin
(`usage`, `usageDetails.indexedDB / caches / serviceWorkerRegistrations`) and the quota; `persist()` / `persisted()`;
`getDirectory()` rejects (no Origin Private File System).

**Cache Storage** (`caches`, `Cache`): `open`, `has`, `delete`, `keys`, `match` (all caches in creation order, or
`{cacheName}`), `Cache.match`, `matchAll`, `add`, `addAll`, `put`, `delete`, `keys`, with `ignoreSearch` and
`ignoreMethod`. `Vary` is ignored when matching (`ignoreVary` is therefore always in effect). `put` rejects non-http(s)
URLs, non-GET requests, status 206, `Vary: *` and used bodies with `TypeError`; `add`/`addAll` reject on a non-2xx
answer and add nothing when any one fails. `put` over an existing entry replaces it in place. The response a match
returns is a fresh `Response` every time; its body can be read once, `clone()` keeps binary bodies intact.

`indexedDB` and `navigator.storage` exist on every http(s) origin; `caches` additionally needs a secure context
(https, or http on `localhost`, `*.localhost` or a loopback address). Documents without an http(s) origin
(`about:blank`, `data:`, `file:`) have no storage: `indexedDB.open` throws `SecurityError`, `caches` is `undefined`.

## What is kept, and where

Persistence is per `BrowserContext`. A context persists only when it has a storage directory:
`engine.Options.StorageDir` for the default context, `ContextOptions.StorageDir` for others. The app sets
`<profile>/storage` for the normal window and nothing for the private window's context, so private browsing and
throw-away agent contexts keep everything in memory and leave no file behind (tests:
`TestIDBEphemeralContextsStayOffDisk`, `TestSiteStorageOfPrivateWindowStaysInMemory`).

```
<profile>/storage/
  <sha256(origin)[:12], hex>/
    meta.json                      {"origin": "https://example.com", "persisted": false}
    idb/<sha256(db name)[:12]>.db  one append-only log per database
    caches/index.log               cache names and entry metadata, append-only log
    caches/bodies/<id>             one file per stored response body
    sw/regs.json, sw/scripts/*.js  service worker registrations, see service-workers.md
```

**Database file.** A log of records `[u32 length][u32 CRC-32C][payload]`. The first record names the database; every
following record is one committed transaction (a batch of operations: set version, create/delete/rename store or
index, put with its index keys, delete, clear, key generator value). A transaction becomes durable by appending one
record, so a crash leaves a whole transaction or none. A torn or corrupt tail is cut off on open. The records are
replayed into memory when the database is opened: a database is held in memory while the browser runs (sorted
slices; inserting into the middle of a very large store is O(n)). When the log exceeds 1 MiB plus twice the live
bytes it is rewritten as a snapshot (temporary file, `fsync`, rename).

Commits are written with `write(2)` and not `fsync`ed one by one (like the default `durability: "default"` of other
engines): they survive a crash of the browser, not a power failure in the last moments. The log is synced when the
last connection closes, on compaction and when the engine shuts down.

**Cache Storage.** The index holds metadata only; bodies are separate files that are read on a `match` and never kept in
memory. A body is written to a temporary name and renamed before the index record that refers to it is appended, so a
crash leaves at most an unreferenced file, which the next open deletes. The index is compacted when dead records
outnumber live ones.

**Quota.** One limit per origin, `Options.StorageQuotaMiB` (default 1024), counting database logs, cache bodies and
metadata, and service worker scripts. It is a fixed number, not derived from free disk space. A transaction that would
exceed it is aborted with `QuotaExceededError` (nothing of it is written); `cache.put` / `add` reject with the same
error. `estimate().usage` is the same sum, so it reflects what is on disk (the log size includes records not yet
compacted away).

**Persistence flag.** `persist()` resolves `true` for a persistent context (there is no permission prompt) and records it
in `meta.json`; in a memory-only context it resolves `false`. The flag has no effect on eviction because nothing is
evicted: data stays until the site or the person removes it.

## Privacy

* Keyed by origin (scheme, host, port) inside one `BrowserContext`. The engine does not partition cookies or
  `localStorage` by top-level site (it blocks third-party cookies instead), and storage follows the same model: there
  is no per top-level-site partition. Iframes are not loaded as documents by this engine (`contentWindow` is `null`), so a
  third party embedded in a page has no script, and therefore no storage, of its own to partition.
* Private windows use a separate context without a directory (above). Closing it discards everything.
* "Clear browsing data" (`App.clearBrowsingData`) calls `BrowserContext.ClearSiteData("")` on the profile's context:
  open connections get `close` and are closed, then every origin's databases, caches, service worker registrations and
  their files are deleted. `ClearSiteData(origin)` does it for one origin.

## Limits and what is not done

* IndexedDB and Cache Storage are not available inside dedicated `Worker`s (their VM is the minimal worker prelude);
  they are inside service workers.
* The whole database is held in memory; a database with hundreds of megabytes of values is not a use case. Large values
  cost time: a value crosses the host boundary as one string, a 1 MiB `ArrayBuffer` takes roughly 0.3 s to turn back
  into bytes because QuickJS runs the loop without a JIT.
* Key paths are evaluated against the value you pass, not against a clone of it (the same unless a getter or the
  prototype chain is involved). Index keys that are not valid keys are skipped as the specification says.
* `IDBObjectStore.getAllRecords` and the `getAll({query, count, direction})` dictionary form are not implemented;
  `durability` has no effect; there is no eviction, so `close` events on `IDBDatabase` come only from clearing site data.
* Cache `Vary` is ignored, `Cache.keys()` returns `Request`s rebuilt from the stored URL, method and request headers
  (no body, mode or credentials). Responses are buffered: no streaming bodies, no opaque (`no-cors` cross-origin)
  responses in a cache.
* Quota is fixed, not adaptive; there is no eviction under pressure.

## Measured

Debian VM, 4 vCPU x86_64, Go 1.26, QuickJS without JIT, loopback HTTP, data on the VM's disk, one run each (spread
over several runs in the second figure). The scripts are the `*Throughput` tests (`go test -run Throughput -v
./internal/engine`); 88% of the CPU time of the IndexedDB one is inside the QuickJS interpreter, the Go side is small.

| | |
|---|---|
| IndexedDB, 10 000 `put` of `{id, g, name, tags}` with one index, one readwrite transaction | 1.4 s (6.5-7.4k puts/s) |
| 10 000 `get` requests in one readonly transaction | 0.9-1.1 s (8.8-10.8k gets/s) |
| `getAll()` of the 10 000 records | 0.17-0.27 s |
| index cursor over 10 000 records (`continue()` per record) | 0.87-1.0 s (9.8-11.5k steps/s) |
| 1 000 transactions with one `put` each (`await` complete) | 0.40 s (2.5k transactions/s, each one a log append) |
| Cache Storage, 300 `put` of a 2 KiB text response | 0.14-0.16 s (about 2k/s) |
| 300 `match` of them | 0.08-0.27 s |
| `put` of a 1 MiB body, 10 times | 0.57-0.62 s (about 17 MiB/s) |
| `match` + `arrayBuffer()` of a 1 MiB body, 10 times | 2.0-3.4 s (the byte-by-byte conversion in JS; `text()`, or returning it from a service worker with `respondWith`, does not pay it) |
