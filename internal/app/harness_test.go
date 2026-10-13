package app

import (
	"context"
	"fmt"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/headless"
	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/shell"
)

type harness struct {
	t    testing.TB
	app  *App
	win  *headless.Window
	done chan error
	dir  string
	once sync.Once
}

type hopts struct {
	cfg   Config
	w, h  int
	scale float64
}

// newHarness runs a full app on a headless window with the placeholder chrome.
func newHarness(t testing.TB, o hopts) *harness {
	t.Helper()
	if o.w == 0 {
		o.w, o.h = 1000, 700
	}
	if o.scale == 0 {
		o.scale = 1
	}
	win := headless.New(o.w, o.h, o.scale)
	cfg := o.cfg
	cfg.Shell = win
	if cfg.ChromeFS == nil {
		cfg.ChromeFS = PlaceholderFS()
	}
	if cfg.Profile == nil && cfg.ProfileDir == "" {
		cfg.Profile = store.OpenIncognito()
		cfg.NoRestore = true
	}
	if cfg.DownloadsDir == "" {
		cfg.DownloadsDir = t.TempDir()
	}
	cfg.Logf = t.Logf
	a, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	PanicHook = func(r any) { t.Errorf("panic on a tab worker: %v\n%s", r, debug.Stack()) }
	h := &harness{t: t, app: a, win: win, done: make(chan error, 1), dir: t.TempDir()}
	go func() { h.done <- a.Run(context.Background()) }()
	t.Cleanup(h.close)
	return h
}

func (h *harness) close() {
	h.once.Do(func() {
		h.app.Close()
		select {
		case <-h.done:
		case <-time.After(15 * time.Second):
			h.t.Error("app did not stop")
		}
	})
}

func (h *harness) idle() {
	h.t.Helper()
	if !h.app.WaitIdle(120*time.Millisecond, 30*time.Second) {
		h.t.Fatal("app never went idle")
	}
}

func (h *harness) shot(name string) string {
	h.t.Helper()
	h.idle()
	dir := os.Getenv("SWEEF_SHOTS")
	if dir == "" {
		dir = h.dir
	}
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, name)
	if err := h.win.SavePNG(p); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func pageServer(t testing.TB) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Home Page</title></head><body style="margin:0;background:#fff">
<div style="height:120px;background:#d33a3a;color:#fff;font:28px sans-serif">Red banner</div>
<a href="/two" id="l" style="display:block;height:60px;background:#f2c94c;color:#000;font:20px sans-serif">go to two</a>
<a href="/blank" target="_blank" style="display:block;height:60px;background:#9b59b6;color:#fff;font:20px sans-serif">open in new tab</a>
<input id="f" type="text" style="display:block;width:300px;height:30px;margin:10px"></body></html>`)
	})
	mux.HandleFunc("/two", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Page Two</title></head><body style="margin:0"><div style="height:200px;background:#2f9e44">Green</div></body></html>`)
	})
	mux.HandleFunc("/blank", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Blank Target</title></head><body style="margin:0"><div style="height:200px;background:#17a2b8"></div></body></html>`)
	})
	mux.HandleFunc("/tall", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><title>Tall</title></head><body style="margin:0"><div style="height:1000px;background:#2255cc">top</div><div style="height:1000px;background:#cc8822">bottom</div></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s", what)
}

// ---- input helpers --------------------------------------------------------------------

func (h *harness) key(chord string) {
	h.t.Helper()
	for _, e := range headless.KeyEvents(chord) {
		h.win.Push(e)
	}
	h.idle()
}

// keyNoWait sends a chord without waiting for the app to go idle, for chords that end the app: a quitting app
// may never report idle, and the caller waits for h.done instead.
func (h *harness) keyNoWait(chord string) {
	h.t.Helper()
	for _, e := range headless.KeyEvents(chord) {
		h.win.Push(e)
	}
}

func (h *harness) typeText(s string) {
	h.t.Helper()
	for _, e := range headless.TypeEvents(s) {
		h.win.Push(e)
	}
	h.idle()
}

func (h *harness) click(x, y float64) {
	h.t.Helper()
	h.win.Push(shell.Event{Kind: shell.EvMouseMove, X: x, Y: y})
	h.win.Push(shell.Event{Kind: shell.EvMouseDown, X: x, Y: y})
	h.win.Push(shell.Event{Kind: shell.EvMouseUp, X: x, Y: y})
	h.idle()
}

func (h *harness) tab() state.Tab {
	t, _ := h.app.activeTab()
	return t
}

func (h *harness) px(x, y int) color.RGBA { return h.win.Screen().RGBAAt(x, y) }

func (h *harness) wantPx(x, y int, want string) {
	h.t.Helper()
	w, _ := ParseColor(want)
	g := h.px(x, y)
	if abs(int(g.R)-int(w.R)) > 3 || abs(int(g.G)-int(w.G)) > 3 || abs(int(g.B)-int(w.B)) > 3 {
		h.t.Fatalf("pixel (%d,%d) = #%02x%02x%02x, want %s", x, y, g.R, g.G, g.B, want)
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

func (h *harness) waitTitle(title string) {
	h.t.Helper()
	h.waitFor("title "+title, func() bool { return h.tab().Title == title })
	h.idle()
}

func (h *harness) tabIDs() []string { return h.app.model.Order(h.app.winID) }

// chromeEval evaluates JS in the chrome tab (through its worker) and decodes the JSON result.
func (h *harness) chromeEval(js string, out any) {
	h.t.Helper()
	c := h.app.chromeTab()
	if err := c.call(func() error { return c.eval(js, out) }); err != nil {
		h.t.Fatalf("chrome eval %q: %v", js, err)
	}
}

// pageEval evaluates JS in the active page tab.
func (h *harness) pageEval(js string, out any) {
	h.t.Helper()
	e := h.app.activePage()
	if err := e.call(func() error { return e.eval(js, out) }); err != nil {
		h.t.Fatalf("page eval %q: %v", js, err)
	}
}

// uiPost simulates the chrome page calling sweef.post(type, payload).
func (h *harness) uiPost(typ string, payload any) {
	h.t.Helper()
	h.chromeEval("sweef.post("+jsString(typ)+","+jsJSON(payload)+"); 1", nil)
	h.idle()
}

// loop runs f on the app's UI loop and waits, so tests can read loop-owned state without racing.
func (h *harness) loop(f func()) {
	h.t.Helper()
	done := make(chan struct{})
	h.app.post(func() { f(); close(done) })
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		h.t.Fatal("UI loop did not run the callback")
	}
}

func (h *harness) focusName() (s string) { h.loop(func() { s = h.app.focus }); return }
func (h *harness) vp() (r rect)          { h.loop(func() { r = h.app.vpLog }); return }
func (h *harness) ovlVisible() (b bool)  { h.loop(func() { b = h.app.ovl.visible() }); return }
func (h *harness) logicalW() (n int)     { h.loop(func() { n = h.app.wLog }); return }
func (h *harness) scaleV() (f float64)   { h.loop(func() { f = h.app.scale }); return }
func (h *harness) hasIncog() (b bool)    { h.loop(func() { b = h.app.incog != nil }); return }
func (h *harness) sidebarOpen() (b bool) { h.loop(func() { b = h.app.model.Sidebar.Open }); return }
func (h *harness) findOpen() (b bool)    { h.loop(func() { b = h.app.model.Find.Open }); return }
func (h *harness) animating() (b bool) {
	h.loop(func() { b = h.app.animOn.Load() || h.app.animT != nil })
	return
}
func (h *harness) state(k string) (v any) { h.loop(func() { v = h.app.lastState[k] }); return }
func (h *harness) pagePaints() int64      { return h.app.stats.pagePaints.Load() }
func (h *harness) stateTabs() (tabs []any) {
	h.loop(func() { tabs, _ = h.app.lastState["tabs"].([]any) })
	return
}

// targetAt reports where the router would send a pointer at logical (x,y).
func (h *harness) targetAt(x, y float64) (s string) {
	h.loop(func() { s = h.app.target(x, y) })
	return
}
