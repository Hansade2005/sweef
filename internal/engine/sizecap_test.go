package engine

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// trustServer makes both transports of the context accept the test server's certificate.
func trustServer(c *BrowserContext, srv *httptest.Server) {
	m := c.CountedTransport().(*cacheRT).base.(*meterRT)
	for _, rt := range []http.RoundTripper{m.base, m.capped} {
		rt.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: x509Pool(srv)}
	}
}

func bigImageServer(t *testing.T, size int) (*httptest.Server, *atomic.Int32) {
	var bigReqs atomic.Int32
	body := filler(size, 7)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><img src="/big.png" width=100 height=100></body></html>`)
		case "/big.png":
			bigReqs.Add(1)
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "max-age=3600")
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Write(body)
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, &bigReqs
}

func loadAndPaint(t *testing.T, tab *Tab, url string) {
	t.Helper()
	if err := tab.Navigate(url, nil); err != nil {
		t.Fatal(err)
	}
	tab.Settle(300 * time.Millisecond)
	_, _ = tab.PaintRGBA(1280, 720, 1, false)
	tab.Settle(300 * time.Millisecond)
}

// Over HTTP/2 a refused response used to cost its whole body: the server had been allowed a 4 MiB window before
// the stream was reset. The capped transport bounds that to its small window.
func TestSizeCapOverHTTP2CostsAtMostOneSmallWindow(t *testing.T) {
	srv, _ := bigImageServer(t, 1400<<10)
	e := newDSEngine(t)
	c := e.NewContext("", ContextOptions{})
	c.SetDataPolicy(PolicyFor(SaverStandard))
	trustServer(c, srv)
	tab, _ := c.NewTab("about:blank")
	loadAndPaint(t, tab, srv.URL+"/")
	st := c.DataStats()
	if st.Skipped[KindImage] != 1 || st.AbortedBytes < 1400<<10 {
		t.Fatalf("the image must be refused: %+v", st)
	}
	t.Logf("refused 1.4 MB image: wire in %d, aborted %d", st.WireIn, st.AbortedBytes)
	if st.WireIn > 250<<10 {
		t.Fatalf("a refused 1.4 MB image cost %d bytes on the wire over HTTP/2 (window 64 KiB expected)", st.WireIn)
	}
}

// The refusal is remembered with the response's freshness: the next visit does not request the image at all.
func TestRefusedImageIsNotRequestedAgainWhileFresh(t *testing.T) {
	srv, reqs := bigImageServer(t, 600<<10)
	e := newDSEngine(t)
	c := e.NewContext("", ContextOptions{})
	c.SetDataPolicy(PolicyFor(SaverStandard))
	trustServer(c, srv)
	tab, _ := c.NewTab("about:blank")
	loadAndPaint(t, tab, srv.URL+"/")
	first := c.DataStats()
	loadAndPaint(t, tab, srv.URL+"/")
	if reqs.Load() != 1 {
		t.Fatalf("the image was requested %d times, want 1", reqs.Load())
	}
	st := c.DataStats()
	if st.Skipped[KindImage] != 2 {
		t.Fatalf("the image is still shown as skipped: %+v", st.Skipped)
	}
	if second := st.WireIn - first.WireIn; second > 20<<10 {
		t.Fatalf("second visit cost %d bytes, want just the document", second)
	}
	// tap to load ignores the refusal and gets the real thing
	if _, err := tab.LoadSkippedImages(); err != nil {
		t.Fatal(err)
	}
	tab.Settle(300 * time.Millisecond)
	if reqs.Load() != 2 {
		t.Fatalf("tap to load must fetch the image: %d requests", reqs.Load())
	}
}
