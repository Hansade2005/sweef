package raster

import (
	"math"
	"strings"

	"github.com/sweefbrowser/sweef/internal/render"
)

// SVG text: addressable characters (x/y/dx/dy/rotate lists, whitespace),
// text chunks and anchors, textLength, textPath, baseline selection and
// baseline-shift are resolved by render.LayoutSVGText; this file supplies
// the font advances and draws the placed glyphs.

type textGlyph struct {
	text      string
	st        *sStyle
	node      *svgNode
	x, y      float64 // glyph origin on its baseline
	angle     float64
	sx        float64
	w         float64 // laid out advance
	nat       float64 // natural advance (font + letter/word spacing)
	asc, desc float64
}

func (g *textGlyph) plain() bool { return g.angle == 0 && g.sx == 1 }

type glyphMetric struct{ w, asc, desc float64 }

func isTextTag(t string) bool { return t == "tspan" || t == "a" || t == "textpath" }

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

func (r *svgR) layoutText(n *svgNode, st *sStyle) []textGlyph {
	styles := map[*svgNode]*sStyle{n: st}
	parents := map[*svgNode]*svgNode{}
	var walk func(p *svgNode, ps sStyle, depth int)
	walk = func(p *svgNode, ps sStyle, depth int) {
		if depth > 40 {
			return
		}
		for _, k := range p.Kids {
			if k.Tag == "#text" || k.A == nil || !isTextTag(k.Tag) || k.A["display"] == "none" {
				continue
			}
			ks := r.inherit(ps, k.A)
			styles[k] = &ks
			parents[k] = p
			walk(k, ks, depth+1)
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
		paths[ch.Path] = sp
	}
	c := r.c
	c.Save()
	defer c.Restore()
	cache := map[string]glyphMetric{}
	fm := map[string]glyphMetric{}
	measure := func(sty *sStyle, ch rune) (glyphMetric, glyphMetric) {
		fs := r.fontOf(sty)
		c.SetFont(fs)
		c.SetLetterSpacing(0)
		c.SetWordSpacing(0)
		c.SetFontKerning("auto")
		f, ok := fm[fs]
		if !ok {
			tm := c.MeasureText("M")
			f = glyphMetric{asc: tm.FontAscent, desc: tm.FontDescent}
			fm[fs] = f
		}
		k := fs + "\x00" + string(ch)
		g, ok := cache[k]
		if !ok {
			g = glyphMetric{w: c.MeasureText(string(ch)).Width}
			cache[k] = g
		}
		return g, f
	}
	ins := make([]render.SVGTextIn, len(chars))
	mets := make([]glyphMetric, len(chars))
	nats := make([]float64, len(chars))
	for i, ch := range chars {
		sty := styles[ch.Node]
		if sty == nil {
			sty = st
		}
		g, f := measure(sty, ch.R)
		adv := g.w + r.length(sty.letterSp, 0, sty.fontSize, 0)
		if ch.R == ' ' {
			adv += r.length(sty.wordSp, 0, sty.fontSize, 0)
		}
		nats[i] = adv
		mets[i] = f
		var off float64
		if sty.alignBase != "" && sty.alignBase != "baseline" && sty.alignBase != "auto" {
			off = baselineOffset(sty.alignBase, f.asc, f.desc, sty.fontSize)
		} else {
			off = baselineOffset(sty.baseline, f.asc, f.desc, sty.fontSize)
		}
		for a := ch.Node; a != nil; a = parents[a] {
			as := styles[a]
			if as == nil || as.shift == "" || as.shift == "baseline" {
				continue
			}
			_, af := measure(as, 'M')
			switch as.shift {
			case "sub":
				off += (af.asc + af.desc) / 2
			case "super":
				off -= (af.asc + af.desc) / 2
			default:
				off -= r.length(as.shift, as.fontSize, as.fontSize, 0)
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
	var out []textGlyph
	for i, p := range pos {
		if p.Hidden {
			continue
		}
		g := textGlyph{text: string(chars[i].R), st: styles[chars[i].Node], node: chars[i].Node, x: p.X, y: p.Y, angle: p.Angle, sx: p.ScaleX,
			w: p.Adv, nat: nats[i], asc: mets[i].asc, desc: mets[i].desc}
		if g.st == nil {
			g.st = st
		}
		// merge into the previous run when the pen follows the natural advance
		if k := len(out) - 1; k >= 0 {
			pv := &out[k]
			if pv.node == g.node && pv.st == g.st && pv.plain() && g.plain() && math.Abs(pv.y-g.y) < 1e-9 &&
				math.Abs(pv.x+pv.w-g.x) < 1e-6 && math.Abs(pv.w-pv.runNat()) < 1e-6 && math.Abs(g.w-g.nat) < 1e-6 {
				pv.text += g.text
				pv.w += g.w
				pv.nat += g.nat
				continue
			}
		}
		out = append(out, g)
	}
	return out
}

func (g *textGlyph) runNat() float64 { return g.nat }

func (r *svgR) textBBox(n *svgNode, st *sStyle) (Rect, bool) {
	placed := r.layoutText(n, st)
	if len(placed) == 0 {
		return Rect{}, false
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range placed {
		s, cs := math.Sincos(p.angle)
		for _, pt := range [4][2]float64{{0, -p.asc}, {p.w, -p.asc}, {p.w, p.desc}, {0, p.desc}} {
			x := p.x + pt[0]*cs - pt[1]*s
			y := p.y + pt[0]*s + pt[1]*cs
			x0, x1 = math.Min(x0, x), math.Max(x1, x)
			y0, y1 = math.Min(y0, y), math.Max(y1, y)
		}
	}
	return Rect{x0, y0, x1 - x0, y1 - y0}, true
}

func (r *svgR) drawText(n *svgNode, st sStyle, m Matrix) {
	if !st.visible {
		return
	}
	placed := r.layoutText(n, &st)
	if len(placed) == 0 {
		return
	}
	bb, hasBB := r.textBBox(n, &st)
	c := r.c
	for _, p := range placed {
		if p.st.visible == false {
			continue
		}
		if strings.TrimSpace(p.text) == "" && !strings.Contains(p.st.decoration, "underline") && !strings.Contains(p.st.decoration, "line-through") {
			continue
		}
		c.SetFont(r.fontOf(p.st))
		c.SetLetterSpacing(r.length(p.st.letterSp, 0, p.st.fontSize, 0))
		c.SetWordSpacing(r.length(p.st.wordSp, 0, p.st.fontSize, 0))
		c.SetTextAlign("left")
		c.SetFontKerning("auto")
		c.SetTextBaseline("alphabetic")
		plain := p.plain()
		screenScale := math.Sqrt((m[0]*m[0] + m[1]*m[1] + m[2]*m[2] + m[3]*m[3]) / 2)
		gm := m.Mul(Translate(p.x, p.y)).Mul(Rotate(p.angle)).Mul(Scale(p.sx, 1))
		if p.st.fill.kind != pNone && p.st.fillOp > 0 {
			if sh := r.shaderFor(p.st.fill, p.st.fillOp, n, bb, hasBB, p.st, m); sh != nil {
				if plain {
					shp, ox, oy, sx := c.prepText(p.text, p.x, p.y, 0)
					shp.ScreenScale = screenScale
					if p.st.fill.kind == pColor {
						shp.Lum = textLum(Color{p.st.fill.col.R, p.st.fill.col.G, p.st.fill.col.B, 1})
					}
					if len(shp.Glyphs) > 0 {
						c.draw(c.textMask(shp, ox, oy, sx), sh)
					}
				} else {
					shp, ox, oy, sx := c.prepText(p.text, 0, 0, 0)
					shp.ScreenScale = screenScale
					if p.st.fill.kind == pColor {
						shp.Lum = textLum(Color{p.st.fill.col.R, p.st.fill.col.G, p.st.fill.col.B, 1})
					}
					if len(shp.Glyphs) > 0 {
						c.Save()
						c.SetTransform(gm)
						mk := c.textMask(shp, ox, oy, sx)
						c.Restore()
						c.draw(mk, sh)
					}
				}
			}
		}
		if p.st.stroke.kind != pNone && p.st.strokeOp > 0 && p.st.sw > 0 {
			if sh := r.shaderFor(p.st.stroke, p.st.strokeOp, n, bb, hasBB, p.st, m); sh != nil {
				if plain {
					shp, ox, oy, sx := c.prepText(p.text, p.x, p.y, 0)
					shp.ScreenScale = screenScale
					if st := r.strokeStyle(p.st); len(st.Dash) == 0 {
						if col := p.st.stroke; col.kind == pColor {
							shp.Lum = textLum(Color{col.col.R, col.col.G, col.col.B, 1})
						}
						if mk, ok := c.strokeGlyphMaskFor(shp, ox, oy, sx, st); ok {
							c.draw(mk, sh)
							goto strokeDone
						}
					}
					if tp := c.textPath(shp, ox, oy, sx); tp != nil {
						if mk := StrokeMask(tp, m, r.strokeStyle(p.st), c.drawBounds()); mk != nil {
							c.draw(mk, sh)
						}
					}
				} else {
					shp, ox, oy, sx := c.prepText(p.text, 0, 0, 0)
					shp.ScreenScale = screenScale
					if st := r.strokeStyle(p.st); len(st.Dash) == 0 {
						c.Save()
						c.SetTransform(gm)
						if col := p.st.stroke; col.kind == pColor {
							shp.Lum = textLum(Color{col.col.R, col.col.G, col.col.B, 1})
						}
						mk, ok := c.strokeGlyphMaskFor(shp, ox, oy, sx, st)
						c.Restore()
						if ok {
							c.draw(mk, sh)
							goto strokeDone
						}
					}
					if tp := c.textPath(shp, ox, oy, sx); tp != nil {
						if mk := StrokeMask(tp.Transform(Rotate(p.angle).Mul(Scale(p.sx, 1))).Transform(Translate(p.x, p.y)), m, r.strokeStyle(p.st), c.drawBounds()); mk != nil {
							c.draw(mk, sh)
						}
					}
				}
			}
		}
	strokeDone:
		if p.st.decoration != "" && p.st.fill.kind == pColor {
			th := math.Max(1, p.st.fontSize/16)
			col := p.st.fill.col
			col.A *= p.st.fillOp
			deco := func(dy float64) {
				pa := NewPath()
				pa.Rect(0, dy, p.w, th)
				c.FillPathShader(pa.Transform(Rotate(p.angle)).Transform(Translate(p.x, p.y)), NonZero, Solid(col))
			}
			if strings.Contains(p.st.decoration, "underline") {
				deco(p.st.fontSize * 0.1)
			}
			if strings.Contains(p.st.decoration, "line-through") {
				deco(-p.st.fontSize * 0.3)
			}
			if strings.Contains(p.st.decoration, "overline") {
				deco(-p.asc)
			}
		}
	}
	c.SetLetterSpacing(0)
	c.SetWordSpacing(0)
}
