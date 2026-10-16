package style

import (
	"math"
	"strings"

	"github.com/sweefbrowser/sweef/internal/font"
)

// cstate is the working state while computing one element's style.
type cstate struct {
	c    *Computed
	p    *Computed
	uc   unitCtx
	env  *Env
	cc   colorCtx
	eng  *Engine
	vars map[string]string
	// font-size dependent lazily computed metrics
	metricsDone bool
}

var keywordSizes = [8]float64{9, 10, 13, 16, 18, 24, 32, 48}

// fixed font table rows for medium sizes 9..16 (monospace default is 13).
var fixedTable = [8][8]float64{
	{9, 9, 9, 9, 11, 14, 18, 27},
	{9, 9, 9, 10, 12, 15, 20, 30},
	{9, 9, 10, 11, 13, 17, 22, 33},
	{9, 9, 10, 12, 14, 18, 24, 36},
	{9, 10, 12, 13, 16, 20, 26, 40},
	{9, 10, 12, 14, 17, 21, 28, 42},
	{9, 10, 13, 15, 18, 23, 30, 45},
	{9, 10, 13, 16, 18, 24, 32, 48},
}

func isMonospaceOnly(fam []string) bool { return len(fam) == 1 && fam[0] == "monospace" }

func (s *cstate) sizeKeyword(k string) (float64, bool) {
	idx := -1
	switch k {
	case "xx-small":
		idx = 0
	case "x-small":
		idx = 1
	case "small":
		idx = 2
	case "medium":
		idx = 3
	case "large":
		idx = 4
	case "x-large":
		idx = 5
	case "xx-large":
		idx = 6
	case "xxx-large":
		idx = 7
	}
	if idx < 0 {
		return 0, false
	}
	if isMonospaceOnly(s.c.FontFamily) {
		return fixedTable[4][idx], true
	}
	return keywordSizes[idx], true
}

func (s *cstate) setupMetrics() {
	s.uc.em = s.c.FontSize
	s.metricsDone = false
}

// lazy font metrics for ex / ch / cap
func (s *cstate) fontMetrics() (ex, ch, capH float64) {
	s.eng.noteWebMetrics(s.c.FontFamily)
	st := font.Style{Weight: s.c.FontWeight, Italic: s.c.FontStyle != 0}
	f := font.Resolve(s.c.FontFamily, st)
	if f == nil {
		return s.c.FontSize / 2, s.c.FontSize / 2, s.c.FontSize * 0.7
	}
	m := f.Metrics(s.c.FontSize)
	ex = m.XHeight
	if ex <= 0 {
		ex = s.c.FontSize / 2
	}
	ch = f.Advance('0', s.c.FontSize)
	if ch <= 0 {
		ch = s.c.FontSize / 2
	}
	return ex, ch, m.CapHeight
}

func (s *cstate) length(v string, allowNum bool) (Length, bool) {
	if strings.Contains(v, "lh") {
		s.uc.lh = s.lineHeightPx()
	}
	if strings.Contains(v, "ex") || strings.Contains(v, "ch") || strings.Contains(v, "cap") {
		if !s.metricsDone {
			s.uc.exH, s.uc.chW, s.uc.capH = s.fontMetrics()
			s.metricsDone = true
		}
	}
	return s.uc.parseLength(v, allowNum)
}

// lineHeightPx is the element's computed line-height in px (for the lh unit).
func (s *cstate) lineHeightPx() float64 {
	lh := s.c.LineHeight
	var v float64
	switch lh.K {
	case LNumber:
		v = lh.V * s.c.FontSize
	case LPx:
		v = lh.V
	default:
		s.eng.noteWebMetrics(s.c.FontFamily)
		st := font.Style{Weight: s.c.FontWeight, Italic: s.c.FontStyle != 0}
		if f := font.Resolve(s.c.FontFamily, st); f != nil {
			m := f.Metrics(s.c.FontSize)
			v = m.Ascent + m.Descent + m.LineGap
		} else {
			v = s.c.FontSize * 1.2
		}
	}
	return math.Floor(v*64) / 64
}

func (s *cstate) nonNegLen(v string) (Length, bool) {
	l, ok := s.length(v, false)
	if !ok || l.K == LPx && l.V < 0 || l.K == LPct && l.V < 0 {
		return l, false
	}
	return l, true
}

// px parses an absolute length (no percentages).
func (s *cstate) px(v string) (float64, bool) {
	l, ok := s.length(v, false)
	if !ok || l.K != LPx {
		return 0, false
	}
	return l.V, true
}

func (s *cstate) color(v string) (Color, bool) {
	s.cc.cur = s.c.Color
	c, _, ok := parseColor(v, s.cc)
	return c, ok
}

// colorSpec returns (color, isCurrent, ok).
func (s *cstate) colorSpec(v string) (Color, bool, bool) {
	s.cc.cur = s.c.Color
	return parseColor(v, s.cc)
}

func borderWidth(w float64) float64 {
	if w > 0 && w < 1 {
		return 1
	}
	return math.Floor(w)
}

func isBorderStyle(w string) bool {
	switch w {
	case "none", "hidden", "dotted", "dashed", "solid", "double", "groove", "ridge", "inset", "outset":
		return true
	}
	return false
}

// borderWidthVal parses a border width incl. keywords.
func (s *cstate) borderWidthVal(w string) (float64, bool) {
	switch w {
	case "thin":
		return 1, true
	case "medium":
		return 3, true
	case "thick":
		return 5, true
	}
	l, ok := s.length(w, false)
	if !ok || l.K != LPx || l.V < 0 {
		return 0, false
	}
	return borderWidth(l.V), true
}

var sideIdx = map[string]int{"top": 0, "right": 1, "bottom": 2, "left": 3}

func (s *cstate) box4Len(v string, allowAuto, nonNeg bool) ([4]Length, bool) {
	f := fields(v)
	if len(f) == 0 || len(f) > 4 {
		return [4]Length{}, false
	}
	var vals []Length
	for _, w := range f {
		var l Length
		var ok bool
		if nonNeg {
			l, ok = s.nonNegLen(w)
		} else {
			l, ok = s.length(w, false)
		}
		if !ok || (!allowAuto && l.K != LPx && l.K != LPct && l.K != LCalc) {
			return [4]Length{}, false
		}
		vals = append(vals, l)
	}
	return expand4(vals), true
}

func expand4[T any](v []T) [4]T {
	var o [4]T
	switch len(v) {
	case 1:
		o = [4]T{v[0], v[0], v[0], v[0]}
	case 2:
		o = [4]T{v[0], v[1], v[0], v[1]}
	case 3:
		o = [4]T{v[0], v[1], v[2], v[1]}
	default:
		o = [4]T{v[0], v[1], v[2], v[3]}
	}
	return o
}

// Supports reports whether name: val is a valid declaration.
func Supports(name, val string) bool {
	name = strings.ToLower(name)
	if strings.HasPrefix(name, "--") {
		return true
	}
	s := &cstate{c: Initial(), p: Initial(), env: &Env{Width: 1280, Height: 800}}
	s.uc = unitCtx{em: 16, rem: 16, vw: 1280, vh: 800}
	val = strings.TrimSpace(val)
	if strings.Contains(strings.ToLower(val), "var(") {
		return true
	}
	return s.apply(name, val, strings.ToLower(val))
}

func isGlobal(l string) string {
	switch l {
	case "inherit", "initial", "unset", "revert", "revert-layer":
		return l
	}
	return ""
}

// apply sets one declaration; it returns false when the value is invalid.
func (s *cstate) apply(name, v, l string) bool {
	c := s.c
	switch name {
	// ---- display & positioning ------------------------------------------------
	case "display":
		d, ok := normDisplay(l)
		if !ok {
			return false
		}
		c.Display = d
	case "position":
		switch l {
		case "static", "relative", "absolute", "fixed", "sticky":
			c.Position = l
		case "-webkit-sticky":
			c.Position = "sticky"
		default:
			return false
		}
	case "float":
		switch l {
		case "left", "right", "none":
			c.Float = l
		case "inline-start":
			c.Float = "left"
		case "inline-end":
			c.Float = "right"
		default:
			return false
		}
	case "clear":
		switch l {
		case "left", "right", "both", "none":
			c.Clear = l
		case "inline-start":
			c.Clear = "left"
		case "inline-end":
			c.Clear = "right"
		default:
			return false
		}
	case "box-sizing":
		if l != "content-box" && l != "border-box" {
			return false
		}
		c.BoxSizing = l
	case "visibility":
		if l != "visible" && l != "hidden" && l != "collapse" {
			return false
		}
		c.Visibility = l
	case "overflow":
		f := strings.Fields(l)
		if len(f) == 0 || len(f) > 2 {
			return false
		}
		for _, w := range f {
			if !isOverflow(w) {
				return false
			}
		}
		c.OverflowX = f[0]
		c.OverflowY = f[len(f)-1]
	case "overflow-x":
		if !isOverflow(l) {
			return false
		}
		c.OverflowX = l
	case "overflow-y":
		if !isOverflow(l) {
			return false
		}
		c.OverflowY = l
	case "overflow-block":
		if !isOverflow(l) {
			return false
		}
		c.OverflowY = l
	case "overflow-inline":
		if !isOverflow(l) {
			return false
		}
		c.OverflowX = l
	case "overflow-clip-margin":
	case "z-index":
		if l == "auto" {
			c.ZAuto = true
			c.ZIndex = 0
			return true
		}
		n, ok := parseInt(l)
		if !ok {
			return false
		}
		c.ZAuto = false
		c.ZIndex = n
	case "top", "right", "bottom", "left":
		x, ok := s.length(v, false)
		if !ok || x.K == LNone || x.IsIntrinsic() {
			return false
		}
		c.Inset[sideIdx[name]] = x
	case "inset":
		b, ok := s.box4Len(v, true, false)
		if !ok {
			return false
		}
		c.Inset = b
	case "inset-block":
		return s.logical2(v, 0, 2, &c.Inset)
	case "inset-inline":
		return s.logical2(v, s.inlineStart(), s.inlineEnd(), &c.Inset)
	case "inset-block-start":
		return s.setLen(&c.Inset[0], v, true)
	case "inset-block-end":
		return s.setLen(&c.Inset[2], v, true)
	case "inset-inline-start":
		return s.setLen(&c.Inset[s.inlineStart()], v, true)
	case "inset-inline-end":
		return s.setLen(&c.Inset[s.inlineEnd()], v, true)
	case "text-orientation":
		switch l {
		case "mixed", "upright", "sideways", "sideways-right":
			c.TextOrientation = l
		default:
			return false
		}
	case "contain":
		c.Contain = l
	case "content-visibility":
		switch l {
		case "visible", "hidden", "auto":
			c.ContentVisibility = l
		default:
			return false
		}
	case "contain-intrinsic-width", "contain-intrinsic-height", "contain-intrinsic-size", "contain-intrinsic-inline-size", "contain-intrinsic-block-size":
		parts := fields(l)
		parse := func(p string) (Length, bool, bool) {
			auto := false
			if strings.HasPrefix(p, "auto ") {
				auto = true
				p = strings.TrimSpace(p[5:])
			}
			if p == "none" {
				return Length{K: LNone}, auto, true
			}
			x, ok := s.length(p, false)
			return x, auto, ok
		}
		// "auto 200px" arrives as two fields: re-join an auto prefix
		var vals []string
		for i := 0; i < len(parts); i++ {
			if parts[i] == "auto" && i+1 < len(parts) {
				vals = append(vals, "auto "+parts[i+1])
				i++
			} else {
				vals = append(vals, parts[i])
			}
		}
		if len(vals) == 0 || len(vals) > 2 {
			return false
		}
		w, wa, ok := parse(vals[0])
		if !ok {
			return false
		}
		h, ha := w, wa
		if len(vals) == 2 {
			var ok2 bool
			h, ha, ok2 = parse(vals[1])
			if !ok2 {
				return false
			}
		}
		switch name {
		case "contain-intrinsic-width", "contain-intrinsic-inline-size":
			c.CIntrW, c.CIntrAutoW = w, wa
		case "contain-intrinsic-height", "contain-intrinsic-block-size":
			c.CIntrH, c.CIntrAutoH = w, wa
		default:
			c.CIntrW, c.CIntrAutoW, c.CIntrH, c.CIntrAutoH = w, wa, h, ha
		}
	case "isolation":
		c.Isolation = l
	case "appearance", "-webkit-appearance", "-moz-appearance":
		c.Appearance = l
	case "resize":
		c.Resize = l
	case "scrollbar-width":
		c.Scrollbar = l
	case "scrollbar-gutter":
		c.ScrollbarGutter = l
	case "will-change":
		c.WillChange = l
	// ---- box model --------------------------------------------------------------
	case "width", "height", "min-width", "min-height", "max-width", "max-height":
		x, ok := s.length(v, false)
		if !ok {
			return false
		}
		if (x.K == LPx || x.K == LPct) && x.V < 0 {
			return false
		}
		if strings.HasPrefix(name, "max-") && x.K == LAuto {
			return false
		}
		if strings.HasPrefix(name, "min-") && x.K == LNone {
			return false
		}
		if (name == "width" || name == "height") && x.K == LNone {
			return false
		}
		switch name {
		case "width":
			c.Width = x
		case "height":
			c.Height = x
		case "min-width":
			c.MinWidth = x
		case "min-height":
			c.MinHeight = x
		case "max-width":
			c.MaxWidth = x
		case "max-height":
			c.MaxHeight = x
		}
	case "inline-size":
		return s.apply("width", v, l)
	case "block-size":
		return s.apply("height", v, l)
	case "min-inline-size":
		return s.apply("min-width", v, l)
	case "min-block-size":
		return s.apply("min-height", v, l)
	case "max-inline-size":
		return s.apply("max-width", v, l)
	case "max-block-size":
		return s.apply("max-height", v, l)
	case "margin":
		b, ok := s.box4Len(v, true, false)
		if !ok {
			return false
		}
		c.Margin = b
	case "margin-top", "margin-right", "margin-bottom", "margin-left":
		x, ok := s.length(v, false)
		if !ok || x.IsIntrinsic() || x.K == LNone {
			return false
		}
		c.Margin[sideIdx[name[7:]]] = x
	case "margin-block":
		return s.logical2(v, 0, 2, &c.Margin)
	case "margin-inline":
		return s.logical2(v, s.inlineStart(), s.inlineEnd(), &c.Margin)
	case "margin-block-start":
		return s.setLen(&c.Margin[0], v, true)
	case "margin-block-end":
		return s.setLen(&c.Margin[2], v, true)
	case "margin-inline-start":
		return s.setLen(&c.Margin[s.inlineStart()], v, true)
	case "margin-inline-end":
		return s.setLen(&c.Margin[s.inlineEnd()], v, true)
	case "padding":
		b, ok := s.box4Len(v, false, true)
		if !ok {
			return false
		}
		c.Padding = b
	case "padding-top", "padding-right", "padding-bottom", "padding-left":
		x, ok := s.nonNegLen(v)
		if !ok || x.K == LAuto || x.IsIntrinsic() || x.K == LNone {
			return false
		}
		c.Padding[sideIdx[name[8:]]] = x
	case "padding-block":
		return s.logical2(v, 0, 2, &c.Padding)
	case "padding-inline":
		return s.logical2(v, s.inlineStart(), s.inlineEnd(), &c.Padding)
	case "padding-block-start":
		return s.setLen(&c.Padding[0], v, false)
	case "padding-block-end":
		return s.setLen(&c.Padding[2], v, false)
	case "padding-inline-start":
		return s.setLen(&c.Padding[s.inlineStart()], v, false)
	case "padding-inline-end":
		return s.setLen(&c.Padding[s.inlineEnd()], v, false)
	case "aspect-ratio":
		c.AspectAuto = false
		c.AspectRatio = 0
		if l == "auto" {
			return true
		}
		f := fields(l)
		if len(f) > 0 && f[0] == "auto" {
			c.AspectAuto = true
			f = f[1:]
		}
		if len(f) == 0 {
			return false
		}
		a, ok := parseNumber(f[0])
		if !ok {
			return false
		}
		b := 1.0
		if len(f) == 3 && f[1] == "/" {
			b, ok = parseNumber(f[2])
			if !ok {
				return false
			}
		} else if len(f) != 1 {
			return false
		}
		if b == 0 || a <= 0 {
			return true
		}
		c.AspectRatio = a / b
	case "zoom":
		if z, ok := parseNumber(strings.TrimSuffix(l, "%")); ok {
			if strings.HasSuffix(l, "%") {
				z /= 100
			}
			if z > 0 {
				c.Zoom = z
			}
		}
	// ---- borders ---------------------------------------------------------------------
	case "border":
		return s.borderShorthand(v, l, [4]bool{true, true, true, true})
	case "border-top", "border-right", "border-bottom", "border-left":
		var m [4]bool
		m[sideIdx[name[7:]]] = true
		return s.borderShorthand(v, l, m)
	case "border-block":
		return s.borderShorthand(v, l, [4]bool{true, false, true, false})
	case "border-inline":
		return s.borderShorthand(v, l, [4]bool{false, true, false, true})
	case "border-block-start":
		return s.borderShorthand(v, l, [4]bool{true, false, false, false})
	case "border-block-end":
		return s.borderShorthand(v, l, [4]bool{false, false, true, false})
	case "border-inline-start":
		var m [4]bool
		m[s.inlineStart()] = true
		return s.borderShorthand(v, l, m)
	case "border-inline-end":
		var m [4]bool
		m[s.inlineEnd()] = true
		return s.borderShorthand(v, l, m)
	case "border-width":
		f := fields(l)
		if len(f) == 0 || len(f) > 4 {
			return false
		}
		var vals []float64
		for _, w := range f {
			x, ok := s.borderWidthVal(w)
			if !ok {
				return false
			}
			vals = append(vals, x)
		}
		c.BorderWidth = expand4(vals)
	case "border-style":
		f := fields(l)
		if len(f) == 0 || len(f) > 4 {
			return false
		}
		for _, w := range f {
			if !isBorderStyle(w) {
				return false
			}
		}
		c.BorderStyle = expand4(f)
	case "border-color":
		f := fields(v)
		if len(f) == 0 || len(f) > 4 {
			return false
		}
		var cols []Color
		var curs []bool
		for _, w := range f {
			x, cur, ok := s.colorSpec(w)
			if !ok {
				return false
			}
			cols = append(cols, x)
			curs = append(curs, cur)
		}
		c.BorderColor = expand4(cols)
		c.bcSet = [4]bool{true, true, true, true}
		_ = curs
	case "border-top-width", "border-right-width", "border-bottom-width", "border-left-width":
		x, ok := s.borderWidthVal(l)
		if !ok {
			return false
		}
		c.BorderWidth[sideIdx[name[7:len(name)-6]]] = x
	case "border-top-style", "border-right-style", "border-bottom-style", "border-left-style":
		if !isBorderStyle(l) {
			return false
		}
		c.BorderStyle[sideIdx[name[7:len(name)-6]]] = l
	case "border-top-color", "border-right-color", "border-bottom-color", "border-left-color":
		x, _, ok := s.colorSpec(v)
		if !ok {
			return false
		}
		i := sideIdx[name[7:len(name)-6]]
		c.BorderColor[i] = x
		c.bcSet[i] = true
	case "border-block-width", "border-inline-width", "border-block-style", "border-inline-style", "border-block-color", "border-inline-color":
		f := fields(v)
		if len(f) == 0 || len(f) > 2 {
			return false
		}
		a, b := f[0], f[len(f)-1]
		i1, i2 := 0, 2
		if strings.Contains(name, "inline") {
			i1, i2 = 3, 1
		}
		kind := name[strings.LastIndexByte(name, '-')+1:]
		sides := [4]string{"top", "right", "bottom", "left"}
		return s.apply("border-"+sides[i1]+"-"+kind, a, strings.ToLower(a)) && s.apply("border-"+sides[i2]+"-"+kind, b, strings.ToLower(b))
	case "border-radius":
		return s.radiusShorthand(v)
	case "border-top-left-radius", "border-top-right-radius", "border-bottom-right-radius", "border-bottom-left-radius":
		f := fields(v)
		if len(f) == 0 || len(f) > 2 {
			return false
		}
		h, ok := s.nonNegLen(f[0])
		if !ok {
			return false
		}
		vv := h
		if len(f) == 2 {
			vv, ok = s.nonNegLen(f[1])
			if !ok {
				return false
			}
		}
		idx := map[string]int{"border-top-left-radius": 0, "border-top-right-radius": 1, "border-bottom-right-radius": 2, "border-bottom-left-radius": 3}[name]
		c.Radius[idx] = [2]Length{h, vv}
	case "border-start-start-radius", "border-start-end-radius", "border-end-end-radius", "border-end-start-radius":
		idx := map[string]int{"border-start-start-radius": 0, "border-start-end-radius": 1, "border-end-end-radius": 2, "border-end-start-radius": 3}[name]
		h, ok := s.nonNegLen(strings.Fields(v)[0])
		if !ok {
			return false
		}
		c.Radius[idx] = [2]Length{h, h}
	case "border-image", "border-image-source":
		c.BorderImage = l != "none"
	case "border-image-slice", "border-image-width", "border-image-outset", "border-image-repeat":
	case "border-collapse":
		switch l {
		case "collapse":
			c.BorderCollapse = true
		case "separate":
			c.BorderCollapse = false
		default:
			return false
		}
	case "border-spacing":
		f := fields(l)
		if len(f) == 0 || len(f) > 2 {
			return false
		}
		h, ok := s.px(f[0])
		if !ok || h < 0 {
			return false
		}
		vv := h
		if len(f) == 2 {
			vv, ok = s.px(f[1])
			if !ok || vv < 0 {
				return false
			}
		}
		c.SpacingH, c.SpacingV = h, vv
	case "caption-side":
		c.CaptionSide = l
	case "empty-cells":
		c.EmptyCellsHide = l == "hide"
	case "container-type":
		switch l {
		case "normal", "size", "inline-size":
			c.ContainerType = l
		default:
			return false
		}
	case "container-name":
		c.ContainerName = parseContainerNames(l)
	case "container":
		nm, ty := l, "normal"
		if i := strings.IndexByte(l, '/'); i >= 0 {
			nm, ty = strings.TrimSpace(l[:i]), strings.TrimSpace(l[i+1:])
		}
		if ty != "normal" && ty != "size" && ty != "inline-size" {
			return false
		}
		c.ContainerType = ty
		c.ContainerName = parseContainerNames(nm)
	case "table-layout":
		if l != "auto" && l != "fixed" {
			return false
		}
		c.TableLayout = l
	// ---- outline -------------------------------------------------------------------
	case "outline":
		c.OutlineWidth, c.OutlineStyle, c.outlineSet = 3, "none", false
		for _, w := range fields(v) {
			lw := strings.ToLower(w)
			if isBorderStyle(lw) || lw == "auto" {
				c.OutlineStyle = lw
			} else if x, ok := s.borderWidthVal(lw); ok {
				c.OutlineWidth = x
			} else if x, _, ok := s.colorSpec(w); ok {
				c.OutlineColor = x
				c.outlineSet = true
			} else {
				return false
			}
		}
	case "outline-width":
		x, ok := s.borderWidthVal(l)
		if !ok {
			return false
		}
		c.OutlineWidth = x
	case "outline-style":
		if !isBorderStyle(l) && l != "auto" {
			return false
		}
		c.OutlineStyle = l
	case "outline-color":
		x, _, ok := s.colorSpec(v)
		if !ok {
			return false
		}
		c.OutlineColor = x
		c.outlineSet = true
	case "outline-offset":
		x, ok := s.px(l)
		if !ok {
			return false
		}
		c.OutlineOffset = x
	// ---- color & background -----------------------------------------------------------
	case "color":
		if l == "currentcolor" {
			return true
		}
		x, ok := s.colorOf(v, s.p.Color)
		if !ok {
			return false
		}
		c.Color = x
	case "background-color":
		x, _, ok := s.colorSpec(v)
		if !ok {
			return false
		}
		c.BgColor = x
	case "background":
		return s.backgroundShorthand(v)
	case "background-image", "background-position", "background-position-x", "background-position-y", "background-size", "background-repeat",
		"background-origin", "background-clip", "background-attachment", "background-blend-mode", "-webkit-background-clip":
		return s.backgroundLonghand(name, v, l)
	case "opacity":
		x, ok := s.numOrPct(l)
		if !ok {
			return false
		}
		c.Opacity = math.Max(0, math.Min(1, x))
		c.OpacitySet = true
	case "box-shadow":
		sh, ok := s.shadows(v, false)
		if !ok {
			return false
		}
		c.BoxShadow = sh
	case "text-shadow":
		sh, ok := s.shadows(v, true)
		if !ok {
			return false
		}
		c.TextShadow = sh
	case "mix-blend-mode":
		c.MixBlend = l
	case "filter":
		f, ok := s.filters(v)
		if !ok {
			return false
		}
		c.Filter = f
	case "backdrop-filter", "-webkit-backdrop-filter":
		f, ok := s.filters(v)
		if !ok {
			return false
		}
		c.BackdropFilter = f
	case "clip-path", "-webkit-clip-path":
		cp, ok := s.clipPathValue(v)
		if !ok {
			return false
		}
		c.ClipPathV = cp
		c.ClipPath = cp != nil
	case "transform":
		t, ok := s.transform(v)
		if !ok {
			return false
		}
		c.Transform = t
	case "translate", "rotate", "scale":
		t, ok := s.individualTransform(name, v)
		if !ok {
			return false
		}
		switch name {
		case "translate":
			c.Translate = t
		case "rotate":
			c.Rotate = t
		default:
			c.Scale = t
		}
	case "transform-origin":
		f := fields(l)
		if len(f) == 0 || len(f) > 3 {
			return false
		}
		x, y, ok := s.originPair(f)
		if !ok {
			return false
		}
		c.TransformOrigin = [2]Length{x, y}
		c.TOriginZ = 0
		if len(f) == 3 {
			z, ok := s.px(f[2])
			if !ok {
				return false
			}
			c.TOriginZ = z
		}
	case "perspective", "-webkit-perspective":
		if l == "none" {
			c.Perspective = 0
		} else if x, ok := s.px(l); ok {
			c.Perspective = x
		} else {
			return false
		}
	case "perspective-origin", "-webkit-perspective-origin":
		f := fields(l)
		if len(f) == 0 || len(f) > 2 {
			return false
		}
		x, y, ok := s.positionPair(f)
		if !ok {
			return false
		}
		c.PerspectiveOrigin = [2]Length{x, y}
	case "transform-style", "-webkit-transform-style":
		switch l {
		case "flat":
			c.TransformStyle = ""
		case "preserve-3d":
			c.TransformStyle = "preserve-3d"
		default:
			return false
		}
	case "backface-visibility", "-webkit-backface-visibility":
		switch l {
		case "visible":
			c.BackfaceHidden = false
		case "hidden":
			c.BackfaceHidden = true
		default:
			return false
		}
	case "transform-box":
		switch l {
		case "content-box", "border-box", "fill-box", "stroke-box", "view-box":
			c.TransformBox = l
		default:
			return false
		}
	// ---- fonts & text -----------------------------------------------------------------
	case "font":
		return s.fontShorthand(v, l)
	case "font-family":
		fam := s.eng.scopeFamilies(parseFamilies(v))
		if len(fam) == 0 {
			return false
		}
		c.FontFamily = fam
	case "font-size":
		return s.fontSize(v, l)
	case "font-weight":
		w, ok := s.fontWeight(l)
		if !ok {
			return false
		}
		c.FontWeight = w
	case "font-style":
		switch {
		case l == "normal":
			c.FontStyle = 0
		case l == "italic":
			c.FontStyle = 1
		case strings.HasPrefix(l, "oblique"):
			c.FontStyle = 2
		default:
			return false
		}
	case "font-variant", "font-variant-caps":
		c.SmallCaps = strings.Contains(l, "small-caps")
		if strings.Contains(l, "all-small-caps") {
			c.SmallCaps = true
		}
	case "font-stretch":
	case "font-kerning":
		c.FontKerning = l
	case "font-variant-numeric":
		c.FontVariantNum = l
	case "font-variant-ligatures":
		c.FontLigOff = strings.Contains(l, "none") || strings.Contains(l, "no-common-ligatures")
	case "font-feature-settings":
		if l == "normal" {
			c.FontLigOff = false
		} else {
			for _, tag := range []string{"liga", "clig"} {
				for _, off := range []string{"0", "off"} {
					if strings.Contains(l, "\""+tag+"\" "+off) || strings.Contains(l, "'"+tag+"' "+off) {
						c.FontLigOff = true
					}
				}
			}
		}
	case "font-variation-settings", "font-optical-sizing", "font-synthesis", "font-variant-east-asian", "font-language-override", "font-size-adjust", "text-size-adjust", "-webkit-text-size-adjust", "-webkit-font-smoothing", "-moz-osx-font-smoothing", "font-smooth", "font-palette", "font-variant-position", "font-variant-alternates":
	case "line-height":
		return s.lineHeight(v, l)
	case "letter-spacing":
		if l == "normal" {
			c.LetterSpacing = 0
			return true
		}
		x, ok := s.px(l)
		if !ok {
			return false
		}
		c.LetterSpacing = x
	case "word-spacing":
		if l == "normal" {
			c.WordSpacing = 0
			return true
		}
		x, ok := s.length(l, false)
		if !ok {
			return false
		}
		if x.K == LPct {
			x = Px(x.V * c.FontSize / 100)
		}
		c.WordSpacing = x.V
	case "text-align":
		switch l {
		case "start", "end", "left", "right", "center", "justify", "match-parent", "-webkit-center", "-webkit-left", "-webkit-right", "-internal-center", "justify-all":
			c.TextAlign = l
		default:
			return false
		}
	case "text-align-last":
		c.TextAlignLast = l
	case "text-indent":
		f := fields(l)
		if len(f) == 0 {
			return false
		}
		x, ok := s.length(f[0], false)
		if !ok {
			return false
		}
		c.TextIndent = x
		c.TextIndentHang = len(f) > 1 && f[1] == "hanging"
	case "text-transform":
		c.TextTransform = strings.Fields(l)[0]
	case "white-space":
		switch l {
		case "normal", "nowrap", "pre", "pre-wrap", "pre-line", "break-spaces":
			c.WhiteSpace = l
		default:
			return false
		}
	case "white-space-collapse":
		switch l {
		case "collapse":
			if c.WhiteSpace == "pre-wrap" || c.WhiteSpace == "break-spaces" {
				c.WhiteSpace = "normal"
			} else if c.WhiteSpace == "pre" {
				c.WhiteSpace = "nowrap"
			}
		case "preserve":
			if c.WhiteSpace == "normal" {
				c.WhiteSpace = "pre-wrap"
			} else if c.WhiteSpace == "nowrap" {
				c.WhiteSpace = "pre"
			}
		case "preserve-breaks":
			c.WhiteSpace = "pre-line"
		case "break-spaces":
			c.WhiteSpace = "break-spaces"
		}
	case "text-wrap", "text-wrap-mode", "text-wrap-style":
		switch l {
		case "wrap", "balance", "pretty", "stable", "nowrap":
			if name == "text-wrap-mode" || l == "nowrap" || l == "wrap" {
				if l == "nowrap" {
					c.TextWrap = "nowrap"
				} else {
					c.TextWrap = "wrap"
				}
			} else {
				c.TextWrap = l
			}
		}
	case "word-break":
		c.WordBreak = l
	case "overflow-wrap", "word-wrap":
		c.OverflowWrap = l
	case "line-break":
		c.LineBreak = l
	case "hyphens", "-webkit-hyphens":
		c.Hyphens = l
	case "direction":
		if l != "ltr" && l != "rtl" {
			return false
		}
		c.Direction = l
	case "unicode-bidi":
		c.UnicodeBidi = l
	case "writing-mode", "-webkit-writing-mode":
		c.WritingMode = l
	case "tab-size", "-moz-tab-size":
		if x, ok := parseNumber(l); ok {
			c.TabSize, c.TabSizePx = x, false
		} else if x, ok := s.px(l); ok {
			c.TabSize, c.TabSizePx = x, true
		} else {
			return false
		}
	case "text-decoration":
		return s.textDecoration(v, l)
	case "text-decoration-line":
		c.TextDecorLine = decoLines(l)
	case "text-decoration-style":
		c.TextDecorStyle = l
	case "text-decoration-color":
		x, cur, ok := s.colorSpec(v)
		if !ok {
			return false
		}
		c.TextDecorColor, c.DecorCur = x, cur
	case "text-decoration-thickness":
		x, ok := s.length(l, false)
		if !ok && l != "auto" && l != "from-font" {
			return false
		}
		c.TextDecorationThickness = x
	case "text-underline-offset":
		x, _ := s.length(l, false)
		c.TextUnderlineOffset = x
	case "text-underline-position":
		c.TextUnderlinePos = l
	case "text-overflow":
		c.TextOverflow = strings.Fields(l)[0]
	case "-webkit-text-fill-color":
		if l == "currentcolor" {
			c.TextFillSet = false
			return true
		}
		x, ok := s.color(v)
		if !ok {
			return false
		}
		c.TextFill, c.TextFillSet = x, true
	case "-webkit-text-stroke":
		for _, w := range fields(v) {
			if x, ok := s.px(strings.ToLower(w)); ok {
				c.TextStrokeW = x
			} else if x, ok := s.color(w); ok {
				c.TextStrokeC, c.TextStrokeSet = x, true
			}
		}
	case "-webkit-text-stroke-width":
		if x, ok := s.borderWidthVal(l); ok {
			c.TextStrokeW = x
		}
	case "-webkit-text-stroke-color":
		if x, ok := s.color(v); ok {
			c.TextStrokeC, c.TextStrokeSet = x, true
		}
	case "vertical-align":
		switch l {
		case "baseline", "sub", "super", "top", "text-top", "middle", "bottom", "text-bottom", "-webkit-baseline-middle":
			c.VerticalAlign = l
			c.VerticalAlignLen = Length{}
		default:
			x, ok := s.length(l, false)
			if !ok || x.K == LAuto {
				return false
			}
			c.VerticalAlign = "length"
			c.VerticalAlignLen = x
		}
	case "cursor":
		f := splitTop(l, ',')
		c.Cursor = strings.TrimSpace(f[len(f)-1])
	case "pointer-events":
		c.PointerEvents = l
	case "user-select", "-webkit-user-select", "-moz-user-select":
		c.UserSelect = l
	case "scroll-behavior":
		if l != "auto" && l != "smooth" {
			return false
		}
		c.ScrollBehavior = l
	case "overscroll-behavior", "overscroll-behavior-x", "overscroll-behavior-y", "overscroll-behavior-inline", "overscroll-behavior-block":
		f := strings.Fields(l)
		if len(f) == 0 || len(f) > 2 {
			return false
		}
		for _, w := range f {
			if w != "auto" && w != "contain" && w != "none" {
				return false
			}
		}
		x, y := f[0], f[len(f)-1]
		switch name {
		case "overscroll-behavior":
			c.OverscrollX, c.OverscrollY = x, y
		case "overscroll-behavior-x", "overscroll-behavior-inline":
			c.OverscrollX = x
		default:
			c.OverscrollY = x
		}
	case "image-rendering":
		c.ImageRendering = l
	case "text-rendering":
		c.TextRendering = l
	case "color-scheme":
		c.ColorScheme = l
	case "accent-color":
		if x, ok := s.color(v); ok {
			c.AccentColor, c.AccentSet = x, true
		}
	case "caret-color":
		if x, ok := s.color(v); ok {
			c.CaretColor, c.CaretSet = x, true
		}
	case "-webkit-line-clamp", "line-clamp":
		if l == "none" {
			c.LineClamp = 0
		} else if n, ok := parseInt(l); ok {
			c.LineClamp = n
		}
	case "orphans", "widows":
		n, ok := parseInt(l)
		if !ok || n < 1 {
			return false
		}
		if name == "orphans" {
			c.Orphans = n
		} else {
			c.Widows = n
		}
	case "break-before", "page-break-before":
		c.BreakBefore = normBreak(l)
	case "break-after", "page-break-after":
		c.BreakAfter = normBreak(l)
	case "break-inside", "page-break-inside":
		c.BreakInside = normBreak(l)
	case "object-fit":
		c.ObjectFit = l
	case "object-position":
		f := fields(l)
		x, y, ok := s.positionPair(f)
		if !ok {
			return false
		}
		c.ObjectPos = [2]Length{x, y}
	// ---- lists & generated content ----------------------------------------------------
	case "list-style":
		return s.listStyleShorthand(v, l)
	case "list-style-type":
		c.ListStyleType = unquoteKeep(v, l)
	case "list-style-position":
		c.ListStylePos = l
	case "list-style-image":
		img, ok := s.imageValue(v)
		if !ok {
			return false
		}
		c.ListStyleImage = img
	case "content":
		return s.content(v, l)
	case "quotes":
		if l == "none" {
			c.Quotes = nil
			return true
		}
		f := fields(v)
		if len(f)%2 != 0 || len(f) == 0 {
			return l == "auto"
		}
		var q [][2]string
		for i := 0; i < len(f); i += 2 {
			q = append(q, [2]string{unquote(f[i]), unquote(f[i+1])})
		}
		c.Quotes = q
	case "counter-reset", "counter-increment", "counter-set":
		ops, ok := parseCounters(l, name == "counter-increment")
		if !ok {
			return false
		}
		switch name {
		case "counter-reset":
			c.CounterReset = ops
		case "counter-increment":
			c.CounterIncr = ops
		default:
			c.CounterSet = ops
		}
	// ---- flex --------------------------------------------------------------------------
	case "flex":
		return s.flexShorthand(v, l)
	case "flex-direction":
		switch l {
		case "row", "row-reverse", "column", "column-reverse":
			c.FlexDirection = l
		default:
			return false
		}
	case "flex-wrap":
		switch l {
		case "nowrap", "wrap", "wrap-reverse":
			c.FlexWrap = l
		default:
			return false
		}
	case "flex-flow":
		for _, w := range fields(l) {
			switch w {
			case "row", "row-reverse", "column", "column-reverse":
				c.FlexDirection = w
			case "nowrap", "wrap", "wrap-reverse":
				c.FlexWrap = w
			default:
				return false
			}
		}
	case "flex-grow":
		x, ok := parseNumber(l)
		if !ok || x < 0 {
			return false
		}
		c.FlexGrow = x
	case "flex-shrink":
		x, ok := parseNumber(l)
		if !ok || x < 0 {
			return false
		}
		c.FlexShrink = x
	case "flex-basis":
		x, ok := s.nonNegLen(v)
		if !ok {
			return false
		}
		if l == "content" {
			x = Length{K: LMaxContent, V: -2}
		}
		c.FlexBasis = x
	case "order":
		x, ok := parseInt(l)
		if !ok {
			return false
		}
		c.Order = x
	case "justify-content":
		c.JustifyContent = normAlign(l)
	case "align-items":
		c.AlignItems = normAlign(l)
	case "align-self":
		c.AlignSelf = normAlign(l)
	case "align-content":
		c.AlignContent = normAlign(l)
	case "justify-items":
		c.JustifyItems = normAlign(l)
	case "justify-self":
		c.JustifySelf = normAlign(l)
	case "place-content":
		f := strings.Fields(l)
		if len(f) == 0 {
			return false
		}
		c.AlignContent = normAlign(f[0])
		c.JustifyContent = normAlign(f[len(f)-1])
		if len(f) == 2 && (f[0] == "first" || f[0] == "last" || f[0] == "safe" || f[0] == "unsafe") {
			c.AlignContent = normAlign(f[0] + " " + f[1])
			c.JustifyContent = c.AlignContent
		}
	case "place-items":
		f := strings.Fields(l)
		if len(f) == 0 {
			return false
		}
		c.AlignItems = normAlign(f[0])
		c.JustifyItems = normAlign(f[len(f)-1])
	case "place-self":
		f := strings.Fields(l)
		if len(f) == 0 {
			return false
		}
		c.AlignSelf = normAlign(f[0])
		c.JustifySelf = normAlign(f[len(f)-1])
	case "gap", "grid-gap":
		f := fields(l)
		if len(f) == 0 || len(f) > 2 {
			return false
		}
		r, ok := s.gapLen(f[0])
		if !ok {
			return false
		}
		cc := r
		if len(f) == 2 {
			cc, ok = s.gapLen(f[1])
			if !ok {
				return false
			}
		}
		c.RowGap, c.ColumnGap = r, cc
	case "row-gap", "grid-row-gap":
		x, ok := s.gapLen(l)
		if !ok {
			return false
		}
		c.RowGap = x
	case "column-gap", "grid-column-gap":
		x, ok := s.gapLen(l)
		if !ok {
			return false
		}
		c.ColumnGap = x
	case "-webkit-box-orient":
		c.BoxOrient = l
	case "-webkit-box-direction", "-webkit-box-pack", "-webkit-box-align", "-webkit-box-flex", "-webkit-box-lines", "-webkit-box-ordinal-group":
		switch name {
		case "-webkit-box-pack":
			c.JustifyContent = map[string]string{"start": "flex-start", "end": "flex-end", "center": "center", "justify": "space-between"}[l]
		case "-webkit-box-align":
			c.AlignItems = map[string]string{"start": "flex-start", "end": "flex-end", "center": "center", "stretch": "stretch", "baseline": "baseline"}[l]
		case "-webkit-box-flex":
			if x, ok := parseNumber(l); ok {
				c.FlexGrow = x
				c.FlexShrink = 0
			}
		}
	// ---- grid -----------------------------------------------------------------------------
	case "grid-template-columns", "grid-template-rows":
		tl, ok := s.trackList(v)
		if !ok {
			return false
		}
		if name == "grid-template-columns" {
			c.GridCols = tl
		} else {
			c.GridRows = tl
		}
	case "grid-auto-columns", "grid-auto-rows":
		tl, ok := s.autoTracks(v)
		if !ok {
			return false
		}
		if name == "grid-auto-columns" {
			c.GridAutoCols = tl
		} else {
			c.GridAutoRows = tl
		}
	case "grid-auto-flow":
		c.GridAutoFlow = l
	case "grid-template-areas":
		if l == "none" {
			c.GridAreas = nil
			return true
		}
		var rows [][]string
		for _, str := range quotedStrings(v) {
			rows = append(rows, strings.Fields(str))
		}
		if len(rows) == 0 {
			return false
		}
		c.GridAreas = rows
	case "grid-template":
		return s.gridTemplate(v, l)
	case "grid":
		return s.gridTemplate(v, l)
	case "grid-row-start", "grid-row-end", "grid-column-start", "grid-column-end":
		// Grid line and area names are case-sensitive custom idents, so parse v, not l.
		g, ok := parseGridLine(v)
		if !ok {
			return false
		}
		switch name {
		case "grid-row-start":
			c.GridRowStart = g
		case "grid-row-end":
			c.GridRowEnd = g
		case "grid-column-start":
			c.GridColStart = g
		default:
			c.GridColEnd = g
		}
	case "grid-row", "grid-column", "grid-area":
		return s.gridPlacement(name, v)
	// ---- multicol ---------------------------------------------------------------------------
	case "column-count":
		if l == "auto" {
			c.ColumnCount = 0
		} else if n, ok := parseInt(l); ok && n > 0 {
			c.ColumnCount = n
		} else {
			return false
		}
	case "column-width":
		if l == "auto" {
			c.ColumnWidth = Length{}
		} else if x, ok := s.nonNegLen(l); ok {
			c.ColumnWidth = x
		} else {
			return false
		}
	case "columns":
		c.ColumnCount, c.ColumnWidth = 0, Length{}
		for _, w := range fields(l) {
			if n, ok := parseInt(w); ok {
				c.ColumnCount = n
			} else if x, ok := s.nonNegLen(w); ok {
				c.ColumnWidth = x
			} else if w != "auto" {
				return false
			}
		}
	case "column-rule":
		c.ColumnRuleW, c.ColumnRuleStyle = 3, "none"
		for _, w := range fields(v) {
			lw := strings.ToLower(w)
			if isBorderStyle(lw) {
				c.ColumnRuleStyle = lw
			} else if x, ok := s.borderWidthVal(lw); ok {
				c.ColumnRuleW = x
			} else if x, ok := s.color(w); ok {
				c.ColumnRuleColor = x
			}
		}
	case "column-rule-width":
		if x, ok := s.borderWidthVal(l); ok {
			c.ColumnRuleW = x
		}
	case "column-rule-style":
		c.ColumnRuleStyle = l
	case "column-rule-color":
		if x, ok := s.color(v); ok {
			c.ColumnRuleColor = x
		}
	case "column-fill":
		c.ColumnFill = l
	case "box-decoration-break", "-webkit-box-decoration-break":
		if l != "slice" && l != "clone" {
			return false
		}
		c.BoxDecorationBreak = l
	case "column-span":
		c.ColumnSpan = l
	case "all":
	default:
		if handled, ok := s.applyMask(name, v, l); handled {
			return ok
		}
		// unknown or purely visual/irrelevant property (transition, animation, ...)
		if strings.HasPrefix(name, "transition") || strings.HasPrefix(name, "animation") || strings.HasPrefix(name, "-webkit-") || strings.HasPrefix(name, "-moz-") ||
			strings.HasPrefix(name, "scroll-") || strings.HasPrefix(name, "overscroll") || strings.HasPrefix(name, "mask-") || strings.HasPrefix(name, "touch-") || strings.HasPrefix(name, "view-") {
			return true
		}
		switch name {
		case "will-change", "backface-visibility", "transform-style", "transform-box", "shape-outside", "shape-margin", "shape-image-threshold", "fill", "stroke", "stroke-width", "fill-opacity", "stroke-opacity",
			"stroke-linecap", "stroke-linejoin", "stroke-dasharray", "stroke-dashoffset", "fill-rule", "clip-rule", "stop-color", "stop-opacity", "vector-effect", "paint-order",
			"text-emphasis", "text-emphasis-color", "text-emphasis-style", "text-emphasis-position", "ruby-position", "hanging-punctuation", "forced-color-adjust", "print-color-adjust",
			"image-orientation", "mask-type", "anchor-name", "position-anchor", "interpolate-size", "field-sizing", "cx", "cy", "r", "rx", "ry", "x", "y", "d", "marker", "marker-start", "marker-mid", "marker-end", "dominant-baseline", "alignment-baseline", "baseline-shift", "text-anchor", "shape-rendering", "color-interpolation", "color-interpolation-filters", "flood-color", "flood-opacity", "lighting-color", "clip", "math-style", "math-depth", "initial-letter", "offset", "offset-path", "text-justify", "text-combine-upright", "speak", "src", "unicode-range", "font-display", "ascent-override", "descent-override", "line-gap-override", "size-adjust", "page", "size", "marks", "bleed", "caret", "caret-shape", "nav-up", "nav-down", "nav-left", "nav-right", "zoom-x":
			return true
		}
		return false
	}
	return true
}

func isOverflow(w string) bool {
	switch w {
	case "visible", "hidden", "scroll", "auto", "clip", "overlay":
		return true
	}
	return false
}

func (s *cstate) setLen(dst *Length, v string, allowAuto bool) bool {
	x, ok := s.length(v, false)
	if !ok || (!allowAuto && x.K == LAuto) || x.IsIntrinsic() || x.K == LNone {
		return false
	}
	*dst = x
	return true
}

func (s *cstate) logical2(v string, a, b int, arr *[4]Length) bool {
	f := fields(v)
	if len(f) == 0 || len(f) > 2 {
		return false
	}
	x, ok := s.length(f[0], false)
	if !ok {
		return false
	}
	y := x
	if len(f) == 2 {
		if y, ok = s.length(f[1], false); !ok {
			return false
		}
	}
	arr[a], arr[b] = x, y
	return true
}

func (s *cstate) gapLen(w string) (Length, bool) {
	if w == "normal" {
		return Length{}, true
	}
	return s.nonNegLen(w)
}

func (s *cstate) numOrPct(l string) (float64, bool) {
	if strings.HasSuffix(l, "%") {
		x, ok := parseNumber(strings.TrimSuffix(l, "%"))
		return x / 100, ok
	}
	return parseNumber(l)
}

func unquoteKeep(v, l string) string {
	if len(v) > 0 && (v[0] == '"' || v[0] == '\'') {
		return "\x00" + unquote(v)
	}
	return l
}

func normAlign(l string) string {
	l = strings.TrimSpace(l)
	switch l {
	case "start":
		return "start"
	case "flex-start", "self-start", "left":
		return l
	case "-webkit-center":
		return "center"
	}
	if strings.HasPrefix(l, "safe ") || strings.HasPrefix(l, "unsafe ") {
		f := strings.Fields(l)
		if len(f) == 2 {
			return f[0] + " " + f[1]
		}
	}
	if l == "first baseline" {
		return "baseline"
	}
	return l
}

func normDisplay(l string) (string, bool) {
	f := strings.Fields(l)
	if len(f) == 2 {
		// two-value syntax: outside inside
		outside, inside := f[0], f[1]
		if outside == "block" || outside == "inline" || outside == "run-in" {
		} else {
			outside, inside = inside, outside
		}
		switch {
		case inside == "flow" && outside == "block":
			return "block", true
		case inside == "flow" && outside == "inline":
			return "inline", true
		case inside == "flow-root" && outside == "block":
			return "flow-root", true
		case inside == "flow-root" && outside == "inline":
			return "inline-block", true
		case inside == "flex" && outside == "block":
			return "flex", true
		case inside == "flex" && outside == "inline":
			return "inline-flex", true
		case inside == "grid" && outside == "block":
			return "grid", true
		case inside == "grid" && outside == "inline":
			return "inline-grid", true
		case inside == "table" && outside == "block":
			return "table", true
		case inside == "table" && outside == "inline":
			return "inline-table", true
		case inside == "flow" && f[1] == "list-item" || f[0] == "list-item":
			return "list-item", true
		}
		return "", false
	}
	switch l {
	case "block", "inline", "inline-block", "flow-root", "flex", "inline-flex", "grid", "inline-grid", "table", "inline-table",
		"table-row-group", "table-header-group", "table-footer-group", "table-row", "table-cell", "table-column-group", "table-column",
		"table-caption", "list-item", "none", "contents", "ruby", "ruby-text":
		return l, true
	case "-webkit-box":
		return "-webkit-box", true
	case "-webkit-inline-box":
		return "-webkit-inline-box", true
	case "-webkit-flex":
		return "flex", true
	case "-webkit-inline-flex":
		return "inline-flex", true
	case "-ms-flexbox":
		return "flex", true
	case "inline list-item", "list-item inline":
		return "list-item", true
	}
	return "", false
}

func (s *cstate) colorOf(v string, cur Color) (Color, bool) {
	s.cc.cur = cur
	c, _, ok := parseColor(v, s.cc)
	return c, ok
}

func decoLines(l string) uint8 {
	var m uint8
	for _, w := range strings.Fields(l) {
		switch w {
		case "underline":
			m |= DecoUnderline
		case "overline":
			m |= DecoOverline
		case "line-through":
			m |= DecoLineThrough
		}
	}
	return m
}

func (s *cstate) textDecoration(v, l string) bool {
	c := s.c
	c.TextDecorLine, c.TextDecorStyle, c.DecorCur = 0, "solid", true
	c.TextDecorationThickness = Length{}
	for _, w := range fields(v) {
		lw := strings.ToLower(w)
		switch lw {
		case "none":
		case "underline":
			c.TextDecorLine |= DecoUnderline
		case "overline":
			c.TextDecorLine |= DecoOverline
		case "line-through":
			c.TextDecorLine |= DecoLineThrough
		case "blink", "spelling-error", "grammar-error":
		case "solid", "double", "dotted", "dashed", "wavy":
			c.TextDecorStyle = lw
		case "from-font", "auto":
		default:
			if x, cur, ok := s.colorSpec(w); ok {
				c.TextDecorColor, c.DecorCur = x, cur
			} else if x, ok := s.length(lw, false); ok {
				c.TextDecorationThickness = x
			} else {
				return false
			}
		}
	}
	return true
}

func parseCounters(l string, incr bool) ([]CounterOp, bool) {
	if l == "none" {
		return nil, true
	}
	f := strings.Fields(l)
	var out []CounterOp
	for i := 0; i < len(f); i++ {
		op := CounterOp{Name: f[i]}
		if incr {
			op.Val = 1
		}
		if i+1 < len(f) {
			if n, ok := parseInt(f[i+1]); ok {
				op.Val = n
				i++
			}
		}
		out = append(out, op)
	}
	return out, len(out) > 0
}

func quotedStrings(v string) []string {
	var out []string
	for i := 0; i < len(v); i++ {
		if v[i] == '"' || v[i] == '\'' {
			q := v[i]
			j := i + 1
			for j < len(v) && v[j] != q {
				if v[j] == '\\' {
					j++
				}
				j++
			}
			if j > len(v) {
				j = len(v)
			}
			out = append(out, unescapeStr(v[i+1:j]))
			i = j
		}
	}
	return out
}

func parseFamilies(v string) []string {
	var out []string
	for _, p := range splitTop(v, ',') {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if p[0] == '"' || p[0] == '\'' {
			out = append(out, strings.ToLower(unquote(p)))
		} else {
			f := strings.Fields(p)
			out = append(out, strings.ToLower(strings.Join(f, " ")))
		}
	}
	return out
}

func (s *cstate) fontWeight(l string) (int, bool) {
	switch l {
	case "normal":
		return 400, true
	case "bold":
		return 700, true
	case "bolder":
		p := s.p.FontWeight
		switch {
		case p < 350:
			return 400, true
		case p < 550:
			return 700, true
		}
		return 900, true
	case "lighter":
		p := s.p.FontWeight
		switch {
		case p < 550:
			return 100, true
		case p < 750:
			return 400, true
		}
		return 700, true
	}
	n, ok := parseNumber(l)
	if !ok || n < 1 || n > 1000 {
		return 0, false
	}
	return int(n), true
}

func (s *cstate) fontSize(v, l string) bool {
	c := s.c
	if k, ok := s.sizeKeyword(l); ok {
		c.FontSize = k
		c.FontSizeKw = s.kwIndex(l)
		c.FontAbs = false
		s.setupMetrics()
		return true
	}
	switch l {
	case "smaller":
		c.FontSize = smallerSize(s.p.FontSize)
		c.FontSizeKw, c.FontAbs = 0, s.p.FontAbs
		s.setupMetrics()
		return true
	case "larger":
		c.FontSize = largerSize(s.p.FontSize)
		c.FontSizeKw, c.FontAbs = 0, s.p.FontAbs
		s.setupMetrics()
		return true
	}
	save := s.uc.em
	s.uc.em = s.p.FontSize
	metricsSave := s.metricsDone
	if strings.Contains(l, "ex") || strings.Contains(l, "ch") {
		// ex/ch relative to the parent font
		sv := c.FontSize
		c.FontSize = s.p.FontSize
		sf := c.FontFamily
		c.FontFamily = s.p.FontFamily
		s.metricsDone = false
		x, ok := s.length(l, false)
		c.FontSize, c.FontFamily = sv, sf
		s.uc.em = save
		s.metricsDone = false
		if !ok || x.K == LAuto || x.K == LNone || x.IsIntrinsic() {
			return false
		}
		if x.K == LPct {
			x = Px(x.V * s.p.FontSize / 100)
		}
		c.FontSize = x.Resolve(s.p.FontSize)
		c.FontSizeKw, c.FontAbs = 0, s.p.FontAbs
		s.setupMetrics()
		return true
	}
	x, ok := s.length(l, false)
	s.uc.em = save
	s.metricsDone = metricsSave
	if !ok || x.K == LAuto || x.K == LNone || x.IsIntrinsic() || x.K == LNumber {
		return false
	}
	if (x.K == LPx || x.K == LPct) && x.V < 0 {
		return false
	}
	c.FontSize = x.Resolve(s.p.FontSize)
	c.FontSizeKw = 0
	c.FontAbs = !fontRelativeUnit(l)
	if c.FontAbs == false && x.K != LPct && !strings.Contains(l, "%") {
		c.FontAbs = s.p.FontAbs
	} else if x.K == LPct || strings.Contains(l, "%") {
		c.FontAbs = s.p.FontAbs
	}
	s.setupMetrics()
	return true
}

func fontRelativeUnit(l string) bool {
	return strings.Contains(l, "em") || strings.Contains(l, "ex") || strings.Contains(l, "ch") || strings.Contains(l, "%") || strings.Contains(l, "lh") || strings.Contains(l, "cap")
}

func (s *cstate) kwIndex(l string) int8 {
	for i, k := range []string{"xx-small", "x-small", "small", "medium", "large", "x-large", "xx-large", "xxx-large"} {
		if k == l {
			return int8(i + 1)
		}
	}
	return 0
}

// fixMonospace applies Blink's monospace size adjustment once the font group is done.
func (s *cstate) fixMonospace() {
	c, p := s.c, s.p
	if c.FontAbs {
		return
	}
	nm, om := isMonospaceOnly(c.FontFamily), isMonospaceOnly(p.FontFamily)
	if nm == om {
		return
	}
	if c.FontSizeKw > 0 {
		if nm {
			c.FontSize = fixedTable[4][c.FontSizeKw-1]
		} else {
			c.FontSize = keywordSizes[c.FontSizeKw-1]
		}
		return
	}
	if nm {
		c.FontSize = c.FontSize * 13 / 16
	} else {
		c.FontSize = c.FontSize * 16 / 13
	}
}

func smallerSize(p float64) float64 {
	// Chromium: font-size: smaller picks the next smaller keyword size, else /1.2
	return p / 1.2
}

func largerSize(p float64) float64 { return p * 1.2 }

func (s *cstate) lineHeight(v, l string) bool {
	c := s.c
	if l == "normal" {
		c.LineHeight = Length{}
		return true
	}
	if x, ok := parseNumber(l); ok {
		if x < 0 {
			return false
		}
		c.LineHeight = Length{K: LNumber, V: x}
		return true
	}
	x, ok := s.length(l, false)
	if !ok || x.K == LAuto || x.K == LNone || x.IsIntrinsic() {
		return false
	}
	if x.K == LPx && x.V < 0 {
		return false
	}
	if x.K == LPct {
		c.LineHeight = Px(x.V * c.FontSize / 100)
	} else {
		c.LineHeight = Px(x.Resolve(c.FontSize))
	}
	return true
}

func (s *cstate) fontShorthand(v, l string) bool {
	c := s.c
	switch l {
	case "caption", "icon", "menu", "message-box", "small-caption", "status-bar", "-webkit-small-control", "-webkit-mini-control", "-webkit-control":
		c.FontStyle, c.FontWeight, c.SmallCaps = 0, 400, false
		c.LineHeight = Length{}
		c.FontFamily = []string{s.eng.systemFont()}
		switch l {
		case "-webkit-mini-control":
			c.FontSize = 9
		default:
			c.FontSize = 13.333333
		}
		s.setupMetrics()
		return true
	}
	toks := fields(v)
	if len(toks) < 2 {
		return false
	}
	style, weight, smallcaps := uint8(0), 400, false
	i := 0
	for ; i < len(toks); i++ {
		t := strings.ToLower(toks[i])
		switch t {
		case "normal":
			continue
		case "italic":
			style = 1
			continue
		case "oblique":
			style = 2
			if i+1 < len(toks) {
				if _, ok := s.uc.parseAngleTok(toks[i+1]); ok {
					i++
				}
			}
			continue
		case "small-caps":
			smallcaps = true
			continue
		case "bold", "bolder", "lighter":
			w, _ := s.fontWeight(t)
			weight = w
			continue
		case "ultra-condensed", "extra-condensed", "condensed", "semi-condensed", "semi-expanded", "expanded", "extra-expanded", "ultra-expanded":
			continue
		}
		if n, ok := parseNumber(t); ok && n >= 1 && n <= 1000 && i+1 < len(toks) && toks[i+1] != "/" {
			weight = int(n)
			continue
		}
		break
	}
	if i >= len(toks) {
		return false
	}
	// size[/line-height]
	sizeTok := toks[i]
	lh := ""
	i++
	if i < len(toks) && toks[i] == "/" && i+1 < len(toks) {
		lh = toks[i+1]
		i += 2
	} else if k := strings.IndexByte(sizeTok, '/'); k > 0 {
		lh = sizeTok[k+1:]
		sizeTok = sizeTok[:k]
	}
	if i >= len(toks) {
		return false
	}
	fam := s.eng.scopeFamilies(parseFamilies(strings.Join(toks[i:], " ")))
	if len(fam) == 0 {
		return false
	}
	// reset subproperties
	c.FontStyle, c.FontWeight, c.SmallCaps = style, weight, smallcaps
	c.LineHeight = Length{}
	c.FontFamily = fam
	if !s.fontSize(sizeTok, strings.ToLower(sizeTok)) {
		return false
	}
	if lh != "" {
		if !s.lineHeight(lh, strings.ToLower(lh)) {
			return false
		}
	}
	return true
}

func (u *unitCtx) parseAngleTok(s string) (float64, bool) { return parseAngle(s) }

func (s *cstate) borderShorthand(v, l string, mask [4]bool) bool {
	c := s.c
	w, st, col := 3.0, "none", Color{}
	colSet := false
	for _, t := range fields(v) {
		lt := strings.ToLower(t)
		if isBorderStyle(lt) {
			st = lt
		} else if x, ok := s.borderWidthVal(lt); ok {
			w = x
		} else if x, _, ok := s.colorSpec(t); ok {
			col, colSet = x, true
		} else if lt == "initial" {
		} else {
			return false
		}
	}
	for i := 0; i < 4; i++ {
		if !mask[i] {
			continue
		}
		c.BorderWidth[i], c.BorderStyle[i] = w, st
		c.BorderColor[i], c.bcSet[i] = col, colSet
	}
	if mask == [4]bool{true, true, true, true} {
		c.BorderImage = false
	}
	return true
}

func (s *cstate) radiusShorthand(v string) bool {
	parts := strings.Split(v, "/")
	if len(parts) > 2 {
		return false
	}
	parse := func(p string) ([]Length, bool) {
		f := fields(p)
		if len(f) == 0 || len(f) > 4 {
			return nil, false
		}
		var o []Length
		for _, w := range f {
			x, ok := s.nonNegLen(w)
			if !ok {
				return nil, false
			}
			o = append(o, x)
		}
		return o, true
	}
	h, ok := parse(parts[0])
	if !ok {
		return false
	}
	vv := h
	if len(parts) == 2 {
		if vv, ok = parse(parts[1]); !ok {
			return false
		}
	}
	he, ve := expand4(h), expand4(vv)
	for i := 0; i < 4; i++ {
		s.c.Radius[i] = [2]Length{he[i], ve[i]}
	}
	return true
}

func (s *cstate) listStyleShorthand(v, l string) bool {
	c := s.c
	c.ListStyleType, c.ListStylePos, c.ListStyleImage = "disc", "outside", Image{}
	for _, t := range fields(v) {
		lt := strings.ToLower(t)
		switch {
		case lt == "inside" || lt == "outside":
			c.ListStylePos = lt
		case lt == "none":
			c.ListStyleType = "none"
		case strings.HasPrefix(lt, "url(") || strings.Contains(lt, "gradient("):
			img, ok := s.imageValue(t)
			if !ok {
				return false
			}
			c.ListStyleImage = img
		default:
			c.ListStyleType = unquoteKeep(t, lt)
		}
	}
	return true
}

func (s *cstate) flexShorthand(v, l string) bool {
	c := s.c
	switch l {
	case "none":
		c.FlexGrow, c.FlexShrink, c.FlexBasis = 0, 0, Length{}
		return true
	case "auto":
		c.FlexGrow, c.FlexShrink, c.FlexBasis = 1, 1, Length{}
		return true
	case "initial":
		c.FlexGrow, c.FlexShrink, c.FlexBasis = 0, 1, Length{}
		return true
	}
	f := fields(l)
	if len(f) == 0 || len(f) > 3 {
		return false
	}
	grow, shrink := 1.0, 1.0
	basis := Pct(0)
	nums := 0
	for _, w := range f {
		if n, ok := parseNumber(w); ok && n >= 0 {
			if nums == 0 {
				grow = n
			} else if nums == 1 {
				shrink = n
			} else {
				return false
			}
			nums++
			continue
		}
		x, ok := s.nonNegLen(w)
		if !ok {
			return false
		}
		basis = x
	}
	if nums == 1 && len(f) == 1 {
		shrink = 1
	}
	c.FlexGrow, c.FlexShrink, c.FlexBasis = grow, shrink, basis
	return true
}

func (s *cstate) inlineStart() int {
	if s.c.Direction == "rtl" {
		return 1
	}
	return 3
}

func (s *cstate) inlineEnd() int {
	if s.c.Direction == "rtl" {
		return 3
	}
	return 1
}

// normBreak maps break-* keywords onto auto, avoid or a forced break.
func normBreak(l string) string {
	switch l {
	case "avoid", "avoid-page", "avoid-column", "avoid-region":
		return "avoid"
	case "column", "page", "always", "left", "right", "recto", "verso", "region":
		return "column"
	}
	return "auto"
}
