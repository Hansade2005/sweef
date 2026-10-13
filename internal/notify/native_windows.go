//go:build windows

package notify

// Windows notifications through Shell_NotifyIconW balloon notifications, which Windows 10 and 11 present as toast
// notifications. Each notification gets its own (hidden-after-use) notification-area icon, because one icon can
// show only one balloon at a time. A message-only window owned by an OS-thread-locked goroutine receives the
// callbacks: NIN_BALLOONUSERCLICK is a click, NIN_BALLOONTIMEOUT is a timeout.
//
// UNVERIFIED: this file is compiled and vetted for windows/amd64 but has never been run on Windows. Balloon
// notifications have no action buttons, so Notification.Actions are not shown here, and a notification that the
// user lets time out may go to the Action Center without a further event.

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32          = windows.NewLazySystemDLL("shell32.dll")
	user32           = windows.NewLazySystemDLL("user32.dll")
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	pShellNotifyIcon = shell32.NewProc("Shell_NotifyIconW")
	pRegisterClassEx = user32.NewProc("RegisterClassExW")
	pCreateWindowEx  = user32.NewProc("CreateWindowExW")
	pDestroyWindow   = user32.NewProc("DestroyWindow")
	pDefWindowProc   = user32.NewProc("DefWindowProcW")
	pGetMessage      = user32.NewProc("GetMessageW")
	pDispatchMessage = user32.NewProc("DispatchMessageW")
	pPostMessage     = user32.NewProc("PostMessageW")
	pLoadIcon        = user32.NewProc("LoadIconW")
	pGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
)

const (
	nimAdd          = 0
	nimModify       = 1
	nimDelete       = 2
	nimSetVersion   = 4
	nifMessage      = 0x1
	nifIcon         = 0x2
	nifTip          = 0x4
	nifInfo         = 0x10
	niifInfo        = 0x1
	niifNoSound     = 0x10
	niifLargeIcon   = 0x20
	wmApp           = 0x8000
	wmCallback      = wmApp + 1
	wmQuitSink      = wmApp + 2
	wmClose         = 0x10
	ninBalloonShow  = 0x402
	ninBalloonHide  = 0x403
	ninBalloonTime  = 0x404
	ninBalloonClick = 0x405
	hwndMessage     = ^uintptr(2) // HWND_MESSAGE (-3)
)

// notifyIconData is NOTIFYICONDATAW (Windows Vista and later layout).
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type winMsg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

type winSink struct {
	h     handlerBox
	hwnd  uintptr
	icon  uintptr
	ready chan error
	done  chan struct{}

	mu   sync.Mutex
	next uint32
	byID map[string]uint32
	byUI map[uint32]string
}

var theWin *winSink // the window procedure is a plain callback: one sink per process

// Native opens the Windows balloon notification sink.
func Native(appName string) (Sink, error) {
	if theWin != nil {
		return nil, errors.New("notify: the Windows sink is already open")
	}
	s := &winSink{ready: make(chan error, 1), done: make(chan struct{}), byID: map[string]uint32{}, byUI: map[uint32]string{}}
	theWin = s
	go s.loop(appName)
	if err := <-s.ready; err != nil {
		theWin = nil
		return nil, errors.Join(ErrUnavailable, err)
	}
	return s, nil
}

func (s *winSink) Name() string             { return "windows-balloon" }
func (s *winSink) SetHandler(f func(Event)) { s.h.set(f) }

func utf16Fixed(dst []uint16, str string) {
	u, _ := windows.UTF16FromString(str)
	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0
	}
	copy(dst, u)
}

func (s *winSink) loop(appName string) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(s.done)
	inst, _, _ := pGetModuleHandle.Call(0)
	name, _ := windows.UTF16PtrFromString("SweefNotifyWindow")
	cb := windows.NewCallback(func(hwnd uintptr, msg uint32, wp, lp uintptr) uintptr {
		if msg == wmCallback {
			s.callback(uint32(wp), uint32(lp)&0xffff)
			return 0
		}
		r, _, _ := pDefWindowProc.Call(hwnd, uintptr(msg), wp, lp)
		return r
	})
	wc := wndClassEx{LpfnWndProc: cb, HInstance: inst, LpszClassName: name}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	if a, _, err := pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc))); a == 0 {
		s.ready <- err
		return
	}
	title, _ := windows.UTF16PtrFromString(appName)
	hwnd, _, err := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(title)), 0, 0, 0, 0, 0, hwndMessage, 0, inst, 0)
	if hwnd == 0 {
		s.ready <- err
		return
	}
	s.hwnd = hwnd
	s.icon, _, _ = pLoadIcon.Call(0, 32512) // IDI_APPLICATION
	s.ready <- nil
	var m winMsg
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 || (m.Message == wmQuitSink) {
			break
		}
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
	pDestroyWindow.Call(hwnd)
}

func (s *winSink) data(uid uint32, flags uint32) *notifyIconData {
	d := &notifyIconData{HWnd: s.hwnd, UID: uid, UFlags: flags, UCallbackMessage: wmCallback, HIcon: s.icon}
	d.CbSize = uint32(unsafe.Sizeof(*d))
	return d
}

func (s *winSink) callback(uid, ev uint32) {
	s.mu.Lock()
	id := s.byUI[uid]
	s.mu.Unlock()
	if id == "" {
		return
	}
	switch ev {
	case ninBalloonClick:
		s.remove(id, uid)
		s.h.emit(Event{Kind: Click, ID: id})
	case ninBalloonTime, ninBalloonHide:
		s.remove(id, uid)
		s.h.emit(Event{Kind: Closed, ID: id, Reason: ReasonTimeout})
	}
}

func (s *winSink) remove(id string, uid uint32) {
	s.mu.Lock()
	delete(s.byID, id)
	delete(s.byUI, uid)
	s.mu.Unlock()
	d := s.data(uid, 0)
	pShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(d)))
}

func (s *winSink) Show(n Notification) error {
	select {
	case <-s.done:
		return ErrUnavailable
	default:
	}
	s.mu.Lock()
	s.next++
	uid := s.next
	s.byID[n.ID], s.byUI[uid] = uid, n.ID
	s.mu.Unlock()
	d := s.data(uid, nifMessage|nifIcon|nifTip|nifInfo)
	utf16Fixed(d.SzTip[:], "Sweef")
	title := n.Title
	if n.Origin != "" {
		title = n.Title + " - " + n.Origin
	}
	utf16Fixed(d.SzInfoTitle[:], title)
	utf16Fixed(d.SzInfo[:], n.Body)
	d.DwInfoFlags = niifInfo
	if n.Silent {
		d.DwInfoFlags |= niifNoSound
	}
	if r, _, err := pShellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(d))); r == 0 {
		s.mu.Lock()
		delete(s.byID, n.ID)
		delete(s.byUI, uid)
		s.mu.Unlock()
		return errors.Join(ErrUnavailable, err)
	}
	d.UVersion = 4
	pShellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(d)))
	return nil
}

func (s *winSink) Close(id string) {
	s.mu.Lock()
	uid, ok := s.byID[id]
	s.mu.Unlock()
	if ok {
		s.remove(id, uid)
	}
}

func (s *winSink) Shutdown() error {
	s.mu.Lock()
	ids := map[string]uint32{}
	for k, v := range s.byID {
		ids[k] = v
	}
	s.mu.Unlock()
	for id, uid := range ids {
		s.remove(id, uid)
	}
	pPostMessage.Call(s.hwnd, wmQuitSink, 0, 0)
	<-s.done
	theWin = nil
	return nil
}
