package render

import (
	"math"
	"strings"

	"golang.org/x/net/html"
)

// Hit testing of foreignObject content under SVG ancestors that clip. The
// painter records hit rectangles in the foreignObject's user space; clip-path
// on the foreignObject itself and on its <g>/<svg>/<a> ancestors (url(#clipPath)
// of rect/circle/ellipse/polygon/path/line children, or a CSS basic shape)
// further restricts them. Chromium applies clip-path (but not mask) to hit
// testing. clipPath <path> children are tested by their bounding box.

// foAnc is an SVG ancestor (or the foreignObject itself) with a clip-path, with
// the user-space matrix in effect for its children (its own transform included).
type foAnc struct {
	n      *html.Node
	m      m6
	vw, vh float64
}

// foreignHitClips returns the point tests (taking a point in the foreignObject's
// user space) for the clip-paths of anc; foM is the foreignObject's matrix.
func (c *ctx) foreignHitClips(doc *SVGDoc, anc []foAnc, foM m6) []func(x, y float64) bool {
	var out []func(x, y float64) bool
	for _, a := range anc {
		v, _ := svgProp(doc, a.n, "clip-path")
		v = strings.TrimSpace(v)
		if v == "" || v == "none" {
			continue
		}
		inv, ok := invert(a.m)
		if !ok {
			continue
		}
		to := mmul(inv, foM) // foreignObject space -> ancestor space
		sn := doc.dom[a.n]
		var test func(x, y float64) bool
		switch {
		case strings.HasPrefix(v, "url("):
			test = c.clipPathTest(doc, sn, v, a)
		default:
			test = c.clipShapeTest(doc, sn, v, a)
		}
		if test == nil {
			continue
		}
		out = append(out, func(x, y float64) bool {
			return test(to[0]*x+to[2]*y+to[4], to[1]*x+to[3]*y+to[5])
		})
	}
	return out
}

// svgNodeBBox is the fill bounding box of n's content in n's own user space
// (children's coordinate system, i.e. including n's transform attribute not).
func (c *ctx) svgNodeBBox(doc *SVGDoc, n *SVGNode, vw, vh float64, depth int) bbox {
	var bb bbox
	if n == nil || depth > 32 || n.A["display"] == "none" {
		return bb
	}
	g := func(k string, ref float64) float64 { return svgLen(n.A[k], ref, 0) }
	switch n.Tag {
	case "rect", "image", "foreignobject":
		x, y, w, h := g("x", vw), g("y", vh), g("width", vw), g("height", vh)
		if w > 0 && h > 0 {
			bb.addRect(m6{1, 0, 0, 1, 0, 0}, x, y, x+w, y+h)
		}
	case "circle":
		cx, cy, r := g("cx", vw), g("cy", vh), g("r", math.Hypot(vw, vh)/math.Sqrt2)
		bb.addRect(m6{1, 0, 0, 1, 0, 0}, cx-r, cy-r, cx+r, cy+r)
	case "ellipse":
		cx, cy, rx, ry := g("cx", vw), g("cy", vh), g("rx", vw), g("ry", vh)
		bb.addRect(m6{1, 0, 0, 1, 0, 0}, cx-rx, cy-ry, cx+rx, cy+ry)
	case "line":
		bb.add(g("x1", vw), g("y1", vh))
		bb.add(g("x2", vw), g("y2", vh))
	case "polygon", "polyline":
		pts := strings.FieldsFunc(n.A["points"], func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
		for i := 0; i+1 < len(pts); i += 2 {
			bb.add(svgNum(pts[i], 0), svgNum(pts[i+1], 0))
		}
	case "path":
		svgPathBBox(n.A["d"], m6{1, 0, 0, 1, 0, 0}, &bb)
	case "use":
		if t := doc.ByID[strings.TrimPrefix(strings.TrimSpace(n.A["href"]), "#")]; t != nil && t != n {
			m := m6{1, 0, 0, 1, g("x", vw), g("y", vh)}
			if tr := t.A["transform"]; tr != "" && tr != "none" {
				m = mmul(m, parseSVGTransform(tr))
			}
			sub := c.svgNodeBBox(doc, t, vw, vh, depth+1)
			if sub.ok {
				bb.addRect(m, sub.x0, sub.y0, sub.x1, sub.y1)
			}
		}
	case "g", "a", "svg", "switch", "symbol":
		for _, k := range n.Kids {
			if k.Tag == "#text" || k.A["display"] == "none" {
				continue
			}
			switch k.Tag {
			case "defs", "clippath", "mask", "pattern", "marker", "lineargradient", "radialgradient", "filter", "style", "title", "desc", "metadata", "script":
				continue
			}
			sub := c.svgNodeBBox(doc, k, vw, vh, depth+1)
			if !sub.ok {
				continue
			}
			m := m6{1, 0, 0, 1, 0, 0}
			if tr := k.A["transform"]; tr != "" && tr != "none" {
				m = parseSVGTransform(tr)
			}
			bb.addRect(m, sub.x0, sub.y0, sub.x1, sub.y1)
		}
	}
	return bb
}

// clipPathTest tests a point (in the referencing element's user space) against
// the clipPath named by v; nil when the reference does not clip (missing id).
func (c *ctx) clipPathTest(doc *SVGDoc, ref *SVGNode, v string, a foAnc) func(x, y float64) bool {
	i, j := strings.Index(v, "#"), strings.LastIndex(v, ")")
	if i < 0 || j < i {
		return nil
	}
	id := strings.Trim(strings.TrimSpace(v[i+1:j]), `"' `)
	cp := doc.ByID[id]
	if cp == nil || cp.Tag != "clippath" {
		return nil
	}
	pre := m6{1, 0, 0, 1, 0, 0}
	if tr := cp.A["transform"]; tr != "" && tr != "none" {
		pre = parseSVGTransform(tr)
	}
	if strings.EqualFold(strings.TrimSpace(cp.A["clippathunits"]), "objectboundingbox") {
		bb := c.svgNodeBBox(doc, ref, a.vw, a.vh, 0)
		if !bb.ok || bb.x1 <= bb.x0 || bb.y1 <= bb.y0 {
			return func(x, y float64) bool { return false }
		}
		pre = mmul(pre, m6{bb.x1 - bb.x0, 0, 0, bb.y1 - bb.y0, bb.x0, bb.y0})
	}
	type shape struct {
		inv m6
		sub ClipSub
	}
	var shapes []shape
	var addShape func(k *SVGNode, base m6, depth int)
	addShape = func(k *SVGNode, base m6, depth int) {
		if depth > 8 || k.A["display"] == "none" {
			return
		}
		m := base
		if tr := k.A["transform"]; tr != "" && tr != "none" {
			m = mmul(m, parseSVGTransform(tr))
		}
		g := func(key string, ref float64) float64 { return svgLen(k.A[key], ref, 0) }
		var sub ClipSub
		switch k.Tag {
		case "rect":
			x, y, w, h := g("x", a.vw), g("y", a.vh), g("width", a.vw), g("height", a.vh)
			if w <= 0 || h <= 0 {
				return
			}
			sub = ClipSub{Kind: "rrect", Rect: Rect{x, y, w, h}}
			rx, ry := g("rx", a.vw), g("ry", a.vh)
			if k.A["rx"] == "" {
				rx = ry
			}
			if k.A["ry"] == "" {
				ry = rx
			}
			rx, ry = math.Min(rx, w/2), math.Min(ry, h/2)
			if rx > 0 && ry > 0 {
				sub.Radii = Radii{{rx, ry}, {rx, ry}, {rx, ry}, {rx, ry}}
			}
		case "circle":
			r := g("r", math.Hypot(a.vw, a.vh)/math.Sqrt2)
			sub = ClipSub{Kind: "ellipse", CX: g("cx", a.vw), CY: g("cy", a.vh), RX: r, RY: r}
		case "ellipse":
			sub = ClipSub{Kind: "ellipse", CX: g("cx", a.vw), CY: g("cy", a.vh), RX: g("rx", a.vw), RY: g("ry", a.vh)}
		case "polygon":
			pts := strings.FieldsFunc(k.A["points"], func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
			var ps [][2]float64
			for i := 0; i+1 < len(pts); i += 2 {
				ps = append(ps, [2]float64{svgNum(pts[i], 0), svgNum(pts[i+1], 0)})
			}
			if len(ps) < 3 {
				return
			}
			sub = ClipSub{Kind: "polygon", Pts: ps, EvenOdd: k.A["clip-rule"] == "evenodd"}
		case "path":
			var bb bbox
			svgPathBBox(k.A["d"], m6{1, 0, 0, 1, 0, 0}, &bb)
			if !bb.ok {
				return
			}
			sub = ClipSub{Kind: "rrect", Rect: Rect{bb.x0, bb.y0, bb.x1 - bb.x0, bb.y1 - bb.y0}}
		case "use":
			t := doc.ByID[strings.TrimPrefix(strings.TrimSpace(k.A["href"]), "#")]
			if t == nil || t == k {
				return
			}
			addShape(t, mmul(m, m6{1, 0, 0, 1, g("x", a.vw), g("y", a.vh)}), depth+1)
			return
		default:
			return
		}
		inv, ok := invert(m)
		if !ok {
			return
		}
		shapes = append(shapes, shape{inv, sub})
	}
	for _, k := range cp.Kids {
		addShape(k, pre, 0)
	}
	return func(x, y float64) bool {
		for _, s := range shapes {
			if clipShapeContains(&s.sub, s.inv[0]*x+s.inv[2]*y+s.inv[4], s.inv[1]*x+s.inv[3]*y+s.inv[5]) {
				return true
			}
		}
		return false
	}
}

// clipShapeTest tests against a CSS basic-shape clip-path on an SVG element.
func (c *ctx) clipShapeTest(doc *SVGDoc, ref *SVGNode, v string, a foAnc) func(x, y float64) bool {
	cs, ok := ParseSVGClip(v)
	if !ok {
		return nil
	}
	var R Rect
	if cs.Box == "view-box" {
		R = Rect{0, 0, a.vw, a.vh}
	} else {
		bb := c.svgNodeBBox(doc, ref, a.vw, a.vh, 0)
		if bb.ok {
			R = Rect{bb.x0, bb.y0, bb.x1 - bb.x0, bb.y1 - bb.y0}
		}
	}
	sub := cs.Resolve(R)
	return func(x, y float64) bool { return clipShapeContains(&sub, x, y) }
}
