package engine

import (
	"bytes"
	"compress/gzip"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// brotliScript is `document.title='brotli-ok';/*` + 3.5 KB of filler + `*/`
// compressed with brotli (quality 9); 68 bytes on the wire.
const brotliScriptHex = "1bb60d00c4b649d37a9b2098ecd1b6bc100507a607dc44503891693220434e020ed3af4e8ef1b39c06ac263d98ef18af5457cae20deb022891041a575300805c3f11a903"

func brotliScriptSrc() string {
	return "document.title='brotli-ok';" + "/*" + strings.Repeat("sweef data saver brotli test ", 120) + "*/"
}

func gz(b []byte) []byte {
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

func serveEncodingPage(s *dsSite) {
	br, _ := hex.DecodeString(brotliScriptHex)
	src := brotliScriptSrc()
	page := `<!doctype html><html><head><title>enc</title></head><body><p id="t">encoded document text</p>
<script src="/br.js"></script><script src="/gz.js"></script></body></html>`
	s.mu.Lock()
	s.hook = func(w http.ResponseWriter, r *http.Request) bool {
		ae := r.Header.Get("Accept-Encoding")
		switch r.URL.Path {
		case "/enc":
			w.Header().Set("Content-Type", "text/html")
			if strings.Contains(ae, "gzip") {
				w.Header().Set("Content-Encoding", "gzip")
				w.Write(gz([]byte(page)))
				return true
			}
			fmt.Fprint(w, page)
		case "/br.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Header().Add("Vary", "Accept-Encoding")
			if strings.Contains(ae, "br") {
				w.Header().Set("Content-Encoding", "br")
				w.Write(br)
				return true
			}
			fmt.Fprint(w, src)
		case "/gz.js":
			w.Header().Set("Content-Type", "text/javascript")
			js := []byte("document.documentElement.setAttribute('data-gz','ok');")
			if strings.Contains(ae, "gzip") {
				w.Header().Set("Content-Encoding", "gzip")
				w.Write(gz(js))
				return true
			}
			w.Write(js)
		default:
			return false
		}
		return true
	}
	s.mu.Unlock()
}

func TestDataSaverHeadersAndEncodings(t *testing.T) {
	s := newDSSite(t)
	serveEncodingPage(s)
	e := newDSEngine(t)
	type res struct {
		in            int64
		brBytes       int64
		title, gzAttr string
		text          string
	}
	run := func(level SaverLevel) res {
		s.reset()
		c, tab := ctxTab(t, e, polPtr(level))
		if err := tab.Navigate(s.URL()+"/enc", nil); err != nil {
			t.Fatal(err)
		}
		var r res
		r.text, _ = tab.Text("#t")
		r.title = tab.Title()
		v, _ := tab.Evaluate(`document.documentElement.getAttribute('data-gz')`)
		r.gzAttr = string(v)
		r.in = c.DataStats().WireIn
		for _, rq := range s.requests() {
			sd := rq.Header.Get("Save-Data")
			ae := rq.Header.Get("Accept-Encoding")
			switch level {
			case SaverOff:
				if sd != "" || ae != "gzip" {
					t.Fatalf("off must look like before (Save-Data %q, Accept-Encoding %q) on %s", sd, ae, rq.Path)
				}
			default:
				if sd != "on" || ae != "br, gzip" {
					t.Fatalf("%s: Save-Data %q, Accept-Encoding %q on %s", level, sd, ae, rq.Path)
				}
			}
		}
		return r
	}
	off, std := run(SaverOff), run(SaverStandard)
	for name, r := range map[string]res{"off": off, "standard": std} {
		if r.text != "encoded document text" || r.title != "brotli-ok" || r.gzAttr != `"ok"` {
			t.Fatalf("%s: decoding failed: %+v", name, r)
		}
	}
	// the 3.5 KB script travels as 68 bytes of brotli instead of its identity form
	if std.in >= off.in-3000 {
		t.Fatalf("brotli should save about 3.4 KB here: off=%d standard=%d", off.in, std.in)
	}
}

func TestBrotliAndGzipDecodingThroughMeter(t *testing.T) {
	// the decoders behind Accept-Encoding, on their own: a truncated body is an error, not a hang or a panic
	br, _ := hex.DecodeString(brotliScriptHex)
	mi := &meterInfo{kind: KindScript}
	body := &meteredBody{rc: nopCloser{bytes.NewReader(br[:20])}, mi: mi, enc: "br"}
	buf := make([]byte, 8192)
	var err error
	for err == nil {
		_, err = body.Read(buf)
	}
	if err == nil || err.Error() == "" {
		t.Fatal("expected an error for a truncated brotli stream")
	}
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

// The counters sit under TLS: a real HTTPS + HTTP/2 exchange must show up at the
// socket with its handshake and framing, and equal what the server saw.
func TestWireCountersUnderTLSAndHTTP2(t *testing.T) {
	var sIn, sOut atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write(filler(100<<10, 3))
	}))
	srv.EnableHTTP2 = true
	srv.Listener = &srvListener{Listener: srv.Listener, in: &sIn, out: &sOut}
	srv.StartTLS()
	defer srv.Close()

	var w wireCounters
	tr := newTransport(&w)
	tr.TLSClientConfig = &tls.Config{RootCAs: x509Pool(srv)}
	cl := &http.Client{Transport: &meterRT{base: tr}}
	e := New(DefaultOptions())
	defer e.Close()
	c := e.DefaultContext()
	req, _ := http.NewRequest("GET", srv.URL+"/x", nil)
	req, mi := withMeter(req, c, nil, KindDocument, PolicyFor(SaverStandard))
	res, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.ProtoMajor != 2 {
		t.Fatalf("expected HTTP/2, got %s", res.Proto)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	time.Sleep(50 * time.Millisecond)
	if len(b) != 100<<10 {
		t.Fatalf("body %d", len(b))
	}
	if w.in.Load() < int64(len(b)) || w.in.Load() > int64(len(b))+8<<10 {
		t.Fatalf("wire in %d for a %d byte body: TLS and h2 overhead should be a few KB", w.in.Load(), len(b))
	}
	if w.in.Load() != sOut.Load() || w.out.Load() != sIn.Load() {
		t.Fatalf("client in/out %d/%d, server out/in %d/%d", w.in.Load(), w.out.Load(), sOut.Load(), sIn.Load())
	}
	if raw := mi.rawBytes.Load(); raw < int64(len(b)) {
		t.Fatalf("attributed body bytes %d", raw)
	}
	tr.CloseIdleConnections()
}

func x509Pool(srv *httptest.Server) *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(srv.Certificate())
	return p
}
