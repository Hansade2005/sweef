// Package pixconv converts the premultiplied RGBA frames the compositor
// produces into the pixel layouts native window systems want. It is shared
// by the X11 and Win32 shells and tested on every platform.
package pixconv

import (
	"encoding/binary"
	"unsafe"
)

// hostLittleEndian is true on every Linux target we build for except the
// big-endian ones, where the word-wise path below must not be used.
var hostLittleEndian = func() bool {
	x := uint16(1)
	return *(*byte)(unsafe.Pointer(&x)) == 1
}()

// SwapRB converts premultiplied RGBA (bytes R,G,B,A) to the X server's
// little-endian 32-bpp TrueColor layout (bytes B,G,R,A; A is the ignored
// pad byte of a depth-24 visual). dst and src must have equal length, a
// multiple of 4. Two pixels are processed per 64-bit word when both slices
// are 8-byte aligned (the common case: row starts of Go images and of the
// page-aligned SHM segment); otherwise 32-bit/byte-wise paths are used.
func SwapRB(dst, src []byte) {
	n := len(src) &^ 3
	if n == 0 {
		return
	}
	dst = dst[:n]
	if hostLittleEndian &&
		uintptr(unsafe.Pointer(&src[0]))&7 == 0 && uintptr(unsafe.Pointer(&dst[0]))&7 == 0 {
		words := n / 8
		if words > 0 {
			s := unsafe.Slice((*uint64)(unsafe.Pointer(&src[0])), words)
			d := unsafe.Slice((*uint64)(unsafe.Pointer(&dst[0])), words)
			d = d[:len(s)]
			for i, w := range s {
				d[i] = w&0xFF00FF00FF00FF00 | (w<<16)&0x00FF000000FF0000 | (w>>16)&0x000000FF000000FF
			}
		}
		if n&4 != 0 {
			w := binary.LittleEndian.Uint32(src[n-4:])
			binary.LittleEndian.PutUint32(dst[n-4:], w&0xFF00FF00|(w<<16)&0x00FF0000|(w>>16)&0x000000FF)
		}
		return
	}
	// Unaligned / big-endian fallback: explicit byte loads, no unsafe.
	src = src[:n]
	for i := 0; i+4 <= n; i += 4 {
		s := src[i : i+4 : i+4]
		d := dst[i : i+4 : i+4]
		d[0], d[1], d[2], d[3] = s[2], s[1], s[0], s[3]
	}
}

// ConvertRect copies the pixel rectangle [x0,x1)x[y0,y1) from a premultiplied
// RGBA image (pix, stride, origin ox,oy) into the shadow buffer (same window
// coordinates, dstStride bytes per row), swapping to BGRX unless rgbx is set.
func ConvertRect(dst []byte, dstStride int, pix []byte, srcStride, ox, oy, x0, y0, x1, y1 int, rgbx bool) {
	n := (x1 - x0) * 4
	for y := y0; y < y1; y++ {
		so := (y-oy)*srcStride + (x0-ox)*4
		do := y*dstStride + x0*4
		if rgbx {
			copy(dst[do:do+n], pix[so:so+n])
		} else {
			SwapRB(dst[do:do+n], pix[so:so+n])
		}
	}
}

// Unpremultiply converts premultiplied RGBA to straight (non-premultiplied)
// RGBA in place, as icon APIs expect.
func Unpremultiply(pix []byte) {
	for i := 0; i+4 <= len(pix); i += 4 {
		a := uint32(pix[i+3])
		if a == 0 || a == 255 {
			continue
		}
		for k := 0; k < 3; k++ {
			v := uint32(pix[i+k]) * 255 / a
			if v > 255 {
				v = 255
			}
			pix[i+k] = byte(v)
		}
	}
}
