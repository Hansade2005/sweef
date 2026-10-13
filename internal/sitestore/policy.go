package sitestore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Quota, eviction and persistence, modelled on Chromium's quota manager.
//
// Budget. The browser may use a global budget for all sites together:
// FreeFraction (60%) of what is available on the profile volume (the free space
// plus what the browser already uses, so the budget does not shrink as it fills
// up), never more than BudgetCap. One origin may use at most OriginFraction
// (20%) of the budget. Both are overridable: Options.Budget and
// Options.OriginQuota replace the derived numbers.
//
// Accounting. Usage is exact bookkeeping, not a directory scan: every write
// batch carries the usage change of the data it contains, so after a crash the
// recorded usage is that of the data that survived. It covers IndexedDB (keys,
// values, index entries, blobs), Cache Storage (metadata and bodies), the OPFS
// tree and the service worker scripts.
//
// Writes. A transaction commit, cache.put and OPFS write that would take the
// origin over its cap fail with QuotaExceededError. One that would take the
// browser over the global budget first evicts other origins.
//
// Eviction. Origins are either persistent (navigator.storage.persist() was
// granted: never evicted) or best-effort. Under global pressure whole
// best-effort origins are deleted, least recently used first; the origin that
// is writing is spared. An evicted origin's open connections get the "closed"
// notification (IDBDatabase.onclose, outstanding requests fail with AbortError),
// its queued open requests fail, and all its data goes in one step.
//
// Persistence is decided by a policy hook set by the application: the engine
// asks it whenever a page calls persist(). Without a hook the answer is no.

// Options configure a Manager. The zero value derives everything from the volume.
type Options struct {
	// OriginQuota is the hard cap of one origin in bytes (0 = OriginFraction of the budget).
	OriginQuota int64
	// Budget is the global budget in bytes (0 = derived from the free space of the volume).
	Budget int64
	// BudgetCap bounds the derived budget (0 = 64 GiB).
	BudgetCap int64
	// FreeFraction is the share of the available space the derived budget may take (0 = 0.6).
	FreeFraction float64
	// OriginFraction is the share of the budget one origin may take (0 = 0.2).
	OriginFraction float64
	// MemoryBudget is the budget of a manager without a root directory (0 = 1 GiB).
	MemoryBudget int64
	// FreeSpace reports the free bytes of the volume that holds dir (nil = the operating system's answer).
	FreeSpace func(dir string) (uint64, error)
	// Now is the clock (nil = time.Now); tests use it to order accesses.
	Now func() time.Time
	// PersistPolicy decides persist() calls (nil = deny).
	PersistPolicy func(origin string) bool
	// OnEvict is called, with no lock held, after an origin was evicted.
	OnEvict func(origin string)
	// MaxOpenStores caps how many origins keep their ordered store open at once (0 = 12).
	MaxOpenStores int
	// IdleClose is how long an unused store stays open (0 = 20 s).
	IdleClose time.Duration
	// MaxTxBytes caps the uncommitted writes of one transaction (0 = 256 MiB).
	MaxTxBytes int64
}

func (o Options) withDefaults() Options {
	if o.BudgetCap <= 0 {
		o.BudgetCap = 64 << 30
	}
	if o.FreeFraction <= 0 {
		o.FreeFraction = 0.6
	}
	if o.OriginFraction <= 0 {
		o.OriginFraction = 0.2
	}
	if o.MemoryBudget <= 0 {
		o.MemoryBudget = 1 << 30
	}
	if o.MaxOpenStores <= 0 {
		o.MaxOpenStores = 12
	}
	if o.MaxTxBytes <= 0 {
		o.MaxTxBytes = 256 << 20
	}
	if o.FreeSpace == nil {
		o.FreeSpace = diskFree
	}
	return o
}

// SetPersistPolicy installs the hook that decides persist() calls: it gets the origin and answers whether
// persistent storage is granted. It may be called from any goroutine and should not block for long.
func (m *Manager) SetPersistPolicy(f func(origin string) bool) {
	m.polMu.Lock()
	m.persistFn = f
	m.polMu.Unlock()
}

// SetEvictHook installs the function called after an origin was evicted (to drop what lives outside the store,
// e.g. service worker registrations).
func (m *Manager) SetEvictHook(f func(origin string)) {
	m.polMu.Lock()
	m.evictFn = f
	m.polMu.Unlock()
}

// RequestPersist implements navigator.storage.persist(): true when the origin is persistent after the call.
// A memory-only manager never grants it (nothing there survives the process).
func (o *Origin) RequestPersist() bool {
	if o.persist.Load() {
		return true
	}
	if o.dir == "" {
		return false
	}
	o.m.polMu.Lock()
	f := o.m.persistFn
	o.m.polMu.Unlock()
	if f != nil && f(o.name) {
		o.SetPersisted(true)
		return true
	}
	return false
}

// Budget is the quota state of the whole manager.
type Budget struct {
	Global    int64 // bytes all origins together may use
	Used      int64 // bytes they use
	OriginCap int64 // bytes one origin may use
	Free      int64 // free bytes on the volume (-1 if unknown)
	Derived   bool  // Global came from the volume's free space
}

func (m *Manager) free() (uint64, error) {
	m.polMu.Lock()
	defer m.polMu.Unlock()
	if !m.freeAt.IsZero() && m.now().Sub(m.freeAt) < time.Second && m.opts.Now == nil {
		return m.freeVal, m.freeErr
	}
	dir := m.root
	for dir != "" {
		if _, err := os.Stat(dir); err == nil {
			break
		}
		p := filepath.Dir(dir)
		if p == dir {
			break
		}
		dir = p
	}
	m.freeVal, m.freeErr = m.opts.FreeSpace(dir)
	m.freeAt = m.now()
	return m.freeVal, m.freeErr
}

// totalUsage is the use of every known origin.
func (m *Manager) totalUsage() int64 {
	m.loadCold()
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, o := range m.origins {
		n += o.usage()
	}
	return n
}

// Budget reports the global budget, the use and the per-origin cap.
func (m *Manager) Budget() Budget {
	used := m.totalUsage()
	b := Budget{Used: used, Free: -1}
	switch {
	case m.opts.Budget > 0:
		b.Global = m.opts.Budget
	case m.root == "":
		b.Global = min64(m.opts.MemoryBudget, m.opts.BudgetCap)
	default:
		free, err := m.free()
		if err != nil {
			b.Global = m.opts.BudgetCap
		} else {
			b.Free = int64(free)
			b.Global = min64(int64(m.opts.FreeFraction*float64(int64(free)+used)), m.opts.BudgetCap)
			b.Derived = true
		}
	}
	if b.Global < used && !b.Derived {
		// an explicit budget below the current use is honoured: eviction will bring the use down
	}
	b.OriginCap = m.opts.OriginQuota
	if b.OriginCap <= 0 {
		b.OriginCap = int64(m.opts.OriginFraction * float64(b.Global))
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// Quota is what navigator.storage.estimate() reports as the quota: the origin's cap, or its use plus what is
// left of the global budget when that is less.
func (o *Origin) Quota() int64 {
	b := o.m.Budget()
	u := o.usage()
	room := b.Global - b.Used
	if room < 0 {
		room = 0
	}
	q := u + room
	if b.OriginCap < q {
		q = b.OriginCap
	}
	return q
}

// reserve is called before bytes are added to an origin: it enforces the origin's cap and makes room in the
// global budget by evicting other best-effort origins, least recently used first. It must be called without
// any database or cache lock held.
func (o *Origin) reserve(delta int64) error {
	if delta <= 0 {
		return nil
	}
	m := o.m
	b := m.Budget()
	if o.usage()+delta > b.OriginCap {
		return errf("QuotaExceededError", "The storage quota for this origin was exceeded.")
	}
	if b.Used+delta <= b.Global {
		return nil
	}
	m.evictMu.Lock()
	defer m.evictMu.Unlock()
	for {
		b = m.Budget()
		if b.Used+delta <= b.Global {
			return nil
		}
		v := m.pickVictim(o)
		if v == nil {
			return errf("QuotaExceededError", "The storage quota of the browser was exceeded.")
		}
		v.wipe("evicted")
		m.evicted.Add(1)
		m.polMu.Lock()
		f := m.evictFn
		m.polMu.Unlock()
		if f != nil {
			go f(v.name)
		}
	}
}

// pickVictim is the least recently used best-effort origin that has data, other than skip.
func (m *Manager) pickVictim(skip *Origin) *Origin {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Origin
	for _, o := range m.origins {
		if o == skip || o.persist.Load() || o.usage() == 0 {
			continue
		}
		if best == nil || o.lastAccess.Load() < best.lastAccess.Load() {
			best = o
		}
	}
	return best
}

// EvictionOrder lists the best-effort origins in the order global pressure would delete them.
func (m *Manager) EvictionOrder() []string {
	m.loadCold()
	m.mu.Lock()
	var os_ []*Origin
	for _, o := range m.origins {
		if !o.persist.Load() && o.usage() > 0 {
			os_ = append(os_, o)
		}
	}
	m.mu.Unlock()
	sort.Slice(os_, func(i, j int) bool {
		a, b := os_[i].lastAccess.Load(), os_[j].lastAccess.Load()
		if a != b {
			return a < b
		}
		return os_[i].name < os_[j].name
	})
	out := make([]string, len(os_))
	for i, o := range os_ {
		out[i] = o.name
	}
	return out
}

// Evicted is how many origins were evicted under pressure since the manager was created.
func (m *Manager) Evicted() int64 { return m.evicted.Load() }

// SiteInfo is one origin as the settings page shows it.
type SiteInfo struct {
	Origin     string    `json:"origin"`
	Usage      Usage     `json:"usage"`
	Persisted  bool      `json:"persisted"`
	LastAccess time.Time `json:"lastAccess"`
}

// Sites lists the origins that store data, largest first.
func (m *Manager) Sites() []SiteInfo {
	m.loadCold()
	m.mu.Lock()
	var out []SiteInfo
	for _, o := range m.origins {
		u := o.Usage()
		if u.Total == 0 && !o.persist.Load() {
			continue
		}
		out = append(out, SiteInfo{Origin: o.name, Usage: u, Persisted: o.persist.Load(), LastAccess: o.LastAccess()})
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Usage.Total != out[j].Usage.Total {
			return out[i].Usage.Total > out[j].Usage.Total
		}
		return out[i].Origin < out[j].Origin
	})
	return out
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
