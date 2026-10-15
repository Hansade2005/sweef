package engine

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Tests of the page-load path: bare window methods, about:blank, detached
// images, duplicate requests, progressive loading.

func TestBareWindowMethodsAreCallableAsGlobals(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body><script>
var log = [];
function tryit(name, f) { try { var r = f(); log.push(name + ':ok'); return r; } catch (e) { log.push(name + ':' + e.name + ':' + e.message); } }
var loaded = 0, custom = 0;
tryit('addEventListener', function () { addEventListener('load', function () { loaded++; }); });
tryit('addEventListener-opts', function () { addEventListener('load', function () { loaded += 10; }, { once: true }); });
var h = function () { custom++; };
tryit('addEventListener-custom', function () { addEventListener('custom', h); });
tryit('dispatchEvent', function () { return dispatchEvent(new Event('custom')); });
tryit('removeEventListener', function () { removeEventListener('custom', h); });
tryit('dispatchEvent-removed', function () { dispatchEvent(new Event('custom')); });
tryit('setTimeout', function () { setTimeout(function () { window.timerRan = true; }, 0); });
tryit('clearTimeout', function () { clearTimeout(setTimeout(function () {}, 1000)); });
tryit('setInterval', function () { clearInterval(setInterval(function () {}, 1000)); });
tryit('requestAnimationFrame', function () { cancelAnimationFrame(requestAnimationFrame(function () {})); });
tryit('queueMicrotask', function () { queueMicrotask(function () { window.microRan = true; }); });
tryit('getComputedStyle', function () { return getComputedStyle(document.body); });
tryit('matchMedia', function () { return matchMedia('(min-width: 1px)').matches; });
tryit('postMessage', function () { postMessage('x', '*'); });
tryit('scrollTo', function () { scrollTo(0, 0); });
tryit('scrollBy', function () { scrollBy(0, 0); });
tryit('atob', function () { return atob(btoa('x')); });
tryit('fetch', function () { return typeof fetch; });
tryit('structuredClone', function () { return structuredClone({ a: 1 }); });
tryit('alert', function () { alert('x'); });
tryit('getSelection', function () { return getSelection(); });
tryit('focus', function () { focus(); blur(); });
tryit('open', function () { return typeof open; });
tryit('requestIdleCallback', function () { return requestIdleCallback(function () {}); });
tryit('reportError', function () { return typeof reportError; });
</script></body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newJSTab(t, &httptest.Server{URL: srv.URL}, "/")
	tab.Settle(200 * time.Millisecond)
	got := mustEval(t, tab, `log.join('\n')`)
	var bad []string
	for _, l := range strings.Split(strings.Trim(got, `"`), `\n`) {
		if !strings.HasSuffix(l, ":ok") {
			bad = append(bad, l)
		}
	}
	if len(bad) > 0 {
		t.Fatalf("bare window methods that threw:\n%s", strings.Join(bad, "\n"))
	}
	if v := mustEval(t, tab, `loaded`); v != "11" {
		t.Fatalf("load listeners registered through the bare global: loaded=%s", v)
	}
	if v := mustEval(t, tab, `custom`); v != "1" {
		t.Fatalf("dispatchEvent/removeEventListener as globals: custom=%s", v)
	}
	if v := mustEval(t, tab, `[window.timerRan, window.microRan].join()`); v != `"true,true"` {
		t.Fatalf("timers/microtasks: %s", v)
	}
}

var _ = sync.Mutex{}
var _ atomic.Int32

func TestDetachedImageIsRequestedAndFiresLoad(t *testing.T) {
	var hits atomic.Int32
	png := tinyPNG()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body><script>
window.ev = [];
var a = new Image();
a.onload = function () { ev.push('a-load:' + a.naturalWidth + 'x' + a.naturalHeight + ':' + a.complete); };
a.onerror = function () { ev.push('a-error'); };
a.src = '/px.png?a=1';
var b = new Image();
b.addEventListener('error', function () { ev.push('b-error'); });
b.addEventListener('load', function () { ev.push('b-load'); });
b.src = '/missing.png';
var c = document.createElement('img');
c.onload = function () { ev.push('c-load'); };
c.src = '/px.png?c=1';
new Image().src = '/beacon?x=1';
</script></body></html>`)
	})
	mux.HandleFunc("/px.png", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	})
	mux.HandleFunc("/beacon", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(100)
		w.Header().Set("Content-Type", "image/gif")
		w.Write(png)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newJSTab(t, &httptest.Server{URL: srv.URL}, "/")
	deadline := time.Now().Add(5 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		tab.Settle(100 * time.Millisecond)
		got = mustEval(t, tab, `ev.slice().sort().join(',')`)
		if strings.Count(got, ",") >= 2 {
			break
		}
	}
	if got != `"a-load:2x3:true,b-error,c-load"` {
		t.Fatalf("events: %s\nconsole:\n%s", got, consoleText(tab))
	}
	if h := hits.Load(); h != 102 {
		t.Fatalf("requests: want 2 images and 1 beacon (2+100), got %d", h)
	}
}

// tinyPNG is a 2x3 image.
func tinyPNG() []byte {
	im := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 2; x++ {
			im.Set(x, y, color.RGBA{R: 200, A: 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return b.Bytes()
}

// countingSite serves a page with stylesheets, scripts and images that must not
// be stored (Cache-Control: no-store, like the benchmark fixtures), and counts
// requests per path.
type countingSite struct {
	srv  *httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newCountingSite(t *testing.T, page string) *countingSite {
	t.Helper()
	s := &countingSite{hits: map[string]int{}}
	png := tinyPNG()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.count(r.URL.Path)
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, page)
	})
	for _, p := range []string{"/a.css", "/b.css"} {
		p := p
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			s.count(p)
			w.Header().Set("Content-Type", "text/css")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprint(w, "body{margin:0}.x{background:#123;height:20px}")
		})
	}
	for _, p := range []string{"/one.js", "/two.js", "/mod.js"} {
		p := p
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			s.count(p)
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("Cache-Control", "no-store")
			fmt.Fprintf(w, "window.ran=(window.ran||'')+'%s;';", p)
		})
	}
	for _, p := range []string{"/i1.png", "/i2.png", "/bg.png"} {
		p := p
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			s.count(p)
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(png)
		})
	}
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *countingSite) count(p string) { s.mu.Lock(); s.hits[p]++; s.mu.Unlock() }

func (s *countingSite) snapshot() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for k, v := range s.hits {
		out[k] = v
	}
	return out
}

const dupPage = `<!doctype html><html><head><link rel="stylesheet" href="/a.css"><link rel="stylesheet" href="/b.css">
<script src="/one.js"></script></head><body style="background:url(/bg.png)"><div class="x"></div>
<img src="/i1.png"><img src="/i2.png"><img src="/i1.png">
<script src="/two.js" defer></script><script type="module" src="/mod.js"></script>
<script>document.title = 'dup ' + (window.ran || '');</script></body></html>`

func wantOnce(t *testing.T, got map[string]int, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if got[p] != 1 {
			t.Errorf("%s was requested %d times, want exactly 1 (all requests: %v)", p, got[p], got)
		}
	}
}

// Every stylesheet, script and image of a page is requested once per navigation,
// however many consumers ask for it (prefetch, loader, every relayout).
func TestSubresourcesRequestedOncePerNavigation(t *testing.T) {
	for _, mode := range []string{"navigate", "progressive"} {
		t.Run(mode, func(t *testing.T) {
			s := newCountingSite(t, dupPage)
			o := DefaultOptions()
			o.SettleMs = 300
			o.CacheDir = "off"
			e := New(o)
			defer e.Close()
			tab, err := e.NewTab("about:blank")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "navigate" {
				if err := tab.Navigate(s.srv.URL+"/", nil); err != nil {
					t.Fatal(err)
				}
			} else {
				tab.SetAsyncImages(true)
				if err := tab.NavigateProgressive(s.srv.URL+"/", nil); err != nil {
					t.Fatal(err)
				}
				tab.PaintRGBA(800, 600, 1, false)
				tab.LoadFinish()
				time.Sleep(300 * time.Millisecond) // images arrive after the first layout
				tab.PaintRGBA(800, 600, 1, false)
			}
			// a relayout (viewport change) must not fetch anything again
			tab.SetViewport(900, 700, 1, false, false)
			tab.PaintRGBA(900, 700, 1, false)
			wantOnce(t, s.snapshot(), "/", "/a.css", "/b.css", "/one.js", "/two.js", "/mod.js", "/i1.png", "/i2.png", "/bg.png")
			if v := mustEval(t, tab, `window.ran`); !strings.Contains(v, "/one.js;") || !strings.Contains(v, "/two.js;") {
				t.Fatalf("scripts did not run: %s", v)
			}
		})
	}
}

func TestAboutBlankLoadsAnEmptyDocument(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	e := New(DefaultOptions())
	defer e.Close()
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	if err := tab.Navigate("about:blank", nil); err != nil {
		t.Fatalf("about:blank must load, not fail: %v", err)
	}
	if got := tab.URL(); got != "about:blank" {
		t.Fatalf("url %q", got)
	}
	if hits.Load() != before {
		t.Fatal("about:blank made a request")
	}
	if txt, _ := tab.Text("body"); strings.TrimSpace(txt) != "" {
		t.Fatalf("about:blank is not empty: %q", txt)
	}
	for _, u := range []string{"about:blank#x", "ABOUT:blank"} {
		if err := tab.Navigate(u, nil); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
}

func TestDetachedImageHonoursTheDataSaver(t *testing.T) {
	png := tinyPNG()
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><script>window.ev=[];var i=new Image();i.onload=function(){ev.push('load')};i.onerror=function(){ev.push('error')};i.src='/p.png';</script>`)
	})
	mux.HandleFunc("/p.png", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for _, tc := range []struct {
		level    SaverLevel
		ev       string
		requests int32
	}{{SaverOff, "load", 1}, {SaverStrict, "error", 0}} {
		t.Run(tc.level.String(), func(t *testing.T) {
			hits.Store(0)
			e := newDSEngine(t)
			c, tab := ctxTab(t, e, polPtr(tc.level))
			nav(t, tab, srv.URL+"/")
			deadline := time.Now().Add(3 * time.Second)
			var got string
			for time.Now().Before(deadline) && (got == "" || got == `""`) {
				tab.Settle(50 * time.Millisecond)
				got = mustEval(t, tab, `ev.join(',')`)
			}
			if got != `"`+tc.ev+`"` {
				t.Fatalf("events %s, want %s", got, tc.ev)
			}
			if hits.Load() != tc.requests {
				t.Fatalf("requests %d, want %d", hits.Load(), tc.requests)
			}
			st := tab.DataStats()
			if tc.requests > 0 && st.ByKind[KindImage] == 0 {
				t.Fatalf("the image's bytes are not counted: %+v", st)
			}
			if tc.requests == 0 && (st.ByKind[KindImage] != 0 || st.SkippedKnownBytes+st.SkippedEstimate == 0) {
				t.Fatalf("a withheld image must count as skipped, not as received: %+v", st)
			}
			_ = c
		})
	}
}

// slowSite serves a page whose stylesheet and scripts take a configurable time.
func slowSite(t *testing.T, cssDelay, jsDelay time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><head><title>Slow</title><link rel="stylesheet" href="/s.css">
<script src="/slow.js"></script><script src="/defer.js" defer></script></head>
<body><div id="box" class="box">hello</div><script>window.inline = (window.slowRan ? 'after-slow' : 'before-slow');</script>
<script>setTimeout(function () { window.timerRan = true; }, 120);</script></body></html>`)
	})
	mux.HandleFunc("/s.css", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(cssDelay)
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, ".box{display:block;width:123px;height:45px;background:#0a0}")
	})
	mux.HandleFunc("/slow.js", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(jsDelay)
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "window.slowRan = true;")
	})
	mux.HandleFunc("/defer.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "window.deferRan = (window.inline || '') ;")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTabForLoad(t *testing.T) *Tab {
	t.Helper()
	o := DefaultOptions()
	o.SettleMs = 300
	o.CacheDir = "off"
	e := New(o)
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

// NavigateProgressive returns when a first frame can be painted: the document is parsed and the stylesheet is in, a
// slow blocking script is still downloading. Navigate (headless) returns only when the page has settled.
func TestNavigateProgressiveDoesNotWaitForScripts(t *testing.T) {
	srv := slowSite(t, 150*time.Millisecond, 900*time.Millisecond)
	tab := newTabForLoad(t)
	t0 := time.Now()
	if err := tab.NavigateProgressive(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	ready := time.Since(t0)
	if ready < 140*time.Millisecond {
		t.Errorf("returned in %v, before the render-blocking stylesheet (150ms) was in", ready)
	}
	if ready > 700*time.Millisecond {
		t.Fatalf("NavigateProgressive took %v: it waited for the 900ms script", ready)
	}
	if !tab.LoadPending() || tab.LoadLoaded() {
		t.Fatal("the load must still be pending after the first frame")
	}
	// the first frame has the stylesheet applied and no script ran yet
	if r, ok := tab.PaintRGBA(300, 120, 1, false); !ok || r.RGBAAt(10, 10).G < 150 || r.RGBAAt(10, 10).R > 60 {
		t.Fatalf("first frame does not show the styled box: %v", r.RGBAAt(10, 10))
	}
	if v := mustEval(t, tab, `String(window.slowRan)`); v != `"undefined"` {
		t.Fatalf("the slow blocking script ran before the first frame: %s", v)
	}
	// drive the rest in slices, as a window does
	steps := 0
	for tab.LoadPending() {
		more, wait := tab.LoadStep(10 * time.Millisecond)
		steps++
		if !more {
			break
		}
		if wait > 0 {
			time.Sleep(wait)
		}
	}
	if steps < 2 {
		t.Errorf("the load finished in %d step(s): it is not sliced", steps)
	}
	if v := mustEval(t, tab, `[window.slowRan, window.inline, window.deferRan, window.timerRan].join()`); v != `"true,after-slow,after-slow,true"` {
		t.Fatalf("scripts did not run in order to completion: %s", v)
	}
	if !tab.LoadLoaded() || tab.LoadPending() {
		t.Fatal("not done")
	}

	// the headless path waits for everything
	tab2 := newTabForLoad(t)
	t0 = time.Now()
	if err := tab2.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	if time.Since(t0) < 850*time.Millisecond {
		t.Fatalf("Navigate returned after %v, before the slow script ran", time.Since(t0))
	}
	if v := mustEval(t, tab2, `[window.slowRan, window.inline, window.deferRan, window.timerRan].join()`); v != `"true,after-slow,after-slow,true"` {
		t.Fatalf("headless: %s", v)
	}
	if tab2.LoadPending() {
		t.Fatal("Navigate left work pending")
	}
}

// The document a progressive load ends with is the one a blocking load produces.
func TestProgressiveAndBlockingLoadsEndInTheSameDocument(t *testing.T) {
	srv := testServer(t)
	for _, path := range []string{"/inline", "/order", "/dynamic", "/jquery", "/module", "/storage", "/fetch", "/react"} {
		t.Run(strings.TrimPrefix(path, "/"), func(t *testing.T) {
			blocking := newTabForLoad(t)
			if err := blocking.Navigate(srv.URL+path, nil); err != nil {
				t.Fatal(err)
			}
			prog := newTabForLoad(t)
			prog.SetAsyncImages(true)
			if err := prog.NavigateProgressive(srv.URL+path, nil); err != nil {
				t.Fatal(err)
			}
			prog.LoadFinish()
			a, b := blocking.HTML(true), prog.HTML(true)
			if a != b {
				t.Fatalf("documents differ.\nblocking: %.600s\nprogressive: %.600s\nconsole: %s", a, b, consoleText(prog))
			}
			if blocking.Title() != prog.Title() {
				t.Fatalf("titles %q vs %q", blocking.Title(), prog.Title())
			}
		})
	}
}

// Timers are not run ahead of the page's own scripts while the load is pending, and a pending timer is reported
// as a wait instead of being slept on.
func TestLoadStepReportsWaitsAndKeepsOrder(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><script>window.log=[];setTimeout(function(){log.push('timer0')},0);setTimeout(function(){log.push('timer200')},200);</script>
<script src="/late.js" defer></script><script>log.push('inline');</script>`)
	})
	mux.HandleFunc("/late.js", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "log.push('defer');")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newTabForLoad(t)
	if err := tab.NavigateProgressive(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	tab.Settle(50 * time.Millisecond) // a post-action pump before DOMContentLoaded must not fire timers early
	if v := mustEval(t, tab, `log.join()`); v != `"inline"` {
		t.Fatalf("before DOMContentLoaded: %s", v)
	}
	sawWait := false
	for {
		more, wait := tab.LoadStep(5 * time.Millisecond)
		if wait > 20*time.Millisecond {
			sawWait = true
		}
		if !more {
			break
		}
		time.Sleep(wait)
	}
	if !sawWait {
		t.Error("no wait was reported for the 200ms timer")
	}
	if v := mustEval(t, tab, `log.join()`); v != `"inline,defer,timer0,timer200"` {
		t.Fatalf("order: %s", v)
	}
}

// A page that navigates itself while it loads progressively ends on the new page, with its own load pending.
func TestProgressivePageInitiatedNavigation(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>One</title><body><script>setTimeout(function(){ location.href = '/two'; }, 10);</script>`)
	})
	mux.HandleFunc("/two", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><title>Two</title><body><script>window.two = 1;</script>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newTabForLoad(t)
	tab.SetProgressiveNav(true)
	if err := tab.NavigateProgressive(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	tab.LoadFinish()
	if tab.Title() != "Two" || !strings.HasSuffix(tab.URL(), "/two") {
		t.Fatalf("ended on %q %q", tab.URL(), tab.Title())
	}
	if v := mustEval(t, tab, `String(window.two)`); v != `"1"` {
		t.Fatalf("the second page's scripts did not run: %s", v)
	}
}

// Indexing a live collection must not rebuild it every time: jQuery's find('*') loops over
// getElementsByTagName('*'), which took seconds (and hit the script timeout) on go.dev. And it must stay live.
func TestLiveCollectionsAreLinearAndStillLive(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		var sb strings.Builder
		sb.WriteString("<!doctype html><body><ul id=u>")
		for i := 0; i < 4000; i++ {
			fmt.Fprintf(&sb, "<li class=c%d>%d</li>", i%7, i)
		}
		sb.WriteString("</ul>")
		fmt.Fprint(&sb, `<script>
var t0 = Date.now(), n = 0;
var all = document.getElementsByTagName('*');
for (var i = 0; i < all.length; i++) { if (all[i].nodeType === 1) n++; }
var kids = document.getElementById('u').childNodes, m = 0;
for (var j = 0; j < kids.length; j++) { if (kids[j].nodeType === 1) m++; }
window.walk = { n: n, m: m, ms: Date.now() - t0 };
var live = document.getElementsByClassName('c3'), before = live.length;
document.getElementById('u').appendChild(Object.assign(document.createElement('li'), { className: 'c3' }));
var afterAdd = live.length;
document.getElementById('u').removeChild(document.getElementById('u').lastChild);
window.liveness = [before, afterAdd, live.length].join();
var ch = document.getElementById('u').children; var c0 = ch.length;
document.getElementById('u').firstChild.remove();
window.kidsLive = [c0, ch.length].join();
</script>`)
		w.Write([]byte(sb.String()))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newTabForLoad(t)
	t0 := time.Now()
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 4*time.Second {
		t.Fatalf("load took %v", d)
	}
	if v := mustEval(t, tab, `window.walk.n + ',' + window.walk.m`); v != `"4005,4000"` {
		t.Fatalf("walk counts %s (console: %s)", v, consoleText(tab))
	}
	if ms := mustEval(t, tab, `window.walk.ms`); len(ms) > 4 { // 5+ digits: >= 10 s
		t.Fatalf("walking the collections took %sms", ms)
	}
	if v := mustEval(t, tab, `window.liveness`); v != `"571,572,571"` {
		t.Fatalf("getElementsByClassName is not live: %s", v)
	}
	if v := mustEval(t, tab, `window.kidsLive`); v != `"4000,3999"` {
		t.Fatalf("children is not live: %s", v)
	}
}

// Relative imports of a module script resolve against that script's URL (they used to resolve against the
// document, so MDN's and GitHub's module graphs failed with 404s), and bare specifiers go through the import map.
func TestModuleImportsResolveAgainstTheImportingScript(t *testing.T) {
	mux := http.NewServeMux()
	js := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprint(w, body)
		})
	}
	mux.HandleFunc("/docs/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><p id="out">-</p>
<script type="importmap">{"imports":{"lib":"/vendor/lib.js","util/":"/vendor/util/"}}</script>
<script type="module" src="/static/app/main.js"></script>
<script type="module">import { who } from './inline-dep.js'; window.inlineWho = who;</script>`)
	})
	js("/static/app/main.js", `import { a } from './dep.js'; import { b } from '../shared/b.js'; import { lib } from 'lib'; import { u } from 'util/x.js';
document.getElementById('out').textContent = [a, b, lib, u].join(',');
import('./late.js').then(function (m) { window.late = m.late; });`)
	js("/static/app/dep.js", `export const a = 'a';`)
	js("/static/shared/b.js", `export const b = 'b';`)
	js("/static/app/late.js", `export const late = 'late';`)
	js("/vendor/lib.js", `export const lib = 'lib';`)
	js("/vendor/util/x.js", `export const u = 'u';`)
	js("/docs/inline-dep.js", `export const who = 'inline';`)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newTabForLoad(t)
	if err := tab.Navigate(srv.URL+"/docs/page", nil); err != nil {
		t.Fatal(err)
	}
	tab.Settle(100 * time.Millisecond)
	if v := mustEval(t, tab, `document.getElementById('out').textContent + '|' + window.inlineWho + '|' + window.late`); v != `"a,b,lib,u|inline|late"` {
		t.Fatalf("modules: %s\nconsole:\n%s", v, consoleText(tab))
	}
}

// innerHTML on a <template> (every template-based framework) and on non-element nodes must parse, not throw.
func TestTemplateInnerHTML(t *testing.T) {
	e := New(Options{CacheDir: "off"})
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetDocument(`<html><body></body></html>`, "http://x/")
	got := mustEval(t, tab, `(function () {
  var t = document.createElement('template'); t.innerHTML = '<b>x</b> &amp; y';
  var f = document.createDocumentFragment(); var d = document.createElement('div'); f.appendChild(d); d.innerHTML = '<i>z</i>';
  return t.innerHTML + '|' + t.content.textContent + '|' + d.innerHTML;
})()`)
	var s string
	_ = json.Unmarshal([]byte(got), &s)
	if s != "<b>x</b> &amp; y|x & y|<i>z</i>" {
		t.Fatalf("got %q", s)
	}
}

// A module script replays cached bytecode on the next visit and behaves the same.
func TestModuleBytecodeCache(t *testing.T) {
	big := "export const x = '" + strings.Repeat("y", 400) + "'; export function f() { return x.length; }"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><script type="module">import { f } from '/m.js'; window.len = f() + ':' + `+"`"+strings.Repeat("z", 300)+"`"+`.length;</script>`)
	})
	mux.HandleFunc("/m.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, big)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	o := DefaultOptions()
	o.CacheDir = t.TempDir()
	e := New(o)
	defer e.Close()
	for i := 0; i < 2; i++ {
		tab, err := e.NewTab(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		if v := mustEval(t, tab, `window.len`); v != `"400:300"` {
			t.Fatalf("visit %d: %s\nconsole: %s", i, v, consoleText(tab))
		}
		tl := tab.LoadTimeline()
		var state string
		for _, m := range tl {
			if m.Name == "script.exec" && strings.Contains(m.Detail, "inline") {
				state = m.Detail
			}
		}
		want := "bytecode:miss"
		if i == 1 {
			want = "bytecode:hit"
		}
		if !strings.HasSuffix(state, want) {
			t.Fatalf("visit %d: the module script was %q, want %s", i, state, want)
		}
	}
}

// The bytecode cache of classic scripts is hit on the second visit (also across engine restarts, through the
// profile's cache directory), and the timeline says so.
func TestClassicBytecodeCacheIsHitOnTheSecondVisit(t *testing.T) {
	body := "window.big = '" + strings.Repeat("q", 1000) + "'.length;"
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><script src="/big.js"></script>`)
	})
	mux.HandleFunc("/big.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Header().Set("Cache-Control", "max-age=600")
		fmt.Fprint(w, body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	dir := t.TempDir()
	for visit := 0; visit < 3; visit++ { // each visit is a fresh engine: the code comes back from the disk cache
		o := DefaultOptions()
		o.CacheDir = dir
		e := New(o)
		tab, err := e.NewTab(srv.URL + "/")
		if err != nil {
			t.Fatal(err)
		}
		var state string
		for _, m := range tab.LoadTimeline() {
			if m.Name == "script.exec" && strings.Contains(m.Detail, "/big.js") {
				state = m.Detail
			}
		}
		want := "bytecode:miss"
		if visit > 0 {
			want = "bytecode:hit"
		}
		if !strings.HasSuffix(state, want) {
			t.Fatalf("visit %d: %q, want %s", visit, state, want)
		}
		if v := mustEval(t, tab, `window.big`); v != "1000" {
			t.Fatalf("visit %d: %s", visit, v)
		}
		e.Close()
		time.Sleep(50 * time.Millisecond) // the disk copy is written in the background
	}
}

// The window's progressive path ends with the same pixels and document as the headless one on the fidelity pages
// (with async images, scripts in slices and the settle pump driven by LoadStep).
func TestProgressiveFinalPaintEqualsBlockingOnFidelityPages(t *testing.T) {
	dir := "../../testdata/fidelity"
	files, _ := filepath.Glob(filepath.Join(dir, "*.html"))
	if len(files) == 0 {
		t.Skip("no fidelity pages")
	}
	step := 1
	if testing.Short() {
		step = 6
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()
	blocking := newTabForLoad(t)
	prog := newTabForLoad(t)
	prog.SetAsyncImages(true)
	for i := 0; i < len(files); i += step {
		name := filepath.Base(files[i])
		url := srv.URL + "/" + name
		if err := blocking.Navigate(url, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := prog.NavigateProgressive(url, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		prog.PaintRGBA(1280, 800, 1, false) // the first frame: its layout asks for the images
		prog.LoadFinish()
		deadline := time.Now().Add(300 * time.Millisecond) // async images land after the last step
		for time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		a, ok1 := blocking.PaintRGBA(1280, 800, 1, false)
		prog.mu.RLock()
		d := prog.dom
		prog.mu.RUnlock()
		d.Touch() // what the update hook does for late images
		b, ok2 := prog.PaintRGBA(1280, 800, 1, false)
		if !ok1 || !ok2 {
			t.Fatalf("%s: no paint", name)
		}
		if hashPix(a) != hashPix(b) {
			t.Errorf("%s: progressive final paint differs from the blocking one", name)
		}
		if blocking.HTML(true) != prog.HTML(true) {
			t.Errorf("%s: final documents differ", name)
		}
	}
}

func hashPix(img *image.RGBA) string {
	h := sha256.New()
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		o := img.PixOffset(img.Rect.Min.X, y)
		h.Write(img.Pix[o : o+4*img.Rect.Dx()])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Page requests are asynchronous: three fetches overlap, a request that hangs neither holds the tab nor the
// load event of a window, and a headless load still waits for requests that are in flight (within httpGrace).
func TestFetchIsAsynchronous(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><script>
window.log = [];
var t0 = Date.now();
Promise.all([1, 2, 3].map(function (i) { return fetch('/slow?i=' + i).then(function (r) { return r.text(); }); }))
  .then(function (v) { log.push('all:' + v.join('')); window.allMs = Date.now() - t0; });
var x = new XMLHttpRequest(); x.open('GET', '/slow?i=x'); x.onload = function () { log.push('xhr:' + x.responseText); }; x.send();
fetch('/hang').then(function () { log.push('hang-done'); }, function () { log.push('hang-failed'); });
navigator.sendBeacon('/beacon', 'b');
setTimeout(function () { log.push('timer'); }, 20);
log.push('script-end');
</script>`)
	})
	var beacons atomic.Int32
	mux.HandleFunc("/beacon", func(w http.ResponseWriter, r *http.Request) { beacons.Add(1) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		fmt.Fprint(w, r.URL.Query().Get("i"))
	})
	mux.HandleFunc("/hang", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(1500 * time.Millisecond):
		case <-r.Context().Done():
		}
		fmt.Fprint(w, "late")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	old := httpGrace
	httpGrace = 5 * time.Second
	defer func() { httpGrace = old }()

	// window: the load event does not wait for the hanging request; the tab stays usable meanwhile
	win := newTabForLoad(t)
	t0 := time.Now()
	if err := win.NavigateProgressive(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	var loadAt time.Duration
	for win.LoadPending() {
		more, wait := win.LoadStep(10 * time.Millisecond)
		if loadAt == 0 && win.LoadLoaded() {
			loadAt = time.Since(t0)
		}
		if !more {
			break
		}
		time.Sleep(wait)
	}
	if loadAt == 0 || loadAt > 1200*time.Millisecond {
		t.Fatalf("load event at %v: it waited for the hanging request", loadAt)
	}
	if v := mustEval(t, win, `log.join()`); !strings.Contains(v, "script-end,timer") || strings.Contains(v, "hang-done") {
		t.Fatalf("window, right after load: %s", v)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(mustEval(t, win, `log.join()`), "hang-done") {
		win.Settle(50 * time.Millisecond)
	}
	got := mustEval(t, win, `log.join()`)
	for _, want := range []string{"all:123", "xhr:x", "hang-done"} {
		if !strings.Contains(got, want) {
			t.Fatalf("window: %q missing from %s", want, got)
		}
	}
	if ms := mustEval(t, win, `window.allMs`); len(ms) > 3 || ms > "699" && len(ms) == 3 {
		t.Errorf("three 300 ms fetches took %sms: they did not overlap", ms)
	}
	if beacons.Load() != 1 {
		t.Errorf("sendBeacon requests: %d", beacons.Load())
	}

	// headless: settled means the in-flight requests were answered
	head := newTabForLoad(t)
	t0 = time.Now()
	if err := head.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d < 1300*time.Millisecond || d > 3500*time.Millisecond {
		t.Errorf("headless Navigate took %v: it must wait for the 1.5 s request, no longer", d)
	}
	if v := mustEval(t, head, `log.join()`); !strings.Contains(v, "hang-done") || !strings.Contains(v, "all:123") {
		t.Fatalf("headless: %s", v)
	}

	// ... but not for ever: a request that outlives the grace period does not hold the settled page
	httpGrace = 400 * time.Millisecond
	head2 := newTabForLoad(t)
	t0 = time.Now()
	if err := head2.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Errorf("headless Navigate with a short grace took %v", d)
	}
	if v := mustEval(t, head2, `log.join()`); strings.Contains(v, "hang-done") {
		t.Errorf("the hanging request should still be outstanding: %s", v)
	}
}

// A module whose top-level await depends on fetch() completes (requests made under a module evaluation run in place).
func TestModuleTopLevelAwaitFetch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><script type="module">const r = await fetch('/d.json'); const j = await r.json(); window.tla = j.v; document.title = 'tla ' + j.v;</script>`)
	})
	mux.HandleFunc("/d.json", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"v": 42}`) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newTabForLoad(t)
	done := make(chan error, 1)
	go func() { done <- tab.Navigate(srv.URL+"/", nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a module with a top-level await on fetch never finished")
	}
	if tab.Title() != "tla 42" {
		t.Fatalf("title %q console %s", tab.Title(), consoleText(tab))
	}
}

// The modules of a graph download in parallel (their static imports are found as each source arrives), not one
// round trip after the other from inside the evaluation.
func TestModuleGraphDownloadsInParallel(t *testing.T) {
	mux := http.NewServeMux()
	var imports strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&imports, "import { v%d } from './leaf%d.js';\n", i, i)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><script type="module" src="/entry.js"></script>`)
	})
	mux.HandleFunc("/entry.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, imports.String()+"import { deep } from './mid.js'; window.sum = [v0,v1,v2,v3,v4,v5,v6,v7,v8,v9].join('') + deep;")
	})
	mux.HandleFunc("/mid.js", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "import { bottom } from './bottom.js'; export const deep = 'm' + bottom;")
	})
	mux.HandleFunc("/bottom.js", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "export const bottom = 'b';")
	})
	for i := 0; i < 10; i++ {
		i := i
		mux.HandleFunc(fmt.Sprintf("/leaf%d.js", i), func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(150 * time.Millisecond)
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprintf(w, "export const v%d = '%d';", i, i)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newTabForLoad(t)
	t0 := time.Now()
	if err := tab.Navigate(srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	d := time.Since(t0)
	if v := mustEval(t, tab, `window.sum`); v != `"0123456789mb"` {
		t.Fatalf("sum %s\nconsole: %s", v, consoleText(tab))
	}
	// sequential: 12 modules x 150 ms = 1.8 s; parallel: the entry plus a chain of two levels, about 0.5 s
	if d > 1100*time.Millisecond {
		t.Fatalf("the module graph took %v: its modules were fetched one after the other", d)
	}
}

// An entry module is compiled under its URL: a dynamic import("./x.js") run later (from a timer, after other
// scripts ran) resolves against the module, import.meta.url is the module's URL (webpack's automatic
// publicPath reads it), and a chunk importing the entry module back gets the instance that already ran.
func TestEntryModuleKeepsItsURL(t *testing.T) {
	mux := http.NewServeMux()
	js := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/javascript")
			fmt.Fprint(w, body)
		})
	}
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><body><p id="out">-</p>
<script type="module" src="/assets/main.js"></script>
<script src="/third/party.js"></script>`)
	})
	js("/assets/main.js", `window.runs = (window.runs || 0) + 1;
export const tag = 'main';
window.metaURL = import.meta.url;
setTimeout(function () { import('./chunk.js').then(function (m) { document.getElementById('out').textContent = m.v; }, function (e) { document.getElementById('out').textContent = 'ERR ' + e; }); }, 20);`)
	js("/assets/chunk.js", `import { tag } from './main.js'; export const v = 'chunk:' + tag;`)
	js("/third/party.js", `window.third = 1;`)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	tab := newTabForLoad(t)
	if err := tab.Navigate(srv.URL+"/page", nil); err != nil {
		t.Fatal(err)
	}
	tab.Settle(300 * time.Millisecond)
	want := `"chunk:main|` + srv.URL + `/assets/main.js|1"`
	if v := mustEval(t, tab, `document.getElementById('out').textContent + '|' + window.metaURL + '|' + window.runs`); v != want {
		t.Fatalf("got %s, want %s\nconsole:\n%s", v, want, consoleText(tab))
	}
}
