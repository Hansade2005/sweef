package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
)

func scrollServer(t testing.TB) *httptest.Server {
	mux := http.NewServeMux()
	page := func(title, body string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `<!doctype html><html><head><title>%s</title></head><body style="margin:0;background:#fff;font:16px sans-serif">%s</body></html>`, title, body)
		}
	}
	// a scroller inside a long page
	mux.HandleFunc("/chain", page("Chain", `<div id="inner" style="height:200px;overflow:auto;background:#eee"><div style="height:1000px;background:#aa0000">inner content</div></div>
<div style="height:3000px;background:#00aa00">outer</div>`))
	// the same, with overscroll-behavior: contain
	mux.HandleFunc("/contain", page("Contain", `<div id="inner" style="height:200px;overflow:auto;overscroll-behavior:contain;background:#eee"><div style="height:1000px;background:#aa0000">inner content</div></div>
<div style="height:3000px;background:#00aa00">outer</div>`))
	// wheel listeners
	mux.HandleFunc("/wheelcancel", page("WheelCancel", `<div style="height:4000px;background:#2255cc">tall</div>
<script>window.log=[];addEventListener('wheel',function(e){log.push(['win',e.deltaY,e.cancelable,e.clientX,e.clientY]);},{passive:true});
document.body.addEventListener('wheel',function(e){ if(window.block) e.preventDefault(); log.push(['body',e.deltaY,e.defaultPrevented]); },{passive:false});</script>`))
	mux.HandleFunc("/wheelpassive", page("WheelPassive", `<div style="height:4000px;background:#2255cc">tall</div>
<script>window.log=[];addEventListener('wheel',function(e){ e.preventDefault(); log.push(['win',e.deltaY,e.defaultPrevented]); });</script>`))
	// script scrolling
	mux.HandleFunc("/script", page("Script", `<div style="height:600px;background:#2255cc">a</div><div id="t1" style="height:600px;background:#cc8822">b</div><div id="t2" style="height:2000px;background:#22aa55">c</div>
<script>window.ev=[];addEventListener('scroll',function(){ev.push('scroll:'+Math.round(scrollY));});addEventListener('scrollend',function(){ev.push('scrollend:'+Math.round(scrollY));});</script>`))
	mux.HandleFunc("/smooth", page("Smooth", `<style>html{scroll-behavior:smooth}</style><div style="height:600px;background:#2255cc">a</div><div id="t1" style="height:3000px;background:#cc8822">b</div>`))
	mux.HandleFunc("/anchor", page("Anchor", `<a id="go" href="#target" style="display:block;height:40px">go to target</a><div style="height:1500px;background:#2255cc">a</div><h2 id="target" style="margin:0;height:60px;background:#cc2222">target</h2><div style="height:3000px;background:#22aa55">c</div>`))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func (h *harness) loadScrollPage(srv *httptest.Server, path, title string) {
	h.t.Helper()
	h.app.post(func() { h.app.navigateInput(h.app.model.Active(h.app.winID), srv.URL+path) })
	h.waitTitle(title)
	h.idle()
}

func (h *harness) wheel(x, y, dy float64, mods shell.Mods) {
	h.win.Push(shell.Event{Kind: shell.EvWheel, X: x, Y: y, DY: dy, Mods: mods})
}

func (h *harness) pageScroll() int { p, _ := h.app.comp.Scroll(); return p }

func (h *harness) evalInt(js string) int {
	h.t.Helper()
	var v float64
	h.pageEval(js, &v)
	return int(v)
}

func newScrollHarness(t *testing.T, path, title string) (*harness, *httptest.Server) {
	srv := scrollServer(t)
	h := newHarness(t, hopts{})
	h.loadScrollPage(srv, path, title)
	return h, srv
}

func TestWheelPreventDefaultKeepsTheViewStill(t *testing.T) {
	h, _ := newScrollHarness(t, "/wheelcancel", "WheelCancel")
	x, y := 400.0, float64(vpTop)+100
	// the first wheel of a document asks the page, which has a listener that could cancel: wait for its answer
	h.wheel(x, y, 200, 0)
	h.idle()
	if got := h.pageScroll(); got < 150 {
		t.Fatalf("a wheel turn the page did not cancel must scroll, at %d", got)
	}
	// the page now cancels
	h.pageEval("window.block = true; 1", nil)
	before := h.pageScroll()
	h.wheel(x, y, 300, 0)
	h.idle()
	if got := h.pageScroll(); got != before {
		t.Fatalf("preventDefault on the wheel event must stop the scroll: %d -> %d", before, got)
	}
	var log [][]any
	h.pageEval("window.log", &log)
	if len(log) < 4 {
		t.Fatalf("both listeners should have seen both events: %v", log)
	}
	var sawBody bool
	for _, l := range log {
		if l[0] == "body" && l[1].(float64) == 300 && l[2] == true {
			sawBody = true // the body listener cancelled it and saw it cancelled
		}
	}
	if !sawBody {
		t.Fatalf("no body event with deltaY 300 and defaultPrevented: %v", log)
	}
	// the window listener is passive and the body one is not: cancelable at the body, and the body saw it cancelled
	if log[0][0] != "body" || log[1][0] != "win" || log[1][2] != true {
		t.Fatalf("the event must be cancelable and reach both listeners: %v", log[:2])
	}
	// the page lets go again
	h.pageEval("window.block = false; 1", nil)
	h.wheel(x, y, 300, 0)
	h.idle()
	if got := h.pageScroll(); got <= before {
		t.Fatalf("scrolling resumes when the page stops cancelling: %d", got)
	}
}

func TestPassiveWindowWheelListenerCannotCancel(t *testing.T) {
	h, _ := newScrollHarness(t, "/wheelpassive", "WheelPassive")
	// a wheel listener on window is passive by default: preventDefault is ignored, and the scroll does not wait for the page
	h.wheel(400, float64(vpTop)+100, 240, 0)
	h.idle()
	if got := h.pageScroll(); got < 200 {
		t.Fatalf("a passive listener must not stop the scroll, at %d", got)
	}
	var log [][]any
	h.pageEval("window.log", &log)
	if len(log) != 1 || log[0][2] != false {
		t.Fatalf("the page should have seen one event whose preventDefault was ignored: %v", log)
	}
	// from now on the scroll does not wait for the page: with its worker busy the view still moves at once
	done := make(chan struct{})
	e := h.app.activePage()
	e.w.do(func() { <-done })
	before := h.pageScroll()
	h.wheel(400, float64(vpTop)+100, 240, 0)
	h.waitFor("scroll while the page's worker is blocked", func() bool { return h.pageScroll() > before+100 })
	close(done)
}

func TestWheelOverInnerScrollerChainsToThePage(t *testing.T) {
	h, _ := newScrollHarness(t, "/chain", "Chain")
	x, y := 200.0, float64(vpTop)+60 // inside #inner
	h.wheel(x, y, 300, 0)
	h.idle()
	if got := h.evalInt("document.getElementById('inner').scrollTop"); got < 280 || got > 320 {
		t.Fatalf("the inner scroller takes the wheel first: scrollTop %d", got)
	}
	if h.pageScroll() != 0 {
		t.Fatalf("the page must not move while the inner scroller can: %d", h.pageScroll())
	}
	// the same swipe goes on past the inner scroller's end: it stays with the inner scroller (latched)
	h.wheel(x, y, 900, 0)
	h.wheel(x, y, 300, 0)
	h.idle()
	if got := h.evalInt("document.getElementById('inner').scrollTop"); got != 800 {
		t.Fatalf("inner scrollTop %d, want its maximum 800", got)
	}
	if h.pageScroll() != 0 {
		t.Fatalf("a gesture does not leave the scroller it started in: page at %d", h.pageScroll())
	}
	// a new gesture over the exhausted inner scroller scrolls the page
	time.Sleep(wheelLatchFor + 150*time.Millisecond)
	h.wheel(x, y, 400, 0)
	h.idle()
	if got := h.pageScroll(); got < 350 {
		t.Fatalf("a new gesture over an inner scroller at its end must scroll the page, at %d", got)
	}
	if got := h.evalInt("document.getElementById('inner').scrollTop"); got != 800 {
		t.Fatalf("inner scrollTop moved to %d", got)
	}
	// scrolling back up: the page moved, so the pointer is now over other content; the inner scroller is chained
	// from the other end when it is under the pointer again
	h.shot("chain.png")
}

func TestOverscrollBehaviorContainStopsTheChain(t *testing.T) {
	h, _ := newScrollHarness(t, "/contain", "Contain")
	x, y := 200.0, float64(vpTop)+60
	h.wheel(x, y, 1500, 0)
	h.idle()
	if got := h.evalInt("document.getElementById('inner').scrollTop"); got != 800 {
		t.Fatalf("inner scrollTop %d", got)
	}
	time.Sleep(wheelLatchFor + 150*time.Millisecond)
	h.wheel(x, y, 500, 0)
	h.idle()
	if got := h.pageScroll(); got != 0 {
		t.Fatalf("overscroll-behavior: contain must keep the wheel from scrolling the page behind, at %d", got)
	}
	// the other direction still scrolls the inner scroller
	h.wheel(x, y, -200, 0)
	h.idle()
	if got := h.evalInt("document.getElementById('inner').scrollTop"); got > 650 {
		t.Fatalf("scrolling back up: scrollTop %d", got)
	}
}

func TestScriptScrollAndEvents(t *testing.T) {
	h, _ := newScrollHarness(t, "/script", "Script")
	h.pageEval("scrollTo(0, 700); 1", nil)
	h.idle()
	if got := h.pageScroll(); got != 700 {
		t.Fatalf("window.scrollTo(0,700): scroll %d", got)
	}
	if got := h.evalInt("scrollY"); got != 700 {
		t.Fatalf("scrollY %d", got)
	}
	h.pageEval("scrollBy({top: 100}); 1", nil)
	h.idle()
	if got := h.pageScroll(); got != 800 {
		t.Fatalf("scrollBy: %d", got)
	}
	// rects are relative to the viewport, as page script expects
	var top float64
	h.pageEval("document.getElementById('t1').getBoundingClientRect().top", &top)
	if top != -200 {
		t.Fatalf("getBoundingClientRect().top = %v, want -200 (element at 600, scrolled 800)", top)
	}
	h.pageEval("document.getElementById('t2').scrollIntoView(); 1", nil)
	h.idle()
	if got := h.pageScroll(); got != 1200 {
		t.Fatalf("scrollIntoView: %d", got)
	}
	var ev []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.pageEval("ev", &ev)
		if len(ev) >= 2 && ev[len(ev)-1] == "scrollend:1200" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(ev) < 2 || ev[len(ev)-1] != "scrollend:1200" {
		t.Fatalf("scroll events: %v", ev)
	}
	sawScroll := false
	for _, e := range ev {
		if e == "scroll:1200" {
			sawScroll = true
		}
	}
	if !sawScroll {
		t.Fatalf("expected a scroll event at the final position: %v", ev)
	}
	// instant request reads back at once
	if got := h.evalInt("scrollTo(0, 50), scrollY"); got != 50 {
		t.Fatalf("scrollY right after scrollTo: %d", got)
	}
}

func TestSmoothScrollBehaviorAnimates(t *testing.T) {
	h, _ := newScrollHarness(t, "/smooth", "Smooth")
	before := h.win.Presents()
	h.pageEval("scrollTo(0, 1500); 1", nil) // scroll-behavior: smooth on <html>
	h.idle()
	if got := h.pageScroll(); got != 1500 {
		t.Fatalf("smooth scroll must end at the target, at %d", got)
	}
	if n := h.win.Presents() - before; n < 6 {
		t.Fatalf("a smooth scroll should take several frames, saw %d", n)
	}
	h.pageEval("scrollTo({top: 0, behavior: 'instant'}); 1", nil)
	h.idle()
	if got := h.pageScroll(); got != 0 {
		t.Fatalf("behavior instant must jump: %d", got)
	}
	before = h.win.Presents()
	h.pageEval("scrollTo({top: 900, behavior: 'smooth'}); 1", nil)
	h.idle()
	if h.pageScroll() != 900 || h.win.Presents()-before < 5 {
		t.Fatalf("explicit smooth: pos %d, %d frames", h.pageScroll(), h.win.Presents()-before)
	}
}

func TestAnchorLinkScrollsToTheFragment(t *testing.T) {
	h, srv := newScrollHarness(t, "/anchor", "Anchor")
	h.click(40, float64(vpTop)+10)
	h.idle()
	if got := h.pageScroll(); got != 1540 {
		t.Fatalf("clicking an in-page link must scroll its target to the top: %d, want 1540", got)
	}
	// loading a URL with a fragment starts at the fragment
	h.loadScrollPage(srv, "/anchor#target", "Anchor")
	h.waitFor("fragment scroll", func() bool { return h.pageScroll() == 1540 })
}

func TestKeyboardScrollIsSmooth(t *testing.T) {
	h, _ := newScrollHarness(t, "/script", "Script")
	h.click(400, float64(vpTop)+200)
	before := h.win.Presents()
	h.key("PageDown")
	h.idle()
	want := int(float64(h.app.vpDevice().Dy()) * 0.875)
	if got := h.pageScroll(); got < want-1 || got > want+1 {
		t.Fatalf("PageDown moves by 87.5%% of the view: %d, want %d", got, want)
	}
	if n := h.win.Presents() - before; n < 5 {
		t.Fatalf("PageDown should be eased over several frames, saw %d", n)
	}
	h.key("End")
	h.waitFor("end", func() bool { p, m := h.app.comp.Scroll(); return p == m })
	h.key("Home")
	h.waitFor("home", func() bool { return h.pageScroll() == 0 })
}

func TestPreciseSwipeEndsInAFling(t *testing.T) {
	h, _ := newScrollHarness(t, "/script", "Script")
	x, y := 400.0, float64(vpTop)+200
	for i := 0; i < 14; i++ { // a fast swipe of a precision touchpad: 40 px every 8 ms
		h.win.Push(shell.Event{Kind: shell.EvWheel, X: x, Y: y, DY: 40, Precise: true})
		time.Sleep(8 * time.Millisecond)
	}
	// the view followed the fingers; when they lift it keeps going and slows down
	var lift int
	h.loop(func() { lift = int(h.app.scroll.Pos) })
	h.idle()
	end := h.pageScroll()
	if end < lift+300 {
		t.Fatalf("a fast swipe must go on after the fingers lift: %d at lift, %d at rest", lift, end)
	}
	// a slow drag does not
	h.key("Home")
	h.waitFor("home", func() bool { return h.pageScroll() == 0 })
	for i := 0; i < 6; i++ {
		h.win.Push(shell.Event{Kind: shell.EvWheel, X: x, Y: y, DY: 1, Precise: true})
		time.Sleep(20 * time.Millisecond)
	}
	h.idle()
	if got := h.pageScroll(); got < 4 || got > 8 {
		t.Fatalf("a slow drag stops where it stopped: %d", got)
	}
}

func TestElementScrollApiAndEvents(t *testing.T) {
	h, _ := newScrollHarness(t, "/chain", "Chain")
	h.pageEval(`window.ev = []; var i = document.getElementById('inner'); i.addEventListener('scroll', function(e){ ev.push('scroll:' + i.scrollTop + ':' + e.bubbles); }); i.addEventListener('scrollend', function(){ ev.push('end:' + i.scrollTop); }); i.scrollTop = 120; 1`, nil)
	if got := h.evalInt("document.getElementById('inner').scrollTop"); got != 120 {
		t.Fatalf("scrollTop set/get: %d", got)
	}
	if got := h.evalInt("document.getElementById('inner').scrollHeight - document.getElementById('inner').clientHeight"); got != 800 {
		t.Fatalf("range %d", got)
	}
	h.pageEval("document.getElementById('inner').scrollBy(0, 100); 1", nil)
	h.pageEval("document.getElementById('inner').scrollTo({top: 5000}); 1", nil) // clamped to the range
	if got := h.evalInt("document.getElementById('inner').scrollTop"); got != 800 {
		t.Fatalf("scrollTo beyond the end: %d", got)
	}
	h.pageEval("document.getElementById('inner').scrollTop = -50; 1", nil)
	if got := h.evalInt("document.getElementById('inner').scrollTop"); got != 0 {
		t.Fatalf("negative scrollTop: %d", got)
	}
	// the content really moved: the red block's top edge is above the container's top
	h.pageEval("document.getElementById('inner').scrollTop = 300; 1", nil)
	h.idle()
	if top := h.evalInt("Math.round(document.getElementById('inner').firstElementChild.getBoundingClientRect().top)"); top != -300 {
		t.Fatalf("content top %d, want -300", top)
	}
	var ev []string
	e := h.app.activePage()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.w.do(e.afterOp) // run the page's timers: the events are queued on them
		h.pageEval("ev", &ev)
		if len(ev) > 0 && ev[len(ev)-1] == "end:300" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(ev) == 0 || ev[len(ev)-1] != "end:300" || ev[0][:7] != "scroll:" || ev[0][len(ev[0])-6:] != ":false" {
		t.Fatalf("element scroll events (they do not bubble): %v", ev)
	}
	// scrollIntoView inside a scroller scrolls the scroller, not the page
	h.pageEval("var d = document.createElement('div'); d.id='mark'; d.style.cssText='position:absolute;top:700px;height:20px'; document.getElementById('inner').firstElementChild.style.position='relative'; document.getElementById('inner').firstElementChild.appendChild(d); document.getElementById('inner').scrollTop = 0; d.scrollIntoView(); 1", nil)
	h.idle()
	if got := h.evalInt("document.getElementById('inner').scrollTop"); got != 700 {
		t.Fatalf("scrollIntoView in a scroller: scrollTop %d, want 700", got)
	}
	if h.pageScroll() != 0 {
		t.Fatalf("the page must not scroll for an element inside an inner scroller that is already in view: %d", h.pageScroll())
	}
}
