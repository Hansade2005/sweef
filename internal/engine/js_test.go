package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// testServer serves the JS fixtures: vendored framework bundles plus small
// pages that exercise the DOM bridge the way real sites do.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/vendor/", http.StripPrefix("/vendor/", http.FileServer(http.Dir("testdata/vendor"))))
	page := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(body))
		})
	}
	page("/inline", `<!doctype html><html><head><title>Inline</title></head><body>
<h1>Items</h1><ul id="list"></ul>
<script>
document.addEventListener('DOMContentLoaded', function () {
  var ul = document.getElementById('list');
  ['alpha','beta','gamma'].forEach(function (s, i) {
    var li = document.createElement('li');
    li.textContent = s + ' ' + i;
    li.className = 'item';
    ul.appendChild(li);
  });
  document.title = 'Inline ' + ul.children.length;
});
</script></body></html>`)
	page("/order", `<!doctype html><html><body>
<script>window.order = ['sync1'];</script>
<script src="/order-defer.js" defer></script>
<script src="/order-ext.js"></script>
<script>window.order.push('sync2'); window.addEventListener('DOMContentLoaded', () => window.order.push('dcl')); window.addEventListener('load', () => window.order.push('load'));</script>
</body></html>`)
	mux.HandleFunc("/order-defer.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`window.order.push('defer');`))
	})
	mux.HandleFunc("/order-ext.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`window.order.push('ext');`))
	})
	page("/fetch", `<!doctype html><html><body><div id="out">loading</div>
<script>
fetch('/api/data').then(r => r.json()).then(d => {
  document.getElementById('out').textContent = 'got ' + d.items.join(',');
  setTimeout(() => { document.getElementById('out').setAttribute('data-done', '1'); }, 20);
});
</script></body></html>`)
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"items":[1,2,3]}`))
	})
	page("/events", `<!doctype html><html><body>
<a id="lnk" href="/never">stay</a>
<form id="f" action="/submitted"><input id="q" name="q"><button id="go" type="submit">Go</button></form>
<button id="cnt">0</button>
<script>
document.getElementById('lnk').addEventListener('click', e => { e.preventDefault(); document.title = 'prevented'; });
var n = 0;
document.getElementById('cnt').addEventListener('click', e => { n++; e.target.textContent = String(n); });
document.getElementById('q').addEventListener('input', e => { document.title = 'typed:' + e.target.value; });
</script></body></html>`)
	page("/submitted", `<html><head><title>Submitted</title></head><body>ok</body></html>`)
	page("/dynamic", `<!doctype html><html><body><div id="d">waiting</div>
<script>
var s = document.createElement('script');
s.src = '/chunk.js';
s.onload = function () { document.getElementById('d').textContent = 'chunk:' + window.chunkValue; };
document.head.appendChild(s);
</script></body></html>`)
	mux.HandleFunc("/chunk.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`window.chunkValue = 42;`))
	})
	page("/react", `<!doctype html><html><body><div id="root"></div>
<script src="/vendor/react.js"></script>
<script src="/vendor/react-dom.js"></script>
<script>
const e = React.createElement;
function Counter() {
  const [n, setN] = React.useState(0);
  const [text, setText] = React.useState('');
  return e('div', null,
    e('h1', null, 'Count: ' + n),
    e('button', { id: 'inc', onClick: () => setN(n + 1) }, 'Increment'),
    e('input', { id: 'name', value: text, onChange: ev => setText(ev.target.value), placeholder: 'Your name' }),
    e('p', { id: 'echo' }, text ? 'Hello ' + text : 'no name'),
    e('ul', null, Array.from({length: n}, (_, i) => e('li', { key: i }, 'row ' + i)))
  );
}
ReactDOM.createRoot(document.getElementById('root')).render(e(Counter));
</script></body></html>`)
	page("/preact", `<!doctype html><html><body><div id="app"></div>
<script src="/vendor/preact.js"></script>
<script>
const { h, render } = preact;
let count = 0;
function App() { return h('div', null, h('span', { id: 'c' }, 'c=' + count), h('button', { id: 'b', onClick: () => { count++; render(h(App), document.getElementById('app')); } }, '+')); }
render(h(App), document.getElementById('app'));
</script></body></html>`)
	page("/jquery", `<!doctype html><html><body><div class="box">a</div><div class="box">b</div><p id="r"></p>
<script src="/vendor/jquery.js"></script>
<script>
$(function () {
  $('#r').text($('.box').length + ' boxes');
  $('.box').on('click', function () { $(this).addClass('clicked'); $('#r').text('clicked ' + $(this).text()); });
  $.get('/api/data', function (d) { $('body').append('<i id="ajax">' + d.items.length + '</i>'); });
});
</script></body></html>`)
	page("/loop", `<!doctype html><html><head><title>Loop</title></head><body><p>alive</p><script>while (true) {}</script><script>document.title = 'after';</script></body></html>`)
	page("/storage", `<!doctype html><html><body><script>
localStorage.setItem('k', 'v1');
document.title = localStorage.getItem('k') + ':' + localStorage.length;
</script></body></html>`)
	page("/module", `<!doctype html><html><body><p id="m"></p>
<script type="module">
import { greet } from './mod.js';
document.getElementById('m').textContent = greet('module');
</script></body></html>`)
	mux.HandleFunc("/mod.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		w.Write([]byte(`export function greet(x) { return 'hi ' + x; }`))
	})
	page("/navigate", `<!doctype html><html><body><script>setTimeout(() => { location.href = '/submitted'; }, 10);</script></body></html>`)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newJSTab(t *testing.T, srv *httptest.Server, path string) *Tab {
	t.Helper()
	if _, err := os.Stat("dom_prelude.js"); err != nil {
		t.Skip("prelude missing")
	}
	e := New(DefaultOptions())
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab(srv.URL + path)
	if err != nil {
		t.Fatalf("nav: %v", err)
	}
	return tab
}

func consoleText(tab *Tab) string {
	var sb strings.Builder
	for _, ev := range tab.ConsoleLog(0) {
		b, _ := json.Marshal(ev.Data)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func mustEval(t *testing.T, tab *Tab, expr string) string {
	t.Helper()
	r, err := tab.Evaluate(expr)
	if err != nil {
		t.Fatalf("eval %q: %v\nconsole:\n%s", expr, err, consoleText(tab))
	}
	return string(r)
}

func TestJSInlineDOMBuild(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/inline")
	y, _ := tab.AriaSnapshotYAML()
	if !strings.Contains(y, "alpha 0") || !strings.Contains(y, "gamma 2") {
		t.Fatalf("list not built:\n%s\nconsole:\n%s", y, consoleText(tab))
	}
	if tab.Title() != "Inline 3" {
		t.Fatalf("title via document.title: %q", tab.Title())
	}
	if got := mustEval(t, tab, `document.querySelectorAll('li.item').length`); got != "3" {
		t.Fatalf("qsa length: %s", got)
	}
}

func TestJSScriptOrderAndLifecycle(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/order")
	got := mustEval(t, tab, `window.order.join(',')`)
	if got != `"sync1,ext,sync2,defer,dcl,load"` {
		t.Fatalf("order: %s\n%s", got, consoleText(tab))
	}
}

func TestJSFetchAndTimers(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/fetch")
	txt, _ := tab.Text("#out")
	if txt != "got 1,2,3" {
		t.Fatalf("fetch result: %q\n%s", txt, consoleText(tab))
	}
	if v, ok, _ := tab.Locator("#out").GetAttribute("data-done"); !ok || v != "1" {
		t.Fatalf("timer did not fire")
	}
	if got := mustEval(t, tab, `await fetch('/api/data').then(r => r.status)`); got != "200" {
		t.Fatalf("await fetch: %s", got)
	}
}

func TestJSEventsPreventDefaultAndState(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/events")
	if err := tab.Click("#lnk"); err != nil {
		t.Fatal(err)
	}
	if tab.Title() != "prevented" || !strings.HasSuffix(tab.URL(), "/events") {
		t.Fatalf("preventDefault ignored: title=%q url=%s", tab.Title(), tab.URL())
	}
	for i := 0; i < 3; i++ {
		if err := tab.Click("#cnt"); err != nil {
			t.Fatal(err)
		}
	}
	if txt, _ := tab.Text("#cnt"); txt != "3" {
		t.Fatalf("counter: %q", txt)
	}
	if err := tab.Fill("#q", "hey"); err != nil {
		t.Fatal(err)
	}
	if tab.Title() != "typed:hey" {
		t.Fatalf("input event: %q", tab.Title())
	}
	if err := tab.Click("#go"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tab.URL(), "/submitted?q=hey") {
		t.Fatalf("form submit: %s", tab.URL())
	}
}

func TestJSDynamicScript(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/dynamic")
	if txt, _ := tab.Text("#d"); txt != "chunk:42" {
		t.Fatalf("dynamic script: %q\n%s", txt, consoleText(tab))
	}
}

func TestJSReactCounter(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/react")
	y, _ := tab.AriaSnapshotYAML()
	if !strings.Contains(y, `heading "Count: 0"`) {
		t.Fatalf("react did not render:\n%s\nconsole:\n%s", y, consoleText(tab))
	}
	if err := tab.Click("#inc"); err != nil {
		t.Fatal(err)
	}
	if err := tab.Click("text=Increment"); err != nil {
		t.Fatal(err)
	}
	y, _ = tab.AriaSnapshotYAML()
	if !strings.Contains(y, `heading "Count: 2"`) || !strings.Contains(y, "row 1") {
		t.Fatalf("react state did not update:\n%s\nconsole:\n%s", y, consoleText(tab))
	}
	if err := tab.Fill("#name", "Ada"); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab.Text("#echo"); txt != "Hello Ada" {
		t.Fatalf("controlled input: %q\n%s", txt, consoleText(tab))
	}
}

func TestJSPreactAndJQuery(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/preact")
	if err := tab.Click("#b"); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab.Text("#c"); txt != "c=1" {
		t.Fatalf("preact: %q\n%s", txt, consoleText(tab))
	}
	tab2 := newJSTab(t, srv, "/jquery")
	if txt, _ := tab2.Text("#r"); txt != "2 boxes" {
		t.Fatalf("jquery ready: %q\n%s", txt, consoleText(tab2))
	}
	if err := tab2.Click("text=b"); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab2.Text("#r"); txt != "clicked b" {
		t.Fatalf("jquery click: %q\n%s", txt, consoleText(tab2))
	}
	if txt, _ := tab2.Text("#ajax"); txt != "3" {
		t.Fatalf("jquery ajax: %q\n%s", txt, consoleText(tab2))
	}
}

func TestJSInfiniteLoopIsInterrupted(t *testing.T) {
	srv := testServer(t)
	e := New(Options{JSTimeoutMs: 300, SettleMs: 100, ConsoleRingSize: 64, NetworkRingSize: 64, StateRingSize: 64, DefaultUserAgent: "t"})
	t.Cleanup(func() { e.Close() })
	start := time.Now()
	tab, _ := e.NewTab(srv.URL + "/loop")
	if time.Since(start) > 5*time.Second {
		t.Fatalf("loop not interrupted in time")
	}
	if tab.Title() != "after" {
		t.Fatalf("tab unusable after interrupt: %q\n%s", tab.Title(), consoleText(tab))
	}
	if !strings.Contains(consoleText(tab), "interrupted") {
		t.Fatalf("expected interrupt report in console:\n%s", consoleText(tab))
	}
}

func TestJSStorageModuleNavigate(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/storage")
	if tab.Title() != "v1:1" {
		t.Fatalf("localStorage: %q\n%s", tab.Title(), consoleText(tab))
	}
	tab2 := newJSTab(t, srv, "/module")
	if txt, _ := tab2.Text("#m"); txt != "hi module" {
		t.Fatalf("module: %q\n%s", txt, consoleText(tab2))
	}
	tab3 := newJSTab(t, srv, "/navigate")
	if !strings.HasSuffix(tab3.URL(), "/submitted") || tab3.Title() != "Submitted" {
		t.Fatalf("location.href navigation: %s %q", tab3.URL(), tab3.Title())
	}
}

func TestJSEvaluateShapes(t *testing.T) {
	srv := testServer(t)
	tab := newJSTab(t, srv, "/inline")
	cases := map[string]string{
		`1+2*3`:                                `7`,
		`[1,2,3].map(x => x*2)`:                `[2,4,6]`,
		`document.title`:                       `"Inline 3"`,
		`var zz = 5; zz * 2`:                   `10`,
		`({a: 1, b: [true, null]})`:            `{"a":1,"b":[true,null]}`,
		`await Promise.resolve('p')`:           `"p"`,
		`typeof window.fetch`:                  `"function"`,
		`document.querySelector('h1').tagName`: `"H1"`,
	}
	for expr, want := range cases {
		if got := mustEval(t, tab, expr); got != want {
			t.Errorf("%s => %s, want %s", expr, got, want)
		}
	}
	if _, err := tab.Evaluate(`throw new Error('boom')`); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error propagation: %v", err)
	}
	if got := mustEval(t, tab, `document.querySelector('h1')`); !strings.Contains(got, `"tagName":"h1"`) && !strings.Contains(got, `"tagName":"H1"`) {
		t.Errorf("node serialisation: %s", got)
	}
}
