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

import (
	"encoding/base64"
	"sync"

	"github.com/sweefbrowser/sweef/internal/sitestore"
)

var (
	opfsFallbackOnce sync.Once
	opfsFallbackMgr  *sitestore.Manager
)

// opfsFor returns the file system of the document's origin. Documents without an http(s) origin (about:blank,
// data:, file:) and script VMs without a tab get a process-wide memory-only tree, as before.
func (j *jsVM) opfsFor() *sitestore.OPFS {
	if j.tab != nil {
		if o := siteOriginOf(j.tab.URL()); o != "" {
			return j.tab.Context().SiteStorage().Origin(o).OPFS()
		}
	}
	opfsFallbackOnce.Do(func() { opfsFallbackMgr = sitestore.NewManager("", 0) })
	name := "null"
	if j.tab != nil {
		name = j.tab.origin()
	}
	return opfsFallbackMgr.Origin(name).OPFS()
}

func (j *jsVM) opfsHost(f map[string]hostFn) {
	f["opfs"] = func(a []any) (any, error) {
		fs := j.opfsFor()
		op, parts := argS(a, 0), opfsSplit(argS(a, 1))
		switch op {
		case "usage":
			var quota int64
			if j.tab != nil {
				if o := siteOriginOf(j.tab.URL()); o != "" {
					quota = j.tab.Context().SiteStorage().Origin(o).Quota()
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
			return map[string]any{"kind": kind, "size": st.Size, "mtime": st.Mtime}, nil
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
			if err := fs.Remove(parts, argB(a, 2)); err != nil {
				return nil, err
			}
			return true, nil
		case "read":
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
			if err := fs.Write(parts, data); err != nil {
				return nil, err
			}
			return true, nil
		case "move": // (path, newPath): rename or move within the tree
			if err := fs.Move(parts, opfsSplit(argS(a, 2))); err != nil {
				return nil, err
			}
			return true, nil
		}
		return nil, &sitestore.Error{Name: "TypeError", Msg: "unknown opfs operation"}
	}
}

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
