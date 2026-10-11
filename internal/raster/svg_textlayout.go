package raster

import (
	"math"
	"os"
	"strings"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/render"
)

// SVG text: addressable characters (x/y/dx/dy/rotate lists, whitespace),
// text chunks and anchors, textLength, textPath, baseline selection and
// baseline-shift are resolved by render.LayoutSVGText; this file supplies
// the font advances and the placed glyphs.
//
// Blink (LayoutNG SVG text) lays text out in a "scaled" space: the font is
// instantiated at font-size * k, where k = sqrt((a^2+b^2+c^2+d^2)/2) of the
// transform to the screen, the x/y/dx/dy lists and textLength are multiplied
// by k, all glyph positions are computed there, and the text is painted under
// a canvas scale of 1/k. Everything below is in that scaled space; k = 1
// reproduces unscaled text.

type textGlyph struct {
	r         rune
	g         Glyph // face and, for a ligature head, the ligature glyph
	skip      bool  // tail of a ligature: not drawn
	st        *sStyle
	node      *svgNode
	x, y      float64 // glyph origin on its baseline (scaled space)
	angle     float64
	sx        float64
	w         float64 // laid out advance
	nat       float64 // natural advance (font + letter/word spacing)
	ex        float64 // exact advance
	asc, desc float64
	size      float64 // scaled font size
	fake      bool    // synthetic bold
	skew      bool    // synthetic italic
	nom       int     // index of the addressable character
	pivX      float64 // rotation pivot (x)
}

func (g *textGlyph) plain() bool { return g.angle == 0 && g.sx == 1 }

func isTextTag(t string) bool { return t == "tspan" || t == "a" || t == "textpath" }

// svgScreenScale is Blink's font scaling factor for a text under the device
// matrix m (SVGLayoutSupport::CalculateScreenFontSizeScalingFactor).
func svgScreenScale(m Matrix) float64 {
	k := float64(float32(math.Sqrt((m[0]*m[0] + m[1]*m[1] + m[2]*m[2] + m[3]*m[3]) / 2)))
	if k <= 0 || math.IsNaN(k) || math.IsInf(k, 0) {
		return 1
	}
	return k
}

// svgScaledSize is the pixel size of the font Blink instantiates: the
// product of font size and scaling factor, truncated to 1/100 (the platform
// font cache precision) in single precision.
func svgScaledSize(fs, k float64) float64 {
	s := float32(fs) * float32(k)
	return float64(float32(math.Floor(float64(s)*100+1e-6)) / 100)
}

// baselineOffset is the downward shift of the glyph origin that puts the
// given baseline of a font on the text position.
func baselineOffset(kind string, asc, desc, fs float64) float64 {
	switch kind {
	case "central":
		return (asc - desc) / 2
	case "middle":
		return fs * 0.2642
	case "hanging":
		return 0.8 * asc
	case "text-before-edge", "text-top":
		return asc
	case "text-after-edge", "text-bottom", "ideographic":
		return -desc
	case "mathematical":
		return 0.5 * asc
	}
	return 0
}

// svgRun is the shaping of one run of characters sharing a font.
type svgRun struct {
	adv        []float64 // per character, including kerning and spacing, (layout)
	ex         []float64 // exact per-character advance (glyph offsets inside a fragment)
	glyph      []Glyph
	skip       []bool
	asc, desc  float64
	size       float64
	fake, skew bool
}

// shapeSVGRun shapes rs with the font spec at its (already scaled) size the
// way Blink does for an SVG text item: the whole run goes through the shaper
// at once (so kerning applies across spaces), advances are linear in the
// FreeType pixel size, and each character's advance is the difference of the
// 1/64-floored cumulative pen positions.
func shapeSVGRun(spec FontSpec, rs []rune, letterSp, wordSp float64) svgRun {
	fs := resolveFaces(spec.Families, spec.Weight, spec.Italic)
	out := svgRun{adv: make([]float64, len(rs)), ex: make([]float64, len(rs)), glyph: make([]Glyph, len(rs)), skip: make([]bool, len(rs)), size: spec.Size}
	if len(fs.faces) > 0 {
		f0 := fs.faces[0]
		out.fake = spec.Weight >= 600 && f0.Style.Weight < 600
		out.skew = spec.Italic && !f0.Style.Italic
		m := f0.Metrics(spec.Size)
		out.asc, out.desc = math.Round(m.Ascent), math.Round(m.Descent)
	}
	raw := make([]float64, len(rs))
	opts := font.ShapeOptions{Kern: true, Liga: letterSp == 0}
	for i := 0; i < len(rs); {
		f := fs.faceFor(rs[i])
		if f == nil {
			i++
			continue
		}
		if !f.HasGlyph(rs[i]) {
			out.glyph[i] = Glyph{R: rs[i], Face: f}
			raw[i] = spec.Size * 0.5
			i++
			continue
		}
		j := i + 1
		for j < len(rs) && fs.faceFor(rs[j]) == f && f.HasGlyph(rs[j]) {
			j++
		}
		run := f.ShapeRun(rs[i:j], spec.Size, opts)
		for gi, g := range run {
			next := g.X + g.Adv
			if gi+1 < len(run) {
				next = run[gi+1].X
			}
			a := next - g.X
			n := g.NRunes
			if n < 1 {
				n = 1
			}
			for c := 0; c < n; c++ {
				idx := i + g.Cluster + c
				out.glyph[idx] = Glyph{R: rs[idx], Face: f}
				raw[idx] = a / float64(n)
				if c > 0 {
					out.skip[idx] = true
				}
			}
			if n > 1 {
				out.glyph[i+g.Cluster].GID = g.GID
			}
		}
		i = j
	}
	pen, prev := 0.0, 0.0
	for i, r := range rs {
		pen += raw[i] + letterSp
		if r == ' ' {
			pen += wordSp
		}
		out.ex[i] = raw[i] + letterSp
		if r == ' ' {
			out.ex[i] += wordSp
		}
		fl := pen
		if os.Getenv("SVG_ADVFLOOR") != "" {
			fl = math.Floor(pen*64+1e-6) / 64
		}
		out.adv[i] = fl - prev
		prev = fl
	}
	return out
}

func (r *svgR) layoutText(n *svgNode, st *sStyle, k float64) []textGlyph {
	styles := map[*svgNode]*sStyle{n: st}
	parents := map[*svgNode]*svgNode{}
	var walk func(p *svgNode, ps sStyle, depth int)
	walk = func(p *svgNode, ps sStyle, depth int) {
		if depth > 40 {
			return
		}
		for _, kid := range p.Kids {
			if kid.Tag == "#text" || kid.A == nil || !isTextTag(kid.Tag) || kid.A["display"] == "none" {
				continue
			}
			ks := r.inherit(ps, kid.A)
			styles[kid] = &ks
			parents[kid] = p
			walk(kid, ks, depth+1)
		}
	}
	walk(n, *st, 0)
	lenFn := func(nd *svgNode, s string, axis byte) float64 {
		fs := st.fontSize
		if ns := styles[nd]; ns != nil {
			fs = ns.fontSize
		}
		return r.length(s, r.refFor(axis), fs, 0)
	}
	chars, lens := render.CollectSVGText(n, lenFn)
	if len(chars) == 0 {
		return nil
	}
	// scaled space
	for i := range chars {
		c := &chars[i]
		c.X, c.Y, c.DX, c.DY = c.X*k, c.Y*k, c.DX*k, c.DY*k
	}
	for i := range lens {
		lens[i].Length *= k
	}
	// text paths
	paths := map[*svgNode]*render.SVGTextPath{}
	for _, ch := range chars {
		if ch.Path == nil || paths[ch.Path] != nil {
			continue
		}
		id := strings.TrimPrefix(strings.TrimSpace(ch.Path.A["href"]), "#")
		t := r.doc.ByID[id]
		if t == nil || t.Tag != "path" {
			return nil
		}
		sp := &render.SVGTextPath{Contours: render.SVGPathContours(t.A["d"])}
		sp.Start = render.SVGParsePct(ch.Path.A["startoffset"], sp.Length())
		sp.Scale(k)
		paths[ch.Path] = sp
	}
	// shaping runs: consecutive characters with the same font and spacing
	type runKey struct {
		font   string
		ls, ws float64
	}
	keyOf := func(i int) (runKey, *sStyle) {
		sty := styles[chars[i].Node]
		if sty == nil {
			sty = st
		}
		return runKey{r.fontOf(sty), r.length(sty.letterSp, 0, sty.fontSize, 0) * k, r.length(sty.wordSp, 0, sty.fontSize, 0) * k}, sty
	}
	shaped := make([]svgRun, len(chars))
	runOf := make([]int, len(chars)) // start of the run a character belongs to
	for i := 0; i < len(chars); {
		key, sty := keyOf(i)
		j := i + 1
		for j < len(chars) {
			k2, _ := keyOf(j)
			if k2 != key {
				break
			}
			j++
		}
		spec, ok := ParseFont(key.font, 16)
		if !ok {
			spec = FontSpec{Families: []string{"serif"}, Weight: 400, Size: sty.fontSize}
		}
		spec.Size = svgScaledSize(sty.fontSize, k)
		rs := make([]rune, j-i)
		for q := i; q < j; q++ {
			rs[q-i] = chars[q].R
		}
		sr := shapeSVGRun(spec, rs, key.ls, key.ws)
		for q := i; q < j; q++ {
			shaped[q] = svgRun{adv: sr.adv[q-i : q-i+1], ex: sr.ex[q-i : q-i+1], glyph: sr.glyph[q-i : q-i+1], skip: sr.skip[q-i : q-i+1],
				asc: sr.asc, desc: sr.desc, size: sr.size, fake: sr.fake, skew: sr.skew}
			runOf[q] = i
		}
		i = j
	}
	ins := make([]render.SVGTextIn, len(chars))
	nats := make([]float64, len(chars))
	for i, ch := range chars {
		sty := styles[ch.Node]
		if sty == nil {
			sty = st
		}
		sr := &shaped[i]
		adv := sr.adv[0]
		nats[i] = adv
		var off float64
		fsK := sr.size
		if sty.alignBase != "" && sty.alignBase != "baseline" && sty.alignBase != "auto" {
			off = baselineOffset(sty.alignBase, sr.asc, sr.desc, fsK)
		} else {
			off = baselineOffset(sty.baseline, sr.asc, sr.desc, fsK)
		}
		for a := ch.Node; a != nil; a = parents[a] {
			as := styles[a]
			if as == nil || as.shift == "" || as.shift == "baseline" {
				continue
			}
			am := r.metricOf(as, k)
			switch as.shift {
			case "sub":
				off += (am.asc + am.desc) / 2
			case "super":
				off -= (am.asc + am.desc) / 2
			default:
				off -= r.length(as.shift, as.fontSize, as.fontSize, 0) * k
			}
		}
		anchor := 0
		switch sty.anchor {
		case "middle":
			anchor = 1
		case "end":
			anchor = 2
		}
		ins[i] = render.SVGTextIn{SVGTextChar: ch, Adv: adv, YOff: off, Anchor: anchor, Space: ch.R == ' '}
		if ch.Path != nil {
			ins[i].PathM = paths[ch.Path]
		}
	}
	pos := render.LayoutSVGText(ins, lens)
	out := make([]textGlyph, 0, len(pos))
	for i, p := range pos {
		if p.Hidden {
			continue
		}
		sty := styles[chars[i].Node]
		if sty == nil {
			sty = st
		}
		sr := &shaped[i]
		out = append(out, textGlyph{r: chars[i].R, g: sr.glyph[0], skip: sr.skip[0], st: sty, node: chars[i].Node,
			x: p.X, y: p.Y, angle: p.Angle, sx: p.ScaleX, w: p.Adv, nat: nats[i], ex: sr.ex[0], asc: sr.asc, desc: sr.desc,
			size: sr.size, fake: sr.fake, skew: sr.skew, nom: i})
	}
	byNom := make(map[int]*textGlyph, len(out))
	for i := range out {
		byNom[out[i].nom] = &out[i]
	}
	for i := range out {
		g := &out[i]
		g.pivX = g.x
		// the pivot is the character position Blink reports: the run start
		// plus the floored offset
		if st0 := byNom[runOf[g.nom]]; st0 != nil && os.Getenv("SVG_PIVRAW") == "" {
			g.pivX = st0.x + float64(lu64(g.x)-lu64(st0.x))
		}
	}
	return out
}

type fontMet struct{ asc, desc float64 }

// metricOf is the rounded ascent and descent of the scaled font of a style.
func (r *svgR) metricOf(sty *sStyle, k float64) fontMet {
	spec, ok := ParseFont(r.fontOf(sty), 16)
	if !ok {
		return fontMet{}
	}
	spec.Size = svgScaledSize(sty.fontSize, k)
	fs := resolveFaces(spec.Families, spec.Weight, spec.Italic)
	if len(fs.faces) == 0 {
		return fontMet{}
	}
	m := fs.faces[0].Metrics(spec.Size)
	return fontMet{math.Round(m.Ascent), math.Round(m.Descent)}
}

// glyphsBBox is the box of the placed glyph cells, in user space.
func glyphsBBox(placed []textGlyph, k float64) (Rect, bool) {
	if len(placed) == 0 {
		return Rect{}, false
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range placed {
		s, cs := math.Sincos(p.angle)
		for _, pt := range [4][2]float64{{0, -p.asc}, {p.w, -p.asc}, {p.w, p.desc}, {0, p.desc}} {
			x := (p.x + pt[0]*cs - pt[1]*s) / k
			y := (p.y + pt[0]*s + pt[1]*cs) / k
			x0, x1 = math.Min(x0, x), math.Max(x1, x)
			y0, y1 = math.Min(y0, y), math.Max(y1, y)
		}
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}, true
}

func (r *svgR) textBBox(n *svgNode, st *sStyle) (Rect, bool) {
	return glyphsBBox(r.layoutText(n, st, 1), 1)
}
