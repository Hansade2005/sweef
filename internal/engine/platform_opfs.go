package engine

// platform_opfs.go: storage behind navigator.storage.getDirectory(), the
// Origin Private File System. The tree is memory-backed and shared by every
// document and worker of the same storage key (origin, or origin plus top-level
// site in a third-party context, see partition.go) inside one BrowserContext:
// it survives navigations and reloads, not a restart, and a private window's
// context shares nothing with the normal window's.
// platformjs/opfs.js builds the handle classes on top of it. FileSystemSyncAccessHandle
// (createSyncAccessHandle, dedicated workers only) works in place on the file's
// bytes under an exclusive lock: while a sync access handle is open no other
// sync handle or writable stream can be opened on the file, and it cannot be
// removed or moved.

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const opfsQuota = 256 << 20 // bytes per storage key

type opfsNode struct {
	dir     bool
	data    []byte
	kids    map[string]*opfsNode
	mtime   int64
	sync    bool // a FileSystemSyncAccessHandle is open on the file
	writers int  // open FileSystemWritableFileStreams
}

type opfsHandle struct {
	node   *opfsNode
	writer bool // a writable stream's reservation rather than a sync access handle
	vm     *jsVM
}

type opfsOrigin struct {
	mu      sync.Mutex
	root    *opfsNode
	size    int64
	handles map[int64]*opfsHandle
	next    int64
}

// opfsFor returns the tree of one storage key in this context.
func (h *workerHost) opfsFor(key string) *opfsOrigin {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.opfs == nil {
		h.opfs = map[string]*opfsOrigin{}
	}
	o := h.opfs[key]
	if o == nil {
		o = &opfsOrigin{root: &opfsNode{dir: true, kids: map[string]*opfsNode{}}, handles: map[int64]*opfsHandle{}}
		h.opfs[key] = o
	}
	return o
}

// opfsClear forgets the trees of every storage key that holds state of origin ("" = all).
func (c *BrowserContext) opfsClear(origin string) {
	h := c.wk.Load()
	if h == nil {
		return
	}
	h.mu.Lock()
	for k := range h.opfs {
		if keyMatches(k, origin) {
			delete(h.opfs, k)
		}
	}
	h.mu.Unlock()
}

func opfsSplit(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// walk returns the node at the path (nil when absent).
func (o *opfsOrigin) walk(parts []string) *opfsNode {
	n := o.root
	for _, p := range parts {
		if n == nil || !n.dir {
			return nil
		}
		n = n.kids[p]
	}
	return n
}

func opfsErr(name, msg string) error { return fmt.Errorf("%s: %s", name, msg) }

func opfsTotal(n *opfsNode) int64 {
	if !n.dir {
		return int64(len(n.data))
	}
	var t int64
	for _, k := range n.kids {
		t += opfsTotal(k)
	}
	return t
}

// opfsLocked reports whether the node, or anything below it, has an open sync access handle. Writable streams
// work on a swap file and do not stop the file from being removed or moved (their close then fails).
func opfsLocked(n *opfsNode) bool {
	if !n.dir {
		return n.sync
	}
	for _, k := range n.kids {
		if opfsLocked(k) {
			return true
		}
	}
	return false
}

var errOPFSLocked = opfsErr("NoModificationAllowedError", "An attempt was made to modify an object where modifications are not allowed.")

// opfsKey is the storage key of the tab's document ("" when its origin is opaque and it has no private file system).
func (t *Tab) opfsKey() string {
	if o := t.docOrigin(); o != "" {
		return t.storageKeyFor(o)
	}
	return ""
}

var opfsVMs sync.Map // *jsVM -> struct{}, VMs that hold handles

func init() {
	registerCloseHook(func(j *jsVM) {
		if _, ok := opfsVMs.LoadAndDelete(j); !ok || j.tab == nil {
			return
		}
		h := j.tab.Context().wk.Load()
		if h == nil {
			return
		}
		h.mu.Lock()
		trees := make([]*opfsOrigin, 0, len(h.opfs))
		for _, o := range h.opfs {
			trees = append(trees, o)
		}
		h.mu.Unlock()
		for _, o := range trees {
			o.mu.Lock()
			for id, hd := range o.handles {
				if hd.vm == j {
					o.release(id)
				}
			}
			o.mu.Unlock()
		}
	})
}

// release frees a handle's lock. o.mu is held.
func (o *opfsOrigin) release(id int64) {
	hd := o.handles[id]
	if hd == nil {
		return
	}
	delete(o.handles, id)
	if hd.writer {
		if hd.node.writers > 0 {
			hd.node.writers--
		}
	} else {
		hd.node.sync = false
	}
}

func (j *jsVM) opfsHost(f map[string]hostFn) {
	f["opfs"] = func(a []any) (any, error) {
		if j.tab == nil {
			return nil, opfsErr("SecurityError", "Storage is not available here.")
		}
		key := j.tab.opfsKey()
		if key == "" {
			return nil, opfsErr("SecurityError", "Storage is disabled inside 'data:' URLs and for opaque origins.")
		}
		o := j.tab.Context().workers().opfsFor(key)
		o.mu.Lock()
		defer o.mu.Unlock()
		op, parts := argS(a, 0), opfsSplit(argS(a, 1))
		now := time.Now().UnixMilli()
		switch op {
		case "usage":
			return map[string]any{"usage": o.size, "quota": opfsQuota}, nil
		case "stat":
			n := o.walk(parts)
			if n == nil {
				return nil, nil
			}
			kind := "file"
			if n.dir {
				kind = "directory"
			}
			return map[string]any{"kind": kind, "size": len(n.data), "mtime": n.mtime}, nil
		case "get": // (path, kind, create): get or create a child
			kind, create := argS(a, 2), argB(a, 3)
			if len(parts) == 0 {
				return nil, nil
			}
			parent := o.walk(parts[:len(parts)-1])
			if parent == nil || !parent.dir {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			name := parts[len(parts)-1]
			n := parent.kids[name]
			if n != nil {
				if n.dir != (kind == "directory") {
					return nil, opfsErr("TypeMismatchError", "The path supplied exists, but was not an entry of requested type.")
				}
				return true, nil
			}
			if !create {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			n = &opfsNode{dir: kind == "directory", mtime: now}
			if n.dir {
				n.kids = map[string]*opfsNode{}
			}
			parent.kids[name] = n
			parent.mtime = now
			return true, nil
		case "list":
			n := o.walk(parts)
			if n == nil || !n.dir {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			names := make([]string, 0, len(n.kids))
			for k := range n.kids {
				names = append(names, k)
			}
			sort.Strings(names)
			out := make([]any, 0, len(names))
			for _, k := range names {
				kind := "file"
				if n.kids[k].dir {
					kind = "directory"
				}
				out = append(out, []any{k, kind})
			}
			return out, nil
		case "remove": // (path, recursive)
			if len(parts) == 0 {
				return nil, opfsErr("NoModificationAllowedError", "The root directory cannot be removed.")
			}
			parent := o.walk(parts[:len(parts)-1])
			if parent == nil || !parent.dir {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			name := parts[len(parts)-1]
			n := parent.kids[name]
			if n == nil {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			if n.dir && len(n.kids) > 0 && !argB(a, 2) {
				return nil, opfsErr("InvalidModificationError", "The object can not be modified in this way.")
			}
			if opfsLocked(n) {
				return nil, errOPFSLocked
			}
			o.size -= opfsTotal(n)
			delete(parent.kids, name)
			parent.mtime = now
			return true, nil
		case "read":
			n := o.walk(parts)
			if n == nil || n.dir {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			return map[string]any{"d": base64.StdEncoding.EncodeToString(n.data), "mtime": n.mtime}, nil
		case "write":
			n := o.walk(parts)
			if n == nil || n.dir {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			data, err := base64.StdEncoding.DecodeString(argS(a, 2))
			if err != nil {
				return nil, opfsErr("TypeError", "bad data")
			}
			if n.sync {
				return nil, errOPFSLocked
			}
			if o.size-int64(len(n.data))+int64(len(data)) > opfsQuota {
				return nil, opfsErr("QuotaExceededError", "The origin private file system quota was exceeded.")
			}
			o.size += int64(len(data)) - int64(len(n.data))
			n.data, n.mtime = data, now
			return true, nil
		case "move": // (path, newPath): rename or move within the tree
			np := opfsSplit(argS(a, 2))
			if len(parts) == 0 || len(np) == 0 {
				return nil, opfsErr("NoModificationAllowedError", "The root directory cannot be moved.")
			}
			sp := o.walk(parts[:len(parts)-1])
			dp := o.walk(np[:len(np)-1])
			if sp == nil || dp == nil || !dp.dir || sp.kids[parts[len(parts)-1]] == nil {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			n := sp.kids[parts[len(parts)-1]]
			if ex := dp.kids[np[len(np)-1]]; ex != nil && (ex.dir || n.dir) {
				return nil, opfsErr("InvalidModificationError", "The object can not be modified in this way.")
			}
			if opfsLocked(n) {
				return nil, errOPFSLocked
			}
			if ex := dp.kids[np[len(np)-1]]; ex != nil {
				if opfsLocked(ex) {
					return nil, errOPFSLocked
				}
				o.size -= opfsTotal(ex)
			}
			delete(sp.kids, parts[len(parts)-1])
			dp.kids[np[len(np)-1]] = n
			n.mtime = now
			return true, nil

		// ---- locks: writable streams and synchronous access handles ----
		case "writerOpen": // (path): reserve the file for a writable stream; fails while a sync access handle is open
			n := o.walk(parts)
			if n == nil || n.dir {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			if n.sync {
				return nil, errOPFSLocked
			}
			n.writers++
			return o.newHandle(&opfsHandle{node: n, writer: true, vm: j}), nil
		case "writerClose": // (path, id)
			o.release(argI(a, 2))
			return nil, nil
		case "syncOpen": // (path): exclusive lock, returns [id, size]
			n := o.walk(parts)
			if n == nil || n.dir {
				return nil, opfsErr("NotFoundError", "A requested file or directory could not be found.")
			}
			if n.sync || n.writers > 0 {
				return nil, errOPFSLocked
			}
			n.sync = true
			return []any{o.newHandle(&opfsHandle{node: n, vm: j}), len(n.data)}, nil
		case "syncClose": // (path, id)
			o.release(argI(a, 2))
			return nil, nil
		case "syncRead": // (path, id, at, len) -> base64 of up to len bytes from at
			hd := o.handles[argI(a, 2)]
			if hd == nil || hd.writer {
				return nil, opfsErr("InvalidStateError", "The access handle is closed.")
			}
			at, n := argI(a, 3), argI(a, 4)
			d := hd.node.data
			if at < 0 || at >= int64(len(d)) || n <= 0 {
				return "", nil
			}
			end := at + n
			if end > int64(len(d)) {
				end = int64(len(d))
			}
			return base64.StdEncoding.EncodeToString(d[at:end]), nil
		case "syncWrite": // (path, id, at, base64) -> bytes written
			hd := o.handles[argI(a, 2)]
			if hd == nil || hd.writer {
				return nil, opfsErr("InvalidStateError", "The access handle is closed.")
			}
			data, err := base64.StdEncoding.DecodeString(argS(a, 4))
			if err != nil {
				return nil, opfsErr("TypeError", "bad data")
			}
			at := argI(a, 3)
			if at < 0 {
				return nil, opfsErr("TypeError", "Invalid offset.")
			}
			nd := hd.node
			end := at + int64(len(data))
			if grow := end - int64(len(nd.data)); grow > 0 {
				if o.size+grow > opfsQuota {
					return nil, opfsErr("QuotaExceededError", "The origin private file system quota was exceeded.")
				}
				if end > int64(cap(nd.data)) {
					nb := make([]byte, end, end+end/4+64)
					copy(nb, nd.data)
					nd.data = nb
				} else {
					nd.data = nd.data[:end]
				}
				o.size += grow
			}
			copy(nd.data[at:], data)
			nd.mtime = now
			return len(data), nil
		case "syncSize": // (path, id)
			hd := o.handles[argI(a, 2)]
			if hd == nil || hd.writer {
				return nil, opfsErr("InvalidStateError", "The access handle is closed.")
			}
			return len(hd.node.data), nil
		case "syncTruncate": // (path, id, size)
			hd := o.handles[argI(a, 2)]
			if hd == nil || hd.writer {
				return nil, opfsErr("InvalidStateError", "The access handle is closed.")
			}
			size := argI(a, 3)
			if size < 0 {
				return nil, opfsErr("TypeError", "Invalid size.")
			}
			nd := hd.node
			cur := int64(len(nd.data))
			switch {
			case size < cur:
				nd.data = nd.data[:size]
			case size > cur:
				if o.size+(size-cur) > opfsQuota {
					return nil, opfsErr("QuotaExceededError", "The origin private file system quota was exceeded.")
				}
				nb := make([]byte, size)
				copy(nb, nd.data)
				nd.data = nb
			}
			o.size += size - cur
			nd.mtime = now
			return true, nil
		}
		return nil, opfsErr("TypeError", "unknown opfs operation")
	}
}

func (o *opfsOrigin) newHandle(h *opfsHandle) int64 {
	o.next++
	o.handles[o.next] = h
	if h.vm != nil {
		opfsVMs.Store(h.vm, struct{}{})
	}
	return o.next
}
