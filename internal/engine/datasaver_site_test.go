package engine

// Test fixtures for the Data Saver: servers on 127.0.0.1 (the site) and
// 127.0.0.2 (ads, trackers, embeds: a different "site" as far as the engine is
// concerned) that serve a realistic heavy page and count, at the socket, what
// they really read and wrote.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type srvConn struct {
	net.Conn
	in, out *atomic.Int64
}

func (c *srvConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.in.Add(int64(n))
	return n, err
}

func (c *srvConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	c.out.Add(int64(n))
	return n, err
}

type srvListener struct {
	net.Listener
	in, out *atomic.Int64
}

func (l *srvListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &srvConn{Conn: c, in: l.in, out: l.out}, nil
}

type reqRec struct {
	Host, Path, Query string
	Header            http.Header
	At                time.Time
}

type dsSite struct {
	t            *testing.T
	first, third *httptest.Server
	// socket bytes seen by the servers (client writes = server reads)
	srvIn, srvOut atomic.Int64

	mu   sync.Mutex
	log  []reqRec
	hook func(w http.ResponseWriter, r *http.Request) bool // per-test overrides
}

func noisePNG(w, h int, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetRGBA(x, y, color.RGBA{uint8(rng.Intn(256)), uint8(rng.Intn(256)), uint8(rng.Intn(256)), 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return b.Bytes()
}

func filler(n int, seed int64) []byte {
	rng := rand.New(rand.NewSource(seed))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte('a' + rng.Intn(26))
	}
	return b
}

func listenOn(t *testing.T, host string) net.Listener {
	l, err := net.Listen("tcp", host+":0")
	if err != nil {
		t.Skipf("cannot listen on %s: %v", host, err)
	}
	return l
}

// newDSSite starts both servers. The handler serves the heavy page described
// in the file comment of datasaver_test.go.
func newDSSite(t *testing.T) *dsSite {
	s := &dsSite{t: t}
	hero := noisePNG(320, 200, 1)
	small := make([][]byte, 9)
	for i := range small {
		small[i] = noisePNG(160, 120, int64(10+i))
	}
	video := filler(3<<20, 99)
	ads := append([]byte("document.documentElement.setAttribute('data-ads','ran');/*"), append(filler(60<<10, 5), '*', '/')...)
	app := append([]byte("document.documentElement.setAttribute('data-app','ran');/*"), append(filler(20<<10, 6), '*', '/')...)

	mk := func(host string, h http.HandlerFunc) *httptest.Server {
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			s.log = append(s.log, reqRec{Host: r.Host, Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), At: time.Now()})
			hook := s.hook
			s.mu.Unlock()
			if hook != nil && hook(w, r) {
				return
			}
			h(w, r)
		}))
		srv.Listener = &srvListener{Listener: listenOn(t, host), in: &s.srvIn, out: &s.srvOut}
		srv.Start()
		t.Cleanup(srv.Close)
		return srv
	}
	s.third = mk("127.0.0.2", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ads.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(ads)
		case "/embed.html":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html><body>embedded ad</body></html>")
		default:
			http.NotFound(w, r)
		}
	})
	s.first = mk("127.0.0.1", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, s.heavyHTML())
		case r.URL.Path == "/style.css":
			w.Header().Set("Content-Type", "text/css")
			fmt.Fprint(w, `@font-face{font-family:"Brand";src:url(/font.woff2) format("woff2")}
body{font-family:"Brand",Arial,sans-serif;margin:8px}h1{font-size:28px}p{line-height:20px}`)
		case r.URL.Path == "/font.woff2":
			w.Header().Set("Content-Type", "font/woff2")
			w.Write(filler(40<<10, 7))
		case r.URL.Path == "/app.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(app)
		case r.URL.Path == "/img/hero.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(hero)
		case strings.HasPrefix(r.URL.Path, "/img/i") && strings.HasSuffix(r.URL.Path, ".png"):
			var n int
			fmt.Sscanf(r.URL.Path, "/img/i%d.png", &n)
			if n < 0 || n >= len(small) {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Write(small[n])
		case r.URL.Path == "/video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			http.ServeContent(w, r, "video.mp4", time.Time{}, bytes.NewReader(video))
		case r.URL.Path == "/next.html":
			fmt.Fprint(w, "<html><body>next</body></html>")
		default:
			http.NotFound(w, r)
		}
	})
	return s
}

func (s *dsSite) URL() string { return s.first.URL }

func (s *dsSite) thirdURL() string { return s.third.URL }

func (s *dsSite) heavyHTML() string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>Heavy article</title>
<link rel="stylesheet" href="/style.css">
<link rel="preload" href="/font.woff2" as="font" type="font/woff2" crossorigin>
<link rel="prefetch" href="/next.html">
<link rel="prerender" href="/next.html">
<link rel="dns-prefetch" href="//127.0.0.2">
<script src="/app.js" defer></script>
<script src="` + s.third.URL + `/ads.js" async></script>
</head><body>
<h1 id="h">Heavy article</h1>
<p id="intro">The quick brown fox jumps over the lazy dog. This paragraph is the text a person came to read.</p>
<img id="hero" src="/img/hero.png" width="320" height="200" alt="Hero image">
<video id="v" src="/video.mp4" autoplay muted preload="auto" width="320" height="180"></video>
<iframe id="ad" src="` + s.third.URL + `/embed.html" width="300" height="250"></iframe>
<p id="mid">Middle paragraph before the long scroll.</p>
<div style="height:1500px"></div>
`)
	for i := 1; i <= 8; i++ {
		fmt.Fprintf(&b, `<div class="card" style="height:700px"><img id="i%d" src="/img/i%d.png" width="160" height="120" alt="Photo %d"><p>Caption %d</p></div>`+"\n", i, i, i, i)
	}
	b.WriteString(`<p id="end">The end.</p></body></html>`)
	return b.String()
}

// count returns how many requests the site saw for path (any host).
func (s *dsSite) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.log {
		if r.Path == path {
			n++
		}
	}
	return n
}

func (s *dsSite) countPrefix(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.log {
		if strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

func (s *dsSite) requests() []reqRec {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]reqRec(nil), s.log...)
}

func (s *dsSite) reset() {
	s.mu.Lock()
	s.log = nil
	s.mu.Unlock()
}

func (s *dsSite) last(path string) (reqRec, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.log) - 1; i >= 0; i-- {
		if s.log[i].Path == path {
			return s.log[i], true
		}
	}
	return reqRec{}, false
}

// ctxTab opens a tab in a fresh context with the given policy (its own cache
// partition and its own socket counters).
func ctxTab(t *testing.T, e *Engine, pol *DataPolicy) (*BrowserContext, *Tab) {
	t.Helper()
	c := e.NewContext("", ContextOptions{})
	if pol != nil {
		c.SetDataPolicy(*pol)
	}
	tab, err := c.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	return c, tab
}

func newDSEngine(t *testing.T) *Engine {
	t.Helper()
	o := DefaultOptions()
	o.SettleMs = 300
	e := New(o)
	t.Cleanup(func() { e.Close() })
	return e
}

func polPtr(l SaverLevel) *DataPolicy { p := PolicyFor(l); return &p }
