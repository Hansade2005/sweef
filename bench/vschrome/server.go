//go:build linux

package main

// server.go: the fixed local pages. Deterministic content (seeded generators), no compression, Cache-Control:
// no-store, served by an httptest-style plain net/http server on 127.0.0.1. Every accepted connection is wrapped in a
// byte counter, so "bytes on the wire" for the local pages is counted by something neither browser controls.

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type beacon struct {
	Page, Event, Run string
	At               time.Time
}

type fixtureServer struct {
	ln      net.Listener
	srv     *http.Server
	in, out atomic.Int64 // bytes read from / written to browser sockets
	conns   atomic.Int64
	reqs    atomic.Int64
	mu      sync.Mutex
	beacons []beacon
	hits    map[string]int
	notify  chan struct{}
	pages   map[string][]byte
	assets  map[string][]byte
	types   map[string]string
}

type countConn struct {
	net.Conn
	s *fixtureServer
}

func (c *countConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.s.in.Add(int64(n))
	return n, err
}
func (c *countConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.s.out.Add(int64(n))
	return n, err
}

type countListener struct {
	net.Listener
	s *fixtureServer
}

func (l *countListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.s.conns.Add(1)
	return &countConn{c, l.s}, nil
}

func newFixtureServer() (*fixtureServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &fixtureServer{ln: ln, notify: make(chan struct{}, 1024), hits: map[string]int{}, pages: map[string][]byte{}, assets: map[string][]byte{}, types: map[string]string{}}
	s.build()
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	s.srv = &http.Server{Handler: mux}
	go s.srv.Serve(&countListener{ln, s})
	return s, nil
}

func (s *fixtureServer) close()       { s.srv.Close() }
func (s *fixtureServer) addr() string { return s.ln.Addr().String() }

type wireSnap struct{ in, out, conns, reqs int64 }

func (s *fixtureServer) snap() wireSnap {
	return wireSnap{s.in.Load(), s.out.Load(), s.conns.Load(), s.reqs.Load()}
}
func (a wireSnap) sub(b wireSnap) wireSnap {
	return wireSnap{a.in - b.in, a.out - b.out, a.conns - b.conns, a.reqs - b.reqs}
}

func (s *fixtureServer) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/__b" {
		q := r.URL.Query()
		s.mu.Lock()
		s.beacons = append(s.beacons, beacon{q.Get("p"), q.Get("e"), q.Get("r"), time.Now()})
		s.mu.Unlock()
		select {
		case s.notify <- struct{}{}:
		default:
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "image/gif")
		w.Write([]byte("GIF89a\x01\x00\x01\x00\x80\x00\x00\x00\x00\x00\xff\xff\xff!\xf9\x04\x01\x00\x00\x00\x00,\x00\x00\x00\x00\x01\x00\x01\x00\x00\x02\x02D\x01\x00;"))
		return
	}
	s.reqs.Add(1)
	s.mu.Lock()
	s.hits[path]++
	s.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	if b, ok := s.pages[path]; ok {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
		return
	}
	if b, ok := s.assets[path]; ok {
		w.Header().Set("Content-Type", s.types[path])
		w.Write(b)
		return
	}
	http.NotFound(w, r)
}

// waitBeacon waits for the beacon (event e of run id r) and returns its arrival time.
func (s *fixtureServer) waitBeacon(r, e string, timeout time.Duration) (time.Time, bool) {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		for _, b := range s.beacons {
			if b.Run == r && b.Event == e {
				s.mu.Unlock()
				return b.At, true
			}
		}
		s.mu.Unlock()
		rem := time.Until(deadline)
		if rem <= 0 {
			return time.Time{}, false
		}
		select {
		case <-s.notify:
		case <-time.After(rem):
		}
	}
}

func (s *fixtureServer) beaconCount(r, e string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, b := range s.beacons {
		if b.Run == r && b.Event == e {
			n++
		}
	}
	return n
}

// ---- content ---------------------------------------------------------------------------------------------------------

const beaconJS = `<script>window.addEventListener('load',function(){var q=location.search.match(/r=([^&]+)/);var x=new XMLHttpRequest();x.open('GET','/__b?p=%s&e=load&r='+(q?q[1]:'')+'&t='+Math.random());x.send()})</script>`

func (s *fixtureServer) build() {
	rng := rand.New(rand.NewSource(20260101))
	words := strings.Fields("alpha beta gamma delta engine render paint layout browser window latency memory cache network packet socket frame pixel glyph font style sheet script object model event loop queue thread process kernel signal buffer stream filter module bundle vector matrix texture shader raster compose damage region cursor focus input output syntax parser token lexer symbol scope closure prototype promise async await fetch request response header cookie session storage origin policy sandbox")
	sentence := func() string {
		n := 8 + rng.Intn(10)
		var b strings.Builder
		for i := 0; i < n; i++ {
			w := words[rng.Intn(len(words))]
			if i == 0 {
				w = strings.ToUpper(w[:1]) + w[1:]
			}
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(w)
		}
		b.WriteByte('.')
		return b.String()
	}
	para := func() string {
		n := 3 + rng.Intn(4)
		var b strings.Builder
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(sentence())
		}
		return b.String()
	}

	// /article: a text article, ~100 KB, one stylesheet inline, a handful of links, no images.
	var a bytes.Buffer
	a.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Fixture article</title><style>
body{font:16px/1.55 Georgia,serif;color:#1a1a1a;background:#fff;margin:0}
header{background:#12355b;color:#fff;padding:18px 40px}
header h1{margin:0;font:700 28px/1.2 Arial,sans-serif}
main{max-width:760px;margin:0 auto;padding:24px 20px 80px}
h2{font:700 22px/1.3 Arial,sans-serif;margin:34px 0 8px;color:#12355b}
p{margin:0 0 14px}
a{color:#0b57d0}
ul{margin:0 0 14px 22px}
footer{border-top:1px solid #ddd;padding:20px 40px;color:#666;font:13px Arial}
</style></head><body><header><h1>The fixture article: notes on measuring browsers from the outside</h1></header><main>`)
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&a, "<h2 id=s%d>Section %d. %s</h2>\n", i, i, sentence())
		for j := 0; j < 3; j++ {
			fmt.Fprintf(&a, "<p>%s <a href=\"#s%d\">see section %d</a></p>\n", para(), (i%40)+1, (i%40)+1)
		}
		a.WriteString("<ul>")
		for j := 0; j < 4; j++ {
			fmt.Fprintf(&a, "<li>%s</li>", sentence())
		}
		a.WriteString("</ul>\n")
	}
	a.WriteString(`</main><footer>End of the fixture article.</footer>` + fmt.Sprintf(beaconJS, "article") + `</body></html>`)
	s.pages["/article"] = a.Bytes()

	// images (generated once; smooth gradients plus seeded noise so that they do not compress to nothing)
	mkImg := func(seed int64, w, h int) *image.RGBA {
		r := rand.New(rand.NewSource(seed))
		im := image.NewRGBA(image.Rect(0, 0, w, h))
		c0 := [3]int{r.Intn(200), r.Intn(200), r.Intn(200)}
		c1 := [3]int{55 + r.Intn(200), 55 + r.Intn(200), 55 + r.Intn(200)}
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				t := float64(x+y) / float64(w+h)
				n := r.Intn(24) - 12
				im.SetRGBA(x, y, color.RGBA{clamp8(int(float64(c0[0])*(1-t)+float64(c1[0])*t) + n), clamp8(int(float64(c0[1])*(1-t)+float64(c1[1])*t) + n), clamp8(int(float64(c0[2])*(1-t)+float64(c1[2])*t) + n), 255})
			}
		}
		return im
	}
	var heavy bytes.Buffer
	heavy.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>Fixture heavy page</title><link rel="stylesheet" href="/css/site.css"></head><body><header><h1>Fixture heavy page: images and scripts</h1></header><div id="grid">`)
	for i := 0; i < 12; i++ {
		var buf bytes.Buffer
		im := mkImg(int64(100+i), 480, 300)
		path := fmt.Sprintf("/img/p%d", i)
		if i%2 == 0 {
			jpeg.Encode(&buf, im, &jpeg.Options{Quality: 82})
			path += ".jpg"
			s.types[path] = "image/jpeg"
		} else {
			png.Encode(&buf, im)
			path += ".png"
			s.types[path] = "image/png"
		}
		s.assets[path] = buf.Bytes()
		fmt.Fprintf(&heavy, `<figure><img src="%s" width="240" height="150" alt="photo %d"><figcaption>%s</figcaption></figure>`, path, i, sentence())
	}
	heavy.WriteString(`</div><section id="out"></section><script src="/js/a.js"></script><script src="/js/b.js"></script><script src="/js/c.js"></script>` + fmt.Sprintf(beaconJS, "heavy") + `</body></html>`)
	s.pages["/heavy"] = heavy.Bytes()
	s.assets["/css/site.css"] = []byte(`body{margin:0;font:15px/1.5 Arial,sans-serif;background:#f4f6f8;color:#222}
header{background:#7a1f3d;color:#fff;padding:16px 28px}header h1{margin:0;font-size:24px}
#grid{display:flex;flex-wrap:wrap;gap:14px;padding:18px 28px}
figure{margin:0;background:#fff;border:1px solid #d8dde3;border-radius:6px;padding:8px;width:240px}
figure img{display:block;border-radius:4px}figcaption{padding-top:6px;font-size:13px;color:#555}
#out{padding:0 28px 40px}.card{background:#fff;border:1px solid #d8dde3;border-radius:6px;margin:6px 0;padding:8px 12px;display:flex;justify-content:space-between}
.card b{color:#7a1f3d}table{border-collapse:collapse}td{border:1px solid #ccd;padding:2px 8px;font-size:12px}
` + strings.Repeat("/* padding to a realistic stylesheet size */ .x{color:#123}\n", 200))
	s.types["/css/site.css"] = "text/css"
	s.assets["/js/a.js"] = []byte(`(function(){var n=0,c=[];var N=120000;var s=new Uint8Array(N+1);for(var i=2;i<=N;i++){if(!s[i]){n++;if(c.length<60)c.push(i);for(var j=i*2;j<=N;j+=i)s[j]=1}}
var t=document.createElement('table'),h='';for(var r=0;r<6;r++){h+='<tr>';for(var k=0;k<10;k++)h+='<td>'+c[r*10+k]+'</td>';h+='</tr>'}t.innerHTML=h;document.getElementById('out').appendChild(t);window.__primes=n})();`)
	s.assets["/js/b.js"] = []byte(`(function(){var o=document.getElementById('out'),f=document.createDocumentFragment();for(var i=0;i<400;i++){var d=document.createElement('div');d.className='card';d.innerHTML='<span>Item <b>'+i+'</b></span><span>'+(i*37%101)+' units</span>';f.appendChild(d)}o.appendChild(f)})();`)
	s.assets["/js/c.js"] = []byte(`(function(){var a=[];var x=12345;for(var i=0;i<30000;i++){x=(x*1103515245+12345)&0x7fffffff;a.push(x%100000)}a.sort(function(p,q){return p-q});var m={};for(var i=0;i<a.length;i++){var k='k'+(a[i]%997);m[k]=(m[k]||0)+1}var j=JSON.stringify(m);var back=JSON.parse(j);var s='';for(var i=0;i<3000;i++)s+=String.fromCharCode(97+i%26);window.__c=Object.keys(back).length+s.length})();`)
	for _, p := range []string{"/js/a.js", "/js/b.js", "/js/c.js"} {
		s.types[p] = "application/javascript"
	}

	// /spa: an app shell that renders everything client side from a JSON API, with hash routing.
	s.pages["/spa"] = []byte(`<!doctype html><html><head><meta charset="utf-8"><title>Fixture SPA</title><meta name="viewport" content="width=device-width"><link rel="stylesheet" href="/css/spa.css"></head><body><div id="app"><div class="boot">Loading</div></div><script src="/js/spa.js"></script>` + fmt.Sprintf(beaconJS, "spa") + `</body></html>`)
	s.assets["/css/spa.css"] = []byte(`body{margin:0;font:15px/1.5 system-ui,Arial,sans-serif;background:#eef2ee;color:#1d2b1d}
nav{background:#1f5f3a;color:#fff;padding:12px 24px;display:flex;gap:18px}nav a{color:#fff;text-decoration:none;font-weight:600}
.list{padding:16px 24px;display:grid;grid-template-columns:repeat(auto-fill,minmax(280px,1fr));gap:12px}
.item{background:#fff;border:1px solid #cfd8cf;border-radius:8px;padding:12px}.item h3{margin:0 0 4px;font-size:16px}.item p{margin:0;color:#4b5d4b;font-size:13px}
.tag{display:inline-block;background:#dcefe2;border-radius:10px;padding:0 8px;font-size:12px;margin-right:4px}.boot{padding:40px;font-size:20px}
`)
	s.types["/css/spa.css"] = "text/css"
	var items bytes.Buffer
	items.WriteString("[")
	for i := 0; i < 240; i++ {
		if i > 0 {
			items.WriteString(",")
		}
		fmt.Fprintf(&items, `{"id":%d,"title":%q,"body":%q,"tags":["t%d","t%d"]}`, i, "Item "+strconv.Itoa(i)+": "+sentence()[:30], para(), i%7, i%11)
	}
	items.WriteString("]")
	s.assets["/api/items"] = items.Bytes()
	s.types["/api/items"] = "application/json"
	// ~60 KB of "framework": a tiny virtual-DOM-ish renderer plus filler modules, so that parse cost is realistic
	var js bytes.Buffer
	js.WriteString(`"use strict";(function(){function h(t,p){var e=document.createElement(t);if(p)for(var k in p){if(k==='class')e.className=p[k];else if(k==='text')e.textContent=p[k];else e.setAttribute(k,p[k])}for(var i=2;i<arguments.length;i++){var c=arguments[i];if(c)e.appendChild(typeof c==='string'?document.createTextNode(c):c)}return e}
var state={items:[],route:'#/'};var mods={};
`)
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&js, "mods.m%d=function(x){var a=0;for(var i=0;i<x.length;i++){a=(a*31+x.charCodeAt(i)+%d)|0}return a};\n", i, i)
	}
	js.WriteString(`function view(){var app=document.getElementById('app');app.textContent='';var nav=h('nav',null,h('a',{href:'#/'},'Home'),h('a',{href:'#/odd'},'Odd'),h('a',{href:'#/even'},'Even'));var list=h('div',{class:'list'});
var sel=state.items.filter(function(it){return state.route==='#/odd'?it.id%2===1:state.route==='#/even'?it.id%2===0:true});
sel.forEach(function(it){list.appendChild(h('div',{class:'item'},h('h3',{text:it.title}),h('p',{text:it.body}),h('div',null,h('span',{class:'tag',text:it.tags[0]}),h('span',{class:'tag',text:it.tags[1]}))));mods.m7(it.title)});
app.appendChild(nav);app.appendChild(list)}
window.addEventListener('hashchange',function(){state.route=location.hash||'#/';view()});
var q=location.search.match(/r=([^&]+)/);
var x=new XMLHttpRequest();x.open('GET','/api/items?r='+(q?q[1]:''));x.onload=function(){state.items=JSON.parse(x.responseText);view();var y=new XMLHttpRequest();y.open('GET','/__b?p=spa&e=rendered&r='+(q?q[1]:'')+'&t='+Math.random());y.send()};x.send();})();`)
	s.assets["/js/spa.js"] = js.Bytes()
	s.types["/js/spa.js"] = "application/javascript"
}

func clamp8(v int) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

// hitsSince returns the per-path request counts accumulated since a snapshot taken with hitsSnap.
func (s *fixtureServer) hitsSnap() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.hits))
	for k, v := range s.hits {
		out[k] = v
	}
	return out
}

func diffHits(now, before map[string]int) map[string]int {
	out := map[string]int{}
	for k, v := range now {
		if d := v - before[k]; d > 0 {
			out[k] = d
		}
	}
	return out
}
