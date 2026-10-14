package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
)

// tinySite serves a mutable set of pages (a second one is a second origin).
type tinySite struct {
	*httptest.Server
	mu       sync.Mutex
	pages    map[string]string
	handlers map[string]http.HandlerFunc
}

func newTinySite(t testing.TB) *tinySite {
	s := &tinySite{pages: map[string]string{}, handlers: map[string]http.HandlerFunc{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		body, ok := s.pages[r.URL.Path]
		fn := s.handlers[r.URL.Path]
		s.mu.Unlock()
		if fn != nil {
			fn(w, r)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(body, "REDIRECT ") {
			http.Redirect(w, r, strings.TrimPrefix(body, "REDIRECT "), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *tinySite) handle(path string, fn http.HandlerFunc) {
	s.mu.Lock()
	s.handlers[path] = fn
	s.mu.Unlock()
}

func (s *tinySite) set(path, body string) {
	s.mu.Lock()
	s.pages[path] = body
	s.mu.Unlock()
}

// The page's iframe sits at (0,100) of the page and is 400x200; its green box fills the frame.
func TestIframeIsPaintedClickableAndTypeable(t *testing.T) {
	child := newTinySite(t)
	child.set("/", `<!doctype html><title>child</title><body style="margin:0;background:#2f9e44">
<button id="b" style="position:absolute;left:20px;top:20px;width:100px;height:40px;background:#fff">go</button>
<input id="i" style="position:absolute;left:20px;top:100px;width:200px;height:30px">
<script>
window.clicks = 0;
document.getElementById('b').addEventListener('click', function () { window.clicks++; });
</script></body>`)
	top := newTinySite(t)
	top.set("/", `<!doctype html><title>Top</title><body style="margin:0;background:#fff">
<div style="height:100px;background:#d33a3a"></div>
<iframe src="`+child.URL+`/" style="display:block;border:0;width:400px;height:200px"></iframe></body>`)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{top.URL + "/"}}})
	h.waitTitle("Top")
	// the frame's pixels are in the window
	h.waitFor("frame content in the window", func() bool { return near(h.px(300, vpTop+250), "#2f9e44") })
	h.wantPx(300, vpTop+50, "#d33a3a") // the parent's own content is still there
	h.wantPx(500, vpTop+250, "#ffffff")

	fr := h.app.activePage().t.Frames()
	if len(fr) != 1 {
		t.Fatalf("frames: %d", len(fr))
	}
	ct := fr[0].Tab
	clicks := func() string {
		raw, err := ct.Evaluate("String(window.clicks)")
		if err != nil {
			t.Fatal(err)
		}
		return strings.Trim(string(raw), `"`)
	}
	// click the frame's button with the mouse: window (50, vpTop+100+30) -> frame (50,30)
	h.click(50, vpTop+100+30)
	h.waitFor("click in the frame", func() bool { return clicks() == "1" })
	// type into the frame's input: click it, then text events
	h.click(50, vpTop+100+115)
	h.typeText("hello")
	h.waitFor("typing in the frame", func() bool {
		raw, _ := ct.Evaluate("document.getElementById('i').value")
		return strings.Trim(string(raw), `"`) == "hello"
	})
	// a click on the page's own area does not reach the frame
	h.click(300, vpTop+50)
	if got := clicks(); got != "1" {
		t.Fatalf("frame clicks = %s", got)
	}
}

func TestOAuthPopupInTheBrowser(t *testing.T) {
	app := newTinySite(t)
	idp := newTinySite(t)
	app.set("/", `<!doctype html><title>App</title><body style="margin:0;background:#fff">
<div id="login" style="height:200px;background:#f2c94c;font:20px sans-serif">Sign in</div>
<script>
window.msgs = [];
window.addEventListener('message', function (e) { window.msgs.push({origin: e.origin, data: e.data}); });
document.getElementById('login').addEventListener('click', function () {
  window.popup = window.open('`+idp.URL+`/authorize', 'oauth', 'width=500,height=600');
});
</script></body>`)
	// the identity provider page waits at its login form until the test lets it go on
	gate := make(chan struct{})
	idp.handle("/gate", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-gate:
			fmt.Fprint(w, "go")
		default:
			fmt.Fprint(w, "wait")
		}
	})
	idp.set("/authorize", `<!doctype html><title>IdP</title><body style="background:#2255cc"><script>
window.opener.postMessage({step: 'authorize', hasOpener: !!window.opener}, '`+app.URL+`');
(function poll() {
  var x = new XMLHttpRequest();
  x.open('GET', '/gate'); x.onload = function () { if (x.responseText === 'go') location.href = '/grant'; else setTimeout(poll, 40); };
  x.send();
})();
</script></body>`)
	idp.set("/grant", "REDIRECT "+app.URL+"/callback?code=zz")
	app.set("/callback", `<!doctype html><title>Done</title><script>
window.opener.postMessage({type: 'oauth', token: 'tok-' + new URLSearchParams(location.search).get('code')}, '`+app.URL+`');
window.close();
</script>`)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{app.URL + "/"}}})
	h.waitTitle("App")
	openerID := h.app.model.Active(h.app.winID)
	// a user click on "Sign in" opens the popup (no waiting for idle: the popup's login page keeps polling)
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: 100, Y: vpTop + 50})
	h.win.Push(shell.Event{Kind: shell.EvMouseDown, X: 100, Y: vpTop + 50})
	h.win.Push(shell.Event{Kind: shell.EvMouseUp, X: 100, Y: vpTop + 50})
	h.waitFor("popup tab", func() bool { return len(h.tabIDs()) == 2 })
	h.waitFor("popup to show the identity provider", func() bool {
		tb := h.tab()
		return tb.ID != openerID && tb.Popup
	})
	if ids := h.tabIDs(); ids[0] != openerID {
		t.Fatalf("popup must sit next to the opener: %v", ids)
	}
	h.waitFor("the identity provider's page", func() bool { return h.tab().Title == "IdP" })
	close(gate)
	// the popup visits the identity provider, comes back to the app's origin and closes itself
	h.waitFor("popup to close itself", func() bool { return len(h.tabIDs()) == 1 })
	h.waitFor("opener active again", func() bool { return h.app.model.Active(h.app.winID) == openerID })
	var n int
	h.waitFor("both messages in the opener", func() bool {
		h.pageEval(`window.msgs.length`, &n)
		return n == 2
	})
	var s string
	h.pageEval(`window.msgs.map(function (m) { return m.origin + '|' + (m.data.step || m.data.token); }).join(',')`, &s)
	if s != idp.URL+"|authorize,"+app.URL+"|tok-zz" {
		t.Fatalf("messages: %q", s)
	}
	var closed bool
	h.pageEval(`window.popup.closed`, &closed)
	if !closed {
		t.Fatal("popup.closed is false after the popup closed itself")
	}
}

func TestPopupWithoutGestureIsBlockedWithAToast(t *testing.T) {
	srv := newTinySite(t)
	srv.set("/", `<!doctype html><title>Home</title><body>x</body>`)
	srv.set("/p", `<!doctype html><title>P</title>p`)
	h := newHarness(t, hopts{cfg: Config{InitialURLs: []string{srv.URL + "/"}}})
	h.waitTitle("Home")
	time.Sleep(2300 * time.Millisecond) // no gesture for longer than the activation window
	var isNull bool
	h.pageEval(`window.open('/p') === null`, &isNull)
	h.idle()
	if !isNull || len(h.tabIDs()) != 1 {
		t.Fatalf("popup without a gesture: null=%v tabs=%d", isNull, len(h.tabIDs()))
	}
}
