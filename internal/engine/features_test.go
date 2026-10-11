package engine

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/sweefbrowser/sweef/internal/pdf"
)

func featureServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	page := func(path, body string) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, body)
		})
	}
	mux.HandleFunc("/api/real", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"from":"server"}`)
	})
	mux.HandleFunc("/api/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, r.Method+" "+string(b))
	})
	page("/fetcher", `<!doctype html><html><body><p id="out">-</p><script>
fetch('/api/real').then(r => r.json()).then(d => { document.getElementById('out').textContent = d.from; });
</script></body></html>`)
	page("/static", `<!doctype html><html><head><title>Static</title></head><body><h1>No scripts here</h1><a href="/fetcher">go</a></body></html>`)
	page("/late", `<!doctype html><html><body><p id="s">start</p><script>
setTimeout(() => fetch('/api/real').then(r => r.text()).then(t => { document.getElementById('s').textContent = 'late:' + t.length; }), 200);
</script></body></html>`)
	page("/bind", `<!doctype html><html><body><p id="r">-</p><script>
window.addEventListener('load', async () => { const v = await window.agentNotify(1, 'two', {three: 3}); document.getElementById('r').textContent = JSON.stringify(v); });
</script></body></html>`)
	mux.HandleFunc("/file.csv", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="report.csv"`)
		io.WriteString(w, "a,b\n1,2\n")
	})
	mux.HandleFunc("/blob.bin", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write([]byte{0, 1, 2, 3, 255})
	})
	page("/dl", `<!doctype html><html><body>
<a id="direct" href="/blob.bin" download="data.bin">direct</a>
<button id="mk">make</button>
<script>
document.getElementById('mk').addEventListener('click', () => {
  const a = document.createElement('a');
  a.href = URL.createObjectURL(new Blob(['hello from blob'], {type: 'text/plain'}));
  a.download = 'note.txt';
  document.body.appendChild(a); a.click();
});
</script></body></html>`)
	mux.Handle("/ws", websocket.Handler(func(c *websocket.Conn) {
		for {
			var msg []byte
			if err := websocket.Message.Receive(c, &msg); err != nil {
				return
			}
			if len(msg) > 0 && msg[0] == 0xff {
				websocket.Message.Send(c, append([]byte{0xfe}, msg[1:]...))
				continue
			}
			websocket.Message.Send(c, "echo:"+string(msg))
		}
	}))
	page("/wspage", `<!doctype html><html><body><p id="w">-</p><p id="b">-</p><script>
const ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/ws');
ws.binaryType = 'arraybuffer';
ws.onopen = () => { ws.send('hi'); ws.send(new Uint8Array([255, 7, 8])); };
ws.onmessage = (e) => {
  if (typeof e.data === 'string') document.getElementById('w').textContent = e.data;
  else document.getElementById('b').textContent = Array.from(new Uint8Array(e.data)).join(',');
};
</script></body></html>`)
	mux.HandleFunc("/worker.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, `importScripts('/helper.js');
self.onmessage = async (e) => {
  const r = await fetch('/api/real').then(r => r.json());
  setTimeout(() => postMessage({sum: add(e.data.a, e.data.b), from: r.from}), 10);
};`)
	})
	mux.HandleFunc("/helper.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, `function add(a, b) { return a + b; }`)
	})
	page("/workerpage", `<!doctype html><html><body><p id="o">-</p><p id="bo">-</p><script>
const w = new Worker('/worker.js');
w.onmessage = (e) => { document.getElementById('o').textContent = e.data.sum + ':' + e.data.from; };
w.postMessage({a: 40, b: 2});
const src = 'onmessage = e => postMessage(e.data * 2)';
const bw = new Worker(URL.createObjectURL(new Blob([src], {type: 'text/javascript'})));
bw.onmessage = (e) => { document.getElementById('bo').textContent = String(e.data); };
bw.postMessage(21);
</script></body></html>`)
	mux.HandleFunc("/site.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		io.WriteString(w, `.linked-hidden { display: none } @media (max-width: 600px) { .wide-only { display: none } }`)
	})
	page("/css", `<!doctype html><html><head><link rel="stylesheet" href="/site.css"><style>.menu { display: none } .ghost { visibility: hidden }</style></head><body>
<ul class="menu"><li><a href="/x">Hidden menu link</a></li></ul>
<p class="linked-hidden">Hidden by linked sheet</p>
<p class="ghost">Invisible text</p>
<p class="wide-only">Only on wide screens</p>
<a href="/y">Shown link</a>
</body></html>`)
	page("/form", `<!doctype html><html><body><form method="post" action="/api/echo"><input name="q" value="x"><button>Send</button></form></body></html>`)
	page("/doc", `<!doctype html><html><head><title>Report</title></head><body><h1>Quarterly report</h1><p>Revenue grew.</p><ul><li>One</li><li>Two</li></ul><a href="https://example.com/more">More</a></body></html>`)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newFeatureEngine(t *testing.T) *Engine {
	t.Helper()
	o := DefaultOptions()
	o.DownloadsDir = t.TempDir()
	e := New(o)
	t.Cleanup(func() { e.Close() })
	return e
}

func TestLazyVMForStaticPages(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, err := e.NewTab(srv.URL + "/static")
	if err != nil {
		t.Fatal(err)
	}
	tab.mu.RLock()
	hasVM := tab.js != nil
	tab.mu.RUnlock()
	if hasVM {
		t.Fatal("script-less page should not get a VM")
	}
	if got := mustEval(t, tab, `document.querySelector('h1').textContent`); got != `"No scripts here"` {
		t.Fatalf("lazy evaluate: %s", got)
	}
	if err := tab.Click("text=go"); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab.Text("#out"); txt != "server" {
		t.Fatalf("navigation from lazy page to script page: %q", txt)
	}
}

func TestRoutesFulfillAbortContinue(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, _ := e.NewTab("about:blank")
	if err := tab.AddRoute(&RouteRule{Pattern: "**/api/real", Action: "fulfill", Body: `{"from":"mock"}`, ContentType: "application/json"}); err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(srv.URL+"/fetcher", nil); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab.Text("#out"); txt != "mock" {
		t.Fatalf("fulfill for fetch: %q\n%s", txt, consoleText(tab))
	}
	tab.RemoveRoutes("")
	if err := tab.AddRoute(&RouteRule{Pattern: "/.*\\/static$/", Action: "abort"}); err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(srv.URL+"/static", nil); err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("abort navigation: %v", err)
	}
	tab.RemoveRoutes("")
	nb := "q=rewritten"
	if err := tab.AddRoute(&RouteRule{Pattern: "**/api/echo", Method: "POST", BodyContains: "q=x", Action: "continue", NewBody: &nb, Times: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tab.Navigate(srv.URL+"/form", nil); err != nil {
		t.Fatal(err)
	}
	if err := tab.Click("text=Send"); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab.Text("body"); txt != "POST q=rewritten" {
		t.Fatalf("continue with new body: %q", txt)
	}
	if len(tab.Routes()) != 0 {
		t.Fatalf("times=1 rule should be gone: %v", tab.Routes())
	}
	found := false
	for _, ev := range tab.NetworkLog(0) {
		if m, ok := ev.Data.(map[string]string); ok && m["request.postData"] == "q=x" {
			found = true
		}
	}
	if !found {
		t.Fatal("POST body missing from network log")
	}
}

func TestNetworkIdle(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, _ := e.NewTab(srv.URL + "/late")
	start := time.Now()
	if err := tab.WaitForLoadState("networkidle", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab.Text("#s"); !strings.HasPrefix(txt, "late:") {
		t.Fatalf("networkidle returned before the late fetch: %q after %v", txt, time.Since(start))
	}
}

func TestExposedBindingWithReply(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, _ := e.NewTab("about:blank")
	tab.ExposeBinding("agentNotify")
	tab.SetBindingReply("agentNotify", `{"ack":true}`)
	if err := tab.Navigate(srv.URL+"/bind", nil); err != nil {
		t.Fatal(err)
	}
	if txt, _ := tab.Text("#r"); txt != `{"ack":true}` {
		t.Fatalf("binding reply: %q\n%s", txt, consoleText(tab))
	}
	calls := tab.BindingCalls(0)
	if len(calls) != 1 || calls[0].Name != "agentNotify" || string(calls[0].Args) != `[1,"two",{"three":3}]` {
		t.Fatalf("binding calls: %+v", calls)
	}
}

func TestDownloads(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, _ := e.NewTab(srv.URL + "/dl")
	// navigation to an attachment saves it and keeps the page
	if err := tab.Navigate(srv.URL+"/file.csv", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(tab.URL(), "/dl") {
		t.Fatalf("page replaced by download: %s", tab.URL())
	}
	if err := tab.Click("#direct"); err != nil {
		t.Fatal(err)
	}
	if err := tab.Click("#mk"); err != nil {
		t.Fatal(err)
	}
	ds := tab.Downloads()
	if len(ds) != 3 {
		t.Fatalf("want 3 downloads, got %+v\n%s", ds, consoleText(tab))
	}
	want := map[string]string{"report.csv": "a,b\n1,2\n", "data.bin": "\x00\x01\x02\x03\xff", "note.txt": "hello from blob"}
	for _, d := range ds {
		b, err := os.ReadFile(d.Path)
		if err != nil {
			t.Fatal(err)
		}
		if w, ok := want[filepath.Base(d.Path)]; !ok || string(b) != w {
			t.Fatalf("download %s: %q", d.Path, b)
		}
	}
}

func TestWebSocketEcho(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, _ := e.NewTab(srv.URL + "/wspage")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		w, _ := tab.Text("#w")
		b, _ := tab.Text("#b")
		if w == "echo:hi" && b == "254,7,8" {
			found := false
			for _, ev := range tab.NetworkLog(0) {
				if ev.Kind == EvWebSocket {
					found = true
				}
			}
			if !found {
				t.Fatal("websocket frames missing from network log")
			}
			return
		}
		tab.Settle(50 * time.Millisecond)
	}
	w, _ := tab.Text("#w")
	b, _ := tab.Text("#b")
	t.Fatalf("websocket: text=%q binary=%q\n%s", w, b, consoleText(tab))
}

func TestWorkers(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, _ := e.NewTab(srv.URL + "/workerpage")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		o, _ := tab.Text("#o")
		bo, _ := tab.Text("#bo")
		if o == "42:server" && bo == "42" {
			return
		}
		tab.Settle(50 * time.Millisecond)
	}
	o, _ := tab.Text("#o")
	bo, _ := tab.Text("#bo")
	t.Fatalf("workers: url worker=%q blob worker=%q\n%s", o, bo, consoleText(tab))
}

func TestCSSVisibilityInSnapshot(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, _ := e.NewTab("about:blank")
	tab.SetViewport(500, 800, 1, false, false)
	if err := tab.Navigate(srv.URL+"/css", nil); err != nil {
		t.Fatal(err)
	}
	y, _ := tab.AriaSnapshotYAML()
	for _, hidden := range []string{"Hidden menu link", "Hidden by linked sheet", "Invisible text", "Only on wide screens"} {
		if strings.Contains(y, hidden) {
			t.Fatalf("%q should be hidden by CSS:\n%s", hidden, y)
		}
	}
	if !strings.Contains(y, "Shown link") {
		t.Fatalf("visible link missing:\n%s", y)
	}
	if v, _ := tab.Locator("text=Shown link").IsVisible(); !v {
		t.Fatal("IsVisible false for visible link")
	}
}

func TestPDFExport(t *testing.T) {
	srv := featureServer(t)
	e := newFeatureEngine(t)
	tab, _ := e.NewTab(srv.URL + "/doc")
	b, err := tab.PDF(pdf.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-1.4")) || !bytes.Contains(b, []byte("Quarterly report")) || !bytes.Contains(b, []byte("/URI (https://example.com/more)")) {
		t.Fatalf("pdf content unexpected (%d bytes)", len(b))
	}
}
