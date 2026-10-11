package raster

import (
	"math"
	"strings"
)

// Filter is one CSS filter function.
type Filter struct {
	Name  string    // blur, brightness, contrast, grayscale, hue-rotate, invert, opacity, saturate, sepia, drop-shadow
	Args  []float64 // blur: [sigma px]; amounts: [fraction]; hue-rotate: [degrees]; drop-shadow: [dx dy blur]
	Color Color     // drop-shadow
}

func (f Filter) arg(i int, def float64) float64 {
	if i < len(f.Args) {
		return f.Args[i]
	}
	return def
}

func filterPad(fl []Filter) int {
	p := 0
	for _, f := range fl {
		switch f.Name {
		case "blur":
			p += int(math.Ceil(3 * math.Max(0, f.arg(0, 0))))
		case "drop-shadow":
			p += int(math.Ceil(math.Max(math.Abs(f.arg(0, 0)), math.Abs(f.arg(1, 0))))) + int(math.Ceil(3*math.Max(0, f.arg(2, 0))))
		}
	}
	return p
}

func filterPadMax(fl []Filter) int { return filterPad(fl) }

type cmat [3][3]float32

func colorMatrix(f Filter) (cmat, bool) {
	a := f.arg(0, 1)
	switch f.Name {
	case "grayscale":
		a = float64(clamp01(a))
		s := float32(1 - a)
		return cmat{
			{0.2126 + 0.7874*s, 0.7152 - 0.7152*s, 0.0722 - 0.0722*s},
			{0.2126 - 0.2126*s, 0.7152 + 0.2848*s, 0.0722 - 0.0722*s},
			{0.2126 - 0.2126*s, 0.7152 - 0.7152*s, 0.0722 + 0.9278*s}}, true
	case "sepia":
		a = float64(clamp01(a))
		s := float32(1 - a)
		return cmat{
			{0.393 + 0.607*s, 0.769 - 0.769*s, 0.189 - 0.189*s},
			{0.349 - 0.349*s, 0.686 + 0.314*s, 0.168 - 0.168*s},
			{0.272 - 0.272*s, 0.534 - 0.534*s, 0.131 + 0.869*s}}, true
	case "saturate":
		s := float32(math.Max(0, a))
		return cmat{
			{0.213 + 0.787*s, 0.715 - 0.715*s, 0.072 - 0.072*s},
			{0.213 - 0.213*s, 0.715 + 0.285*s, 0.072 - 0.072*s},
			{0.213 - 0.213*s, 0.715 - 0.715*s, 0.072 + 0.928*s}}, true
	case "hue-rotate":
		sn, cs := math.Sincos(f.arg(0, 0) * math.Pi / 180)
		c, s := float32(cs), float32(sn)
		return cmat{
			{0.213 + c*0.787 - s*0.213, 0.715 - c*0.715 - s*0.715, 0.072 - c*0.072 + s*0.928},
			{0.213 - c*0.213 + s*0.143, 0.715 + c*0.285 + s*0.140, 0.072 - c*0.072 - s*0.283},
			{0.213 - c*0.213 - s*0.787, 0.715 - c*0.715 + s*0.715, 0.072 + c*0.928 + s*0.072}}, true
	}
	return cmat{}, false
}

// ApplyFilters runs a CSS filter list over s in place (s must already be
// padded for blur/shadow extents).
func ApplyFilters(s *Surface, fl []Filter) {
	for _, f := range fl {
		switch f.Name {
		case "blur":
			sg := math.Max(0, f.arg(0, 0))
			if sg > 0 {
				blurPlane(s.Pix, s.W(), s.H(), s.Stride, 4, sg, sg)
			}
		case "drop-shadow":
			dropShadow(s, f)
		case "opacity":
			a := uint32(clamp01(f.arg(0, 1))*256 + 0.5)
			for i := range s.Pix {
				s.Pix[i] = uint8((uint32(s.Pix[i]) * a) >> 8)
			}
		case "brightness", "contrast", "invert":
			transferFilter(s, f)
		default:
			if m, ok := colorMatrix(f); ok {
				matrixFilter(s, m)
			}
		}
	}
}

func eachPixel(s *Surface, fn func(px []uint8)) {
	h := s.H()
	parallelFor(h, 64, func(lo, hi int) {
		for y := lo; y < hi; y++ {
			row := s.Pix[y*s.Stride : y*s.Stride+s.W()*4]
			for i := 0; i+3 < len(row); i += 4 {
				if row[i+3] != 0 {
					fn(row[i : i+4 : i+4])
				}
			}
		}
	})
}

func matrixFilter(s *Surface, m cmat) {
	eachPixel(s, func(p []uint8) {
		a := float32(p[3]) / 255
		r, g, b := float32(p[0])/255/a, float32(p[1])/255/a, float32(p[2])/255/a
		nr := m[0][0]*r + m[0][1]*g + m[0][2]*b
		ng := m[1][0]*r + m[1][1]*g + m[1][2]*b
		nb := m[2][0]*r + m[2][1]*g + m[2][2]*b
		p[0], p[1], p[2] = f2u8(clampf(nr)*a), f2u8(clampf(ng)*a), f2u8(clampf(nb)*a)
	})
}

func clampf(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func transferFilter(s *Surface, f Filter) {
	amt := float32(f.arg(0, 1))
	var fn func(c float32) float32
	switch f.Name {
	case "brightness":
		amt = max(0, amt)
		fn = func(c float32) float32 { return c * amt }
	case "contrast":
		amt = max(0, amt)
		fn = func(c float32) float32 { return c*amt + (0.5 - 0.5*amt) }
	default: // invert
		amt = float32(clamp01(float64(amt)))
		fn = func(c float32) float32 { return c*(1-2*amt) + amt }
	}
	var lut [256]float32
	for i := range lut {
		lut[i] = clampf(fn(float32(i) / 255))
	}
	eachPixel(s, func(p []uint8) {
		a := uint32(p[3])
		// unpremultiply to 8 bits, map, re-premultiply
		for i := 0; i < 3; i++ {
			c := unpremul(p[i], a)
			p[i] = f2u8(lut[c] * float32(a) / 255)
		}
	})
}

func dropShadow(s *Surface, f Filter) {
	dx, dy := int(math.Round(f.arg(0, 0))), int(math.Round(f.arg(1, 0)))
	// CSS filter drop-shadow's blur argument is the standard deviation itself
	// (unlike box-shadow, whose blur radius is twice sigma)
	sigma := math.Max(0, f.arg(2, 0))
	w, h := s.W(), s.H()
	sh := newMask(s.R)
	for y := 0; y < h; y++ {
		row := s.Pix[y*s.Stride : y*s.Stride+w*4]
		out := sh.A[y*sh.Stride : y*sh.Stride+w]
		for x := 0; x < w; x++ {
			out[x] = row[x*4+3]
		}
	}
	bm := BlurMask(sh, sigma, sigma, dx, dy, s.R)
	if sigma == 0 && bm == nil {
		return
	}
	shadow := NewSurfaceRect(s.R)
	if bm != nil {
		col := f.Color
		composite(shadow, bm, Solid(col), SourceOver, &clipState{R: s.R})
	}
	// original over shadow
	composite(shadow, nil, surfShader{s, 256}, SourceOver, &clipState{R: s.R})
	copy(s.Pix, shadow.Pix)
}

// ParseFilterList parses a CSS filter value ("blur(2px) brightness(1.2)").
// Returns ok=false on syntax errors; "none" yields an empty list.
func ParseFilterList(s string, cur Color) ([]Filter, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "none" {
		return nil, true
	}
	var out []Filter
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
			i++
		}
		if i >= len(s) {
			break
		}
		j := strings.IndexByte(s[i:], '(')
		if j < 0 {
			return nil, false
		}
		name := strings.ToLower(strings.TrimSpace(s[i : i+j]))
		depth, k := 0, i+j
		for ; k < len(s); k++ {
			if s[k] == '(' {
				depth++
			} else if s[k] == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if k >= len(s) {
			return nil, false
		}
		body := strings.TrimSpace(s[i+j+1 : k])
		i = k + 1
		f := Filter{Name: name}
		switch name {
		case "blur":
			v, ok := parseLen(body)
			if body == "" {
				v, ok = 0, true
			}
			if !ok || v < 0 {
				return nil, false
			}
			f.Args = []float64{v}
		case "brightness", "contrast", "grayscale", "invert", "opacity", "saturate", "sepia":
			v := 1.0
			if body != "" {
				var ok bool
				v, ok = parseAmount(body)
				if !ok || v < 0 {
					return nil, false
				}
			}
			f.Args = []float64{v}
		case "hue-rotate":
			v := 0.0
			if body != "" {
				var ok bool
				v, ok = parseHue2(body)
				if !ok {
					return nil, false
				}
			}
			f.Args = []float64{v}
		case "drop-shadow":
			parts := splitTop(body, " \t")
			var nums []float64
			col := cur
			haveCol := false
			for _, p := range parts {
				if v, ok := parseLen(p); ok {
					nums = append(nums, v)
				} else if c, ok := ParseColor(p, cur); ok && !haveCol {
					col, haveCol = c, true
				} else {
					return nil, false
				}
			}
			if len(nums) < 2 || len(nums) > 3 {
				return nil, false
			}
			for len(nums) < 3 {
				nums = append(nums, 0)
			}
			f.Args = nums
			f.Color = col
		default:
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

func parseLen(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasSuffix(s, "px"):
		s = s[:len(s)-2]
	case strings.HasSuffix(s, "em"):
		v, err := parseFloat(s[:len(s)-2])
		return v * 16, err == nil
	case strings.HasSuffix(s, "rem"):
		v, err := parseFloat(s[:len(s)-3])
		return v * 16, err == nil
	}
	v, err := parseFloat(s)
	return v, err == nil
}

func parseAmount(s string) (float64, bool) {
	if strings.HasSuffix(s, "%") {
		v, err := parseFloat(s[:len(s)-1])
		return v / 100, err == nil
	}
	v, err := parseFloat(s)
	return v, err == nil
}

func parseHue2(s string) (float64, bool) {
	if v, ok := parseHue2raw(s); ok {
		return v, true
	}
	return 0, false
}

func parseHue2raw(s string) (float64, bool) {
	mul := 1.0
	switch {
	case strings.HasSuffix(s, "deg"):
		s = s[:len(s)-3]
	case strings.HasSuffix(s, "grad"):
		s, mul = s[:len(s)-4], 0.9
	case strings.HasSuffix(s, "rad"):
		s, mul = s[:len(s)-3], 180/math.Pi
	case strings.HasSuffix(s, "turn"):
		s, mul = s[:len(s)-4], 360
	}
	v, err := parseFloat(s)
	return v * mul, err == nil
}
