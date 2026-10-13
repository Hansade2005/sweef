package engine

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// proxyRecorder is a minimal HTTP proxy: plain requests are forwarded to the target address in the URL, CONNECT
// tunnels. It records everything it was asked for and counts the bytes of tunnels.
type proxyRecorder struct {
	*httptest.Server
	mu       sync.Mutex
	plain    []string
	connects []string
	tunnelIn atomic.Int64 // bytes the client sent through tunnels
}

func newProxyRecorder(t *testing.T) *proxyRecorder {
	p := &proxyRecorder{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			p.mu.Lock()
			p.connects = append(p.connects, r.Host)
			p.mu.Unlock()
			up, err := net.Dial("tcp", r.Host)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			hj := w.(http.Hijacker)
			c, buf, _ := hj.Hijack()
			fmt.Fprint(c, "HTTP/1.1 200 Connection established\r\n\r\n")
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				n, _ := io.Copy(up, io.MultiReader(buf, c))
				p.tunnelIn.Add(n)
				up.(*net.TCPConn).CloseWrite()
			}()
			go func() { defer wg.Done(); io.Copy(c, up); c.Close() }()
			wg.Wait()
			up.Close()
			return
		}
		p.mu.Lock()
		p.plain = append(p.plain, r.Method+" "+r.URL.String())
		p.mu.Unlock()
		if !strings.HasSuffix(r.URL.Hostname(), ".invalid") { // real targets are forwarded
			r.RequestURI = ""
			resp, err := http.DefaultTransport.RoundTrip(r)
			if err != nil {
				http.Error(w, err.Error(), 502)
				return
			}
			defer resp.Body.Close()
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			return
		}
		fmt.Fprint(w, "via proxy: "+r.URL.Host+r.URL.Path)
	}))
	t.Cleanup(p.Close)
	return p
}

func TestContextProxyURLIsUsed(t *testing.T) {
	p := newProxyRecorder(t)
	e := newDSEngine(t)
	c := e.NewContext("", ContextOptions{ProxyURL: p.URL})
	tab, _ := c.NewTab("about:blank")
	// a host that does not exist: only the proxy can answer
	r, err := tab.httpDo(httpOpts{method: "GET", url: "http://no-such-host.invalid/x", kind: KindXHR})
	if err != nil {
		t.Fatal(err)
	}
	if string(r.body) != "via proxy: no-such-host.invalid/x" {
		t.Fatalf("body %q", r.body)
	}
	if len(p.plain) != 1 {
		t.Fatalf("proxy saw %v", p.plain)
	}
	// the bytes to the proxy are in the context's wire counters
	if st := c.DataStats(); st.WireIn == 0 || st.WireOut == 0 {
		t.Fatalf("proxy traffic not counted: %+v", st)
	}
	// a context without the option does not use it
	c2 := e.NewContext("", ContextOptions{ProxyURL: "direct"})
	tab2, _ := c2.NewTab("about:blank")
	if _, err := tab2.httpDo(httpOpts{method: "GET", url: "http://no-such-host.invalid/x", kind: KindXHR}); err == nil {
		t.Fatal("direct must not reach the proxy")
	}
	// a bad proxy fails closed instead of going direct
	c3 := e.NewContext("", ContextOptions{ProxyURL: "http://%zz"})
	tab3, _ := c3.NewTab("about:blank")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "direct") }))
	defer srv.Close()
	if _, err := tab3.httpDo(httpOpts{method: "GET", url: srv.URL, kind: KindXHR}); err == nil {
		t.Fatal("an invalid proxy URL must fail the request, not bypass the proxy")
	}
}

func TestEngineProxyOptionAppliesToDefaultContext(t *testing.T) {
	p := newProxyRecorder(t)
	o := DefaultOptions()
	o.ProxyURL = p.URL
	e := New(o)
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	r, err := tab.httpDo(httpOpts{method: "GET", url: "http://no-such-host.invalid/y", kind: KindXHR})
	if err != nil || !strings.HasPrefix(string(r.body), "via proxy") {
		t.Fatalf("%v %v", r, err)
	}
}

func wsEchoServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(websocket.Handler(func(c *websocket.Conn) {
		for {
			var m string
			if err := websocket.Message.Receive(c, &m); err != nil {
				return
			}
			websocket.Message.Send(c, "echo:"+m+strings.Repeat("x", 4000))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func wsPage(t *testing.T, e *Engine, c *BrowserContext, wsURL string) string {
	srvPage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body><p id=t>wait</p><script>
var s=new WebSocket(%q); var n=0;
s.onopen=function(){ s.send("hello"); };
s.onmessage=function(e){ document.getElementById('t').textContent='got '+e.data.length; };
</script></body></html>`, wsURL)
	}))
	t.Cleanup(srvPage.Close)
	tab, err := c.NewTab(srvPage.URL)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tab.Settle(200 * time.Millisecond)
		if txt, _ := tab.Text("#t"); strings.HasPrefix(txt, "got") {
			return txt
		}
	}
	t.Fatal("no websocket message arrived")
	return ""
}

func TestWebSocketBytesAreCounted(t *testing.T) {
	ws := wsEchoServer(t)
	e := newDSEngine(t)
	c := e.NewContext("", ContextOptions{})
	// the page itself costs a few hundred bytes; the echo is 4 KB, which only shows if the socket is counted
	before := c.DataStats()
	wsPage(t, e, c, "ws"+strings.TrimPrefix(ws.URL, "http"))
	st := c.DataStats()
	if st.WireIn-before.WireIn < 4000 {
		t.Fatalf("websocket frames missing from WireIn: %d", st.WireIn-before.WireIn)
	}
}

func TestWebSocketGoesThroughTheProxy(t *testing.T) {
	ws := wsEchoServer(t)
	p := newProxyRecorder(t)
	e := newDSEngine(t)
	c := e.NewContext("", ContextOptions{ProxyURL: p.URL})
	wsURL := "ws" + strings.TrimPrefix(ws.URL, "http")
	// the page is plain http on loopback and goes through the proxy too (explicit proxy: nothing is exempt)
	wsPage(t, e, c, wsURL)
	p.mu.Lock()
	connects := append([]string(nil), p.connects...)
	p.mu.Unlock()
	want := strings.TrimPrefix(ws.URL, "http://")
	found := false
	for _, h := range connects {
		if h == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("the WebSocket must be tunnelled through the proxy: CONNECTs %v, want %s", connects, want)
	}
	if st := c.DataStats(); st.WireIn < 4000 {
		t.Fatalf("socket bytes through the proxy are counted: %+v", st)
	}
}
