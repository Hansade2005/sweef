package engine

// sw_host.go: the host functions behind navigator.serviceWorker (sw_page_prelude.js) and the
// service worker global scope (sw_scope.js), and the bridge that lets MessageChannel ports
// travel between a document and a worker.

import (
	_ "embed"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

//go:embed sw_page_prelude.js
var swPagePreludeJS string

func init() {
	registerPrelude("sw_page", swPagePreludeJS)
	registerHostExtension("sw", func(j *jsVM) map[string]hostFn { return (&swHost{j: j}).fns() })
	registerAsyncHandler("sw", func(j *jsVM, e asyncEvent) {
		if _, err := j.call("__swSwEvent", e.data); err != nil && j.tab != nil {
			j.tab.LogConsole("error", jsErrorText(err, "service worker event"))
		}
		j.jobs()
	})
	registerCloseHook(func(j *jsVM) {
		if j.tab == nil {
			return
		}
		m := j.tab.Context().swIfStarted()
		if m == nil {
			return
		}
		m.mu.Lock()
		delete(m.subs, j)
		m.mu.Unlock()
		m.ports.dropVM(j)
	})
}

type swHost struct{ j *jsVM }

func (h *swHost) mgr() *swManager { return h.j.tab.Context().sw() }

func (h *swHost) origin() string {
	if h.j.tab == nil {
		return ""
	}
	return siteOriginOf(h.j.tab.URL())
}

func (h *swHost) available() bool {
	return h.j.tab != nil && h.origin() != "" && potentiallyTrustworthy(h.j.tab.URL())
}

func (h *swHost) runtime() *swRuntime {
	if h.j.tab == nil {
		return nil
	}
	return h.j.tab.sw.rt
}

func errToResult(token int64, err error) map[string]any {
	name, msg := "UnknownError", err.Error()
	if e, ok := err.(*swError); ok {
		name, msg = e.name, e.msg
	}
	return map[string]any{"t": "result", "token": token, "ok": false, "name": name, "msg": msg}
}

func clientDesc(c *swClient) map[string]any {
	return map[string]any{"id": c.id, "url": c.tab.URL(), "type": "window", "frameType": "top-level", "visibilityState": "visible", "focused": false}
}

func (m *swManager) regByID(id string) *swReg {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.regs {
		if r.id == id {
			return r
		}
	}
	return nil
}

// sameOriginClients lists the documents of origin, oldest first.
func (m *swManager) clientsOfLocked(origin string) []*swClient {
	var out []*swClient
	for _, c := range m.clients {
		if siteOriginOf(c.url) == origin {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (h *swHost) fns() map[string]hostFn {
	j := h.j
	return map[string]hostFn{
		"swAvailable": func(a []any) (any, error) { return h.available(), nil },
		"swSubscribe": func(a []any) (any, error) {
			if !h.available() {
				return nil, nil
			}
			m := h.mgr()
			m.mu.Lock()
			m.subs[j] = h.origin()
			m.mu.Unlock()
			return nil, nil
		},
		"swController": func(a []any) (any, error) {
			if j.tab == nil {
				return nil, nil
			}
			ctl := j.tab.sw.ctl.Load()
			if ctl == nil {
				return nil, nil
			}
			m := h.mgr()
			m.mu.Lock()
			defer m.mu.Unlock()
			if ctl.reg.active == nil {
				return nil, nil
			}
			return map[string]any{"worker": ctl.reg.active.info(), "reg": ctl.reg.snapshot()}, nil
		},
		"swGetRegistration": func(a []any) (any, error) {
			if !h.available() {
				return nil, nil
			}
			u := argS(a, 0)
			if siteOriginOf(u) != h.origin() {
				return nil, &swError{"SecurityError", "Failed to get a ServiceWorkerRegistration: The URL provided ('" + u + "') is not under this origin."}
			}
			m := h.mgr()
			reg := m.lookup(u)
			if reg == nil {
				return nil, nil
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			return reg.snapshot(), nil
		},
		"swGetRegistrations": func(a []any) (any, error) {
			out := []any{}
			if !h.available() {
				return out, nil
			}
			m := h.mgr()
			m.ensure(h.origin())
			m.mu.Lock()
			defer m.mu.Unlock()
			var list []*swReg
			for _, r := range m.regs {
				if r.origin == h.origin() && !r.unregistered {
					list = append(list, r)
				}
			}
			sort.Slice(list, func(i, k int) bool { return list[i].scope < list[k].scope })
			for _, r := range list {
				out = append(out, r.snapshot())
			}
			return out, nil
		},
		"swRegister": func(a []any) (any, error) {
			token, scriptURL, scope, typ, uvc := argI(a, 0), argS(a, 1), argS(a, 2), argS(a, 3), argS(a, 4)
			if !h.available() {
				return nil, &swError{"SecurityError", "Failed to register a ServiceWorker: this document is not a secure context."}
			}
			scope, err := swPrecheck(h.j.tab.URL(), scriptURL, scope)
			if err != nil {
				return nil, err
			}
			m := h.mgr()
			m.goJob(func() {
				snap, err := m.register(scriptURL, scope, typ, uvc)
				if err != nil {
					pushSW(j, errToResult(token, err))
					return
				}
				pushSW(j, map[string]any{"t": "result", "token": token, "ok": true, "reg": snap})
			})
			return nil, nil
		},
		"swUpdate": func(a []any) (any, error) {
			token := argI(a, 0)
			m := h.mgr()
			reg := m.regByID(argS(a, 1))
			if reg == nil {
				return nil, &swError{"InvalidStateError", "Failed to update a ServiceWorkerRegistration: The registration is unregistered."}
			}
			m.goJob(func() {
				if err := m.update(reg); err != nil {
					pushSW(j, errToResult(token, err))
					return
				}
				m.mu.Lock()
				snap := reg.snapshot()
				m.mu.Unlock()
				pushSW(j, map[string]any{"t": "result", "token": token, "ok": true, "reg": snap})
			})
			return nil, nil
		},
		"swUnregister": func(a []any) (any, error) {
			token := argI(a, 0)
			m := h.mgr()
			reg := m.regByID(argS(a, 1))
			m.goJob(func() {
				ok := reg != nil && m.unregister(reg)
				pushSW(j, map[string]any{"t": "result", "token": token, "ok": true, "value": ok})
			})
			return nil, nil
		},
		"swPostMessage": func(a []any) (any, error) {
			id, data := argI(a, 0), argS(a, 1)
			ports := portList(argAt(a, 2))
			m := h.mgr()
			m.mu.Lock()
			w := m.byWorker[id]
			m.mu.Unlock()
			if w == nil || w.state == "redundant" || w.reg.origin != h.origin() {
				return nil, &swError{"InvalidStateError", "Failed to execute 'postMessage' on 'ServiceWorker': ServiceWorker is in redundant state."}
			}
			var source map[string]any
			if c := j.tab.sw.clientEntry(); c != nil {
				source = clientDesc(c)
			}
			origin := h.origin()
			m.goJob(func() {
				rt, err := m.runtimeFor(w)
				if err != nil {
					return
				}
				_, _ = rt.dispatch("message", map[string]any{"data": data, "origin": origin, "source": source, "ports": ports}, swFetchTimeout)
			})
			return nil, nil
		},
		"swPortPair": func(a []any) (any, error) {
			p, q := h.mgr().ports.pair(j)
			return []any{p, q}, nil
		},
		"swPortAdopt": func(a []any) (any, error) {
			h.mgr().ports.adopt(j, argI(a, 0))
			return nil, nil
		},
		"swPortPost": func(a []any) (any, error) {
			h.mgr().ports.post(argI(a, 0), argS(a, 1))
			return nil, nil
		},
		// ---- worker side ----
		"swOwnWorker": func(a []any) (any, error) {
			rt := h.runtime()
			if rt == nil {
				return nil, nil
			}
			m := h.mgr()
			m.mu.Lock()
			defer m.mu.Unlock()
			return rt.w.info(), nil
		},
		"swOwnRegistration": func(a []any) (any, error) {
			rt := h.runtime()
			if rt == nil {
				return nil, nil
			}
			m := h.mgr()
			m.mu.Lock()
			defer m.mu.Unlock()
			return rt.w.reg.snapshot(), nil
		},
		"swSkipWaiting": func(a []any) (any, error) {
			if rt := h.runtime(); rt != nil {
				m := h.mgr()
				m.goJob(func() { m.skipWaiting(rt.w) })
			}
			return nil, nil
		},
		"swClientsClaim": func(a []any) (any, error) {
			rt := h.runtime()
			if rt == nil {
				return nil, nil
			}
			h.mgr().claim(rt.w.reg)
			return nil, nil
		},
		"swClientsGet": func(a []any) (any, error) {
			rt := h.runtime()
			if rt == nil {
				return nil, nil
			}
			m := h.mgr()
			m.mu.Lock()
			defer m.mu.Unlock()
			if c := m.clients[argS(a, 0)]; c != nil && siteOriginOf(c.url) == rt.w.reg.origin {
				return clientDesc(c), nil
			}
			return nil, nil
		},
		"swClientsMatchAll": func(a []any) (any, error) {
			out := []any{}
			rt := h.runtime()
			if rt == nil {
				return out, nil
			}
			includeUncontrolled := argB(a, 0)
			m := h.mgr()
			m.mu.Lock()
			defer m.mu.Unlock()
			for _, c := range m.clientsOfLocked(rt.w.reg.origin) {
				if c.reg == rt.w.reg || includeUncontrolled {
					out = append(out, clientDesc(c))
				}
			}
			return out, nil
		},
		"swClientPostMessage": func(a []any) (any, error) {
			rt := h.runtime()
			if rt == nil {
				return nil, nil
			}
			m := h.mgr()
			m.mu.Lock()
			c := m.clients[argS(a, 0)]
			from := rt.w.info()
			m.mu.Unlock()
			if c == nil {
				return nil, nil
			}
			c.tab.mu.RLock()
			doc := c.tab.js
			c.tab.mu.RUnlock()
			pushSW(doc, map[string]any{"t": "message", "data": argS(a, 1), "from": from, "ports": portList(argAt(a, 2))})
			return nil, nil
		},
		"swImportScript": func(a []any) (any, error) {
			rt := h.runtime()
			if rt == nil {
				return nil, &swError{"NetworkError", "importScripts is only available in a service worker"}
			}
			return h.mgr().importScript(rt, argS(a, 0))
		},
		"swEventDone": func(a []any) (any, error) {
			rt := h.runtime()
			if rt == nil {
				return nil, nil
			}
			var meta map[string]any
			_ = json.Unmarshal([]byte(argS(a, 1)), &meta)
			res := swEventResult{meta: meta, body: argS(a, 2)}
			if ok, _ := meta["ok"].(bool); !ok {
				res.err, _ = meta["error"].(string)
				if res.err == "" {
					res.err = "the event handler failed"
				}
			}
			rt.finish(argI(a, 0), res)
			return nil, nil
		},
	}
}

func portList(v any) []any {
	out := []any{}
	for _, p := range asSlice(v) {
		out = append(out, p)
	}
	return out
}

// importScript is importScripts() of the worker: scripts fetched while the worker installs are stored with it;
// afterwards only those can be imported.
func (m *swManager) importScript(rt *swRuntime, u string) (string, error) {
	w := rt.w
	m.mu.Lock()
	src, ok := w.imports[u]
	installed := w.installed
	m.mu.Unlock()
	if ok {
		return src, nil
	}
	if installed {
		return "", &swError{"NetworkError", "Failed to execute 'importScripts' on 'WorkerGlobalScope': The script at '" + u + "' was not imported while the service worker was installing."}
	}
	if siteOriginOf(u) == "" {
		return "", &swError{"NetworkError", "Failed to execute 'importScripts': unsupported URL " + u}
	}
	r, err := rt.tab.httpDo(httpOpts{method: "GET", url: u, kind: KindScript})
	if err != nil {
		return "", &swError{"NetworkError", "Failed to execute 'importScripts' on 'WorkerGlobalScope': The script at '" + u + "' failed to load."}
	}
	if r.status < 200 || r.status >= 300 {
		return "", &swError{"NetworkError", "Failed to execute 'importScripts' on 'WorkerGlobalScope': The script at '" + u + "' failed to load (status " + itoa(r.status) + ")."}
	}
	src = string(r.body)
	m.mu.Lock()
	w.imports[u] = src
	m.mu.Unlock()
	return src, nil
}

// claim is clients.claim(): every document in scope that this registration is the best match for becomes controlled by it.
func (m *swManager) claim(reg *swReg) {
	m.mu.Lock()
	if reg.active == nil {
		m.mu.Unlock()
		return
	}
	type change struct {
		c    *swClient
		prev *swReg
	}
	var changed []change
	for _, c := range m.clients {
		if c.reg == reg || siteOriginOf(c.url) != reg.origin {
			continue
		}
		if m.lookupLocked(c.url) != reg {
			continue
		}
		changed = append(changed, change{c, c.reg})
		c.reg = reg
	}
	info, snap := reg.active.info(), reg.snapshot()
	m.mu.Unlock()
	for _, ch := range changed {
		ch.c.tab.sw.ctl.Store(&swControl{reg: reg, clientID: ch.c.id})
		ch.c.tab.mu.RLock()
		doc := ch.c.tab.js
		ch.c.tab.mu.RUnlock()
		pushSW(doc, map[string]any{"t": "controller", "worker": info, "reg": snap})
		if ch.prev != nil {
			prev := ch.prev
			m.goJob(func() { m.afterClientGone(prev) })
		}
	}
}

// ---- MessageChannel ports across VMs ----------------------------------------------------------------

type swPorts struct {
	mu     sync.Mutex
	next   int64
	peer   map[int64]int64
	owner  map[int64]*jsVM
	queued map[int64][]string
}

func newSWPorts() swPorts {
	return swPorts{peer: map[int64]int64{}, owner: map[int64]*jsVM{}, queued: map[int64][]string{}}
}

// pair creates two entangled endpoints; the first belongs to j, the second to whoever adopts it.
func (p *swPorts) pair(j *jsVM) (int64, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a, b := p.next+1, p.next+2
	p.next += 2
	p.peer[a], p.peer[b] = b, a
	p.owner[a] = j
	return a, b
}

func (p *swPorts) adopt(j *jsVM, ep int64) {
	p.mu.Lock()
	p.owner[ep] = j
	q := p.queued[ep]
	delete(p.queued, ep)
	p.mu.Unlock()
	for _, d := range q {
		pushSW(j, map[string]any{"t": "port", "ep": ep, "data": d})
	}
}

func (p *swPorts) post(from int64, data string) {
	p.mu.Lock()
	to, ok := p.peer[from]
	dst := p.owner[to]
	if ok && dst == nil {
		p.queued[to] = append(p.queued[to], data)
	}
	p.mu.Unlock()
	if dst != nil {
		pushSW(dst, map[string]any{"t": "port", "ep": to, "data": data})
	}
}

func (p *swPorts) dropVM(j *jsVM) {
	p.mu.Lock()
	for ep, o := range p.owner {
		if o == j {
			delete(p.owner, ep)
			delete(p.queued, ep)
		}
	}
	p.mu.Unlock()
}

var _ = time.Second
