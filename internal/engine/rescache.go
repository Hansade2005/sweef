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
	"net/url"
	"os"
	"path/filepath"
	"sort"
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

	// Data Saver additions
	xfer       int64    // transfer bytes the response that filled the entry cost (headers + raw body)
	vary       []string // lower-cased request header names from the response's Vary
	varyVal    string   // snapshot of those request headers when the entry was stored
	windowOnly bool     // expires is only the short same-navigation window, not real freshness
	tab        string   // tab and navigation epoch that stored it (for windowOnly entries)
	epoch      int64
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

// get returns a snapshot of the entry: refresh updates the stored one in place.
func (c *resCache) get(url string) *resEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[url]; e != nil {
		cp := *e
		return &cp
	}
	return nil
}

// key partitions the cache: the default context shares entries (as before),
// every other context (private browsing, isolated contexts) has its own, so a
// cached response never crosses a context boundary.
func (c *resCache) key(ctx *BrowserContext, url string) string {
	if ctx == nil || ctx.name == "default" {
		return url
	}
	return ctx.ID + "|" + url
}

// refresh updates the expiry of an entry after a successful revalidation.
func (c *resCache) refresh(key string, exp time.Time, now time.Time, window bool, tab string, epoch int64) {
	c.mu.Lock()
	if e := c.entries[key]; e != nil {
		e.expires, e.storedAt, e.windowOnly = exp, now, window
		if window {
			e.tab, e.epoch = tab, epoch
		}
	}
	c.mu.Unlock()
}

// drop removes an entry (a response that must not be reused).
func (c *resCache) drop(key string) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok {
		c.bytes -= len(e.body)
		delete(c.entries, key)
		for i, k := range c.order {
			if k == key {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
	}
	c.mu.Unlock()
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
	if strings.Contains(cc, "no-store") {
		return false
	}
	// Vary: * says no request can be matched to the stored response
	for _, v := range h.Values("Vary") {
		if strings.TrimSpace(v) == "*" {
			return false
		}
	}
	return true
}

// varyNames lists the request headers a response varies on (lower case;
// Accept-Encoding is left out because the engine decodes every encoding it asks for).
func varyNames(h http.Header) []string {
	var out []string
	for _, v := range h.Values("Vary") {
		for _, n := range strings.Split(v, ",") {
			n = strings.ToLower(strings.TrimSpace(n))
			if n != "" && n != "accept-encoding" {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out
}

// versionedURL reports whether the URL carries a version or content hash
// (app.3f9a1c7e.js, logo-9b2e4d1a.png, lib.js?v=1.2.3): its content changes
// only when the URL does.
func versionedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	name := u.Path
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '_' || r == '-' }) {
		hex, digit, letter := true, false, false
		for _, r := range seg {
			switch {
			case r >= '0' && r <= '9':
				digit = true
			case r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F':
				letter = true
			default:
				hex = false
				if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
					letter = true
				}
			}
		}
		if len(seg) >= 8 && hex && digit {
			return true
		}
		if len(seg) >= 16 && digit && letter {
			return true
		}
	}
	q := u.Query()
	for _, k := range []string{"v", "ver", "version", "rev", "hash", "h", "cb", "build"} {
		if q.Get(k) != "" {
			return true
		}
	}
	return false
}

func staticKind(k DataKind) bool {
	switch k {
	case KindImage, KindFont, KindStyle, KindScript:
		return true
	}
	return false
}

// extendedFreshness is freshness for the saver levels. It only ever applies to
// static assets that can be revalidated (images, fonts, CSS, JS with an ETag or
// Last-Modified) and never overrides no-store / no-cache:
//
//   - an immutable or versioned URL is fresh for at least a day (or the
//     server's longer max-age, capped at 30 days);
//   - without any explicit lifetime, RFC 9111's heuristic applies: 10% of the
//     time since Last-Modified, at most a day;
//   - otherwise the server's lifetime stands, and when it ends the asset is
//     revalidated with If-None-Match / If-Modified-Since (a 304 costs a few
//     hundred bytes) instead of being downloaded again.
//
// Documents are never made fresher than the server said.
func extendedFreshness(h http.Header, now time.Time, kind DataKind, raw string) time.Time {
	base := freshness(h, now)
	cc := strings.ToLower(h.Get("Cache-Control"))
	if !staticKind(kind) || strings.Contains(cc, "no-store") || strings.Contains(cc, "no-cache") ||
		(h.Get("ETag") == "" && h.Get("Last-Modified") == "") {
		return base
	}
	if strings.Contains(cc, "immutable") || versionedURL(raw) {
		life := 24 * time.Hour
		if !base.IsZero() {
			if d := base.Sub(now); d > life {
				life = d
			}
		}
		if mx := maxAgeOf(cc); mx > life {
			life = mx
		}
		if life > 30*24*time.Hour {
			life = 30 * 24 * time.Hour
		}
		return now.Add(life)
	}
	if base.IsZero() {
		if lm, err := http.ParseTime(h.Get("Last-Modified")); err == nil && lm.Before(now) {
			life := now.Sub(lm) / 10
			if life > 24*time.Hour {
				life = 24 * time.Hour
			}
			if life > 0 {
				return now.Add(life)
			}
		}
	}
	return base
}

func maxAgeOf(cc string) time.Duration {
	for _, part := range strings.Split(cc, ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "max-age=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(part, "max-age=")); err == nil && n > 0 {
				return time.Duration(n) * time.Second
			}
		}
	}
	return 0
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
