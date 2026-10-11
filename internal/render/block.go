package render

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/style"
)

// layoutIn carries the constraints for laying out one box.
type layoutIn struct {
	cbw, cbh   float64 // containing block content size for percentages; cbh < 0 when indefinite
	fixW, fixH float64 // forced border-box size (<0 when unset)
	shrink     bool    // shrink-to-fit auto width
	availW     float64 // for shrink-to-fit: width available to the margin box
	bx, by     float64 // BFC coordinates of this box's border-box origin (when it joins the parent's BFC)
	bfc        *bfcState
	stretchW   bool // width was stretched by a parent (flex/grid align stretch)
	item       bool // flex/grid/table item: no auto-margin fixups
}

func noFix() (float64, float64) { return -1, -1 }

const sbSize = 15

type strut struct{ pos, neg float64 }

func (s *strut) add(m float64) {
	if m >= 0 {
		s.pos = math.Max(s.pos, m)
	} else {
		s.neg = math.Min(s.neg, m)
	}
}
func (s strut) val() float64 { return s.pos + s.neg }

// ---- BFC / floats ---------------------------------------------------------------------

type floatRec struct {
	x0, y0, x1, y1 float64
	left           bool
}

type bfcState struct {
	floats  []floatRec
	lastTop float64
}

func (s *bfcState) band(y0, y1, lo, hi float64) (float64, float64) {
	l, r := lo, hi
	for _, f := range s.floats {
		if f.y0 < y1 && f.y1 > y0 && f.y1 > f.y0 {
			if f.left {
				l = math.Max(l, f.x1)
			} else {
				r = math.Min(r, f.x0)
			}
		}
	}
	return l, r
}

func (s *bfcState) clearY(side string) float64 {
	y := -1e9
	for _, f := range s.floats {
		if (side == "both" || side == "left" && f.left || side == "right" && !f.left) && f.y1 > f.y0 {
			y = math.Max(y, f.y1)
		}
	}
	return y
}

// place positions a float of margin-box size w x h with top no earlier than y.
func (s *bfcState) place(w, h, y, lo, hi float64, left bool) (float64, float64) {
	if y < s.lastTop {
		y = s.lastTop
	}
	for iter := 0; iter < 10000; iter++ {
		l, r := s.band(y, y+math.Max(h, 0.01), lo, hi)
		if r-l >= w-1e-6 || (l <= lo && r >= hi) {
			var x float64
			if left {
				x = l
			} else {
				x = r - w
			}
			s.floats = append(s.floats, floatRec{x, y, x + w, y + h, left})
			s.lastTop = y
			return x, y
		}
		// move below the next float edge
		next := math.Inf(1)
		for _, f := range s.floats {
			if f.y1 > y && f.y1 < next && f.y1 > f.y0 {
				next = f.y1
			}
		}
		if math.IsInf(next, 1) {
			var x float64
			if left {
				x = l
			} else {
				x = r - w
			}
			s.floats = append(s.floats, floatRec{x, y, x + w, y + h, left})
			s.lastTop = y
			return x, y
		}
		y = next
	}
	return lo, y
}

func (b *lbox) isBFCRoot() bool {
	st := b.st
	if b.parent == nil && b.n != nil {
		return true
	}
	if b.floated || b.oof || b.kind == kAtomic {
		return true
	}
	switch b.inner {
	case iCell, iFlex, iGrid, iTable, iCaption, iReplaced, iControl:
		return true
	}
	if b.inner == iFlow && b.parent != nil && (b.parent.inner == iFlex || b.parent.inner == iGrid) {
		return true
	}
	if st.OverflowX != "visible" && st.OverflowX != "clip" || st.OverflowY != "visible" && st.OverflowY != "clip" {
		return true
	}
	if st.Display == "flow-root" || st.Display == "inline-block" || st.Display == "list-item" && false {
		return true
	}
	if st.Contain != "" && st.Contain != "none" && (contains(st.Contain, "layout") || contains(st.Contain, "paint") || contains(st.Contain, "strict") || contains(st.Contain, "content")) {
		return true
	}
	if st.ColumnCount > 0 || st.ColumnWidth.K != style.LAuto {
		return true
	}
	if st.Position == "absolute" || st.Position == "fixed" {
		return true
	}
	return false
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---- edges ----------------------------------------------------------------------------

func rl(l style.Length, base float64) float64 {
	if base < 0 {
		base = 0
	}
	return l.Resolve(base)
}

func (c *ctx) edges(b *lbox, cbw float64) {
	st := b.st
	for i := 0; i < 4; i++ {
		m := st.Margin[i]
		if m.K == style.LAuto {
			b.marAuto[i] = true
			b.mar[i] = 0
		} else {
			b.marAuto[i] = false
			b.mar[i] = rl(m, cbw)
		}
		b.pad[i] = math.Max(0, rl(st.Padding[i], cbw))
		b.bor[i] = st.BorderWidth[i]
	}
	if b.hasBorOv {
		b.bor = b.borOv
	}
	if b.kind == kInline {
		// vertical margins do not apply
		b.mar[0], b.mar[2] = 0, 0
		if b.cont > 0 {
			b.mar[3], b.pad[3], b.bor[3] = 0, 0, 0
		}
		if b.split && !b.lastPart {
			b.mar[1], b.pad[1], b.bor[1] = 0, 0, 0
		}
	}
}

func (b *lbox) hExtra() float64 { return b.pad[1] + b.pad[3] + b.bor[1] + b.bor[3] }
func (b *lbox) vExtra() float64 { return b.pad[0] + b.pad[2] + b.bor[0] + b.bor[2] }

// contentBoxAdjust converts a specified size to a border-box size.
func (b *lbox) borderBoxW(cw float64) float64 {
	if b.st.BoxSizing == "border-box" {
		return cw
	}
	return cw + b.hExtra()
}

// resolveW returns the content width implied by a specified width value.
func (c *ctx) specW(b *lbox, l style.Length, cbw float64) (float64, bool) {
	switch l.K {
	case style.LPx, style.LPct, style.LCalc:
		if l.K != style.LPx && cbw < 0 {
			return 0, false
		}
		v := rl(l, cbw)
		if b.st.BoxSizing == "border-box" {
			v -= b.hExtra()
		}
		return math.Max(0, v), true
	case style.LMinContent:
		return c.minContent(b), true
	case style.LMaxContent:
		return c.maxContent(b), true
	case style.LFitContent:
		mn, mx := c.minContent(b), c.maxContent(b)
		avail := cbw - b.mar[1] - b.mar[3] - b.hExtra()
		if l.V >= 0 {
			avail = l.V
			if b.st.BoxSizing == "border-box" {
				avail -= b.hExtra()
			}
		}
		return math.Min(math.Max(mn, avail), mx), true
	}
	return 0, false
}

func (c *ctx) specH(b *lbox, l style.Length, cbh float64) (float64, bool) {
	switch l.K {
	case style.LPx:
	case style.LPct, style.LCalc:
		if cbh < 0 {
			return 0, false
		}
	default:
		return 0, false
	}
	v := rl(l, cbh)
	if b.st.BoxSizing == "border-box" {
		v -= b.vExtra()
	}
	return math.Max(0, v), true
}

// usedContentWidth computes the content width of a block-level box.
func (c *ctx) usedContentWidth(b *lbox, in layoutIn) float64 {
	st := b.st
	if in.fixW >= 0 {
		return math.Max(0, in.fixW-b.hExtra()-b.sbW)
	}
	cw, ok := c.specW(b, st.Width, in.cbw)
	if !ok && st.AspectRatio > 0 && !st.AspectAuto || !ok && st.AspectRatio > 0 {
		if hh, ok2 := c.specH(b, st.Height, in.cbh); ok2 {
			cw = hh * st.AspectRatio
			if st.BoxSizing == "border-box" {
				cw = (hh+b.vExtra())*st.AspectRatio - b.hExtra()
			}
			ok = true
		}
	}
	if !ok {
		if in.shrink {
			avail := in.availW - b.mar[1] - b.mar[3] - b.hExtra()
			mn, mx := c.minContent(b), c.maxContent(b)
			cw = math.Min(math.Max(mn, avail), mx) - b.sbW
		} else {
			cw = in.cbw - b.mar[1] - b.mar[3] - b.hExtra() - b.sbW
		}
		if cw < 0 {
			cw = 0
		}
	} else {
		cw = math.Max(0, cw-b.sbW)
	}
	cw = c.clampW(b, cw, in.cbw)
	return cw
}

func (c *ctx) clampW(b *lbox, cw, cbw float64) float64 {
	st := b.st
	if mx, ok := c.specW(b, st.MaxWidth, cbw); ok && st.MaxWidth.K != style.LNone {
		if cw > mx {
			cw = mx
		}
	}
	if mn, ok := c.specW(b, st.MinWidth, cbw); ok {
		if cw < mn {
			cw = mn
		}
	}
	return cw
}

// ---- main entry ------------------------------------------------------------------------

func (a layoutIn) sameAs(o layoutIn, usesBFC bool) bool {
	if a.cbw != o.cbw || a.cbh != o.cbh || a.fixW != o.fixW || a.fixH != o.fixH || a.shrink != o.shrink || a.availW != o.availW || a.stretchW != o.stretchW {
		return false
	}
	if usesBFC && (a.bx != o.bx || a.by != o.by || a.bfc != o.bfc) {
		return false
	}
	return true
}

func (c *ctx) layoutBox(b *lbox, in layoutIn) {
	if b.hasLast && !b.touchesBFC && b.lastIn.sameAs(in, false) && !c.noCache {
		return
	}
	b.lastIn, b.hasLast = in, true
	b.hasFloat, b.touchesBFC = false, false
	b.legendBase = -1
	c.edges(b, in.cbw)
	switch b.inner {
	case iFlex:
		c.layoutFlex(b, in)
	case iGrid:
		c.layoutGrid(b, in)
	case iTable:
		c.layoutTable(b, in)
	case iReplaced:
		c.layoutReplaced(b, in)
	case iControl:
		c.layoutControl(b, in)
	default:
		c.layoutFlow(b, in)
	}
}

// vertical scrollbar / horizontal scrollbar reservation for overflow:scroll
func (c *ctx) scrollbars(b *lbox) {
	b.sbW, b.sbH = 0, 0
	st := b.st
	if st.Scrollbar == "none" {
		return
	}
	if st.OverflowY == "scroll" {
		b.sbW = sbSize
	}
	if st.OverflowX == "scroll" {
		b.sbH = sbSize
	}
	if b.vscroll {
		b.sbW = sbSize
	}
	if b.hscroll {
		b.sbH = sbSize
	}
}

func (c *ctx) layoutFlow(b *lbox, in layoutIn) {
	st := b.st
	b.vscroll, b.hscroll = false, false
	for pass := 0; pass < 2; pass++ {
		c.scrollbars(b)
		cw := c.usedContentWidth(b, in)
		if in.fixW < 0 {
			// sb space is inside the border box: content width excludes it
		}
		bw := cw + b.hExtra() + b.sbW
		if in.fixW >= 0 {
			bw = in.fixW
		}
		b.w = bw
		c.horizontalMargins(b, in, bw)
		// establish context
		var bfc *bfcState
		c.fieldsetLegend(b, cw)
		bw += 0
		bx, by := in.bx, in.by
		root := b.isBFCRoot()
		if root {
			bfc = &bfcState{}
			bx, by = 0, 0
		} else {
			bfc = in.bfc
		}
		cx := bx + b.bor[3] + b.pad[3]
		cy := by + b.bor[0] + b.pad[0]
		cbh := -1.0
		if hh, ok := c.specH(b, st.Height, in.cbh); ok {
			cbh = hh
		}
		if in.fixH >= 0 {
			cbh = math.Max(0, in.fixH-b.vExtra()-b.sbH)
		} else if cbh >= 0 {
			cbh = math.Max(0, cbh-b.sbH)
		}
		if b.parent == nil && b.n != nil && cbh < 0 && st.Height.K == style.LAuto {
			cbh = -1
		}
		pushed := c.pushCB(b)
		var contentH float64
		topAdj := !root && b.pad[0] == 0 && b.bor[0] == 0
		botAdj := !root && b.pad[2] == 0 && b.bor[2] == 0 && st.Height.K == style.LAuto && in.fixH < 0
		b.mtopOut, b.mbotOut, b.through = b.mar[0], b.mar[2], false
		if st.Height.K == style.LAuto && in.fixH < 0 && st.AspectRatio > 0 {
			// handled after
		}
		c.inFlow = append(c.inFlow, b)
		if b.inner == iGrid {
			contentH = c.layoutGridContent(b, cw, cbh, in)
		} else if b.inner == iFlex {
			contentH = c.layoutFlexContent(b, cw, cbh, in)
		} else if b.isMulticol() && !b.ifc && b.inner == iFlow {
			contentH = c.layoutMulticolContent(b, cw, cx, cy, bfc, cbh, topAdj, in)
		} else if b.ifc {
			contentH = c.layoutInlineContent(b, cw, cx, cy, bfc, cbh)
		} else {
			contentH = c.layoutBlockKids(b, cw, cx, cy, bfc, cbh, topAdj, botAdj, root)
		}
		c.inFlow = c.inFlow[:len(c.inFlow)-1]
		b.contentH = contentH
		// height
		h := contentH
		hSpec := false
		if in.fixH >= 0 {
			h = math.Max(0, in.fixH-b.vExtra())
			hSpec = true
		} else if hh, ok := c.specH(b, st.Height, in.cbh); ok {
			h = hh
			hSpec = true
		} else if st.Height.IsIntrinsic() {
			// min/max/fit-content block size = content height
		} else if st.AspectRatio > 0 {
			h = cw / st.AspectRatio
			if st.BoxSizing == "border-box" {
				h = (cw+b.hExtra())/st.AspectRatio - b.vExtra()
			}
			hSpec = true
		}
		if b.inner == iCell && hSpec && h < contentH {
			h = contentH
		}
		h = c.clampH(b, h, in.cbh, contentH)
		if c.quirks && !hSpec && in.fixH < 0 && (b == c.bodyBox || b == c.rootBox) {
			want := c.vh - b.vExtra() - b.sbH
			if b == c.bodyBox && c.rootBox != nil {
				want -= b.mar[0] + b.mar[2] + c.rootBox.vExtra() + c.rootBox.mar[0] + c.rootBox.mar[2]
			} else {
				want -= b.mar[0] + b.mar[2]
			}
			if want > h {
				h = want
			}
		}
		if hSpec || h != contentH {
			// bottom margin cannot collapse through an explicit height
			b.mbotOut = b.mar[2]
		}
		if b.ifc && len(b.lines) == 0 && h == 0 && b.vExtra() == 0 && !root && !hSpec && b.floatCount() == 0 {
			s := strut{}
			s.add(b.mar[0])
			s.add(b.mar[2])
			b.through = true
			b.mtopOut, b.mbotOut = s.val(), s.val()
		}
		if b.through && (h != 0 || hSpec && h != 0) {
			b.through = false
			b.mbotOut = b.mar[2]
			b.mtopOut = b.mar[0]
		}
		if hSpec {
			b.h = h + b.vExtra()
		} else {
			b.h = h + b.vExtra() + b.sbH
		}
		// scrollbar necessity for overflow:auto
		if pass == 0 && (st.OverflowY == "auto" || st.OverflowX == "auto") && st.Scrollbar != "none" {
			need := false
			if st.OverflowY == "auto" && !b.vscroll && contentH > h+0.01 && hSpec {
				b.vscroll = true
				need = true
			}
			if st.OverflowX == "auto" && !b.hscroll && c.contentOverflowX(b) > cw+0.01 {
				b.hscroll = true
				need = true
			}
			if need {
				c.popCB(b, pushed)
				c.noCache = true
				c.resetKids(b)
				continue
			}
		}
		c.popCB(b, pushed)
		c.shiftContent(b)
		c.finishOOF(b)
		break
	}
	c.noCache = false
	if b.floated || b.oof {
		b.mtopOut, b.mbotOut = b.mar[0], b.mar[2]
		b.through = false
	}
	if b.isBFCRoot() {
		b.mtopOut, b.mbotOut = b.mar[0], b.mar[2]
	}
}

func (c *ctx) resetKids(b *lbox) {
	var rec func(x *lbox)
	rec = func(x *lbox) {
		x.hasLast = false
		for _, k := range x.kids {
			rec(k)
		}
	}
	rec(b)
}

func (c *ctx) contentOverflowX(b *lbox) float64 {
	m := 0.0
	var rec func(x *lbox, ox float64)
	rec = func(x *lbox, ox float64) {
		for _, k := range x.kids {
			if k.kind == kBlock && !k.oof {
				m = math.Max(m, ox+k.x+k.w+k.mar[1])
				if k.st.OverflowX == "visible" {
					rec(k, ox+k.x)
				}
			}
		}
		for _, ln := range x.lines {
			m = math.Max(m, ln.x+ln.contentW)
		}
	}
	rec(b, -(b.bor[3] + b.pad[3]))
	return m
}

func (c *ctx) clampH(b *lbox, h, cbh, contentH float64) float64 {
	st := b.st
	if st.MaxHeight.K != style.LNone && st.MaxHeight.K != style.LAuto {
		if mx, ok := c.specH(b, st.MaxHeight, cbh); ok && h > mx {
			h = mx
		}
	}
	if mn, ok := c.specH(b, st.MinHeight, cbh); ok && h < mn {
		h = mn
	}
	return h
}

// horizontalMargins resolves auto margins and over-constrained margins.
func (c *ctx) horizontalMargins(b *lbox, in layoutIn, bw float64) {
	if in.shrink || b.floated || b.oof || in.item || in.fixW >= 0 && false {
		for _, i := range []int{1, 3} {
			if b.marAuto[i] {
				b.mar[i] = 0
			}
		}
		return
	}
	left := b.marAuto[3]
	right := b.marAuto[1]
	rest := in.cbw - bw - b.mar[1] - b.mar[3]
	if in.cbw < 0 {
		return
	}
	switch {
	case left && right:
		if rest > 0 {
			b.mar[3], b.mar[1] = rest/2, rest/2
		}
	case left:
		b.mar[3] = math.Max(0, rest)
	case right:
		b.mar[1] = math.Max(0, rest)
	default:
		ta, pdir := "", "ltr"
		if b.parent != nil {
			ta = b.parent.st.TextAlign
			pdir = b.parent.st.Direction
		}
		switch {
		case ta == "-webkit-center" && rest > 0:
			b.mar[3] += rest / 2
			b.mar[1] += rest / 2
		case ta == "-webkit-right" && rest > 0:
			b.mar[3] += rest
		case pdir == "rtl":
			b.mar[3] += rest
		default:
			b.mar[1] += rest
		}
	}
}

func (c *ctx) pushCB(b *lbox) bool {
	if b.isContainingBlockForAbs() {
		c.cbStack = append(c.cbStack, b)
		return true
	}
	return false
}

func (c *ctx) popCB(b *lbox, pushed bool) {
	if pushed {
		c.cbStack = c.cbStack[:len(c.cbStack)-1]
	}
}

func (b *lbox) isContainingBlockForAbs() bool {
	st := b.st
	if st.Position != "static" || len(st.Transform) > 0 || len(st.Translate) > 0 || len(st.Rotate) > 0 || len(st.Scale) > 0 || len(st.Filter) > 0 || st.Perspective != 0 {
		return true
	}
	if st.WillChange != "" && (contains(st.WillChange, "transform") || contains(st.WillChange, "perspective")) {
		return true
	}
	if st.Contain != "" && (contains(st.Contain, "layout") || contains(st.Contain, "paint") || contains(st.Contain, "strict") || contains(st.Contain, "content")) {
		return true
	}
	return false
}

// ---- block children ----------------------------------------------------------------------

func (c *ctx) layoutBlockKids(b *lbox, cw, cx, cy float64, bfc *bfcState, cbh float64, topAdj, botAdj, isRoot bool) float64 {
	var st strut
	y := 0.0
	leading := topAdj
	startMT := b.mar[0]
	if leading {
		st.add(b.mar[0])
	}
	sawContent := false
	b.kidsBase()
	nfl := len(bfc.floats)
	_ = nfl
	for _, k := range b.kids {
		if k.kind == kMarker || k.kind == kText || k.kind == kBreak || k.kind == kInline {
			continue
		}
		if k == b.legend {
			continue
		}
		if k.oof {
			c.registerOOF(b, k, cx-0, y, st, leading, cw)
			continue
		}
		if k.floated {
			c.placeFloatInBlock(b, k, cw, cx, cy, y, &st, bfc, cbh, leading)
			b.hasFloat = true
			continue
		}
		// in-flow block
		ccbh := cbh
		c.edges(k, cw)
		// clearance
		clearTo := math.Inf(-1)
		if k.st.Clear != "none" {
			clearTo = bfc.clearY(k.st.Clear)
		}
		assumedGap := func() float64 {
			s := st
			s.add(k.mar[0])
			return s.val()
		}
		by := cy + y
		if !leading {
			by += assumedGap()
		} else {
			s := st
			s.add(k.mar[0])
			by = cy + 0
		}
		// clearance affects the assumed position
		pos := by
		if clearTo > pos {
			pos = clearTo
		}
		in := layoutIn{cbw: cw, cbh: ccbh, fixW: -1, fixH: -1, bfc: bfc, bx: cx, by: pos}
		shiftX := 0.0
		pushedPos := math.Inf(-1)
		if len(bfc.floats) > 0 && k.isBFCRoot() {
			mSum := k.mar[1] + k.mar[3]
			// a BFC root must not overlap floats: narrow it or move it down
			for try := 0; try < 64; try++ {
				l, r := bfc.band(pos, pos+1, cx, cx+cw)
				if l <= cx && r >= cx+cw {
					break
				}
				avail := r - l
				in.cbw = avail
				in.bx, in.by = l, pos
				k.hasLast = false
				c.layoutBox(k, in)
				if k.w+mSum <= avail+0.01 {
					shiftX = l - cx
					break
				}
				next := math.Inf(1)
				for _, f := range bfc.floats {
					if f.y1 > pos && f.y1 < next && f.y1 > f.y0 {
						next = f.y1
					}
				}
				if math.IsInf(next, 1) {
					break
				}
				pos = next
				pushedPos = next
				in.cbw, in.bx = cw, cx
			}
			in.by = pos
		}
		c.layoutBox(k, in)
		if k.hiddenFlow {
			// content-visibility:hidden style content (closed details): laid out in place, takes no space
			s := st
			s.add(k.mtopOut)
			k.x, k.y, k.lp = k.mar[3], y+s.val(), b
			continue
		}
		if k.hasFloat || k.touchesBFC {
			b.touchesBFC = true
		}
		// real gap
		s := st
		s.add(k.mtopOut)
		gap := s.val()
		var ky float64
		cleared := false
		if leading && !k.through {
			ky = 0
		} else if leading && k.through {
			ky = 0
		} else {
			ky = y + gap
		}
		if clearTo > cy+ky {
			ky = clearTo - cy
			cleared = true
		}
		if pushedPos > cy+ky {
			ky = pushedPos - cy
		}
		if cleared && leading {
			// clearance prevents collapsing with the parent
			leading = false
			b.mtopOut = b.mar[0]
			y = 0
			st = strut{}
			ky = clearTo - cy
		}
		if k.through && !cleared {
			// margins collapse through the empty box
			st.add(k.mtopOut)
			k.x = k.mar[3] + shiftX
			k.y = ky
			if leading {
				k.y = 0
			}
			k.lp = b
			continue
		}
		if leading {
			// first real child: its margins joined b's top margin
			b.mtopOut = st.val()
			if gap != st.val() {
				s2 := st
				s2.add(k.mtopOut)
				b.mtopOut = s2.val()
			}
			leading = false
			y = 0
			ky = 0
			st = strut{}
		}
		if k.hasFloat && math.Abs((cy+ky)-pos) > 0.01 && !cleared {
			// the box landed elsewhere than assumed: lay it out again
			k.hasLast = false
			in.by = cy + ky
			c.layoutBox(k, in)
		}
		k.x = k.mar[3] + shiftX
		k.y = ky
		k.lp = b
		y = ky + k.h
		st = strut{}
		st.add(k.mbotOut)
		sawContent = true
		if k.hasBase && !b.hasBase {
			b.hasBase = true
			b.baseline = k.y + k.baseline - (b.bor[0] + b.pad[0])
		}
		if k.hasBase {
			b.lastBase = k.y + k.lastBase - (b.bor[0] + b.pad[0])
		}
	}
	_ = startMT
	var contentH float64
	if leading {
		// all children collapsed through (or none)
		if botAdj && topAdj {
			// empty box: the margins join
			s := st
			s.add(b.mar[2])
			b.mtopOut = s.val()
			b.mbotOut = s.val()
			b.through = !hasInFlowContent(b) && b.vExtra() == 0
			if !b.through {
				b.mtopOut, b.mbotOut = b.mar[0], b.mar[2]
			}
			return 0
		}
		b.mtopOut = b.mar[0]
		if topAdj {
			s := st
			b.mtopOut = s.val()
		}
		contentH = 0
		y = 0
		st = strut{}
	}
	if botAdj {
		s := st
		s.add(b.mar[2])
		b.mbotOut = s.val()
		contentH = y
	} else {
		contentH = y + st.val()
		b.mbotOut = b.mar[2]
	}
	_ = sawContent
	// BFC roots contain their floats
	if isRoot {
		for _, f := range bfc.floats {
			if f.y1-cy > contentH {
				contentH = f.y1 - cy
			}
		}
	}
	return contentH
}

func hasInFlowContent(b *lbox) bool {
	for _, k := range b.kids {
		if k.kind == kBlock && !k.floated && !k.oof {
			return true
		}
	}
	return b.ifc && len(b.lines) > 0
}

func (b *lbox) kidsBase() {
	b.hasBase, b.baseline, b.lastBase = false, 0, 0
}

// placeFloatInBlock lays out and places a float found among block children.
func (c *ctx) placeFloatInBlock(b, k *lbox, cw, cx, cy, y float64, st *strut, bfc *bfcState, cbh float64, leading bool) {
	c.edges(k, cw)
	in := layoutIn{cbw: cw, cbh: cbh, fixW: -1, fixH: -1, shrink: true, availW: cw, bx: 0, by: 0, bfc: &bfcState{}}
	c.layoutBox(k, in)
	mw := k.w + k.mar[1] + k.mar[3]
	mh := k.h + k.mar[0] + k.mar[2]
	ty := cy + y
	if !leading {
		ty += st.val()
	}
	if k.st.Clear != "none" {
		if cy := bfc.clearY(k.st.Clear); cy > ty {
			ty = cy
		}
	}
	lo, hi := cx, cx+cw
	x, fy := bfc.place(mw, mh, ty, lo, hi, k.st.Float == "left")
	k.x = x - cx + k.mar[3]
	k.y = fy - cy + k.mar[0]
	k.lp = b
	b.touchesBFC = true
}

// ---- positioned (absolute / fixed) layout ------------------------------------------------

// registerOOF notes an absolutely positioned box and its static position.
func (c *ctx) registerOOF(b, k *lbox, cx, y float64, st strut, leading bool, cw float64) {
	k.lp = b
	yy := y
	if !leading {
		yy += st.val()
	}
	k.staticX = b.bor[3] + b.pad[3]
	k.staticY = b.bor[0] + b.pad[0] + yy
	k.staticSet = true
	c.addOOF(k)
}

func (c *ctx) addOOF(k *lbox) {
	k.oofDone = false
	if k.st.Position == "fixed" {
		c.rootBox.oofKids = appendOnce(c.rootBox.oofKids, k)
		k.cbBox = c.rootBox
		return
	}
	for i := len(c.cbStack) - 1; i >= 0; i-- {
		cb := c.cbStack[i]
		cb.oofKids = appendOnce(cb.oofKids, k)
		k.cbBox = cb
		return
	}
	c.rootBox.oofKids = appendOnce(c.rootBox.oofKids, k)
	k.cbBox = c.rootBox
}

func appendOnce(l []*lbox, k *lbox) []*lbox {
	for _, x := range l {
		if x == k {
			return l
		}
	}
	return append(l, k)
}

// finishOOF lays out the absolutely positioned descendants of b once b's size is known.
func (c *ctx) finishOOF(b *lbox) {
	if len(b.oofKids) == 0 {
		return
	}
	kids := b.oofKids
	b.oofKids = nil
	for _, k := range kids {
		c.layoutAbs(k, b)
	}
	b.oofKids = kids
}

func (c *ctx) layoutAbs(k, cb *lbox) {
	st := k.st
	// containing block: padding box of cb (the initial containing block for the root)
	var cbW, cbH float64
	var ox, oy float64 // origin of the padding box relative to cb's border box
	if cb == c.rootBox || cb.parent == nil && cb.n != nil && !cb.isContainingBlockForAbs() {
		cbW, cbH = c.vw-c.sbViewport, c.vh-c.sbViewportH
		if st.Position == "fixed" {
			cbW, cbH = c.vw-c.sbViewport, c.vh-c.sbViewportH
		}
		ox, oy = 0, 0
		if cb == c.rootBox && cb.isContainingBlockForAbs() {
			cbW, cbH = cb.w-cb.bor[1]-cb.bor[3], cb.h-cb.bor[0]-cb.bor[2]
			ox, oy = cb.bor[3], cb.bor[0]
		}
	} else {
		cbW = cb.w - cb.bor[1] - cb.bor[3]
		cbH = cb.h - cb.bor[0] - cb.bor[2]
		ox, oy = cb.bor[3], cb.bor[0]
	}
	c.edges(k, cbW)
	left, right, top, bottom := st.Inset[3], st.Inset[1], st.Inset[0], st.Inset[2]
	lAuto, rAuto, tAuto, bAuto := left.K == style.LAuto, right.K == style.LAuto, top.K == style.LAuto, bottom.K == style.LAuto
	lv, rv, tv, bv := rl(left, cbW), rl(right, cbW), rl(top, cbH), rl(bottom, cbH)
	// static position relative to the cb padding box origin
	sx, sy := c.staticInCB(k, cb, ox, oy)
	repl := k.rep != nil && k.inner != iFlow
	// width
	hExtra := k.hExtra()
	var cw float64
	_, wSpec := c.specW(k, st.Width, cbW)
	var wval float64
	if wSpec {
		wval, _ = c.specW(k, st.Width, cbW)
	}
	if repl && !wSpec {
		rw, _ := c.replacedUsedSize(k, cbW, cbH, true)
		wval, wSpec = rw, true
	}
	if !wSpec && st.AspectRatio > 0 {
		if hv, ok := c.specH(k, st.Height, cbH); ok {
			wval, wSpec = hv*st.AspectRatio, true
		} else if !tAuto && !bAuto {
			hh := cbH - tv - bv - k.mar[0] - k.mar[2] - k.vExtra()
			wval, wSpec = hh*st.AspectRatio, true
		}
	}
	if wSpec {
		cw = wval
	}
	freeW := func(cwv float64) float64 {
		return cbW - lvOr(lAuto, lv) - rvOr(rAuto, rv) - k.mar[1] - k.mar[3] - hExtra - cwv
	}
	switch {
	case !lAuto && !rAuto && !wSpec:
		cw = cbW - lv - rv - k.mar[1] - k.mar[3] - hExtra
		cw = math.Max(0, cw)
	case !wSpec:
		// shrink-to-fit
		avail := cbW
		if !lAuto {
			avail -= lv
		} else {
			avail -= sx - 0
			if !rAuto {
				avail = cbW - rv
			}
		}
		if !lAuto && !rAuto {
			avail = cbW - lv - rv
		} else if lAuto && !rAuto {
			avail = cbW - rv
		} else if !lAuto && rAuto {
			avail = cbW - lv
		} else {
			avail = cbW - sx
		}
		avail = avail - k.mar[1] - k.mar[3] - hExtra
		mn, mx := c.minContent(k), c.maxContent(k)
		cw = math.Min(math.Max(mn, avail), mx)
	}
	cw = c.clampW(k, cw, cbW)
	bw := cw + hExtra
	// horizontal position
	var xpos float64
	switch {
	case !lAuto && !rAuto:
		rest := cbW - lv - rv - bw - k.mar[1] - k.mar[3]
		ml, mr := k.mar[3], k.mar[1]
		switch {
		case k.marAuto[3] && k.marAuto[1]:
			if rest >= 0 {
				ml, mr = rest/2, rest/2
			} else {
				ml, mr = 0, rest
			}
		case k.marAuto[3]:
			ml = rest + k.mar[3]
		case k.marAuto[1]:
			mr = rest + k.mar[1]
		}
		k.mar[3], k.mar[1] = ml, mr
		xpos = lv + ml
	case !lAuto:
		if k.marAuto[3] {
			k.mar[3] = 0
		}
		xpos = lv + k.mar[3]
	case !rAuto:
		xpos = cbW - rv - k.mar[1] - bw
	default:
		xpos = sx + k.mar[3]
	}
	// layout content with the width fixed
	in := layoutIn{cbw: cbW, cbh: cbH, fixW: bw, fixH: -1, bx: 0, by: 0, bfc: &bfcState{}}
	// height: resolve before layout when determined by insets
	_, hSpec := c.specH(k, st.Height, cbH)
	if !hSpec && !tAuto && !bAuto && !repl {
		hh := cbH - tv - bv - k.mar[0] - k.mar[2]
		if hh >= 0 {
			in.fixH = math.Max(0, hh)
			if st.MaxHeight.K != style.LNone || st.MinHeight.K != style.LAuto {
				in.fixH = c.clampH(k, in.fixH-k.vExtra(), cbH, 0) + k.vExtra()
			}
		}
	}
	k.hasLast = false
	savedCB := c.cbStack
	c.cbStack = nil
	c.layoutBox(k, in)
	c.cbStack = savedCB
	// vertical position
	bh := k.h
	var ypos float64
	switch {
	case !tAuto && !bAuto:
		rest := cbH - tv - bv - bh - k.mar[0] - k.mar[2]
		mt, mb := k.mar[0], k.mar[2]
		switch {
		case k.marAuto[0] && k.marAuto[2]:
			mt, mb = rest/2, rest/2
		case k.marAuto[0]:
			mt = rest + k.mar[0]
		case k.marAuto[2]:
			mb = rest + k.mar[2]
		}
		k.mar[0], k.mar[2] = mt, mb
		ypos = tv + mt
	case !tAuto:
		ypos = tv + k.mar[0]
	case !bAuto:
		ypos = cbH - bv - k.mar[2] - bh
	default:
		ypos = sy + k.mar[0]
	}
	k.x = ox + xpos
	k.y = oy + ypos
	k.oofDone = true
	_ = freeW
	_ = rAuto
}

func lvOr(auto bool, v float64) float64 {
	if auto {
		return 0
	}
	return v
}
func rvOr(auto bool, v float64) float64 { return lvOr(auto, v) }

// staticInCB converts the static position recorded relative to k.lp into the
// cb padding-box coordinate system.
func (c *ctx) staticInCB(k, cb *lbox, ox, oy float64) (float64, float64) {
	x, y := k.staticX, k.staticY
	for p := k.lp; p != nil && p != cb; p = p.lp {
		x += p.x
		y += p.y
		if p.lp == nil {
			break
		}
	}
	return x - ox, y - oy
}

func (b *lbox) floatCount() int {
	n := 0
	for _, k := range b.kids {
		if k.floated {
			n++
		}
	}
	return n
}
