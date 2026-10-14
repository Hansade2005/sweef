package app

// fullscreen.go: element fullscreen (the Fullscreen API, a video's fullscreen button). The page asks through
// engine.EvFullscreen; the window goes fullscreen (when it is not already) and the page gets the whole window, so
// the chrome is hidden. Esc, F11, switching the tab or a navigation leave it, and the page is told like any exit.

// onTabFullscreen is the page entering or leaving element fullscreen (UI loop).
func (a *App) onTabFullscreen(e *etab, active bool) {
	if active {
		if e.id != a.model.Active(a.winID) || e.kind != "page" {
			e.t.ExitFullscreen() // a background tab cannot take the window
			return
		}
		if a.htmlFS {
			a.fsTab = e.id
			return
		}
		a.htmlFS, a.fsTab = true, e.id
		if !a.fullscreen {
			a.fullscreen, a.fsWinSet = true, true
			a.win.SetFullscreen(true)
		}
		a.reapplyViewport()
		a.markState()
		return
	}
	if a.htmlFS && a.fsTab == e.id {
		a.leaveHTMLFullscreen(false)
	}
}

// leaveHTMLFullscreen restores the window and the chrome; tell says whether the page still has to be told.
func (a *App) leaveHTMLFullscreen(tell bool) {
	if !a.htmlFS {
		return
	}
	id := a.fsTab
	a.htmlFS, a.fsTab = false, ""
	if a.fsWinSet {
		a.fsWinSet, a.fullscreen = false, false
		a.win.SetFullscreen(false)
	}
	a.reapplyViewport()
	a.markState()
	if tell {
		if e := a.page(id); e != nil {
			e.w.do(func() { e.t.ExitFullscreen(); e.afterOp() })
		}
	}
}

// fullscreenViewport is the viewport while a page element is fullscreen: the whole window.
func (a *App) fullscreenViewport() rect { return rect{0, 0, float64(a.wLog), float64(a.hLog)} }

func (a *App) reapplyViewport() {
	if a.htmlFS {
		a.applyViewport(a.fullscreenViewport())
		return
	}
	if a.haveLayout {
		a.applyViewport(a.vpChrome)
	} else {
		a.layoutFallback()
	}
}
