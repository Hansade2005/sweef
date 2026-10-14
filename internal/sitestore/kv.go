package sitestore

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/syndtr/goleveldb/leveldb"
	lerrors "github.com/syndtr/goleveldb/leveldb/errors"
	"github.com/syndtr/goleveldb/leveldb/filter"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/memdb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/storage"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// The ordered key-value store under every origin (ADR 0002 explains the choice
// of an LSM tree, goleveldb, over a B+tree and over Pebble).
//
// One store per origin holds the IndexedDB databases (records, index entries,
// schema, key generators), the Cache Storage metadata and the OPFS tree. Large
// values are not in it: they are files next to it (blobs.go) and the store only
// holds their reference. What the store keeps in memory is bounded and does not
// depend on the amount of data: the memtable (kvWriteBuffer), the block cache
// (kvBlockCache) and a few table readers.

const (
	kvWriteBuffer = 4 << 20
	kvBlockCache  = 6 << 20
	kvOpenFiles   = 48

	// formatMark is the first record of every store. A directory that holds a store without it (or with another
	// one) is in a format this build does not know: it is moved aside as <dir>.bak and a new store starts.
	formatMark = "SWKV1"
)

var keyFormat = []byte{0x00, 'f', 'm', 't'}

// errUnknownFormat marks a store this build cannot interpret (as opposed to one it cannot open right now).
var errUnknownFormat = errors.New("sitestore: unknown store format")

// kvStore is an open ordered store.
type kvStore struct {
	db   *leveldb.DB
	dir  string // "" = in memory
	stor storage.Storage
}

func kvOptions() *opt.Options {
	return &opt.Options{
		WriteBuffer:            kvWriteBuffer,
		BlockCacheCapacity:     kvBlockCache,
		OpenFilesCacheCapacity: kvOpenFiles,
		Filter:                 filter.NewBloomFilter(10),
		Compression:            opt.SnappyCompression,
		// a batch larger than the write buffer is written through a transaction, straight to table files
		// (the default); the journal tolerates a torn tail, which is what a crash leaves
	}
}

// openKV opens (creating it if needed) the store in dir; dir == "" opens an in-memory store.
// A store that cannot be read is moved aside (dir.bak) and replaced by an empty one.
func openKV(dir string) (*kvStore, error) {
	if dir == "" {
		return openKVOn(storage.NewMemStorage(), "")
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	k, err := openKVDir(dir)
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, errUnknownFormat) && !lerrors.IsCorrupted(err) {
		return nil, err // locked by another process, no permission, disk error: the data may be fine
	}
	// unknown or damaged: keep the evidence, start empty
	bak := dir + ".bak"
	_ = os.RemoveAll(bak)
	if rerr := os.Rename(dir, bak); rerr != nil {
		return nil, fmt.Errorf("sitestore: %v (and the store could not be set aside: %v)", err, rerr)
	}
	return openKVDir(dir)
}

func openKVDir(dir string) (*kvStore, error) {
	stor, err := storage.OpenFile(dir, false)
	if err != nil {
		return nil, err
	}
	k, err := openKVOn(stor, dir)
	if err != nil {
		stor.Close()
		return nil, err
	}
	return k, nil
}

// openKVOn opens a store on a given storage (tests inject faulty storage here).
func openKVOn(stor storage.Storage, dir string) (*kvStore, error) {
	db, err := leveldb.Open(stor, kvOptions())
	if lerrors.IsCorrupted(err) {
		// a missing or damaged manifest is rebuilt from the table files, if they are intact
		db, err = leveldb.Recover(stor, kvOptions())
	}
	if err != nil {
		return nil, err
	}
	k := &kvStore{db: db, dir: dir, stor: stor}
	v, err := db.Get(keyFormat, nil)
	switch {
	case err == nil:
		if string(v) != formatMark {
			db.Close()
			return nil, fmt.Errorf("%w %q", errUnknownFormat, v)
		}
	case errors.Is(err, leveldb.ErrNotFound):
		it := db.NewIterator(nil, nil)
		nonEmpty := it.First()
		it.Release()
		if nonEmpty {
			db.Close()
			return nil, fmt.Errorf("%w: no format mark", errUnknownFormat)
		}
		if err := db.Put(keyFormat, []byte(formatMark), &opt.WriteOptions{Sync: true}); err != nil {
			db.Close()
			return nil, err
		}
	default:
		db.Close()
		return nil, err
	}
	return k, nil
}

func (k *kvStore) close() error {
	if k == nil || k.db == nil {
		return nil
	}
	err := k.db.Close()
	k.db = nil
	if k.stor != nil { // Open (unlike OpenFile) leaves the storage, and with it the directory lock, to us
		if e := k.stor.Close(); err == nil {
			err = e
		}
	}
	return err
}

func (k *kvStore) get(key []byte) ([]byte, bool) {
	v, err := k.db.Get(key, nil)
	if err != nil {
		return nil, false
	}
	return v, true
}

// write applies a batch atomically. sync forces it to stable storage before returning; without it the batch is
// in the journal (and so survives a crash of the process) and reaches the disk with the next sync.
func (k *kvStore) write(b *leveldb.Batch, sync bool) error {
	return k.db.Write(b, &opt.WriteOptions{Sync: sync})
}

func (k *kvStore) sync() {
	if k == nil || k.db == nil {
		return
	}
	// an empty synchronous write flushes the journal
	var b leveldb.Batch
	b.Put(keyFormat, []byte(formatMark))
	_ = k.db.Write(&b, &opt.WriteOptions{Sync: true})
}

// ---- helpers over iterators ----------------------------------------------------------------

// prefixEnd is the smallest key greater than every key that starts with p (nil if there is none).
func prefixEnd(p []byte) []byte {
	e := append([]byte(nil), p...)
	for i := len(e) - 1; i >= 0; i-- {
		if e[i] != 0xFF {
			e[i]++
			return e[:i+1]
		}
	}
	return nil
}

func prefixRange(p []byte) *util.Range { return &util.Range{Start: p, Limit: prefixEnd(p)} }

// ---- the merged view of a transaction ------------------------------------------------------------

// Overlay values are one marker byte followed by the value: a deletion hides the key in the snapshot below.
const (
	ovDel = 0
	ovPut = 1
)

type overlay struct {
	db    *memdb.DB
	bytes int64
}

func newOverlay() *overlay { return &overlay{db: memdb.New(cmpBytes{}, 0)} }

type cmpBytes struct{}

func (cmpBytes) Compare(a, b []byte) int { return bytes.Compare(a, b) }

func (o *overlay) put(k, v []byte) {
	buf := make([]byte, 1+len(v))
	buf[0] = ovPut
	copy(buf[1:], v)
	_ = o.db.Put(k, buf)
	o.bytes += int64(len(k) + len(v) + 32)
}

func (o *overlay) del(k []byte) {
	_ = o.db.Put(k, []byte{ovDel})
	o.bytes += int64(len(k) + 33)
}

// get returns the overlay's verdict on a key: found, and whether it is a deletion.
func (o *overlay) get(k []byte) (v []byte, found, deleted bool) {
	raw, err := o.db.Get(k)
	if err != nil || len(raw) == 0 {
		return nil, false, false
	}
	if raw[0] == ovDel {
		return nil, true, true
	}
	return raw[1:], true, false
}

func (o *overlay) empty() bool { return o.db.Len() == 0 }

// view merges an iterator over a snapshot with an iterator over a transaction's overlay into one ordered
// stream of live items. It supports the two positioning operations cursors need (first item >= k and last
// item < k) and stepping in the direction it was positioned in. It never materialises a result set.
type view struct {
	base iterator.Iterator
	ov   iterator.Iterator // nil when the transaction wrote nothing

	fwd      bool
	key, val []byte
	fromOv   bool
	bOK, oOK bool
	valid    bool
	released bool
}

func newView(base, ov iterator.Iterator) *view { return &view{base: base, ov: ov} }

func (v *view) release() {
	if v.released {
		return
	}
	v.released = true
	if v.base != nil {
		v.base.Release()
	}
	if v.ov != nil {
		v.ov.Release()
	}
}

// SeekGE positions on the first live item with key >= k.
func (v *view) SeekGE(k []byte) bool {
	v.fwd = true
	v.bOK = v.base.Seek(k)
	if v.ov != nil {
		v.oOK = v.ov.Seek(k)
	}
	return v.settleFwd()
}

// Next steps forward.
func (v *view) Next() bool {
	if !v.valid {
		return false
	}
	if v.fwd {
		v.stepFwd()
		return v.settleFwd()
	}
	return false
}

// stepFwd moves past the current item in whichever source(s) hold it.
func (v *view) stepFwd() {
	if v.fromOv {
		if v.bOK && v.base != nil && bytes.Compare(v.base.Key(), v.key) == 0 {
			v.bOK = v.base.Next()
		}
		v.oOK = v.ov.Next()
	} else {
		v.bOK = v.base.Next()
	}
}

func (v *view) settleFwd() bool {
	for {
		switch {
		case !v.bOK && !v.oOK:
			v.valid = false
			return false
		case v.bOK && v.oOK:
			c := bytes.Compare(v.base.Key(), v.ov.Key())
			if c < 0 {
				v.useBase()
				return true
			}
			if c > 0 {
				if v.useOv() {
					return true
				}
				v.oOK = v.ov.Next()
				continue
			}
			// same key: the overlay wins, the snapshot's version is hidden
			if v.ovLive() {
				v.useOv()
				return true
			}
			v.bOK = v.base.Next()
			v.oOK = v.ov.Next()
		case v.bOK:
			v.useBase()
			return true
		default:
			if v.useOv() {
				return true
			}
			v.oOK = v.ov.Next()
		}
	}
}

// SeekLT positions on the last live item with key < k.
func (v *view) SeekLT(k []byte) bool {
	v.fwd = false
	if v.base.Seek(k) {
		v.bOK = v.base.Prev()
	} else {
		v.bOK = v.base.Last()
	}
	if v.ov != nil {
		if v.ov.Seek(k) {
			v.oOK = v.ov.Prev()
		} else {
			v.oOK = v.ov.Last()
		}
	}
	return v.settleBack()
}

// Prev steps backward (only after SeekLT).
func (v *view) Prev() bool {
	if !v.valid || v.fwd {
		return false
	}
	if v.fromOv {
		if v.bOK && bytes.Compare(v.base.Key(), v.key) == 0 {
			v.bOK = v.base.Prev()
		}
		v.oOK = v.ov.Prev()
	} else {
		v.bOK = v.base.Prev()
	}
	return v.settleBack()
}

func (v *view) settleBack() bool {
	for {
		switch {
		case !v.bOK && !v.oOK:
			v.valid = false
			return false
		case v.bOK && v.oOK:
			c := bytes.Compare(v.base.Key(), v.ov.Key())
			if c > 0 {
				v.useBase()
				return true
			}
			if c < 0 {
				if v.useOv() {
					return true
				}
				v.oOK = v.ov.Prev()
				continue
			}
			if v.ovLive() {
				v.useOv()
				return true
			}
			v.bOK = v.base.Prev()
			v.oOK = v.ov.Prev()
		case v.bOK:
			v.useBase()
			return true
		default:
			if v.useOv() {
				return true
			}
			v.oOK = v.ov.Prev()
		}
	}
}

func (v *view) ovLive() bool {
	raw := v.ov.Value()
	return len(raw) > 0 && raw[0] == ovPut
}

func (v *view) useBase() {
	v.key, v.val, v.fromOv, v.valid = v.base.Key(), v.base.Value(), false, true
}

// useOv makes the overlay item current when it is a put; it reports false for a deletion.
func (v *view) useOv() bool {
	raw := v.ov.Value()
	if len(raw) == 0 || raw[0] != ovPut {
		return false
	}
	v.key, v.val, v.fromOv, v.valid = v.ov.Key(), raw[1:], true, true
	return true
}

func (v *view) Key() []byte   { return v.key }
func (v *view) Value() []byte { return v.val }
func (v *view) Valid() bool   { return v.valid }
