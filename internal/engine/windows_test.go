package engine

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func jsonOf(t *testing.T, tab *Tab, js string) string {
	t.Helper()
	raw, err := tab.Evaluate(js)
	if err != nil {
		t.Fatalf("evaluate %q: %v", js, err)
	}
	return string(raw)
}

// popupOf returns the popup tab opened by tab (the newest tab of the engine that is not tab).
func popupOf(t *testing.T, e *Engine, tab *Tab) *Tab {
	t.Helper()
	var p *Tab
	waitFor(t, "a popup tab", 3*time.Second, func() bool {
		for _, x := range e.ListTabs() {
			if x != tab {
				p = x
				return true
			}
		}
		return false
	})
	return p
}

// waitEval polls a JavaScript expression until it yields a non-empty string.
func waitEval(t *testing.T, tab *Tab, js string) string {
	t.Helper()
	var got string
	waitFor(t, js, 5*time.Second, func() bool {
		raw, err := tab.Evaluate(js)
		if err != nil || len(raw) == 0 || string(raw) == "null" {
			return false
		}
		got = strings.Trim(string(raw), `"`)
		return got != ""
	})
	return got
}

func waitURL(t *testing.T, tab *Tab, suffix string) {
	t.Helper()
	waitFor(t, "navigation to "+suffix, 5*time.Second, func() bool { return strings.HasSuffix(tab.URL(), suffix) })
}

// ---- the popup blocker ---------------------------------------------------------------------

func TestWindowOpenBlockedWithoutUserActivation(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<!doctype html><button id=b>x</button>`, "/popup": `<!doctype html><title>pop</title>hello`})
	e := newEng(t)
	tab, err := e.NewTab(s.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, tab, `window.open('/popup', '_blank') === null`); got != "true" {
		t.Fatalf("window.open without a gesture returned a window: %s", got)
	}
	if n := len(e.ListTabs()); n != 1 {
		t.Fatalf("a popup was created without a gesture (%d tabs)", n)
	}
	bl := tab.BlockedPopups()
	if len(bl) != 1 || !bl[0].Blocked || !strings.HasSuffix(bl[0].URL, "/popup") || bl[0].Reason == "" {
		t.Fatalf("blocked popup not recorded: %+v", bl)
	}
	// script-dispatched events are not user gestures either
	if got := jsonOf(t, tab, `document.getElementById('b').click(); window.open('/popup') === null`); got != "true" {
		t.Fatalf("element.click() granted activation: %s", got)
	}
}

func TestWindowOpenAllowedAfterUserActivation(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<!doctype html><button id=b>x</button>`, "/popup": `<!doctype html><title>pop</title>hello`})
	e := newEng(t)
	tab, err := e.NewTab(s.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	tab.NoteUserActivation()
	if got := jsonOf(t, tab, `(function(){ window.w = window.open('/popup', '_blank'); return window.w !== null && window.w.closed === false; })()`); got != "true" {
		t.Fatalf("window.open after a gesture: %s", got)
	}
	p := popupOf(t, e, tab)
	waitURL(t, p, "/popup")
	if p.Opener() != tab {
		t.Fatal("popup does not know its opener")
	}
	if got := jsonOf(t, p, `window.opener !== null && window.opener.closed === false`); got != "true" {
		t.Fatalf("window.opener inside the popup: %s", got)
	}
	// the gesture is consumed: a second popup from the same click is blocked
	if got := jsonOf(t, tab, `window.open('/popup') === null`); got != "true" {
		t.Fatalf("transient activation was not consumed: %s", got)
	}
	// the window is closed by the opener's reference
	if got := jsonOf(t, tab, `(window.w.close(), true)`); got != "true" {
		t.Fatal(got)
	}
	waitFor(t, "popup closed", 3*time.Second, func() bool { return p.WindowClosed() })
	if got := jsonOf(t, tab, `window.w.closed`); got != "true" {
		t.Fatalf("opener still sees an open window: %s", got)
	}
}

func TestWindowOpenActivationExpires(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<!doctype html>x`})
	e := newEng(t)
	tab, _ := e.NewTab(s.URL + "/")
	tab.SetActivationWindow(80 * time.Millisecond)
	tab.NoteUserActivation()
	time.Sleep(160 * time.Millisecond)
	if got := jsonOf(t, tab, `window.open('/x') === null`); got != "true" {
		t.Fatalf("an expired gesture allowed a popup: %s", got)
	}
}

func TestClickOnLinkWithTargetBlankOpensNoOpenerWindow(t *testing.T) {
	s := newSite(t, map[string]string{
		"/":  `<!doctype html><a id=a target=_blank href="/p">plain</a><a id=o target=_blank rel=opener href="/p">opener</a><a id=n target=_blank rel=noopener href="/p">n</a>`,
		"/p": `<!doctype html><title>p</title>p`,
	})
	e := newEng(t)
	tab, _ := e.NewTab(s.URL + "/")
	if err := tab.Click("#a"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second tab", 3*time.Second, func() bool { return len(e.ListTabs()) == 2 })
	p := e.ListTabs()[1]
	waitURL(t, p, "/p")
	if p.Opener() != nil {
		t.Fatal("target=_blank must not give the new window an opener by default")
	}
	if err := tab.Click("#o"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "third tab", 3*time.Second, func() bool { return len(e.ListTabs()) == 3 })
	if p2 := e.ListTabs()[2]; p2.Opener() != tab {
		t.Fatal("rel=opener must keep the opener")
	}
	if err := tab.Click("#n"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fourth tab", 3*time.Second, func() bool { return len(e.ListTabs()) == 4 })
	if p3 := e.ListTabs()[3]; p3.Opener() != nil {
		t.Fatal("rel=noopener must cut the opener")
	}
}

func TestWindowOpenNoopener(t *testing.T) {
	s := newSite(t, map[string]string{"/": `<!doctype html>x`, "/p": `<!doctype html>p`})
	e := newEng(t)
	tab, _ := e.NewTab(s.URL + "/")
	tab.NoteUserActivation()
	if got := jsonOf(t, tab, `window.open('/p', '_blank', 'noopener') === null`); got != "true" {
		t.Fatalf("noopener window.open must return null: %s", got)
	}
	waitFor(t, "popup", 3*time.Second, func() bool { return len(e.ListTabs()) == 2 })
	p := e.ListTabs()[1]
	waitURL(t, p, "/p")
	if p.Opener() != nil {
		t.Fatal("noopener popup has an opener")
	}
	if got := jsonOf(t, p, `window.opener === null`); got != "true" {
		t.Fatalf("window.opener in a noopener popup: %s", got)
	}
}

// ---- named targets ---------------------------------------------------------------------------

func TestNamedTargetsReuseWindow(t *testing.T) {
	s := newSite(t, map[string]string{
		"/":   `<!doctype html><a id=a target=win1 href="/p1">1</a><a id=b target=win1 href="/p2">2</a><a id=c target=other href="/p3">3</a>`,
		"/p1": `<!doctype html><title>one</title>one`,
		"/p2": `<!doctype html><title>two</title>two`,
		"/p3": `<!doctype html><title>three</title>three`,
	})
	e := newEng(t)
	tab, _ := e.NewTab(s.URL + "/")
	if err := tab.Click("#a"); err != nil {
		t.Fatal(err)
	}
	p := popupOf(t, e, tab)
	waitURL(t, p, "/p1")
	if p.WindowName() != "win1" {
		t.Fatalf("window name = %q", p.WindowName())
	}
	if err := tab.Click("#b"); err != nil {
		t.Fatal(err)
	}
	p.Settle(20 * time.Millisecond) // the navigation runs on the popup's own loop
	waitURL(t, p, "/p2")
	if n := len(e.ListTabs()); n != 2 {
		t.Fatalf("the named window was not reused: %d tabs", n)
	}
	// a different name opens another window
	if err := tab.Click("#c"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "third tab", 3*time.Second, func() bool { return len(e.ListTabs()) == 3 })
	// window.open with the same name returns the same window object
	tab.NoteUserActivation()
	if got := jsonOf(t, tab, `(function(){ var a = window.open('/p1', 'win1'); var b = window.open('/p1', 'win1'); return a !== null && a === b; })()`); got != "true" {
		// the second call needs its own gesture in a browser; the first one reuses win1 and consumes it
		t.Logf("same-name window.open identity: %s", got)
	}
	tab.NoteUserActivation()
	if got := jsonOf(t, tab, `(function(){ window.x1 = window.open('', 'win1'); return window.x1 !== null; })()`); got != "true" {
		t.Fatalf("window.open by name: %s", got)
	}
	tab.NoteUserActivation()
	if got := jsonOf(t, tab, `window.open('', 'win1') === window.x1`); got != "true" {
		t.Fatalf("proxies of one window are not identical: %s", got)
	}
}

func TestLinkTargetsNamedFrame(t *testing.T) {
	a := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0;background:#f00">`, "/b": `<!doctype html><body style="margin:0;background:#00f">`})
	top := newSite(t, map[string]string{"/": `<!doctype html><body style="margin:0"><a id=l target=f href="` + a.URL + `/b" style="position:absolute;left:0;top:0">go</a>
<iframe name=f src="` + a.URL + `/" style="border:0;position:absolute;left:0;top:50px;width:100px;height:60px"></iframe>`})
	e := newEng(t)
	tab, _ := e.NewTab(top.URL + "/")
	img, _ := tab.PaintRGBA(200, 150, 1, false)
	if c := px(img, 50, 80); !isColor(c, 255, 0, 0) {
		t.Fatalf("frame before: %v", c)
	}
	if err := tab.Click("#l"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.ListTabs()); n != 1 {
		t.Fatalf("a link targeting a frame opened a window (%d tabs)", n)
	}
	c := firstFrame(t, tab)
	waitFor(t, "frame navigation", 5*time.Second, func() bool { return strings.HasSuffix(c.URL(), "/b") })
	waitFor(t, "frame repaint", 5*time.Second, func() bool {
		img, _ := tab.PaintRGBA(200, 150, 1, false)
		return isColor(px(img, 50, 80), 0, 0, 255)
	})
}

// ---- OAuth-style popup round trip ---------------------------------------------------------

func TestOAuthPopupRoundTrip(t *testing.T) {
	app := newSite(t, nil)
	idp := newSite(t, nil)
	app.set("/", `<!doctype html><button id="login">Sign in</button>
<script>
window.msgs = [];
window.addEventListener('message', function (e) { window.msgs.push({origin: e.origin, data: e.data, fromPopup: e.source === window.popup}); });
document.getElementById('login').addEventListener('click', function () {
  window.popup = window.open('`+idp.URL+`/authorize?state=xyz', 'oauth', 'width=500,height=600');
  window.popupOpened = window.popup !== null;
});
</script>`)
	// the identity provider shows a page that talks to the opener, then redirects back with a code
	idp.set("/authorize", `<!doctype html><title>Sign in</title><script>
window.opener.postMessage({step: 'authorize', hasOpener: window.opener !== null, name: window.name}, '`+app.URL+`');
setTimeout(function () { location.href = '/grant'; }, 20);
</script>`)
	idp.set("/grant", "REDIRECT "+app.URL+"/callback?code=abc123&state=xyz")
	app.set("/callback", `<!doctype html><title>done</title><script>
var code = new URLSearchParams(location.search).get('code');
window.opener.postMessage({type: 'oauth', token: 'tok-' + code}, '`+app.URL+`');
window.close();
</script>`)

	e := newEng(t)
	tab, err := e.NewTab(app.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	if err := tab.Click("#login"); err != nil {
		t.Fatal(err)
	}
	if got := jsonOf(t, tab, `window.popupOpened`); got != "true" {
		t.Fatalf("login click did not open the popup: %s", got)
	}
	p := popupOf(t, e, tab)
	// the popup travels app -> idp (cross-origin) -> app (back on the opener's origin) and closes itself
	waitFor(t, "popup to close itself", 10*time.Second, func() bool { return p.WindowClosed() })
	waitFor(t, "both messages", 5*time.Second, func() bool {
		tab.Settle(20 * time.Millisecond)
		return jsonOf(t, tab, `window.msgs.length`) == "2"
	})
	var msgs []struct {
		Origin    string         `json:"origin"`
		Data      map[string]any `json:"data"`
		FromPopup bool           `json:"fromPopup"`
	}
	if err := json.Unmarshal([]byte(jsonOf(t, tab, `window.msgs`)), &msgs); err != nil {
		t.Fatal(err)
	}
	if msgs[0].Origin != idp.URL || msgs[0].Data["hasOpener"] != true || msgs[0].Data["name"] != "oauth" || !msgs[0].FromPopup {
		t.Fatalf("message from the identity provider page: %+v", msgs[0])
	}
	if msgs[1].Origin != app.URL || msgs[1].Data["token"] != "tok-abc123" || !msgs[1].FromPopup {
		t.Fatalf("message from the callback page: %+v", msgs[1])
	}
	if got := jsonOf(t, tab, `window.popup.closed`); got != "true" {
		t.Fatalf("popup.closed after window.close(): %s", got)
	}
}

func TestPostMessageTargetOriginIsEnforced(t *testing.T) {
	app := newSite(t, nil)
	other := newSite(t, nil)
	app.set("/", `<!doctype html><script>window.msgs = []; window.addEventListener('message', function (e) { window.msgs.push(e.data); });</script>`)
	app.set("/pop", `<!doctype html><script>
window.opener.postMessage('wrong', '`+other.URL+`');
window.opener.postMessage('star', '*');
window.opener.postMessage('slash', '/');
window.opener.postMessage({d: new Date(86400000), m: new Map([[1, 'a']]), s: new Set([2]), u: undefined, n: NaN, b: new Uint8Array([1,2,3])}, '`+app.URL+`');
</script>`)
	e := newEng(t)
	tab, _ := e.NewTab(app.URL + "/")
	tab.NoteUserActivation()
	jsonOf(t, tab, `window.open('/pop'); 1`)
	p := popupOf(t, e, tab)
	waitURL(t, p, "/pop")
	waitFor(t, "messages", 5*time.Second, func() bool {
		tab.Settle(20 * time.Millisecond)
		return jsonOf(t, tab, `window.msgs.length`) == "3"
	})
	if got := jsonOf(t, tab, `window.msgs[0] + ',' + window.msgs[1]`); got != `"star,slash"` {
		t.Fatalf("delivered %s (a mismatched targetOrigin must be dropped)", got)
	}
	got := jsonOf(t, tab, `(function(){ var m = window.msgs[2]; return [m.d instanceof Date && m.d.getTime(), m.m instanceof Map && m.m.get(1), m.s instanceof Set && m.s.has(2), 'u' in m ? 'has-u' : 'no-u', Number.isNaN(m.n), m.b instanceof Uint8Array && m.b.length].join('|'); })()`)
	if got != `"86400000|a|true|no-u|true|3"` && got != `"86400000|a|true|has-u|true|3"` {
		t.Fatalf("structured clone: %s", got)
	}
}

// ---- COOP ---------------------------------------------------------------------------------------

func TestCOOPSameOriginAllowPopupsKeepsOpenerForUnsafeNonePopup(t *testing.T) {
	app := newSite(t, nil)
	idp := newSite(t, nil)
	app.set("/", `<!doctype html>app`)
	app.setHeader("/", "Cross-Origin-Opener-Policy", "same-origin-allow-popups")
	idp.set("/login", `<!doctype html><script>window.report = String(window.opener !== null);</script>`)
	e := newEng(t)
	tab, _ := e.NewTab(app.URL + "/")
	tab.NoteUserActivation()
	jsonOf(t, tab, `window.open('`+idp.URL+`/login','idp'); 1`)
	p := popupOf(t, e, tab)
	waitURL(t, p, "/login")
	if p.Opener() != tab {
		t.Fatal("same-origin-allow-popups opener lost its popup")
	}
	if got := waitEval(t, p, `window.report`); got != "true" {
		t.Fatalf("window.opener in the popup: %s", got)
	}
}

func TestCOOPSameOriginSeversOpener(t *testing.T) {
	app := newSite(t, nil)
	idp := newSite(t, nil)
	app.set("/", `<!doctype html>app`)
	idp.set("/login", `<!doctype html><script>window.report = String(window.opener);</script>`)
	idp.setHeader("/login", "Cross-Origin-Opener-Policy", "same-origin")
	e := newEng(t)
	tab, _ := e.NewTab(app.URL + "/")
	tab.NoteUserActivation()
	jsonOf(t, tab, `window.w = window.open('`+idp.URL+`/login','idp'); 1`)
	p := popupOf(t, e, tab)
	waitURL(t, p, "/login")
	waitFor(t, "opener severed", 3*time.Second, func() bool { return p.win.severed.Load() })
	if p.Opener() != nil {
		t.Fatal("COOP: same-origin did not sever the opener")
	}
	if got := waitEval(t, p, `window.report`); got != "null" {
		t.Fatalf("window.opener in a COOP popup: %s", got)
	}
	if got := jsonOf(t, tab, `window.w.closed`); got != "true" {
		t.Fatalf("the opener must see the severed window as closed: %s", got)
	}
	// and a message to it goes nowhere
	jsonOf(t, tab, `window.w.postMessage('x', '*'); 1`)
	if p.inboxLen() != 0 {
		t.Fatal("message reached a severed window")
	}
}

func TestCOOPMatchTable(t *testing.T) {
	a, b := "http://a.test", "http://b.test"
	for _, c := range []struct {
		op, oo, pp, po string
		keep           bool
	}{
		{"unsafe-none", a, "unsafe-none", b, true},
		{"same-origin-allow-popups", a, "unsafe-none", b, true},
		{"same-origin", a, "unsafe-none", b, false},
		{"unsafe-none", a, "same-origin", b, false},
		{"same-origin", a, "same-origin", a, true},
		{"same-origin", a, "same-origin", b, false},
		{"same-origin-allow-popups", a, "same-origin-allow-popups", a, true},
		{"same-origin-allow-popups", a, "same-origin-allow-popups", b, false},
		{"unsafe-none", a, "same-origin-allow-popups", b, false},
		{"same-origin", a, "same-origin-allow-popups", a, false},
	} {
		if got := coopMatch(c.op, c.oo, c.pp, c.po); got != c.keep {
			t.Errorf("coopMatch(%s %s, %s %s) = %v, want %v", c.op, c.oo, c.pp, c.po, got, c.keep)
		}
	}
	for in, want := range map[string]string{"": "unsafe-none", "same-origin": "same-origin", "Same-Origin; report-to=\"x\"": "same-origin", "same-origin-allow-popups": "same-origin-allow-popups", "bogus": "unsafe-none"} {
		if got := parseCOOP(in); got != want {
			t.Errorf("parseCOOP(%q) = %q, want %q", in, got, want)
		}
	}
}
