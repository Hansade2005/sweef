package fonttest

import "math"

// Variable font building blocks: fvar, avar, gvar and HVAR writers, enough to
// make small variable fonts for tests.

// Axis is one fvar axis.
type Axis struct {
	Tag           string
	Min, Def, Max float64
}

// Tuple is one gvar tuple variation of a glyph.
type Tuple struct {
	SharedIdx    int       // index into VarSpec.Shared; -1 embeds Peak
	Peak         []float64 // embedded peak (normalised coordinates)
	Start, End   []float64 // intermediate region (both or none)
	Points       []int     // touched points; nil means all points (with the four phantom points)
	DX, DY       []int     // deltas of the listed points (or of all points)
	UseSharedPts bool      // take the point numbers from the glyph's shared point set
}

// GlyphVar is the variation data of one glyph.
type GlyphVar struct {
	HasShared bool  // the glyph has a shared point number set
	SharedPts []int // nil with HasShared: all points
	Tuples    []Tuple
}

// HVAR is a minimal horizontal metrics variation table: one region per entry
// of Regions (axis triples), advance deltas per glyph and region.
type HVAR struct {
	Regions [][][3]float64 // [region][axis] = start, peak, end
	Deltas  [][]int        // [glyph][region]
}

// VarSpec describes the variations of a font.
type VarSpec struct {
	Axes     []Axis
	Shared   [][]float64
	Glyphs   map[int]GlyphVar
	Avar     [][][2]float64 // per axis (from, to) pairs; nil: no avar table
	HVAR     *HVAR
	LongGvar bool // 32-bit glyph variation data offsets
}

func fixed1616(v float64) []byte { return be32(uint32(int32(math.Round(v * 65536)))) }
func f2d14(v float64) []byte     { return be16(int(int16(math.Round(v*16384))) & 0xffff) }

// AddVariations turns the font into a variable font.
func (f *Font) AddVariations(v VarSpec) {
	if f.ExtraTables == nil {
		f.ExtraTables = map[string][]byte{}
	}
	nAx := len(v.Axes)
	fv := cat(be16(1), be16(0), be16(16), be16(2), be16(nAx), be16(20), be16(0), be16(0))
	for _, a := range v.Axes {
		fv = append(fv, cat([]byte(a.Tag), fixed1616(a.Min), fixed1616(a.Def), fixed1616(a.Max), be16(0), be16(256))...)
	}
	f.ExtraTables["fvar"] = fv
	if v.Avar != nil {
		av := cat(be16(1), be16(0), be16(0), be16(nAx))
		for _, m := range v.Avar {
			av = append(av, be16(len(m))...)
			for _, p := range m {
				av = append(av, cat(f2d14(p[0]), f2d14(p[1]))...)
			}
		}
		f.ExtraTables["avar"] = av
	}
	f.ExtraTables["gvar"] = f.gvar(v)
	if v.HVAR != nil {
		f.ExtraTables["HVAR"] = f.hvar(v)
	}
}

func packPoints(pts []int) []byte {
	if pts == nil {
		return []byte{0}
	}
	var out []byte
	n := len(pts)
	if n < 128 {
		out = append(out, byte(n))
	} else {
		out = append(out, byte(0x80|n>>8), byte(n))
	}
	prev := 0
	for i := 0; i < n; {
		j := min(i+128, n)
		words := false
		p := prev
		for k := i; k < j; k++ {
			if pts[k]-p > 255 {
				words = true
			}
			p = pts[k]
		}
		ctrl := byte(j - i - 1)
		if words {
			ctrl |= 0x80
		}
		out = append(out, ctrl)
		p = prev
		for k := i; k < j; k++ {
			d := pts[k] - p
			p = pts[k]
			if words {
				out = append(out, be16(d)...)
			} else {
				out = append(out, byte(d))
			}
		}
		prev = p
		i = j
	}
	return out
}

func packDeltas(d []int) []byte {
	var out []byte
	for i := 0; i < len(d); {
		j := i
		switch {
		case d[i] == 0:
			for j < len(d) && j-i < 64 && d[j] == 0 {
				j++
			}
			out = append(out, byte(0x80|(j-i-1)))
		case d[i] >= -128 && d[i] <= 127:
			for j < len(d) && j-i < 64 && d[j] >= -128 && d[j] <= 127 {
				j++
			}
			out = append(out, byte(j-i-1))
			for k := i; k < j; k++ {
				out = append(out, byte(int8(d[k])))
			}
		default:
			for j < len(d) && j-i < 64 && (d[j] < -128 || d[j] > 127) {
				j++
			}
			out = append(out, byte(0x40|(j-i-1)))
			for k := i; k < j; k++ {
				out = append(out, be16(d[k]&0xffff)...)
			}
		}
		i = j
	}
	return out
}

func (f *Font) gvar(v VarSpec) []byte {
	nAx := len(v.Axes)
	n := len(f.Glyphs)
	var shared []byte
	for _, t := range v.Shared {
		for _, c := range t {
			shared = append(shared, f2d14(c)...)
		}
	}
	var blobs [][]byte
	for g := 0; g < n; g++ {
		gv, ok := v.Glyphs[g]
		if !ok || len(gv.Tuples) == 0 {
			blobs = append(blobs, nil)
			continue
		}
		var hdrs, ser []byte
		count := len(gv.Tuples)
		flags := 0
		if gv.HasShared {
			flags |= 0x8000
			ser = append(ser, packPoints(gv.SharedPts)...)
		}
		for _, t := range gv.Tuples {
			var td []byte
			idx := 0
			switch {
			case t.SharedIdx >= 0:
				idx = t.SharedIdx
			default:
				idx |= 0x8000
			}
			if t.Start != nil {
				idx |= 0x4000
			}
			if !t.UseSharedPts {
				idx |= 0x2000
				td = append(td, packPoints(t.Points)...)
			}
			td = append(td, packDeltas(t.DX)...)
			td = append(td, packDeltas(t.DY)...)
			hdrs = append(hdrs, cat(be16(len(td)), be16(idx))...)
			if t.SharedIdx < 0 {
				for _, c := range t.Peak {
					hdrs = append(hdrs, f2d14(c)...)
				}
			}
			if t.Start != nil {
				for _, c := range t.Start {
					hdrs = append(hdrs, f2d14(c)...)
				}
				for _, c := range t.End {
					hdrs = append(hdrs, f2d14(c)...)
				}
			}
			ser = append(ser, td...)
		}
		head := cat(be16(flags|count), be16(4+len(hdrs)))
		blobs = append(blobs, pad2(cat(head, hdrs, ser)))
	}
	offs := make([]int, n+1)
	var data []byte
	for i, b := range blobs {
		offs[i] = len(data)
		data = append(data, b...)
	}
	offs[n] = len(data)
	flags := 0
	var offTab []byte
	if v.LongGvar {
		flags = 1
		for _, o := range offs {
			offTab = append(offTab, be32(uint32(o))...)
		}
	} else {
		for _, o := range offs {
			offTab = append(offTab, be16(o/2)...)
		}
	}
	hdrLen := 20
	sharedOff := hdrLen + len(offTab)
	dataOff := sharedOff + len(shared)
	return cat(be16(1), be16(0), be16(nAx), be16(len(v.Shared)), be32(uint32(sharedOff)), be16(n), be16(flags), be32(uint32(dataOff)), offTab, shared, data)
}

func pad2(b []byte) []byte {
	if len(b)&1 != 0 {
		b = append(b, 0)
	}
	return b
}

func (f *Font) hvar(v VarSpec) []byte {
	h := v.HVAR
	nAx := len(v.Axes)
	nReg := len(h.Regions)
	// region list
	rl := cat(be16(nAx), be16(nReg))
	for _, r := range h.Regions {
		for _, a := range r {
			rl = append(rl, cat(f2d14(a[0]), f2d14(a[1]), f2d14(a[2]))...)
		}
	}
	// one ItemVariationData with every glyph as an item, all deltas words
	n := len(f.Glyphs)
	idata := cat(be16(n), be16(nReg), be16(nReg))
	for r := 0; r < nReg; r++ {
		idata = append(idata, be16(r)...)
	}
	for g := 0; g < n; g++ {
		for r := 0; r < nReg; r++ {
			d := 0
			if g < len(h.Deltas) && r < len(h.Deltas[g]) {
				d = h.Deltas[g][r]
			}
			idata = append(idata, be16(d&0xffff)...)
		}
	}
	ivsHdr := 8 + 4
	ivs := cat(be16(1), be32(uint32(ivsHdr)), be16(1), be32(uint32(ivsHdr+len(rl))), rl, idata)
	return cat(be16(1), be16(0), be32(20), be32(0), be32(0), be32(0), ivs)
}

// VarBoxes is Boxes with a weight axis (wght 100..900, default 400): at the
// bold end (normalised +1) every glyph's right side moves right by 160 units
// and the advance grows by 160; at the light end (-1) the advance shrinks by
// 100. Deltas are given for all points, so the glyph outlines scale linearly
// with the coordinate. withHVAR chooses between advance deltas in HVAR and in
// the phantom points of gvar.
func VarBoxes(family string, withHVAR bool) *Font {
	f := Boxes(family, "Regular", 400, false)
	f.ShortLoca = false
	vs := VarSpec{
		Axes:   []Axis{{Tag: "wght", Min: 100, Def: 400, Max: 900}},
		Shared: [][]float64{{1}, {-1}},
		Glyphs: map[int]GlyphVar{},
	}
	var hv *HVAR
	if withHVAR {
		hv = &HVAR{Regions: [][][3]float64{{{0, 1, 1}}, {{-1, -1, 0}}}}
		vs.HVAR = hv
	}
	for gid, g := range f.Glyphs {
		if len(g.Contours) == 0 {
			continue
		}
		var pts []Pt
		for _, c := range g.Contours {
			pts = append(pts, c...)
		}
		n := len(pts)
		mk := func(shared int, right, adv int) Tuple {
			dx := make([]int, n+4)
			dy := make([]int, n+4)
			maxX := 0
			for _, p := range pts {
				maxX = max(maxX, p.X)
			}
			for i, p := range pts {
				if p.X >= maxX-1 {
					dx[i] = right
				}
			}
			dx[n+1] = adv // phantom point 2: the advance
			return Tuple{SharedIdx: shared, DX: dx, DY: dy}
		}
		if withHVAR {
			vs.Glyphs[gid] = GlyphVar{Tuples: []Tuple{mk(0, 160, 0), mk(1, -100, 0)}}
			for len(hv.Deltas) <= gid {
				hv.Deltas = append(hv.Deltas, []int{0, 0})
			}
			hv.Deltas[gid] = []int{160, -100}
		} else {
			vs.Glyphs[gid] = GlyphVar{Tuples: []Tuple{mk(0, 160, 160), mk(1, -100, -100)}}
		}
	}
	if withHVAR {
		for len(hv.Deltas) < len(f.Glyphs) {
			hv.Deltas = append(hv.Deltas, []int{0, 0})
		}
		// glyphs without outlines (space) still change width
		for gid, g := range f.Glyphs {
			if len(g.Contours) == 0 && len(g.Comps) == 0 {
				hv.Deltas[gid] = []int{160, -100}
			}
		}
	}
	f.AddVariations(vs)
	return f
}
