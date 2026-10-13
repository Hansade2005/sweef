package sitestore

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// Cache Storage of one origin, in the origin's ordered store (origin.go lists the key layout).
//
//	pCacheReg  name                      -> cache id, creation sequence
//	pCacheOrd  seq                       -> name                      (caches in creation order)
//	pCacheEnt  cache | entry seq         -> entry metadata            (request, status, headers, sizes)
//	pCacheBody cache | entry seq         -> the body, or a reference to its blob file
//	pCacheURL  cache | url-sans-query 0 url 0 entry seq -> (nothing)   (lookup by URL, with or without the query)
//
// Entry metadata and bodies are separate keys so that listing a cache (keys())
// never reads a body. A body above BlobThreshold is a file and is streamed
// from it (MatchStream); only the reference is in the store. Replacing a
// request keeps the entry's position, as the specification's "in place"
// requires: the new entry takes the old entry's sequence.

const pCacheBody = 0x24

// CacheEntry is one stored request/response pair.
type CacheEntry struct {
	ID         int64  // the entry's sequence in its cache (stable when the request is replaced)
	URL        string // request URL without fragment
	Method     string
	ReqHeaders [][2]string
	Status     int
	StatusText string
	ResHeaders [][2]string
	Type       string // response type: basic, cors, default
	ResURL     string // response.url
	Size       int64  // body bytes
}

// CacheQuery is the matching part of a request plus the CacheQueryOptions.
type CacheQuery struct {
	URL          string
	Method       string
	IgnoreSearch bool
	IgnoreMethod bool
}

// CacheStore is the Cache Storage of one origin. It holds no data: every call goes to the ordered store.
type CacheStore struct {
	o  *Origin
	mu sync.Mutex // read-modify-write sequences (put replaces, delete)
}

// Caches returns the origin's Cache Storage.
func (o *Origin) Caches() (*CacheStore, error) {
	o.cacheMu.Lock()
	defer o.cacheMu.Unlock()
	if o.cache == nil {
		o.cache = &CacheStore{o: o}
	}
	return o.cache, nil
}

func u32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func u64(b []byte) uint64 { return binary.BigEndian.Uint64(b) }

func cacheRegKey(name string) []byte { return append([]byte{pCacheReg}, name...) }

func cacheKey(kind byte, cid uint32, seq uint64) []byte {
	k := make([]byte, 13)
	k[0] = kind
	binary.BigEndian.PutUint32(k[1:], cid)
	binary.BigEndian.PutUint64(k[5:], seq)
	return k
}

func cachePrefix(kind byte, cid uint32) []byte {
	k := make([]byte, 5)
	k[0] = kind
	binary.BigEndian.PutUint32(k[1:], cid)
	return k
}

func ordKey(seq uint64) []byte {
	k := make([]byte, 9)
	k[0] = pCacheOrd
	binary.BigEndian.PutUint64(k[1:], seq)
	return k
}

func cleanURL(u string) string { return strings.ReplaceAll(u, "\x00", "") }

// urlKeyPrefix is the lookup prefix for a URL: with fullURL "" every query variant of the path matches.
func urlKeyPrefix(cid uint32, rawURL string, exact bool) []byte {
	u := cleanURL(rawURL)
	k := append(cachePrefix(pCacheURL, cid), stripQuery(u)...)
	k = append(k, 0)
	if exact {
		k = append(k, u...)
		k = append(k, 0)
	}
	return k
}

func urlKey(cid uint32, rawURL string, seq uint64) []byte {
	k := urlKeyPrefix(cid, rawURL, true)
	return binary.BigEndian.AppendUint64(k, seq)
}

type cacheReg struct {
	id  uint32
	seq uint64
}

func (c *CacheStore) lookup(k *kvStore, name string) (cacheReg, bool) {
	v, ok := k.get(cacheRegKey(name))
	if !ok || len(v) < 12 {
		return cacheReg{}, false
	}
	return cacheReg{u32(v), u64(v[4:])}, true
}

// Open creates the cache if it does not exist. It reports whether it was created.
func (c *CacheStore) Open(name string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k, err := c.o.acquire()
	if err != nil {
		return false, err
	}
	defer c.o.release(k)
	if _, ok := c.lookup(k, name); ok {
		c.o.access()
		return false, nil
	}
	c.o.touch()
	id, seq := uint32(c.o.allocID()), c.o.allocID()
	var reg [12]byte
	binary.BigEndian.PutUint32(reg[:], id)
	binary.BigEndian.PutUint64(reg[4:], seq)
	var b leveldb.Batch
	b.Put(cacheRegKey(name), reg[:])
	b.Put(ordKey(seq), []byte(name))
	if err := c.o.commitBatch(k, &b, 0, 0, 0, false); err != nil {
		return false, errf("UnknownError", "%v", err)
	}
	c.o.access()
	return true, nil
}

func (c *CacheStore) Has(name string) bool {
	k, err := c.o.acquire()
	if err != nil {
		return false
	}
	defer c.o.release(k)
	_, ok := c.lookup(k, name)
	return ok
}

// Keys lists the cache names in creation order.
func (c *CacheStore) Keys() []string {
	if !c.o.hasStore() {
		return nil
	}
	k, err := c.o.acquire()
	if err != nil {
		return nil
	}
	defer c.o.release(k)
	var out []string
	it := k.db.NewIterator(prefixRange([]byte{pCacheOrd}), nil)
	for it.Next() {
		out = append(out, string(it.Value()))
	}
	it.Release()
	return out
}

// Delete removes a cache and its bodies. The entries are dropped by the background collector; the cache is
// gone for every reader the moment the batch commits.
func (c *CacheStore) Delete(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.o.hasStore() {
		return false
	}
	k, err := c.o.acquire()
	if err != nil {
		return false
	}
	defer c.o.release(k)
	reg, ok := c.lookup(k, name)
	if !ok {
		return false
	}
	var b leveldb.Batch
	b.Delete(cacheRegKey(name))
	b.Delete(ordKey(reg.seq))
	b.Put(garbageKey(gcCaches, reg.id, 0), nil)
	// its bytes: the sum of the entries' costs, kept in the cache's counter record
	freed := c.cacheBytes(k, reg.id)
	b.Delete(cacheBytesKey(reg.id))
	if err := c.o.commitBatch(k, &b, 0, -freed, 0, false); err != nil {
		return false
	}
	c.o.kickGC()
	c.o.access()
	return true
}

// cacheBytesKey is the cache's byte count: the last key of the cache's entry range (see origin.go).
func cacheBytesKey(cid uint32) []byte { return cacheKey(pCacheEnt, cid, math.MaxUint64) }

func (c *CacheStore) cacheBytes(k *kvStore, cid uint32) int64 {
	if v, ok := k.get(cacheBytesKey(cid)); ok {
		n, _ := binary.Varint(v)
		return n
	}
	return 0
}

func entryCost(e *CacheEntry) int64 {
	n := e.Size + int64(len(e.URL)+len(e.StatusText)+64)
	for _, h := range e.ReqHeaders {
		n += int64(len(h[0]) + len(h[1]))
	}
	for _, h := range e.ResHeaders {
		n += int64(len(h[0]) + len(h[1]))
	}
	return n
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

func (q CacheQuery) methodOK() bool {
	return q.IgnoreMethod || q.Method == "" || q.Method == "GET" || q.Method == "HEAD"
}

// entries lists the entries of a cache that match q, in insertion order. A query without a URL matches
// every entry.
func (c *CacheStore) entries(k *kvStore, cid uint32, q CacheQuery, limit int) []*CacheEntry {
	var out []*CacheEntry
	if q.URL == "" {
		it := k.db.NewIterator(&util.Range{Start: cachePrefix(pCacheEnt, cid), Limit: cacheBytesKey(cid)}, nil)
		for it.Next() && (limit <= 0 || len(out) < limit) {
			r := rbuf{b: it.Value()}
			if e := decEntry(&r); r.err == nil {
				out = append(out, e)
			}
		}
		it.Release()
		return out
	}
	if !q.methodOK() {
		return nil
	}
	url := stripFragment(q.URL)
	pre := urlKeyPrefix(cid, url, !q.IgnoreSearch)
	var seqs []uint64
	it := k.db.NewIterator(prefixRange(pre), nil)
	for it.Next() {
		key := it.Key()
		seqs = append(seqs, u64(key[len(key)-8:]))
	}
	it.Release()
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for _, s := range seqs {
		if limit > 0 && len(out) >= limit {
			break
		}
		v, ok := k.get(cacheKey(pCacheEnt, cid, s))
		if !ok {
			continue
		}
		r := rbuf{b: v}
		if e := decEntry(&r); r.err == nil {
			out = append(out, e)
		}
	}
	return out
}

// Put stores an entry (replacing the first entry for the same request in place and dropping any other
// duplicates). body is the response body.
func (c *CacheStore) Put(cacheName string, e CacheEntry, body []byte) error {
	e.URL = cleanURL(stripFragment(e.URL))
	e.Size = int64(len(body))
	cost := entryCost(&e)
	// room first, before any lock of ours is held: making room can evict other origins
	if err := c.o.reserve(cost); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k, err := c.o.acquire()
	if err != nil {
		return errf("UnknownError", "%v", err)
	}
	defer c.o.release(k)
	reg, ok := c.lookup(k, cacheName)
	if !ok {
		return errf("NotFoundError", "The cache was deleted.")
	}
	stored, blob, err := c.o.packValue(body)
	if err != nil {
		return errf("UnknownError", "%v", err)
	}
	var b leveldb.Batch
	seq := c.o.allocID()
	var freed int64
	var freedBlobs []uint64
	for _, old := range c.entries(k, reg.id, CacheQuery{URL: e.URL, Method: e.Method, IgnoreMethod: true}, 0) {
		if old.Method != e.Method {
			continue
		}
		freed += entryCost(old)
		b.Delete(cacheKey(pCacheEnt, reg.id, uint64(old.ID)))
		b.Delete(urlKey(reg.id, old.URL, uint64(old.ID)))
		if bv, ok := k.get(cacheKey(pCacheBody, reg.id, uint64(old.ID))); ok {
			if id, _, vok := valueInfo(bv); vok && id != 0 && isBlobValue(bv) {
				b.Delete(blobKey(id))
				freedBlobs = append(freedBlobs, id)
			}
		}
		b.Delete(cacheKey(pCacheBody, reg.id, uint64(old.ID)))
		if uint64(old.ID) < seq {
			seq = uint64(old.ID) // keep the position of the earliest replaced entry
		}
	}
	e.ID = int64(seq)
	var w wbuf
	encEntry(&w, &e)
	b.Put(cacheKey(pCacheEnt, reg.id, seq), w.b)
	b.Put(cacheKey(pCacheBody, reg.id, seq), stored)
	b.Put(urlKey(reg.id, e.URL, seq), nil)
	if blob != 0 {
		var sz [8]byte
		binary.BigEndian.PutUint64(sz[:], uint64(len(body)))
		b.Put(blobKey(blob), sz[:])
	}
	// the cache's own byte counter (to give the bytes back when the whole cache is deleted)
	nb := c.cacheBytes(k, reg.id) + cost - freed
	b.Put(cacheBytesKey(reg.id), binary.AppendVarint(nil, nb))
	if err := c.o.commitBatch(k, &b, 0, cost-freed, 0, false); err != nil {
		if blob != 0 {
			c.o.dropBlobFiles([]uint64{blob})
		}
		return errf("UnknownError", "%v", err)
	}
	c.o.dropBlobFiles(freedBlobs)
	c.o.access()
	return nil
}

// CacheMatch is a stored entry together with its body.
type CacheMatch struct {
	Entry CacheEntry
	Body  []byte
}

// CacheStream is a stored entry whose body is read as a stream.
type CacheStream struct {
	Entry CacheEntry
	Body  io.ReadCloser
}

func (c *CacheStore) openBody(k *kvStore, cid uint32, e *CacheEntry) io.ReadCloser {
	v, ok := k.get(cacheKey(pCacheBody, cid, uint64(e.ID)))
	if !ok || len(v) < 2 || v[0] != valV1 {
		return io.NopCloser(bytes.NewReader(nil))
	}
	if v[1]&flagBlob == 0 {
		return io.NopCloser(bytes.NewReader(append([]byte(nil), v[2:]...)))
	}
	id, _, _ := valueInfo(v)
	rc, _, err := c.o.openBlob(id)
	if err != nil {
		return io.NopCloser(bytes.NewReader(nil))
	}
	return rc
}

// MatchStream finds entries for a request in one cache, or, with everyCache, in every cache in creation order,
// and hands back each body as a stream (a large body is read from its file, never loaded whole). The caller
// closes every Body. all=false stops at the first match.
func (c *CacheStore) MatchStream(cacheName string, everyCache bool, q CacheQuery, all bool) []CacheStream {
	if !c.o.hasStore() {
		return nil
	}
	k, err := c.o.acquire()
	if err != nil {
		return nil
	}
	defer c.o.release(k)
	var names []string
	if everyCache {
		it := k.db.NewIterator(prefixRange([]byte{pCacheOrd}), nil)
		for it.Next() {
			names = append(names, string(it.Value()))
		}
		it.Release()
	} else {
		names = []string{cacheName}
	}
	var out []CacheStream
	for _, name := range names {
		reg, ok := c.lookup(k, name)
		if !ok {
			continue
		}
		limit := 1
		if all {
			limit = 0
		}
		for _, e := range c.entries(k, reg.id, q, limit) {
			out = append(out, CacheStream{Entry: *e, Body: c.openBody(k, reg.id, e)})
			if !all {
				c.o.access()
				return out
			}
		}
	}
	c.o.access()
	return out
}

// Match is MatchStream with the bodies read whole.
func (c *CacheStore) Match(cacheName string, everyCache bool, q CacheQuery, all bool) []CacheMatch {
	var out []CacheMatch
	for _, s := range c.MatchStream(cacheName, everyCache, q, all) {
		b, _ := io.ReadAll(s.Body)
		s.Body.Close()
		out = append(out, CacheMatch{Entry: s.Entry, Body: b})
	}
	return out
}

// Requests lists the stored requests of one cache (those matching q when q.URL != "").
func (c *CacheStore) Requests(cacheName string, q CacheQuery) ([]CacheEntry, error) {
	if !c.o.hasStore() {
		return nil, errf("NotFoundError", "The cache was deleted.")
	}
	k, err := c.o.acquire()
	if err != nil {
		return nil, errf("UnknownError", "%v", err)
	}
	defer c.o.release(k)
	reg, ok := c.lookup(k, cacheName)
	if !ok {
		return nil, errf("NotFoundError", "The cache was deleted.")
	}
	var out []CacheEntry
	for _, e := range c.entries(k, reg.id, q, 0) {
		out = append(out, *e)
	}
	c.o.access()
	return out, nil
}

// DeleteEntries removes the entries matching q from a cache and reports how many.
func (c *CacheStore) DeleteEntries(cacheName string, q CacheQuery) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.o.hasStore() {
		return 0, errf("NotFoundError", "The cache was deleted.")
	}
	k, err := c.o.acquire()
	if err != nil {
		return 0, errf("UnknownError", "%v", err)
	}
	defer c.o.release(k)
	reg, ok := c.lookup(k, cacheName)
	if !ok {
		return 0, errf("NotFoundError", "The cache was deleted.")
	}
	var b leveldb.Batch
	var freed int64
	var blobs []uint64
	es := c.entries(k, reg.id, q, 0)
	for _, old := range es {
		freed += entryCost(old)
		b.Delete(cacheKey(pCacheEnt, reg.id, uint64(old.ID)))
		b.Delete(urlKey(reg.id, old.URL, uint64(old.ID)))
		if bv, ok := k.get(cacheKey(pCacheBody, reg.id, uint64(old.ID))); ok {
			if id, _, vok := valueInfo(bv); vok && id != 0 && isBlobValue(bv) {
				b.Delete(blobKey(id))
				blobs = append(blobs, id)
			}
		}
		b.Delete(cacheKey(pCacheBody, reg.id, uint64(old.ID)))
	}
	if len(es) == 0 {
		return 0, nil
	}
	b.Put(cacheBytesKey(reg.id), binary.AppendVarint(nil, c.cacheBytes(k, reg.id)-freed))
	if err := c.o.commitBatch(k, &b, 0, -freed, 0, false); err != nil {
		return 0, errf("UnknownError", "%v", err)
	}
	c.o.dropBlobFiles(blobs)
	c.o.access()
	return len(es), nil
}

func (o *Origin) wipeCaches() {
	o.cacheMu.Lock()
	o.cache = nil
	o.cacheMu.Unlock()
}
