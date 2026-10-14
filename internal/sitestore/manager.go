package sitestore

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultQuota is the per-origin hard cap used by NewManager when none is given and no budget can be derived.
const DefaultQuota = int64(1) << 30

// Manager owns the storage of one browsing context. With an empty root it
// keeps everything in memory (incognito, ephemeral agent contexts); with a
// root directory it persists every origin under it:
//
//	<root>/<sha256(origin)[:24]>/meta.json        {"origin": ..., "persisted": ..., "lastAccess": ...}
//	<root>/<...>/kv/                              the origin's ordered store (IndexedDB, Cache Storage, OPFS tree)
//	<root>/<...>/blobs/                           large values and bodies, one file each
//	<root>/<...>/sw/                              service worker registrations
//
// Quota, eviction and persistence are described in policy.go.
type Manager struct {
	root string
	opts Options

	nextID atomic.Int64

	mu      sync.Mutex
	origins map[string]*Origin
	cold    bool // the directories of origins that are not loaded yet have been listed

	openMu    sync.Mutex
	openOrder []*Origin // origins whose store is open, oldest first

	regMu sync.RWMutex
	conns map[int64]*Conn
	txs   map[int64]*Tx

	polMu     sync.Mutex
	freeAt    time.Time
	freeVal   uint64
	freeErr   error
	evictMu   sync.Mutex
	persistFn func(origin string) bool
	evictFn   func(origin string)
	evicted   atomic.Int64
}

// NewManager returns a manager. quota > 0 is a hard cap per origin in bytes; quota <= 0 derives the cap from
// the global budget (see Options).
func NewManager(root string, quota int64) *Manager {
	return NewManagerOptions(root, Options{OriginQuota: quota})
}

// NewManagerOptions returns a manager with explicit policy options.
func NewManagerOptions(root string, opts Options) *Manager {
	m := &Manager{root: root, opts: opts.withDefaults(), origins: map[string]*Origin{}, conns: map[int64]*Conn{}, txs: map[int64]*Tx{}}
	m.persistFn = opts.PersistPolicy
	m.evictFn = opts.OnEvict
	return m
}

func (m *Manager) now() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

// Root is the directory everything lives under ("" = memory only).
func (m *Manager) Root() string { return m.root }

// Persistent reports whether data survives the process.
func (m *Manager) Persistent() bool { return m.root != "" }

func (m *Manager) regConn(c *Conn) { m.regMu.Lock(); m.conns[c.id] = c; m.regMu.Unlock() }
func (m *Manager) unregConn(id int64) {
	m.regMu.Lock()
	delete(m.conns, id)
	m.regMu.Unlock()
}
func (m *Manager) regTx(t *Tx) { m.regMu.Lock(); m.txs[t.id] = t; m.regMu.Unlock() }
func (m *Manager) unregTx(id int64) {
	m.regMu.Lock()
	delete(m.txs, id)
	m.regMu.Unlock()
}
func (m *Manager) conn(id int64) *Conn {
	m.regMu.RLock()
	defer m.regMu.RUnlock()
	return m.conns[id]
}
func (m *Manager) tx(id int64) *Tx {
	m.regMu.RLock()
	defer m.regMu.RUnlock()
	return m.txs[id]
}

func originDirName(origin string) string {
	h := sha256.Sum256([]byte(origin))
	return hex.EncodeToString(h[:12])
}

// Origin returns the storage of one origin (creating its in-memory state; nothing
// touches the disk until something is written).
func (m *Manager) Origin(origin string) *Origin {
	m.loadCold()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.originLocked(origin)
}

func (m *Manager) originLocked(origin string) *Origin {
	if o := m.origins[origin]; o != nil {
		return o
	}
	dir := ""
	if m.root != "" {
		dir = filepath.Join(m.root, originDirName(origin))
	}
	o := newOrigin(m, origin, dir)
	if dir != "" {
		o.readMeta()
		o.scanSW()
	}
	m.origins[origin] = o
	return o
}

// loadCold makes an Origin of every directory under the root that has not been loaded, so the totals and the
// eviction order cover origins that were not touched since the browser started. It reads one small file each.
func (m *Manager) loadCold() {
	if m.root == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cold {
		return
	}
	m.cold = true
	ents, _ := os.ReadDir(m.root)
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		var meta originMeta
		b, err := os.ReadFile(filepath.Join(m.root, e.Name(), "meta.json"))
		if err != nil || jsonUnmarshal(b, &meta) != nil || meta.Origin == "" {
			continue
		}
		m.originLocked(meta.Origin)
	}
}

// Origins lists every origin that has data (in memory or on disk).
func (m *Manager) Origins() []string {
	m.loadCold()
	m.mu.Lock()
	out := make([]string, 0, len(m.origins))
	for n := range m.origins {
		out = append(out, n)
	}
	m.mu.Unlock()
	sort.Strings(out)
	return out
}

// DropSink closes every connection that belongs to sink (its script VM is gone),
// aborting their transactions and withdrawing their queued requests.
func (m *Manager) DropSink(sink Sink) {
	m.mu.Lock()
	var dbs []*Database
	for _, o := range m.origins {
		o.mu.Lock()
		for _, d := range o.dbs {
			dbs = append(dbs, d)
		}
		o.mu.Unlock()
	}
	m.mu.Unlock()
	for _, d := range dbs {
		d.mu.Lock()
		kept := d.reqs[:0]
		for i, r := range d.reqs {
			if r.sink == sink && !(i == 0 && r.state == 4) {
				continue
			}
			kept = append(kept, r)
		}
		d.reqs = kept
		for _, c := range d.openConns() {
			if c.sink == sink {
				d.forceClose(c)
			}
		}
		d.pump()
		d.unlock()
	}
}

// ClearOrigin deletes everything stored for origin ("" = every origin): the
// open connections are closed by force, the files removed. The caller reloads
// or discards the script contexts that used it.
func (m *Manager) ClearOrigin(origin string) {
	var targets []*Origin
	if origin == "" {
		for _, n := range m.Origins() {
			targets = append(targets, m.Origin(n))
		}
	} else {
		targets = append(targets, m.Origin(origin))
	}
	for _, o := range targets {
		o.wipe("cleared")
	}
}

// Close flushes and closes every store that nothing is using and writes meta.json files. The manager stays
// usable: a store is reopened by the next access.
func (m *Manager) Close() {
	m.mu.Lock()
	var all []*Origin
	for _, o := range m.origins {
		all = append(all, o)
	}
	m.mu.Unlock()
	for _, o := range all {
		o.kvMu.Lock()
		if o.kvs != nil {
			if o.kvRefs == 0 && o.dir != "" {
				o.closeKVLocked()
			} else {
				o.kvs.sync()
			}
		}
		o.kvMu.Unlock()
		if o.dir != "" {
			if _, err := os.Stat(o.dir); err == nil {
				o.writeMeta()
			}
		}
	}
}

// Shutdown closes every store, in-memory ones included (their data is gone), and writes meta.json files. The
// manager must not be used afterwards: it is for the end of a browsing context's life.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	var all []*Origin
	for _, o := range m.origins {
		all = append(all, o)
	}
	m.mu.Unlock()
	for _, o := range all {
		o.kvMu.Lock()
		o.closeKVLocked()
		o.kvMu.Unlock()
		if o.dir != "" {
			if _, err := os.Stat(o.dir); err == nil {
				o.writeMeta()
			}
		}
	}
}

// noteOpen and noteClose track which origins have an open store (to cap how many there are).
func (m *Manager) noteOpen(o *Origin) {
	m.openMu.Lock()
	m.openOrder = append(m.openOrder, o)
	m.openMu.Unlock()
}

func (m *Manager) noteClose(o *Origin) {
	m.openMu.Lock()
	for i, x := range m.openOrder {
		if x == o {
			m.openOrder = append(m.openOrder[:i], m.openOrder[i+1:]...)
			break
		}
	}
	m.openMu.Unlock()
}

// trimOpen closes idle stores, oldest first, while more than the allowed number are open.
func (m *Manager) trimOpen() {
	if m.root == "" {
		return
	}
	max := m.opts.MaxOpenStores
	m.openMu.Lock()
	if len(m.openOrder) <= max {
		m.openMu.Unlock()
		return
	}
	cands := append([]*Origin(nil), m.openOrder...)
	m.openMu.Unlock()
	over := len(cands) - max
	for _, c := range cands {
		if over <= 0 {
			break
		}
		c.kvMu.Lock()
		idle := c.kvRefs == 0 && c.kvs != nil
		c.kvMu.Unlock()
		if idle {
			c.closeIdle()
			over--
		}
	}
}

func (m *Manager) noteUsage(o *Origin) {}

func dbFileName(name string) string {
	h := sha256.Sum256([]byte(name))
	return hex.EncodeToString(h[:12])
}
