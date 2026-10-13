package engine

// webcoord.go: the cross-context coordination APIs, BroadcastChannel and Web Locks. Both live in the
// BrowserContext (workerHost) and are namespaced by storage key, so they connect the documents and workers of one
// origin inside one partition and nothing else: a private window's context shares nothing with the normal
// window's, and the same embed under two different top-level sites cannot talk to itself across them.

import (
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

var coordSeq atomic.Int64

// ---- BroadcastChannel -------------------------------------------------------------------------------

type bcMember struct {
	id     int64
	j      *jsVM
	key    string
	name   string
	origin string
}

type bcHub struct {
	mu      sync.Mutex
	members map[int64]*bcMember
	groups  map[string][]*bcMember // key + "\x00" + name
}

func (h *bcHub) init() {
	h.members = map[int64]*bcMember{}
	h.groups = map[string][]*bcMember{}
}

func (h *bcHub) open(j *jsVM, key, name, origin string) int64 {
	m := &bcMember{id: coordSeq.Add(1), j: j, key: key, name: name, origin: origin}
	h.mu.Lock()
	h.members[m.id] = m
	g := key + "\x00" + name
	h.groups[g] = append(h.groups[g], m)
	h.mu.Unlock()
	b := j.bgState()
	b.mu.Lock()
	b.bcs = append(b.bcs, m.id)
	b.mu.Unlock()
	return m.id
}

// post delivers data to every other open channel of the group, in the order posted.
func (h *bcHub) post(id int64, from *jsVM, data string) {
	h.mu.Lock()
	m := h.members[id]
	if m == nil || m.j != from {
		h.mu.Unlock()
		return
	}
	var to []*bcMember
	for _, o := range h.groups[m.key+"\x00"+m.name] {
		if o != m {
			to = append(to, o)
		}
	}
	h.mu.Unlock()
	for _, o := range to {
		pushWk(o.j, map[string]any{"t": "bc", "ch": o.id, "data": data, "origin": m.origin})
	}
}

func (h *bcHub) close(id int64, from *jsVM) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(id, from)
}

func (h *bcHub) removeLocked(id int64, from *jsVM) {
	m := h.members[id]
	if m == nil || (from != nil && m.j != from) {
		return
	}
	delete(h.members, id)
	g := m.key + "\x00" + m.name
	l := h.groups[g]
	for i, o := range l {
		if o == m {
			h.groups[g] = append(l[:i:i], l[i+1:]...)
			break
		}
	}
	if len(h.groups[g]) == 0 {
		delete(h.groups, g)
	}
}

func (h *bcHub) vmGone(ids []int64) {
	h.mu.Lock()
	for _, id := range ids {
		h.removeLocked(id, nil)
	}
	h.mu.Unlock()
}

// ---- Web Locks ----------------------------------------------------------------------------------------

type lockReq struct {
	id       int64
	j        *jsVM
	key      string
	name     string
	mode     string // exclusive | shared
	ifAvail  bool
	granted  bool
	clientID int64
}

type lockHub struct {
	mu      sync.Mutex
	reqs    map[int64]*lockReq
	held    map[string][]*lockReq // by storage key, in grant order
	pending map[string][]*lockReq // by storage key, in request order
}

func (h *lockHub) init() {
	h.reqs = map[int64]*lockReq{}
	h.held = map[string][]*lockReq{}
	h.pending = map[string][]*lockReq{}
}

func lockEvent(r *lockReq, st string) {
	pushWk(r.j, map[string]any{"t": "lock", "req": r.id, "st": st})
}

func (h *lockHub) conflicts(r *lockReq) bool {
	for _, o := range h.held[r.key] {
		if o.name == r.name && (o.mode == "exclusive" || r.mode == "exclusive") {
			return true
		}
	}
	return false
}

// request registers a lock request and returns its id; the outcome arrives as a "lock" event
// (granted | unavailable). steal releases the holders of the name first and takes the lock at once.
func (h *lockHub) request(j *jsVM, key, name, mode string, ifAvailable, steal bool) int64 {
	if mode != "shared" {
		mode = "exclusive"
	}
	var cid int64
	if j.tab != nil {
		cid = j.tab.winID
	}
	r := &lockReq{id: coordSeq.Add(1), j: j, key: key, name: name, mode: mode, ifAvail: ifAvailable, clientID: cid}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reqs[r.id] = r
	j.bgState() // makes sure the VM's teardown releases what it holds
	if steal {
		var kept []*lockReq
		for _, o := range h.held[key] {
			if o.name == name {
				delete(h.reqs, o.id)
				lockEvent(o, "stolen")
				continue
			}
			kept = append(kept, o)
		}
		h.held[key] = kept
		r.granted = true
		h.held[key] = append(h.held[key], r)
		lockEvent(r, "granted")
		h.processLocked(key)
		return r.id
	}
	// a request waits behind earlier requests for the same name
	blocked := h.conflicts(r)
	if !blocked {
		for _, o := range h.pending[key] {
			if o.name == name {
				blocked = true
				break
			}
		}
	}
	if blocked && ifAvailable {
		delete(h.reqs, r.id)
		lockEvent(r, "unavailable")
		return r.id
	}
	if blocked {
		h.pending[key] = append(h.pending[key], r)
		return r.id
	}
	r.granted = true
	h.held[key] = append(h.held[key], r)
	lockEvent(r, "granted")
	return r.id
}

// processLocked grants queued requests that no longer conflict, in order; a request that cannot be granted holds
// back the later ones for the same name.
func (h *lockHub) processLocked(key string) {
	var rest []*lockReq
	blockedNames := map[string]bool{}
	for _, r := range h.pending[key] {
		if blockedNames[r.name] || h.conflicts(r) {
			blockedNames[r.name] = true
			rest = append(rest, r)
			continue
		}
		r.granted = true
		h.held[key] = append(h.held[key], r)
		lockEvent(r, "granted")
	}
	if len(rest) == 0 {
		delete(h.pending, key)
	} else {
		h.pending[key] = rest
	}
}

func (h *lockHub) removeLocked(r *lockReq) {
	delete(h.reqs, r.id)
	l := h.held[r.key]
	for i, o := range l {
		if o == r {
			h.held[r.key] = append(l[:i:i], l[i+1:]...)
			break
		}
	}
	p := h.pending[r.key]
	for i, o := range p {
		if o == r {
			h.pending[r.key] = append(p[:i:i], p[i+1:]...)
			break
		}
	}
	if len(h.held[r.key]) == 0 {
		delete(h.held, r.key)
	}
}

func (h *lockHub) release(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.reqs[id]
	if r == nil {
		return
	}
	h.removeLocked(r)
	h.processLocked(r.key)
}

// abort withdraws a request that has not been granted yet.
func (h *lockHub) abort(id int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.reqs[id]
	if r == nil || r.granted {
		return
	}
	h.removeLocked(r)
	h.processLocked(r.key)
}

// vmGone drops every request and lock of a closed VM.
func (h *lockHub) vmGone(j *jsVM) {
	h.mu.Lock()
	defer h.mu.Unlock()
	keys := map[string]bool{}
	for _, r := range h.reqs {
		if r.j == j {
			h.removeLocked(r)
			keys[r.key] = true
		}
	}
	for k := range keys {
		h.processLocked(k)
	}
}

func (h *lockHub) query(key string) any {
	h.mu.Lock()
	defer h.mu.Unlock()
	conv := func(l []*lockReq) []any {
		out := make([]any, 0, len(l))
		for _, r := range l {
			out = append(out, map[string]any{"name": r.name, "mode": r.mode, "clientId": strconv.FormatInt(r.clientID, 10)})
		}
		return out
	}
	held := append([]*lockReq(nil), h.held[key]...)
	pend := append([]*lockReq(nil), h.pending[key]...)
	sort.SliceStable(held, func(a, b int) bool { return held[a].id < held[b].id })
	return map[string]any{"held": conv(held), "pending": conv(pend)}
}
