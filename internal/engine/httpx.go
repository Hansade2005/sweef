package engine

// httpx.go: the one place sub-resource and script-initiated requests are
// made. It applies route interception, the policy gates that need the
// network (background suspension, size caps with early abort) and returns the
// bytes together with what they cost. fetchRes sits on top of it and adds the
// HTTP cache and the per-kind policy gates.

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	fnet "github.com/sweefbrowser/sweef/internal/net"
)

type httpOpts struct {
	method  string
	url     string
	headers map[string]string
	body    string
	kind    DataKind
	// maxBody > 0 aborts the response when its transfer size exceeds this:
	// before reading anything when Content-Length says so, else while reading.
	maxBody int64
	// anonymous: send no cookies and keep none from the response (CORS requests without
	// credentials, such as a cross-origin web font)
	anonymous bool
}

type httpResp struct {
	status   int
	header   http.Header
	body     []byte
	finalURL string
	xfer     int64 // header + raw body bytes this response cost (0 for synthetic responses)
	declared int64 // Content-Length as received (transfer size), -1 unknown
	aborted  bool  // the size cap tripped; body is empty
	synth    bool

	statusText string // reason phrase to report instead of the standard one (service worker responses)

	fromCache  bool   // answered by the HTTP cache (hit, 304 revalidation or stale-if-error)
	cachedXfer int64  // what the stored response cost when it was downloaded (for budgets and savings)
	cacheState string // see meterInfo.cacheState
}

var (
	errSaverBlocked = errors.New("blocked by Data Saver")
	errBackground   = errors.New("TypeError: Failed to fetch (tab is in the background)")
)

// errTooLarge is returned for a response aborted by a size cap. declared is its
// Content-Length (-1 when the server did not say).
type errTooLarge struct{ declared int64 }

func (e errTooLarge) Error() string {
	return fmt.Sprintf("response larger than the Data Saver cap (%d bytes)", e.declared)
}

// suspendedKind: what a suspended (background) tab may not fetch. The document
// and its static sub-resources are part of a navigation somebody asked for.
func suspendedKind(k DataKind) bool {
	switch k {
	case KindXHR, KindMedia, KindOther, KindFrame, KindFont:
		return true
	}
	return false
}

// httpDo performs a request on behalf of the page or the engine: cookies, UA,
// extra headers, route interception and the network ring all apply.
func (t *Tab) httpDo(o httpOpts) (*httpResp, error) {
	method := o.method
	if method == "" {
		method = "GET"
	}
	kind := o.kind
	if kind == "" {
		kind = KindXHR
	}
	abs, err := resolveURL(t.URL(), o.url)
	if err != nil {
		return nil, err
	}
	if r, handled, err := t.swFetch(o, abs, kind); handled { // a service worker controls the document
		return r, err
	}
	if t.IsOfflineMode() {
		return nil, fmt.Errorf("TypeError: Failed to fetch (offline)")
	}
	if suspendedKind(kind) && t.suspended() {
		return nil, errBackground
	}
	synth, m2, target, hdrs2, body2, rerr := t.routedRequest(strings.ToUpper(method), abs, o.headers, o.body)
	if rerr != nil {
		return nil, fmt.Errorf("TypeError: Failed to fetch (%s)", rerr.Error())
	}
	if synth != nil {
		rb, _ := io.ReadAll(synth.Body)
		t.netRing.Push(Event{Tab: t.ID, Kind: EvResponse, At: time.Now(), Data: fnet.RequestRecord{Method: m2, URL: abs, FinalURL: abs, Status: synth.StatusCode, Headers: map[string]string{"x-sweef-route": "fulfill"}}})
		return &httpResp{status: synth.StatusCode, header: synth.Header, body: rb, finalURL: abs, declared: -1, synth: true}, nil
	}
	abs, method, headers, body := target, m2, hdrs2, body2
	t.touchNet()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.withNetTrace(t.eng.ctx, string(kind)), strings.ToUpper(method), abs, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fontCSSUserAgent(req.URL.Hostname(), t.UserAgent()))
	t.setBrowserHeaders(req.Header, req.URL, kind, false)
	for k, v := range t.ExtraHeaders() {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if ck := t.Context().JarRef().ForURL(abs); ck != "" && !o.anonymous {
		req.Header.Set("Cookie", ck)
	}
	rec := fnet.RequestRecord{Method: req.Method, URL: abs, Headers: flattenHeaders(req.Header)}
	t.netRing.Push(Event{Tab: t.ID, Kind: EvRequest, At: time.Now(), Data: rec})
	start := time.Now()
	req, mi := withMeter(req, t.Context(), t, kind, t.policy())
	mi.maxBody = o.maxBody
	cl := t.eng.clientFor(t.Context())
	if o.anonymous { // the client's own cookie jar must not add or keep cookies either
		c2 := *cl
		c2.Jar = nil
		cl = &c2
	}
	res, err := cl.Do(req)
	defer func() { t.tspan("http."+string(kind), abs, start) }()
	if err != nil {
		t.netRing.Push(Event{Tab: t.ID, Kind: EvFailure, At: time.Now(), Data: map[string]string{"url": abs, "error": err.Error()}})
		return nil, fmt.Errorf("TypeError: Failed to fetch (%s)", err.Error())
	}
	defer res.Body.Close()
	declared := mi.declared.Load()
	out := &httpResp{status: res.StatusCode, header: res.Header, declared: declared,
		fromCache: mi.fromCache.Load(), cachedXfer: mi.cachedXfer.Load(), cacheState: mi.cacheState()}
	if o.maxBody > 0 && declared > o.maxBody && !out.fromCache {
		// Early abort: nothing of the body is read; closing the response drops
		// the connection. The bytes the server would have sent are not read.
		out.aborted = true
		res.Body.Close()
		t.noteAborted(declared - mi.rawBytes.Load())
		out.xfer = mi.hdrBytes.Load() + mi.rawBytes.Load()
		out.finalURL = res.Request.URL.String()
		t.recordResponse(&rec, res, start, out.cacheState)
		return out, nil
	}
	const limit = int64(8 << 20)
	var data []byte
	buf := make([]byte, 32<<10)
	for int64(len(data)) < limit {
		n, rerr := res.Body.Read(buf)
		data = append(data, buf[:n]...)
		if o.maxBody > 0 && mi.rawBytes.Load() > o.maxBody {
			out.aborted = true
			data = nil
			break
		}
		if rerr != nil {
			break
		}
	}
	for _, sc := range res.Header.Values("Set-Cookie") {
		if o.anonymous {
			break
		}
		if c := parseCookie(sc, t.eng.now()); c != nil {
			if c.Domain == "" {
				c.Domain = res.Request.URL.Hostname()
			}
			t.Context().JarRef().Set(c)
		}
	}
	out.body = data
	out.xfer = mi.hdrBytes.Load() + mi.rawBytes.Load()
	out.finalURL = res.Request.URL.String()
	out.cacheState = mi.cacheState() // a redirect chain ends with the last hop's outcome
	t.recordResponse(&rec, res, start, out.cacheState)
	return out, nil
}

// recordResponse pushes the response event. cacheState ("hit", "revalidated", "miss: no-store", ...) is
// recorded on the request headers under x-sweef-cache so the network log shows what the cache did.
func (t *Tab) recordResponse(rec *fnet.RequestRecord, res *http.Response, start time.Time, cacheState string) {
	if cacheState != "" {
		h := make(map[string]string, len(rec.Headers)+1)
		for k, v := range rec.Headers {
			h[k] = v
		}
		h["x-sweef-cache"] = cacheState
		rec.Headers = h
	}
	rec.Status = res.StatusCode
	rec.FinalURL = res.Request.URL.String()
	rec.DurationMs = time.Since(start).Milliseconds()
	rec.ResponseHeaders = flattenHeaders(res.Header)
	t.netRing.Push(Event{Tab: t.ID, Kind: EvResponse, At: time.Now(), Data: *rec})
}

// httpForScript performs fetch/XHR on behalf of page code.
func (t *Tab) httpForScript(method, rawurl string, headers map[string]string, body string) (map[string]any, error) {
	return t.httpForScriptKind(KindXHR, method, rawurl, headers, body)
}

func (t *Tab) httpForScriptKind(kind DataKind, method, rawurl string, headers map[string]string, body string) (map[string]any, error) {
	r, err := t.httpDo(httpOpts{method: method, url: rawurl, headers: headers, body: body, kind: kind})
	if err != nil {
		return nil, err
	}
	hdr := map[string]any{}
	for k, v := range r.header {
		hdr[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	statusText := http.StatusText(r.status)
	if r.statusText != "" {
		statusText = r.statusText
	}
	out := map[string]any{"status": r.status, "statusText": statusText, "headers": hdr, "body": string(r.body), "url": r.finalURL}
	if !r.synth && !utf8.Valid(r.body) {
		// binary payload: the text form above is lossy, so also hand over the exact bytes
		out["b64"] = base64.StdEncoding.EncodeToString(r.body)
	}
	return out, nil
}
