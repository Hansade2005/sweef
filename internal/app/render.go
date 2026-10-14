package app

import (
	"image"
	"math"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/engine"
)

func (a *App) scaleBits() float64 { return math.Float64frombits(a.scaleBitsV.Load()) }

// zoomF is the page zoom factor (1 = 100%).
func (a *App) zoomF() float64 {
	z := math.Float64frombits(a.zoomBitsV.Load())
	if z < 0.25 || z > 5 {
		return 1
	}
	return z
}

// pageDSF is the device pixel ratio the page is laid out and painted at.
func (a *App) pageDSF() float64 { return a.scaleBits() * a.zoomF() }

// pageCSS converts logical window pixels to the page's (zoomed) CSS pixels.
func (a *App) pageCSS(v int) int { return int(float64(v)/a.zoomF() + 0.5) }

// resize applies a new window size / device scale.
func (a *App) resize(w, h int, scale float64) {
	a.ctxDismiss()
	if scale <= 0 {
		scale = 1
	}
	if w < 1 || h < 1 {
		return
	}
	a.wLog, a.hLog, a.scale = w, h, scale
	a.scaleBitsV.Store(math.Float64bits(scale))
	a.model.SetWindowSize(a.winID, w, h, scale)
	a.comp.Resize(dev(w, scale), dev(h, scale), scale)
	a.mu.Lock()
	c, ov := a.chrome, a.overlay
	a.mu.Unlock()
	for _, e := range []*etab{c, ov} {
		if e == nil {
			continue
		}
		e := e
		e.vpW.Store(int32(w))
		e.vpH.Store(int32(h))
		e.w.do(func() {
			e.t.SetViewport(w, h, scale, false, false)
			e.afterOp()
			e.paint(true)
		})
	}
	a.layoutFallback()
	a.markState()
}

// layoutFallback derives a viewport hole from the window size until the
// chrome page reports its own (or when it never does).
func (a *App) layoutFallback() {
	if a.htmlFS {
		a.applyViewport(a.fullscreenViewport())
		return
	}
	if a.haveLayout {
		return
	}
	top := 96.0
	w := float64(a.wLog)
	if a.model.Sidebar.Open {
		w -= float64(a.model.Sidebar.Width)
	}
	a.applyViewport(rect{0, top, w, float64(a.hLog) - top})
}

// setViewport is the hole reported by the chrome page.
func (a *App) setViewport(r rect) {
	if r.empty() {
		return
	}
	a.haveLayout = true
	a.vpChrome = r
	if a.htmlFS {
		return // the page keeps the whole window until it leaves fullscreen
	}
	a.applyViewport(r)
}

// vpSnapshot is the viewport rectangle for other goroutines (the chrome probe).
func (a *App) vpSnapshot() rect {
	a.vpMu.Lock()
	defer a.vpMu.Unlock()
	return a.vpShadow
}

func (a *App) applyViewport(r rect) {
	if r == a.vpLog {
		return
	}
	a.vpLog = r
	a.vpMu.Lock()
	a.vpShadow = r
	a.vpMu.Unlock()
	vp := a.vpDevice()
	a.comp.SetViewport(vp)
	w, h := a.vpLogW(), a.vpLogH()
	a.mu.Lock()
	for _, e := range a.pages {
		e.vpW.Store(int32(w))
		e.vpH.Store(int32(h))
	}
	a.mu.Unlock()
	if e := a.activePage(); e != nil {
		e.w.do(func() {
			e.setViewport(a.pageCSS(w), a.pageCSS(h), a.pageDSF())
			e.paint(true)
		})
	}
	a.scroll.SetMax(a.scrollMax())
}

func (a *App) scrollMax() float64 {
	_, m := a.comp.Scroll()
	return float64(m)
}

// onChromePainted installs new chrome pixels.
func (a *App) onChromePainted(kind string, img *image.RGBA, dt time.Duration) {
	if kind == "overlay" {
		a.onOverlayPainted(img, dt)
		return
	}
	a.comp.RecordPaint(kind, dt)
	a.comp.SetChrome(img)
	a.stats.chromePaints.Add(1)
}

// onPagePainted installs the page texture of the active tab.
func (a *App) onPagePainted(id string, img *image.RGBA, originDev, docCSS int, dt time.Duration) {
	if id != a.model.Active(a.winID) {
		return
	}
	a.comp.RecordPaint("page", dt)
	spare := a.comp.SetPageAt(img, originDev)
	if e := a.page(id); e != nil && spare != nil && spare != img {
		e.spare.Store(spare)
	}
	a.setActiveShown(id)
	a.onPageMetrics(id, docCSS)
	a.stats.pagePaints.Add(1)
}

// applyRestoreScroll jumps to the scroll offset saved with a restored tab, once
// the document has loaded (so its height is known).
func (a *App) applyRestoreScroll(e *etab) {
	r := e.restoreScroll.Swap(0)
	if r <= 0 || a.model.Active(a.winID) != e.id {
		return
	}
	a.scroll.SetMax(a.scrollMax())
	a.scroll.ScrollTo(float64(r)*a.pageDSF(), true)
	pos := int(a.scroll.Pos)
	a.comp.ScrollTo(pos)
	a.noteScrolled(pos)
}

// onPageMetrics records the document height (scroll range).
func (a *App) onPageMetrics(id string, docCSS int) {
	if id != a.model.Active(a.winID) || docCSS <= 0 {
		return
	}
	a.paintedDocH = docCSS
	a.comp.SetDocHeight(int(float64(docCSS)*a.pageDSF() + 0.5))
	a.scroll.SetMax(a.scrollMax())
}

func (a *App) resetScroll() {
	a.scroll.Reset(0)
	a.comp.ScrollTo(0)
	if e := a.activePage(); e != nil {
		e.scrollCSS.Store(0)
	}
}

// ---- animation tick --------------------------------------------------------------------------------

const frameInterval = 16 * time.Millisecond

// startAnim arms the ~60 Hz tick. It stops by itself when nothing animates,
// so an idle browser has no timer at all.
func (a *App) startAnim() {
	if a.animOn.Load() {
		return
	}
	a.animOn.Store(true)
	a.lastTick = time.Now()
	a.animT = time.NewTimer(frameInterval)
}

// stepScroll advances the scroll physics to now and moves the view.
func (a *App) stepScroll() {
	pos := a.scroll.TickNow()
	if a.comp.ScrollTo(pos) {
		a.noteScrolled(pos)
	}
	a.stepElemScroll()
}

func (a *App) tick() {
	now := time.Now()
	a.lastTick = now
	dragging := a.dragScroll() || a.dragHeldOutside() // the selection gesture may not have reported in yet
	a.stepScroll()
	// overlay scrollbar: solid for 900 ms after activity, then a 250 ms fade
	since := now.Sub(a.scrollbarT)
	var alpha uint8
	switch {
	case a.scrollbarT.IsZero() || since > 1150*time.Millisecond:
		alpha = 0
	case since < 900*time.Millisecond:
		alpha = 150
	default:
		alpha = uint8(150 * (1150*time.Millisecond - since) / (250 * time.Millisecond))
	}
	a.comp.SetScrollbar(alpha)
	if a.scroll.Animating() || a.elemAnimating() || alpha > 0 || dragging {
		a.animT.Reset(frameInterval)
		return
	}
	a.animT.Stop()
	a.animT = nil
	a.animOn.Store(false)
	a.stats.animStops.Add(1)
}

// noteScrolled tells the active page about its new scroll offset (debounced)
// and makes sure the texture covers what is about to become visible.
func (a *App) noteScrolled(posDev int) {
	e := a.activePage()
	if e == nil {
		return
	}
	css := int(float64(posDev)/a.pageDSF() + 0.5)
	e.scrollCSS.Store(int32(css))
	e.t.NoteScrollY(css) // window.scrollY and getBoundingClientRect follow at once
	if st, ok := a.model.Tab(e.id); ok && st.Scroll != css {
		a.model.Update(e.id, func(t *state.Tab) { t.Scroll = css })
		a.scheduleSession()
	}
	vp := a.vpDevice()
	if old := int(e.scrollPosDev.Swap(int32(posDev))); old != posDev {
		d := int32(1)
		if posDev < old {
			d = -1
		}
		e.scrollDir.Store(d)
		now := time.Now().UnixNano()
		if dt := float64(now-e.scrollAt.Swap(now)) / 1e9; dt > 0.0005 && dt < 0.25 {
			v := float64(posDev-old) / a.pageDSF() / dt
			e.scrollVel.Store(int32(0.5*v + 0.5*float64(e.scrollVel.Load())))
		} else if dt >= 0.25 {
			e.scrollVel.Store(0)
		}
	}
	// the band of the page that is rasterised must keep covering the view: ask for the next one in good time
	if y0, y1 := a.comp.PageBand(); y1 > 0 {
		doc := int(e.docCSS.Load())
		docDev := int(float64(doc)*a.pageDSF() + 0.5)
		n0, n1 := pageNeed(int(float64(posDev)/a.pageDSF()), int(float64(vp.Dy())/a.pageDSF()), doc, int(e.scrollVel.Load()))
		needTop, needBot := int(float64(n0)*a.pageDSF()), min(docDev, int(float64(n1)*a.pageDSF()))
		if needTop < y0 || needBot > y1 {
			e.requestPaint(false)
		}
	} else {
		e.requestPaint(false)
	}
	if e.scrollNotify.CompareAndSwap(false, true) {
		// page script hears about scrolling at most about every third frame, and once more when it stops
		time.AfterFunc(48*time.Millisecond, func() {
			e.scrollNotify.Store(false)
			y := int(e.scrollCSS.Load())
			e.w.tryDo(func() {
				_, _ = e.t.Evaluate("__sweefScrollTo(" + itoa(y) + ")")
				e.afterOp() // runs the timers the scroll events were queued on; lazy images follow the scroll
			})
		})
	}
}

// scriptScroll moves the viewport because page script asked (window.scrollTo and friends, scrollIntoView, a
// link to a fragment). A smooth request animates; an instant one jumps. UI loop only.
func (a *App) scriptScroll(e *etab, r engine.ScrollRequest) {
	if a.activePage() != e {
		e.scrollCSS.Store(int32(r.Y))
		return
	}
	a.scroll.SetMax(a.scrollMax())
	target := r.Y * a.pageDSF()
	a.scrollbarT = time.Now()
	if r.Smooth {
		a.scroll.EaseTo(target)
	} else {
		a.scroll.ScrollTo(target, true)
	}
	a.kickScroll()
}

// zoom changes page zoom by delta percent (0 resets). Zoom is implemented by
// scaling the page's device pixel ratio.
func (a *App) zoom(delta int) {
	cur := a.prof.Settings.Int("zoom.default")
	if delta == 0 {
		cur = 100
	} else {
		cur += delta
	}
	if cur < 25 {
		cur = 25
	}
	if cur > 500 {
		cur = 500
	}
	_ = a.prof.Settings.Set("zoom.default", cur)
	a.applyZoom()
	a.sendChrome(mToast, map[string]any{"text": "Zoom " + itoa(cur) + "%", "kind": "info"})
}

// find searches the active page, scrolls to the n-th match and reports
// find.result {count,index} to the chrome. dir 0 starts a new search, +1/-1
// steps to the next/previous match.
func (a *App) find(q string, dir int) {
	e := a.activePage()
	if e == nil {
		return
	}
	f := a.model.Find
	f.Open, f.Query = true, q
	if q == "" {
		f.Matches, f.Index = 0, 0
		a.model.Find = f
		a.sendChrome("find.result", map[string]any{"count": 0, "index": 0})
		a.markState()
		return
	}
	idx := f.Index
	switch {
	case dir == 0:
		idx = 1
	case dir > 0:
		idx++
		if f.Matches > 0 && idx > f.Matches {
			idx = 1
		}
	default:
		idx--
		if idx < 1 {
			idx = f.Matches
			if idx < 1 {
				idx = 1
			}
		}
	}
	f.Index = idx // steps that follow before the answer is in count from here
	a.model.Find = f
	e.w.do(func() {
		var r engine.FindResult
		if dir == 0 {
			r = e.t.FindStart(q, float64(e.scrollCSS.Load()))
		} else {
			r = e.t.FindStep(idx - 1)
		}
		e.paint(false)
		a.post(func() {
			ff := a.model.Find
			ff.Matches = r.Count
			ff.Index = r.Index + 1
			if r.Count == 0 || r.Index < 0 {
				ff.Index = 0
			}
			a.model.Find = ff
			a.sendChrome("find.result", map[string]any{"count": ff.Matches, "index": ff.Index})
			if r.Top >= 0 {
				// bring the active match into view, a third of the way down, unless it is already comfortably there
				vh := float64(a.vpDevice().Dy())
				top := r.Top * a.pageDSF()
				pos := a.scroll.Target
				if top < pos+vh*0.1 || top+r.H*a.pageDSF() > pos+vh*0.9 {
					a.scroll.EaseTo(math.Max(0, top-vh/3))
					a.scrollbarT = time.Now()
					a.kickScroll()
				}
			}
			a.markState()
		})
	})
}

// findClear removes the find highlights of the active page.
func (a *App) findClear() {
	if e := a.activePage(); e != nil {
		e.w.do(func() {
			e.t.FindClear()
			e.paint(false)
		})
	}
}

// applyZoom re-lays-out the active page at the stored zoom.
func (a *App) applyZoom() {
	a.zoomBitsV.Store(math.Float64bits(float64(a.prof.Settings.Int("zoom.default")) / 100))
	w, h := a.vpLogW(), a.vpLogH()
	if e := a.activePage(); e != nil {
		e.w.do(func() {
			e.setViewport(a.pageCSS(w), a.pageCSS(h), a.pageDSF())
			e.paint(true)
		})
	}
}
