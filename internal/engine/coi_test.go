package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// route is one response of a coiServer.
type route struct {
	body string
	typ  string            // Content-Type (default: by extension)
	hdr  map[string]string // extra response headers
}

// coiServer serves fixed routes with arbitrary response headers (COOP, COEP, CORP, CORS ...). Unknown paths answer
// a blank HTML page with the headers in def, so a test can open any x.html.
type coiServer struct {
	*httptest.Server
	mu     sync.Mutex
	routes map[string]route
	def    map[string]string
	hits   map[string]int
	cookie map[string]string // path -> Cookie header the server saw
}

var isolatedHdr = map[string]string{"Cross-Origin-Opener-Policy": "same-origin", "Cross-Origin-Embedder-Policy": "require-corp"}

func newCOIServer(t *testing.T, def map[string]string, routes map[string]route) *coiServer {
	t.Helper()
	s := &coiServer{routes: routes, def: def, hits: map[string]int{}, cookie: map[string]string{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.cookie[r.URL.Path] = r.Header.Get("Cookie")
		rt, ok := s.routes[r.URL.Path]
		s.mu.Unlock()
		if !ok {
			if !strings.HasSuffix(r.URL.Path, ".html") && r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			rt = route{body: "<!doctype html><html><head><title>t</title></head><body><p id=o>-</p></body></html>", hdr: def}
		}
		typ := rt.typ
		if typ == "" {
			switch {
			case strings.HasSuffix(r.URL.Path, ".js"):
				typ = "application/javascript"
			case strings.HasSuffix(r.URL.Path, ".wasm"):
				typ = "application/wasm"
			case strings.HasSuffix(r.URL.Path, ".css"):
				typ = "text/css"
			default:
				typ = "text/html; charset=utf-8"
			}
		}
		w.Header().Set("Content-Type", typ)
		for k, v := range rt.hdr {
			w.Header().Set(k, v)
		}
		io.WriteString(w, rt.body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *coiServer) set(path string, r route) {
	s.mu.Lock()
	s.routes[path] = r
	s.mu.Unlock()
}

func mergeHdr(a, b map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range a {
		m[k] = v
	}
	for k, v := range b {
		m[k] = v
	}
	return m
}

func TestCrossOriginIsolationGatesSharedArrayBuffer(t *testing.T) {
	cases := []struct {
		name string
		hdr  map[string]string
		want string
	}{
		{"coop+coep require-corp", isolatedHdr, `{"isolated":true,"sab":"function","atomics":"object","waitAsync":"function"}`},
		{"coop+coep credentialless", map[string]string{"Cross-Origin-Opener-Policy": "same-origin", "Cross-Origin-Embedder-Policy": "credentialless"}, `{"isolated":true,"sab":"function","atomics":"object","waitAsync":"function"}`},
		{"coep with parameters", map[string]string{"Cross-Origin-Opener-Policy": "same-origin; report-to=\"x\"", "Cross-Origin-Embedder-Policy": "require-corp; report-to=\"y\""}, `{"isolated":true,"sab":"function","atomics":"object","waitAsync":"function"}`},
		{"no headers", nil, `{"isolated":false,"sab":"undefined","atomics":"object","waitAsync":"function"}`},
		{"coep only", map[string]string{"Cross-Origin-Embedder-Policy": "require-corp"}, `{"isolated":false,"sab":"undefined","atomics":"object","waitAsync":"function"}`},
		{"coop only", map[string]string{"Cross-Origin-Opener-Policy": "same-origin"}, `{"isolated":false,"sab":"undefined","atomics":"object","waitAsync":"function"}`},
		{"coop same-origin-allow-popups", map[string]string{"Cross-Origin-Opener-Policy": "same-origin-allow-popups", "Cross-Origin-Embedder-Policy": "require-corp"}, `{"isolated":false,"sab":"undefined","atomics":"object","waitAsync":"function"}`},
		{"coep unsafe-none", map[string]string{"Cross-Origin-Opener-Policy": "same-origin", "Cross-Origin-Embedder-Policy": "unsafe-none"}, `{"isolated":false,"sab":"undefined","atomics":"object","waitAsync":"function"}`},
	}
	e := newFeatureEngine(t)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newCOIServer(t, c.hdr, nil)
			tab := openTab(t, e, srv.URL+"/a.html")
			got := js(t, tab, `return JSON.stringify({ isolated: crossOriginIsolated, sab: typeof SharedArrayBuffer, atomics: typeof Atomics, waitAsync: typeof Atomics.waitAsync });`)
			if got != c.want {
				t.Fatalf("got %s\nwant %s\nconsole:\n%s", got, c.want, consoleText(tab))
			}
		})
	}
}

// Navigating from an isolated page to a plain one takes SharedArrayBuffer away again (the VM is rebuilt per document).
func TestCrossOriginIsolationFollowsTheDocument(t *testing.T) {
	srv := newCOIServer(t, nil, map[string]route{"/iso.html": {body: "<!doctype html><html><body>iso</body></html>", hdr: isolatedHdr}})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/iso.html")
	if got := js(t, tab, `return [crossOriginIsolated, typeof SharedArrayBuffer].join()`); got != "true,function" {
		t.Fatalf("iso page: %s", got)
	}
	if err := tab.Navigate(srv.URL+"/plain.html", nil); err != nil {
		t.Fatal(err)
	}
	if got := js(t, tab, `return [crossOriginIsolated, typeof SharedArrayBuffer].join()`); got != "false,undefined" {
		t.Fatalf("plain page: %s", got)
	}
}

func TestCOEPRequireCorpBlocksNoCORSSubresources(t *testing.T) {
	other := newCOIServer(t, nil, map[string]route{
		"/plain.js":     {body: "window.plain=1"},
		"/corp.js":      {body: "window.corp=1", hdr: map[string]string{"Cross-Origin-Resource-Policy": "cross-origin"}},
		"/samesite.js":  {body: "window.ss=1", hdr: map[string]string{"Cross-Origin-Resource-Policy": "same-site"}},
		"/sameorig.js":  {body: "window.so=1", hdr: map[string]string{"Cross-Origin-Resource-Policy": "same-origin"}},
		"/cors.js":      {body: "window.cors=1", hdr: map[string]string{"Access-Control-Allow-Origin": "*"}},
		"/style.css":    {body: "body{color:red}"},
		"/img.png":      {body: "x", typ: "image/png"},
		"/img-corp.png": {body: "x", typ: "image/png", hdr: map[string]string{"Cross-Origin-Resource-Policy": "cross-origin"}},
	})
	srv := newCOIServer(t, isolatedHdr, map[string]route{"/self.js": {body: "window.self1=1"}})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	type c struct {
		path string
		kind DataKind
		ok   bool
	}
	for _, x := range []c{
		{"/plain.js", KindScript, false},
		{"/corp.js", KindScript, true},
		{"/samesite.js", KindScript, true}, // 127.0.0.1 with another port is the same site
		{"/sameorig.js", KindScript, false},
		{"/cors.js", KindScript, true},
		{"/style.css", KindStyle, false},
		{"/img.png", KindImage, false},
		{"/img-corp.png", KindImage, true},
		{"/plain.js", KindXHR, true}, // fetch/XHR run in CORS mode
		{"/plain.js", KindFont, true},
	} {
		_, _, err := tab.fetchRes(other.URL+x.path, fetchOpts{kind: x.kind})
		if (err == nil) != x.ok {
			t.Errorf("%s as %s: err=%v, want ok=%v", x.path, x.kind, err, x.ok)
		}
	}
	// a same-origin resource needs nothing
	if _, _, err := tab.fetchRes(srv.URL+"/self.js", fetchOpts{kind: KindScript}); err != nil {
		t.Errorf("same-origin script blocked: %v", err)
	}
	// the page that is not isolated may load all of them
	plain := newCOIServer(t, nil, nil)
	tab2 := openTab(t, e, plain.URL+"/a.html")
	if _, _, err := tab2.fetchRes(other.URL+"/plain.js", fetchOpts{kind: KindScript}); err != nil {
		t.Errorf("non-COEP page blocked a cross-origin script: %v", err)
	}
	if !strings.Contains(consoleText(tab), "Cross-Origin-Embedder-Policy: require-corp") {
		t.Errorf("no console explanation:\n%s", consoleText(tab))
	}
}

func TestCOEPCredentiallessSendsNoCookies(t *testing.T) {
	other := newCOIServer(t, nil, map[string]route{"/a.js": {body: "1"}, "/b.js": {body: "2"}})
	srv := newCOIServer(t, map[string]string{"Cross-Origin-Opener-Policy": "same-origin", "Cross-Origin-Embedder-Policy": "credentialless"}, nil)
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	if err := tab.Context().AddCookies([]map[string]any{{"name": "sid", "value": "secret", "url": other.URL}}); err != nil {
		t.Fatal(err)
	}
	// no CORP header needed, and the request is anonymous
	if _, _, err := tab.fetchRes(other.URL+"/a.js", fetchOpts{kind: KindScript}); err != nil {
		t.Fatalf("credentialless blocked the script: %v", err)
	}
	other.mu.Lock()
	gotNo := other.cookie["/a.js"]
	other.mu.Unlock()
	if gotNo != "" {
		t.Errorf("credentialless cross-origin script sent cookies: %q", gotNo)
	}
	// a CORS-mode request (fetch) keeps its credentials
	if _, err := tab.httpForScriptKind(KindXHR, "GET", other.URL+"/b.js", nil, ""); err != nil {
		t.Fatal(err)
	}
	other.mu.Lock()
	gotYes := other.cookie["/b.js"]
	other.mu.Unlock()
	if !strings.Contains(gotYes, "sid=secret") {
		t.Errorf("CORS-mode request lost its cookie: %q", gotYes)
	}
}

func TestCOEPFramesAndWorkersNeedThePolicyToo(t *testing.T) {
	srv := newCOIServer(t, isolatedHdr, map[string]route{
		"/with.html":    {body: "<!doctype html><html><body>with</body></html>", hdr: isolatedHdr},
		"/without.html": {body: "<!doctype html><html><body>without</body></html>"},
		"/page.html": {body: `<!doctype html><html><body>
<iframe id=a src="/with.html"></iframe><iframe id=b src="/without.html"></iframe></body></html>`, hdr: isolatedHdr},
		"/w-ok.js":  {body: "postMessage({iso: crossOriginIsolated, sab: typeof SharedArrayBuffer})", hdr: isolatedHdr},
		"/w-bad.js": {body: "postMessage('should not run')"},
	})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/page.html")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (len(tab.FrameTabs()) < 2 || tab.framesPending() > 0) {
		time.Sleep(20 * time.Millisecond)
	}
	infos := tab.Frames()
	if len(infos) != 2 {
		t.Fatalf("frames: %+v", infos)
	}
	if infos[0].Blocked || !infos[1].Blocked {
		t.Errorf("frame blocking: with=%v without=%v (want false, true)", infos[0].Blocked, infos[1].Blocked)
	}
	if kids := tab.FrameTabs(); !kids[0].CrossOriginIsolated() {
		t.Errorf("a frame that opted in is not isolated")
	}
	got := js(t, tab, `
const run = (u) => new Promise((res) => { const w = new Worker(u); w.onmessage = (e) => { w.terminate(); res(JSON.stringify(e.data)); }; w.onerror = (e) => { w.terminate(); res('error'); }; setTimeout(() => res('timeout'), 3000); });
return JSON.stringify([await run('/w-ok.js'), await run('/w-bad.js')]);`)
	if want := `["{\"iso\":true,\"sab\":\"function\"}","error"]`; got != want {
		t.Fatalf("got %s want %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}
