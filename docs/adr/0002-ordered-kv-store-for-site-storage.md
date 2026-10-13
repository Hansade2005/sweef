# 0002. An ordered key-value store (goleveldb) under IndexedDB, Cache Storage and OPFS

* Status: Accepted
* Date: 2026-10-13
* Deciders: @Hansade2005

## Context
IndexedDB and Cache Storage were backed by one append-only record log per database that was replayed into memory when
the database was opened: sorted slices, O(n) inserts into the middle of a store, the whole database resident for as long
as the browser ran. The Origin Private File System was a map in memory. A page that stores a few hundred megabytes (an
offline mail client, a map tile cache, a WebAssembly file system) therefore cost that much RAM in every tab of the
browser, for as long as it ran, and cost seconds to open. The quota was a fixed 1 GiB per site and nothing was ever
evicted, so a long-lived profile could fill the disk.

What the replacement has to be, for a browser:

1. **Memory bounded, independent of database size.** Open a 1 GB database and the resident set stays in the tens of
   megabytes; a cursor over a million records costs what a cursor over three does.
2. **Crash safe.** `kill -9` or a power cut in the middle of a transaction leaves the transaction whole or absent, never
   a half; a transaction that was acknowledged survives a crash of the process.
3. **Small and dependable:** pure Go (`CGO_ENABLED=0`, the whole browser is), permissive licence (the dependency review
   workflow enforces it), few dependencies, small binary.
4. **The right shape for IndexedDB:** ordered keys, range scans in both directions, point lookups, atomic multi-key
   writes, cheap consistent snapshots for transactions.
5. **Testable:** an in-memory mode with identical behaviour for private windows (they must leave no file behind) and a
   way to inject faults deterministically.

Chromium stores IndexedDB in LevelDB: key = database id + object store id + index id + a sortable encoding of the IndexedDB
key, a version tag on every value, large values and blobs outside the store. That layout fits requirement 4 and is what we
implement natively.

## Decision
One ordered key-value store per origin, [goleveldb](https://github.com/syndtr/goleveldb) (BSD-2-Clause, an LSM tree:
journal, memtable, sorted tables, bloom filters), opened lazily and closed when idle; at most 12 origins keep one open.
IndexedDB keys get a compact memcmp-ordered encoding (`internal/sitestore/keys.go`); object stores and indexes are key
ranges in it; values above 64 KiB are files next to it. Details and the key layout are in
[docs/storage.md](../storage.md); the engine is confined to `internal/sitestore/kv.go` and the transaction overlay.

### Measurements
Debian VM, 4 vCPU (shared with other work), Go 1.26, ext4 on a virtual disk, one run each, so read the digits as orders of
magnitude with a few tens of percent of noise. Harness: [`tools/kvbench`](../../tools/kvbench) (the same program drives all
three engines). Workload: 1,000,000 records of a 16-byte key and a 1,000-byte random (incompressible) value, written in
batches of 1,000 without `fsync`, memory options set to what a browser would use (4 MiB write buffer, 8 MiB block cache
for goleveldb and Pebble). The page cache was dropped before the open and read phases; every phase is its own process.

| | bbolt v1.5.0 | goleveldb (2022-07 master) | Pebble v2.1.7 |
|---|---|---|---|
| structure | copy-on-write B+tree, mmap | LSM, `pread` | LSM, `pread` |
| write, sequential keys | 112k puts/s | 85k puts/s | 127k puts/s |
| write, random keys | 23k puts/s | 21k puts/s | 24k puts/s |
| commit with `fsync` (10 puts), p50 / p99 | 1.1 ms / 2.1 ms | 0.5 ms / 5.2 ms | 0.3 ms / 3.2 ms |
| on disk for 1 GB of values | 1,999 MiB (sequential), 1,710 MiB (random) | 983 MiB | 989 MiB |
| open of the 1 GB database (cold cache) | 2.4 ms with the free list persisted; **26 s** without (it scans the file) | 152 ms | 56 ms |
| random get, cold, p50 / p99 | 47 µs / 180 µs | 43 µs / 214 µs | 49 µs / 173 µs |
| full scan, cold | **26 s** (37 MiB/s) | 2.0 s (495 MiB/s) | 1.9 s (509 MiB/s) |
| resident set after 200k random gets | **700 MiB** (file pages mapped) | 33 MiB | 55 MiB |
| resident set after a full scan | **2.0 GiB** | 33 MiB | 39 MiB |
| peak resident set while writing 1 GB | 57 MiB | 50 MiB | 49 MiB |
| binary size added (stripped hello world 1.6 MB) | +0.45 MB | +0.85 MB | **+15.8 MB** |
| modules added to `go.mod` | 2 | 2 (with snappy) | about 30 |
| `kill -9` of a writer, 15 rounds | 0 torn batches, 0 acknowledged batches lost | 0, 0 | 0, **3 rounds lost acknowledged batches** |
| in-memory mode for private windows | no (would need our own B-tree) | yes (`storage.NewMemStorage`) | yes (`vfs.NewMem`) |
| fault injection | wrap the file (mmap makes it awkward) | wrap `storage.Storage` (a 20-line interface) | wrap `vfs.FS` |
| licence | MIT | BSD-2-Clause | BSD-3-Clause |

The product-level numbers (the same store behind the real IndexedDB layer, 100,000 to 1,000,000 records, a 1 GB database)
are in [docs/storage.md](../storage.md).

## Alternatives considered
* **bbolt.** Excellent for a small configuration database, and it is the smallest. It loses on every requirement that
  matters at a gigabyte: it is memory-mapped, so reading data makes the *resident set* the size of what was read (700 MiB
  after random reads, 2 GiB after a scan, all of it counted against the browser by the operating system's accounting
  even though the pages are clean cache); copy-on-write with 4 KiB pages stores 1 GB of 1 KB values in 2 GB; opening
  without a persisted free list reads the whole file (26 s); there is one writer and a long-lived read transaction pins
  pages, which fits IndexedDB's overlapping transactions badly; and there is no in-memory mode, so private windows would
  need a second engine. Sequential writes are about 30% faster and `fsync` commits cheaper, which does not outweigh that.
* **Pebble.** The best engine of the three on raw speed, and the natural upgrade path (same LSM model, same ordered-key
  contract). It costs 15.8 MB of binary (the entire pure-Go browser is a fraction of that) and about thirty modules of
  dependencies for features we do not use (range keys, MVCC, metrics, a manifest format shared with CockroachDB); it prints
  its own log lines; and a `kill -9` right after a non-`fsync` commit
  lost acknowledged transactions in 3 of 15 rounds (we did not dig into why; its write-ahead log is flushed by a
  background goroutine, which would explain it). A browser gets killed, so that counts against it unless every commit is
  synchronous, which costs more than goleveldb's journal `write(2)`. If goleveldb's maintenance
  ever becomes a problem, Pebble is where to go; the surface to port is `kv.go`, the overlay (`memdb`) and the merged
  iterator (`view`).
* **SQLite (modernc.org/sqlite).** Pure Go (transpiled C), but a SQL engine where we need ordered keys, a translation layer
  from key ranges to queries, and a second large transpiled runtime next to QuickJS. Not measured: it was ruled out on
  shape, not on numbers.
* **Our own B+tree or LSM.** Rejected: crash safety is the part that takes years to get right and an LSM journal with
  checksums and an atomic manifest is exactly what goleveldb has had in production since 2012.
* **One store for all origins.** Simpler to open, but "delete this site's data" and eviction become range deletes in a
  shared tree, and one origin's compaction would stall another's writes. Per-origin stores make clearing a site
  `rm -r` and keep a failure inside the origin it happened in.

## Consequences
* **Memory:** per open store a 4 MiB memtable, a 6 MiB block cache and a few table readers (measured 16 MiB resident for a
  1 GB database that was just opened, 33 MiB after 200k random reads); at most 12 stores are open, idle ones are closed
  after 20 s. A transaction's uncommitted writes are held in an ordered overlay and are capped (256 MiB by default,
  `QuotaExceededError` beyond that).
* **Durability:** a commit is one batch written to the journal with `write(2)`; it survives a crash of the process and is
  in the kernel's hands afterwards. A version change, a `durability: "strict"` transaction and closing a store `fsync`.
  After a power loss the store opens with a prefix of the commits (a power-loss test with a fault-injecting file layer
  proves atomicity and that synchronous commits survive); none can be half-applied.
* **Disk:** about 1.0x the logical size; compaction runs in the background and needs transient free space of a few table
  files. Snappy compression is on (stored values are JSON text).
* **Dependency risk:** goleveldb's last tagged release is 2019 and its last commit is 2022. It is small (about 15,000
  lines), widely deployed (Ethereum clients used it for years), BSD-2, and we use a narrow slice of it. If it must be
  patched, vendoring it under `third_party/` (as `gopus` and `go-mp3` are) is cheap.
* **goleveldb quirk:** after `Close` one janitor goroutine lingers up to a second waiting for a pooled memtable; the leak
  test accounts for it.
* **Format:** every store starts with a format mark and every value carries a version tag. A directory in a format this
  build does not know (or that cannot be recovered) is moved aside as `kv.bak` and a new store starts; a store that is
  merely locked or unreadable for an I/O reason is never moved. The earlier log format never shipped and was deleted.
