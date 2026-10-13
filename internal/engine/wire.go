package engine

// wire.go: exact socket accounting and per-request attribution.
//
// Two separate measurements exist and are never mixed:
//
//   - Wire counters (wireCounters): every net.Conn the HTTP transport dials is
//     wrapped, and each Read and Write adds to atomic counters. The connection
//     under TLS is the one wrapped, so the numbers are what crossed the socket
//     (TLS records and handshakes included, TCP/IP headers not). Each
//     BrowserContext has its own transport and so its own counters; every
//     context also feeds the engine total.
//   - Attribution (meterRT): a RoundTripper wrapper measures, per HTTP
//     exchange, the request header and body bytes written and the response
//     header bytes plus the raw (still compressed) body bytes read, and
//     charges them to a kind (script, image, ...) and an origin. Header sizes
//     are computed from the parsed headers, so they are close to, not equal
//     to, the bytes on the wire (HTTP/2 compresses headers; TLS adds framing).
//     That is why these numbers are labelled approximate everywhere.
//
// The wrapper also decodes Content-Encoding (gzip and, when the policy asks
// for it, brotli) itself: asking for br is only safe when the engine can
// decode it, and counting the compressed body bytes needs the raw stream.

import (
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dsnet/compress/brotli"
)

// wireCounters counts bytes read from and written to sockets. Counts also
// propagate to parent (the engine total).
type wireCounters struct {
	in, out atomic.Int64
	parent  *wireCounters
}

func (w *wireCounters) addIn(n int) {
	for c := w; c != nil; c = c.parent {
		c.in.Add(int64(n))
	}
}

func (w *wireCounters) addOut(n int) {
	for c := w; c != nil; c = c.parent {
		c.out.Add(int64(n))
	}
}

type countConn struct {
	net.Conn
	w *wireCounters
}

func (c *countConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.w.addIn(n)
	}
	return n, err
}

func (c *countConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.w.addOut(n)
	}
	return n, err
}

// newTransport builds the bounded transport used for every request. Dialing
// goes through a counting wrapper; ForceAttemptHTTP2 keeps HTTP/2 enabled,
// which a custom DialContext would otherwise turn off.
func newTransport(w *wireCounters) *http.Transport {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 4, // tiny boxes don't need Chromium's 256/host
		IdleConnTimeout:     60 * time.Second,
		ForceAttemptHTTP2:   true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &countConn{Conn: c, w: w}, nil
		},
	}
}

// ---- per-request attribution ------------------------------------------------

type meterKeyT struct{}

var meterKey meterKeyT

// meterInfo travels in the request context from the call site to meterRT.
type meterInfo struct {
	c    *BrowserContext
	t    *Tab // nil for context-level requests
	kind DataKind
	pol  DataPolicy

	// final hop, written by meterRT
	declared atomic.Int64 // Content-Length as received (transfer size), -1 unknown
	hdrBytes atomic.Int64
	rawBytes atomic.Int64
}

// meterRT measures every exchange that carries a meterInfo and decodes
// Content-Encoding. Requests without one pass through untouched.
type meterRT struct{ base http.RoundTripper }

func (m *meterRT) RoundTrip(req *http.Request) (*http.Response, error) {
	mi, _ := req.Context().Value(meterKey).(*meterInfo)
	if mi == nil {
		return m.base.RoundTrip(req)
	}
	// RoundTrippers must not modify the request: work on a shallow copy
	r2 := new(http.Request)
	*r2 = *req
	r2.Header = req.Header.Clone()
	if r2.Header == nil {
		r2.Header = http.Header{}
	}
	pol := mi.pol
	if pol.SaveDataHeader && r2.Header.Get("Save-Data") == "" {
		r2.Header.Set("Save-Data", "on")
	}
	// Taking charge of Accept-Encoding means decoding it ourselves. Range
	// requests stay as they are (the transport does not compress those).
	if r2.Header.Get("Accept-Encoding") == "" && r2.Header.Get("Range") == "" && r2.Method != http.MethodHead {
		if pol.Level != SaverOff {
			r2.Header.Set("Accept-Encoding", "br, gzip")
		} else {
			r2.Header.Set("Accept-Encoding", "gzip") // what net/http sends by itself
		}
	}
	out := requestBytes(r2)
	res, err := m.base.RoundTrip(r2)
	origin := r2.URL.Scheme + "://" + r2.URL.Host
	if err != nil {
		mi.charge(r2.URL.Scheme, origin, out, 0)
		return nil, err
	}
	hdr := responseHeaderBytes(res)
	mi.declared.Store(res.ContentLength)
	mi.hdrBytes.Store(hdr)
	mi.rawBytes.Store(0)
	body := &meteredBody{rc: res.Body, mi: mi, origin: origin, scheme: r2.URL.Scheme, out: out, hdr: hdr}
	if r2.Method != http.MethodHead && res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotModified {
		switch strings.ToLower(strings.TrimSpace(res.Header.Get("Content-Encoding"))) {
		case "gzip", "x-gzip":
			body.enc = "gzip"
		case "br":
			body.enc = "br"
		}
	}
	if body.enc != "" {
		res.Header.Del("Content-Encoding")
		res.Header.Del("Content-Length")
		res.ContentLength = -1
		res.Uncompressed = true
	}
	res.Body = body
	return res, nil
}

// charge books one exchange against the tab (if any) and the context.
func (mi *meterInfo) charge(scheme, origin string, out, in int64) {
	if mi.t != nil {
		mi.t.ds.acc.addTransfer(mi.kind, origin, out, in)
		mi.t.noteDataChanged()
	}
	if mi.c != nil {
		d := mi.c.data()
		d.acc.addTransfer(mi.kind, origin, out, in)
		if mi.t == nil {
			mi.c.noteDataChanged()
		}
	}
}

// meteredBody counts the raw bytes of a response body and decodes it.
type meteredBody struct {
	rc     io.ReadCloser
	mi     *meterInfo
	origin string
	scheme string
	out    int64
	hdr    int64
	raw    atomic.Int64
	enc    string

	once sync.Once
	dec  io.Reader
	derr error
	done sync.Once
}

type rawCounter struct{ b *meteredBody }

func (r rawCounter) Read(p []byte) (int, error) {
	n, err := r.b.rc.Read(p)
	if n > 0 {
		r.b.raw.Add(int64(n))
		r.b.mi.rawBytes.Store(r.b.raw.Load())
	}
	return n, err
}

func (b *meteredBody) Read(p []byte) (int, error) {
	if b.enc == "" {
		return rawCounter{b}.Read(p)
	}
	b.once.Do(func() {
		switch b.enc {
		case "gzip":
			b.dec, b.derr = gzip.NewReader(rawCounter{b})
		case "br":
			b.dec, b.derr = brotli.NewReader(rawCounter{b}, nil)
		}
	})
	if b.derr != nil {
		return 0, b.derr
	}
	return b.dec.Read(p)
}

func (b *meteredBody) Close() error {
	err := b.rc.Close()
	b.done.Do(func() {
		b.mi.charge(b.scheme, b.origin, b.out, b.hdr+b.raw.Load())
	})
	return err
}

// ---- header size estimates ----------------------------------------------------

type countW struct{ n int64 }

func (c *countW) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

// requestBytes estimates what the request puts on the wire as HTTP/1.1: the
// request line, the Host header, the other headers and the body.
func requestBytes(r *http.Request) int64 {
	var cw countW
	uri := r.URL.RequestURI()
	cw.n += int64(len(r.Method) + 1 + len(uri) + len(" HTTP/1.1\r\n"))
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	cw.n += int64(len("Host: ") + len(host) + 2)
	_ = r.Header.WriteSubset(&cw, map[string]bool{"Host": true, "Content-Length": true, "Transfer-Encoding": true})
	if r.ContentLength > 0 {
		cw.n += int64(len("Content-Length: ")+len(strconv.FormatInt(r.ContentLength, 10))+2) + r.ContentLength
	}
	cw.n += 2
	return cw.n
}

func responseHeaderBytes(res *http.Response) int64 {
	var cw countW
	st := res.Status
	if st == "" {
		st = strconv.Itoa(res.StatusCode) + " " + http.StatusText(res.StatusCode)
	}
	cw.n += int64(len("HTTP/1.1 ") + len(st) + 2)
	_ = res.Header.Write(&cw)
	cw.n += 2
	return cw.n
}

// withMeter attaches accounting to a request.
func withMeter(req *http.Request, c *BrowserContext, t *Tab, kind DataKind, pol DataPolicy) (*http.Request, *meterInfo) {
	mi := &meterInfo{c: c, t: t, kind: kind, pol: pol}
	mi.declared.Store(-1)
	return req.WithContext(context.WithValue(req.Context(), meterKey, mi)), mi
}
