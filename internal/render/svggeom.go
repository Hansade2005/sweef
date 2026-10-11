package render

import (
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

type svgGRun struct {
	x, y, w, asc, desc float64
	owners             []*html.Node
	chunk              int
}

// svgText lays out a <text> element (and its tspans) the way Chromium does
// for client rects: runs advance a pen, x/y/dx/dy reposition it, and
// text-anchor shifts each absolutely positioned chunk. Boxes are set for the
// text element and every tspan; the text bbox is returned.
func (c *ctx) svgText(n *html.Node, m m6, vw, vh float64, doc *SVGDoc) bbox {
	var runs []svgGRun
	px, py := 0.0, 0.0
	chunk := 0
	prevSpace := true
	var owners []*html.Node
	var walk func(el *html.Node)
	numList := func(el *html.Node, k string) []float64 {
		v, _ := svgProp(doc, el, k)
		if v == "" {
			return nil
		}
		var out []float64
		for _, f := range strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
			out = append(out, svgLen(f, vw, 0))
		}
		return out
	}
	styleOf := func(el *html.Node) *style.Computed {
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
		return &cp
	}
	walk = func(el *html.Node) {
		xs, ys := numList(el, "x"), numList(el, "y")
		dxs, dys := numList(el, "dx"), numList(el, "dy")
		if len(xs) > 0 {
			chunk++
			px = xs[0]
		}
		if len(ys) > 0 {
			py = ys[0]
		}
		if len(dxs) > 0 {
			px += dxs[0]
		}
		if len(dys) > 0 {
			py += dys[0]
		}
		owners = append(owners, el)
		for ch := el.FirstChild; ch != nil; ch = ch.NextSibling {
			switch {
			case ch.Type == html.TextNode:
				t := collapseSpaces(ch.Data)
				if prevSpace && strings.HasPrefix(t, " ") {
					t = t[1:]
				}
				if t == "" {
					continue
				}
				prevSpace = strings.HasSuffix(t, " ")
				st := styleOf(el)
				fs := c.spec(st)
				w := c.svgAdvance(fs, t)
				runs = append(runs, svgGRun{x: px, y: py, w: w, asc: fs.asc, desc: fs.desc, owners: append([]*html.Node(nil), owners...), chunk: chunk})
				px += w
			case ch.Type == html.ElementNode && (ch.Data == "tspan" || ch.Data == "a" || ch.Data == "textpath"):
				walk(ch)
			}
		}
		owners = owners[:len(owners)-1]
	}
	walk(n)
	// trailing space of the last run is not rendered
	// anchors per chunk
	type span struct{ lo, hi float64 }
	spans := map[int]*span{}
	for _, r := range runs {
		s := spans[r.chunk]
		if s == nil {
			spans[r.chunk] = &span{r.x, r.x + r.w}
			continue
		}
		if r.x < s.lo {
			s.lo = r.x
		}
		if r.x+r.w > s.hi {
			s.hi = r.x + r.w
		}
	}
	boxes := map[*html.Node]*bbox{}
	var all bbox
	for _, r := range runs {
		s := spans[r.chunk]
		off := 0.0
		// anchor of the chunk's first owner (the text/tspan that started it)
		switch svgInh(doc, r.owners[len(r.owners)-1], "text-anchor") {
		case "middle":
			off = -(s.hi - s.lo) / 2
		case "end":
			off = -(s.hi - s.lo)
		}
		for _, o := range r.owners {
			b := boxes[o]
			if b == nil {
				b = &bbox{}
				boxes[o] = b
			}
			b.addRect(m, r.x+off, r.y-r.asc, r.x+off+r.w, r.y+r.desc)
		}
		all.addRect(m, r.x+off, r.y-r.asc, r.x+off+r.w, r.y+r.desc)
	}
	for o, b := range boxes {
		if o != n {
			c.setSVGBox(o, *b)
		}
	}
	c.setSVGBox(n, all)
	return all
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
