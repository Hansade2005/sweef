//go:build amd64 || arm64

package h264

import "unsafe"

// Row copies for the motion compensation: fixed widths as a few 64-bit loads and stores (the compiler turns
// an array assignment of 32 bytes into a call to memmove). Unaligned 64-bit accesses are fine on these
// architectures; every other one uses copy (copy_generic.go).

func copyRow16(dst, src []uint16) {
	_, _ = dst[15], src[15]
	d := (*[4]uint64)(unsafe.Pointer(&dst[0]))
	s := (*[4]uint64)(unsafe.Pointer(&src[0]))
	a, b, c, e := s[0], s[1], s[2], s[3]
	d[0], d[1], d[2], d[3] = a, b, c, e
}

func copyRow8(dst, src []uint16) {
	_, _ = dst[7], src[7]
	d := (*[2]uint64)(unsafe.Pointer(&dst[0]))
	s := (*[2]uint64)(unsafe.Pointer(&src[0]))
	a, b := s[0], s[1]
	d[0], d[1] = a, b
}

func copyRow4(dst, src []uint16) {
	_, _ = dst[3], src[3]
	*(*uint64)(unsafe.Pointer(&dst[0])) = *(*uint64)(unsafe.Pointer(&src[0]))
}
