package engine

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/publicsuffix"
)

// BrowserContext is the Playwright unit of isolation: its own cookie jar,
// storage state, UA/viewport/geolocation overrides and tab set. One Engine can
// host many contexts — agents spin up throw-away profiles per task for free.
type BrowserContext struct {
	ID      string
	name    string
	eng     *Engine
	options ContextOptions
	jar     *Jar
	state   map[string]any // localStorage/sessionStorage snapshots per origin
	metrics *Metrics
	tabIDs  []string
	created time.Time
	closed  bool
	mu      sync.RWMutex

	httpClient *http.Client            // per-context client with its own cookie jar (see clientFor)
	ds         atomic.Pointer[ctxData] // Data Saver policy, counters and stats (datasaver.go)
}

type ContextOptions struct {
	UserAgent         string            `json:"userAgent,omitempty"`
	ViewportWidth     int               `json:"viewportWidth,omitempty"`
	ViewportHeight    int               `json:"viewportHeight,omitempty"`
	DeviceScaleFactor float64           `json:"deviceScaleFactor,omitempty"`
	Mobile            bool              `json:"mobile,omitempty"`
	Landscape         bool              `json:"landscape,omitempty"`
	Geolocation       *[2]float64       `json:"geolocation,omitempty"`
	Permissions       []string          `json:"permissions,omitempty"`
	Offline           bool              `json:"offline,omitempty"`
	ColorScheme       string            `json:"colorScheme,omitempty"`
	ExtraHeaders      map[string]string `json:"extraHTTPHeaders,omitempty"`
	HTTPCredentials   *[2]string        `json:"-"`
	ProxyURL          string            `json:"proxy,omitempty"` // overrides Options.ProxyURL and the environment for this context
	AcceptLanguage    string            `json:"acceptLanguage,omitempty"`
	StoragePath       string            `json:"storagePath,omitempty"` // load/save state JSON
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// NewContext creates an isolated browsing context inside the engine.
func (e *Engine) NewContext(name string, opts ContextOptions) *BrowserContext {
	e.mu.Lock()
	if name == "" {
		name = "ctx-" + randHex(4)
	}
	if e.contexts == nil {
		e.contexts = map[string]*BrowserContext{}
	}
	c := &BrowserContext{
		ID:      "c_" + randHex(6),
		name:    name,
		eng:     e,
		options: opts,
		jar:     NewJar(),
		state:   map[string]any{},
		metrics: NewMetrics(e.opts.MaxRSSMiB),
		created: time.Now(),
	}
	if c.options.ViewportWidth == 0 {
		c.options.ViewportWidth = e.opts.ViewportWidth
	}
	if c.options.ViewportHeight == 0 {
		c.options.ViewportHeight = e.opts.ViewportHeight
	}
	e.contexts[name] = c
	e.emit(EvConsole, map[string]any{"contextCreated": name})
	e.mu.Unlock()
	if opts.StoragePath != "" {
		_ = c.LoadState(opts.StoragePath)
	}
	return c
}

func (e *Engine) LookupContext(name string) (*BrowserContext, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	c, ok := e.contexts[name]
	return c, ok
}

// DefaultContext returns the implicit context every bare engine has.
func (e *Engine) DefaultContext() *BrowserContext {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.defaultCtx == nil {
		e.defaultCtx = &BrowserContext{
			ID: "c_default", name: "default", eng: e,
			jar: e.jar, metrics: e.metrics, state: map[string]any{},
			created: e.started,
			options: ContextOptions{
				ViewportWidth:  e.opts.ViewportWidth,
				ViewportHeight: e.opts.ViewportHeight,
			},
		}
		e.contexts["default"] = e.defaultCtx
	}
	return e.defaultCtx
}

// ListContexts names every live context (Playwright browser.contexts()).
func (e *Engine) ListContexts() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, 0, len(e.contexts))
	for n := range e.contexts {
		out = append(out, n)
	}
	return out
}

func (c *BrowserContext) Name() string { return c.name }
func (c *BrowserContext) JarRef() *Jar { return c.jar }

func (c *BrowserContext) Options() ContextOptions { return c.options }

func (c *BrowserContext) NewTab(urlStr string) (*Tab, error) {
	t, err := c.eng.NewTabInContext(c, urlStr)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.tabIDs = append(c.tabIDs, t.ID)
	c.mu.Unlock()
	return t, nil
}

func (c *BrowserContext) Tabs() []*Tab {
	var out []*Tab
	for _, id := range c.tabIDs {
		if t, err := c.eng.Tab(id); err == nil {
			out = append(out, t)
		}
	}
	return out
}

func (c *BrowserContext) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	ids := append([]string(nil), c.tabIDs...)
	c.mu.Unlock()
	for _, id := range ids {
		_ = c.eng.CloseTab(id)
	}
	if c.options.StoragePath != "" {
		_ = c.SaveState(c.options.StoragePath)
	}
	c.closeIdle()
	c.eng.removeContext(c.name)
	c.eng.emit(EvConsole, map[string]any{"contextClosed": c.name})
	return nil
}

func (e *Engine) removeContext(name string) {
	e.mu.Lock()
	delete(e.contexts, name)
	e.mu.Unlock()
}

// ---- storage state -----------------------------------------------------------

// SaveState writes a Playwright-compatible storage_state JSON: cookies with
// proper domain/path/expiry/httpOnly/secure fields plus localStorage dumps.
func (c *BrowserContext) SaveState(path string) error {
	var ck []map[string]any
	for _, cki := range c.jar.All("") {
		dom := strings.TrimPrefix(cki.Domain, ".")
		exp := cki.Expires
		if exp <= 0 {
			exp = -1
		}
		ck = append(ck, map[string]any{
			"name": cki.Name, "value": cki.Value, "domain": dom,
			"path": cki.Path, "expires": exp, "httpOnly": cki.HttpOnly,
			"secure": cki.Secure, "sameSite": sameSiteName(cki.SameSite),
		})
	}
	b, err := json.MarshalIndent(map[string]any{
		"cookies":        ck,
		"origins":        c.originsSnapshot(),
		"localStorage":   c.state,
		"sessionStorage": map[string]any{},
		"indexedDB":      map[string]any{},
	}, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return os.WriteFile(path, b, 0o600)
}

func sameSiteName(s int8) string {
	switch s {
	case 1:
		return "Strict"
	case 2:
		return "Lax"
	case 3:
		return "None"
	}
	return "Lax"
}

func (c *BrowserContext) originsSnapshot() []map[string]any {
	var out []map[string]any
	for k, v := range c.state {
		out = append(out, map[string]any{"origin": k, "localStorage": v})
	}
	return out
}

// LoadState restores cookies + storage from a storage-state JSON file.
func (c *BrowserContext) LoadState(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc struct {
		Cookies []struct {
			Name     string  `json:"name"`
			Value    string  `json:"value"`
			Domain   string  `json:"domain"`
			Path     string  `json:"path"`
			Expires  float64 `json:"expires"`
			HTTPOnly bool    `json:"httpOnly"`
			Secure   bool    `json:"secure"`
			SameSite string  `json:"sameSite"`
		} `json:"cookies"`
		LocalStorage map[string]any `json:"localStorage"`
		Origins      []struct {
			Origin       string         `json:"origin"`
			LocalStorage map[string]any `json:"localStorage"`
		} `json:"origins"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, ck := range doc.Cookies {
		ss := int8(2)
		switch strings.ToLower(ck.SameSite) {
		case "strict":
			ss = 1
		case "lax":
			ss = 2
		case "none":
			ss = 3
		}
		exp := int64(ck.Expires)
		if exp < 0 {
			exp = 0 // session cookie
		}
		c.jar.Set(&Cookie{Name: ck.Name, Value: ck.Value, Domain: ck.Domain, Path: ck.Path,
			Expires: exp, HttpOnly: ck.HTTPOnly, Secure: ck.Secure, SameSite: ss, Creation: now})
	}
	if doc.LocalStorage != nil {
		c.mu.Lock()
		for k, v := range doc.LocalStorage {
			c.state[k] = v
		}
		c.mu.Unlock()
	}
	for _, o := range doc.Origins {
		c.mu.Lock()
		c.state[o.Origin] = o.LocalStorage
		c.mu.Unlock()
	}
	return nil
}

// SetStorageValue records a key under an origin bucket (used by Storage API).
func (c *BrowserContext) SetStorageValue(origin, key, val string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	bucket, _ := c.state[origin].(map[string]any)
	if bucket == nil {
		bucket = map[string]any{}
	}
	bucket[key] = val
	c.state[origin] = bucket
}

func (c *BrowserContext) GetStorageValue(origin, key string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	bucket, _ := c.state[origin].(map[string]any)
	if bucket == nil {
		return "", false
	}
	v, ok := bucket[key].(string)
	return v, ok
}

// RemoveStorageValue deletes one key from an origin bucket.
func (c *BrowserContext) RemoveStorageValue(origin, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if bucket, _ := c.state[origin].(map[string]any); bucket != nil {
		delete(bucket, key)
	}
}

// StorageKeys lists the keys stored for an origin, in insertion-independent order.
func (c *BrowserContext) StorageKeys(origin string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	bucket, _ := c.state[origin].(map[string]any)
	keys := make([]string, 0, len(bucket))
	for k := range bucket {
		keys = append(keys, k)
	}
	return keys
}

func (c *BrowserContext) ClearStorage(origin string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if origin == "" {
		c.state = map[string]any{}
		return
	}
	delete(c.state, origin)
}

// ClearCookies drops all or matching-name cookies.
func (c *BrowserContext) ClearCookies(name string) {
	all := c.jar.All("")
	var keep []*Cookie
	for _, ck := range all {
		if name != "" && ck.Name == name {
			continue
		}
		keep = append(keep, ck)
	}
	c.jar.Replace(keep)
}

// AddCookies injects cookies (Playwright context.addCookies).
func (c *BrowserContext) AddCookies(items []map[string]any) error {
	now := time.Now().Unix()
	for _, it := range items {
		ck := &Cookie{Name: str(it["name"]), Value: str(it["value"]), Path: "/", Creation: now}
		host := str(it["domain"])
		if host == "" {
			u := str(it["url"])
			pu, err := url.Parse(u)
			if err != nil {
				return fmt.Errorf("cookie needs domain or valid url")
			}
			host = pu.Hostname()
		}
		ck.Domain = host
		if p := str(it["path"]); p != "" {
			ck.Path = p
		}
		if exp, ok := it["expires"].(float64); ok && exp > 0 {
			ck.Expires = int64(exp)
		}
		if b, _ := it["httpOnly"].(bool); b {
			ck.HttpOnly = true
		}
		if b, _ := it["secure"].(bool); b {
			ck.Secure = true
		}
		switch strings.ToLower(str(it["sameSite"])) {
		case "strict":
			ck.SameSite = 1
		case "lax":
			ck.SameSite = 2
		case "none":
			ck.SameSite = 3
		}
		c.jar.Set(ck)
	}
	return nil
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// ---- cross-origin resource access ------------------------------------------

// Fetch performs a context-scoped request (cookies + credentials apply) that
// does NOT change any tab's DOM — Playwright's APIRequestContext.
func (c *BrowserContext) Fetch(method, urlStr string, headers map[string]string, body string) (int, map[string]string, string, error) {
	req, err := http.NewRequest(strings.ToUpper(method), urlStr, strings.NewReader(body))
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("User-Agent", c.userAgentOr(c.eng.opts.DefaultUserAgent))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if u := c.options.HTTPCredentials; u != nil {
		req.SetBasicAuth(u[0], u[1])
	}
	if ck := c.jar.ForURL(urlStr); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	req, _ = withMeter(req, c, nil, KindXHR, c.policy())
	resp, err := c.eng.clientFor(c).Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	data := readCapped(resp.Body, 8<<20)
	hdr := map[string]string{}
	for k, vs := range resp.Header {
		if len(vs) > 0 {
			hdr[k] = vs[0]
		}
	}
	// persist Set-Cookie like a real context would
	for _, sc := range resp.Header.Values("Set-Cookie") {
		if ck := parseCookie(sc, c.eng.now()); ck != nil {
			host, _ := publicsuffix.EffectiveTLDPlusOne(hostOf(urlStr))
			ck.Domain = host
			c.jar.Set(ck)
		}
	}
	return resp.StatusCode, hdr, string(data), nil
}

func (c *BrowserContext) userAgentOr(def string) string {
	if c.options.UserAgent != "" {
		return c.options.UserAgent
	}
	return def
}

// DeleteCache forgets the HTTP cache entries of this context. For the default context that includes the
// on-disk cache, which is shared by the whole profile.
func (c *BrowserContext) DeleteCache() {
	if c.name == "default" {
		c.eng.res.clear()
		return
	}
	c.eng.res.purgeContext(c.ID)
}

// DropMemoryCache forgets the in-memory tier of the HTTP cache after writing out what is queued for disk, so
// the next requests are served from the on-disk cache only: the state of a browser that was just restarted.
func (e *Engine) DropMemoryCache() { e.res.dropMemory() }

// ClearHTTPCache empties the HTTP cache of every context, the on-disk one included.
func (e *Engine) ClearHTTPCache() { e.res.clear() }
