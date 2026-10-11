package style

import (
	"math"
	"strings"
)

// ---- images & gradients --------------------------------------------------------------

func (s *cstate) imageValue(v string) (Image, bool) {
	v = strings.TrimSpace(v)
	l := strings.ToLower(v)
	if l == "none" {
		return Image{}, true
	}
	name, args, ok := funcArgs(v)
	if !ok {
		return Image{}, false
	}
	switch name {
	case "url":
		return Image{URL: urlArg(args)}, true
	case "image-set", "-webkit-image-set":
		// pick the first candidate (or the best for dpr)
		best := ""
		bestRes := 0.0
		for _, p := range splitTop(args, ',') {
			f := fields(strings.TrimSpace(p))
			if len(f) == 0 {
				continue
			}
			u := ""
			if n2, a2, ok2 := funcArgs(f[0]); ok2 && (n2 == "url" || n2 == "src") {
				u = urlArg(a2)
			} else if f[0][0] == '"' || f[0][0] == '\'' {
				u = unquote(f[0])
			}
			if u == "" {
				continue
			}
			res := 1.0
			if len(f) > 1 {
				if x, rest, ok3 := numPrefix(strings.ToLower(f[1])); ok3 {
					res = x
					if rest == "dpi" {
						res = x / 96
					}
				}
			}
			dpr := s.env.DPR
			if dpr == 0 {
				dpr = 1
			}
			if best == "" || math.Abs(res-dpr) < math.Abs(bestRes-dpr) {
				best, bestRes = u, res
			}
		}
		if best == "" {
			return Image{}, false
		}
		return Image{URL: best}, true
	case "linear-gradient", "repeating-linear-gradient", "-webkit-linear-gradient", "-webkit-repeating-linear-gradient",
		"radial-gradient", "repeating-radial-gradient", "-webkit-radial-gradient", "conic-gradient", "repeating-conic-gradient":
		g, ok := s.gradient(name, args)
		if !ok {
			return Image{}, false
		}
		return Image{Grad: g}, true
	case "cross-fade", "element", "paint", "-webkit-canvas":
		return Image{}, true
	}
	return Image{}, false
}

func urlArg(a string) string {
	a = strings.TrimSpace(a)
	if len(a) >= 2 && (a[0] == '"' || a[0] == '\'') {
		return unquote(a)
	}
	return a
}

func (s *cstate) gradient(name, args string) (*Gradient, bool) {
	g := &Gradient{}
	if strings.HasPrefix(name, "repeating-") || strings.Contains(name, "repeating") {
		g.Repeating = true
	}
	switch {
	case strings.Contains(name, "linear"):
		g.Kind = "linear"
	case strings.Contains(name, "radial"):
		g.Kind = "radial"
	default:
		g.Kind = "conic"
	}
	parts := splitTop(args, ',')
	first := strings.TrimSpace(parts[0])
	fl := strings.ToLower(first)
	stopsFrom := 0
	switch g.Kind {
	case "linear":
		g.Angle = 180
		if strings.HasPrefix(fl, "to ") {
			g.ToCorner = strings.TrimSpace(fl[3:])
			switch g.ToCorner {
			case "top":
				g.Angle = 0
				g.ToCorner = ""
			case "right":
				g.Angle = 90
				g.ToCorner = ""
			case "bottom":
				g.Angle = 180
				g.ToCorner = ""
			case "left":
				g.Angle = 270
				g.ToCorner = ""
			default:
				f := strings.Fields(g.ToCorner)
				if len(f) != 2 {
					return nil, false
				}
				// normalize to "<vertical> <horizontal>"
				if f[0] == "left" || f[0] == "right" {
					f[0], f[1] = f[1], f[0]
				}
				g.ToCorner = f[0] + " " + f[1]
			}
			stopsFrom = 1
		} else if a, ok := parseAngle(fl); ok {
			g.Angle = a
			stopsFrom = 1
		} else if strings.HasPrefix(fl, "in ") {
			stopsFrom = 1
		}
		if strings.HasPrefix(name, "-webkit-") {
			g.Angle = math.Mod(450-g.Angle, 360)
		}
	case "radial":
		g.Shape, g.Extent = "ellipse", "farthest-corner"
		isCfg := func(t string) bool {
			for _, w := range strings.Fields(t) {
				switch w {
				case "circle", "ellipse", "closest-side", "farthest-side", "closest-corner", "farthest-corner", "at", "in":
					return true
				}
			}
			if strings.HasPrefix(t, "at ") {
				return true
			}
			// leading length => size
			f := strings.Fields(t)
			if len(f) > 0 {
				if _, ok := s.length(f[0], false); ok {
					if c, _, ok2 := parseColor(f[0], colorCtx{}); !ok2 || c == (Color{}) {
						return true
					}
				}
			}
			return false
		}
		if isCfg(fl) {
			stopsFrom = 1
			rest := fl
			pos := ""
			if i := strings.Index(rest, " at "); i >= 0 {
				pos = rest[i+4:]
				rest = rest[:i]
			} else if strings.HasPrefix(rest, "at ") {
				pos = rest[3:]
				rest = ""
			}
			var lens []Length
			for _, w := range strings.Fields(rest) {
				switch w {
				case "circle", "ellipse":
					g.Shape = w
				case "closest-side", "farthest-side", "closest-corner", "farthest-corner":
					g.Extent = w
				case "in":
				default:
					if x, ok := s.length(w, false); ok {
						lens = append(lens, x)
					}
				}
			}
			if len(lens) == 1 {
				g.RadiusX, g.RadiusY, g.HasSize = lens[0], lens[0], true
				g.Shape = "circle"
			} else if len(lens) == 2 {
				g.RadiusX, g.RadiusY, g.HasSize = lens[0], lens[1], true
				g.Shape = "ellipse"
			}
			if pos != "" {
				x, y, ok := s.positionPair(strings.Fields(pos))
				if !ok {
					return nil, false
				}
				g.Pos, g.HasPos = [2]Length{x, y}, true
			}
		}
	case "conic":
		if strings.HasPrefix(fl, "from ") || strings.HasPrefix(fl, "at ") || strings.HasPrefix(fl, "in ") {
			stopsFrom = 1
			rest := fl
			if strings.HasPrefix(rest, "in ") {
				if i := strings.Index(rest, " from "); i >= 0 {
					rest = rest[i+1:]
				} else if i := strings.Index(rest, " at "); i >= 0 {
					rest = rest[i+1:]
				} else {
					rest = ""
				}
			}
			if strings.HasPrefix(rest, "from ") {
				r := rest[5:]
				pos := ""
				if i := strings.Index(r, " at "); i >= 0 {
					pos = r[i+4:]
					r = r[:i]
				}
				a, ok := parseAngle(strings.TrimSpace(r))
				if !ok {
					return nil, false
				}
				g.From = a
				rest = ""
				if pos != "" {
					rest = "at " + pos
				}
			}
			if strings.HasPrefix(rest, "at ") {
				x, y, ok := s.positionPair(strings.Fields(rest[3:]))
				if !ok {
					return nil, false
				}
				g.Pos, g.HasPos = [2]Length{x, y}, true
			}
		}
	}
	for _, p := range parts[stopsFrom:] {
		p = strings.TrimSpace(p)
		f := fields(p)
		if len(f) == 0 {
			return nil, false
		}
		// color first or last
		var colTok string
		var posToks []string
		if col, cur, ok := s.colorSpec(f[0]); ok {
			colTok = f[0]
			posToks = f[1:]
			_ = col
			_ = cur
		} else if len(f) >= 2 {
			if _, _, ok := s.colorSpec(f[len(f)-1]); ok {
				colTok = f[len(f)-1]
				posToks = f[:len(f)-1]
			}
		}
		if colTok == "" {
			// color hint (position only)
			if len(f) == 1 {
				if x, ok := s.gradPos(f[0], g.Kind == "conic"); ok {
					g.Stops = append(g.Stops, Stop{Pos: x, HasPos: true, Hint: true})
					continue
				}
			}
			return nil, false
		}
		col, cur, _ := s.colorSpec(colTok)
		_ = cur
		if len(posToks) == 0 {
			g.Stops = append(g.Stops, Stop{Color: col})
			continue
		}
		for _, pt := range posToks {
			x, ok := s.gradPos(pt, g.Kind == "conic")
			if !ok {
				return nil, false
			}
			g.Stops = append(g.Stops, Stop{Color: col, Pos: x, HasPos: true})
		}
	}
	if len(g.Stops) < 1 {
		return nil, false
	}
	return g, true
}

func (s *cstate) gradPos(w string, conic bool) (Length, bool) {
	if conic {
		if a, ok := parseAngle(w); ok {
			return Length{K: LPct, V: a / 360 * 100}, true
		}
	}
	return s.length(w, false)
}

// positionPair parses a <position> (1-4 tokens) into x and y lengths: percent
// of the free space plus px offset.
func (s *cstate) positionPair(f []string) (Length, Length, bool) {
	if len(f) == 0 || len(f) > 4 {
		return Length{}, Length{}, false
	}
	for i := range f {
		f[i] = strings.ToLower(f[i])
	}
	kw := func(w string) (axis byte, pct float64, ok bool) {
		switch w {
		case "left":
			return 'x', 0, true
		case "right":
			return 'x', 100, true
		case "top":
			return 'y', 0, true
		case "bottom":
			return 'y', 100, true
		case "center":
			return 'c', 50, true
		}
		return 0, 0, false
	}
	mk := func(pct, px float64) Length {
		if px == 0 {
			return Pct(pct)
		}
		if pct == 0 {
			return Px(px)
		}
		return Length{K: LCalc, C: &Calc{Px: px, Pct: pct}}
	}
	lenVal := func(w string) (Length, bool) { return s.length(w, false) }
	switch len(f) {
	case 1:
		if ax, p, ok := kw(f[0]); ok {
			switch ax {
			case 'x':
				return Pct(p), Pct(50), true
			case 'y':
				return Pct(50), Pct(p), true
			}
			return Pct(50), Pct(50), true
		}
		x, ok := lenVal(f[0])
		return x, Pct(50), ok
	case 2:
		a0, p0, k0 := kw(f[0])
		a1, p1, k1 := kw(f[1])
		switch {
		case k0 && k1:
			if a0 == 'y' || a1 == 'x' {
				if a0 == 'y' && a1 == 'y' || a0 == 'x' && a1 == 'x' {
					return Length{}, Length{}, false
				}
				return Pct(p1), Pct(p0), true
			}
			return Pct(p0), Pct(p1), true
		case k0:
			y, ok := lenVal(f[1])
			if a0 == 'y' {
				return Length{}, Length{}, false
			}
			return Pct(p0), y, ok
		case k1:
			x, ok := lenVal(f[0])
			if a1 == 'x' {
				return Length{}, Length{}, false
			}
			return x, Pct(p1), ok
		}
		x, ok1 := lenVal(f[0])
		y, ok2 := lenVal(f[1])
		return x, y, ok1 && ok2
	}
	// 3 or 4 tokens: edge offset pairs
	x, y := Pct(50), Pct(50)
	i := 0
	for i < len(f) {
		ax, p, ok := kw(f[i])
		if !ok {
			return Length{}, Length{}, false
		}
		off := 0.0
		pctOff := 0.0
		if i+1 < len(f) {
			if _, _, isKw := kw(f[i+1]); !isKw {
				ol, ok2 := lenVal(f[i+1])
				if !ok2 {
					return Length{}, Length{}, false
				}
				if ol.K == LPct {
					pctOff = ol.V
				} else {
					off = ol.V
				}
				i++
			}
		}
		var res Length
		if p == 100 {
			res = Length{K: LCalc, C: &Calc{Px: -off, Pct: 100 - pctOff}}
		} else {
			res = mk(p+pctOff, off)
		}
		switch ax {
		case 'x':
			x = res
		case 'y':
			y = res
		default:
			return Length{}, Length{}, false
		}
		i++
	}
	return x, y, true
}

func (s *cstate) originPair(f []string) (Length, Length, bool) {
	if len(f) == 1 {
		switch f[0] {
		case "top", "bottom":
			x, y, ok := s.positionPair(f)
			return x, y, ok
		}
	}
	if len(f) == 3 {
		f = f[:2]
	}
	return s.positionPair(f)
}

// ---- background --------------------------------------------------------------------

func (s *cstate) backgroundShorthand(v string) bool {
	c := s.c
	layers := splitTop(v, ',')
	var out []BgLayer
	var color Color
	colorSet := false
	for li, ly := range layers {
		bl := defaultLayer()
		toks := fields(strings.TrimSpace(ly))
		var posToks, sizeToks []string
		inSize := false
		boxes := []string{}
		for _, t := range toks {
			lt := strings.ToLower(t)
			switch {
			case t == "/":
				inSize = true
				continue
			case lt == "none":
			case strings.HasPrefix(lt, "url(") || strings.Contains(lt, "gradient(") || strings.HasPrefix(lt, "image-set(") || strings.HasPrefix(lt, "-webkit-image-set("):
				img, ok := s.imageValue(t)
				if !ok {
					return false
				}
				bl.Image = img
			case lt == "repeat-x":
				bl.RepeatX, bl.RepeatY = "repeat", "no-repeat"
			case lt == "repeat-y":
				bl.RepeatX, bl.RepeatY = "no-repeat", "repeat"
			case lt == "repeat" || lt == "no-repeat" || lt == "space" || lt == "round":
				if len(boxes) >= 0 && bl.RepeatSet {
					bl.RepeatY = lt
				} else {
					bl.RepeatX, bl.RepeatY = lt, lt
					bl.RepeatSet = true
				}
			case lt == "scroll" || lt == "fixed" || lt == "local":
				bl.Attach = lt
			case lt == "border-box" || lt == "padding-box" || lt == "content-box":
				boxes = append(boxes, lt)
			case lt == "text":
				boxes = append(boxes, "text")
			default:
				if inSize {
					sizeToks = append(sizeToks, lt)
				} else if _, _, ok := s.colorSpec(t); ok && !isPosKeyword(lt) {
					if li != len(layers)-1 {
						return false
					}
					color, _, _ = s.colorSpec(t)
					colorSet = true
				} else {
					posToks = append(posToks, lt)
				}
			}
		}
		switch len(boxes) {
		case 1:
			bl.Origin = boxes[0]
			bl.Clip = boxes[0]
			if boxes[0] == "text" {
				bl.Origin = "padding-box"
			}
		case 2:
			bl.Origin, bl.Clip = boxes[0], boxes[1]
		}
		if len(posToks) > 0 {
			x, y, ok := s.positionPair(posToks)
			if !ok {
				return false
			}
			bl.PosX, bl.PosY = x, y
		}
		if len(sizeToks) > 0 {
			if !s.bgSize(&bl, sizeToks) {
				return false
			}
		}
		out = append(out, bl)
	}
	c.BgLayers = out
	c.BgColor = color
	_ = colorSet
	return true
}

func isPosKeyword(l string) bool {
	switch l {
	case "left", "right", "top", "bottom", "center":
		return true
	}
	return false
}

func defaultLayer() BgLayer {
	return BgLayer{PosX: Pct(0), PosY: Pct(0), RepeatX: "repeat", RepeatY: "repeat", Origin: "padding-box", Clip: "border-box", Attach: "scroll"}
}

func (s *cstate) bgSize(bl *BgLayer, f []string) bool {
	if len(f) == 1 && (f[0] == "cover" || f[0] == "contain") {
		bl.SizeKind = f[0]
		return true
	}
	if len(f) == 0 || len(f) > 2 {
		return false
	}
	w, ok := s.nonNegLen(f[0])
	if !ok {
		return false
	}
	h := Length{}
	if len(f) == 2 {
		if h, ok = s.nonNegLen(f[1]); !ok {
			return false
		}
	}
	bl.SizeKind, bl.SizeW, bl.SizeH = "", w, h
	return true
}

func (s *cstate) backgroundLonghand(name, v, l string) bool {
	c := s.c
	items := splitTop(v, ',')
	n := len(items)
	for len(c.BgLayers) < n {
		c.BgLayers = append(c.BgLayers, defaultLayer())
	}
	// layers beyond n are cycled when painting; keep count = max
	layers := make([]BgLayer, len(c.BgLayers))
	copy(layers, c.BgLayers)
	c.BgLayers = layers
	for i := range c.BgLayers {
		it := strings.TrimSpace(items[i%n])
		lt := strings.ToLower(it)
		bl := &c.BgLayers[i]
		switch name {
		case "background-image":
			img, ok := s.imageValue(it)
			if !ok {
				return false
			}
			bl.Image = img
		case "background-position":
			x, y, ok := s.positionPair(strings.Fields(lt))
			if !ok {
				return false
			}
			bl.PosX, bl.PosY = x, y
		case "background-position-x":
			x, _, ok := s.positionPair(append(strings.Fields(lt), "center"))
			if len(strings.Fields(lt)) == 1 && !isPosKeyword(lt) {
				x2, ok2 := s.length(lt, false)
				x, ok = x2, ok2
			}
			if !ok {
				return false
			}
			bl.PosX = x
		case "background-position-y":
			var y Length
			var ok bool
			if isPosKeyword(lt) {
				_, y, ok = s.positionPair([]string{"center", lt})
			} else {
				y, ok = s.length(lt, false)
			}
			if !ok {
				return false
			}
			bl.PosY = y
		case "background-size":
			if !s.bgSize(bl, strings.Fields(lt)) {
				return false
			}
		case "background-repeat":
			f := strings.Fields(lt)
			switch {
			case len(f) == 1 && f[0] == "repeat-x":
				bl.RepeatX, bl.RepeatY = "repeat", "no-repeat"
			case len(f) == 1 && f[0] == "repeat-y":
				bl.RepeatX, bl.RepeatY = "no-repeat", "repeat"
			case len(f) == 1:
				bl.RepeatX, bl.RepeatY = f[0], f[0]
			case len(f) == 2:
				bl.RepeatX, bl.RepeatY = f[0], f[1]
			default:
				return false
			}
		case "background-origin":
			bl.Origin = lt
		case "background-clip", "-webkit-background-clip":
			bl.Clip = lt
		case "background-attachment":
			bl.Attach = lt
		case "background-blend-mode":
			bl.Blend = lt
		}
	}
	if len(c.BgLayers) > n {
		// number of layers is determined by background-image
		if name == "background-image" {
			c.BgLayers = c.BgLayers[:n]
		}
	}
	return true
}

// ---- shadows, filters, transforms -----------------------------------------------------

func (s *cstate) shadows(v string, text bool) ([]Shadow, bool) {
	if strings.ToLower(strings.TrimSpace(v)) == "none" {
		return nil, true
	}
	var out []Shadow
	for _, p := range splitTop(v, ',') {
		sh := Shadow{}
		sh.Color = s.c.Color
		var nums []float64
		for _, t := range fields(strings.TrimSpace(p)) {
			lt := strings.ToLower(t)
			if lt == "inset" && !text {
				sh.Inset = true
				continue
			}
			if x, ok := s.px(lt); ok {
				nums = append(nums, x)
				continue
			}
			if x, cur, ok := s.colorSpec(t); ok {
				sh.Color, sh.cur = x, cur
				continue
			}
			return nil, false
		}
		if len(nums) < 2 || len(nums) > 4 || (text && len(nums) > 3) {
			return nil, false
		}
		sh.DX, sh.DY = nums[0], nums[1]
		if len(nums) > 2 {
			sh.Blur = math.Max(0, nums[2])
		}
		if len(nums) > 3 {
			sh.Spread = nums[3]
		}
		out = append(out, sh)
	}
	return out, len(out) > 0
}

func (s *cstate) filters(v string) ([]FilterFn, bool) {
	if strings.ToLower(strings.TrimSpace(v)) == "none" {
		return nil, true
	}
	var out []FilterFn
	for _, t := range fields(v) {
		name, args, ok := funcArgs(t)
		if !ok {
			return nil, false
		}
		f := FilterFn{Name: name}
		switch name {
		case "blur":
			x, ok := s.px(args)
			if !ok && strings.TrimSpace(args) == "" {
				x, ok = 0, true
			}
			if !ok {
				return nil, false
			}
			f.Args = []float64{x}
		case "brightness", "contrast", "grayscale", "invert", "opacity", "saturate", "sepia":
			x := 1.0
			if strings.TrimSpace(args) != "" {
				x, ok = s.numOrPct(strings.TrimSpace(args))
				if !ok {
					return nil, false
				}
			}
			f.Args = []float64{x}
		case "hue-rotate":
			x := 0.0
			if strings.TrimSpace(args) != "" {
				x, ok = parseAngle(args)
				if !ok {
					return nil, false
				}
			}
			f.Args = []float64{x}
		case "drop-shadow":
			sh, ok := s.shadows(args, true)
			if !ok || len(sh) != 1 {
				return nil, false
			}
			f.Shad = sh[0]
			f.Color = sh[0].Color
			f.Args = []float64{sh[0].DX, sh[0].DY, sh[0].Blur}
		case "url":
			continue
		default:
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

func (s *cstate) transform(v string) ([]TOp, bool) {
	if strings.ToLower(strings.TrimSpace(v)) == "none" {
		return nil, true
	}
	var out []TOp
	for _, t := range fields(v) {
		name, args, ok := funcArgs(t)
		if !ok {
			return nil, false
		}
		var op TOp
		op.Fn = name
		parts := splitTop(args, ',')
		if len(parts) > 6 && name != "matrix3d" {
			return nil, false
		}
		angle := func(x string) (Length, bool) {
			a, ok := parseAngle(strings.TrimSpace(x))
			return Length{K: LNumber, V: a}, ok
		}
		lenp := func(x string) (Length, bool) { return s.length(strings.TrimSpace(x), false) }
		num := func(x string) (Length, bool) {
			n, ok := s.numOrPct(strings.TrimSpace(x))
			return Length{K: LNumber, V: n}, ok
		}
		var conv func(string) (Length, bool)
		switch name {
		case "translate", "translatex", "translatey", "translatez", "translate3d":
			conv = lenp
		case "scale", "scalex", "scaley", "scalez", "scale3d", "matrix", "matrix3d", "perspective":
			conv = num
			if name == "perspective" {
				conv = lenp
			}
		case "rotate", "rotatex", "rotatey", "rotatez", "skew", "skewx", "skewy":
			conv = angle
		case "rotate3d":
			conv = num
		default:
			return nil, false
		}
		for i, p := range parts {
			if i >= 6 {
				break
			}
			var x Length
			var ok bool
			if name == "rotate3d" && i == 3 {
				x, ok = angle(p)
			} else {
				x, ok = conv(p)
			}
			if !ok {
				return nil, false
			}
			op.A[i] = x
			op.N++
		}
		if name == "matrix3d" && len(parts) == 16 {
			// keep 2D components a,b,c,d,tx,ty = m[0],m[1],m[4],m[5],m[12],m[13]
			vals := make([]float64, 16)
			for i, p := range parts {
				n, ok := parseNumber(strings.TrimSpace(p))
				if !ok {
					return nil, false
				}
				vals[i] = n
			}
			op.Fn = "matrix"
			for i, idx := range []int{0, 1, 4, 5, 12, 13} {
				op.A[i] = Length{K: LNumber, V: vals[idx]}
			}
			op.N = 6
		}
		out = append(out, op)
	}
	return out, len(out) > 0
}

func (s *cstate) individualTransform(name, v string) ([]TOp, bool) {
	l := strings.ToLower(strings.TrimSpace(v))
	if l == "none" {
		return nil, true
	}
	f := fields(l)
	switch name {
	case "translate":
		op := TOp{Fn: "translate"}
		for i, w := range f {
			if i > 1 {
				break
			}
			x, ok := s.length(w, false)
			if !ok {
				return nil, false
			}
			op.A[i] = x
			op.N++
		}
		return []TOp{op}, op.N > 0
	case "scale":
		op := TOp{Fn: "scale"}
		for i, w := range f {
			if i > 1 {
				break
			}
			n, ok := s.numOrPct(w)
			if !ok {
				return nil, false
			}
			op.A[i] = Length{K: LNumber, V: n}
			op.N++
		}
		return []TOp{op}, op.N > 0
	default:
		for _, w := range f {
			if a, ok := parseAngle(w); ok {
				return []TOp{{Fn: "rotate", A: [6]Length{{K: LNumber, V: a}}, N: 1}}, true
			}
		}
	}
	return nil, false
}

// ---- content ----------------------------------------------------------------------------

func (s *cstate) content(v, l string) bool {
	c := s.c
	c.Content, c.ContentSet, c.ContentNone = nil, false, false
	switch l {
	case "normal":
		return true
	case "none":
		c.ContentSet, c.ContentNone = true, true
		return true
	}
	var items []ContentItem
	altIdx := strings.Index(v, "/")
	if altIdx > 0 && !strings.Contains(v[:altIdx], "(") {
		v = v[:altIdx]
	}
	for _, t := range fields(v) {
		lt := strings.ToLower(t)
		switch {
		case t == "/":
			goto done
		case t[0] == '"' || t[0] == '\'':
			items = append(items, ContentItem{K: "str", S: unquote(t)})
		case lt == "open-quote" || lt == "close-quote" || lt == "no-open-quote" || lt == "no-close-quote":
			items = append(items, ContentItem{K: lt})
		default:
			name, args, ok := funcArgs(t)
			if !ok {
				return false
			}
			parts := splitTop(args, ',')
			switch name {
			case "attr":
				a := strings.TrimSpace(parts[0])
				if i := strings.IndexAny(a, " "); i > 0 {
					a = a[:i]
				}
				items = append(items, ContentItem{K: "attr", S: a})
			case "counter":
				it := ContentItem{K: "counter", S: strings.TrimSpace(parts[0]), Style: "decimal"}
				if len(parts) > 1 {
					it.Style = strings.ToLower(strings.TrimSpace(parts[1]))
				}
				items = append(items, it)
			case "counters":
				if len(parts) < 2 {
					return false
				}
				it := ContentItem{K: "counters", S: strings.TrimSpace(parts[0]), Sep: unquote(parts[1]), Style: "decimal"}
				if len(parts) > 2 {
					it.Style = strings.ToLower(strings.TrimSpace(parts[2]))
				}
				items = append(items, it)
			case "url", "image-set", "-webkit-image-set":
				items = append(items, ContentItem{K: "url", S: urlArg(args)})
			case "linear-gradient", "radial-gradient", "conic-gradient":
				items = append(items, ContentItem{K: "url", S: ""})
			default:
				return false
			}
		}
	}
done:
	c.Content, c.ContentSet = items, true
	return true
}

// ---- grid -------------------------------------------------------------------------------

func (s *cstate) trackSz(w string, allowFr bool) (Sz, bool) {
	lw := strings.ToLower(strings.TrimSpace(w))
	switch lw {
	case "auto":
		return Sz{K: SzAuto}, true
	case "min-content":
		return Sz{K: SzMinContent}, true
	case "max-content":
		return Sz{K: SzMaxContent}, true
	}
	if strings.HasSuffix(lw, "fr") {
		x, ok := parseNumber(lw[:len(lw)-2])
		if !ok || !allowFr || x < 0 {
			return Sz{}, false
		}
		return Sz{K: SzFr, Fr: x}, true
	}
	x, ok := s.nonNegLen(lw)
	if !ok {
		return Sz{}, false
	}
	return Sz{K: SzLen, L: x}, true
}

func (s *cstate) trackFn(w string) (Track, bool) {
	name, args, ok := funcArgs(w)
	if ok {
		switch name {
		case "minmax":
			p := splitTop(args, ',')
			if len(p) != 2 {
				return Track{}, false
			}
			mn, ok1 := s.trackSz(p[0], false)
			mx, ok2 := s.trackSz(p[1], true)
			if !ok1 || !ok2 {
				// minmax(fr, ...) invalid; allow fr in min? treat as auto
				if ok2 && strings.HasSuffix(strings.TrimSpace(p[0]), "fr") {
					return Track{}, false
				}
				return Track{}, false
			}
			return Track{Min: mn, Max: mx}, true
		case "fit-content":
			x, ok2 := s.nonNegLen(strings.TrimSpace(args))
			if !ok2 {
				return Track{}, false
			}
			return Track{Min: Sz{K: SzAuto}, Max: Sz{K: SzFitContent, L: x}}, true
		case "calc", "min", "max", "clamp":
			x, ok2 := s.nonNegLen(w)
			if !ok2 {
				return Track{}, false
			}
			sz := Sz{K: SzLen, L: x}
			return Track{Min: sz, Max: sz}, true
		}
		return Track{}, false
	}
	sz, ok := s.trackSz(w, true)
	if !ok {
		return Track{}, false
	}
	if sz.K == SzFr {
		return Track{Min: Sz{K: SzAuto}, Max: sz}, true
	}
	return Track{Min: sz, Max: sz}, true
}

func parseNames(w string) []string {
	return strings.Fields(strings.TrimSpace(w[1 : len(w)-1]))
}

func (s *cstate) trackList(v string) (TrackList, bool) {
	l := strings.ToLower(strings.TrimSpace(v))
	if l == "none" {
		return TrackList{None: true}, true
	}
	var tl TrackList
	if strings.HasPrefix(l, "subgrid") {
		tl.Subgrid = true
		return tl, true
	}
	var cur *[]Track = &tl.Pre
	var names []string
	seenRep := false
	for _, w := range fields(v) {
		if w[0] == '[' {
			names = append(names, parseNames(w)...)
			continue
		}
		lw := strings.ToLower(w)
		if strings.HasPrefix(lw, "repeat(") {
			_, args, ok := funcArgs(lw)
			if !ok {
				return tl, false
			}
			p := splitTop(args, ',')
			if len(p) < 2 {
				return tl, false
			}
			cnt := strings.TrimSpace(p[0])
			body := strings.Join(p[1:], ",")
			var group []Track
			var gnames []string
			var endNames []string
			for _, bw := range fields(body) {
				if bw[0] == '[' {
					gnames = append(gnames, parseNames(bw)...)
					continue
				}
				t, ok := s.trackFn(bw)
				if !ok {
					return tl, false
				}
				t.Names = gnames
				gnames = nil
				group = append(group, t)
			}
			endNames = gnames
			if len(group) == 0 {
				return tl, false
			}
			if cnt == "auto-fill" || cnt == "auto-fit" {
				if seenRep {
					return tl, false
				}
				seenRep = true
				if len(names) > 0 {
					// names before the repeat attach to the first repeated track on first iteration; keep in Pre end names
					tl.EndNames = nil
				}
				if len(names) > 0 {
					group[0].Names = append(append([]string(nil), names...), group[0].Names...)
				}
				names = nil
				tl.Rep = group
				tl.RepMode = strings.TrimPrefix(cnt, "auto-")
				tl.RepEndNames = endNames
				cur = &tl.Post
				continue
			}
			n, ok := parseInt(cnt)
			if !ok || n < 1 {
				return tl, false
			}
			if n > 1000 {
				n = 1000
			}
			for i := 0; i < n; i++ {
				for gi, t := range group {
					t2 := t
					if gi == 0 {
						t2.Names = append(append([]string(nil), names...), t.Names...)
						names = nil
					}
					*cur = append(*cur, t2)
				}
				if i < n-1 && len(endNames) > 0 {
					names = append(names, endNames...)
				}
			}
			names = append(names, endNames...)
			continue
		}
		t, ok := s.trackFn(w)
		if !ok {
			return tl, false
		}
		t.Names = names
		names = nil
		*cur = append(*cur, t)
	}
	tl.EndNames = names
	if len(tl.Pre)+len(tl.Rep)+len(tl.Post) == 0 {
		return tl, false
	}
	return tl, true
}

func (s *cstate) autoTracks(v string) ([]Track, bool) {
	var out []Track
	for _, w := range fields(v) {
		t, ok := s.trackFn(w)
		if !ok {
			return nil, false
		}
		out = append(out, t)
	}
	return out, len(out) > 0
}

func parseGridLine(l string) (GridLine, bool) {
	g := GridLine{Set: true}
	l = strings.TrimSpace(l)
	if l == "auto" {
		g.Auto = true
		return g, true
	}
	f := strings.Fields(l)
	for i := 0; i < len(f); i++ {
		if f[i] == "span" {
			g.Span = 1
			continue
		}
		if n, ok := parseInt(f[i]); ok {
			if g.Span > 0 {
				g.Span = n
			} else {
				g.Num = n
			}
			continue
		}
		g.Name = f[i]
	}
	if g.Span > 0 && g.Name != "" && g.Span == 0 {
		g.Span = 1
	}
	return g, true
}

func (s *cstate) gridPlacement(name, l string) bool {
	c := s.c
	parts := strings.Split(l, "/")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	get := func(i int, dflt GridLine) (GridLine, bool) {
		if i < len(parts) {
			return parseGridLine(parts[i])
		}
		return dflt, true
	}
	switch name {
	case "grid-row", "grid-column":
		st, ok := parseGridLine(parts[0])
		if !ok {
			return false
		}
		var en GridLine
		if len(parts) > 1 {
			en, ok = parseGridLine(parts[1])
			if !ok {
				return false
			}
		} else {
			en = GridLine{Auto: true, Set: true}
			if st.Name != "" && st.Num == 0 && st.Span == 0 {
				en = GridLine{Name: st.Name, Set: true}
			}
		}
		if name == "grid-row" {
			c.GridRowStart, c.GridRowEnd = st, en
		} else {
			c.GridColStart, c.GridColEnd = st, en
		}
	case "grid-area":
		rs, ok1 := get(0, GridLine{Auto: true, Set: true})
		cs, ok2 := get(1, GridLine{})
		re, ok3 := get(2, GridLine{})
		ce, ok4 := get(3, GridLine{})
		if !ok1 || !ok2 || !ok3 || !ok4 {
			return false
		}
		// single ident: all four lines are the named area
		if len(parts) == 1 && rs.Name != "" && rs.Num == 0 && rs.Span == 0 {
			c.GridRowStart, c.GridColStart = rs, rs
			c.GridRowEnd, c.GridColEnd = rs, rs
			c.GridRowStart.Name, c.GridRowStart.Num = rs.Name, 0
			return true
		}
		if len(parts) < 2 {
			cs = rs
		}
		if len(parts) < 3 {
			re = rs
			if len(parts) < 3 && rs.Name == "" {
				re = GridLine{Auto: true, Set: true}
			}
		}
		if len(parts) < 4 {
			ce = cs
			if cs.Name == "" {
				ce = GridLine{Auto: true, Set: true}
			}
		}
		c.GridRowStart, c.GridColStart, c.GridRowEnd, c.GridColEnd = rs, cs, re, ce
	}
	return true
}

func (s *cstate) gridTemplate(v, l string) bool {
	c := s.c
	if l == "none" {
		c.GridCols, c.GridRows, c.GridAreas = TrackList{None: true}, TrackList{None: true}, nil
		return true
	}
	// grid shorthand with auto-flow
	if strings.Contains(l, "auto-flow") {
		parts := strings.Split(v, "/")
		if len(parts) != 2 {
			return false
		}
		left, right := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if strings.Contains(strings.ToLower(left), "auto-flow") {
			c.GridAutoFlow = "row"
			if strings.Contains(strings.ToLower(left), "dense") {
				c.GridAutoFlow = "row dense"
			}
			rest := strings.TrimSpace(strings.NewReplacer("auto-flow", "", "dense", "").Replace(strings.ToLower(left)))
			if rest != "" {
				t, ok := s.autoTracks(rest)
				if !ok {
					return false
				}
				c.GridAutoRows = t
			}
			tl, ok := s.trackList(right)
			if !ok {
				return false
			}
			c.GridCols = tl
			return true
		}
		c.GridAutoFlow = "column"
		if strings.Contains(strings.ToLower(right), "dense") {
			c.GridAutoFlow = "column dense"
		}
		rest := strings.TrimSpace(strings.NewReplacer("auto-flow", "", "dense", "").Replace(strings.ToLower(right)))
		if rest != "" {
			t, ok := s.autoTracks(rest)
			if !ok {
				return false
			}
			c.GridAutoCols = t
		}
		tl, ok := s.trackList(left)
		if !ok {
			return false
		}
		c.GridRows = tl
		return true
	}
	parts := strings.Split(v, "/")
	if len(parts) == 2 && !strings.ContainsAny(parts[0], "\"'") {
		r, ok1 := s.trackList(strings.TrimSpace(parts[0]))
		cc, ok2 := s.trackList(strings.TrimSpace(parts[1]))
		if !ok1 || !ok2 {
			return false
		}
		c.GridRows, c.GridCols, c.GridAreas = r, cc, nil
		return true
	}
	// areas form: "a b" 100px "c d" auto / cols
	colsPart := ""
	body := v
	if len(parts) == 2 {
		body, colsPart = parts[0], strings.TrimSpace(parts[1])
	}
	var areas [][]string
	var rows TrackList
	rowTokens := []string{}
	pendingNames := []string{}
	for _, t := range fields(body) {
		if t[0] == '"' || t[0] == '\'' {
			areas = append(areas, strings.Fields(unquote(t)))
			if len(pendingNames) > 0 || len(rowTokens) < len(areas) {
				rowTokens = append(rowTokens, "")
			}
			continue
		}
		if t[0] == '[' {
			pendingNames = append(pendingNames, parseNames(t)...)
			continue
		}
		// a size following a string
		if len(rowTokens) > 0 && rowTokens[len(rowTokens)-1] == "" {
			rowTokens[len(rowTokens)-1] = t
		}
	}
	for i := range areas {
		sz := "auto"
		if i < len(rowTokens) && rowTokens[i] != "" {
			sz = rowTokens[i]
		}
		tr, ok := s.trackFn(sz)
		if !ok {
			return false
		}
		rows.Pre = append(rows.Pre, tr)
	}
	c.GridRows, c.GridAreas = rows, areas
	if colsPart != "" {
		cc, ok := s.trackList(colsPart)
		if !ok {
			return false
		}
		c.GridCols = cc
	}
	return len(areas) > 0
}
