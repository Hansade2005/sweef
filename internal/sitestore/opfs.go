package sitestore

import (
	"encoding/binary"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
)

// The Origin Private File System of one origin (navigator.storage.getDirectory()).
//
// The tree lives in the origin's ordered store like everything else:
//
//	pOPFSKid  dir(8) name   -> child id (8), kind (1)
//	pOPFSNode id(8)         -> kind, mtime, size
//	pOPFSData id(8)         -> the file's bytes (inline) or a reference to a blob file
//
// File contents above BlobThreshold are files under blobs/, so a large file
// is never held in memory by the store, and what an operation holds is one
// file at a time (the script API reads and writes whole files). The tree
// counts against the same quota as IndexedDB and Cache Storage.

const (
	pOPFSData = 0x32
	opfsRoot  = uint64(1)

	opfsFile = 0
	opfsDir  = 1

	opfsNodeCost = 96 // bytes a node costs besides its contents
)

// OPFS is the file system of one origin.
type OPFS struct {
	o  *Origin
	mu sync.Mutex
}

// OPFS returns the origin's private file system.
func (o *Origin) OPFS() *OPFS {
	o.cacheMu.Lock()
	defer o.cacheMu.Unlock()
	if o.opfs == nil {
		o.opfs = &OPFS{o: o}
	}
	return o.opfs
}

func (o *Origin) wipeOPFS() {
	o.cacheMu.Lock()
	o.opfs = nil
	o.cacheMu.Unlock()
}

// Usage is the number of bytes the tree occupies.
func (f *OPFS) Usage() int64 { return f.o.usageOPFS.Load() }

// OPFSStat is what stat reports.
type OPFSStat struct {
	Dir   bool
	Size  int64
	Mtime int64
}

// OPFSEntry is one directory entry.
type OPFSEntry struct {
	Name string
	Dir  bool
}

func opfsErr(name, msg string) error { return &Error{name, msg} }

var errNotFound = opfsErr("NotFoundError", "A requested file or directory could not be found.")

func kidKey(dir uint64, name string) []byte {
	k := make([]byte, 9, 9+len(name))
	k[0] = pOPFSKid
	binary.BigEndian.PutUint64(k[1:], dir)
	return append(k, name...)
}

func nodeKey(kind byte, id uint64) []byte {
	k := make([]byte, 9)
	k[0] = kind
	binary.BigEndian.PutUint64(k[1:], id)
	return k
}

type opfsNodeRec struct {
	dir   bool
	mtime int64
	size  int64
}

func encNode(n opfsNodeRec) []byte {
	var w wbuf
	if n.dir {
		w.byte(opfsDir)
	} else {
		w.byte(opfsFile)
	}
	w.i(n.mtime)
	w.i(n.size)
	return w.b
}

func decNode(b []byte) (opfsNodeRec, bool) {
	r := rbuf{b: b}
	k := r.byte()
	n := opfsNodeRec{dir: k == opfsDir, mtime: r.i(), size: r.i()}
	return n, r.err == nil
}

// session is one operation's view of the store, with the batch it builds.
type opfsOp struct {
	f       *OPFS
	k       *kvStore
	b       leveldb.Batch
	delta   int64
	blobs   []uint64 // to remove after the commit
	created []uint64
	now     int64
}

func (f *OPFS) begin() (*opfsOp, error) {
	k, err := f.o.acquire()
	if err != nil {
		return nil, errf("UnknownError", "%v", err)
	}
	return &opfsOp{f: f, k: k, now: f.o.m.now().UnixMilli()}, nil
}

func (op *opfsOp) end() { op.f.o.release(op.k) }

func (op *opfsOp) node(id uint64) (opfsNodeRec, bool) {
	if v, ok := op.k.get(nodeKey(pOPFSNode, id)); ok {
		return decNode(v)
	}
	if id == opfsRoot {
		return opfsNodeRec{dir: true}, true
	}
	return opfsNodeRec{}, false
}

func (op *opfsOp) kid(dir uint64, name string) (id uint64, isDir bool, ok bool) {
	v, found := op.k.get(kidKey(dir, name))
	if !found || len(v) < 9 {
		return 0, false, false
	}
	return u64(v), v[8] == opfsDir, true
}

func (op *opfsOp) putKid(dir uint64, name string, id uint64, isDir bool) {
	kind := byte(opfsFile)
	if isDir {
		kind = opfsDir
	}
	v := binary.BigEndian.AppendUint64(nil, id)
	op.b.Put(kidKey(dir, name), append(v, kind))
}

func (op *opfsOp) touchDir(id uint64) {
	n, _ := op.node(id)
	n.dir = true
	n.mtime = op.now
	op.b.Put(nodeKey(pOPFSNode, id), encNode(n))
}

// walk resolves a path to a node id.
func (op *opfsOp) walk(parts []string) (id uint64, isDir bool, ok bool) {
	id, isDir = opfsRoot, true
	for _, p := range parts {
		if !isDir {
			return 0, false, false
		}
		var found bool
		if id, isDir, found = op.kid(id, p); !found {
			return 0, false, false
		}
	}
	return id, isDir, true
}

func (op *opfsOp) commit() error {
	err := op.f.o.commitBatch(op.k, &op.b, 0, 0, op.delta, false)
	if err != nil {
		op.f.o.dropBlobFiles(op.created)
		return errf("UnknownError", "%v", err)
	}
	op.f.o.dropBlobFiles(op.blobs)
	op.f.o.access()
	return nil
}

// Stat reports a node; nil when the path does not exist.
func (f *OPFS) Stat(parts []string) (*OPFSStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, err := f.begin()
	if err != nil {
		return nil, err
	}
	defer op.end()
	id, _, ok := op.walk(parts)
	if !ok {
		return nil, nil
	}
	n, _ := op.node(id)
	return &OPFSStat{Dir: n.dir, Size: n.size, Mtime: n.mtime}, nil
}

// Get gets or creates a child: parts is the full path, dir says what kind it must be.
func (f *OPFS) Get(parts []string, dir, create bool) error {
	if len(parts) == 0 {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if create {
		f.mu.Unlock()
		err := f.o.reserve(opfsNodeCost)
		f.mu.Lock()
		if err != nil {
			return err
		}
	}
	op, err := f.begin()
	if err != nil {
		return err
	}
	defer op.end()
	pid, pdir, ok := op.walk(parts[:len(parts)-1])
	if !ok || !pdir {
		return errNotFound
	}
	name := parts[len(parts)-1]
	if _, isDir, found := op.kid(pid, name); found {
		if isDir != dir {
			return opfsErr("TypeMismatchError", "The path supplied exists, but was not an entry of requested type.")
		}
		return nil
	}
	if !create {
		return errNotFound
	}
	id := f.o.allocID()
	op.putKid(pid, name, id, dir)
	op.b.Put(nodeKey(pOPFSNode, id), encNode(opfsNodeRec{dir: dir, mtime: op.now}))
	op.touchDir(pid)
	op.delta += opfsNodeCost
	return op.commit()
}

// List lists a directory in name order.
func (f *OPFS) List(parts []string) ([]OPFSEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, err := f.begin()
	if err != nil {
		return nil, err
	}
	defer op.end()
	id, isDir, ok := op.walk(parts)
	if !ok || !isDir {
		return nil, errNotFound
	}
	var out []OPFSEntry
	pre := kidKey(id, "")
	it := op.k.db.NewIterator(prefixRange(pre), nil)
	for it.Next() {
		v := it.Value()
		out = append(out, OPFSEntry{Name: string(it.Key()[len(pre):]), Dir: len(v) >= 9 && v[8] == opfsDir})
	}
	it.Release()
	return out, nil
}

// freeTree adds the deletion of a node and everything under it to the batch.
func (op *opfsOp) freeTree(id uint64, isDir bool) {
	if isDir {
		pre := kidKey(id, "")
		it := op.k.db.NewIterator(prefixRange(pre), nil)
		type kid struct {
			name string
			id   uint64
			dir  bool
		}
		var kids []kid
		for it.Next() {
			v := it.Value()
			if len(v) >= 9 {
				kids = append(kids, kid{string(it.Key()[len(pre):]), u64(v), v[8] == opfsDir})
			}
		}
		it.Release()
		for _, c := range kids {
			op.b.Delete(kidKey(id, c.name))
			op.freeTree(c.id, c.dir)
		}
	} else {
		if v, ok := op.k.get(nodeKey(pOPFSData, id)); ok {
			if bid, _, vok := valueInfo(v); vok && bid != 0 && isBlobValue(v) {
				op.b.Delete(blobKey(bid))
				op.blobs = append(op.blobs, bid)
			}
		}
		op.b.Delete(nodeKey(pOPFSData, id))
	}
	n, _ := op.node(id)
	op.delta -= opfsNodeCost + n.size
	op.b.Delete(nodeKey(pOPFSNode, id))
}

// Remove deletes a file or directory; a non-empty directory needs recursive.
func (f *OPFS) Remove(parts []string, recursive bool) error {
	if len(parts) == 0 {
		return opfsErr("NoModificationAllowedError", "The root directory cannot be removed.")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	op, err := f.begin()
	if err != nil {
		return err
	}
	defer op.end()
	pid, pdir, ok := op.walk(parts[:len(parts)-1])
	if !ok || !pdir {
		return errNotFound
	}
	name := parts[len(parts)-1]
	id, isDir, found := op.kid(pid, name)
	if !found {
		return errNotFound
	}
	if isDir && !recursive {
		it := op.k.db.NewIterator(prefixRange(kidKey(id, "")), nil)
		nonEmpty := it.First()
		it.Release()
		if nonEmpty {
			return opfsErr("InvalidModificationError", "The object can not be modified in this way.")
		}
	}
	op.b.Delete(kidKey(pid, name))
	op.freeTree(id, isDir)
	op.touchDir(pid)
	return op.commit()
}

// Read returns a file's bytes and modification time.
func (f *OPFS) Read(parts []string) ([]byte, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	op, err := f.begin()
	if err != nil {
		return nil, 0, err
	}
	defer op.end()
	id, isDir, ok := op.walk(parts)
	if !ok || isDir {
		return nil, 0, errNotFound
	}
	n, _ := op.node(id)
	v, ok := op.k.get(nodeKey(pOPFSData, id))
	if !ok {
		return nil, n.mtime, nil
	}
	b, err := f.o.unpackValue(v)
	if err != nil {
		return nil, 0, errf("UnknownError", "%v", err)
	}
	f.o.access()
	return append([]byte(nil), b...), n.mtime, nil
}

// Write replaces a file's contents.
func (f *OPFS) Write(parts []string, data []byte) error {
	// room first, outside our lock: making room can evict other origins
	f.mu.Lock()
	var old int64
	if op, err := f.begin(); err == nil {
		if id, isDir, ok := op.walk(parts); ok && !isDir {
			n, _ := op.node(id)
			old = n.size
		}
		op.end()
	}
	f.mu.Unlock()
	if err := f.o.reserve(int64(len(data)) - old); err != nil {
		return opfsErr("QuotaExceededError", "The origin private file system quota was exceeded.")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	op, err := f.begin()
	if err != nil {
		return err
	}
	defer op.end()
	id, isDir, ok := op.walk(parts)
	if !ok || isDir {
		return errNotFound
	}
	n, _ := op.node(id)
	stored, blob, err := f.o.packValue(data)
	if err != nil {
		return errf("UnknownError", "%v", err)
	}
	if blob != 0 {
		op.created = append(op.created, blob)
		var sz [8]byte
		binary.BigEndian.PutUint64(sz[:], uint64(len(data)))
		op.b.Put(blobKey(blob), sz[:])
	}
	if v, ok := op.k.get(nodeKey(pOPFSData, id)); ok {
		if bid, _, vok := valueInfo(v); vok && bid != 0 && isBlobValue(v) {
			op.b.Delete(blobKey(bid))
			op.blobs = append(op.blobs, bid)
		}
	}
	op.b.Put(nodeKey(pOPFSData, id), stored)
	op.b.Put(nodeKey(pOPFSNode, id), encNode(opfsNodeRec{mtime: op.now, size: int64(len(data))}))
	op.delta += int64(len(data)) - n.size
	return op.commit()
}

// Move renames or moves a node within the tree.
func (f *OPFS) Move(from, to []string) error {
	if len(from) == 0 || len(to) == 0 {
		return opfsErr("NoModificationAllowedError", "The root directory cannot be moved.")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	op, err := f.begin()
	if err != nil {
		return err
	}
	defer op.end()
	sp, spdir, ok1 := op.walk(from[:len(from)-1])
	dp, dpdir, ok2 := op.walk(to[:len(to)-1])
	if !ok1 || !ok2 || !spdir || !dpdir {
		return errNotFound
	}
	sname, dname := from[len(from)-1], to[len(to)-1]
	id, isDir, found := op.kid(sp, sname)
	if !found {
		return errNotFound
	}
	if isDir && len(to) > len(from) {
		inside := true
		for i := range from {
			if from[i] != to[i] {
				inside = false
				break
			}
		}
		if inside {
			return opfsErr("InvalidModificationError", "The object can not be modified in this way.")
		}
	}
	if eid, edir, exists := op.kid(dp, dname); exists {
		if edir || isDir {
			return opfsErr("InvalidModificationError", "The object can not be modified in this way.")
		}
		if eid != id {
			op.freeTree(eid, false) // the file it overwrites
		}
	}
	op.b.Delete(kidKey(sp, sname))
	op.putKid(dp, dname, id, isDir)
	n, _ := op.node(id)
	n.mtime = op.now
	op.b.Put(nodeKey(pOPFSNode, id), encNode(n))
	op.touchDir(sp)
	if dp != sp {
		op.touchDir(dp)
	}
	return op.commit()
}
