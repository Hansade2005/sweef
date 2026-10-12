package app

import (
	"math"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/shell"
)

type mouseState struct {
	captured    string // "chrome" | "page" while a button is down
	downX       float64
	downY       float64
	downBtn     int
	moveBusy    atomic.Bool
	moveX       atomic.Value // pending [2]float64
	hoverTarget string
}

// handleEvent routes one shell event. It returns true when the app should quit.
func (a *App) handleEvent(ev shell.Event) bool {
	switch ev.Kind {
	case shell.EvClose:
		return true
	case shell.EvResize:
		a.resize(ev.W, ev.H, ev.Scale)
	case shell.EvScale:
		a.resize(a.wLog, a.hLog, ev.Scale)
	case shell.EvFocus:
		a.model.SetFocused(a.winID, ev.Focus)
		a.markState()
	case shell.EvMouseMove:
		a.onMouseMove(ev)
	case shell.EvMouseDown:
		a.stats.markInput()
		a.onMouseButton(ev, true)
	case shell.EvMouseUp:
		a.onMouseButton(ev, false)
	case shell.EvWheel:
		a.stats.markInput()
		a.onWheel(ev)
	case shell.EvKeyDown:
		a.stats.markInput()
		a.onKeyDown(ev)
	case shell.EvText:
		a.stats.markInput()
		a.onText(ev.Text)
	case shell.EvDropFiles:
		for _, f := range ev.Files {
			a.newTab("file://"+f, false, true)
		}
	}
	return false
}

func (a *App) inViewport(x, y float64) bool {
	if a.vpLog.empty() {
		return false
	}
	return x >= a.vpLog.X && x < a.vpLog.X+a.vpLog.W && y >= a.vpLog.Y && y < a.vpLog.Y+a.vpLog.H
}

// target decides who receives a pointer at (x,y): the page in the hole, else chrome.
func (a *App) target(x, y float64) string {
	if a.mouse.captured != "" {
		return a.mouse.captured
	}
	if a.overlayHit(x, y) && a.overlayTab() != nil {
		return "overlay"
	}
	if a.inViewport(x, y) && a.activePage() != nil {
		return "page"
	}
	return "chrome"
}

func (a *App) activePage() *etab { return a.page(a.model.Active(a.winID)) }

// pageCoords converts window logical coords to page CSS coords.
func (a *App) pageCoords(x, y float64) (int, int) {
	pos, _ := a.comp.Scroll()
	z := a.zoomF()
	return int((x - a.vpLog.X) / z), int((y-a.vpLog.Y)/z + float64(pos)/a.pageDSF())
}

func modBits(m shell.Mods) int {
	b := 0
	if m&shell.ModShift != 0 {
		b |= 1
	}
	if m&shell.ModCtrl != 0 {
		b |= 2
	}
	if m&shell.ModAlt != 0 {
		b |= 4
	}
	if m&shell.ModMeta != 0 {
		b |= 8
	}
	return b
}

func (a *App) onMouseMove(ev shell.Event) {
	tgt := a.target(ev.X, ev.Y)
	e := a.targetTab(tgt)
	if e == nil {
		return
	}
	px, py := int(ev.X), int(ev.Y)
	if tgt == "page" {
		px, py = a.pageCoords(ev.X, ev.Y)
	}
	hover := a.mouse.captured == ""
	// coalesce: at most one move in flight per target, always the latest position
	if !a.mouse.moveBusy.CompareAndSwap(false, true) {
		a.mouse.moveX.Store([4]int{px, py, modBits(ev.Mods), b2i(hover)})
		return
	}
	a.runMove(e, tgt, px, py, modBits(ev.Mods), hover)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (a *App) runMove(e *etab, tgt string, px, py, mods int, hover bool) {
	e.w.do(func() {
		_, _ = e.t.Evaluate("__sweefPtr('move'," + itoa(px) + "," + itoa(py) + ",0," + itoa(mods) + ")")
		cur := shell.CursorDefault
		if hover {
			var c string
			err := e.eval("__sweefCursor("+itoa(px)+","+itoa(py)+")", &c)
			a.logf("cursor eval %d,%d -> %q %v", px, py, c, err)
			if err == nil {
				cur = cursorFor(c)
			}
		}
		e.drainBindings()
		e.paint(false)
		// pick up a newer queued position, if any
		if v := a.mouse.moveX.Swap(nil); v != nil {
			if p, ok := v.([4]int); ok {
				a.post(func() { a.runMoveNext(tgt, p) })
			}
		}
		a.mouse.moveBusy.Store(false)
		if hover {
			a.post(func() { a.setCursor(cur) })
		}
	})
}

func (a *App) runMoveNext(tgt string, p [4]int) {
	e := a.targetTab(tgt)
	if e == nil {
		return
	}
	if !a.mouse.moveBusy.CompareAndSwap(false, true) {
		a.mouse.moveX.Store(p)
		return
	}
	a.runMove(e, tgt, p[0], p[1], p[2], p[3] == 1)
}

func cursorFor(css string) shell.Cursor {
	switch css {
	case "pointer", "hand":
		return shell.CursorPointer
	case "text", "vertical-text":
		return shell.CursorText
	case "wait", "progress":
		return shell.CursorWait
	case "crosshair":
		return shell.CursorCrosshair
	case "ew-resize", "col-resize", "e-resize", "w-resize":
		return shell.CursorResizeEW
	case "ns-resize", "row-resize", "n-resize", "s-resize":
		return shell.CursorResizeNS
	case "not-allowed", "no-drop":
		return shell.CursorNotAllowed
	case "grab", "grabbing", "move":
		return shell.CursorGrab
	}
	return shell.CursorDefault
}

func (a *App) setCursor(c shell.Cursor) {
	if c == a.cursorShown {
		return
	}
	a.cursorShown = c
	a.win.SetCursor(c)
}

func (a *App) targetTab(tgt string) *etab {
	if tgt == "page" {
		return a.activePage()
	}
	if tgt == "overlay" {
		return a.overlayTab()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.chrome
}

func (a *App) onMouseButton(ev shell.Event, down bool) {
	tgt := a.target(ev.X, ev.Y)
	if down {
		a.mouse.captured, a.mouse.downX, a.mouse.downY, a.mouse.downBtn = tgt, ev.X, ev.Y, ev.Button
		if tgt != "overlay" && a.ovl.visible {
			a.sendChrome("overlay.dismiss", map[string]any{})
		}
		a.setFocus(tgt)
	}
	e := a.targetTab(tgt)
	if !down {
		a.mouse.captured = ""
	}
	if e == nil {
		return
	}
	e.lastInput.Store(time.Now().UnixNano())
	px, py := int(ev.X), int(ev.Y)
	if tgt == "page" {
		px, py = a.pageCoords(ev.X, ev.Y)
	}
	mods := modBits(ev.Mods)
	btn := ev.Button
	if down {
		if btn == 2 {
			e.w.do(func() { a.contextMenu(e, tgt, px, py, mods, ev.X, ev.Y) })
			return
		}
		e.w.do(func() {
			_, _ = e.t.Evaluate("__sweefPtr('down'," + itoa(px) + "," + itoa(py) + "," + itoa(btn) + "," + itoa(mods) + ")")
			e.afterOp()
		})
		return
	}
	if btn == 2 {
		return
	}
	clicked := math.Abs(ev.X-a.mouse.downX) < 6 && math.Abs(ev.Y-a.mouse.downY) < 6
	e.w.do(func() {
		_, _ = e.t.Evaluate("__sweefPtr('up'," + itoa(px) + "," + itoa(py) + "," + itoa(btn) + "," + itoa(mods) + ")")
		if clicked {
			var res struct {
				Open string `json:"open"`
				BG   bool   `json:"bg"`
			}
			_ = e.eval("__sweefPtr('click',"+itoa(px)+","+itoa(py)+","+itoa(btn)+","+itoa(mods)+")", &res)
			if res.Open != "" {
				open, bg := res.Open, res.BG
				a.post(func() { a.openTab(openOpts{URL: open, Incognito: e.incog, Activate: !bg, After: e.id}) })
			}
		}
		e.afterOp()
		a.post(func() { a.afterPageInput(e) })
	})
}

// afterPageInput notices navigation that page code started in response to input.
func (a *App) afterPageInput(e *etab) {
	if e.kind != "page" || e.closed.Load() {
		return
	}
	e.w.do(func() {
		if cur := e.t.URL(); cur != "" {
			a.post(func() { a.onEngineNavigated(e, cur) })
		}
	})
}

func (a *App) contextMenu(e *etab, tgt string, px, py, mods int, wx, wy float64) {
	var res struct {
		Prevented bool   `json:"prevented"`
		Link      string `json:"link"`
		Image     string `json:"image"`
		Selection string `json:"selection"`
		Editable  bool   `json:"editable"`
	}
	_ = e.eval("__sweefPtr('context',"+itoa(px)+","+itoa(py)+",2,"+itoa(mods)+")", &res)
	e.afterOp()
	if res.Prevented {
		return
	}
	id := e.id
	a.post(func() {
		a.sendChrome("context", map[string]any{"x": wx, "y": wy, "tab": id, "target": tgt,
			"link": res.Link, "image": res.Image, "selection": res.Selection, "editable": res.Editable})
	})
}

func (a *App) onWheel(ev shell.Event) {
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
	if ev.Mods&(shell.ModCtrl|shell.ModMeta) != 0 { // ctrl+wheel zooms
		if ev.DY < 0 {
			a.zoom(10)
		} else if ev.DY > 0 {
			a.zoom(-10)
		}
		return
	}
	e := a.activePage()
	if e == nil {
		return
	}
	e.lastInput.Store(time.Now().UnixNano())
	a.scroll.Wheel(ev.DY * a.scale)
	a.scrollbarT = time.Now()
	a.startAnim()
	x, y := a.pageCoords(ev.X, ev.Y)
	dx, dy := int(ev.DX), int(ev.DY)
	e.w.do(func() { // let page script see the wheel (lazy loaders listen to it)
		_, _ = e.t.Evaluate("__swWheel(" + itoa(dx) + "," + itoa(dy) + "," + itoa(x) + "," + itoa(y) + ")")
		e.drainBindings()
	})
}

// ---- keyboard -------------------------------------------------------------------------------------

func (a *App) ctrlMod(m shell.Mods) bool {
	if m&shell.ModCtrl != 0 {
		return true
	}
	return runtime.GOOS == "darwin" && m&shell.ModMeta != 0
}

func (a *App) onKeyDown(ev shell.Event) {
	if a.shortcut(ev) {
		return
	}
	key := ev.Key
	ctrl := a.ctrlMod(ev.Mods)
	if ctrl && len(key) == 1 {
		switch strings.ToLower(key) {
		case "c":
			a.clipboardOp("copy")
			return
		case "x":
			a.clipboardOp("cut")
			return
		case "v":
			a.clipboardOp("paste")
			return
		case "a":
			a.clipboardOp("selectall")
			return
		}
	}
	// printable characters arrive as EvText; do not type them twice
	if len([]rune(key)) == 1 && ev.Mods&(shell.ModCtrl|shell.ModAlt|shell.ModMeta) == 0 && key != " " {
		return
	}
	e := a.focusTab()
	if e == nil {
		return
	}
	mods := modBits(ev.Mods)
	e.lastInput.Store(time.Now().UnixNano())
	isPage := e.kind == "page"
	if key == "Escape" && a.model.Find.Open {
		a.model.Find = state.Find{}
		a.markState()
		return
	}
	e.w.do(func() {
		var ok bool
		_ = e.eval("__sweefKey("+jsString(key)+","+itoa(mods)+")", &ok)
		editing := false
		if isPage {
			_ = e.eval("__sweefEditing()", &editing)
		}
		e.afterOp()
		if isPage && !editing && (ok || true) {
			a.post(func() { a.scrollKey(key, ev.Mods&shell.ModShift != 0) })
		}
		if isPage {
			a.post(func() { a.afterPageInput(e) })
		}
	})
}

func (a *App) scrollKey(key string, shift bool) {
	_, max := a.comp.Scroll()
	if max == 0 && a.scroll.Max == 0 {
		return
	}
	vh := float64(a.vpDevice().Dy())
	line := 48 * a.scale
	switch key {
	case "ArrowDown":
		a.scroll.Wheel(line)
	case "ArrowUp":
		a.scroll.Wheel(-line)
	case "PageDown":
		a.scroll.Wheel(vh * 0.875)
	case "PageUp":
		a.scroll.Wheel(-vh * 0.875)
	case " ":
		if shift {
			a.scroll.Wheel(-vh * 0.875)
		} else {
			a.scroll.Wheel(vh * 0.875)
		}
	case "Home":
		a.scroll.ScrollTo(0, false)
	case "End":
		a.scroll.ScrollTo(a.scroll.Max, false)
	default:
		return
	}
	a.scrollbarT = time.Now()
	a.startAnim()
}

func (a *App) focusTab() *etab {
	if a.focus == "overlay" {
		if e := a.overlayTab(); e != nil {
			return e
		}
	}
	if a.focus == "page" {
		if e := a.activePage(); e != nil {
			return e
		}
	}
	return a.targetTab("chrome")
}

func (a *App) setFocus(target string) {
	if target == "" || target == a.focus {
		return
	}
	a.focus = target
	a.sendChrome("focus", map[string]any{"target": target})
}

func (a *App) onText(text string) {
	if text == "" {
		return
	}
	e := a.focusTab()
	if e == nil {
		return
	}
	e.lastInput.Store(time.Now().UnixNano())
	isPage := e.kind == "page"
	e.w.do(func() {
		for _, r := range text {
			if r < 0x10000 {
				_, _ = e.t.Evaluate("__sweefKey(" + jsString(string(r)) + ",0)")
			} else {
				_, _ = e.t.Evaluate("__sweefInsert(" + jsString(string(r)) + ")")
			}
		}
		e.afterOp()
		if isPage {
			a.post(func() { a.afterPageInput(e) })
		}
	})
}

// clipboardOp implements copy/cut/paste/select-all for the focused tab.
func (a *App) clipboardOp(op string) {
	e := a.focusTab()
	if e == nil {
		return
	}
	clip := ""
	if op == "paste" {
		clip = a.win.Clipboard()
	}
	e.w.do(func() {
		switch op {
		case "copy", "cut":
			var sel string
			_ = e.eval("__sweefSel()", &sel)
			if sel != "" {
				a.post(func() { a.win.SetClipboard(sel) })
			}
			if op == "cut" {
				_, _ = e.t.Evaluate("__sweefEdit('cut')")
			}
		case "paste":
			if clip != "" {
				_, _ = e.t.Evaluate("__sweefInsert(" + jsString(clip) + ")")
			}
		case "selectall":
			_, _ = e.t.Evaluate("__sweefEdit('selectall')")
		}
		e.afterOp()
	})
}

// focusOmnibox moves keyboard focus into the chrome page's omnibox. The
// chrome page also hears `omnibox.focus`; this makes it work with a page
// that does not implement that message.
func (a *App) focusOmnibox() {
	c := a.targetTab("chrome")
	if c == nil {
		return
	}
	c.w.do(func() {
		_, _ = c.t.Evaluate(`(function(){var o=document.getElementById('omnibox')||document.querySelector('[data-omnibox],input[type=url],input[type=search],input[type=text]');
if(o){o.focus();try{o.setSelectionRange(0,(o.value||'').length)}catch(e){}}})()`)
		c.afterOp()
	})
}

// shortcut handles global shortcuts. It reports whether the key was consumed.
func (a *App) shortcut(ev shell.Event) bool {
	ctrl := a.ctrlMod(ev.Mods)
	shift := ev.Mods&shell.ModShift != 0
	alt := ev.Mods&shell.ModAlt != 0
	key := ev.Key
	if len(key) == 1 {
		key = strings.ToLower(key)
	}
	act := a.model.Active(a.winID)
	switch {
	case ctrl && !shift && !alt && key == "t":
		a.newTab("", false, true)
	case ctrl && shift && key == "t":
		a.reopenClosed()
	case ctrl && !shift && key == "w", ctrl && key == "f4":
		a.closeTab(act)
	case ctrl && !shift && (key == "l" || key == "k") || key == "F6" || alt && key == "d":
		a.setFocus("chrome")
		a.sendChrome("omnibox.focus", map[string]any{"select": true, "mode": map[bool]string{true: "search", false: "url"}[key == "k"]})
		a.focusOmnibox()
	case ctrl && !shift && key == "r", key == "F5":
		a.reload(act, false)
	case ctrl && shift && key == "r":
		a.reload(act, true)
	case ctrl && !shift && key == "f":
		a.model.Find.Open = true
		a.sendChrome("find.open", map[string]any{})
		a.markState()
	case ctrl && key == "Tab" && !shift, ctrl && key == "PageDown":
		a.cycleTab(+1)
	case ctrl && key == "Tab" && shift, ctrl && key == "PageUp":
		a.cycleTab(-1)
	case ctrl && !shift && len(key) == 1 && key >= "1" && key <= "9":
		order := a.model.Order(a.winID)
		n := int(key[0] - '1')
		if key == "9" || n >= len(order) {
			n = len(order) - 1
		}
		if n >= 0 {
			a.activateTab(order[n])
		}
	case alt && key == "ArrowLeft", ctrl && key == "[" && runtime.GOOS == "darwin":
		a.traverse(act, -1)
	case alt && key == "ArrowRight", ctrl && key == "]" && runtime.GOOS == "darwin":
		a.traverse(act, +1)
	case key == "F11":
		a.fullscreen = !a.fullscreen
		a.win.SetFullscreen(a.fullscreen)
	case ctrl && shift && key == "n":
		a.newTab("", true, true)
	case ctrl && shift && key == "a":
		a.setSidebar(!a.model.Sidebar.Open)
	case ctrl && !shift && key == "d":
		a.toggleBookmark(act)
	case ctrl && !shift && key == "u":
		if st, ok := a.model.Tab(act); ok && strings.HasPrefix(st.URL, "http") {
			a.newTab("view-source:"+st.URL, st.Incognito, true)
		}
	case ctrl && !shift && key == "h":
		a.newTab("sweef://history", false, true)
	case ctrl && !shift && key == "j":
		a.newTab("sweef://downloads", false, true)
	case ctrl && (key == "=" || key == "+"):
		a.zoom(10)
	case ctrl && key == "-":
		a.zoom(-10)
	case ctrl && key == "0":
		a.zoom(0)
	case ctrl && !shift && key == "q" && runtime.GOOS != "windows":
		a.requestClose()
	default:
		return false
	}
	return true
}

func (a *App) cycleTab(d int) {
	order := a.model.Order(a.winID)
	if len(order) < 2 {
		return
	}
	act := a.model.Active(a.winID)
	for i, id := range order {
		if id == act {
			a.activateTab(order[(i+d+len(order))%len(order)])
			return
		}
	}
}
