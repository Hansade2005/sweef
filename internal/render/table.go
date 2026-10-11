package render

import (
	"math"
	"os"
	"strconv"

	"github.com/sweefbrowser/sweef/internal/style"
)

// ---- box tree fixup ---------------------------------------------------------------------

func isTableInternal(inner int) bool {
	switch inner {
	case iRow, iRowGroup, iCell, iCaption, iColGroup, iCol:
		return true
	}
	return false
}

func (c *ctx) anonBox(parent *lbox, display string, inner int) *lbox {
	an := newBox(nil, c.eng.Anonymous(parent.st, display), kBlock)
	an.anon = true
	an.inner = inner
	return an
}

func (c *ctx) wrapCells(parent *lbox, run []*lbox) *lbox {
	cell := c.anonBox(parent, "table-cell", iCell)
	for _, r := range run {
		cell.addKid(r)
	}
	c.fixupChildren(cell)
	return cell
}

func isBlankText(k *lbox) bool {
	return k.kind == kText && onlyWhitespace(k.text) && collapsibleWS(k.st)
}

// fixupTableInternal makes row groups and rows well formed.
func (c *ctx) fixupTableInternal(b *lbox) {
	var nk []*lbox
	var run []*lbox
	flush := func() {
		if len(run) == 0 {
			return
		}
		real := false
		for _, r := range run {
			if !isBlankText(r) {
				real = true
			}
		}
		if real {
			if b.inner == iRow {
				nk = append(nk, c.wrapCells(b, run))
			} else {
				row := c.anonBox(b, "table-row", iRow)
				row.addKid(c.wrapCells(row, run))
				nk = append(nk, row)
			}
		}
		run = nil
	}
	for _, k := range b.kids {
		ok := false
		switch b.inner {
		case iRow:
			ok = k.inner == iCell
		case iRowGroup:
			ok = k.inner == iRow
		case iColGroup:
			ok = k.inner == iCol
		}
		if b.inner == iColGroup {
			nk = append(nk, k)
			continue
		}
		if ok || k.oof {
			flush()
			nk = append(nk, k)
		} else if k.floated && false {
			nk = append(nk, k)
		} else {
			run = append(run, k)
		}
	}
	flush()
	b.kids = nil
	for _, k := range nk {
		b.addKid(k)
	}
}

func (c *ctx) fixupTable(b *lbox) {
	var nk []*lbox
	var run []*lbox
	flush := func() {
		if len(run) == 0 {
			return
		}
		real := false
		for _, r := range run {
			if !isBlankText(r) {
				real = true
			}
		}
		if real {
			row := c.anonBox(b, "table-row", iRow)
			// consecutive cells in one row, other content in anonymous cells
			var cells []*lbox
			var other []*lbox
			fl := func() {
				if len(other) > 0 {
					cells = append(cells, c.wrapCells(row, other))
					other = nil
				}
			}
			for _, r := range run {
				if r.inner == iCell {
					fl()
					cells = append(cells, r)
				} else {
					other = append(other, r)
				}
			}
			fl()
			for _, ce := range cells {
				row.addKid(ce)
			}
			nk = append(nk, row)
		}
		run = nil
	}
	for _, k := range b.kids {
		switch k.inner {
		case iRow, iRowGroup, iCaption, iColGroup, iCol:
			flush()
			nk = append(nk, k)
		default:
			if k.oof {
				flush()
				nk = append(nk, k)
			} else {
				run = append(run, k)
			}
		}
	}
	flush()
	b.kids = nil
	for _, k := range nk {
		b.addKid(k)
	}
}

// ---- table model --------------------------------------------------------------------------

type tcell struct {
	b          *lbox
	row, col   int
	rs, cs     int
	minW, maxW float64
	fixedW     float64
	hasFixed   bool
	pct        float64
	hasPct     bool
	h          float64
	base       float64
	hasBase    bool
	valign     string
}

type tcolumn struct {
	min, max, fixed, pct float64
	hasFixed, hasPct     bool
	w                    float64
	box                  *lbox
}

type trow struct {
	b     *lbox
	group *lbox
	cells []*tcell
	h     float64
	y     float64
	base  float64
}

type tableModel struct {
	rows     []*trow
	groups   []*lbox
	cols     []*tcolumn
	caps     []*lbox
	ncols    int
	cells    []*tcell
	grid     [][]*tcell
	colBoxes []*lbox
}

func intAttr(b *lbox, k string, d int) int {
	if b.n == nil {
		return d
	}
	if v := attr(b.n, k); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return d
}

func (c *ctx) tableModelOf(b *lbox) *tableModel {
	if m, ok := b.extra.(*tableModel); ok {
		return m
	}
	m := &tableModel{}
	var headRows, bodyRows, footRows []*trow
	var addRows func(g *lbox, list *[]*trow)
	addRows = func(g *lbox, list *[]*trow) {
		for _, r := range g.kids {
			if r.inner == iRow {
				*list = append(*list, &trow{b: r, group: g})
			}
		}
	}
	for _, k := range b.kids {
		switch k.inner {
		case iCaption:
			m.caps = append(m.caps, k)
		case iColGroup:
			m.groups = append(m.groups, k)
		case iCol:
			m.colBoxes = append(m.colBoxes, k)
		case iRowGroup:
			switch k.st.Display {
			case "table-header-group":
				addRows(k, &headRows)
			case "table-footer-group":
				addRows(k, &footRows)
			default:
				addRows(k, &bodyRows)
			}
		case iRow:
			bodyRows = append(bodyRows, &trow{b: k, group: b})
		}
	}
	m.rows = append(append(headRows, bodyRows...), footRows...)
	// slots
	occupied := map[[2]int]*tcell{}
	for ri, r := range m.rows {
		col := 0
		for _, ce := range r.b.kids {
			if ce.inner != iCell {
				continue
			}
			for occupied[[2]int{ri, col}] != nil {
				col++
			}
			tc := &tcell{b: ce, row: ri, col: col, rs: intAttr(ce, "rowspan", 1), cs: intAttr(ce, "colspan", 1)}
			if tc.cs > 1000 {
				tc.cs = 1000
			}
			if tc.rs > 65534 {
				tc.rs = 65534
			}
			if tc.rs == 0 {
				tc.rs = 1
			}
			for rr := ri; rr < ri+tc.rs && rr < len(m.rows); rr++ {
				for cc := col; cc < col+tc.cs; cc++ {
					occupied[[2]int{rr, cc}] = tc
				}
			}
			if tc.row+tc.rs > len(m.rows) {
				tc.rs = len(m.rows) - tc.row
			}
			r.cells = append(r.cells, tc)
			m.cells = append(m.cells, tc)
			if col+tc.cs > m.ncols {
				m.ncols = col + tc.cs
			}
			col += tc.cs
		}
	}
	// col elements
	ci := 0
	var colList []*lbox
	for _, g := range m.groups {
		if len(g.kids) == 0 {
			span := intAttr(g, "span", 1)
			for i := 0; i < span; i++ {
				colList = append(colList, g)
			}
		}
		for _, cl := range g.kids {
			span := intAttr(cl, "span", 1)
			for i := 0; i < span; i++ {
				colList = append(colList, cl)
			}
		}
	}
	for _, cl := range m.colBoxes {
		span := intAttr(cl, "span", 1)
		for i := 0; i < span; i++ {
			colList = append(colList, cl)
		}
	}
	_ = ci
	if len(colList) > m.ncols {
		m.ncols = len(colList)
	}
	m.cols = make([]*tcolumn, m.ncols)
	for i := range m.cols {
		m.cols[i] = &tcolumn{}
		if i < len(colList) {
			m.cols[i].box = colList[i]
		}
	}
	m.grid = make([][]*tcell, len(m.rows))
	for i := range m.grid {
		m.grid[i] = make([]*tcell, m.ncols)
	}
	for k, v := range occupied {
		if k[0] < len(m.grid) && k[1] < m.ncols {
			m.grid[k[0]][k[1]] = v
		}
	}
	b.extra = m
	return m
}

// ---- collapsed borders ------------------------------------------------------------------------

type bedge struct {
	w     float64
	style string
	color Color
	prio  int
}

var borderStyleRank = map[string]int{"double": 8, "solid": 7, "dashed": 6, "dotted": 5, "ridge": 4, "outset": 3, "groove": 2, "inset": 1}

func betterEdge(a, b bedge) bedge {
	if b.style == "hidden" {
		return b
	}
	if a.style == "hidden" {
		return a
	}
	if b.style == "none" || b.style == "" {
		return a
	}
	if a.style == "none" || a.style == "" {
		return b
	}
	if b.w != a.w {
		if b.w > a.w {
			return b
		}
		return a
	}
	if borderStyleRank[b.style] != borderStyleRank[a.style] {
		if borderStyleRank[b.style] > borderStyleRank[a.style] {
			return b
		}
		return a
	}
	if b.prio > a.prio {
		return b
	}
	return a
}

func sideEdge(st *style.Computed, side, prio int) bedge {
	if st.BorderStyle[side] == "none" || st.BorderStyle[side] == "" {
		return bedge{}
	}
	return bedge{w: st.BorderWidth[side], style: st.BorderStyle[side], color: st.BorderColor[side], prio: prio}
}

type collapsed struct {
	h [][]bedge // [R+1][C]
	v [][]bedge // [R][C+1]
}

func (c *ctx) collapseBorders(b *lbox, m *tableModel) *collapsed {
	R, C := len(m.rows), m.ncols
	cl := &collapsed{h: make([][]bedge, R+1), v: make([][]bedge, R)}
	for i := range cl.h {
		cl.h[i] = make([]bedge, C)
	}
	for i := range cl.v {
		cl.v[i] = make([]bedge, C+1)
	}
	st := b.st
	// table borders on the outer edges
	for ci := 0; ci < C; ci++ {
		cl.h[0][ci] = betterEdge(cl.h[0][ci], sideEdge(st, 0, 1))
		cl.h[R][ci] = betterEdge(cl.h[R][ci], sideEdge(st, 2, 1))
	}
	for ri := 0; ri < R; ri++ {
		cl.v[ri][0] = betterEdge(cl.v[ri][0], sideEdge(st, 3, 1))
		cl.v[ri][C] = betterEdge(cl.v[ri][C], sideEdge(st, 1, 1))
	}
	// column and column-group borders
	for ci, col := range m.cols {
		if col.box == nil {
			continue
		}
		for ri := 0; ri < R; ri++ {
			if g := col.box.parent; g != nil && g.inner == iColGroup {
				cl.v[ri][ci] = betterEdge(cl.v[ri][ci], sideEdge(g.st, 3, 2))
				cl.v[ri][ci+1] = betterEdge(cl.v[ri][ci+1], sideEdge(g.st, 1, 2))
			}
			cl.v[ri][ci] = betterEdge(cl.v[ri][ci], sideEdge(col.box.st, 3, 3))
			cl.v[ri][ci+1] = betterEdge(cl.v[ri][ci+1], sideEdge(col.box.st, 1, 3))
		}
	}
	// row groups and rows
	for ri, r := range m.rows {
		for ci := 0; ci < C; ci++ {
			if r.group != nil && r.group != b {
				// group borders only apply on the group's outer edges; approximate per row edge
				g := r.group
				first := g.kids[0] == r.b
				last := g.kids[len(g.kids)-1] == r.b
				if first {
					cl.h[ri][ci] = betterEdge(cl.h[ri][ci], sideEdge(g.st, 0, 4))
				}
				if last {
					cl.h[ri+1][ci] = betterEdge(cl.h[ri+1][ci], sideEdge(g.st, 2, 4))
				}
			}
			cl.h[ri][ci] = betterEdge(cl.h[ri][ci], sideEdge(r.b.st, 0, 5))
			cl.h[ri+1][ci] = betterEdge(cl.h[ri+1][ci], sideEdge(r.b.st, 2, 5))
		}
		cl.v[ri][0] = betterEdge(cl.v[ri][0], sideEdge(r.b.st, 3, 5))
		cl.v[ri][C] = betterEdge(cl.v[ri][C], sideEdge(r.b.st, 1, 5))
	}
	for _, tc := range m.cells {
		cs := tc.b.st
		for ci := tc.col; ci < tc.col+tc.cs; ci++ {
			cl.h[tc.row][ci] = betterEdge(cl.h[tc.row][ci], sideEdge(cs, 0, 6))
			cl.h[tc.row+tc.rs][ci] = betterEdge(cl.h[tc.row+tc.rs][ci], sideEdge(cs, 2, 6))
		}
		for ri := tc.row; ri < tc.row+tc.rs; ri++ {
			cl.v[ri][tc.col] = betterEdge(cl.v[ri][tc.col], sideEdge(cs, 3, 6))
			cl.v[ri][tc.col+tc.cs] = betterEdge(cl.v[ri][tc.col+tc.cs], sideEdge(cs, 1, 6))
		}
	}
	return cl
}

func edgeW(e bedge) float64 {
	if e.style == "hidden" || e.style == "none" || e.style == "" {
		return 0
	}
	return e.w
}

// ---- layout ---------------------------------------------------------------------------------------

func (c *ctx) layoutTable(b *lbox, in layoutIn) {
	st := b.st
	m := c.tableModelOf(b)
	R, C := len(m.rows), m.ncols
	collapse := st.BorderCollapse
	// borders
	b.hasBorOv = false
	var cl *collapsed
	if collapse {
		cl = c.collapseBorders(b, m)
		b.cl = cl
		var top, bot, left, right float64
		for ci := 0; ci < C; ci++ {
			if R > 0 {
				top = math.Max(top, edgeW(cl.h[0][ci]))
				bot = math.Max(bot, edgeW(cl.h[R][ci]))
			}
		}
		for ri := 0; ri < R; ri++ {
			left = math.Max(left, edgeW(cl.v[ri][0]))
			right = math.Max(right, edgeW(cl.v[ri][C]))
		}
		b.borOv = [4]float64{top / 2, right / 2, bot / 2, left / 2}
		b.hasBorOv = true
	} else {
		b.cl = nil
	}
	c.edges(b, in.cbw)
	spH, spV := st.SpacingH, st.SpacingV
	if collapse {
		spH, spV = 0, 0
	}
	// cell edges
	for _, tc := range m.cells {
		cb := tc.b
		if collapse {
			var top, bot, left, right float64
			for ci := tc.col; ci < tc.col+tc.cs; ci++ {
				top = math.Max(top, edgeW(cl.h[tc.row][ci]))
				bot = math.Max(bot, edgeW(cl.h[tc.row+tc.rs][ci]))
			}
			for ri := tc.row; ri < tc.row+tc.rs; ri++ {
				left = math.Max(left, edgeW(cl.v[ri][tc.col]))
				right = math.Max(right, edgeW(cl.v[ri][tc.col+tc.cs]))
			}
			cb.borOv = [4]float64{top / 2, right / 2, bot / 2, left / 2}
			cb.hasBorOv = true
			cb.borMin = [4]float64{math.Ceil(top / 2), math.Ceil(right / 2), math.Ceil(bot / 2), math.Ceil(left / 2)}
		} else {
			cb.hasBorOv = false
		}
		c.edges(cb, 0)
	}
	// width available
	tableFixed := false
	var tw float64
	boxExtra := b.hExtra()
	_ = tableFixed
	cbw := in.cbw
	// column constraints
	for _, col := range m.cols {
		*col = tcolumn{box: col.box}
	}
	for _, tc := range m.cells {
		cb := tc.b
		cb.hasIntr = false
		cmin := c.minContent(cb) + cb.hExtra()
		cmax := c.maxContent(cb) + cb.hExtra()
		if collapse {
			// the minimum width uses rounded-up half borders
			cmin += (cb.borMin[1] + cb.borMin[3]) - (cb.bor[1] + cb.bor[3])
			cmax += (cb.borMin[1] + cb.borMin[3]) - (cb.bor[1] + cb.bor[3])
		}
		if cmax < cmin {
			cmax = cmin
		}
		if cb.st.Width.K == style.LPx {
			w := cb.st.Width.V
			if cb.st.BoxSizing != "border-box" {
				w += cb.hExtra()
			}
			tc.fixedW, tc.hasFixed = w, true
			cmax = math.Max(cmin, w)
		} else if cb.st.Width.K == style.LPct {
			tc.pct, tc.hasPct = cb.st.Width.V, true
		}
		tc.minW, tc.maxW = cmin, cmax
	}
	// column elements' widths
	for _, col := range m.cols {
		if col.box != nil {
			switch col.box.st.Width.K {
			case style.LPx:
				col.fixed, col.hasFixed = col.box.st.Width.V, true
			case style.LPct:
				col.pct, col.hasPct = col.box.st.Width.V, true
			}
		}
	}
	for _, tc := range m.cells {
		if tc.cs == 1 {
			col := m.cols[tc.col]
			col.min = math.Max(col.min, tc.minW)
			col.max = math.Max(col.max, tc.maxW)
			if tc.hasFixed {
				col.fixed, col.hasFixed = math.Max(col.fixed, tc.fixedW), true
			}
			if tc.hasPct {
				col.pct, col.hasPct = math.Max(col.pct, tc.pct), true
			}
		}
	}
	for _, col := range m.cols {
		if col.hasFixed && col.fixed > col.min {
			col.max = math.Max(col.max, col.fixed)
		}
		if col.hasFixed && col.max < col.fixed {
			col.max = col.fixed
		}
		if col.max < col.min {
			col.max = col.min
		}
	}
	// spanning cells
	for _, tc := range m.cells {
		if tc.cs <= 1 {
			continue
		}
		span := m.cols[tc.col : tc.col+tc.cs]
		inner := spH * float64(tc.cs-1)
		sumMin, sumMax := 0.0, 0.0
		for _, col := range span {
			sumMin += col.min
			sumMax += col.max
		}
		if need := tc.minW - inner - sumMin; need > 0 {
			distribute(span, need, func(col *tcolumn) float64 { return col.max }, func(col *tcolumn, v float64) { col.min += v })
		}
		if need := tc.maxW - inner - sumMax; need > 0 {
			distribute(span, need, func(col *tcolumn) float64 { return col.max }, func(col *tcolumn, v float64) { col.max += v })
		}
		if tc.hasPct {
			// percent spread evenly
			for _, col := range span {
				if !col.hasPct {
					col.pct, col.hasPct = tc.pct/float64(tc.cs), true
				}
			}
		}
	}
	for _, col := range m.cols {
		if col.max < col.min {
			col.max = col.min
		}
	}
	sumMin, sumMax := 0.0, 0.0
	for _, col := range m.cols {
		sumMin += col.min
		sumMax += col.max
	}
	gaps := spH * float64(C+1)
	if C == 0 {
		gaps = 0
	}
	if collapse {
		gaps = 0
	}
	// pct columns' lower bound for the table width
	pctMinTable := 0.0
	for _, col := range m.cols {
		if col.hasPct && col.pct > 0 {
			pctMinTable = math.Max(pctMinTable, (col.max)/(col.pct/100))
		}
	}
	tableMin := sumMin + gaps + boxExtra
	tableMax := sumMax + gaps + boxExtra
	if pctMinTable > 0 {
		tableMax = math.Max(tableMax, pctMinTable+gaps+boxExtra)
	}
	// table border-box width
	var bw float64
	fixedLayout := st.TableLayout == "fixed" && (st.Width.K != style.LAuto)
	if in.fixW >= 0 {
		bw = in.fixW
	} else if wv, ok := c.specW(b, st.Width, cbw); ok && !(st.Width.K == style.LAuto) {
		bw = wv + boxExtra
		if st.BoxSizing == "border-box" || true {
			// UA sets box-sizing:border-box for tables; honour the computed value
			if st.BoxSizing == "border-box" {
				bw = wv + boxExtra
			}
		}
		bw = math.Max(bw, tableMin)
		if !fixedLayout {
			bw = math.Max(bw, tableMin)
		}
	} else if in.shrink {
		avail := in.availW - b.mar[1] - b.mar[3]
		bw = math.Min(math.Max(tableMin, avail), tableMax)
	} else {
		avail := in.cbw - b.mar[1] - b.mar[3]
		bw = math.Min(math.Max(tableMin, avail), tableMax)
	}
	// captions can widen the table
	for _, cp := range m.caps {
		c.edges(cp, in.cbw)
		cp.hasIntr = false
		if bw < c.minContent(cp)+cp.hExtra() && st.Width.K == style.LAuto {
			bw = c.minContent(cp) + cp.hExtra()
		}
	}
	// max-width/min-width on the table
	if st.MaxWidth.K == style.LPx {
		mx := st.MaxWidth.V
		if st.BoxSizing != "border-box" {
			mx += boxExtra
		}
		bw = math.Min(bw, math.Max(mx, tableMin))
	}
	if v, ok := c.specW(b, st.MinWidth, cbw); ok && st.MinWidth.K != style.LAuto {
		mn := v + boxExtra
		bw = math.Max(bw, mn)
	}
	tw = bw - boxExtra - gaps
	if tw < 0 {
		tw = 0
	}
	if os.Getenv("TBLDBG") != "" {
		for _, col := range m.cols {
			println("COL", int(col.min*1000), int(col.max*1000), col.hasFixed, col.hasPct)
		}
		println("TW", int(tw*1000))
	}
	c.distributeColumns(m, tw, fixedLayout, in.cbw)
	b.w = bw
	// col x positions
	colX := make([]float64, C+1)
	x := spH
	for i, col := range m.cols {
		colX[i] = x
		x += col.w + spH
	}
	colX[C] = x - spH
	c.horizontalMargins(b, in, bw)
	// lay out cells
	for _, r := range m.rows {
		r.h = 0
		r.base = 0
	}
	for _, tc := range m.cells {
		w := 0.0
		for ci := tc.col; ci < tc.col+tc.cs; ci++ {
			w += m.cols[ci].w
		}
		w += spH * float64(tc.cs-1)
		cb := tc.b
		cb.hasLast = false
		in2 := layoutIn{cbw: bw - boxExtra, cbh: -1, fixW: w, fixH: -1, bfc: &bfcState{}}
		// cells with a specified height are laid out with it as the minimum
		c.layoutBox(cb, in2)
		tc.h = cb.h
		tc.valign = cb.st.VerticalAlign
		if cb.hasBase {
			tc.base, tc.hasBase = cb.baseline, true
		}
		if tc.valign == "baseline" {
			if !tc.hasBase {
				tc.base = cb.h
			}
		}
	}
	// row heights
	for ri, r := range m.rows {
		rh := 0.0
		if r.b.st.Height.K == style.LPx {
			rh = r.b.st.Height.V
		}
		maxAbove, maxBelow := 0.0, 0.0
		anyBase := false
		for _, tc := range r.cells {
			if tc.rs == 1 {
				rh = math.Max(rh, tc.h)
				if tc.valign == "baseline" {
					anyBase = true
					maxAbove = math.Max(maxAbove, tc.base)
					maxBelow = math.Max(maxBelow, tc.h-tc.base)
				}
			}
		}
		if anyBase {
			rh = math.Max(rh, maxAbove+maxBelow)
			r.base = maxAbove
		}
		r.h = rh
		_ = ri
	}
	// rowspans
	for _, tc := range m.cells {
		if tc.rs <= 1 {
			continue
		}
		sum := spV * float64(tc.rs-1)
		for ri := tc.row; ri < tc.row+tc.rs; ri++ {
			sum += m.rows[ri].h
		}
		if tc.h > sum {
			add := (tc.h - sum) / float64(tc.rs)
			for ri := tc.row; ri < tc.row+tc.rs; ri++ {
				m.rows[ri].h += add
			}
		}
	}
	// table height
	rowsH := spV
	for _, r := range m.rows {
		rowsH += r.h + spV
	}
	if R == 0 {
		rowsH = 0
		if !collapse {
			rowsH = 0
		}
	}
	// caption layout
	capTop, capBot := 0.0, 0.0
	for _, cp := range m.caps {
		cp.hasLast = false
		in2 := layoutIn{cbw: bw, cbh: -1, fixW: bw - cp.mar[1] - cp.mar[3], fixH: -1, bfc: &bfcState{}}
		c.layoutBox(cp, in2)
		h := cp.h + cp.mar[0] + cp.mar[2]
		if cp.st.CaptionSide == "bottom" {
			capBot += h
		} else {
			capTop += h
		}
	}
	innerH := rowsH
	extraH := 0.0
	var specH float64
	hasH := false
	if in.fixH >= 0 {
		specH, hasH = in.fixH-b.vExtra()-capTop-capBot, true
	} else if hv, ok := c.specH(b, st.Height, in.cbh); ok {
		specH, hasH = hv, true
		if st.BoxSizing != "border-box" {
			specH = hv
		}
	}
	if hasH && specH > innerH {
		extraH = specH - innerH
	}
	if mn, ok := c.specH(b, st.MinHeight, in.cbh); ok && st.MinHeight.K != style.LAuto {
		if mn-capTop-capBot > innerH+extraH {
			extraH = mn - capTop - capBot - innerH
		}
	}
	if extraH > 0 && R > 0 {
		total := 0.0
		for _, r := range m.rows {
			total += r.h
		}
		for _, r := range m.rows {
			if total > 0 {
				r.h += extraH * r.h / total
			} else {
				r.h += extraH / float64(R)
			}
		}
		innerH += extraH
	}
	// positions
	y := spV
	gridTop := capTop + b.bor[0] + b.pad[0]
	baseline := 0.0
	hasBaseline := false
	for _, r := range m.rows {
		r.y = y
		y += r.h + spV
	}
	// row group / row / cell placement (coordinates relative to parents' border boxes)
	rowX0 := b.bor[3] + b.pad[3] + spH
	rowW := colX[C] - spH
	if C == 0 {
		rowW = 0
	}
	if collapse {
		rowX0 = b.bor[3] + b.pad[3]
		rowW = colX[C]
	}
	for _, g := range m.allGroups(b) {
		_ = g
	}
	groupTop := map[*lbox]float64{}
	groupBot := map[*lbox]float64{}
	for _, r := range m.rows {
		if r.group != nil && r.group != b {
			if _, ok := groupTop[r.group]; !ok {
				groupTop[r.group] = r.y
			}
			groupBot[r.group] = r.y + r.h
		}
	}
	for g, t := range groupTop {
		g.x = rowX0
		g.y = gridTop + t
		g.w = rowW
		g.h = groupBot[g] - t
		g.lp = b
		g.hasLast = true
		g.mar = [4]float64{}
		g.bor = [4]float64{}
		g.pad = [4]float64{}
	}
	for _, r := range m.rows {
		rb := r.b
		rb.mar, rb.bor, rb.pad = [4]float64{}, [4]float64{}, [4]float64{}
		rb.w, rb.h = rowW, r.h
		rb.hasLast = true
		if r.group != nil && r.group != b {
			rb.x, rb.y = 0, r.y-groupTop[r.group]
			rb.lp = r.group
		} else {
			rb.x, rb.y = rowX0, gridTop+r.y
			rb.lp = b
		}
		for _, tc := range r.cells {
			cb := tc.b
			ch := 0.0
			for ri := tc.row; ri < tc.row+tc.rs; ri++ {
				ch += m.rows[ri].h
			}
			ch += spV * float64(tc.rs-1)
			// vertical alignment
			free := ch - cb.h
			shift := 0.0
			switch tc.valign {
			case "middle":
				shift = free / 2
			case "bottom":
				shift = free
			case "baseline":
				if tc.rs == 1 {
					shift = r.base - tc.base
				}
			}
			if shift > 0.0001 || free > 0.0001 {
				c.shiftKidsY(cb, shift)
			}
			cb.h = ch
			// cell geometry relative to its row
			cb.x = colX[tc.col] - (rowX0 - b.bor[3] - b.pad[3])
			cb.y = 0
			cb.lp = rb
			cb.mar = [4]float64{}
			if tc.valign == "baseline" && tc.rs == 1 && !hasBaseline {
				baseline = r.y + r.base + gridTop
				hasBaseline = true
			}
		}
	}
	// captions position
	cy := 0.0
	for _, cp := range m.caps {
		if cp.st.CaptionSide == "bottom" {
			continue
		}
		cp.x = cp.mar[3]
		cp.y = cy + cp.mar[0]
		cp.lp = b
		cy += cp.h + cp.mar[0] + cp.mar[2]
	}
	by := capTop + b.bor[0] + b.pad[0] + innerH + b.pad[2] + b.bor[2]
	for _, cp := range m.caps {
		if cp.st.CaptionSide != "bottom" {
			continue
		}
		cp.x = cp.mar[3]
		cp.y = by + cp.mar[0]
		by += cp.h + cp.mar[0] + cp.mar[2]
	}
	b.h = capTop + capBot + innerH + b.vExtra()
	if hasH && st.BoxSizing == "border-box" {
		// border-box heights already include borders: not extending beyond rows
	}
	b.contentH = innerH
	b.mtopOut, b.mbotOut = b.mar[0], b.mar[2]
	b.through = false
	b.hasBase = false
	if hasBaseline {
		b.hasBase, b.baseline = true, baseline
	} else if len(m.rows) > 0 {
		r := m.rows[0]
		b.hasBase = true
		b.baseline = gridTop + r.y + r.h/2
		for _, tc := range r.cells {
			if tc.hasBase {
				b.baseline = gridTop + r.y + tc.base
				break
			}
		}
	}
	// positioned descendants
	pushed := c.pushCB(b)
	c.popCB(b, pushed)
	for _, k := range b.kids {
		if k.oof {
			k.lp = b
			k.staticX = b.bor[3] + b.pad[3]
			k.staticY = b.bor[0] + b.pad[0]
			c.addOOF(k)
		}
	}
	c.finishOOF(b)
	b.lastIn.fixW = in.fixW
}

func (m *tableModel) allGroups(b *lbox) []*lbox { return nil }

// shiftKidsY moves the contents of a cell vertically (vertical alignment).
func (c *ctx) shiftKidsY(cell *lbox, dy float64) {
	if dy == 0 {
		return
	}
	if cell.ifc {
		for _, k := range cell.inlineKids {
			k.y += dy
		}
		for _, ln := range cell.lines {
			ln.y += dy
		}
		return
	}
	for _, k := range cell.kids {
		if k.oof || k.kind == kText || k.kind == kInline || k.kind == kBreak || k.kind == kMarker {
			continue
		}
		k.y += dy
	}
	for _, k := range cell.oofKids {
		if k.lp == cell {
			_ = k
		}
	}
}

func distribute(cols []*tcolumn, amount float64, weight func(*tcolumn) float64, add func(*tcolumn, float64)) {
	total := 0.0
	for _, c := range cols {
		total += weight(c)
	}
	for _, c := range cols {
		if total > 0 {
			add(c, amount*weight(c)/total)
		} else {
			add(c, amount/float64(len(cols)))
		}
	}
}

// distributeColumns assigns final widths (tw = total width of all columns).
func (c *ctx) distributeColumns(m *tableModel, tw float64, fixed bool, cbw float64) {
	cols := m.cols
	if len(cols) == 0 {
		return
	}
	if fixed {
		// table-layout: fixed: widths from <col>, then first-row cells, rest equal
		w := make([]float64, len(cols))
		set := make([]bool, len(cols))
		for i, col := range cols {
			if col.hasFixed {
				w[i], set[i] = col.fixed, true
			} else if col.hasPct {
				w[i], set[i] = col.pct*tw/100, true
			}
		}
		if len(m.rows) > 0 {
			for _, tc := range m.rows[0].cells {
				if tc.hasFixed && tc.cs == 1 && !set[tc.col] {
					w[tc.col], set[tc.col] = tc.fixedW, true
				} else if tc.hasPct && tc.cs == 1 && !set[tc.col] {
					w[tc.col], set[tc.col] = tc.pct*tw/100, true
				}
			}
		}
		used, unset := 0.0, 0
		for i := range w {
			if set[i] {
				used += w[i]
			} else {
				unset++
			}
		}
		if unset > 0 {
			rest := math.Max(0, tw-used) / float64(unset)
			for i := range w {
				if !set[i] {
					w[i] = rest
				}
			}
		} else if used < tw {
			// grow proportionally
			for i := range w {
				w[i] += (tw - used) * w[i] / used
			}
		}
		for i, col := range cols {
			col.w = w[i]
		}
		return
	}
	sumMin := 0.0
	for _, col := range cols {
		col.w = col.min
		sumMin += col.min
	}
	if tw <= sumMin {
		return
	}
	// percentage columns
	remaining := tw
	pctTotal := 0.0
	for _, col := range cols {
		if col.hasPct {
			want := math.Max(col.min, col.pct*tw/100)
			col.w = want
			pctTotal += want
		}
	}
	// others
	var auto, fixedCols []*tcolumn
	for _, col := range cols {
		if col.hasPct {
			continue
		}
		if col.hasFixed {
			fixedCols = append(fixedCols, col)
		} else {
			auto = append(auto, col)
		}
	}
	otherMin := 0.0
	for _, col := range cols {
		if !col.hasPct {
			otherMin += col.min
		}
	}
	if pctTotal+otherMin > tw && pctTotal > 0 {
		// not enough room: percent columns shrink toward their minimum
		avail := math.Max(0, tw-otherMin)
		sumPctMin := 0.0
		for _, col := range cols {
			if col.hasPct {
				sumPctMin += col.min
			}
		}
		for _, col := range cols {
			if col.hasPct {
				if pctTotal > 0 {
					col.w = math.Max(col.min, col.w*avail/pctTotal)
				}
			}
		}
		_ = sumPctMin
		return
	}
	remaining = tw - pctTotal
	sumMaxAuto, sumMinAuto, sumMaxFixed, sumMinFixed := 0.0, 0.0, 0.0, 0.0
	for _, col := range auto {
		sumMaxAuto += col.max
		sumMinAuto += col.min
	}
	for _, col := range fixedCols {
		sumMaxFixed += col.max
		sumMinFixed += col.min
	}
	if len(auto)+len(fixedCols) == 0 {
		return
	}
	if remaining >= sumMaxAuto+sumMaxFixed {
		// everything reaches its preferred width; excess goes to auto columns
		for _, col := range auto {
			col.w = col.max
		}
		for _, col := range fixedCols {
			col.w = col.max
		}
		excess := remaining - sumMaxAuto - sumMaxFixed
		switch {
		case len(auto) > 0 && sumMaxAuto > 0:
			for _, col := range auto {
				col.w += excess * col.max / sumMaxAuto
			}
		case len(auto) > 0:
			for _, col := range auto {
				col.w += excess / float64(len(auto))
			}
		case len(fixedCols) > 0:
			for _, col := range fixedCols {
				col.w += excess * col.max / math.Max(sumMaxFixed, 1e-9)
			}
		}
		return
	}
	// between minimum and preferred widths
	// fixed columns first take their preferred width
	rem := remaining
	if len(auto) > 0 {
		// auto columns share between min and max; fixed columns at max if possible
		fixedNeed := sumMaxFixed
		if rem-fixedNeed >= sumMinAuto {
			for _, col := range fixedCols {
				col.w = col.max
			}
			rem -= fixedNeed
		} else {
			for _, col := range fixedCols {
				col.w = col.min
			}
			rem -= sumMinFixed
		}
		span := sumMaxAuto - sumMinAuto
		if span > 0 {
			f := (rem - sumMinAuto) / span
			f = clampf(f, 0, 1)
			for _, col := range auto {
				col.w = col.min + (col.max-col.min)*f
			}
		}
		return
	}
	span := sumMaxFixed - sumMinFixed
	if span > 0 {
		f := clampf((rem-sumMinFixed)/span, 0, 1)
		for _, col := range fixedCols {
			col.w = col.min + (col.max-col.min)*f
		}
	}
}

func (c *ctx) tableIntrinsic(b *lbox) (float64, float64) {
	m := c.tableModelOf(b)
	st := b.st
	C := m.ncols
	if len(m.cols) == 0 {
		return 0, 0
	}
	collapse := st.BorderCollapse
	spH := st.SpacingH
	if collapse {
		spH = 0
	}
	sumMin, sumMax := 0.0, 0.0
	cmin := make([]float64, C)
	cmax := make([]float64, C)
	for _, tc := range m.cells {
		cb := tc.b
		c.edges(cb, 0)
		mn := c.minContent(cb) + cb.hExtra()
		mx := c.maxContent(cb) + cb.hExtra()
		if cb.st.Width.K == style.LPx {
			w := cb.st.Width.V
			if cb.st.BoxSizing != "border-box" {
				w += cb.hExtra()
			}
			mx = math.Max(mn, w)
		}
		if tc.cs == 1 {
			cmin[tc.col] = math.Max(cmin[tc.col], mn)
			cmax[tc.col] = math.Max(cmax[tc.col], mx)
		}
	}
	for _, tc := range m.cells {
		if tc.cs > 1 {
			cb := tc.b
			mn := c.minContent(cb) + cb.hExtra()
			mx := c.maxContent(cb) + cb.hExtra()
			s0, s1 := 0.0, 0.0
			for i := tc.col; i < tc.col+tc.cs; i++ {
				s0 += cmin[i]
				s1 += cmax[i]
			}
			if mn > s0 {
				for i := tc.col; i < tc.col+tc.cs; i++ {
					cmin[i] += (mn - s0) / float64(tc.cs)
				}
			}
			if mx > s1 {
				for i := tc.col; i < tc.col+tc.cs; i++ {
					cmax[i] += (mx - s1) / float64(tc.cs)
				}
			}
		}
	}
	for i := 0; i < C; i++ {
		sumMin += cmin[i]
		sumMax += math.Max(cmax[i], cmin[i])
	}
	gaps := spH * float64(C+1)
	for _, cp := range m.caps {
		c.edges(cp, 0)
		sumMin = math.Max(sumMin, c.minContent(cp)+cp.hExtra()-gaps)
		sumMax = math.Max(sumMax, c.maxContent(cp)+cp.hExtra()-gaps)
	}
	// content-box widths: remove the table's own border/padding (added by callers)
	return sumMin + gaps, sumMax + gaps
}
