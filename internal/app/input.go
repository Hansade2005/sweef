package app

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/engine"
	"github.com/sweefbrowser/sweef/internal/shell"
)

type mouseState struct {
	captured    string // "chrome" | "page" while a button is down
	downX       float64
	downY       float64
	downBtn     int
	moveBusy    atomic.Bool
	moveMu      sync.Mutex
	movePending *[4]int // latest position queued while a move was in flight
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
		if !ev.Focus {
			a.ctxDismiss()
		}
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
		// The hole belongs to the page unless the chrome has something there: a
		// scrim or modal covering it (probed in the chrome with elementFromPoint,
		// the contract's rule), or visible chrome pixels (dropdown, find bar, toast).
		if a.holeBusy.Load() {
			return "chrome"
		}
		if a.comp.ChromeAlphaAt(a.l2d(x), a.l2d(y)) != 0 {
			return "chrome"
		}
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
		a.mouse.moveMu.Lock()
		a.mouse.movePending = &[4]int{px, py, modBits(ev.Mods), b2i(hover)}
		a.mouse.moveMu.Unlock()
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
		tt, lx, ly := e.pointerTarget(tgt, px, py)
		e.leaveHover(tt)
		_, _ = tt.Evaluate("__sweefPtr('move'," + itoa(lx) + "," + itoa(ly) + ",0," + itoa(mods) + ")")
		cur := shell.CursorDefault
		if hover {
			var c string
			err := e.evalOn(tt, "__sweefCursor("+itoa(lx)+","+itoa(ly)+")", &c)
			if err == nil {
				cur = cursorFor(c)
			}
		}
		e.drainBindings()
		e.paint(false)
		// pick up a newer queued position, if any
		a.mouse.moveMu.Lock()
		next := a.mouse.movePending
		a.mouse.movePending = nil
		a.mouse.moveMu.Unlock()
		a.mouse.moveBusy.Store(false)
		if next != nil {
			p := *next
			a.post(func() { a.runMoveNext(tgt, p) })
		}
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
		a.mouse.moveMu.Lock()
		a.mouse.movePending = &p
		a.mouse.moveMu.Unlock()
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
		if tgt != "overlay" && a.ovl.visible() {
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
		if down {
			e.t.NoteUserActivation() // a click is the gesture that allows window.open
		}
	}
	mods := modBits(ev.Mods)
	btn := ev.Button
	if down {
		if btn == 2 {
			e.w.do(func() { a.contextMenu(e, tgt, px, py, mods, ev.X, ev.Y) })
			return
		}
		e.w.do(func() {
			tt, lx, ly := e.pointerTarget(tgt, px, py)
			if tgt == "page" {
				e.t.FocusFrame(tt) // keyboard input follows the click into a frame
				tt.NoteUserActivation()
			}
			e.t.MarkUserActivation() // a real click: sites may now ask for permissions
			_, _ = tt.Evaluate("__sweefPtr('down'," + itoa(lx) + "," + itoa(ly) + "," + itoa(btn) + "," + itoa(mods) + ")")
			e.afterOp()
		})
		return
	}
	if btn == 2 {
		return
	}
	clicked := math.Abs(ev.X-a.mouse.downX) < 6 && math.Abs(ev.Y-a.mouse.downY) < 6
	e.w.do(func() {
		tt, lx, ly := e.pointerTarget(tgt, px, py)
		if tt != e.t && !tt.ScriptsAllowed() {
			// a frame whose sandbox lacks allow-scripts runs no script, not even the input helper: click it natively
			if clicked {
				_ = tt.MouseClick(lx, ly)
			}
			e.afterOp()
			a.post(func() { a.afterPageInput(e) })
			return
		}
		_, _ = tt.Evaluate("__sweefPtr('up'," + itoa(lx) + "," + itoa(ly) + "," + itoa(btn) + "," + itoa(mods) + ")")
		if clicked {
			var res struct {
				Open string `json:"open"`
				BG   bool   `json:"bg"`
				Lite *int   `json:"lite"` // index of a tapped Data Saver placeholder
			}
			_ = e.evalOn(tt, "__sweefPtr('click',"+itoa(lx)+","+itoa(ly)+","+itoa(btn)+","+itoa(mods)+")", &res)
			if res.Lite != nil && tt == e.t {
				a.dataTapAt(e, *res.Lite)
			}
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
		e.sweepThirdPartyCookies()
		if cur := e.t.URL(); cur != "" {
			a.post(func() { a.onEngineNavigated(e, cur) })
		}
	})
}

// contextMenu (right button) lives in ctxmenu.go.

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
	// a frame under the pointer that can still scroll takes the wheel; otherwise the page scrolls
	wx, wy := a.pageCoords(ev.X, ev.Y)
	if e.t.WheelAt(wx, wy, int(ev.DX/a.zoomF()), int(ev.DY/a.zoomF())) {
		return
	}
	if ev.Precise { // trackpad: pixel-accurate deltas already feel smooth, do not add easing
		a.scroll.ScrollTo(a.scroll.Target+ev.DY*a.scale, true)
		a.stepScroll(0)
	} else {
		a.scroll.Wheel(ev.DY * a.scale)
	}
	a.scrollbarT = time.Now()
	a.stepScroll(float64(frameInterval) / float64(time.Second)) // first frame now, not 16 ms from now
	a.startAnim()
	x, y := a.pageCoords(ev.X, ev.Y)
	dx, dy := int(ev.DX), int(ev.DY)
	e.w.tryDo(func() { // let page script see the wheel (lazy loaders listen to it)
		tt, lx, ly := e.t.FrameHit(x, y)
		_, _ = tt.Evaluate("__swWheel(" + itoa(dx) + "," + itoa(dy) + "," + itoa(lx) + "," + itoa(ly) + ")")
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
	if ev.Key == "Escape" && a.htmlFS {
		a.leaveHTMLFullscreen(true)
		return
	}
	if a.shortcut(ev) {
		return
	}
	key := ev.Key
	ctrl := a.ctrlMod(ev.Mods)
	if ctrl && ev.Mods&(shell.ModShift|shell.ModAlt) == 0 && len(key) == 1 {
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
	// Space is printable like any letter: its text arrives as EvText, which is what types it into a field and
	// gives the page its keydown/keypress/keyup. Sending the key as well typed every space twice (the omnibox
	// showed "a  the  quick"; the dropdown collapses whitespace and looked right). Here Space only scrolls a page
	// that is not being typed into.
	if key == " " && ev.Mods&(shell.ModCtrl|shell.ModAlt|shell.ModMeta) == 0 {
		if isPage {
			shift := ev.Mods&shell.ModShift != 0
			e.w.do(func() {
				editing := false
				_ = e.evalOn(e.inputTab(), "__sweefEditing()", &editing)
				if !editing {
					a.post(func() { a.scrollKey(" ", shift) })
				}
			})
		}
		return
	}
	if isPage {
		e.t.FocusedTab().NoteUserActivation() // a key press is a user gesture too
	}
	e.w.do(func() {
		var ok bool
		tt := e.inputTab()
		if key != "Escape" {
			e.t.MarkUserActivation()
		}
		_ = e.evalOn(tt, "__sweefKey("+jsString(key)+","+itoa(mods)+")", &ok)
		editing := false
		if isPage {
			_ = e.evalOn(tt, "__sweefEditing()", &editing)
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
	if isPage {
		e.t.FocusedTab().NoteUserActivation()
	}
	e.w.do(func() {
		tt := e.inputTab()
		for _, r := range text {
			if r < 0x10000 {
				t0 := time.Now()
				e.t.MarkUserActivation()
				_, _ = tt.Evaluate("__sweefKey(" + jsString(string(r)) + ",0)")
				if e.kind == "chrome" {
					// the chrome page's own JavaScript handler, for the stats breakdown
					a.comp.RecordPaint("chrome: input handler (JS)", time.Since(t0))
				}
			} else {
				_, _ = tt.Evaluate("__sweefInsert(" + jsString(string(r)) + ")")
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
	var flavours []engine.ClipboardEntry
	if op == "paste" {
		flavours = a.clip.readNow()
	}
	e.w.do(func() {
		tt := e.inputTab()
		switch op {
		case "copy", "cut":
			var r struct {
				Handled bool `json:"handled"`
				Entries []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"entries"`
			}
			_ = e.evalOn(tt, "__sweefCopy("+jsString(op)+")", &r)
			var es []engine.ClipboardEntry
			for _, x := range r.Entries {
				es = append(es, engine.ClipboardEntry{Type: x.Type, Data: []byte(x.Text)})
			}
			if len(es) > 0 {
				a.post(func() { _ = a.clip.writeNow(es) })
			}
			if op == "cut" && !r.Handled {
				_, _ = tt.Evaluate("__sweefEdit('cut')")
			}
		case "paste":
			type item struct {
				Type string `json:"type"`
				Text string `json:"text,omitempty"`
				B64  string `json:"b64,omitempty"`
			}
			var items []item
			for _, f := range flavours {
				switch {
				case f.Type == "text/plain" || f.Type == "text/html":
					items = append(items, item{Type: f.Type, Text: string(f.Data)})
				case strings.HasPrefix(f.Type, "image/"):
					items = append(items, item{Type: f.Type, B64: base64.StdEncoding.EncodeToString(f.Data)})
				}
			}
			if len(items) > 0 {
				b, _ := json.Marshal(items)
				_, _ = tt.Evaluate("__sweefPaste(" + string(b) + ")")
			}
		case "selectall":
			_, _ = tt.Evaluate("__sweefEdit('selectall')")
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

// shortcut handles global accelerators and reports whether the key was consumed.
//
// Accelerators the chrome page implements itself (Ctrl+K, L, F, T, W, D,
// Ctrl+Shift+A, F6, Alt+D) are not acted on here: with the chrome focused the
// key is forwarded to it as an ordinary key event, with the page focused it is
// turned into a `ui.command` so the chrome does the work. Everything else
// (tab cycling, reload, history navigation, zoom, ...) is handled here
// whichever surface has focus.
func (a *App) shortcut(ev shell.Event) bool {
	ctrl := a.ctrlMod(ev.Mods)
	shift := ev.Mods&shell.ModShift != 0
	alt := ev.Mods&shell.ModAlt != 0
	key := ev.Key
	if len(key) == 1 {
		key = strings.ToLower(key)
	}
	act := a.model.Active(a.winID)
	uiCmd := ""
	switch {
	case ctrl && !shift && !alt && key == "k":
		uiCmd = "palette"
	case ctrl && !shift && key == "l", key == "F6", alt && !ctrl && key == "d":
		uiCmd = "focusOmnibox"
	case ctrl && !shift && key == "f":
		uiCmd = "find.open"
	case ctrl && shift && key == "a":
		uiCmd = "sidebar.toggle"
	case ctrl && !shift && !alt && key == "t":
		uiCmd = "tab.new"
	case ctrl && !shift && !alt && key == "w":
		uiCmd = "tab.closeActive"
	case ctrl && !shift && !alt && key == "d":
		uiCmd = "bookmark.toggle"
	case alt && shift && !ctrl && key == "p" && a.hasPrompt(act):
		uiCmd = "perm.focus" // answer the permission question next to the address bar
	}
	if uiCmd != "" {
		if a.focus == "chrome" {
			return false // the chrome page sees the key and runs the command itself
		}
		a.runUICommand(uiCmd)
		return true
	}
	switch {
	case key == "F12", ctrl && shift && !alt && (key == "i" || ev.Code == "KeyI"), ctrl && alt && !shift && runtime.GOOS == "darwin" && (key == "i" || ev.Code == "KeyI"):
		a.toggleDevTools()
	case ctrl && shift && !alt && (key == "j" || ev.Code == "KeyJ"):
		a.openDevToolsPanel(act, "console")
	case ctrl && shift && !alt && (key == "c" || ev.Code == "KeyC"):
		a.openDevToolsPanel(act, "elements")
	case ctrl && shift && key == "t":
		a.reopenClosed()
	case ctrl && key == "f4":
		a.closeTab(act)
	case ctrl && !shift && key == "r", key == "F5":
		a.reload(act, false)
	case ctrl && shift && key == "r":
		a.reload(act, true)
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
	case key == "F11" && a.htmlFS:
		a.leaveHTMLFullscreen(true)
	case key == "F11":
		a.fullscreen = !a.fullscreen
		a.win.SetFullscreen(a.fullscreen)
	case ctrl && shift && key == "n":
		a.newTab("", true, true)
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

// runUICommand asks the chrome page to run one of its commands (ui.command).
func (a *App) runUICommand(name string) {
	switch name {
	case "palette", "focusOmnibox", "find.open", "sidebar.toggle", "sidebar.focus", "agent.chat.new", "perm.focus":
		a.setFocus("chrome") // these put the keyboard focus into the chrome page
	}
	a.sendChrome("ui.command", map[string]any{"name": name})
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
