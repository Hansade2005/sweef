package render

import (
	"math"
	"strconv"
	"strings"

	"golang.org/x/net/html"

	"github.com/sweefbrowser/sweef/internal/style"
)

// CSS clip-path and mask painting: resolves the style values against the
// element's boxes and emits PushClipPath / PushMask ops (see ops_clip.go).

// refBoxRect returns the reference/painting box of b named by a CSS geometry
// box keyword together with its corner radii.
func (p *painter) refBoxRect(b *lbox, box string) (Rect, Radii) {
	r := rectOf(b)
	rad := p.radii(b)
	switch box {
	case "margin-box":
		o := Rect{r.X - b.mar[3], r.Y - b.mar[0], r.W + b.mar[1] + b.mar[3], r.H + b.mar[0] + b.mar[2]}
		var out Radii
		m := [4][2]float64{{b.mar[3], b.mar[0]}, {b.mar[1], b.mar[0]}, {b.mar[1], b.mar[2]}, {b.mar[3], b.mar[2]}}
		for i := range rad {
			if rad[i][0] > 0 && rad[i][1] > 0 {
				out[i] = [2]float64{rad[i][0] + m[i][0], rad[i][1] + m[i][1]}
			}
		}
		return o, out
	case "padding-box":
		return Rect{r.X + b.bor[3], r.Y + b.bor[0], math.Max(0, r.W-b.bor[1]-b.bor[3]), math.Max(0, r.H-b.bor[0]-b.bor[2])}, shrinkRadii(rad, b.bor)
	case "content-box", "fill-box":
		in := [4]float64{b.bor[0] + b.pad[0], b.bor[1] + b.pad[1], b.bor[2] + b.pad[2], b.bor[3] + b.pad[3]}
		return Rect{r.X + in[3], r.Y + in[0], math.Max(0, r.W-in[1]-in[3]), math.Max(0, r.H-in[0]-in[2])}, shrinkRadii(rad, in)
	}
	return r, rad
}

// scaleRadiiTo reduces radii that overlap on a w x h box (CSS border-radius rules).
func scaleRadiiTo(r Radii, w, h float64) Radii {
	f := 1.0
	chk := func(sum, lim float64) {
		if sum > 0 && lim/sum < f {
			f = lim / sum
		}
	}
	chk(r[0][0]+r[1][0], w)
	chk(r[3][0]+r[2][0], w)
	chk(r[0][1]+r[3][1], h)
	chk(r[1][1]+r[2][1], h)
	if f < 1 {
		for i := range r {
			r[i][0] *= f
			r[i][1] *= f
		}
	}
	return r
}

func resolveRadii(rr [4][2]style.Length, w, h float64) Radii {
	var out Radii
	for i := 0; i < 4; i++ {
		x, y := rr[i][0].Resolve(w), rr[i][1].Resolve(h)
		if x > 0 && y > 0 {
			out[i] = [2]float64{x, y}
		}
	}
	return scaleRadiiTo(out, w, h)
}

// shapeSub resolves a basic shape against the reference box R.
func shapeSub(sh *style.BasicShape, R Rect) ClipSub {
	switch sh.Kind {
	case "inset", "rect", "xywh":
		var x0, y0, x1, y1 float64
		switch sh.Kind {
		case "inset":
			t, r, bt, l := sh.Edges[0].Resolve(R.H), sh.Edges[1].Resolve(R.W), sh.Edges[2].Resolve(R.H), sh.Edges[3].Resolve(R.W)
			x0, y0, x1, y1 = R.X+l, R.Y+t, R.X+R.W-r, R.Y+R.H-bt
		case "rect":
			x0, y0, x1, y1 = R.X, R.Y, R.X+R.W, R.Y+R.H
			if sh.Edges[3].K != style.LAuto {
				x0 = R.X + sh.Edges[3].Resolve(R.W)
			}
			if sh.Edges[0].K != style.LAuto {
				y0 = R.Y + sh.Edges[0].Resolve(R.H)
			}
			if sh.Edges[1].K != style.LAuto {
				x1 = R.X + sh.Edges[1].Resolve(R.W)
			}
			if sh.Edges[2].K != style.LAuto {
				y1 = R.Y + sh.Edges[2].Resolve(R.H)
			}
		default:
			x0, y0 = R.X+sh.Edges[0].Resolve(R.W), R.Y+sh.Edges[1].Resolve(R.H)
			x1, y1 = x0+sh.Edges[2].Resolve(R.W), y0+sh.Edges[3].Resolve(R.H)
		}
		if x1 < x0 {
			x1 = x0
		}
		if y1 < y0 {
			y1 = y0
		}
		rc := Rect{x0, y0, x1 - x0, y1 - y0}
		var rad Radii
		if sh.HasRound {
			rad = resolveRadii(sh.Round, rc.W, rc.H)
		}
		return ClipSub{Kind: "rrect", Rect: rc, Radii: rad}
	case "circle", "ellipse":
		cx, cy := R.X+sh.Pos[0].Resolve(R.W), R.Y+sh.Pos[1].Resolve(R.H)
		dl, dr, dt, db := math.Abs(cx-R.X), math.Abs(R.X+R.W-cx), math.Abs(cy-R.Y), math.Abs(R.Y+R.H-cy)
		if sh.Kind == "circle" {
			var rr float64
			switch sh.RKw[0] {
			case "closest-side":
				rr = math.Min(math.Min(dl, dr), math.Min(dt, db))
			case "farthest-side":
				rr = math.Max(math.Max(dl, dr), math.Max(dt, db))
			default:
				rr = sh.R[0].Resolve(math.Sqrt((R.W*R.W + R.H*R.H) / 2))
			}
			return ClipSub{Kind: "ellipse", CX: cx, CY: cy, RX: rr, RY: rr}
		}
		radius := func(i int, lo, hi, base float64) float64 {
			switch sh.RKw[i] {
			case "closest-side":
				return lo
			case "farthest-side":
				return hi
			}
			return sh.R[i].Resolve(base)
		}
		rx := radius(0, math.Min(dl, dr), math.Max(dl, dr), R.W)
		ry := radius(1, math.Min(dt, db), math.Max(dt, db), R.H)
		return ClipSub{Kind: "ellipse", CX: cx, CY: cy, RX: rx, RY: ry}
	case "polygon":
		pts := make([][2]float64, len(sh.Pts))
		for i, q := range sh.Pts {
			pts[i] = [2]float64{R.X + q[0].Resolve(R.W), R.Y + q[1].Resolve(R.H)}
		}
		return ClipSub{Kind: "polygon", Pts: pts, EvenOdd: sh.Evenod}
	case "path":
		return ClipSub{Kind: "path", D: sh.D, M: [6]float64{1, 0, 0, 1, R.X, R.Y}, HasM: true, EvenOdd: sh.Evenod}
	}
	return ClipSub{Kind: "rrect"}
}

// capture runs f and returns the ops it emitted instead of emitting them.
func (p *painter) capture(f func()) []Op {
	saved := p.ops
	p.ops = nil
	f()
	out := p.ops
	p.ops = saved
	return out
}

// clipMaskBegin opens the clip-path and mask of b (inside its transform) and
// returns the function that closes them; nil when b has neither.
func (p *painter) clipMaskBegin(b *lbox) func() {
	st := b.st
	var closers []func()
	if cp := st.ClipPathV; cp != nil {
		R, rad := p.refBoxRect(b, cp.Box)
		bounds := Rect{b.ovLeft, b.ovTop, b.ovRight - b.ovLeft, b.ovBottom - b.ovTop}
		switch {
		case cp.Shape != nil:
			sub := shapeSub(cp.Shape, R)
			p.emit(PushClipPath{Subs: []ClipSub{sub}, Bounds: bounds})
			p.pushHitShape(sub)
			closers = append(closers, func() { p.emit(PopClipPath{}); p.clips = p.clips[:len(p.clips)-1] })
		case cp.URL != "":
			if ops, ok := p.svgRefOps(b, cp.URL, R, "clippath"); ok {
				p.emit(PushMask{Bounds: bounds, Layers: []MaskLayerOps{{Ops: ops, Mode: "alpha", Composite: "add"}}})
				closers = append(closers, func() { p.emit(PopMask{}) })
			}
		default:
			sub := ClipSub{Kind: "rrect", Rect: R, Radii: rad}
			p.emit(PushClipPath{Subs: []ClipSub{sub}, Bounds: bounds})
			p.pushHitShape(sub)
			closers = append(closers, func() { p.emit(PopClipPath{}); p.clips = p.clips[:len(p.clips)-1] })
		}
	}
	if st.MaskImage && len(st.Mask) > 0 {
		if pm, ok := p.maskOp(b); ok {
			p.emit(pm)
			closers = append(closers, func() { p.emit(PopMask{}) })
		}
	}
	if len(closers) == 0 {
		return nil
	}
	return func() {
		for i := len(closers) - 1; i >= 0; i-- {
			closers[i]()
		}
	}
}

func (p *painter) maskOp(b *lbox) (PushMask, bool) {
	st := b.st
	pm := PushMask{}
	var bounds Rect
	first := true
	big := Rect{b.ovLeft, b.ovTop, b.ovRight - b.ovLeft, b.ovBottom - b.ovTop}
	for _, ml := range st.Mask {
		if ml.Image.IsNone() {
			continue
		}
		var clipBox Rect
		var clipRad Radii
		if ml.Clip == "no-clip" {
			clipBox = big
		} else {
			clipBox, clipRad = p.refBoxRect(b, ml.Clip)
		}
		pa, _ := p.refBoxRect(b, ml.Origin)
		mode := ml.Mode
		var ops []Op
		if strings.HasPrefix(ml.Image.URL, "#") {
			// an unresolvable reference is a transparent-black layer
			ops, _ = p.svgRefOps(b, ml.Image.URL, pa, "mask")
			mode = "alpha"
		} else {
			ops = p.capture(func() { p.paintBgLayer(b, ml.BgLayer, clipBox, clipRad, pa) })
			if mode == "" || mode == "match-source" {
				mode = "alpha"
			}
		}
		comp := ml.Composite
		if comp == "" {
			comp = "add"
		}
		pm.Layers = append(pm.Layers, MaskLayerOps{Ops: ops, Mode: mode, Composite: comp})
		if first {
			bounds, first = clipBox, false
		} else {
			bounds = bounds.union(clipBox)
		}
	}
	if len(pm.Layers) == 0 {
		return pm, false
	}
	pm.Bounds = bounds
	return pm, true
}

// svgRefOps renders a same-document SVG <clipPath> or <mask> (ref "#id") over
// the reference rectangle R as a layer of SVG ops that paints an opaque black
// rectangle through the referenced element.
func (p *painter) svgRefOps(b *lbox, ref string, R Rect, want string) ([]Op, bool) {
	i := strings.LastIndexByte(ref, '#')
	if i < 0 {
		return nil, false
	}
	id := ref[i+1:]
	if id == "" || i > 0 {
		return nil, false
	}
	node := p.c.elementByID(id)
	if node == nil || strings.ToLower(node.Data) != want {
		return nil, false
	}
	root := node
	for a := node.Parent; a != nil; a = a.Parent {
		if a.Type == html.ElementNode && strings.ToLower(a.Data) == "svg" {
			root = a
		}
	}
	if strings.ToLower(root.Data) != "svg" {
		return nil, false
	}
	base := p.c.svgDocFor(root, b.st)
	if base == nil || base.ByID[id] == nil {
		return nil, false
	}
	const pad = 256.0
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	attrs := map[string]string{"x": "0", "y": "0", "width": f(R.W), "height": f(R.H), "fill": "#000"}
	if want == "mask" {
		attrs["mask"] = "url(#" + id + ")"
	} else {
		attrs["clip-path"] = "url(#" + id + ")"
	}
	rect := &SVGNode{Tag: "rect", A: attrs}
	rootAttrs := map[string]string{"viewbox": f(-pad) + " " + f(-pad) + " " + f(R.W+2*pad) + " " + f(R.H+2*pad), "overflow": "visible"}
	doc := &SVGDoc{Root: &SVGNode{Tag: "svg", A: rootAttrs, Kids: []*SVGNode{rect}}, ByID: base.ByID, Inherit: base.Inherit, dom: base.dom, base: base.base}
	dst := Rect{R.X - pad, R.Y - pad, R.W + 2*pad, R.H + 2*pad}
	op := p.svgDraw(b, dst, doc)
	return []Op{op}, true
}

// elementByID finds the first element with the given id attribute.
func (c *ctx) elementByID(id string) *html.Node {
	var found *html.Node
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "id" && a.Val == id {
					found = n
					return
				}
			}
		}
		for ch := n.FirstChild; ch != nil && found == nil; ch = ch.NextSibling {
			walk(ch)
		}
	}
	if c.docEl != nil {
		walk(c.docEl)
	}
	return found
}

// pushHitShape records a clip-path shape so hit testing ignores points outside it.
func (p *painter) pushHitShape(sub ClipSub) {
	r := Rect{-1e9, -1e9, 2e9, 2e9}
	switch sub.Kind {
	case "rrect":
		r = sub.Rect
	case "ellipse":
		r = Rect{sub.CX - sub.RX, sub.CY - sub.RY, 2 * sub.RX, 2 * sub.RY}
	case "polygon":
		if len(sub.Pts) > 0 {
			x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
			for _, q := range sub.Pts {
				x0, x1 = math.Min(x0, q[0]), math.Max(x1, q[0])
				y0, y1 = math.Min(y0, q[1]), math.Max(y1, q[1])
			}
			r = Rect{x0, y0, x1 - x0, y1 - y0}
		}
	}
	s := sub
	p.clips = append(append([]clipRec(nil), p.clips...), clipRec{r: r, shape: &s})
}

// clipShapeContains reports whether (x, y) lies inside the clip shape.
func clipShapeContains(s *ClipSub, x, y float64) bool {
	switch s.Kind {
	case "rrect":
		r := s.Rect
		if x < r.X || x >= r.Right() || y < r.Y || y >= r.Bottom() {
			return false
		}
		// corner arcs
		for i, c := range s.Radii {
			if c[0] <= 0 || c[1] <= 0 {
				continue
			}
			var cx, cy float64
			switch i {
			case 0:
				cx, cy = r.X+c[0], r.Y+c[1]
				if x >= cx || y >= cy {
					continue
				}
			case 1:
				cx, cy = r.Right()-c[0], r.Y+c[1]
				if x <= cx || y >= cy {
					continue
				}
			case 2:
				cx, cy = r.Right()-c[0], r.Bottom()-c[1]
				if x <= cx || y <= cy {
					continue
				}
			default:
				cx, cy = r.X+c[0], r.Bottom()-c[1]
				if x >= cx || y <= cy {
					continue
				}
			}
			dx, dy := (x-cx)/c[0], (y-cy)/c[1]
			if dx*dx+dy*dy > 1 {
				return false
			}
		}
		return true
	case "ellipse":
		if s.RX <= 0 || s.RY <= 0 {
			return false
		}
		dx, dy := (x-s.CX)/s.RX, (y-s.CY)/s.RY
		return dx*dx+dy*dy <= 1
	case "polygon":
		wind, cross := 0, 0
		n := len(s.Pts)
		for i := 0; i < n; i++ {
			a, b := s.Pts[i], s.Pts[(i+1)%n]
			if (a[1] <= y) != (b[1] <= y) {
				t := (y - a[1]) / (b[1] - a[1])
				if a[0]+t*(b[0]-a[0]) > x {
					cross++
					if b[1] > a[1] {
						wind++
					} else {
						wind--
					}
				}
			}
		}
		if s.EvenOdd {
			return cross%2 == 1
		}
		return wind != 0
	}
	return true
}

// zeroNonRenderedSVG gives the descendants of <defs>, <clipPath>, <mask>,
// <pattern>, <marker> and <symbol> empty boxes, as Chromium does: they are
// never rendered in place, so getBoundingClientRect reports zeros.
func (c *ctx) zeroNonRenderedSVG() {
	var walk func(n *html.Node, hidden bool)
	walk = func(n *html.Node, hidden bool) {
		if n.Type == html.ElementNode {
			if hidden {
				if b := c.res.Boxes[n]; b != nil && b.Display != "none" {
					b.Border, b.Padding, b.Content = Rect{}, Rect{}, Rect{}
					b.ClientW, b.ClientH, b.ScrollW, b.ScrollH = 0, 0, 0, 0
					b.Frags = nil
				}
			}
			switch strings.ToLower(n.Data) {
			case "defs", "clippath", "mask", "pattern", "marker", "symbol":
				if n.Namespace == "svg" || n.Namespace == "" {
					hidden = true
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch, hidden)
		}
	}
	if c.docEl != nil {
		walk(c.docEl, false)
	}
}
