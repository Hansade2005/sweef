package engine

// rescache.go — the two caches that make repeat navigations cheap:
//
//   - resCache: an in-memory HTTP cache for sub-resources (scripts, modules)
//     keyed by URL. Fresh entries (Cache-Control max-age / immutable /
//     Expires) are served without a request; stale entries with an ETag or
//     Last-Modified are revalidated with a conditional request and reused
//     on 304. Bounded by Options.MaxCacheMiB, oldest evicted first.
//   - codeCache: compiled QuickJS bytecode keyed by the SHA-256 of the
//     script source, so a framework bundle is parsed once per process, the
//     way V8's code cache works. Execution still happens on every page.

import (
	"crypto/sha256"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type resEntry struct {
	body     string
	status   int
	etag     string
	lastMod  string
	expires  time.Time
	storedAt time.Time
}

type resCache struct {
	mu      sync.Mutex
	entries map[string]*resEntry
	order   []string
	bytes   int
	max     int
}

func newResCache(maxMiB int) *resCache {
	if maxMiB <= 0 {
		maxMiB = 8
	}
	return &resCache{entries: map[string]*resEntry{}, max: maxMiB << 20}
}

func (c *resCache) get(url string) *resEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.entries[url]
}

func (c *resCache) put(url string, e *resEntry) {
	if len(e.body) > c.max/4 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[url]; ok {
		c.bytes -= len(old.body)
	} else {
		c.order = append(c.order, url)
	}
	c.entries[url] = e
	c.bytes += len(e.body)
	for c.bytes > c.max && len(c.order) > 0 {
		victim := c.order[0]
		c.order = c.order[1:]
		if v, ok := c.entries[victim]; ok {
			c.bytes -= len(v.body)
			delete(c.entries, victim)
		}
	}
}

// freshness derives an expiry from response headers; zero means "revalidate".
func freshness(h http.Header, now time.Time) time.Time {
	cc := strings.ToLower(h.Get("Cache-Control"))
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") {
		return time.Time{}
	}
	if strings.Contains(cc, "immutable") {
		return now.Add(24 * time.Hour)
	}
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "max-age=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(part, "max-age=")); err == nil && n > 0 {
				return now.Add(time.Duration(n) * time.Second)
			}
		}
	}
	if exp := h.Get("Expires"); exp != "" {
		if t, err := http.ParseTime(exp); err == nil && t.After(now) {
			return t
		}
	}
	return time.Time{}
}

func cacheable(h http.Header) bool {
	cc := strings.ToLower(h.Get("Cache-Control"))
	return !strings.Contains(cc, "no-store")
}

type codeCache struct {
	mu    sync.Mutex
	m     map[[32]byte][]byte
	bytes int
	max   int
}

func newCodeCache() *codeCache { return &codeCache{m: map[[32]byte][]byte{}, max: 32 << 20} }

func (c *codeCache) get(key [32]byte) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[key]
}

func (c *codeCache) put(key [32]byte, bc []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bytes+len(bc) > c.max {
		c.m = map[[32]byte][]byte{}
		c.bytes = 0
	}
	c.m[key] = bc
	c.bytes += len(bc)
}

func codeKey(src string) [32]byte { return sha256.Sum256([]byte(src)) }
