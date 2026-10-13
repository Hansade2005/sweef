package engine

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cookie is our own cookie record so storage-state can round-trip the full
// attribute set (Chromium's http.Cookie loses SameSite=None + creation time).
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Expires  int64  `json:"expires"` // unix seconds; 0 = session cookie
	HttpOnly bool   `json:"httpOnly"`
	Secure   bool   `json:"secure"`
	SameSite int8   `json:"sameSite"` // 0 default,1 strict,2 lax,3 none
	Creation int64  `json:"creation"`
	// Partitioned is the Partitioned attribute (CHIPS): the cookie is keyed by the top-level site it was set under.
	Partitioned bool `json:"partitioned,omitempty"`
	// Partition is the top-level site ("https://example.com") the cookie is double-keyed with; "" = the unpartitioned
	// jar. Third-party contexts keep their cookies here (partition.go).
	Partition string `json:"partitionKey,omitempty"`
}

// Jar is a small RFC-6265-flavoured cookie store shared by one BrowserContext.
// Capped at 1024 entries with coarse LRU eviction — bounded memory again. The unpartitioned jar and one jar per
// top-level site (the partitions) live side by side; a request reads the ones its context allows (see Header).
type Jar struct {
	mu  sync.Mutex
	m   map[string]map[string]map[string]*Cookie // partition ("" = none) -> host -> name -> cookie
	n   int
	cap int
}

func NewJar() *Jar { return &Jar{m: map[string]map[string]map[string]*Cookie{}, cap: 1024} }

func jarHost(u string) string {
	if pu, err := url.Parse(u); err == nil {
		return strings.TrimPrefix(pu.Hostname(), "www.")
	}
	return ""
}

func (j *Jar) Set(c *Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	host := strings.TrimPrefix(strings.ToLower(c.Domain), ".")
	host = strings.TrimPrefix(host, "www.")
	if host == "" {
		return
	}
	hosts, ok := j.m[c.Partition]
	if !ok {
		hosts = map[string]map[string]*Cookie{}
		j.m[c.Partition] = hosts
	}
	bag, ok := hosts[host]
	if !ok {
		bag = map[string]*Cookie{}
		hosts[host] = bag
	}
	if _, exists := bag[c.Name]; !exists {
		if j.n >= j.cap {
			j.evictLocked()
		}
		j.n++
	}
	cc := *c
	bag[c.Name] = &cc
}

func (j *Jar) evictLocked() {
	// partitioned cookies go first: the unpartitioned jar holds what the person signed in with
	for pk, hosts := range j.m {
		if pk == "" {
			continue
		}
		if j.evictFromLocked(pk, hosts) {
			return
		}
	}
	j.evictFromLocked("", j.m[""])
}

func (j *Jar) evictFromLocked(pk string, hosts map[string]map[string]*Cookie) bool {
	for host, bag := range hosts {
		for name := range bag {
			delete(bag, name)
			j.n--
			if len(bag) == 0 {
				delete(hosts, host)
			}
			if len(hosts) == 0 {
				delete(j.m, pk)
			}
			return true
		}
	}
	return false
}

// cookieQuery says which cookies a read may see.
type cookieQuery struct {
	Unpart    bool   // include the unpartitioned jar
	Partition string // include this partition ("" = none)
	Script    bool   // a script's view (document.cookie): no HttpOnly cookies
	Cross     bool   // a cross-site context: only SameSite=None cookies are available
}

// ForURL renders the Cookie header for a request URL from the unpartitioned jar ("" if none).
func (j *Jar) ForURL(rawurl string) string { return j.Header(rawurl, cookieQuery{Unpart: true}) }

// Header renders the Cookie header for a request URL from the jars the query allows.
func (j *Jar) Header(rawurl string, q cookieQuery) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	pu, err := url.Parse(rawurl)
	if err != nil {
		return ""
	}
	host := strings.TrimPrefix(pu.Hostname(), "www.")
	now := time.Now().Unix()
	secureOK := pu.Scheme == "https" || loopbackHost(pu.Hostname())
	var picked []*Cookie
	scan := func(pk string) {
		for h, bag := range j.m[pk] {
			if h != host && !strings.HasSuffix(host, "."+h) {
				continue
			}
			for _, c := range bag {
				if c.Expires > 0 && c.Expires < now {
					continue
				}
				if c.Secure && !secureOK {
					continue
				}
				if q.Script && c.HttpOnly {
					continue
				}
				if q.Cross && c.SameSite != 3 {
					continue
				}
				picked = append(picked, c)
			}
		}
	}
	if q.Unpart {
		scan("")
	}
	if q.Partition != "" {
		scan(q.Partition)
	}
	sort.Slice(picked, func(a, b int) bool {
		if picked[a].Creation != picked[b].Creation {
			return picked[a].Creation < picked[b].Creation
		}
		return picked[a].Name < picked[b].Name
	})
	parts := make([]string, 0, len(picked))
	for _, c := range picked {
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// All returns every stored cookie of every partition (unsorted).
func (j *Jar) All(_ string) []*Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []*Cookie
	for _, hosts := range j.m {
		for _, bag := range hosts {
			for _, c := range bag {
				cc := *c
				out = append(out, &cc)
			}
		}
	}
	return out
}

// Replace swaps in a new cookie list (used by ClearCookies).
func (j *Jar) Replace(list []*Cookie) {
	j.mu.Lock()
	j.m = map[string]map[string]map[string]*Cookie{}
	j.n = 0
	j.mu.Unlock()
	for _, c := range list {
		j.Set(c)
	}
}

// Attach installs the jar onto an http.Client via a transport RoundTrip hook,
// bypassing net/http's own jar so we keep SameSite/expiry semantics ourselves.
func (j *Jar) Attach(tr *http.Transport) http.RoundTripper {
	base := tr
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if ck := j.ForURL(req.URL.String()); ck != "" {
			req.Header.Set("Cookie", ck)
		}
		resp, err := base.RoundTrip(req)
		if err != nil {
			return resp, err
		}
		for _, sc := range resp.Header.Values("Set-Cookie") {
			if c := ParseSetCookie(sc, req.URL); c != nil {
				j.Set(c)
			}
		}
		return resp, nil
	})
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ParseSetCookie converts a Set-Cookie header into our Cookie record.
func ParseSetCookie(h string, ctx *url.URL) *Cookie {
	parts := strings.Split(h, ";")
	if len(parts) == 0 {
		return nil
	}
	kv := strings.SplitN(strings.TrimSpace(parts[0]), "=", 2)
	if len(kv) != 2 {
		return nil
	}
	c := &Cookie{Name: kv[0], Value: kv[1], Path: "/", Creation: time.Now().Unix()}
	if ctx != nil {
		c.Domain = strings.TrimPrefix(ctx.Hostname(), "www.")
	}
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		lp := strings.ToLower(p)
		switch {
		case lp == "httponly":
			c.HttpOnly = true
		case lp == "secure":
			c.Secure = true
		case lp == "partitioned":
			c.Partitioned = true
		case strings.HasPrefix(lp, "domain="):
			c.Domain = strings.TrimPrefix(strings.TrimPrefix(p[7:], "."), "www.")
		case strings.HasPrefix(lp, "path="):
			c.Path = p[5:]
		case strings.HasPrefix(lp, "max-age="):
			if sec, err := parseInt(p[8:]); err == nil {
				c.Expires = time.Now().Unix() + sec
			}
		case strings.HasPrefix(lp, "expires="):
			if tt, err := http.ParseTime(p[8:]); err == nil {
				c.Expires = tt.Unix()
			}
		case strings.HasPrefix(lp, "samesite="):
			switch strings.ToLower(p[9:]) {
			case "strict":
				c.SameSite = 1
			case "lax":
				c.SameSite = 2
			case "none":
				c.SameSite = 3
				c.Secure = true // browsers require Secure with SameSite=None
			}
		}
	}
	return c
}

func parseInt(s string) (int64, error) {
	var n int64
	neg := false
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return 0, errBadInt
		}
		n = n*10 + int64(ch-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

var errBadInt = &parseErr{"bad integer"}

type parseErr struct{ s string }

func (e *parseErr) Error() string { return e.s }
