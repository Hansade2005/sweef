package engine

// pwa_locks.go: Web Locks. One lock manager per BrowserContext, keyed by origin, so a lock is shared by every tab and
// every service worker of that origin (dedicated Web Workers run in a smaller VM that has no navigator.locks).
// The queueing rules are those of the specification: requests for a name are served in order; an exclusive
// request needs the name to be free, a shared one only needs no exclusive holder; `ifAvailable` answers null when a
// request cannot be granted at once; `steal` takes the name from its holders, who are told with an AbortError.

import (
	"sync"
)

type lockReq struct {
	token       int64
	vm          *jsVM
	origin      string
	name, mode  string
	ifAvailable bool
	client      string
}

type lockHeld struct {
	id     int64
	vm     *jsVM
	origin string
	name   string
	mode   string
	client string
	token  int64
}

type lockMgr struct {
	mu     sync.Mutex
	nextID int64
	held   map[string][]*lockHeld // origin|name
	queue  map[string][]*lockReq
	closed bool
}

func newLockMgr() *lockMgr {
	return &lockMgr{held: map[string][]*lockHeld{}, queue: map[string][]*lockReq{}}
}

func lockKey(origin, name string) string { return origin + "\x00" + name }

func (m *lockMgr) grantableLocked(r *lockReq) bool {
	for _, h := range m.held[lockKey(r.origin, r.name)] {
		if r.mode == "exclusive" || h.mode == "exclusive" {
			return false
		}
	}
	return true
}

// processLocked grants queued requests of a name as far as the rules allow.
func (m *lockMgr) processLocked(origin, name string) {
	k := lockKey(origin, name)
	for len(m.queue[k]) > 0 {
		r := m.queue[k][0]
		if !m.grantableLocked(r) {
			return
		}
		m.queue[k] = m.queue[k][1:]
		m.nextID++
		h := &lockHeld{id: m.nextID, vm: r.vm, origin: origin, name: name, mode: r.mode, client: r.client, token: r.token}
		m.held[k] = append(m.held[k], h)
		pushPWA(r.vm, map[string]any{"t": "lock", "token": r.token, "lockId": h.id})
	}
	if len(m.queue[k]) == 0 {
		delete(m.queue, k)
	}
}

func (m *lockMgr) request(r *lockReq, steal bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		pushPWA(r.vm, map[string]any{"t": "lock", "token": r.token, "lockId": 0})
		return
	}
	k := lockKey(r.origin, r.name)
	if steal {
		for _, h := range m.held[k] {
			pushPWA(h.vm, map[string]any{"t": "lockstolen", "lockId": h.id})
		}
		delete(m.held, k)
		m.queue[k] = append([]*lockReq{r}, m.queue[k]...)
		m.processLocked(r.origin, r.name)
		return
	}
	if r.ifAvailable && (len(m.queue[k]) > 0 || !m.grantableLocked(r)) {
		pushPWA(r.vm, map[string]any{"t": "lock", "token": r.token, "lockId": 0})
		return
	}
	m.queue[k] = append(m.queue[k], r)
	m.processLocked(r.origin, r.name)
}

func (m *lockMgr) release(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, hs := range m.held {
		for i, h := range hs {
			if h.id == id {
				m.held[k] = append(hs[:i:i], hs[i+1:]...)
				if len(m.held[k]) == 0 {
					delete(m.held, k)
				}
				m.processLocked(h.origin, h.name)
				return
			}
		}
	}
}

// abort removes a request that has not been granted yet.
func (m *lockMgr) abort(vm *jsVM, token int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, q := range m.queue {
		for i, r := range q {
			if r.vm == vm && r.token == token {
				m.queue[k] = append(q[:i:i], q[i+1:]...)
				m.processLocked(r.origin, r.name)
				return
			}
		}
	}
}

type lockInfo struct {
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	ClientID string `json:"clientId"`
}

func (m *lockMgr) query(origin string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, pending := []any{}, []any{}
	for _, hs := range m.held {
		for _, h := range hs {
			if h.origin == origin {
				held = append(held, map[string]any{"name": h.name, "mode": h.mode, "clientId": h.client})
			}
		}
	}
	for _, q := range m.queue {
		for _, r := range q {
			if r.origin == origin {
				pending = append(pending, map[string]any{"name": r.name, "mode": r.mode, "clientId": r.client})
			}
		}
	}
	return map[string]any{"held": held, "pending": pending}
}

// dropVM releases everything a closed document or worker held or waited for.
func (m *lockMgr) dropVM(j *jsVM) {
	m.mu.Lock()
	defer m.mu.Unlock()
	type nm struct{ origin, name string }
	var touched []nm
	for k, hs := range m.held {
		out := hs[:0:0]
		for _, h := range hs {
			if h.vm == j {
				touched = append(touched, nm{h.origin, h.name})
			} else {
				out = append(out, h)
			}
		}
		if len(out) == 0 {
			delete(m.held, k)
		} else {
			m.held[k] = out
		}
	}
	for k, q := range m.queue {
		out := q[:0:0]
		for _, r := range q {
			if r.vm == j {
				touched = append(touched, nm{r.origin, r.name})
			} else {
				out = append(out, r)
			}
		}
		if len(out) == 0 {
			delete(m.queue, k)
		} else {
			m.queue[k] = out
		}
	}
	for _, t := range touched {
		m.processLocked(t.origin, t.name)
	}
}

func (m *lockMgr) closeAll() {
	m.mu.Lock()
	m.closed = true
	m.held = map[string][]*lockHeld{}
	m.queue = map[string][]*lockReq{}
	m.mu.Unlock()
}

// clientID names the VM for LockManager.query(): the document's client id, or the worker's id.
// lockKey is the namespace of the VM's locks: its storage key, so the same origin embedded under two sites has two
// lock namespaces (partition.go).
func (h *pwaHost) lockKey() string {
	if t := h.j.tab; t != nil {
		if o := t.docOrigin(); o != "" {
			return t.storageKeyFor(o)
		}
		return "opaque:" + itoa(int(t.winID))
	}
	return h.origin()
}

func (h *pwaHost) clientID() string {
	if rt := h.worker(); rt != nil {
		return "sw" + itoa(int(rt.w.id))
	}
	if t := h.j.tab; t != nil {
		if c := t.sw.clientEntry(); c != nil {
			return c.id
		}
		return "tab-" + t.ID
	}
	return ""
}

func (h *pwaHost) locksFns(f map[string]hostFn) {
	f["locksRequest"] = func(a []any) (any, error) {
		if !h.secure() {
			return nil, &swError{"SecurityError", "Access to the Locks API is denied in this context."}
		}
		r := &lockReq{token: argI(a, 0), vm: h.j, origin: h.lockKey(), name: argS(a, 1), mode: argS(a, 2), ifAvailable: argB(a, 3), client: h.clientID()}
		h.st().locks.request(r, argB(a, 4))
		return nil, nil
	}
	f["locksRelease"] = func(a []any) (any, error) {
		h.st().locks.release(argI(a, 0))
		return nil, nil
	}
	f["locksAbort"] = func(a []any) (any, error) {
		h.st().locks.abort(h.j, argI(a, 0))
		return nil, nil
	}
	f["locksQuery"] = func(a []any) (any, error) {
		if !h.secure() {
			return nil, &swError{"SecurityError", "Access to the Locks API is denied in this context."}
		}
		return h.st().locks.query(h.lockKey()), nil
	}
}
