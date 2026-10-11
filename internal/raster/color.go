package raster

import (
	"math"
	"strconv"
	"strings"
)

// Color is a non-premultiplied sRGB color with components in [0,1].
type Color struct{ R, G, B, A float64 }

// RGBA8 builds a color from 8-bit components.
func RGBA8(r, g, b, a uint8) Color {
	return Color{float64(r) / 255, float64(g) / 255, float64(b) / 255, float64(a) / 255}
}

// Black is opaque black; Transparent is transparent black.
var (
	Black       = Color{0, 0, 0, 1}
	Transparent = Color{}
)

// Premul8 returns premultiplied 8-bit components.
func (c Color) Premul8() (r, g, b, a uint8) {
	a8 := clamp01(c.A)
	return clampU8(clamp01(c.R) * a8 * 255), clampU8(clamp01(c.G) * a8 * 255), clampU8(clamp01(c.B) * a8 * 255), clampU8(a8 * 255)
}

// WithAlpha multiplies alpha.
func (c Color) WithAlpha(a float64) Color { c.A *= a; return c }

// CSS formats the color the way canvas fillStyle getters do.
func (c Color) CSS() string {
	r := int(clampU8(clamp01(c.R) * 255))
	g := int(clampU8(clamp01(c.G) * 255))
	b := int(clampU8(clamp01(c.B) * 255))
	a8 := int(clampU8(clamp01(c.A) * 255))
	if a8 == 255 {
		const hx = "0123456789abcdef"
		return "#" + string([]byte{hx[r>>4], hx[r&15], hx[g>>4], hx[g&15], hx[b>>4], hx[b&15]})
	}
	// Chromium prints alpha with up to 4 significant digits, rounded so that
	// it round-trips through 8 bits.
	a := float64(a8) / 255
	as := strconv.FormatFloat(math.Round(a*100)/100, 'f', -1, 64)
	if math.Abs(math.Round(a*100)/100*255-float64(a8)) > 0.5 {
		as = strconv.FormatFloat(math.Round(a*1000)/1000, 'f', -1, 64)
	}
	return "rgba(" + strconv.Itoa(r) + ", " + strconv.Itoa(g) + ", " + strconv.Itoa(b) + ", " + as + ")"
}

const namedColors = "aliceblue f0f8ff antiquewhite faebd7 aqua 00ffff aquamarine 7fffd4 azure f0ffff beige f5f5dc bisque ffe4c4 black 000000 blanchedalmond ffebcd blue 0000ff blueviolet 8a2be2 brown a52a2a burlywood deb887 cadetblue 5f9ea0 chartreuse 7fff00 chocolate d2691e coral ff7f50 cornflowerblue 6495ed cornsilk fff8dc crimson dc143c cyan 00ffff darkblue 00008b darkcyan 008b8b darkgoldenrod b8860b darkgray a9a9a9 darkgreen 006400 darkgrey a9a9a9 darkkhaki bdb76b darkmagenta 8b008b darkolivegreen 556b2f darkorange ff8c00 darkorchid 9932cc darkred 8b0000 darksalmon e9967a darkseagreen 8fbc8f darkslateblue 483d8b darkslategray 2f4f4f darkslategrey 2f4f4f darkturquoise 00ced1 darkviolet 9400d3 deeppink ff1493 deepskyblue 00bfff dimgray 696969 dimgrey 696969 dodgerblue 1e90ff firebrick b22222 floralwhite fffaf0 forestgreen 228b22 fuchsia ff00ff gainsboro dcdcdc ghostwhite f8f8ff gold ffd700 goldenrod daa520 gray 808080 green 008000 greenyellow adff2f grey 808080 honeydew f0fff0 hotpink ff69b4 indianred cd5c5c indigo 4b0082 ivory fffff0 khaki f0e68c lavender e6e6fa lavenderblush fff0f5 lawngreen 7cfc00 lemonchiffon fffacd lightblue add8e6 lightcoral f08080 lightcyan e0ffff lightgoldenrodyellow fafad2 lightgray d3d3d3 lightgreen 90ee90 lightgrey d3d3d3 lightpink ffb6c1 lightsalmon ffa07a lightseagreen 20b2aa lightskyblue 87cefa lightslategray 778899 lightslategrey 778899 lightsteelblue b0c4de lightyellow ffffe0 lime 00ff00 limegreen 32cd32 linen faf0e6 magenta ff00ff maroon 800000 mediumaquamarine 66cdaa mediumblue 0000cd mediumorchid ba55d3 mediumpurple 9370db mediumseagreen 3cb371 mediumslateblue 7b68ee mediumspringgreen 00fa9a mediumturquoise 48d1cc mediumvioletred c71585 midnightblue 191970 mintcream f5fffa mistyrose ffe4e1 moccasin ffe4b5 navajowhite ffdead navy 000080 oldlace fdf5e6 olive 808000 olivedrab 6b8e23 orange ffa500 orangered ff4500 orchid da70d6 palegoldenrod eee8aa palegreen 98fb98 paleturquoise afeeee palevioletred db7093 papayawhip ffefd5 peachpuff ffdab9 peru cd853f pink ffc0cb plum dda0dd powderblue b0e0e6 purple 800080 rebeccapurple 663399 red ff0000 rosybrown bc8f8f royalblue 4169e1 saddlebrown 8b4513 salmon fa8072 sandybrown f4a460 seagreen 2e8b57 seashell fff5ee sienna a0522d silver c0c0c0 skyblue 87ceeb slateblue 6a5acd slategray 708090 slategrey 708090 snow fffafa springgreen 00ff7f steelblue 4682b4 tan d2b48c teal 008080 thistle d8bfd8 tomato ff6347 turquoise 40e0d0 violet ee82ee wheat f5deb3 white ffffff whitesmoke f5f5f5 yellow ffff00 yellowgreen 9acd32"

var namedMap map[string]Color

func init() {
	f := strings.Fields(namedColors)
	namedMap = make(map[string]Color, len(f)/2+1)
	for i := 0; i+1 < len(f); i += 2 {
		v, _ := strconv.ParseUint(f[i+1], 16, 32)
		namedMap[f[i]] = RGBA8(uint8(v>>16), uint8(v>>8), uint8(v), 255)
	}
	namedMap["transparent"] = Transparent
}

func splitTop(s string, seps string) []string {
	var out []string
	depth := 0
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		}
		if depth == 0 && strings.IndexByte(seps, s[i]) >= 0 {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// ParseColor parses any CSS color syntax. currentColor resolves to cur.
func ParseColor(s string, cur Color) (Color, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return Color{}, false
	}
	if s == "currentcolor" {
		return cur, true
	}
	if c, ok := namedMap[s]; ok {
		return c, true
	}
	if s[0] == '#' {
		h := s[1:]
		for _, ch := range h {
			if !strings.ContainsRune("0123456789abcdef", ch) {
				return Color{}, false
			}
		}
		x := func(i, n int) float64 {
			v, _ := strconv.ParseUint(h[i:i+n], 16, 32)
			if n == 1 {
				v = v*16 + v
			}
			return float64(v) / 255
		}
		switch len(h) {
		case 3:
			return Color{x(0, 1), x(1, 1), x(2, 1), 1}, true
		case 4:
			return Color{x(0, 1), x(1, 1), x(2, 1), x(3, 1)}, true
		case 6:
			return Color{x(0, 2), x(2, 2), x(4, 2), 1}, true
		case 8:
			return Color{x(0, 2), x(2, 2), x(4, 2), x(6, 2)}, true
		}
		return Color{}, false
	}
	op := strings.IndexByte(s, '(')
	if op < 0 || !strings.HasSuffix(s, ")") {
		return Color{}, false
	}
	fn := strings.TrimSpace(s[:op])
	body := strings.TrimSpace(s[op+1 : len(s)-1])
	if fn == "color-mix" {
		return parseColorMix(body, cur)
	}
	// split into components and optional alpha
	var comps []string
	alpha := "1"
	hasAlpha := false
	if i := strings.IndexByte(body, '/'); i >= 0 {
		alpha = strings.TrimSpace(body[i+1:])
		body = body[:i]
		hasAlpha = true
	}
	comps = splitTop(body, " ,\t\n")
	if !hasAlpha && strings.Contains(body, ",") && (fn == "rgba" || fn == "hsla" || fn == "rgb" || fn == "hsl") && len(comps) == 4 {
		alpha = comps[3]
		comps = comps[:3]
		hasAlpha = true
	}
	a := 1.0
	if hasAlpha {
		v, ok := parseNumPct(alpha, 1)
		if !ok {
			return Color{}, false
		}
		a = clamp01(v)
	}
	if fn == "color" {
		return parseColorFn(comps, a)
	}
	if len(comps) != 3 {
		return Color{}, false
	}
	switch fn {
	case "rgb", "rgba":
		var v [3]float64
		for i, c := range comps {
			if c == "none" {
				continue
			}
			x, ok := parseNumPct(c, 255)
			if !ok {
				return Color{}, false
			}
			v[i] = clamp01(x / 255)
		}
		return Color{v[0], v[1], v[2], a}, true
	case "hsl", "hsla", "hwb":
		h, ok := parseHue(comps[0])
		if !ok {
			return Color{}, false
		}
		p1, ok1 := parseNumPct(comps[1], 100)
		p2, ok2 := parseNumPct(comps[2], 100)
		if !ok1 || !ok2 {
			return Color{}, false
		}
		if fn == "hwb" {
			r, g, b := hwb2rgb(h, clamp01(p1/100), clamp01(p2/100))
			return Color{r, g, b, a}, true
		}
		r, g, b := hsl2rgb(h, clamp01(p1/100), clamp01(p2/100))
		return Color{r, g, b, a}, true
	case "lab", "oklab":
		l, ok1 := parseNumPct(comps[0], 100)
		if fn == "oklab" {
			l, ok1 = parseNumPct(comps[0], 1)
		}
		ma, ok2 := parseNumPct(comps[1], map[bool]float64{true: 125, false: 0.4}[fn == "lab"])
		mb, ok3 := parseNumPct(comps[2], map[bool]float64{true: 125, false: 0.4}[fn == "lab"])
		if !ok1 || !ok2 || !ok3 {
			return Color{}, false
		}
		var r, g, b float64
		if fn == "lab" {
			r, g, b = lab2srgb(l, ma, mb)
		} else {
			r, g, b = oklab2srgb(l, ma, mb)
		}
		return Color{clamp01(r), clamp01(g), clamp01(b), a}, true
	case "lch", "oklch":
		l, ok1 := parseNumPct(comps[0], map[bool]float64{true: 100, false: 1}[fn == "lch"])
		cc, ok2 := parseNumPct(comps[1], map[bool]float64{true: 150, false: 0.4}[fn == "lch"])
		h, ok3 := parseHue(comps[2])
		if !ok1 || !ok2 || !ok3 {
			return Color{}, false
		}
		sh, ch := math.Sincos(h * math.Pi / 180)
		var r, g, b float64
		if fn == "lch" {
			r, g, b = lab2srgb(l, cc*ch, cc*sh)
		} else {
			r, g, b = oklab2srgb(l, cc*ch, cc*sh)
		}
		return Color{clamp01(r), clamp01(g), clamp01(b), a}, true
	}
	return Color{}, false
}

func parseNumPct(s string, pctBase float64) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "none" {
		return 0, true
	}
	if strings.HasSuffix(s, "%") {
		v, err := strconv.ParseFloat(s[:len(s)-1], 64)
		return v / 100 * pctBase, err == nil
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func parseHue(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "none" {
		return 0, true
	}
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
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	v *= mul
	v = math.Mod(v, 360)
	if v < 0 {
		v += 360
	}
	return v, true
}

func hsl2rgb(h, s, l float64) (r, g, b float64) {
	f := func(n float64) float64 {
		k := math.Mod(n+h/30, 12)
		a := s * math.Min(l, 1-l)
		return l - a*math.Max(-1, math.Min(math.Min(k-3, 9-k), 1))
	}
	return f(0), f(8), f(4)
}

func hwb2rgb(h, w, b float64) (float64, float64, float64) {
	if w+b >= 1 {
		g := w / (w + b)
		return g, g, g
	}
	r, g, bl := hsl2rgb(h, 1, 0.5)
	k := 1 - w - b
	return r*k + w, g*k + w, bl*k + w
}

func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func linearToSRGB(c float64) float64 {
	if c <= 0.0031308 {
		return 12.92 * c
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

func xyz2srgb(x, y, z float64) (float64, float64, float64) { // D65
	r := 3.2409699419045226*x - 1.537383177570094*y - 0.4986107602930034*z
	g := -0.9692436362808796*x + 1.8759675015077202*y + 0.04155505740717559*z
	b := 0.05563007969699366*x - 0.20397695888897652*y + 1.0569715142428786*z
	return linearToSRGB(r), linearToSRGB(g), linearToSRGB(b)
}

func lab2srgb(l, a, b float64) (float64, float64, float64) { // CIE Lab D50
	const e, k = 216.0 / 24389, 24389.0 / 27
	fy := (l + 16) / 116
	fx := a/500 + fy
	fz := fy - b/200
	cube := func(v float64) float64 {
		if v*v*v > e {
			return v * v * v
		}
		return (116*v - 16) / k
	}
	x := cube(fx) * 0.3457 / 0.3585
	y := 0.0
	if l > k*e {
		y = math.Pow((l+16)/116, 3)
	} else {
		y = l / k
	}
	z := cube(fz) * (1 - 0.3457 - 0.3585) / 0.3585
	// D50 -> D65 (Bradford)
	X := 0.955473421488075*x - 0.02309845494876471*y + 0.06325924320057072*z
	Y := -0.0283697093338637*x + 1.0099953980813041*y + 0.021041441191917323*z
	Z := 0.012314014864481998*x - 0.020507649298898964*y + 1.330365926242124*z
	return xyz2srgb(X, Y, Z)
}

func oklab2srgb(l, a, b float64) (float64, float64, float64) {
	l_ := l + 0.3963377774*a + 0.2158037573*b
	m_ := l - 0.1055613458*a - 0.0638541728*b
	s_ := l - 0.0894841775*a - 1.2914855480*b
	L, M, S := l_*l_*l_, m_*m_*m_, s_*s_*s_
	r := 4.0767416621*L - 3.3077115913*M + 0.2309699292*S
	g := -1.2684380046*L + 2.6097574011*M - 0.3413193965*S
	bb := -0.0041960863*L - 0.7034186147*M + 1.7076147010*S
	return linearToSRGB(r), linearToSRGB(g), linearToSRGB(bb)
}

func srgb2oklab(r, g, b float64) (float64, float64, float64) {
	r, g, b = srgbToLinear(r), srgbToLinear(g), srgbToLinear(b)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	return 0.2104542553*l + 0.7936177850*m - 0.0040720468*s,
		1.9779984951*l - 2.4285922050*m + 0.4505937099*s,
		0.0259040371*l + 0.7827717662*m - 0.8086757660*s
}

func parseColorFn(c []string, a float64) (Color, bool) {
	if len(c) != 4 {
		return Color{}, false
	}
	var v [3]float64
	for i := 0; i < 3; i++ {
		x, ok := parseNumPct(c[i+1], 1)
		if !ok {
			return Color{}, false
		}
		v[i] = x
	}
	mat := func(m [9]float64, lin bool) Color {
		r := m[0]*v[0] + m[1]*v[1] + m[2]*v[2]
		g := m[3]*v[0] + m[4]*v[1] + m[5]*v[2]
		b := m[6]*v[0] + m[7]*v[1] + m[8]*v[2]
		return Color{clamp01(linearToSRGB(r)), clamp01(linearToSRGB(g)), clamp01(linearToSRGB(b)), a}
	}
	switch c[0] {
	case "srgb":
		return Color{clamp01(v[0]), clamp01(v[1]), clamp01(v[2]), a}, true
	case "srgb-linear":
		return Color{clamp01(linearToSRGB(v[0])), clamp01(linearToSRGB(v[1])), clamp01(linearToSRGB(v[2])), a}, true
	case "display-p3":
		for i := range v {
			v[i] = srgbToLinear(v[i])
		}
		return mat([9]float64{1.2249401762805598, -0.22494017628055996, 0, -0.04205695470968816, 1.0420569547096881, 0, -0.019637554590334432, -0.07863604555063188, 1.0982736001409663}, true), true
	case "rec2020":
		for i := range v {
			if v[i] < 0.08124 {
				v[i] /= 4.5
			} else {
				v[i] = math.Pow((v[i]+0.09929682680944)/1.09929682680944, 1/0.45)
			}
		}
		return mat([9]float64{1.6604910021084345, -0.5876411387885495, -0.07284986331988139, -0.12455047452159074, 1.1328998971259603, -0.008349422604369515, -0.018150763354905303, -0.10057889800800739, 1.1187296613629127}, true), true
	case "a98-rgb":
		for i := range v {
			v[i] = math.Pow(math.Abs(v[i]), 563.0/256) * math.Copysign(1, v[i])
		}
		return mat([9]float64{1.3982832, -0.3982832, 0, 0, 1, 0, 0, -0.0429383, 1.0429383}, true), true
	case "xyz", "xyz-d65":
		r, g, b := xyz2srgb(v[0], v[1], v[2])
		return Color{clamp01(r), clamp01(g), clamp01(b), a}, true
	case "xyz-d50":
		X := 0.955473421488075*v[0] - 0.02309845494876471*v[1] + 0.06325924320057072*v[2]
		Y := -0.0283697093338637*v[0] + 1.0099953980813041*v[1] + 0.021041441191917323*v[2]
		Z := 0.012314014864481998*v[0] - 0.020507649298898964*v[1] + 1.330365926242124*v[2]
		r, g, b := xyz2srgb(X, Y, Z)
		return Color{clamp01(r), clamp01(g), clamp01(b), a}, true
	}
	return Color{}, false
}

func parseColorMix(body string, cur Color) (Color, bool) {
	parts := splitTop(body, ",")
	if len(parts) != 3 {
		return Color{}, false
	}
	in := strings.Fields(strings.TrimSpace(parts[0]))
	if len(in) < 2 || in[0] != "in" {
		return Color{}, false
	}
	space := in[1]
	parseItem := func(s string) (Color, float64, bool, bool) {
		s = strings.TrimSpace(s)
		pct := -1.0
		// percentage may be first or last token outside parens
		f := splitTop(s, " \t")
		var cs []string
		for _, t := range f {
			if strings.HasSuffix(t, "%") {
				v, err := strconv.ParseFloat(t[:len(t)-1], 64)
				if err != nil {
					return Color{}, 0, false, false
				}
				pct = v / 100
			} else {
				cs = append(cs, t)
			}
		}
		c, ok := ParseColor(strings.Join(cs, " "), cur)
		return c, pct, pct >= 0, ok
	}
	c1, p1, h1, ok1 := parseItem(parts[1])
	c2, p2, h2, ok2 := parseItem(parts[2])
	if !ok1 || !ok2 {
		return Color{}, false
	}
	switch {
	case !h1 && !h2:
		p1, p2 = 0.5, 0.5
	case h1 && !h2:
		p2 = 1 - p1
	case !h1 && h2:
		p1 = 1 - p2
	}
	sum := p1 + p2
	if sum <= 0 {
		return Color{}, false
	}
	alphaMul := 1.0
	if sum < 1 {
		alphaMul = sum
	}
	p1, p2 = p1/sum, p2/sum
	// premultiplied interpolation in the chosen space
	toSpace := func(c Color) [3]float64 {
		switch space {
		case "srgb-linear":
			return [3]float64{srgbToLinear(c.R), srgbToLinear(c.G), srgbToLinear(c.B)}
		case "oklab", "oklch":
			l, a, b := srgb2oklab(c.R, c.G, c.B)
			if space == "oklch" {
				return [3]float64{l, math.Hypot(a, b), math.Mod(math.Atan2(b, a)*180/math.Pi+360, 360)}
			}
			return [3]float64{l, a, b}
		}
		return [3]float64{c.R, c.G, c.B}
	}
	a1, a2 := toSpace(c1), toSpace(c2)
	var m [3]float64
	alpha := c1.A*p1 + c2.A*p2
	for i := 0; i < 3; i++ {
		if space == "oklch" && i == 2 {
			d := a2[2] - a1[2]
			if d > 180 {
				d -= 360
			} else if d < -180 {
				d += 360
			}
			m[2] = math.Mod(a1[2]+d*p2+360, 360)
			continue
		}
		if alpha > 0 {
			m[i] = (a1[i]*c1.A*p1 + a2[i]*c2.A*p2) / alpha
		}
	}
	var out Color
	switch space {
	case "srgb-linear":
		out = Color{linearToSRGB(m[0]), linearToSRGB(m[1]), linearToSRGB(m[2]), alpha}
	case "oklab":
		r, g, b := oklab2srgb(m[0], m[1], m[2])
		out = Color{r, g, b, alpha}
	case "oklch":
		s, c := math.Sincos(m[2] * math.Pi / 180)
		r, g, b := oklab2srgb(m[0], m[1]*c, m[1]*s)
		out = Color{r, g, b, alpha}
	default:
		out = Color{m[0], m[1], m[2], alpha}
	}
	out.R, out.G, out.B = clamp01(out.R), clamp01(out.G), clamp01(out.B)
	out.A = clamp01(out.A * alphaMul)
	return out, true
}
