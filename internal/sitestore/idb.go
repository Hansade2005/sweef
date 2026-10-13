package sitestore

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// This file is the storage and concurrency core of IndexedDB. The JavaScript
// side (the engine's idb prelude) owns everything the web platform defines in
// terms of script: structured clone, key extraction from values, request and
// event objects, transaction activity. This side owns what has to be shared
// between tabs and survive a restart: the records, their order, the indexes,
// the key generators, the rules for which transactions may run together, the
// version-change handshake between connections, and the log on disk.
//
// Keys and values are opaque strings here. A key is the order-preserving
// encoding the prelude produces (plain byte-wise comparison of two keys is
// the IndexedDB key order, see idb_prelude.js); a value is a serialised
// structured clone.

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

type record struct {
	key, val string
	ik       map[int][]string // index id -> index keys (nil when the store has no index)
}

type ientry struct{ ik, pk string }

func (a ientry) less(b ientry) bool {
	if a.ik != b.ik {
		return a.ik < b.ik
	}
	return a.pk < b.pk
}

type index struct {
	id      int
	name    string
	keyPath string // JSON text of the key path
	unique  bool
	multi   bool
	ents    []ientry // sorted by (ik, pk)
}

type store struct {
	name    string
	keyPath string // JSON text of the key path; "" for out-of-line keys
	hasKP   bool
	auto    bool
	gen     int64 // next key the generator hands out (starts at 1)
	nextIdx int
	recs    []*record // sorted by key
	idx     map[string]*index
}

func newStore(name, kp string, hasKP, auto bool) *store {
	return &store{name: name, keyPath: kp, hasKP: hasKP, auto: auto, gen: 1, idx: map[string]*index{}}
}

func (s *store) find(key string) (int, bool) {
	i := sort.Search(len(s.recs), func(i int) bool { return s.recs[i].key >= key })
	return i, i < len(s.recs) && s.recs[i].key == key
}

func (ix *index) find(e ientry) (int, bool) {
	i := sort.Search(len(ix.ents), func(i int) bool { return !ix.ents[i].less(e) })
	return i, i < len(ix.ents) && ix.ents[i] == e
}

func (ix *index) add(e ientry) {
	i, found := ix.find(e)
	if found {
		return
	}
	ix.ents = append(ix.ents, ientry{})
	copy(ix.ents[i+1:], ix.ents[i:])
	ix.ents[i] = e
}

func (ix *index) del(e ientry) {
	if i, found := ix.find(e); found {
		ix.ents = append(ix.ents[:i], ix.ents[i+1:]...)
	}
}

func (s *store) addIdx(r *record) {
	if r.ik == nil {
		return
	}
	for _, ix := range s.idx {
		for _, k := range r.ik[ix.id] {
			ix.add(ientry{k, r.key})
		}
	}
}

func (s *store) delIdx(r *record) {
	if r.ik == nil {
		return
	}
	for _, ix := range s.idx {
		for _, k := range r.ik[ix.id] {
			ix.del(ientry{k, r.key})
		}
	}
}

func recSize(r *record) int64 {
	if r == nil {
		return 0
	}
	n := int64(len(r.key) + len(r.val) + 16)
	for _, ks := range r.ik {
		for _, k := range ks {
			n += int64(len(k)) + 8
		}
	}
	return n
}

// Database is one named database of an origin, loaded into memory. Its mutex
// guards everything below it.
type Database struct {
	o    *Origin
	name string
	path string

	mu      sync.Mutex
	version int64
	exists  bool // committed at least once (an aborted first upgrade leaves nothing)
	stores  map[string]*store
	bytes   int64 // live bytes, approximate
	log     *recLog
	conns   map[int64]*Conn
	txs     []*Tx
	reqs    []*openReq
	out     []outMsg
	usage   atomic.Int64
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
	ops     [][]byte
	genDirt map[*store]bool
	delta   int64
	upgrade *openReq
	fresh   bool // being created: Begin reports its start itself
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

func (d *Database) newConn(sink Sink) *Conn {
	c := &Conn{id: d.o.m.nextID.Add(1), db: d, sink: sink, version: d.version}
	d.conns[c.id] = c
	d.o.m.regConn(c)
	return c
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
				c := d.newConn(r.sink)
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
		c := d.newConn(r.sink)
		r.conn = c
		tx := &Tx{id: d.o.m.nextID.Add(1), db: d, conn: c, mode: ModeVersionChange, started: true, upgrade: r}
		c.ntx++
		d.txs = append(d.txs, tx)
		d.o.m.regTx(tx)
		// the version changes with the transaction: an abort puts it back
		oldV, oldExists := d.version, d.exists
		d.version, d.exists = r.newV, true
		c.version = r.newV
		tx.undo = append(tx.undo, func() { d.version, d.exists = oldV, oldExists; c.version = oldV })
		tx.ops = append(tx.ops, encOp(func(w *wbuf) { w.byte(opSetVersion); w.i(r.newV) }))
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
	if len(d.conns) == 0 && len(d.txs) == 0 && len(d.reqs) == 0 {
		d.log.sync()
	}
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

// destroy forgets the database and its file (deleteDatabase, clear site data).
func (d *Database) destroy() {
	d.stores = map[string]*store{}
	d.version, d.exists, d.bytes = 0, false, 0
	d.log.remove()
	d.log = nil
	d.usage.Store(0)
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

func (d *Database) abortTx(t *Tx) {
	if t.done {
		return
	}
	for i := len(t.undo) - 1; i >= 0; i-- {
		t.undo[i]()
	}
	t.undo, t.ops = nil, nil
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

// Commit makes a transaction durable. A QuotaExceededError leaves it uncommitted: the caller aborts it.
func (m *Manager) Commit(txID int64) error {
	t := m.tx(txID)
	if t == nil {
		return errf("InvalidStateError", "The transaction is finished.")
	}
	d := t.db
	d.mu.Lock()
	defer d.unlock()
	if t.done {
		return errf("InvalidStateError", "The transaction is finished.")
	}
	if len(t.ops) > 0 {
		if q := m.quota; q > 0 && t.delta > 0 && d.o.usage()+t.delta > q {
			return errf("QuotaExceededError", "The storage quota for this origin was exceeded.")
		}
		for st := range t.genDirt {
			if st.auto {
				n := st.gen
				t.ops = append(t.ops, encOp(func(w *wbuf) { w.byte(opGen); w.s(st.name); w.i(n) }))
			}
		}
		if err := d.persist(t.ops); err != nil {
			return errf("UnknownError", "%v", err)
		}
	}
	d.finishTx(t)
	return nil
}

func (d *Database) persist(ops [][]byte) error {
	if d.o.dir == "" {
		return nil
	}
	if d.log == nil {
		if err := d.openFresh(); err != nil {
			return err
		}
	}
	var w wbuf
	for _, o := range ops {
		w.b = append(w.b, o...)
	}
	if err := d.log.append(w.b); err != nil {
		return err
	}
	if sz := d.log.length(); sz > (1<<20)+2*d.bytes && sz > 4*d.bytes/3 {
		d.compact()
	} else {
		d.usage.Store(sz)
	}
	return nil
}

func (d *Database) openFresh() error {
	l, _, err := openLog(d.path, true)
	if err != nil {
		return err
	}
	var w wbuf
	w.s(dbMagic)
	w.s(d.name)
	if l.length() == 0 {
		if err := l.append(w.b); err != nil {
			l.close()
			return err
		}
	}
	d.log = l
	return nil
}

const dbMagic = "SWIDB1"

// compact rewrites the log as a snapshot of the live state.
func (d *Database) compact() {
	if d.log == nil {
		return
	}
	recs := d.snapshot()
	if err := d.log.rewrite(recs); err == nil {
		d.usage.Store(d.log.length())
	}
}

func (d *Database) snapshot() [][]byte {
	var hw wbuf
	hw.s(dbMagic)
	hw.s(d.name)
	out := [][]byte{hw.b}
	var w wbuf
	w.byte(opSetVersion)
	w.i(d.version)
	names := make([]string, 0, len(d.stores))
	for n := range d.stores {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		st := d.stores[n]
		w.byte(opCreateStore)
		w.s(st.name)
		w.s(st.keyPath)
		w.bool(st.hasKP)
		w.bool(st.auto)
		w.byte(opGen)
		w.s(st.name)
		w.i(st.gen)
		inames := make([]string, 0, len(st.idx))
		for in := range st.idx {
			inames = append(inames, in)
		}
		sort.Strings(inames)
		for _, in := range inames {
			ix := st.idx[in]
			w.byte(opCreateIndex)
			w.s(st.name)
			w.s(ix.name)
			w.s(ix.keyPath)
			w.bool(ix.unique)
			w.bool(ix.multi)
			w.u(0) // entries follow as puts below
		}
	}
	out = append(out, w.b)
	for _, n := range names {
		st := d.stores[n]
		var pw wbuf
		for _, r := range st.recs {
			encPut(&pw, st, r)
			if len(pw.b) > 1<<20 {
				out = append(out, pw.b)
				pw = wbuf{}
			}
		}
		if len(pw.b) > 0 {
			out = append(out, pw.b)
		}
	}
	return out
}

// ---- ops: the unit of persistence ---------------------------------------------------

const (
	opSetVersion  = 1
	opCreateStore = 2
	opDeleteStore = 3
	opRenameStore = 4
	opCreateIndex = 5
	opDeleteIndex = 6
	opRenameIndex = 7
	opPut         = 8
	opDelete      = 9
	opClear       = 10
	opGen         = 11
)

func encOp(f func(w *wbuf)) []byte {
	var w wbuf
	f(&w)
	return w.b
}

func encPut(w *wbuf, st *store, r *record) {
	w.byte(opPut)
	w.s(st.name)
	w.s(r.key)
	w.s(r.val)
	var names []string
	for _, ix := range st.idx {
		if len(r.ik[ix.id]) > 0 {
			names = append(names, ix.name)
		}
	}
	sort.Strings(names)
	w.u(uint64(len(names)))
	for _, n := range names {
		ks := r.ik[st.idx[n].id]
		w.s(n)
		w.u(uint64(len(ks)))
		for _, k := range ks {
			w.s(k)
		}
	}
}

// loadDatabase replays a database file.
func loadDatabase(o *Origin, name, path string) (*Database, error) {
	l, recs, err := openLog(path, false)
	if err != nil {
		return nil, err
	}
	d := newDatabase(o, name, path)
	d.log = l
	if len(recs) == 0 {
		l.close()
		d.log = nil
		return d, nil
	}
	hr := rbuf{b: recs[0]}
	if hr.s() != dbMagic || hr.err != nil {
		l.close()
		return nil, fmt.Errorf("sitestore: %s is not a database file", path)
	}
	hr.s()
	for _, rec := range recs[1:] {
		r := rbuf{b: rec}
		for r.more() {
			if err := d.replayOp(&r); err != nil {
				break // a damaged batch is dropped from its first bad op; earlier records stand
			}
		}
	}
	d.exists = d.version > 0
	d.usage.Store(l.length())
	return d, nil
}

func newDatabase(o *Origin, name, path string) *Database {
	return &Database{o: o, name: name, path: path, stores: map[string]*store{}, conns: map[int64]*Conn{}}
}

func (d *Database) replayOp(r *rbuf) error {
	switch r.byte() {
	case opSetVersion:
		d.version = r.i()
	case opCreateStore:
		name, kp, hasKP, auto := r.s(), r.s(), r.bool(), r.bool()
		if r.err == nil {
			d.stores[name] = newStore(name, kp, hasKP, auto)
		}
	case opDeleteStore:
		name := r.s()
		if st := d.stores[name]; st != nil {
			for _, rec := range st.recs {
				d.bytes -= recSize(rec)
			}
			delete(d.stores, name)
		}
	case opRenameStore:
		from, to := r.s(), r.s()
		if st := d.stores[from]; st != nil && r.err == nil {
			delete(d.stores, from)
			st.name = to
			d.stores[to] = st
		}
	case opCreateIndex:
		sn, name, kp, unique, multi := r.s(), r.s(), r.s(), r.bool(), r.bool()
		n := int(r.u())
		var ents []idxEntry
		for i := 0; i < n && r.err == nil; i++ {
			e := idxEntry{PK: r.s()}
			nk := int(r.u())
			for j := 0; j < nk && r.err == nil; j++ {
				e.Keys = append(e.Keys, r.s())
			}
			ents = append(ents, e)
		}
		if st := d.stores[sn]; st != nil && r.err == nil {
			_ = d.doCreateIndex(nil, st, name, kp, unique, multi, ents)
		}
	case opDeleteIndex:
		sn, name := r.s(), r.s()
		if st := d.stores[sn]; st != nil {
			d.doDeleteIndex(nil, st, name)
		}
	case opRenameIndex:
		sn, from, to := r.s(), r.s(), r.s()
		if st := d.stores[sn]; st != nil && st.idx[from] != nil {
			d.doRenameIndex(nil, st, from, to)
		}
	case opPut:
		sn, key, val := r.s(), r.s(), r.s()
		n := int(r.u())
		ik := map[string][]string{}
		for i := 0; i < n && r.err == nil; i++ {
			in := r.s()
			nk := int(r.u())
			for j := 0; j < nk && r.err == nil; j++ {
				ik[in] = append(ik[in], r.s())
			}
		}
		if st := d.stores[sn]; st != nil && r.err == nil {
			_ = d.doPut(nil, st, key, val, ik, false)
		}
	case opDelete:
		sn, key := r.s(), r.s()
		if st := d.stores[sn]; st != nil {
			d.doDelete(nil, st, key)
		}
	case opClear:
		if st := d.stores[r.s()]; st != nil {
			d.doClear(nil, st)
		}
	case opGen:
		sn, n := r.s(), r.i()
		if st := d.stores[sn]; st != nil {
			st.gen = n
		}
	default:
		return errCorrupt
	}
	return r.err
}

// ---- mutations (shared by live transactions and log replay; tx == nil on replay) ----

func (tx *Tx) addUndo(f func()) {
	if tx != nil {
		tx.undo = append(tx.undo, f)
	}
}

func (tx *Tx) addOp(b []byte) {
	if tx != nil {
		tx.ops = append(tx.ops, b)
	}
}

func (d *Database) bump(tx *Tx, n int64) {
	d.addBytes(n)
	if tx != nil {
		tx.delta += n
		tx.addUndo(func() { d.addBytes(-n) })
	}
}

// addBytes adjusts the live byte count. A memory-only database reports it as its usage.
func (d *Database) addBytes(n int64) {
	d.bytes += n
	if d.o.dir == "" {
		d.usage.Store(d.bytes)
	}
}

func (d *Database) doCreateStore(tx *Tx, name, kp string, hasKP, auto bool) error {
	if d.stores[name] != nil {
		return errf("ConstraintError", "An object store with the specified name already exists.")
	}
	d.stores[name] = newStore(name, kp, hasKP, auto)
	tx.addUndo(func() { delete(d.stores, name) })
	tx.addOp(encOp(func(w *wbuf) { w.byte(opCreateStore); w.s(name); w.s(kp); w.bool(hasKP); w.bool(auto) }))
	return nil
}

func (d *Database) doDeleteStore(tx *Tx, st *store) {
	delete(d.stores, st.name)
	var freed int64
	for _, r := range st.recs {
		freed += recSize(r)
	}
	d.addBytes(-freed)
	tx.addUndo(func() { d.stores[st.name] = st; d.addBytes(freed) })
	tx.addOp(encOp(func(w *wbuf) { w.byte(opDeleteStore); w.s(st.name) }))
}

func (d *Database) doRenameStore(tx *Tx, st *store, to string) {
	from := st.name
	delete(d.stores, from)
	st.name = to
	d.stores[to] = st
	tx.addUndo(func() { delete(d.stores, to); st.name = from; d.stores[from] = st })
	tx.addOp(encOp(func(w *wbuf) { w.byte(opRenameStore); w.s(from); w.s(to) }))
}

// IndexEntry is one record's keys in an index being created.
type IndexEntry struct {
	PK   string
	Keys []string
}

type idxEntry = IndexEntry

func (d *Database) doCreateIndex(tx *Tx, st *store, name, kp string, unique, multi bool, ents []idxEntry) error {
	if st.idx[name] != nil {
		return errf("ConstraintError", "An index with the specified name already exists.")
	}
	ix := &index{id: st.nextIdx, name: name, keyPath: kp, unique: unique, multi: multi}
	st.nextIdx++
	type pair struct {
		r    *record
		prev []string
		had  bool
	}
	var touched []pair
	var all []ientry
	for _, e := range ents {
		i, ok := st.find(e.PK)
		if !ok {
			continue
		}
		r := st.recs[i]
		for _, k := range e.Keys {
			all = append(all, ientry{k, r.key})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].less(all[j]) })
	if unique {
		for i := 1; i < len(all); i++ {
			if all[i].ik == all[i-1].ik && all[i].pk != all[i-1].pk {
				return errf("ConstraintError", "Unable to add the index: existing records violate its uniqueness constraint.")
			}
		}
	}
	// de-duplicate identical entries (a record whose multiEntry keys repeat)
	w := 0
	for i, e := range all {
		if i == 0 || e != all[i-1] {
			all[w] = e
			w++
		}
	}
	ix.ents = all[:w]
	for _, e := range ents {
		i, ok := st.find(e.PK)
		if !ok {
			continue
		}
		r := st.recs[i]
		prev, had := r.ik[ix.id]
		touched = append(touched, pair{r, prev, had})
		if r.ik == nil {
			r.ik = map[int][]string{}
		}
		r.ik[ix.id] = e.Keys
		d.bump(tx, indexBytes(e.Keys))
	}
	st.idx[name] = ix
	tx.addUndo(func() {
		delete(st.idx, name)
		for _, p := range touched {
			if p.had {
				p.r.ik[ix.id] = p.prev
			} else {
				delete(p.r.ik, ix.id)
			}
		}
	})
	tx.addOp(encOp(func(w *wbuf) {
		w.byte(opCreateIndex)
		w.s(st.name)
		w.s(name)
		w.s(kp)
		w.bool(unique)
		w.bool(multi)
		w.u(uint64(len(ents)))
		for _, e := range ents {
			w.s(e.PK)
			w.u(uint64(len(e.Keys)))
			for _, k := range e.Keys {
				w.s(k)
			}
		}
	}))
	return nil
}

func indexBytes(ks []string) int64 {
	var n int64
	for _, k := range ks {
		n += int64(len(k)) + 8
	}
	return n
}

func (d *Database) doDeleteIndex(tx *Tx, st *store, name string) {
	ix := st.idx[name]
	if ix == nil {
		return
	}
	delete(st.idx, name)
	tx.addUndo(func() { st.idx[name] = ix })
	tx.addOp(encOp(func(w *wbuf) { w.byte(opDeleteIndex); w.s(st.name); w.s(name) }))
}

func (d *Database) doRenameIndex(tx *Tx, st *store, from, to string) {
	ix := st.idx[from]
	delete(st.idx, from)
	ix.name = to
	st.idx[to] = ix
	tx.addUndo(func() { delete(st.idx, to); ix.name = from; st.idx[from] = ix })
	tx.addOp(encOp(func(w *wbuf) { w.byte(opRenameIndex); w.s(st.name); w.s(from); w.s(to) }))
}

// doPut stores a record under key. ik maps index names to the index keys of the value.
func (d *Database) doPut(tx *Tx, st *store, key, val string, ik map[string][]string, noOverwrite bool) error {
	pos, found := st.find(key)
	if found && noOverwrite {
		return errf("ConstraintError", "A record with the given key already exists.")
	}
	var rec = &record{key: key, val: val}
	if len(st.idx) > 0 {
		for name, ks := range ik {
			ix := st.idx[name]
			if ix == nil || len(ks) == 0 {
				continue
			}
			if ix.unique {
				for _, k := range ks {
					i := sort.Search(len(ix.ents), func(i int) bool { return ix.ents[i].ik >= k })
					for ; i < len(ix.ents) && ix.ents[i].ik == k; i++ {
						if ix.ents[i].pk != key {
							return errf("ConstraintError", "Unable to add key to index '%s': at least one key does not satisfy the uniqueness requirements.", name)
						}
					}
				}
			}
			if rec.ik == nil {
				rec.ik = map[int][]string{}
			}
			rec.ik[ix.id] = ks
		}
	}
	var old *record
	if found {
		old = st.recs[pos]
		st.delIdx(old)
		st.recs[pos] = rec
	} else {
		st.recs = append(st.recs, nil)
		copy(st.recs[pos+1:], st.recs[pos:])
		st.recs[pos] = rec
	}
	st.addIdx(rec)
	d.bump(tx, recSize(rec)-recSize(old))
	if tx != nil {
		tx.addUndo(func() {
			st.delIdx(rec)
			i, _ := st.find(key)
			if old != nil {
				st.recs[i] = old
				st.addIdx(old)
			} else {
				st.recs = append(st.recs[:i], st.recs[i+1:]...)
			}
		})
		tx.addOp(encOp(func(w *wbuf) { encPut(w, st, rec) }))
	}
	return nil
}

func (d *Database) doDelete(tx *Tx, st *store, key string) {
	pos, found := st.find(key)
	if !found {
		return
	}
	old := st.recs[pos]
	st.delIdx(old)
	st.recs = append(st.recs[:pos], st.recs[pos+1:]...)
	d.bump(tx, -recSize(old))
	if tx != nil {
		tx.addUndo(func() {
			i, _ := st.find(key)
			st.recs = append(st.recs, nil)
			copy(st.recs[i+1:], st.recs[i:])
			st.recs[i] = old
			st.addIdx(old)
		})
		tx.addOp(encOp(func(w *wbuf) { w.byte(opDelete); w.s(st.name); w.s(key) }))
	}
}

func (d *Database) doClear(tx *Tx, st *store) {
	oldRecs := st.recs
	oldEnts := map[*index][]ientry{}
	for _, ix := range st.idx {
		oldEnts[ix] = ix.ents
		ix.ents = nil
	}
	var freed int64
	for _, r := range oldRecs {
		freed += recSize(r)
	}
	st.recs = nil
	d.bump(tx, -freed)
	if tx != nil {
		tx.addUndo(func() {
			st.recs = oldRecs
			for ix, e := range oldEnts {
				ix.ents = e
			}
		})
		tx.addOp(encOp(func(w *wbuf) { w.byte(opClear); w.s(st.name) }))
	}
}

// bumpGen moves the key generator past an explicit numeric key.
func (d *Database) bumpGen(tx *Tx, st *store, key string) {
	if !st.auto {
		return
	}
	v, ok := DecodeNumberKey(key)
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
		d.setGen(tx, st, ng)
	}
}

func (d *Database) setGen(tx *Tx, st *store, n int64) {
	old := st.gen
	st.gen = n
	if tx != nil {
		tx.addUndo(func() { st.gen = old })
		if tx.genDirt == nil {
			tx.genDirt = map[*store]bool{}
		}
		tx.genDirt[st] = true
	}
}

// ---- number keys -----------------------------------------------------------------

// EncodeNumberKey is the key encoding of a number: tag 0x10 and 16 hex digits of
// the order-preserving bit pattern.
func EncodeNumberKey(f float64) string {
	if f == 0 {
		f = 0 // -0 is 0
	}
	b := math.Float64bits(f)
	if b>>63 != 0 {
		b = ^b
	} else {
		b |= 1 << 63
	}
	return "\x10" + fmt.Sprintf("%016x", b)
}

// DecodeNumberKey reads a key written by EncodeNumberKey.
func DecodeNumberKey(k string) (float64, bool) {
	if len(k) != 17 || k[0] != 0x10 {
		return 0, false
	}
	var b uint64
	for i := 1; i < 17; i++ {
		c := k[i]
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		default:
			return 0, false
		}
		b = b<<4 | uint64(v)
	}
	if b>>63 != 0 {
		b &^= 1 << 63
	} else {
		b = ^b
	}
	return math.Float64frombits(b), true
}

// ---- operations on a transaction --------------------------------------------------

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
	return f(t, d)
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

// AllRecords lists the (key, value) pairs of a store, for populating a new index.
func (m *Manager) AllRecords(txID int64, storeName string) ([]any, error) {
	var out []any
	err := m.withTx(txID, false, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		out = make([]any, 0, len(st.recs)*2)
		for _, r := range st.recs {
			out = append(out, r.key, r.val)
		}
		return nil
	})
	return out, err
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
		out = EncodeNumberKey(float64(st.gen))
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
		if key == "" {
			if !st.auto {
				return errf("DataError", "The object store uses out-of-line keys and has no key generator: a key must be provided.")
			}
			if st.gen > maxGen {
				return errf("ConstraintError", "The key generator has reached its maximum value.")
			}
			key = EncodeNumberKey(float64(st.gen))
			d.setGen(t, st, st.gen+1)
		} else {
			d.bumpGen(t, st, key)
		}
		if err := d.doPut(t, st, key, val, ik, noOverwrite); err != nil {
			return err
		}
		out = key
		return nil
	})
	return out, err
}

// Range is a key range; an empty bound is unbounded.
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

// span returns the half-open index interval of n sorted items (key(i)) inside the range.
func (r Range) span(n int, key func(i int) string) (int, int) {
	from, to := 0, n
	if r.Lo != "" {
		if r.LoOpen {
			from = sort.Search(n, func(i int) bool { return key(i) > r.Lo })
		} else {
			from = sort.Search(n, func(i int) bool { return key(i) >= r.Lo })
		}
	}
	if r.Hi != "" {
		if r.HiOpen {
			to = sort.Search(n, func(i int) bool { return key(i) >= r.Hi })
		} else {
			to = sort.Search(n, func(i int) bool { return key(i) > r.Hi })
		}
	}
	if to < from {
		to = from
	}
	return from, to
}

// DeleteRange removes the records of a store whose key lies in the range.
func (m *Manager) DeleteRange(txID int64, storeName string, r Range) error {
	return m.withTx(txID, true, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		from, to := r.span(len(st.recs), func(i int) string { return st.recs[i].key })
		if from == 0 && to == len(st.recs) && r.Lo == "" && r.Hi == "" {
			d.doClear(t, st)
			return nil
		}
		keys := make([]string, 0, to-from)
		for _, rec := range st.recs[from:to] {
			keys = append(keys, rec.key)
		}
		for _, k := range keys {
			d.doDelete(t, st, k)
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
		if indexName == "" {
			from, to := r.span(len(st.recs), func(i int) string { return st.recs[i].key })
			if kind == QCount {
				out = []any{fmt.Sprint(to - from)}
				return nil
			}
			for i := from; i < to && limit > 0; i, limit = i+1, limit-1 {
				rec := st.recs[i]
				switch kind {
				case QValue, QValues:
					out = append(out, rec.val)
				case QKey, QKeys:
					out = append(out, rec.key)
				case QRecords:
					out = append(out, rec.key, rec.key, rec.val)
				}
			}
			return nil
		}
		ix := st.idx[indexName]
		if ix == nil {
			return errf("NotFoundError", "The index '%s' was not found.", indexName)
		}
		from, to := r.span(len(ix.ents), func(i int) string { return ix.ents[i].ik })
		if kind == QCount {
			out = []any{fmt.Sprint(to - from)}
			return nil
		}
		for i := from; i < to && limit > 0; i, limit = i+1, limit-1 {
			e := ix.ents[i]
			switch kind {
			case QKey, QKeys:
				out = append(out, e.pk)
			case QValue, QValues:
				if j, ok := st.find(e.pk); ok {
					out = append(out, st.recs[j].val)
				}
			case QRecords:
				if j, ok := st.find(e.pk); ok {
					out = append(out, e.ik, e.pk, st.recs[j].val)
				}
			}
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

// Cursor moves a cursor and returns [key, primaryKey, value] or nil at the end.
func (m *Manager) Cursor(txID int64, storeName string, q CursorReq) ([]any, error) {
	var out []any
	err := m.withTx(txID, false, func(t *Tx, d *Database) error {
		st, err := t.store(d, storeName)
		if err != nil {
			return err
		}
		var n int
		var at func(i int) (string, string)
		if q.Index == "" {
			q.CurPK, q.TargetPK = "", "" // a store cursor is positioned by its primary key alone
			n = len(st.recs)
			at = func(i int) (string, string) { return st.recs[i].key, "" }
		} else {
			ix := st.idx[q.Index]
			if ix == nil {
				return errf("NotFoundError", "The index '%s' was not found.", q.Index)
			}
			n = len(ix.ents)
			at = func(i int) (string, string) { return ix.ents[i].ik, ix.ents[i].pk }
		}
		from, to := q.Range.span(n, func(i int) string { a, _ := at(i); return a })
		p := cursorPos(n, at, from, to, q)
		if p < 0 {
			return nil
		}
		a, b := at(p)
		pk := a
		if q.Index != "" {
			pk = b
		}
		out = []any{a, pk}
		if !q.KeyOnly {
			if j, ok := st.find(pk); ok {
				out = append(out, st.recs[j].val)
			} else {
				out = append(out, "")
			}
		}
		return nil
	})
	return out, err
}

func cmp2(a1, b1, a2, b2 string) int {
	if a1 != a2 {
		if a1 < a2 {
			return -1
		}
		return 1
	}
	if b1 != b2 {
		if b1 < b2 {
			return -1
		}
		return 1
	}
	return 0
}

// cursorPos returns the item index the cursor lands on, or -1.
func cursorPos(n int, at func(i int) (string, string), from, to int, q CursorReq) int {
	if from >= to {
		return -1
	}
	// first index whose (a,b) >= (x,y)
	lower := func(x, y string) int {
		return sort.Search(n, func(i int) bool { a, b := at(i); return cmp2(a, b, x, y) >= 0 })
	}
	upper := func(x, y string) int {
		return sort.Search(n, func(i int) bool { a, b := at(i); return cmp2(a, b, x, y) > 0 })
	}
	// first index with a >= x / a > x
	lowerA := func(x string) int { return sort.Search(n, func(i int) bool { a, _ := at(i); return a >= x }) }
	upperA := func(x string) int { return sort.Search(n, func(i int) bool { a, _ := at(i); return a > x }) }
	steps := q.Advance
	if steps < 1 {
		steps = 1
	}
	forward := q.Dir == DirNext || q.Dir == DirNextUnique
	unique := q.Dir == DirNextUnique || q.Dir == DirPrevUnique
	var p int
	if forward {
		if q.HasCur {
			if unique {
				p = upperA(q.CurKey)
			} else {
				p = upper(q.CurKey, q.CurPK)
			}
		} else {
			p = from
		}
		if q.Target != "" {
			var tp int
			if q.TargetPK != "" {
				tp = lower(q.Target, q.TargetPK)
			} else {
				tp = lowerA(q.Target)
			}
			if tp > p {
				p = tp
			}
		}
		if p < from {
			p = from
		}
		for s := 1; s < steps && p < to; s++ {
			if unique {
				a, _ := at(p)
				p = upperA(a)
			} else {
				p++
			}
		}
		if p >= to {
			return -1
		}
		return p
	}
	// backward
	if q.HasCur {
		if unique {
			p = lowerA(q.CurKey) - 1
		} else {
			p = lower(q.CurKey, q.CurPK) - 1
		}
	} else {
		p = to - 1
	}
	if q.Target != "" {
		var tp int
		if q.TargetPK != "" {
			tp = upper(q.Target, q.TargetPK) - 1
		} else {
			tp = upperA(q.Target) - 1
		}
		if tp < p {
			p = tp
		}
	}
	if p >= to {
		p = to - 1
	}
	groupFirst := func(i int) int {
		if i < 0 {
			return i
		}
		a, _ := at(i)
		return lowerA(a)
	}
	if unique && p >= from {
		p = groupFirst(p)
	}
	for s := 1; s < steps && p >= from; s++ {
		if unique {
			a, _ := at(p)
			p = groupFirst(lowerA(a) - 1)
		} else {
			p--
		}
	}
	if p < from {
		return -1
	}
	return p
}

// ---- helpers for the host layer -------------------------------------------------------

// ParseRange builds a Range from a host argument slice [lo, hi, loOpen, hiOpen].
func ParseRange(lo, hi string, loOpen, hiOpen bool) Range { return Range{lo, hi, loOpen, hiOpen} }

// SplitName is used by tests and the host layer to describe errors.
func IsError(err error, name string) bool {
	e, ok := err.(*Error)
	return ok && e.Name == name
}

var _ = strings.Compare
