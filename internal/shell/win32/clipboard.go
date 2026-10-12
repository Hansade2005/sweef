//go:build windows

package win32

import (
	"strings"
	"time"
	"unsafe"

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

// SetClipboard places s on the clipboard as CF_UNICODETEXT (CRLF newlines).
func (w *Window) SetClipboard(s string) {
	if w.closed.Load() {
		return
	}
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
	u := keymap.UTF16Z(s)
	size := uintptr(len(u) * 2)
	h, _, _ := pGlobalAlloc.Call(gmemMoveable, size)
	if h == 0 {
		return
	}
	p, _, _ := pGlobalLock.Call(h)
	if p == 0 {
		pGlobalFree.Call(h)
		return
	}
	dst := unsafe.Slice((*uint16)(ptrFrom(p)), len(u))
	copy(dst, u)
	pGlobalUnlock.Call(h)
	if !w.openClipboard() {
		pGlobalFree.Call(h)
		return
	}
	defer pCloseClipboard.Call()
	pEmptyClipboard.Call()
	if r, _, _ := pSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		pGlobalFree.Call(h) // on success the system owns h
	}
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
