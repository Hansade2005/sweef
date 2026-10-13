package sitestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// DefaultQuota is the per-origin storage quota when none is configured.
const DefaultQuota = int64(1) << 30

// Manager owns the storage of one browsing context. With an empty root it
// keeps everything in memory (incognito, ephemeral agent contexts); with a
// root directory it persists every origin under it:
//
//	<root>/<sha256(origin)[:24]>/meta.json        {"origin": ..., "persisted": ...}
//	<root>/<...>/idb/<sha256(name)[:24]>.db       one append-only log per database
//	<root>/<...>/caches/                          Cache Storage (see cache.go)
//	<root>/<...>/sw/                              service worker registrations
type Manager struct {
	root  string
	quota int64

	nextID atomic.Int64

	mu      sync.Mutex
	origins map[string]*Origin

	regMu sync.RWMutex
	conns map[int64]*Conn
	txs   map[int64]*Tx
}

// NewManager returns a manager. quota <= 0 means DefaultQuota.
func NewManager(root string, quota int64) *Manager {
	if quota <= 0 {
		quota = DefaultQuota
	}
	return &Manager{root: root, quota: quota, origins: map[string]*Origin{}, conns: map[int64]*Conn{}, txs: map[int64]*Tx{}}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	if o := m.origins[origin]; o != nil {
		return o
	}
	o := &Origin{m: m, name: origin, dbs: map[string]*Database{}}
	if m.root != "" {
		o.dir = filepath.Join(m.root, originDirName(origin))
		o.scan()
	}
	m.origins[origin] = o
	return o
}

// Origins lists every origin that has data (in memory or on disk).
func (m *Manager) Origins() []string {
	seen := map[string]bool{}
	m.mu.Lock()
	for n := range m.origins {
		seen[n] = true
	}
	m.mu.Unlock()
	if m.root != "" {
		ents, _ := os.ReadDir(m.root)
		for _, e := range ents {
			if b, err := os.ReadFile(filepath.Join(m.root, e.Name(), "meta.json")); err == nil {
				var meta struct {
					Origin string `json:"origin"`
				}
				if json.Unmarshal(b, &meta) == nil && meta.Origin != "" {
					seen[meta.Origin] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
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
		o.wipe()
	}
}

// Close flushes every log. The manager stays usable.
func (m *Manager) Close() {
	m.mu.Lock()
	var os_ []*Origin
	for _, o := range m.origins {
		os_ = append(os_, o)
	}
	m.mu.Unlock()
	for _, o := range os_ {
		o.mu.Lock()
		for _, d := range o.dbs {
			d.mu.Lock()
			d.log.sync()
			d.mu.Unlock()
		}
		o.mu.Unlock()
		o.syncCaches()
	}
}

// Origin is the storage of one origin.
type Origin struct {
	m    *Manager
	name string
	dir  string // "" = memory only

	mu      sync.Mutex
	dbs     map[string]*Database
	files   map[string]int64 // sizes of database files not loaded yet
	loaded  bool
	cache   *CacheStore
	cacheMu sync.Mutex
	extra   atomic.Int64 // bytes of Cache Storage
	other   atomic.Int64 // bytes of service worker scripts
	persist atomic.Bool
}

func (o *Origin) scan() {
	o.files = map[string]int64{}
	ents, err := os.ReadDir(filepath.Join(o.dir, "idb"))
	if err == nil {
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".db") {
				if fi, err := e.Info(); err == nil {
					o.files[filepath.Join(o.dir, "idb", e.Name())] = fi.Size()
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(o.dir, "meta.json")); err == nil {
		var meta struct {
			Persisted bool `json:"persisted"`
		}
		if json.Unmarshal(b, &meta) == nil {
			o.persist.Store(meta.Persisted)
		}
	}
	o.scanCaches()
	o.scanSW()
}

// scanSW sums the service worker files of an origin.
func (o *Origin) scanSW() {
	if o.dir == "" {
		return
	}
	var n int64
	_ = filepath.WalkDir(filepath.Join(o.dir, "sw"), func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	o.other.Store(n)
}

// touch makes sure the origin's directory and meta.json exist.
func (o *Origin) touch() {
	if o.dir == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(o.dir, "meta.json")); err == nil {
		return
	}
	o.writeMeta()
}

func (o *Origin) writeMeta() {
	if o.dir == "" {
		return
	}
	_ = os.MkdirAll(o.dir, 0o700)
	b, _ := json.Marshal(map[string]any{"origin": o.name, "persisted": o.persist.Load()})
	tmp := filepath.Join(o.dir, "meta.json.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, filepath.Join(o.dir, "meta.json"))
	}
}

// SWDir is where the origin's service worker registrations live ("" = memory only).
func (o *Origin) SWDir() string {
	if o.dir == "" {
		return ""
	}
	return filepath.Join(o.dir, "sw")
}

// SetOtherBytes records how much the origin's service worker scripts occupy (counted in usage and quota).
func (o *Origin) SetOtherBytes(n int64) { o.other.Store(n) }

// Touch makes sure the origin is listed on disk (see Origins).
func (o *Origin) Touch() { o.touch() }

// Persisted reports whether the origin asked for (and got) persistent storage.
func (o *Origin) Persisted() bool { return o.persist.Load() }

// SetPersisted records the outcome of navigator.storage.persist().
func (o *Origin) SetPersisted(v bool) {
	if o.persist.Swap(v) != v {
		o.writeMeta()
	}
}

func dbFileName(name string) string {
	h := sha256.Sum256([]byte(name))
	return hex.EncodeToString(h[:12]) + ".db"
}

// DB returns the named database, loading it from disk when needed. A database
// that does not exist yet is created empty in memory (it exists for the
// platform only once an upgrade transaction commits).
func (o *Origin) DB(name string) (*Database, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if d := o.dbs[name]; d != nil {
		return d, nil
	}
	var d *Database
	if o.dir != "" {
		path := filepath.Join(o.dir, "idb", dbFileName(name))
		if _, err := os.Stat(path); err == nil {
			var lerr error
			if d, lerr = loadDatabase(o, name, path); lerr != nil {
				return nil, lerr
			}
			delete(o.files, path)
		} else {
			d = newDatabase(o, name, path)
		}
		o.touch()
	} else {
		d = newDatabase(o, name, "")
	}
	o.dbs[name] = d
	return d, nil
}

// DBInfo is one entry of indexedDB.databases().
type DBInfo struct {
	Name    string
	Version int64
}

// Databases lists the databases that exist.
func (o *Origin) Databases() []DBInfo {
	if o.dir != "" {
		o.mu.Lock()
		paths := make([]string, 0, len(o.files))
		for p := range o.files {
			paths = append(paths, p)
		}
		o.mu.Unlock()
		for _, p := range paths {
			if name, ok := readDBName(p); ok {
				_, _ = o.DB(name)
			}
		}
	}
	o.mu.Lock()
	ds := make([]*Database, 0, len(o.dbs))
	for _, d := range o.dbs {
		ds = append(ds, d)
	}
	o.mu.Unlock()
	var out []DBInfo
	for _, d := range ds {
		d.mu.Lock()
		if d.exists {
			out = append(out, DBInfo{d.name, d.version})
		}
		d.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func readDBName(path string) (string, bool) {
	l, recs, err := openLog(path, false)
	if err != nil {
		return "", false
	}
	l.close()
	if len(recs) == 0 {
		return "", false
	}
	r := rbuf{b: recs[0]}
	if r.s() != dbMagic {
		return "", false
	}
	name := r.s()
	return name, r.err == nil
}

// Usage is what the origin stores, by kind, in bytes.
type Usage struct {
	IndexedDB int64
	Caches    int64
	Other     int64 // service worker scripts
	Total     int64
}

func (o *Origin) idbUsage() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	var n int64
	for _, d := range o.dbs {
		n += d.usage.Load() // a file size, or the live byte count of a memory-only database
	}
	for _, sz := range o.files {
		n += sz
	}
	return n
}

func (o *Origin) usage() int64 { return o.idbUsage() + o.extra.Load() + o.other.Load() }

// Usage reports the origin's current storage use.
func (o *Origin) Usage() Usage {
	u := Usage{IndexedDB: o.idbUsage(), Caches: o.extra.Load(), Other: o.other.Load()}
	u.Total = u.IndexedDB + u.Caches + u.Other
	return u
}

// Quota is the origin's storage limit in bytes.
func (o *Origin) Quota() int64 { return o.m.quota }

// wipe removes every trace of the origin.
func (o *Origin) wipe() {
	o.mu.Lock()
	ds := make([]*Database, 0, len(o.dbs))
	for _, d := range o.dbs {
		ds = append(ds, d)
	}
	o.mu.Unlock()
	for _, d := range ds {
		d.mu.Lock()
		for _, c := range d.openConns() {
			d.notify(c.sink, Msg{"t": "closed", "conn": c.id})
			d.forceClose(c)
		}
		d.reqs = nil
		d.destroy()
		d.unlock()
	}
	o.wipeCaches()
	o.mu.Lock()
	o.files = map[string]int64{}
	o.persist.Store(false)
	o.mu.Unlock()
	if o.dir != "" {
		_ = os.RemoveAll(o.dir)
	}
}
