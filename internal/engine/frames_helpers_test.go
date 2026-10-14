package engine

import (
	"fmt"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- helpers -------------------------------------------------------------------------

// site serves a set of pages; every httptest.Server has its own port, so two sites
// are two origins. A page body of the form "REDIRECT <url>" answers 302. "{{self}}"
// in a body is replaced by the site's own origin.
type site struct {
	*httptest.Server
	mu      sync.Mutex
	pages   map[string]string
	headers map[string]map[string]string // path -> extra response headers
	hits    []string
	reqHdr  map[string]http.Header // path -> headers of the latest request
}

func newSite(t *testing.T, pages map[string]string) *site {
	t.Helper()
	s := &site{pages: map[string]string{}, headers: map[string]map[string]string{}, reqHdr: map[string]http.Header{}}
	for k, v := range pages {
		s.pages[k] = v
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits = append(s.hits, r.URL.Path)
		s.reqHdr[r.URL.Path] = r.Header.Clone()
		hdr := s.headers[r.URL.Path]
		body, ok := s.pages[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		if strings.HasPrefix(body, "REDIRECT ") {
			http.Redirect(w, r, strings.TrimPrefix(body, "REDIRECT "), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, strings.ReplaceAll(body, "{{self}}", "http://"+r.Host))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *site) set(path, body string) {
	s.mu.Lock()
	s.pages[path] = body
	s.mu.Unlock()
}

func (s *site) setHeader(path, k, v string) {
	s.mu.Lock()
	if s.headers[path] == nil {
		s.headers[path] = map[string]string{}
	}
	s.headers[path][k] = v
	s.mu.Unlock()
}

func newEng(t *testing.T) *Engine {
	t.Helper()
	o := DefaultOptions()
	o.SettleMs = 600
	e := New(o)
	t.Cleanup(func() { e.Close() })
	return e
}

func px(img *image.RGBA, x, y int) [3]uint8 {
	o := img.PixOffset(x, y)
	return [3]uint8{img.Pix[o], img.Pix[o+1], img.Pix[o+2]}
}

func isColor(c [3]uint8, r, g, b uint8) bool {
	d := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	return d(c[0], r) < 6 && d(c[1], g) < 6 && d(c[2], b) < 6
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- painting ---------------------------------------------------------------------------

func TestIframePaintsIntoParent(t *testing.T) {
	child := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#f00"><p style="color:#fff">x</p>`})
	parent := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#fff">
<iframe src="` + child.URL + `/" style="border:0;position:absolute;left:20px;top:30px;width:100px;height:60px"></iframe></body>`})
	e := newEng(t)
	tab, err := e.NewTab(parent.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	img, ok := tab.PaintRGBA(300, 200, 1, false)
	if !ok {
		t.Fatal("no paint")
	}
	if c := px(img, 100, 85); !isColor(c, 255, 0, 0) {
		t.Fatalf("inside the frame: want red, got %v", c)
	}
	for _, p := range [][2]int{{10, 10}, {130, 60}, {60, 95}, {19, 60}} {
		if c := px(img, p[0], p[1]); !isColor(c, 255, 255, 255) {
			t.Fatalf("outside the frame at %v: want white, got %v", p, c)
		}
	}
}
