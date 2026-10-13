package engine

// platform_opfs.go: host side of navigator.storage.getDirectory(), the Origin
// Private File System. The tree is kept by internal/sitestore (OPFS): its
// nodes live in the origin's ordered store and file contents above 64 KiB are
// files under the origin's storage directory, so nothing is held in memory
// beyond the file an operation is working on, and it counts against the
// origin's quota like IndexedDB and Cache Storage. A context without a storage
// directory (private windows, throw-away agent contexts) keeps the tree in
// memory and discards it with the context. platformjs/opfs.js builds the
// handle classes on top of this.
//
// The tree is keyed by the document's storage key (origin, plus the top-level site when it is embedded, see
// partition.go), so an embed has one file system per site that embeds it.
//
// FileSystemSyncAccessHandle (dedicated workers) works on a buffer: createSyncAccessHandle takes an exclusive lock
// on the file and reads it once, read/write/truncate/getSize are synchronous operations on that buffer, and flush
// and close write it back with one store write. The store has only whole-file operations, so a file held by a sync
// handle is in memory for as long as the handle is open, and a crash loses what was not flushed. While a handle is
// open no second handle or writable stream can be opened on the file, and it cannot be written, removed or moved.

import (
	"encoding/base64"
	"strings"
	"sync"

	"github.com/sweefbrowser/sweef/internal/sitestore"
)

var (
	opfsFallbackOnce sync.Once
	opfsFallbackMgr  *sitestore.Manager
)

// opfsFor returns the file system of the document's storage key. A document with an opaque origin (about:blank,
// data:, a sandboxed frame) or a VM without a tab gets a memory-only tree of its own window.
func (j *jsVM) opfsFor() *sitestore.OPFS {
	if j.tab != nil {
		if key := j.tab.opfsKey(); key != "" {
			return j.tab.Context().SiteStorage().Origin(key).OPFS()
		}
	}
	opfsFallbackOnce.Do(func() { opfsFallbackMgr = sitestore.NewManager("", 0) })
	name := "null"
	if j.tab != nil {
		name = "opaque:" + itoa64(j.tab.winID)
	}
	return opfsFallbackMgr.Origin(name).OPFS()
}

// opfsKey is the storage key of the tab's document ("" when its origin is opaque).
func (t *Tab) opfsKey() string {
	if o := t.docOrigin(); o != "" {
		return t.storageKeyFor(o)
	}
	return ""
}

// ---- locks and sync access handles ---------------------------------------------------------------------

type opfsHandle struct {
	parts  []string
	writer bool // a writable stream's reservation, not a sync access handle
	buf    []byte
	dirty  bool
	vm     *jsVM
}

type opfsLocks struct {
	mu      sync.Mutex
	next    int64
	handles map[int64]*opfsHandle
}

var opfsLockTable sync.Map // *sitestore.OPFS -> *opfsLocks

func opfsLocksOf(fs *sitestore.OPFS) *opfsLocks {
	v, _ := opfsLockTable.LoadOrStore(fs, &opfsLocks{handles: map[int64]*opfsHandle{}})
	return v.(*opfsLocks)
}

func samePath(a, b []string) bool { return strings.Join(a, "/") == strings.Join(b, "/") }

// under reports whether p is path or below it.
func under(p, path []string) bool {
	if len(p) < len(path) {
		return false
	}
	for i := range path {
		if p[i] != path[i] {
			return false
		}
	}
	return true
}

var errOPFSLocked = &sitestore.Error{Name: "NoModificationAllowedError", Msg: "An attempt was made to modify an object where modifications are not allowed."}

// syncLockedUnder reports whether a sync access handle is open on path or anything below it. l.mu is held.
func (l *opfsLocks) syncLockedUnder(path []string) bool {
	for _, h := range l.handles {
		if !h.writer && under(h.parts, path) {
			return true
		}
	}
	return false
}

var opfsVMs sync.Map // *jsVM -> *sitestore.OPFS list owner, for teardown

func init() {
	registerCloseHook(func(j *jsVM) {
		if _, ok := opfsVMs.LoadAndDelete(j); !ok {
			return
		}
		opfsLockTable.Range(func(k, v any) bool {
			fs, l := k.(*sitestore.OPFS), v.(*opfsLocks)
			l.mu.Lock()
			for id, h := range l.handles {
				if h.vm == j {
					if !h.writer && h.dirty {
						_ = fs.Write(h.parts, h.buf)
					}
					delete(l.handles, id)
				}
			}
			l.mu.Unlock()
			return true
		})
	})
}

func (j *jsVM) opfsHost(f map[string]hostFn) {
	f["opfs"] = func(a []any) (any, error) {
		fs := j.opfsFor()
		lk := opfsLocksOf(fs)
		op, parts := argS(a, 0), opfsSplit(argS(a, 1))
		switch op {
		case "usage":
			var quota int64
			if j.tab != nil {
				if key := j.tab.opfsKey(); key != "" {
					quota = j.tab.Context().SiteStorage().Origin(key).Quota()
				}
			}
			if quota == 0 {
				quota = 256 << 20
			}
			return map[string]any{"usage": fs.Usage(), "quota": quota}, nil
		case "stat":
			st, err := fs.Stat(parts)
			if err != nil || st == nil {
				return nil, err
			}
			kind := "file"
			if st.Dir {
				kind = "directory"
			}
			size := st.Size
			lk.mu.Lock()
			for _, h := range lk.handles { // a file held by a sync handle: the handle's size is the file's
				if !h.writer && samePath(h.parts, parts) {
					size = int64(len(h.buf))
				}
			}
			lk.mu.Unlock()
			return map[string]any{"kind": kind, "size": size, "mtime": st.Mtime}, nil
		case "get": // (path, kind, create): get or create a child
			if len(parts) == 0 {
				return nil, nil
			}
			if err := fs.Get(parts, argS(a, 2) == "directory", argB(a, 3)); err != nil {
				return nil, err
			}
			return true, nil
		case "list":
			es, err := fs.List(parts)
			if err != nil {
				return nil, err
			}
			out := make([]any, 0, len(es))
			for _, e := range es {
				kind := "file"
				if e.Dir {
					kind = "directory"
				}
				out = append(out, []any{e.Name, kind})
			}
			return out, nil
		case "remove": // (path, recursive)
			lk.mu.Lock()
			locked := lk.syncLockedUnder(parts)
			lk.mu.Unlock()
			if locked {
				return nil, errOPFSLocked
			}
			if err := fs.Remove(parts, argB(a, 2)); err != nil {
				return nil, err
			}
			return true, nil
		case "read":
			lk.mu.Lock()
			for _, h := range lk.handles {
				if !h.writer && samePath(h.parts, parts) && h.dirty { // the unflushed bytes of an open sync handle
					b := append([]byte(nil), h.buf...)
					lk.mu.Unlock()
					return map[string]any{"d": base64.StdEncoding.EncodeToString(b), "mtime": int64(0)}, nil
				}
			}
			lk.mu.Unlock()
			b, mtime, err := fs.Read(parts)
			if err != nil {
				return nil, err
			}
			return map[string]any{"d": base64.StdEncoding.EncodeToString(b), "mtime": mtime}, nil
		case "write":
			data, err := base64.StdEncoding.DecodeString(argS(a, 2))
			if err != nil {
				return nil, &sitestore.Error{Name: "TypeError", Msg: "bad data"}
			}
			lk.mu.Lock()
			locked := lk.syncLockedUnder(parts)
			lk.mu.Unlock()
			if locked {
				return nil, errOPFSLocked
			}
			if err := fs.Write(parts, data); err != nil {
				return nil, err
			}
			return true, nil
		case "move": // (path, newPath): rename or move within the tree
			to := opfsSplit(argS(a, 2))
			lk.mu.Lock()
			locked := lk.syncLockedUnder(parts) || lk.syncLockedUnder(to)
			lk.mu.Unlock()
			if locked {
				return nil, errOPFSLocked
			}
			if err := fs.Move(parts, to); err != nil {
				return nil, err
			}
			return true, nil

		// ---- writable streams and sync access handles ----
		case "writerOpen": // (path): reserve the file for a writable stream; fails while a sync access handle is open
			st, err := fs.Stat(parts)
			if err != nil {
				return nil, err
			}
			if st == nil || st.Dir {
				return nil, &sitestore.Error{Name: "NotFoundError", Msg: "A requested file or directory could not be found."}
			}
			lk.mu.Lock()
			defer lk.mu.Unlock()
			if lk.syncLockedUnder(parts) {
				return nil, errOPFSLocked
			}
			lk.next++
			lk.handles[lk.next] = &opfsHandle{parts: parts, writer: true, vm: j}
			opfsVMs.Store(j, struct{}{})
			return lk.next, nil
		case "writerClose": // (path, id)
			lk.mu.Lock()
			delete(lk.handles, argI(a, 2))
			lk.mu.Unlock()
			return nil, nil
		case "syncOpen": // (path): exclusive lock, loads the file; returns [id, size]
			st, err := fs.Stat(parts)
			if err != nil {
				return nil, err
			}
			if st == nil || st.Dir {
				return nil, &sitestore.Error{Name: "NotFoundError", Msg: "A requested file or directory could not be found."}
			}
			lk.mu.Lock()
			defer lk.mu.Unlock()
			for _, h := range lk.handles {
				if samePath(h.parts, parts) {
					return nil, errOPFSLocked // another sync handle, or a writable stream
				}
			}
			b, _, err := fs.Read(parts)
			if err != nil {
				return nil, err
			}
			lk.next++
			lk.handles[lk.next] = &opfsHandle{parts: parts, buf: b, vm: j}
			opfsVMs.Store(j, struct{}{})
			return []any{lk.next, len(b)}, nil
		}
		// the remaining operations work on an open sync handle: (op, path, id, ...)
		lk.mu.Lock()
		defer lk.mu.Unlock()
		h := lk.handles[argI(a, 2)]
		if h == nil || h.writer {
			return nil, &sitestore.Error{Name: "InvalidStateError", Msg: "The access handle is closed."}
		}
		switch op {
		case "syncRead": // at, len -> base64 of up to len bytes from at
			at, n := argI(a, 3), argI(a, 4)
			if at < 0 || at >= int64(len(h.buf)) || n <= 0 {
				return "", nil
			}
			end := at + n
			if end > int64(len(h.buf)) {
				end = int64(len(h.buf))
			}
			return base64.StdEncoding.EncodeToString(h.buf[at:end]), nil
		case "syncWrite": // at, base64 -> bytes written
			data, err := base64.StdEncoding.DecodeString(argS(a, 4))
			if err != nil {
				return nil, &sitestore.Error{Name: "TypeError", Msg: "bad data"}
			}
			at := argI(a, 3)
			if at < 0 {
				return nil, &sitestore.Error{Name: "TypeError", Msg: "Invalid offset."}
			}
			if end := at + int64(len(data)); end > int64(len(h.buf)) {
				if end > opfsSyncMax {
					return nil, &sitestore.Error{Name: "QuotaExceededError", Msg: "The access handle's file is too large."}
				}
				nb := make([]byte, end, end+end/4+64)
				copy(nb, h.buf)
				h.buf = nb
			}
			copy(h.buf[at:], data)
			h.dirty = true
			return len(data), nil
		case "syncSize":
			return len(h.buf), nil
		case "syncTruncate":
			size := argI(a, 3)
			if size < 0 {
				return nil, &sitestore.Error{Name: "TypeError", Msg: "Invalid size."}
			}
			if size > opfsSyncMax {
				return nil, &sitestore.Error{Name: "QuotaExceededError", Msg: "The access handle's file is too large."}
			}
			switch {
			case size < int64(len(h.buf)):
				h.buf = h.buf[:size]
			case size > int64(len(h.buf)):
				nb := make([]byte, size)
				copy(nb, h.buf)
				h.buf = nb
			}
			h.dirty = true
			return true, nil
		case "syncFlush":
			if h.dirty {
				if err := fs.Write(h.parts, h.buf); err != nil {
					return nil, err
				}
				h.dirty = false
			}
			return true, nil
		case "syncClose":
			var err error
			if h.dirty {
				err = fs.Write(h.parts, h.buf)
			}
			delete(lk.handles, argI(a, 2))
			return nil, err
		}
		return nil, &sitestore.Error{Name: "TypeError", Msg: "unknown opfs operation"}
	}
}

// opfsSyncMax bounds the file a sync access handle holds in memory.
const opfsSyncMax = 512 << 20

func opfsSplit(p string) []string {
	if p == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i <= len(p); i++ {
		if i == len(p) || p[i] == '/' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	return out
}
