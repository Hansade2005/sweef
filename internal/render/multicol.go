package render

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/style"
)

type mcUnit struct {
	blk        *lbox
	ln         *line
	x, y, w, h float64 // container-content coordinates before fragmentation
	col        int
}

type mcBlock struct {
	b              *lbox
	parent         *mcBlock
	x, y           float64 // container coordinates of the border box before fragmentation
	units          []*mcUnit
	finalX, finalY float64
	kids           []*mcBlock
}

func (b *lbox) isMulticol() bool {
	return b.st.ColumnCount > 0 || b.st.ColumnWidth.K != style.LAuto
}

// layoutMulticolContent lays out the children in columns and returns the content height.
func (c *ctx) layoutMulticolContent(b *lbox, cw, cx, cy float64, bfc *bfcState, cbh float64, topAdj bool, in layoutIn) float64 {
	st := b.st
	gap := 16.0 * 0
	gap = st.FontSize
	if st.ColumnGap.K != style.LAuto {
		gap = rl(st.ColumnGap, cw)
	}
	n := st.ColumnCount
	var colW float64
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
	colW = (cw - float64(n-1)*gap) / float64(n)
	if colW < 0 {
		colW = 0
	}
	b.mcN, b.mcW, b.mcGap = n, colW, gap
	c.resetKids(b)
	// lay out as one tall column
	inner := &bfcState{}
	h := c.layoutBlockKids(b, colW, 0, 0, inner, -1, false, false, true)
	if n == 1 {
		return h
	}
	// collect units
	var units []*mcUnit
	var root = &mcBlock{b: b}
	c.mcCollect(b, root, 0, 0, &units)
	if len(units) == 0 {
		return h
	}
	fixedH := -1.0
	if cbh >= 0 {
		fixedH = cbh
	}
	breakAt := func(H float64) (int, []float64) {
		cols := 1
		y0 := 0.0
		starts := []float64{0}
		for i, u := range units {
			if u.y+u.h-y0 > H+0.01 && i > 0 && u.y > y0+0.001 {
				cols++
				y0 = u.y
				starts = append(starts, y0)
			}
		}
		return cols, starts
	}
	lo, hi := 0.0, h
	for _, u := range units {
		lo = math.Max(lo, u.h)
	}
	best := h
	if fixedH >= 0 && fixedH < h {
		// definite height: fill columns sequentially up to the height
		best = fixedH
	} else {
		for it := 0; it < 40 && hi-lo > 0.01; it++ {
			mid := (lo + hi) / 2
			cols, _ := breakAt(mid)
			if cols <= n {
				best = mid
				hi = mid
			} else {
				lo = mid
			}
		}
		_, _ = breakAt(best)
	}
	cols, starts := breakAt(best)
	_ = cols
	// assign columns
	ci := 0
	for _, u := range units {
		for ci+1 < len(starts) && u.y >= starts[ci+1]-0.001 {
			ci++
		}
		u.col = ci
	}
	// final column height
	colH := 0.0
	for _, u := range units {
		colH = math.Max(colH, u.y+u.h-starts[u.col])
	}
	// final rects
	shift := func(col int) (float64, float64) { return float64(col) * (colW + gap), -starts[col] }
	var finalize func(mb *mcBlock, px, py float64)
	finalize = func(mb *mcBlock, px, py float64) {
		bb := mb.b
		fx, fy := mb.x, mb.y
		fw, fh := bb.w, bb.h
		if len(mb.units) > 0 {
			c0 := mb.units[0].col
			single := true
			for _, u := range mb.units {
				if u.col != c0 {
					single = false
				}
			}
			if single {
				dx, dy := shift(c0)
				fx, fy = mb.x+dx, mb.y+dy
			} else {
				x0, y0, x1, y1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
				for _, u := range mb.units {
					dx, dy := shift(u.col)
					x0 = math.Min(x0, u.x+dx)
					y0 = math.Min(y0, u.y+dy)
					x1 = math.Max(x1, u.x+u.w+dx)
					y1 = math.Max(y1, u.y+u.h+dy)
				}
				fx, fy, fw, fh = x0, y0, x1-x0, y1-y0
			}
		} else if mb.parent != nil {
			// empty block: follow the parent
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
			if u.ln != nil {
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
	if fixedH >= 0 {
		return fixedH
	}
	return colH
}

func (c *ctx) mcCollect(b *lbox, mb *mcBlock, ox, oy float64, units *[]*mcUnit) {
	for _, k := range b.kids {
		if k.kind != kBlock || k.oof || k.floated {
			continue
		}
		kb := &mcBlock{b: k, parent: mb, x: ox + k.x, y: oy + k.y}
		mb.kids = append(mb.kids, kb)
		if k.ifc {
			c.collectInlineKids(k)
			for _, ln := range k.lines {
				u := &mcUnit{blk: k, ln: ln, x: kb.x, y: kb.y + ln.y, w: k.w, h: ln.h}
				*units = append(*units, u)
				for p := kb; p != nil; p = p.parent {
					p.units = append(p.units, u)
				}
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
			u := &mcUnit{blk: k, x: kb.x, y: kb.y, w: k.w, h: k.h}
			*units = append(*units, u)
			for p := kb; p != nil; p = p.parent {
				p.units = append(p.units, u)
			}
			continue
		}
		c.mcCollect(k, kb, kb.x, kb.y, units)
	}
}
