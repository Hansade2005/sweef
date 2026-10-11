package raster

import (
	"math"
	"strconv"
	"strings"

	"github.com/sweefbrowser/sweef/internal/render"
)

// SVG painting: the render package resolves the page cascade into a scene
// (render.SVGDoc); this file interprets it onto a Context.

type svgNode = render.SVGNode

// ---- style -------------------------------------------------------------------------

const (
	pNone = iota
	pColor
	pURL
)

type sPaint struct {
	kind int
	col  Color
	ref  string
	fb   *sPaint
	cur  bool
}

type sStyle struct {
	color              Color
	fill, stroke       sPaint
	fillOp, strokeOp   float64
	sw                 float64
	cap                LineCap
	join               LineJoin
	miter              float64
	dash               []string
	dashOff            string
	fillRule, clipRule FillRule
	fontSize           float64
	family             string
	weight             int
	italic             bool
	anchor             string
	baseline           string // dominant-baseline (inherited)
	alignBase, shift   string // alignment-baseline and baseline-shift (not inherited)
	letterSp, wordSp   string
	visible            bool
	mStart, mMid, mEnd string
	paintOrder         string
	vecEff             string
	cif                string // color-interpolation-filters
	imageRendering     string
	shapeRendering     string
	decoration         string
	parentFS           float64
}

type svgR struct {
	c      *Context
	doc    *render.SVGDoc
	root   sStyle
	vw, vh float64
	depth  int
	dpr    float64
	active map[*svgNode]bool
	draw   render.SVGDraw
	nodes  int
}

func (r *svgR) sub(c *Context) *svgR {
	n := *r
	n.c = c
	return &n
}

func svgNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// splitNums splits a number list ("1,2 3-4") into numbers (units ignored).
func splitNums(s string) []float64 {
	var out []float64
	i := 0
	for i < len(s) {
		for i < len(s) && (s[i] == ' ' || s[i] == ',' || s[i] == '\n' || s[i] == '\t' || s[i] == '\r') {
			i++
		}
		j := i
		if j < len(s) && (s[j] == '-' || s[j] == '+') {
			j++
		}
		dot := false
		for j < len(s) {
			ch := s[j]
			if ch >= '0' && ch <= '9' {
				j++
			} else if ch == '.' && !dot {
				dot = true
				j++
			} else if (ch == 'e' || ch == 'E') && j+1 < len(s) && (s[j+1] == '-' || s[j+1] == '+' || s[j+1] >= '0' && s[j+1] <= '9') {
				j += 2
			} else {
				break
			}
		}
		if j == i {
			i++
			continue
		}
		v, _ := strconv.ParseFloat(s[i:j], 64)
		out = append(out, v)
		i = j
		for i < len(s) && s[i] != ' ' && s[i] != ',' && s[i] != '-' && s[i] != '+' && s[i] != '.' && !(s[i] >= '0' && s[i] <= '9') {
			i++ // unit suffix
		}
	}
	return out
}

// length resolves an SVG length against ref (for %) and the font size.
func (r *svgR) length(s string, ref, fs, dflt float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return dflt
	}
	units := []struct {
		u string
		k float64
	}{{"px", 1}, {"pt", 96.0 / 72}, {"pc", 16}, {"mm", 96 / 25.4}, {"cm", 96 / 2.54}, {"in", 96}, {"rem", 16}, {"em", fs}, {"ex", fs / 2}, {"q", 96 / 101.6}}
	if strings.HasSuffix(s, "%") {
		if v, ok := svgNumber(s[:len(s)-1]); ok {
			return v / 100 * ref
		}
		return dflt
	}
	ls := strings.ToLower(s)
	for _, u := range units {
		if strings.HasSuffix(ls, u.u) {
			if v, ok := svgNumber(ls[:len(ls)-len(u.u)]); ok {
				return v * u.k
			}
			return dflt
		}
	}
	if strings.HasPrefix(ls, "calc(") {
		return dflt
	}
	if v, ok := svgNumber(s); ok {
		return v
	}
	return dflt
}

func (r *svgR) attrLen(n *svgNode, key string, axis byte, st *sStyle, dflt float64) float64 {
	v, ok := n.A[key]
	if !ok {
		return dflt
	}
	return r.length(v, r.refFor(axis), st.fontSize, dflt)
}

func (r *svgR) refFor(axis byte) float64 {
	switch axis {
	case 'x':
		return r.vw
	case 'y':
		return r.vh
	}
	return math.Sqrt((r.vw*r.vw + r.vh*r.vh) / 2)
}

func parsePaint(s string, cur Color) sPaint {
	s = strings.TrimSpace(s)
	ls := strings.ToLower(s)
	switch {
	case ls == "" || ls == "none":
		return sPaint{kind: pNone}
	case ls == "currentcolor":
		return sPaint{kind: pColor, col: cur, cur: true}
	case strings.HasPrefix(ls, "url("):
		j := strings.IndexByte(s, ')')
		if j < 0 {
			return sPaint{kind: pNone}
		}
		ref := strings.Trim(strings.TrimSpace(s[4:j]), "'\"")
		ref = strings.TrimPrefix(ref, "#")
		p := sPaint{kind: pURL, ref: ref}
		if rest := strings.TrimSpace(s[j+1:]); rest != "" {
			fb := parsePaint(rest, cur)
			p.fb = &fb
		}
		return p
	}
	if c, ok := ParseColor(s, cur); ok {
		return sPaint{kind: pColor, col: c}
	}
	return sPaint{kind: pNone}
}

func parseOpacity(s string, dflt float64) float64 {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "%") {
		if v, ok := svgNumber(s[:len(s)-1]); ok {
			return clamp01(v / 100)
		}
		return dflt
	}
	if v, ok := svgNumber(s); ok {
		return clamp01(v)
	}
	return dflt
}

func baseStyle(o render.SVGDraw) sStyle {
	fs := o.FontSize
	if fs <= 0 {
		fs = 16
	}
	w := o.Weight
	if w == 0 {
		w = 400
	}
	fam := o.Family
	if fam == "" {
		fam = "serif"
	}
	return sStyle{
		color: RGBA8(o.Color.R, o.Color.G, o.Color.B, o.Color.A),
		fill:  sPaint{kind: pColor, col: RGBA8(0, 0, 0, 255)}, fillOp: 1, strokeOp: 1, sw: 1, miter: 4,
		fontSize: fs, family: fam, weight: w, italic: o.Italic, anchor: "start", visible: true, cif: "linearrgb",
		parentFS: fs,
	}
}

// apply overlays n's properties on a copy of the parent style.
func (r *svgR) inherit(par sStyle, a map[string]string) sStyle {
	st := par
	st.parentFS = par.fontSize
	get := func(k string) (string, bool) {
		v, ok := a[k]
		if !ok {
			return "", false
		}
		v = strings.TrimSpace(v)
		if v == "inherit" || v == "unset" || v == "revert" || v == "initial" && false {
			return "", false
		}
		return v, true
	}
	if v, ok := get("color"); ok {
		if strings.EqualFold(v, "currentcolor") {
		} else if c, ok := ParseColor(v, par.color); ok {
			st.color = c
		}
	}
	if v, ok := get("font-size"); ok {
		st.fontSize = r.fontSizeOf(v, par.fontSize)
	}
	if v, ok := get("font-family"); ok {
		st.family = v
	}
	if v, ok := get("font-weight"); ok {
		switch v {
		case "normal":
			st.weight = 400
		case "bold":
			st.weight = 700
		case "bolder":
			st.weight = 700
		case "lighter":
			st.weight = 300
		default:
			if f, ok := svgNumber(v); ok {
				st.weight = int(f)
			}
		}
	}
	if v, ok := get("font-style"); ok {
		st.italic = v == "italic" || strings.HasPrefix(v, "oblique")
	}
	// paints: currentColor must follow the final color, so resolve after color.
	if v, ok := get("fill"); ok {
		st.fill = parsePaint(v, st.color)
	} else if st.fill.cur {
		st.fill.col = st.color
	}
	if v, ok := get("stroke"); ok {
		st.stroke = parsePaint(v, st.color)
	} else if st.stroke.cur {
		st.stroke.col = st.color
	}
	if v, ok := get("fill-opacity"); ok {
		st.fillOp = parseOpacity(v, st.fillOp)
	}
	if v, ok := get("stroke-opacity"); ok {
		st.strokeOp = parseOpacity(v, st.strokeOp)
	}
	if v, ok := get("stroke-width"); ok {
		st.sw = r.length(v, r.refFor('d'), st.fontSize, st.sw)
	}
	if v, ok := get("stroke-linecap"); ok {
		switch v {
		case "round":
			st.cap = CapRound
		case "square":
			st.cap = CapSquare
		default:
			st.cap = CapButt
		}
	}
	if v, ok := get("stroke-linejoin"); ok {
		switch v {
		case "round":
			st.join = JoinRound
		case "bevel":
			st.join = JoinBevel
		default:
			st.join = JoinMiter
		}
	}
	if v, ok := get("stroke-miterlimit"); ok {
		if f, ok := svgNumber(v); ok && f >= 1 {
			st.miter = f
		}
	}
	if v, ok := get("stroke-dasharray"); ok {
		if v == "none" {
			st.dash = nil
		} else {
			st.dash = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
		}
	}
	if v, ok := get("stroke-dashoffset"); ok {
		st.dashOff = v
	}
	if v, ok := get("fill-rule"); ok {
		st.fillRule = ruleOf(v)
	}
	if v, ok := get("clip-rule"); ok {
		st.clipRule = ruleOf(v)
	}
	if v, ok := get("text-anchor"); ok {
		st.anchor = v
	}
	if v, ok := get("dominant-baseline"); ok {
		st.baseline = v
	}
	st.alignBase, st.shift = "", ""
	if v, ok := get("alignment-baseline"); ok {
		st.alignBase = v
	}
	if v, ok := get("baseline-shift"); ok {
		st.shift = v
	}
	if v, ok := get("letter-spacing"); ok {
		st.letterSp = v
	}
	if v, ok := get("word-spacing"); ok {
		st.wordSp = v
	}
	if v, ok := get("visibility"); ok {
		st.visible = v == "visible"
	}
	if v, ok := get("marker-start"); ok {
		st.mStart = v
	}
	if v, ok := get("marker-mid"); ok {
		st.mMid = v
	}
	if v, ok := get("marker-end"); ok {
		st.mEnd = v
	}
	if v, ok := get("paint-order"); ok {
		st.paintOrder = v
	}
	if v, ok := get("vector-effect"); ok {
		st.vecEff = v
	}
	if v, ok := get("color-interpolation-filters"); ok {
		st.cif = strings.ToLower(v)
	}
	if v, ok := get("image-rendering"); ok {
		st.imageRendering = v
	}
	if v, ok := get("shape-rendering"); ok {
		st.shapeRendering = v
	}
	if v, ok := get("text-decoration"); ok {
		st.decoration = v
	}
	return st
}

func ruleOf(v string) FillRule {
	if v == "evenodd" {
		return EvenOdd
	}
	return NonZero
}

func (r *svgR) fontSizeOf(v string, parent float64) float64 {
	switch v {
	case "xx-small":
		return 9
	case "x-small":
		return 10
	case "small":
		return 13
	case "medium":
		return 16
	case "large":
		return 18
	case "x-large":
		return 24
	case "xx-large":
		return 32
	case "larger":
		return parent * 1.2
	case "smaller":
		return parent / 1.2
	}
	return r.length(v, parent, parent, parent)
}

// ---- transforms --------------------------------------------------------------------

func svgTransform(s string) Matrix {
	m := Identity
	s = strings.TrimSpace(s)
	for len(s) > 0 {
		i := strings.IndexByte(s, '(')
		j := strings.IndexByte(s, ')')
		if i < 0 || j < i {
			break
		}
		name := strings.ToLower(strings.TrimSpace(strings.Trim(s[:i], ", \t\n")))
		body := s[i+1 : j]
		s = strings.TrimLeft(s[j+1:], ", \t\n")
		parts := strings.FieldsFunc(body, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
		val := func(k int, d float64, angle bool) float64 {
			if k >= len(parts) {
				return d
			}
			p := strings.ToLower(parts[k])
			mult := 1.0
			switch {
			case strings.HasSuffix(p, "deg"):
				p = p[:len(p)-3]
			case strings.HasSuffix(p, "grad"):
				p, mult = p[:len(p)-4], 0.9
			case strings.HasSuffix(p, "rad"):
				p, mult = p[:len(p)-3], 180/math.Pi
			case strings.HasSuffix(p, "turn"):
				p, mult = p[:len(p)-4], 360
			case strings.HasSuffix(p, "px"):
				p = p[:len(p)-2]
			}
			v, ok := svgNumber(p)
			if !ok {
				return d
			}
			_ = angle
			return v * mult
		}
		t := Identity
		switch name {
		case "translate":
			t[4], t[5] = val(0, 0, false), val(1, 0, false)
		case "translatex":
			t[4] = val(0, 0, false)
		case "translatey":
			t[5] = val(0, 0, false)
		case "scale":
			t[0] = val(0, 1, false)
			t[3] = val(1, t[0], false)
		case "scalex":
			t[0] = val(0, 1, false)
		case "scaley":
			t[3] = val(0, 1, false)
		case "rotate":
			a := val(0, 0, true) * math.Pi / 180
			cs, sn := math.Cos(a), math.Sin(a)
			t = Matrix{cs, sn, -sn, cs, 0, 0}
			if len(parts) >= 3 {
				cx, cy := val(1, 0, false), val(2, 0, false)
				t = Translate(cx, cy).Mul(t).Mul(Translate(-cx, -cy))
			}
		case "skewx":
			t[2] = math.Tan(val(0, 0, true) * math.Pi / 180)
		case "skewy":
			t[1] = math.Tan(val(0, 0, true) * math.Pi / 180)
		case "skew":
			t[2] = math.Tan(val(0, 0, true) * math.Pi / 180)
			t[1] = math.Tan(val(1, 0, true) * math.Pi / 180)
		case "matrix":
			t = Matrix{val(0, 1, false), val(1, 0, false), val(2, 0, false), val(3, 1, false), val(4, 0, false), val(5, 0, false)}
		default:
			continue
		}
		m = m.Mul(t)
	}
	return m
}

// viewBoxMatrix maps the viewBox onto a w x h viewport at the origin.
func viewBoxMatrix(vb []float64, par string, w, h float64) (Matrix, bool) {
	if len(vb) != 4 || vb[2] <= 0 || vb[3] <= 0 || w <= 0 || h <= 0 {
		return Identity, false
	}
	sx, sy := w/vb[2], h/vb[3]
	f := strings.Fields(par)
	if len(f) > 0 && f[0] == "defer" {
		f = f[1:]
	}
	if len(f) > 0 && f[0] == "none" {
		return Matrix{sx, 0, 0, sy, -vb[0] * sx, -vb[1] * sy}, true
	}
	s := math.Min(sx, sy)
	if len(f) > 1 && f[1] == "slice" {
		s = math.Max(sx, sy)
	}
	ox, oy := (w-vb[2]*s)/2, (h-vb[3]*s)/2
	if len(f) > 0 {
		switch {
		case strings.HasPrefix(f[0], "xMin"):
			ox = 0
		case strings.HasPrefix(f[0], "xMax"):
			ox = w - vb[2]*s
		}
		switch {
		case strings.HasSuffix(f[0], "YMin"):
			oy = 0
		case strings.HasSuffix(f[0], "YMax"):
			oy = h - vb[3]*s
		}
	}
	return Matrix{s, 0, 0, s, ox - vb[0]*s, oy - vb[1]*s}, true
}

// ---- entry point ---------------------------------------------------------------------

func (p *Painter) svgDraw(o render.SVGDraw) {
	svgDrawOn(p.c, o)
}

// RenderSVG rasterizes an SVG scene into a new w x h surface (device pixels),
// mapping the root viewBox onto the whole surface. It is the entry point for
// canvas drawImage() of SVG images.
func RenderSVG(doc *render.SVGDoc, w, h int, color render.Color) *Surface {
	s := NewSurface(w, h)
	if doc == nil || w <= 0 || h <= 0 {
		return s
	}
	c := NewContextSurface(s)
	c.SetSmoothing(true, QualityLow)
	svgDrawOn(c, render.SVGDraw{Dst: render.Rect{W: float64(w), H: float64(h)}, Doc: doc, Color: color, FontSize: 16, Family: "serif", Weight: 400})
	return s
}

func svgDrawOn(c *Context, o render.SVGDraw) {
	if o.Doc == nil || o.Doc.Root == nil || o.Dst.W <= 0 || o.Dst.H <= 0 {
		return
	}
	depth, ld := c.SaveDepth(), c.LayerDepth()
	c.Save()
	defer func() {
		// a malformed scene must never take the page down
		if e := recover(); e != nil {
			for c.LayerDepth() > ld {
				c.EndLayer(1, SourceOver)
			}
			for c.SaveDepth() > depth+1 {
				c.Restore()
			}
		}
		c.Restore()
	}()
	r := &svgR{c: c, doc: o.Doc, draw: o, active: map[*svgNode]bool{}, dpr: c.Transform().MaxScale()}
	r.root = baseStyle(o)
	r.root = r.inherit(r.root, o.Doc.Inherit)
	root := o.Doc.Root
	vb := splitNums(strings.ReplaceAll(root.A["viewbox"], ",", " "))
	w, h := o.Dst.W, o.Dst.H
	if len(vb) != 4 && o.Doc.IntrW > 0 && o.Doc.IntrH > 0 {
		vb = []float64{0, 0, o.Doc.IntrW, o.Doc.IntrH}
	}
	m := c.Transform().Mul(Translate(o.Dst.X, o.Dst.Y))
	r.vw, r.vh = w, h
	if vm, ok := viewBoxMatrix(vb, root.A["preserveaspectratio"], w, h); ok {
		m = m.Mul(vm)
		r.vw, r.vh = vb[2], vb[3]
	}
	st := r.inherit(r.root, root.A)
	r.drawNode(root, st, m, true)
}

// ---- node drawing --------------------------------------------------------------------

func isContainer(tag string) bool {
	switch tag {
	case "g", "a", "svg", "switch", "symbol":
		return true
	}
	return false
}

func nonRendered(tag string) bool {
	switch tag {
	case "defs", "clippath", "mask", "marker", "pattern", "lineargradient", "radialgradient", "filter", "style", "title", "desc",
		"metadata", "script", "symbol", "stop", "fegaussianblur", "feoffset", "fedropshadow", "fecolormatrix", "feblend", "femerge",
		"feflood", "fecomposite", "femergenode", "fecomponenttransfer", "feimage", "feturbulence", "femorphology", "foreignobject":
		return true
	}
	return false
}

func (r *svgR) nodeTransform(n *svgNode, st *sStyle) Matrix {
	t, ok := n.A["transform"]
	if !ok || strings.TrimSpace(t) == "" || t == "none" {
		return Identity
	}
	m := svgTransform(t)
	if o, ok := n.A["transform-origin"]; ok && o != "" {
		ox, oy := r.transformOrigin(n, st, o)
		return Translate(ox, oy).Mul(m).Mul(Translate(-ox, -oy))
	}
	return m
}

func (r *svgR) transformOrigin(n *svgNode, st *sStyle, o string) (float64, float64) {
	f := strings.Fields(o)
	for len(f) < 2 {
		f = append(f, "50%")
	}
	var bx, by, bw, bh float64
	if n.A["transform-box"] == "fill-box" || n.A["transform-box"] == "stroke-box" {
		if b, ok := r.bbox(n, st); ok {
			bx, by, bw, bh = b.X, b.Y, b.W, b.H
		}
	} else {
		bw, bh = r.vw, r.vh
	}
	conv := func(s string, org, size float64, horiz bool) float64 {
		switch s {
		case "left", "top":
			return org
		case "right", "bottom":
			return org + size
		case "center":
			return org + size/2
		}
		return org + r.length(s, size, st.fontSize, 0)
	}
	if f[0] == "top" || f[0] == "bottom" {
		f[0], f[1] = f[1], f[0]
	}
	return conv(f[0], bx, bw, true), conv(f[1], by, bh, false)
}

func (r *svgR) drawChildren(n *svgNode, st sStyle, m Matrix) {
	if n.Tag == "switch" {
		for _, k := range n.Kids {
			if k.Tag == "#text" || nonRendered(k.Tag) {
				continue
			}
			if tl, ok := k.A["systemlanguage"]; ok && !strings.HasPrefix(strings.ToLower(tl), "en") {
				continue
			}
			if _, ok := k.A["requiredextensions"]; ok {
				continue
			}
			r.drawNode(k, r.inherit(st, k.A), m, false)
			return
		}
		return
	}
	for _, k := range n.Kids {
		if k.Tag == "#text" {
			continue
		}
		r.drawNode(k, r.inherit(st, k.A), m, false)
	}
}

func (r *svgR) hasEffects(n *svgNode) (opacity float64, clip, mask, filter, blend string) {
	opacity = 1
	if v, ok := n.A["opacity"]; ok {
		opacity = parseOpacity(v, 1)
	}
	clip, mask, filter = strings.TrimSpace(n.A["clip-path"]), strings.TrimSpace(n.A["mask"]), strings.TrimSpace(n.A["filter"])
	blend = n.A["mix-blend-mode"]
	if clip == "none" {
		clip = ""
	}
	if mask == "none" {
		mask = ""
	}
	if filter == "none" {
		filter = ""
	}
	if blend == "normal" {
		blend = ""
	}
	return
}

func urlRef(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(strings.ToLower(s), "url(") {
		return "", false
	}
	j := strings.IndexByte(s, ')')
	if j < 0 {
		return "", false
	}
	return strings.TrimPrefix(strings.Trim(strings.TrimSpace(s[4:j]), "'\""), "#"), true
}

// drawNode paints one element (st is already inherited for n).
func (r *svgR) drawNode(n *svgNode, st sStyle, m Matrix, isRoot bool) {
	if nonRendered(n.Tag) && !(isRoot && n.Tag == "svg") {
		return
	}
	if v := n.A["display"]; v == "none" {
		return
	}
	if r.depth > 40 {
		return
	}
	r.nodes++
	if r.nodes > 200000 {
		return
	}
	r.depth++
	defer func() { r.depth-- }()
	c := r.c
	c.Save()
	defer c.Restore()
	if !isRoot {
		m = m.Mul(r.nodeTransform(n, &st))
	} else if t := n.A["transform"]; t != "" {
		m = m.Mul(r.nodeTransform(n, &st))
	}
	c.SetTransform(m)
	opacity, clipS, maskS, filterS, blend := r.hasEffects(n)
	single := false
	switch n.Tag {
	case "rect", "circle", "ellipse", "line", "polyline", "polygon", "path":
		// opacity composes without a group when only one of fill/stroke is painted
		single = !(st.fill.kind != pNone && st.stroke.kind != pNone) || opacity >= 1
		if st.fill.kind != pNone && st.stroke.kind != pNone && st.fillOp > 0 && st.strokeOp > 0 {
			single = opacity >= 1
		}
	}
	needLayer := filterS != "" || maskS != "" || blend != "" || (opacity < 1 && !single)
	if opacity < 1 && single && !needLayer {
		st.fillOp *= opacity
		st.strokeOp *= opacity
		opacity = 1
	}
	if n.Tag == "image" && opacity < 1 && !needLayer {
		c.SetGlobalAlpha(opacity)
		opacity = 1
	}
	if clipS != "" {
		if id, ok := urlRef(clipS); ok {
			if cn := r.doc.ByID[id]; cn != nil && cn.Tag == "clippath" {
				if !r.applyClip(cn, n, &st, m) {
					return
				}
			}
		}
	}
	if !needLayer {
		r.drawContent(n, st, m, isRoot)
		return
	}
	// group: compute device bounds
	bounds := c.ClipBounds()
	var filt *svgFilterSpec
	if filterS != "" {
		filt = r.prepareFilter(n, &st, filterS, m)
		if filt != nil && !filt.region.Empty() {
			bounds = filt.region
		} else if filt == nil && strings.HasPrefix(strings.ToLower(filterS), "url(") {
			// reference to a missing filter: the element is not rendered
			return
		}
	}
	var cssFilters []Filter
	if filterS != "" && filt == nil {
		if fl, ok := ParseFilterList(filterS, st.color); ok {
			sc := m.MaxScale()
			for i := range fl {
				switch fl[i].Name {
				case "blur":
					fl[i].Args[0] *= sc
				case "drop-shadow":
					for k := range fl[i].Args {
						fl[i].Args[k] *= sc
					}
				}
			}
			cssFilters = fl
		}
	}
	op := SourceOver
	if blend != "" {
		if o, ok := ParseOp(blend); ok {
			op = o
		}
	}
	c.BeginLayer(bounds, cssFilters)
	r.drawContent(n, st, m, isRoot)
	if filt != nil {
		r.runFilter(filt, n, &st, m)
	}
	if maskS != "" {
		if id, ok := urlRef(maskS); ok {
			if mn := r.doc.ByID[id]; mn != nil && mn.Tag == "mask" {
				r.applyMask(mn, n, &st, m)
			}
		}
	}
	c.EndLayer(opacity, op)
}

func (r *svgR) drawContent(n *svgNode, st sStyle, m Matrix, isRoot bool) {
	switch n.Tag {
	case "svg":
		r.drawSVGViewport(n, st, m, isRoot)
	case "g", "a", "switch":
		r.drawChildren(n, st, m)
	case "use":
		r.drawUse(n, st, m)
	case "image":
		r.drawImage(n, st, m)
	case "text":
		r.drawText(n, st, m)
	case "rect", "circle", "ellipse", "line", "polyline", "polygon", "path":
		r.drawShape(n, st, m)
	}
}

func (r *svgR) drawSVGViewport(n *svgNode, st sStyle, m Matrix, isRoot bool) {
	if isRoot {
		r.drawChildren(n, st, m)
		return
	}
	x, y := r.attrLen(n, "x", 'x', &st, 0), r.attrLen(n, "y", 'y', &st, 0)
	w, h := r.attrLen(n, "width", 'x', &st, r.vw), r.attrLen(n, "height", 'y', &st, r.vh)
	r.drawViewport(n, st, m, x, y, w, h)
}

// drawViewport draws n's children in a viewport (nested svg, symbol via use).
func (r *svgR) drawViewport(n *svgNode, st sStyle, m Matrix, x, y, w, h float64) {
	if w <= 0 || h <= 0 {
		return
	}
	c := r.c
	c.Save()
	defer c.Restore()
	ov := n.A["overflow"]
	if ov != "visible" && ov != "auto" {
		c.SetTransform(m)
		c.ClipRect(x, y, w, h)
	}
	vb := splitNums(strings.ReplaceAll(n.A["viewbox"], ",", " "))
	ovw, ovh := r.vw, r.vh
	mm := m.Mul(Translate(x, y))
	r.vw, r.vh = w, h
	if vm, ok := viewBoxMatrix(vb, n.A["preserveaspectratio"], w, h); ok {
		mm = mm.Mul(vm)
		r.vw, r.vh = vb[2], vb[3]
	}
	r.drawChildren(n, st, mm)
	r.vw, r.vh = ovw, ovh
}

func (r *svgR) drawUse(n *svgNode, st sStyle, m Matrix) {
	id := strings.TrimPrefix(strings.TrimSpace(n.A["href"]), "#")
	t := r.doc.ByID[id]
	if t == nil || r.active[t] || r.active[n] {
		return
	}
	r.active[n] = true
	defer delete(r.active, n)
	x, y := r.attrLen(n, "x", 'x', &st, 0), r.attrLen(n, "y", 'y', &st, 0)
	m = m.Mul(Translate(x, y))
	r.active[t] = true
	defer delete(r.active, t)
	tst := r.inherit(st, t.A)
	switch t.Tag {
	case "symbol", "svg":
		w, h := r.vw, r.vh
		if v, ok := n.A["width"]; ok {
			w = r.length(v, r.vw, st.fontSize, w)
		} else if v, ok := t.A["width"]; ok && t.Tag == "svg" {
			w = r.length(v, r.vw, st.fontSize, w)
		}
		if v, ok := n.A["height"]; ok {
			h = r.length(v, r.vh, st.fontSize, h)
		} else if v, ok := t.A["height"]; ok && t.Tag == "svg" {
			h = r.length(v, r.vh, st.fontSize, h)
		}
		if t.A["display"] == "none" {
			return
		}
		// symbol: apply opacity/effects of the symbol element are ignored
		r.c.Save()
		r.c.SetTransform(m)
		r.drawViewport(t, tst, m, 0, 0, w, h)
		r.c.Restore()
	default:
		r.drawNode(t, tst, m, false)
	}
}

// ---- shapes -----------------------------------------------------------------------------

func (r *svgR) shapePath(n *svgNode, st *sStyle) *Path {
	p := NewPath()
	switch n.Tag {
	case "rect":
		x, y := r.attrLen(n, "x", 'x', st, 0), r.attrLen(n, "y", 'y', st, 0)
		w, h := r.attrLen(n, "width", 'x', st, 0), r.attrLen(n, "height", 'y', st, 0)
		if w <= 0 || h <= 0 {
			return nil
		}
		rx, okx := r.optLen(n, "rx", 'x', st)
		ry, oky := r.optLen(n, "ry", 'y', st)
		if okx && !oky {
			ry = rx
		} else if oky && !okx {
			rx = ry
		}
		rx, ry = math.Min(math.Max(rx, 0), w/2), math.Min(math.Max(ry, 0), h/2)
		if rx > 0 && ry > 0 {
			p.RoundRect(Rect{x, y, w, h}, [4][2]float64{{rx, ry}, {rx, ry}, {rx, ry}, {rx, ry}})
		} else {
			p.Rect(x, y, w, h)
		}
	case "circle":
		cx, cy, rad := r.attrLen(n, "cx", 'x', st, 0), r.attrLen(n, "cy", 'y', st, 0), r.attrLen(n, "r", 'd', st, 0)
		if rad <= 0 {
			return nil
		}
		p.Ellipse(cx, cy, rad, rad)
	case "ellipse":
		cx, cy := r.attrLen(n, "cx", 'x', st, 0), r.attrLen(n, "cy", 'y', st, 0)
		rx, okx := r.optLen(n, "rx", 'x', st)
		ry, oky := r.optLen(n, "ry", 'y', st)
		if okx && !oky {
			ry = rx
		} else if oky && !okx {
			rx = ry
		}
		if rx <= 0 || ry <= 0 {
			return nil
		}
		p.Ellipse(cx, cy, rx, ry)
	case "line":
		p.MoveTo(r.attrLen(n, "x1", 'x', st, 0), r.attrLen(n, "y1", 'y', st, 0))
		p.LineTo(r.attrLen(n, "x2", 'x', st, 0), r.attrLen(n, "y2", 'y', st, 0))
	case "polyline", "polygon":
		v := splitNums(n.A["points"])
		if len(v) < 4 {
			return nil
		}
		for i := 0; i+1 < len(v); i += 2 {
			if i == 0 {
				p.MoveTo(v[0], v[1])
			} else {
				p.LineTo(v[i], v[i+1])
			}
		}
		if n.Tag == "polygon" {
			p.Close()
		}
	case "path":
		d := n.A["d"]
		if strings.HasPrefix(d, "path(") {
			d = strings.Trim(strings.TrimSuffix(strings.TrimPrefix(d, "path("), ")"), "'\" ")
		}
		pp, _ := ParseSVGPath(d)
		if pp == nil || pp.Empty() {
			return nil
		}
		return pp
	default:
		return nil
	}
	if p.Empty() {
		return nil
	}
	return p
}

func (r *svgR) optLen(n *svgNode, key string, axis byte, st *sStyle) (float64, bool) {
	v, ok := n.A[key]
	if !ok || v == "auto" || strings.TrimSpace(v) == "" {
		return 0, false
	}
	return r.length(v, r.refFor(axis), st.fontSize, 0), true
}

func (r *svgR) strokeStyle(st *sStyle) StrokeStyle {
	ss := StrokeStyle{Width: st.sw, Cap: st.cap, Join: st.join, MiterLimit: st.miter}
	if len(st.dash) > 0 {
		var d []float64
		sum := 0.0
		bad := false
		for _, s := range st.dash {
			v := r.length(s, r.refFor('d'), st.fontSize, 0)
			if v < 0 {
				bad = true
			}
			sum += v
			d = append(d, v)
		}
		if !bad && sum > 0 {
			ss.Dash = d
			ss.DashOffset = r.length(st.dashOff, r.refFor('d'), st.fontSize, 0)
		}
	}
	return ss
}

func (r *svgR) drawShape(n *svgNode, st sStyle, m Matrix) {
	if !st.visible {
		return
	}
	p := r.shapePath(n, &st)
	if p == nil {
		return
	}
	bb, hasBB := pathBBox(p)
	order := []string{"fill", "stroke", "markers"}
	if po := strings.Fields(st.paintOrder); len(po) > 0 && po[0] != "normal" {
		order = nil
		seen := map[string]bool{}
		for _, s := range po {
			if (s == "fill" || s == "stroke" || s == "markers") && !seen[s] {
				order = append(order, s)
				seen[s] = true
			}
		}
		for _, s := range []string{"fill", "stroke", "markers"} {
			if !seen[s] {
				order = append(order, s)
			}
		}
	}
	for _, step := range order {
		switch step {
		case "fill":
			if st.fill.kind == pNone || st.fillOp <= 0 {
				continue
			}
			if n.Tag == "line" {
				continue
			}
			if sh := r.shaderFor(st.fill, st.fillOp, n, bb, hasBB, &st, m); sh != nil {
				r.c.FillPathShader(p, st.fillRule, sh)
			}
		case "stroke":
			if st.stroke.kind == pNone || st.strokeOp <= 0 || st.sw <= 0 {
				continue
			}
			sh := r.shaderFor(st.stroke, st.strokeOp, n, bb, hasBB, &st, m)
			if sh == nil {
				continue
			}
			ss := r.strokeStyle(&st)
			var mask *Mask
			if st.vecEff == "non-scaling-stroke" {
				dp := p.Transform(m)
				mask = StrokeMask(dp, Identity, ss, r.c.drawBounds())
			} else {
				mask = StrokeMask(p, m, ss, r.c.drawBounds())
			}
			if mask != nil {
				r.c.draw(mask, sh)
			}
		case "markers":
			if n.Tag == "path" || n.Tag == "line" || n.Tag == "polyline" || n.Tag == "polygon" {
				r.drawMarkers(n, p, st, m)
			}
		}
	}
}

// pathBBox is the exact fill bounding box (curves flattened).
func pathBBox(p *Path) (Rect, bool) {
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, sp := range p.Flatten(0.02) {
		for _, q := range sp.P {
			x0, x1 = math.Min(x0, q.X), math.Max(x1, q.X)
			y0, y1 = math.Min(y0, q.Y), math.Max(y1, q.Y)
		}
	}
	if math.IsInf(x0, 1) {
		return Rect{}, false
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}, true
}

// bbox is the object bounding box of n in its own user space (before n's
// transform).
func (r *svgR) bbox(n *svgNode, st *sStyle) (Rect, bool) {
	switch n.Tag {
	case "rect", "circle", "ellipse", "line", "polyline", "polygon", "path":
		p := r.shapePath(n, st)
		if p == nil {
			return Rect{}, false
		}
		return pathBBox(p)
	case "g", "a", "svg", "switch", "symbol":
		return r.groupBBox(n, st)
	case "use":
		id := strings.TrimPrefix(strings.TrimSpace(n.A["href"]), "#")
		t := r.doc.ByID[id]
		if t == nil || r.active[t] {
			return Rect{}, false
		}
		r.active[t] = true
		defer delete(r.active, t)
		x, y := r.attrLen(n, "x", 'x', st, 0), r.attrLen(n, "y", 'y', st, 0)
		ts := r.inherit(*st, t.A)
		b, ok := r.bbox(t, &ts)
		if !ok {
			return b, false
		}
		tm := r.nodeTransform(t, &ts)
		b, _ = transformRect(b, Translate(x, y).Mul(tm))
		return b, true
	case "image":
		w, h := r.attrLen(n, "width", 'x', st, 0), r.attrLen(n, "height", 'y', st, 0)
		return Rect{r.attrLen(n, "x", 'x', st, 0), r.attrLen(n, "y", 'y', st, 0), w, h}, w > 0 && h > 0
	case "text":
		return r.textBBox(n, st)
	}
	return Rect{}, false
}

func transformRect(b Rect, m Matrix) (Rect, bool) {
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, q := range [][2]float64{{b.X, b.Y}, {b.X + b.W, b.Y}, {b.X, b.Y + b.H}, {b.X + b.W, b.Y + b.H}} {
		x, y := m.Apply(q[0], q[1])
		x0, x1 = math.Min(x0, x), math.Max(x1, x)
		y0, y1 = math.Min(y0, y), math.Max(y1, y)
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}, true
}

func (r *svgR) groupBBox(n *svgNode, st *sStyle) (Rect, bool) {
	var out Rect
	have := false
	for _, k := range n.Kids {
		if k.Tag == "#text" || nonRendered(k.Tag) || k.A["display"] == "none" {
			continue
		}
		ks := r.inherit(*st, k.A)
		b, ok := r.bbox(k, &ks)
		if !ok {
			continue
		}
		b, _ = transformRect(b, r.nodeTransform(k, &ks))
		if !have {
			out, have = b, true
			continue
		}
		x0, y0 := math.Min(out.X, b.X), math.Min(out.Y, b.Y)
		x1, y1 := math.Max(out.X+out.W, b.X+b.W), math.Max(out.Y+out.H, b.Y+b.H)
		out = Rect{x0, y0, x1 - x0, y1 - y0}
	}
	return out, have
}
