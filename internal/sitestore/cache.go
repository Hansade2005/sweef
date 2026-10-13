package sitestore

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Cache Storage of one origin.
//
// On disk (persistent managers):
//
//	<origin>/caches/index.log     append-only log of cache creations/deletions and entry puts/deletes
//	<origin>/caches/bodies/<id>   one file per stored response body
//
// A body file is written (to a temporary name, then renamed) before the index
// record that points at it, so a crash leaves at worst an unreferenced file,
// which the next open deletes. Bodies are read from disk on a match; only the
// entry metadata lives in memory.

// CacheEntry is one stored request/response pair.
type CacheEntry struct {
	ID         int64
	URL        string // request URL without fragment
	Method     string
	ReqHeaders [][2]string
	Status     int
	StatusText string
	ResHeaders [][2]string
	Type       string // response type: basic, cors, default
	ResURL     string // response.url
	Size       int64  // body bytes
	body       []byte // memory-only stores keep the body here
}

// CacheQuery is the matching part of a request plus the CacheQueryOptions.
type CacheQuery struct {
	URL          string
	Method       string
	IgnoreSearch bool
	IgnoreMethod bool
}

type cacheData struct {
	name    string
	entries []*CacheEntry
}

// CacheStore is the Cache Storage of one origin.
type CacheStore struct {
	o      *Origin
	mu     sync.Mutex
	dir    string // "" = memory only
	log    *recLog
	caches map[string]*cacheData
	order  []string
	nextID int64
	dead   int // log records that no longer describe live state
	live   int
}

const (
	cOpCreate = 1
	cOpDelete = 2
	cOpPut    = 3
	cOpDel    = 4
)

// Caches returns the origin's Cache Storage, loading it from disk on first use.
func (o *Origin) Caches() (*CacheStore, error) {
	o.cacheMu.Lock()
	defer o.cacheMu.Unlock()
	if o.cache != nil {
		return o.cache, nil
	}
	c := &CacheStore{o: o, caches: map[string]*cacheData{}, nextID: 1}
	if o.dir != "" {
		c.dir = filepath.Join(o.dir, "caches")
		if err := c.load(); err != nil {
			return nil, err
		}
		o.touch()
	}
	o.cache = c
	return c, nil
}

func (c *CacheStore) bodyPath(id int64) string {
	return filepath.Join(c.dir, "bodies", strconv.FormatInt(id, 10))
}

func (c *CacheStore) load() error {
	l, recs, err := openLog(filepath.Join(c.dir, "index.log"), false)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	c.log = l // nil until the first write creates the file
	referenced := map[int64]bool{}
	for _, rec := range recs {
		r := rbuf{b: rec}
		for r.more() {
			if c.replay(&r) != nil {
				break
			}
		}
	}
	for _, cd := range c.caches {
		for _, e := range cd.entries {
			referenced[e.ID] = true
		}
	}
	// unreferenced bodies are leftovers of a crash between the file and the index record
	ents, _ := os.ReadDir(filepath.Join(c.dir, "bodies"))
	for _, e := range ents {
		id, err := strconv.ParseInt(e.Name(), 10, 64)
		if err != nil || !referenced[id] {
			_ = os.Remove(filepath.Join(c.dir, "bodies", e.Name()))
		}
	}
	c.live = len(c.caches)
	var cost int64
	for _, cd := range c.caches {
		c.live += len(cd.entries)
		for _, e := range cd.entries {
			cost += c.entryCost(e)
		}
	}
	c.o.extra.Store(cost) // same accounting as Put and Delete from here on
	c.dead = len(recs) - c.live
	if c.dead < 0 {
		c.dead = 0
	}
	return nil
}

func encEntry(w *wbuf, e *CacheEntry) {
	w.i(e.ID)
	w.s(e.URL)
	w.s(e.Method)
	w.i(int64(e.Status))
	w.s(e.StatusText)
	w.s(e.Type)
	w.s(e.ResURL)
	w.i(e.Size)
	for _, hs := range [][][2]string{e.ReqHeaders, e.ResHeaders} {
		w.u(uint64(len(hs)))
		for _, h := range hs {
			w.s(h[0])
			w.s(h[1])
		}
	}
}

func decEntry(r *rbuf) *CacheEntry {
	e := &CacheEntry{ID: r.i(), URL: r.s(), Method: r.s(), Status: int(r.i()), StatusText: r.s(), Type: r.s(), ResURL: r.s(), Size: r.i()}
	for k := 0; k < 2; k++ {
		n := int(r.u())
		var hs [][2]string
		for i := 0; i < n && r.err == nil; i++ {
			hs = append(hs, [2]string{r.s(), r.s()})
		}
		if k == 0 {
			e.ReqHeaders = hs
		} else {
			e.ResHeaders = hs
		}
	}
	return e
}

func (c *CacheStore) replay(r *rbuf) error {
	switch r.byte() {
	case cOpCreate:
		name := r.s()
		if r.err == nil && c.caches[name] == nil {
			c.caches[name] = &cacheData{name: name}
			c.order = append(c.order, name)
		}
	case cOpDelete:
		c.dropCache(r.s())
	case cOpPut:
		name := r.s()
		e := decEntry(r)
		if r.err != nil {
			return r.err
		}
		if cd := c.caches[name]; cd != nil {
			cd.entries = append(cd.entries, e)
		}
		if e.ID >= c.nextID {
			c.nextID = e.ID + 1
		}
	case cOpDel:
		name, id := r.s(), r.i()
		if cd := c.caches[name]; cd != nil {
			for i, e := range cd.entries {
				if e.ID == id {
					cd.entries = append(cd.entries[:i], cd.entries[i+1:]...)
					break
				}
			}
		}
	default:
		return errCorrupt
	}
	return r.err
}

func (c *CacheStore) dropCache(name string) {
	delete(c.caches, name)
	for i, n := range c.order {
		if n == name {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *CacheStore) appendOp(f func(w *wbuf)) error {
	if c.dir == "" {
		return nil
	}
	if c.log == nil {
		l, _, err := openLog(filepath.Join(c.dir, "index.log"), true)
		if err != nil {
			return err
		}
		c.log = l
	}
	var w wbuf
	f(&w)
	return c.log.append(w.b)
}

// compact rewrites the index as a snapshot when dead records outnumber live ones.
func (c *CacheStore) maybeCompact() {
	if c.log == nil || c.dead < 256 || c.dead < c.live {
		return
	}
	var recs [][]byte
	for _, name := range c.order {
		var w wbuf
		w.byte(cOpCreate)
		w.s(name)
		recs = append(recs, w.b)
		for _, e := range c.caches[name].entries {
			var pw wbuf
			pw.byte(cOpPut)
			pw.s(name)
			encEntry(&pw, e)
			recs = append(recs, pw.b)
		}
	}
	if c.log.rewrite(recs) == nil {
		c.dead, c.live = 0, len(recs)
	}
}

// Open creates the cache if it does not exist. It reports whether it was created.
func (c *CacheStore) Open(name string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.caches[name] != nil {
		return false, nil
	}
	if err := c.appendOp(func(w *wbuf) { w.byte(cOpCreate); w.s(name) }); err != nil {
		return false, err
	}
	c.caches[name] = &cacheData{name: name}
	c.order = append(c.order, name)
	c.live++
	return true, nil
}

func (c *CacheStore) Has(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.caches[name] != nil
}

// Keys lists the cache names in creation order.
func (c *CacheStore) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.order...)
}

// Delete removes a cache and its bodies.
func (c *CacheStore) Delete(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	cd := c.caches[name]
	if cd == nil {
		return false
	}
	_ = c.appendOp(func(w *wbuf) { w.byte(cOpDelete); w.s(name) })
	var freed int64
	for _, e := range cd.entries {
		freed += c.entryCost(e)
		c.removeBody(e)
	}
	c.o.extra.Add(-freed)
	c.dropCache(name)
	c.dead += 1 + len(cd.entries)
	c.live -= 1 + len(cd.entries)
	c.maybeCompact()
	return true
}

func (c *CacheStore) entryCost(e *CacheEntry) int64 {
	n := e.Size + int64(len(e.URL)+len(e.StatusText)+64)
	for _, h := range e.ReqHeaders {
		n += int64(len(h[0]) + len(h[1]))
	}
	for _, h := range e.ResHeaders {
		n += int64(len(h[0]) + len(h[1]))
	}
	return n
}

func (c *CacheStore) removeBody(e *CacheEntry) {
	if c.dir != "" {
		_ = os.Remove(c.bodyPath(e.ID))
	}
	e.body = nil
}

func stripFragment(u string) string {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}

func stripQuery(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

func (q CacheQuery) matches(e *CacheEntry) bool {
	if q.URL == "" { // no request: every entry
		return true
	}
	if !q.IgnoreMethod && q.Method != "" && q.Method != "GET" && q.Method != "HEAD" {
		return false
	}
	a, b := stripFragment(q.URL), e.URL
	if q.IgnoreSearch {
		a, b = stripQuery(a), stripQuery(b)
	}
	return a == b
}

// Put stores an entry (replacing the first entry for the same request in place and dropping any other
// duplicates). body is the response body.
func (c *CacheStore) Put(cacheName string, e CacheEntry, body []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cd := c.caches[cacheName]
	if cd == nil {
		return errf("NotFoundError", "The cache was deleted.")
	}
	e.ID = c.nextID
	e.URL = stripFragment(e.URL)
	e.Size = int64(len(body))
	cost := c.entryCost(&e)
	if q := c.o.m.quota; q > 0 && c.o.usage()+cost > q {
		return errf("QuotaExceededError", "The storage quota for this origin was exceeded.")
	}
	if c.dir != "" {
		dir := filepath.Join(c.dir, "bodies")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		tmp := c.bodyPath(e.ID) + ".tmp"
		if err := os.WriteFile(tmp, body, 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, c.bodyPath(e.ID)); err != nil {
			os.Remove(tmp)
			return err
		}
	} else {
		e.body = body
	}
	ne := &e
	if err := c.appendOp(func(w *wbuf) { w.byte(cOpPut); w.s(cacheName); encEntry(w, ne) }); err != nil {
		c.removeBody(ne)
		return errf("UnknownError", "%v", err)
	}
	c.nextID++
	q := CacheQuery{URL: e.URL, Method: e.Method}
	var kept []*CacheEntry
	replaced := false
	var freed int64
	for _, old := range cd.entries {
		if q.matches(old) && old.Method == e.Method {
			_ = c.appendOp(func(w *wbuf) { w.byte(cOpDel); w.s(cacheName); w.i(old.ID) })
			c.removeBody(old)
			freed += c.entryCost(old)
			c.dead += 2
			c.live--
			if !replaced {
				kept = append(kept, ne)
				replaced = true
			}
			continue
		}
		kept = append(kept, old)
	}
	if !replaced {
		kept = append(kept, ne)
	}
	cd.entries = kept
	c.live++
	c.o.extra.Add(cost - freed)
	c.maybeCompact()
	return nil
}

// CacheMatch is a stored entry together with its body.
type CacheMatch struct {
	Entry CacheEntry
	Body  []byte
}

func (c *CacheStore) readBody(e *CacheEntry) []byte {
	if c.dir == "" {
		return e.body
	}
	b, _ := os.ReadFile(c.bodyPath(e.ID))
	return b
}

// Match finds entries for a request in one cache, or, with everyCache, in every cache in creation order.
// all=false stops at the first match.
func (c *CacheStore) Match(cacheName string, everyCache bool, q CacheQuery, all bool) []CacheMatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []CacheMatch
	for _, name := range c.searchOrder(cacheName, everyCache) {
		for _, e := range c.caches[name].entries {
			if q.matches(e) {
				out = append(out, CacheMatch{Entry: *e, Body: c.readBody(e)})
				if !all {
					return out
				}
			}
		}
	}
	return out
}

func (c *CacheStore) searchOrder(cacheName string, everyCache bool) []string {
	if everyCache {
		return append([]string(nil), c.order...)
	}
	if c.caches[cacheName] == nil {
		return nil
	}
	return []string{cacheName}
}

// Requests lists the stored requests of one cache (those matching q when q.URL != "").
func (c *CacheStore) Requests(cacheName string, q CacheQuery) ([]CacheEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cd := c.caches[cacheName]
	if cd == nil {
		return nil, errf("NotFoundError", "The cache was deleted.")
	}
	var out []CacheEntry
	for _, e := range cd.entries {
		if q.URL == "" || q.matches(e) {
			out = append(out, *e)
		}
	}
	return out, nil
}

// DeleteEntries removes the entries matching q from a cache and reports how many.
func (c *CacheStore) DeleteEntries(cacheName string, q CacheQuery) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cd := c.caches[cacheName]
	if cd == nil {
		return 0, errf("NotFoundError", "The cache was deleted.")
	}
	var kept []*CacheEntry
	n := 0
	var freed int64
	for _, e := range cd.entries {
		if q.matches(e) {
			_ = c.appendOp(func(w *wbuf) { w.byte(cOpDel); w.s(cacheName); w.i(e.ID) })
			c.removeBody(e)
			freed += c.entryCost(e)
			c.dead += 2
			c.live--
			n++
			continue
		}
		kept = append(kept, e)
	}
	cd.entries = kept
	c.o.extra.Add(-freed)
	c.maybeCompact()
	return n, nil
}

func (c *CacheStore) sync() {
	c.mu.Lock()
	c.log.sync()
	c.mu.Unlock()
}

func (o *Origin) syncCaches() {
	o.cacheMu.Lock()
	c := o.cache
	o.cacheMu.Unlock()
	if c != nil {
		c.sync()
	}
}

func (o *Origin) wipeCaches() {
	o.cacheMu.Lock()
	c := o.cache
	o.cache = nil
	o.cacheMu.Unlock()
	if c != nil {
		c.mu.Lock()
		c.log.close()
		c.log = nil
		c.mu.Unlock()
	}
	o.extra.Store(0)
	if o.dir != "" {
		_ = os.RemoveAll(filepath.Join(o.dir, "caches"))
	}
}

// scanCaches sums what an origin's Cache Storage occupies on disk, so usage is right before it is opened.
func (o *Origin) scanCaches() {
	if o.dir == "" {
		return
	}
	var n int64
	if fi, err := os.Stat(filepath.Join(o.dir, "caches", "index.log")); err == nil {
		n += fi.Size()
	}
	ents, _ := os.ReadDir(filepath.Join(o.dir, "caches", "bodies"))
	for _, e := range ents {
		if fi, err := e.Info(); err == nil {
			n += fi.Size()
		}
	}
	o.extra.Store(n)
}

var _ = fmt.Sprint
