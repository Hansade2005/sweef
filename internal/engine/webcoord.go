package engine

// webcoord.go: the cross-context coordination API BroadcastChannel (Web Locks are pwa_locks.go). It lives in the
// BrowserContext (workerHost) and are namespaced by storage key, so they connect the documents and workers of one
// origin inside one partition and nothing else: a private window's context shares nothing with the normal
// window's, and the same embed under two different top-level sites cannot talk to itself across them.

import (
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
