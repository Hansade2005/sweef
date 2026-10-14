//go:build windows

package win32

import (
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sweefbrowser/sweef/internal/shell/keymap"
)

// openClipboard retries briefly: another process may hold the clipboard.
func (w *Window) openClipboard() bool {
	for i := 0; i < 20; i++ {
		if r, _, _ := pOpenClipboard.Call(w.hwnd); r != 0 {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// globalFrom copies b into a new movable global memory block, which SetClipboardData takes over on success.
func globalFrom(b []byte) uintptr {
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, uintptr(len(b)))
	if h == 0 {
		return 0
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return 0
	}
	copy(unsafe.Slice((*byte)(ptrFrom(p)), len(b)), b)
	pGlobalUnlock.Call(h)
	return h
}

func utf16Bytes(s string) []byte {
	u := keymap.UTF16Z(s)
	b := make([]byte, 0, len(u)*2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return b
}

var htmlFormat uintptr

// cfHTML is the registered clipboard format "HTML Format".
func cfHTML() uintptr {
	if htmlFormat == 0 {
		name, _ := windows.UTF16PtrFromString("HTML Format")
		htmlFormat, _, _ = pRegisterClipboardFormatW.Call(uintptr(unsafe.Pointer(name)))
	}
	return htmlFormat
}

// SetClipboard places s on the clipboard as CF_UNICODETEXT (CRLF newlines).
func (w *Window) SetClipboard(s string) { w.setClipboard(s, "") }

// SetClipboardRich places the text and its HTML form on the clipboard together (shell.RichClipboard).
func (w *Window) SetClipboardRich(text, html string) { w.setClipboard(text, html) }

func (w *Window) setClipboard(s, html string) {
	if w.closed.Load() {
		return
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	h := globalFrom(utf16Bytes(s))
	if h == 0 {
		return
	}
	var hh uintptr
	if html != "" {
		hh = globalFrom(htmlClipboardData(html))
	}
	if !w.openClipboard() {
		pGlobalFree.Call(h)
		if hh != 0 {
			pGlobalFree.Call(hh)
		}
		return
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	if r, _, _ := pSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		pGlobalFree.Call(h) // on success the system owns h
	}
	if hh != 0 {
		if r, _, _ := pSetClipboardData.Call(cfHTML(), hh); r == 0 {
			pGlobalFree.Call(hh)
		}
	}
}

// ClipboardHTML returns the HTML fragment on the clipboard ("" when there is none).
func (w *Window) ClipboardHTML() string {
	if w.closed.Load() {
		return ""
	}
	f := cfHTML()
	if f == 0 {
		return ""
	}
	if r, _, _ := pIsClipboardFormatAvail.Call(f); r == 0 {
		return ""
	}
	if !w.openClipboard() {
		return ""
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardData.Call(f)
	if h == 0 {
		return ""
	}
	size, _, _ := pGlobalSize.Call(h)
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer pGlobalUnlock.Call(h)
	if size > 64<<20 {
		size = 64 << 20
	}
	buf := make([]byte, size)
	copy(buf, unsafe.Slice((*byte)(ptrFrom(p)), size))
	return htmlFragment(buf)
}

// Clipboard returns the clipboard text ("" when it holds no text).
func (w *Window) Clipboard() string {
	if w.closed.Load() {
		return ""
	}
	if r, _, _ := pIsClipboardFormatAvail.Call(cfUnicodeText); r == 0 {
		return ""
	}
	if !w.openClipboard() {
		return ""
	}
	defer pCloseClipboard.Call()
	h, _, _ := pGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return ""
	}
	size, _, _ := pGlobalSize.Call(h)
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		return ""
	}
	defer pGlobalUnlock.Call(h)
	n := int(size / 2)
	if n > 1<<27 {
		n = 1 << 27 // 256 MiB cap
	}
	u := unsafe.Slice((*uint16)(ptrFrom(p)), n)
	end := 0
	for end < len(u) && u[end] != 0 {
		end++
	}
	return strings.ReplaceAll(keymap.FromUTF16(u[:end]), "\r\n", "\n")
}
