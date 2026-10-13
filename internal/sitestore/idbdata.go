package sitestore

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strconv"

	"github.com/syndtr/goleveldb/leveldb"
	lerrors "github.com/syndtr/goleveldb/leveldb/errors"
)

// The data of an IndexedDB database in the origin's ordered store.
//
// A store's records, its reverse entries and its indexes are ranges of one
// keyspace, the way Chromium lays IndexedDB out on LevelDB:
//
//	pData | db(4) | store(4) | 0 | pk                record: [value tag][flags][payload or blob ref]
//	pData | db(4) | store(4) | 1 | pk                reverse entry: the index keys that record contributed
//	pData | db(4) | store(4) | i | index key | pk     entry of index i (no value)
//
// pk and index key are the storage form of IndexedDB keys (keys.go), whose byte
// order is the key order and which are self-delimiting, so "index key || pk"
// sorts by (index key, primary key) and a range of keys is a range of bytes.
// Cursors are positioned by seeks in these ranges and step through them one
// item at a time; nothing builds a result set except the methods whose
// contract is to return one (getAll and friends, bounded by their count).
//
// Clearing an object store (or deleting a store, an index or a database) does
// not touch the data: the store gets a new physical id (clear) or its id is
// queued as garbage in the same atomic batch, and a background task deletes
// the orphaned range in chunks (origin.go). The operation is O(1) and atomic
// for any amount of data.

const dataPrefixLen = 13

func (d *Database) rkey(st *store, iid uint32, parts ...[]byte) []byte {
	n := dataPrefixLen
	for _, p := range parts {
		n += len(p)
	}
	k := make([]byte, dataPrefixLen, n)
	k[0] = pData
	binary.BigEndian.PutUint32(k[1:], d.id)
	binary.BigEndian.PutUint32(k[5:], st.id)
	binary.BigEndian.PutUint32(k[9:], iid)
	for _, p := range parts {
		k = append(k, p...)
	}
	return k
}

func recCost(pkLen int, payload int64) int64 { return int64(pkLen) + payload + 16 }
func idxCost(ikLen, pkLen int) int64         { return int64(2*ikLen+pkLen) + 16 }

// ---- reading through the transaction ---------------------------------------------------------

func (t *Tx) fail(err error) {
	if err != nil && t.ioErr == nil {
		t.ioErr = err
	}
}

func (t *Tx) snapshot() *leveldb.Snapshot {
	if t.snap == nil {
		s, err := t.conn.kv.db.GetSnapshot()
		if err != nil {
			t.fail(err)
			return nil
		}
		t.snap = s
	}
	return t.snap
}

// get reads a key as the transaction sees it: its own writes first, then the snapshot.
func (t *Tx) get(key []byte) ([]byte, bool) {
	if t.ov != nil {
		if v, found, deleted := t.ov.get(key); found {
			if deleted {
				return nil, false
			}
			return v, true
		}
	}
	s := t.snapshot()
	if s == nil {
		return nil, false
	}
	v, err := s.Get(key, nil)
	if err != nil {
		if !errors.Is(err, lerrors.ErrNotFound) {
			t.fail(err)
		}
		return nil, false
	}
	return v, true
}

// view is the transaction's merged iterator over the snapshot and its own writes.
func (t *Tx) view() *view {
	if t.vw == nil {
		s := t.snapshot()
		if s == nil {
			return nil
		}
		base := s.NewIterator(nil, nil)
		v := newView(base, nil)
		if t.ov != nil {
			v.ov = t.ov.db.NewIterator(nil)
		}
		t.vw = v
	}
	return t.vw
}

// writable prepares the overlay for a write.
func (t *Tx) writable() {
	if t.ov == nil {
		t.ov = newOverlay()
		if t.vw != nil { // the cached view was built without an overlay iterator
			t.vw.release()
			t.vw = nil
		}
	}
	t.wrote = true
}

func (t *Tx) put(k, v []byte) error {
	t.writable()
	t.ov.put(k, v)
	return t.checkSize()
}

func (t *Tx) del(k []byte) {
	t.writable()
	t.ov.del(k)
}

func (t *Tx) checkSize() error {
	if t.ov.bytes > t.db.o.m.opts.MaxTxBytes {
		return errf("QuotaExceededError", "The transaction holds too much uncommitted data.")
	}
	return nil
}

func (t *Tx) markDirty(st *store) {
	if t.dirty == nil {
		t.dirty = map[*store]bool{}
	}
	t.dirty[st] = true
}

func (t *Tx) addUndo(f func()) { t.undo = append(t.undo, f) }

// adjust changes the byte and record counts of a store (and so the origin's usage when the transaction commits).
func (t *Tx) adjust(st *store, dCount, dBytes int64) {
	st.count += dCount
	st.bytes += dBytes
	t.delta += dBytes
	t.markDirty(st)
	t.addUndo(func() { st.count -= dCount; st.bytes -= dBytes; t.delta -= dBytes })
}

func (t *Tx) adjustIndex(st *store, ix *index, dBytes int64) {
	ix.bytes += dBytes
	t.delta += dBytes
	t.markDirty(st)
	t.addUndo(func() { ix.bytes -= dBytes; t.delta -= dBytes })
}

func (t *Tx) free(blob uint64) {
	if blob != 0 {
		t.freed = append(t.freed, blob)
	}
}

// ---- reverse entries ----------------------------------------------------------------------------

type revKeys map[uint32][][]byte

func encRev(m revKeys) []byte {
	ids := make([]uint32, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var w wbuf
	w.u(uint64(len(ids)))
	for _, id := range ids {
		w.u(uint64(id))
		w.u(uint64(len(m[id])))
		for _, k := range m[id] {
			w.u(uint64(len(k)))
			w.b = append(w.b, k...)
		}
	}
	return w.b
}

func decRev(b []byte) revKeys {
	r := rbuf{b: b}
	n := int(r.u())
	out := make(revKeys, n)
	for i := 0; i < n && r.err == nil; i++ {
		id := uint32(r.u())
		nk := int(r.u())
		for j := 0; j < nk && r.err == nil; j++ {
			l := int(r.u())
			if r.err != nil || l > len(r.b) {
				r.err = errCorrupt
				break
			}
			out[id] = append(out[id], append([]byte(nil), r.b[:l]...))
			r.b = r.b[l:]
		}
	}
	return out
}

// ---- schema mutations -------------------------------------------------------------------------

func (d *Database) doCreateStore(t *Tx, name, kp string, hasKP, auto bool) error {
	if d.stores[name] != nil {
		return errf("ConstraintError", "An object store with the specified name already exists.")
	}
	st := newStore(name, uint32(d.o.allocID()), kp, hasKP, auto)
	d.stores[name] = st
	t.schemaDirty, t.wrote = true, true
	t.markDirty(st)
	t.addUndo(func() { delete(d.stores, name) })
	return nil
}

func (d *Database) doDeleteStore(t *Tx, st *store) {
	delete(d.stores, st.name)
	freed := st.bytes
	for _, ix := range st.idx {
		freed += ix.bytes
	}
	t.delta -= freed
	t.schemaDirty, t.wrote = true, true
	t.garbage = append(t.garbage, [2]uint32{st.id, iidAll})
	t.addUndo(func() {
		d.stores[st.name] = st
		t.delta += freed
		t.garbage = t.garbage[:len(t.garbage)-1]
	})
}

func (d *Database) doRenameStore(t *Tx, st *store, to string) {
	from := st.name
	delete(d.stores, from)
	st.name = to
	d.stores[to] = st
	t.schemaDirty, t.wrote = true, true
	t.addUndo(func() { delete(d.stores, to); st.name = from; d.stores[from] = st })
}

// IndexEntry is one record's keys in an index being created.
type IndexEntry struct {
	PK   string
	Keys []string
}

type idxEntry = IndexEntry

func (d *Database) doCreateIndex(t *Tx, st *store, name, kp string, unique, multi bool, ents []idxEntry) error {
	if st.idx[name] != nil {
		return errf("ConstraintError", "An index with the specified name already exists.")
	}
	type item struct {
		pk []byte
		ks [][]byte
	}
	var items []item
	for _, e := range ents {
		pk, err := WireToStorage(e.PK)
		if err != nil {
			continue
		}
		var ks [][]byte
		seen := map[string]bool{}
		for _, w := range e.Keys {
			k, err := WireToStorage(w)
			if err != nil || seen[string(k)] {
				continue
			}
			seen[string(k)] = true
			ks = append(ks, k)
		}
		if len(ks) > 0 {
			items = append(items, item{pk, ks})
		}
	}
	if unique {
		seen := map[string][]byte{}
		for _, it := range items {
			for _, k := range it.ks {
				if prev, ok := seen[string(k)]; ok && !bytes.Equal(prev, it.pk) {
					return errf("ConstraintError", "Unable to add the index: existing records violate its uniqueness constraint.")
				}
				seen[string(k)] = it.pk
			}
		}
	}
	ix := &index{id: st.nextIdx, name: name, keyPath: kp, unique: unique, multi: multi}
	st.nextIdx++
	st.idx[name] = ix
	t.schemaDirty, t.wrote = true, true
	t.markDirty(st)
	t.addUndo(func() { delete(st.idx, name); st.nextIdx-- })
	var added int64
	for _, it := range items {
		rk := d.rkey(st, iidRecord, it.pk)
		if _, ok := t.get(rk); !ok {
			continue // the script evaluated a record that is not there (any more)
		}
		var rev revKeys
		if rv, ok := t.get(d.rkey(st, iidReverse, it.pk)); ok {
			rev = decRev(rv)
		}
		if rev == nil {
			rev = revKeys{}
		}
		rev[ix.id] = it.ks
		for _, k := range it.ks {
			if err := t.put(d.rkey(st, ix.id, k, it.pk), nil); err != nil {
				return err
			}
			added += idxCost(len(k), len(it.pk))
		}
		if err := t.put(d.rkey(st, iidReverse, it.pk), encRev(rev)); err != nil {
			return err
		}
	}
	t.adjustIndex(st, ix, added)
	return nil
}

func (d *Database) doDeleteIndex(t *Tx, st *store, name string) {
	ix := st.idx[name]
	if ix == nil {
		return
	}
	delete(st.idx, name)
	t.schemaDirty, t.wrote = true, true
	t.garbage = append(t.garbage, [2]uint32{st.id, ix.id})
	t.delta -= ix.bytes
	t.markDirty(st)
	t.addUndo(func() {
		st.idx[name] = ix
		t.delta += ix.bytes
		t.garbage = t.garbage[:len(t.garbage)-1]
	})
}

func (d *Database) doRenameIndex(t *Tx, st *store, from, to string) {
	ix := st.idx[from]
	delete(st.idx, from)
	ix.name = to
	st.idx[to] = ix
	t.schemaDirty, t.wrote = true, true
	t.addUndo(func() { delete(st.idx, to); ix.name = from; st.idx[from] = ix })
}

// ---- records ----------------------------------------------------------------------------------

// indexKeysIn turns the script's {index name: [wire keys]} into {index id: [storage keys]}, dropping indexes
// that do not exist (the schema changed under a queued request) and keys that are not keys.
func (st *store) indexKeysIn(ik map[string][]string) map[uint32][][]byte {
	if len(ik) == 0 || len(st.idx) == 0 {
		return nil
	}
	out := map[uint32][][]byte{}
	for name, ws := range ik {
		ix := st.idx[name]
		if ix == nil {
			continue
		}
		seen := map[string]bool{}
		for _, w := range ws {
			k, err := WireToStorage(w)
			if err != nil || seen[string(k)] {
				continue
			}
			seen[string(k)] = true
			out[ix.id] = append(out[ix.id], k)
		}
	}
	return out
}

// doPut stores a record under the storage-form key pk. ik holds the record's index keys.
func (d *Database) doPut(t *Tx, st *store, pk, val []byte, ik map[uint32][][]byte, noOverwrite bool) error {
	rk := d.rkey(st, iidRecord, pk)
	oldStored, had := t.get(rk)
	if had && noOverwrite {
		return errf("ConstraintError", "A record with the given key already exists.")
	}
	var oldRev revKeys
	if had && len(st.idx) > 0 {
		if rv, ok := t.get(d.rkey(st, iidReverse, pk)); ok {
			oldRev = decRev(rv)
		}
	}
	// uniqueness first: a failed put leaves no trace
	for iid, ks := range ik {
		ix := st.indexByID(iid)
		if ix == nil || !ix.unique {
			continue
		}
		v := t.view()
		if v == nil {
			return nil
		}
		for _, k := range ks {
			prefix := d.rkey(st, iid, k)
			for ok := v.SeekGE(prefix); ok && bytes.HasPrefix(v.Key(), prefix); ok = v.Next() {
				if !bytes.Equal(v.Key()[len(prefix):], pk) {
					return errf("ConstraintError", "Unable to add key to index '%s': at least one key does not satisfy the uniqueness requirements.", ix.name)
				}
			}
		}
	}
	stored, blob, err := d.o.packValue(val)
	if err != nil {
		return errf("UnknownError", "%v", err)
	}
	if blob != 0 {
		t.created = append(t.created, blob)
		var sz [8]byte
		binary.BigEndian.PutUint64(sz[:], uint64(len(val)))
		if err := t.put(blobKey(blob), sz[:]); err != nil {
			return err
		}
	}
	// index entries: remove the ones the new value no longer has, add the new ones
	oldSet := map[string]bool{}
	for iid, ks := range oldRev {
		if st.indexByID(iid) == nil {
			continue
		}
		for _, k := range ks {
			oldSet[string(binary.BigEndian.AppendUint32(nil, iid))+string(k)] = true
		}
	}
	newRev := revKeys{}
	for iid, ks := range ik {
		if st.indexByID(iid) == nil {
			continue
		}
		for _, k := range ks {
			newRev[iid] = append(newRev[iid], k)
		}
	}
	for iid, ks := range newRev {
		ix := st.indexByID(iid)
		for _, k := range ks {
			id := string(binary.BigEndian.AppendUint32(nil, iid)) + string(k)
			if oldSet[id] {
				delete(oldSet, id)
				continue
			}
			if err := t.put(d.rkey(st, iid, k, pk), nil); err != nil {
				return err
			}
			t.adjustIndex(st, ix, idxCost(len(k), len(pk)))
		}
	}
	for id := range oldSet { // left over: the old value had it, the new one does not
		iid := binary.BigEndian.Uint32([]byte(id))
		k := []byte(id[4:])
		t.del(d.rkey(st, iid, k, pk))
		t.adjustIndex(st, st.indexByID(iid), -idxCost(len(k), len(pk)))
	}
	switch {
	case len(newRev) > 0:
		if err := t.put(d.rkey(st, iidReverse, pk), encRev(newRev)); err != nil {
			return err
		}
	case len(oldRev) > 0:
		t.del(d.rkey(st, iidReverse, pk))
	}
	if err := t.put(rk, stored); err != nil {
		return err
	}
	newBlob, newSize, _ := valueInfo(stored)
	_ = newBlob
	if had {
		ob, osz, _ := valueInfo(oldStored)
		t.free(ob)
		t.adjust(st, 0, recCost(len(pk), newSize)-recCost(len(pk), osz))
	} else {
		t.adjust(st, 1, recCost(len(pk), newSize))
	}
	return nil
}

func (d *Database) doDelete(t *Tx, st *store, pk []byte) {
	rk := d.rkey(st, iidRecord, pk)
	old, had := t.get(rk)
	if !had {
		return
	}
	if len(st.idx) > 0 {
		if rv, ok := t.get(d.rkey(st, iidReverse, pk)); ok {
			for iid, ks := range decRev(rv) {
				ix := st.indexByID(iid)
				if ix == nil {
					continue
				}
				for _, k := range ks {
					t.del(d.rkey(st, iid, k, pk))
					t.adjustIndex(st, ix, -idxCost(len(k), len(pk)))
				}
			}
			t.del(d.rkey(st, iidReverse, pk))
		}
	}
	t.del(rk)
	ob, osz, _ := valueInfo(old)
	t.free(ob)
	t.adjust(st, -1, -recCost(len(pk), osz))
}

// doClear empties a store in O(1): the store moves to a fresh physical id and the old range becomes garbage.
func (d *Database) doClear(t *Tx, st *store) {
	oldID, oldCount, oldRec := st.id, st.count, st.bytes
	oldIdx := map[*index]int64{}
	freed := oldRec
	for _, ix := range st.idx {
		oldIdx[ix] = ix.bytes
		freed += ix.bytes
		ix.bytes = 0
	}
	st.id = uint32(d.o.allocID())
	st.count, st.bytes = 0, 0
	t.delta -= freed
	t.wrote = true
	t.garbage = append(t.garbage, [2]uint32{oldID, iidAll})
	t.schemaDirty = true // the new physical id is part of the schema record
	t.markDirty(st)
	t.addUndo(func() {
		st.id, st.count, st.bytes = oldID, oldCount, oldRec
		for ix, b := range oldIdx {
			ix.bytes = b
		}
		t.delta += freed
		t.garbage = t.garbage[:len(t.garbage)-1]
	})
}

// bumpGen moves the key generator past an explicit numeric key.
func (d *Database) bumpGen(t *Tx, st *store, pk []byte) {
	if !st.auto {
		return
	}
	v, ok := storageNumber(pk)
	if !ok {
		return
	}
	var ng int64
	switch {
	case v >= float64(maxGen):
		ng = maxGen + 1
	case v < 0:
		return
	default:
		ng = int64(math.Floor(v)) + 1
	}
	if ng > st.gen {
		d.setGen(t, st, ng)
	}
}

func (d *Database) setGen(t *Tx, st *store, n int64) {
	old := st.gen
	st.gen = n
	t.wrote = true
	t.markDirty(st)
	t.addUndo(func() { st.gen = old })
}

// ---- operations on a transaction ---------------------------------------------------------------

func (m *Manager) txFor(txID int64, write bool) (*Tx, error) {
	t := m.tx(txID)
	if t == nil {
		return nil, errf("TransactionInactiveError", "The transaction has finished.")
	}
	return t, nil
}

// withTx runs f on an unfinished, started transaction under the database lock.
func (m *Manager) withTx(txID int64, write bool, f func(t *Tx, d *Database) error) error {
	t, err := m.txFor(txID, write)
	if err != nil {
		return err
	}
	d := t.db
	d.mu.Lock()
	defer d.unlock()
	if t.done || !t.started {
		return errf("TransactionInactiveError", "The transaction is not running.")
	}
	if write && t.mode == ModeReadOnly {
		return errf("ReadOnlyError", "The transaction is read-only.")
	}
	t.ioErr = nil
	err = f(t, d)
	if err == nil && t.ioErr != nil {
		err = errf("UnknownError", "%v", t.ioErr)
	}
	return err
}

func (t *Tx) store(d *Database, name string) (*store, error) {
	st := d.stores[name]
	if st == nil || (t.scope != nil && !t.scope[name]) {
		return nil, errf("NotFoundError", "The object store '%s' was not found.", name)
	}
	return st, nil
}

// Meta describes the schema as the transaction (or connection) sees it.
func (m *Manager) Meta(connID int64) (map[string]any, error) {
	c := m.conn(connID)
	if c == nil {
		return nil, errf("InvalidStateError", "The database connection is closed.")
	}
	d := c.db
	d.mu.Lock()
	defer d.unlock()
	return d.metaLocked(), nil
}

func (d *Database) metaLocked() map[string]any {
	names := make([]string, 0, len(d.stores))
	for n := range d.stores {
		names = append(names, n)
	}
	sort.Strings(names)
	stores := make([]any, 0, len(names))
	for _, n := range names {
		stores = append(stores, storeMeta(d.stores[n]))
	}
	return map[string]any{"version": d.version, "stores": stores}
}

func storeMeta(st *store) map[string]any {
	inames := make([]string, 0, len(st.idx))
	for n := range st.idx {
		inames = append(inames, n)
	}
	sort.Strings(inames)
	idx := make([]any, 0, len(inames))
	for _, n := range inames {
		ix := st.idx[n]
		idx = append(idx, map[string]any{"name": ix.name, "keyPath": ix.keyPath, "unique": ix.unique, "multiEntry": ix.multi})
	}
	return map[string]any{"name": st.name, "keyPath": st.keyPath, "hasKeyPath": st.hasKP, "autoIncrement": st.auto, "indexes": idx}
}

// StoreMeta returns one store's schema as the transaction sees it.
func (m *Manager) StoreMeta(txID int64, name string) (map[string]any, error) {
	t := m.tx(txID)
	if t == nil {
		return nil, errf("InvalidStateError", "The transaction has finished.")
	}
	d := t.db
	d.mu.Lock()
	defer d.unlock()
	// schema only changes in version change transactions, which run alone: a waiting transaction may ask
	st, err := t.store(d, name)
	if err != nil {
		return nil, err
	}
	return storeMeta(st), nil
}

func (m *Manager) CreateStore(txID int64, name, kp string, hasKP, auto bool) error {
	return m.withTx(txID, true, func(t *Tx, d *Database) error {
		if t.mode != ModeVersionChange {
			return errf("InvalidStateError", "Not in a version change transaction.")
		}
		return d.doCreateStore(t, name, kp, hasKP, auto)
	})
}

func (m *Manager) DeleteStore(txID int64, name string) error {
	return m.withTx(txID, true, func(t *Tx, d *Database) error {
		st := d.stores[name]
		if st == nil || t.mode != ModeVersionChange {
			return errf("NotFoundError", "The object store '%s' was not found.", name)
		}
		d.doDeleteStore(t, st)
		return nil
	})
}

func (m *Manager) RenameStore(txID int64, from, to string) error {
	return m.withTx(txID, true, func(t *Tx, d *Database) error {
		st := d.stores[from]
		if st == nil {
			return errf("NotFoundError", "The object store '%s' was not found.", from)
		}
		if from == to {
			return nil
		}
		if d.stores[to] != nil {
			return errf("ConstraintError", "An object store with the specified name already exists.")
		}
		d.doRenameStore(t, st, to)
		return nil
	})
}

// AllRecords lists the (key, value) pairs of a store, for populating a new index. It materialises the store:
// RecordsPage is the bounded form.
func (m *Manager) AllRecords(txID int64, storeName string) ([]any, error) {
	var out []any
	err := m.withTx(txID, false, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		out = make([]any, 0, st.count*2)
		var last string
		for {
			page, next, err := d.recordsPage(t, st, "", last, 512)
			if err != nil {
				return err
			}
			out = append(out, page...)
			if next == "" {
				return nil
			}
			last = next
		}
	})
	return out, err
}

// RecordsPage returns up to limit (key, value) pairs of a store whose key is greater than after ("" = from the
// start) and the key to continue after ("" when there is no more). Index population reads a store this way
// without holding it in memory.
func (m *Manager) RecordsPage(txID int64, storeName, after string, limit int) (page []any, next string, err error) {
	err = m.withTx(txID, false, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		page, next, err = d.recordsPage(t, st, "", after, limit)
		return err
	})
	return
}

func (d *Database) recordsPage(t *Tx, st *store, _ string, after string, limit int) ([]any, string, error) {
	if limit <= 0 {
		limit = 512
	}
	base := d.rkey(st, iidRecord)
	seek := base
	if after != "" {
		ak, err := WireToStorage(after)
		if err != nil {
			return nil, "", errf("DataError", "The parameter is not a valid key.")
		}
		seek = append(append([]byte(nil), base...), ak...)
		seek = append(seek, 0)
	}
	hi := prefixEnd(base)
	v := t.view()
	if v == nil {
		return nil, "", nil
	}
	var out []any
	var last string
	ok := v.SeekGE(seek)
	for ; ok && bytes.Compare(v.Key(), hi) < 0 && len(out) < limit*2; ok = v.Next() {
		w, err := StorageToWire(v.Key()[dataPrefixLen:])
		if err != nil {
			continue
		}
		s, err := d.o.unpackValue(v.Value())
		if err != nil {
			return nil, "", errf("UnknownError", "%v", err)
		}
		out = append(out, w, string(s))
		last = w
	}
	if ok && bytes.Compare(v.Key(), hi) < 0 {
		return out, last, nil
	}
	return out, "", nil
}

func (m *Manager) CreateIndex(txID int64, storeName, name, kp string, unique, multi bool, ents []idxEntry) error {
	return m.withTx(txID, true, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		return d.doCreateIndex(t, st, name, kp, unique, multi, ents)
	})
}

func (m *Manager) DeleteIndex(txID int64, storeName, name string) error {
	return m.withTx(txID, true, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		if st.idx[name] == nil {
			return errf("NotFoundError", "The index '%s' was not found.", name)
		}
		d.doDeleteIndex(t, st, name)
		return nil
	})
}

func (m *Manager) RenameIndex(txID int64, storeName, from, to string) error {
	return m.withTx(txID, true, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		if st.idx[from] == nil {
			return errf("NotFoundError", "The index '%s' was not found.", from)
		}
		if from == to {
			return nil
		}
		if st.idx[to] != nil {
			return errf("ConstraintError", "An index with the specified name already exists.")
		}
		d.doRenameIndex(t, st, from, to)
		return nil
	})
}

// GenKey hands out the next generated key of an auto-increment store (the encoded key).
func (m *Manager) GenKey(txID int64, storeName string) (string, error) {
	var out string
	err := m.withTx(txID, true, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		if !st.auto {
			return errf("DataError", "The object store does not use a key generator.")
		}
		if st.gen > maxGen {
			return errf("ConstraintError", "The key generator has reached its maximum value.")
		}
		out = NumberKey(float64(st.gen)).Wire()
		d.setGen(t, st, st.gen+1)
		return nil
	})
	return out, err
}

// Put stores a record. key == "" asks the store's key generator (out-of-line keys with autoIncrement).
// It returns the key the record was stored under.
func (m *Manager) Put(txID int64, storeName, key, val string, noOverwrite bool, ik map[string][]string) (string, error) {
	var out string
	err := m.withTx(txID, true, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		var pk []byte
		if key == "" {
			if !st.auto {
				return errf("DataError", "The object store uses out-of-line keys and has no key generator: a key must be provided.")
			}
			if st.gen > maxGen {
				return errf("ConstraintError", "The key generator has reached its maximum value.")
			}
			pk = NumberKey(float64(st.gen)).Storage()
			d.setGen(t, st, st.gen+1)
			out = NumberKey(float64(st.gen - 1)).Wire()
		} else {
			if pk, err = WireToStorage(key); err != nil {
				return errf("DataError", "The parameter is not a valid key.")
			}
			d.bumpGen(t, st, pk)
			out = key
		}
		return d.doPut(t, st, pk, []byte(val), st.indexKeysIn(ik), noOverwrite)
	})
	if err != nil {
		out = ""
	}
	return out, err
}

// Range is a key range in the prelude's wire form; an empty bound is unbounded.
type Range struct {
	Lo, Hi         string
	LoOpen, HiOpen bool
}

func (r Range) contains(k string) bool {
	if r.Lo != "" && (k < r.Lo || (r.LoOpen && k == r.Lo)) {
		return false
	}
	if r.Hi != "" && (k > r.Hi || (r.HiOpen && k == r.Hi)) {
		return false
	}
	return true
}

// bounds turns a range into the half-open byte interval [lo, hi) of the items of one keyspace (a store's
// records when iid is iidRecord, otherwise an index).
func (d *Database) bounds(st *store, iid uint32, r Range) (lo, hi []byte, err error) {
	base := d.rkey(st, iid)
	lo, hi = base, prefixEnd(base)
	isIdx := iid != iidRecord
	if r.Lo != "" {
		k, e := WireToStorage(r.Lo)
		if e != nil {
			return nil, nil, errf("DataError", "The parameter is not a valid key.")
		}
		lo = append(append([]byte(nil), base...), k...)
		if r.LoOpen {
			if isIdx {
				lo = prefixEnd(lo)
			} else {
				lo = append(lo, 0)
			}
		}
	}
	if r.Hi != "" {
		k, e := WireToStorage(r.Hi)
		if e != nil {
			return nil, nil, errf("DataError", "The parameter is not a valid key.")
		}
		hi = append(append([]byte(nil), base...), k...)
		if !r.HiOpen {
			if isIdx {
				hi = prefixEnd(hi)
			} else {
				hi = append(hi, 0)
			}
		}
	}
	return lo, hi, nil
}

// DeleteRange removes the records of a store whose key lies in the range.
func (m *Manager) DeleteRange(txID int64, storeName string, r Range) error {
	return m.withTx(txID, true, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		if r.Lo == "" && r.Hi == "" {
			d.doClear(t, st)
			return nil
		}
		lo, hi, err := d.bounds(st, iidRecord, r)
		if err != nil {
			return err
		}
		// in chunks: deleting while the view is positioned would be fine, but a chunk keeps the keys we hold bounded
		for bytes.Compare(lo, hi) < 0 {
			v := t.view()
			if v == nil {
				return nil
			}
			var pks [][]byte
			for ok := v.SeekGE(lo); ok && bytes.Compare(v.Key(), hi) < 0 && len(pks) < 1024; ok = v.Next() {
				pks = append(pks, append([]byte(nil), v.Key()[dataPrefixLen:]...))
			}
			if len(pks) == 0 {
				break
			}
			for _, pk := range pks {
				d.doDelete(t, st, pk)
			}
			lo = append(d.rkey(st, iidRecord, pks[len(pks)-1]), 0)
			if len(pks) < 1024 {
				break
			}
		}
		return nil
	})
}

func (m *Manager) Clear(txID int64, storeName string) error {
	return m.withTx(txID, true, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		d.doClear(t, st)
		return nil
	})
}

// Query kinds.
const (
	QValue   = 'v' // first value
	QKey     = 'k' // first primary key
	QValues  = 'V' // all values (up to count)
	QKeys    = 'K' // all primary keys (up to count)
	QCount   = 'n'
	QRecords = 'R' // key, primary key, value triples
)

// itemKeys splits the key of an index item (after the 13-byte prefix) into index key and primary key.
func splitIndexItem(rest []byte) (ik, pk []byte, ok bool) {
	n := keyLen(rest)
	if n <= 0 || n >= len(rest) {
		return nil, nil, false
	}
	return rest[:n], rest[n:], true
}

// keyLen is the length of the storage-form key at the front of b (0 if malformed).
func keyLen(b []byte) int {
	n, ok := skipKey(b, 0)
	if !ok {
		return 0
	}
	return n
}

func skipKey(b []byte, depth int) (int, bool) {
	if len(b) == 0 || depth > 64 {
		return 0, false
	}
	switch b[0] {
	case KeyNumber, KeyDate:
		if len(b) < 9 {
			return 0, false
		}
		return 9, true
	case KeyString, KeyBinary:
		for i := 1; i < len(b); i++ {
			if b[i] == 0 {
				if i+1 >= len(b) {
					return 0, false
				}
				if b[i+1] == 0x01 {
					return i + 2, true
				}
				i++ // 00 FF
			}
		}
		return 0, false
	case KeyArray:
		i := 1
		for i < len(b) {
			if b[i] == 0 {
				return i + 1, true
			}
			n, ok := skipKey(b[i:], depth+1)
			if !ok {
				return 0, false
			}
			i += n
		}
	}
	return 0, false
}

func (d *Database) valueString(stored []byte) (string, error) {
	p, err := d.o.unpackValue(stored)
	if err != nil {
		return "", errf("UnknownError", "%v", err)
	}
	return string(p), nil
}

// Query reads from a store, or from one of its indexes (indexName != "").
// The result is a slice of strings: for QCount, a single decimal string.
func (m *Manager) Query(txID int64, storeName, indexName string, kind byte, r Range, count int) ([]any, error) {
	var out []any
	err := m.withTx(txID, false, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		limit := count
		if limit <= 0 {
			limit = math.MaxInt
		}
		switch kind {
		case QValue, QKey:
			limit = 1
		}
		iid := uint32(iidRecord)
		var ix *index
		if indexName != "" {
			if ix = st.idx[indexName]; ix == nil {
				return errf("NotFoundError", "The index '%s' was not found.", indexName)
			}
			iid = ix.id
		}
		if kind == QCount && indexName == "" && r.Lo == "" && r.Hi == "" {
			out = []any{strconv.FormatInt(st.count, 10)}
			return nil
		}
		lo, hi, err := d.bounds(st, iid, r)
		if err != nil {
			return err
		}
		if bytes.Compare(lo, hi) >= 0 {
			if kind == QCount {
				out = []any{"0"}
			}
			return nil
		}
		v := t.view()
		if v == nil {
			return nil
		}
		var n int64
		for ok := v.SeekGE(lo); ok && bytes.Compare(v.Key(), hi) < 0 && limit > 0; ok = v.Next() {
			if kind == QCount {
				n++
				continue
			}
			rest := v.Key()[dataPrefixLen:]
			if indexName == "" {
				switch kind {
				case QValue, QValues:
					s, err := d.valueString(v.Value())
					if err != nil {
						return err
					}
					out = append(out, s)
				case QKey, QKeys:
					w, _ := StorageToWire(rest)
					out = append(out, w)
				case QRecords:
					w, _ := StorageToWire(rest)
					s, err := d.valueString(v.Value())
					if err != nil {
						return err
					}
					out = append(out, w, w, s)
				}
				limit--
				continue
			}
			ik, pk, good := splitIndexItem(rest)
			if !good {
				continue
			}
			switch kind {
			case QKey, QKeys:
				w, _ := StorageToWire(pk)
				out = append(out, w)
			case QValue, QValues, QRecords:
				rec, found := t.get(d.rkey(st, iidRecord, pk))
				if !found {
					continue
				}
				s, err := d.valueString(rec)
				if err != nil {
					return err
				}
				if kind == QRecords {
					wi, _ := StorageToWire(ik)
					wp, _ := StorageToWire(pk)
					out = append(out, wi, wp, s)
				} else {
					out = append(out, s)
				}
			}
			limit--
		}
		if kind == QCount {
			out = []any{strconv.FormatInt(n, 10)}
		}
		return nil
	})
	return out, err
}

// Cursor directions.
const (
	DirNext       = 0
	DirNextUnique = 1
	DirPrev       = 2
	DirPrevUnique = 3
)

// CursorReq positions a cursor step. Cur* is the current position (empty on the
// first step); Target* is the key passed to continue()/continuePrimaryKey().
type CursorReq struct {
	Index    string
	Range    Range
	Dir      int
	CurKey   string // for index cursors the index key; for store cursors the primary key
	CurPK    string
	HasCur   bool
	Target   string
	TargetPK string
	Advance  int // number of steps (at least 1)
	KeyOnly  bool
}

func succ(k []byte) []byte { return append(append([]byte(nil), k...), 0) }

func cat(parts ...[]byte) []byte {
	var n int
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// Cursor moves a cursor and returns [key, primaryKey, value] or nil at the end. It is stateless: the current
// position comes with the request and the step is a seek in the key range plus at most Advance moves, so a
// cursor over any number of records costs the same memory as one over three.
func (m *Manager) Cursor(txID int64, storeName string, q CursorReq) ([]any, error) {
	var out []any
	err := m.withTx(txID, false, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		out, err = d.cursorStep(t, st, q)
		return err
	})
	return out, err
}

func (d *Database) cursorStep(t *Tx, st *store, q CursorReq) ([]any, error) {
	isIdx := q.Index != ""
	iid := uint32(iidRecord)
	if isIdx {
		ix := st.idx[q.Index]
		if ix == nil {
			return nil, errf("NotFoundError", "The index '%s' was not found.", q.Index)
		}
		iid = ix.id
	} else {
		q.CurPK, q.TargetPK = "", "" // a store cursor is positioned by its primary key alone
	}
	base := d.rkey(st, iid)
	lo, hi, err := d.bounds(st, iid, q.Range)
	if err != nil {
		return nil, err
	}
	if bytes.Compare(lo, hi) >= 0 {
		return nil, nil
	}
	conv := func(w string) ([]byte, error) {
		if w == "" {
			return nil, nil
		}
		k, err := WireToStorage(w)
		if err != nil {
			return nil, errf("DataError", "The parameter is not a valid key.")
		}
		return k, nil
	}
	curK, e1 := conv(q.CurKey)
	curPK, e2 := conv(q.CurPK)
	tgt, e3 := conv(q.Target)
	tgtPK, e4 := conv(q.TargetPK)
	for _, e := range []error{e1, e2, e3, e4} {
		if e != nil {
			return nil, e
		}
	}
	steps := q.Advance
	if steps < 1 {
		steps = 1
	}
	forward := q.Dir == DirNext || q.Dir == DirNextUnique
	unique := (q.Dir == DirNextUnique || q.Dir == DirPrevUnique) && isIdx
	v := t.view()
	if v == nil {
		return nil, nil
	}
	ikOf := func(key []byte) []byte {
		ik, _, _ := splitIndexItem(key[dataPrefixLen:])
		return ik
	}
	var ok bool
	if forward {
		seek := lo
		if q.HasCur {
			switch {
			case isIdx && unique:
				seek = prefixEnd(cat(base, curK))
			case isIdx:
				seek = succ(cat(base, curK, curPK))
			default:
				seek = succ(cat(base, curK))
			}
		}
		if len(tgt) > 0 {
			ts := cat(base, tgt)
			if isIdx && len(tgtPK) > 0 {
				ts = cat(base, tgt, tgtPK)
			}
			if bytes.Compare(ts, seek) > 0 {
				seek = ts
			}
		}
		if bytes.Compare(seek, lo) < 0 {
			seek = lo
		}
		ok = v.SeekGE(seek)
		for s := 1; s < steps && ok && bytes.Compare(v.Key(), hi) < 0; s++ {
			if unique {
				ok = v.SeekGE(prefixEnd(cat(base, ikOf(v.Key()))))
			} else {
				ok = v.Next()
			}
		}
		ok = ok && bytes.Compare(v.Key(), hi) < 0
	} else {
		bound := hi // exclusive: the item sought is the last one below it
		if q.HasCur {
			cb := cat(base, curK)
			if isIdx && !unique {
				cb = cat(base, curK, curPK)
			}
			if bytes.Compare(cb, bound) < 0 {
				bound = cb
			}
		}
		if len(tgt) > 0 {
			var tb []byte
			switch {
			case isIdx && len(tgtPK) > 0:
				tb = succ(cat(base, tgt, tgtPK))
			case isIdx:
				tb = prefixEnd(cat(base, tgt))
			default:
				tb = succ(cat(base, tgt))
			}
			if bytes.Compare(tb, bound) < 0 {
				bound = tb
			}
		}
		ok = v.SeekLT(bound) && bytes.Compare(v.Key(), lo) >= 0
		groupFirst := func() {
			// prevunique lands on the first entry of the index key's group, or of its part inside the range
			gs := cat(base, ikOf(v.Key()))
			if bytes.Compare(gs, lo) < 0 {
				gs = lo
			}
			ok = v.SeekGE(gs)
		}
		if ok && unique {
			groupFirst()
		}
		for s := 1; s < steps && ok; s++ {
			if unique {
				gs := cat(base, ikOf(v.Key()))
				ok = v.SeekLT(gs) && bytes.Compare(v.Key(), lo) >= 0
				if ok {
					groupFirst()
				}
			} else {
				ok = v.Prev() && bytes.Compare(v.Key(), lo) >= 0
			}
		}
	}
	if t.ioErr != nil || !ok {
		return nil, nil
	}
	rest := v.Key()[dataPrefixLen:]
	var wk, wp string
	var pk []byte
	if isIdx {
		ik, p, good := splitIndexItem(rest)
		if !good {
			return nil, nil
		}
		pk = p
		wk, _ = StorageToWire(ik)
		wp, _ = StorageToWire(p)
	} else {
		pk = rest
		wk, _ = StorageToWire(rest)
		wp = wk
	}
	res := []any{wk, wp}
	if !q.KeyOnly {
		var stored []byte
		if isIdx {
			s, found := t.get(d.rkey(st, iidRecord, pk))
			if found {
				stored = s
			}
		} else {
			stored = v.Value()
		}
		if stored == nil {
			res = append(res, "")
		} else {
			s, err := d.valueString(stored)
			if err != nil {
				return nil, err
			}
			res = append(res, s)
		}
	}
	return res, nil
}

// ParseRange builds a Range from a host argument slice [lo, hi, loOpen, hiOpen].
func ParseRange(lo, hi string, loOpen, hiOpen bool) Range { return Range{lo, hi, loOpen, hiOpen} }

// IsError reports whether err is the IndexedDB error with the given name.
func IsError(err error, name string) bool {
	e, ok := err.(*Error)
	return ok && e.Name == name
}
