package render

import (
	"math"
	"strings"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// svgInh looks an inherited SVG property up the DOM chain of n inside doc.
func svgInh(doc *SVGDoc, n *html.Node, k string) string {
	for a := n; a != nil && a.Type == html.ElementNode; a = a.Parent {
		if sn := doc.dom[a]; sn != nil {
			if v, ok := sn.A[k]; ok {
				return v
			}
		}
		if a.Data == "svg" && doc.dom[a] != nil && a.Parent != nil && doc.dom[a.Parent] == nil {
			break
		}
	}
	return doc.Inherit[k]
}

func svgProp(doc *SVGDoc, n *html.Node, k string) (string, bool) {
	if sn := doc.dom[n]; sn != nil {
		v, ok := sn.A[k]
		return v, ok
	}
	return attr(n, k), hasAttr(n, k)
}

// svgText lays out a <text> element (and its tspans / textPaths) the way
// Chromium does for client rects: addressable characters advance a pen,
// x/y/dx/dy/rotate reposition them, text-anchor shifts each chunk, textPath
// glyphs follow the path. Boxes are set for the text element and every
// content child; the text bbox is returned.
func (c *ctx) svgText(n *html.Node, m m6, vw, vh float64, doc *SVGDoc) bbox {
	var all bbox
	root := doc.dom[n]
	if root == nil {
		return all
	}
	styles := map[*html.Node]*style.Computed{}
	styleOf := func(el *html.Node) *style.Computed {
		if s, ok := styles[el]; ok {
			return s
		}
		cp := *c.eng.Anonymous(c.eng.InitialStyle(), "inline")
		if doc.base != nil {
			cp = *c.eng.Anonymous(doc.base, "inline")
		}
		if v := svgInh(doc, el, "font-size"); v != "" {
			cp.FontSize = svgLen(v, cp.FontSize, cp.FontSize)
			if strings.HasSuffix(v, "em") {
				cp.FontSize = svgNum(strings.TrimSuffix(v, "em"), 1) * cp.FontSize
			}
		}
		if v := svgInh(doc, el, "font-family"); v != "" {
			var fam []string
			for _, f := range strings.Split(v, ",") {
				fam = append(fam, strings.ToLower(strings.Trim(strings.TrimSpace(f), "\"'")))
			}
			cp.FontFamily = fam
		}
		if v := svgInh(doc, el, "font-weight"); v != "" {
			switch v {
			case "bold", "bolder":
				cp.FontWeight = 700
			case "normal":
				cp.FontWeight = 400
			default:
				if f := svgNum(v, 400); f > 0 {
					cp.FontWeight = int(f)
				}
			}
		}
		if v := svgInh(doc, el, "font-style"); v == "italic" || strings.HasPrefix(v, "oblique") {
			cp.FontStyle = 1
		}
		styles[el] = &cp
		return &cp
	}
	lenFn := func(nd *SVGNode, s string, axis byte) float64 {
		ref := vw
		if axis == 'y' {
			ref = vh
		}
		fs := 16.0
		if nd.dom != nil {
			fs = styleOf(nd.dom).FontSize
		}
		return svgLenFS(s, ref, fs)
	}
	chars, lens := CollectSVGText(root, lenFn)
	if len(chars) == 0 {
		c.setSVGBox(n, all)
		return all
	}
	paths := map[*SVGNode]*SVGTextPath{}
	for ci := 0; ci < len(chars); ci++ {
		ch := chars[ci]
		if ch.Path == nil || paths[ch.Path] != nil {
			continue
		}
		t := doc.ByID[strings.TrimPrefix(strings.TrimSpace(ch.Path.A["href"]), "#")]
		if t == nil || t.Tag != "path" {
			// Chromium lays such text out at y=0 and paints nothing
			for i := range chars {
				chars[i].Path = nil
				chars[i].HasY, chars[i].Y = i == 0, 0
			}
			paths = nil
			break
		}
		sp := &SVGTextPath{Contours: SVGPathContours(t.A["d"])}
		sp.Start = SVGParsePct(ch.Path.A["startoffset"], sp.Length())
		paths[ch.Path] = sp
	}
	ins := make([]SVGTextIn, len(chars))
	type metric struct{ asc, desc float64 }
	mets := make([]metric, len(chars))
	inks := make([][4]float64, len(chars))
	for i, ch := range chars {
		el := ch.Node.dom
		st := styleOf(el)
		fs := c.spec(st)
		adv := c.svgAdvance(fs, string(ch.R))
		inks[i] = c.svgInk(fs, ch.R)
		if v := svgInh(doc, el, "letter-spacing"); v != "" && v != "normal" {
			adv += svgLenFS(v, 0, st.FontSize)
		}
		if ch.R == ' ' {
			if v := svgInh(doc, el, "word-spacing"); v != "" && v != "normal" {
				adv += svgLenFS(v, 0, st.FontSize)
			}
		}
		mets[i] = metric{fs.asc, fs.desc}
		var off float64
		kind := svgInh(doc, el, "dominant-baseline")
		if v, _ := svgProp(doc, el, "alignment-baseline"); v != "" && v != "baseline" && v != "auto" {
			kind = v
		}
		off = svgBaselineOffset(kind, fs.asc, fs.desc, fs.xh)
		for a := ch.Node; a != nil; a = svgParentNode(doc, a) {
			if a.dom == nil {
				break
			}
			v, _ := svgProp(doc, a.dom, "baseline-shift")
			if a == root {
				if v == "" || v == "baseline" {
					break
				}
			}
			if v == "" || v == "baseline" {
				continue
			}
			as := styleOf(a.dom)
			af := c.spec(as)
			switch v {
			case "sub":
				off += (af.asc + af.desc) / 2
			case "super":
				off -= (af.asc + af.desc) / 2
			default:
				off -= svgLenFS(v, as.FontSize, as.FontSize)
			}
		}
		anchor := 0
		switch svgInh(doc, el, "text-anchor") {
		case "middle":
			anchor = 1
		case "end":
			anchor = 2
		}
		ins[i] = SVGTextIn{SVGTextChar: ch, Adv: adv, YOff: off, Anchor: anchor, Space: ch.R == ' '}
		if ch.Path != nil {
			ins[i].PathM = paths[ch.Path]
		}
	}
	pos := LayoutSVGText(ins, lens)
	boxes := map[*html.Node]*bbox{}
	for i, p := range pos {
		if p.Hidden {
			continue
		}
		s, cs := math.Sincos(p.Angle)
		var cell bbox
		ix0, iy0, ix1, iy1 := math.Min(0, inks[i][0]), math.Min(-mets[i].asc, inks[i][1]), math.Max(p.Adv, inks[i][2]), math.Max(mets[i].desc, inks[i][3])
		for _, pt := range [4][2]float64{{ix0, iy0}, {ix1, iy0}, {ix1, iy1}, {ix0, iy1}} {
			x := p.X + pt[0]*cs - pt[1]*s
			y := p.Y + pt[0]*s + pt[1]*cs
			cell.add(m[0]*x+m[2]*y+m[4], m[1]*x+m[3]*y+m[5])
		}
		all.union(cell)
		for a := chars[i].Node; a != nil && a != root; a = svgParentNode(doc, a) {
			if a.dom == nil {
				continue
			}
			b := boxes[a.dom]
			if b == nil {
				b = &bbox{}
				boxes[a.dom] = b
			}
			b.union(cell)
		}
	}
	for o, b := range boxes {
		c.setSVGBox(o, *b)
	}
	c.setSVGBox(n, all)
	return all
}

// svgParentNode returns the parent of a text content node within its scene.
func svgParentNode(doc *SVGDoc, n *SVGNode) *SVGNode {
	if n.dom == nil || n.dom.Parent == nil {
		return nil
	}
	return doc.dom[n.dom.Parent]
}

// svgLenFS parses a length with em / ex units relative to fs.
func svgLenFS(s string, ref, fs float64) float64 {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasSuffix(s, "rem"):
		return svgNum(strings.TrimSuffix(s, "rem"), 0) * 16
	case strings.HasSuffix(s, "em"):
		return svgNum(strings.TrimSuffix(s, "em"), 0) * fs
	case strings.HasSuffix(s, "ex"):
		return svgNum(strings.TrimSuffix(s, "ex"), 0) * fs / 2
	}
	return svgLen(s, ref, 0)
}

// svgBaselineOffset is the downward shift of the glyph origin for a baseline
// keyword (dominant-baseline / alignment-baseline).
func svgBaselineOffset(kind string, asc, desc, xh float64) float64 {
	switch kind {
	case "central":
		return (asc - desc) / 2
	case "middle":
		return xh / 2
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

// svgUse computes the bounding box of a <use> instance (content, not viewport)
// and leaves the boxes of the referenced original untouched.
func (c *ctx) svgUse(n *html.Node, m m6, vw, vh float64, doc *SVGDoc) bbox {
	var bb bbox
	href, _ := svgProp(doc, n, "href")
	if href == "" {
		href = attr(n, "xlink:href")
	}
	t := doc.ByID[strings.TrimPrefix(strings.TrimSpace(href), "#")]
	if sn := doc.dom[n]; sn != nil && sn.Sub != nil {
		// <use href="other.svg#id">: the target lives in another scene
		t = sn.Sub.Root
		if sn.SubID != "" {
			t = sn.Sub.ByID[sn.SubID]
		}
		doc = sn.Sub
	}
	if t == nil || t.dom == nil {
		return bb
	}
	x, y := svgLen(attr(n, "x"), vw, 0), svgLen(attr(n, "y"), vh, 0)
	m = mmul(m, m6{1, 0, 0, 1, x, y})
	// snapshot boxes of the target subtree so instancing does not move them
	saved := map[*html.Node]*Box{}
	var snap func(el *html.Node)
	snap = func(el *html.Node) {
		saved[el] = c.res.Boxes[el]
		for ch := el.FirstChild; ch != nil; ch = ch.NextSibling {
			if ch.Type == html.ElementNode {
				snap(ch)
			}
		}
	}
	snap(t.dom)
	defer func() {
		for el, b := range saved {
			if b == nil {
				delete(c.res.Boxes, el)
			} else {
				c.res.Boxes[el] = b
			}
		}
	}()
	if t.Tag == "symbol" || t.Tag == "svg" {
		w, h := vw, vh
		if v := attr(n, "width"); v != "" {
			w = svgLen(v, vw, w)
		} else if v := svgProp2(t, "width"); v != "" && t.Tag == "svg" {
			w = svgLen(v, vw, w)
		}
		if v := attr(n, "height"); v != "" {
			h = svgLen(v, vh, h)
		} else if v := svgProp2(t, "height"); v != "" && t.Tag == "svg" {
			h = svgLen(v, vh, h)
		}
		vbw, vbh := w, h
		mm := m
		if vb := strings.Fields(strings.ReplaceAll(t.A["viewbox"], ",", " ")); len(vb) == 4 {
			vx, vy := svgNum(vb[0], 0), svgNum(vb[1], 0)
			vbw, vbh = svgNum(vb[2], w), svgNum(vb[3], h)
			if vbw > 0 && vbh > 0 {
				sx, sy := w/vbw, h/vbh
				par := strings.Fields(t.A["preserveaspectratio"])
				if len(par) > 0 && par[0] == "none" {
					mm = mmul(m, m6{sx, 0, 0, sy, -vx * sx, -vy * sy})
				} else {
					s := minf(sx, sy)
					if len(par) > 1 && par[1] == "slice" {
						s = maxf(sx, sy)
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
						case strings.HasSuffix(par[0], "YMin"):
							oy = 0
						case strings.HasSuffix(par[0], "YMax"):
							oy = h - vbh*s
						}
					}
					mm = mmul(m, m6{s, 0, 0, s, ox - vx*s, oy - vy*s})
				}
			}
		}
		for ch := t.dom.FirstChild; ch != nil; ch = ch.NextSibling {
			bb.union(c.svgElem(ch, mm, vbw, vbh, doc))
		}
	} else {
		bb = c.svgElem(t.dom, m, vw, vh, doc)
	}
	c.setSVGBox(n, bb)
	return bb
}

func svgProp2(n *SVGNode, k string) string { return n.A[k] }

// svgAdvance is the unkerned linear advance of t (Chromium lays SVG text out
// with unhinted, unkerned advances).
func (c *ctx) svgAdvance(fs *fontSpec, t string) float64 {
	w := 0.0
	for _, r := range t {
		f := fs.faceFor(r)
		if f == nil {
			w += fs.size * 0.5
			continue
		}
		a := f.Advance(r, 2048) * fs.size / 2048
		if a < 0 {
			a = fs.size * 0.5
		}
		w += a
	}
	return w
}

// svgInk is the glyph's outline bounds relative to its origin rounded out to
// whole pixels (what Chromium unions into a text element's bounding box);
// empty for glyphs without ink.
func (c *ctx) svgInk(fs *fontSpec, r rune) [4]float64 {
	f := fs.faceFor(r)
	if f == nil || r == ' ' {
		return [4]float64{}
	}
	segs := f.Outline(r, fs.size)
	if len(segs) == 0 {
		return [4]float64{}
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	add := func(x, y float64) {
		x0, x1 = math.Min(x0, x), math.Max(x1, x)
		y0, y1 = math.Min(y0, y), math.Max(y1, y)
	}
	var cx, cy float64
	quadExt := func(p0, p1, p2 float64) (float64, bool) {
		d := p0 - 2*p1 + p2
		if math.Abs(d) < 1e-12 {
			return 0, false
		}
		t := (p0 - p1) / d
		if t <= 0 || t >= 1 {
			return 0, false
		}
		return t, true
	}
	for _, sg := range segs {
		switch sg.Op {
		case 'M', 'L':
			cx, cy = sg.X0, sg.Y0
			add(cx, cy)
		case 'Q':
			add(sg.X1, sg.Y1)
			for _, ax := range []int{0, 1} {
				var p0, p1, p2 float64
				if ax == 0 {
					p0, p1, p2 = cx, sg.X0, sg.X1
				} else {
					p0, p1, p2 = cy, sg.Y0, sg.Y1
				}
				if t, ok := quadExt(p0, p1, p2); ok {
					mt := 1 - t
					add(mt*mt*cx+2*mt*t*sg.X0+t*t*sg.X1, mt*mt*cy+2*mt*t*sg.Y0+t*t*sg.Y1)
				}
			}
			cx, cy = sg.X1, sg.Y1
		case 'C':
			add(sg.X0, sg.Y0)
			add(sg.X1, sg.Y1)
			add(sg.X2, sg.Y2)
			cx, cy = sg.X2, sg.Y2
		}
	}
	if math.IsInf(x0, 1) {
		return [4]float64{}
	}
	return [4]float64{math.Floor(x0), math.Floor(y0), math.Ceil(x1), math.Ceil(y1)}
}
