package raster

import (
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"net/url"
	"strings"
	"sync"
)

// ---- text --------------------------------------------------------------------------------

type placedRun struct {
	text   string
	st     sStyle
	x, y   float64
	w      float64
	asc    float64
	desc   float64
	anchor string
}

type textRunIn struct {
	text   string
	st     sStyle
	x, y   *float64
	dx, dy float64
	xs, ys []float64 // per-character absolute lists
}

func collapseWS(s string) string {
	var b strings.Builder
	sp := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !sp {
				b.WriteByte(' ')
			}
			sp = true
			continue
		}
		sp = false
		b.WriteRune(r)
	}
	return b.String()
}

func listNums(n *svgNode, k string) []float64 {
	v, ok := n.A[k]
	if !ok {
		return nil
	}
	return splitNums(v)
}

func (r *svgR) collectRuns(n *svgNode, st sStyle, out *[]textRunIn, first bool) {
	xs, ys := listNums(n, "x"), listNums(n, "y")
	dxs, dys := listNums(n, "dx"), listNums(n, "dy")
	var dx, dy float64
	if len(dxs) > 0 {
		dx = dxs[0]
	}
	if len(dys) > 0 {
		dy = dys[0]
	}
	pending := textRunIn{st: st, dx: dx, dy: dy}
	if len(xs) > 0 {
		x := xs[0]
		pending.x = &x
		if len(xs) > 1 {
			pending.xs = xs
		}
	}
	if len(ys) > 0 {
		y := ys[0]
		pending.y = &y
		if len(ys) > 1 {
			pending.ys = ys
		}
	}
	used := false
	for _, k := range n.Kids {
		switch k.Tag {
		case "#text":
			t := textRunIn{text: k.Text, st: st}
			if !used {
				t.x, t.y, t.dx, t.dy, t.xs, t.ys = pending.x, pending.y, pending.dx, pending.dy, pending.xs, pending.ys
				used = true
			}
			*out = append(*out, t)
		case "tspan", "a":
			if !used && (pending.x != nil || pending.y != nil || dx != 0 || dy != 0) {
				// positioning applies to the first text of the first child as well
			}
			ks := r.inherit(st, k.A)
			if k.A["display"] == "none" {
				continue
			}
			mark := len(*out)
			r.collectRuns(k, ks, out, false)
			if !used && mark < len(*out) && (pending.x != nil || pending.y != nil || dx != 0 || dy != 0) {
				t := &(*out)[mark]
				if t.x == nil {
					t.x = pending.x
				}
				if t.y == nil {
					t.y = pending.y
				}
				t.dx += pending.dx
				t.dy += pending.dy
			}
			used = true
		}
	}
}

func (r *svgR) fontOf(st *sStyle) string {
	it := ""
	if st.italic {
		it = "italic "
	}
	fam := st.family
	if fam == "" {
		fam = "serif"
	}
	return fmt.Sprintf("%s%d %.4gpx %s", it, st.weight, st.fontSize, fam)
}

func (r *svgR) layoutText(n *svgNode, st *sStyle) []placedRun {
	var runs []textRunIn
	r.collectRuns(n, *st, &runs, true)
	// whitespace collapsing across runs
	prevSpace := true
	for i := range runs {
		t := collapseWS(runs[i].text)
		if prevSpace && strings.HasPrefix(t, " ") {
			t = t[1:]
		}
		if t != "" {
			prevSpace = strings.HasSuffix(t, " ")
		}
		runs[i].text = t
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].text == "" {
			continue
		}
		runs[i].text = strings.TrimSuffix(runs[i].text, " ")
		break
	}
	c := r.c
	c.Save()
	defer c.Restore()
	var placed []placedRun
	px, py := 0.0, 0.0
	chunkStart := -1
	flush := func() {}
	_ = flush
	apply := func(from int) {
		if from < 0 || from >= len(placed) {
			return
		}
		a := placed[from].anchor
		if a != "middle" && a != "end" {
			return
		}
		var tot float64
		for _, p := range placed[from:] {
			tot += p.w
		}
		off := -tot
		if a == "middle" {
			off = -tot / 2
		}
		for i := from; i < len(placed); i++ {
			placed[i].x += off
		}
	}
	for _, ru := range runs {
		if ru.text == "" && ru.x == nil && ru.y == nil {
			continue
		}
		if ru.x != nil || ru.y != nil {
			if ru.x != nil {
				apply(chunkStart)
				chunkStart = len(placed)
				px = *ru.x
			}
			if ru.y != nil {
				py = *ru.y
			}
		}
		if chunkStart < 0 {
			chunkStart = len(placed)
		}
		px += ru.dx
		py += ru.dy
		c.SetFont(r.fontOf(&ru.st))
		ls := r.length(ru.st.letterSp, 0, ru.st.fontSize, 0)
		ws := r.length(ru.st.wordSp, 0, ru.st.fontSize, 0)
		c.SetLetterSpacing(ls)
		c.SetWordSpacing(ws)
		c.SetFontKerning("none")
		tm := c.MeasureText("M")
		asc, desc := tm.FontAscent, tm.FontDescent
		if ru.xs != nil || ru.ys != nil {
			// per-character positions
			chars := []rune(ru.text)
			for i, ch := range chars {
				if i < len(ru.xs) {
					if i > 0 {
						apply(chunkStart)
						chunkStart = len(placed)
					}
					px = ru.xs[i]
				}
				if i < len(ru.ys) {
					py = ru.ys[i]
				}
				w := c.MeasureText(string(ch)).Width
				placed = append(placed, placedRun{text: string(ch), st: ru.st, x: px, y: py, w: w, asc: asc, desc: desc, anchor: ru.st.anchor})
				px += w
			}
			continue
		}
		w := c.MeasureText(ru.text).Width
		placed = append(placed, placedRun{text: ru.text, st: ru.st, x: px, y: py, w: w, asc: asc, desc: desc, anchor: ru.st.anchor})
		px += w
	}
	apply(chunkStart)
	// dominant-baseline
	for i := range placed {
		p := &placed[i]
		switch p.st.baseline {
		case "central":
			p.y += (p.asc - p.desc) / 2
		case "middle":
			p.y += 0.26 * p.st.fontSize
		case "hanging":
			p.y += 0.8 * p.asc
		case "text-before-edge", "text-top":
			p.y += p.asc
		case "text-after-edge", "text-bottom":
			p.y -= p.desc
		}
	}
	return placed
}

func (r *svgR) textBBox(n *svgNode, st *sStyle) (Rect, bool) {
	placed := r.layoutText(n, st)
	if len(placed) == 0 {
		return Rect{}, false
	}
	x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range placed {
		x0, x1 = math.Min(x0, p.x), math.Max(x1, p.x+p.w)
		y0, y1 = math.Min(y0, p.y-p.asc), math.Max(y1, p.y+p.desc)
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
		if strings.TrimSpace(p.text) == "" && !strings.Contains(p.st.decoration, "underline") {
			continue
		}
		c.SetFont(r.fontOf(&p.st))
		c.SetLetterSpacing(r.length(p.st.letterSp, 0, p.st.fontSize, 0))
		c.SetWordSpacing(r.length(p.st.wordSp, 0, p.st.fontSize, 0))
		c.SetTextAlign("left")
		c.SetFontKerning("none")
		c.SetTextBaseline("alphabetic")
		if p.st.fill.kind != pNone && p.st.fillOp > 0 {
			if sh := r.shaderFor(p.st.fill, p.st.fillOp, n, bb, hasBB, &p.st, m); sh != nil {
				shp, ox, oy, sx := c.prepText(p.text, p.x, p.y, 0)
				if len(shp.Glyphs) > 0 {
					c.draw(c.textMask(shp, ox, oy, sx), sh)
				}
			}
		}
		if p.st.stroke.kind != pNone && p.st.strokeOp > 0 && p.st.sw > 0 {
			if sh := r.shaderFor(p.st.stroke, p.st.strokeOp, n, bb, hasBB, &p.st, m); sh != nil {
				shp, ox, oy, sx := c.prepText(p.text, p.x, p.y, 0)
				if tp := c.textPath(shp, ox, oy, sx); tp != nil {
					if mk := StrokeMask(tp, m, r.strokeStyle(&p.st), c.drawBounds()); mk != nil {
						c.draw(mk, sh)
					}
				}
			}
		}
		if p.st.decoration != "" && p.st.fill.kind == pColor {
			th := math.Max(1, p.st.fontSize/16)
			col := p.st.fill.col
			col.A *= p.st.fillOp
			deco := func(y float64) {
				pa := NewPath()
				pa.Rect(p.x, y, p.w, th)
				c.FillPathShader(pa, NonZero, Solid(col))
			}
			if strings.Contains(p.st.decoration, "underline") {
				deco(p.y + p.st.fontSize*0.1)
			}
			if strings.Contains(p.st.decoration, "line-through") {
				deco(p.y - p.st.fontSize*0.3)
			}
			if strings.Contains(p.st.decoration, "overline") {
				deco(p.y - p.asc)
			}
		}
	}
	c.SetLetterSpacing(0)
	c.SetWordSpacing(0)
}

// ---- <image> ----------------------------------------------------------------------------

var imgCache sync.Map // href -> image.Image

func decodeDataImage(href string) image.Image {
	if v, ok := imgCache.Load(href); ok {
		im, _ := v.(image.Image)
		return im
	}
	var im image.Image
	if strings.HasPrefix(href, "data:") {
		if i := strings.IndexByte(href, ','); i > 0 {
			meta, body := href[5:i], href[i+1:]
			var data []byte
			if strings.Contains(meta, ";base64") {
				data, _ = base64.StdEncoding.DecodeString(strings.Map(func(r rune) rune {
					if r == ' ' || r == '\n' || r == '\r' {
						return -1
					}
					return r
				}, body))
			} else if s, err := url.PathUnescape(body); err == nil {
				data = []byte(s)
			}
			if len(data) > 0 {
				im, _, _ = image.Decode(strings.NewReader(string(data)))
			}
		}
	}
	imgCache.Store(href, im)
	return im
}

func (r *svgR) drawImage(n *svgNode, st sStyle, m Matrix) {
	if !st.visible {
		return
	}
	im := decodeDataImage(strings.TrimSpace(n.A["href"]))
	if im == nil {
		return
	}
	x, y := r.attrLen(n, "x", 'x', &st, 0), r.attrLen(n, "y", 'y', &st, 0)
	iw, ih := float64(im.Bounds().Dx()), float64(im.Bounds().Dy())
	w, h := r.attrLen(n, "width", 'x', &st, iw), r.attrLen(n, "height", 'y', &st, ih)
	if w <= 0 || h <= 0 || iw <= 0 || ih <= 0 {
		return
	}
	c := r.c
	c.Save()
	defer c.Restore()
	c.SetTransform(m)
	c.ClipRect(x, y, w, h)
	vm, _ := viewBoxMatrix([]float64{0, 0, iw, ih}, n.A["preserveaspectratio"], w, h)
	surf := FromImage(im)
	pix := st.imageRendering == "pixelated" || st.imageRendering == "crisp-edges"
	c.SetSmoothing(!pix, QualityLow)
	c.Concat(Translate(x, y).Mul(vm))
	c.DrawImage(surf, 0, 0, iw, ih, 0, 0, iw, ih)
}

// ---- markers ----------------------------------------------------------------------------

type mvert struct {
	x, y    float64
	in, out Pt
	hasIn   bool
	hasOut  bool
}

func nz(p, fb Pt) Pt {
	if p.X != 0 || p.Y != 0 {
		return p
	}
	return fb
}

func pathVertices(p *Path) []mvert {
	var vs []mvert
	var cur, start Pt
	si := 0 // index in Pts
	subStart := -1
	for _, cmd := range p.Cmds {
		switch cmd {
		case 'M':
			cur = Pt{p.Pts[si], p.Pts[si+1]}
			si += 2
			start = cur
			vs = append(vs, mvert{x: cur.X, y: cur.Y})
			subStart = len(vs) - 1
		case 'L':
			to := Pt{p.Pts[si], p.Pts[si+1]}
			si += 2
			d := Pt{to.X - cur.X, to.Y - cur.Y}
			if len(vs) > 0 {
				vs[len(vs)-1].out, vs[len(vs)-1].hasOut = d, true
			}
			vs = append(vs, mvert{x: to.X, y: to.Y, in: d, hasIn: true})
			cur = to
		case 'Q':
			c1 := Pt{p.Pts[si], p.Pts[si+1]}
			to := Pt{p.Pts[si+2], p.Pts[si+3]}
			si += 4
			d0 := nz(Pt{c1.X - cur.X, c1.Y - cur.Y}, Pt{to.X - cur.X, to.Y - cur.Y})
			d1 := nz(Pt{to.X - c1.X, to.Y - c1.Y}, Pt{to.X - cur.X, to.Y - cur.Y})
			if len(vs) > 0 {
				vs[len(vs)-1].out, vs[len(vs)-1].hasOut = d0, true
			}
			vs = append(vs, mvert{x: to.X, y: to.Y, in: d1, hasIn: true})
			cur = to
		case 'C':
			c1 := Pt{p.Pts[si], p.Pts[si+1]}
			c2 := Pt{p.Pts[si+2], p.Pts[si+3]}
			to := Pt{p.Pts[si+4], p.Pts[si+5]}
			si += 6
			chord := Pt{to.X - cur.X, to.Y - cur.Y}
			d0 := nz(Pt{c1.X - cur.X, c1.Y - cur.Y}, nz(Pt{c2.X - cur.X, c2.Y - cur.Y}, chord))
			d1 := nz(Pt{to.X - c2.X, to.Y - c2.Y}, nz(Pt{to.X - c1.X, to.Y - c1.Y}, chord))
			if len(vs) > 0 {
				vs[len(vs)-1].out, vs[len(vs)-1].hasOut = d0, true
			}
			vs = append(vs, mvert{x: to.X, y: to.Y, in: d1, hasIn: true})
			cur = to
		case 'Z':
			d := Pt{start.X - cur.X, start.Y - cur.Y}
			v := mvert{x: start.X, y: start.Y}
			if d.X != 0 || d.Y != 0 {
				if len(vs) > 0 {
					vs[len(vs)-1].out, vs[len(vs)-1].hasOut = d, true
				}
				v.in, v.hasIn = d, true
			} else if len(vs) > 0 {
				v.in, v.hasIn = vs[len(vs)-1].in, vs[len(vs)-1].hasIn
			}
			if subStart >= 0 && subStart < len(vs) && vs[subStart].hasOut {
				v.out, v.hasOut = vs[subStart].out, true
				// the start vertex also sees the closing segment
				vs[subStart].in, vs[subStart].hasIn = v.in, v.hasIn
			}
			vs = append(vs, v)
			cur = start
		}
	}
	return vs
}

func (v mvert) angle() float64 {
	switch {
	case v.hasIn && v.hasOut:
		a1 := math.Atan2(v.in.Y, v.in.X)
		a2 := math.Atan2(v.out.Y, v.out.X)
		d := a2 - a1
		for d > math.Pi {
			d -= 2 * math.Pi
		}
		for d < -math.Pi {
			d += 2 * math.Pi
		}
		return a1 + d/2
	case v.hasIn:
		return math.Atan2(v.in.Y, v.in.X)
	case v.hasOut:
		return math.Atan2(v.out.Y, v.out.X)
	}
	return 0
}

func (r *svgR) drawMarkers(n *svgNode, p *Path, st sStyle, m Matrix) {
	if st.mStart == "" && st.mMid == "" && st.mEnd == "" {
		return
	}
	vs := pathVertices(p)
	if len(vs) == 0 {
		return
	}
	get := func(ref string) *svgNode {
		id, ok := urlRef(ref)
		if !ok {
			return nil
		}
		mk := r.doc.ByID[id]
		if mk == nil || mk.Tag != "marker" {
			return nil
		}
		return mk
	}
	ms, mm, me := get(st.mStart), get(st.mMid), get(st.mEnd)
	for i, v := range vs {
		var mk *svgNode
		isStart := i == 0
		switch {
		case i == 0:
			mk = ms
		case i == len(vs)-1:
			mk = me
		default:
			mk = mm
		}
		if mk == nil {
			continue
		}
		r.drawMarker(mk, v, isStart, st, m)
	}
}

func (r *svgR) drawMarker(mk *svgNode, v mvert, isStart bool, st sStyle, m Matrix) {
	if r.active[mk] {
		return
	}
	r.active[mk] = true
	defer delete(r.active, mk)
	mw := r.length(mk.A["markerwidth"], r.vw, st.fontSize, 3)
	mh := r.length(mk.A["markerheight"], r.vh, st.fontSize, 3)
	if mw <= 0 || mh <= 0 {
		return
	}
	mst := r.inherit(r.root, mk.A)
	rx := r.length(mk.A["refx"], r.vw, mst.fontSize, 0)
	ry := r.length(mk.A["refy"], r.vh, mst.fontSize, 0)
	ang := 0.0
	switch o := strings.TrimSpace(mk.A["orient"]); o {
	case "auto":
		ang = v.angle()
	case "auto-start-reverse":
		ang = v.angle()
		if isStart {
			ang += math.Pi
		}
	default:
		if f, ok := svgNumber(strings.TrimSuffix(o, "deg")); ok {
			ang = f * math.Pi / 180
		}
	}
	s := 1.0
	if mk.A["markerunits"] != "userSpaceOnUse" {
		s = st.sw
	}
	vb := splitNums(strings.ReplaceAll(mk.A["viewbox"], ",", " "))
	vm, hasVB := viewBoxMatrix(vb, mk.A["preserveaspectratio"], mw, mh)
	if !hasVB {
		vm = Identity
	}
	rpx, rpy := vm.Apply(rx, ry)
	base := m.Mul(Translate(v.x, v.y)).Mul(Rotate(ang)).Mul(Scale(s, s)).Mul(Translate(-rpx, -rpy))
	c := r.c
	c.Save()
	defer c.Restore()
	if ov := mk.A["overflow"]; ov != "visible" && ov != "auto" {
		c.SetTransform(base)
		if hasVB {
			// clip to the viewBox region mapped into the viewport
			c.ClipRect(0, 0, mw, mh)
		} else {
			c.ClipRect(0, 0, mw, mh)
		}
	}
	ovw, ovh := r.vw, r.vh
	if hasVB {
		r.vw, r.vh = vb[2], vb[3]
	}
	r.drawChildren(mk, mst, base.Mul(vm))
	r.vw, r.vh = ovw, ovh
}
