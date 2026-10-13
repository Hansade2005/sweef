package engine

// platform_opfs.go: storage behind navigator.storage.getDirectory(), the
// Origin Private File System. The tree is memory-backed and shared by every
// tab of the same origin in this process (it survives navigations and reloads,
// not a restart); platformjs/opfs.js builds the handle classes on top of it.

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const opfsQuota = 256 << 20 // bytes per origin

type opfsNode struct {
	dir   bool
	data  []byte
	kids  map[string]*opfsNode
	mtime int64
}

type opfsOrigin struct {
	mu   sync.Mutex
	root *opfsNode
	size int64
}

var (
	opfsMu      sync.Mutex
	opfsOrigins = map[string]*opfsOrigin{}
)

func opfsFor(origin string) *opfsOrigin {
	opfsMu.Lock()
	defer opfsMu.Unlock()
	o := opfsOrigins[origin]
	if o == nil {
		o = &opfsOrigin{root: &opfsNode{dir: true, kids: map[string]*opfsNode{}}}
		opfsOrigins[origin] = o
	}
	return o
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

func (o *opfsOrigin) total(n *opfsNode) int64 {
	if !n.dir {
		return int64(len(n.data))
	}
	var t int64
	for _, k := range n.kids {
		t += o.total(k)
	}
	return t
}

func (j *jsVM) opfsHost(f map[string]hostFn) {
	origin := func() string {
		if j.tab == nil {
			return "null"
		}
		return j.tab.origin()
	}
	f["opfs"] = func(a []any) (any, error) {
		o := opfsFor(origin())
		o.mu.Lock()
		defer o.mu.Unlock()
		op, parts := argS(a, 0), opfsSplit(argS(a, 1))
		now := time.Now().UnixMilli()
		switch op {
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
			if o.total(o.root)-int64(len(n.data))+int64(len(data)) > opfsQuota {
				return nil, opfsErr("QuotaExceededError", "The origin private file system quota was exceeded.")
			}
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
			delete(sp.kids, parts[len(parts)-1])
			dp.kids[np[len(np)-1]] = n
			n.mtime = now
			return true, nil
		}
		return nil, opfsErr("TypeError", "unknown opfs operation")
	}
}
