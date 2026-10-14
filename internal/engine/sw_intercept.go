package engine

// sw_intercept.go: where a service worker meets the request path.
//
//   - Tab.fetch (navigations) asks swNavigation first: a URL inside the scope of an
//     activated registration is answered by the worker's fetch handler.
//   - Tab.httpDo (everything a document requests afterwards: fetch(), XHR, scripts,
//     stylesheets, images, fonts, ...) asks swFetch when the document is controlled.
//   - setDocument calls swDocumentCommitted, which records the document as a client of
//     the registration that controls it; closing or replacing it calls swDocumentGone.
//
// A request the worker does not respond to (no fetch listener, or respondWith never
// called) continues to the network unchanged.

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// tabSW is the service worker state of a tab.
type tabSW struct {
	ctl   atomic.Pointer[swControl] // the registration controlling the current (or, while it loads, the next) document
	rt    *swRuntime                // set for the hidden tab of a worker
	noNav bool                      // the hidden tab of a worker never navigates
	cmu   sync.Mutex
	cli   *swClient
}

// swControl says which registration controls a document and under which client id.
type swControl struct {
	reg      *swReg
	clientID string
}

func (s *tabSW) clientEntry() *swClient {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	return s.cli
}

// swFetchReq is a request handed to a worker's fetch handler.
type swFetchReq struct {
	URL         string      `json:"url"`
	Method      string      `json:"method"`
	Headers     [][2]string `json:"headers"`
	Body        string      `json:"body,omitempty"`
	Mode        string      `json:"mode"`
	Credentials string      `json:"credentials"`
	Destination string      `json:"destination"`
	ClientID    string      `json:"clientId"`
}

// swFetchResp is what a worker answered.
type swFetchResp struct {
	Status     int
	StatusText string
	Header     http.Header
	Body       []byte
	URL        string
}

func swDestination(k DataKind) string {
	switch k {
	case KindDocument:
		return "document"
	case KindScript:
		return "script"
	case KindStyle:
		return "style"
	case KindImage:
		return "image"
	case KindFont:
		return "font"
	case KindMedia:
		return "video"
	case KindFrame:
		return "iframe"
	}
	return ""
}

// fetch sends a request to the active worker of reg. handled=false means the worker did not respond.
func (m *swManager) fetch(reg *swReg, req swFetchReq) (resp *swFetchResp, handled bool, err error) {
	m.mu.Lock()
	w := reg.active
	m.mu.Unlock()
	if w == nil {
		return nil, false, nil
	}
	rt, err := m.runtimeFor(w)
	if err != nil {
		// a worker that cannot start (its script throws) is as good as none: the page goes to the network
		return nil, false, nil
	}
	res, err := rt.dispatch("fetch", req, swFetchTimeout)
	if err != nil {
		return nil, true, fmt.Errorf("service worker: %w", err)
	}
	if h, _ := res.meta["handled"].(bool); !h {
		return nil, false, nil
	}
	out := &swFetchResp{Status: int(argI([]any{res.meta["status"]}, 0)), StatusText: str(res.meta["statusText"]), Header: http.Header{}, Body: binStrToBytes(res.body), URL: str(res.meta["url"])}
	for _, p := range headerPairs(res.meta["headers"]) {
		out.Header.Add(p[0], p[1])
	}
	return out, true, nil
}

// swFetch routes a request of a controlled document to its worker.
func (t *Tab) swFetch(o httpOpts, abs string, kind DataKind) (*httpResp, bool, error) {
	ctl := t.sw.ctl.Load()
	if ctl == nil || t.sw.rt != nil {
		return nil, false, nil
	}
	req := swFetchReq{URL: abs, Method: o.method, Body: o.body, Credentials: "include", Destination: swDestination(kind), ClientID: ctl.clientID}
	if req.Method == "" {
		req.Method = "GET"
	}
	req.Mode = "no-cors"
	if kind == KindXHR {
		req.Mode = "cors"
	}
	for k, v := range o.headers {
		req.Headers = append(req.Headers, [2]string{k, v})
	}
	resp, handled, err := ctl.reg.m.fetch(ctl.reg, req)
	if !handled {
		return nil, false, nil
	}
	start := time.Now()
	if err != nil {
		return nil, true, fmt.Errorf("TypeError: Failed to fetch (%s)", err.Error())
	}
	final := abs
	if resp.URL != "" {
		final = resp.URL
	}
	t.netRing.Push(Event{Tab: t.ID, Kind: EvResponse, At: start, Data: swRecord(req.Method, abs, final, resp)})
	return &httpResp{status: resp.Status, header: resp.Header, body: resp.Body, finalURL: final, declared: -1, statusText: resp.StatusText}, true, nil
}

// swNavigation asks the worker for a document. The returned response is used as if it came from the network.
func (t *Tab) swNavigation(target string, headers map[string]string) (*http.Response, bool, error) {
	ctl := t.sw.ctl.Load()
	if ctl == nil || t.sw.rt != nil {
		return nil, false, nil
	}
	m := ctl.reg.m
	if m.lookup(t.keyForURL(target), target) != ctl.reg {
		return nil, false, nil
	}
	req := swFetchReq{URL: target, Method: "GET", Mode: "navigate", Credentials: "include", Destination: "document"}
	for k, v := range headers {
		req.Headers = append(req.Headers, [2]string{k, v})
	}
	resp, handled, err := m.fetch(ctl.reg, req)
	if !handled {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("net::ERR_FAILED (service worker: %v)", err)
	}
	u, _ := url.Parse(target)
	if resp.URL != "" {
		if ru, err := url.Parse(resp.URL); err == nil {
			u = ru
		}
	}
	h := resp.Header
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "text/html; charset=utf-8")
	}
	return &http.Response{
		StatusCode: resp.Status, Status: fmt.Sprintf("%d %s", resp.Status, resp.StatusText), Header: h,
		Body: io.NopCloser(bytes.NewReader(resp.Body)), ContentLength: int64(len(resp.Body)),
		Request: &http.Request{Method: "GET", URL: u},
	}, true, nil
}

func swRecord(method, reqURL, final string, resp *swFetchResp) map[string]any {
	hdr := map[string]string{"x-sweef-route": "service-worker"}
	return map[string]any{"method": method, "url": reqURL, "finalUrl": final, "status": resp.Status, "headers": hdr}
}

// ---- documents as clients -------------------------------------------------------------------------------

// swPrepareNavigation decides which registration will control the document a navigation is about to load, so that
// its early sub-resource requests (the preload scanner runs before the document is committed) already go through the worker.
func (t *Tab) swPrepareNavigation(target string) {
	if t.sw.rt != nil || t.eng.closed.Load() {
		return
	}
	if siteOriginOf(target) == "" {
		t.sw.ctl.Store(nil)
		return
	}
	m := t.Context().sw()
	reg := m.lookup(t.keyForURL(target), target)
	if reg == nil {
		t.sw.ctl.Store(nil)
		return
	}
	m.mu.Lock()
	has := reg.active != nil
	m.mu.Unlock()
	if !has {
		t.sw.ctl.Store(nil)
		return
	}
	t.sw.ctl.Store(&swControl{reg: reg, clientID: randHex(8)})
}

// swDocumentCommitted records the document that was just installed as a client.
func (t *Tab) swDocumentCommitted(finalURL string) {
	if t.sw.rt != nil || t.eng.closed.Load() {
		return
	}
	t.swDocumentGone()
	if siteOriginOf(finalURL) == "" {
		t.sw.ctl.Store(nil)
		return
	}
	m := t.Context().sw()
	key := t.keyForURL(finalURL)
	reg := m.lookup(key, finalURL)
	m.mu.Lock()
	if reg != nil && reg.active == nil {
		reg = nil
	}
	m.mu.Unlock()
	id := randHex(8)
	if ctl := t.sw.ctl.Load(); ctl != nil && ctl.reg == reg {
		id = ctl.clientID
	}
	c := &swClient{id: id, tab: t, url: finalURL, key: key, reg: reg}
	m.mu.Lock()
	m.nextC++
	c.seq = m.nextC
	m.clients[id] = c
	m.mu.Unlock()
	t.sw.cmu.Lock()
	t.sw.cli = c
	t.sw.cmu.Unlock()
	if reg != nil {
		t.sw.ctl.Store(&swControl{reg: reg, clientID: id})
	} else {
		t.sw.ctl.Store(nil)
	}
}

// swDocumentGone forgets the document of the tab (it is being replaced or the tab closed).
func (t *Tab) swDocumentGone() {
	t.sw.cmu.Lock()
	c := t.sw.cli
	t.sw.cli = nil
	t.sw.cmu.Unlock()
	if c == nil {
		return
	}
	m := t.Context().swIfStarted()
	if m == nil {
		return
	}
	m.mu.Lock()
	delete(m.clients, c.id)
	reg := c.reg
	m.mu.Unlock()
	if reg != nil {
		m.goJob(func() { m.afterClientGone(reg) })
	}
}
