package render

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/style"
)

// Vertical writing modes (writing-mode: vertical-rl / vertical-lr, sideways-*).
//
// A box whose writing mode is vertical while its parent's is horizontal is an
// orthogonal flow root. Its subtree is laid out by the horizontal engine in
// the flow-relative ("logical") frame: the styles of the subtree are replaced
// by transposed copies (width <-> height, margin/padding/border sides rotated,
// ...), laid out, and the geometry is converted back to physical coordinates.
//
// Text is converted to one single-glyph line per character stacked down the
// column, so the painter draws upright glyphs at the right places; rotated
// (sideways) Latin glyphs are approximated this way (see the report).

func vertMode(st *style.Computed) int { return st.VerticalMode() }

// markVertical flags b as an orthogonal flow root at build time.
func (c *ctx) markVertical(b *lbox, pst *style.Computed) {
	vm := b.st.VerticalMode()
	if vm == 0 || (pst != nil && pst.VerticalMode() != 0) {
		return
	}
	if b.kind != kBlock && b.kind != kAtomic {
		return
	}
	switch b.inner {
	case iFlow, iFlex, iGrid:
	default:
		return
	}
	if b.oof {
		return
	}
	b.vflow = int8(vm)
}

func (c *ctx) vstyle(st *style.Computed, mode int) *style.Computed {
	if c.vcache == nil {
		c.vcache = map[vkey]*style.Computed{}
	}
	k := vkey{st, mode}
	if t, ok := c.vcache[k]; ok {
		return t
	}
	t := st.Transposed(mode)
	c.vcache[k] = t
	return t
}

type vkey struct {
	st   *style.Computed
	mode int
}

func (c *ctx) transposeIn(b *lbox, mode int, on bool) {
	var walk func(k *lbox)
	walk = func(k *lbox) {
		if on {
			k.ost = k.st
			k.st = c.vstyle(k.ost, mode)
			k.vt = int8(mode)
		} else if k.ost != nil {
			k.st = k.ost
			k.ost = nil
			k.vt = 0
		}
		k.hasIntr = false
		k.hasLast = false
		if k.marker != nil {
			walk(k.marker)
		}
		for _, g := range k.kids {
			if g.vflow != 0 && g != b {
				continue
			}
			walk(g)
		}
	}
	walk(b)
}

// layoutVertical lays out an orthogonal flow root.
func (c *ctx) layoutVertical(b *lbox, in layoutIn) {
	vm := int(b.vflow)
	c.edges(b, in.cbw)
	st := b.st
	phAuto := b.marAuto
	phMar := b.mar
	availH := in.cbh
	if availH < 0 {
		availH = c.vh
	}
	logW, logH := -1.0, -1.0 // logical border-box width (= physical height) and height (= physical width)
	if in.fixH >= 0 {
		logW = in.fixH
	} else if hv, ok := c.specH(b, st.Height, in.cbh); ok {
		hv = c.clampH(b, hv, in.cbh, 0)
		logW = hv + b.vExtra()
	}
	if in.fixW >= 0 {
		logH = in.fixW
	} else if wv, ok := c.specW(b, st.Width, in.cbw); ok && !c.vIntrinsic {
		wv = c.clampW(b, wv, in.cbw)
		logH = wv + b.hExtra()
	}
	c.transposeIn(b, vm, true)
	if c.vIntrinsic {
		// content size: ignore the specified physical width (logical height)
		cp := *b.st
		cp.Height = style.Length{K: style.LAuto}
		cp.MinHeight = style.Length{K: style.LAuto}
		cp.MaxHeight = style.Length{K: style.LNone}
		b.st = &cp
	}
	lin := layoutIn{cbw: availH, cbh: in.cbw, fixW: logW, fixH: logH, shrink: logW < 0, availW: availH, bfc: &bfcState{}, item: true}
	b.hasLast = false
	c.layoutBoxH(b, lin)
	c.vconvKids(b, vm)
	c.vswap(b, vm)
	c.transposeIn(b, vm, false)
	b.vt = 0
	b.mar = phMar
	b.marAuto = phAuto
	// physical auto margins on the left/right
	if !in.item && !b.floated && in.cbw >= 0 && (phAuto[1] || phAuto[3]) {
		rest := math.Max(0, in.cbw-b.w-phMar[1]-phMar[3])
		switch {
		case phAuto[1] && phAuto[3]:
			b.mar[1], b.mar[3] = rest/2, rest/2
		case phAuto[1]:
			b.mar[1] = rest
		default:
			b.mar[3] = rest
		}
	} else {
		for _, i := range []int{1, 3} {
			if phAuto[i] {
				b.mar[i] = 0
			}
		}
	}
	b.mtopOut, b.mbotOut, b.through = b.mar[0], b.mar[2], false
	b.hasBase = false
	b.hasFloat, b.touchesBFC = false, false
}

// vconvKids converts the geometry of b's descendants from logical to physical
// coordinates; b.w and b.h are still logical.
func (c *ctx) vconvKids(b *lbox, vm int) {
	Wl, Hl := b.w, b.h
	if b.ifc {
		c.collectInlineKids(b)
		kids := append([]*lbox(nil), b.inlineKids...)
		for _, k := range kids {
			c.vconvBox(k, Wl, Hl, vm)
		}
		c.vBuildLines(b, vm)
		return
	}
	for _, k := range b.kids {
		if k.kind == kText || k.kind == kInline || k.kind == kBreak || k.kind == kMarker || k.oof {
			continue
		}
		if k.vflow != 0 {
			continue
		}
		c.vconvBox(k, Wl, Hl, vm)
	}
}

func (c *ctx) vconvBox(k *lbox, Wl, Hl float64, vm int) {
	c.vconvKids(k, vm)
	lx, ly, lw, lh := k.x, k.y, k.w, k.h
	if vm == 1 {
		k.x = Hl - ly - lh
	} else {
		k.x = ly
	}
	k.y = lx
	k.w, k.h = lh, lw
	c.vperm(k, vm)
	k.hasBase = false
}

// vswap converts the root box itself (its position is the parent's business).
func (c *ctx) vswap(b *lbox, vm int) {
	b.w, b.h = b.h, b.w
	c.vperm(b, vm)
}

// vperm maps logical (top right bottom left) arrays back to physical sides.
func (c *ctx) vperm(k *lbox, vm int) {
	m := [4]int{1, 2, 3, 0} // logical index -> physical index
	if vm == 2 {
		m = [4]int{3, 2, 1, 0}
	}
	perm := func(a [4]float64) [4]float64 {
		var o [4]float64
		for l := 0; l < 4; l++ {
			o[m[l]] = a[l]
		}
		return o
	}
	k.mar, k.pad, k.bor = perm(k.mar), perm(k.pad), perm(k.bor)
	if k.hasBorOv {
		k.borOv = perm(k.borOv)
	}
	var am [4]bool
	for l := 0; l < 4; l++ {
		am[m[l]] = k.marAuto[l]
	}
	k.marAuto = am
	k.sbW, k.sbH = k.sbH, k.sbW
}

// uprightRune reports whether r is set upright in vertical text with the
// default text-orientation: mixed (CJK, kana, hangul, fullwidth forms).
func uprightRune(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x11ff, r >= 0x2e80 && r <= 0xa4cf, r >= 0xac00 && r <= 0xd7a3,
		r >= 0xf900 && r <= 0xfaff, r >= 0xfe30 && r <= 0xfe4f, r >= 0xff00 && r <= 0xff60,
		r >= 0xffe0 && r <= 0xffe6, r >= 0x1f000 && r <= 0x1faff, r >= 0x20000 && r <= 0x3ffff, r >= 0x3000 && r <= 0x303f:
		return true
	}
	return false
}

// vBuildLines replaces the logical lines of IFC root b by physical ones:
// upright characters become single-glyph lines stacked down the column,
// sideways runs become rotated single-line boxes (transform: rotate(90deg)).
// b.w and b.h are logical.
func (c *ctx) vBuildLines(b *lbox, vm int) {
	Hl := b.h
	b.vlines = b.lines
	b.vmode = int8(vm)
	var out []*line
	var carrier *line
	var rots []*lbox
	base := b.ost
	if base == nil {
		base = b.st
	}
	for _, L := range b.vlines {
		var x0, x1 float64
		if vm == 1 {
			x1 = Hl - L.y
			x0 = x1 - L.h
		} else {
			x0 = L.y
			x1 = x0 + L.h
		}
		cx := (x0 + x1) / 2
		for _, fr := range L.frags {
			switch fr.kind {
			case aText:
				it := fr.it
				fs := it.fs
				orient := it.st.TextOrientation
				isUp := func(r rune) bool {
					switch orient {
					case "upright":
						return true
					case "sideways", "sideways-right":
						return false
					}
					return uprightRune(r)
				}
				k := fr.a
				for k < fr.e {
					r := it.runes[k]
					if isUp(r) {
						adv := it.sh.x[k+1] - it.sh.x[k]
						u := L.x + fr.x + (it.sh.x[k] - it.sh.x[fr.a])
						k++
						if r == ' ' || r == '\n' || r == '\t' || r == 0xa0 || isZeroWidth(r) {
							continue
						}
						bl := u + adv/2 + (fs.asc-fs.desc)/2
						var face *font.Face
						if k-1 < len(it.sh.faces) {
							face = it.sh.faces[k-1]
						}
						gi := &textItem{owner: it.owner, src: it.src, runes: []rune{r}, fs: fs, st: it.st,
							sh: &shaped{r: []rune{r}, faces: []*font.Face{face}, x: []float64{0, adv}}}
						gl := &frag{kind: aText, box: fr.box, it: gi, a: 0, e: 1, x: 0, w: adv, base: 0}
						nat := adv
						if face != nil {
							if a := face.AdvanceF(r, fs.size); a > 0 {
								nat = a
							}
						}
						gl.w = nat
						out = append(out, &line{x: cx - nat/2, y: bl, w: nat, contentW: nat, h: adv, frags: []*frag{gl}, hasContent: true})
						continue
					}
					// a sideways run
					k1 := k
					for k1 < fr.e && !isUp(it.runes[k1]) {
						k1++
					}
					w := it.sh.x[k1] - it.sh.x[k]
					u0 := L.x + fr.x + (it.sh.x[k] - it.sh.x[fr.a])
					xs := make([]float64, k1-k+1)
					for i := range xs {
						xs[i] = it.sh.x[k+i] - it.sh.x[k]
					}
					ri := &textItem{owner: it.owner, src: it.src, runes: append([]rune(nil), it.runes[k:k1]...), fs: fs, st: it.st,
						sh: &shaped{r: it.runes[k:k1], faces: it.sh.faces[k:k1], x: xs}}
					rf := &frag{kind: aText, box: fr.box, it: ri, a: 0, e: k1 - k, x: 0, w: w, base: fr.base}
					if k1 == fr.e {
						rf.trail = fr.trail
					}
					S := c.vRotBox(b, base, w, L.h, rf, L.baseline)
					S.x, S.y = x1, u0
					rots = append(rots, S)
					if carrier == nil {
						carrier = &line{hasContent: true}
					}
					carrier.frags = append(carrier.frags, &frag{kind: aAtomic, box: S, w: w})
					k = k1
				}
			case aAtomic:
				if carrier == nil {
					carrier = &line{hasContent: true}
				}
				carrier.frags = append(carrier.frags, &frag{kind: aAtomic, box: fr.box, w: fr.w})
			}
		}
	}
	if carrier != nil {
		out = append([]*line{carrier}, out...)
	}
	b.lines = out
	b.inlineKids = append(b.inlineKids, rots...)
}

// vRotBox builds the box holding one sideways run: a horizontal line rotated
// a quarter turn about its top-left corner.
func (c *ctx) vRotBox(parent *lbox, base *style.Computed, w, h float64, fr *frag, baseline float64) *lbox {
	st := *c.eng.Anonymous(base, "block")
	st.Transform = []style.TOp{{Fn: "rotate", A: [6]style.Length{{K: style.LNumber, V: 90}}, N: 1}}
	st.TransformOrigin = [2]style.Length{style.Px(0), style.Px(0)}
	S := newBox(nil, &st, kAtomic)
	S.inner = iFlow
	S.ifc = true
	S.inlineLv = true
	S.parent, S.lp = parent, parent
	S.w, S.h = w, h
	S.noFrags = true
	S.lines = []*line{{x: 0, y: 0, w: w, contentW: w, h: h, baseline: baseline, frags: []*frag{fr}, hasContent: true}}
	return S
}

// placeVerticalInline computes the physical fragments of the inline boxes of
// a vertical IFC root (called from placeInline once b.ax, b.ay are known).
func (c *ctx) placeVerticalInline(b *lbox) {
	var reset func(x *lbox)
	reset = func(x *lbox) {
		for _, k := range x.kids {
			if k.kind == kInline || k.kind == kBreak {
				k.absFrags = k.absFrags[:0]
				reset(k)
			}
		}
	}
	reset(b)
	var boxes []lineBox
	for _, ln := range b.vlines {
		for _, lb := range ln.boxes {
			bx := lb.box
			fs := c.spec(bx.st)
			u := ln.x + lb.x0
			v := ln.y + lb.base - fs.asc - bx.pad[0] - bx.bor[0]
			du := lb.x1 - lb.x0
			dv := fs.asc + fs.desc + bx.pad[0] + bx.pad[2] + bx.bor[0] + bx.bor[2]
			var px float64
			if b.vmode == 1 {
				px = b.ax + b.w - v - dv
			} else {
				px = b.ax + v
			}
			r := Rect{px, b.ay + u, dv, du}
			lb.rect = r
			bx.absFrags = append(bx.absFrags, r)
			boxes = append(boxes, lb)
		}
	}
	if len(b.lines) == 0 {
		return
	}
	for _, ln := range b.lines {
		ln.boxes = nil
	}
	b.lines[0].boxes = boxes
}
