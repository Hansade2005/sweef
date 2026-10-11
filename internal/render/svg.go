package render

import (
	"math"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// SVG support is limited to geometry: every SVG element gets the client
// bounding box Chromium reports (fill bounding box of shapes, transforms and
// the viewBox mapping applied); painting of SVG content is not implemented.

type m6 = [6]float64

func mmul(a, b m6) m6 { return mulM(a, b) }

func svgNum(s string, dflt float64) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "px"))
	if s == "" {
		return dflt
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return dflt
	}
	return v
}

func svgLen(s string, base, dflt float64) float64 {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "%") {
		v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err == nil {
			return v / 100 * base
		}
	}
	for _, u := range []struct {
		s string
		k float64
	}{{"pt", 96.0 / 72}, {"pc", 16}, {"mm", 96 / 25.4}, {"cm", 96 / 2.54}, {"in", 96}, {"em", 16}} {
		if strings.HasSuffix(s, u.s) {
			if v, err := strconv.ParseFloat(strings.TrimSuffix(s, u.s), 64); err == nil {
				return v * u.k
			}
		}
	}
	return svgNum(s, dflt)
}

func parseSVGTransform(s string) m6 {
	m := m6{1, 0, 0, 1, 0, 0}
	s = strings.TrimSpace(s)
	for len(s) > 0 {
		i := strings.IndexByte(s, '(')
		j := strings.IndexByte(s, ')')
		if i < 0 || j < i {
			break
		}
		name := strings.ToLower(strings.TrimSpace(strings.Trim(s[:i], ", ")))
		args := strings.FieldsFunc(s[i+1:j], func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' })
		var a []float64
		for _, x := range args {
			x = strings.TrimSuffix(strings.TrimSuffix(x, "px"), "deg")
			v, _ := strconv.ParseFloat(x, 64)
			a = append(a, v)
		}
		s = s[j+1:]
		t := m6{1, 0, 0, 1, 0, 0}
		g := func(k int, d float64) float64 {
			if k < len(a) {
				return a[k]
			}
			return d
		}
		switch name {
		case "translate":
			t[4], t[5] = g(0, 0), g(1, 0)
		case "scale":
			t[0], t[3] = g(0, 1), g(1, g(0, 1))
		case "rotate":
			r := g(0, 0) * math.Pi / 180
			c, sn := math.Cos(r), math.Sin(r)
			t = m6{c, sn, -sn, c, 0, 0}
			if len(a) >= 3 {
				cx, cy := a[1], a[2]
				t = mmul(mmul(m6{1, 0, 0, 1, cx, cy}, t), m6{1, 0, 0, 1, -cx, -cy})
			}
		case "skewx":
			t[2] = math.Tan(g(0, 0) * math.Pi / 180)
		case "skewy":
			t[1] = math.Tan(g(0, 0) * math.Pi / 180)
		case "matrix":
			t = m6{g(0, 1), g(1, 0), g(2, 0), g(3, 1), g(4, 0), g(5, 0)}
		}
		m = mmul(m, t)
	}
	return m
}

type bbox struct {
	x0, y0, x1, y1 float64
	ok             bool
}

func (b *bbox) add(x, y float64) {
	if !b.ok {
		b.x0, b.x1, b.y0, b.y1, b.ok = x, x, y, y, true
		return
	}
	b.x0, b.x1 = math.Min(b.x0, x), math.Max(b.x1, x)
	b.y0, b.y1 = math.Min(b.y0, y), math.Max(b.y1, y)
}

func (b *bbox) union(o bbox) {
	if !o.ok {
		return
	}
	b.add(o.x0, o.y0)
	b.add(o.x1, o.y1)
}

// addTransformed adds the corners of a user-space box transformed by m.
func (b *bbox) addRect(m m6, x0, y0, x1, y1 float64) {
	for _, p := range [][2]float64{{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}} {
		b.add(m[0]*p[0]+m[2]*p[1]+m[4], m[1]*p[0]+m[3]*p[1]+m[5])
	}
}

func (c *ctx) svgGeometry(b *lbox) {
	n := b.n
	doc := c.svgDocFor(n, b.st)
	if doc == nil {
		doc = &SVGDoc{dom: map[*html.Node]*SVGNode{}}
	}
	base, vbw, vbh := c.svgBaseMatrix(b)
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c.svgElem(ch, base, vbw, vbh, doc)
	}
}

// svgBaseMatrix maps the user space of an inline <svg> (viewBox and
// preserveAspectRatio applied) to page coordinates; vbw/vbh are the user-space
// viewport dimensions.
func (c *ctx) svgBaseMatrix(b *lbox) (base m6, vbw, vbh float64) {
	n := b.n
	cr := c.contentRect0(b)
	w, h := cr.W, cr.H
	vbx, vby := 0.0, 0.0
	vbw, vbh = w, h
	if vb := strings.Fields(strings.ReplaceAll(attr(n, "viewBox"), ",", " ")); len(vb) == 4 {
		vbx, vby = svgNum(vb[0], 0), svgNum(vb[1], 0)
		vbw, vbh = svgNum(vb[2], w), svgNum(vb[3], h)
	}
	base = m6{1, 0, 0, 1, cr.X, cr.Y}
	if vbw > 0 && vbh > 0 {
		sx, sy := w/vbw, h/vbh
		par := strings.Fields(attr(n, "preserveAspectRatio"))
		if len(par) > 0 && par[0] == "none" {
			base = mmul(base, m6{sx, 0, 0, sy, -vbx * sx, -vby * sy})
		} else {
			s := math.Min(sx, sy)
			slice := len(par) > 1 && par[1] == "slice"
			if slice {
				s = math.Max(sx, sy)
			}
			ox, oy := (w-vbw*s)/2, (h-vbh*s)/2
			if len(par) > 0 {
				switch {
				case strings.HasPrefix(par[0], "xMin"):
					ox = 0
				case strings.HasPrefix(par[0], "xMax"):
					ox = w - vbw*s
				}
				switch {
				case strings.Contains(par[0], "YMin"):
					oy = 0
				case strings.Contains(par[0], "YMax"):
					oy = h - vbh*s
				}
			}
			base = mmul(base, m6{s, 0, 0, s, ox - vbx*s, oy - vby*s})
		}
	}
	return base, vbw, vbh
}

func (c *ctx) contentRect0(b *lbox) Rect {
	return Rect{b.ax + b.bor[3] + b.pad[3], b.ay + b.bor[0] + b.pad[0], b.w - b.hExtra(), b.h - b.vExtra()}
}

// svgElem computes the box of one SVG element (and descendants) under m.
func (c *ctx) svgElem(n *html.Node, m m6, vw, vh float64, doc *SVGDoc) bbox {
	var bb bbox
	if n.Type != html.ElementNode {
		return bb
	}
	if t, _ := svgProp(doc, n, "transform"); t != "" && t != "none" {
		m = mmul(m, parseSVGTransform(t))
	}
	g := func(k string) float64 { return svgLen(attr(n, k), vw, 0) }
	tag := strings.ToLower(n.Data)
	switch tag {
	case "foreignobject":
		x, y := g("x"), svgLen(attr(n, "y"), vh, 0)
		w, h := g("width"), svgLen(attr(n, "height"), vh, 0)
		bb.addRect(m, x, y, x+w, y+h)
		if _, laid := c.styles[n]; !laid {
			c.setSVGBox(n, bb)
		}
		return bb
	case "rect", "image":
		x, y := g("x"), svgLen(attr(n, "y"), vh, 0)
		w, h := g("width"), svgLen(attr(n, "height"), vh, 0)
		bb.addRect(m, x, y, x+w, y+h)
	case "circle":
		cx, cy, r := g("cx"), svgLen(attr(n, "cy"), vh, 0), svgLen(attr(n, "r"), math.Hypot(vw, vh)/math.Sqrt2, 0)
		bb.addRect(m, cx-r, cy-r, cx+r, cy+r)
	case "ellipse":
		cx, cy, rx, ry := g("cx"), svgLen(attr(n, "cy"), vh, 0), g("rx"), svgLen(attr(n, "ry"), vh, 0)
		bb.addRect(m, cx-rx, cy-ry, cx+rx, cy+ry)
	case "line":
		bb.add(m[0]*g("x1")+m[2]*svgLen(attr(n, "y1"), vh, 0)+m[4], m[1]*g("x1")+m[3]*svgLen(attr(n, "y1"), vh, 0)+m[5])
		bb.add(m[0]*g("x2")+m[2]*svgLen(attr(n, "y2"), vh, 0)+m[4], m[1]*g("x2")+m[3]*svgLen(attr(n, "y2"), vh, 0)+m[5])
	case "polygon", "polyline":
		pts := strings.FieldsFunc(attr(n, "points"), func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' })
		for i := 0; i+1 < len(pts); i += 2 {
			x, y := svgNum(pts[i], 0), svgNum(pts[i+1], 0)
			bb.add(m[0]*x+m[2]*y+m[4], m[1]*x+m[3]*y+m[5])
		}
	case "path":
		svgPathBBox(attr(n, "d"), m, &bb)
	case "use":
		bb = c.svgUse(n, m, vw, vh, doc)
	case "text":
		bb = c.svgText(n, m, vw, vh, doc)
		return bb
	case "tspan":
		return bb
	case "svg":
		x, y, w, h := g("x"), svgLen(attr(n, "y"), vh, 0), svgLen(attr(n, "width"), vw, vw), svgLen(attr(n, "height"), vh, vh)
		bb.addRect(m, x, y, x+w, y+h)
		if vb := strings.Fields(strings.ReplaceAll(attr(n, "viewBox"), ",", " ")); len(vb) == 4 {
			vbw, vbh := svgNum(vb[2], w), svgNum(vb[3], h)
			if vbw > 0 && vbh > 0 {
				s := math.Min(w/vbw, h/vbh)
				mm := mmul(m, m6{s, 0, 0, s, x - svgNum(vb[0], 0)*s, y - svgNum(vb[1], 0)*s})
				for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
					c.svgElem(ch, mm, vbw, vbh, doc)
				}
			}
		}
		c.setSVGBox(n, bb)
		return bb
	case "g", "a", "switch", "symbol", "marker", "defs", "clippath", "mask", "pattern", "lineargradient", "radialgradient":
		if tag == "defs" || tag == "clippath" || tag == "mask" || tag == "pattern" || tag == "lineargradient" || tag == "radialgradient" || tag == "marker" || tag == "symbol" {
			for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
				c.svgElem(ch, m, vw, vh, doc)
			}
			c.res.Boxes[n] = &Box{Display: "inline", Visible: true, Transform: [6]float64{1, 0, 0, 1, 0, 0}}
			return bb
		}
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		if ch.Type == html.ElementNode && n.Data != "text" {
			bb.union(c.svgElem(ch, m, vw, vh, doc))
		} else if ch.Type == html.ElementNode {
			bb.union(c.svgElem(ch, m, vw, vh, doc))
		}
	}
	c.setSVGBox(n, bb)
	return bb
}

func (c *ctx) setSVGBox(n *html.Node, bb bbox) {
	bx := &Box{Display: "inline", Visible: true, Transform: [6]float64{1, 0, 0, 1, 0, 0}}
	if bb.ok {
		bx.Border = Rect{bb.x0, bb.y0, bb.x1 - bb.x0, bb.y1 - bb.y0}
		bx.Padding, bx.Content = bx.Border, bx.Border
		bx.ClientW, bx.ClientH = bx.Border.W, bx.Border.H
	}
	c.res.Boxes[n] = bx
}

// svgPathBBox adds the exact bounding box of path data d transformed by m.
func svgPathBBox(d string, m m6, bb *bbox) {
	toks := tokenizePath(d)
	var cx, cy, sx, sy, lcx, lcy float64
	var cmd byte
	i := 0
	num := func() (float64, bool) {
		if i < len(toks) && toks[i].isNum {
			v := toks[i].v
			i++
			return v, true
		}
		return 0, false
	}
	tp := func(x, y float64) { bb.add(m[0]*x+m[2]*y+m[4], m[1]*x+m[3]*y+m[5]) }
	cubic := func(x0, y0, x1, y1, x2, y2, x3, y3 float64) {
		tp(x3, y3)
		// extrema of each axis
		for axis := 0; axis < 2; axis++ {
			var p0, p1, p2, p3 float64
			if axis == 0 {
				p0, p1, p2, p3 = x0, x1, x2, x3
			} else {
				p0, p1, p2, p3 = y0, y1, y2, y3
			}
			a := -p0 + 3*p1 - 3*p2 + p3
			bq := 2 * (p0 - 2*p1 + p2)
			cq := p1 - p0
			var ts []float64
			if math.Abs(a) < 1e-12 {
				if math.Abs(bq) > 1e-12 {
					ts = append(ts, -cq/bq)
				}
			} else {
				disc := bq*bq - 4*a*cq
				if disc >= 0 {
					sq := math.Sqrt(disc)
					ts = append(ts, (-bq+sq)/(2*a), (-bq-sq)/(2*a))
				}
			}
			for _, t := range ts {
				if t > 0 && t < 1 {
					mt := 1 - t
					x := mt*mt*mt*x0 + 3*mt*mt*t*x1 + 3*mt*t*t*x2 + t*t*t*x3
					y := mt*mt*mt*y0 + 3*mt*mt*t*y1 + 3*mt*t*t*y2 + t*t*t*y3
					tp(x, y)
				}
			}
		}
	}
	for i < len(toks) {
		if !toks[i].isNum {
			cmd = toks[i].c
			i++
			if cmd == 'Z' || cmd == 'z' {
				cx, cy = sx, sy
				continue
			}
		} else if cmd == 0 {
			i++
			continue
		}
		rel := cmd >= 'a' && cmd <= 'z'
		up := cmd
		if rel {
			up = cmd - 32
		}
		ox, oy := 0.0, 0.0
		if rel {
			ox, oy = cx, cy
		}
		switch up {
		case 'M':
			x, ok1 := num()
			y, ok2 := num()
			if !ok1 || !ok2 {
				return
			}
			cx, cy = ox+x, oy+y
			sx, sy = cx, cy
			tp(cx, cy)
			if rel {
				cmd = 'l'
			} else {
				cmd = 'L'
			}
			lcx, lcy = cx, cy
		case 'L':
			x, ok1 := num()
			y, ok2 := num()
			if !ok1 || !ok2 {
				return
			}
			cx, cy = ox+x, oy+y
			tp(cx, cy)
			lcx, lcy = cx, cy
		case 'H':
			x, ok := num()
			if !ok {
				return
			}
			cx = ox + x
			if rel {
				cx = ox + x
			}
			tp(cx, cy)
			lcx, lcy = cx, cy
		case 'V':
			y, ok := num()
			if !ok {
				return
			}
			cy = oy + y
			tp(cx, cy)
			lcx, lcy = cx, cy
		case 'C':
			var v [6]float64
			for k := 0; k < 6; k++ {
				x, ok := num()
				if !ok {
					return
				}
				v[k] = x
			}
			x1, y1, x2, y2, x3, y3 := ox+v[0], oy+v[1], ox+v[2], oy+v[3], ox+v[4], oy+v[5]
			cubic(cx, cy, x1, y1, x2, y2, x3, y3)
			lcx, lcy = x2, y2
			cx, cy = x3, y3
		case 'S':
			var v [4]float64
			for k := 0; k < 4; k++ {
				x, ok := num()
				if !ok {
					return
				}
				v[k] = x
			}
			x1, y1 := 2*cx-lcx, 2*cy-lcy
			x2, y2, x3, y3 := ox+v[0], oy+v[1], ox+v[2], oy+v[3]
			cubic(cx, cy, x1, y1, x2, y2, x3, y3)
			lcx, lcy = x2, y2
			cx, cy = x3, y3
		case 'Q', 'T':
			var x1, y1, x2, y2 float64
			if up == 'Q' {
				var v [4]float64
				for k := 0; k < 4; k++ {
					x, ok := num()
					if !ok {
						return
					}
					v[k] = x
				}
				x1, y1, x2, y2 = ox+v[0], oy+v[1], ox+v[2], oy+v[3]
			} else {
				x, ok1 := num()
				y, ok2 := num()
				if !ok1 || !ok2 {
					return
				}
				x1, y1 = 2*cx-lcx, 2*cy-lcy
				x2, y2 = ox+x, oy+y
			}
			// elevate to cubic
			c1x, c1y := cx+2.0/3*(x1-cx), cy+2.0/3*(y1-cy)
			c2x, c2y := x2+2.0/3*(x1-x2), y2+2.0/3*(y1-y2)
			cubic(cx, cy, c1x, c1y, c2x, c2y, x2, y2)
			lcx, lcy = x1, y1
			cx, cy = x2, y2
		case 'A':
			var v [7]float64
			for k := 0; k < 7; k++ {
				x, ok := num()
				if !ok {
					return
				}
				v[k] = x
			}
			ex, ey := ox+v[5], oy+v[6]
			arcBBox(cx, cy, v[0], v[1], v[2], v[3] != 0, v[4] != 0, ex, ey, tp)
			cx, cy = ex, ey
			lcx, lcy = cx, cy
		default:
			i++
		}
	}
}

type ptok struct {
	isNum bool
	v     float64
	c     byte
}

func tokenizePath(d string) []ptok {
	var out []ptok
	i := 0
	for i < len(d) {
		ch := d[i]
		switch {
		case ch == ' ' || ch == ',' || ch == '\n' || ch == '\t' || ch == '\r':
			i++
		case ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z':
			out = append(out, ptok{c: ch})
			i++
		default:
			j := i
			if j < len(d) && (d[j] == '-' || d[j] == '+') {
				j++
			}
			seenDot := false
			for j < len(d) {
				if d[j] >= '0' && d[j] <= '9' {
					j++
				} else if d[j] == '.' && !seenDot {
					seenDot = true
					j++
				} else if (d[j] == 'e' || d[j] == 'E') && j+1 < len(d) {
					j++
					if d[j] == '-' || d[j] == '+' {
						j++
					}
				} else {
					break
				}
			}
			if j == i {
				i++
				continue
			}
			v, _ := strconv.ParseFloat(d[i:j], 64)
			out = append(out, ptok{isNum: true, v: v})
			i = j
		}
	}
	return out
}

// arcBBox samples an SVG elliptical arc.
func arcBBox(x1, y1, rx, ry, phi float64, large, sweep bool, x2, y2 float64, tp func(x, y float64)) {
	arcSample(x1, y1, rx, ry, phi, large, sweep, x2, y2, 64, tp)
}

// arcSample samples an SVG elliptical arc with the given number of steps.
func arcSample(x1, y1, rx, ry, phi float64, large, sweep bool, x2, y2 float64, steps int, tp func(x, y float64)) {
	if rx == 0 || ry == 0 || (x1 == x2 && y1 == y2) {
		tp(x2, y2)
		return
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	ph := phi * math.Pi / 180
	cp, sp := math.Cos(ph), math.Sin(ph)
	dx, dy := (x1-x2)/2, (y1-y2)/2
	x1p, y1p := cp*dx+sp*dy, -sp*dx+cp*dy
	lam := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry)
	if lam > 1 {
		s := math.Sqrt(lam)
		rx, ry = rx*s, ry*s
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	co := 0.0
	if den != 0 && num > 0 {
		co = math.Sqrt(num / den)
	}
	if large == sweep {
		co = -co
	}
	cxp, cyp := co*rx*y1p/ry, -co*ry*x1p/rx
	cx, cy := cp*cxp-sp*cyp+(x1+x2)/2, sp*cxp+cp*cyp+(y1+y2)/2
	ang := func(ux, uy, vx, vy float64) float64 {
		a := math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy)
		return a
	}
	t1 := ang(1, 0, (x1p-cxp)/rx, (y1p-cyp)/ry)
	dt := ang((x1p-cxp)/rx, (y1p-cyp)/ry, (-x1p-cxp)/rx, (-y1p-cyp)/ry)
	if !sweep && dt > 0 {
		dt -= 2 * math.Pi
	} else if sweep && dt < 0 {
		dt += 2 * math.Pi
	}
	for k := 0; k <= steps; k++ {
		t := t1 + dt*float64(k)/float64(steps)
		x := cx + rx*math.Cos(t)*cp - ry*math.Sin(t)*sp
		y := cy + rx*math.Cos(t)*sp + ry*math.Sin(t)*cp
		tp(x, y)
	}
}
