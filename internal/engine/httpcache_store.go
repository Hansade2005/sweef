package engine

// httpcache_store.go: where cached responses live.
//
//   - A memory tier (LRU, Options.MaxCacheMiB) serves every context.
//   - A disk tier (Options.DiskCacheMiB, default 100, under <CacheDir>/http) is the profile-level HTTP cache:
//     it is used by the default context only, so private windows and isolated contexts stay in memory and
//     leave nothing behind. Loopback hosts are never written to disk (a developer's localhost assets must not
//     outlive the server). Files are written to a temporary name and renamed into place, so a reader (this
//     process or another one on the same profile) sees a whole entry or none. When the directory grows past
//     the cap the least recently used files are deleted.
//
// Entries are immutable once stored; a revalidation stores a new entry under the same key.

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type cacheEntry struct {
	Key    string      `json:"k"`
	URL    string      `json:"u"`
	Status int         `json:"s"`
	Header http.Header `json:"h"`
	Vary   []string    `json:"v,omitempty"` // lower-case request header names the response varies on
	VaryV  string      `json:"vv,omitempty"`
	Resp   time.Time   `json:"t"`  // when the response arrived
	Xfer   int64       `json:"x"`  // transfer bytes (headers + raw body) the response cost
	Kind   string      `json:"kd"` // DataKind of the request that filled it
	BodyN  int         `json:"n"`
	// TooLarge > 0: a tombstone. The response was refused unread because its transfer size (this value) was over
	// the request's cap; the entry keeps its headers so the next visit skips the request while it is fresh.
	TooLarge int64 `json:"tl,omitempty"`

	Body []byte `json:"-"`

	// navOnly: no lifetime and no validator. Only the navigation that fetched it may reuse it (the loader
	// finding what the parallel prefetch read); it is never served by the cache proper nor written to disk.
	navOnly bool
	tab     string // tab and navigation epoch that fetched or last used the entry
	epoch   int64
}

func (e *cacheEntry) size() int { return len(e.Body) + 1024 }

func (e *cacheEntry) clone() *cacheEntry { c := *e; return &c }

type httpCache struct {
	mu    sync.Mutex
	mem   map[string]*list.Element
	lru   *list.List // front = most recently used; values are *cacheEntry
	bytes int
	max   int
	disk  *diskCache
}

func newHTTPCache(memMiB int, dir string, diskMiB int) *httpCache {
	if memMiB <= 0 {
		memMiB = 32
	}
	c := &httpCache{mem: map[string]*list.Element{}, lru: list.New(), max: memMiB << 20}
	if dir != "" && diskMiB >= 0 {
		if diskMiB == 0 {
			diskMiB = 100
		}
		c.disk = newDiskCache(filepath.Join(dir, "http"), int64(diskMiB)<<20)
	}
	return c
}

// persistLoopback lets tests exercise the disk tier with httptest servers.
var persistLoopback = false

// loopbackHost: localhost, *.localhost, 127.0.0.0/8, ::1 and unspecified addresses.
func loopbackHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsUnspecified()
	}
	return false
}

// key partitions the cache by browsing context. The default context uses the plain URL (and may persist); every
// other context has its own namespace in memory.
func (c *httpCache) key(ctx *BrowserContext, rawurl, rng string) (key string, persist bool) {
	return c.keyTop(ctx, "", rawurl, rng)
}

// keyTop is key for a request made under a top-level site: the same URL is a different entry for each top-level
// site that asks for it, so what one site loaded says nothing to another about what the person has visited
// (the cache-probing side channel), and a third party cannot recognise a person through a cached copy. top is the
// schemeful site ("" = none, as for a page without a site).
func (c *httpCache) keyTop(ctx *BrowserContext, top, rawurl, rng string) (key string, persist bool) {
	norm := normalizeURL(rawurl)
	if rng != "" {
		norm += "\x00range=" + rng
	}
	if top != "" {
		norm = top + "\x00" + norm
	}
	if ctx == nil || ctx.name == "default" {
		persist = c.disk != nil
		if persist {
			if u, err := url.Parse(rawurl); err != nil || (loopbackHost(u.Hostname()) && !persistLoopback) || (u.Scheme != "http" && u.Scheme != "https") {
				persist = false
			}
		}
		return norm, persist
	}
	return ctx.ID + "|" + norm, false
}

// get returns the stored entry (a private copy) or nil.
func (c *httpCache) get(key string, persist bool) *cacheEntry {
	c.mu.Lock()
	if el, ok := c.mem[key]; ok {
		c.lru.MoveToFront(el)
		e := el.Value.(*cacheEntry).clone()
		c.mu.Unlock()
		return e
	}
	c.mu.Unlock()
	if !persist || c.disk == nil {
		return nil
	}
	e := c.disk.read(key)
	if e == nil {
		return nil
	}
	c.putMem(e)
	return e.clone()
}

// peek is get without touching recency or the disk (memory entries only).
func (c *httpCache) peek(key string) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.mem[key]; ok {
		return el.Value.(*cacheEntry).clone()
	}
	return nil
}

// peekXfer is the transfer size a stored response cost, or -1 (memory tier only).
func (c *httpCache) peekXfer(key string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.mem[key]; ok {
		if x := el.Value.(*cacheEntry).Xfer; x > 0 {
			return x
		}
	}
	return -1
}

// navEntry returns the entry if the given navigation of the given tab stored or used it.
func (c *httpCache) navEntry(key, tab string, epoch int64) *cacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.mem[key]; ok {
		e := el.Value.(*cacheEntry)
		if e.tab == tab && e.epoch == epoch && e.Status == http.StatusOK {
			c.lru.MoveToFront(el)
			return e.clone()
		}
	}
	return nil
}

// mark records that a navigation used the entry (so a second use in it is not a new saving).
func (c *httpCache) mark(key, tab string, epoch int64) {
	c.mu.Lock()
	if el, ok := c.mem[key]; ok {
		e := el.Value.(*cacheEntry)
		e.tab, e.epoch = tab, epoch
	}
	c.mu.Unlock()
}

func (c *httpCache) putMem(e *cacheEntry) {
	if e.size() > c.max/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.mem[e.Key]; ok {
		c.bytes -= el.Value.(*cacheEntry).size()
		c.lru.Remove(el)
	}
	c.mem[e.Key] = c.lru.PushFront(e)
	c.bytes += e.size()
	for c.bytes > c.max {
		last := c.lru.Back()
		if last == nil {
			break
		}
		v := last.Value.(*cacheEntry)
		c.lru.Remove(last)
		delete(c.mem, v.Key)
		c.bytes -= v.size()
	}
}

// put stores an entry in memory and, for persistent keys, on disk.
func (c *httpCache) put(e *cacheEntry, persist bool) {
	e.BodyN = len(e.Body)
	c.putMem(e)
	if persist && c.disk != nil && !e.navOnly {
		c.disk.write(e)
	}
}

// drop removes an entry from every tier.
func (c *httpCache) drop(key string, persist bool) {
	c.mu.Lock()
	if el, ok := c.mem[key]; ok {
		c.bytes -= el.Value.(*cacheEntry).size()
		c.lru.Remove(el)
		delete(c.mem, key)
	}
	c.mu.Unlock()
	if persist && c.disk != nil {
		c.disk.remove(key)
	}
}

// purgeContext forgets the memory entries of one non-default context.
func (c *httpCache) purgeContext(ctxID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix := ctxID + "|"
	for k, el := range c.mem {
		if strings.HasPrefix(k, prefix) {
			c.bytes -= el.Value.(*cacheEntry).size()
			c.lru.Remove(el)
			delete(c.mem, k)
		}
	}
}

// dropMemory empties the memory tier; the disk tier is flushed first and kept.
func (c *httpCache) dropMemory() {
	c.flush()
	c.mu.Lock()
	c.mem = map[string]*list.Element{}
	c.lru.Init()
	c.bytes = 0
	c.mu.Unlock()
}

// clear forgets everything, the disk tier included.
func (c *httpCache) clear() {
	c.mu.Lock()
	c.mem = map[string]*list.Element{}
	c.lru.Init()
	c.bytes = 0
	c.mu.Unlock()
	if c.disk != nil {
		c.disk.clear()
	}
}

// flush waits until the disk writer has written everything queued so far.
func (c *httpCache) flush() {
	if c.disk != nil {
		c.disk.flush()
	}
}

func (c *httpCache) close() {
	if c.disk != nil {
		c.disk.close()
	}
}

// ---- disk tier ----------------------------------------------------------------

const diskMagic = "SWHC\x01"

type diskCache struct {
	dir string
	max int64

	mu     sync.Mutex
	size   int64
	inited bool
	q      chan func()
	quit   chan struct{}
	closed bool
	wg     sync.WaitGroup
}

func newDiskCache(dir string, max int64) *diskCache { return &diskCache{dir: dir, max: max} }

func (d *diskCache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:])
	return filepath.Join(d.dir, h[:2], h[2:])
}

// maxEntry is the largest body written to disk.
func (d *diskCache) maxEntry() int {
	n := int(d.max / 8)
	if n < 1<<20 {
		n = 1 << 20
	}
	return n
}

// start creates the directory and the writer goroutine on first use, so engine start-up touches no disk.
func (d *diskCache) start() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return false
	}
	if d.inited {
		return d.q != nil
	}
	d.inited = true
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return false
	}
	d.q = make(chan func(), 256)
	d.quit = make(chan struct{})
	q, quit := d.q, d.quit
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.scan()
		for {
			select {
			case f := <-q:
				f()
			case <-quit:
				return
			}
		}
	}()
	return true
}

// scan measures the directory and removes temporary files left by a crash.
func (d *diskCache) scan() {
	var total int64
	_ = filepath.WalkDir(d.dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return nil
		}
		info, err := de.Info()
		if err != nil {
			return nil
		}
		if strings.HasPrefix(de.Name(), ".tmp-") {
			if time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(p)
			}
			return nil
		}
		total += info.Size()
		return nil
	})
	d.mu.Lock()
	d.size += total
	d.mu.Unlock()
	d.evictIfNeeded()
}

func (d *diskCache) enqueue(f func()) {
	if !d.start() {
		return
	}
	d.mu.Lock()
	q, closed := d.q, d.closed
	d.mu.Unlock()
	if closed {
		return
	}
	select {
	case q <- f:
	default: // writer is far behind: skip this write, the cache is only an optimisation
	}
}

func (d *diskCache) write(e *cacheEntry) {
	if len(e.Body) > d.maxEntry() {
		return
	}
	snap := e.clone()
	d.enqueue(func() { d.writeNow(snap) })
}

func (d *diskCache) writeNow(e *cacheEntry) {
	meta, err := json.Marshal(e)
	if err != nil {
		return
	}
	p := d.path(e.Key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-")
	if err != nil {
		return
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(meta)))
	_, e1 := f.WriteString(diskMagic)
	_, e2 := f.Write(lenBuf[:])
	_, e3 := f.Write(meta)
	_, e4 := f.Write(e.Body)
	e5 := f.Close()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
		_ = os.Remove(f.Name())
		return
	}
	var old int64
	if st, err := os.Stat(p); err == nil {
		old = st.Size()
	}
	if err := os.Rename(f.Name(), p); err != nil {
		_ = os.Remove(f.Name())
		return
	}
	d.mu.Lock()
	d.size += int64(len(diskMagic)+4+len(meta)+len(e.Body)) - old
	over := d.size > d.max
	d.mu.Unlock()
	if over {
		d.evictIfNeeded()
	}
}

func (d *diskCache) read(key string) *cacheEntry {
	p := d.path(key)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	e := parseDiskEntry(b)
	if e == nil || e.Key != key {
		if e == nil {
			_ = os.Remove(p) // corrupt
		}
		return nil
	}
	if st, err := os.Stat(p); err == nil && time.Since(st.ModTime()) > time.Hour {
		now := time.Now()
		_ = os.Chtimes(p, now, now) // recency for eviction
	}
	return e
}

func parseDiskEntry(b []byte) *cacheEntry {
	if len(b) < len(diskMagic)+4 || string(b[:len(diskMagic)]) != diskMagic {
		return nil
	}
	b = b[len(diskMagic):]
	n := int(binary.BigEndian.Uint32(b))
	b = b[4:]
	if n <= 0 || n > len(b) {
		return nil
	}
	var e cacheEntry
	if json.Unmarshal(b[:n], &e) != nil {
		return nil
	}
	body := b[n:]
	if len(body) != e.BodyN || e.Header == nil {
		return nil
	}
	e.Body = body
	return &e
}

func (d *diskCache) remove(key string) {
	p := d.path(key)
	if st, err := os.Stat(p); err == nil {
		if os.Remove(p) == nil {
			d.mu.Lock()
			d.size -= st.Size()
			d.mu.Unlock()
		}
	}
}

// evictIfNeeded deletes the least recently used files until the directory is at 90% of the cap.
func (d *diskCache) evictIfNeeded() {
	d.mu.Lock()
	over := d.size > d.max
	d.mu.Unlock()
	if !over {
		return
	}
	type fe struct {
		p    string
		size int64
		mod  time.Time
	}
	var files []fe
	var total int64
	_ = filepath.WalkDir(d.dir, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() || strings.HasPrefix(de.Name(), ".tmp-") {
			return nil
		}
		if info, err := de.Info(); err == nil {
			files = append(files, fe{p, info.Size(), info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	target := d.max / 10 * 9
	for _, f := range files {
		if total <= target {
			break
		}
		if os.Remove(f.p) == nil {
			total -= f.size
		}
	}
	d.mu.Lock()
	d.size = total
	d.mu.Unlock()
}

func (d *diskCache) clear() {
	d.flush()
	_ = os.RemoveAll(d.dir)
	d.mu.Lock()
	d.size = 0
	d.mu.Unlock()
	_ = os.MkdirAll(d.dir, 0o700)
}

// flush returns once everything queued before the call has been written (or after 5 s).
func (d *diskCache) flush() {
	d.mu.Lock()
	q, closed, inited := d.q, d.closed, d.inited
	d.mu.Unlock()
	if !inited || q == nil || closed {
		return
	}
	done := make(chan struct{})
	select {
	case q <- func() { close(done) }:
	case <-time.After(time.Second):
		return
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

func (d *diskCache) close() {
	d.flush()
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	quit := d.quit
	d.mu.Unlock()
	if quit != nil {
		close(quit)
		d.wg.Wait()
	}
}

// diskUsage is the size the cache believes the directory has (for tests and diagnostics).
func (d *diskCache) diskUsage() int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.size
}
