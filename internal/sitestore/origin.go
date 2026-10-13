package sitestore

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
)

// Key prefixes of the ordered store (the first byte of every key). Everything
// is big-endian and fixed-width where it is a number, so byte order is numeric
// order; names and URLs are variable and always last or length-delimited by a
// byte that cannot occur in them.
//
//	00 'fmt'                          format mark (kv.go)
//	00 'cnt'                          id counter (uvarint)
//	00 'usg'                          usage: IndexedDB, Cache Storage, OPFS bytes (varints)
//	02 name                           database registry: schema record of an IndexedDB database
//	03 db(4) store(4)                 store state: key generator, record count, bytes
//	04 db(4) store(4) index(4)        garbage: a range of data that is deleted in the background
//	06 blob(8)                        registry of blob files
//	10 db(4) store(4) 0(4) pk         a record (value)
//	10 db(4) store(4) 1(4) pk         reverse entry: the index keys of that record
//	10 db(4) store(4) i(4) ik pk      index i entry (empty value), ordered by (index key, primary key)
//	20 name                           Cache Storage: cache name -> id, creation sequence
//	21 cache(4) seq(8)                a cache entry (metadata; the body is a blob)
//	22 seq(8)                         caches in creation order -> name
//	23 cache(4) url0 fullurl 0 seq(8) lookup of entries by URL
//	30 dir(8) name                    OPFS: children of a directory
//	31 id(8)                          OPFS: node
const (
	pDBReg      = 0x02
	pStoreState = 0x03
	pGarbage    = 0x04
	pBlob       = 0x06
	pData       = 0x10
	pCacheReg   = 0x20
	pCacheEnt   = 0x21
	pCacheOrd   = 0x22
	pCacheURL   = 0x23
	pOPFSKid    = 0x30
	pOPFSNode   = 0x31

	iidRecord  = 0
	iidReverse = 1
	iidFirst   = 2 // first id an index can have
	iidAll     = 0xFFFFFFFF
)

var (
	keyCounters = []byte{0x00, 'c', 'n', 't'}
	keyUsage    = []byte{0x00, 'u', 's', 'g'}
)

// Origin is the storage of one origin.
type Origin struct {
	m    *Manager
	name string
	dir  string // "" = memory only

	mu    sync.Mutex // dbs
	dbs   map[string]*Database
	cache *CacheStore
	opfs  *OPFS

	cacheMu sync.Mutex

	// the ordered store is opened on first use and closed when nothing uses it any more
	kvMu    sync.Mutex
	kvs     *kvStore
	kvRefs  int
	idle    *time.Timer
	gcWG    sync.WaitGroup
	gcStop  chan struct{}
	gcMu    sync.Mutex // gcBusy
	gcBusy  bool
	gcCond  *sync.Cond
	commitM sync.Mutex // serialises batches (usage and counters are part of every one)

	nextIDv  atomic.Uint64
	memMu    sync.Mutex
	memBlobs map[uint64][]byte

	usageIDB   atomic.Int64
	usageCache atomic.Int64
	usageOPFS  atomic.Int64
	other      atomic.Int64 // bytes of service worker scripts
	persist    atomic.Bool
	lastAccess atomic.Int64 // unix nanoseconds
	metaWrite  atomic.Int64
	conns      atomic.Int64 // open IndexedDB connections (also keeps the store open)
}

func newOrigin(m *Manager, name, dir string) *Origin {
	o := &Origin{m: m, name: name, dir: dir, dbs: map[string]*Database{}}
	o.gcCond = sync.NewCond(&o.gcMu)
	o.nextIDv.Store(1)
	o.lastAccess.Store(m.now().UnixNano())
	return o
}

// Name is the origin string.
func (o *Origin) Name() string { return o.name }

// ---- the store ----------------------------------------------------------------------------------

// kvIdleClose is how long an unused store stays open.
const kvIdleClose = 20 * time.Second

// acquire returns the origin's store, opening it if needed. Every acquire is paired with a release.
func (o *Origin) acquire() (*kvStore, error) {
	o.kvMu.Lock()
	defer o.kvMu.Unlock()
	if o.kvs == nil {
		dir := ""
		if o.dir != "" {
			dir = filepath.Join(o.dir, "kv")
			o.removeLegacy()
		}
		open := openKV
		if o.m.opts.openStore != nil {
			open = o.m.opts.openStore
		}
		k, err := open(dir)
		if err != nil {
			return nil, err
		}
		o.kvs = k
		o.loadState(k)
		o.m.noteOpen(o)
	}
	o.kvRefs++
	if o.idle != nil {
		o.idle.Stop()
		o.idle = nil
	}
	return o.kvs, nil
}

func (o *Origin) release(k *kvStore) {
	o.kvMu.Lock()
	defer o.kvMu.Unlock()
	if o.kvs != k { // the store was closed by a wipe while this reference was out
		return
	}
	o.kvRefs--
	if o.kvRefs == 0 && o.dir != "" {
		d := o.m.opts.IdleClose
		if d <= 0 {
			d = kvIdleClose
		}
		if o.idle != nil {
			o.idle.Stop()
		}
		o.idle = time.AfterFunc(d, o.closeIdle)
	}
}

// closeIdle closes the store if nobody is using it.
func (o *Origin) closeIdle() {
	o.kvMu.Lock()
	if o.kvRefs > 0 || o.kvs == nil || o.dir == "" {
		o.kvMu.Unlock()
		return
	}
	o.closeKVLocked()
	o.kvMu.Unlock()
	o.writeMeta()
}

// closeKVLocked stops the garbage collector and closes the store. Caller holds kvMu.
func (o *Origin) closeKVLocked() {
	if o.kvs == nil {
		return
	}
	if o.gcStop != nil {
		close(o.gcStop)
		o.gcStop = nil
	}
	o.gcWG.Wait()
	o.kvs.sync()
	_ = o.kvs.close()
	o.kvs = nil
	o.kvRefs = 0
	if o.idle != nil {
		o.idle.Stop()
		o.idle = nil
	}
	o.m.noteClose(o)
}

// StoreOpen reports whether the origin's ordered store is currently open (tests, diagnostics).
func (o *Origin) StoreOpen() bool {
	o.kvMu.Lock()
	defer o.kvMu.Unlock()
	return o.kvs != nil
}

// loadState reads the counters, the usage and starts the background work an open store owes. Caller holds kvMu.
func (o *Origin) loadState(k *kvStore) {
	if v, ok := k.get(keyCounters); ok {
		if n, c := binary.Uvarint(v); c > 0 && n >= o.nextIDv.Load() {
			o.nextIDv.Store(n)
		}
	}
	if v, ok := k.get(keyUsage); ok {
		var vals [3]int64
		for i := range vals {
			n, c := binary.Varint(v)
			if c <= 0 {
				break
			}
			vals[i], v = n, v[c:]
		}
		o.usageIDB.Store(vals[0])
		o.usageCache.Store(vals[1])
		o.usageOPFS.Store(vals[2])
	}
	o.sweepBlobs(k)
	o.startGCLocked(k)
}

func (o *Origin) allocID() uint64 { return o.nextIDv.Add(1) }

// removeLegacy deletes the directories of the log format that preceded the ordered store; it never shipped.
func (o *Origin) removeLegacy() {
	for _, d := range []string{"idb", "caches"} {
		_ = os.RemoveAll(filepath.Join(o.dir, d))
	}
}

// commitBatch adds the id counter and the usage change to a batch and writes it. Every write to the store
// goes through here, so the usage record and the data it describes are always committed together.
func (o *Origin) commitBatch(k *kvStore, b *leveldb.Batch, dIDB, dCache, dOPFS int64, sync bool) error {
	o.commitM.Lock()
	defer o.commitM.Unlock()
	idb, cache, opfs := o.usageIDB.Load()+dIDB, o.usageCache.Load()+dCache, o.usageOPFS.Load()+dOPFS
	if idb < 0 {
		idb = 0
	}
	if cache < 0 {
		cache = 0
	}
	if opfs < 0 {
		opfs = 0
	}
	var u []byte
	u = binary.AppendVarint(u, idb)
	u = binary.AppendVarint(u, cache)
	u = binary.AppendVarint(u, opfs)
	b.Put(keyUsage, u)
	b.Put(keyCounters, binary.AppendUvarint(nil, o.nextIDv.Load()))
	if err := k.write(b, sync); err != nil {
		return err
	}
	o.usageIDB.Store(idb)
	o.usageCache.Store(cache)
	o.usageOPFS.Store(opfs)
	o.m.noteUsage(o)
	return nil
}

// ---- garbage collection -----------------------------------------------------------------------------

func garbageKey(db, store, index uint32) []byte {
	k := make([]byte, 13)
	k[0] = pGarbage
	binary.BigEndian.PutUint32(k[1:], db)
	binary.BigEndian.PutUint32(k[5:], store)
	binary.BigEndian.PutUint32(k[9:], index)
	return k
}

func dataPrefix(db, store, index uint32) []byte {
	k := make([]byte, 13)
	k[0] = pData
	binary.BigEndian.PutUint32(k[1:], db)
	binary.BigEndian.PutUint32(k[5:], store)
	binary.BigEndian.PutUint32(k[9:], index)
	return k
}

func (o *Origin) startGCLocked(k *kvStore) {
	o.gcMu.Lock()
	defer o.gcMu.Unlock()
	if o.gcBusy {
		return
	}
	// is there anything to do?
	it := k.db.NewIterator(prefixRange([]byte{pGarbage}), nil)
	any := it.First()
	it.Release()
	if !any {
		return
	}
	o.gcBusy = true
	if o.gcStop == nil {
		o.gcStop = make(chan struct{})
	}
	stop := o.gcStop
	o.gcWG.Add(1)
	go o.gcLoop(k, stop)
}

// kickGC starts the collector if garbage was registered.
func (o *Origin) kickGC() {
	o.kvMu.Lock()
	if o.kvs != nil {
		o.startGCLocked(o.kvs)
	}
	o.kvMu.Unlock()
}

const gcChunk = 2048

// gcCaches marks a garbage record that stands for a deleted cache instead of a database range.
const gcCaches = 0xFFFFFFFF

type gcRange struct {
	prefix []byte
	blobs  bool // the values in this range may reference blob files
}

func gcRanges(db, store, index uint32) []gcRange {
	if db == gcCaches {
		return []gcRange{{cachePrefix(pCacheBody, store), true}, {cachePrefix(pCacheEnt, store), false}, {cachePrefix(pCacheURL, store), false}}
	}
	if index == iidAll {
		return []gcRange{{dataPrefix(db, store, 0)[:9], false}}
	}
	return []gcRange{{dataPrefix(db, store, index), false}}
}

func (o *Origin) gcLoop(k *kvStore, stop chan struct{}) {
	defer o.gcWG.Done()
	defer func() {
		o.gcMu.Lock()
		o.gcBusy = false
		o.gcCond.Broadcast()
		o.gcMu.Unlock()
	}()
	for {
		select {
		case <-stop:
			return
		default:
		}
		it := k.db.NewIterator(prefixRange([]byte{pGarbage}), nil)
		if !it.First() {
			it.Release()
			return
		}
		marker := append([]byte(nil), it.Key()...)
		it.Release()
		db := binary.BigEndian.Uint32(marker[1:])
		store := binary.BigEndian.Uint32(marker[5:])
		index := binary.BigEndian.Uint32(marker[9:])
		var b leveldb.Batch
		var blobs []uint64
		n := 0
		exhausted := true
		for _, rg := range gcRanges(db, store, index) {
			di := k.db.NewIterator(prefixRange(rg.prefix), nil)
			ok := di.First()
			for ; ok && n < gcChunk; ok = di.Next() {
				key := di.Key()
				b.Delete(key)
				n++
				isRec := rg.blobs
				if db != gcCaches && len(key) >= 13 && binary.BigEndian.Uint32(key[9:]) == iidRecord {
					isRec = true
				}
				if isRec && isBlobValue(di.Value()) {
					if id, _, vok := valueInfo(di.Value()); vok && id != 0 {
						b.Delete(blobKey(id))
						blobs = append(blobs, id)
					}
				}
			}
			di.Release()
			if ok {
				exhausted = false
				break
			}
			if n >= gcChunk {
				exhausted = false // this range was emptied exactly at the limit: look again next round
				break
			}
		}
		if exhausted {
			b.Delete(marker)
		}
		if b.Len() > 0 {
			if err := o.commitBatch(k, &b, 0, 0, 0, false); err != nil {
				return
			}
			o.dropBlobFiles(blobs)
		}
	}
}

func isBlobValue(stored []byte) bool { return len(stored) >= 2 && stored[1]&flagBlob != 0 }

// WaitGC blocks until the background deletion of dropped data has finished (tests, and the quota code, which
// wants the space back).
func (o *Origin) WaitGC() {
	o.gcMu.Lock()
	for o.gcBusy {
		o.gcCond.Wait()
	}
	o.gcMu.Unlock()
}

// ---- meta.json ---------------------------------------------------------------------------------------------

type originMeta struct {
	Origin     string `json:"origin"`
	Persisted  bool   `json:"persisted"`
	LastAccess int64  `json:"lastAccess,omitempty"` // unix nanoseconds
	IDB        int64  `json:"idb,omitempty"`
	Caches     int64  `json:"caches,omitempty"`
	OPFS       int64  `json:"opfs,omitempty"`
}

func (o *Origin) readMeta() {
	if o.dir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(o.dir, "meta.json"))
	if err != nil {
		return
	}
	var meta originMeta
	if json.Unmarshal(b, &meta) != nil {
		return
	}
	o.persist.Store(meta.Persisted)
	if meta.LastAccess > 0 {
		o.lastAccess.Store(meta.LastAccess)
	}
	o.usageIDB.Store(meta.IDB)
	o.usageCache.Store(meta.Caches)
	o.usageOPFS.Store(meta.OPFS)
}

func (o *Origin) writeMeta() {
	if o.dir == "" {
		return
	}
	_ = os.MkdirAll(o.dir, 0o700)
	b, _ := json.Marshal(originMeta{Origin: o.name, Persisted: o.persist.Load(), LastAccess: o.lastAccess.Load(),
		IDB: o.usageIDB.Load(), Caches: o.usageCache.Load(), OPFS: o.usageOPFS.Load()})
	tmp := filepath.Join(o.dir, "meta.json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(o.dir, "meta.json"))
	}
	o.metaWrite.Store(o.m.now().UnixNano())
}

// touch makes sure the origin's directory and meta.json exist.
func (o *Origin) touch() {
	if o.dir == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(o.dir, "meta.json")); err == nil {
		return
	}
	o.writeMeta()
}

// access records that the origin was used now (least-recently-used eviction order). meta.json is rewritten at
// most once a minute here and whenever the store closes.
func (o *Origin) access() {
	now := o.m.now().UnixNano()
	o.lastAccess.Store(now)
	if o.dir != "" && now-o.metaWrite.Load() > int64(time.Minute) {
		o.writeMeta()
	}
}

// LastAccess is when the origin last opened a database, committed a transaction or touched its caches.
func (o *Origin) LastAccess() time.Time { return time.Unix(0, o.lastAccess.Load()) }

// SWDir is where the origin's service worker registrations live ("" = memory only).
func (o *Origin) SWDir() string {
	if o.dir == "" {
		return ""
	}
	return filepath.Join(o.dir, "sw")
}

// scanSW sums the service worker files of an origin.
func (o *Origin) scanSW() {
	if o.dir == "" {
		return
	}
	var n int64
	_ = filepath.WalkDir(filepath.Join(o.dir, "sw"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	o.other.Store(n)
}

// SetOtherBytes records how much the origin's service worker scripts occupy (counted in usage and quota).
func (o *Origin) SetOtherBytes(n int64) { o.other.Store(n); o.m.noteUsage(o) }

// Touch makes sure the origin is listed on disk (see Manager.Origins).
func (o *Origin) Touch() { o.touch() }

// Persisted reports whether the origin has persistent storage (it is never evicted).
func (o *Origin) Persisted() bool { return o.persist.Load() }

// SetPersisted records a grant (or revocation) of persistent storage, bypassing the policy: for the app
// layer, which knows when the person has decided.
func (o *Origin) SetPersisted(v bool) {
	if o.persist.Swap(v) != v {
		o.writeMeta()
	}
}

// Usage is what the origin stores, by kind, in bytes.
type Usage struct {
	IndexedDB int64
	Caches    int64
	OPFS      int64
	Other     int64 // service worker scripts
	Total     int64
}

// Usage reports the origin's current storage use.
func (o *Origin) Usage() Usage {
	u := Usage{IndexedDB: o.usageIDB.Load(), Caches: o.usageCache.Load(), OPFS: o.usageOPFS.Load(), Other: o.other.Load()}
	u.Total = u.IndexedDB + u.Caches + u.OPFS + u.Other
	return u
}

func (o *Origin) usage() int64 { return o.Usage().Total }
