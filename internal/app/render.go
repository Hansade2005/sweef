package app

import (
	"image"
	"math"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
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
	a.updateChromePaintH()
	a.layoutFallback()
	a.markState()
}

// layoutFallback derives a viewport hole from the window size until the
// chrome page reports its own (or when it never does).
func (a *App) layoutFallback() {
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
	a.applyViewport(r)
}

func (a *App) applyViewport(r rect) {
	if r == a.vpLog {
		return
	}
	a.vpLog = r
	a.updateChromePaintH()
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
			e.t.SetViewport(a.pageCSS(w), a.pageCSS(h), a.pageDSF(), false, false)
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
	a.stats.chromePaints++
}

// chromePaintH is how many logical rows of the chrome page need painting:
// everything when something sits right of or below the viewport hole
// (sidebar, find bar), else only the strip above it.
func (a *App) chromePaintH() int {
	vp := a.vpLog
	if !a.haveLayout || vp.empty() {
		return 0
	}
	if vp.X > 1 || float64(a.wLog)-(vp.X+vp.W) > 1 || float64(a.hLog)-(vp.Y+vp.H) > 1 {
		return 0
	}
	return int(math.Ceil(vp.Y)) + 1
}

func (a *App) updateChromePaintH() {
	a.mu.Lock()
	c := a.chrome
	a.mu.Unlock()
	if c == nil {
		return
	}
	h := int32(a.chromePaintH())
	if c.paintH.Swap(h) != h {
		c.requestPaint(true)
	}
}

// onPagePainted installs the page texture of the active tab.
func (a *App) onPagePainted(id string, img *image.RGBA, docCSS int, dt time.Duration) {
	if id != a.model.Active(a.winID) {
		return
	}
	a.comp.RecordPaint("page", dt)
	a.comp.SetPage(img)
	a.setActiveShown(id)
	a.onPageMetrics(id, docCSS)
	a.stats.pagePaints++
	if e := a.page(id); e != nil {
		if r := e.restoreScroll.Swap(0); r > 0 {
			a.scroll.ScrollTo(float64(r)*a.pageDSF(), true)
			a.comp.ScrollTo(int(a.scroll.Pos))
		}
	}
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
	if a.animOn {
		return
	}
	a.animOn = true
	a.lastTick = time.Now()
	a.animT = time.NewTimer(frameInterval)
}

func (a *App) tick() {
	now := time.Now()
	dt := now.Sub(a.lastTick).Seconds()
	a.lastTick = now
	pos := a.scroll.Tick(dt)
	if a.comp.ScrollTo(pos) {
		a.noteScrolled(pos)
	}
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
	if a.scroll.Animating() || alpha > 0 {
		a.animT.Reset(frameInterval)
		return
	}
	a.animT.Stop()
	a.animT = nil
	a.animOn = false
	a.stats.animStops++
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
	if st, ok := a.model.Tab(e.id); ok && st.Scroll != css {
		a.model.Update(e.id, func(t *state.Tab) { t.Scroll = css })
		a.scheduleSession()
	}
	vp := a.vpDevice()
	if posDev+2*vp.Dy() > a.comp.PageHeight() {
		e.requestPaint(false)
	}
	if e.scrollNotify.CompareAndSwap(false, true) {
		time.AfterFunc(120*time.Millisecond, func() {
			e.scrollNotify.Store(false)
			y := int(e.scrollCSS.Load())
			e.w.do(func() {
				_, _ = e.t.Evaluate("__sweefScrollTo(" + itoa(y) + ")")
				e.drainBindings()
			})
		})
	}
}

// zoom changes page zoom by delta percent (0 resets). Zoom is implemented by
// scaling the page's device pixel ratio.
func (a *App) zoom(delta int) {
	cur := a.prof.Settings.Int("defaultZoom")
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
	_ = a.prof.Settings.Set("defaultZoom", cur)
	a.applyZoom()
	a.sendChrome(mToast, map[string]any{"text": "Zoom " + itoa(cur) + "%", "kind": "info"})
}

// find searches the active page and scrolls to the n-th match.
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
		a.markState()
		return
	}
	idx := f.Index
	if dir > 0 && f.Query == q && f.Matches > 0 {
		idx = f.Index % f.Matches
		if idx = idx + 1; idx > f.Matches {
			idx = 1
		}
	} else if dir < 0 && f.Matches > 0 {
		if idx = idx - 1; idx < 1 {
			idx = f.Matches
		}
	} else if idx < 1 {
		idx = 1
	}
	e.w.do(func() {
		var r struct {
			Count int `json:"count"`
			Top   int `json:"top"`
		}
		if e.eval("__sweefFind("+jsString(q)+","+itoa(idx-1)+")", &r) != nil {
			return
		}
		a.post(func() {
			ff := a.model.Find
			ff.Matches = r.Count
			ff.Index = idx
			if r.Count == 0 {
				ff.Index = 0
			} else if idx > r.Count {
				ff.Index = 1
			}
			a.model.Find = ff
			if r.Top >= 0 {
				vh := float64(a.vpDevice().Dy())
				a.scroll.ScrollTo(float64(r.Top)*a.pageDSF()-vh/3, false)
				a.scrollbarT = time.Now()
				a.startAnim()
			}
			a.markState()
		})
	})
}

// applyZoom re-lays-out the active page at the stored zoom.
func (a *App) applyZoom() {
	a.zoomBitsV.Store(math.Float64bits(float64(a.prof.Settings.Int("defaultZoom")) / 100))
	w, h := a.vpLogW(), a.vpLogH()
	if e := a.activePage(); e != nil {
		e.w.do(func() {
			e.t.SetViewport(a.pageCSS(w), a.pageCSS(h), a.pageDSF(), false, false)
			e.paint(true)
		})
	}
}
