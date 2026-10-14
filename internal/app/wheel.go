package app

// wheel.go: what a wheel turn or a touchpad swipe does.
//
// The page sees the wheel event first. Scrolling is the event's default action, so a page that cancels it (a
// non-passive listener calling preventDefault) keeps the view where it is; ctrl+wheel (zoom) is cancelled the
// same way. A page that has no listener that can cancel never delays the scroll: the first wheel after a
// document loads asks the page whether it has one, and from then on the scroll happens at once while the event
// is delivered alongside.
//
// The default action is a scroll chain. The overflow container under the pointer that can move in the
// direction of the gesture takes it (with its own smooth animation), then the frame under the pointer, and the
// page last; a container that ends the gesture at its limit and has overscroll-behavior contain or none stops the
// chain. Once a gesture has picked its target it keeps it until the wheel has been still for a moment, so
// reaching the end of an inner scroller does not start moving the page under the same swipe.

import (
	"math"
	"strconv"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/compositor"
	"github.com/sweefbrowser/sweef/internal/shell"
)

// wheelLatchFor is how long a gesture keeps its scroll target after the last wheel event.
const wheelLatchFor = 450 * time.Millisecond

// wheel states of a document (etab.wheelState)
const (
	wheelUnknown   = 0 // not asked yet
	wheelPassive   = 1 // no listener that can cancel: scroll without waiting
	wheelCancelled = 2 // the page may cancel: wait for its answer
)

type wheelLatch struct {
	kind  int // 0 none, 1 viewport, 2 element, 3 blocked by overscroll-behavior
	tab   string
	id    int64
	until time.Time
}

const (
	latchNone = iota
	latchViewport
	latchElement
	latchBlocked
)

func (a *App) onWheel(ev shell.Event) {
	a.ctxDismiss() // a menu closes when the page scrolls
	if a.mouse.captured == "" && !a.inViewport(ev.X, ev.Y) {
		if c := a.targetTab("chrome"); c != nil {
			x, y, dx, dy := int(ev.X), int(ev.Y), int(ev.DX), int(ev.DY)
			c.w.do(func() {
				_, _ = c.t.Evaluate("__swWheel(" + itoa(dx) + "," + itoa(dy) + "," + itoa(x) + "," + itoa(y) + ")")
				c.afterOp()
			})
		}
		return
	}
	e := a.activePage()
	if e == nil {
		return
	}
	e.lastInput.Store(time.Now().UnixNano())
	z := a.zoomF()
	wx, wy := a.pageCoords(ev.X, ev.Y)
	dx, dy := ev.DX/z, ev.DY/z // CSS px
	mods := modBits(ev.Mods)
	call := "__sweefWheel(" + ftoa(dx) + "," + ftoa(dy) + "," + itoa(wx) + "," + itoa(wy) + "," + itoa(mods) + ")"
	if e.wheelState.Load() == wheelPassive {
		a.nativeWheel(e, ev, wx, wy)
		e.w.tryDo(func() { a.runWheelCall(e, call) })
		return
	}
	if !e.w.tryDo(func() {
		prevented := a.runWheelCall(e, call)
		if !prevented {
			a.post(func() { a.nativeWheel(e, ev, wx, wy) })
		}
	}) {
		a.nativeWheel(e, ev, wx, wy) // a worker this far behind must not hold the scroll back
	}
}

// runWheelCall delivers the wheel event to the page and remembers whether the page can cancel wheel events.
// It reports whether the page cancelled this one. Worker only.
func (a *App) runWheelCall(e *etab, call string) (prevented bool) {
	var res struct {
		Prevented bool `json:"prevented"`
		NP        bool `json:"np"`
	}
	if e.evalOn(e.inputTab(), call, &res) != nil {
		return false
	}
	if res.NP {
		e.wheelState.Store(wheelCancelled)
	} else {
		e.wheelState.Store(wheelPassive)
	}
	e.drainBindings()
	return res.Prevented
}

func ftoa(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "0"
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

// nativeWheel is the default action of a wheel event. UI loop only.
func (a *App) nativeWheel(e *etab, ev shell.Event, wx, wy int) {
	if a.activePage() != e {
		return
	}
	if ev.Mods&(shell.ModCtrl|shell.ModMeta) != 0 { // ctrl+wheel zooms
		if ev.DY < 0 {
			a.zoom(10)
		} else if ev.DY > 0 {
			a.zoom(-10)
		}
		return
	}
	z := a.zoomF()
	dx, dy := ev.DX/z, ev.DY/z
	// a frame under the pointer that can still scroll takes the wheel
	if e.t.WheelAt(wx, wy, int(dx), int(dy)) {
		return
	}
	now := time.Now()
	l := &a.wlatch
	if l.kind == latchNone || l.tab != e.id || now.After(l.until) {
		*l = wheelLatch{tab: e.id, kind: latchViewport}
		if tg, found, blocked := e.t.ScrollTargetAt(float64(wx), float64(wy), dx, dy); found {
			l.kind, l.id = latchElement, tg.ID
		} else if blocked {
			l.kind = latchBlocked
		}
		a.scroll.Stop() // a new gesture stops a fling that is still running
	}
	l.until = now.Add(wheelLatchFor)
	switch l.kind {
	case latchBlocked:
		l.kind = latchNone // decided per event: the other direction may well move the container
		return
	case latchElement:
		a.scrollElement(e, l.id, dx, dy, ev.Precise)
		return
	}
	if dy == 0 {
		return
	}
	if ev.Precise { // touchpad: the view follows the fingers, and a fast swipe goes on as a fling
		a.scroll.Precise(ev.DY * a.scale)
	} else {
		a.scroll.Wheel(ev.DY * a.scale)
	}
	a.scrollbarT = now
	a.kickScroll()
}

// kickScroll draws the first frame of a scroll now and arms the frame timer.
func (a *App) kickScroll() {
	a.stepScroll()
	a.startAnim()
}

// ---- inner scrollers ----------------------------------------------------------------------------

// elemScroll animates the scroll offsets of one overflow container.
type elemScroll struct {
	e      *etab
	id     int64
	x, y   *compositor.Scroller // CSS px
	sent   [2]int
	queued bool
}

func newScrollerLike(a *App) *compositor.Scroller {
	s := compositor.NewScroller()
	s.Smooth = a.scroll.Smooth
	return s
}

// scrollElement applies a wheel delta (CSS px) to the container with backend id.
func (a *App) scrollElement(e *etab, id int64, dx, dy float64, precise bool) {
	tg, ok := e.t.ScrollTargetOf(id)
	if !ok {
		return
	}
	es := a.escroll
	if es == nil || es.e != e || es.id != id {
		if es != nil {
			a.finishElemScroll()
		}
		es = &elemScroll{e: e, id: id, x: newScrollerLike(a), y: newScrollerLike(a)}
		a.escroll = es
	}
	if !es.x.Animating() && !es.y.Animating() {
		es.x.Reset(tg.X)
		es.y.Reset(tg.Y)
	}
	es.x.SetMax(tg.MaxX)
	es.y.SetMax(tg.MaxY)
	if precise {
		if dx != 0 {
			es.x.Precise(dx)
		}
		if dy != 0 {
			es.y.Precise(dy)
		}
	} else {
		if dx != 0 {
			es.x.Wheel(dx)
		}
		if dy != 0 {
			es.y.Wheel(dy)
		}
	}
	a.kickScroll()
}

// finishElemScroll lets a running element scroll land where it was going.
func (a *App) finishElemScroll() {
	es := a.escroll
	a.escroll = nil
	if es == nil {
		return
	}
	es.x.ScrollTo(es.x.Target, true)
	es.y.ScrollTo(es.y.Target, true)
	a.sendElemScroll(es, true)
}

func (a *App) elemAnimating() bool {
	es := a.escroll
	return es != nil && (es.x.Animating() || es.y.Animating())
}

// stepElemScroll advances the inner scroller by one frame. UI loop only.
func (a *App) stepElemScroll() {
	es := a.escroll
	if es == nil {
		return
	}
	if es.x.Animating() || es.y.Animating() {
		es.x.TickNow()
		es.y.TickNow()
		a.sendElemScroll(es, false)
	}
}

// sendElemScroll gives the engine the container's new offsets, at most one update in flight; later positions
// replace the one waiting.
func (a *App) sendElemScroll(es *elemScroll, force bool) {
	x, y := int(math.Round(es.x.Pos)), int(math.Round(es.y.Pos))
	if !force && es.sent == [2]int{x, y} {
		return
	}
	es.sent = [2]int{x, y}
	es.e.elemPos.Store(int64(x)<<32 | int64(uint32(y)))
	if !es.e.elemBusy.CompareAndSwap(false, true) {
		return
	}
	e, id := es.e, es.id
	e.w.do(func() {
		for {
			p := e.elemPos.Load()
			px, py := float64(p>>32), float64(int64(int32(p&0xffffffff)))
			_, _, changed := e.t.SetElementScroll(id, px, py)
			if changed {
				_, _ = e.t.Evaluate("__swElScrolled(" + itoa(int(id)) + ")")
				e.afterOp()
			}
			e.elemBusy.Store(false)
			if e.elemPos.Load() == p || !e.elemBusy.CompareAndSwap(false, true) {
				return
			}
		}
	})
}
