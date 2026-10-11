package raster

import (
	"math"
	"sync"
)

// Skia's preblend ("mask gamma") tables, as configured by Chromium on Linux
// (contrast 0.2, gamma 1.2). They make dark text slightly lighter and light
// text on dark backgrounds heavier; the table depends on the text color's
// luminance, quantized to its top three bits.

const (
	textContrast = 0.2
	textGamma    = 1.2
)

var (
	lutMu    sync.Mutex
	lutCache [8]*[256]uint8
)

// textLum computes Skia's luminance byte for a color (alpha ignored).
func textLum(c Color) uint8 {
	lin := func(v float64) float64 { return math.Pow(clamp01(v), textGamma) }
	l := lin(c.R)*0.2126 + lin(c.G)*0.7152 + lin(c.B)*0.0722
	return uint8(math.Round(math.Pow(l, 1/textGamma) * 255))
}

// lutForLum returns the coverage table for a luminance byte.
func lutForLum(lum uint8) *[256]uint8 {
	bits := lum >> 5
	lutMu.Lock()
	defer lutMu.Unlock()
	if t := lutCache[bits]; t != nil {
		return t
	}
	canon := (bits << 5) | (bits << 2) | (bits >> 1)
	t := new([256]uint8)
	src := float64(canon) / 255
	linSrc := math.Pow(src, textGamma)
	dst := 1 - src
	linDst := math.Pow(dst, textGamma)
	adj := textContrast * linDst
	if math.Abs(src-dst) < 1.0/256 {
		for i := range t {
			t[i] = uint8(i)
		}
	} else {
		for i := range t {
			raw := float64(i) / 255
			a := raw + (1-raw)*adj*raw
			lo := linSrc*a + (1-a)*linDst
			out := math.Pow(lo, 1/textGamma)
			r := (out - dst) / (src - dst)
			t[i] = uint8(math.Round(clamp01(r) * 255))
		}
	}
	lutCache[bits] = t
	return t
}
