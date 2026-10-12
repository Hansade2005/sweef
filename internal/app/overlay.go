package app

import (
	"image"
	"math"
	"time"
)

// The overlay is a second privileged page (chrome/overlay.html): the agent's
// "acting" surface. It is as large as the window, painted with a transparent
// canvas and blended above everything. Go drives it with
//
//	overlay {state: off|acting|control|done|stopped, viewport:{x,y,w,h}, task, step, cursor}
//
// and the page answers with `overlay.hit {rects}`, the only places where the
// overlay takes mouse events: everywhere else clicks pass through to the
// chrome and the page. While the state is "off" nothing is painted or blended.

type overlayState struct {
	state  string
	rects  []rect         // overlay.hit
	cursor map[string]any // agent cursor {x,y,label,pressed}, viewport-relative; nil = hidden
}

func (o overlayState) visible() bool { return o.state != "" && o.state != "off" }

func (a *App) overlayHit(x, y float64) bool {
	if !a.ovl.visible() {
		return false
	}
	for _, r := range a.ovl.rects {
		if x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H {
			return true
		}
	}
	return false
}

// setOverlay sends a new overlay state to the overlay page (UI loop).
func (a *App) setOverlay(state, task, step string) {
	e := a.overlayTab()
	if e == nil {
		return
	}
	was := a.ovl.visible()
	a.ovl.state = state
	if state == "" || state == "off" {
		a.ovl.state = "off"
		a.ovl.rects = nil
	}
	vp := a.vpLog
	msg := map[string]any{
		"state": state, "task": task, "step": step,
		"viewport": map[string]any{"x": math.Round(vp.X), "y": math.Round(vp.Y), "w": math.Round(vp.W), "h": math.Round(vp.H)},
	}
	if a.ovl.cursor != nil && a.ovl.state == "acting" {
		msg["cursor"] = a.ovl.cursor
	}
	if !a.ovl.visible() {
		a.ovl.cursor = nil
	}
	a.sendTo(e, "overlay", msg)
	if a.ovl.visible() {
		e.paintW.Store(int32(a.wLog))
		e.paintH.Store(int32(a.hLog))
		e.requestPaint(true)
		return
	}
	e.paintW.Store(0)
	if was {
		a.comp.SetOverlay(nil, image.Point{}, 0)
	}
}

// onOverlayHit stores the rectangles where the overlay wants the mouse.
func (a *App) onOverlayHit(rs []rect) {
	a.ovl.rects = rs
	if !a.ovl.visible() && a.focus == "overlay" {
		a.setFocus("page")
	}
}

// onOverlayPainted hands the painted overlay to the compositor.
func (a *App) onOverlayPainted(img *image.RGBA, dt time.Duration) {
	a.comp.RecordPaint("overlay", dt)
	if !a.ovl.visible() {
		return
	}
	a.comp.SetOverlay(img, image.Point{}, 255)
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
