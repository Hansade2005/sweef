package render

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/style"
)

// Multi-column layout.
//
// The in-flow content is first laid out as one tall column of the column
// width; the resulting leaf units (lines of inline formatting contexts and
// childless blocks) are then distributed over the columns, honouring forced
// breaks, break-inside: avoid, orphans and widows, and column-fill. A child
// with column-span: all splits the content into independently balanced
// segments laid out at the full container width in between.

type mcUnit struct {
	blk         *lbox
	ln          *line
	li, nl      int     // line index and line count within blk (inline contexts)
	x, y, w, h  float64 // container-content coordinates before fragmentation
	col         int
	idx         int
	top, bot    float64 // extent including the borders/padding of blocks this unit starts/ends
	botM        float64 // bot plus the trailing margin kept when a forced break follows
	owner       *mcBlock
	forceBefore bool
}

type mcBlock struct {
	b              *lbox
	parent         *mcBlock
	x, y           float64 // container coordinates of the border box before fragmentation
	units          []*mcUnit
	finalX, finalY float64
	kids           []*mcBlock
}

// mcSeg is a vertical range (container-content coordinates) in which the
// columns are in effect; column rules are drawn within these ranges only.
type mcSeg struct {
	y0, y1 float64
	ncol   int // columns actually holding content
}

func (b *lbox) isMulticol() bool {
	return b.st.ColumnCount > 0 || b.st.ColumnWidth.K != style.LAuto
}

func isSpanner(k *lbox) bool {
	return k.kind == kBlock && !k.oof && !k.floated && k.st.ColumnSpan == "all"
}

// layoutMulticolContent lays out the children in columns and returns the content height.
func (c *ctx) layoutMulticolContent(b *lbox, cw, cx, cy float64, bfc *bfcState, cbh float64, topAdj bool, in layoutIn) float64 {
	st := b.st
	gap := st.FontSize
	if st.ColumnGap.K != style.LAuto {
		gap = rl(st.ColumnGap, cw)
	}
	n := st.ColumnCount
	if st.ColumnWidth.K != style.LAuto {
		wv := st.ColumnWidth.V
		if n > 0 {
			maxN := int(math.Floor((cw + gap) / (wv + gap)))
			if maxN < 1 {
				maxN = 1
			}
			if maxN < n {
				n = maxN
			}
		} else {
			n = int(math.Floor((cw + gap) / (wv + gap)))
			if n < 1 {
				n = 1
			}
		}
	}
	if n < 1 {
		n = 1
	}
	colW := (cw - float64(n-1)*gap) / float64(n)
	if colW < 0 {
		colW = 0
	}
	b.mcN, b.mcW, b.mcGap = n, colW, gap
	b.mcSegs = b.mcSegs[:0]
	c.resetKids(b)
	full := b.kids
	defer func() { b.kids = full }()
	hasSpan := false
	for _, k := range full {
		if isSpanner(k) {
			hasSpan = true
		}
	}
	if n == 1 && !hasSpan {
		inner := &bfcState{}
		return c.layoutBlockKids(b, colW, 0, 0, inner, -1, false, false, true)
	}
	fixedH := -1.0
	if cbh >= 0 && !hasSpan {
		fixedH = cbh
	}
	y0 := 0.0
	i := 0
	segCount := 0
	maxUsed := 1
	for i < len(full) {
		j := i
		for j < len(full) && !isSpanner(full[j]) {
			j++
		}
		if j > i {
			seg := full[i:j]
			b.kids = seg
			inner := &bfcState{}
			h := c.layoutBlockKids(b, colW, 0, 0, inner, -1, false, false, true)
			b.kids = full
			colH := h
			used := 1
			if n > 1 {
				colH, used = c.mcFragment(b, seg, n, colW, gap, y0, fixedH, h)
			} else {
				for _, k := range seg {
					if k.kind == kBlock && !k.oof && !k.floated {
						k.y += y0
					}
				}
			}
			if colH > 0 || n > 1 {
				b.mcSegs = append(b.mcSegs, mcSeg{y0, y0 + colH, used})
				if used > maxUsed {
					maxUsed = used
				}
			}
			y0 += colH
			segCount++
		}
		if j < len(full) {
			sp := full[j]
			c.edges(sp, cw)
			sp.hasLast = false
			sin := layoutIn{cbw: cw, cbh: -1, fixW: -1, fixH: -1, bfc: &bfcState{}}
			c.layoutBox(sp, sin)
			y0 += sp.mar[0]
			sp.x, sp.y, sp.lp = sp.mar[3], y0, b
			y0 += sp.h + sp.mar[2]
			j++
		}
		i = j
	}
	b.mcN = maxUsed
	if fixedH >= 0 {
		return fixedH
	}
	return y0
}

// mcFragment distributes the units of one segment over the columns and
// returns the segment's column height.
func (c *ctx) mcFragment(b *lbox, kids []*lbox, n int, colW, gap, Y0, fixedH, h float64) (float64, int) {
	var units []*mcUnit
	root := &mcBlock{b: b}
	c.mcCollect(kids, root, 0, 0, &units)
	for i, u := range units {
		u.idx = i
		u.top, u.bot = u.y, u.y+u.h
		u.botM = u.bot
	}
	var edges func(mb *mcBlock)
	edges = func(mb *mcBlock) {
		for _, k := range mb.kids {
			if len(k.units) > 0 {
				if f := k.units[0]; k.y < f.top {
					f.top = k.y
				}
				l := k.units[len(k.units)-1]
				if k.y+k.b.h > l.bot {
					l.bot = k.y + k.b.h
				}
				if m := k.y + k.b.h + k.b.mbotOut; m > l.botM {
					l.botM = m
				}
			}
			edges(k)
		}
	}
	edges(root)
	for _, u := range units {
		if u.botM < u.bot {
			u.botM = u.bot
		}
	}
	if len(units) == 0 {
		for _, k := range kids {
			if k.kind == kBlock && !k.oof && !k.floated {
				k.y += Y0
			}
		}
		return 0, 1
	}
	c.mcMarkForced(root, units)
	st := b.st
	fillAuto := st.ColumnFill == "auto"
	var starts []int
	var H float64
	switch {
	case fixedH >= 0 && fillAuto:
		H = fixedH
	default:
		// like Blink: start from an even share of the content (margins
		// included) and only grow until the content fits in n columns
		lo := h / float64(n)
		for _, u := range units {
			lo = math.Max(lo, u.bot-u.top)
		}
		if len(mcBreaks(units, lo)) <= n {
			H = lo
		} else {
			hi := math.Max(h, lo)
			if len(mcBreaks(units, hi)) > n {
				hi = units[len(units)-1].bot - units[0].top
				if len(mcBreaks(units, hi)) > n {
					hi = lo * float64(n+1)
				}
			}
			best := hi
			for it := 0; it < 40 && hi-lo > 0.01; it++ {
				mid := (lo + hi) / 2
				if len(mcBreaks(units, mid)) <= n {
					best = mid
					hi = mid
				} else {
					lo = mid
				}
			}
			H = best
		}
		if fixedH >= 0 && fixedH < H {
			H = fixedH
		}
	}
	starts = mcBreaks(units, H)
	ncol := len(starts)
	for ci := 0; ci < ncol; ci++ {
		end := len(units)
		if ci+1 < ncol {
			end = starts[ci+1]
		}
		for _, u := range units[starts[ci]:end] {
			u.col = ci
		}
	}
	startY := make([]float64, ncol)
	for ci := range startY {
		startY[ci] = units[starts[ci]].top
	}
	startY[0] = 0 // the first column starts at the top of the segment (its first margin is kept)
	colH := 0.0
	for i, u := range units {
		bot := u.bot
		if i+1 < len(units) && units[i+1].forceBefore {
			bot = u.botM
		}
		colH = math.Max(colH, bot-startY[u.col])
	}
	if fillAuto && fixedH >= 0 {
		colH = fixedH
	} else if H > colH {
		colH = H
	}
	fragH := colH // fragmentainer height non-final fragments stretch to
	shift := func(col int) (float64, float64) {
		return float64(col) * (colW + gap), -startY[col] + Y0
	}
	var finalize func(mb *mcBlock, px, py float64)
	finalize = func(mb *mcBlock, px, py float64) {
		bb := mb.b
		fx, fy := mb.x, mb.y+Y0
		fw, fh := bb.w, bb.h
		if len(mb.units) > 0 {
			cmin, cmax := mb.units[0].col, mb.units[0].col
			for _, u := range mb.units {
				if u.col < cmin {
					cmin = u.col
				}
				if u.col > cmax {
					cmax = u.col
				}
			}
			dxMin, dyFirst := shift(mb.units[0].col)
			_, dyLast := shift(mb.units[len(mb.units)-1].col)
			dxMax, _ := shift(cmax)
			if cmin == cmax {
				fx, fy = mb.x+dxMin, mb.y+dyFirst
			} else {
				// fragments in several columns: bounding box; every fragment but the
				// last stretches to the bottom of its column
				x0, x1 := mb.x+dxMin, mb.x+bb.w+dxMax
				y0 := math.Min(mb.y+dyFirst, Y0)
				y1 := mb.y + bb.h + dyLast
				if bot := Y0 + fragH; bot > y1 {
					y1 = bot
				}
				fx, fy, fw, fh = x0, y0, x1-x0, y1-y0
			}
			if cmin == cmax {
				fw, fh = bb.w, bb.h
			}
		} else if mb.parent != nil && mb.parent.b != b {
			fx, fy = mb.parent.finalX+(mb.x-mb.parent.x), mb.parent.finalY+(mb.y-mb.parent.y)
		}
		mb.finalX, mb.finalY = fx, fy
		bb.x, bb.y = fx-px, fy-py
		bb.w, bb.h = fw, fh
		for _, ch := range mb.kids {
			finalize(ch, fx, fy)
		}
		// lines of IFC blocks
		for _, u := range mb.units {
			if u.ln != nil && u.owner == mb {
				dx, dy := shift(u.col)
				nx, ny := u.x+dx-fx, u.y+dy-fy
				ddx, ddy := nx-u.ln.x, ny-u.ln.y
				for _, fr := range u.ln.frags {
					if fr.kind == aAtomic {
						fr.box.x += ddx
						fr.box.y += ddy
					}
				}
				u.ln.x, u.ln.y = nx, ny
			}
		}
	}
	for _, ch := range root.kids {
		finalize(ch, 0, 0)
	}
	return colH, ncol
}

// mcBreaks greedily fills columns of height H and returns the index of the
// first unit of every column.
func mcBreaks(units []*mcUnit, H float64) []int {
	starts := []int{0}
	y0 := units[0].top
	i := 0
	for i < len(units) {
		colStart := starts[len(starts)-1]
		u := units[i]
		if i > colStart && u.forceBefore {
			starts = append(starts, i)
			y0 = u.top
			continue
		}
		if i > colStart && u.bot-y0 > H+0.01 {
			b := mcAdjustBreak(units, colStart, i)
			if b <= colStart {
				b = i
			}
			starts = append(starts, b)
			y0 = units[b].top
			i = b
			continue
		}
		i++
	}
	return starts
}

// mcAdjustBreak moves a break before unit b earlier to satisfy orphans,
// widows and break-inside: avoid; it returns colStart when that is
// impossible without emptying the column.
func mcAdjustBreak(units []*mcUnit, colStart, b int) int {
	for guard := 0; guard < 64; guard++ {
		ub, up := units[b], units[b-1]
		moved := false
		if ub.ln != nil && ub.blk == up.blk {
			L := ub.blk
			// first unit of L within this column
			f := b - 1
			for f-1 >= colStart && units[f-1].blk == L {
				f--
			}
			r := ub.nl - ub.li
			orphans, widows := L.st.Orphans, L.st.Widows
			if orphans < 1 {
				orphans = 1
			}
			if widows < 1 {
				widows = 1
			}
			first := units[f].li == 0
			nb := b
			if r < widows {
				nb = b - (widows - r)
			}
			if nb < f {
				nb = f
			}
			if first && nb-f < orphans {
				nb = f
			}
			if nb != b {
				if nb <= colStart {
					return colStart
				}
				b = nb
				moved = true
			}
		}
		// break-inside: avoid on an ancestor holding both neighbours
		if !moved {
			anc := map[*mcBlock]bool{}
			for p := up.owner; p != nil; p = p.parent {
				anc[p] = true
			}
			var avoid *mcBlock
			for p := ub.owner; p != nil; p = p.parent {
				if anc[p] && p.b != nil && p.b.st.BreakInside == "avoid" && len(p.units) > 0 {
					avoid = p // keep walking up: the outermost wins
				}
			}
			if avoid != nil {
				fi := avoid.units[0].idx
				if fi > colStart && fi < b {
					b = fi
					moved = true
				} else if fi <= colStart {
					// the block began in an earlier column: nothing to avoid here
				}
			}
		}
		if !moved {
			return b
		}
	}
	return b
}

// mcMarkForced flags the units that must start a new column.
func (c *ctx) mcMarkForced(mb *mcBlock, units []*mcUnit) {
	for _, k := range mb.kids {
		if len(k.units) > 0 {
			if k.b.st.BreakBefore == "column" {
				k.units[0].forceBefore = true
			}
			if k.b.st.BreakAfter == "column" {
				last := k.units[len(k.units)-1]
				if last.idx+1 < len(units) {
					units[last.idx+1].forceBefore = true
				}
			}
		}
		c.mcMarkForced(k, units)
	}
}

func (c *ctx) mcCollect(kids []*lbox, mb *mcBlock, ox, oy float64, units *[]*mcUnit) {
	for _, k := range kids {
		if k.kind != kBlock || k.oof || k.floated {
			continue
		}
		kb := &mcBlock{b: k, parent: mb, x: ox + k.x, y: oy + k.y}
		mb.kids = append(mb.kids, kb)
		add := func(u *mcUnit) {
			u.owner = kb
			*units = append(*units, u)
			for p := kb; p != nil; p = p.parent {
				p.units = append(p.units, u)
			}
		}
		if k.ifc {
			c.collectInlineKids(k)
			for li, ln := range k.lines {
				add(&mcUnit{blk: k, ln: ln, li: li, nl: len(k.lines), x: kb.x, y: kb.y + ln.y, w: k.w, h: ln.h})
			}
			continue
		}
		hasBlockKids := false
		for _, g := range k.kids {
			if g.kind == kBlock && !g.oof && !g.floated {
				hasBlockKids = true
			}
		}
		if !hasBlockKids {
			add(&mcUnit{blk: k, x: kb.x, y: kb.y, w: k.w, h: k.h})
			continue
		}
		c.mcCollect(k.kids, kb, kb.x, kb.y, units)
	}
}
