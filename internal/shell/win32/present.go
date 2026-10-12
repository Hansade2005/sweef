//go:build windows

package win32

import (
	"image"
	"unsafe"

	"github.com/sweefbrowser/sweef/internal/shell/pixconv"
)

// ensureDIB makes the backing DIB section at least dw x dh, keeping its
// contents. Caller holds dmu.
func (w *Window) ensureDIB(dw, dh int) bool {
	if w.pix != nil && dw <= w.capW && dh <= w.capH {
		return true
	}
	if w.memDC == 0 {
		return false
	}
	ncw, nch := (max(dw, w.capW)+63)&^63, (max(dh, w.capH)+63)&^63
	var bi bitmapInfo
	bi.Header = bitmapInfoHeader{
		Size: uint32(unsafe.Sizeof(bi.Header)), Width: int32(ncw), Height: -int32(nch), // negative = top-down
		Planes: 1, BitCount: 32, Compression: biRGB,
	}
	var bits unsafe.Pointer
	hbm, _, _ := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbm == 0 || bits == nil {
		return false
	}
	npix := unsafe.Slice((*byte)(bits), ncw*nch*4)
	if w.pix != nil {
		rows, n := min(w.capH, nch), min(w.capW, ncw)*4
		for y := 0; y < rows; y++ {
			copy(npix[y*ncw*4:y*ncw*4+n], w.pix[y*w.capW*4:y*w.capW*4+n])
		}
	}
	old, _, _ := pSelectObject.Call(w.memDC, hbm)
	if w.bmp != 0 {
		pDeleteObject.Call(w.bmp)
	} else {
		w.origBmp = old
	}
	w.bmp, w.pix, w.capW, w.capH = hbm, npix, ncw, nch
	return true
}

// Present converts the damaged part of frame into the DIB section and blits
// it to the window. It may be called from any goroutine.
func (w *Window) Present(frame *image.RGBA, damage image.Rectangle) {
	if frame == nil || w.closed.Load() {
		return
	}
	r := damage
	if r.Empty() {
		r = frame.Rect
	}
	r = r.Intersect(frame.Rect)
	dw, dh := w.devSize()
	r = r.Intersect(image.Rect(0, 0, dw, dh))
	if r.Empty() {
		return
	}
	w.dmu.Lock()
	defer w.dmu.Unlock()
	if !w.ensureDIB(dw, dh) {
		return
	}
	// DIB 32 bpp BI_RGB is B,G,R,x in memory; frame is R,G,B,A.
	pixconv.ConvertRect(w.pix, w.capW*4, frame.Pix, frame.Stride, frame.Rect.Min.X, frame.Rect.Min.Y,
		r.Min.X, r.Min.Y, r.Max.X, r.Max.Y, false)
	dc, _, _ := pGetDC.Call(w.hwnd)
	if dc == 0 {
		return
	}
	pBitBlt.Call(dc, uintptr(r.Min.X), uintptr(r.Min.Y), uintptr(r.Dx()), uintptr(r.Dy()),
		w.memDC, uintptr(r.Min.X), uintptr(r.Min.Y), srccopy)
	pGdiFlush.Call() // the DIB may be rewritten as soon as we unlock
	pReleaseDC.Call(w.hwnd, dc)
	rc := rect{int32(r.Min.X), int32(r.Min.Y), int32(r.Max.X), int32(r.Max.Y)}
	pValidateRect.Call(w.hwnd, uintptr(unsafe.Pointer(&rc)))
}

// paint repaints an invalidated rectangle from the DIB (WM_PAINT, UI thread).
func (w *Window) paint(hdc uintptr, rc rect) {
	if rc.Right <= rc.Left || rc.Bottom <= rc.Top {
		return
	}
	w.dmu.Lock()
	defer w.dmu.Unlock()
	if w.pix == nil || w.memDC == 0 {
		return
	}
	pBitBlt.Call(hdc, uintptr(rc.Left), uintptr(rc.Top), uintptr(rc.Right-rc.Left), uintptr(rc.Bottom-rc.Top),
		w.memDC, uintptr(rc.Left), uintptr(rc.Top), srccopy)
	pGdiFlush.Call()
}

// SetIcon sets the window icon from a premultiplied RGBA image.
func (w *Window) SetIcon(icon *image.RGBA) {
	if icon == nil || w.closed.Load() {
		return
	}
	cp := image.NewRGBA(icon.Rect)
	copy(cp.Pix, icon.Pix)
	w.postFn(func() { w.setIconUI(cp) })
}

func (w *Window) setIconUI(icon *image.RGBA) {
	iw, ih := icon.Rect.Dx(), icon.Rect.Dy()
	if iw <= 0 || ih <= 0 || iw > 512 || ih > 512 {
		return
	}
	var bi bitmapInfo
	bi.Header = bitmapInfoHeader{
		Size: uint32(unsafe.Sizeof(bi.Header)), Width: int32(iw), Height: -int32(ih),
		Planes: 1, BitCount: 32, Compression: biRGB,
	}
	var bits unsafe.Pointer
	color, _, _ := pCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), dibRGBColors,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 || bits == nil {
		return
	}
	defer pDeleteObject.Call(color)
	dst := unsafe.Slice((*byte)(bits), iw*ih*4)
	for y := 0; y < ih; y++ {
		so := icon.PixOffset(icon.Rect.Min.X, icon.Rect.Min.Y+y)
		pixconv.SwapRB(dst[y*iw*4:(y+1)*iw*4], icon.Pix[so:so+iw*4])
	}
	// Icons carry straight (non-premultiplied) alpha.
	px := make([]byte, len(dst))
	copy(px, dst)
	for i := 0; i+4 <= len(px); i += 4 {
		a := uint32(px[i+3])
		if a != 0 && a != 255 {
			for k := 0; k < 3; k++ {
				v := uint32(px[i+k]) * 255 / a
				if v > 255 {
					v = 255
				}
				px[i+k] = byte(v)
			}
		}
	}
	copy(dst, px)
	mask, _, _ := pCreateBitmap.Call(uintptr(iw), uintptr(ih), 1, 1, 0)
	if mask == 0 {
		return
	}
	defer pDeleteObject.Call(mask)
	ii := iconInfo{FIcon: 1, HbmMask: mask, HbmColor: color}
	h, _, _ := pCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	if h == 0 {
		return
	}
	for _, which := range []uintptr{0, 1} { // ICON_SMALL, ICON_BIG
		pSendMessageW.Call(w.hwnd, wmSetIcon, which, h)
	}
	// One handle serves both sizes; Windows scales it. Free the previous one.
	if w.hicon[0] != 0 {
		pDestroyIcon.Call(w.hicon[0])
	}
	w.hicon[0] = h
}
