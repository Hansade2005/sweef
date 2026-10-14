package engine

import (
	"strings"
	"testing"
	"time"
)

func TestFrameDocumentsLoadAndAreReachable(t *testing.T) {
	s := newSites(t, "a.localhost", "b.localhost")
	s.page("a.localhost", "/", `<!doctype html><title>top</title><body><p id=o>top</p>
<iframe id=same src="/inner.html" name="innerframe"></iframe>
<iframe id=cross src="`+s.u("b.localhost", "/embed.html")+`"></iframe>
<iframe id=sd srcdoc="<p id='x'>from srcdoc</p><script>parent.postMessage('hello from srcdoc', '*')</script>"></iframe>
<iframe id=blank></iframe></body>`)
	s.page("a.localhost", "/inner.html", `<!doctype html><title>inner</title><p id=t>inner text</p><script>window.answer = 42; function add(a,b){return a+b}</script>`)
	s.page("b.localhost", "/embed.html", `<!doctype html><title>embed</title><p id=t>cross text</p><script>window.secret = 'b-secret'</script>`)
	e := newSiteEngine(t)
	tab, err := e.NewTab(s.u("a.localhost", "/"))
	if err != nil {
		t.Fatal(err)
	}
	if !tab.WaitFrames(10 * time.Second) {
		t.Fatalf("frames did not finish loading: %+v", tab.Frames())
	}
	fs := tab.Frames()
	if len(fs) != 4 {
		t.Fatalf("want 4 frames, got %+v", fs)
	}
	if fs[0].URL != s.u("a.localhost", "/inner.html") || fs[0].Origin != "http://a.localhost:"+s.port || fs[0].Name != "innerframe" {
		t.Fatalf("same-origin frame: %+v", fs[0])
	}
	if fs[1].Origin != "http://b.localhost:"+s.port {
		t.Fatalf("cross-origin frame: %+v", fs[1])
	}
	if fs[2].URL != "about:srcdoc" || fs[2].Origin != "http://a.localhost:"+s.port {
		t.Fatalf("srcdoc frame must inherit its creator's origin: %+v", fs[2])
	}
	if fs[3].URL != "about:blank" || fs[3].Origin != "http://a.localhost:"+s.port {
		t.Fatalf("blank frame: %+v", fs[3])
	}
	// a frame is a tab of its own: read it from outside
	kids := tab.FrameTabs()
	if txt, _ := kids[0].Text("#t"); txt != "inner text" {
		t.Fatalf("inner text %q", txt)
	}
	if txt, _ := kids[1].Text("#t"); txt != "cross text" {
		t.Fatalf("cross text %q", txt)
	}
	if txt, _ := kids[2].Text("#x"); txt != "from srcdoc" {
		t.Fatalf("srcdoc text %q", txt)
	}
	// the frame request carried frame headers
	h := s.hitsFor("b.localhost", "/embed.html")
	if len(h) != 1 || h[0].Dest != "iframe" || h[0].Site != "cross-site" || h[0].Mode != "no-cors" || !strings.HasPrefix(h[0].Referer, "http://a.localhost:") {
		t.Fatalf("frame request headers: %+v", h)
	}

	got := js(t, tab, `
const r = {};
const same = document.getElementById('same'), cross = document.getElementById('cross');
r.same = [same.contentWindow.answer, same.contentWindow.add(2, 3), same.contentDocument.getElementById('t').textContent, same.contentDocument.title, same.contentWindow.location.pathname, same.contentWindow === same.contentWindow, same.contentDocument === same.contentWindow.document];
r.sameWrite = (same.contentDocument.body.appendChild(same.contentDocument.createElement('b')), same.contentDocument.body.lastChild.tagName);
same.contentWindow.document.getElementById('t').textContent = 'changed by parent';
r.crossDoc = cross.contentDocument;
r.crossWin = typeof cross.contentWindow;
try { cross.contentWindow.secret; } catch (e) { r.crossGet = e.name; }
try { cross.contentWindow.document; } catch (e) { r.crossDocument = e.name; }
r.crossLen = [cross.contentWindow.length, cross.contentWindow.closed, cross.contentWindow.parent === window, cross.contentWindow.top === window, cross.contentWindow.frames === cross.contentWindow];
r.names = [window.frames.length, window.length, window.frames[0] === same.contentWindow, window.frames[1] === cross.contentWindow];
r.blankDoc = document.getElementById('blank').contentDocument.body.tagName;
const bd = document.getElementById('blank').contentDocument; bd.body.innerHTML = '<span id=w>written</span>';
r.blankWritten = document.getElementById('blank').contentDocument.getElementById('w').textContent;
r.sdText = document.getElementById('sd').contentDocument.getElementById('x').textContent;
return JSON.stringify(r);`)
	want := `{"same":[42,5,"inner text","inner","/inner.html",true,true],"sameWrite":"B","crossDoc":null,"crossWin":"object","crossGet":"SecurityError","crossDocument":"SecurityError","crossLen":[0,false,true,true,true],"names":[4,4,true,true],"blankDoc":"BODY","blankWritten":"written","sdText":"from srcdoc"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
	if txt, _ := kids[0].Text("#t"); txt != "changed by parent" {
		t.Fatalf("the parent's write did not reach the frame: %q", txt)
	}
}

func TestFramePostMessageOrigins(t *testing.T) {
	s := newSites(t, "a.localhost", "b.localhost")
	s.page("a.localhost", "/", `<!doctype html><body><script>window.log = []; window.addEventListener('message', (e) => log.push([e.data && e.data.constructor === Object ? 'obj' : e.data, e.origin, e.source === document.getElementById('f').contentWindow]));</script><iframe id=f src="`+s.u("b.localhost", "/child.html")+`"></iframe></body>`)
	s.page("b.localhost", "/child.html", `<!doctype html><script>
window.got = [];
window.addEventListener('message', (e) => {
  got.push({ data: e.data, origin: e.origin, sourceIsParent: e.source === window.parent, sourceIsTop: e.source === window.top, ports: e.ports.length });
  if (e.data === 'ping') e.source.postMessage('pong', e.origin);
  if (e.data && e.data.port) e.data.port.postMessage('over the port');
});
window.parent.postMessage('child-ready', '*');
</script>`)
	e := newSiteEngine(t)
	tab, err := e.NewTab(s.u("a.localhost", "/"))
	if err != nil {
		t.Fatal(err)
	}
	tab.WaitFrames(10 * time.Second)
	got := js(t, tab, `
const f = document.getElementById('f');
const wait = async (n) => { for (let i = 0; i < 300 && log.length < n; i++) await new Promise((r) => setTimeout(r, 10)); };
await wait(1);
f.contentWindow.postMessage('ping', '`+s.u("b.localhost", "")+`');
await wait(2);
// the wrong target origin delivers nothing, a "/" target means this window's own origin (not the child's)
f.contentWindow.postMessage('wrong-origin', '`+s.u("a.localhost", "")+`');
f.contentWindow.postMessage('slash', '/');
f.contentWindow.postMessage({ nested: [1, { x: new Date(7) }] }, '*');
const mc = new MessageChannel(); const portMsg = new Promise((res) => { mc.port1.onmessage = (m) => res(m.data); });
f.contentWindow.postMessage({ port: mc.port2 }, '*', [mc.port2]);
const viaPort = await portMsg;
let bad = ''; try { f.contentWindow.postMessage('x', 'not a url'); } catch (e) { bad = e.name; }
return JSON.stringify({ log, viaPort, bad });`)
	if !strings.Contains(got, `["child-ready","http://b.localhost:`+s.port+`",true]`) || !strings.Contains(got, `["pong","http://b.localhost:`+s.port+`",true]`) || !strings.Contains(got, `"viaPort":"over the port"`) || !strings.Contains(got, `"bad":"SyntaxError"`) {
		t.Fatalf("got %s\nconsole:\n%s", got, consoleText(tab))
	}
	child := tab.FrameTabs()[0]
	seen, _ := child.Evaluate(`JSON.stringify(window.got.map(function (g) { return [typeof g.data === 'object' && g.data && g.data.port ? 'port' : (g.data && g.data.nested ? 'nested' : g.data), g.origin, g.sourceIsParent, g.sourceIsTop, g.ports]; }))`)
	want := `"[[\"ping\",\"http://a.localhost:` + s.port + `\",true,true,0],[\"nested\",\"http://a.localhost:` + s.port + `\",true,true,0],[\"port\",\"http://a.localhost:` + s.port + `\",true,true,1]]"`
	if string(seen) != want {
		t.Fatalf("the child received\n%s\nwant\n%s", seen, want)
	}
}

func TestFrameBlockedByFramingHeaders(t *testing.T) {
	s := newSites(t, "a.localhost", "b.localhost")
	body := `<!doctype html><p id=t>secret page</p><script>window.ran = true</script>`
	s.page("b.localhost", "/deny.html", body, "X-Frame-Options", "DENY")
	s.page("b.localhost", "/same.html", body, "X-Frame-Options", "SAMEORIGIN")
	s.page("b.localhost", "/csp-none.html", body, "Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
	s.page("b.localhost", "/csp-self.html", body, "Content-Security-Policy", "frame-ancestors 'self'")
	s.page("b.localhost", "/csp-a.html", body, "Content-Security-Policy", "frame-ancestors http://a.localhost:"+s.port)
	s.page("b.localhost", "/csp-wild.html", body, "Content-Security-Policy", "frame-ancestors http://*.localhost:*")
	s.page("b.localhost", "/csp-other.html", body, "Content-Security-Policy", "frame-ancestors https://example.com")
	s.page("b.localhost", "/csp-overrides-xfo.html", body, "X-Frame-Options", "DENY", "Content-Security-Policy", "frame-ancestors *")
	s.page("b.localhost", "/free.html", body)
	frames := ""
	paths := []string{"deny", "same", "csp-none", "csp-self", "csp-a", "csp-wild", "csp-other", "csp-overrides-xfo", "free"}
	for _, p := range paths {
		frames += `<iframe src="` + s.u("b.localhost", "/"+p+".html") + `"></iframe>`
	}
	s.page("a.localhost", "/", `<!doctype html><body>`+frames+`</body>`)
	// framed by its own origin: SAMEORIGIN and 'self' are fine
	s.page("b.localhost", "/self-framed.html", `<!doctype html><iframe src="`+s.u("b.localhost", "/same.html")+`"></iframe><iframe src="`+s.u("b.localhost", "/csp-self.html")+`"></iframe><iframe src="`+s.u("b.localhost", "/deny.html")+`"></iframe>`)
	e := newSiteEngine(t)
	tab, err := e.NewTab(s.u("a.localhost", "/"))
	if err != nil {
		t.Fatal(err)
	}
	if !tab.WaitFrames(15 * time.Second) {
		t.Fatal("frames did not finish")
	}
	var got []string
	for i, f := range tab.Frames() {
		state := "shown"
		if f.Blocked {
			state = "blocked"
		}
		got = append(got, paths[i]+"="+state)
	}
	want := "deny=blocked same=blocked csp-none=blocked csp-self=blocked csp-a=shown csp-wild=shown csp-other=blocked csp-overrides-xfo=shown free=shown"
	if strings.Join(got, " ") != want {
		t.Fatalf("got  %s\nwant %s", strings.Join(got, " "), want)
	}
	// a blocked frame has no readable document and its script never ran; the shown one did run
	kids := tab.FrameTabs()
	if v, _ := kids[0].Evaluate(`typeof window.ran`); string(v) != `"undefined"` {
		t.Fatalf("script of a blocked frame ran: %s", v)
	}
	if v, _ := kids[8].Evaluate(`window.ran`); string(v) != "true" {
		t.Fatalf("script of the shown frame did not run: %s", v)
	}
	if !strings.Contains(consoleText(tab), "Refused to display") {
		t.Fatalf("no console message for the refused frame:\n%s", consoleText(tab))
	}
	// the same-origin rules: b.localhost framing itself
	tab2, err := e.NewTab(s.u("b.localhost", "/self-framed.html"))
	if err != nil {
		t.Fatal(err)
	}
	tab2.WaitFrames(10 * time.Second)
	var got2 []string
	for _, f := range tab2.Frames() {
		got2 = append(got2, map[bool]string{true: "blocked", false: "shown"}[f.Blocked])
	}
	if strings.Join(got2, " ") != "shown shown blocked" {
		t.Fatalf("self-framing: %v", got2)
	}
}

func TestFrameSandbox(t *testing.T) {
	s := newSites(t, "a.localhost")
	s.page("a.localhost", "/inner.html", `<!doctype html><p id=t>inner</p><script>window.ran = true; window.o = location.origin; try { localStorage.setItem('k', 'v'); window.ls = 'ok'; } catch (e) { window.ls = e.name; }</script>`)
	s.page("a.localhost", "/navtop.html", `<!doctype html><script>window.ran = true; window.top.location.href = '/two.html';</script>`)
	s.page("a.localhost", "/two.html", `<!doctype html><title>two</title>`)
	s.page("a.localhost", "/", `<!doctype html><body>
<iframe src="/inner.html" sandbox></iframe>
<iframe src="/inner.html" sandbox="allow-scripts"></iframe>
<iframe src="/inner.html" sandbox="allow-scripts allow-same-origin"></iframe>
<iframe src="/inner.html"></iframe>
<iframe src="/navtop.html" sandbox="allow-scripts"></iframe></body>`)
	e := newSiteEngine(t)
	tab, err := e.NewTab(s.u("a.localhost", "/"))
	if err != nil {
		t.Fatal(err)
	}
	tab.WaitFrames(10 * time.Second)
	kids := tab.FrameTabs()
	ev := func(k *Tab, expr string) string { r, _ := k.Evaluate(expr); return string(r) }
	if ev(kids[0], `typeof window.ran`) != `"undefined"` && kids[0].js != nil {
		t.Fatalf("a sandboxed frame without allow-scripts ran its script")
	}
	if ev(kids[1], `window.ran`) != "true" || ev(kids[1], `window.o`) != `"null"` || ev(kids[1], `window.ls`) != `"SecurityError"` {
		t.Fatalf("allow-scripts only: ran=%s origin=%s ls=%s", ev(kids[1], `window.ran`), ev(kids[1], `window.o`), ev(kids[1], `window.ls`))
	}
	if ev(kids[2], `window.o`) != `"http://a.localhost:`+s.port+`"` || ev(kids[2], `window.ls`) != `"ok"` {
		t.Fatalf("allow-scripts allow-same-origin: origin=%s ls=%s", ev(kids[2], `window.o`), ev(kids[2], `window.ls`))
	}
	if fs := tab.Frames(); len(fs) != 5 || fs[1].Origin != "" || fs[2].Origin == "" || !fs[0].Sandbox || fs[3].Sandbox {
		t.Fatalf("frame infos: %+v", fs)
	}
	got := js(t, tab, `const f = document.querySelectorAll('iframe'); let sec = ''; try { f[1].contentWindow.document; } catch (e) { sec = e.name; } return JSON.stringify([f[1].sandbox.value, sec, f[2].contentWindow.ran, f[3].contentWindow.ran, typeof f[1].sandbox.contains]);`)
	if got != `["allow-scripts","SecurityError",true,true,"function"]` {
		t.Fatalf("got %s\n%s", got, consoleText(tab))
	}
	// the sandboxed frame could not navigate the top-level page; the page is still the same one
	if !strings.Contains(consoleText(tab), "sandboxed frame") || !strings.HasSuffix(tab.URL(), "/") {
		t.Fatalf("expected a refusal to navigate the top-level page, url %s:\n%s", tab.URL(), consoleText(tab))
	}
	// the same script in a frame without a sandbox does navigate it
	s.page("a.localhost", "/free.html", `<!doctype html><body><iframe src="/navtop.html"></iframe></body>`)
	tab2, err := e.NewTab(s.u("a.localhost", "/free.html"))
	if err != nil {
		t.Fatal(err)
	}
	tab2.WaitFrames(10 * time.Second)
	for i := 0; i < 200 && !strings.HasSuffix(tab2.URL(), "/two.html"); i++ {
		tab2.Settle(20 * time.Millisecond) // the top-level page performs the navigation its frame asked for
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.HasSuffix(tab2.URL(), "/two.html") {
		t.Fatalf("an unsandboxed frame should be able to navigate the top page, url %s", tab2.URL())
	}
}

func TestFrameNavigationRemovalAndLimits(t *testing.T) {
	s := newSites(t, "a.localhost")
	s.page("a.localhost", "/one.html", `<!doctype html><p id=t>one</p>`)
	s.page("a.localhost", "/two.html", `<!doctype html><p id=t>two</p>`)
	s.page("a.localhost", "/", `<!doctype html><body><div id=host></div></body>`)
	e := newSiteEngine(t)
	tab, err := e.NewTab(s.u("a.localhost", "/"))
	if err != nil {
		t.Fatal(err)
	}
	got := js(t, tab, `
const r = {};
const f = document.createElement('iframe');
r.detached = f.contentWindow;
f.src = '/one.html';
const loaded = new Promise((res) => f.addEventListener('load', () => res(), { once: true }));
document.getElementById('host').appendChild(f);
await loaded;
r.one = f.contentDocument.getElementById('t').textContent;
const again = new Promise((res) => f.addEventListener('load', () => res(), { once: true }));
f.src = '/two.html'; await again;
r.two = f.contentDocument.getElementById('t').textContent;
const w = f.contentWindow;
f.remove();
r.removed = [f.contentWindow, f.contentDocument, w.closed];
// a frame made by innerHTML
document.getElementById('host').innerHTML = '<iframe id=h srcdoc="<b id=z>hi</b>"></iframe>';
await new Promise((r2) => setTimeout(r2, 300));
r.html = document.getElementById('h').contentDocument.getElementById('z').textContent;
return JSON.stringify(r);`)
	want := `{"detached":null,"one":"one","two":"two","removed":[null,null,true],"html":"hi"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\n%s", got, want, consoleText(tab))
	}
	// navigating the page closes its frames
	n := len(tab.FrameTabs())
	if n != 1 {
		t.Fatalf("want one frame, have %d", n)
	}
	kid := tab.FrameTabs()[0]
	if err := tab.Navigate(s.u("a.localhost", "/one.html"), nil); err != nil {
		t.Fatal(err)
	}
	if !kid.closed.Load() || len(tab.FrameTabs()) != 0 {
		t.Fatal("the frame of the old document was not closed")
	}
}
