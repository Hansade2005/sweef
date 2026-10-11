package render

import (
	"math"
	"sort"
	"strings"

	"github.com/sweefbrowser/sweef/internal/style"
)

type gtrack struct {
	minF, maxF style.Sz
	base       float64
	limit      float64
	flex       float64
	isFlex     bool
	size       float64
	pos        float64
	fitMax     float64
	hasFit     bool
	shift      float64
}

type gitem struct {
	b              *lbox
	r0, rs, c0, cs int
	rDef, cDef     bool
	order          int
	idx            int
	areaW, areaH   float64
	x, y           float64
}

type gridPlace struct{}

type gridModel struct {
	cols, rows []*gtrack
	items      []*gitem
	colNames   map[string][]int
	rowNames   map[string][]int
}

func (c *ctx) gridTrackFromDef(t style.Track, avail float64) *gtrack {
	tr := &gtrack{minF: t.Min, maxF: t.Max}
	return tr
}

// expandTemplate builds the explicit track list for one axis.
func (c *ctx) expandTemplate(tl style.TrackList, avail, gap float64, autoSizes []style.Track) ([]style.Track, [][]string, []string) {
	var tracks []style.Track
	tracks = append(tracks, tl.Pre...)
	if len(tl.Rep) > 0 {
		count := 1
		if avail >= 0 {
			// compute the repeat count that fits
			fixed := 0.0
			for _, t := range tl.Pre {
				fixed += trackFixedSize(t, avail)
			}
			for _, t := range tl.Post {
				fixed += trackFixedSize(t, avail)
			}
			per := 0.0
			for _, t := range tl.Rep {
				per += trackFixedSize(t, avail)
			}
			ntr := float64(len(tl.Pre) + len(tl.Post))
			// each repetition adds len(Rep) tracks and gaps
			if per+gap*float64(len(tl.Rep)) > 0 {
				count = int(math.Floor((avail - fixed - ntr*gap + gap) / (per + gap*float64(len(tl.Rep)))))
				// recompute carefully: total = fixed + per*n + gap*(ntr + len(Rep)*n - 1) <= avail
				count = 0
				for n := 1; n < 4000; n++ {
					total := fixed + per*float64(n) + gap*(ntr+float64(len(tl.Rep)*n)-1)
					if total <= avail+1e-6 {
						count = n
					} else {
						break
					}
				}
			}
			if count < 1 {
				count = 1
			}
		}
		for i := 0; i < count; i++ {
			tracks = append(tracks, tl.Rep...)
		}
	}
	tracks = append(tracks, tl.Post...)
	return tracks, nil, tl.EndNames
}

func trackFixedSize(t style.Track, avail float64) float64 {
	v := func(s style.Sz) (float64, bool) {
		if s.K == style.SzLen {
			if s.L.K == style.LPct || s.L.K == style.LCalc {
				if avail < 0 {
					return 0, false
				}
				return s.L.Resolve(avail), true
			}
			return s.L.V, true
		}
		return 0, false
	}
	if x, ok := v(t.Max); ok {
		if m, ok2 := v(t.Min); ok2 && t.Min.K == style.SzLen && m > x {
			return m
		}
		return x
	}
	if x, ok := v(t.Min); ok {
		return x
	}
	return 0
}

func (c *ctx) buildLineNames(tracks []style.Track, end []string) map[string][]int {
	m := map[string][]int{}
	for i, t := range tracks {
		for _, n := range t.Names {
			m[n] = append(m[n], i)
		}
	}
	for _, n := range end {
		m[n] = append(m[n], len(tracks))
	}
	return m
}

func (gm *gridModel) resolveLine(g style.GridLine, names map[string][]int, areaLine func(string, bool) (int, bool), isEnd bool, ntracks int) (int, bool) {
	if g.Auto || !g.Set {
		return 0, false
	}
	if g.Name != "" {
		occ := g.Num
		if occ == 0 {
			occ = 1
		}
		if l, ok := names[g.Name]; ok && len(l) > 0 {
			if occ > 0 {
				if occ <= len(l) {
					return l[occ-1], true
				}
				return l[len(l)-1] + (occ - len(l)), true
			}
			idx := len(l) + occ
			if idx >= 0 {
				return l[idx], true
			}
		}
		if areaLine != nil {
			if v, ok := areaLine(g.Name, isEnd); ok {
				return v, true
			}
		}
		// an unknown line name resolves to the first implicit line
		return ntracks, true
	}
	if g.Num > 0 {
		return g.Num - 1, true
	}
	if g.Num < 0 {
		return ntracks + g.Num + 1, true
	}
	return 0, false
}

func (c *ctx) layoutGridContent(b *lbox, cw, cbh float64, in layoutIn) float64 {
	st := b.st
	// gaps
	cgap, rgap := 0.0, 0.0
	if st.ColumnGap.K != style.LAuto {
		cgap = rl(st.ColumnGap, cw)
	}
	if st.RowGap.K != style.LAuto {
		if cbh >= 0 {
			rgap = rl(st.RowGap, cbh)
		} else {
			rgap = rl(st.RowGap, 0)
		}
	}
	colDefs, _, colEnd := c.expandTemplate(st.GridCols, cw, cgap, st.GridAutoCols)
	rowDefs, _, rowEnd := c.expandTemplate(st.GridRows, cbh, rgap, st.GridAutoRows)
	if st.GridCols.None {
		colDefs = nil
	}
	if st.GridRows.None {
		rowDefs = nil
	}
	colNames := c.buildLineNames(colDefs, colEnd)
	rowNames := c.buildLineNames(rowDefs, rowEnd)
	// areas
	areaPos := map[string][4]int{} // r0,r1,c0,c1
	for ri, row := range st.GridAreas {
		for ci, name := range row {
			if name == "." {
				continue
			}
			a, ok := areaPos[name]
			if !ok {
				areaPos[name] = [4]int{ri, ri + 1, ci, ci + 1}
				continue
			}
			if ri+1 > a[1] {
				a[1] = ri + 1
			}
			if ci+1 > a[3] {
				a[3] = ci + 1
			}
			if ci < a[2] {
				a[2] = ci
			}
			areaPos[name] = a
		}
	}
	// implicit explicit-grid size from areas
	nrows := len(rowDefs)
	ncols := len(colDefs)
	if len(st.GridAreas) > nrows {
		nrows = len(st.GridAreas)
	}
	for _, row := range st.GridAreas {
		if len(row) > ncols {
			ncols = len(row)
		}
	}
	gm := &gridModel{colNames: colNames, rowNames: rowNames}
	rowArea := func(n string, isEnd bool) (int, bool) {
		n = strings.TrimSuffix(strings.TrimSuffix(n, "-start"), "-end")
		a, ok := areaPos[n]
		if !ok {
			return 0, false
		}
		if isEnd {
			return a[1], true
		}
		return a[0], true
	}
	colArea := func(n string, isEnd bool) (int, bool) {
		n = strings.TrimSuffix(strings.TrimSuffix(n, "-start"), "-end")
		a, ok := areaPos[n]
		if !ok {
			return 0, false
		}
		if isEnd {
			return a[3], true
		}
		return a[2], true
	}
	// items
	var items []*gitem
	for i, k := range b.kids {
		if k.oof || k.kind == kText || k.kind == kBreak || k.kind == kMarker || k.kind == kInline {
			continue
		}
		it := &gitem{b: k, order: k.st.Order, idx: i}
		items = append(items, it)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].order < items[j].order })
	// resolve explicit positions
	type axisPlace struct {
		start, span int
		def         bool
	}
	place := func(s, e style.GridLine, names map[string][]int, area func(string, bool) (int, bool), n int) axisPlace {
		// named area shorthand: a lone name refers to the area on both lines
		sv, sOK := gm.resolveLine(s, names, area, false, n)
		ev, eOK := gm.resolveLine(e, names, area, true, n)
		ssp, esp := 1, 1
		if s.Span > 0 {
			ssp = s.Span
		}
		if e.Span > 0 {
			esp = e.Span
		}
		switch {
		case sOK && eOK:
			if ev < sv {
				sv, ev = ev, sv
			}
			if ev == sv {
				ev = sv + 1
			}
			return axisPlace{sv, ev - sv, true}
		case sOK && e.Span > 0:
			return axisPlace{sv, esp, true}
		case sOK:
			return axisPlace{sv, 1, true}
		case eOK && s.Span > 0:
			st := ev - ssp
			if st < 0 {
				st = 0
			}
			return axisPlace{st, ssp, true}
		case eOK:
			st := ev - 1
			if st < 0 {
				st = 0
			}
			return axisPlace{st, 1, true}
		}
		sp := 1
		if s.Span > 0 {
			sp = ssp
		} else if e.Span > 0 {
			sp = esp
		}
		return axisPlace{0, sp, false}
	}
	colMajor := strings.HasPrefix(st.GridAutoFlow, "column")
	dense := strings.Contains(st.GridAutoFlow, "dense")
	for _, it := range items {
		k := it.b
		rp := place(k.st.GridRowStart, k.st.GridRowEnd, rowNames, rowArea, nrows)
		cp := place(k.st.GridColStart, k.st.GridColEnd, colNames, colArea, ncols)
		it.r0, it.rs, it.rDef = rp.start, rp.span, rp.def
		it.c0, it.cs, it.cDef = cp.start, cp.span, cp.def
		// a single named area on grid-area
		if k.st.GridRowStart.Name != "" && k.st.GridRowStart == k.st.GridRowEnd && k.st.GridColStart == k.st.GridRowStart {
			if a, ok := areaPos[k.st.GridRowStart.Name]; ok {
				it.r0, it.rs, it.c0, it.cs = a[0], a[1]-a[0], a[2], a[3]-a[2]
				it.rDef, it.cDef = true, true
			}
		}
	}
	// occupancy
	occ := map[[2]int]bool{}
	fits := func(r, c, rs, cs int) bool {
		for i := r; i < r+rs; i++ {
			for j := c; j < c+cs; j++ {
				if occ[[2]int{i, j}] {
					return false
				}
			}
		}
		return true
	}
	mark := func(it *gitem) {
		for i := it.r0; i < it.r0+it.rs; i++ {
			for j := it.c0; j < it.c0+it.cs; j++ {
				occ[[2]int{i, j}] = true
			}
		}
	}
	// 1. definite both
	var rest []*gitem
	for _, it := range items {
		if it.rDef && it.cDef {
			mark(it)
		} else {
			rest = append(rest, it)
		}
	}
	// 2/4. the rest
	curR, curC := 0, 0
	maxCols := ncols
	if maxCols < 1 {
		maxCols = 1
	}
	maxRows := nrows
	if maxRows < 1 {
		maxRows = 1
	}
	// helpers using a "major" axis: with row flow items fill columns of a row
	for _, it := range rest {
		if !colMajor {
			switch {
			case it.rDef && !it.cDef:
				// definite row, auto column
				cc := 0
				if !dense {
					cc = 0
				}
				for !fits(it.r0, cc, it.rs, it.cs) || (cc+it.cs > maxCols && cc > 0 && false) {
					cc++
				}
				it.c0 = cc
				mark(it)
			case !it.rDef && it.cDef:
				// definite column, auto row: scan down
				rr := 0
				if !dense {
					if it.c0 < curC {
						curR++
					}
					rr = curR
				}
				for !fits(rr, it.c0, it.rs, it.cs) {
					rr++
				}
				it.r0 = rr
				curR, curC = rr, it.c0
				mark(it)
			default:
				if dense {
					curR, curC = 0, 0
				}
				for {
					if curC+it.cs > maxCols {
						curR++
						curC = 0
						continue
					}
					if fits(curR, curC, it.rs, it.cs) {
						break
					}
					curC++
				}
				it.r0, it.c0 = curR, curC
				mark(it)
				curC += it.cs
			}
		} else {
			switch {
			case it.cDef && !it.rDef:
				rr := 0
				for !fits(rr, it.c0, it.rs, it.cs) {
					rr++
				}
				it.r0 = rr
				mark(it)
			case !it.cDef && it.rDef:
				cc := 0
				if !dense {
					if it.r0 < curR {
						curC++
					}
					cc = curC
				}
				for !fits(it.r0, cc, it.rs, it.cs) {
					cc++
				}
				it.c0 = cc
				curC, curR = cc, it.r0
				mark(it)
			default:
				if dense {
					curR, curC = 0, 0
				}
				for {
					if curR+it.rs > maxRows {
						curC++
						curR = 0
						continue
					}
					if fits(curR, curC, it.rs, it.cs) {
						break
					}
					curR++
				}
				it.r0, it.c0 = curR, curC
				mark(it)
				curR += it.rs
			}
		}
	}
	// total grid size
	for _, it := range items {
		if it.r0+it.rs > nrows {
			nrows = it.r0 + it.rs
		}
		if it.c0+it.cs > ncols {
			ncols = it.c0 + it.cs
		}
	}
	if len(items) == 0 {
		// still honor explicit tracks
	}
	// tracks
	mkTracks := func(defs []style.Track, n int, auto []style.Track) []*gtrack {
		out := make([]*gtrack, n)
		for i := 0; i < n; i++ {
			var d style.Track
			if i < len(defs) {
				d = defs[i]
			} else if len(auto) > 0 {
				d = auto[(i-len(defs))%len(auto)]
			} else {
				d = style.Track{Min: style.Sz{K: style.SzAuto}, Max: style.Sz{K: style.SzAuto}}
			}
			out[i] = &gtrack{minF: d.Min, maxF: d.Max}
		}
		return out
	}
	// auto-fit: empty repeated tracks collapse (removed together with their gutters)
	if st.GridCols.RepMode == "fit" {
		used := map[int]bool{}
		for _, it := range items {
			for j := it.c0; j < it.c0+it.cs; j++ {
				used[j] = true
			}
		}
		start := len(st.GridCols.Pre)
		end := len(colDefs) - len(st.GridCols.Post)
		remap := make([]int, len(colDefs))
		var nd []style.Track
		for i, d := range colDefs {
			if i >= start && i < end && !used[i] {
				remap[i] = -1
				continue
			}
			remap[i] = len(nd)
			nd = append(nd, d)
		}
		if len(nd) != len(colDefs) {
			for _, it := range items {
				if it.c0 < len(remap) && remap[it.c0] >= 0 {
					it.c0 = remap[it.c0]
				}
			}
			ncols -= len(colDefs) - len(nd)
			colDefs = nd
		}
	}
	gm.cols = mkTracks(colDefs, ncols, st.GridAutoCols)
	gm.rows = mkTracks(rowDefs, nrows, st.GridAutoRows)
	gm.items = items
	// column sizing
	colAvail := cw
	for _, it := range items {
		c.edges(it.b, cw)
	}
	for _, it := range items {
		it := it
		_ = it
	}
	colSpanItems := items
	c.sizeAxis(gm.cols, colSpanItems, cgap, colAvail, true, st, func(it *gitem) (int, int) { return it.c0, it.cs },
		func(it *gitem, wantMin bool) float64 { return c.outerIntrinsic(it.b, wantMin) }, cw, cbh, gm)
	// justify-content for columns
	c.alignTracks(gm.cols, cgap, cw, stripSafe(st.JustifyContent), true)
	// row sizing needs item heights at their column widths
	for _, it := range items {
		w := 0.0
		for j := it.c0; j < it.c0+it.cs; j++ {
			w += gm.cols[j].size
		}
		w += cgap * float64(it.cs-1)
		it.areaW = w
	}
	rowAvail := cbh
	if in.fixH >= 0 {
		rowAvail = math.Max(0, in.fixH-b.vExtra()-b.sbH)
	}
	c.sizeAxis(gm.rows, items, rgap, rowAvail, false, st, func(it *gitem) (int, int) { return it.r0, it.rs },
		func(it *gitem, wantMin bool) float64 { return c.gridItemHeight(it, cw, cbh) }, cw, cbh, gm)
	// content height
	total := 0.0
	for i, t := range gm.rows {
		total += t.size
		if i > 0 {
			total += rgap
		}
	}
	if rowAvail < 0 {
		// min-height / max-height make the row space definite for fr tracks
		want := total
		if mn, ok := c.specH(b, st.MinHeight, in.cbh); ok && st.MinHeight.K != style.LAuto && mn > want {
			want = mn
		}
		if mx, ok := c.specH(b, st.MaxHeight, in.cbh); ok && st.MaxHeight.K != style.LNone && st.MaxHeight.K != style.LAuto && want > mx {
			want = mx
		}
		if want != total {
			rowAvail = want
			c.sizeAxis(gm.rows, items, rgap, rowAvail, false, st, func(it *gitem) (int, int) { return it.r0, it.rs },
				func(it *gitem, wantMin bool) float64 { return c.gridItemHeight(it, cw, cbh) }, cw, cbh, gm)
			total = 0
			for i, t := range gm.rows {
				total += t.size
				if i > 0 {
					total += rgap
				}
			}
		}
	}
	innerH := total
	if rowAvail >= 0 {
		innerH = rowAvail
	}
	c.alignTracks(gm.rows, rgap, innerH, stripSafe(st.AlignContent), false)
	// positions
	x := 0.0
	for i, t := range gm.cols {
		t.pos = x + t.shift
		x += t.size + cgap
		_ = i
	}
	y := 0.0
	for _, t := range gm.rows {
		t.pos = y + t.shift
		y += t.size + rgap
	}
	// place items
	for _, it := range items {
		ax, ay := gm.cols[it.c0].pos, gm.rows[it.r0].pos
		aw, ah := 0.0, 0.0
		for j := it.c0; j < it.c0+it.cs; j++ {
			aw += gm.cols[j].size
		}
		aw += cgap * float64(it.cs-1)
		for j := it.r0; j < it.r0+it.rs; j++ {
			ah += gm.rows[j].size
		}
		ah += rgap * float64(it.rs-1)
		it.areaW, it.areaH = aw, ah
		c.placeGridItem(b, it, ax, ay, aw, ah)
	}
	// static positions of positioned children
	for _, k := range b.kids {
		if k.oof {
			k.lp = b
			k.staticX = b.bor[3] + b.pad[3]
			k.staticY = b.bor[0] + b.pad[0]
			c.addOOF(k)
		}
	}
	b.hasBase = false
	if len(items) > 0 {
		b.hasBase = true
		if items[0].b.hasBase {
			b.baseline = items[0].b.y + items[0].b.baseline
		} else {
			b.baseline = items[0].b.y + items[0].b.h
		}
		b.lastBase = b.baseline
	}
	return innerH
}

func (c *ctx) gridItemHeight(it *gitem, cw, cbh float64) float64 {
	k := it.b
	w := it.areaW
	margin := k.mar[1] + k.mar[3]
	st := k.st
	var bw float64
	if st.Width.K == style.LAuto && k.rep == nil {
		bw = w - margin
	} else if wv, ok := c.specW(k, st.Width, w); ok {
		bw = wv + k.hExtra()
	} else if k.rep != nil {
		rw, _ := c.replacedUsedSize(k, w, -1, false)
		bw = rw + k.hExtra()
	} else {
		bw = w - margin
	}
	if k.inner == iReplaced {
		_, rh := c.replacedUsedSize(k, w-margin, -1, false)
		return rh + k.vExtra() + k.mar[0] + k.mar[2]
	}
	in := layoutIn{item: true, cbw: w, cbh: -1, fixW: math.Max(0, bw), fixH: -1, bfc: &bfcState{}}
	c.layoutBox(k, in)
	return k.h + k.mar[0] + k.mar[2]
}

func (c *ctx) placeGridItem(b *lbox, it *gitem, ax, ay, aw, ah float64) {
	k := it.b
	st := k.st
	c.edges(k, aw)
	just := stripSafe(st.JustifySelf)
	if just == "auto" || just == "" {
		just = stripSafe(b.st.JustifyItems)
	}
	if just == "legacy" || just == "normal" || just == "" {
		just = "stretch"
	}
	al := stripSafe(st.AlignSelf)
	if al == "auto" || al == "" {
		al = stripSafe(b.st.AlignItems)
	}
	if al == "normal" || al == "" {
		al = "stretch"
	}
	replaced := k.inner == iReplaced
	// width
	var bw float64
	wAuto := st.Width.K == style.LAuto
	freeW := aw - k.mar[1] - k.mar[3]
	switch {
	case !wAuto:
		if wv, ok := c.specW(k, st.Width, aw); ok {
			bw = c.clampW(k, wv, aw) + k.hExtra()
		} else {
			bw = freeW
		}
	case replaced:
		rw, _ := c.replacedUsedSize(k, aw, ah, false)
		bw = rw + k.hExtra()
	case just == "stretch" && !k.marAuto[1] && !k.marAuto[3]:
		bw = c.clampW(k, freeW-k.hExtra(), aw) + k.hExtra()
	default:
		mn, mx := c.minContent(k), c.maxContent(k)
		bw = math.Min(math.Max(mn, freeW-k.hExtra()), mx) + k.hExtra()
		bw = c.clampW(k, bw-k.hExtra(), aw) + k.hExtra()
	}
	// height
	hAuto := st.Height.K == style.LAuto
	freeH := ah - k.mar[0] - k.mar[2]
	fixH := -1.0
	switch {
	case !hAuto:
		if hv, ok := c.specH(k, st.Height, ah); ok {
			fixH = c.clampH(k, hv, ah, 0) + k.vExtra()
		}
	case replaced:
	case al == "stretch" && !k.marAuto[0] && !k.marAuto[2]:
		fixH = c.clampH(k, freeH-k.vExtra(), ah, 0) + k.vExtra()
	}
	k.hasLast = false
	in := layoutIn{item: true, cbw: aw, cbh: ah, fixW: bw, fixH: fixH, bfc: &bfcState{}}
	if replaced {
		in.fixW = -1
		in.fixH = -1
		if !wAuto || true {
			in.fixW = bw
		}
		if fixH >= 0 {
			in.fixH = fixH
		}
	}
	c.layoutBox(k, in)
	// horizontal position
	fx := aw - k.w - k.mar[1] - k.mar[3]
	var dx float64
	switch {
	case k.marAuto[3] && k.marAuto[1]:
		dx = k.mar[3] + math.Max(0, fx)/2
	case k.marAuto[3]:
		dx = k.mar[3] + math.Max(0, fx)
	case k.marAuto[1]:
		dx = k.mar[3]
	default:
		switch just {
		case "end", "flex-end", "right", "self-end":
			dx = k.mar[3] + fx
		case "center":
			dx = k.mar[3] + fx/2
		default:
			dx = k.mar[3]
		}
	}
	fy := ah - k.h - k.mar[0] - k.mar[2]
	var dy float64
	switch {
	case k.marAuto[0] && k.marAuto[2]:
		dy = k.mar[0] + math.Max(0, fy)/2
	case k.marAuto[0]:
		dy = k.mar[0] + math.Max(0, fy)
	case k.marAuto[2]:
		dy = k.mar[0]
	default:
		switch al {
		case "end", "flex-end", "self-end":
			dy = k.mar[0] + fy
		case "center":
			dy = k.mar[0] + fy/2
		case "baseline", "first baseline":
			dy = k.mar[0]
		default:
			dy = k.mar[0]
		}
	}
	k.x, k.y = ax+dx, ay+dy
	k.lp = b
	if k.hasFloat || k.touchesBFC {
		b.touchesBFC = true
	}
}

// alignTracks applies justify-content / align-content when there is free space.
func (c *ctx) alignTracks(tracks []*gtrack, gap, avail float64, mode string, isCol bool) {
	if avail < 0 || len(tracks) == 0 {
		return
	}
	used := gap * float64(len(tracks)-1)
	for _, t := range tracks {
		used += t.size
	}
	free := avail - used
	if free <= 0 {
		return
	}
	off, between := 0.0, 0.0
	switch mode {
	case "end", "flex-end", "right":
		off = free
	case "center":
		off = free / 2
	case "space-between":
		if len(tracks) > 1 {
			between = free / float64(len(tracks)-1)
		}
	case "space-around":
		between = free / float64(len(tracks))
		off = between / 2
	case "space-evenly":
		between = free / float64(len(tracks)+1)
		off = between
	case "stretch":
		// handled by auto track stretching
		return
	default:
		return
	}
	for i, t := range tracks {
		t.shift = off + between*float64(i)
	}
}

// sizeAxis implements a simplified grid track sizing algorithm.
func (c *ctx) sizeAxis(tracks []*gtrack, items []*gitem, gap, avail float64, isCol bool, st *style.Computed,
	span func(*gitem) (int, int), contrib func(*gitem, bool) float64, cw, cbh float64, gm *gridModel) {
	n := len(tracks)
	if n == 0 {
		return
	}
	resolve := func(s style.Sz) (float64, bool) {
		if s.K != style.SzLen {
			return 0, false
		}
		if s.L.K == style.LPct || s.L.K == style.LCalc {
			if avail < 0 {
				return 0, false
			}
			return s.L.Resolve(avail), true
		}
		return s.L.V, true
	}
	for _, t := range tracks {
		t.base, t.limit = 0, math.Inf(1)
		t.isFlex, t.flex = false, 0
		if v, ok := resolve(t.minF); ok {
			t.base = v
		}
		switch t.maxF.K {
		case style.SzLen:
			if v, ok := resolve(t.maxF); ok {
				t.limit = v
			}
		case style.SzFr:
			t.isFlex, t.flex = true, t.maxF.Fr
		case style.SzFitContent:
			if v, ok := resolve(style.Sz{K: style.SzLen, L: t.maxF.L}); ok {
				t.fitMax = v
				t.hasFit = true
			}
		}
		if t.limit < t.base {
			t.limit = t.base
		}
		t.fitMax = 0
	}
	intrinsicMin := func(t *gtrack) bool {
		return t.minF.K == style.SzAuto || t.minF.K == style.SzMinContent || t.minF.K == style.SzMaxContent
	}
	intrinsicMax := func(t *gtrack) bool {
		return t.maxF.K == style.SzAuto || t.maxF.K == style.SzMinContent || t.maxF.K == style.SzMaxContent || t.maxF.K == style.SzFitContent
	}
	// items sorted by span
	sorted := append([]*gitem(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		_, si := span(sorted[i])
		_, sj := span(sorted[j])
		return si < sj
	})
	spansFlex := func(s, e int) bool {
		for i := s; i < s+e; i++ {
			if tracks[i].isFlex {
				return true
			}
		}
		return false
	}
	for pass := 0; pass < 2; pass++ {
		for _, it := range sorted {
			s, e := span(it)
			if s+e > n {
				continue
			}
			flex := spansFlex(s, e)
			if pass == 0 && flex && e > 1 {
				continue
			}
			inner := gap * float64(e-1)
			// minimum contribution
			var minC, maxC float64
			k := it.b
			if isCol {
				minC = contrib(it, true)
				maxC = contrib(it, false)
				if k.st.MinWidth.K != style.LAuto && k.st.MinWidth.K != style.LNone {
					v, _ := c.specW(k, k.st.MinWidth, cw)
					minC = k.mar[1] + k.mar[3] + k.hExtra() + v
				} else if k.overflowX() != "visible" && k.overflowX() != "clip" {
					minC = k.mar[1] + k.mar[3] + k.hExtra()
				}
			} else {
				minC = contrib(it, true)
				maxC = minC
				if k.st.MinHeight.K != style.LAuto && k.st.MinHeight.K != style.LNone && k.st.MinHeight.K != style.LPct {
					v, _ := c.specH(k, k.st.MinHeight, cbh)
					if need := k.mar[0] + k.mar[2] + k.vExtra() + v; need < minC {
						minC = need
					}
				}
			}
			if pass == 0 {
				// base sizes
				var elig []*gtrack
				cur := inner
				for i := s; i < s+e; i++ {
					cur += tracks[i].base
					if intrinsicMin(tracks[i]) {
						elig = append(elig, tracks[i])
					}
				}
				want := minC
				if e == 1 && tracks[s].minF.K == style.SzMaxContent {
					want = maxC
				}
				if extra := want - cur; extra > 0 && len(elig) > 0 {
					// tracks with auto min and a fixed max only grow up to the limit... simplified
					for _, t := range elig {
						t.base += extra / float64(len(elig))
					}
				}
				// growth limits
				if !flex {
					var eligL []*gtrack
					curL := inner
					for i := s; i < s+e; i++ {
						l := tracks[i].limit
						if math.IsInf(l, 1) {
							l = tracks[i].base
						}
						curL += l
						if intrinsicMax(tracks[i]) {
							eligL = append(eligL, tracks[i])
						}
					}
					wantL := maxC
					if e == 1 && tracks[s].maxF.K == style.SzMinContent {
						wantL = minC
					}
					if extra := wantL - curL; extra > 0 && len(eligL) > 0 {
						for _, t := range eligL {
							if math.IsInf(t.limit, 1) {
								t.limit = t.base + extra/float64(len(eligL))
							} else {
								t.limit += extra / float64(len(eligL))
							}
						}
					}
				}
			}
		}
		if pass == 0 {
			for _, t := range tracks {
				if math.IsInf(t.limit, 1) && !t.isFlex {
					t.limit = t.base
				}
				if t.limit < t.base {
					t.limit = t.base
				}
			}
		}
	}
	// maximize tracks
	used := gap * float64(n-1)
	for _, t := range tracks {
		used += t.base
	}
	if avail >= 0 {
		free := avail - used
		for free > 0.001 {
			var grow []*gtrack
			for _, t := range tracks {
				if !t.isFlex && t.base < t.limit-0.001 {
					grow = append(grow, t)
				}
			}
			if len(grow) == 0 {
				break
			}
			share := free / float64(len(grow))
			minRoom := math.Inf(1)
			for _, t := range grow {
				minRoom = math.Min(minRoom, t.limit-t.base)
			}
			inc := math.Min(share, minRoom)
			for _, t := range grow {
				t.base += inc
			}
			free -= inc * float64(len(grow))
		}
	} else {
		// indefinite: tracks grow to their limits
		for _, t := range tracks {
			if !t.isFlex && !math.IsInf(t.limit, 1) && t.base < t.limit {
				t.base = t.limit
			}
		}
	}
	// flexible tracks
	var flexTracks []*gtrack
	sumFactor := 0.0
	for _, t := range tracks {
		if t.isFlex {
			flexTracks = append(flexTracks, t)
			sumFactor += t.flex
		}
	}
	if len(flexTracks) > 0 {
		var frSize float64
		if avail >= 0 {
			left := avail - gap*float64(n-1)
			for _, t := range tracks {
				if !t.isFlex {
					left -= t.base
				}
			}
			frozen := map[*gtrack]bool{}
			for iter := 0; iter < len(flexTracks)+1; iter++ {
				sum := 0.0
				rem := left
				for _, t := range flexTracks {
					if frozen[t] {
						rem -= t.base
					} else {
						sum += t.flex
					}
				}
				if sum < 1 {
					sum = 1
				}
				frSize = rem / sum
				changed := false
				for _, t := range flexTracks {
					if !frozen[t] && t.flex*frSize < t.base {
						frozen[t] = true
						changed = true
					}
				}
				if !changed {
					break
				}
			}
			if frSize < 0 {
				frSize = 0
			}
		} else {
			// content-based
			for _, t := range flexTracks {
				if t.flex > 0 {
					frSize = math.Max(frSize, t.base/math.Max(t.flex, 1e-9))
				}
			}
			for _, it := range items {
				s, e := span(it)
				if s+e > n || !spansFlex(s, e) {
					continue
				}
				sf := 0.0
				fixed := gap * float64(e-1)
				for i := s; i < s+e; i++ {
					if tracks[i].isFlex {
						sf += tracks[i].flex
					} else {
						fixed += tracks[i].base
					}
				}
				if sf <= 0 {
					continue
				}
				need := contrib(it, false)
				if !isCol {
					need = contrib(it, true)
				}
				frSize = math.Max(frSize, (need-fixed)/math.Max(sf, 1))
			}
		}
		for _, t := range flexTracks {
			t.size = math.Max(t.base, t.flex*frSize)
			t.base = t.size
		}
	}
	for _, t := range tracks {
		if !t.isFlex {
			t.size = t.base
		}
	}
	// stretch auto tracks
	if avail >= 0 {
		mode := stripSafe(st.JustifyContent)
		if !isCol {
			mode = stripSafe(st.AlignContent)
		}
		if mode == "normal" || mode == "stretch" || mode == "" {
			free := avail - gap*float64(n-1)
			for _, t := range tracks {
				free -= t.size
			}
			if free > 0.001 {
				var autos []*gtrack
				for _, t := range tracks {
					if t.maxF.K == style.SzAuto {
						autos = append(autos, t)
					}
				}
				for _, t := range autos {
					t.size += free / float64(len(autos))
				}
			}
		}
	}
	for _, t := range tracks {
		if t.hasFit && t.maxF.K == style.SzFitContent {
			// fit-content(x) clamps the max-content size to x
		}
	}
}

func (c *ctx) gridIntrinsic(b *lbox) (float64, float64) {
	// approximate: lay out tracks with indefinite space
	st := b.st
	cgap := 0.0
	if st.ColumnGap.K == style.LPx {
		cgap = st.ColumnGap.V
	}
	colDefs, _, _ := c.expandTemplate(st.GridCols, -1, cgap, st.GridAutoCols)
	if st.GridCols.None {
		colDefs = nil
	}
	var items []*lbox
	for _, k := range b.kids {
		if k.oof || k.kind == kText || k.kind == kBreak || k.kind == kMarker || k.kind == kInline {
			continue
		}
		items = append(items, k)
	}
	if len(items) == 0 {
		return 0, 0
	}
	ncols := len(colDefs)
	if ncols == 0 {
		ncols = 1
		if strings.HasPrefix(st.GridAutoFlow, "column") {
			ncols = len(items)
		}
	}
	mins := make([]float64, ncols)
	maxs := make([]float64, ncols)
	for i, k := range items {
		col := i % ncols
		if st.GridCols.None || len(colDefs) == 0 {
			col = 0
			if strings.HasPrefix(st.GridAutoFlow, "column") {
				col = i
			}
		}
		if col >= ncols {
			col = ncols - 1
		}
		mins[col] = math.Max(mins[col], c.outerIntrinsic(k, true))
		maxs[col] = math.Max(maxs[col], c.outerIntrinsic(k, false))
	}
	var mn, mx float64
	for i := 0; i < ncols; i++ {
		var d style.Track
		if i < len(colDefs) {
			d = colDefs[i]
		}
		cm, cx := mins[i], maxs[i]
		if i < len(colDefs) && d.Max.K == style.SzLen && d.Max.L.K == style.LPx && d.Min.K == style.SzLen {
			cm, cx = d.Max.L.V, d.Max.L.V
		} else if i < len(colDefs) && d.Max.K == style.SzLen && d.Max.L.K == style.LPx {
			cx = d.Max.L.V
			if d.Min.K == style.SzLen {
				cm = d.Min.L.V
			}
		}
		mn += cm
		mx += cx
	}
	gaps := cgap * float64(ncols-1)
	return mn + gaps, mx + gaps
}
