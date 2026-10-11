package render

import (
	"math"
	"strings"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// <foreignObject>: the HTML subtree is laid out with the ordinary CSS engine
// as a block of exactly the foreignObject's width x height placed at (x, y)
// in SVG user space, then mapped to the page by the user-space matrix of the
// object (viewBox, ancestors' transforms) carried as the wrapper's transform.
// Because the wrapper is a regular lbox the descendants get element boxes,
// hit-testing and ClientRect (ancestor transforms compose through the DOM)
// like any HTML content, and they paint through the display list as a
// stacking context emitted right after the owning scene (foreignObject
// content therefore paints above the SVG shapes of the same <svg>).

// svgHasForeign reports whether the subtree under n contains a foreignObject.
func svgHasForeign(n *html.Node, depth int) bool {
	if depth > 64 {
		return false
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type != html.ElementNode {
			continue
		}
		if strings.EqualFold(ch.Data, "foreignObject") {
			return true
		}
		if svgHasForeign(ch, depth+1) {
			return true
		}
	}
	return false
}

// layoutForeignObjects lays out the foreignObject content of every inline
// <svg> (and of inline svgs nested in foreignObjects). Called once layout is
// final and before element boxes are produced.
func (c *ctx) layoutForeignObjects() {
	seen := map[*lbox]bool{}
	for round := 0; round < 4; round++ {
		var todo []*lbox
		for _, bs := range c.byNode {
			for _, b := range bs {
				if b.rep != nil && b.rep.kind == "svg" && !seen[b] && b.n != nil {
					seen[b] = true
					if svgHasForeign(b.n, 0) {
						todo = append(todo, b)
					}
				}
			}
		}
		if len(todo) == 0 {
			return
		}
		for _, b := range todo {
			c.foreignFor(b)
		}
	}
}

func (c *ctx) foreignFor(b *lbox) {
	doc := c.svgDocFor(b.n, b.st)
	if doc == nil {
		return
	}
	base, vw, vh := c.svgBaseMatrix(b)
	var walk func(n *html.Node, m m6, vw, vh float64, pst *style.Computed, depth int)
	walk = func(n *html.Node, m m6, vw, vh float64, pst *style.Computed, depth int) {
		if n.Type != html.ElementNode || depth > 64 {
			return
		}
		tag := strings.ToLower(n.Data)
		if t, _ := svgProp(doc, n, "transform"); t != "" && t != "none" {
			m = mmul(m, parseSVGTransform(t))
		}
		if d, _ := svgProp(doc, n, "display"); d == "none" {
			return
		}
		switch tag {
		case "defs", "clippath", "mask", "pattern", "marker", "symbol", "lineargradient", "radialgradient", "filter", "style", "script", "text":
			return
		}
		st := c.eng.Compute(n, pst, "")
		if st == nil {
			return
		}
		switch tag {
		case "foreignobject":
			c.layoutForeign(b, n, m, vw, vh, st, pst, doc)
			return
		case "svg":
			x, y := svgLen(attr(n, "x"), vw, 0), svgLen(attr(n, "y"), vh, 0)
			w, h := svgLen(attr(n, "width"), vw, vw), svgLen(attr(n, "height"), vh, vh)
			mm := mmul(m, m6{1, 0, 0, 1, x, y})
			nvw, nvh := w, h
			if vb := strings.Fields(strings.ReplaceAll(attr(n, "viewBox"), ",", " ")); len(vb) == 4 {
				bw, bh := svgNum(vb[2], w), svgNum(vb[3], h)
				if bw > 0 && bh > 0 {
					s := math.Min(w/bw, h/bh)
					mm = mmul(mm, m6{s, 0, 0, s, -svgNum(vb[0], 0) * s, -svgNum(vb[1], 0) * s})
					nvw, nvh = bw, bh
				}
			}
			m, vw, vh = mm, nvw, nvh
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch, m, vw, vh, st, depth+1)
		}
	}
	for ch := b.n.FirstChild; ch != nil; ch = ch.NextSibling {
		walk(ch, base, vw, vh, b.st, 0)
	}
}

func (c *ctx) layoutForeign(owner *lbox, n *html.Node, m m6, vw, vh float64, st, pst *style.Computed, doc *SVGDoc) {
	num := func(k string, ref float64) float64 {
		v, _ := svgProp(doc, n, k)
		return svgLen(v, ref, 0)
	}
	x, y, w, h := num("x", vw), num("y", vh), num("width", vw), num("height", vh)
	if w <= 0 || h <= 0 {
		return
	}
	cp := *st
	cp.Display = "block"
	cp.Position = "relative"
	cp.Float = "none"
	cp.Visibility = "visible"
	if v, _ := svgProp(doc, n, "visibility"); v == "hidden" || v == "collapse" {
		cp.Visibility = "hidden"
	}
	if cp.OverflowX == "visible" && cp.OverflowY == "visible" {
		// the UA rule `foreignObject { overflow: hidden }` unless the author set visible
		if v, ok := svgProp(doc, n, "overflow"); !ok || (v != "visible" && v != "auto") {
			cp.OverflowX, cp.OverflowY = "hidden", "hidden"
		}
	}
	// the user-space matrix about the box origin
	px, py := x, y
	mm := mmul(mmul(m6{1, 0, 0, 1, -px, -py}, m), m6{1, 0, 0, 1, px, py})
	cp.Translate, cp.Rotate, cp.Scale = nil, nil, nil
	cp.Transform = nil
	if mm != (m6{1, 0, 0, 1, 0, 0}) {
		op := style.TOp{Fn: "matrix", N: 6}
		for i := 0; i < 6; i++ {
			op.A[i] = style.Px(mm[i])
		}
		cp.Transform = []style.TOp{op}
	}
	cp.TransformOrigin = [2]style.Length{style.Px(0), style.Px(0)}
	cp.Opacity, cp.OpacitySet = st.Opacity, st.OpacitySet
	zero := style.Px(0)
	cp.Margin = [4]style.Length{zero, zero, zero, zero}
	cp.Inset = [4]style.Length{}
	cp.Width, cp.Height = style.Length{}, style.Length{}
	cp.MinWidth, cp.MinHeight = style.Length{}, style.Length{}
	cp.Padding = [4]style.Length{zero, zero, zero, zero}
	for i := range cp.BorderWidth {
		cp.BorderWidth[i] = 0
	}
	c.styles[n] = &cp
	wb := newBox(n, &cp, kBlock)
	wb.inner = iFlow
	var kids []*lbox
	c.buildChildrenInto(n, &cp, &kids, nil)
	for _, k := range kids {
		wb.addKid(k)
	}
	c.finishCommon(wb)
	c.fixupChildren(wb)
	in := layoutIn{cbw: w, cbh: h, fixW: w, fixH: h, bfc: &bfcState{}}
	wb.hasLast = false
	c.layoutBox(wb, in)
	wb.x, wb.y = x, y
	c.place(wb, x, y)
	if c.fo == nil {
		c.fo = map[*lbox][]*lbox{}
	}
	c.fo[owner] = append(c.fo[owner], wb)
}

// paintForeign emits the foreignObject content of an inline svg.
func (p *painter) paintForeign(b *lbox) {
	for _, w := range p.c.fo[b] {
		p.paintSC(w, false)
	}
}
