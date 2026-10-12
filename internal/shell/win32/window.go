//go:build windows

// Package win32 is the Windows native window shell: pure Go (no CGO) over
// user32/gdi32/shcore/dwmapi/shell32. A dedicated, OS-thread-locked
// goroutine owns the window and its message loop; everything else talks to
// it through Present (thread-safe), the event queue and posted closures.
//
// NOTE: written without the ability to run Windows; see ../README.md
// (status: compiled and vetted only, runtime behaviour UNVERIFIED).
package win32

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sweefbrowser/sweef/internal/shell"
	"github.com/sweefbrowser/sweef/internal/shell/evqueue"
	"github.com/sweefbrowser/sweef/internal/shell/keymap"
)

const className = "SweefWindowClass"

var (
	initOnce  sync.Once
	classOnce sync.Once
	classErr  error

	wndProcCB = windows.NewCallback(wndProc)

	// windows maps HWND -> *Window for the window procedure.
	windowsMu sync.RWMutex
	registry  = map[uintptr]*Window{}

	createMu sync.Mutex
	pending  atomic.Pointer[Window] // window being created (messages before WM_NCCREATE)
)

// Window implements shell.Window on Win32.
type Window struct {
	hwnd uintptr
	q    *evqueue.Queue
	done chan struct{}

	closed atomic.Bool

	gmu        sync.Mutex
	clientW    int // device pixels
	clientH    int
	scale      float64
	wheelLines int

	// accessed only on the UI thread
	buttons   int
	tracking  bool
	focused   bool
	sj        keymap.SurrogateJoiner
	fs        bool
	prevStyle uintptr
	prevPlace windowPlacement
	cursor    uintptr
	hicon     [2]uintptr

	// DIB section the frames are converted into
	dmu     sync.Mutex
	memDC   uintptr
	origBmp uintptr
	bmp     uintptr
	pix     []byte
	capW    int
	capH    int

	fnMu sync.Mutex
	fns  []func()
}

// Open creates a window and starts its message loop.
func Open(cfg shell.Config) (shell.Window, error) {
	w := &Window{q: evqueue.New(), done: make(chan struct{}), scale: 1, wheelLines: 3}
	ready := make(chan error, 1)
	go func() {
		runtime.LockOSThread() // window + message loop live on this thread forever
		err := w.create(cfg)
		ready <- err
		if err != nil {
			close(w.done)
			return
		}
		w.loop()
		w.cleanup()
		w.q.Close()
		close(w.done)
	}()
	if err := <-ready; err != nil {
		w.q.Close()
		return nil, err
	}
	return w, nil
}

func initProcess() {
	initOnce.Do(func() {
		// Per-monitor-v2 awareness without a manifest. Fails harmlessly if a
		// manifest or an earlier call already fixed the awareness mode.
		if pSetProcessDpiAwarenessContext.Find() == nil {
			if r, _, _ := pSetProcessDpiAwarenessContext.Call(dpiPerMonitorAwareV2); r != 0 {
				return
			}
		}
		if pSetProcessDpiAwareness.Find() == nil {
			pSetProcessDpiAwareness.Call(2) // PROCESS_PER_MONITOR_DPI_AWARE
			return
		}
		pSetProcessDPIAware.Call()
	})
}

func registerClass() error {
	classOnce.Do(func() {
		hInst, _, _ := pGetModuleHandleW.Call(0)
		cur, _, _ := pLoadCursorW.Call(0, idcArrow)
		wc := wndClassEx{
			Style:     csHRedraw | csVRedraw,
			WndProc:   wndProcCB,
			Instance:  hInst,
			Cursor:    cur,
			ClassName: windows.StringToUTF16Ptr(className),
		}
		wc.CbSize = uint32(unsafe.Sizeof(wc))
		r, _, err := pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		if r == 0 {
			classErr = fmt.Errorf("win32: RegisterClassExW: %w", err)
		}
	})
	return classErr
}

func systemDPI() uint32 {
	if pGetDpiForSystem.Find() == nil {
		if r, _, _ := pGetDpiForSystem.Call(); r != 0 {
			return uint32(r)
		}
	}
	dc, _, _ := pGetDC.Call(0)
	if dc != 0 {
		defer pReleaseDC.Call(0, dc)
		if r, _, _ := pGetDeviceCaps.Call(dc, 88); r != 0 { // LOGPIXELSX
			return uint32(r)
		}
	}
	return 96
}

func windowDPI(hwnd uintptr) uint32 {
	if pGetDpiForWindow.Find() == nil {
		if r, _, _ := pGetDpiForWindow.Call(hwnd); r != 0 {
			return uint32(r)
		}
	}
	return systemDPI()
}

func adjustRect(r *rect, style, exStyle uintptr, dpi uint32) {
	if pAdjustWindowRectExFD.Find() == nil {
		if ok, _, _ := pAdjustWindowRectExFD.Call(uintptr(unsafe.Pointer(r)), style, 0, exStyle, uintptr(dpi)); ok != 0 {
			return
		}
	}
	pAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(r)), style, 0, exStyle)
}

// create runs on the UI thread.
func (w *Window) create(cfg shell.Config) error {
	initProcess()
	if err := registerClass(); err != nil {
		return err
	}
	hInst, _, _ := pGetModuleHandleW.Call(0)
	dpi := systemDPI()
	scale := keymap.ScaleFromDPI(float64(dpi))
	cw, ch := cfg.Width, cfg.Height
	if cw <= 0 {
		cw = 1280
	}
	if ch <= 0 {
		ch = 800
	}
	const style = wsOverlappedWindow | wsClipChildren | wsClipSiblings
	r := rect{0, 0, int32(keymap.ToDevice(cw, scale)), int32(keymap.ToDevice(ch, scale))}
	adjustRect(&r, style, wsExAppWindow, dpi)

	title := cfg.Title
	if title == "" {
		title = "Sweef"
	}
	titleW := keymap.UTF16Z(title)

	createMu.Lock()
	pending.Store(w)
	hwnd, _, err := pCreateWindowExW.Call(
		wsExAppWindow,
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(className))),
		uintptr(unsafe.Pointer(&titleW[0])),
		style,
		uintptr(uint32(cwUseDefault)), uintptr(uint32(cwUseDefault)),
		uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top),
		0, 0, hInst, 0)
	pending.Store(nil)
	createMu.Unlock()
	if hwnd == 0 {
		return fmt.Errorf("win32: CreateWindowExW: %w", err)
	}
	w.hwnd = hwnd

	// The window may have landed on a monitor with a different DPI.
	if wd := windowDPI(hwnd); wd != dpi {
		scale = keymap.ScaleFromDPI(float64(wd))
		nr := rect{0, 0, int32(keymap.ToDevice(cw, scale)), int32(keymap.ToDevice(ch, scale))}
		adjustRect(&nr, style, wsExAppWindow, wd)
		pSetWindowPos.Call(hwnd, 0, 0, 0, uintptr(nr.Right-nr.Left), uintptr(nr.Bottom-nr.Top),
			swpNoMove|swpNoZOrder|swpNoActivate)
	}
	var cr rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
	w.gmu.Lock()
	w.scale = scale
	w.clientW, w.clientH = int(cr.Right-cr.Left), int(cr.Bottom-cr.Top)
	w.gmu.Unlock()

	var lines uint32
	if ok, _, _ := pSystemParametersInfoW.Call(spiGetWheelScrollLines, 0, uintptr(unsafe.Pointer(&lines)), 0); ok != 0 && lines > 0 && lines != 0xFFFFFFFF {
		w.wheelLines = int(lines)
	}

	dc, _, _ := pCreateCompatibleDC.Call(0)
	w.memDC = dc
	setDarkTitleBar(hwnd, cfg.Dark)
	pDragAcceptFiles.Call(hwnd, 1)
	if cfg.Icon != nil {
		w.postFn(func() { w.setIconUI(cfg.Icon) })
	}
	pShowWindow.Call(hwnd, swShowNormal)

	lw, lh := keymap.LogicalSize(w.clientW, w.clientH, scale)
	w.q.Push(shell.Event{Kind: shell.EvResize, W: lw, H: lh, Scale: scale})
	return nil
}

func setDarkTitleBar(hwnd uintptr, dark bool) {
	if pDwmSetWindowAttribute.Find() != nil {
		return
	}
	v := int32(0)
	if dark {
		v = 1
	}
	// Attribute 20 is documented (Windows 10 20H1+/11); 19 is the pre-release
	// value used by Windows 10 1903-2004.
	hr, _, _ := pDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&v)), 4)
	if hr != 0 {
		pDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkModeOld, uintptr(unsafe.Pointer(&v)), 4)
	}
}

// loop is the message pump; it returns after WM_QUIT.
func (w *Window) loop() {
	var m msg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // 0 = WM_QUIT, -1 = error
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (w *Window) cleanup() {
	w.dmu.Lock()
	if w.memDC != 0 {
		if w.origBmp != 0 {
			pSelectObject.Call(w.memDC, w.origBmp)
		}
		if w.bmp != 0 {
			pDeleteObject.Call(w.bmp)
		}
		pDeleteDC.Call(w.memDC)
		w.memDC, w.bmp, w.pix = 0, 0, nil
	}
	w.dmu.Unlock()
	for i, h := range w.hicon {
		if h != 0 {
			pDestroyIcon.Call(h)
			w.hicon[i] = 0
		}
	}
	windowsMu.Lock()
	delete(registry, w.hwnd)
	windowsMu.Unlock()
}

// postFn runs f on the UI thread.
func (w *Window) postFn(f func()) {
	if w.hwnd == 0 {
		// still creating: we are on the UI thread already
		f()
		return
	}
	w.fnMu.Lock()
	w.fns = append(w.fns, f)
	w.fnMu.Unlock()
	pPostMessageW.Call(w.hwnd, wmApp, 0, 0)
}

func (w *Window) runPosted() {
	w.fnMu.Lock()
	fns := w.fns
	w.fns = nil
	w.fnMu.Unlock()
	for _, f := range fns {
		f()
	}
}

// ---- shell.Window ----------------------------------------------------------

func (w *Window) Events() <-chan shell.Event { return w.q.Events() }

func (w *Window) Size() (int, int, float64) {
	w.gmu.Lock()
	defer w.gmu.Unlock()
	lw, lh := keymap.LogicalSize(w.clientW, w.clientH, w.scale)
	return lw, lh, w.scale
}

func (w *Window) devSize() (int, int) {
	w.gmu.Lock()
	defer w.gmu.Unlock()
	return w.clientW, w.clientH
}

func (w *Window) SetTitle(t string) {
	if w.closed.Load() {
		return
	}
	tw := keymap.UTF16Z(t)
	w.postFn(func() { pSetWindowTextW.Call(w.hwnd, uintptr(unsafe.Pointer(&tw[0]))) })
}

var cursorIDs = map[shell.Cursor]uintptr{
	shell.CursorDefault: idcArrow, shell.CursorPointer: idcHand, shell.CursorText: idcIBeam,
	shell.CursorWait: idcWait, shell.CursorCrosshair: idcCross, shell.CursorResizeEW: idcSizeWE,
	shell.CursorResizeNS: idcSizeNS, shell.CursorNotAllowed: idcNo, shell.CursorGrab: idcSizeAll,
}

func (w *Window) SetCursor(c shell.Cursor) {
	if w.closed.Load() {
		return
	}
	id, ok := cursorIDs[c]
	if !ok {
		id = idcArrow
	}
	w.postFn(func() {
		h, _, _ := pLoadCursorW.Call(0, id) // shared system cursors need no cleanup
		w.cursor = h
		pSetCursor.Call(h)
	})
}

func (w *Window) SetFullscreen(on bool) {
	if w.closed.Load() {
		return
	}
	w.postFn(func() { w.setFullscreenUI(on) })
}

func (w *Window) setFullscreenUI(on bool) {
	if on == w.fs {
		return
	}
	hwnd := w.hwnd
	if on {
		w.prevStyle = getWindowLong(hwnd, gwlStyle)
		w.prevPlace = windowPlacement{Length: uint32(unsafe.Sizeof(windowPlacement{}))}
		pGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&w.prevPlace)))
		mon, _, _ := pMonitorFromWindow.Call(hwnd, monitorDefaultToNearest)
		mi := monitorInfo{CbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
		if ok, _, _ := pGetMonitorInfoW.Call(mon, uintptr(unsafe.Pointer(&mi))); ok == 0 {
			return
		}
		setWindowLong(hwnd, gwlStyle, w.prevStyle&^wsOverlappedWindow)
		pSetWindowPos.Call(hwnd, 0, uintptr(mi.RcMonitor.Left), uintptr(mi.RcMonitor.Top),
			uintptr(mi.RcMonitor.Right-mi.RcMonitor.Left), uintptr(mi.RcMonitor.Bottom-mi.RcMonitor.Top),
			swpNoOwnerZOrder|swpFrameChanged)
	} else {
		setWindowLong(hwnd, gwlStyle, w.prevStyle)
		pSetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&w.prevPlace)))
		pSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
			swpNoMove|swpNoSize|swpNoZOrder|swpNoOwnerZOrder|swpFrameChanged)
	}
	w.fs = on
}

// OpenURL opens url with the registered handler (ShellExecuteW "open").
// keymap.CheckOpenURL enforces an absolute URL with a real scheme, so drive
// paths ("C:\\x.exe"), file: URLs and bare command lines are never executed.
func (w *Window) OpenURL(raw string) error {
	if _, err := keymap.CheckOpenURL(raw); err != nil {
		return fmt.Errorf("win32: %w", err)
	}
	verb := keymap.UTF16Z("open")
	target := keymap.UTF16Z(raw)
	r, _, _ := pShellExecuteW.Call(0, uintptr(unsafe.Pointer(&verb[0])), uintptr(unsafe.Pointer(&target[0])), 0, 0, swShowNormal)
	if r <= 32 {
		return fmt.Errorf("win32: ShellExecute failed (code %d)", r)
	}
	return nil
}

// Close destroys the window and stops the message loop.
func (w *Window) Close() {
	if !w.closed.CompareAndSwap(false, true) {
		return
	}
	w.postFn(func() { pDestroyWindow.Call(w.hwnd) })
	select {
	case <-w.done:
	case <-time.After(3 * time.Second):
		// UI thread is wedged; ask for quit and give up waiting.
		pPostMessageW.Call(w.hwnd, wmClose, 0, 0)
	}
}

// ---- window procedure ------------------------------------------------------

func wndProc(hwnd, umsg, wparam, lparam uintptr) (ret uintptr) {
	defer func() {
		if r := recover(); r != nil {
			// A Go panic must never unwind into Windows.
			ret, _, _ = pDefWindowProcW.Call(hwnd, umsg, wparam, lparam)
		}
	}()
	w := func() *Window {
		windowsMu.RLock()
		w := registry[hwnd]
		windowsMu.RUnlock()
		return w
	}()
	if w == nil {
		if p := pending.Load(); umsg == wmNCCreate && p != nil {
			w = p
			w.hwnd = hwnd
			windowsMu.Lock()
			registry[hwnd] = w
			windowsMu.Unlock()
		} else {
			r, _, _ := pDefWindowProcW.Call(hwnd, umsg, wparam, lparam)
			return r
		}
	}
	if r, handled := w.handle(hwnd, uint32(umsg), wparam, lparam); handled {
		return r
	}
	r, _, _ := pDefWindowProcW.Call(hwnd, umsg, wparam, lparam)
	return r
}
