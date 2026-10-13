# Origin storage: IndexedDB, Cache Storage, StorageManager

What a page (and a service worker) can keep between visits, where it lives, and what is not done. Service workers have
their own page: [service-workers.md](service-workers.md).

Code: `internal/sitestore` (the data: key encoding, the ordered store, records, indexes, transaction scheduling, quota and eviction; no JavaScript),
`internal/engine/storage_host.go`, `storage_cache.go` (host functions, per-context managers),
`idb_prelude.js`, `cache_prelude.js` (the web API). Tests: `internal/sitestore/*_test.go`,
`internal/engine/storage_*_test.go`, `internal/app/sitestorage_test.go`.

## Supported surface

**IndexedDB** (pages, dedicated and shared workers, service workers)

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
`getDirectory()` returns the Origin Private File System (`platformjs/opfs.js` on top of `sitestore.OPFS`); `persist()` asks the application's policy (below).

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
throw-away agent contexts keep everything in memory (the same ordered store on in-memory storage) and leave no file
behind (tests: `TestIDBEphemeralContextsStayOffDisk`, `TestSiteStorageOfPrivateWindowStaysInMemory`).

```
<profile>/storage/
  <sha256(origin)[:12], hex>/
    meta.json         {"origin", "persisted", "lastAccess", "idb", "caches", "opfs"}  (the numbers are for sites not opened yet)
    kv/               the origin's ordered store (goleveldb: journal, tables, manifest)
    kv.bak/           only after a store in an unknown or unrecoverable format was set aside
    blobs/<id>        one file per large value, cache body or OPFS file (above 64 KiB)
    sw/regs.json, sw/scripts/*.js  service worker registrations, see service-workers.md
```

### The ordered store

One key-value store per origin (why goleveldb: [ADR 0002](adr/0002-ordered-kv-store-for-site-storage.md), with the
measurements). Keys are byte strings compared with `memcmp`; the layout is Chromium's, adapted (full table in
`internal/sitestore/origin.go`):

| key | value |
|---|---|
| `db registry: name` | the schema of a database: version, stores (name, key path, flags, physical id), indexes |
| `data: db, store, 0, primary key` | a record: `[version tag][flags][value]`, or a reference to a blob file |
| `data: db, store, 1, primary key` | the index keys that record contributed (to delete them when it is replaced or removed) |
| `data: db, store, index, index key, primary key` | an index entry (empty): entries sort by (index key, primary key) |
| `data: db, store, FFFFFFFE` | the store's state: key generator, record count, bytes |
| `cache entry / body / url`, `OPFS node / child` | Cache Storage and the file system tree |

**Keys.** The script side sends keys as strings whose byte order is the IndexedDB order. `keys.go` converts them to a
compact form (a number is 9 bytes, not 17) that is self-delimiting, so `index key || primary key` sorts correctly and a
key range is a byte range: number < date < string (by UTF-16 code unit) < binary < array (element by element, a prefix
first); `-0` is `0`; `NaN` and invalid dates are not keys. A reference comparator (`CompareKeys`) is the oracle for
property tests over random keys (signed zeros, infinities, denormals, NUL bytes, surrogate halves, nested arrays) and a
fuzz target; a second, independent transcription of the script's encoder checks the wire form.

**Cursors** are not objects with state: a step carries its position, seeks to it in the key range of the store or index
and moves at most `advance` items, through a merged iterator over the transaction's snapshot and its own writes. A cursor
over any number of records holds what a cursor over three does (test: a million records, heap +4 MiB while walking them
all). `count()` of a whole store is a counter; `getAll` and friends are bounded by their `count`; creating an index reads the
existing records a page at a time.

**Transactions.** Overlapping transactions are scheduled as before (readonly ones overlap, a writer is alone with its
scope). A transaction reads from a snapshot taken at its first read and buffers its writes in an ordered overlay it reads
through; commit turns the overlay, the changed schema, key generators and counts into one atomic batch; abort drops the
overlay. Uncommitted writes are held in memory up to 256 MiB per transaction, then the transaction fails with
`QuotaExceededError`. `clear()`, deleting an object store, an index or a database does not touch the data: the store
gets a new physical id in the same batch and the old range is queued as garbage that a background task deletes in chunks
(and resumes after a restart), so these are atomic and O(1) for any size.

**Large values.** A value, cache body or OPFS file above 64 KiB is a file `blobs/<id>`, written to a temporary name, synced
and renamed before the batch that refers to it commits; the batch also registers it. Replacing or deleting the record
unregisters it in the same batch and removes the file afterwards. A crash can only leave files nobody refers to; opening
a store lists `blobs/` and removes every file without a registry entry (one lookup per file, no scan of the data). Cache
bodies are streamed from their file (`CacheStore.MatchStream`); a value crosses the host boundary into JavaScript as one
string, which is the limit on what a single record can cost in memory while a script reads it.

**Memory.** Per open store: a 4 MiB memtable, a 6 MiB block cache, a few table readers. A store is opened on first use,
closed 20 s after its last user and at most 12 are open at once (least recently released closed first). Nothing in
memory grows with the size of a database.

**Durability and crashes.** A commit is one batch appended to the store's journal with `write(2)` (checksummed): it
survives a crash of the process. A version change, a `durability: "strict"` transaction and closing a store `fsync`. After
a power loss the store opens with a prefix of the commits; a transaction is whole or absent. Tests: a writer process
killed with SIGKILL eight times against one directory (every acknowledged transaction present, none partial, blob files
exactly the surviving blob values) and 25 simulated power losses through a fault-injecting file layer that drops
unsynced bytes and keeps random torn prefixes (synchronous commits always survive).

**Format safety.** Every store starts with a format mark and every value carries a version tag. A directory in a format
this build does not know, or that cannot be recovered, is moved aside as `kv.bak` and a new store starts empty; a store
that is merely locked by another process or unreadable for an I/O reason is left alone. The log format that preceded this
one never shipped and is not read; its directories are deleted when found.

**Cache Storage.** Entry metadata (request, status, headers, sizes) is in the store; bodies are values or blobs. `put`
over an existing request keeps the entry's position; entries are found by URL through an index, with or without the query.

## Quota, eviction and persistence

Modelled on Chromium's quota manager (`internal/sitestore/policy.go`).

* **Global budget:** 60% of what is available on the profile's volume (its free space plus what the browser already uses,
  so the budget does not shrink as it fills), at most 64 GiB (`Options.BudgetCap`). `engine.Options.StorageBudgetMiB`
  replaces it. A manager without a directory (private windows) has a 1 GiB budget.
* **Per origin:** at most 20% of the budget (`engine.Options.StorageQuotaMiB` replaces it).
* **Accounting** is exact bookkeeping, not a directory scan: records, keys, index entries, blobs, cache metadata and
  bodies, OPFS nodes and files, service worker scripts. The numbers are part of the data (see the key table: a store's
  state sits at the end of its own range), so after a crash they are those of the data that survived. They are
  recomputed from those records when a store opens (test: `TestUsageIsExactAfterARestart`).
* **Writes over the cap** fail with `QuotaExceededError`: a transaction at commit (nothing of it is written), `cache.put` /
  `add`, an OPFS write. A write that fits the origin's cap but not the browser's budget first evicts others.
* **Eviction:** under global pressure whole best-effort origins are deleted, least recently used first (last access is
  recorded when a database opens, a transaction commits, a cache or the file system is used). The origin that is writing
  is spared; persistent origins are never evicted; if nothing can be evicted the write fails. Open connections of an
  evicted origin get `close` (`IDBDatabase.onclose`, their transactions abort with `AbortError`), requests waiting to open
  fail, all its data goes in one step and its service worker registrations are unregistered. The evicted origin can
  start again at once.
* **`navigator.storage.estimate()`** reports the real usage (`usageDetails.indexedDB`, `caches`, `fileSystem`,
  `serviceWorkerRegistrations`) and the quota: the origin's cap, or its use plus what is left of the budget when that is
  less.
* **`navigator.storage.persist()`** asks `Manager.SetPersistPolicy`. The engine grants nothing by itself; the app grants it
  for a site with a bookmark and otherwise asks the prompt an embedder installs (`App.SetStoragePersistPrompt`); with
  none, the answer is no. Memory-only contexts never grant it. The flag is kept in `meta.json` and cleared with the site.
* **For the application:** `Manager.Sites()` (usage, persistence and last access per origin), `Manager.Budget()`,
  `Manager.ClearOrigin(origin)`, `Origin.SetPersisted`. Settings > Data shows the list with a "Clear" button per site
  (`state.data.storage`, `data.storageClear`, see gui-contract.md).

## Privacy

* Keyed by origin (scheme, host, port) inside one `BrowserContext` in a first-party context, and by (origin, top-level
  site) when the document or worker is embedded under another site; the directory name is the hash of that key. The
  policy for every API is in [storage-partitioning.md](storage-partitioning.md).
* Private windows use a separate context without a directory (above). Closing it discards everything.
* "Clear browsing data" (`App.clearBrowsingData`) calls `BrowserContext.ClearSiteData("")` on the profile's context:
  open connections get `close` and are closed, then every origin's databases, caches, service worker registrations and
  their files are deleted. `ClearSiteData(origin)` does it for one origin and for the partitions that mention it.

## Limits and what is not done

* The Origin Private File System has a fixed quota shared with the rest of the origin's storage (see above).
* A value crosses the host boundary as one string, and a 1 MiB `ArrayBuffer` takes roughly 0.3 s to turn back into bytes
  because QuickJS runs the loop without a JIT. Large values are not kept in the store, but a script that reads one holds it.
* A transaction's uncommitted writes are memory (cap 256 MiB, then `QuotaExceededError`). Deleting a key range one record at
  a time (not the whole store) costs the keys it deletes.
* Key paths are evaluated against the value you pass, not against a clone of it (the same unless a getter or the
  prototype chain is involved). Index keys that are not valid keys are skipped as the specification says.
* `IDBObjectStore.getAllRecords` and the `getAll({query, count, direction})` dictionary form are not implemented.
  `durability: "relaxed"` and `"default"` both mean "journal, no `fsync`".
* Cache `Vary` is ignored, `Cache.keys()` returns `Request`s rebuilt from the stored URL, method and request headers
  (no body, mode or credentials). Responses are buffered on the way in (the JavaScript side hands over the whole body); a
  large one is streamed out of its file by the Go API, but the script sees it whole. No opaque (`no-cors` cross-origin)
  responses in a cache.
* The budget follows the volume when it is asked (at most once a second); a disk that fills up from outside shrinks the
  budget for the next write, it does not trigger eviction by itself.
* goleveldb keeps one janitor goroutine alive for up to a second after a store closes.

## Measured

Debian VM, 4 vCPU x86_64 shared with other work, Go 1.26, QuickJS without JIT, ext4 on a virtual disk, one run each, so
read them as orders of magnitude. Reproduce the Go-side numbers with
`SWEEF_STORAGE_BENCH=1 go test -run TestStorageNumbers -v -timeout 30m ./internal/sitestore` (values are random,
incompressible text; quota is lifted for the run) and the JavaScript-side ones with
`go test -run Throughput -v ./internal/engine`. The engine comparison is in ADR 0002 and `tools/kvbench`.

**The Go side (no JavaScript), page cache warm**

| | 10 000 records | 1 000 000 records |
|---|---|---|
| puts, 100 B values, 1 000 per transaction | 231k/s | 186k/s |
| one `put` per transaction (journal append, no `fsync`) | 31k transactions/s | 25k transactions/s |
| 10 000 puts with one index | 122k/s | |
| random point reads, p50 / p99 | 1.2 us / 3 us (627k/s) | 7.7 us / 44 us (89k/s) |
| cursor steps (one request per step) | 947k/s | 343k/s |

**A database of about 1 GB** (1,048,576 records of 1 KiB, 1,059 MiB on disk; every phase a separate process; the page cache was not
dropped, so reads are as warm as the OS keeps them)

| | |
|---|---|
| fill (2,000 puts per transaction; 8.8 s of the 26 s is the harness generating random values) | 26 s, 40k puts/s; peak resident set 64 MiB |
| open and first read | 44 ms; resident set 16 MiB |
| 200,000 random reads | 33k/s, p50 26 us, p99 81 us; resident set 33 MiB (peak 42) |
| walk the whole store with a cursor | 158k steps/s; resident set 27 MiB (peak 39) |

The first version of this layer kept a usage record that every commit rewrote; the 1 GB fill took 92 s because every table it
flushed spanned the whole keyspace and each compaction rewrote everything below. Moving each hot record next to the data it
describes (see "The ordered store") made it 3.5 times faster; sequential inserts are sequential for the LSM tree again.

**Through JavaScript** (88% of the CPU time of these is the QuickJS interpreter; the Go side is a small part)

| | |
|---|---|
| IndexedDB, 10 000 `put` of `{id, g, name, tags}` with one index, one readwrite transaction | 2.0 s (5k puts/s; 1.4 s before, the old engine did no `fsync`-able work and no index maintenance on disk) |
| 10 000 `get` requests in one readonly transaction | 1.1 s |
| `getAll()` of the 10 000 records | 0.31 s |
| index cursor over 10 000 records (`continue()` per record) | 0.96 s |
| 1 000 transactions with one `put` each (`await` complete) | 0.41 s (2.5k transactions/s) |
| Cache Storage, 300 `put` of a 2 KiB text response | 0.07 s |
| 300 `match` of them | 0.10 s |
| `put` of a 1 MiB body, 10 times | 0.65 s |
| `match` + `arrayBuffer()` of a 1 MiB body, 10 times | 3.2 s (the byte-by-byte conversion in JS) |
