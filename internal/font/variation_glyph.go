package font

import "math"

// variation_glyph.go: glyf parsing for variable fonts, gvar tuple evaluation,
// inferred deltas (IUP) and HVAR.

type comp struct {
	gid     int
	flags   int
	dx, dy  float64
	a, b, c float64 // matrix: x' = a*x + c*y + dx, y' = b*x + d*y + dy
	d       float64
}

type rawGlyph struct {
	pts   []gpt  // simple glyph: outline points; composite: one entry per component (its offset)
	ends  []int  // contour end indexes (simple glyphs)
	comps []comp // composite glyph
}

const (
	cArgWords   = 0x0001
	cArgsXY     = 0x0002
	cScale      = 0x0008
	cMore       = 0x0020
	cXYScale    = 0x0040
	c2x2        = 0x0080
	cScaledOffs = 0x0800
)

func (v *variation) glyphBytes(gid int) []byte {
	if gid < 0 || gid >= v.nGly {
		return nil
	}
	var a, b int
	if v.locL {
		t := otTable(v.loca)
		a, b = t.u32(4*gid), t.u32(4*gid+4)
	} else {
		t := otTable(v.loca)
		a, b = 2*t.u16(2*gid), 2*t.u16(2*gid+2)
	}
	if a < 0 || b < a || b > len(v.glyf) {
		return nil
	}
	return v.glyf[a:b]
}

// rawGlyph parses a glyph of the default instance.
func (v *variation) rawGlyph(gid int) (*rawGlyph, error) {
	b := v.glyphBytes(gid)
	g := &rawGlyph{}
	if len(b) == 0 {
		return g, nil
	}
	r := &rd{b: b}
	nc := r.i16()
	r.skip(8)
	if r.err != nil {
		return nil, r.err
	}
	if nc >= 0 {
		g.ends = make([]int, nc)
		last := -1
		for i := range g.ends {
			g.ends[i] = r.u16()
			if g.ends[i] < last {
				return nil, bad("glyph contours")
			}
			last = g.ends[i]
		}
		r.skip(r.u16())
		np := 0
		if nc > 0 {
			np = g.ends[nc-1] + 1
		}
		if r.err != nil || np > 65535 {
			return nil, bad("glyph")
		}
		fl := make([]byte, 0, np)
		for len(fl) < np {
			f := byte(r.u8())
			fl = append(fl, f)
			if f&flagRepeat != 0 {
				for rep := r.u8(); rep > 0 && len(fl) < np; rep-- {
					fl = append(fl, f)
				}
			}
			if r.err != nil {
				return nil, r.err
			}
		}
		g.pts = make([]gpt, np)
		x := 0
		for i, f := range fl {
			switch {
			case f&flagXShort != 0:
				d := r.u8()
				if f&flagXSame == 0 {
					d = -d
				}
				x += d
			case f&flagXSame == 0:
				x += r.i16()
			}
			g.pts[i].x = float64(x)
			g.pts[i].on = f&flagOnCurve != 0
		}
		y := 0
		for i, f := range fl {
			switch {
			case f&flagYShort != 0:
				d := r.u8()
				if f&flagYSame == 0 {
					d = -d
				}
				y += d
			case f&flagYSame == 0:
				y += r.i16()
			}
			g.pts[i].y = float64(y)
		}
		if r.err != nil {
			return nil, r.err
		}
		return g, nil
	}
	for {
		fl := r.u16()
		c := comp{gid: r.u16(), flags: fl, a: 1, d: 1}
		if fl&cArgsXY == 0 {
			return nil, bad("point matching composites are not supported")
		}
		if fl&cArgWords != 0 {
			c.dx, c.dy = float64(r.i16()), float64(r.i16())
		} else {
			c.dx, c.dy = float64(int8(r.u8())), float64(int8(r.u8()))
		}
		switch {
		case fl&cScale != 0:
			c.a = f2dot14(r.i16())
			c.d = c.a
		case fl&cXYScale != 0:
			c.a = f2dot14(r.i16())
			c.d = f2dot14(r.i16())
		case fl&c2x2 != 0:
			c.a = f2dot14(r.i16())
			c.b = f2dot14(r.i16())
			c.c = f2dot14(r.i16())
			c.d = f2dot14(r.i16())
		}
		if r.err != nil {
			return nil, r.err
		}
		g.comps = append(g.comps, c)
		g.pts = append(g.pts, gpt{x: c.dx, y: c.dy, on: true})
		if fl&cMore == 0 || len(g.comps) > maxCompsTotal {
			break
		}
	}
	return g, nil
}

// gvarData is the GlyphVariationData of gid (nil: none).
func (v *variation) gvarData(gid int) []byte {
	if gid < 0 || gid >= v.nGly {
		return nil
	}
	t := v.gvar
	var a, b int
	if v.gvarLong {
		a, b = t.u32(v.gvarOffs+4*gid), t.u32(v.gvarOffs+4*gid+4)
	} else {
		a, b = 2*t.u16(v.gvarOffs+2*gid), 2*t.u16(v.gvarOffs+2*gid+2)
	}
	a += v.gvarArrOff
	b += v.gvarArrOff
	if a < 0 || b <= a || b > len(t) {
		return nil
	}
	return t[a:b]
}

func tupleScalar(coords, peak, start, end []float64) float64 {
	s := 1.0
	for i, c := range coords {
		if i >= len(peak) {
			break
		}
		p := peak[i]
		if p == 0 || c == p {
			continue
		}
		st, en := start[i], end[i]
		if c <= st || c >= en {
			return 0
		}
		if c < p {
			s *= (c - st) / (p - st)
		} else {
			s *= (en - c) / (en - p)
		}
	}
	return s
}

// readPoints reads packed point numbers; all reports that every point is meant.
func readPoints(r *rd, total int) (pts []int, all bool, err error) {
	b := r.u8()
	if r.err != nil {
		return nil, false, r.err
	}
	if b == 0 {
		return nil, true, nil
	}
	n := b
	if b&0x80 != 0 {
		n = (b&0x7f)<<8 | r.u8()
	}
	if n > total {
		return nil, false, bad("gvar: too many points")
	}
	pts = make([]int, 0, n)
	p := 0
	for len(pts) < n {
		ctrl := r.u8()
		run := ctrl&0x7f + 1
		for i := 0; i < run && len(pts) < n; i++ {
			if ctrl&0x80 != 0 {
				p += r.u16()
			} else {
				p += r.u8()
			}
			if r.err != nil {
				return nil, false, r.err
			}
			if p >= total {
				return nil, false, bad("gvar: point number out of range")
			}
			pts = append(pts, p)
		}
		if r.err != nil {
			return nil, false, r.err
		}
	}
	return pts, false, nil
}

func readDeltas(r *rd, n int) ([]float64, error) {
	out := make([]float64, n)
	for i := 0; i < n; {
		ctrl := r.u8()
		run := ctrl&0x3f + 1
		if r.err != nil || i+run > n {
			return nil, bad("gvar: deltas overrun")
		}
		switch {
		case ctrl&0x80 != 0:
			// zeros
		case ctrl&0x40 != 0:
			for k := 0; k < run; k++ {
				out[i+k] = float64(r.i16())
			}
		default:
			for k := 0; k < run; k++ {
				out[i+k] = float64(int8(r.u8()))
			}
		}
		if r.err != nil {
			return nil, r.err
		}
		i += run
	}
	return out, nil
}

// glyphDeltas sums the gvar deltas for coords: dx, dy have len(g.pts)+4 entries
// (the last four are the phantom points).
func (v *variation) glyphDeltas(gid int, g *rawGlyph, coords []float64) (dx, dy []float64, err error) {
	n := len(g.pts)
	dx, dy = make([]float64, n+4), make([]float64, n+4)
	data := v.gvarData(gid)
	if len(data) == 0 {
		return dx, dy, nil
	}
	nAx := len(v.axes)
	r := &rd{b: data}
	tvc := r.u16()
	dataOff := r.u16()
	count := tvc & 0x0fff
	if r.err != nil || count > maxTuples || dataOff > len(data) {
		return nil, nil, bad("gvar: bad glyph header")
	}
	type hdr struct {
		size, idx        int
		peak, start, end []float64
	}
	hs := make([]hdr, count)
	for i := range hs {
		h := &hs[i]
		h.size, h.idx = r.u16(), r.u16()
		if h.idx&0x8000 != 0 {
			h.peak = make([]float64, nAx)
			for k := range h.peak {
				h.peak[k] = f2dot14(r.i16())
			}
		} else {
			si := h.idx & 0x0fff
			if si >= v.nShared {
				return nil, nil, bad("gvar: shared tuple index")
			}
			h.peak = v.sharedTuples[si*nAx : (si+1)*nAx]
		}
		if h.idx&0x4000 != 0 {
			h.start, h.end = make([]float64, nAx), make([]float64, nAx)
			for k := range h.start {
				h.start[k] = f2dot14(r.i16())
			}
			for k := range h.end {
				h.end[k] = f2dot14(r.i16())
			}
		} else {
			h.start, h.end = make([]float64, nAx), make([]float64, nAx)
			for k, p := range h.peak {
				h.start[k], h.end[k] = math.Min(0, p), math.Max(0, p)
			}
		}
		if r.err != nil {
			return nil, nil, r.err
		}
	}
	sr := &rd{b: data[dataOff:]}
	var shared []int
	sharedAll := false
	if tvc&0x8000 != 0 {
		shared, sharedAll, err = readPoints(sr, n+4)
		if err != nil {
			return nil, nil, err
		}
	}
	touched := make([]bool, n)
	tx, ty := make([]float64, n), make([]float64, n)
	for _, h := range hs {
		td := sr.bytes(h.size)
		if sr.err != nil {
			return nil, nil, sr.err
		}
		sc := tupleScalar(coords, h.peak, h.start, h.end)
		if sc == 0 {
			continue
		}
		tr := &rd{b: td}
		pts, all := shared, sharedAll
		if h.idx&0x2000 != 0 {
			pts, all, err = readPoints(tr, n+4)
			if err != nil {
				return nil, nil, err
			}
		}
		cnt := len(pts)
		if all {
			cnt = n + 4
		}
		ddx, err1 := readDeltas(tr, cnt)
		ddy, err2 := readDeltas(tr, cnt)
		if err1 != nil || err2 != nil {
			return nil, nil, bad("gvar: bad deltas")
		}
		if all {
			for k := 0; k < n+4; k++ {
				dx[k] += sc * ddx[k]
				dy[k] += sc * ddy[k]
			}
			continue
		}
		for k := range touched {
			touched[k] = false
			tx[k], ty[k] = 0, 0
		}
		for i, p := range pts {
			if p >= n {
				dx[p] += sc * ddx[i]
				dy[p] += sc * ddy[i]
				continue
			}
			touched[p] = true
			tx[p], ty[p] = ddx[i], ddy[i]
		}
		if len(g.ends) > 0 {
			iup(g, touched, tx, ty)
		}
		for k := 0; k < n; k++ {
			dx[k] += sc * tx[k]
			dy[k] += sc * ty[k]
		}
	}
	return dx, dy, nil
}

// iup infers the deltas of untouched points from the touched ones of their
// contour (interpolating each axis separately, as the TrueType rasteriser
// does).
func iup(g *rawGlyph, touched []bool, tx, ty []float64) {
	start := 0
	for _, end := range g.ends {
		if end >= len(touched) {
			return
		}
		var idx []int
		for i := start; i <= end; i++ {
			if touched[i] {
				idx = append(idx, i)
			}
		}
		switch len(idx) {
		case 0:
		case 1:
			for i := start; i <= end; i++ {
				tx[i], ty[i] = tx[idx[0]], ty[idx[0]]
			}
		default:
			for k, a := range idx {
				b := idx[(k+1)%len(idx)]
				for i := a + 1; ; i++ {
					if i > end {
						i = start
					}
					if i == b {
						break
					}
					tx[i] = iupDelta(g.pts[i].x, g.pts[a].x, g.pts[b].x, tx[a], tx[b])
					ty[i] = iupDelta(g.pts[i].y, g.pts[a].y, g.pts[b].y, ty[a], ty[b])
				}
			}
		}
		start = end + 1
	}
}

func iupDelta(c, cA, cB, dA, dB float64) float64 {
	if cA > cB {
		cA, cB = cB, cA
		dA, dB = dB, dA
	}
	switch {
	case cA == cB:
		if dA == dB {
			return dA
		}
		return 0
	case c <= cA:
		return dA
	case c >= cB:
		return dB
	}
	return dA + (c-cA)*(dB-dA)/(cB-cA)
}

// glyphContours returns the glyph's contours in font units for coords.
func (v *variation) glyphContours(d *instanceData, gid int, coords []float64, depth int) ([][]gpt, error) {
	budget := maxCompsTotal
	return v.contours(gid, coords, depth, &budget)
}

func (v *variation) contours(gid int, coords []float64, depth int, budget *int) ([][]gpt, error) {
	if depth > maxCompDepth {
		return nil, bad("composite glyph too deep")
	}
	g, err := v.rawGlyph(gid)
	if err != nil {
		return nil, err
	}
	dx, dy, err := v.glyphDeltas(gid, g, coords)
	if err != nil {
		return nil, err
	}
	if len(g.comps) == 0 {
		var out [][]gpt
		start := 0
		for _, end := range g.ends {
			c := make([]gpt, 0, end-start+1)
			for i := start; i <= end; i++ {
				p := g.pts[i]
				c = append(c, gpt{x: p.x + dx[i], y: p.y + dy[i], on: p.on})
			}
			out = append(out, c)
			start = end + 1
		}
		return out, nil
	}
	var out [][]gpt
	for i, c := range g.comps {
		if *budget--; *budget < 0 {
			return nil, bad("too many components")
		}
		child, err := v.contours(c.gid, coords, depth+1, budget)
		if err != nil {
			return nil, err
		}
		ox, oy := c.dx+dx[i], c.dy+dy[i]
		if c.flags&cScaledOffs != 0 {
			ox, oy = ox*math.Hypot(c.a, c.b), oy*math.Hypot(c.c, c.d)
		}
		for _, ct := range child {
			nc := make([]gpt, len(ct))
			for k, p := range ct {
				nc[k] = gpt{x: c.a*p.x + c.c*p.y + ox, y: c.b*p.x + c.d*p.y + oy, on: p.on}
			}
			out = append(out, nc)
		}
	}
	return out, nil
}

// ---- HVAR --------------------------------------------------------------------------

type regionAxis struct{ start, peak, end float64 }

type ivData struct {
	regions []int
	items   int
	words   int
	long    bool
	rows    []byte
}

type hvarTable struct {
	regions [][]regionAxis
	data    []ivData
	advMap  []byte // DeltaSetIndexMap, nil: index = glyph id
}

func parseHVAR(b []byte, nAx int) (h *hvarTable) {
	defer func() {
		if recover() != nil {
			h = nil
		}
	}()
	t := otTable(b)
	if t.u16(0) != 1 {
		return nil
	}
	ivsOff, advOff := t.u32(4), t.u32(8)
	if ivsOff <= 0 || ivsOff >= len(b) {
		return nil
	}
	s := otTable(b[ivsOff:])
	if s.u16(0) != 1 {
		return nil
	}
	rlOff := s.u32(2)
	nData := s.u16(6)
	if rlOff <= 0 || rlOff+4 > len(s) || nData > 4096 {
		return nil
	}
	if s.u16(rlOff) != nAx {
		return nil
	}
	nReg := s.u16(rlOff + 2)
	if rlOff+4+nReg*nAx*6 > len(s) {
		return nil
	}
	h = &hvarTable{}
	for i := 0; i < nReg; i++ {
		var ra []regionAxis
		for a := 0; a < nAx; a++ {
			o := rlOff + 4 + (i*nAx+a)*6
			ra = append(ra, regionAxis{f2dot14(s.i16(o)), f2dot14(s.i16(o + 2)), f2dot14(s.i16(o + 4))})
		}
		h.regions = append(h.regions, ra)
	}
	for i := 0; i < nData; i++ {
		o := s.u32(8 + 4*i)
		if o <= 0 || o+6 > len(s) {
			return nil
		}
		items, wc, nr := s.u16(o), s.u16(o+2), s.u16(o+4)
		long := wc&0x8000 != 0
		wc &= 0x7fff
		if nr > nReg || wc > nr || o+6+2*nr > len(s) {
			return nil
		}
		d := ivData{items: items, words: wc, long: long}
		for k := 0; k < nr; k++ {
			ri := s.u16(o + 6 + 2*k)
			if ri >= nReg {
				return nil
			}
			d.regions = append(d.regions, ri)
		}
		w, bsz := 2, 1
		if long {
			w, bsz = 4, 2
		}
		row := wc*w + (nr-wc)*bsz
		start := o + 6 + 2*nr
		if start+items*row > len(s) {
			return nil
		}
		d.rows = s[start : start+items*row]
		h.data = append(h.data, d)
	}
	if advOff > 0 && advOff < len(b) {
		h.advMap = b[advOff:]
	}
	return h
}

func regionScalar(ra []regionAxis, coords []float64) float64 {
	s := 1.0
	for i, c := range coords {
		if i >= len(ra) {
			break
		}
		r := ra[i]
		if r.peak == 0 || r.start > r.peak || r.peak > r.end || (r.start < 0 && r.end > 0) {
			continue
		}
		if c == r.peak {
			continue
		}
		if c <= r.start || c >= r.end {
			return 0
		}
		if c < r.peak {
			s *= (c - r.start) / (r.peak - r.start)
		} else {
			s *= (r.end - c) / (r.end - r.peak)
		}
	}
	return s
}

// advanceDelta is the HVAR advance width delta of gid.
func (h *hvarTable) advanceDelta(gid int, coords []float64) (float64, bool) {
	outer, inner := 0, gid
	if h.advMap != nil {
		m := otTable(h.advMap)
		format := h.advMap[0]
		ef := int(h.advMap[1])
		var count, hdr int
		if format == 0 {
			count, hdr = m.u16(2), 4
		} else {
			count, hdr = m.u32(2), 6
		}
		if count == 0 {
			return 0, false
		}
		i := gid
		if i >= count {
			i = count - 1
		}
		sz := (ef>>4)&3 + 1
		bits := uint(ef&0xf) + 1
		o := hdr + i*sz
		if o+sz > len(h.advMap) {
			return 0, false
		}
		val := 0
		for k := 0; k < sz; k++ {
			val = val<<8 | int(h.advMap[o+k])
		}
		outer, inner = val>>bits, val&(1<<bits-1)
	}
	if outer >= len(h.data) {
		return 0, false
	}
	d := h.data[outer]
	if inner >= d.items {
		return 0, false
	}
	w, bsz := 2, 1
	if d.long {
		w, bsz = 4, 2
	}
	row := d.words*w + (len(d.regions)-d.words)*bsz
	r := &rd{b: d.rows[inner*row : (inner+1)*row]}
	sum := 0.0
	for k, ri := range d.regions {
		var dv int
		if k < d.words {
			if d.long {
				dv = int(int32(r.u32()))
			} else {
				dv = r.i16()
			}
		} else if d.long {
			dv = r.i16()
		} else {
			dv = int(int8(r.u8()))
		}
		sc := regionScalar(h.regions[ri], coords)
		sum += sc * float64(dv)
	}
	if r.err != nil {
		return 0, false
	}
	return sum, true
}
