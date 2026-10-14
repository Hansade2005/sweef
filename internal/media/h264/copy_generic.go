//go:build !(amd64 || arm64)

package h264

func copyRow16(dst, src []uint16) { copy(dst[:16], src[:16]) }
func copyRow8(dst, src []uint16)  { copy(dst[:8], src[:8]) }
func copyRow4(dst, src []uint16)  { copy(dst[:4], src[:4]) }
