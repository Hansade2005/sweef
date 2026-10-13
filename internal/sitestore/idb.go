package sitestore

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/iterator"
)

// This file is the concurrency core of IndexedDB: connections, the
// version-change handshake, which transactions may run together, snapshots and
// commit. The data itself (records, indexes, cursors) is in idbdata.go; it
// lives in the origin's ordered store (kv.go), nothing of a database is kept
// in memory but its schema.
//
// The JavaScript side (the engine's idb prelude) owns everything the web
// platform defines in terms of script: structured clone, key extraction from
// values, request and event objects, transaction activity. This side owns what
// has to be shared between tabs and survive a restart: the records, their
// order, the indexes, the key generators, the rules for which transactions may
// run together, the version-change handshake and the durability.
//
// Keys cross this boundary as the prelude's order-preserving strings (keys.go
// converts them to the compact form used inside the store); a value is a
// serialised structured clone and is opaque here.
//
// Transactions. A transaction reads from a snapshot of the store taken when
// it first reads (nothing it is allowed to see can change before that: an
// overlapping writer is scheduled before or after it, never beside it) and
// buffers its writes in an ordered overlay that it reads through. Commit turns
// the overlay, the changed schema and counters, and the usage change into one
// atomic batch; abort throws the overlay away. A crash therefore leaves a
// whole transaction or none.

// Msg is a notification to a connection's owner (a script VM). It is delivered
// through Sink.Notify and is plain JSON-compatible data.
type Msg = map[string]any

// Sink receives notifications. Notify must not block and must not call back
// into the store from the same goroutine.
type Sink interface{ Notify(m Msg) }

// Error is an IndexedDB error: Name is the DOMException name.
type Error struct{ Name, Msg string }

func (e *Error) Error() string { return e.Name + ": " + e.Msg }

func errf(name, f string, a ...any) error { return &Error{name, fmt.Sprintf(f, a...)} }

// Transaction modes.
const (
	ModeReadOnly      = 0
	ModeReadWrite     = 1
	ModeVersionChange = 2
)

const maxGen = int64(1) << 53

type index struct {
	id      uint32
	name    string
	keyPath string // JSON text of the key path
	unique  bool
	multi   bool
	bytes   int64 // what its entries cost
}

type store struct {
	name    string
	id      uint32 // physical id: changes when the store is cleared (see doClear)
	keyPath string // JSON text of the key path; "" for out-of-line keys
	hasKP   bool
	auto    bool
	gen     int64 // next key the generator hands out (starts at 1)
	count   int64 // records
	bytes   int64 // what the records cost
	nextIdx uint32
	idx     map[string]*index
}

func newStore(name string, id uint32, kp string, hasKP, auto bool) *store {
	return &store{name: name, id: id, keyPath: kp, hasKP: hasKP, auto: auto, gen: 1, nextIdx: iidFirst, idx: map[string]*index{}}
}

func (s *store) indexByID(id uint32) *index {
	for _, ix := range s.idx {
		if ix.id == id {
			return ix
		}
	}
	return nil
}

// Database is one named database of an origin. Its mutex guards everything below it.
type Database struct {
	o    *Origin
	name string

	mu      sync.Mutex
	id      uint32 // physical id, 0 until the database is first created
	version int64
	exists  bool // committed at least once (an aborted first upgrade leaves nothing)
	stores  map[string]*store
	conns   map[int64]*Conn
	txs     []*Tx
	reqs    []*openReq
	out     []outMsg
}

type outMsg struct {
	sink Sink
	msg  Msg
}

func (d *Database) notify(s Sink, m Msg) { d.out = append(d.out, outMsg{s, m}) }

// unlock releases the database lock and then delivers the queued notifications.
func (d *Database) unlock() {
	out := d.out
	d.out = nil
	d.mu.Unlock()
	for _, o := range out {
		o.sink.Notify(o.msg)
	}
}

// Conn is an open connection (IDBDatabase).
type Conn struct {
	id           int64
	db           *Database
	sink         Sink
	kv           *kvStore
	closePending bool
	closed       bool
	ntx          int
	version      int64
}

// Tx is a transaction.
type Tx struct {
	id      int64
	db      *Database
	conn    *Conn
	mode    int
	scope   map[string]bool // nil for a version change transaction: every store
	started bool
	done    bool
	undo    []func()
	upgrade *openReq
	fresh   bool // being created: Begin reports its start itself

	snap *leveldb.Snapshot
	ov   *overlay
	vw   *view

	ioErr       error
	strict      bool // commit with a synchronous write (IDBTransaction durability "strict")
	wrote       bool
	delta       int64
	schemaDirty bool
	dirty       map[*store]bool
	garbage     [][2]uint32 // (store id, index id | iidAll) ranges to delete in the background
	freed       []uint64    // blobs the commit unregisters
	created     []uint64    // blobs written by this transaction (removed again on abort)
}

type openReq struct {
	id      int64
	sink    Sink
	del     bool
	version int64
	state   int
	waitAck map[int64]bool
	conn    *Conn
	oldV    int64
	newV    int64
	blocked bool
}

func (t *Tx) conflicts(u *Tx) bool {
	if t.mode == ModeReadOnly && u.mode == ModeReadOnly {
		return false
	}
	if t.scope == nil || u.scope == nil {
		return true
	}
	for n := range t.scope {
		if u.scope[n] {
			return true
		}
	}
	return false
}

// ---- opening, closing, deleting --------------------------------------------------

// Open queues an open request. The outcome arrives through sink as one of:
// open-error, open-ready (with upgrade true or false), and possibly blocked and
// versionchange notifications to other connections first. reqID is echoed back.
func (d *Database) Open(reqID int64, sink Sink, version int64) {
	d.mu.Lock()
	d.reqs = append(d.reqs, &openReq{id: reqID, sink: sink, version: version})
	d.pump()
	d.unlock()
}

// Delete queues a deleteDatabase request: delete-done or delete-error arrives through sink.
func (d *Database) Delete(reqID int64, sink Sink) {
	d.mu.Lock()
	d.reqs = append(d.reqs, &openReq{id: reqID, sink: sink, del: true})
	d.pump()
	d.unlock()
}

func (d *Database) openConns() []*Conn {
	var out []*Conn
	for _, c := range d.conns {
		if !c.closed {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// newConn opens a connection; it keeps the origin's store open until it is closed.
func (d *Database) newConn(sink Sink) (*Conn, error) {
	k, err := d.o.acquire()
	if err != nil {
		return nil, err
	}
	c := &Conn{id: d.o.m.nextID.Add(1), db: d, sink: sink, kv: k, version: d.version}
	d.conns[c.id] = c
	d.o.m.regConn(c)
	d.o.conns.Add(1)
	d.o.access()
	return c, nil
}

// pump advances the head of the request queue as far as it can go. Caller holds d.mu.
func (d *Database) pump() {
	for len(d.reqs) > 0 {
		if !d.step(d.reqs[0]) {
			return
		}
		d.reqs = d.reqs[1:]
	}
}

// step returns true when the request is finished and may leave the queue.
func (d *Database) step(r *openReq) bool {
	cur := d.version
	if !d.exists {
		cur = 0
	}
	if r.state == 0 {
		if r.del {
			if !d.exists && len(d.openConns()) == 0 {
				d.notify(r.sink, Msg{"t": "delete-done", "req": r.id, "oldVersion": int64(0)})
				return true
			}
			r.oldV = cur
		} else {
			want := r.version
			if want == 0 {
				if cur == 0 {
					want = 1
				} else {
					want = cur
				}
			}
			if cur > want {
				d.notify(r.sink, Msg{"t": "open-error", "req": r.id, "name": "VersionError",
					"msg": fmt.Sprintf("The requested version (%d) is less than the existing version (%d).", want, cur)})
				return true
			}
			if cur == want {
				c, err := d.newConn(r.sink)
				if err != nil {
					d.notify(r.sink, Msg{"t": "open-error", "req": r.id, "name": "UnknownError", "msg": err.Error()})
					return true
				}
				d.notify(r.sink, Msg{"t": "open-ready", "req": r.id, "conn": c.id, "version": cur, "upgrade": false})
				return true
			}
			r.oldV, r.newV = cur, want
		}
		r.waitAck = map[int64]bool{}
		for _, c := range d.openConns() {
			if c.closePending {
				continue
			}
			m := Msg{"t": "versionchange", "conn": c.id, "oldVersion": r.oldV}
			if !r.del {
				m["newVersion"] = r.newV
			}
			d.notify(c.sink, m)
			r.waitAck[c.id] = true
		}
		r.state = 1
	}
	if r.state == 1 {
		if len(r.waitAck) > 0 {
			return false
		}
		if len(d.openConns()) > 0 && !r.blocked {
			r.blocked = true
			m := Msg{"t": "blocked", "req": r.id, "oldVersion": r.oldV}
			if !r.del {
				m["newVersion"] = r.newV
			}
			d.notify(r.sink, m)
		}
		r.state = 2
	}
	if r.state == 2 {
		if len(d.openConns()) > 0 {
			return false
		}
		r.state = 3
	}
	if r.state == 3 {
		if r.del {
			d.destroy()
			d.notify(r.sink, Msg{"t": "delete-done", "req": r.id, "oldVersion": r.oldV})
			return true
		}
		c, err := d.newConn(r.sink)
		if err != nil {
			d.notify(r.sink, Msg{"t": "open-error", "req": r.id, "name": "UnknownError", "msg": err.Error()})
			return true
		}
		r.conn = c
		tx := &Tx{id: d.o.m.nextID.Add(1), db: d, conn: c, mode: ModeVersionChange, started: true, upgrade: r}
		c.ntx++
		d.txs = append(d.txs, tx)
		d.o.m.regTx(tx)
		// the version changes with the transaction: an abort puts it back
		oldV, oldExists, oldID := d.version, d.exists, d.id
		if d.id == 0 {
			d.id = uint32(d.o.allocID())
		}
		d.version, d.exists = r.newV, true
		c.version = r.newV
		tx.schemaDirty, tx.wrote = true, true
		tx.undo = append(tx.undo, func() { d.version, d.exists, d.id = oldV, oldExists, oldID; c.version = oldV })
		d.notify(r.sink, Msg{"t": "open-ready", "req": r.id, "conn": c.id, "version": r.newV, "upgrade": true,
			"oldVersion": r.oldV, "tx": tx.id})
		r.state = 4
		return false
	}
	return false // state 4: the upgrade transaction finishing pops the request
}

// VersionChangeDone acknowledges a versionchange notification: the owner has run
// its event handlers (which usually closed the connection).
func (m *Manager) VersionChangeDone(connID int64) {
	c := m.conn(connID)
	if c == nil {
		return
	}
	d := c.db
	d.mu.Lock()
	if len(d.reqs) > 0 {
		delete(d.reqs[0].waitAck, connID)
	}
	d.pump()
	d.unlock()
}

// CloseConn marks a connection close-pending; it closes for real once its
// transactions are finished.
func (m *Manager) CloseConn(connID int64) {
	c := m.conn(connID)
	if c == nil {
		return
	}
	d := c.db
	d.mu.Lock()
	c.closePending = true
	if c.ntx == 0 {
		d.finalizeClose(c)
	}
	d.unlock()
}

func (d *Database) finalizeClose(c *Conn) {
	if c.closed {
		return
	}
	c.closed = true
	delete(d.conns, c.id)
	d.o.m.unregConn(c.id)
	if len(d.reqs) > 0 {
		delete(d.reqs[0].waitAck, c.id)
	}
	d.pump()
	d.o.conns.Add(-1)
	d.o.release(c.kv)
	d.o.m.trimOpen()
}

// forceClose aborts a connection's transactions and closes it (its owner went away).
func (d *Database) forceClose(c *Conn) {
	for _, t := range append([]*Tx(nil), d.txs...) {
		if t.conn == c && !t.done {
			d.abortTx(t)
		}
	}
	c.closePending = true
	d.finalizeClose(c)
}

// ---- transactions ----------------------------------------------------------------

// Begin creates a transaction on a connection. started tells whether it can run
// now; otherwise a txstart notification follows when it can.
func (m *Manager) Begin(connID int64, stores []string, mode int) (txID int64, started bool, err error) {
	c := m.conn(connID)
	if c == nil {
		return 0, false, errf("InvalidStateError", "The database connection is closed.")
	}
	d := c.db
	d.mu.Lock()
	defer d.unlock()
	if c.closePending || c.closed {
		return 0, false, errf("InvalidStateError", "The database connection is closing.")
	}
	t := &Tx{id: m.nextID.Add(1), db: d, conn: c, mode: mode}
	t.scope = map[string]bool{}
	for _, n := range stores {
		if d.stores[n] == nil {
			return 0, false, errf("NotFoundError", "One of the specified object stores was not found.")
		}
		t.scope[n] = true
	}
	c.ntx++
	d.txs = append(d.txs, t)
	m.regTx(t)
	t.fresh = true
	d.schedule()
	t.fresh = false
	return t.id, t.started, nil
}

// schedule starts every waiting transaction that no earlier unfinished one conflicts with.
func (d *Database) schedule() {
	for i, t := range d.txs {
		if t.started || t.done {
			continue
		}
		ok := true
		for _, e := range d.txs[:i] {
			if !e.done && e.conflicts(t) {
				ok = false
				break
			}
		}
		if ok {
			t.started = true
			if !t.fresh {
				d.notify(t.conn.sink, Msg{"t": "txstart", "tx": t.id})
			}
		}
	}
}

func (d *Database) finishTx(t *Tx) {
	t.done = true
	t.release()
	for i, e := range d.txs {
		if e == t {
			d.txs = append(d.txs[:i], d.txs[i+1:]...)
			break
		}
	}
	d.o.m.unregTx(t.id)
	t.conn.ntx--
	d.schedule()
	if t.conn.closePending && t.conn.ntx == 0 {
		d.finalizeClose(t.conn)
	}
	if t.upgrade != nil {
		r := t.upgrade
		if len(d.reqs) > 0 && d.reqs[0] == r {
			d.reqs = d.reqs[1:]
		}
		d.pump()
	}
}

// release drops the transaction's snapshot and iterators.
func (t *Tx) release() {
	if t.vw != nil {
		t.vw.release()
		t.vw = nil
	}
	if t.snap != nil {
		t.snap.Release()
		t.snap = nil
	}
	t.ov = nil
}

func (d *Database) abortTx(t *Tx) {
	if t.done {
		return
	}
	for i := len(t.undo) - 1; i >= 0; i-- {
		t.undo[i]()
	}
	t.undo = nil
	d.o.dropBlobFiles(t.created) // written for a commit that never happened
	t.created, t.freed, t.garbage = nil, nil, nil
	if t.upgrade != nil {
		// an aborted upgrade closes its connection
		t.conn.closePending = true
	}
	d.finishTx(t)
}

// Abort rolls a transaction back.
func (m *Manager) Abort(txID int64) {
	t := m.tx(txID)
	if t == nil {
		return
	}
	d := t.db
	d.mu.Lock()
	d.abortTx(t)
	d.unlock()
}

// SetDurability marks a transaction strict: its commit returns only once the data is on stable storage
// (IDBTransaction durability "strict"). The default commit reaches the journal, which survives a crash of the
// process and is written out with the next synchronous commit, when a store is closed and by the operating
// system; a version change always commits strictly.
func (m *Manager) SetDurability(txID int64, strict bool) {
	if t := m.tx(txID); t != nil {
		t.db.mu.Lock()
		t.strict = strict
		t.db.mu.Unlock()
	}
}

// Commit makes a transaction durable. A QuotaExceededError leaves it uncommitted: the caller aborts it.
func (m *Manager) Commit(txID int64) error {
	t := m.tx(txID)
	if t == nil {
		return errf("InvalidStateError", "The transaction is finished.")
	}
	d := t.db
	d.mu.Lock()
	if t.done {
		d.unlock()
		return errf("InvalidStateError", "The transaction is finished.")
	}
	delta, wrote := t.delta, t.wrote
	d.unlock()
	if wrote && delta > 0 {
		// outside the database lock: making room may evict other origins
		if err := d.o.reserve(delta); err != nil {
			return err
		}
	}
	d.mu.Lock()
	defer d.unlock()
	if t.done {
		return errf("InvalidStateError", "The transaction is finished.")
	}
	if wrote {
		if err := d.commitTx(t); err != nil {
			return err
		}
	}
	d.finishTx(t)
	return nil
}

// commitTx writes everything a transaction changed as one atomic batch.
func (d *Database) commitTx(t *Tx) error {
	o := d.o
	var b leveldb.Batch
	if t.ov != nil {
		it := t.ov.db.NewIterator(nil)
		for it.Next() {
			v := it.Value()
			if len(v) > 0 && v[0] == ovPut {
				b.Put(it.Key(), v[1:])
			} else {
				b.Delete(it.Key())
			}
		}
		it.Release()
	}
	if t.schemaDirty {
		b.Put(regKey(d.name), d.encodeSchema())
	}
	for st := range t.dirty {
		if d.stores[st.name] == st {
			b.Put(stateKey(d.id, st.id), encState(st))
		}
	}
	for _, g := range t.garbage {
		b.Put(garbageKey(d.id, g[0], g[1]), nil) // the store's state record is inside the range being dropped
	}
	for _, id := range t.freed {
		b.Delete(blobKey(id))
	}
	if err := o.commitBatch(t.conn.kv, &b, t.delta, 0, 0, t.mode == ModeVersionChange || t.strict); err != nil {
		return errf("UnknownError", "%v", err)
	}
	o.dropBlobFiles(t.freed)
	t.created, t.freed = nil, nil
	if len(t.garbage) > 0 {
		o.kickGC()
	}
	o.access()
	return nil
}

// destroy forgets the database and deletes its data (deleteDatabase): one atomic batch removes the registry
// entry and marks every store's data for background deletion.
func (d *Database) destroy() {
	if d.id != 0 && d.exists {
		if k, err := d.o.acquire(); err == nil {
			var b leveldb.Batch
			b.Delete(regKey(d.name))
			var freed int64
			for _, st := range d.stores {
				b.Put(garbageKey(d.id, st.id, iidAll), nil)
				freed += st.bytes
				for _, ix := range st.idx {
					freed += ix.bytes
				}
			}
			_ = d.o.commitBatch(k, &b, -freed, 0, 0, true)
			d.o.release(k)
			d.o.kickGC()
		}
	}
	d.stores = map[string]*store{}
	d.version, d.exists, d.id = 0, false, 0
}

// ---- schema records --------------------------------------------------------------------

func regKey(name string) []byte { return append([]byte{pDBReg}, name...) }

// stateKey is the key of a store's state record: the last key of the store's range (see origin.go).
func stateKey(db, store uint32) []byte { return dataPrefix(db, store, iidState) }

// encodeSchema is the registry record: the version, the database id and the schema of every store.
func (d *Database) encodeSchema() []byte {
	var w wbuf
	w.byte(1)
	w.i(d.version)
	w.u(uint64(d.id))
	names := make([]string, 0, len(d.stores))
	for n := range d.stores {
		names = append(names, n)
	}
	sort.Strings(names)
	w.u(uint64(len(names)))
	for _, n := range names {
		st := d.stores[n]
		w.s(st.name)
		w.u(uint64(st.id))
		w.s(st.keyPath)
		w.bool(st.hasKP)
		w.bool(st.auto)
		w.u(uint64(st.nextIdx))
		inames := make([]string, 0, len(st.idx))
		for in := range st.idx {
			inames = append(inames, in)
		}
		sort.Strings(inames)
		w.u(uint64(len(inames)))
		for _, in := range inames {
			ix := st.idx[in]
			w.u(uint64(ix.id))
			w.s(ix.name)
			w.s(ix.keyPath)
			w.bool(ix.unique)
			w.bool(ix.multi)
		}
	}
	return w.b
}

// encState is the mutable part of a store: key generator, record count and what records and indexes cost.
func encState(st *store) []byte {
	var w wbuf
	w.i(st.gen)
	w.i(st.count)
	w.i(st.bytes)
	w.u(uint64(len(st.idx)))
	ids := make([]*index, 0, len(st.idx))
	for _, ix := range st.idx {
		ids = append(ids, ix)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].id < ids[j].id })
	for _, ix := range ids {
		w.u(uint64(ix.id))
		w.i(ix.bytes)
	}
	return w.b
}

func (d *Database) decodeSchema(b []byte) error {
	r := rbuf{b: b}
	if r.byte() != 1 {
		return errCorrupt
	}
	d.version = r.i()
	d.id = uint32(r.u())
	n := int(r.u())
	d.stores = map[string]*store{}
	for i := 0; i < n && r.err == nil; i++ {
		st := &store{name: r.s(), id: uint32(r.u()), keyPath: r.s(), hasKP: r.bool(), auto: r.bool(), gen: 1, idx: map[string]*index{}}
		st.nextIdx = uint32(r.u())
		ni := int(r.u())
		for j := 0; j < ni && r.err == nil; j++ {
			ix := &index{id: uint32(r.u()), name: r.s(), keyPath: r.s(), unique: r.bool(), multi: r.bool()}
			st.idx[ix.name] = ix
		}
		d.stores[st.name] = st
	}
	return r.err
}

func (d *Database) loadStates(k *kvStore) {
	for _, st := range d.stores {
		v, ok := k.get(stateKey(d.id, st.id))
		if !ok {
			continue
		}
		r := rbuf{b: v}
		st.gen, st.count, st.bytes = r.i(), r.i(), r.i()
		ni := int(r.u())
		for j := 0; j < ni && r.err == nil; j++ {
			id, by := uint32(r.u()), r.i()
			if ix := st.indexByID(id); ix != nil {
				ix.bytes = by
			}
		}
	}
}

func newDatabase(o *Origin, name string) *Database {
	return &Database{o: o, name: name, stores: map[string]*store{}, conns: map[int64]*Conn{}}
}

// hasStore reports whether the origin has (or may have) an ordered store, without creating one.
func (o *Origin) hasStore() bool {
	o.kvMu.Lock()
	open := o.kvs != nil
	o.kvMu.Unlock()
	if open {
		return true
	}
	if o.dir == "" {
		return false // a memory-only store exists from its first use until the context ends
	}
	_, err := os.Stat(filepath.Join(o.dir, "kv"))
	return err == nil
}

// DB returns the named database, loading its schema from the store when it exists. A database that does not
// exist yet is created empty in memory (it exists for the platform only once an upgrade transaction commits).
func (o *Origin) DB(name string) (*Database, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if d := o.dbs[name]; d != nil {
		return d, nil
	}
	d := newDatabase(o, name)
	if o.hasStore() {
		k, err := o.acquire()
		if err != nil {
			return nil, err
		}
		if rec, ok := k.get(regKey(name)); ok {
			if err := d.decodeSchema(rec); err != nil {
				o.release(k)
				return nil, err
			}
			d.exists = d.version > 0
			d.loadStates(k)
		}
		o.release(k)
	}
	o.dbs[name] = d
	o.access()
	return d, nil
}

// DBInfo is one entry of indexedDB.databases().
type DBInfo struct {
	Name    string
	Version int64
}

// Databases lists the databases that exist.
func (o *Origin) Databases() []DBInfo {
	seen := map[string]DBInfo{}
	if o.hasStore() {
		if k, err := o.acquire(); err == nil {
			it := k.db.NewIterator(prefixRange([]byte{pDBReg}), nil)
			for it.Next() {
				var tmp Database
				if tmp.decodeSchema(it.Value()) == nil && tmp.version > 0 {
					n := string(it.Key()[1:])
					seen[n] = DBInfo{n, tmp.version}
				}
			}
			it.Release()
			o.release(k)
		}
	}
	o.mu.Lock()
	ds := make([]*Database, 0, len(o.dbs))
	for _, d := range o.dbs {
		ds = append(ds, d)
	}
	o.mu.Unlock()
	for _, d := range ds {
		d.mu.Lock()
		if d.exists {
			seen[d.name] = DBInfo{d.name, d.version}
		} else {
			delete(seen, d.name)
		}
		d.mu.Unlock()
	}
	out := make([]DBInfo, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// wipe removes every trace of the origin. Open connections are told ("closed": IDBDatabase.onclose fires, their
// transactions abort and the requests in them fail), requests still waiting to open fail too, then the store
// and the files go in one step. reason is "cleared" (the person cleared the data) or "evicted" (quota pressure).
func (o *Origin) wipe(reason string) {
	o.mu.Lock()
	ds := make([]*Database, 0, len(o.dbs))
	for _, d := range o.dbs {
		ds = append(ds, d)
	}
	o.mu.Unlock()
	for _, d := range ds {
		d.mu.Lock()
		for _, c := range d.openConns() {
			d.notify(c.sink, Msg{"t": "closed", "conn": c.id, "reason": reason})
			d.forceClose(c)
		}
		for _, r := range d.reqs {
			if r.state < 3 || r.state == 4 {
				d.notify(r.sink, Msg{"t": "open-error", "req": r.id, "name": "AbortError",
					"msg": "The origin's storage was " + reason + "."})
			}
		}
		d.reqs = nil
		d.stores = map[string]*store{}
		d.version, d.exists, d.id = 0, false, 0
		d.unlock()
	}
	o.wipeCaches()
	o.wipeOPFS()
	o.kvMu.Lock()
	o.closeKVLocked()
	o.kvMu.Unlock()
	o.persist.Store(false)
	o.usageIDB.Store(0)
	o.usageCache.Store(0)
	o.usageOPFS.Store(0)
	o.other.Store(0)
	o.memMu.Lock()
	o.memBlobs = nil
	o.memMu.Unlock()
	o.nextIDv.Store(1)
	o.idHigh.Store(0)
	if o.dir != "" {
		_ = os.RemoveAll(o.dir)
	}
}

// ---- number keys -----------------------------------------------------------------

// EncodeNumberKey is the wire form of a number key: tag 0x10 and 16 hex digits of
// the order-preserving bit pattern.
func EncodeNumberKey(f float64) string { return NumberKey(f).Wire() }

// DecodeNumberKey reads a wire key written by EncodeNumberKey.
func DecodeNumberKey(k string) (float64, bool) {
	key, err := ParseWire(k)
	if err != nil || key.Kind != KeyNumber {
		return 0, false
	}
	return key.Num, true
}

// storageNumber reads a number from a storage-form key.
func storageNumber(k []byte) (float64, bool) {
	if len(k) != 9 || k[0] != KeyNumber {
		return 0, false
	}
	return fromSortableBits(binary.BigEndian.Uint64(k[1:])), true
}

var _ = math.MaxInt
var _ iterator.Iterator
var _ = bytes.Equal
