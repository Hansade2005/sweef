package engine

// storage_host.go: IndexedDB and navigator.storage for page scripts (and for
// service workers, which run in a hidden tab, see sw_vm.go).
//
// internal/sitestore holds the data and the transaction rules; idb_prelude.js
// is the script-facing API. This file is the glue: it hands each browsing
// context its sitestore.Manager (persistent when the context has a storage
// directory, memory only otherwise: incognito and throw-away agent contexts
// never touch the disk), exposes the manager's operations as __sw.idb* host
// functions, and turns the manager's notifications (a transaction may start, a
// versionchange is coming) into events on the script VM's event loop.

import (
	_ "embed"
	"encoding/json"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/sitestore"
)

//go:embed idb_prelude.js
var idbPreludeJS string

func init() {
	registerPrelude("storage_idb", idbPreludeJS)
	registerHostExtension("storage_idb", func(j *jsVM) map[string]hostFn { return (&storageHost{j: j}).fns() })
	registerAsyncHandler("storage", func(j *jsVM, e asyncEvent) {
		if e.typ != "task" {
			if _, err := j.call("__swStorageEvent", e.data); err != nil && j.tab != nil {
				j.tab.LogConsole("error", jsErrorText(err, "storage event"))
			}
			j.jobs()
			return
		}
		// Run queued tasks one after the other, the microtasks of each before the next starts (that is what keeps a
		// transaction active across an await). A bounded batch, so timers and input are not starved.
		deadline := time.Now().Add(4 * time.Millisecond)
		for i := 0; i < 512; i++ {
			r, err := j.call("__swIdbTask")
			if err != nil {
				if j.tab != nil {
					j.tab.LogConsole("error", jsErrorText(err, "storage task"))
				}
				return
			}
			j.jobs()
			if n, _ := r.(int); n == 0 {
				return
			}
			if i%16 == 15 && time.Now().After(deadline) {
				break
			}
		}
		j.rt().push(asyncEvent{kind: "storage", typ: "task"}) // more is queued: continue on the next turn
	})
}

// ---- extension points added by the storage/service worker files --------------------------------

var closeHooks []func(j *jsVM)

// registerCloseHook runs f when a script VM is closed (called from init functions only).
func registerCloseHook(f func(j *jsVM)) { closeHooks = append(closeHooks, f) }

func runCloseHooks(j *jsVM) {
	for _, f := range closeHooks {
		f(j)
	}
}

// ---- per-context storage ------------------------------------------------------------------------

type siteHolder struct {
	once sync.Once
	mgr  atomic.Pointer[sitestore.Manager]
	swm  swHolder
}

// SiteStorage is the context's origin-scoped storage (IndexedDB, Cache Storage, service worker data).
func (c *BrowserContext) SiteStorage() *sitestore.Manager {
	c.site.once.Do(func() {
		root := c.options.StorageDir
		if root == "" && c.ID == "c_default" {
			root = c.eng.opts.StorageDir
		}
		quota := int64(c.eng.opts.StorageQuotaMiB) << 20
		c.site.mgr.Store(sitestore.NewManager(root, quota))
	})
	return c.site.mgr.Load()
}

// siteClose flushes the context's storage (and stops its service workers) when the context goes away.
func (c *BrowserContext) siteClose() {
	c.swShutdown()
	if m := c.site.mgr.Load(); m != nil {
		m.Close()
	}
}

// ClearSiteData deletes the IndexedDB databases, Cache Storage and service worker registrations of one
// origin ("" = every origin of the context). Open connections are closed by force.
func (c *BrowserContext) ClearSiteData(origin string) {
	c.swUnregisterAll(origin)
	c.SiteStorage().ClearOrigin(origin)
}

// siteOrigin is the storage origin of the page the VM runs for: scheme://host[:port] of an http(s) document,
// "" for opaque origins (about:blank, data:, file:), which have no storage.
func siteOriginOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// potentiallyTrustworthy reports whether a URL is a secure context: https, or http on a loopback host.
func potentiallyTrustworthy(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "https", "wss", "file":
		return true
	case "http":
		h := u.Hostname()
		if h == "localhost" || strings.HasSuffix(h, ".localhost") {
			return true
		}
		if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
			return true
		}
	}
	return false
}

// ---- notifications to a VM ----------------------------------------------------------------------

// vmSink delivers sitestore notifications to the event loop of one script VM.
type vmSink struct{ j *jsVM }

func (s *vmSink) Notify(m sitestore.Msg) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	s.j.rt().push(asyncEvent{kind: "storage", typ: "event", data: string(b)})
	if t := s.j.tab; t != nil {
		t.wake()
	}
}

var sinkMu sync.Mutex
var sinks = map[*jsVM]*vmSink{}

func sinkFor(j *jsVM) *vmSink {
	sinkMu.Lock()
	defer sinkMu.Unlock()
	s := sinks[j]
	if s == nil {
		s = &vmSink{j}
		sinks[j] = s
	}
	return s
}

func init() {
	registerCloseHook(func(j *jsVM) {
		sinkMu.Lock()
		s := sinks[j]
		delete(sinks, j)
		sinkMu.Unlock()
		if s != nil && j.tab != nil {
			j.tab.Context().SiteStorage().DropSink(s)
		}
	})
}

// ---- host functions -----------------------------------------------------------------------------

type storageHost struct{ j *jsVM }

func (h *storageHost) origin() string {
	if h.j.tab == nil {
		return ""
	}
	return siteOriginOf(h.j.tab.URL())
}

func (h *storageHost) mgr() *sitestore.Manager { return h.j.tab.Context().SiteStorage() }

func (h *storageHost) site() (*sitestore.Origin, error) {
	o := h.origin()
	if o == "" {
		return nil, &sitestore.Error{Name: "SecurityError", Msg: "storage is not available for this document's origin"}
	}
	return h.mgr().Origin(o), nil
}

func strSlice(v any) []string {
	a, _ := normalizeJS(v).([]any)
	out := make([]string, 0, len(a))
	for _, x := range a {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func rangeArgs(a []any, i int) sitestore.Range {
	return sitestore.Range{Lo: argS(a, i), Hi: argS(a, i+1), LoOpen: argB(a, i+2), HiOpen: argB(a, i+3)}
}

func (h *storageHost) fns() map[string]hostFn {
	j := h.j
	m := h.mgr
	return map[string]hostFn{
		"idbAvail": func(a []any) (any, error) { return h.origin() != "", nil },
		"secureContext": func(a []any) (any, error) {
			return j.tab != nil && potentiallyTrustworthy(j.tab.URL()), nil
		},
		"idbSched": func(a []any) (any, error) {
			j.rt().push(asyncEvent{kind: "storage", typ: "task"})
			if j.tab != nil {
				j.tab.wake()
			}
			return nil, nil
		},
		"idbOpen": func(a []any) (any, error) {
			o, err := h.site()
			if err != nil {
				return nil, err
			}
			d, err := o.DB(argS(a, 1))
			if err != nil {
				return nil, err
			}
			d.Open(argI(a, 0), sinkFor(j), argI(a, 2))
			return nil, nil
		},
		"idbDelete": func(a []any) (any, error) {
			o, err := h.site()
			if err != nil {
				return nil, err
			}
			d, err := o.DB(argS(a, 1))
			if err != nil {
				return nil, err
			}
			d.Delete(argI(a, 0), sinkFor(j))
			return nil, nil
		},
		"idbDatabases": func(a []any) (any, error) {
			o, err := h.site()
			if err != nil {
				return nil, err
			}
			out := []any{}
			for _, d := range o.Databases() {
				out = append(out, []any{d.Name, d.Version})
			}
			return out, nil
		},
		"idbMeta": func(a []any) (any, error) { return m().Meta(argI(a, 0)) },
		"idbVcDone": func(a []any) (any, error) {
			m().VersionChangeDone(argI(a, 0))
			return nil, nil
		},
		"idbClose": func(a []any) (any, error) {
			m().CloseConn(argI(a, 0))
			return nil, nil
		},
		"idbBegin": func(a []any) (any, error) {
			id, started, err := m().Begin(argI(a, 0), strSlice(argAt(a, 1)), int(argI(a, 2)))
			if err != nil {
				return nil, err
			}
			return []any{id, started}, nil
		},
		"idbCommit": func(a []any) (any, error) { return nil, m().Commit(argI(a, 0)) },
		"idbAbort": func(a []any) (any, error) {
			m().Abort(argI(a, 0))
			return nil, nil
		},
		"idbStoreMeta": func(a []any) (any, error) { return m().StoreMeta(argI(a, 0), argS(a, 1)) },
		"idbCreateStore": func(a []any) (any, error) {
			return nil, m().CreateStore(argI(a, 0), argS(a, 1), argS(a, 2), argB(a, 3), argB(a, 4))
		},
		"idbDeleteStore": func(a []any) (any, error) { return nil, m().DeleteStore(argI(a, 0), argS(a, 1)) },
		"idbRenameStore": func(a []any) (any, error) { return nil, m().RenameStore(argI(a, 0), argS(a, 1), argS(a, 2)) },
		"idbAllRecords": func(a []any) (any, error) {
			r, err := m().AllRecords(argI(a, 0), argS(a, 1))
			if r == nil {
				r = []any{}
			}
			return r, err
		},
		"idbCreateIndex": func(a []any) (any, error) {
			var ents []sitestore.IndexEntry
			for _, e := range asSlice(argAt(a, 6)) {
				p := asSlice(e)
				if len(p) == 2 {
					pk, _ := p[0].(string)
					ents = append(ents, sitestore.IndexEntry{PK: pk, Keys: strSlice(p[1])})
				}
			}
			return nil, m().CreateIndex(argI(a, 0), argS(a, 1), argS(a, 2), argS(a, 3), argB(a, 4), argB(a, 5), ents)
		},
		"idbDeleteIndex": func(a []any) (any, error) { return nil, m().DeleteIndex(argI(a, 0), argS(a, 1), argS(a, 2)) },
		"idbRenameIndex": func(a []any) (any, error) {
			return nil, m().RenameIndex(argI(a, 0), argS(a, 1), argS(a, 2), argS(a, 3))
		},
		"idbGenKey": func(a []any) (any, error) { return m().GenKey(argI(a, 0), argS(a, 1)) },
		"idbPut": func(a []any) (any, error) {
			var ik map[string][]string
			if mm, ok := normalizeJS(argAt(a, 5)).(map[string]any); ok && len(mm) > 0 {
				ik = make(map[string][]string, len(mm))
				for k, v := range mm {
					ik[k] = strSlice(v)
				}
			}
			return m().Put(argI(a, 0), argS(a, 1), argS(a, 2), argS(a, 3), argB(a, 4), ik)
		},
		"idbDelRange": func(a []any) (any, error) {
			return nil, m().DeleteRange(argI(a, 0), argS(a, 1), rangeArgs(a, 2))
		},
		"idbClear": func(a []any) (any, error) { return nil, m().Clear(argI(a, 0), argS(a, 1)) },
		"idbQuery": func(a []any) (any, error) {
			kind := argS(a, 3)
			if kind == "" {
				kind = "V"
			}
			r, err := m().Query(argI(a, 0), argS(a, 1), argS(a, 2), kind[0], rangeArgs(a, 4), int(argI(a, 8)))
			if r == nil {
				r = []any{}
			}
			return r, err
		},
		"idbCursor": func(a []any) (any, error) {
			q := sitestore.CursorReq{
				Index: argS(a, 2), Range: rangeArgs(a, 3), Dir: int(argI(a, 7)),
				HasCur: argB(a, 8), CurKey: argS(a, 9), CurPK: argS(a, 10),
				Target: argS(a, 11), TargetPK: argS(a, 12), Advance: int(argI(a, 13)), KeyOnly: argB(a, 14),
			}
			r, err := m().Cursor(argI(a, 0), argS(a, 1), q)
			if r == nil {
				r = []any{}
			}
			return r, err
		},
		"storageEstimate": func(a []any) (any, error) {
			o, err := h.site()
			if err != nil {
				return map[string]any{"usage": 0, "quota": 0, "indexedDB": 0, "caches": 0, "serviceWorkers": 0}, nil
			}
			u := o.Usage()
			return map[string]any{"usage": u.Total, "quota": o.Quota(), "indexedDB": u.IndexedDB, "caches": u.Caches, "serviceWorkers": u.Other}, nil
		},
		"storagePersist": func(a []any) (any, error) {
			o, err := h.site()
			if err != nil || !m().Persistent() {
				return false, nil
			}
			o.SetPersisted(true)
			return true, nil
		},
		"storagePersisted": func(a []any) (any, error) {
			o, err := h.site()
			if err != nil {
				return false, nil
			}
			return o.Persisted(), nil
		},
	}
}

func asSlice(v any) []any {
	s, _ := normalizeJS(v).([]any)
	return s
}
