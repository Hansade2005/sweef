package app

import (
	"image"
	"math"
	"time"
)

// The overlay is a second privileged chrome tab (chrome/overlay.html) used for
// popovers, menus and the omnibox dropdown, which must draw over the page
// viewport. It is painted with a transparent canvas and alpha-blended above
// the page by the compositor. The overlay page tells Go what it occupies:
//
//	sweef.post("overlay", {rect:{x,y,w,h}, visible:true, alpha:1})
//
// Pointer events inside that rect go to the overlay; a press outside it sends
// `overlay.dismiss` to the overlay and chrome pages.

type overlayState struct {
	rect    rect
	visible bool
	alpha   float64
}

func (a *App) overlayHit(x, y float64) bool {
	o := a.ovl
	return o.visible && x >= o.rect.X && x < o.rect.X+o.rect.W && y >= o.rect.Y && y < o.rect.Y+o.rect.H
}

// onOverlayMsg handles the overlay page's `overlay` message.
func (a *App) onOverlayMsg(r rect, visible bool, alpha float64) {
	if alpha <= 0 || alpha > 1 {
		alpha = 1
	}
	if r.empty() {
		visible = false
	}
	a.ovl = overlayState{rect: r, visible: visible, alpha: alpha}
	a.mu.Lock()
	e := a.overlay
	a.mu.Unlock()
	if e == nil {
		return
	}
	if !visible {
		a.comp.SetOverlay(nil, image.Point{}, 0)
		e.paintW.Store(0)
		if a.focus == "overlay" {
			a.setFocus("chrome")
		}
		return
	}
	e.paintW.Store(int32(math.Ceil(r.X + r.W)))
	e.paintH.Store(int32(math.Ceil(r.Y + r.H)))
	e.requestPaint(true)
}

// onOverlayPainted crops the painted overlay to its rect and hands it to the
// compositor.
func (a *App) onOverlayPainted(img *image.RGBA, dt time.Duration) {
	a.comp.RecordPaint("overlay", dt)
	if !a.ovl.visible {
		return
	}
	r := image.Rect(a.l2d(a.ovl.rect.X), a.l2d(a.ovl.rect.Y), a.l2d(a.ovl.rect.X+a.ovl.rect.W), a.l2d(a.ovl.rect.Y+a.ovl.rect.H)).Intersect(img.Bounds())
	if r.Empty() {
		return
	}
	sub, _ := img.SubImage(r).(*image.RGBA)
	a.comp.SetOverlay(sub, r.Min, uint8(a.ovl.alpha*255+0.5))
}

// startOverlay creates the overlay tab when the UI ships an overlay page.
func (a *App) startOverlay() {
	entry := a.cfg.OverlayEntry
	if entry == "" {
		entry = "chrome/overlay.html"
	}
	html, err := a.asset(entry)
	if err != nil {
		return
	}
	t, err := a.eng.NewTab("about:blank")
	if err != nil {
		return
	}
	e := a.newEtab("overlay", "overlay", t, false)
	e.vpW.Store(int32(a.wLog))
	e.vpH.Store(int32(a.hLog))
	a.mu.Lock()
	a.overlay = e
	a.mu.Unlock()
	e.w.do(func() {
		e.t.SetViewport(a.wLog, a.hLog, a.scale, false, false)
		e.t.SetDocument(string(html), "sweef://"+entry)
		e.drainBindings()
		e.pump(30 * time.Millisecond)
		e.drainBindings()
	})
}

func (a *App) overlayTab() *etab {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.overlay
}
