// Package csscolor parses CSS Color 4/5 values: named colours, hex, rgb(),
// hsl(), hwb(), lab(), lch(), oklab(), oklch(), color() with the common
// predefined spaces, color-mix(), transparent and system colours. It is
// shared by the style cascade, canvas 2D fill/stroke styles and SVG paint.
package csscolor

import (
	"math"
	"strconv"
	"strings"

	"github.com/sweefbrowser/sweef/internal/render/gfx"
)

// Result of a parse: the colour, or CurrentColor when the value is the
// currentcolor keyword (the caller substitutes its own colour).
type Result struct {
	Color        gfx.Color
	CurrentColor bool
}

// Parse parses a CSS colour value. ok is false for anything that is not a
// valid colour.
func Parse(s string) (Result, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return Result{}, false
	}
	if s == "currentcolor" {
		return Result{CurrentColor: true}, true
	}
	if s[0] == '#' {
		c, ok := parseHex(s[1:])
		return Result{Color: c}, ok
	}
	if i := strings.IndexByte(s, '('); i > 0 && strings.HasSuffix(s, ")") {
		fn, args := s[:i], s[i+1:len(s)-1]
		c, cur, ok := parseFunc(fn, args)
		return Result{Color: c, CurrentColor: cur}, ok
	}
	if c, ok := named[s]; ok {
		return Result{Color: c}, true
	}
	if c, ok := system[s]; ok {
		return Result{Color: c}, true
	}
	return Result{}, false
}

// MustColor parses s, substituting current for currentcolor and black
// for invalid input.
func MustColor(s string, current gfx.Color) gfx.Color {
	r, ok := Parse(s)
	if !ok {
		return gfx.Black
	}
	if r.CurrentColor {
		return current
	}
	return r.Color
}

func parseHex(h string) (gfx.Color, bool) {
	for _, c := range h {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return gfx.Color{}, false
		}
	}
	nib := func(i int) float64 { v, _ := strconv.ParseUint(h[i:i+1], 16, 8); return float64(v*17) / 255 }
	byt := func(i int) float64 { v, _ := strconv.ParseUint(h[i:i+2], 16, 8); return float64(v) / 255 }
	switch len(h) {
	case 3:
		return gfx.Color{R: nib(0), G: nib(1), B: nib(2), A: 1}, true
	case 4:
		return gfx.Color{R: nib(0), G: nib(1), B: nib(2), A: nib(3)}, true
	case 6:
		return gfx.Color{R: byt(0), G: byt(2), B: byt(4), A: 1}, true
	case 8:
		return gfx.Color{R: byt(0), G: byt(2), B: byt(4), A: byt(6)}, true
	}
	return gfx.Color{}, false
}

// splitArgs splits function arguments on commas or whitespace, returning
// the components and the alpha (after "/" or as a 4th comma argument).
func splitArgs(args string) (comps []string, alpha string, ok bool) {
	args = strings.TrimSpace(args)
	if strings.Contains(args, ",") {
		parts := strings.Split(args, ",")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		if len(parts) == 4 {
			return parts[:3], parts[3], true
		}
		if len(parts) == 3 {
			return parts, "", true
		}
		return nil, "", false
	}
	main := args
	if i := strings.IndexByte(args, '/'); i >= 0 {
		main, alpha = strings.TrimSpace(args[:i]), strings.TrimSpace(args[i+1:])
	}
	comps = strings.Fields(main)
	return comps, alpha, true
}

// num parses a number or percentage; pctScale maps 100% to that value.
func num(s string, pctScale float64) (float64, bool) {
	if s == "none" {
		return 0, true
	}
	if strings.HasSuffix(s, "%") {
		v, err := strconv.ParseFloat(s[:len(s)-1], 64)
		return v / 100 * pctScale, err == nil
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// angle parses a hue in degrees (deg, rad, grad, turn or bare number).
func angle(s string) (float64, bool) {
	if s == "none" {
		return 0, true
	}
	for _, u := range []struct {
		suf string
		k   float64
	}{{"deg", 1}, {"grad", 0.9}, {"rad", 180 / math.Pi}, {"turn", 360}} {
		if strings.HasSuffix(s, u.suf) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suf), 64)
			return v * u.k, err == nil
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func alphaOf(s string) (float64, bool) {
	if s == "" {
		return 1, true
	}
	v, ok := num(s, 1)
	return clamp(v, 0, 1), ok
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func parseFunc(fn, args string) (gfx.Color, bool, bool) {
	switch fn {
	case "rgb", "rgba":
		c, a, ok := splitArgs(args)
		if !ok || len(c) != 3 {
			return gfx.Color{}, false, false
		}
		var v [3]float64
		for i := range c {
			x, ok := num(c[i], 255)
			if !ok {
				return gfx.Color{}, false, false
			}
			v[i] = clamp(math.Round(x), 0, 255) / 255
		}
		al, ok := alphaOf(a)
		return gfx.Color{R: v[0], G: v[1], B: v[2], A: al}, false, ok
	case "hsl", "hsla":
		c, a, ok := splitArgs(args)
		if !ok || len(c) != 3 {
			return gfx.Color{}, false, false
		}
		h, ok1 := angle(c[0])
		s, ok2 := num(strings.TrimSuffix(c[1], "%"), 1)
		l, ok3 := num(strings.TrimSuffix(c[2], "%"), 1)
		al, ok4 := alphaOf(a)
		if !(ok1 && ok2 && ok3 && ok4) {
			return gfx.Color{}, false, false
		}
		r, g, b := hslToRGB(h, clamp(s/100, 0, 1), clamp(l/100, 0, 1))
		return quantize(gfx.Color{R: r, G: g, B: b, A: al}), false, true
	case "hwb":
		c, a, ok := splitArgs(args)
		if !ok || len(c) != 3 {
			return gfx.Color{}, false, false
		}
		h, ok1 := angle(c[0])
		w, ok2 := num(strings.TrimSuffix(c[1], "%"), 1)
		bk, ok3 := num(strings.TrimSuffix(c[2], "%"), 1)
		al, ok4 := alphaOf(a)
		if !(ok1 && ok2 && ok3 && ok4) {
			return gfx.Color{}, false, false
		}
		w, bk = w/100, bk/100
		if w+bk >= 1 {
			g := w / (w + bk)
			return quantize(gfx.Color{R: g, G: g, B: g, A: al}), false, true
		}
		r, g, b := hslToRGB(h, 1, 0.5)
		f := func(x float64) float64 { return x*(1-w-bk) + w }
		return quantize(gfx.Color{R: f(r), G: f(g), B: f(b), A: al}), false, true
	case "lab", "lch", "oklab", "oklch":
		c, a, ok := splitArgs(args)
		if !ok || len(c) != 3 {
			return gfx.Color{}, false, false
		}
		al, ok := alphaOf(a)
		if !ok {
			return gfx.Color{}, false, false
		}
		var L, A, B float64
		switch fn {
		case "lab":
			L, _ = num(c[0], 100)
			A, _ = num(c[1], 125)
			B, _ = num(c[2], 125)
		case "lch":
			L, _ = num(c[0], 100)
			C, _ := num(c[1], 150)
			H, _ := angle(c[2])
			A, B = C*math.Cos(H*math.Pi/180), C*math.Sin(H*math.Pi/180)
		case "oklab":
			L, _ = num(c[0], 1)
			A, _ = num(c[1], 0.4)
			B, _ = num(c[2], 0.4)
		case "oklch":
			L, _ = num(c[0], 1)
			C, _ := num(c[1], 0.4)
			H, _ := angle(c[2])
			A, B = C*math.Cos(H*math.Pi/180), C*math.Sin(H*math.Pi/180)
		}
		var r, g, b float64
		if fn == "lab" || fn == "lch" {
			r, g, b = labToSRGB(L, A, B)
		} else {
			r, g, b = oklabToSRGB(L, A, B)
		}
		return gfx.Color{R: clamp(r, 0, 1), G: clamp(g, 0, 1), B: clamp(b, 0, 1), A: al}, false, true
	case "color":
		c, a, ok := splitArgs(args)
		if !ok || len(c) != 4 {
			return gfx.Color{}, false, false
		}
		al, ok := alphaOf(a)
		if !ok {
			return gfx.Color{}, false, false
		}
		var v [3]float64
		for i := 0; i < 3; i++ {
			v[i], _ = num(c[i+1], 1)
		}
		switch c[0] {
		case "srgb":
			return gfx.Color{R: clamp(v[0], 0, 1), G: clamp(v[1], 0, 1), B: clamp(v[2], 0, 1), A: al}, false, true
		case "srgb-linear":
			return gfx.Color{R: clamp(gammaEnc(v[0]), 0, 1), G: clamp(gammaEnc(v[1]), 0, 1), B: clamp(gammaEnc(v[2]), 0, 1), A: al}, false, true
		case "display-p3":
			r, g, b := p3ToSRGB(v[0], v[1], v[2])
			return gfx.Color{R: clamp(r, 0, 1), G: clamp(g, 0, 1), B: clamp(b, 0, 1), A: al}, false, true
		case "xyz", "xyz-d65":
			r, g, b := xyzD65ToSRGB(v[0], v[1], v[2])
			return gfx.Color{R: clamp(r, 0, 1), G: clamp(g, 0, 1), B: clamp(b, 0, 1), A: al}, false, true
		}
		return gfx.Color{}, false, false
	case "color-mix":
		return colorMix(args)
	}
	return gfx.Color{}, false, false
}

// quantize rounds to 8-bit channels the way Chromium stores legacy colours.
func quantize(c gfx.Color) gfx.Color {
	q := func(v float64) float64 { return math.Round(clamp(v, 0, 1)*255) / 255 }
	return gfx.Color{R: q(c.R), G: q(c.G), B: q(c.B), A: c.A}
}

func hslToRGB(h, s, l float64) (float64, float64, float64) {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	f := func(n float64) float64 {
		k := math.Mod(n+h/30, 12)
		a := s * math.Min(l, 1-l)
		return l - a*math.Max(-1, math.Min(k-3, math.Min(9-k, 1)))
	}
	return f(0), f(8), f(4)
}

func gammaEnc(v float64) float64 {
	sign := 1.0
	if v < 0 {
		sign, v = -1, -v
	}
	if v <= 0.0031308 {
		return sign * 12.92 * v
	}
	return sign * (1.055*math.Pow(v, 1/2.4) - 0.055)
}

func gammaDec(v float64) float64 {
	sign := 1.0
	if v < 0 {
		sign, v = -1, -v
	}
	if v <= 0.04045 {
		return sign * v / 12.92
	}
	return sign * math.Pow((v+0.055)/1.055, 2.4)
}

func xyzD65ToSRGB(x, y, z float64) (float64, float64, float64) {
	r := 3.2409699419045226*x - 1.537383177570094*y - 0.4986107602930034*z
	g := -0.9692436362808796*x + 1.8759675015077202*y + 0.04155505740717559*z
	b := 0.05563007969699366*x - 0.20397695888897652*y + 1.0569715142428786*z
	return gammaEnc(r), gammaEnc(g), gammaEnc(b)
}

func labToSRGB(L, a, b float64) (float64, float64, float64) {
	const k, e = 24389.0 / 27, 216.0 / 24389
	fy := (L + 16) / 116
	fx := a/500 + fy
	fz := fy - b/200
	cube := func(f float64) float64 {
		if f*f*f > e {
			return f * f * f
		}
		return (116*f - 16) / k
	}
	xr, zr := cube(fx), cube(fz)
	yr := (L + 16) / 116
	yr = yr * yr * yr
	if L <= k*e {
		yr = L / k
	}
	// D50 white, then Bradford to D65
	x, y, z := xr*0.3457/0.3585, yr, zr*(1-0.3457-0.3585)/0.3585
	X := 0.9554734527042182*x - 0.023098536874261423*y + 0.0632593086610217*z
	Y := -0.028369706963208136*x + 1.0099954580058226*y + 0.021041398966943008*z
	Z := 0.012314001688319899*x - 0.020507696433477912*y + 1.3303659366080753*z
	return xyzD65ToSRGB(X, Y, Z)
}

func oklabToSRGB(L, a, b float64) (float64, float64, float64) {
	l := L + 0.3963377774*a + 0.2158037573*b
	m := L - 0.1055613458*a - 0.0638541728*b
	s := L - 0.0894841775*a - 1.2914855480*b
	l, m, s = l*l*l, m*m*m, s*s*s
	r := 4.0767416621*l - 3.3077115913*m + 0.2309699292*s
	g := -1.2684380046*l + 2.6097574011*m - 0.3413193965*s
	bb := -0.0041960863*l - 0.7034186147*m + 1.7076147010*s
	return gammaEnc(r), gammaEnc(g), gammaEnc(bb)
}

func p3ToSRGB(r, g, b float64) (float64, float64, float64) {
	r, g, b = gammaDec(r), gammaDec(g), gammaDec(b)
	x := 0.4865709486482162*r + 0.26566769316909306*g + 0.1982172852343625*b
	y := 0.2289745640697488*r + 0.6917385218365064*g + 0.079286914093745*b
	z := 0.0*r + 0.04511338185890264*g + 1.043944368900976*b
	return xyzD65ToSRGB(x, y, z)
}

// colorMix implements color-mix(in <space>, c1 [p1], c2 [p2]) for srgb,
// srgb-linear and oklab interpolation (other spaces fall back to oklab).
func colorMix(args string) (gfx.Color, bool, bool) {
	parts := splitTop(args)
	if len(parts) != 3 || !strings.HasPrefix(strings.TrimSpace(parts[0]), "in ") {
		return gfx.Color{}, false, false
	}
	space := strings.Fields(parts[0])[1]
	parse := func(s string) (gfx.Color, float64, bool, bool) {
		s = strings.TrimSpace(s)
		pct := -1.0
		if j := strings.LastIndexByte(s, ' '); j > 0 && strings.HasSuffix(s, "%") {
			v, err := strconv.ParseFloat(s[j+1:len(s)-1], 64)
			if err == nil {
				pct = v / 100
				s = strings.TrimSpace(s[:j])
			}
		}
		r, ok := Parse(s)
		return r.Color, pct, r.CurrentColor, ok
	}
	c1, p1, cur1, ok1 := parse(parts[1])
	c2, p2, cur2, ok2 := parse(parts[2])
	if !ok1 || !ok2 || cur1 || cur2 {
		return gfx.Color{}, cur1 || cur2, ok1 && ok2 && false
	}
	switch {
	case p1 < 0 && p2 < 0:
		p1, p2 = 0.5, 0.5
	case p1 < 0:
		p1 = 1 - p2
	case p2 < 0:
		p2 = 1 - p1
	}
	sum := p1 + p2
	if sum <= 0 {
		return gfx.Color{}, false, false
	}
	am := 1.0
	if sum < 1 {
		am = sum
	}
	t := p2 / sum
	a := c1.A*(1-t) + c2.A*t
	mixPre := func(x, y float64) float64 {
		if a == 0 {
			return 0
		}
		return (x*c1.A*(1-t) + y*c2.A*t) / a
	}
	var out gfx.Color
	switch space {
	case "srgb":
		out = gfx.Color{R: mixPre(c1.R, c2.R), G: mixPre(c1.G, c2.G), B: mixPre(c1.B, c2.B)}
	case "srgb-linear":
		out = gfx.Color{R: gammaEnc(mixPre(gammaDec(c1.R), gammaDec(c2.R))), G: gammaEnc(mixPre(gammaDec(c1.G), gammaDec(c2.G))), B: gammaEnc(mixPre(gammaDec(c1.B), gammaDec(c2.B)))}
	default:
		l1, a1, b1 := srgbToOklab(c1)
		l2, a2, b2 := srgbToOklab(c2)
		r, g, b := oklabToSRGB(mixPre(l1, l2), mixPre(a1, a2), mixPre(b1, b2))
		out = gfx.Color{R: clamp(r, 0, 1), G: clamp(g, 0, 1), B: clamp(b, 0, 1)}
	}
	out.A = a * am
	return out, false, true
}

func srgbToOklab(c gfx.Color) (float64, float64, float64) {
	r, g, b := gammaDec(c.R), gammaDec(c.G), gammaDec(c.B)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return 0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s
}

// splitTop splits on commas not inside parentheses.
func splitTop(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

func rgb(r, g, b uint8) gfx.Color { return gfx.RGBA8(r, g, b, 1) }

// system colours as Chromium resolves them in the light scheme.
var system = map[string]gfx.Color{
	"canvas": rgb(255, 255, 255), "canvastext": rgb(0, 0, 0), "linktext": rgb(0, 0, 238),
	"visitedtext": rgb(85, 26, 139), "activetext": rgb(255, 0, 0), "buttonface": rgb(239, 239, 239),
	"buttontext": rgb(0, 0, 0), "buttonborder": rgb(118, 118, 118), "field": rgb(255, 255, 255),
	"fieldtext": rgb(0, 0, 0), "highlight": rgb(181, 213, 255), "highlighttext": rgb(0, 0, 0),
	"selecteditem": rgb(0, 117, 255), "selecteditemtext": rgb(255, 255, 255), "mark": rgb(255, 255, 0),
	"marktext": rgb(0, 0, 0), "graytext": rgb(109, 109, 109), "accentcolor": rgb(0, 117, 255),
	"accentcolortext": rgb(255, 255, 255),
	// deprecated system colours
	"activeborder": rgb(118, 118, 118), "activecaption": rgb(255, 255, 255), "appworkspace": rgb(255, 255, 255),
	"background": rgb(99, 99, 206), "buttonhighlight": rgb(221, 221, 221), "buttonshadow": rgb(136, 136, 136),
	"captiontext": rgb(0, 0, 0), "inactiveborder": rgb(255, 255, 255), "inactivecaption": rgb(255, 255, 255),
	"inactivecaptiontext": rgb(127, 127, 127), "infobackground": rgb(251, 252, 197), "infotext": rgb(0, 0, 0),
	"menu": rgb(247, 247, 247), "menutext": rgb(0, 0, 0), "scrollbar": rgb(255, 255, 255),
	"threeddarkshadow": rgb(102, 102, 102), "threedface": rgb(192, 192, 192), "threedhighlight": rgb(221, 221, 221),
	"threedlightshadow": rgb(192, 192, 192), "threedshadow": rgb(136, 136, 136), "window": rgb(255, 255, 255),
	"windowframe": rgb(204, 204, 204), "windowtext": rgb(0, 0, 0),
}

var named = map[string]gfx.Color{
	"transparent": {},
	"aliceblue":   rgb(240, 248, 255), "antiquewhite": rgb(250, 235, 215), "aqua": rgb(0, 255, 255),
	"aquamarine": rgb(127, 255, 212), "azure": rgb(240, 255, 255), "beige": rgb(245, 245, 220),
	"bisque": rgb(255, 228, 196), "black": rgb(0, 0, 0), "blanchedalmond": rgb(255, 235, 205),
	"blue": rgb(0, 0, 255), "blueviolet": rgb(138, 43, 226), "brown": rgb(165, 42, 42),
	"burlywood": rgb(222, 184, 135), "cadetblue": rgb(95, 158, 160), "chartreuse": rgb(127, 255, 0),
	"chocolate": rgb(210, 105, 30), "coral": rgb(255, 127, 80), "cornflowerblue": rgb(100, 149, 237),
	"cornsilk": rgb(255, 248, 220), "crimson": rgb(220, 20, 60), "cyan": rgb(0, 255, 255),
	"darkblue": rgb(0, 0, 139), "darkcyan": rgb(0, 139, 139), "darkgoldenrod": rgb(184, 134, 11),
	"darkgray": rgb(169, 169, 169), "darkgreen": rgb(0, 100, 0), "darkgrey": rgb(169, 169, 169),
	"darkkhaki": rgb(189, 183, 107), "darkmagenta": rgb(139, 0, 139), "darkolivegreen": rgb(85, 107, 47),
	"darkorange": rgb(255, 140, 0), "darkorchid": rgb(153, 50, 204), "darkred": rgb(139, 0, 0),
	"darksalmon": rgb(233, 150, 122), "darkseagreen": rgb(143, 188, 143), "darkslateblue": rgb(72, 61, 139),
	"darkslategray": rgb(47, 79, 79), "darkslategrey": rgb(47, 79, 79), "darkturquoise": rgb(0, 206, 209),
	"darkviolet": rgb(148, 0, 211), "deeppink": rgb(255, 20, 147), "deepskyblue": rgb(0, 191, 255),
	"dimgray": rgb(105, 105, 105), "dimgrey": rgb(105, 105, 105), "dodgerblue": rgb(30, 144, 255),
	"firebrick": rgb(178, 34, 34), "floralwhite": rgb(255, 250, 240), "forestgreen": rgb(34, 139, 34),
	"fuchsia": rgb(255, 0, 255), "gainsboro": rgb(220, 220, 220), "ghostwhite": rgb(248, 248, 255),
	"gold": rgb(255, 215, 0), "goldenrod": rgb(218, 165, 32), "gray": rgb(128, 128, 128),
	"green": rgb(0, 128, 0), "greenyellow": rgb(173, 255, 47), "grey": rgb(128, 128, 128),
	"honeydew": rgb(240, 255, 240), "hotpink": rgb(255, 105, 180), "indianred": rgb(205, 92, 92),
	"indigo": rgb(75, 0, 130), "ivory": rgb(255, 255, 240), "khaki": rgb(240, 230, 140),
	"lavender": rgb(230, 230, 250), "lavenderblush": rgb(255, 240, 245), "lawngreen": rgb(124, 252, 0),
	"lemonchiffon": rgb(255, 250, 205), "lightblue": rgb(173, 216, 230), "lightcoral": rgb(240, 128, 128),
	"lightcyan": rgb(224, 255, 255), "lightgoldenrodyellow": rgb(250, 250, 210), "lightgray": rgb(211, 211, 211),
	"lightgreen": rgb(144, 238, 144), "lightgrey": rgb(211, 211, 211), "lightpink": rgb(255, 182, 193),
	"lightsalmon": rgb(255, 160, 122), "lightseagreen": rgb(32, 178, 170), "lightskyblue": rgb(135, 206, 250),
	"lightslategray": rgb(119, 136, 153), "lightslategrey": rgb(119, 136, 153), "lightsteelblue": rgb(176, 196, 222),
	"lightyellow": rgb(255, 255, 224), "lime": rgb(0, 255, 0), "limegreen": rgb(50, 205, 50),
	"linen": rgb(250, 240, 230), "magenta": rgb(255, 0, 255), "maroon": rgb(128, 0, 0),
	"mediumaquamarine": rgb(102, 205, 170), "mediumblue": rgb(0, 0, 205), "mediumorchid": rgb(186, 85, 211),
	"mediumpurple": rgb(147, 112, 219), "mediumseagreen": rgb(60, 179, 113), "mediumslateblue": rgb(123, 104, 238),
	"mediumspringgreen": rgb(0, 250, 154), "mediumturquoise": rgb(72, 209, 204), "mediumvioletred": rgb(199, 21, 133),
	"midnightblue": rgb(25, 25, 112), "mintcream": rgb(245, 255, 250), "mistyrose": rgb(255, 228, 225),
	"moccasin": rgb(255, 228, 181), "navajowhite": rgb(255, 222, 173), "navy": rgb(0, 0, 128),
	"oldlace": rgb(253, 245, 230), "olive": rgb(128, 128, 0), "olivedrab": rgb(107, 142, 35),
	"orange": rgb(255, 165, 0), "orangered": rgb(255, 69, 0), "orchid": rgb(218, 112, 214),
	"palegoldenrod": rgb(238, 232, 170), "palegreen": rgb(152, 251, 152), "paleturquoise": rgb(175, 238, 238),
	"palevioletred": rgb(219, 112, 147), "papayawhip": rgb(255, 239, 213), "peachpuff": rgb(255, 218, 185),
	"peru": rgb(205, 133, 63), "pink": rgb(255, 192, 203), "plum": rgb(221, 160, 221),
	"powderblue": rgb(176, 224, 230), "purple": rgb(128, 0, 128), "rebeccapurple": rgb(102, 51, 153),
	"red": rgb(255, 0, 0), "rosybrown": rgb(188, 143, 143), "royalblue": rgb(65, 105, 225),
	"saddlebrown": rgb(139, 69, 19), "salmon": rgb(250, 128, 114), "sandybrown": rgb(244, 164, 96),
	"seagreen": rgb(46, 139, 87), "seashell": rgb(255, 245, 238), "sienna": rgb(160, 82, 45),
	"silver": rgb(192, 192, 192), "skyblue": rgb(135, 206, 235), "slateblue": rgb(106, 90, 205),
	"slategray": rgb(112, 128, 144), "slategrey": rgb(112, 128, 144), "snow": rgb(255, 250, 250),
	"springgreen": rgb(0, 255, 127), "steelblue": rgb(70, 130, 180), "tan": rgb(210, 180, 140),
	"teal": rgb(0, 128, 128), "thistle": rgb(216, 191, 216), "tomato": rgb(255, 99, 71),
	"turquoise": rgb(64, 224, 208), "violet": rgb(238, 130, 238), "wheat": rgb(245, 222, 179),
	"white": rgb(255, 255, 255), "whitesmoke": rgb(245, 245, 245), "yellow": rgb(255, 255, 0),
	"yellowgreen": rgb(154, 205, 50),
}
