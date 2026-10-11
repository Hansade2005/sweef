package render

import (
	"math"
	"sort"
	"strings"

	"github.com/sweefbrowser/sweef/internal/style"
)

type fitem struct {
	b          *lbox
	order      int
	idx        int
	mMain      [2]float64 // margin start, end along main
	mCross     [2]float64
	autoMain   [2]bool
	autoCross  [2]bool
	pbMain     float64
	pbCross    float64
	base       float64 // flex base size (border-box)
	hypo       float64 // hypothetical main size (border-box)
	minMain    float64 // border-box
	maxMain    float64
	target     float64
	frozen     bool
	grow       float64
	shrink     float64
	cross      float64 // hypothetical cross size (border-box)
	crossFinal float64
	mainPos    float64
	crossPos   float64
	align      string
	baseOff    float64 // baseline offset from margin-box cross start
	hasBase    bool
}

func stripSafe(v string) string {
	v = strings.TrimPrefix(v, "safe ")
	v = strings.TrimPrefix(v, "unsafe ")
	return v
}

func flexDir(b *lbox) (column, reverse bool) {
	st := b.st
	if st.Display == "-webkit-box" || st.Display == "-webkit-inline-box" {
		return st.BoxOrient == "vertical" || st.BoxOrient == "block-axis", false
	}
	switch st.FlexDirection {
	case "column":
		return true, false
	case "column-reverse":
		return true, true
	case "row-reverse":
		return false, true
	}
	return false, false
}

func (c *ctx) flexItems(b *lbox) []*fitem {
	var items []*fitem
	for i, k := range b.kids {
		if k.oof || k.kind == kText || k.kind == kBreak || k.kind == kMarker || k.kind == kInline {
			continue
		}
		items = append(items, &fitem{b: k, order: k.st.Order, idx: i})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].order < items[j].order })
	return items
}

func (c *ctx) layoutFlexContent(b *lbox, cw, cbh float64, in layoutIn) float64 {
	st := b.st
	column, reverse := flexDir(b)
	items := c.flexItems(b)
	wrap := st.FlexWrap != "nowrap" && st.Display != "-webkit-box" && st.Display != "-webkit-inline-box"
	wrapRev := st.FlexWrap == "wrap-reverse"
	// gaps
	cgap := 0.0
	if st.ColumnGap.K != style.LAuto {
		cgap = rl(st.ColumnGap, cw)
	}
	rgap := 0.0
	if st.RowGap.K != style.LAuto {
		if cbh >= 0 {
			rgap = rl(st.RowGap, cbh)
		} else {
			rgap = rl(st.RowGap, 0)
		}
	}
	mainGap, crossGap := cgap, rgap
	if column {
		mainGap, crossGap = rgap, cgap
	}
	// container inner sizes
	var innerMain, innerCross float64
	mainDefinite, crossDefinite := false, false
	if column {
		innerCross = cw
		crossDefinite = true
		if cbh >= 0 {
			innerMain, mainDefinite = cbh, true
		}
	} else {
		innerMain, mainDefinite = cw, true
		if cbh >= 0 {
			innerCross, crossDefinite = cbh, true
		}
	}
	// resolve item edges and bases
	for _, it := range items {
		k := it.b
		c.edges(k, cw)
		if column {
			it.mMain = [2]float64{k.mar[0], k.mar[2]}
			it.mCross = [2]float64{k.mar[3], k.mar[1]}
			it.autoMain = [2]bool{k.marAuto[0], k.marAuto[2]}
			it.autoCross = [2]bool{k.marAuto[3], k.marAuto[1]}
			it.pbMain, it.pbCross = k.vExtra(), k.hExtra()
		} else {
			it.mMain = [2]float64{k.mar[3], k.mar[1]}
			it.mCross = [2]float64{k.mar[0], k.mar[2]}
			it.autoMain = [2]bool{k.marAuto[3], k.marAuto[1]}
			it.autoCross = [2]bool{k.marAuto[0], k.marAuto[2]}
			it.pbMain, it.pbCross = k.hExtra(), k.vExtra()
		}
		it.grow, it.shrink = k.st.FlexGrow, k.st.FlexShrink
		if st.Display == "-webkit-box" || st.Display == "-webkit-inline-box" {
			it.shrink = 0
			if it.grow == 0 && k.st.FlexGrow == 0 {
				it.grow = 0
			}
		}
		al := k.st.AlignSelf
		if al == "auto" || al == "" {
			al = st.AlignItems
		}
		al = stripSafe(al)
		if al == "normal" || al == "" {
			al = "stretch"
		}
		it.align = al
		if wrapRev {
			// handled by line ordering
		}
	}
	// cross size of an item for column layout (needed for base size)
	crossSizeFor := func(it *fitem) (float64, bool) {
		k := it.b
		if column {
			// item width
			if w, ok := c.specW(k, k.st.Width, cw); ok && !k.st.Width.IsIntrinsic() || k.st.Width.IsIntrinsic() && ok {
				w = c.clampW(k, w, cw)
				return w + k.hExtra(), true
			}
			if it.align == "stretch" && !it.autoCross[0] && !it.autoCross[1] {
				w := cw - it.mCross[0] - it.mCross[1]
				cwc := c.clampW(k, w-k.hExtra(), cw)
				return cwc + k.hExtra(), true
			}
			mn, mx := c.minContent(k), c.maxContent(k)
			avail := cw - it.mCross[0] - it.mCross[1] - k.hExtra()
			v := math.Min(math.Max(mn, avail), mx)
			v = c.clampW(k, v, cw)
			return v + k.hExtra(), false
		}
		return 0, false
	}
	// flex base sizes and hypothetical sizes
	for _, it := range items {
		k := it.b
		st2 := k.st
		basis := st2.FlexBasis
		var content float64
		if !column {
			// width based
			switch {
			case basis.K == style.LAuto:
				if w, ok := c.specW(k, st2.Width, cw); ok {
					content = w
				} else {
					content = c.maxContent(k)
					if k.st.AspectRatio > 0 || k.rep != nil {
						content = c.rowRepContent(k, cw, cbh, content)
					}
				}
			case basis.K == style.LMaxContent && basis.V == -2:
				content = c.maxContent(k)
			default:
				if w, ok := c.specW(k, basis, cw); ok {
					content = w
				} else {
					content = c.maxContent(k)
				}
			}
			it.base = content + k.hExtra()
			// min / max
			it.maxMain = math.Inf(1)
			if st2.MaxWidth.K != style.LNone && st2.MaxWidth.K != style.LAuto {
				if v, ok := c.specW(k, st2.MaxWidth, cw); ok {
					it.maxMain = v + k.hExtra()
				}
			}
			if st2.MinWidth.K == style.LAuto || st2.MinWidth.K == style.LNone {
				if k.overflowX() != "visible" && k.overflowX() != "clip" {
					it.minMain = 0
				} else {
					sug := c.minContent(k) + k.hExtra()
					if w, ok := c.specW(k, st2.Width, cw); ok {
						sug = math.Min(sug, w+k.hExtra())
					} else if k.rep != nil && k.inner == iReplaced {
						rw, _ := c.replacedUsedSize(k, cw, cbh, false)
						sug = math.Min(sug, rw+k.hExtra())
					}
					if sug > it.maxMain {
						sug = it.maxMain
					}
					it.minMain = sug
				}
			} else if v, ok := c.specW(k, st2.MinWidth, cw); ok {
				it.minMain = v + k.hExtra()
			}
		} else {
			cross, _ := crossSizeFor(it)
			switch {
			case basis.K == style.LAuto && !(st2.Height.K == style.LPx || (st2.Height.K == style.LPct || st2.Height.K == style.LCalc) && mainDefinite):
				content = c.contentHeightAt(k, cross, cw)
			case basis.K == style.LAuto:
				content, _ = c.specH(k, st2.Height, innerMain)
			case basis.K == style.LMaxContent && basis.V == -2:
				content = c.contentHeightAt(k, cross, cw)
			default:
				if h, ok := c.specH(k, basis, innerMainOr(mainDefinite, innerMain)); ok {
					content = h
				} else {
					content = c.contentHeightAt(k, cross, cw)
				}
			}
			it.base = content + k.vExtra()
			it.maxMain = math.Inf(1)
			if st2.MaxHeight.K != style.LNone && st2.MaxHeight.K != style.LAuto {
				if v, ok := c.specH(k, st2.MaxHeight, innerMainOr(mainDefinite, innerMain)); ok {
					it.maxMain = v + k.vExtra()
				}
			}
			if st2.MinHeight.K == style.LAuto || st2.MinHeight.K == style.LNone {
				if k.overflowY() != "visible" && k.overflowY() != "clip" {
					it.minMain = 0
				} else {
					sug := c.contentHeightAt(k, cross, cw) + k.vExtra()
					if h, ok := c.specH(k, st2.Height, innerMainOr(mainDefinite, innerMain)); ok {
						sug = math.Min(sug, h+k.vExtra())
					}
					if sug > it.maxMain {
						sug = it.maxMain
					}
					it.minMain = sug
				}
			} else if v, ok := c.specH(k, st2.MinHeight, innerMainOr(mainDefinite, innerMain)); ok {
				it.minMain = v + k.vExtra()
			}
		}
		if it.base < 0 {
			it.base = 0
		}
		it.hypo = clampf(it.base, it.minMain, math.Max(it.maxMain, it.minMain))
	}
	outer := func(it *fitem, v float64) float64 { return v + it.mMain[0] + it.mMain[1] }
	// container main size when indefinite
	if !mainDefinite {
		sum := 0.0
		for i, it := range items {
			sum += outer(it, it.hypo)
			if i > 0 {
				sum += mainGap
			}
		}
		if wrap {
			// single line sized to content
		}
		innerMain = sum
		// apply container min/max on the main axis for column containers
		if column {
			if mx, ok := c.specH(b, st.MaxHeight, -1); ok && st.MaxHeight.K != style.LNone {
				innerMain = math.Min(innerMain, mx)
			}
			if mn, ok := c.specH(b, st.MinHeight, -1); ok {
				innerMain = math.Max(innerMain, mn)
			}
			if in.fixH >= 0 {
				innerMain = math.Max(0, in.fixH-b.vExtra()-b.sbH)
			}
		}
	}
	// collect lines
	var lines [][]*fitem
	if !wrap {
		lines = [][]*fitem{items}
	} else {
		var cur []*fitem
		used := 0.0
		for _, it := range items {
			o := outer(it, it.hypo)
			add := o
			if len(cur) > 0 {
				add += mainGap
			}
			if len(cur) > 0 && used+add > innerMain+0.001 {
				lines = append(lines, cur)
				cur, used = nil, 0
				add = o
			}
			cur = append(cur, it)
			used += add
		}
		if len(cur) > 0 {
			lines = append(lines, cur)
		}
	}
	if len(lines) == 0 {
		lines = [][]*fitem{nil}
	}
	// resolve flexible lengths per line
	for _, ln := range lines {
		c.resolveFlex(ln, innerMain, mainGap, outer)
	}
	// lay out items at their target sizes to learn cross sizes
	for _, ln := range lines {
		for _, it := range ln {
			k := it.b
			ccbh := -1.0
			if column {
				in2 := layoutIn{item: true, cbw: cw, cbh: innerMain, fixW: -1, fixH: it.target, bfc: &bfcState{}}
				cross, _ := crossSizeFor(it)
				in2.fixW = cross
				if it.align == "stretch" && !it.autoCross[0] && !it.autoCross[1] && k.st.Width.K == style.LAuto {
					in2.fixW = cw - it.mCross[0] - it.mCross[1]
				}
				c.layoutBox(k, in2)
				it.cross = k.w
				continue
			}
			if crossDefinite {
				ccbh = innerCross
			}
			in2 := layoutIn{item: true, cbw: cw, cbh: ccbh, fixW: it.target, fixH: -1, bfc: &bfcState{}}
			// definite height items
			if h, ok := c.specH(k, k.st.Height, innerCrossOr(crossDefinite, innerCross)); ok {
				in2.fixH = h + k.vExtra()
				in2.fixH = c.clampH(k, in2.fixH-k.vExtra(), innerCross, 0) + k.vExtra()
			}
			c.layoutBox(k, in2)
			it.cross = k.h
			if k.hasBase {
				it.baseOff = k.baseline + k.mar[0]
				it.hasBase = true
			}
		}
	}
	// line cross sizes
	lineCross := make([]float64, len(lines))
	for li, ln := range lines {
		m := 0.0
		for _, it := range ln {
			cs := it.cross + it.mCross[0] + it.mCross[1]
			if it.align == "baseline" && it.hasBase {
				continue
			}
			m = math.Max(m, cs)
		}
		// baseline-aligned items
		maxAbove, maxBelow := 0.0, 0.0
		anyBase := false
		for _, it := range ln {
			if it.align == "baseline" && it.hasBase && !column {
				anyBase = true
				above := it.baseOff
				below := it.cross + it.mCross[0] + it.mCross[1] - it.baseOff
				maxAbove = math.Max(maxAbove, above)
				maxBelow = math.Max(maxBelow, below)
			} else if it.align == "baseline" && !column {
				m = math.Max(m, it.cross+it.mCross[0]+it.mCross[1])
			}
		}
		if anyBase {
			m = math.Max(m, maxAbove+maxBelow)
		}
		lineCross[li] = m
	}
	// container cross size
	totalCross := 0.0
	for i, lc := range lineCross {
		totalCross += lc
		if i > 0 {
			totalCross += crossGap
		}
	}
	if !wrap && crossDefinite {
		lineCross[0] = innerCross
		totalCross = innerCross
	} else if !crossDefinite {
		innerCross = totalCross
		if !column {
			// clamp by container min/max height
			if mx, ok := c.specH(b, st.MaxHeight, -1); ok && st.MaxHeight.K != style.LNone {
				innerCross = math.Min(innerCross, mx)
			}
			if mn, ok := c.specH(b, st.MinHeight, -1); ok {
				innerCross = math.Max(innerCross, mn)
			}
			if in.fixH >= 0 {
				innerCross = math.Max(0, in.fixH-b.vExtra()-b.sbH)
			}
			if innerCross > totalCross && !wrap {
				lineCross[0] = innerCross
				totalCross = innerCross
			}
		}
	}
	// align-content for multi-line containers
	crossStart := make([]float64, len(lines))
	free := innerCross - totalCross
	ac := stripSafe(st.AlignContent)
	if ac == "normal" || ac == "" {
		ac = "stretch"
	}
	if !wrap {
		ac = "flex-start"
		crossStart[0] = 0
	} else {
		off := 0.0
		gapExtra := 0.0
		switch ac {
		case "stretch":
			if free > 0 {
				for i := range lineCross {
					lineCross[i] += free / float64(len(lines))
				}
			}
		case "flex-end", "end":
			off = free
		case "center":
			off = free / 2
		case "space-between":
			if len(lines) > 1 {
				gapExtra = free / float64(len(lines)-1)
			}
		case "space-around":
			gapExtra = free / float64(len(lines))
			off = gapExtra / 2
		case "space-evenly":
			gapExtra = free / float64(len(lines)+1)
			off = gapExtra
		}
		if free < 0 && (ac == "space-between" || ac == "space-around" || ac == "space-evenly") {
			gapExtra, off = 0, 0
		}
		pos := off
		for i := range lines {
			crossStart[i] = pos
			pos += lineCross[i] + crossGap + gapExtra
		}
	}
	if !wrap {
		crossStart[0] = 0
	}
	if wrapRev && wrap {
		// reverse the line order
		total := innerCross
		for i := range lines {
			crossStart[i] = total - crossStart[i] - lineCross[i]
		}
	}
	// final layout of items: cross sizes, alignment, positions
	for li, ln := range lines {
		lc := lineCross[li]
		// main axis alignment
		used := 0.0
		autoCount := 0
		for _, it := range ln {
			used += outer(it, it.target)
			for _, a := range it.autoMain {
				if a {
					autoCount++
				}
			}
		}
		used += mainGap * float64(maxInt(len(ln)-1, 0))
		freeMain := innerMain - used
		jc := stripSafe(st.JustifyContent)
		if st.Display == "-webkit-box" || st.Display == "-webkit-inline-box" {
			if jc == "" || jc == "normal" {
				jc = "flex-start"
			}
		}
		lead, between := 0.0, mainGap
		autoShare := 0.0
		if autoCount > 0 && freeMain > 0 {
			autoShare = freeMain / float64(autoCount)
			freeMain = 0
		} else {
			if freeMain < 0 && (jc == "space-between" || jc == "space-around" || jc == "space-evenly") {
				jc = "flex-start"
			}
			switch jc {
			case "flex-end", "end", "right":
				lead = freeMain
				if reverse && (jc == "flex-end" || jc == "end") {
					lead = freeMain
				}
			case "center":
				lead = freeMain / 2
			case "space-between":
				if len(ln) > 1 {
					between = mainGap + freeMain/float64(len(ln)-1)
				}
			case "space-around":
				if len(ln) > 0 {
					s := freeMain / float64(len(ln))
					lead = s / 2
					between = mainGap + s
				}
			case "space-evenly":
				s := freeMain / float64(len(ln)+1)
				lead = s
				between = mainGap + s
			}
			if reverse {
				switch jc {
				case "flex-start", "normal", "start", "left", "stretch", "":
					lead = freeMain
				case "flex-end", "end", "right":
					lead = 0
				}
			}
		}
		// order along the main axis
		seq := ln
		if reverse {
			seq = make([]*fitem, len(ln))
			for i := range ln {
				seq[len(ln)-1-i] = ln[i]
			}
		}
		pos := lead
		// baseline reference
		maxAbove := 0.0
		for _, it := range ln {
			if it.align == "baseline" && it.hasBase && !column {
				maxAbove = math.Max(maxAbove, it.baseOff)
			}
		}
		for _, it := range seq {
			k := it.b
			ms, me := it.mMain[0], it.mMain[1]
			if it.autoMain[0] {
				ms += autoShare
			}
			if it.autoMain[1] {
				me += autoShare
			}
			pos += ms
			it.mainPos = pos
			pos += it.target + me + between
			// cross size
			cs := it.cross
			avail := lc - it.mCross[0] - it.mCross[1]
			crossAuto := false
			if column {
				crossAuto = k.st.Width.K == style.LAuto
			} else {
				crossAuto = k.st.Height.K == style.LAuto && k.st.AspectRatio == 0 || k.st.Height.K == style.LAuto
			}
			if it.align == "stretch" && !it.autoCross[0] && !it.autoCross[1] && crossAuto {
				cs = avail
				if column {
					cwc := c.clampW(k, cs-k.hExtra(), cw)
					cs = cwc + k.hExtra()
				} else {
					cs = c.clampH(k, cs-k.vExtra(), innerCross, 0) + k.vExtra()
				}
			}
			it.crossFinal = cs
			if cs != it.cross || !column && (cs != k.h) {
				if column {
					in2 := layoutIn{item: true, cbw: cw, cbh: innerMain, fixW: cs, fixH: it.target, bfc: &bfcState{}}
					c.layoutBox(k, in2)
				} else {
					ccbh := -1.0
					if crossDefinite || true {
						ccbh = cs - k.vExtra()
					}
					in2 := layoutIn{item: true, cbw: cw, cbh: ccbh, fixW: it.target, fixH: cs, bfc: &bfcState{}}
					c.layoutBox(k, in2)
				}
			}
			// cross position
			free := lc - cs - it.mCross[0] - it.mCross[1]
			var cp float64
			switch {
			case it.autoCross[0] && it.autoCross[1]:
				cp = it.mCross[0] + math.Max(0, free)/2
			case it.autoCross[0]:
				cp = it.mCross[0] + math.Max(0, free)
			case it.autoCross[1]:
				cp = it.mCross[0]
			default:
				al := it.align
				if wrapRev && wrap {
					switch al {
					case "flex-start", "start", "self-start":
						al = "flex-end"
					case "flex-end", "end", "self-end":
						al = "flex-start"
					}
				}
				switch al {
				case "flex-end", "end", "self-end":
					cp = it.mCross[0] + free
				case "center":
					cp = it.mCross[0] + free/2
				case "baseline":
					if it.hasBase && !column {
						cp = maxAbove - it.baseOff + it.mCross[0]
					} else {
						cp = it.mCross[0]
					}
				default:
					cp = it.mCross[0]
				}
			}
			it.crossPos = crossStart[li] + cp
		}
	}
	// write positions
	for _, ln := range lines {
		for _, it := range ln {
			k := it.b
			if column {
				k.x, k.y = it.crossPos, it.mainPos
			} else {
				k.x, k.y = it.mainPos, it.crossPos
			}
			k.lp = b
			if k.hasFloat || k.touchesBFC {
				b.touchesBFC = true
			}
		}
	}
	// baseline of the container: first item
	b.hasBase = false
	if len(items) > 0 {
		var f *fitem
		for _, it := range lines[0] {
			if it.align == "baseline" && it.hasBase {
				f = it
				break
			}
		}
		if f == nil && len(lines[0]) > 0 {
			f = lines[0][0]
			if reverse && !column {
				f = lines[0][len(lines[0])-1]
			}
		}
		if f != nil && f.b.hasBase {
			b.hasBase = true
			b.baseline = f.b.y + f.b.baseline
			b.lastBase = b.baseline
		}
	}
	for _, k := range b.kids {
		if k.oof {
			c.registerOOF(b, k, 0, 0, strut{}, true, cw)
		}
	}
	if column {
		return innerMain
	}
	return innerCross
}

func innerMainOr(def bool, v float64) float64 {
	if def {
		return v
	}
	return -1
}
func innerCrossOr(def bool, v float64) float64 { return innerMainOr(def, v) }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (c *ctx) rowRepContent(k *lbox, cw, cbh, dflt float64) float64 {
	if k.rep != nil && k.inner == iReplaced {
		w, _ := c.replacedUsedSize(k, cw, cbh, false)
		return w
	}
	if k.st.AspectRatio > 0 {
		if hh, ok := c.specH(k, k.st.Height, cbh); ok {
			return hh * k.st.AspectRatio
		}
	}
	return dflt
}

// contentHeightAt lays out k at a border-box width and returns its content height.
func (c *ctx) contentHeightAt(k *lbox, borderW, cbw float64) float64 {
	if k.inner == iReplaced {
		_, h := c.replacedUsedSize(k, cbw, -1, false)
		return h
	}
	in := layoutIn{item: true, cbw: cbw, cbh: -1, fixW: borderW, fixH: -1, bfc: &bfcState{}}
	// ignore a specified height: we want the natural content size
	save := k.st
	if k.st.Height.K != style.LAuto {
		cp := *k.st
		cp.Height = style.Length{}
		cp.MinHeight = style.Length{}
		cp.MaxHeight = style.Length{K: style.LNone}
		k.st = &cp
	}
	c.layoutBox(k, in)
	h := k.h - k.vExtra() - k.sbH
	if k.st != save {
		k.st = save
		k.hasLast = false
	}
	if k.contentH > 0 && k.contentH < h {
		h = k.contentH
	}
	return h
}

func (c *ctx) resolveFlex(ln []*fitem, innerMain, gap float64, outer func(*fitem, float64) float64) {
	if len(ln) == 0 {
		return
	}
	sumBase := 0.0
	for _, it := range ln {
		sumBase += outer(it, it.hypo)
	}
	sumBase += gap * float64(len(ln)-1)
	grow := sumBase < innerMain
	for _, it := range ln {
		it.frozen = false
		it.target = it.hypo
		if grow && it.grow == 0 || !grow && it.shrink == 0 || it.base > it.hypo && grow || it.base < it.hypo && !grow {
			it.frozen = true
			if it.grow == 0 && grow || it.shrink == 0 && !grow {
				it.target = it.hypo
			}
		}
	}
	for iter := 0; iter < len(ln)+2; iter++ {
		frozenSum, unfrozen := 0.0, 0
		totalFactor := 0.0
		for _, it := range ln {
			if it.frozen {
				frozenSum += outer(it, it.target)
			} else {
				frozenSum += outer(it, it.base)
				unfrozen++
				if grow {
					totalFactor += it.grow
				} else {
					totalFactor += it.shrink * it.base
				}
			}
		}
		if unfrozen == 0 {
			break
		}
		frozenSum += gap * float64(len(ln)-1)
		free := innerMain - frozenSum
		if grow {
			// sum of grow factors < 1 limits the distribution
			sumG := 0.0
			for _, it := range ln {
				if !it.frozen {
					sumG += it.grow
				}
			}
			if sumG < 1 {
				free = math.Min(free, free*sumG)
			}
		} else {
			sumS := 0.0
			for _, it := range ln {
				if !it.frozen {
					sumS += it.shrink
				}
			}
			_ = sumS
		}
		total := 0.0
		for _, it := range ln {
			if it.frozen {
				continue
			}
			if totalFactor > 0 {
				if grow {
					it.target = it.base + free*it.grow/totalFactor
				} else {
					it.target = it.base + free*(it.shrink*it.base)/totalFactor
				}
			} else {
				it.target = it.base
			}
			total += 0
		}
		// min/max violations
		viol := 0.0
		for _, it := range ln {
			if it.frozen {
				continue
			}
			cl := clampf(it.target, it.minMain, math.Max(it.maxMain, it.minMain))
			viol += cl - it.target
		}
		for _, it := range ln {
			if it.frozen {
				continue
			}
			cl := clampf(it.target, it.minMain, math.Max(it.maxMain, it.minMain))
			switch {
			case viol == 0:
				it.frozen = true
				it.target = cl
			case viol > 0 && cl > it.target:
				it.frozen = true
				it.target = cl
			case viol < 0 && cl < it.target:
				it.frozen = true
				it.target = cl
			}
		}
		allFrozen := true
		for _, it := range ln {
			if !it.frozen {
				allFrozen = false
			}
		}
		if allFrozen {
			break
		}
	}
	for _, it := range ln {
		if it.target < 0 {
			it.target = 0
		}
	}
}

func (c *ctx) flexIntrinsic(b *lbox) (float64, float64) {
	column, _ := flexDir(b)
	items := c.flexItems(b)
	st := b.st
	wrap := st.FlexWrap != "nowrap"
	gap := 0.0
	if st.ColumnGap.K == style.LPx {
		gap = st.ColumnGap.V
	}
	var mn, mx float64
	if column {
		for _, it := range items {
			mn = math.Max(mn, c.outerIntrinsic(it.b, true))
			mx = math.Max(mx, c.outerIntrinsic(it.b, false))
		}
		return mn, mx
	}
	sumMax, sumMin := 0.0, 0.0
	for i, it := range items {
		omx, omn := c.outerIntrinsic(it.b, false), c.outerIntrinsic(it.b, true)
		// basis-aware contributions
		if bs := it.b.st.FlexBasis; bs.K == style.LPx {
			omx = bs.V + it.b.hExtra() + it.b.mar[1] + it.b.mar[3]
			if it.b.st.BoxSizing == "border-box" {
				omx = bs.V + it.b.mar[1] + it.b.mar[3]
			}
			if it.b.st.FlexShrink > 0 {
				omn = math.Min(omn, omx)
			}
		}
		sumMax += omx
		sumMin += omn
		if i > 0 {
			sumMax += gap
			sumMin += gap
		}
		mn = math.Max(mn, omn)
	}
	if !wrap {
		mn = sumMin
	}
	return mn, sumMax
}
