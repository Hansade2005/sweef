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
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"modernc.org/quickjs"
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

// codeCache keeps compiled bytecode in memory and, when a cache directory is
// available, on disk, so a freshly started daemon does not re-parse the
// framework bundles it has seen before (like V8's on-disk code cache).
// Keys include the QuickJS version, so an engine upgrade never loads
// bytecode in an old format.
type codeCache struct {
	mu    sync.Mutex
	m     map[[32]byte][]byte
	bytes int
	max   int
	dir   string
}

func newCodeCache(dir string) *codeCache {
	c := &codeCache{m: map[[32]byte][]byte{}, max: 32 << 20}
	if dir != "" {
		d := filepath.Join(dir, "code-"+sanitizeVersion(quickjs.Version()))
		if os.MkdirAll(d, 0o700) == nil {
			c.dir = d
		}
	}
	return c
}

func sanitizeVersion(v string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, v)
}

func (c *codeCache) path(key [32]byte) string {
	return filepath.Join(c.dir, hex.EncodeToString(key[:])+".qjsbc")
}

func (c *codeCache) get(key [32]byte) []byte {
	c.mu.Lock()
	bc := c.m[key]
	dir := c.dir
	c.mu.Unlock()
	if bc != nil || dir == "" {
		return bc
	}
	b, err := os.ReadFile(c.path(key))
	if err != nil || len(b) == 0 {
		return nil
	}
	c.mu.Lock()
	c.m[key] = b
	c.bytes += len(b)
	c.mu.Unlock()
	return b
}

func (c *codeCache) put(key [32]byte, bc []byte) {
	c.mu.Lock()
	if c.bytes+len(bc) > c.max {
		c.m = map[[32]byte][]byte{}
		c.bytes = 0
	}
	c.m[key] = bc
	c.bytes += len(bc)
	dir := c.dir
	c.mu.Unlock()
	if dir != "" {
		go func() {
			tmp := c.path(key) + ".tmp"
			if os.WriteFile(tmp, bc, 0o600) == nil {
				_ = os.Rename(tmp, c.path(key))
			}
		}()
	}
}

// forget drops an entry that failed to load (corrupt or stale file).
func (c *codeCache) forget(key [32]byte) {
	c.mu.Lock()
	delete(c.m, key)
	dir := c.dir
	c.mu.Unlock()
	if dir != "" {
		_ = os.Remove(c.path(key))
	}
}

func codeKey(src string) [32]byte { return sha256.Sum256([]byte(src)) }

// defaultCacheDir is $SWEEF_CACHE_DIR, else the user cache dir + /sweef.
func defaultCacheDir() string {
	if d := os.Getenv("SWEEF_CACHE_DIR"); d != "" {
		if d == "off" {
			return ""
		}
		return d
	}
	if d, err := os.UserCacheDir(); err == nil {
		return filepath.Join(d, "sweef")
	}
	return ""
}
