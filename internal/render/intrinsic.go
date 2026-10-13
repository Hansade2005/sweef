package render

import (
	"math"

	"github.com/sweefbrowser/sweef/internal/style"
)

// minContent / maxContent return content-box widths (no padding/border).
func (c *ctx) minContent(b *lbox) float64 {
	c.intrinsic(b)
	return b.minW
}

func (c *ctx) maxContent(b *lbox) float64 {
	c.intrinsic(b)
	return b.maxW
}

func (c *ctx) intrinsic(b *lbox) {
	if b.hasIntr {
		return
	}
	b.hasIntr = true // guards recursion
	var mn, mx float64
	if b.vflow != 0 && b.vt == 0 {
		// orthogonal flow root: its width is its block size, found by laying it out
		b.hasIntr = false
		saved := *b
		c.vIntrinsic = true
		defer func() { c.vIntrinsic = false }()
		c.layoutVertical(b, layoutIn{cbw: c.vw, cbh: -1, fixW: -1, fixH: -1, shrink: true, availW: c.vw, bfc: &bfcState{}})
		w := b.w - b.hExtra()
		*b = saved
		b.hasIntr = true
		b.minW, b.maxW = w, w
		return
	}
	switch b.inner {
	case iReplaced, iControl:
		mn, mx = c.replacedIntrinsic(b)
	case iFlex:
		mn, mx = c.flexIntrinsic(b)
	case iGrid:
		mn, mx = c.gridIntrinsic(b)
	case iTable:
		mn, mx = c.tableIntrinsic(b)
	default:
		if b.ifc {
			mn, mx = c.ifcIntrinsic(b)
		} else {
			mn, mx = c.blockIntrinsic(b)
		}
	}
	if b.st.ContainsInlineSize() && b.st.WritingMode != "vertical-rl" && b.st.WritingMode != "vertical-lr" {
		mn, mx = 0, 0
		if b.st.CIntrW.K == style.LPx {
			mn, mx = b.st.CIntrW.V, b.st.CIntrW.V
		}
	}
	// scrollbars
	if b.st.OverflowY == "scroll" {
		mn += sbSize
		mx += sbSize
	}
	b.minW, b.maxW = mn, mx
}

// outerIntrinsic returns the margin-box contribution of a child.
func (c *ctx) outerIntrinsic(k *lbox, wantMin bool) float64 {
	c.edges(k, 0)
	st := k.st
	extra := k.mar[1] + k.mar[3] + k.hExtra()
	var w float64
	switch st.Width.K {
	case style.LPx:
		w = st.Width.V
		if st.BoxSizing != "border-box" {
			w += k.hExtra()
		}
		w += k.mar[1] + k.mar[3]
		// min/max clamps
		return c.clampOuter(k, w, extra)
	case style.LMinContent:
		w = c.minContent(k) + extra
	case style.LMaxContent, style.LFitContent:
		w = c.maxContent(k) + extra
		if wantMin && st.Width.K == style.LFitContent {
			w = c.minContent(k) + extra
		}
	default:
		if wantMin {
			w = c.minContent(k) + extra
			if k.inner == iReplaced && (st.Width.K == style.LPct || st.MaxWidth.K == style.LPct) {
				w = extra
			}
		} else {
			w = c.maxContent(k) + extra
		}
	}
	return c.clampOuter(k, w, extra)
}

func (c *ctx) clampOuter(k *lbox, w, extra float64) float64 {
	st := k.st
	if st.MaxWidth.K == style.LPx {
		mx := st.MaxWidth.V
		if st.BoxSizing != "border-box" {
			mx += k.hExtra()
		}
		mx += k.mar[1] + k.mar[3]
		if w > mx {
			w = mx
		}
	}
	if st.MinWidth.K == style.LPx {
		mn := st.MinWidth.V
		if st.BoxSizing != "border-box" {
			mn += k.hExtra()
		}
		mn += k.mar[1] + k.mar[3]
		if w < mn {
			w = mn
		}
	}
	return w
}

func (c *ctx) blockIntrinsic(b *lbox) (float64, float64) {
	var mn, mx float64
	floatSumMax, floatSumMin := 0.0, 0.0
	for _, k := range b.kids {
		if k.kind == kText || k.kind == kMarker || k.kind == kBreak || k.kind == kInline || k.oof {
			continue
		}
		omn, omx := c.outerIntrinsic(k, true), c.outerIntrinsic(k, false)
		if k.floated {
			floatSumMax += omx
			floatSumMin = math.Max(floatSumMin, omn)
			continue
		}
		mn = math.Max(mn, omn)
		mx = math.Max(mx, omx)
	}
	mn = math.Max(mn, floatSumMin)
	mx = math.Max(mx, floatSumMax)
	return mn, mx
}

func (c *ctx) ifcIntrinsic(b *lbox) (float64, float64) {
	atoms := c.buildAtoms(b)
	var maxMax, maxMin, curLine, curRun, lastTrail float64
	lineHas := false
	floatMax := 0.0
	flush := func() {
		maxMax = math.Max(maxMax, curLine-lastTrail)
		curLine, lastTrail, lineHas = 0, 0, false
	}
	indent := 0.0
	if b.st.TextIndent.K == style.LPx {
		indent = b.st.TextIndent.V
	}
	curLine = indent
	for _, a := range atoms {
		switch a.kind {
		case aOpen:
			c.edges(a.box, 0)
			w := a.box.mar[3] + a.box.bor[3] + a.box.pad[3]
			curLine += w
			curRun += w
		case aClose:
			w := a.box.mar[1] + a.box.bor[1] + a.box.pad[1]
			curLine += w
			curRun += w
		case aText:
			if a.space && !lineHas && !preservesSpace(a.it.st) {
				continue
			}
			w := a.w
			if a.tab {
				w = a.it.fs.spaceW * 8
			}
			curLine += w
			lastTrail = a.trail
			curRun += w - a.trail
			if !a.space || preservesSpace(a.it.st) {
				lineHas = true
			}
			if a.brk {
				maxMin = math.Max(maxMin, curRun)
				curRun = 0
			} else {
				lastTrail = 0
				if a.trail > 0 {
					lastTrail = a.trail
				}
			}
			if a.it.st.OverflowWrap == "anywhere" || a.it.st.WordBreak == "break-all" {
				// min-content can break anywhere: widest character
				maxMin = math.Max(maxMin, charMax(a))
			}
		case aAtomic:
			ab := a.box
			c.edges(ab, 0)
			w := c.outerIntrinsic(ab, false)
			wmin := c.outerIntrinsic(ab, true)
			if ab.st.Width.K == style.LPct || ab.st.MaxWidth.K == style.LPct {
				wmin = ab.mar[1] + ab.mar[3] + ab.hExtra()
				if ab.rep != nil {
					wmin = math.Min(wmin, w)
				}
			}
			maxMin = math.Max(maxMin, curRun)
			curRun = 0
			maxMin = math.Max(maxMin, wmin)
			curLine += w
			lastTrail = 0
			lineHas = true
		case aFloat:
			fb := a.box
			floatMax += c.outerIntrinsic(fb, false)
			maxMin = math.Max(maxMin, c.outerIntrinsic(fb, true))
		case aBreak:
			maxMin = math.Max(maxMin, curRun)
			curRun = 0
			flush()
			curLine = 0
		}
	}
	maxMin = math.Max(maxMin, curRun)
	flush()
	if indent > 0 {
		maxMin += 0
	}
	maxMax = math.Max(maxMax, 0)
	if floatMax > 0 {
		maxMax = math.Max(maxMax, floatMax)
	}
	return maxMin, maxMax
}

func charMax(a atom) float64 {
	m := 0.0
	for k := a.a; k < a.e; k++ {
		m = math.Max(m, a.it.sh.x[k+1]-a.it.sh.x[k])
	}
	return m
}
