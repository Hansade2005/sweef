package engine

// workers.go: dedicated Workers and SharedWorkers.
//
// A worker is a complete script VM (bgvm.go) on its own goroutine, bound to a hidden tab that carries the storage
// partition of the document that created it. Because it is the page VM, a worker has fetch, timers, IndexedDB,
// Cache Storage, the Origin Private File System, BroadcastChannel, Web Locks, WebAssembly, structured clone with
// transfer and nested workers, without a second copy of any of them; worker_scope.js turns the global into a
// DedicatedWorkerGlobalScope / SharedWorkerGlobalScope. Everything the worker receives arrives through its own
// event queue and goroutine; nothing is ever delivered through the creating page's queue except what the worker
// posts back to it.
//
// Messages cross as a structured-clone string (idb_prelude.js __swSer) plus the endpoints of the MessagePorts in
// the transfer list (the MessageChannel bridge of sw_host.go). The page side is workers_prelude.js.

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

//go:embed workers_prelude.js
var workersPreludeJS string

//go:embed worker_scope.js
var workerScopeJS string

var winSeq atomic.Int64
var wkSeq atomic.Int64

// maxWorkersPerVM bounds the dedicated workers one document or worker may have running.
const maxWorkersPerVM = 32

func init() {
	registerPrelude("workers", workersPreludeJS)
	registerHostExtension("workers", func(j *jsVM) map[string]hostFn { return (&wkHost{j: j}).fns() })
	registerAsyncHandler("wk", func(j *jsVM, e asyncEvent) {
		if _, err := j.call("__swWkEvent", e.data); err != nil && j.tab != nil {
			j.tab.LogConsole("error", jsErrorText(err, "worker event"))
		}
		j.jobs()
	})
}

// pushWk queues an event for a VM's workers_prelude.js.
func pushWk(j *jsVM, msg map[string]any) {
	b, err := json.Marshal(msg)
	if err != nil || j == nil {
		return
	}
	j.rt().push(asyncEvent{kind: "wk", typ: "event", data: string(b)})
	if t := j.tab; t != nil {
		t.wake()
	}
}

func jsErr(name, f string, a ...any) error { return fmt.Errorf("%s: %s", name, fmt.Sprintf(f, a...)) }

// ---- per-context registries -------------------------------------------------------------------------

type workerHost struct {
	mu     sync.Mutex
	shared map[string]*bgWorker
	closed bool
	bc     bcHub
}

func (c *BrowserContext) workers() *workerHost {
	if h := c.wk.Load(); h != nil {
		return h
	}
	h := &workerHost{shared: map[string]*bgWorker{}}
	h.bc.init()
	if !c.wk.CompareAndSwap(nil, h) {
		return c.wk.Load()
	}
	return h
}

// workersShutdown stops the shared workers of a closing context.
func (c *BrowserContext) workersShutdown() {
	h := c.wk.Load()
	if h == nil {
		return
	}
	h.mu.Lock()
	h.closed = true
	var ws []*bgWorker
	for _, w := range h.shared {
		ws = append(ws, w)
	}
	h.mu.Unlock()
	for _, w := range ws {
		w.kill()
	}
}

// vmBG is what one VM owns: the dedicated workers it created, its shared worker connections, its channels.
type vmBG struct {
	j       *jsVM
	mu      sync.Mutex
	workers map[int64]*bgWorker
	shared  map[int64]*bgWorker
	bcs     []int64
}

func (j *jsVM) bgState() *vmBG {
	r := j.rt()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.bg == nil {
		r.bg = &vmBG{j: j, workers: map[int64]*bgWorker{}, shared: map[int64]*bgWorker{}}
	}
	return r.bg
}

func (b *vmBG) closeAll() {
	b.mu.Lock()
	ws := make([]*bgWorker, 0, len(b.workers))
	for _, w := range b.workers {
		ws = append(ws, w)
	}
	sh := make([]*bgWorker, 0, len(b.shared))
	for _, w := range b.shared {
		sh = append(sh, w)
	}
	b.workers, b.shared = map[int64]*bgWorker{}, map[int64]*bgWorker{}
	bcs := b.bcs
	b.bcs = nil
	b.mu.Unlock()
	if b.j.tab != nil {
		if h := b.j.tab.Context().wk.Load(); h != nil {
			h.bc.vmGone(bcs)
		}
	}
	for _, w := range ws {
		w.kill()
	}
	for _, w := range sh {
		w.disconnect(b.j)
	}
}

// bgLiveWork: nothing the page's own loop has to wait for; workers report through its queue.
func (j *jsVM) bgLiveWork() bool {
	if j.tab == nil {
		return false
	}
	return j.tab.framesPending() > 0
}

// ---- the worker object ------------------------------------------------------------------------------

type bgWorker struct {
	id        int64
	host      *workerHost
	kind      string // dedicated | shared
	scriptURL string
	name, typ string
	owner     *jsVM // dedicated: the VM that created it
	ownerTab  *Tab
	key       string // shared: registry key

	mu     sync.Mutex
	rt     *bgRuntime
	ready  bool
	dead   bool
	pre    []func(*bgRuntime)
	owners map[*jsVM]int // shared: connected VMs and their connection counts
	winOwn map[*jsVM]bool
}

var wkRegistry sync.Map // id -> *bgWorker

// run executes f on the worker's goroutine once its script has run (messages sent earlier wait).
func (w *bgWorker) run(f func(rt *bgRuntime)) {
	w.mu.Lock()
	if w.dead {
		w.mu.Unlock()
		return
	}
	if !w.ready {
		w.pre = append(w.pre, f)
		w.mu.Unlock()
		return
	}
	rt := w.rt
	w.mu.Unlock()
	rt.post(func() { f(rt) })
}

// fail tells the owner(s) that the worker could not be started or threw.
func (w *bgWorker) report(msg map[string]any) {
	msg["t"] = "err"
	msg["id"] = w.id
	if w.kind == "dedicated" {
		pushWk(w.owner, msg)
		return
	}
	w.mu.Lock()
	var vs []*jsVM
	for j := range w.owners {
		vs = append(vs, j)
	}
	w.mu.Unlock()
	for _, j := range vs {
		pushWk(j, msg)
	}
}

func (w *bgWorker) boot(src string) {
	t := w.ownerTab
	docURL := w.scriptURL
	if strings.HasPrefix(docURL, "blob:") || strings.HasPrefix(docURL, "data:") {
		docURL = t.URL()
	}
	rt, err := t.eng.newBGRuntime(t, docURL)
	if err != nil {
		w.report(map[string]any{"net": true, "message": err.Error(), "filename": w.scriptURL})
		w.kill()
		return
	}
	rt.tab.wk = w
	if w.kind == "dedicated" && w.owner != nil {
		rt.j.joinCluster(w.owner.agentCluster()) // SharedArrayBuffers can be posted within the owner's agent cluster
	} else {
		rt.tab.isoState.Store(&isolation{}) // a shared worker is its own agent cluster: not isolated here
	}
	w.mu.Lock()
	if w.dead {
		w.mu.Unlock()
		rt.stopAsync()
		return
	}
	w.rt = rt
	w.mu.Unlock()
	if src == "" && w.kind == "dedicated" && w.ownerTab.embedderPolicy() != "" && siteOriginOf(w.scriptURL) != "" {
		// a worker of a COEP document has to carry the policy itself
		r, herr := rt.tab.httpDo(httpOpts{method: "GET", url: w.scriptURL, kind: KindScript})
		msg := ""
		switch {
		case herr != nil:
			msg = fmt.Sprintf("Failed to load worker script %s: %s", w.scriptURL, herr.Error())
		case r.status >= 400:
			msg = fmt.Sprintf("Failed to load worker script %s: HTTP %d", w.scriptURL, r.status)
		case parseCOEP(r.header) == "":
			msg = fmt.Sprintf("Worker script %s was blocked: the creating document sets Cross-Origin-Embedder-Policy and the script response does not.", w.scriptURL)
		default:
			src = string(r.body)
		}
		if msg != "" {
			t.LogConsole("error", msg)
			w.report(map[string]any{"net": true, "message": msg, "filename": w.scriptURL})
			w.kill()
			return
		}
	}
	if src == "" {
		body, status, ferr := rt.tab.fetchResourceKind(w.scriptURL, KindScript)
		if ferr != nil || status >= 400 {
			msg := fmt.Sprintf("Failed to load worker script %s", w.scriptURL)
			if ferr != nil {
				msg += ": " + ferr.Error()
			} else {
				msg += fmt.Sprintf(": HTTP %d", status)
			}
			t.LogConsole("error", msg)
			w.report(map[string]any{"net": true, "message": msg, "filename": w.scriptURL})
			w.kill()
			return
		}
		src = body
	}
	info, _ := json.Marshal(map[string]any{"scriptURL": w.scriptURL, "baseURL": docURL, "name": w.name, "kind": w.kind, "type": w.typ})
	if _, err := rt.j.run(strings.Replace(workerScopeJS, "__WK_INFO__", string(info), 1), "worker scope"); err != nil {
		w.report(map[string]any{"net": true, "message": err.Error(), "filename": w.scriptURL})
		w.kill()
		return
	}
	var runErr error
	if w.typ == "module" {
		runErr = rt.j.runModule(src, w.scriptURL)
	} else {
		_, runErr = rt.j.run(src+"\n//# sourceURL="+w.scriptURL, w.scriptURL)
	}
	if runErr != nil {
		w.report(map[string]any{"message": jsErrorText(runErr, ""), "filename": w.scriptURL})
	}
	w.mu.Lock()
	w.ready = true
	pre := w.pre
	w.pre = nil
	dead := w.dead
	w.mu.Unlock()
	if dead {
		rt.stopAsync()
		return
	}
	for _, f := range pre {
		f := f
		rt.post(func() { f(rt) })
	}
	rt.start()
}

// kill terminates the worker: no more messages are delivered and the VM is torn down in the background.
func (w *bgWorker) kill() {
	w.mu.Lock()
	if w.dead {
		w.mu.Unlock()
		return
	}
	w.dead = true
	rt := w.rt
	w.mu.Unlock()
	wkRegistry.Delete(w.id)
	if w.kind == "dedicated" {
		if b := w.owner.bgIfAny(); b != nil {
			b.mu.Lock()
			delete(b.workers, w.id)
			b.mu.Unlock()
		}
	} else {
		w.host.mu.Lock()
		if w.host.shared[w.key] == w {
			delete(w.host.shared, w.key)
		}
		w.host.mu.Unlock()
	}
	if rt != nil {
		rt.stopAsync()
	}
}

func (j *jsVM) bgIfAny() *vmBG {
	if j == nil || j.realtime == nil {
		return nil
	}
	j.realtime.mu.Lock()
	defer j.realtime.mu.Unlock()
	return j.realtime.bg
}

// connect adds an owner VM to a shared worker.
func (w *bgWorker) connect(j *jsVM) {
	w.mu.Lock()
	if w.owners == nil {
		w.owners = map[*jsVM]int{}
	}
	w.owners[j]++
	w.mu.Unlock()
}

// disconnect removes owner j (all its connections). When no owner is left the shared worker ends.
func (w *bgWorker) disconnect(j *jsVM) {
	w.mu.Lock()
	if j != nil {
		delete(w.owners, j)
	}
	left := len(w.owners)
	w.mu.Unlock()
	if left == 0 {
		w.kill()
	}
}

// ---- host functions ---------------------------------------------------------------------------------

type wkHost struct{ j *jsVM }

// key is the namespace of this VM for BroadcastChannel, Web Locks and SharedWorker: its storage key.
func (h *wkHost) key() string {
	t := h.j.tab
	if t == nil {
		return ""
	}
	if o := t.docOrigin(); o != "" {
		return t.storageKeyFor(o)
	}
	return fmt.Sprintf("opaque:%d", t.winID)
}

func (h *wkHost) ctxHost() *workerHost { return h.j.tab.Context().workers() }

func (h *wkHost) self() *bgWorker {
	if h.j.tab == nil {
		return nil
	}
	return h.j.tab.wk
}

func portsArg(v any) []any {
	l, _ := normalizeJS(v).([]any)
	return l
}

func (h *wkHost) fns() map[string]hostFn {
	j := h.j
	return map[string]hostFn{
		"wkCreate": func(a []any) (any, error) {
			t := j.tab
			if t == nil {
				return nil, jsErr("InvalidStateError", "no document")
			}
			scriptURL, src, typ, name := argS(a, 0), argS(a, 1), argS(a, 2), argS(a, 3)
			if err := h.checkScript(scriptURL, src); err != nil {
				return nil, err
			}
			b := j.bgState()
			b.mu.Lock()
			if len(b.workers) >= maxWorkersPerVM {
				b.mu.Unlock()
				return nil, jsErr("QuotaExceededError", "Too many workers.")
			}
			w := &bgWorker{id: wkSeq.Add(1), host: h.ctxHost(), kind: "dedicated", scriptURL: scriptURL, name: name, typ: typ, owner: j, ownerTab: t}
			b.workers[w.id] = w
			b.mu.Unlock()
			wkRegistry.Store(w.id, w)
			go w.boot(src)
			return w.id, nil
		},
		"wkPost": func(a []any) (any, error) {
			v, ok := wkRegistry.Load(argI(a, 0))
			if !ok {
				return nil, nil
			}
			w := v.(*bgWorker)
			if w.owner != j {
				return nil, nil
			}
			data, ports := argS(a, 1), portsArg(argAt(a, 2))
			w.run(func(rt *bgRuntime) { _, _ = rt.j.call("__swWkDeliver", data, ports) })
			return nil, nil
		},
		"wkTerminate": func(a []any) (any, error) {
			if v, ok := wkRegistry.Load(argI(a, 0)); ok {
				if w := v.(*bgWorker); w.owner == j && w.kind == "dedicated" {
					w.kill()
				}
			}
			return nil, nil
		},
		"wkConnect": func(a []any) (any, error) {
			t := j.tab
			if t == nil {
				return nil, jsErr("InvalidStateError", "no document")
			}
			scriptURL, src, typ, name := argS(a, 0), argS(a, 1), argS(a, 2), argS(a, 3)
			ep := argI(a, 4)
			if err := h.checkScript(scriptURL, src); err != nil {
				return nil, err
			}
			ch := h.ctxHost()
			key := h.key() + "|" + scriptURL + "|" + typ + "|" + name
			ch.mu.Lock()
			if ch.closed {
				ch.mu.Unlock()
				return nil, jsErr("InvalidStateError", "context closed")
			}
			w := ch.shared[key]
			fresh := w == nil
			if fresh {
				w = &bgWorker{id: wkSeq.Add(1), host: ch, kind: "shared", scriptURL: scriptURL, name: name, typ: typ, ownerTab: t, key: key}
				ch.shared[key] = w
			}
			w.connect(j)
			ch.mu.Unlock()
			b := j.bgState()
			b.mu.Lock()
			b.shared[w.id] = w
			b.mu.Unlock()
			if fresh {
				wkRegistry.Store(w.id, w)
				go w.boot(src)
			}
			w.run(func(rt *bgRuntime) { _, _ = rt.j.call("__swWConnect", ep) })
			return w.id, nil
		},
		"wkDisconnect": func(a []any) (any, error) {
			id := argI(a, 0)
			b := j.bgState()
			b.mu.Lock()
			w := b.shared[id]
			delete(b.shared, id)
			b.mu.Unlock()
			if w != nil {
				w.disconnect(j)
			}
			return nil, nil
		},
		// ---- inside a worker ----
		"wkOut": func(a []any) (any, error) {
			w := h.self()
			if w == nil || w.kind != "dedicated" {
				return nil, nil
			}
			pushWk(w.owner, map[string]any{"t": "msg", "id": w.id, "data": argS(a, 0), "ports": portsArg(argAt(a, 1))})
			return nil, nil
		},
		"wkClose": func(a []any) (any, error) {
			if w := h.self(); w != nil {
				w.kill()
			}
			return nil, nil
		},
		"wkError": func(a []any) (any, error) {
			if w := h.self(); w != nil {
				w.report(map[string]any{"message": argS(a, 0), "filename": argS(a, 1), "lineno": argI(a, 2), "colno": argI(a, 3)})
			}
			return nil, nil
		},
		"wkImport": func(a []any) (any, error) {
			t := j.tab
			if t == nil {
				return nil, jsErr("NetworkError", "no document")
			}
			u := argS(a, 0)
			body, status, err := t.fetchResourceKind(u, KindScript)
			if err != nil {
				return nil, jsErr("NetworkError", "Failed to execute 'importScripts' on 'WorkerGlobalScope': The script at '%s' failed to load.", u)
			}
			if status >= 400 {
				return nil, jsErr("NetworkError", "Failed to execute 'importScripts' on 'WorkerGlobalScope': The script at '%s' failed to load (HTTP %d).", u, status)
			}
			return body, nil
		},
		// ---- BroadcastChannel ----
		"bcOpen": func(a []any) (any, error) {
			return h.ctxHost().bc.open(j, h.key(), argS(a, 0), h.originForEvents()), nil
		},
		"bcPost": func(a []any) (any, error) {
			h.ctxHost().bc.post(argI(a, 0), j, argS(a, 1))
			return nil, nil
		},
		"bcClose": func(a []any) (any, error) {
			h.ctxHost().bc.close(argI(a, 0), j)
			return nil, nil
		},
	}
}

func (h *wkHost) originForEvents() string {
	if h.j.tab == nil {
		return ""
	}
	if o := h.j.tab.docOrigin(); o != "" {
		return o
	}
	return "null"
}

// checkScript enforces the same-origin rule for worker scripts (blob and data URLs carry their source).
func (h *wkHost) checkScript(scriptURL, src string) error {
	t := h.j.tab
	if src != "" || strings.HasPrefix(scriptURL, "blob:") || strings.HasPrefix(scriptURL, "data:") {
		return nil
	}
	so := siteOriginOf(scriptURL)
	creator := t.docOrigin()
	if so == "" || so != creator {
		o := creator
		if o == "" {
			o = "null"
		}
		return jsErr("SecurityError", "Script at '%s' cannot be accessed from origin '%s'.", scriptURL, o)
	}
	return nil
}
