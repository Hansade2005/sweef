package engine

// httpcache_policy.go: the HTTP caching rules (RFC 9111) as pure functions, so every header case can be
// tested without a network. The cache itself is httpcache_store.go, the RoundTripper that applies these rules
// is httpcache_rt.go.
//
// This is a private (single-user) browser cache: `private` responses are stored, `s-maxage` and
// `proxy-revalidate` are ignored. Rules in short:
//
//   - no-store: never stored, and an older stored copy is dropped;
//   - no-cache: stored, but revalidated before every reuse;
//   - freshness: max-age, else Expires minus Date, else (not for documents) 10% of the time since
//     Last-Modified, at most 7 days; 301/308 with no stated lifetime are fresh for a day;
//   - age: max(Age, apparent age) plus the time since the response arrived;
//   - a stale entry with ETag/Last-Modified is revalidated (If-None-Match / If-Modified-Since); a 304 keeps
//     the stored body and merges the new headers;
//   - Vary: the request headers a response varies on must match; Vary: * is never stored;
//   - stale-if-error serves a stale entry when revalidation fails or answers 5xx (not with must-revalidate);
//   - a document is only ever served without asking when the server stated a lifetime (no heuristics).

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ccDirectives are the Cache-Control directives the cache acts on.
type ccDirectives struct {
	noStore, noCache, private, public bool
	mustRevalidate, immutable         bool
	onlyIfCached                      bool

	maxAge        time.Duration
	hasMaxAge     bool // a valid max-age was present
	invalidMaxAge bool // max-age was present but not delta-seconds: treat as already stale
	staleIfError  time.Duration
	minFresh      time.Duration
}

const maxDelta = 1 << 31 // seconds: RFC 9111 5.2.1.1 lets a cache clamp larger values

func deltaSeconds(s string) (time.Duration, bool) {
	s = strings.Trim(strings.TrimSpace(s), `"`)
	if s == "" {
		return 0, false
	}
	var n int64
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int64(r-'0')
		if n > maxDelta {
			n = maxDelta
		}
	}
	return time.Duration(n) * time.Second, true
}

// splitDirectives splits a Cache-Control field value at the commas outside quotes.
func splitDirectives(v string) []string {
	var out []string
	start, quoted := 0, false
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '"':
			quoted = !quoted
		case '\\':
			if quoted {
				i++
			}
		case ',':
			if !quoted {
				out = append(out, v[start:i])
				start = i + 1
			}
		}
	}
	return append(out, v[start:])
}

// parseCacheControl reads every Cache-Control field line of a message.
func parseCacheControl(vals []string) ccDirectives {
	var d ccDirectives
	for _, line := range vals {
		for _, part := range splitDirectives(line) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, val := part, ""
			if i := strings.IndexByte(part, '='); i >= 0 {
				name, val = strings.TrimSpace(part[:i]), strings.TrimSpace(part[i+1:])
			}
			switch strings.ToLower(name) {
			case "no-store":
				d.noStore = true
			case "no-cache":
				d.noCache = true
			case "private":
				d.private = true
			case "public":
				d.public = true
			case "must-revalidate":
				d.mustRevalidate = true
			case "immutable":
				d.immutable = true
			case "only-if-cached":
				d.onlyIfCached = true
			case "max-age":
				if n, ok := deltaSeconds(val); ok {
					// the smallest of several max-age values wins (conservative)
					if !d.hasMaxAge || n < d.maxAge {
						d.maxAge = n
					}
					d.hasMaxAge = true
				} else {
					d.invalidMaxAge = true
				}
			case "stale-if-error":
				if n, ok := deltaSeconds(val); ok {
					d.staleIfError = n
				}
			case "min-fresh":
				if n, ok := deltaSeconds(val); ok {
					d.minFresh = n
				}
			}
		}
	}
	return d
}

// responseCC is the Cache-Control of a response, with the HTTP/1.0 Pragma: no-cache fallback.
func responseCC(h http.Header) ccDirectives {
	d := parseCacheControl(h.Values("Cache-Control"))
	if len(h.Values("Cache-Control")) == 0 && pragmaNoCache(h) {
		d.noCache = true
	}
	return d
}

func pragmaNoCache(h http.Header) bool {
	for _, v := range h.Values("Pragma") {
		if strings.Contains(strings.ToLower(v), "no-cache") {
			return true
		}
	}
	return false
}

// lifetimeInput is what the freshness of a stored response depends on besides its headers.
type lifetimeInput struct {
	kind   DataKind
	rawURL string
	extend bool // Data Saver ExtendCache: versioned / immutable assets stay fresh longer
}

const (
	maxHeuristic      = 7 * 24 * time.Hour
	permanentRedirect = 24 * time.Hour
)

func staticKind(k DataKind) bool {
	switch k {
	case KindImage, KindFont, KindStyle, KindScript:
		return true
	}
	return false
}

// httpDate parses an HTTP date header; the zero Time means absent or invalid.
func httpDate(h http.Header, name string) (time.Time, bool) {
	v := h.Get(name)
	if v == "" {
		return time.Time{}, false
	}
	t, err := http.ParseTime(v)
	if err != nil {
		return time.Time{}, true // present but invalid
	}
	return t, true
}

// freshnessLifetime is how long a response stays fresh after it was generated. explicit says the server
// stated a lifetime (max-age, Expires or no-cache); a heuristic lifetime is explicit == false. respTime is
// when the response arrived, the stand-in for a missing Date header.
func freshnessLifetime(status int, h http.Header, respTime time.Time, in lifetimeInput) (life time.Duration, explicit bool) {
	cc := responseCC(h)
	if cc.noCache {
		return 0, true
	}
	switch {
	case cc.invalidMaxAge && !cc.hasMaxAge:
		return 0, true
	case cc.hasMaxAge:
		life, explicit = cc.maxAge, true
	default:
		if exp, present := httpDate(h, "Expires"); present {
			date, ok := httpDate(h, "Date")
			if !ok || date.IsZero() {
				date = respTime
			}
			if exp.IsZero() || !exp.After(date) { // invalid Expires means already expired
				return 0, true
			}
			life, explicit = exp.Sub(date), true
		}
	}
	if !explicit {
		switch {
		case status == http.StatusMovedPermanently || status == http.StatusPermanentRedirect:
			life = permanentRedirect
		case in.kind != KindDocument:
			if lm, ok := httpDate(h, "Last-Modified"); ok && !lm.IsZero() {
				date, dok := httpDate(h, "Date")
				if !dok || date.IsZero() {
					date = respTime
				}
				if d := date.Sub(lm); d > 0 {
					life = d / 10
					if life > maxHeuristic {
						life = maxHeuristic
					}
				}
			}
		}
		if cc.immutable && life < 24*time.Hour {
			life = 24 * time.Hour
		}
	}
	// Data Saver: static assets that can be revalidated and whose URL says they are versioned stay fresh for
	// at least a day; the server's longer lifetime stands, up to 30 days.
	if in.extend && staticKind(in.kind) && (h.Get("ETag") != "" || h.Get("Last-Modified") != "") {
		if cc.immutable || versionedURL(in.rawURL) {
			if life < 24*time.Hour {
				life = 24 * time.Hour
			}
			if life > 30*24*time.Hour {
				life = 30 * 24 * time.Hour
			}
			explicit = true
		}
	}
	return life, explicit
}

// currentAge is RFC 9111 4.2.3: the larger of the Age header and the apparent age, plus the time the
// response has been stored.
func currentAge(h http.Header, respTime, now time.Time) time.Duration {
	var age time.Duration
	if v := h.Get("Age"); v != "" {
		if n, ok := deltaSeconds(v); ok {
			age = n
		}
	}
	if date, ok := httpDate(h, "Date"); ok && !date.IsZero() {
		if apparent := respTime.Sub(date); apparent > age {
			age = apparent
		}
	}
	if resident := now.Sub(respTime); resident > 0 {
		age += resident
	}
	return age
}

// cacheableStatus says whether a response with this status may be stored. Heuristically cacheable codes
// are stored with any lifetime; other codes only when the server stated one.
func cacheableStatus(status int, explicitLife bool) bool {
	switch status {
	case 200, 203, 204, 300, 301, 308:
		return true
	case 206:
		return true
	}
	if !explicitLife {
		return false
	}
	switch status {
	case 302, 307, 404, 405, 410, 414, 501:
		return true
	}
	return false
}

// varyNames lists the request headers a response varies on (lower case, sorted; Accept-Encoding is left out
// because the engine decodes every encoding it asks for). star reports Vary: *.
func varyNames(h http.Header) (names []string, star bool) {
	seen := map[string]bool{}
	for _, v := range h.Values("Vary") {
		for _, n := range strings.Split(v, ",") {
			n = strings.ToLower(strings.TrimSpace(n))
			switch n {
			case "":
			case "*":
				star = true
			case "accept-encoding":
			default:
				if !seen[n] {
					seen[n] = true
					names = append(names, n)
				}
			}
		}
	}
	sort.Strings(names)
	return names, star
}

// varySnapshot canonicalises the request header values named by names.
func varySnapshot(names []string, get func(name string) string) string {
	if len(names) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, n := range names {
		sb.WriteString(n)
		sb.WriteByte('=')
		sb.WriteString(strings.Join(strings.Fields(get(n)), " "))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// noStoreResponse reports whether the response forbids storing (no-store, Vary: *).
func noStoreResponse(h http.Header) bool {
	if responseCC(h).noStore {
		return true
	}
	_, star := varyNames(h)
	return star
}

// hopByHop are removed from a response before it is stored. Set-Cookie is never replayed from the cache.
var hopByHop = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer",
	"Transfer-Encoding", "Upgrade", "Set-Cookie", "Set-Cookie2"}

func storableHeader(h http.Header) http.Header {
	out := h.Clone()
	if out == nil {
		out = http.Header{}
	}
	for _, k := range hopByHop {
		out.Del(k)
	}
	for _, f := range h.Values("Connection") {
		for _, n := range strings.Split(f, ",") {
			if n = strings.TrimSpace(n); n != "" {
				out.Del(n)
			}
		}
	}
	return out
}

// mergeNotModified updates stored headers with the ones of a 304 (RFC 9111 3.2). Headers that describe the
// body of the stored response are kept.
func mergeNotModified(stored, fresh http.Header) http.Header {
	out := stored.Clone()
	skip := map[string]bool{"Content-Length": true, "Content-Encoding": true, "Content-Range": true, "Content-Type": true,
		"Content-Md5": true}
	for _, k := range hopByHop {
		skip[k] = true
	}
	for k, vs := range fresh {
		if skip[http.CanonicalHeaderKey(k)] {
			continue
		}
		out[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	return out
}

// staleIfErrorOK reports whether a stale response of this age may be served after a failed revalidation.
func staleIfErrorOK(h http.Header, age, life time.Duration) bool {
	cc := responseCC(h)
	if cc.mustRevalidate || cc.noCache || cc.staleIfError <= 0 {
		return false
	}
	return age < life+cc.staleIfError
}

// hasConditional reports whether the caller made the request conditional itself (page code revalidating by
// hand): such requests go to the network and their answers are not stored.
func hasConditional(h http.Header) bool {
	for _, k := range []string{"If-None-Match", "If-Modified-Since", "If-Match", "If-Unmodified-Since", "If-Range"} {
		if h.Get(k) != "" {
			return true
		}
	}
	return false
}

// requestBypass: the request asks to ignore stored copies (a reload: Cache-Control: no-cache or Pragma:
// no-cache). Such a request is sent without validators and its answer replaces the stored one.
func requestBypass(h http.Header) (bypass bool, d ccDirectives) {
	d = parseCacheControl(h.Values("Cache-Control"))
	if d.noCache || (len(h.Values("Cache-Control")) == 0 && pragmaNoCache(h)) {
		return true, d
	}
	return false, d
}

// normalizeURL is the cache key form of a URL: no fragment, lower-case scheme and host, default port
// dropped, empty path "/".
func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	u.Fragment = ""
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	u.Host = host
	if u.Path == "" && u.Opaque == "" {
		u.Path = "/"
	}
	u.User = nil
	return u.String()
}

func i64s(n int64) string { return strconv.FormatInt(n, 10) }
