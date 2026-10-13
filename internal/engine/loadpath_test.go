package engine

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
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
