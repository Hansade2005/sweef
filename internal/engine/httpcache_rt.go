package engine

// httpcache_rt.go: the RoundTripper that applies the HTTP caching rules (httpcache_policy.go) to every request
// the engine makes for a page: documents, sub-resources, page fetch/XHR, redirect hops. It sits above meterRT, so
// a cache hit costs no bytes and is never charged; a revalidation costs the 304's few hundred.
//
//	http.Client -> cacheRT -> meterRT -> http.Transport (counted dialer, proxy)

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type cacheRT struct {
	base  http.RoundTripper
	cache *httpCache
	eng   *Engine
}

// maxCachedBody is the largest body the tee keeps for storing (the memory tier takes a quarter of its size,
// the disk tier an eighth of its cap; larger bodies are streamed through unstored).
func (c *cacheRT) bodyLimit() int {
	n := c.cache.max / 4
	if c.cache.disk != nil && c.cache.disk.maxEntry() > n {
		n = c.cache.disk.maxEntry()
	}
	return n
}

func (c *cacheRT) CloseIdleConnections() {
	if ci, ok := c.base.(interface{ CloseIdleConnections() }); ok {
		ci.CloseIdleConnections()
	}
}

func (c *cacheRT) RoundTrip(req *http.Request) (*http.Response, error) {
	mi, _ := req.Context().Value(meterKey).(*meterInfo)
	if mi == nil || mi.t == nil || c.cache == nil { // context-level requests (APIRequestContext) never use the browser cache
		return c.base.RoundTrip(req)
	}
	now := c.eng.now()
	mi.cachedXfer.Store(0)
	mi.fromCache.Store(false)
	mi.setCache("")
	if req.Method != http.MethodGet {
		res, err := c.base.RoundTrip(req)
		if err == nil && res.StatusCode < 400 && unsafeMethod(req.Method) {
			key, persist := c.cache.key(mi.c, req.URL.String(), "")
			c.cache.drop(key, persist)
		}
		return res, err
	}
	bypass, rd := requestBypass(req.Header)
	if hasConditional(req.Header) || rd.noStore {
		mi.setCache("bypass")
		return c.base.RoundTrip(req)
	}
	rng := req.Header.Get("Range")
	key, persist := c.cache.key(mi.c, req.URL.String(), rng)
	in := lifetimeInput{kind: mi.kind, rawURL: req.URL.String(), extend: mi.pol.ExtendCache}

	var e *cacheEntry
	if !bypass {
		if e = c.cache.get(key, persist); e != nil && !c.varyMatches(e, req, mi) {
			e = nil
		}
	}
	if e != nil && e.TooLarge > 0 {
		// A tombstone answers only while it still justifies the same refusal; otherwise fetch for real.
		life, _ := freshnessLifetime(e.Status, e.Header, e.Resp, in)
		if mi.maxBody > 0 && e.TooLarge > mi.maxBody && currentAge(e.Header, e.Resp, now) < life {
			return c.serveTombstone(req, mi, e), nil
		}
		e = nil
	}
	if e != nil && !e.navOnly {
		life, _ := freshnessLifetime(e.Status, e.Header, e.Resp, in)
		age := currentAge(e.Header, e.Resp, now)
		fresh := age < life
		if fresh && rd.hasMaxAge && age > rd.maxAge {
			fresh = false
		}
		if fresh && rd.minFresh > 0 && life-age < rd.minFresh {
			fresh = false
		}
		if fresh {
			return c.serve(req, mi, e, "hit", nil, age), nil
		}
		if rd.onlyIfCached {
			return gatewayTimeout(req), nil
		}
		if et, lm := e.Header.Get("ETag"), e.Header.Get("Last-Modified"); et != "" || lm != "" {
			return c.revalidate(req, mi, key, persist, e, in, life, age, now)
		}
	} else if rd.onlyIfCached {
		return gatewayTimeout(req), nil
	}
	res, err := c.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	return c.admit(req, res, mi, key, persist, in, now, "miss"), nil
}

func unsafeMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	}
	return false
}

func gatewayTimeout(req *http.Request) *http.Response {
	return &http.Response{Status: "504 Gateway Timeout", StatusCode: 504, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: req}
}

// requestHeader is the value the request will carry for name, including headers meterRT adds below us.
func requestHeader(req *http.Request, mi *meterInfo, name string) string {
	switch name {
	case "save-data":
		if v := req.Header.Get("Save-Data"); v != "" {
			return v
		}
		if mi.pol.SaveDataHeader {
			return "on"
		}
		return ""
	case "host":
		return req.URL.Host
	}
	return strings.Join(req.Header.Values(name), ", ")
}

func (c *cacheRT) varyMatches(e *cacheEntry, req *http.Request, mi *meterInfo) bool {
	if len(e.Vary) == 0 {
		return true
	}
	return e.VaryV == varySnapshot(e.Vary, func(n string) string { return requestHeader(req, mi, n) })
}

// serve answers from a stored entry.
func (c *cacheRT) serve(req *http.Request, mi *meterInfo, e *cacheEntry, state string, fresh http.Header, age time.Duration) *http.Response {
	h := e.Header.Clone()
	h.Set("Age", i64s(int64(age/time.Second)))
	h.Set("Content-Length", i64s(int64(len(e.Body))))
	for _, sc := range fresh.Values("Set-Cookie") { // cookies of a 304 still apply
		h.Add("Set-Cookie", sc)
	}
	res := &http.Response{
		Status: fmt.Sprintf("%d %s", e.Status, http.StatusText(e.Status)), StatusCode: e.Status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: h,
		Body: io.NopCloser(bytes.NewReader(e.Body)), ContentLength: int64(len(e.Body)),
		Request: req, Uncompressed: true,
	}
	mi.setCache(state)
	mi.cachedXfer.Store(e.Xfer)
	mi.fromCache.Store(true)
	mi.declared.Store(int64(len(e.Body)))
	mi.hdrBytes.Store(0)
	mi.rawBytes.Store(0)
	// a hit is a saving only when it comes from an earlier navigation
	var tab string
	var epoch int64
	if mi.t != nil {
		tab, epoch = mi.t.ID, mi.t.ds.epoch.Load()
		if e.tab != tab || e.epoch != epoch {
			mi.t.noteCacheHit(e.Xfer)
		}
	} else if mi.c != nil && e.Xfer > 0 {
		mi.c.data().acc.addCacheHit(e.Xfer)
	}
	if mi.t != nil {
		c.cache.mark(e.Key, tab, epoch)
	}
	return res
}

// serveTombstone answers a request that the cap refused last time with the remembered headers and no body;
// the caller sees the declared size, trips its cap and skips the resource without a request.
func (c *cacheRT) serveTombstone(req *http.Request, mi *meterInfo, e *cacheEntry) *http.Response {
	res := &http.Response{
		Status: "200 OK", StatusCode: e.Status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1, Header: e.Header.Clone(),
		Body: io.NopCloser(bytes.NewReader(nil)), ContentLength: e.TooLarge, Request: req, Uncompressed: true,
	}
	mi.setCache("tombstone")
	mi.declared.Store(e.TooLarge)
	mi.hdrBytes.Store(0)
	mi.rawBytes.Store(0)
	return res
}

// revalidate asks the origin whether the stale entry is still current.
func (c *cacheRT) revalidate(req *http.Request, mi *meterInfo, key string, persist bool, e *cacheEntry, in lifetimeInput, life, age time.Duration, now time.Time) (*http.Response, error) {
	r2 := req.Clone(req.Context())
	if et := e.Header.Get("ETag"); et != "" {
		r2.Header.Set("If-None-Match", et)
	}
	if lm := e.Header.Get("Last-Modified"); lm != "" {
		r2.Header.Set("If-Modified-Since", lm)
	}
	res, err := c.base.RoundTrip(r2)
	if err != nil {
		if staleIfErrorOK(e.Header, age, life) {
			return c.serve(req, mi, e, "stale-if-error", nil, age), nil
		}
		return nil, err
	}
	switch {
	case res.StatusCode == http.StatusNotModified:
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		res.Body.Close() // books the 304's bytes
		ne := e.clone()
		ne.Header = mergeNotModified(e.Header, res.Header)
		ne.Resp = c.eng.now()
		ne.Key = key
		if mi.t != nil {
			ne.tab, ne.epoch = mi.t.ID, mi.t.ds.epoch.Load()
		}
		c.cache.put(ne, persist)
		return c.serve(req, mi, e, "revalidated", res.Header, 0), nil
	case res.StatusCode >= 500 && staleIfErrorOK(e.Header, age, life):
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		res.Body.Close()
		return c.serve(req, mi, e, "stale-if-error", nil, age), nil
	}
	return c.admit(req, res, mi, key, persist, in, now, "miss"), nil
}

// admit decides whether a network response is stored and wraps its body so that a complete read stores it.
func (c *cacheRT) admit(req *http.Request, res *http.Response, mi *meterInfo, key string, persist bool, in lifetimeInput, now time.Time, state string) *http.Response {
	reason := ""
	switch {
	case res.Request != nil && res.Request.Method != http.MethodGet:
		reason = "method"
	case noStoreResponse(res.Header):
		reason = "no-store"
		c.cache.drop(key, persist) // an older copy must not outlive a no-store answer
	case res.StatusCode == http.StatusNotModified:
		reason = "unexpected-304"
	case res.StatusCode == http.StatusPartialContent && (req.Header.Get("Range") == "" || res.Header.Get("Content-Range") == ""):
		reason = "partial"
	case req.Header.Get("Range") != "" && res.StatusCode != http.StatusPartialContent:
		reason = "range-ignored"
	}
	var life time.Duration
	var explicit bool
	if reason == "" {
		life, explicit = freshnessLifetime(res.StatusCode, res.Header, now, in)
		if !cacheableStatus(res.StatusCode, explicit && life > 0) {
			reason = fmt.Sprintf("status-%d", res.StatusCode)
		}
	}
	navOnly := false
	if reason == "" && life <= 0 && res.Header.Get("ETag") == "" && res.Header.Get("Last-Modified") == "" {
		// nothing to revalidate with and not fresh: only the loader of this navigation may reuse it
		if in.kind == KindDocument || in.kind == KindXHR || in.kind == KindMedia || in.kind == KindFrame || res.StatusCode != http.StatusOK {
			reason = "no-lifetime"
		} else {
			navOnly = true
		}
	}
	if reason != "" {
		mi.setCache(state + ": " + reason)
		return res
	}
	names, _ := varyNames(res.Header)
	vary := varySnapshot(names, func(n string) string { return requestHeader(req, mi, n) })
	hdr := storableHeader(res.Header)
	store := func(body []byte) {
		e := &cacheEntry{
			Key: key, URL: req.URL.String(), Status: res.StatusCode, Header: hdr, Vary: names, VaryV: vary,
			Resp: now, Xfer: mi.hdrBytes.Load() + mi.rawBytes.Load(), Kind: string(in.kind), Body: body, navOnly: navOnly,
		}
		if mi.t != nil {
			e.tab, e.epoch = mi.t.ID, mi.t.ds.epoch.Load()
		}
		c.cache.put(e, persist)
	}
	mi.setCache(fmt.Sprintf("%s: stored lifetime=%ds", state, int64(life/time.Second)))
	// small, bodiless or redirect answers are read here: clients close them early
	if res.StatusCode >= 300 && res.StatusCode < 400 || res.StatusCode == http.StatusNoContent {
		b, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		if err == nil {
			res.Body.Close()
			store(b)
			res.Body = io.NopCloser(bytes.NewReader(b))
			res.ContentLength = int64(len(b))
			return res
		}
		res.Body = &prefixBody{r: io.MultiReader(bytes.NewReader(b), res.Body), c: res.Body}
		return res
	}
	tb := &teeBody{rc: res.Body, limit: c.bodyLimit(), onEOF: store}
	if declared := mi.declared.Load(); mi.maxBody > 0 && declared > mi.maxBody && res.StatusCode == http.StatusOK && !navOnly {
		// the caller is about to refuse this response unread: remember that, with its freshness
		tb.onAbandon = func() {
			e := &cacheEntry{Key: key, URL: req.URL.String(), Status: res.StatusCode, Header: hdr, Vary: names, VaryV: vary,
				Resp: now, Kind: string(in.kind), TooLarge: declared}
			c.cache.put(e, persist)
		}
	}
	res.Body = tb
	return res
}

type prefixBody struct {
	r io.Reader
	c io.Closer
}

func (p *prefixBody) Read(b []byte) (int, error) { return p.r.Read(b) }
func (p *prefixBody) Close() error               { return p.c.Close() }

// teeBody collects the body while the caller reads it and stores it once the end is reached without error.
type teeBody struct {
	rc    io.ReadCloser
	buf   bytes.Buffer
	limit int
	over  bool
	done  bool
	nread int64
	onEOF func([]byte)
	// onAbandon runs when the body is closed before a single byte was read
	onAbandon func()
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	t.nread += int64(n)
	if n > 0 && !t.over {
		t.buf.Write(p[:n])
		if t.buf.Len() > t.limit {
			t.over = true
			t.buf = bytes.Buffer{}
		}
	}
	if err == io.EOF && !t.over && !t.done {
		t.done = true
		t.onEOF(t.buf.Bytes())
	}
	return n, err
}

func (t *teeBody) Close() error {
	if t.onAbandon != nil && t.nread == 0 && !t.done {
		t.onAbandon()
		t.onAbandon = nil
	}
	return t.rc.Close()
}
